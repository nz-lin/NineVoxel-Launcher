package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"example.com/ninevoxel/launcher/internal/artifact"
)

// IndexEntry 是索引中某个安装身份对应的记录。
type IndexEntry struct {
	// VersionID 是实际的版本目录名。
	//
	// 由于不使用存储世代，它同时也是对外的逻辑版本 ID；新增别名场景除外。
	VersionID string `json:"versionId"`
	// RecipeFingerprint 是产出该产物的安装配方指纹。
	RecipeFingerprint string `json:"recipeFingerprint"`
	// ManifestPath 是清单相对核心根的路径。
	ManifestPath string `json:"manifestPath"`
	// InstalledAtMs 是安装完成时间。
	InstalledAtMs int64 `json:"installedAtMs,omitempty"`
}

// Index 是版本索引的持久化结构。
//
// 它只承担「安装身份 → 实际版本目录」的映射与配方指纹记录；实例的引用关系
// 不在这里维护（易与实际清单不一致），需要时从实例清单实时重建。
type Index struct {
	SchemaVersion int                   `json:"schemaVersion"`
	Identities    map[string]IndexEntry `json:"identities"`
}

// newIndex 返回一个空索引。
func newIndex() *Index {
	return &Index{SchemaVersion: indexSchemaVersion, Identities: map[string]IndexEntry{}}
}

// Lookup 查询某个安装身份的索引记录。
func (i *Index) Lookup(identity string) (IndexEntry, bool) {
	if i == nil || i.Identities == nil {
		return IndexEntry{}, false
	}
	entry, ok := i.Identities[identity]
	return entry, ok
}

// Identities 返回全部已登记身份，按字典序排列。
func (i *Index) AllIdentities() []string {
	if i == nil || len(i.Identities) == 0 {
		return nil
	}
	result := make([]string, 0, len(i.Identities))
	for identity := range i.Identities {
		result = append(result, identity)
	}
	sort.Strings(result)
	return result
}

// identityIndexPath 返回索引相对核心根的路径。
func identityIndexPath() string {
	return indexDirName + "/" + indexFileName
}

// readIndex 读取核心根下的版本索引。
//
// 文件不存在时返回空索引而不是错误：首次启动或核心尚未安装任何版本时，
// 「没有索引」与「索引为空」等价。
func readIndex(root string) (*Index, error) {
	absolute, resolveErr := artifact.ResolveInsideRoot(root, identityIndexPath())
	if resolveErr != nil {
		return nil, errors.New("版本索引路径无效")
	}
	content, err := os.ReadFile(absolute)
	if err != nil {
		if os.IsNotExist(err) {
			return newIndex(), nil
		}
		return nil, errors.New("无法读取版本索引")
	}
	if len(content) == 0 || len(content) > maxIndexBytes {
		return nil, errors.New("版本索引大小异常")
	}

	var index Index
	if unmarshalErr := json.Unmarshal(content, &index); unmarshalErr != nil {
		return nil, errors.New("版本索引格式损坏")
	}
	if index.SchemaVersion != indexSchemaVersion {
		return nil, fmt.Errorf("版本索引版本不受支持：%d", index.SchemaVersion)
	}
	if index.Identities == nil {
		index.Identities = map[string]IndexEntry{}
	}
	return &index, nil
}

// maxIndexBytes 是索引文件的允许上限。
//
// 每个身份一条记录，正常数量级是几十条；上限取 8 MiB 足以容纳数千条，
// 同时拦住被异常写坏的超大文件。
const maxIndexBytes = 8 << 20

// writeIndexAtomic 以「临时文件 + Rename」方式写入索引。
//
// 单次替换是原子的；并发场景下的读改写丢更新问题由锁保证，不依赖本函数。
func writeIndexAtomic(root string, index *Index) error {
	absolute, resolveErr := artifact.ResolveInsideRoot(root, identityIndexPath())
	if resolveErr != nil {
		return errors.New("版本索引路径无效")
	}
	if mkErr := os.MkdirAll(filepath.Dir(absolute), 0o755); mkErr != nil {
		return errors.New("无法创建版本索引目录")
	}

	index.SchemaVersion = indexSchemaVersion
	if index.Identities == nil {
		index.Identities = map[string]IndexEntry{}
	}
	encoded, marshalErr := json.MarshalIndent(index, "", "  ")
	if marshalErr != nil {
		return errors.New("版本索引无法编码")
	}

	temp, createErr := os.CreateTemp(filepath.Dir(absolute), ".versions-*.json")
	if createErr != nil {
		return errors.New("无法创建版本索引临时文件")
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)

	if _, writeErr := temp.Write(encoded); writeErr != nil {
		temp.Close()
		return errors.New("无法写入版本索引")
	}
	if syncErr := temp.Sync(); syncErr != nil {
		temp.Close()
		return errors.New("无法同步版本索引")
	}
	if closeErr := temp.Close(); closeErr != nil {
		return errors.New("无法关闭版本索引临时文件")
	}
	if renameErr := os.Rename(tempPath, absolute); renameErr != nil {
		return errors.New("无法提交版本索引")
	}
	return nil
}
