package platform

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMigrateLegacyAppDataMovesDataAndCopiesLogs 验证旧数据根迁移到新 AppRoot。
func TestMigrateLegacyAppDataMovesDataAndCopiesLogs(t *testing.T) {
	oldRoot := t.TempDir()
	newRoot := t.TempDir()
	oldData := filepath.Join(oldRoot, dataDirName)
	if err := os.MkdirAll(filepath.Join(oldData, "instances"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oldData, "app.db"), []byte("db"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldLogs := filepath.Join(oldRoot, logDirName)
	if err := os.MkdirAll(oldLogs, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oldLogs, "app-20260101.log"), []byte("old-log"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := migrateLegacyAppData(newRoot, []string{oldRoot}); err != nil {
		t.Fatalf("迁移失败：%v", err)
	}
	body, err := os.ReadFile(filepath.Join(newRoot, dataDirName, "app.db"))
	if err != nil || string(body) != "db" {
		t.Fatalf("数据未迁移：%v %q", err, body)
	}
	if _, err := os.Stat(filepath.Join(oldData, "app.db")); !os.IsNotExist(err) {
		t.Fatalf("旧数据目录应已移走：%v", err)
	}
	body, err = os.ReadFile(filepath.Join(newRoot, logDirName, "app-20260101.log"))
	if err != nil || string(body) != "old-log" {
		t.Fatalf("旧日志未复制：%v %q", err, body)
	}
}

// TestMigrateLegacyAppDataDoesNotOverwriteExistingData 验证新数据非空时不合并。
func TestMigrateLegacyAppDataDoesNotOverwriteExistingData(t *testing.T) {
	oldRoot := t.TempDir()
	newRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(oldRoot, dataDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oldRoot, dataDirName, "app.db"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	newData := filepath.Join(newRoot, dataDirName)
	if err := os.MkdirAll(newData, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(newData, "app.db"), []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := migrateLegacyAppData(newRoot, []string{oldRoot}); err != nil {
		t.Fatalf("迁移失败：%v", err)
	}
	body, err := os.ReadFile(filepath.Join(newData, "app.db"))
	if err != nil || string(body) != "new" {
		t.Fatalf("新数据被覆盖：%v %q", err, body)
	}
}
