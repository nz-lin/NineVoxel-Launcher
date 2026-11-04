package instance

import (
	"os"
	"path/filepath"
	"testing"

	"example.com/ninevoxel/launcher/internal/core"
	"example.com/ninevoxel/launcher/internal/loader"
)

// writeManagedVersion 在核心中创建一个「由启动器安装」的版本目录。
//
// 参数 loader 决定落在哪个根：false 为原版 `versions/`，true 为 `loaders/`。
// 同时写入安装清单，否则回收会把目录当作「用户手动放入」而永远跳过。
func writeManagedVersion(t *testing.T, root, versionID string, loader bool, extraPaths ...string) string {
	t.Helper()
	var dir string
	if loader {
		dir = "loaders/" + versionID
	} else {
		dir = "versions/" + versionID
	}
	jsonRel := dir + "/" + versionID + ".json"
	fixtureWrite(t, filepath.Join(root, filepath.FromSlash(jsonRel)), []byte(`{"id":"`+versionID+`"}`))

	paths := append([]string{jsonRel}, extraPaths...)
	files := make([]core.ManifestFile, 0, len(paths))
	for _, rel := range paths {
		file, err := core.NewManifestFile(root, rel, core.GroupVersionJSON)
		if err != nil {
			t.Fatalf("构造清单条目 %s 失败: %v", rel, err)
		}
		files = append(files, file)
	}
	manifest := core.Manifest{
		SchemaVersion: 2,
		Identity:      "vanilla:" + versionID,
		VersionID:     versionID,
		VersionDir:    dir,
		Files:         files,
	}
	if err := core.WriteManifest(root, manifest); err != nil {
		t.Fatalf("写入清单失败: %v", err)
	}
	return dir
}

// TestCleanupUnusedCoreRemovesUnreferencedVersions 验证无引用版本被回收并统计释放量。
func TestCleanupUnusedCoreRemovesUnreferencedVersions(t *testing.T) {
	s := testService(t)
	writeManagedVersion(t, s.root, "1.21.4", false)
	writeManagedVersion(t, s.root, "fabric-loader-0.16.14-1.21.4", true)

	report, err := s.CleanupUnusedCore()
	if err != nil {
		t.Fatalf("回收失败: %v", err)
	}
	if len(report.Removed) != 2 {
		t.Fatalf("应回收 2 个版本，实际 %+v", report)
	}
	if report.FreedBytes <= 0 {
		t.Fatalf("应统计释放字节数，实际 %d", report.FreedBytes)
	}
	for _, dir := range []string{"versions/1.21.4", "loaders/fabric-loader-0.16.14-1.21.4"} {
		if _, statErr := os.Stat(filepath.Join(s.root, filepath.FromSlash(dir))); !os.IsNotExist(statErr) {
			t.Fatalf("%s 应已删除，实际 stat: %v", dir, statErr)
		}
	}
}

// TestCleanupUnusedCoreKeepsReferencedVersion 验证被实例引用的版本不回收。
func TestCleanupUnusedCoreKeepsReferencedVersion(t *testing.T) {
	s := testService(t)
	item, err := s.CreateInstance(testConfig("引用中"))
	if err != nil {
		t.Fatal(err)
	}
	writeManagedVersion(t, s.root, item.MCVersion, false)
	updated := item
	updated.VersionID = item.MCVersion
	updated.InstallationState = "installed"
	if err := s.saveLocked(updated); err != nil {
		t.Fatal(err)
	}

	report, err := s.CleanupUnusedCore()
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Removed) != 0 {
		t.Fatalf("被引用的版本不应回收，实际 %+v", report)
	}
	if _, statErr := os.Stat(filepath.Join(s.root, "versions", item.MCVersion)); statErr != nil {
		t.Fatalf("被引用的版本被误删: %v", statErr)
	}
}

// TestCleanupUnusedCoreHonoursTrashReferences 验证回收站里的实例仍构成引用。
//
// 删实例只是移入回收站，用户随时可能还原；此时回收它依赖的版本会让「还原」
// 变成一次必然失败的启动。
func TestCleanupUnusedCoreHonoursTrashReferences(t *testing.T) {
	s := testService(t)
	item, err := s.CreateInstance(testConfig("回收站引用"))
	if err != nil {
		t.Fatal(err)
	}
	updated := item
	updated.VersionID = item.MCVersion
	updated.InstallationState = "installed"
	if err := s.saveLocked(updated); err != nil {
		t.Fatal(err)
	}
	writeManagedVersion(t, s.root, item.MCVersion, false)

	if _, err := s.DeleteInstance(IDRequest{InstanceID: item.ID}); err != nil {
		t.Fatal(err)
	}
	// 删除实例本身不回收核心。
	if _, statErr := os.Stat(filepath.Join(s.root, "versions", item.MCVersion)); statErr != nil {
		t.Fatalf("删除实例不应直接删除核心版本: %v", statErr)
	}

	report, err := s.CleanupUnusedCore()
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Removed) != 0 {
		t.Fatalf("回收站引用不应被回收，实际 %+v", report)
	}

	// 永久删除后自动回收。
	trash, err := s.ListInstanceTrash()
	if err != nil || len(trash.Items) != 1 {
		t.Fatalf("回收站条目异常: %+v %v", trash, err)
	}
	if _, err := s.DeleteInstancePermanently(TrashIDRequest{TrashID: trash.Items[0].TrashID}); err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(filepath.Join(s.root, "versions", item.MCVersion)); !os.IsNotExist(statErr) {
		t.Fatalf("永久删除后应回收版本，实际 stat: %v", statErr)
	}
}

// TestCleanupUnusedCoreSkipsUnmanagedVersion 验证没有清单的目录永不自动删除。
func TestCleanupUnusedCoreSkipsUnmanagedVersion(t *testing.T) {
	s := testService(t)
	// 用户手动放入的版本目录：只有 JSON，没有启动器清单。
	fixtureWrite(t, filepath.Join(s.root, "versions", "manual", "manual.json"), []byte(`{"id":"manual"}`))

	report, err := s.CleanupUnusedCore()
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Removed) != 0 {
		t.Fatalf("无清单目录不应回收，实际 %+v", report)
	}
	if len(report.Skipped) != 1 || report.Skipped[0].Reason != reclaimSkipUnmanaged {
		t.Fatalf("应记录 unmanaged 跳过，实际 %+v", report.Skipped)
	}
	if _, statErr := os.Stat(filepath.Join(s.root, "versions", "manual")); statErr != nil {
		t.Fatalf("手动目录被误删: %v", statErr)
	}
}

// TestCleanupUnusedCoreSkipsRunningVersion 验证运行中的版本不回收。
func TestCleanupUnusedCoreSkipsRunningVersion(t *testing.T) {
	s := testService(t)
	writeManagedVersion(t, s.root, "1.21.4", false)
	writeLease(t, s.root, "1.21.4", 4242, 777000)

	report, err := s.CleanupUnusedCore()
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Removed) != 0 {
		t.Fatalf("运行中的版本不应回收，实际 %+v", report)
	}
	if len(report.Skipped) != 1 || report.Skipped[0].Reason != reclaimSkipInUse {
		t.Fatalf("应记录 in_use 跳过，实际 %+v", report.Skipped)
	}
}

// TestCleanupUnusedCoreKeepsParentOfLoaderVersion 验证加载器清单对原版目录的引用。
//
// 加载器实例的清单里包含原版 jar 与父版本 JSON。只看 VersionID 会把原版目录
// 判成无人引用——删掉后所有加载器实例都无法启动。
func TestCleanupUnusedCoreKeepsParentOfLoaderVersion(t *testing.T) {
	s := testService(t)
	item, err := s.CreateInstance(testConfig("加载器引用"))
	if err != nil {
		t.Fatal(err)
	}
	loaderID := "fabric-loader-0.16.14-" + item.MCVersion
	parentJSON := "versions/" + item.MCVersion + "/" + item.MCVersion + ".json"
	fixtureWrite(t, filepath.Join(s.root, filepath.FromSlash(parentJSON)), []byte(`{"id":"`+item.MCVersion+`"}`))
	writeManagedVersion(t, s.root, loaderID, true, parentJSON)

	// 模拟加载器实例：VersionID 指向加载器目录，清单引用原版目录。
	manifest, found, err := core.ReadManifest(s.root, loaderID)
	if err != nil || !found {
		t.Fatalf("读取清单失败: %v", err)
	}
	parentFile, err := core.NewManifestFile(s.root, parentJSON, core.GroupVersionJSON)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Files = append(manifest.Files, parentFile)
	if err := core.WriteManifest(s.root, manifest); err != nil {
		t.Fatal(err)
	}
	updated := item
	updated.Loader = "fabric"
	updated.LoaderVersion = "0.16.14"
	updated.VersionID = loaderID
	updated.InstallationState = "installed"
	if err := s.saveLocked(updated); err != nil {
		t.Fatal(err)
	}
	// 原版目录也必须带清单，否则会被当作 unmanaged 而掩盖真实判定。
	writeManagedVersion(t, s.root, item.MCVersion, false)

	report, err := s.CleanupUnusedCore()
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Removed) != 0 {
		t.Fatalf("加载器与其父版本都应保留，实际 %+v", report)
	}
	for _, dir := range []string{"versions/" + item.MCVersion, "loaders/" + loaderID} {
		if _, statErr := os.Stat(filepath.Join(s.root, filepath.FromSlash(dir))); statErr != nil {
			t.Fatalf("%s 被误删: %v", dir, statErr)
		}
	}
}

// TestLoaderVersionUsesLoadersRoot 验证写入时原版与加载器分别落位。
func TestLoaderVersionUsesLoadersRoot(t *testing.T) {
	cases := []struct {
		kind loader.Kind
		want string
	}{
		{kind: loader.KindVanilla, want: "versions/1.21.4"},
		{kind: loader.KindFabric, want: "loaders/1.21.4"},
		{kind: loader.KindForge, want: "loaders/1.21.4"},
		{kind: loader.KindNeoForge, want: "loaders/1.21.4"},
		{kind: loader.KindQuilt, want: "loaders/1.21.4"},
	}
	for _, testCase := range cases {
		got, err := coreVersionDirRel(testCase.kind, "1.21.4")
		if err != nil {
			t.Fatalf("%s: %v", testCase.kind, err)
		}
		if got != testCase.want {
			t.Fatalf("%s 版本目录 = %q，期望 %q", testCase.kind, got, testCase.want)
		}
	}
}
