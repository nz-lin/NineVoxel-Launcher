package database

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// TestMigrateAppliesInitialSchema 验证首个迁移能在全新数据库上建立两张表。
//
// 这是 P1-04 设置持久化的前置条件：表不存在时任何设置读写都会失败。
// 用例使用临时目录中的真实文件数据库（而非内存库），以覆盖文件型 WAL 场景。
func TestMigrateAppliesInitialSchema(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "app.db")

	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	defer db.Close()

	result, err := Migrate(context.Background(), db)
	if err != nil {
		t.Fatalf("执行迁移失败: %v", err)
	}

	if len(result.Applied) == 0 {
		t.Fatal("首次迁移应当至少应用一个版本，实际为空")
	}
	if result.CurrentVersion <= 0 {
		t.Fatalf("当前版本期望大于 0，实际 %d", result.CurrentVersion)
	}

	// 两张表都必须存在。
	for _, table := range []string{"schema_migrations", "app_settings"} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil {
			t.Fatalf("表 %s 不可查询: %v", table, err)
		}
	}
}

// TestMigrateIsIdempotent 验证重复执行迁移不会重复应用。
//
// 幂等性是启动路径的硬要求：每次启动都会调用 Migrate，
// 若重复应用就会在 schema_migrations 里插入重复版本并导致后续 checksum 校验混乱。
func TestMigrateIsIdempotent(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "app.db")

	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	defer db.Close()

	ctx := context.Background()

	first, err := Migrate(ctx, db)
	if err != nil {
		t.Fatalf("首次迁移失败: %v", err)
	}

	second, err := Migrate(ctx, db)
	if err != nil {
		t.Fatalf("第二次迁移失败: %v", err)
	}

	if len(second.Applied) != 0 {
		t.Fatalf("第二次迁移不应应用任何版本，实际应用 %v", second.Applied)
	}
	if second.CurrentVersion != first.CurrentVersion {
		t.Fatalf("当前版本不一致：首次 %d，第二次 %d", first.CurrentVersion, second.CurrentVersion)
	}
}

// TestMigrateDetectsChecksumMismatch 验证 checksum 不一致时报错且不自动修复。
//
// 该用例直接篡改 schema_migrations 中的 checksum，模拟「迁移文件被改动」，
// 断言 Migrate 返回错误（且错误信息包含 DB_MIGRATION_001），并且不覆盖记录。
func TestMigrateDetectsChecksumMismatch(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "app.db")

	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	defer db.Close()

	ctx := context.Background()

	if _, err := Migrate(ctx, db); err != nil {
		t.Fatalf("首次迁移失败: %v", err)
	}

	// 篡改已应用记录的 checksum。
	if _, err := db.ExecContext(ctx,
		`UPDATE `+migrationTableName+` SET checksum = 'tampered' WHERE version = 1`); err != nil {
		t.Fatalf("篡改 checksum 失败: %v", err)
	}

	_, migrateErr := Migrate(ctx, db)
	if migrateErr == nil {
		t.Fatal("checksum 不一致时期望返回错误，实际通过")
	}
	if !containsSubstring(migrateErr.Error(), MigrationErrorCode) {
		t.Fatalf("错误信息应包含 %s，实际: %v", MigrationErrorCode, migrateErr)
	}

	// 关键断言：记录必须保持被篡改的值，证明没有「自动修复」。
	var checksum string
	if err := db.QueryRowContext(ctx,
		`SELECT checksum FROM `+migrationTableName+` WHERE version = 1`).Scan(&checksum); err != nil {
		t.Fatalf("回读 checksum 失败: %v", err)
	}
	if checksum != "tampered" {
		t.Fatalf("checksum 被自动改回 %q，违反「不得自动修复」要求", checksum)
	}
}

// TestLoadMigrationsParsesEmbeddedFile 验证内嵌迁移被正确解析。
//
// 断言：至少一个迁移、版本号为正、checksum 为 64 位十六进制、语句非空。
// 这些是迁移执行器的基本前提，任一不成立都会让迁移静默失效。
func TestLoadMigrationsParsesEmbeddedFile(t *testing.T) {
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatalf("加载内嵌迁移失败: %v", err)
	}
	if len(migrations) == 0 {
		t.Fatal("未加载到任何迁移文件")
	}

	for _, item := range migrations {
		if item.version <= 0 {
			t.Fatalf("迁移 %s 的版本号非法: %d", item.fileName, item.version)
		}
		if len(item.checksum) != 64 {
			t.Fatalf("迁移 %s 的 checksum 长度期望 64，实际 %d", item.fileName, len(item.checksum))
		}
		if len(item.statements) == 0 {
			t.Fatalf("迁移 %s 未解析出任何 SQL 语句", item.fileName)
		}
		if item.description == "" {
			t.Fatalf("迁移 %s 的说明为空", item.fileName)
		}
	}
}

// TestSplitStatementsStripsComments 验证语句切分能剥离注释与空行。
//
// 迁移文件里的中文注释不参与执行：若被当作 SQL 传入会直接报语法错误。
func TestSplitStatementsStripsComments(t *testing.T) {
	content := []byte(`
-- 第一段说明
CREATE TABLE a (id INTEGER);
-- 中间注释
CREATE TABLE b (id INTEGER);
`)

	statements := splitStatements(content)
	if len(statements) != 2 {
		t.Fatalf("期望解析出 2 条语句，实际 %d 条: %v", len(statements), statements)
	}
	if !containsSubstring(statements[0], "CREATE TABLE a") {
		t.Fatalf("第一条语句不符合预期: %q", statements[0])
	}
}

// TestParseMigrationVersionRejectsBadName 验证不符合约定的文件名被拒绝。
func TestParseMigrationVersionRejectsBadName(t *testing.T) {
	cases := []string{"001.sql", "abc_initial.sql", "000001.sql", "_initial.sql"}
	for _, name := range cases {
		if _, err := parseMigrationVersion(name); err == nil {
			t.Fatalf("文件名 %q 期望解析失败，实际通过", name)
		}
	}
}

// containsSubstring 判断文本是否包含子串。
//
// 独立实现而不引 strings：避免与包内其他文件的 import 产生无谓耦合。
func containsSubstring(text string, sub string) bool {
	if len(sub) == 0 {
		return true
	}
	if len(text) < len(sub) {
		return false
	}
	for i := 0; i+len(sub) <= len(text); i++ {
		if text[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// 编译期断言：本文件使用的 sql 包类型必须可用（防止将来误删 import）。
var _ = sql.ErrNoRows

// Lazy frontend services can migrate separate connections to the same database.
func TestConcurrentServiceMigrations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.db")
	first, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	start := make(chan struct{})
	outcomes := make(chan error, 2)
	for _, db := range []*sql.DB{first, second} {
		go func(db *sql.DB) { <-start; _, err := Migrate(context.Background(), db); outcomes <- err }(db)
	}
	close(start)
	for range 2 {
		if err := <-outcomes; err != nil {
			t.Fatal(err)
		}
	}
}
