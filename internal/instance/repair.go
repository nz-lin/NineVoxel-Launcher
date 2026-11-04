package instance

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"example.com/ninevoxel/launcher/internal/core"
	"example.com/ninevoxel/launcher/internal/download"
	"example.com/ninevoxel/launcher/internal/loader"
	"example.com/ninevoxel/launcher/internal/version"
)

// 本文件实现核心资源修复：按清单比对实际文件，只补齐缺失或损坏的部分。
//
// 与「重新安装」的区别：重装会重新下载全部产物并重跑加载器安装器（几十秒
// 到几分钟）；修复只处理有问题的文件，通常只有几个 MB。
//
// 修复的边界（哪些能修、哪些只能重装）取决于产物来源：
//
//	资源对象  → 按资源索引重新下载（哈希寻址，必然可恢复）
//	依赖库    → 按版本 JSON 的下载元数据重新下载
//	原版 JAR  → 同上
//	natives   → 从对应的 native 压缩包重新解压到实例目录
//	版本 JSON → 按版本清单重新获取
//	安装器产物 → **只能重装**：它由加载器的 processor 生成，没有独立下载源
//
// natives 从清单 schema v2 起属于实例目录，不再登记在核心清单里，因此它的
// 缺失不会被清单校验发现：修复流程单独检查实例 natives 目录并整体重新解压。

// RepairReport 是一次修复的结果。
type RepairReport struct {
	// VersionID 是被修复的版本。
	VersionID string `json:"versionId"`
	// Checked 是校验的文件数量。
	Checked int `json:"checked"`
	// Repaired 是本次成功恢复的文件数量。
	Repaired int `json:"repaired"`
	// Failed 是无法恢复的文件数量。
	Failed int `json:"failed"`
	// NeedsReinstall 表示问题超出修复能力，必须重装。
	NeedsReinstall bool `json:"needsReinstall"`
	// Reason 是 NeedsReinstall 时的可读原因。
	Reason string `json:"reason,omitempty"`
}

// repairInstance 修复某个实例依赖的核心版本。
//
// 参数 instanceID 为实例标识；progress 可选，用于上报阶段。
func (s *InstanceService) repairInstance(ctx context.Context, instanceID string, progress func(string, float64)) (RepairReport, error) {
	return s.repairWithRunner(ctx, instanceID, progress, s.runPlan)
}

// repairWithRunner 是可测试的修复实现。
//
// 参数 runPlan 为下载执行器：生产用 s.runPlan，测试注入假实现，
// 这样可以在不联网的前提下验证「挑出哪些条目」与「结果如何判定」。
func (s *InstanceService) repairWithRunner(ctx context.Context, instanceID string, progress func(string, float64), runPlan func(context.Context, string, download.TransferPlan, float64, float64) error) (RepairReport, error) {
	config, err := ResolveLaunchConfig(instanceID)
	if err != nil {
		return RepairReport{}, err
	}
	versionID := strings.TrimSpace(config.MCVersion)
	if versionID == "" {
		return RepairReport{}, errors.New("实例未记录启动版本，无法修复")
	}
	report := RepairReport{VersionID: versionID}

	// 加锁：修复与安装、删除互斥，否则可能边删边补。
	unlock := s.coreInstallLock("repair:" + versionID)
	defer unlock()

	manifest, found, readErr := core.ReadManifest(s.root, versionID)
	if readErr != nil || !found {
		// 没有清单就无法知道该有什么：按「需要重装」处理，
		// 而不是盲目重下一份全量。
		report.NeedsReinstall = true
		report.Reason = "该版本缺少安装清单，请重新安装"
		return report, nil
	}

	verify, verifyErr := core.VerifyManifest(s.root, manifest, core.VerifyFull)
	if verifyErr != nil {
		report.NeedsReinstall = true
		report.Reason = "安装清单损坏，请重新安装"
		return report, nil
	}
	report.Checked = verify.Checked

	nativesDeclared := s.versionDeclaresNatives(versionID)
	nativesReady := !nativesDeclared || s.nativesReady(instanceID)
	if verify.Complete() && nativesReady {
		return report, nil
	}

	// 判定可修复性：安装器产物与版本 JSON 没有独立下载源。
	if !verify.Complete() {
		if reason, blocked := unrecoverableReason(manifest, verify.ProblemFiles()); blocked {
			report.NeedsReinstall = true
			report.Reason = reason
			return report, nil
		}
	}

	if progress != nil {
		progress("正在重建下载计划", 0.05)
	}
	// 下载计划按**原版版本**构建：加载器版本的依赖库没有独立的官方安装计划，
	// 但它们的原生库与资源都来自原版版本。加载器专有库缺失时会留在最终校验里，
	// 如实报告需要重装。
	prepared, prepErr := prepareForRepair(repairBaseVersion(manifest, versionID))
	if prepErr != nil {
		return report, fmt.Errorf("无法解析该版本的下载信息：%w", prepErr)
	}

	problems := make(map[string]struct{}, len(verify.ProblemFiles()))
	for _, path := range verify.ProblemFiles() {
		problems[path] = struct{}{}
	}

	// 只下载有问题的条目：其余条目已经完整，重复下载没有收益。
	pending := make([]download.PlanItem, 0, len(problems))
	for _, item := range prepared.Plan.Items {
		if _, broken := problems[item.TargetRelPath]; broken {
			pending = append(pending, item)
		}
	}
	if len(pending) > 0 {
		if progress != nil {
			progress("正在恢复文件", 0.2)
		}
		plan := download.TransferPlan{
			VersionID: versionID,
			PlanHash:  prepared.Plan.PlanHash + "-repair",
			Items:     pending,
		}
		if runErr := runPlan(ctx, instanceID, plan, 0.2, 0.5); runErr != nil {
			return report, runErr
		}
		report.Repaired += len(pending)
	}

	// 资源对象：交给资源展开流程，它会跳过仍然有效的对象，只补缺失与损坏。
	if manifest.Assets != nil && anyPathIn(problems, assetsPrefix) {
		if progress != nil {
			progress("正在恢复游戏资源", 0.7)
		}
		index, indexErr := download.LoadAssetIndex(s.root, manifest.Assets.IndexID)
		if indexErr != nil {
			return report, fmt.Errorf("无法读取资源索引：%w", indexErr)
		}
		expansion, expandErr := download.ExpandAssetObjects(s.root, index, download.AssetExpandOptions{VerifyExisting: true})
		if expandErr != nil {
			return report, fmt.Errorf("无法展开资源清单：%w", expandErr)
		}
		if len(expansion.Items) > 0 {
			assets := download.TransferPlan{
				VersionID: versionID,
				PlanHash:  prepared.Plan.PlanHash + "-repair-assets",
				Items:     expansion.Items,
			}
			if runErr := runPlan(ctx, instanceID, assets, 0.7, 0.2); runErr != nil {
				return report, runErr
			}
			report.Repaired += len(expansion.Items)
		}
	}

	// natives：解压产物无法单独下载，从对应压缩包整体重新解压到实例目录。
	// 这一步很便宜（本地 IO），不需要先判断哪个文件缺失。
	if nativesDeclared && !nativesReady && len(prepared.Files) > 0 {
		if progress != nil {
			progress("正在重新解压原生产物", 0.9)
		}
		nativesDir, pathErr := s.nativesDir(instanceID)
		if pathErr != nil {
			return report, pathErr
		}
		for _, file := range prepared.Files {
			if !file.Extract {
				continue
			}
			source, sourceErr := managedPath(s.root, file.Source)
			if sourceErr != nil {
				return report, sourceErr
			}
			if extractErr := extractNatives(ctx, source, nativesDir, file.Exclude); extractErr != nil {
				return report, extractErr
			}
			report.Repaired++
		}
	}

	if verify.Complete() {
		// 只补了 natives，核心文件没有变化，不需要重写清单。
		if progress != nil {
			progress("修复完成", 1)
		}
		return report, nil
	}

	// 重新校验：修复后仍不完整则如实报告，不谎报成功。
	finalVerify, finalErr := core.VerifyManifest(s.root, manifest, core.VerifyFull)
	if finalErr != nil {
		return report, finalErr
	}
	report.Failed = len(finalVerify.ProblemFiles())
	if report.Failed > 0 {
		report.NeedsReinstall = true
		report.Reason = "部分文件无法恢复，请重新安装"
		return report, nil
	}

	// 修复成功后重写清单：文件内容已变化，旧哈希不再有效。
	if writeErr := s.writeCoreManifest(
		loader.Kind(manifest.LoaderKind),
		manifest.MCVersion,
		manifest.LoaderVersion,
		versionID,
		artifactsFromManifest(manifest),
	); writeErr != nil {
		// 清单写失败不影响已修复的文件，但下次仍会判定为不完整。
		return report, writeErr
	}

	if progress != nil {
		progress("修复完成", 1)
	}
	return report, nil
}

// repairBaseVersion 返回构建下载计划时使用的原版版本。
//
// 加载器版本没有独立的官方安装计划，但它继承的原版版本有：原生库、客户端
// JAR 与资源都随原版安装。缺失的加载器专有库无法从这里恢复，会留在最终校验里。
func repairBaseVersion(manifest core.Manifest, versionID string) string {
	if strings.TrimSpace(manifest.MCVersion) != "" {
		return manifest.MCVersion
	}
	return versionID
}

// unrecoverableReason 判断问题文件是否超出修复能力。
//
// 返回原因与是否阻断。阻断项必须在下载前判定：否则会先白白下载一批文件，
// 最后仍然要求用户重装。
func unrecoverableReason(manifest core.Manifest, problems []string) (string, bool) {
	if len(problems) == 0 {
		return "", false
	}
	blocked := make(map[string]struct{}, 8)
	for _, file := range manifest.Files {
		if file.Group == core.GroupProcessor || file.Group == core.GroupVersionJSON {
			blocked[file.Path] = struct{}{}
		}
	}
	for _, path := range problems {
		if _, isBlocked := blocked[path]; isBlocked {
			return "加载器产物或版本元数据损坏，无法单独修复，请重新安装", true
		}
	}
	return "", false
}

// anyPathIn 判断问题集合里是否有以指定前缀开头的路径。
func anyPathIn(problems map[string]struct{}, prefix string) bool {
	for path := range problems {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

// assetsPrefix 返回资源对象目录前缀。
//
// 资源对象路径固定为 `assets/objects/<前两位>/<哈希>`，与索引标识无关，
// 因此不需要从清单里取。
const assetsPrefix = "assets/objects/"

// artifactsFromManifest 从旧清单还原安装产物，供重写清单使用。
//
// 修复不改变「有哪些产物」，只是让它们重新变得完整，因此直接沿用旧清单的
// 分组与路径即可；大小与哈希由 writeCoreManifest 重新计算。
func artifactsFromManifest(manifest core.Manifest) installArtifacts {
	artifacts := installArtifacts{}
	for _, file := range manifest.Files {
		artifacts.Downloaded = append(artifacts.Downloaded, file.Path)
	}
	if manifest.Assets != nil {
		artifacts.AssetIndex = manifest.Assets.Index.Path
		for _, object := range manifest.Assets.Objects {
			artifacts.AssetObjects = append(artifacts.AssetObjects, object.Path)
		}
	}
	return artifacts
}

// prepareForRepair 提供版本安装信息；测试可替换以避免访问线上元数据。
var prepareForRepair = version.PrepareInstallation
