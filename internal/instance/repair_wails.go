package instance

import (
	"context"
	"errors"

	"example.com/ninevoxel/launcher/internal/core"
)

// RepairInstanceRequest 是「修复实例核心资源」的入参。
type RepairInstanceRequest struct {
	// InstanceID 是要修复的实例。
	InstanceID string `json:"instanceId"`
}

// VerifyInstanceRequest 是「校验实例核心资源」的入参。
type VerifyInstanceRequest struct {
	// InstanceID 是要校验的实例。
	InstanceID string `json:"instanceId"`
}

// VerifyInstanceResult 是校验结果。
//
// 只报告问题数量与是否完整，不返回文件路径：核心目录的绝对路径不进入前端，
// 相对路径对用户也没有操作价值（用户不会手动去补某个 jar）。
type VerifyInstanceResult struct {
	// VersionID 是被校验的版本。
	VersionID string `json:"versionId"`
	// Checked 是校验的文件数。
	Checked int `json:"checked"`
	// ProblemCount 是有问题的文件数。
	ProblemCount int `json:"problemCount"`
	// Complete 表示全部文件都通过校验。
	Complete bool `json:"complete"`
	// HasManifest 表示该版本是否已有安装清单；false 时无法校验。
	HasManifest bool `json:"hasManifest"`
}

// VerifyInstance 校验实例依赖的核心资源完整性。
//
// 逐文件计算哈希（L2）：这是「知道到底坏没坏」的唯一方式，
// 只比对存在性与大小会漏掉内容被替换的情况。
func (s *InstanceService) VerifyInstance(req VerifyInstanceRequest) (VerifyInstanceResult, error) {
	s.mu.Lock()
	if err := s.openLocked(); err != nil {
		s.mu.Unlock()
		return VerifyInstanceResult{}, err
	}
	s.mu.Unlock()

	config, err := ResolveLaunchConfig(req.InstanceID)
	if err != nil {
		return VerifyInstanceResult{}, err
	}
	result := VerifyInstanceResult{VersionID: config.MCVersion}

	manifest, found, readErr := core.ReadManifest(s.root, result.VersionID)
	if readErr != nil {
		return result, readErr
	}
	if !found {
		result.HasManifest = false
		return result, nil
	}
	result.HasManifest = true
	result.Checked = len(manifest.RequiredFiles())

	report, verifyErr := core.VerifyManifest(s.root, manifest, core.VerifyFull)
	if verifyErr != nil {
		return result, verifyErr
	}
	result.ProblemCount = len(report.ProblemFiles())
	result.Complete = report.Complete()
	return result, nil
}

// RepairInstanceForUser 修复实例依赖的核心资源。
//
// 与安装不同，修复只补齐缺失或损坏的文件；当问题超出修复能力
// （加载器产物或版本元数据损坏）时，返回 NeedsReinstall 让界面引导用户重装。
func (s *InstanceService) RepairInstanceForUser(req RepairInstanceRequest) (RepairReport, error) {
	s.mu.Lock()
	if err := s.openLocked(); err != nil {
		s.mu.Unlock()
		return RepairReport{}, err
	}
	item, err := s.getLocked(req.InstanceID)
	if err != nil {
		s.mu.Unlock()
		return RepairReport{}, err
	}
	if item.InstallationState == "installing" {
		s.mu.Unlock()
		return RepairReport{}, errors.New("该实例正在安装，请等待完成后再修复")
	}
	s.mu.Unlock()

	return s.repairInstance(context.Background(), req.InstanceID, nil)
}
