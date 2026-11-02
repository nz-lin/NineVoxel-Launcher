package database

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// TestOpenMemoryAndVersion 验证内存数据库可以正常打开并返回非空的 SQLite 版本号。
// 该用例对应 ADR-002 门槛 3 的冒烟断言：构建产出的二进制必须真正能运行 SQLite 引擎。
func TestOpenMemoryAndVersion(t *testing.T) {
	db, err := Open("file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("打开内存数据库失败: %v", err)
	}
	defer db.Close()

	version, err := Version(context.Background(), db)
	if err != nil {
		t.Fatalf("查询 SQLite 版本失败: %v", err)
	}
	if version == "" {
		t.Fatal("SQLite 版本号为空")
	}
	t.Logf("SQLite 版本: %s", version)
}

// TestOpenFilePragmas 验证文件数据库的连接级参数确实来自 DSN。
// 期望 journal_mode 为 wal、foreign_keys 为 1、busy_timeout 为 5000，
// 这些参数一旦漏写进 DSN，新建连接就会退回默认值，本用例即可发现。
func TestOpenFilePragmas(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "launcher.db")

	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("打开文件数据库失败: %v", err)
	}
	defer db.Close()

	ctx := context.Background()

	version, err := Version(ctx, db)
	if err != nil {
		t.Fatalf("查询 SQLite 版本失败: %v", err)
	}
	if version == "" {
		t.Fatal("SQLite 版本号为空")
	}

	var journalMode string
	if err := db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatalf("回读 journal_mode 失败: %v", err)
	}
	if !strings.EqualFold(journalMode, "wal") {
		t.Fatalf("journal_mode 期望 wal，实际 %q", journalMode)
	}

	var foreignKeys int
	if err := db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		t.Fatalf("回读 foreign_keys 失败: %v", err)
	}
	if foreignKeys != 1 {
		t.Fatalf("foreign_keys 期望 1，实际 %d", foreignKeys)
	}

	var busyTimeout int
	if err := db.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		t.Fatalf("回读 busy_timeout 失败: %v", err)
	}
	if busyTimeout != 5000 {
		t.Fatalf("busy_timeout 期望 5000，实际 %d", busyTimeout)
	}
}

// TestOpenRejectsEmptyPath 验证空路径会被拒绝，避免意外打开无关的数据库。
func TestOpenRejectsEmptyPath(t *testing.T) {
	db, err := Open("   ")
	if err == nil {
		db.Close()
		t.Fatal("空路径期望返回错误，实际返回 nil")
	}
}
