package core

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"example.com/ninevoxel/launcher/internal/artifact"
)

// 本文件提供「无人引用的版本目录」的枚举与删除。
//
// 回收的判定依据不在本包：哪些版本仍被实例引用属于实例层知识（见
// instance.collectReferencedVersions）。本包只负责两件事：
//
//   - 列出核心中的版本目录，并标出「是否由启动器安装」（有无安装清单）；
//   - 在**确认没有游戏正在使用**的前提下删除一个版本目录。
//
// 第二条是本包的安全底线：租约检查放在这里而不是交给调用方，避免某条回收
// 路径忘记检查就把正在运行的游戏资源删掉。

// VersionDir 描述核心中的一个版本目录。
type VersionDir struct {
	// RelDir 是相对核心根的目录（`versions/<id>` 或 `loaders/<id>`）。
	RelDir string
	// VersionID 是版本目录名。
	VersionID string
	// Managed 表示该目录由启动器安装（存在合法安装清单）。
	//
	// 只有 Managed 为真的目录才允许自动回收：用户手动放入的版本目录没有
	// 清单，删掉无法重建，必须原样保留。
	Managed bool
	// SizeBytes 是目录下普通文件的总字节数。
	SizeBytes int64
}

// ListVersionDirs 枚举核心中的全部版本目录。
//
// 只扫描 `versions/` 与 `loaders/` 一层子目录；符号链接目录一律跳过。
// 安装暂存目录（`.staging` / `.prev` 后缀）也会被跳过：它们不是可启动版本。
func ListVersionDirs(root string) ([]VersionDir, error) {
	result := make([]VersionDir, 0, 16)
	for _, rootDir := range []string{versionsDirName, loadersDirName} {
		relative := rootDir
		absolute, resolveErr := artifact.ResolveInsideRoot(root, relative)
		if resolveErr != nil {
			return nil, errors.New("核心版本目录路径无效")
		}
		entries, readErr := os.ReadDir(absolute)
		if readErr != nil {
			if os.IsNotExist(readErr) {
				continue
			}
			return nil, errors.New("无法读取核心版本目录")
		}
		for _, entry := range entries {
			name := entry.Name()
			if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
				continue
			}
			if !safeVersionName(name) {
				continue
			}
			dir := &VersionDir{RelDir: rootDir + "/" + name, VersionID: name}
			manifest, found, manifestErr := ReadManifest(root, name)
			dir.Managed = manifestErr == nil && found && manifest.VersionDir == dir.RelDir
			dir.SizeBytes = directorySize(filepath.Join(absolute, name))
			result = append(result, *dir)
		}
	}
	return result, nil
}

// safeVersionName 判断目录名是否可以当作版本标识。
//
// 与安全删除相关：只允许不含分隔符、不以点开头的单段名称。
func safeVersionName(name string) bool {
	if name == "" || name == "." || name == ".." || len(name) > 128 {
		return false
	}
	if strings.HasPrefix(name, ".") || strings.HasSuffix(name, stagingSuffix) || strings.HasSuffix(name, previousSuffix) {
		return false
	}
	return !strings.ContainsAny(name, `/\:`)
}

// directorySize 统计目录下普通文件的字节数；读不到的部分按 0 计。
func directorySize(root string) int64 {
	var total int64
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry == nil {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err == nil && info.Mode().IsRegular() {
			total += info.Size()
		}
		return nil
	})
	return total
}

// RemoveVersionDir 删除一个版本目录。
//
// 参数 relDir 必须是 `versions/<id>` 或 `loaders/<id>`。两道保护任一命中即拒绝：
//
//   - 该版本仍持有运行时租约（游戏正在使用）；
//   - 租约表不可读（无法确认，按使用中处理）。
//
// 调用方负责「是否还有实例引用」的判断：引用关系只存在于实例清单里，
// 核心包不维护它。
func RemoveVersionDir(root, relDir string) error {
	versionID, valid := splitVersionDir(relDir)
	if !valid {
		return errors.New("版本目录不受支持")
	}
	if leaseErr := ensureNoLease(root, versionID); leaseErr != nil {
		return leaseErr
	}
	absolute, resolveErr := artifact.ResolveInsideRoot(root, relDir)
	if resolveErr != nil {
		return errors.New("版本目录路径无效")
	}
	info, statErr := os.Lstat(absolute)
	if statErr != nil {
		if os.IsNotExist(statErr) {
			return nil
		}
		return errors.New("无法访问版本目录")
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("版本目录不是普通目录")
	}
	if removeErr := os.RemoveAll(absolute); removeErr != nil {
		return errors.New("删除版本目录失败")
	}
	return nil
}

// ensureNoLease 确认该版本没有正在运行的游戏。
func ensureNoLease(root, versionID string) error {
	leases, leaseErr := ReadActiveLeases(root)
	if leaseErr != nil {
		return errors.New("无法读取运行时租约，已中止删除")
	}
	if _, active := leases[versionID]; active {
		return errors.New("该版本有游戏正在运行，无法删除")
	}
	return nil
}

// splitVersionDir 把 `versions/<id>` / `loaders/<id>` 拆成版本标识。
func splitVersionDir(relDir string) (string, bool) {
	segments := strings.Split(relDir, "/")
	if len(segments) != 2 {
		return "", false
	}
	if segments[0] != versionsDirName && segments[0] != loadersDirName {
		return "", false
	}
	if !safeVersionName(segments[1]) {
		return "", false
	}
	return segments[1], true
}
