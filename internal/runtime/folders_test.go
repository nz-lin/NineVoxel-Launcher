package runtime

import (
	"os"
	"path/filepath"
	"testing"
)

// TestOpenRuntimeFolderCreatesAndOpensDataRootRuntimes 验证运行时目录按数据根解析。
func TestOpenRuntimeFolderCreatesAndOpensDataRootRuntimes(t *testing.T) {
	root := t.TempDir()
	oldRoot := runtimeDataRoot
	runtimeDataRoot = func() (string, *AppError) { return root, nil }
	t.Cleanup(func() { runtimeDataRoot = oldRoot })

	oldOpen := openRuntimeDirectory
	var opened string
	openRuntimeDirectory = func(target string) error {
		opened = target
		return nil
	}
	t.Cleanup(func() { openRuntimeDirectory = oldOpen })

	if err := (&RuntimeService{}).OpenRuntimeFolder(); err != nil {
		t.Fatalf("打开运行时目录失败：%v", err)
	}
	want := filepath.Join(root, "runtimes")
	if opened != want {
		t.Fatalf("打开路径 = %q，期望 %q", opened, want)
	}
	if info, err := os.Stat(want); err != nil || !info.IsDir() {
		t.Fatalf("运行时目录未创建：%v", err)
	}
}
