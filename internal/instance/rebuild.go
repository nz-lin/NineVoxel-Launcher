package instance

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"example.com/ninevoxel/launcher/internal/artifact"
	"example.com/ninevoxel/launcher/internal/core"
	"example.com/ninevoxel/launcher/internal/platform"
)

// 本文件实现「重建游戏核心」：按已安装清单重新生成一份干净的核心目录，
// 再经 core.Rebuild 的事务化切换替换旧核心。
//
// 与修复的区别：
//
//	修复  只补齐缺失或损坏的文件，保留目录里的其他内容；
//	重建  以清单为准生成新核心，未登记的文件（旧版本残留、失败的暂存目录）
//	      不会进入新核心。
//
// 重建开始前会对待重建版本做一次完整校验；发现损坏先走修复。修复无法恢复
// （加载器产物或版本元数据损坏）时直接中止，要求用户重新安装该实例——
// 与其复制一份明知不完整的数据，不如在切换前就说清楚。

// rebuildFreeSpaceMargin 是重建时在复制量之外额外保留的磁盘空间。
//
// 暂存核心与旧核心会同时存在，可用空间必须容纳一整份副本；额外留出的余量
// 用于索引、清单与文件系统块开销，避免刚好卡在临界值上。
const rebuildFreeSpaceMargin = 128 << 20

// RebuildCoreResult 是一次核心重建的结果。
type RebuildCoreResult struct {
	// Versions 是参与重建的版本数量。
	Versions int `json:"versions"`
	// Files 是复制到新核心的文件数量（不含清单与索引）。
	Files int `json:"files"`
	// Bytes 是复制文件的总字节数。
	Bytes int64 `json:"bytes"`
}

// coreRebuildTarget 是一个参与重建的版本及其引用实例。
type coreRebuildTarget struct {
	// instanceID 是该版本对应的实例，用于修复与错误提示。
	instanceID string
	// instanceName 是实例名，用于面向用户的错误信息。
	instanceName string
	// versionID 是版本目录名，也是清单的版本标识。
	versionID string
	// manifest 是重建依据的安装清单。
	manifest core.Manifest
}

// RebuildCoreForUser 重建共享游戏核心。
//
// 同步执行：重建包含完整校验与磁盘复制，耗时由核心体积决定。中间进度通过
// `core.rebuild.progress.v1` 事件上报，界面据此绘制进度条。
func (s *InstanceService) RebuildCoreForUser() (RebuildCoreResult, error) {
	s.mu.Lock()
	if err := s.openLocked(); err != nil {
		s.mu.Unlock()
		return RebuildCoreResult{}, err
	}
	s.mu.Unlock()

	return s.rebuildCore(context.Background(), emitRebuildProgress)
}

// rebuildCore 是重建的实现；progress 可为空。
func (s *InstanceService) rebuildCore(ctx context.Context, progress func(string, float64)) (RebuildCoreResult, error) {
	result := RebuildCoreResult{}
	if err := ctx.Err(); err != nil {
		return result, errors.New("重建已取消")
	}
	if progress == nil {
		progress = func(string, float64) {}
	}
	progress("正在准备重建", 0.01)

	targets, err := s.collectRebuildTargets(ctx, progress)
	if err != nil {
		return result, err
	}
	if len(targets) == 0 {
		return result, errors.New("没有可重建的已安装实例")
	}

	// 统计要复制的唯一文件。不同版本共享依赖库，按路径去重才是真实复制量。
	unique := make(map[string]struct{}, 256)
	for _, target := range targets {
		for _, file := range target.manifest.RequiredFiles() {
			unique[file.Path] = struct{}{}
		}
	}
	var totalBytes int64
	for _, target := range targets {
		for _, file := range target.manifest.RequiredFiles() {
			if _, counted := unique[file.Path]; !counted {
				continue
			}
			delete(unique, file.Path)
			totalBytes += file.Size
		}
	}
	result.Versions = len(targets)
	result.Files = len(rebuildUniquePaths(targets))
	result.Bytes = totalBytes

	if spaceErr := ensureRebuildSpace(s.root, totalBytes); spaceErr != nil {
		return result, spaceErr
	}

	err = core.Rebuild(core.RebuildOptions{
		Root: s.root,
		Populate: func(stagingRoot string) error {
			return s.populateRebuild(ctx, stagingRoot, targets, progress)
		},
		Verify: func(root string) error {
			return verifyRebuiltCore(root, targets)
		},
		Progress: progress,
	})
	if err != nil {
		return result, err
	}
	return result, nil
}

// collectRebuildTargets 读取全部已安装实例，校验并修复其版本，返回重建清单。
//
// 只处理 InstallationState 为 installed 的实例：未安装完成的实例没有可靠的
// 清单，把它们的半成品纳入重建只会扩大风险。
func (s *InstanceService) collectRebuildTargets(ctx context.Context, progress func(string, float64)) ([]coreRebuildTarget, error) {
	list, err := s.ListInstances()
	if err != nil {
		return nil, err
	}

	byVersion := make(map[string]int, len(list.Items))
	targets := make([]coreRebuildTarget, 0, len(list.Items))
	for _, item := range list.Items {
		if item.InstallationState == "installing" {
			return nil, fmt.Errorf("实例「%s」正在安装，请等待完成后再重建", item.Name)
		}
		if item.InstallationState != "installed" {
			continue
		}
		config, configErr := ResolveLaunchConfig(item.ID)
		if configErr != nil {
			return nil, fmt.Errorf("无法解析实例「%s」的版本：%w", item.Name, configErr)
		}
		versionID := config.MCVersion
		if versionID == "" {
			continue
		}
		if index, exists := byVersion[versionID]; exists {
			// 同一版本被多个实例引用时只重建一次；修复仍用第一个实例的标识。
			_ = index
			continue
		}
		byVersion[versionID] = len(targets)
		targets = append(targets, coreRebuildTarget{
			instanceID:   item.ID,
			instanceName: item.Name,
			versionID:    versionID,
		})
	}

	for index := range targets {
		if err := ctx.Err(); err != nil {
			return nil, errors.New("重建已取消")
		}
		target := &targets[index]
		manifest, prepErr := s.prepareRebuildTarget(ctx, target, index, len(targets), progress)
		if prepErr != nil {
			return nil, prepErr
		}
		target.manifest = manifest
	}
	return targets, nil
}

// prepareRebuildTarget 读取清单并保证该版本完整；不完整时先尝试修复。
func (s *InstanceService) prepareRebuildTarget(ctx context.Context, target *coreRebuildTarget, index, total int, progress func(string, float64)) (core.Manifest, error) {
	if progress != nil {
		progress(fmt.Sprintf("正在校验版本 %s（%d/%d）", target.versionID, index+1, total), 0.02)
	}

	manifest, found, readErr := core.ReadManifest(s.root, target.versionID)
	if readErr != nil {
		return core.Manifest{}, fmt.Errorf("实例「%s」的安装清单损坏：%w", target.instanceName, readErr)
	}
	if !found {
		return core.Manifest{}, fmt.Errorf("实例「%s」缺少安装清单，请重新安装后再重建", target.instanceName)
	}

	report, verifyErr := core.VerifyManifest(s.root, manifest, core.VerifyFull)
	if verifyErr != nil {
		return core.Manifest{}, fmt.Errorf("无法校验实例「%s」的核心：%w", target.instanceName, verifyErr)
	}
	if report.Complete() {
		return manifest, nil
	}

	// 先修复再重建：复制损坏文件没有任何意义，切换后仍然不完整。
	repairReport, repairErr := s.repairWithRunner(ctx, target.instanceID, nil, s.runPlan)
	if repairErr != nil {
		return core.Manifest{}, fmt.Errorf("修复实例「%s」失败：%w", target.instanceName, repairErr)
	}
	if repairReport.NeedsReinstall {
		reason := repairReport.Reason
		if reason == "" {
			reason = "部分文件无法自动修复"
		}
		return core.Manifest{}, fmt.Errorf("实例「%s」需要重新安装后才能重建：%s", target.instanceName, reason)
	}

	// 修复会重写清单（文件哈希可能变化），必须重新读取。
	manifest, found, readErr = core.ReadManifest(s.root, target.versionID)
	if readErr != nil || !found {
		return core.Manifest{}, fmt.Errorf("实例「%s」修复后仍缺少安装清单", target.instanceName)
	}
	return manifest, nil
}

// populateRebuild 把清单登记的文件复制到暂存核心。
//
// 只复制清单里的路径：暂存目录不存在的文件（旧版本残留、失败的安装目录）
// 自然被排除在新核心之外。
func (s *InstanceService) populateRebuild(ctx context.Context, stagingRoot string, targets []coreRebuildTarget, progress func(string, float64)) error {
	unique := rebuildUniquePaths(targets)
	total := len(unique)
	done := 0

	for _, target := range targets {
		for _, file := range target.manifest.RequiredFiles() {
			if err := ctx.Err(); err != nil {
				return errors.New("重建已取消")
			}
			if _, copied := unique[file.Path]; !copied {
				continue
			}
			delete(unique, file.Path)

			if err := copyCoreFile(s.root, stagingRoot, file.Path); err != nil {
				return fmt.Errorf("复制核心文件 %s 失败：%w", file.Path, err)
			}
			done++
			if progress != nil && (done%64 == 0 || done == total) {
				progress(fmt.Sprintf("正在复制核心文件（%d/%d）", done, total), 0.05+0.5*float64(done)/float64(total))
			}
		}

		// 清单本身也要进入新核心，否则重建后的版本无法被识别为已安装。
		if err := core.WriteManifest(stagingRoot, target.manifest); err != nil {
			return fmt.Errorf("写入版本 %s 的安装清单失败：%w", target.versionID, err)
		}
	}

	// 索引不是游戏运行必需，但保留它能让「安装身份 → 版本目录」的映射不丢失。
	if err := copyCoreIndex(s.root, stagingRoot); err != nil {
		return err
	}
	return nil
}

// verifyRebuiltCore 在切换前逐文件校验暂存核心。
//
// 全量哈希而不是只看存在性：复制过程本身也可能出错（磁盘坏块、空间不足
// 导致的截断），而切换后旧核心已被移走，此时才发现问题的代价高得多。
func verifyRebuiltCore(root string, targets []coreRebuildTarget) error {
	for _, target := range targets {
		manifest, found, err := core.ReadManifest(root, target.versionID)
		if err != nil {
			return fmt.Errorf("暂存核心的版本 %s 清单无法读取：%w", target.versionID, err)
		}
		if !found {
			return fmt.Errorf("暂存核心缺少版本 %s 的安装清单", target.versionID)
		}
		report, verifyErr := core.VerifyManifest(root, manifest, core.VerifyFull)
		if verifyErr != nil {
			return fmt.Errorf("暂存核心的版本 %s 无法校验：%w", target.versionID, verifyErr)
		}
		if !report.Complete() {
			return fmt.Errorf("暂存核心的版本 %s 校验未通过：%d 个文件异常", target.versionID, len(report.ProblemFiles()))
		}
	}
	return nil
}

// copyCoreIndex 复制版本索引；索引不存在时视为空索引，不报错。
func copyCoreIndex(sourceRoot, targetRoot string) error {
	relative := core.IndexPath()
	source, resolveErr := artifact.ResolveInsideRoot(sourceRoot, relative)
	if resolveErr != nil {
		return errors.New("版本索引路径无效")
	}
	if _, statErr := os.Stat(source); statErr != nil {
		if os.IsNotExist(statErr) {
			return nil
		}
		return errors.New("无法读取版本索引")
	}
	return copyCoreFile(sourceRoot, targetRoot, relative)
}

// copyCoreFile 复制一个核心文件，写入经临时文件与 Rename 完成。
//
// 目标目录与其他暂存文件相互独立：中途失败最多留下一个临时文件，
// 不会出现半个可被误认为有效的数据文件。
func copyCoreFile(sourceRoot, targetRoot, relative string) error {
	source, sourceErr := artifact.ResolveInsideRoot(sourceRoot, relative)
	if sourceErr != nil {
		return errors.New("核心文件路径无效")
	}
	target, targetErr := artifact.ResolveInsideRoot(targetRoot, relative)
	if targetErr != nil {
		return errors.New("暂存文件路径无效")
	}

	info, statErr := os.Stat(source)
	if statErr != nil {
		return errors.New("核心文件不存在")
	}
	if !info.Mode().IsRegular() {
		return errors.New("核心文件不是普通文件")
	}
	if mkErr := os.MkdirAll(filepath.Dir(target), 0o755); mkErr != nil {
		return errors.New("无法创建暂存目录")
	}

	input, openErr := os.Open(source)
	if openErr != nil {
		return errors.New("无法读取核心文件")
	}
	defer input.Close()

	temp, createErr := os.CreateTemp(filepath.Dir(target), ".rebuild-*")
	if createErr != nil {
		return errors.New("无法创建暂存文件")
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)

	if _, copyErr := io.Copy(temp, input); copyErr != nil {
		temp.Close()
		return errors.New("无法写入暂存文件")
	}
	if syncErr := temp.Sync(); syncErr != nil {
		temp.Close()
		return errors.New("无法同步暂存文件")
	}
	if closeErr := temp.Close(); closeErr != nil {
		return errors.New("无法关闭暂存文件")
	}
	if chmodErr := os.Chmod(tempPath, 0o644); chmodErr != nil {
		return errors.New("无法设置暂存文件权限")
	}
	if renameErr := os.Rename(tempPath, target); renameErr != nil {
		return errors.New("无法提交暂存文件")
	}
	return nil
}

// rebuildUniquePaths 返回全部清单文件的去重路径集合。
func rebuildUniquePaths(targets []coreRebuildTarget) map[string]struct{} {
	result := make(map[string]struct{}, 256)
	for _, target := range targets {
		for _, file := range target.manifest.RequiredFiles() {
			result[file.Path] = struct{}{}
		}
	}
	return result
}

// ensureRebuildSpace 在复制前确认磁盘能容纳一份完整副本。
//
// 探测失败时不阻断：可用空间探针不可用不应成为用户无法重建的原因。
func ensureRebuildSpace(root string, need int64) error {
	if need <= 0 {
		return nil
	}
	available, err := platform.Current.Disk.AvailableBytes(root)
	if err != nil {
		return nil
	}
	required := need + rebuildFreeSpaceMargin
	if available < required {
		return fmt.Errorf(
			"磁盘可用空间不足：重建约需 %d MB，当前可用 %d MB",
			required>>20,
			available>>20,
		)
	}
	return nil
}
