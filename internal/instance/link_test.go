package instance

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// ===== 硬链接落地 =====

// TestMaterializeFileCreatesHardLink 验证同一卷上优先建立硬链接。
//
// 硬链接是「多个实例只占一份磁盘」的前提：只要 `sameFile` 为真就说明没有复制。
func TestMaterializeFileCreatesHardLink(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "cache", "lib.jar")
	target := filepath.Join(root, "instance", "libraries", "lib.jar")
	writeFixture(t, source, "library content")

	if err := materializeFile(context.Background(), source, target); err != nil {
		t.Fatalf("落地失败: %v", err)
	}

	content, readErr := os.ReadFile(target)
	if readErr != nil || string(content) != "library content" {
		t.Fatalf("目标文件内容异常: %q %v", content, readErr)
	}

	same, sameErr := sameFileContent(source, target)
	if sameErr != nil {
		t.Fatalf("比较失败: %v", sameErr)
	}
	if !same {
		t.Fatal("期望建立硬链接（同一 inode），实际仍是独立副本")
	}
}

// TestMaterializeFileReplacesStaleTarget 验证目标已存在时被替换为指向缓存的新内容。
//
// 重复安装同一个版本时，目标文件可能来自旧的缓存对象；改名替换必须原子生效。
func TestMaterializeFileReplacesStaleTarget(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "cache", "lib.jar")
	target := filepath.Join(root, "instance", "libraries", "lib.jar")
	writeFixture(t, source, "fresh content")
	writeFixture(t, target, "stale content")

	if err := materializeFile(context.Background(), source, target); err != nil {
		t.Fatalf("落地失败: %v", err)
	}

	content, _ := os.ReadFile(target)
	if string(content) != "fresh content" {
		t.Fatalf("目标内容期望为新内容，实际 %q", content)
	}
	if same, _ := sameFileContent(source, target); !same {
		t.Fatal("替换后期望为硬链接")
	}
}

// TestMaterializeFileRespectsCancellation 验证已取消的上下文直接失败。
func TestMaterializeFileRespectsCancellation(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "cache", "lib.jar")
	writeFixture(t, source, "content")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := materializeFile(ctx, source, filepath.Join(root, "out.jar")); err == nil {
		t.Fatal("已取消的上下文期望返回错误")
	}
}

// writeFixture 写入测试文件并创建父目录。
func writeFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
