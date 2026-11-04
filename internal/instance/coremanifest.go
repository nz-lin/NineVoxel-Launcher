package instance

import (
	"path/filepath"
	"strings"

	"example.com/ninevoxel/launcher/internal/core"
)

// 本文件把「一次成功安装的产物」汇总成核心安装清单。
//
// 清单是「这个版本是否已完整安装」的唯一判据：只有全部步骤成功后才会写出，
// 因此它天然排除了「装到一半」的中间状态——逐项检查文件是否存在做不到这一点
// （Forge 的 processor 阶段失败时，版本 JSON 早已写好，但 client jar 还没有）。

// manifestInput 描述构建清单所需的全部产物。
//
// 各字段都来自安装流程中**确实成功落盘**的路径，不做任何推断：
// 清单里出现的文件必然存在，否则构建过程会失败并让整次安装判定为不完整。
type manifestInput struct {
	// VersionID 是版本目录名。
	VersionID string
	// VersionDir 是版本目录相对核心根的路径（`versions/<id>` 或 `loaders/<id>`）。
	VersionDir string
	// Identity 是安装身份字符串（`forge:26.3:66.1.0`）。
	Identity string
	// LoaderKind / LoaderVersion / MCVersion 用于排查与展示。
	LoaderKind    string
	LoaderVersion string
	MCVersion     string
	// RecipeFingerprint 是安装配方指纹。
	RecipeFingerprint string
	// Downloaded 是下载流程产出的相对核心根的路径。
	Downloaded []string
	// VersionJSON 是版本链上每个版本 JSON 的相对路径。
	VersionJSON []string
	// AssetIndex 是资源索引的相对路径；为空表示没有资源索引。
	AssetIndex string
	// AssetObjects 是资源对象的相对路径。
	AssetObjects []string
	// ProcessorOutputs 是加载器安装器额外生成的产物相对路径
	// （例如 Forge 的 `-client.jar`）。这些文件无法从下载计划推导，
	// 由调用方在安装后扫描版本目录得到。
	ProcessorOutputs []string
}

// buildCoreManifest 把安装产物汇总为清单。
//
// 任一文件缺失或无法读取都会返回错误：此时整次安装应判定为失败，
// 而不是写出一份「部分记录」的清单——那会让下次启动跳过重装。
func buildCoreManifest(root string, input manifestInput) (core.Manifest, error) {
	manifest := core.Manifest{
		SchemaVersion:     2,
		Identity:          input.Identity,
		VersionID:         input.VersionID,
		VersionDir:        input.VersionDir,
		LoaderKind:        input.LoaderKind,
		LoaderVersion:     input.LoaderVersion,
		MCVersion:         input.MCVersion,
		RecipeFingerprint: input.RecipeFingerprint,
		Platform:          platformInfo(),
		InstalledAtMs:     nowMillis(),
	}

	appendFiles := func(paths []string, group string) error {
		seen := make(map[string]struct{}, len(paths))
		for _, rel := range paths {
			if rel == "" {
				continue
			}
			if _, exists := seen[rel]; exists {
				continue
			}
			seen[rel] = struct{}{}
			file, err := core.NewManifestFile(root, rel, group)
			if err != nil {
				return err
			}
			manifest.Files = append(manifest.Files, file)
		}
		return nil
	}

	// 版本 JSON 单独成组：它是判断「版本是否存在」的最快依据。
	if err := appendFiles(input.VersionJSON, core.GroupVersionJSON); err != nil {
		return core.Manifest{}, err
	}
	if err := appendFiles(input.Downloaded, core.GroupLibrary); err != nil {
		return core.Manifest{}, err
	}
	if err := appendFiles(input.ProcessorOutputs, core.GroupProcessor); err != nil {
		return core.Manifest{}, err
	}

	if input.AssetIndex != "" {
		indexFile, indexErr := core.NewManifestFile(root, input.AssetIndex, core.GroupLibrary)
		if indexErr != nil {
			return core.Manifest{}, indexErr
		}
		objects, objectErr := manifestFiles(root, input.AssetObjects, core.GroupLibrary)
		if objectErr != nil {
			return core.Manifest{}, objectErr
		}
		if len(objects) == 0 {
			// 没有对象说明索引为空或尚未展开；此时不写资源段，
			// 避免把一份空的资源清单当作完整。
			manifest.Assets = nil
		} else {
			manifest.Assets = &core.AssetManifest{
				IndexID: assetIndexIDFromPath(input.AssetIndex),
				Index:   indexFile,
				Objects: objects,
			}
		}
	}

	return manifest, nil
}

// manifestFiles 为一批路径构造清单条目。
func manifestFiles(root string, paths []string, group string) ([]core.ManifestFile, error) {
	result := make([]core.ManifestFile, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, rel := range paths {
		if rel == "" {
			continue
		}
		if _, exists := seen[rel]; exists {
			continue
		}
		seen[rel] = struct{}{}
		file, err := core.NewManifestFile(root, rel, group)
		if err != nil {
			return nil, err
		}
		result = append(result, file)
	}
	return result, nil
}

// scanDirectoryFiles 曾用于把 natives 解压产物登记进核心清单。
//
// natives 自清单 schema v2 起移到实例目录（`instances/<id>/.minecraft/natives`），
// 不再属于核心：它会随实例删除而一并回收，也不参与核心重建。因此这里不再扫描。

// assetIndexIDFromPath 从 `assets/indexes/<id>.json` 反推索引标识。
func assetIndexIDFromPath(rel string) string {
	base := filepath.Base(rel)
	return strings.TrimSuffix(base, filepath.Ext(base))
}
