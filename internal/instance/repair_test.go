package instance

import (
	"os"
	"path/filepath"
	"testing"

	"example.com/ninevoxel/launcher/internal/artifact"
	"example.com/ninevoxel/launcher/internal/core"
	"example.com/ninevoxel/launcher/internal/loader"
)

// setupRepairFixture 构造一个「已完整安装」的版本，返回实例与关键路径。
func setupRepairFixture(t *testing.T) (*InstanceService, Instance, string) {
	t.Helper()
	s := testService(t)

	// ResolveLaunchConfig 走包级数据根，必须与测试服务指向同一目录。
	restore := launchDataRoot
	root := s.root
	launchDataRoot = func() (string, *artifact.Error) { return root, nil }
	t.Cleanup(func() { launchDataRoot = restore })

	item, err := s.CreateInstance(testConfig("修复夹具"))
	if err != nil {
		t.Fatal(err)
	}

	versionID := item.MCVersion
	writeVersionJSONAt(t, s.root, versionID, `{"id":"`+versionID+`"}`)
	libraryRel := writeLibrary(t, s.root, "com/example/lib/1.0/lib-1.0.jar", "library bytes")

	if err := s.writeCoreManifest(loader.KindVanilla, versionID, "", versionID,
		installArtifacts{Downloaded: []string{libraryRel}}); err != nil {
		t.Fatalf("写入清单失败: %v", err)
	}
	item.VersionID = versionID
	item.InstallationState = "installed"
	if err := s.saveLocked(item); err != nil {
		t.Fatal(err)
	}
	return s, item, libraryRel
}

// TestVerifyInstanceReportsComplete 验证完整安装通过校验。
func TestVerifyInstanceReportsComplete(t *testing.T) {
	s, item, _ := setupRepairFixture(t)

	result, err := s.VerifyInstance(VerifyInstanceRequest{InstanceID: item.ID})
	if err != nil {
		t.Fatalf("校验失败: %v", err)
	}
	if !result.HasManifest || !result.Complete {
		t.Fatalf("期望完整，实际 %+v", result)
	}
	if result.ProblemCount != 0 {
		t.Fatalf("问题数量 = %d", result.ProblemCount)
	}
}

// TestVerifyInstanceDetectsMissingFile 验证缺失文件被检出。
func TestVerifyInstanceDetectsMissingFile(t *testing.T) {
	s, item, libraryRel := setupRepairFixture(t)

	if err := os.Remove(filepath.Join(s.root, filepath.FromSlash(libraryRel))); err != nil {
		t.Fatal(err)
	}

	result, err := s.VerifyInstance(VerifyInstanceRequest{InstanceID: item.ID})
	if err != nil {
		t.Fatalf("校验失败: %v", err)
	}
	if result.Complete || result.ProblemCount != 1 {
		t.Fatalf("期望检出 1 个问题，实际 %+v", result)
	}
}

// TestVerifyInstanceWithoutManifest 验证没有清单时如实报告。
func TestVerifyInstanceWithoutManifest(t *testing.T) {
	s := testService(t)
	redirectLaunchDataRoot(t, s)
	item, err := s.CreateInstance(testConfig("无清单"))
	if err != nil {
		t.Fatal(err)
	}
	writeVersionJSONAt(t, s.root, item.MCVersion, `{"id":"`+item.MCVersion+`"}`)

	result, err := s.VerifyInstance(VerifyInstanceRequest{InstanceID: item.ID})
	if err != nil {
		t.Fatalf("校验失败: %v", err)
	}
	if result.HasManifest {
		t.Fatal("没有清单时应如实报告")
	}
}

// ===== 修复判定 =====

// TestRepairRequiresReinstallWithoutManifest 验证无清单时要求重装。
//
// 没有清单就不知道「该有哪些文件」，盲目重下一份全量并不可靠
// （例如加载器产物根本无法通过下载获得）。
func TestRepairRequiresReinstallWithoutManifest(t *testing.T) {
	s := testService(t)
	redirectLaunchDataRoot(t, s)
	item, err := s.CreateInstance(testConfig("无清单修复"))
	if err != nil {
		t.Fatal(err)
	}
	writeVersionJSONAt(t, s.root, item.MCVersion, `{"id":"`+item.MCVersion+`"}`)

	report, repairErr := s.repairInstance(t.Context(), item.ID, nil)
	if repairErr != nil {
		t.Fatalf("修复不应返回错误: %v", repairErr)
	}
	if !report.NeedsReinstall {
		t.Fatalf("无清单时应要求重装，实际 %+v", report)
	}
	if report.Reason == "" {
		t.Fatal("要求重装时应给出原因")
	}
}

// TestRepairSkipsCompleteInstall 验证完整安装不做任何修复。
func TestRepairSkipsCompleteInstall(t *testing.T) {
	s, item, _ := setupRepairFixture(t)

	report, err := s.repairInstance(t.Context(), item.ID, nil)
	if err != nil {
		t.Fatalf("修复失败: %v", err)
	}
	if report.NeedsReinstall || report.Repaired != 0 {
		t.Fatalf("完整安装不应触发修复，实际 %+v", report)
	}
	if report.Checked == 0 {
		t.Fatal("应报告已校验的文件数")
	}
}

// TestUnrecoverableReasonBlocksProcessorOutputs 验证加载器产物损坏时要求重装。
//
// processor 产物由加载器安装器生成，没有独立下载源；如果放行去下载，
// 会先白白下载一批文件，最后仍然要求用户重装。
func TestUnrecoverableReasonBlocksProcessorOutputs(t *testing.T) {
	manifest := core.Manifest{
		Files: []core.ManifestFile{
			{Path: "libraries/a.jar", Group: core.GroupLibrary},
			{Path: "versions/v/client.jar", Group: core.GroupProcessor},
		},
	}
	reason, blocked := unrecoverableReason(manifest, []string{"versions/v/client.jar"})
	if !blocked || reason == "" {
		t.Fatalf("加载器产物损坏应阻断修复，实际 blocked=%v reason=%q", blocked, reason)
	}

	// 依赖库损坏不应阻断。
	if _, blocked := unrecoverableReason(manifest, []string{"libraries/a.jar"}); blocked {
		t.Fatal("依赖库损坏不应阻断修复")
	}
}

// TestUnrecoverableReasonBlocksVersionJSON 验证版本元数据损坏时要求重装。
func TestUnrecoverableReasonBlocksVersionJSON(t *testing.T) {
	manifest := core.Manifest{
		Files: []core.ManifestFile{
			{Path: "versions/v/v.json", Group: core.GroupVersionJSON},
		},
	}
	if _, blocked := unrecoverableReason(manifest, []string{"versions/v/v.json"}); !blocked {
		t.Fatal("版本元数据损坏应阻断修复")
	}
}

// TestNativesAreInstanceScoped 验证 natives 目录属于实例而非核心。
//
// natives 自清单 schema v2 起不进核心清单：它随实例删除一起回收，
// 也不参与核心重建。这条约束一旦被破坏，删除实例会留下永远不清的垃圾目录。
func TestNativesAreInstanceScoped(t *testing.T) {
	s := testService(t)
	item, err := s.CreateInstance(testConfig("natives 归属"))
	if err != nil {
		t.Fatal(err)
	}
	directory, err := s.nativesDir(item.ID)
	if err != nil {
		t.Fatal(err)
	}
	expected := filepath.Join(s.root, "instances", item.ID, ".minecraft", "natives")
	if filepath.Clean(directory) != filepath.Clean(expected) {
		t.Fatalf("natives 目录 = %q，期望 %q", directory, expected)
	}
	if s.nativesReady(item.ID) {
		t.Fatal("尚未解压时不应视为就绪")
	}
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "a.dll"), []byte("dll"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !s.nativesReady(item.ID) {
		t.Fatal("非空目录应视为就绪")
	}
}

// redirectLaunchDataRoot 把包级数据根指向测试服务使用的目录。
//
// ResolveLaunchConfig 读的是包级变量而不是服务字段，不重定向会指向
// 真实用户目录。
func redirectLaunchDataRoot(t *testing.T, s *InstanceService) {
	t.Helper()
	restore := launchDataRoot
	root := s.root
	launchDataRoot = func() (string, *artifact.Error) { return root, nil }
	t.Cleanup(func() { launchDataRoot = restore })
}
