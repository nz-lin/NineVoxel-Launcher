package instance

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"

	"example.com/ninevoxel/launcher/internal/artifact"
)

// 本文件提供安装流程用到的小工具：进程内按名加锁、核心目录读取、版本链枚举。

// installLockTable 是按名互斥锁表，随实例服务创建。
//
// 用引用计数在无等待者时回收条目，避免长期运行后表无限增长。
type installLockTable struct {
	mu      sync.Mutex
	entries map[string]*installLockEntry
}

// installLockEntry 是单个键的锁与等待计数。
type installLockEntry struct {
	mu   sync.Mutex
	refs int
}

// newInstallLockTable 创建按名锁表。
func newInstallLockTable() *installLockTable {
	return &installLockTable{entries: map[string]*installLockEntry{}}
}

// Lock 取得指定键的锁，返回解锁函数。
func (t *installLockTable) Lock(key string) func() {
	t.mu.Lock()
	entry, exists := t.entries[key]
	if !exists {
		entry = &installLockEntry{}
		t.entries[key] = entry
	}
	entry.refs++
	t.mu.Unlock()

	entry.mu.Lock()
	return func() {
		entry.mu.Unlock()
		t.mu.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(t.entries, key)
		}
		t.mu.Unlock()
	}
}

// readCoreDir 列出核心根下某个目录的条目名。
//
// 目录不存在时返回错误：调用方据此判断「该版本目录尚未生成」。
func readCoreDir(root, relative string) ([]string, error) {
	absolute, err := artifact.ResolveInsideRoot(root, relative)
	if err != nil {
		return nil, errors.New("核心目录无效")
	}
	entries, readErr := os.ReadDir(absolute)
	if readErr != nil {
		return nil, readErr
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		names = append(names, entry.Name())
	}
	return names, nil
}

// versionChainFiles 返回版本及其继承链上每个版本 JSON 的相对路径。
//
// 清单需要记录全部继承链的 JSON：只记录子版本时，父版本 JSON 被删除后
// 快速校验发现不了，启动会在合并阶段才失败。
//
// 链上**每一层独立解析位置**：加载器版本在 `loaders/`，它继承的原版版本在
// `versions/`，不能从子版本目录外推父版本目录。
func versionChainFiles(root, versionID string) ([]string, error) {
	chain := make([]string, 0, 4)
	current := versionID
	visited := make(map[string]struct{}, 4)

	for current != "" {
		if _, seen := visited[current]; seen {
			break
		}
		visited[current] = struct{}{}

		relative, found, dirErr := artifact.ResolveVersionDir(root, current)
		if dirErr != nil {
			return nil, errors.New("版本目录无效")
		}
		if !found {
			if current == versionID {
				// 顶层版本目录缺失：安装不完整。
				return nil, errors.New("版本元数据缺失，无法完成安装")
			}
			// 父版本缺失：加载器可能是自包含的（无 inheritsFrom 指向缺失节点），
			// 记录已找到的部分即可，不阻断安装。
			break
		}
		jsonRelative := relative + "/" + current + ".json"
		if _, err := coreFileInfo(root, jsonRelative); err != nil {
			if current == versionID {
				return nil, errors.New("版本元数据缺失，无法完成安装")
			}
			break
		}
		chain = append(chain, jsonRelative)

		next, parentErr := readInheritsFrom(root, jsonRelative)
		if parentErr != nil {
			break
		}
		current = next
	}
	if len(chain) == 0 {
		return nil, errors.New("版本元数据缺失，无法完成安装")
	}
	return chain, nil
}

// coreFileInfo 返回核心根下某个相对路径的文件信息。
func coreFileInfo(root, relative string) (os.FileInfo, error) {
	absolute, err := artifact.ResolveInsideRoot(root, relative)
	if err != nil {
		return nil, errors.New("核心路径无效")
	}
	return os.Stat(absolute)
}

// readInheritsFrom 读取版本 JSON 的 inheritsFrom 字段。
//
// 只解析这一个字段：本函数用于遍历继承链，不需要完整反序列化，
// 也就不受其他字段格式变化的影响。
func readInheritsFrom(root, relative string) (string, error) {
	absolute, err := artifact.ResolveInsideRoot(root, relative)
	if err != nil {
		return "", errors.New("核心路径无效")
	}
	content, readErr := os.ReadFile(absolute)
	if readErr != nil {
		return "", readErr
	}
	if len(content) == 0 || len(content) > maxVersionJSONBytes {
		return "", errors.New("版本 JSON 大小异常")
	}

	var partial struct {
		InheritsFrom string `json:"inheritsFrom"`
	}
	if unmarshalErr := json.Unmarshal(content, &partial); unmarshalErr != nil {
		return "", errors.New("版本 JSON 格式损坏")
	}
	return strings.TrimSpace(partial.InheritsFrom), nil
}

// maxVersionJSONBytes 是版本 JSON 的允许上限。
//
// 正常版本 JSON 在几十到几百 KB；上限取 16 MiB 足以容纳超大整合包，
// 同时拦住被写坏的超大文件。
const maxVersionJSONBytes = 16 << 20
