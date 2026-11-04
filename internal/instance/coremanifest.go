package instance

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"example.com/ninevoxel/launcher/internal/artifact"
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
	// NativesDir 是解压后的 natives 目录相对路径；为空表示该版本没有 natives。
	NativesDir string
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
		SchemaVersion:     1,
		Identity:          input.Identity,
		VersionID:         input.VersionID,
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

	natives, err := scanDirectoryFiles(root, input.NativesDir, core.GroupNative)
	if err != nil {
		return core.Manifest{}, err
	}
	manifest.Files = append(manifest.Files, natives...)

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

// scanDirectoryFiles 递归列出目录下的全部普通文件并构造清单条目。
//
// 参数 relDir 为相对核心根的目录；目录不存在时返回空切片（该版本没有 natives
// 是正常情况，不是错误）。
func scanDirectoryFiles(root, relDir, group string) ([]core.ManifestFile, error) {
	if strings.TrimSpace(relDir) == "" {
		return nil, nil
	}
	absolute, resolveErr := artifact.ResolveInsideRoot(root, relDir)
	if resolveErr != nil {
		return nil, errors.New("原生产物目录无效")
	}
	result := make([]core.ManifestFile, 0, 32)
	walkErr := filepath.WalkDir(absolute, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !entry.Type().IsRegular() {
			return nil
		}
		relative, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		file, fileErr := core.NewManifestFile(root, filepath.ToSlash(relative), group)
		if fileErr != nil {
			return fileErr
		}
		result = append(result, file)
		return nil
	})
	if walkErr != nil {
		if os.IsNotExist(walkErr) {
			return nil, nil
		}
		return nil, errors.New("无法扫描原生产物目录")
	}
	return result, nil
}

// assetIndexIDFromPath 从 `assets/indexes/<id>.json` 反推索引标识。
func assetIndexIDFromPath(rel string) string {
	base := filepath.Base(rel)
	return strings.TrimSuffix(base, filepath.Ext(base))
}
