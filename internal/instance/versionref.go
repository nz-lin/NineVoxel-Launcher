package instance

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"example.com/ninevoxel/launcher/internal/artifact"
	"example.com/ninevoxel/launcher/internal/core"
)

// 本文件实现「共享核心版本被哪些实例引用」的判定。
//
// 背景：核心里的版本目录由所有实例共用。删除它之前必须先确认没有其他实例
// 还在引用——否则会出现「A 实例卸载加载器，B 实例跟着起不来」这种与操作对象
// 无关的故障，而且现场只剩一个「版本元数据缺失」的报错。
//
// 判定依据是各实例的 `instance.json`（磁盘上的权威来源），而不是 SQLite 索引：
// 索引只是可重建的缓存，用它做「能否删除」的判断会在索引滞后时误删。

// versionReferencedByOthers 返回除 exceptInstanceID 外是否仍有实例引用 versionID。
//
// 参数 versionID 为版本目录名；exceptInstanceID 为本次操作涉及的实例（不计入）。
//
// 读取失败（清单损坏、目录不可读）按「仍被引用」处理：宁可留下一个无人引用的
// 版本目录，也不能删掉别人正在用的。
func (s *InstanceService) versionReferencedByOthers(versionID, exceptInstanceID string) bool {
	referenced, err := s.collectReferencedVersionsSkipping(exceptInstanceID)
	if err != nil {
		return true
	}
	_, exists := referenced[versionID]
	return exists
}

// removeCoreVersionIfUnreferenced 删除无人引用的核心版本目录。
//
// 参数 versionID 为版本目录名；exceptInstanceID 为本次操作涉及的实例。
// 返回是否真的删除了目录。
//
// 只删除版本目录本身，不触碰共享的 libraries 与 assets：它们可能被其他版本
// 复用，回收属于后续的垃圾回收能力。
//
// 三道保护，任一命中都不删：
//   - 其他实例仍引用该版本（引用检查）；
//   - 有游戏进程正在使用该版本（运行时租约，由 core.RemoveVersionDir 检查）；
//   - 目录不存在或不在受支持的版本根下。
func (s *InstanceService) removeCoreVersionIfUnreferenced(versionID, exceptInstanceID string) bool {
	if !safeVersionDirectoryName(versionID) {
		return false
	}
	if s.versionReferencedByOthers(versionID, exceptInstanceID) {
		return false
	}
	versionDir, found, dirErr := artifact.ResolveVersionDir(s.root, versionID)
	if dirErr != nil || !found {
		return false
	}
	return core.RemoveVersionDir(s.root, versionDir) == nil
}

// collectReferencedVersions 收集全部仍被引用的版本标识。
//
// 覆盖活动实例与回收站实例：回收站里的实例仍要能还原，删掉它依赖的版本会让
// 「还原」变成一次必然失败的启动。
func (s *InstanceService) collectReferencedVersions() (map[string]struct{}, error) {
	return s.collectReferencedVersionsSkipping("")
}

// collectReferencedVersionsSkipping 与 collectReferencedVersions 相同，但排除
// 指定实例（活动目录按目录名匹配，回收站按 `<instanceID>-` 前缀匹配）。
//
// 排除逻辑用于「卸载加载器」这类操作：调用方已经改写了该实例的 VersionID，
// 但内存快照仍是旧值，按 ID 排除比依赖磁盘状态更稳定。
func (s *InstanceService) collectReferencedVersionsSkipping(exceptInstanceID string) (map[string]struct{}, error) {
	result := make(map[string]struct{}, 8)
	for _, rootRel := range []string{instancesRootRel, trashRootRel} {
		directory, err := managedPath(s.root, rootRel)
		if err != nil {
			return nil, err
		}
		entries, readErr := os.ReadDir(directory)
		if readErr != nil {
			if errors.Is(readErr, os.ErrNotExist) {
				continue
			}
			return nil, errors.New("无法读取实例目录")
		}
		for _, entry := range entries {
			if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
				continue
			}
			name := entry.Name()
			if rootRel == instancesRootRel && name == ".trash" {
				continue
			}
			if exceptInstanceID != "" && strings.HasPrefix(name, exceptInstanceID) {
				continue
			}
			if err := addManifestReferences(s.root, filepath.Join(directory, name), result); err != nil {
				return nil, err
			}
		}
	}
	return result, nil
}

// addManifestReferences 把一个实例清单的引用加入集合。
//
// 引用有三个来源，缺一不可：
//
//	VersionID / MCVersion   实例直接声明的版本；
//	清单中的文件路径          加载器实例的清单包含原版 jar 与父版本 JSON，
//	                        只看 VersionID 会把原版目录判成无人引用。
func addManifestReferences(root, directory string, result map[string]struct{}) error {
	manifest, err := readInstanceManifest(directory)
	if err != nil {
		if errors.Is(err, errManifestNotFound) {
			// 没有清单的目录不构成引用（也无法判断它用什么版本）。
			return nil
		}
		// 清单损坏：无法判断，按「有引用」中止回收。
		return errors.New("实例清单损坏，已中止核心回收")
	}

	versionID := strings.TrimSpace(manifest.VersionID)
	mcVersion := strings.TrimSpace(manifest.MCVersion)
	if versionID != "" {
		result[versionID] = struct{}{}
	}
	if mcVersion != "" {
		result[mcVersion] = struct{}{}
	}

	installVersion := versionID
	if installVersion == "" {
		installVersion = mcVersion
	}
	if installVersion == "" {
		return nil
	}
	coreManifest, found, manifestErr := core.ReadManifest(root, installVersion)
	if manifestErr != nil || !found {
		// 清单不可读：只保留版本标识层面的引用，无法枚举文件。
		return nil
	}
	for _, file := range coreManifest.RequiredFiles() {
		if id, ok := versionIDFromPath(file.Path); ok {
			result[id] = struct{}{}
		}
	}
	return nil
}

// versionIDFromPath 从 `versions/<id>/...` 或 `loaders/<id>/...` 提取版本标识。
func versionIDFromPath(path string) (string, bool) {
	segments := strings.Split(path, "/")
	if len(segments) < 2 {
		return "", false
	}
	if segments[0] != artifact.DirVersions && segments[0] != artifact.DirLoaders {
		return "", false
	}
	if segments[1] == "" || segments[1] == "." || segments[1] == ".." {
		return "", false
	}
	return segments[1], true
}
