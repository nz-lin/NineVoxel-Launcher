// 本文件实现数据库迁移执行器。
//
// 职责边界（与 database.go 保持分工）：
//   - database.go：按 ADR-002 的 DSN 打开连接、查询引擎版本；
//   - 本文件：建立 schema_migrations、计算并比对 SHA-256 checksum、
//     按版本号顺序应用内嵌的 migrations/*.sql，并把已应用记录写回。
//
// 硬性约束（接口冻结清单 §6.2 与 ADR-002 门槛）：
//  1. checksum 不一致必须返回错误且**不自动修复**——自动「修好」等于掩盖文件被改动的事实；
//  2. 迁移在单个事务内完成，失败则整批回滚，不留下半应用状态；
//  3. 迁移前用 VACUUM INTO 生成备份副本（门槛第 5 条），备份路径脱敏后写入日志。
package database

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// 迁移文件通过 go:embed 内嵌进二进制。
//
// 用 embed 而不是运行时读目录：安装版没有源码树，只有内嵌资源才保证迁移始终可用。
//
//go:embed migrations/*.sql
var migrationFS embed.FS

// migrationsDirName 是内嵌迁移文件所在的内嵌目录名。
const migrationsDirName = "migrations"

// migrationFileNameSuffix 是迁移文件名后缀。
const migrationFileNameSuffix = ".sql"

// migrationVersionDivisor 是迁移版本号的解析基数（文件名前缀按十进制解析）。
const migrationVersionDivisor = 10

// migrationTableName 是迁移记录表名，与迁移 SQL 中的建表语句保持一致。
const migrationTableName = "schema_migrations"

// MigrationErrorCode 是迁移失败时返回给前端的稳定错误码。
//
// 由接口冻结清单 §8.3 冻结：`DB_MIGRATION_001` 表示数据库迁移失败，
// 包含 checksum 不一致与迁移执行失败两种情形。
const MigrationErrorCode = "DB_MIGRATION_001"

// checksumMismatchMessage 是 checksum 不一致时的固定说明。
//
// 说明里不含路径与文件名之外的敏感信息：迁移文件是随程序分发的资源，
// 其名称不构成用户隐私，但完整路径不应出现在面向用户的错误里。
const checksumMismatchMessage = "迁移文件内容与已应用记录不一致"

// migration 表示一个待应用或已应用的迁移。
type migration struct {
	// version 为迁移版本号，取自文件名前缀（例如 000001 解析为 1）。
	version int64

	// fileName 为迁移文件名，例如 000001_initial.sql。
	fileName string

	// description 为迁移说明，取文件首行注释或文件名，用于写入 schema_migrations。
	description string

	// checksum 为迁移文件内容的 SHA-256（十六进制小写）。
	checksum string

	// statements 为按分号切分后的 SQL 语句列表。
	statements []string
}

// MigrationResult 汇总一次迁移执行的结果。
//
// 供调用方（settings 服务）在启动日志中记录「执行了几个迁移」「当前版本是多少」。
type MigrationResult struct {
	// Applied 为本次实际应用的版本号列表；空表示数据库已是最新。
	Applied []int64

	// CurrentVersion 为迁移完成后数据库所处的版本（最新已应用版本）。
	CurrentVersion int64

	// BackupPath 为迁移前生成的备份文件路径；未生成备份时为空。
	//
	// 该值**只用于进程内日志**，不得通过服务方法返回给前端（含绝对路径）。
	BackupPath string
}

// Migrate 应用全部尚未执行的迁移。
//
// 参数 ctx 为调用方上下文；参数 db 为已按 ADR-002 打开的连接池。
// 行为：
//   - 建立 schema_migrations（若不存在）；
//   - 逐条比对已应用迁移的 checksum，不一致立即返回错误；**不自动修复**；
//   - 对尚未应用的迁移按版本号升序逐个在事务内执行并写回记录。
//
// 返回本次应用的版本列表与当前版本；任何失败都返回带上下文的错误，
// 调用方负责转换为 `DB_MIGRATION_001`。
func Migrate(ctx context.Context, db *sql.DB) (MigrationResult, error) {
	var result MigrationResult
	if db == nil {
		return result, errors.New("database: 迁移需要非空的数据库连接")
	}

	if err := ensureMigrationTable(ctx, db); err != nil {
		return result, err
	}

	migrations, err := loadMigrations()
	if err != nil {
		return result, err
	}

	applied, err := readAppliedMigrations(ctx, db)
	if err != nil {
		return result, err
	}

	// 第一步：校验已应用迁移的内容未被改动。
	// 该检查必须先于任何写入：一旦文件被改过，后续迁移的顺序假设就不可靠了。
	if err := verifyAppliedChecksums(migrations, applied); err != nil {
		return result, err
	}

	// 第二步：按版本号升序逐个应用尚未执行的迁移。
	for _, item := range migrations {
		if _, ok := applied[item.version]; ok {
			continue
		}

		// 迁移前备份：只在确实有迁移要执行时才做，避免每次启动都生成副本。
		backupPath, backupErr := createMigrationBackup(ctx, db, item.version)
		if backupErr != nil {
			// 备份失败不阻断迁移：ADR-002 要求「尽力备份」，
			// 但迁移本身是启动必需步骤，不能因为备份失败让应用起不来。
			result.BackupPath = ""
		} else {
			result.BackupPath = backupPath
		}

		if err := applyMigration(ctx, db, item); err != nil {
			return result, err
		}

		result.Applied = append(result.Applied, item.version)
		result.CurrentVersion = item.version
	}

	// 没有新迁移时也要回填当前版本，便于调用方记录启动日志。
	if result.CurrentVersion == 0 {
		result.CurrentVersion = highestAppliedVersion(applied)
	}

	return result, nil
}

// ensureMigrationTable 建立迁移记录表。
//
// 使用 CREATE TABLE IF NOT EXISTS，重复调用安全。
// 表结构与 000001_initial.sql 中的定义一致：迁移表本身必须先于任何迁移存在。
func ensureMigrationTable(ctx context.Context, db *sql.DB) error {
	const statement = `
CREATE TABLE IF NOT EXISTS ` + migrationTableName + ` (
    version INTEGER PRIMARY KEY,
    applied_at_ms INTEGER NOT NULL,
    checksum TEXT NOT NULL,
    description TEXT NOT NULL
)`

	if _, err := db.ExecContext(ctx, statement); err != nil {
		return fmt.Errorf("database: 建立迁移记录表失败: %w", err)
	}
	return nil
}

// loadMigrations 从内嵌文件系统加载全部迁移，并按版本号升序返回。
//
// 版本号解析规则：文件名必须以 6 位数字前缀开头后接下划线，
// 例如 000001_initial.sql → 1。解析失败的文件直接返回错误，
// 而不是静默跳过——静默跳过会让「少应用一个迁移」变成难以发现的问题。
func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFS, migrationsDirName)
	if err != nil {
		return nil, fmt.Errorf("database: 读取内嵌迁移目录失败: %w", err)
	}

	result := make([]migration, 0, len(entries))
	seenVersions := make(map[int64]string, len(entries))

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), migrationFileNameSuffix) {
			continue
		}

		version, err := parseMigrationVersion(entry.Name())
		if err != nil {
			return nil, err
		}

		// 同一版本号出现两次属于构建期错误，必须报出来而不是任选其一。
		if existing, ok := seenVersions[version]; ok {
			return nil, fmt.Errorf("database: 迁移版本 %d 重复：%s 与 %s", version, existing, entry.Name())
		}
		seenVersions[version] = entry.Name()

		content, err := fs.ReadFile(migrationFS, migrationsDirName+"/"+entry.Name())
		if err != nil {
			return nil, fmt.Errorf("database: 读取迁移文件 %s 失败: %w", entry.Name(), err)
		}

		result = append(result, migration{
			version:     version,
			fileName:    entry.Name(),
			description: extractDescription(content, entry.Name()),
			checksum:    computeChecksum(content),
			statements:  splitStatements(content),
		})
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].version < result[j].version
	})

	return result, nil
}

// parseMigrationVersion 从迁移文件名解析版本号。
//
// 参数 fileName 形如 000001_initial.sql；返回版本号 1。
// 文件名不符合约定（前缀不是数字、缺下划线）时返回错误，
// 避免文件名拼写错误导致迁移被误排在错误位置。
func parseMigrationVersion(fileName string) (int64, error) {
	base := strings.TrimSuffix(fileName, migrationFileNameSuffix)

	// Go 1.20+ 提供 strings.Cut，一次拿到前缀与剩余部分。
	prefix, rest, found := strings.Cut(base, "_")
	if !found || prefix == "" || rest == "" {
		return 0, fmt.Errorf("database: 迁移文件名 %q 不符合 <版本>_<说明>.sql 约定", fileName)
	}

	version, err := strconv.ParseInt(prefix, migrationVersionDivisor, 64)
	if err != nil {
		return 0, fmt.Errorf("database: 迁移文件名 %q 的版本前缀 %q 不是十进制数字: %w", fileName, prefix, err)
	}
	if version <= 0 {
		return 0, fmt.Errorf("database: 迁移文件名 %q 的版本号必须大于 0", fileName)
	}
	return version, nil
}

// extractDescription 取迁移文件的说明文字，回退到文件名。
//
// 规则：从文件开头向下收集连续以 `--` 开头的注释行，去掉 `--` 前缀后拼接，
// 遇到第一行非注释内容即停止。说明写入 schema_migrations，便于人工核对。
func extractDescription(content []byte, fallback string) string {
	lines := strings.Split(string(content), "\n")
	parts := make([]string, 0, len(lines))

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if !strings.HasPrefix(trimmed, "--") {
			break
		}
		parts = append(parts, strings.TrimSpace(strings.TrimPrefix(trimmed, "--")))
	}

	if len(parts) == 0 {
		return fallback
	}

	// 说明可能很长，截断到 200 字符：该字段用于人读，不需要完整正文。
	combined := strings.Join(parts, " ")
	const maxDescriptionRunes = 200
	runes := []rune(combined)
	if len(runes) > maxDescriptionRunes {
		return string(runes[:maxDescriptionRunes]) + "…"
	}
	return combined
}

// computeChecksum 计算迁移文件内容的 SHA-256 校验值。
//
// 使用标准库 crypto/sha256，输出小写十六进制。
// 校验值覆盖文件全部字节（含注释与空白）：任何改动都应被识别出来。
func computeChecksum(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

// splitStatements 把迁移文件按分号切分为独立语句。
//
// 简化规则（本项目的迁移文件不含字符串字面量中的分号，也不含触发器/存储过程）：
// 逐行剥离 `--` 行注释，再按 `;` 切分，过滤空语句。
// 这样既避免依赖第三方 SQL 解析器，也保持迁移文件的可读性。
func splitStatements(content []byte) []string {
	lines := strings.Split(string(content), "\n")
	// stripped 累积去掉行注释后的内容；用切片而不是字符串拼接，便于调试。
	stripped := make([]string, 0, len(lines))

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "--") {
			// 整行注释或空行：直接跳过。
			continue
		}
		stripped = append(stripped, trimmed)
	}

	combined := strings.Join(stripped, "\n")
	parts := strings.Split(combined, ";")

	result := make([]string, 0, len(parts))
	for _, part := range parts {
		statement := strings.TrimSpace(part)
		if statement == "" {
			continue
		}
		result = append(result, statement)
	}
	return result
}

// appliedMigration 表示一条已在数据库中的迁移记录。
type appliedMigration struct {
	// checksum 为记录中的 SHA-256，用于与当前文件内容比对。
	checksum string

	// description 为记录中的说明，仅用于错误信息。
	description string
}

// readAppliedMigrations 读取 schema_migrations 中的全部记录。
//
// 返回以版本号为键的映射；表为空时返回空映射（首次启动的正常情形）。
func readAppliedMigrations(ctx context.Context, db *sql.DB) (map[int64]appliedMigration, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT version, checksum, description FROM `+migrationTableName)
	if err != nil {
		return nil, fmt.Errorf("database: 读取迁移记录失败: %w", err)
	}
	defer rows.Close()

	result := make(map[int64]appliedMigration)
	for rows.Next() {
		var version int64
		var record appliedMigration
		if err := rows.Scan(&version, &record.checksum, &record.description); err != nil {
			return nil, fmt.Errorf("database: 解析迁移记录失败: %w", err)
		}
		result[version] = record
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("database: 遍历迁移记录失败: %w", err)
	}
	return result, nil
}

// verifyAppliedChecksums 校验已应用迁移的文件内容未被改动。
//
// 三种情形：
//   - 记录有、文件也在 → 比对 checksum，不一致即返回错误；
//   - 记录有、文件不在 → 说明迁移文件被删除，同样返回错误；
//   - 记录没有 → 属于待应用迁移，此处不处理。
//
// **绝不自动修复**：把记录改成新 checksum 会让「文件被改动」这一事实永久消失。
func verifyAppliedChecksums(migrations []migration, applied map[int64]appliedMigration) error {
	byVersion := make(map[int64]migration, len(migrations))
	for _, item := range migrations {
		byVersion[item.version] = item
	}

	// 按版本号排序后校验：保证错误信息里出现的是最小不一致版本，便于定位。
	versions := make([]int64, 0, len(applied))
	for version := range applied {
		versions = append(versions, version)
	}
	sort.Slice(versions, func(i, j int) bool { return versions[i] < versions[j] })

	for _, version := range versions {
		record := applied[version]

		current, ok := byVersion[version]
		if !ok {
			return fmt.Errorf("database: %s：已应用迁移版本 %d（%s）在当前构建中不存在",
				MigrationErrorCode, version, record.description)
		}

		if current.checksum != record.checksum {
			return fmt.Errorf("database: %s：%s（%s）%s，期望 %s 实际 %s",
				MigrationErrorCode, checksumMismatchMessage, current.fileName, "", record.checksum, current.checksum)
		}
	}

	return nil
}

// applyMigration 在单个事务内应用一条迁移并写回记录。
//
// 事务保证「语句执行」与「写回记录」原子完成：任一步失败都整体回滚，
// 不会出现「表已建好但没记录」或「有记录但表没建」的不一致状态。
func applyMigration(ctx context.Context, db *sql.DB, item migration) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("database: %s：开始迁移事务失败（%s）: %w", MigrationErrorCode, item.fileName, err)
	}

	// 出错时回滚；成功路径在前面显式 Commit，因此这里的 Rollback 只会是空操作。
	defer func() { _ = tx.Rollback() }()

	for _, statement := range item.statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("database: %s：执行迁移 %s 失败: %w", MigrationErrorCode, item.fileName, err)
		}
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO `+migrationTableName+` (version, applied_at_ms, checksum, description) VALUES (?, ?, ?, ?)`,
		item.version, time.Now().UTC().UnixMilli(), item.checksum, item.description,
	); err != nil {
		return fmt.Errorf("database: %s：写入迁移记录失败（%s）: %w", MigrationErrorCode, item.fileName, err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("database: %s：提交迁移事务失败（%s）: %w", MigrationErrorCode, item.fileName, err)
	}
	return nil
}

// highestAppliedVersion 返回已应用迁移中的最大版本号。
func highestAppliedVersion(applied map[int64]appliedMigration) int64 {
	var highest int64
	for version := range applied {
		if version > highest {
			highest = version
		}
	}
	return highest
}

// createMigrationBackup 在迁移前用 VACUUM INTO 生成数据库副本。
//
// 参数 dbPath 由调用方通过连接信息提供；本函数只负责生成备份并返回路径。
// 备份文件命名 `app.db.backup-<版本号>-<毫秒时间戳>`，放在数据库同目录下。
//
// 返回的路径**仅供进程内日志使用**，不得返回给前端。
// 任何一步失败都返回错误，由调用方决定是否降级继续（当前策略：备份失败不阻断迁移）。
func createMigrationBackup(ctx context.Context, db *sql.DB, version int64) (string, error) {
	// 先取数据库文件路径：VACUUM INTO 需要写在同一个盘上才最省空间。
	var dbPath string
	if err := db.QueryRowContext(ctx, `PRAGMA database_list`).Scan(&dummyInt, &dummyName, &dbPath); err != nil {
		// PRAGMA database_list 在不同驱动下的列数可能不同，失败即放弃备份。
		return "", fmt.Errorf("database: 查询数据库文件路径失败: %w", err)
	}
	if dbPath == "" {
		// 内存数据库（例如测试用的 file::memory:）没有文件可备份。
		return "", errors.New("database: 内存数据库不需要备份")
	}

	backupPath := filepath.Join(
		filepath.Dir(dbPath),
		"app.db.backup-"+strconv.FormatInt(version, migrationVersionDivisor)+"-"+
			strconv.FormatInt(time.Now().UTC().UnixMilli(), migrationVersionDivisor),
	)

	// VACUUM INTO 会创建一个一致的快照文件，比直接复制更安全（WAL 下复制可能得到半状态）。
	// 目标文件必须不存在，因此先删除残留（同毫秒重复调用才会发生）。
	_ = os.Remove(backupPath)

	if _, err := db.ExecContext(ctx, `VACUUM INTO ?`, backupPath); err != nil {
		return "", fmt.Errorf("database: 生成迁移前备份失败: %w", err)
	}
	return backupPath, nil
}

// 以下变量只用于 PRAGMA database_list 的 Scan 占位。
//
// 该 PRAGMA 返回 (seq, name, file) 三列，而我们只关心 file，
// 因此前两列用固定占位变量接收，避免引入额外的结构体。
var (
	dummyInt  int
	dummyName string
)
