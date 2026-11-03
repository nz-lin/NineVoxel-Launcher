// 本文件实现「把实例目录里已存在的重复文件替换为指向共享缓存的硬链接」。
//
// 场景：硬链接策略只在**本次安装**生效，之前安装的实例仍然各自保存了一份完整副本
// （实测 5 个实例的 `assets` 各 463MB）。本文件提供一次性的空间回收：扫描实例中
// 内容寻址/可共享的目录，把与共享缓存内容相同的文件替换成硬链接。
//
// 安全边界：
//   - 只处理 `libraries/`、`assets/`、`versions/` 下的**普通文件**，不碰 `mods/`、
//     `saves/`、`config/`、`logs/` 等用户可变状态；
//   - 只有在共享缓存中存在**同内容**对象（大小一致且哈希匹配）时才替换；
//   - 替换走「临时名建链 + 改名」，不会让读者看到半个文件；
//   - 文件被占用（游戏正在运行）时改名失败，计入失败数并跳过，不破坏任何数据。
//
// 硬链接的适用前提：`libraries/`、`assets/`、`versions/` 里的内容都是
// **内容寻址、安装后只读**的制品。若将来引入会「就地改写」这些文件的流程
// （例如旧版 Forge 的 jar mod 合并），必须让它先断开硬链接再写，否则会改坏
// 共享缓存，进而影响其它实例。
package instance

import (
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"example.com/ninevoxel/launcher/internal/artifact"
)

// dedupScanRoots 是实例内允许去重的子目录（相对游戏目录）。
//
// 刻意不含 `natives/`：那是每个实例解压出来的私有副本，内容随平台与加载器不同。
var dedupScanRoots = []string{"libraries", "assets", "versions"}

// maxDedupHashBytes 是「愿意读取全文计算哈希」的单文件上限。
//
// 超过该大小的文件（例如未来的大型资源包）直接跳过：去重是空间优化，
// 不值得为它读几百 MB。
const maxDedupHashBytes = 64 << 20

// DedupStorageResult 是空间回收的结果。
type DedupStorageResult struct {
	// ScannedFiles 为扫描到的候选文件数。
	ScannedFiles int `json:"scannedFiles"`

	// LinkedFiles 为本次新建立硬链接的文件数。
	LinkedFiles int `json:"linkedFiles"`

	// SavedBytes 为回收的字节数（等于新建立链接的文件大小之和）。
	SavedBytes int64 `json:"savedBytes"`

	// SkippedFiles 为已在共享缓存之外（找不到同内容对象）而跳过的文件数。
	SkippedFiles int `json:"skippedFiles"`

	// FailedFiles 为替换失败的文件数（通常是文件被占用）。
	FailedFiles int `json:"failedFiles"`
}

// DedupInstanceStorage 把所有实例中的重复文件替换为共享缓存的硬链接。
//
// 该操作幂等：已经是指向缓存的硬链接会被跳过，重复调用不会产生副作用。
// 返回回收结果；实例数据目录不可访问时返回错误。
func (s *InstanceService) DedupInstanceStorage() (DedupStorageResult, error) {
	if err := s.openLocked(); err != nil {
		return DedupStorageResult{}, err
	}

	instancesRoot := filepath.Join(s.root, filepath.FromSlash(instancesRootRel))
	entries, readErr := os.ReadDir(instancesRoot)
	if readErr != nil {
		if errors.Is(readErr, os.ErrNotExist) {
			return DedupStorageResult{}, nil
		}
		return DedupStorageResult{}, errors.New("无法读取实例目录")
	}

	result := DedupStorageResult{}
	for _, entry := range entries {
		if !entry.IsDir() || !validateInstanceID(entry.Name()) {
			continue
		}
		gameDirectory := filepath.Join(instancesRoot, entry.Name(), filepath.FromSlash(".minecraft"))
		for _, scanRoot := range dedupScanRoots {
			dedupDirectory(s.root, filepath.Join(gameDirectory, scanRoot), &result)
		}
	}
	return result, nil
}

// dedupDirectory 递归处理单个目录。
//
// 参数 dataRoot 为共享缓存根；directory 为待扫描目录；result 累积统计。
func dedupDirectory(dataRoot, directory string, result *DedupStorageResult) {
	entries, readErr := os.ReadDir(directory)
	if readErr != nil {
		return
	}

	// 固定遍历顺序：让结果可复现，也便于排查「为什么这个文件没被处理」。
	sort.Slice(entries, func(left, right int) bool { return entries[left].Name() < entries[right].Name() })

	for _, entry := range entries {
		path := filepath.Join(directory, entry.Name())
		if entry.IsDir() {
			dedupDirectory(dataRoot, path, result)
			continue
		}
		dedupFile(dataRoot, path, entry, result)
	}
}

// dedupFile 尝试把单个文件替换为指向共享缓存的硬链接。
func dedupFile(dataRoot, path string, entry os.DirEntry, result *DedupStorageResult) {
	info, infoErr := entry.Info()
	if infoErr != nil || !info.Mode().IsRegular() {
		return
	}
	result.ScannedFiles++

	hash, ok := contentHash(path, entry.Name(), info.Size())
	if !ok {
		result.SkippedFiles++
		return
	}

	cachePath := filepath.Join(dataRoot, filepath.FromSlash(artifact.DirArtifactObjects), hash[:2], hash)
	cacheInfo, cacheErr := os.Stat(cachePath)
	if cacheErr != nil || !cacheInfo.Mode().IsRegular() || cacheInfo.Size() != info.Size() {
		result.SkippedFiles++
		return
	}

	if same, sameErr := sameFileContent(cachePath, path); sameErr == nil && same {
		// 已经是硬链接：不重复计数，也不计入跳过（它本来就不占额外空间）。
		return
	}

	if linkErr := linkAtomically(cachePath, path); linkErr != nil {
		result.FailedFiles++
		return
	}
	result.LinkedFiles++
	result.SavedBytes += info.Size()
}

// contentHash 返回文件的 SHA-1，用于在共享缓存中定位同内容对象。
//
// 优化：`assets/objects/<前两位>/<40 位哈希>` 的文件名本身就是内容哈希，
// 直接复用可以避免读取上百 MB 数据。其余文件（libraries 的 maven 路径、
// 版本 JSON 等）只能实际计算。
func contentHash(path, name string, size int64) (string, bool) {
	if isSHA1Hex(name) {
		// 校验文件名确实是内容哈希：同目录名前两位必须匹配。
		prefix := filepath.Base(filepath.Dir(path))
		if len(prefix) == 2 && strings.EqualFold(prefix, name[:2]) {
			return strings.ToLower(name), true
		}
	}
	if size > maxDedupHashBytes {
		return "", false
	}

	file, openErr := os.Open(path)
	if openErr != nil {
		return "", false
	}
	defer file.Close()

	digest := sha1.New()
	if _, copyErr := io.Copy(digest, file); copyErr != nil {
		return "", false
	}
	return hex.EncodeToString(digest.Sum(nil)), true
}

// isSHA1Hex 判断字符串是否为 40 位十六进制串。
func isSHA1Hex(value string) bool {
	if len(value) != 40 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') && (char < 'A' || char > 'F') {
			return false
		}
	}
	return true
}
