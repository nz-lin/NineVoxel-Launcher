package instance

import (
	"errors"
	"sort"
	"strings"

	"example.com/ninevoxel/launcher/internal/core"
)

// 本文件实现「无人引用的核心版本回收」。
//
// 删除实例只把实例目录移入回收站，不碰共享核心：这是刻意的，否则「删实例」
// 会连带破坏其他实例共用的版本。代价是核心会持续累积旧版本，因此必须有一个
// 显式的回收入口（删除永久化时的自动回收 + 设置页的手动清理）。
//
// 引用关系由 versionref.go 的 collectReferencedVersions 提供；本文件只负责
// 差集与执行。安全底线在 core.RemoveVersionDir：它在删除前会再查一次租约。

// ReclaimSkip 描述一个被跳过的版本目录及其原因。
type ReclaimSkip struct {
	VersionID string `json:"versionId"`
	Reason    string `json:"reason"`
}

// 跳过原因取值。
const (
	// reclaimSkipUnmanaged 表示目录没有安装清单，可能是用户手动放入的。
	reclaimSkipUnmanaged = "unmanaged"
	// reclaimSkipReferenced 表示仍有实例（含回收站实例）引用它。
	reclaimSkipReferenced = "referenced"
	// reclaimSkipInUse 表示有游戏正在使用它。
	reclaimSkipInUse = "in_use"
	// reclaimSkipFailed 表示删除失败（多为文件被占用）。
	reclaimSkipFailed = "failed"
)

// ReclaimReport 是一次核心回收的结果。
type ReclaimReport struct {
	// Removed 是被删除的版本标识。
	Removed []string `json:"removed"`
	// FreedBytes 是释放的字节数（按清单记录的目录大小累计）。
	FreedBytes int64 `json:"freedBytes"`
	// Skipped 是被跳过的版本及原因。
	Skipped []ReclaimSkip `json:"skipped"`
}

// CleanupUnusedCore 回收无人引用的核心版本目录。
//
// 只删「由启动器安装」（存在合法清单）且无人引用的版本；用户手动放入的目录、
// 仍被实例引用的版本、正在运行使用的版本一律跳过。
func (s *InstanceService) CleanupUnusedCore() (ReclaimReport, error) {
	report := ReclaimReport{Removed: []string{}, Skipped: []ReclaimSkip{}}

	referenced, err := s.collectReferencedVersions()
	if err != nil {
		return report, err
	}

	dirs, listErr := core.ListVersionDirs(s.root)
	if listErr != nil {
		return report, listErr
	}
	// 先读一次租约表：运行中的版本要在删除前就跳过，让用户看到
	// 「有游戏在运行」而不是一条删除失败。
	leases, leaseErr := core.ReadActiveLeases(s.root)
	if leaseErr != nil {
		return report, errors.New("无法读取运行时租约，已中止核心回收")
	}
	var firstErr error
	for _, dir := range dirs {
		if !dir.Managed {
			report.Skipped = append(report.Skipped, ReclaimSkip{VersionID: dir.VersionID, Reason: reclaimSkipUnmanaged})
			continue
		}
		if _, referencedVersion := referenced[dir.VersionID]; referencedVersion {
			report.Skipped = append(report.Skipped, ReclaimSkip{VersionID: dir.VersionID, Reason: reclaimSkipReferenced})
			continue
		}
		if _, running := leases[dir.VersionID]; running {
			report.Skipped = append(report.Skipped, ReclaimSkip{VersionID: dir.VersionID, Reason: reclaimSkipInUse})
			continue
		}
		if removeErr := core.RemoveVersionDir(s.root, dir.RelDir); removeErr != nil {
			reason := reclaimSkipFailed
			if strings.Contains(removeErr.Error(), "正在运行") || strings.Contains(removeErr.Error(), "租约") {
				reason = reclaimSkipInUse
			}
			report.Skipped = append(report.Skipped, ReclaimSkip{VersionID: dir.VersionID, Reason: reason})
			if firstErr == nil {
				firstErr = removeErr
			}
			continue
		}
		report.Removed = append(report.Removed, dir.VersionID)
		report.FreedBytes += dir.SizeBytes
	}
	sort.Strings(report.Removed)
	return report, firstErr
}
