// 本文件负责 Runtime 安装记录在 `java_runtimes` 表里的读写，以及「选中」状态的落库。
//
// 为什么与安装编排分开：install.go 关心「怎么装」，本文件关心「装完怎么记」与
// 「选哪个」。分开后安装流程的测试只需要替换数据库路径，不必关心 SQL 细节；
// 而选择逻辑的测试也不必构造下载与解压。
//
// 数据库连接策略与 internal/download 保持一致：包级惰性单例 + 打不开就明确失败。
// 不使用 http 端口、不缓存查询结果之外的任何东西。
package runtime

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
	"sync"
	"time"

	"example.com/ninevoxel/launcher/internal/database"
	"example.com/ninevoxel/launcher/internal/logging"
	"example.com/ninevoxel/launcher/internal/settings"
)

// runtimeRow 是 `java_runtimes` 表的一行。
//
// 字段与接口冻结清单 §5.3 逐一对应；列名在 SQL 里显式写出，不用 `SELECT *`，
// 这样迁移新增列时不会悄悄改变扫描顺序。
type runtimeRow struct {
	// ID 为 runtimeId，同时是主键。
	ID string
	// Kind 为 `local` / `downloaded`。
	Kind string
	// Component 为官方组件名（例如 `java-runtime-gamma`）；本机 Java 为空。
	Component string
	// MajorVersion 为主版本号。
	MajorVersion int
	// FullVersion 为完整版本串。
	FullVersion string
	// Platform 为平台标识（`windows` / `darwin` / `linux`）。
	Platform string
	// Arch 为架构标识（`x64` / `x86` / `aarch64`）。
	Arch string
	// Bitness 为位数（32 / 64 / 0 表示未知）。
	Bitness int
	// Vendor 为厂商标识。
	Vendor string
	// SourceURI 为归档来源地址，**必须剔除查询串**，不写入日志。
	SourceURI string
	// SHA1 为归档校验值（Mojang 官方清单声明值）。
	SHA1 string
	// SHA256 为归档校验值（第三方来源声明值，P3-14）；官方来源为空。
	SHA256 string
	// SourceName 为来源标识（`adoptium`）；官方清单与本机探测为空。
	SourceName string
	// LocalRelPath 为相对数据根的安装目录（`runtimes/<runtimeId>`）。
	LocalRelPath string
	// VerifiedAtMs 为最近一次校验通过的时间戳。
	VerifiedAtMs int64
	// Selected 表示是否为当前选中的 Runtime。
	Selected bool
	// CreatedAtMs 为首次记录时间。
	CreatedAtMs int64
	// LatestCheckedAtMs 为最近一次更新检查的时间戳（P3-14）。
	LatestCheckedAtMs int64
	// LatestFullVersion 为最近一次检查拿到的远端完整版本串（P3-14）。
	LatestFullVersion string
}

var (
	// runtimeDBOnce 保证数据库只打开一次。
	runtimeDBOnce sync.Once
	// runtimeDB 是包级数据库句柄。
	runtimeDB *sql.DB
	// runtimeDBErr 记录打开失败原因，避免每次调用都重试并重复写日志。
	runtimeDBErr *AppError
)

// runtimeDatabasePathResolver 返回应用数据库路径；测试替换为临时路径。
//
// 复用 P1 的数据库路径策略（用户配置目录优先、便携版 exe 同级 data/ 兜底），
// 与 settings、download 两个模块保持同一个 app.db，不另起第二个库。
var runtimeDatabasePathResolver = func() string {
	return settings.ResolveDatabasePath().Path
}

// runtimeLocalProbeList 是本机探测入口；测试替换为空实现。
//
// 单独抽成变量而不是直接调用 ListRuntimes：探测会真的启动 `java -version`，
// 测试环境里不允许执行真实 Java 进程（卡面约束）。
var runtimeLocalProbeList = ListRuntimes

// runtimeDatabase 返回数据库句柄，必要时打开并执行迁移。
//
// 迁移在打开时执行而不是在 bootstrap 里：`java_runtimes` 表由本卡的 000004 迁移
// 建立，安装流程第一次落库前必须保证表存在；放在这里可以避免「服务注册顺序」
// 影响建表时机。
func runtimeDatabase() (*sql.DB, *AppError) {
	runtimeDBOnce.Do(func() {
		path := strings.TrimSpace(runtimeDatabasePathResolver())
		if path == "" {
			runtimeDBErr = newSystemError(ErrorCodeRuntimeInstall, messageRuntimeDatabaseUnavailable, false)
			return
		}
		db, openErr := database.Open(path)
		if openErr != nil {
			runtimeDBErr = newSystemError(ErrorCodeRuntimeInstall, messageRuntimeDatabaseUnavailable, false)
			return
		}
		if _, migrateErr := database.Migrate(context.Background(), db); migrateErr != nil {
			_ = db.Close()
			runtimeDBErr = newSystemError(ErrorCodeRuntimeInstall, messageRuntimeDatabaseUnavailable, false)
			return
		}
		runtimeDB = db
	})
	if runtimeDBErr != nil {
		return nil, runtimeDBErr
	}
	if runtimeDB == nil {
		return nil, newSystemError(ErrorCodeRuntimeInstall, messageRuntimeDatabaseUnavailable, false)
	}
	return runtimeDB, nil
}

// saveRuntime 写入或更新一条 Runtime 记录。
//
// 冲突时更新「可重新验证」的字段，但**不覆盖** `selected`：
// 重装同一个 Runtime 不应该顺手改变用户的选择。
func saveRuntime(ctx context.Context, row runtimeRow) *AppError {
	db, dbErr := runtimeDatabase()
	if dbErr != nil {
		return dbErr
	}
	const statement = `INSERT INTO java_runtimes (
	id, kind, component, major_version, full_version, platform, arch, bitness,
	vendor, source_uri, sha1, sha256, source_name, local_rel_path, verified_at_ms, selected, created_at_ms,
	latest_checked_at_ms, latest_full_version
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
	kind = excluded.kind,
	component = excluded.component,
	major_version = excluded.major_version,
	full_version = excluded.full_version,
	platform = excluded.platform,
	arch = excluded.arch,
	bitness = excluded.bitness,
	vendor = excluded.vendor,
	source_uri = excluded.source_uri,
	sha1 = excluded.sha1,
	sha256 = excluded.sha256,
	source_name = excluded.source_name,
	local_rel_path = excluded.local_rel_path,
	verified_at_ms = excluded.verified_at_ms`
	selected := 0
	if row.Selected {
		selected = 1
	}
	if _, execErr := db.ExecContext(
		ctx,
		statement,
		row.ID,
		row.Kind,
		row.Component,
		row.MajorVersion,
		row.FullVersion,
		row.Platform,
		row.Arch,
		row.Bitness,
		row.Vendor,
		row.SourceURI,
		row.SHA1,
		row.SHA256,
		row.SourceName,
		row.LocalRelPath,
		row.VerifiedAtMs,
		selected,
		row.CreatedAtMs,
		row.LatestCheckedAtMs,
		row.LatestFullVersion,
	); execErr != nil {
		runtimeLog().Warn("写入 Runtime 记录失败", logging.Fields{
			Event:      "runtime.registry.save_failed",
			ErrorCode:  ErrorCodeRuntimeInstall,
			Attributes: map[string]string{"runtimeId": row.ID},
		})
		return newSystemError(ErrorCodeRuntimeInstall, messageRuntimeWriteFailed, false)
	}
	return nil
}

// installedRuntimeRows 读取全部已安装 Runtime 记录。
func installedRuntimeRows(ctx context.Context) ([]runtimeRow, *AppError) {
	db, dbErr := runtimeDatabase()
	if dbErr != nil {
		return nil, dbErr
	}
	const statement = `SELECT
	id, kind, component, major_version, full_version, platform, arch, bitness,
	vendor, source_uri, sha1, sha256, source_name, local_rel_path, verified_at_ms, selected, created_at_ms,
	latest_checked_at_ms, latest_full_version
FROM java_runtimes
ORDER BY major_version DESC, full_version DESC, id ASC`
	rows, queryErr := db.QueryContext(ctx, statement)
	if queryErr != nil {
		return nil, newSystemError(ErrorCodeRuntimeInstall, messageRuntimeReadFailed, false)
	}
	defer func() {
		_ = rows.Close()
	}()

	result := make([]runtimeRow, 0, 4)
	for rows.Next() {
		var row runtimeRow
		var selected int
		if scanErr := rows.Scan(
			&row.ID,
			&row.Kind,
			&row.Component,
			&row.MajorVersion,
			&row.FullVersion,
			&row.Platform,
			&row.Arch,
			&row.Bitness,
			&row.Vendor,
			&row.SourceURI,
			&row.SHA1,
			&row.SHA256,
			&row.SourceName,
			&row.LocalRelPath,
			&row.VerifiedAtMs,
			&selected,
			&row.CreatedAtMs,
			&row.LatestCheckedAtMs,
			&row.LatestFullVersion,
		); scanErr != nil {
			return nil, newSystemError(ErrorCodeRuntimeInstall, messageRuntimeReadFailed, false)
		}
		row.Selected = selected != 0
		result = append(result, row)
	}
	if iterErr := rows.Err(); iterErr != nil {
		return nil, newSystemError(ErrorCodeRuntimeInstall, messageRuntimeReadFailed, false)
	}
	return result, nil
}

// findRuntimeRow 按 id 查询单条记录；不存在时返回 (nil, nil)。
func findRuntimeRow(ctx context.Context, runtimeID string) (*runtimeRow, *AppError) {
	rows, listErr := installedRuntimeRows(ctx)
	if listErr != nil {
		return nil, listErr
	}
	for index := range rows {
		if rows[index].ID == runtimeID {
			candidate := rows[index]
			return &candidate, nil
		}
	}
	return nil, nil
}

// adoptProbedRuntime 把本机探测到的 Java 补录进 `java_runtimes`，返回补录后的行。
//
// 为什么要补录：`java_runtimes` 只存「启动器安装的」与「曾经选择过的本机 Java」，
// 而列表展示时会把实时探测结果也合并进来。用户在本机探测条目上点「选用」时，
// 该条目可能从未落库；此时直接更新 selected 会改到 0 行，用户看到的却是
// `RUNTIME_NOT_FOUND_001`。补录后，两种来源的选择路径完全一致。
//
// 本机 Java 不在数据根内，因此 `LocalRelPath` 留空、`SourceURI` 与 `SHA1` 也留空；
// 「选中之后如何定位 java 可执行文件」属于启动流程的绑定问题，不在此处解决。
// 探测失败或没有匹配项时返回 (nil, nil)，由调用方给出 NOT_FOUND。
func adoptProbedRuntime(runtimeID string) (*runtimeRow, *AppError) {
	probed, probeErr := runtimeLocalProbeList()
	if probeErr != nil {
		return nil, nil
	}
	for index := range probed.Items {
		item := probed.Items[index]
		if item.RuntimeID != runtimeID {
			continue
		}
		row := runtimeRow{
			ID:           item.RuntimeID,
			Kind:         SourceLocal,
			MajorVersion: item.MajorVersion,
			FullVersion:  item.FullVersion,
			Platform:     currentPlatformName(),
			Arch:         item.Arch,
			Bitness:      bitnessValue(item.Bitness),
			Vendor:       item.Vendor,
			CreatedAtMs:  runtimeSelectionNowMs(),
		}
		if saveErr := saveRuntime(context.Background(), row); saveErr != nil {
			return nil, saveErr
		}
		return &row, nil
	}
	return nil, nil
}

// selectedRuntimeID 返回当前选中的 Runtime id；没有选中时返回空串。
func selectedRuntimeID(ctx context.Context) (string, *AppError) {
	rows, listErr := installedRuntimeRows(ctx)
	if listErr != nil {
		return "", listErr
	}
	for index := range rows {
		if rows[index].Selected {
			return rows[index].ID, nil
		}
	}
	return "", nil
}

// markSelectedRuntime 把指定 Runtime 标记为选中，并清除其他行的选中位。
//
// 必须在事务里做两步更新：应用层约定「至多一行 selected=1」，
// 先清后置的顺序保证任何中间时刻都不会出现两个选中行被读到。
func markSelectedRuntime(ctx context.Context, runtimeID string) *AppError {
	db, dbErr := runtimeDatabase()
	if dbErr != nil {
		return dbErr
	}
	tx, beginErr := db.BeginTx(ctx, nil)
	if beginErr != nil {
		return newSystemError(ErrorCodeRuntimeInstall, messageRuntimeWriteFailed, false)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	if _, clearErr := tx.ExecContext(
		ctx,
		`UPDATE java_runtimes SET selected = 0 WHERE selected <> 0 AND id <> ?`,
		runtimeID,
	); clearErr != nil {
		return newSystemError(ErrorCodeRuntimeInstall, messageRuntimeWriteFailed, false)
	}
	result, updateErr := tx.ExecContext(
		ctx,
		`UPDATE java_runtimes SET selected = 1 WHERE id = ?`,
		runtimeID,
	)
	if updateErr != nil {
		return newSystemError(ErrorCodeRuntimeInstall, messageRuntimeWriteFailed, false)
	}
	affected, affectedErr := result.RowsAffected()
	if affectedErr != nil {
		return newSystemError(ErrorCodeRuntimeInstall, messageRuntimeWriteFailed, false)
	}
	if affected == 0 {
		return newInputError(ErrorCodeRuntimeNotFound, messageRuntimeNotFound)
	}
	if commitErr := tx.Commit(); commitErr != nil {
		return newSystemError(ErrorCodeRuntimeInstall, messageRuntimeWriteFailed, false)
	}
	return nil
}

// summaryOfRow 把数据库行转换成面向前端的摘要。
//
// 安全约束：只填 displayName 与 runtimeId，绝对路径（`local_rel_path` 是相对路径，
// 也不下发）不出现在返回值里。
func summaryOfRow(row runtimeRow) RuntimeSummary {
	bitness := bitnessText(row.Bitness)
	compatible, reason := evaluateCompatibility(JavaVersionInfo{
		Arch:    row.Arch,
		Bitness: bitness,
	})
	// 来源取记录自身的 kind：本机补录进来的是 local，启动器安装的是 downloaded。
	// kind 为空的历史记录按 downloaded 处理（P3-09 之前只有安装项会落库）。
	source := row.Kind
	if source == "" {
		source = SourceDownloaded
	}
	return RuntimeSummary{
		RuntimeID:        row.ID,
		DisplayName:      runtimeDisplayName(row),
		Kind:             row.Kind,
		Vendor:           row.Vendor,
		MajorVersion:     row.MajorVersion,
		FullVersion:      row.FullVersion,
		Arch:             row.Arch,
		Bitness:          bitness,
		Source:           source,
		Compatible:       compatible,
		CompatibleReason: reason,
		Selected:         row.Selected,
		VerifiedAtMs:     row.VerifiedAtMs,
	}
}

// runtimeDisplayName 生成已安装 Runtime 的展示名。
//
// 形式与 P3-08 的本机 Java 展示名保持一致（`<厂商> <主版本> (<架构>)`），
// 这样同一个列表里两种来源的条目看起来是同一类东西。
func runtimeDisplayName(row runtimeRow) string {
	vendor := row.Vendor
	if vendor == "" {
		vendor = VendorUnknown
	}
	major := row.MajorVersion
	if major <= 0 {
		return vendorLabel(vendor) + " " + displayUnknownVersion
	}
	label := vendorLabel(vendor) + " " + strconv.Itoa(major)
	arch := row.Arch
	if arch == "" || arch == ArchUnknown {
		return label + " (" + displayUnknownArch + ")"
	}
	return label + " (" + arch + ")"
}

// bitnessText 把数据库整数位数转成 DTO 使用的字符串位数。
func bitnessText(value int) string {
	switch value {
	case 32:
		return Bitness32
	case 64:
		return Bitness64
	default:
		return BitnessUnknown
	}
}

// bitnessValue 把 DTO 字符串位数转成数据库整数位数。
func bitnessValue(value string) int {
	switch value {
	case Bitness32:
		return 32
	case Bitness64:
		return 64
	default:
		return 0
	}
}

// listRuntimesWithInstalled 返回「本机探测结果 + 已安装 Runtime」的合并列表。
//
// 本机探测失败不算整体失败：已安装 Runtime 仍然可用，返回列表并把探测失败
// 记入日志（前端已经从 P3-08 的 warnings 得到过同样的信息，这里不重复造告警）。
func listRuntimesWithInstalled() (RuntimeList, *AppError) {
	result := RuntimeList{Items: make([]RuntimeSummary, 0, 4)}

	// 先放本机探测结果，再用表记录覆盖同 id 项：一旦某个本机 Java 被选择过，
	// 它就会落库，此时以库里的记录为准（库里带 selected 与 kind，探测结果不带）。
	byID := make(map[string]RuntimeSummary, 8)
	order := make([]string, 0, 8)

	local, probeErr := runtimeLocalProbeList()
	if probeErr != nil {
		runtimeLog().Warn("本机 Java 探测失败，仅返回已安装 Runtime", logging.Fields{
			Event:     "runtime.list.probe_failed",
			ErrorCode: probeErr.Code,
		})
	} else {
		result.Requirement = local.Requirement
		for index := range local.Items {
			item := local.Items[index]
			if _, exists := byID[item.RuntimeID]; !exists {
				order = append(order, item.RuntimeID)
			}
			byID[item.RuntimeID] = item
		}
	}

	rows, listErr := installedRuntimeRows(context.Background())
	if listErr != nil {
		return RuntimeList{}, listErr
	}
	for index := range rows {
		summary := summaryOfRow(rows[index])
		if _, exists := byID[summary.RuntimeID]; !exists {
			order = append(order, summary.RuntimeID)
		}
		byID[summary.RuntimeID] = summary
		if rows[index].Selected {
			result.SelectedRuntimeID = rows[index].ID
		}
	}

	result.Items = make([]RuntimeSummary, 0, len(order))
	for _, id := range order {
		result.Items = append(result.Items, byID[id])
	}
	sortRuntimeSummaries(result.Items)
	return result, nil
}

// SelectRuntime 选择要使用的 Runtime。
//
// 校验顺序：存在性 → 兼容性 → 落库。`force=true` 允许选择架构/位数不匹配的
// Runtime（用户可能在 64 位系统上故意选 32 位 Java 做兼容测试）。
//
// major 需求校验的输入来自版本元数据，而 `SelectRuntimeRequest`（冻结契约）
// 不含 versionId，因此这里只能做「环境兼容」这一层；需要「是否满足某版本
// 所需的 Java 大版本」时，由调用方先 ResolveRuntimeRequirement 再自行比较。
func SelectRuntime(req SelectRuntimeRequest) (RuntimeList, *AppError) {
	runtimeID := strings.TrimSpace(req.RuntimeID)
	if runtimeID == "" {
		return RuntimeList{}, newInputError(ErrorCodeRuntimeRequirement, messageRuntimeSelectionRequired)
	}
	row, findErr := findRuntimeRow(context.Background(), runtimeID)
	if findErr != nil {
		return RuntimeList{}, findErr
	}
	if row == nil {
		// 表中没有这条记录：它可能只存在于本机探测结果里（用户在本机 Java 条目上
		// 点了「选用」）。选择需要持久化选中态，因此先把探测结果补录进表，
		// 再走同一套选择逻辑；否则 UPDATE 会改到 0 行并报 NOT_FOUND。
		adopted, adoptErr := adoptProbedRuntime(runtimeID)
		if adoptErr != nil {
			return RuntimeList{}, adoptErr
		}
		row = adopted
	}
	if row == nil {
		return RuntimeList{}, newInputError(ErrorCodeRuntimeNotFound, messageRuntimeNotFound)
	}
	summary := summaryOfRow(*row)
	if !summary.Compatible && !req.Force {
		runtimeLog().Warn("拒绝选择不兼容的 Runtime", logging.Fields{
			Event:      "runtime.select.rejected",
			ErrorCode:  ErrorCodeRuntimeRequirement,
			Attributes: map[string]string{"runtimeId": runtimeID},
		})
		return RuntimeList{}, newInputError(ErrorCodeRuntimeRequirement, messageRuntimeIncompatible)
	}
	if selectErr := markSelectedRuntime(context.Background(), runtimeID); selectErr != nil {
		return RuntimeList{}, selectErr
	}
	runtimeLog().Info("已选择 Runtime", logging.Fields{
		Event:      "runtime.selected",
		Attributes: map[string]string{"runtimeId": runtimeID, "forced": boolText(req.Force)},
	})
	return listRuntimesWithInstalled()
}

// runtimeSelectionNowMs 返回当前毫秒时间戳，测试可替换。
var runtimeSelectionNowMs = func() int64 {
	return time.Now().UnixMilli()
}

// boolText 把布尔值转成日志用文本。
func boolText(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

// updateRuntimeLatest 写回一次更新检查的结果（P3-14）。
//
// 只更新两列：检查时间与远端版本串。`UpdateAvailable` 不落库——它由「安装版本 vs
// 远端版本」现算，落库会让版本号与结论有机会不一致。
func updateRuntimeLatest(ctx context.Context, runtimeID string, checkedAtMs int64, latestFullVersion string) *AppError {
	db, dbErr := runtimeDatabase()
	if dbErr != nil {
		return dbErr
	}
	const statement = `UPDATE java_runtimes
SET latest_checked_at_ms = ?, latest_full_version = ?
WHERE id = ?`
	if _, execErr := db.ExecContext(ctx, statement, checkedAtMs, latestFullVersion, runtimeID); execErr != nil {
		return newSystemError(ErrorCodeRuntimeInstall, messageRuntimeWriteFailed, false)
	}
	return nil
}
