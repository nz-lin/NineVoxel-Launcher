package core

import (
	"os"
	"path/filepath"
	"testing"
)

// writeVersionDirFixture 创建版本目录与它的清单。
func writeVersionDirFixture(t *testing.T, root, versionDir, versionID string, withManifest bool) {
	t.Helper()
	directory := filepath.Join(root, filepath.FromSlash(versionDir))
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, versionID+".json"), []byte(`{"id":"`+versionID+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if !withManifest {
		return
	}
	file, err := NewManifestFile(root, versionDir+"/"+versionID+".json", GroupVersionJSON)
	if err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{
		SchemaVersion: manifestSchemaVersion,
		Identity:      "vanilla:" + versionID,
		VersionID:     versionID,
		VersionDir:    versionDir,
		Files:         []ManifestFile{file},
	}
	if writeErr := WriteManifest(root, manifest); writeErr != nil {
		t.Fatal(writeErr)
	}
}

// TestListVersionDirs 验证枚举两个版本根并标记是否由启动器安装。
func TestListVersionDirs(t *testing.T) {
	root := t.TempDir()
	writeVersionDirFixture(t, root, "versions/1.21.4", "1.21.4", true)
	writeVersionDirFixture(t, root, "loaders/fabric-loader-1.0-1.21.4", "fabric-loader-1.0-1.21.4", true)
	// 用户手动放入：没有清单。
	writeVersionDirFixture(t, root, "versions/manual", "manual", false)
	// 安装暂存目录不属于可启动版本。
	writeVersionDirFixture(t, root, "versions/broken.staging", "broken", false)

	dirs, err := ListVersionDirs(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(dirs) != 3 {
		t.Fatalf("版本目录数量 = %d，期望 3（暂存目录应跳过）：%+v", len(dirs), dirs)
	}
	byID := make(map[string]VersionDir, len(dirs))
	for _, dir := range dirs {
		byID[dir.VersionID] = dir
	}
	if !byID["1.21.4"].Managed || !byID["fabric-loader-1.0-1.21.4"].Managed {
		t.Fatalf("带清单的目录应标记为 managed：%+v", dirs)
	}
	if byID["manual"].Managed {
		t.Fatal("没有清单的目录不应标记为 managed")
	}
	if byID["1.21.4"].SizeBytes <= 0 {
		t.Fatal("应统计目录大小")
	}
}

// TestRemoveVersionDirRefusesWhileLeased 验证租约未释放时拒绝删除。
func TestRemoveVersionDirRefusesWhileLeased(t *testing.T) {
	root := t.TempDir()
	writeVersionDirFixture(t, root, "versions/1.21.4", "1.21.4", true)
	writeLeaseTable(t, root, "1.21.4")

	if err := RemoveVersionDir(root, "versions/1.21.4"); err == nil {
		t.Fatal("有租约时应拒绝删除")
	}
	if _, statErr := os.Stat(filepath.Join(root, "versions", "1.21.4")); statErr != nil {
		t.Fatalf("版本目录被误删: %v", statErr)
	}
}

// TestRemoveVersionDirRejectsForeignPath 验证只能删受支持的版本根。
func TestRemoveVersionDirRejectsForeignPath(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{"libraries", "versions", "versions/1.21.4/../..", "assets/objects"} {
		if err := RemoveVersionDir(root, rel); err == nil {
			t.Fatalf("%q 不应被当作版本目录", rel)
		}
	}
}

// TestRemoveVersionDirDeletesManaged 验证正常删除。
func TestRemoveVersionDirDeletesManaged(t *testing.T) {
	root := t.TempDir()
	writeVersionDirFixture(t, root, "loaders/fabric-loader-1.0-1.21.4", "fabric-loader-1.0-1.21.4", true)
	if err := RemoveVersionDir(root, "loaders/fabric-loader-1.0-1.21.4"); err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "loaders", "fabric-loader-1.0-1.21.4")); !os.IsNotExist(statErr) {
		t.Fatalf("版本目录应已删除，实际 stat: %v", statErr)
	}
}

// writeLeaseTable 写入一份只含指定版本的租约表。
func writeLeaseTable(t *testing.T, root, versionID string) {
	t.Helper()
	directory := filepath.Join(root, indexDirName)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"schemaVersion":1,"leases":[{"versionId":"` + versionID + `","pid":1,"processStartMs":1,"acquiredAtMs":1}]}`
	if err := os.WriteFile(filepath.Join(directory, leaseFileName), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
