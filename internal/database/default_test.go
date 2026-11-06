// 本文件验证进程级共享连接与旧布局搬迁。
//
// 约束：全部在 t.TempDir() 内完成，不触碰真实用户数据目录；
// 共享池是进程级状态，测试结束必须 CloseDefault 复位。
package database

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"example.com/ninevoxel/launcher/internal/artifact"
)

// isolateDataRoot 把用户配置目录重定向到临时目录。
//
// macOS 的 os.UserConfigDir 读 $HOME/Library/Application Support，
// Windows 读 %LocalAppData%/AppData，Linux 读 XDG_CONFIG_HOME；
// 三种变量都设置，保证测试在任何平台上都不写真实目录。
func isolateDataRoot(t *testing.T) string {
	t.Helper()
	temp := t.TempDir()
	t.Setenv("HOME", temp)
	t.Setenv("XDG_CONFIG_HOME", temp)
	t.Setenv("XDG_DATA_HOME", temp)
	t.Setenv("AppData", temp)
	t.Setenv("LocalAppData", temp)
	return temp
}

// TestMigrateLegacyLayoutMovesDatabaseFiles 验证旧版散落的数据库文件被搬入 db/。
func TestMigrateLegacyLayoutMovesDatabaseFiles(t *testing.T) {
	root := t.TempDir()
	targetDir := filepath.Join(root, "db")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatalf("创建目标目录失败: %v", err)
	}

	legacyFiles := []string{
		artifact.DatabaseFileName,
		artifact.DatabaseFileName + "-wal",
		artifact.DatabaseFileName + "-shm",
		artifact.DatabaseFileName + ".backup-1-1000",
	}
	for _, name := range legacyFiles {
		if err := os.WriteFile(filepath.Join(root, name), []byte("legacy"), 0o644); err != nil {
			t.Fatalf("写入旧文件 %s 失败: %v", name, err)
		}
	}

	if err := migrateLegacyLayout(root, targetDir); err != nil {
		t.Fatalf("搬迁旧布局失败: %v", err)
	}

	for _, name := range legacyFiles {
		if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Errorf("旧位置仍存在 %s（期望已搬迁）", name)
		}
		moved := filepath.Join(targetDir, name)
		content, err := os.ReadFile(moved)
		if err != nil {
			t.Errorf("新位置缺少 %s: %v", name, err)
			continue
		}
		if string(content) != "legacy" {
			t.Errorf("%s 内容被破坏: %q", name, content)
		}
	}
}

// TestMigrateLegacyLayoutKeepsNewDatabase 验证目标已存在时绝不覆盖。
func TestMigrateLegacyLayoutKeepsNewDatabase(t *testing.T) {
	root := t.TempDir()
	targetDir := filepath.Join(root, "db")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatalf("创建目标目录失败: %v", err)
	}

	name := artifact.DatabaseFileName
	if err := os.WriteFile(filepath.Join(root, name), []byte("old"), 0o644); err != nil {
		t.Fatalf("写入旧文件失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(targetDir, name), []byte("new"), 0o644); err != nil {
		t.Fatalf("写入新文件失败: %v", err)
	}

	if err := migrateLegacyLayout(root, targetDir); err != nil {
		t.Fatalf("搬迁旧布局失败: %v", err)
	}

	content, err := os.ReadFile(filepath.Join(targetDir, name))
	if err != nil {
		t.Fatalf("读取新文件失败: %v", err)
	}
	if string(content) != "new" {
		t.Fatalf("新库被旧库覆盖: %q", content)
	}
	if _, err := os.Stat(filepath.Join(root, name)); err != nil {
		t.Fatalf("目标已存在时不应移动旧文件: %v", err)
	}
}

// TestDefaultReturnsSharedPool 验证 Default 只打开一次且返回同一个连接。
func TestDefaultReturnsSharedPool(t *testing.T) {
	isolateDataRoot(t)
	if err := CloseDefault(); err != nil {
		t.Fatalf("复位共享池失败: %v", err)
	}
	t.Cleanup(func() { _ = CloseDefault() })

	first, err := Default(context.Background())
	if err != nil {
		t.Fatalf("打开共享池失败: %v", err)
	}
	second, err := Default(context.Background())
	if err != nil {
		t.Fatalf("再次获取共享池失败: %v", err)
	}
	if first != second {
		t.Fatal("Default 返回了不同的连接池")
	}
	if !IsDefault(first) {
		t.Fatal("IsDefault 未识别共享池")
	}
	if IsDefault(nil) {
		t.Fatal("IsDefault(nil) 应为 false")
	}

	// 路径必须是 db/app.db：这是第三节「数据库归拢」的核心约束。
	path, pathErr := DefaultPath()
	if pathErr != nil {
		t.Fatalf("解析默认路径失败: %v", pathErr)
	}
	if filepath.Base(path) != artifact.DatabaseFileName {
		t.Errorf("文件名 = %q，期望 %q", filepath.Base(path), artifact.DatabaseFileName)
	}
	if filepath.Base(filepath.Dir(path)) != "db" {
		t.Errorf("父目录 = %q，期望 db", filepath.Base(filepath.Dir(path)))
	}
}

// TestDefaultPathMigratesLegacyDatabase 验证 DefaultPath 会在真实数据根上完成搬迁。
func TestDefaultPathMigratesLegacyDatabase(t *testing.T) {
	isolateDataRoot(t)
	if err := CloseDefault(); err != nil {
		t.Fatalf("复位共享池失败: %v", err)
	}
	t.Cleanup(func() { _ = CloseDefault() })

	root, rootErr := artifact.DataRoot()
	if rootErr != nil {
		t.Fatalf("解析数据根失败: %v", rootErr)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("创建数据根失败: %v", err)
	}
	legacy := filepath.Join(root, artifact.DatabaseFileName)
	if err := os.WriteFile(legacy, []byte("legacy-db"), 0o644); err != nil {
		t.Fatalf("写入旧数据库失败: %v", err)
	}

	path, pathErr := DefaultPath()
	if pathErr != nil {
		t.Fatalf("解析默认路径失败: %v", pathErr)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatalf("旧数据库仍在原位置: %v", err)
	}
	content, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("读取搬迁后的数据库失败: %v", readErr)
	}
	if string(content) != "legacy-db" {
		t.Fatalf("搬迁后内容异常: %q", content)
	}
}
