// 本文件实现 P2-13 的启动配置持久化：把「Java 可执行文件 + 游戏目录 + 版本 id」写入 SQLite。
//
// 设计要点（对应任务卡 launcher/docs/tasks/P2/P2-13.md）：
//  1. **绝对路径可以入库，但永不返回前端**：对外只返回经 P2-09 口径脱敏的展示串
//     （见 LaunchProfileSummary 的注释）。脱敏函数复用既有的
//     buildJavaDisplay / buildGameRootDisplay / RedactPathText，不新写第二套规则。
//  2. **降级而不是崩溃**：数据库打不开或迁移失败时，读写接口返回
//     LAUNCH_PROFILE_001 并保留中文说明，应用照常启动，只是本次运行无法保存配置。
//  3. **恢复时重新校验**：GetMostRecentLaunchProfile 不会直接相信历史值，
//     而是重新检查 Java 文件与游戏目录是否仍然存在（用户可能已卸载 Java 或移动目录）。
//  4. **不启动任何子进程**：校验只用 os.Stat，绝不执行 `java -version`。
//  5. 连接与迁移全部复用 internal/database（ADR-002 的唯一实现），本文件不写 DSN。
package launch

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"example.com/ninevoxel/launcher/internal/database"
	"example.com/ninevoxel/launcher/internal/logging"
	"example.com/ninevoxel/launcher/internal/settings"
)

// 启动配置库的包级连接状态。
//
// 为什么用 Mutex + 布尔而不是 sync.Once：测试需要把连接重置到临时目录，
// sync.Once 一旦触发就无法回滚，会让第二个测试继续用上一个用例的数据库。
var (
	// profileMutex 保护下列三个变量。
	profileMutex sync.Mutex

	// profileDB 为已打开的启动配置库连接；nil 表示尚未打开或已失败。
	profileDB *sql.DB

	// profileDBErr 为打开失败时的可降级错误；成功时为 nil。
	profileDBErr *Error

	// profileOpened 标记是否已尝试过打开，避免每次调用都重试失败路径。
	profileOpened bool
)

// profileDatabasePath 解析启动配置库的文件路径。
//
// 复用 P1-04 的 ResolveDatabasePath（与设置服务共用同一个 app.db），
// 这样用户只会有一个数据库文件；该变量可被测试替换，以便隔离到临时目录。
var profileDatabasePath = func() settings.DatabasePathStatus {
	return settings.ResolveDatabasePath()
}

// openProfileDatabase 打开启动配置库并执行迁移。
//
// 返回值：成功时返回连接与 nil；失败时返回 nil 与 LAUNCH_PROFILE_001 的可降级错误。
// 失败不 panic、不中断应用启动（任务卡第 61 条：迁移失败必须降级）。
func openProfileDatabase() (*sql.DB, *Error) {
	status := profileDatabasePath()
	if status.Kind == settings.PathKindUnavailable || strings.TrimSpace(status.Path) == "" {
		return nil, newSystemError(ErrorCodeLaunchProfile, messageProfileStorageUnavailable)
	}

	db, err := database.Open(status.Path)
	if err != nil {
		return nil, newSystemError(ErrorCodeLaunchProfile, messageProfileStorageUnavailable)
	}

	// 迁移与设置服务共用同一套版本表：重复调用是幂等的（已应用的迁移不会重跑）。
	if _, migrateErr := database.Migrate(context.Background(), db); migrateErr != nil {
		_ = db.Close()
		// 迁移失败沿用 database 包的稳定错误码 DB_MIGRATION_001（任务卡第 61 条），
		// 让前端能把「数据库迁移失败」和普通的配置读写失败区分开；
		// 无论哪种情况都不 panic，应用照常启动，只是本次运行无法保存启动配置。
		if strings.Contains(migrateErr.Error(), database.MigrationErrorCode) {
			return nil, newSystemError(database.MigrationErrorCode, messageProfileMigrationFailed)
		}
		return nil, newSystemError(ErrorCodeLaunchProfile, messageProfileStorageUnavailable)
	}

	return db, nil
}

// profileDatabase 返回启动配置库连接（惰性打开，只尝试一次）。
//
// 返回值为 (nil, *Error) 时调用方必须原样上抛，让前端如实提示「无法保存配置」。
func profileDatabase() (*sql.DB, *Error) {
	profileMutex.Lock()
	defer profileMutex.Unlock()

	if !profileOpened {
		profileOpened = true
		profileDB, profileDBErr = openProfileDatabase()
	}

	return profileDB, profileDBErr
}

// resetProfileDatabaseForTest 关闭并重置启动配置库连接。
//
// 仅供本包测试使用：让每个用例都能指向自己的 t.TempDir() 数据库。
func resetProfileDatabaseForTest() {
	profileMutex.Lock()
	defer profileMutex.Unlock()

	if profileDB != nil {
		_ = profileDB.Close()
	}
	profileDB = nil
	profileDBErr = nil
	profileOpened = false
}

// profileRow 是 launch_profiles 一行数据的内部形态。
//
// 与 DTO 分开：行里含绝对路径（仅进程内使用），DTO 里只有脱敏展示串。
type profileRow struct {
	id            string
	name          string
	javaPath      string
	gameDirectory string
	versionID     string
	lastUsedAtMs  int64
}

// launchProfileColumns 是查询启动配置时统一使用的列顺序。
const launchProfileColumns = "id, name, java_path, game_directory, version_id, last_used_at_ms"

// scanProfileRow 从单行查询结果扫描一行数据。
//
// 参数 row 为 sql.Row（QueryRow 的返回值）；返回行数据或错误。
func scanProfileRow(row *sql.Row) (profileRow, error) {
	var scanned profileRow
	err := row.Scan(&scanned.id, &scanned.name, &scanned.javaPath, &scanned.gameDirectory, &scanned.versionID, &scanned.lastUsedAtMs)
	return scanned, err
}

// scanProfileRows 从多行查询结果的游标扫描一行数据。
//
// 参数 rows 为已执行查询的游标；返回行数据或错误。
func scanProfileRows(rows *sql.Rows) (profileRow, error) {
	var scanned profileRow
	err := rows.Scan(&scanned.id, &scanned.name, &scanned.javaPath, &scanned.gameDirectory, &scanned.versionID, &scanned.lastUsedAtMs)
	return scanned, err
}

// newProfileSummary 把内部行转换为对外摘要。
//
// **脱敏是本函数的唯一职责边界**：绝对路径进入本函数后只以展示串形式离开。
// 展示口径复用 P2-09 的既有实现，不在此重写规则：
//   - Java：buildJavaDisplay（Java 安装目录 + … + 末三段）；
//   - 游戏目录：buildGameRootDisplay（游戏目录 + … + 末两段）；
//   - 两者再经 RedactPathText 兜底，确保任何残留的盘符或绝对路径都被抹掉。
func newProfileSummary(row profileRow) LaunchProfileSummary {
	return LaunchProfileSummary{
		ID:              row.id,
		Name:            row.name,
		JavaDisplay:     RedactPathText(buildJavaDisplay(row.javaPath)),
		GameRootDisplay: RedactPathText(buildGameRootDisplay(row.gameDirectory)),
		VersionID:       row.versionID,
		LastUsedAtMs:    row.lastUsedAtMs,
	}
}

// normalizeProfilePath 归一化用户提供的路径（去空白 + filepath.Clean）。
//
// 不做大小写折叠：路径大小写由平台语义决定，Go 侧不擅自改写用户的选择。
func normalizeProfilePath(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	return filepath.Clean(trimmed)
}

// defaultProfileName 生成配置的默认显示名称。
//
// P2 阶段前端不传名称，用版本 id 生成一个可读名称供后续配置列表使用。
func defaultProfileName(versionID string) string {
	if strings.TrimSpace(versionID) == "" {
		return "启动配置"
	}
	return versionID + " 启动配置"
}

// newProfileID 生成启动配置的唯一标识。
//
// 形如 `1735689600000-1a2b3c4d`（毫秒时间戳 + 8 位随机十六进制）：
// 无需引入 uuid 依赖，也不会在 go.mod 中新增模块。
func newProfileID(nowMs int64) string {
	buf := make([]byte, 4)
	if _, err := rand.Read(buf); err != nil {
		// 随机源不可用时退化为纯时间戳：唯一性由时间戳与唯一索引兜底。
		return strconv.FormatInt(nowMs, 10)
	}
	return strconv.FormatInt(nowMs, 10) + "-" + hex.EncodeToString(buf)
}

// SaveLaunchProfile 保存一条启动配置。
//
// 参数 req 为用户当前选择的 Java、游戏目录与版本 id（绝对路径）。
// 返回值是**脱敏后的**配置摘要；失败时返回 LAUNCH_PROFILE_001 的中文说明。
//
// 幂等语义（任务卡第 48 条）：同一组 (java_path, game_directory, version_id)
// 只保留一行——命中时更新 updated_at_ms / last_used_at_ms，不插入第二行，
// 并保留原 id，避免调用方持有的 id 失效。
//
// 日志：只记版本 id、是否新建、错误码，**不记任何路径**。
func SaveLaunchProfile(req SaveLaunchProfileRequest) (*LaunchProfileSummary, *Error) {
	javaPath := normalizeProfilePath(req.JavaPath)
	gameDirectory := normalizeProfilePath(req.GameDirectory)
	versionID := strings.TrimSpace(req.VersionID)

	// 保存前的基本校验：空值与相对路径都属用户输入问题，不应写入库。
	if javaPath == "" {
		return nil, newInputError(ErrorCodeLaunchProfile, messageProfileJavaPathEmpty)
	}
	if gameDirectory == "" {
		return nil, newInputError(ErrorCodeLaunchProfile, messageProfileGameDirEmpty)
	}
	if versionID == "" {
		return nil, newInputError(ErrorCodeLaunchProfile, messageProfileVersionEmpty)
	}
	if !filepath.IsAbs(javaPath) || !filepath.IsAbs(gameDirectory) {
		return nil, newInputError(ErrorCodeLaunchProfile, messageProfilePathNotAbsolute)
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = defaultProfileName(versionID)
	}

	db, dbErr := profileDatabase()
	if dbErr != nil {
		return nil, dbErr
	}

	nowMs := time.Now().UnixMilli()

	// 事务内先查后写：既保证幂等，也让「保留原 id」可控
	//（ON CONFLICT 写法会依赖索引匹配细节，读起来也更难解释）。
	tx, txErr := db.Begin()
	if txErr != nil {
		return nil, newSystemError(ErrorCodeLaunchProfile, messageProfileSaveFailed)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	var existingID string
	queryErr := tx.QueryRow(
		"SELECT id FROM launch_profiles WHERE java_path = ? AND game_directory = ? AND version_id = ?",
		javaPath, gameDirectory, versionID,
	).Scan(&existingID)

	created := false
	switch {
	case queryErr == nil && existingID != "":
		// 命中已有配置：只刷新名称与两个时间戳，id 与 created_at_ms 保持不变。
		if _, err := tx.Exec(
			"UPDATE launch_profiles SET name = ?, updated_at_ms = ?, last_used_at_ms = ? WHERE id = ?",
			name, nowMs, nowMs, existingID,
		); err != nil {
			return nil, newSystemError(ErrorCodeLaunchProfile, messageProfileSaveFailed)
		}
	case queryErr == sql.ErrNoRows:
		created = true
		if _, err := tx.Exec(
			"INSERT INTO launch_profiles ("+launchProfileColumns+", created_at_ms, updated_at_ms) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
			newProfileID(nowMs), name, javaPath, gameDirectory, versionID, nowMs, nowMs, nowMs,
		); err != nil {
			return nil, newSystemError(ErrorCodeLaunchProfile, messageProfileSaveFailed)
		}
	default:
		// 查询本身失败（表缺失、数据库损坏等）：按保存失败处理。
		return nil, newSystemError(ErrorCodeLaunchProfile, messageProfileSaveFailed)
	}

	if commitErr := tx.Commit(); commitErr != nil {
		return nil, newSystemError(ErrorCodeLaunchProfile, messageProfileSaveFailed)
	}

	// 回读一行：让返回值与库中真实状态一致（而不是把入参当作结果）。
	row, readErr := readProfileRow(db,
		"SELECT "+launchProfileColumns+" FROM launch_profiles WHERE java_path = ? AND game_directory = ? AND version_id = ?",
		javaPath, gameDirectory, versionID,
	)
	if readErr != nil {
		return nil, newSystemError(ErrorCodeLaunchProfile, messageProfileSaveFailed)
	}

	logger().Info("启动配置已保存", logging.Fields{
		Event: "launch.profile.saved",
		Attributes: map[string]string{
			"versionId": versionID,
			"created":   boolAttribute(created),
		},
	})

	summary := newProfileSummary(row)
	return &summary, nil
}

// ListLaunchProfiles 返回全部启动配置摘要（按最近使用时间倒序）。
//
// 返回值只含脱敏展示串；列表为空时返回空切片而不是 nil，避免前端区分两种「空」。
func ListLaunchProfiles() ([]LaunchProfileSummary, *Error) {
	db, dbErr := profileDatabase()
	if dbErr != nil {
		return nil, dbErr
	}

	rows, queryErr := db.Query(
		"SELECT " + launchProfileColumns + " FROM launch_profiles ORDER BY last_used_at_ms DESC, updated_at_ms DESC, rowid DESC",
	)
	if queryErr != nil {
		return nil, newSystemError(ErrorCodeLaunchProfile, messageProfileListFailed)
	}
	defer func() {
		_ = rows.Close()
	}()

	summaries := make([]LaunchProfileSummary, 0, 8)
	for rows.Next() {
		row, scanErr := scanProfileRows(rows)
		if scanErr != nil {
			return nil, newSystemError(ErrorCodeLaunchProfile, messageProfileListFailed)
		}
		summaries = append(summaries, newProfileSummary(row))
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, newSystemError(ErrorCodeLaunchProfile, messageProfileListFailed)
	}

	return summaries, nil
}

// GetMostRecentLaunchProfile 返回最近使用的一条启动配置摘要。
//
// 行为：
//   - 没有任何配置时返回 (nil, nil)：这是「首次使用」的正常状态，不是错误；
//   - 读到的配置指向的 Java 文件或游戏目录已不存在时返回 LAUNCH_PROFILE_001
//     （文案「上次保存的 Java 或游戏目录已不可用，请重新选择」）——
//     即任务卡要求的「重新校验，不直接信任历史值」；
//   - 校验只用 os.Stat，**不执行 `java -version`**、不启动任何子进程。
func GetMostRecentLaunchProfile() (*LaunchProfileSummary, *Error) {
	db, dbErr := profileDatabase()
	if dbErr != nil {
		return nil, dbErr
	}

	row, readErr := readProfileRow(db,
		"SELECT "+launchProfileColumns+" FROM launch_profiles ORDER BY last_used_at_ms DESC, updated_at_ms DESC, rowid DESC LIMIT 1",
	)
	if errors.Is(readErr, sql.ErrNoRows) {
		return nil, nil
	}
	if readErr != nil {
		return nil, newSystemError(ErrorCodeLaunchProfile, messageProfileListFailed)
	}

	// 重新校验：历史值可能已失效（Java 被卸载、目录被移动或删除）。
	if !isUsableProfileJava(row.javaPath) || !isUsableProfileDirectory(row.gameDirectory) {
		logger().Warn("启动配置已失效", logging.Fields{
			Event:     "launch.profile.stale",
			ErrorCode: ErrorCodeLaunchProfile,
			Attributes: map[string]string{
				"versionId": row.versionID,
			},
		})
		return nil, newInputError(ErrorCodeLaunchProfile, messageProfilePathUnusable)
	}

	summary := newProfileSummary(row)
	return &summary, nil
}

// DeleteLaunchProfile 删除指定的启动配置。
//
// 幂等：配置不存在时视为成功（调用方重复点击删除不应报错）。
func DeleteLaunchProfile(id string) *Error {
	trimmed := strings.TrimSpace(id)
	if trimmed == "" {
		return newInputError(ErrorCodeLaunchProfile, messageProfileIDEmpty)
	}

	db, dbErr := profileDatabase()
	if dbErr != nil {
		return dbErr
	}

	if _, execErr := db.Exec("DELETE FROM launch_profiles WHERE id = ?", trimmed); execErr != nil {
		return newSystemError(ErrorCodeLaunchProfile, messageProfileDeleteFailed)
	}

	return nil
}

// readProfileRow 执行一次单行查询并返回行数据。
//
// 无结果时返回 sql.ErrNoRows，让调用方区分「没有配置」与「查询失败」。
func readProfileRow(db *sql.DB, query string, args ...any) (profileRow, error) {
	return scanProfileRow(db.QueryRow(query, args...))
}

// isUsableProfileJava 判断历史 Java 路径是否仍可用。
//
// 只检查「存在且是普通文件」：版本是否受支持、路径是否可执行
// 属 P2-01 的 ValidateJavaPath（会执行子进程），恢复流程不重复做。
func isUsableProfileJava(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !info.IsDir()
}

// isUsableProfileDirectory 判断历史游戏目录是否仍可用。
//
// 只检查「存在且是目录」：目录结构完整性检验属 P2-02 的 CheckGameDirectory，
// 恢复流程只判断路径是否还在，避免把用户带回一个已经不通的启动页。
func isUsableProfileDirectory(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.IsDir()
}
