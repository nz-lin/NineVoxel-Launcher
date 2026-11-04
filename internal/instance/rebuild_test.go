package instance

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"example.com/ninevoxel/launcher/internal/core"
)

// markInstalled 把实例标记为已安装并写入版本号。
func markInstalled(t *testing.T, s *InstanceService, item Instance, versionID string) Instance {
	t.Helper()
	item.VersionID = versionID
	item.InstallationState = "installed"
	if err := s.saveLocked(item); err != nil {
		t.Fatalf("保存实例失败: %v", err)
	}
	return item
}

// TestRebuildCoreCompactsReferencedFiles 验证重建后只保留清单登记的文件。
//
// 重建的价值不只是「复制一遍」：旧版本残留、失败的暂存目录都不在清单里，
// 新核心不应继承它们。
func TestRebuildCoreCompactsReferencedFiles(t *testing.T) {
	s, item, libraryRel := setupRepairFixture(t)
	item = markInstalled(t, s, item, item.MCVersion)

	orphanLibrary := filepath.Join(s.root, "libraries", "com", "orphan", "1.0", "orphan-1.0.jar")
	if err := os.MkdirAll(filepath.Dir(orphanLibrary), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(orphanLibrary, []byte("orphan"), 0o644); err != nil {
		t.Fatal(err)
	}
	orphanVersion := filepath.Join(s.root, "versions", "old-version")
	if err := os.MkdirAll(orphanVersion, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(orphanVersion, "old.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := s.rebuildCore(context.Background(), nil)
	if err != nil {
		t.Fatalf("重建失败: %v", err)
	}
	if result.Versions != 1 {
		t.Fatalf("参与重建的版本数 = %d", result.Versions)
	}

	// 清单登记的文件必须还在。
	if _, statErr := os.Stat(filepath.Join(s.root, filepath.FromSlash(libraryRel))); statErr != nil {
		t.Fatalf("依赖库未保留: %v", statErr)
	}
	// 未登记的文件必须被排除。
	if _, statErr := os.Stat(orphanLibrary); !os.IsNotExist(statErr) {
		t.Fatalf("未登记的依赖库应被排除: %v", statErr)
	}
	if _, statErr := os.Stat(orphanVersion); !os.IsNotExist(statErr) {
		t.Fatalf("未登记的版本目录应被排除: %v", statErr)
	}
	// 新核心的清单仍然可读且完整。
	manifest, found, readErr := core.ReadManifest(s.root, item.MCVersion)
	if readErr != nil || !found {
		t.Fatalf("重建后清单不可用: %v", readErr)
	}
	report, verifyErr := core.VerifyManifest(s.root, manifest, core.VerifyFull)
	if verifyErr != nil || !report.Complete() {
		t.Fatalf("重建后核心不完整: %v %+v", verifyErr, report)
	}
}

// TestRebuildCoreRequiresInstalledManifest 验证缺少清单时拒绝重建。
//
// 没有清单就不知道「这个版本应该包含什么」，复制出来的核心无法自证完整。
func TestRebuildCoreRequiresInstalledManifest(t *testing.T) {
	s := testService(t)
	redirectLaunchDataRoot(t, s)
	item, err := s.CreateInstance(testConfig("缺少清单"))
	if err != nil {
		t.Fatal(err)
	}
	writeVersionJSONAt(t, s.root, item.MCVersion, `{"id":"`+item.MCVersion+`"}`)
	markInstalled(t, s, item, item.MCVersion)

	_, rebuildErr := s.rebuildCore(context.Background(), nil)
	if rebuildErr == nil {
		t.Fatal("缺少清单时应拒绝重建")
	}
	if !strings.Contains(rebuildErr.Error(), "重新安装") {
		t.Fatalf("错误文案应引导重新安装，实际 %v", rebuildErr)
	}
}

// TestRebuildCoreSharesVersionAcrossInstances 验证同一版本只重建一次。
func TestRebuildCoreSharesVersionAcrossInstances(t *testing.T) {
	s, first, _ := setupRepairFixture(t)
	markInstalled(t, s, first, first.MCVersion)

	second, err := s.CreateInstance(testConfig("共享版本"))
	if err != nil {
		t.Fatal(err)
	}
	markInstalled(t, s, second, first.MCVersion)

	result, rebuildErr := s.rebuildCore(context.Background(), nil)
	if rebuildErr != nil {
		t.Fatalf("重建失败: %v", rebuildErr)
	}
	if result.Versions != 1 {
		t.Fatalf("同一版本应只重建一次，实际 %d", result.Versions)
	}
}

// TestRebuildCoreReportsMonotonicProgress 验证重建会持续上报且进度不回退。
//
// 进度条一旦回退，用户会以为任务重新开始了；同样，全程没有回调就等于又回到
// 「不动的重建中」。这里同时约束这两点。
func TestRebuildCoreReportsMonotonicProgress(t *testing.T) {
	s, item, _ := setupRepairFixture(t)
	markInstalled(t, s, item, item.MCVersion)

	var (
		phases []string
		ratios []float64
	)
	_, err := s.rebuildCore(context.Background(), func(phase string, ratio float64) {
		phases = append(phases, phase)
		ratios = append(ratios, ratio)
	})
	if err != nil {
		t.Fatalf("重建失败: %v", err)
	}
	if len(ratios) < 3 {
		t.Fatalf("应上报多个阶段，实际只有 %d 次", len(ratios))
	}
	for index := 1; index < len(ratios); index++ {
		if ratios[index] < ratios[index-1] {
			t.Fatalf("进度回退：第 %d 次 %v < 第 %d 次 %v（阶段 %q）",
				index, ratios[index], index-1, ratios[index-1], phases[index])
		}
	}
	if len(phases) != len(ratios) {
		t.Fatalf("阶段文案与比例数量不一致：%d vs %d", len(phases), len(ratios))
	}
}

// TestEmitRebuildProgressWithoutBinding 验证未绑定应用时发射不会 panic。
//
// 单元测试与命令行场景下没有 Wails 应用实例；进度上报失败只能影响观感，
// 绝不能让重建本身因此崩溃。
func TestEmitRebuildProgressWithoutBinding(t *testing.T) {
	emitRebuildProgress("正在构建核心资源", 0.5)
}

// ===== 暂存校验 =====

// TestVerifyRebuiltCoreDetectsTamper 验证暂存核心被篡改时拒绝切换。
func TestVerifyRebuiltCoreDetectsTamper(t *testing.T) {
	root := t.TempDir()
	staging := t.TempDir()

	relative := "libraries/com/example/lib/1.0/lib-1.0.jar"
	writeLibrary(t, root, "com/example/lib/1.0/lib-1.0.jar", "library bytes")
	file, err := core.NewManifestFile(root, relative, core.GroupLibrary)
	if err != nil {
		t.Fatal(err)
	}
	manifest := core.Manifest{
		SchemaVersion: 2,
		Identity:      "vanilla:26.3:",
		VersionID:     "26.3",
		VersionDir:    "versions/26.3",
		Files:         []core.ManifestFile{file},
	}
	if writeErr := core.WriteManifest(staging, manifest); writeErr != nil {
		t.Fatal(writeErr)
	}
	if copyErr := copyCoreFile(root, staging, relative); copyErr != nil {
		t.Fatal(copyErr)
	}

	target := coreRebuildTarget{versionID: "26.3"}
	target.manifest = manifest
	if verifyErr := verifyRebuiltCore(staging, []coreRebuildTarget{target}); verifyErr != nil {
		t.Fatalf("完整暂存核心不应报错: %v", verifyErr)
	}

	// 篡改内容但保持大小不变：只有哈希能发现。
	tampered := filepath.Join(staging, filepath.FromSlash(relative))
	if writeErr := os.WriteFile(tampered, []byte("library bytez"), 0o644); writeErr != nil {
		t.Fatal(writeErr)
	}
	if verifyErr := verifyRebuiltCore(staging, []coreRebuildTarget{target}); verifyErr == nil {
		t.Fatal("篡改后的暂存核心应被拒绝")
	}
}

// TestCopyCoreFileRejectsEscapingPath 验证复制不会越出核心根。
func TestCopyCoreFileRejectsEscapingPath(t *testing.T) {
	root := t.TempDir()
	staging := t.TempDir()
	if err := copyCoreFile(root, staging, "../outside.jar"); err == nil {
		t.Fatal("越界路径应被拒绝")
	}
}
