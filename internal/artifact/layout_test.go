package artifact

import (
	"os"
	"path/filepath"
	"testing"
)

// TestResolveVersionDirPrefersLoaders 验证双根探测顺序。
func TestResolveVersionDirPrefersLoaders(t *testing.T) {
	root := t.TempDir()

	if _, found, err := ResolveVersionDir(root, "1.21.4"); err != nil || found {
		t.Fatalf("空核心不应命中版本目录: found=%v err=%v", found, err)
	}

	if err := os.MkdirAll(filepath.Join(root, DirVersions, "1.21.4"), 0o755); err != nil {
		t.Fatal(err)
	}
	dir, found, err := ResolveVersionDir(root, "1.21.4")
	if err != nil || !found || dir != DirVersions+"/1.21.4" {
		t.Fatalf("应命中 versions：dir=%q found=%v err=%v", dir, found, err)
	}

	// 两处同名时优先 loaders：加载器声明指向原版父版本，取错会得到更隐蔽的失败。
	if err := os.MkdirAll(filepath.Join(root, DirLoaders, "1.21.4"), 0o755); err != nil {
		t.Fatal(err)
	}
	dir, found, err = ResolveVersionDir(root, "1.21.4")
	if err != nil || !found || dir != DirLoaders+"/1.21.4" {
		t.Fatalf("同名时应优先 loaders：dir=%q found=%v err=%v", dir, found, err)
	}
}

// TestResolveVersionDirRejectsInvalidID 验证非法版本标识被拒绝。
func TestResolveVersionDirRejectsInvalidID(t *testing.T) {
	root := t.TempDir()
	for _, id := range []string{"", "..", "a/b", `a\b`, "C:evil"} {
		if _, _, err := ResolveVersionDir(root, id); err == nil {
			t.Fatalf("版本标识 %q 应被拒绝", id)
		}
	}
}

// TestLoaderVersionRelPath 验证加载器版本路径落在 loaders/。
func TestLoaderVersionRelPath(t *testing.T) {
	rel, err := LoaderVersionRelPath("fabric-loader-0.16.14-1.21.4", "fabric-loader-0.16.14-1.21.4.json")
	if err != nil {
		t.Fatal(err)
	}
	expected := DirLoaders + "/fabric-loader-0.16.14-1.21.4/fabric-loader-0.16.14-1.21.4.json"
	if rel != expected {
		t.Fatalf("路径 = %q，期望 %q", rel, expected)
	}
}
