package instance

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"example.com/ninevoxel/launcher/internal/artifact"
	"example.com/ninevoxel/launcher/internal/loader"
)

// 本文件实现「共享核心版本被哪些实例引用」的判定。
//
// 背景：核心里的 `versions/<id>/` 由所有实例共用。删除它之前必须先确认没有
// 其他实例还在引用——否则会出现「A 实例卸载加载器，B 实例跟着起不来」这种
// 与操作对象无关的故障，而且现场只剩一个「版本元数据缺失」的报错。

// versionReferencedByOthers 返回除 exceptInstanceID 外是否仍有实例引用 versionID。
//
// 参数 versionID 为版本目录名；exceptInstanceID 为本次操作涉及的实例（不计入）。
//
// 判定依据是各实例的 `instance.json`（磁盘上的权威来源），而不是 SQLite 索引：
// 索引只是可重建的缓存，用它做「能否删除」的判断会在索引滞后时误删。
//
// 读取失败（清单损坏、目录不可读）按「仍被引用」处理：宁可留下一个无人引用的
// 版本目录，也不能删掉别人正在用的。
func (s *InstanceService) versionReferencedByOthers(versionID, exceptInstanceID string) bool {
	if strings.TrimSpace(versionID) == "" {
		return true
	}
	instancesRoot, err := managedPath(s.root, instancesRootRel)
	if err != nil {
		return true
	}
	entries, readErr := os.ReadDir(instancesRoot)
	if readErr != nil {
		// 目录不存在说明还没有任何实例，可以安全删除。
		if errors.Is(readErr, os.ErrNotExist) {
			return false
		}
		return true
	}

	for _, entry := range entries {
		if !entry.IsDir() || !validateInstanceID(entry.Name()) {
			continue
		}
		if entry.Name() == exceptInstanceID {
			continue
		}
		manifest, manifestErr := readInstanceManifest(filepath.Join(instancesRoot, entry.Name()))
		if manifestErr != nil {
			if errors.Is(manifestErr, errManifestNotFound) {
				// 没有清单的目录不构成引用。
				continue
			}
			// 清单损坏：无法判断，保守认定为引用。
			return true
		}
		if strings.TrimSpace(manifest.VersionID) == versionID {
			return true
		}
		// 清单尚未写入 VersionID 时，按「加载器 + MC 版本 + 加载器版本」推导，
		// 覆盖安装中断等中间状态。
		if strings.TrimSpace(manifest.VersionID) == "" && manifest.LoaderVersion != "" {
			if kind, kindErr := loader.ParseKind(manifest.Loader); kindErr == nil {
				if computed, versionErr := loader.VersionID(kind, manifest.MCVersion, manifest.LoaderVersion); versionErr == nil && computed == versionID {
					return true
				}
			}
		}
	}
	return false
}

// removeCoreVersionIfUnreferenced 删除无人引用的核心版本目录。
//
// 参数 versionID 为版本目录名；exceptInstanceID 为本次操作涉及的实例。
// 返回是否真的删除了目录。
//
// 只删除 `versions/<id>/`，不触碰共享的 libraries 与 assets：它们可能被其他
// 版本复用，回收属于后续的垃圾回收能力。
//
// 两道保护，任一命中都不删：
//   - 其他实例仍引用该版本（引用检查）；
//   - 有游戏进程正在使用该版本（运行时租约）。
//
// 租约检查不能省：引用关系只说明「有实例配置指向它」，而运行中的游戏即使
// 实例配置已经改掉，仍在从该目录加载 JAR。
func (s *InstanceService) removeCoreVersionIfUnreferenced(versionID, exceptInstanceID string) bool {
	if !safeVersionDirectoryName(versionID) {
		return false
	}
	if s.versionReferencedByOthers(versionID, exceptInstanceID) {
		return false
	}
	if s.versionInUseByRunningGame(versionID) {
		return false
	}
	target, err := artifact.ResolveInsideRoot(s.root, "versions/"+versionID)
	if err != nil {
		return false
	}
	if removeErr := os.RemoveAll(target); removeErr != nil {
		return false
	}
	return true
}
