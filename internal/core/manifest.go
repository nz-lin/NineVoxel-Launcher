package core

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"example.com/ninevoxel/launcher/internal/artifact"
)

// manifestFileName 是安装清单的文件名，位于 `versions/<versionId>/` 下。
//
// 以点开头，避免与版本目录中的其他产物混淆，也不会被误认为版本标识。
const manifestFileName = ".nv-core-manifest.json"

// manifestSchemaVersion 是清单结构的版本号。
//
// 清单结构发生不兼容变化时递增；读取到不支持的版本时按「未安装」处理，
// 由调用方触发重新安装。
const manifestSchemaVersion = 1

// 资源分组。清单按组记录，便于修复时精确定位缺哪一类文件。
const (
	// GroupLibrary 是版本 JSON 声明的依赖库与 processor 的 classpath 依赖。
	GroupLibrary = "library"
	// GroupNative 是 natives 解压产物（不是 native JAR 本身）。
	GroupNative = "native"
	// GroupProcessor 是 processor 生成的产物（如 Forge 的 client jar）。
	GroupProcessor = "processor"
	// GroupVersionJSON 是版本元数据文件本身。
	GroupVersionJSON = "versionJson"
)

// ManifestFile 描述一个必需文件。
type ManifestFile struct {
	// Path 是相对核心根的路径，使用 `/` 分隔。
	Path string `json:"path"`
	// Size 是字节数。
	Size int64 `json:"size"`
	// SHA256 是本启动器计算的内容哈希。
	//
	// 不记录上游 SHA-1：上游哈希只用于下载校验，进入核心后统一由 SHA-256
	// 管理完整性，这样没有上游哈希的 processor 产物也能被可靠校验。
	SHA256 string `json:"sha256"`
	// Group 是该文件所属的资源分组，见 Group* 常量。
	Group string `json:"group"`
}

// AssetManifest 描述版本使用的资源索引与对象。
//
// 资源对象数量可达数万，单独成组而不是混进 Files：修复时可按索引整体重下，
// 也便于将来做资源层的独立校验与回收。
type AssetManifest struct {
	// IndexID 是资源索引标识。
	IndexID string `json:"indexId"`
	// Index 是索引文件本身。
	Index ManifestFile `json:"index"`
	// Objects 是索引引用的全部对象文件。
	Objects []ManifestFile `json:"objects"`
}

// Manifest 是某个版本在核心中的完整安装记录。
//
// 它同时是「是否已完整安装」的唯一判据与「缺了什么」的查询依据。
type Manifest struct {
	SchemaVersion int    `json:"schemaVersion"`
	Identity      string `json:"identity"`
	VersionID     string `json:"versionId"`

	LoaderKind    string `json:"loaderKind"`
	LoaderVersion string `json:"loaderVersion"`
	MCVersion     string `json:"minecraftVersion"`

	// RecipeFingerprint 是产出该产物的安装配方指纹。
	RecipeFingerprint string `json:"recipeFingerprint"`
	// PreviousRecipeFingerprint 是被本次安装替换掉的旧配方指纹；首次安装为空。
	//
	// 保留它可以在不引入存储世代的前提下回答「这个版本被哪个配方装过、
	// 是否被替换过」。
	PreviousRecipeFingerprint string `json:"previousRecipeFingerprint,omitempty"`

	Platform      CorePlatform `json:"platform"`
	InstalledAtMs int64        `json:"installedAtMs"`

	// Files 汇总除资源对象外的全部必需文件（含版本 JSON）。
	Files []ManifestFile `json:"files"`
	// Assets 为资源索引与对象；版本未声明资源索引时为空。
	Assets *AssetManifest `json:"assets,omitempty"`
}

// manifestPath 返回清单相对核心根的路径。
func manifestPath(versionID string) string {
	return versionsDirName + "/" + versionID + "/" + manifestFileName
}

// FilesOfGroup 返回指定分组的文件列表。
//
// 供修复流程按资源类别分别处理：库文件可从下载元数据恢复，natives 需重新
// 解压，processor 产物只能重跑官方安装器。
func (m Manifest) FilesOfGroup(group string) []ManifestFile {
	result := make([]ManifestFile, 0, len(m.Files))
	for _, file := range m.Files {
		if file.Group == group {
			result = append(result, file)
		}
	}
	return result
}

// RequiredFiles 返回清单记录的全部文件（含资源）。
func (m Manifest) RequiredFiles() []ManifestFile {
	total := len(m.Files)
	if m.Assets != nil {
		total += 1 + len(m.Assets.Objects)
	}
	result := make([]ManifestFile, 0, total)
	result = append(result, m.Files...)
	if m.Assets != nil {
		result = append(result, m.Assets.Index)
		result = append(result, m.Assets.Objects...)
	}
	return result
}

// Validate 检查清单自身的结构完整性与路径安全性。
//
// 清单可能被人工编辑或磁盘损坏，因此在据此删除、覆盖文件之前必须先校验：
// 路径必须是相对路径、不得含 `..`、不得越出核心根。
func (m Manifest) Validate() error {
	if m.SchemaVersion != manifestSchemaVersion {
		return fmt.Errorf("清单版本不受支持：%d", m.SchemaVersion)
	}
	if strings.TrimSpace(m.Identity) == "" {
		return errors.New("清单缺少安装身份")
	}
	if strings.TrimSpace(m.VersionID) == "" {
		return errors.New("清单缺少版本标识")
	}
	if normalized, appErr := artifact.ValidateRelativePath(m.VersionID); appErr != nil || normalized != m.VersionID || strings.ContainsAny(m.VersionID, `/\:`) {
		return errors.New("清单的版本标识无效")
	}
	for _, file := range m.RequiredFiles() {
		if err := validateManifestFile(file); err != nil {
			return err
		}
	}
	return nil
}

// validateManifestFile 校验单个清单条目。
func validateManifestFile(file ManifestFile) error {
	if file.Path == "" {
		return errors.New("清单包含空路径")
	}
	if hasParentSegment(file.Path) || filepath.IsAbs(file.Path) {
		return fmt.Errorf("清单路径越界：%s", file.Path)
	}
	normalized, appErr := artifact.ValidateRelativePath(file.Path)
	if appErr != nil || normalized != file.Path {
		return fmt.Errorf("清单路径非法：%s", file.Path)
	}
	if file.Size < 0 {
		return fmt.Errorf("清单记录的大小为负：%s", file.Path)
	}
	if len(file.SHA256) != 64 {
		return fmt.Errorf("清单记录的哈希无效：%s", file.Path)
	}
	if _, err := hex.DecodeString(file.SHA256); err != nil {
		return fmt.Errorf("清单记录的哈希无效：%s", file.Path)
	}
	return nil
}

// hasParentSegment 判断路径是否包含 `..` 分量。
func hasParentSegment(path string) bool {
	for _, segment := range strings.FieldsFunc(path, func(r rune) bool { return r == '/' || r == '\\' }) {
		if segment == ".." {
			return true
		}
	}
	return false
}

// ===== 校验 =====

// VerifyLevel 表示完整性校验的深度。
type VerifyLevel int

const (
	// VerifyFast 只比对存在性与大小（L1）。
	//
	// 用于每次安装入口：绝大多数情况下文件都在，这一步只需 stat，成本极低。
	VerifyFast VerifyLevel = iota
	// VerifyFull 在 L1 之上逐文件计算 SHA-256（L2）。
	//
	// 用于修复流程与显式校验：读取全部文件内容，成本与核心体积成正比。
	VerifyFull
)

// VerifyReport 是一次校验的结果。
type VerifyReport struct {
	// Level 为本次校验的深度。
	Level VerifyLevel
	// Checked 为检查的文件数。
	Checked int
	// Missing 为缺失的文件路径（相对核心根）。
	Missing []string
	// SizeMismatch 为大小不符的文件路径。
	SizeMismatch []string
	// HashMismatch 为哈希不符的文件路径；仅在 VerifyFull 时可能非空。
	HashMismatch []string
}

// Complete 表示校验是否全部通过。
func (r VerifyReport) Complete() bool {
	return len(r.Missing) == 0 && len(r.SizeMismatch) == 0 && len(r.HashMismatch) == 0
}

// ProblemFiles 返回全部有问题的文件路径。
func (r VerifyReport) ProblemFiles() []string {
	result := make([]string, 0, len(r.Missing)+len(r.SizeMismatch)+len(r.HashMismatch))
	result = append(result, r.Missing...)
	result = append(result, r.SizeMismatch...)
	result = append(result, r.HashMismatch...)
	return result
}

// verifyManifest 按指定深度校验清单记录的文件。
//
// 参数 root 为已归一化的核心根；manifest 需先通过 Validate。
func verifyManifest(root string, manifest Manifest, level VerifyLevel) (VerifyReport, error) {
	report := VerifyReport{Level: level}
	for _, file := range manifest.RequiredFiles() {
		absolute, resolveErr := artifact.ResolveInsideRoot(root, file.Path)
		if resolveErr != nil {
			return VerifyReport{}, fmt.Errorf("清单路径无法解析：%s", file.Path)
		}
		info, statErr := os.Stat(absolute)
		if statErr != nil {
			if os.IsNotExist(statErr) {
				report.Missing = append(report.Missing, file.Path)
				continue
			}
			return VerifyReport{}, fmt.Errorf("无法读取核心文件：%s", file.Path)
		}
		if !info.Mode().IsRegular() {
			report.Missing = append(report.Missing, file.Path)
			continue
		}
		report.Checked++
		if info.Size() != file.Size {
			report.SizeMismatch = append(report.SizeMismatch, file.Path)
			continue
		}
		if level < VerifyFull {
			continue
		}
		actual, hashErr := hashFile(absolute)
		if hashErr != nil {
			return VerifyReport{}, fmt.Errorf("无法计算核心文件哈希：%s", file.Path)
		}
		if !strings.EqualFold(actual, file.SHA256) {
			report.HashMismatch = append(report.HashMismatch, file.Path)
		}
	}
	return report, nil
}

// hashFile 计算文件的 SHA-256（十六进制小写）。
func hashFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	digest := sha256.New()
	if _, copyErr := io.Copy(digest, file); copyErr != nil {
		return "", copyErr
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

// buildManifestFile 构造一个清单条目。
//
// 参数 root 为核心根，rel 为相对路径。文件必须已存在，否则返回错误：
// 清单只记录确实落盘成功的文件。
func buildManifestFile(root, rel, group string) (ManifestFile, error) {
	absolute, resolveErr := artifact.ResolveInsideRoot(root, rel)
	if resolveErr != nil {
		return ManifestFile{}, fmt.Errorf("清单路径非法：%s", rel)
	}
	info, statErr := os.Stat(absolute)
	if statErr != nil {
		return ManifestFile{}, fmt.Errorf("清单文件不存在：%s", rel)
	}
	if !info.Mode().IsRegular() {
		return ManifestFile{}, fmt.Errorf("清单目标不是普通文件：%s", rel)
	}
	hash, hashErr := hashFile(absolute)
	if hashErr != nil {
		return ManifestFile{}, fmt.Errorf("无法计算清单文件哈希：%s", rel)
	}
	return ManifestFile{Path: rel, Size: info.Size(), SHA256: hash, Group: group}, nil
}
