package database

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// TestMigrateCreatesSingleBackupForPendingBatch 验证一次迁移批次只生成一份备份。
func TestMigrateCreatesSingleBackupForPendingBatch(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "app.db")
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	defer db.Close()

	if _, err := Migrate(context.Background(), db); err != nil {
		t.Fatalf("执行迁移失败: %v", err)
	}

	entries, err := os.ReadDir(filepath.Dir(dbPath))
	if err != nil {
		t.Fatal(err)
	}
	backups := 0
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), "app.db.backup-") {
			backups++
		}
	}
	if backups != 1 {
		t.Fatalf("首次迁移生成备份数 = %d，期望 1", backups)
	}
}

// TestMigratePrunesOldBackups 验证重复启动时会把历史备份收敛到保留上限。
func TestMigratePrunesOldBackups(t *testing.T) {
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

	base := time.Now().Add(-time.Hour)
	for index := 0; index < 6; index++ {
		path := filepath.Join(filepath.Dir(dbPath), fmt.Sprintf("app.db.backup-%d-%d", index+1, index+1))
		if err := os.WriteFile(path, []byte("fake"), 0o600); err != nil {
			t.Fatal(err)
		}
		stamp := base.Add(time.Duration(index) * time.Minute)
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := Migrate(ctx, db); err != nil {
		t.Fatalf("第二次迁移失败: %v", err)
	}

	entries, err := os.ReadDir(filepath.Dir(dbPath))
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0)
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), "app.db.backup-") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	if len(names) != migrationBackupRetention {
		t.Fatalf("保留备份数 = %d，期望 %d：%v", len(names), migrationBackupRetention, names)
	}
}
