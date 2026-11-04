package instance

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"example.com/ninevoxel/launcher/internal/core"
	"example.com/ninevoxel/launcher/internal/download"
	"example.com/ninevoxel/launcher/internal/version"
)

// 本文件覆盖修复流程的编排：在不联网的前提下验证「挑出哪些条目」
// 与「结果如何判定」。真实下载由 download 包的测试覆盖。

// fakePlanRunner 把计划里的每个条目写成固定内容，并记录收到的计划。
func fakePlanRunner(t *testing.T, root, content string) (func(context.Context, string, download.TransferPlan, float64, float64) error, *[]download.TransferPlan) {
	t.Helper()
	calls := make([]download.TransferPlan, 0, 2)
	run := func(_ context.Context, _ string, plan download.TransferPlan, _ float64, _ float64) error {
		calls = append(calls, plan)
		for _, item := range plan.Items {
			absolute := filepath.Join(root, filepath.FromSlash(item.TargetRelPath))
			if err := os.MkdirAll(filepath.Dir(absolute), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(absolute, []byte(content), 0o644); err != nil {
				return err
			}
		}
		return nil
	}
	return run, &calls
}

// stubPrepareForRepair 把版本安装信息替换为给定计划，避免访问线上元数据。
func stubPrepareForRepair(t *testing.T, plan download.TransferPlan) {
	t.Helper()
	original := prepareForRepair
	prepareForRepair = func(string) (version.Installation, error) {
		return version.Installation{Plan: plan}, nil
	}
	t.Cleanup(func() { prepareForRepair = original })
}

// planFor 构造只含指定目标路径的下载计划。
func planFor(versionID, targetRel string) download.TransferPlan {
	return download.TransferPlan{
		VersionID: versionID,
		PlanHash:  "repair-fixture",
		Items: []download.PlanItem{{
			ArtifactID:    "fixture",
			TaskType:      download.TaskTypeLibrary,
			SourceURI:     "https://example.invalid/x.jar",
			TargetRelPath: targetRel,
			TargetKind:    download.TargetKindShared,
		}},
	}
}

// TestRepairRestoresMissingLibrary 验证缺失的依赖库被恢复，且只下载该条目。
//
// 这是修复的主要场景：核心文件被杀毒软件误删或磁盘异常。此时不该要求
// 用户重装整个版本。
func TestRepairRestoresMissingLibrary(t *testing.T) {
	s, item, libraryRel := setupRepairFixture(t)
	libraryAbs := filepath.Join(s.root, filepath.FromSlash(libraryRel))
	if err := os.Remove(libraryAbs); err != nil {
		t.Fatal(err)
	}

	stubPrepareForRepair(t, planFor(item.MCVersion, libraryRel))
	run, calls := fakePlanRunner(t, s.root, "library bytes")

	report, err := s.repairWithRunner(t.Context(), item.ID, nil, run)
	if err != nil {
		t.Fatalf("修复失败: %v", err)
	}
	if report.NeedsReinstall {
		t.Fatalf("依赖库缺失应可修复，实际 %+v", report)
	}
	if _, statErr := os.Stat(libraryAbs); statErr != nil {
		t.Fatalf("文件未恢复: %v", statErr)
	}
	if report.Repaired < 1 {
		t.Fatalf("应报告已修复的文件数，实际 %+v", report)
	}

	// 只应请求有问题的条目：完整文件重复下载没有收益。
	if len(*calls) != 1 {
		t.Fatalf("下载计划数期望 1，实际 %d", len(*calls))
	}
	if len((*calls)[0].Items) != 1 || (*calls)[0].Items[0].TargetRelPath != libraryRel {
		t.Fatalf("只应下载缺失条目，实际 %+v", (*calls)[0].Items)
	}
}

// TestRepairSkipsHealthyFiles 验证完整文件不进入下载计划。
func TestRepairSkipsHealthyFiles(t *testing.T) {
	s, item, libraryRel := setupRepairFixture(t)
	stubPrepareForRepair(t, planFor(item.MCVersion, libraryRel))
	run, calls := fakePlanRunner(t, s.root, "ignored")

	report, err := s.repairWithRunner(t.Context(), item.ID, nil, run)
	if err != nil {
		t.Fatalf("修复失败: %v", err)
	}
	if len(*calls) != 0 {
		t.Fatalf("完整安装不应触发任何下载，实际 %d 个计划", len(*calls))
	}
	if report.Repaired != 0 || report.NeedsReinstall {
		t.Fatalf("完整安装不应有修复动作，实际 %+v", report)
	}
}

// TestRepairBlocksProcessorDamage 验证加载器产物损坏时不下下载、直接要求重装。
//
// 这类产物由加载器安装器生成，没有独立下载源。放行下载会先白下一批文件，
// 最后仍然要求用户重装。
func TestRepairBlocksProcessorDamage(t *testing.T) {
	s, item, _ := setupRepairFixture(t)

	manifest, found, err := core.ReadManifest(s.root, item.MCVersion)
	if err != nil || !found {
		t.Fatalf("读取清单失败: %v", err)
	}
	// 追加一个「加载器产物」条目并真正落盘，然后删除它。
	processorRel := "versions/" + item.MCVersion + "/client.jar"
	processorAbs := filepath.Join(s.root, filepath.FromSlash(processorRel))
	if mkErr := os.MkdirAll(filepath.Dir(processorAbs), 0o755); mkErr != nil {
		t.Fatal(mkErr)
	}
	if writeErr := os.WriteFile(processorAbs, []byte("processor output"), 0o644); writeErr != nil {
		t.Fatal(writeErr)
	}
	file, fileErr := core.NewManifestFile(s.root, processorRel, core.GroupProcessor)
	if fileErr != nil {
		t.Fatal(fileErr)
	}
	manifest.Files = append(manifest.Files, file)
	if writeErr := core.WriteManifest(s.root, manifest); writeErr != nil {
		t.Fatal(writeErr)
	}
	if removeErr := os.Remove(filepath.Join(s.root, filepath.FromSlash(processorRel))); removeErr != nil {
		t.Fatal(removeErr)
	}

	run, calls := fakePlanRunner(t, s.root, "ignored")
	report, repairErr := s.repairWithRunner(t.Context(), item.ID, nil, run)
	if repairErr != nil {
		t.Fatalf("修复不应返回错误: %v", repairErr)
	}
	if !report.NeedsReinstall {
		t.Fatalf("加载器产物损坏应要求重装，实际 %+v", report)
	}
	if len(*calls) != 0 {
		t.Fatalf("阻断时不应发起下载，实际 %d 个计划", len(*calls))
	}
}
