// 本文件实现确定性的安装计划（InstallPlan）构建与 plan_hash 计算。
//
// 设计要点（对齐 P3-02 卡面与决策记录 D-26）：
//   - 计划不落库、不写盘、不下载，只是一份纯数据；
//   - plan_hash 只由「版本 id + 解析器版本 + 制品内容身份 + 告警码」决定，
//     同一份元数据重复解析必须得到同一哈希；
//   - 参与哈希的字段按稳定顺序序列化：制品按 (targetRelPath, kind, logicalKey) 排序，
//     告警按 code 排序去重，不含时间戳、随机值、map 迭代顺序与绝对路径。
package version

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"

	"example.com/ninevoxel/launcher/internal/artifact"
)

// ResolverVersion 是安装计划解析器的版本标识。
//
// 语义变更（新增/删除制品字段、改变合并或路径规则）时必须递增，
// 它参与 plan_hash，使旧计划与新计划不会互相冒充。
const ResolverVersion = "p3-02.1"

// installMaxArtifactBytes 是单个制品的期望大小上限（1 GiB）。
//
// 真实的客户端 jar 与资源索引远小于该值；超出即视为恶意或损坏元数据。
const installMaxArtifactBytes = 1 << 30

// 安装计划使用的告警码：供前端按码提示，也参与 plan_hash。
const (
	// WarningClientDownloadMissing 表示版本缺少可下载的客户端 jar 描述。
	WarningClientDownloadMissing = "CLIENT_DOWNLOAD_MISSING"

	// WarningAssetIndexMissing 表示版本缺少资源索引描述。
	WarningAssetIndexMissing = "ASSET_INDEX_MISSING"

	// WarningLibrarySkipped 表示某个库因缺少下载信息或地址不可信被跳过。
	WarningLibrarySkipped = "LIBRARY_SKIPPED"

	// WarningNativeSkipped 表示某个本地库因缺少 classifier 制品被跳过。
	WarningNativeSkipped = "NATIVE_SKIPPED"

	// WarningJavaVersionUnknown 表示元数据未声明 Java 版本要求。
	WarningJavaVersionUnknown = "JAVA_VERSION_UNKNOWN"

	// WarningInheritParentMissing 表示继承链中的父版本缺失。
	WarningInheritParentMissing = "INHERIT_PARENT_MISSING"

	// WarningInheritTooDeep 表示继承层级超过上限。
	WarningInheritTooDeep = "INHERIT_TOO_DEEP"
)

// planArtifact 是安装计划中的单个制品（内部形态，含 sourceUri）。
type planArtifact struct {
	// Kind 为制品类别（client/library/native/asset/logging）。
	Kind string

	// LogicalKey 为逻辑键（库坐标、索引 id、日志配置 id 等）。
	LogicalKey string

	// Version 为归属的版本 id。
	Version string

	// SourceID 为来源标识（本卡恒为官方源）。
	SourceID string

	// SourceURI 为下载地址，只在后端流转，不进入前端 DTO。
	SourceURI string

	// ExpectedSize 为期望字节数。
	ExpectedSize int64

	// SHA1 为内容摘要（P3 一律要求）。
	SHA1 string

	// SHA256 为可选的第二摘要（版本元数据不提供，留空）。
	SHA256 string

	// TargetRelPath 为由 artifact 包生成的目标相对路径。
	TargetRelPath string

	// Immutable 表示内容寻址制品不可变。
	Immutable bool

	// Extract 表示该制品需要解压（旧格式本地库）。
	Extract bool

	// NativesClassifier 为本地库的 classifier（仅 native 制品）。
	NativesClassifier string

	// ExtractExclude 为解压时必须排除的条目（原样保留自元数据）。
	ExtractExclude []string
}

// planWarning 是计划中的非致命告警。
type planWarning struct {
	// Code 为稳定告警码。
	Code string

	// Message 为面向用户的中文说明（不含路径与地址）。
	Message string
}

// compatLayers 是四层兼容状态（对外映射为 CompatLayers）。
type compatLayers struct {
	// MetadataSupported 表示版本元数据可读。
	MetadataSupported bool

	// InstallSupported 表示可生成完整安装计划。
	InstallSupported bool

	// LaunchSupported 表示可用于启动。
	LaunchSupported bool

	// LoaderSupported 表示支持加载器安装（P3 恒为 false）。
	LoaderSupported bool
}

// javaRequirement 是 Java 版本要求（对外映射为 RuntimeRequirement）。
type javaRequirement struct {
	// Major 为主版本号，0 表示未知。
	Major int

	// Component 为组件标识。
	Component string

	// Platform 为平台名。
	Platform string

	// Arch 为架构名。
	Arch string

	// Source 为取值来源（metadata/unknown）。
	Source string
}

// installPlan 是完整的安装计划（内部形态）。
type installPlan struct {
	// VersionID 为版本标识。
	VersionID string

	// ResolverVersion 为解析器版本。
	ResolverVersion string

	// PlanHash 为计划哈希。
	PlanHash string

	// GeneratedAtMs 为生成时间（不参与哈希）。
	GeneratedAtMs int64

	// Artifacts 为已排序的制品列表。
	Artifacts []planArtifact

	// Warnings 为已排序去重的告警列表。
	Warnings []planWarning

	// Compatibility 为四层兼容状态。
	Compatibility compatLayers

	// Status 为兼容状态词（launch_supported/metadata_only/unsupported）。
	Status string

	// JavaVersion 为 Java 版本要求。
	JavaVersion javaRequirement

	// TotalBytes 为全部制品期望大小之和。
	TotalBytes int64

	// ArtifactCount 为制品数量。
	ArtifactCount int

	// MetadataSize 为版本 JSON 文件的字节数。
	MetadataSize int64

	// VersionType 为版本类型（release/snapshot/old_beta/old_alpha）。
	VersionType string

	// ReleaseTime 为发布时间文本。
	ReleaseTime string
}

// buildInstallPlan 读取版本元数据并构建确定性安装计划。
//
// 参数 root 为数据根绝对路径，versionID 为版本标识。
// 元数据不可信（读取失败、结构非法、继承成环）时返回 VERSION_JSON_001；
// 可读但关键字段缺失时仍返回计划，只是 installSupported 为 false 并带告警。
// 本函数不写盘、不下载、不访问网络。
func buildInstallPlan(root, versionID string) (*installPlan, *AppError) {
	merged, loadStatus, warnings, loadErr := loadInstallVersion(root, versionID)
	if loadErr != nil {
		return nil, loadErr
	}
	if merged == nil {
		return nil, newInputError(ErrorCodeArtifactPlan, messageArtifactPlanUnsupported)
	}

	facts := currentPlatformFacts()
	plan := &installPlan{
		VersionID:       merged.ID,
		ResolverVersion: ResolverVersion,
		GeneratedAtMs:   nowFunc().UnixMilli(),
		Warnings:        warnings,
		MetadataSize:    merged.JSONSize,
		VersionType:     merged.Type,
		ReleaseTime:     merged.ReleaseTime,
		JavaVersion: javaRequirement{
			Platform: facts.OSName,
			Arch:     firstArchName(facts),
			Source:   "unknown",
		},
	}

	clientReady := appendClientArtifact(plan, merged)
	assetReady := appendAssetIndexArtifact(plan, merged)
	appendLibraryArtifacts(plan, merged, facts)
	appendLoggingArtifact(plan, merged)

	plan.Compatibility = buildCompatLayers(loadStatus, clientReady, assetReady, merged)
	plan.Status = compatStatusWord(plan.Compatibility)
	plan.JavaVersion = buildJavaRequirement(plan, merged, facts)

	plan.Artifacts = sortPlanArtifacts(plan.Artifacts)
	plan.Warnings = normalizePlanWarnings(plan.Warnings)
	plan.ArtifactCount = len(plan.Artifacts)
	plan.TotalBytes = 0
	for _, item := range plan.Artifacts {
		plan.TotalBytes += item.ExpectedSize
	}
	plan.PlanHash = computePlanHash(plan)

	return plan, nil
}

// appendClientArtifact 追加客户端 jar 制品，并报告客户端是否可用于安装。
//
// 缺少 `downloads.client` 时按卡面回退到 `versions/<id>/<id>.jar`，
// 但该文件既无地址也无哈希，无法进入计划，因此只给告警并返回 false。
func appendClientArtifact(plan *installPlan, merged *installMergedVersion) bool {
	entry := merged.ClientDownload
	if entry == nil {
		plan.Warnings = append(plan.Warnings, planWarning{
			Code:    WarningClientDownloadMissing,
			Message: fmt.Sprintf("版本「%s」缺少客户端 jar 的下载信息，无法安装。", truncateForMessage(merged.ID)),
		})
		return false
	}

	if reason := validatePlanEntry(entry); reason != "" {
		plan.Warnings = append(plan.Warnings, planWarning{
			Code:    WarningClientDownloadMissing,
			Message: fmt.Sprintf("版本「%s」的客户端 jar 信息不可用：%s", truncateForMessage(merged.ID), reason),
		})
		return false
	}

	// 客户端 jar 永远属于**原版**版本目录：加载器版本的声明继承自原版，
	// 它自己没有独立的客户端 jar。按 ClientJarID 定位既符合官方 `jar` 字段
	// 语义，也避免把原版 jar 复制进 `loaders/`。
	jarFile := merged.ClientJarID + ".jar"
	relPath, pathErr := artifact.ResolveTarget(artifact.KindClient, merged.ClientJarID+"/"+jarFile)
	if pathErr != nil {
		plan.Warnings = append(plan.Warnings, planWarning{
			Code:    WarningClientDownloadMissing,
			Message: fmt.Sprintf("版本「%s」的客户端 jar 目标路径非法，已跳过。", truncateForMessage(merged.ID)),
		})
		return false
	}

	plan.Artifacts = append(plan.Artifacts, planArtifact{
		Kind:          string(artifact.KindClient),
		LogicalKey:    "client/" + jarFile,
		Version:       merged.ID,
		SourceID:      officialSourceID,
		SourceURI:     entry.URL,
		ExpectedSize:  entry.Size,
		SHA1:          strings.ToLower(entry.SHA1),
		TargetRelPath: relPath,
		Immutable:     true,
	})
	return true
}

// appendAssetIndexArtifact 追加资源索引制品，并报告索引是否可用于安装。
func appendAssetIndexArtifact(plan *installPlan, merged *installMergedVersion) bool {
	index := merged.AssetIndex
	if index == nil || strings.TrimSpace(index.ID) == "" {
		plan.Warnings = append(plan.Warnings, planWarning{
			Code:    WarningAssetIndexMissing,
			Message: fmt.Sprintf("版本「%s」缺少资源索引描述，无法安装。", truncateForMessage(merged.ID)),
		})
		return false
	}

	entry := &installDownloadEntryJSON{SHA1: index.SHA1, Size: index.Size, URL: index.URL}
	if reason := validatePlanEntry(entry); reason != "" {
		plan.Warnings = append(plan.Warnings, planWarning{
			Code:    WarningAssetIndexMissing,
			Message: fmt.Sprintf("版本「%s」的资源索引信息不可用：%s", truncateForMessage(merged.ID), reason),
		})
		return false
	}

	relPath, pathErr := artifact.AssetIndexRelPath(index.ID)
	if pathErr != nil {
		plan.Warnings = append(plan.Warnings, planWarning{
			Code:    WarningAssetIndexMissing,
			Message: fmt.Sprintf("版本「%s」的资源索引标识非法，已跳过。", truncateForMessage(merged.ID)),
		})
		return false
	}

	plan.Artifacts = append(plan.Artifacts, planArtifact{
		Kind:          string(artifact.KindAsset),
		LogicalKey:    "assetIndex/" + index.ID,
		Version:       merged.ID,
		SourceID:      officialSourceID,
		SourceURI:     index.URL,
		ExpectedSize:  index.Size,
		SHA1:          strings.ToLower(index.SHA1),
		TargetRelPath: relPath,
		Immutable:     true,
	})
	return true
}

// appendLibraryArtifacts 追加库与本地库制品。
//
// 规则与 P2 一致：先按 rules 过滤平台，再取 `downloads.artifact`；
// 有 natives 映射时额外取对应 classifier 制品，并保留 `extract.exclude`。
// 缺少地址、大小或哈希的库按卡面跳过并告警，不让整单失败。
func appendLibraryArtifacts(plan *installPlan, merged *installMergedVersion, facts installPlatformFacts) {
	for _, lib := range merged.Libraries {
		coordinate := strings.TrimSpace(lib.Name)
		if coordinate == "" {
			continue
		}
		if !libraryAppliesToPlatform(lib.Rules, facts) {
			continue
		}
		if lib.Downloads == nil {
			plan.Warnings = append(plan.Warnings, planWarning{
				Code:    WarningLibrarySkipped,
				Message: fmt.Sprintf("库「%s」缺少下载信息，已跳过。", coordinate),
			})
			continue
		}

		if lib.Downloads.Artifact != nil {
			if reason := validatePlanEntry(lib.Downloads.Artifact); reason != "" {
				plan.Warnings = append(plan.Warnings, planWarning{
					Code:    WarningLibrarySkipped,
					Message: fmt.Sprintf("库「%s」的下载信息不可用：%s", coordinate, reason),
				})
			} else if item, pathErr := mavenPlanArtifact(string(artifact.KindLibrary), coordinate, merged.ID, lib.Downloads.Artifact); pathErr == nil {
				plan.Artifacts = append(plan.Artifacts, item)
			} else {
				plan.Warnings = append(plan.Warnings, planWarning{
					Code:    WarningLibrarySkipped,
					Message: fmt.Sprintf("库「%s」的内容地址无法映射到制品路径，已跳过。", coordinate),
				})
			}
		}

		classifier := selectNativeClassifier(lib, facts)
		if classifier == "" {
			continue
		}
		entry, ok := lib.Downloads.Classifiers[classifier]
		if !ok {
			plan.Warnings = append(plan.Warnings, planWarning{
				Code:    WarningNativeSkipped,
				Message: fmt.Sprintf("库「%s」声明了本地库 %s，但缺少对应制品，已跳过。", coordinate, classifier),
			})
			continue
		}
		if reason := validatePlanEntry(&entry); reason != "" {
			plan.Warnings = append(plan.Warnings, planWarning{
				Code:    WarningNativeSkipped,
				Message: fmt.Sprintf("库「%s」的本地库 %s 信息不可用：%s", coordinate, classifier, reason),
			})
			continue
		}
		item, pathErr := mavenPlanArtifact(string(artifact.KindNative), coordinate+":"+classifier, merged.ID, &entry)
		if pathErr != nil {
			plan.Warnings = append(plan.Warnings, planWarning{
				Code:    WarningNativeSkipped,
				Message: fmt.Sprintf("库「%s」的本地库内容地址无法映射到制品路径，已跳过。", coordinate),
			})
			continue
		}
		item.NativesClassifier = classifier
		item.Extract = true
		if lib.Extract != nil {
			item.ExtractExclude = append([]string(nil), lib.Extract.Exclude...)
		}
		plan.Artifacts = append(plan.Artifacts, item)
	}
}

// appendLoggingArtifact 追加日志配置文件制品（可选）。
func appendLoggingArtifact(plan *installPlan, merged *installMergedVersion) {
	if merged.Logging == nil || merged.Logging.Client == nil || merged.Logging.Client.File == nil {
		return
	}
	file := merged.Logging.Client.File
	entry := &installDownloadEntryJSON{SHA1: file.SHA1, Size: file.Size, URL: file.URL}
	if strings.TrimSpace(file.ID) == "" {
		return
	}
	if reason := validatePlanEntry(entry); reason != "" {
		plan.Warnings = append(plan.Warnings, planWarning{
			Code:    WarningLibrarySkipped,
			Message: fmt.Sprintf("日志配置「%s」的信息不可用：%s", truncateForMessage(file.ID), reason),
		})
		return
	}
	relPath, pathErr := artifact.LogConfigRelPath(file.ID)
	if pathErr != nil {
		plan.Warnings = append(plan.Warnings, planWarning{
			Code:    WarningLibrarySkipped,
			Message: fmt.Sprintf("日志配置「%s」的目标路径非法，已跳过。", truncateForMessage(file.ID)),
		})
		return
	}
	plan.Artifacts = append(plan.Artifacts, planArtifact{
		Kind:          "logging",
		LogicalKey:    "logConfig/" + file.ID,
		Version:       merged.ID,
		SourceID:      officialSourceID,
		SourceURI:     entry.URL,
		ExpectedSize:  entry.Size,
		SHA1:          strings.ToLower(entry.SHA1),
		TargetRelPath: relPath,
		Immutable:     true,
	})
}

// mavenPlanArtifact 用 Maven 布局路径构造一个依赖库制品。
//
// 下载直接落在 `libraries/<maven path>`：这是 classpath 实际引用的位置，
// 不再经过内容寻址缓存后再复制一层。
func mavenPlanArtifact(kind, logicalKey, versionID string, entry *installDownloadEntryJSON) (planArtifact, *artifact.Error) {
	relPath, pathErr := artifact.LibraryRelPath(mavenPathFor(entry, logicalKey))
	if pathErr != nil {
		return planArtifact{}, pathErr
	}
	return planArtifact{
		Kind:          kind,
		LogicalKey:    logicalKey,
		Version:       versionID,
		SourceID:      officialSourceID,
		SourceURI:     entry.URL,
		ExpectedSize:  entry.Size,
		SHA1:          strings.ToLower(entry.SHA1),
		TargetRelPath: relPath,
		Immutable:     true,
	}, nil
}

// mavenPathFor 返回制品在 Maven 布局中的相对路径。
//
// 优先使用版本 JSON 声明的 `downloads.artifact.path`；旧版本或部分第三方库
// 不声明该字段，此时按 Maven 坐标推导。推导失败返回空串，由调用方转成
// 「目标路径非法」的可读告警而不是崩溃。
func mavenPathFor(entry *installDownloadEntryJSON, coordinate string) string {
	if declared := strings.TrimSpace(entry.Path); declared != "" {
		return declared
	}
	parts := strings.Split(coordinate, ":")
	if len(parts) < 3 {
		return ""
	}
	fileName := parts[1] + "-" + parts[2]
	if len(parts) > 3 && parts[3] != "" {
		fileName += "-" + parts[3]
	}
	return path.Join(strings.ReplaceAll(parts[0], ".", "/"), parts[1], parts[2], fileName+".jar")
}

// objectPlanArtifact 用内容寻址路径构造一个制品。
//
// 仅用于资源对象：它们天然按内容寻址，多个版本引用同一对象时路径相同。
func objectPlanArtifact(kind, logicalKey, versionID string, entry *installDownloadEntryJSON) (planArtifact, *artifact.Error) {
	relPath, pathErr := artifact.AssetObjectRelPath(strings.ToLower(entry.SHA1))
	if pathErr != nil {
		return planArtifact{}, pathErr
	}
	return planArtifact{
		Kind:          kind,
		LogicalKey:    logicalKey,
		Version:       versionID,
		SourceID:      officialSourceID,
		SourceURI:     entry.URL,
		ExpectedSize:  entry.Size,
		SHA1:          strings.ToLower(entry.SHA1),
		TargetRelPath: relPath,
		Immutable:     true,
	}, nil
}

// validatePlanEntry 校验单个下载描述：地址可信、大小合理、SHA-1 合法。
//
// 返回值是面向用户的中文原因；空串表示通过。
func validatePlanEntry(entry *installDownloadEntryJSON) string {
	if entry == nil {
		return "缺少下载描述"
	}
	if strings.TrimSpace(entry.URL) == "" {
		return "缺少下载地址"
	}
	if urlErr := validateManifestURL(entry.URL); urlErr != nil {
		return "下载地址不在允许的来源列表中"
	}
	if entry.Size <= 0 {
		return "文件大小非法"
	}
	if entry.Size > installMaxArtifactBytes {
		return "文件大小超出上限"
	}
	if !isSHA1Hex(strings.ToLower(entry.SHA1)) {
		return "SHA-1 摘要格式非法"
	}
	return ""
}

// officialSourceID 是官方来源的标识（本卡只使用官方源）。
const officialSourceID = "mojang"

// firstArchName 返回首个架构别名（用于 Java 版本要求展示）。
func firstArchName(facts installPlatformFacts) string {
	if len(facts.ArchNames) == 0 {
		return ""
	}
	return facts.ArchNames[0]
}

// buildCompatLayers 计算四层兼容状态。
//
// 口径（与接口冻结清单 §2.4 的语义一致）：
//   - metadataSupported：元数据可解析（走到这里即为 true）；
//   - installSupported：客户端 jar 与资源索引齐全，且继承链完整；
//   - launchSupported：可安装且声明了 mainClass；
//   - loaderSupported：P3 恒为 false。
func buildCompatLayers(loadStatus installLoadStatus, clientReady, assetReady bool, merged *installMergedVersion) compatLayers {
	layers := compatLayers{MetadataSupported: true}
	chainComplete := loadStatus != installStatusUnsupported && loadStatus != installStatusBroken
	layers.InstallSupported = clientReady && assetReady && chainComplete
	layers.LaunchSupported = layers.InstallSupported && strings.TrimSpace(merged.MainClass) != ""
	layers.LoaderSupported = false
	return layers
}

// compatStatusWord 把四层状态归约为状态词。
func compatStatusWord(layers compatLayers) string {
	switch {
	case !layers.MetadataSupported:
		return "broken"
	case !layers.InstallSupported:
		return "unsupported"
	case !layers.LaunchSupported:
		return "metadata_only"
	default:
		return "launch_supported"
	}
}

// buildJavaRequirement 计算 Java 版本要求；元数据缺失时不猜测，置 0 并告警。
func buildJavaRequirement(plan *installPlan, merged *installMergedVersion, facts installPlatformFacts) javaRequirement {
	requirement := plan.JavaVersion
	if merged.JavaMajorVersion > 0 {
		requirement.Major = merged.JavaMajorVersion
		requirement.Component = merged.JavaComponent
		requirement.Source = "metadata"
		return requirement
	}
	plan.Warnings = append(plan.Warnings, planWarning{
		Code:    WarningJavaVersionUnknown,
		Message: "版本元数据未声明 Java 版本要求，安装时需由用户确认。",
	})
	requirement.Source = "unknown"
	return requirement
}

// sortPlanArtifacts 按稳定顺序排序制品。
//
// 排序键为 (targetRelPath, kind, logicalKey, sha1)：前者决定落盘顺序，
// 后者在极端情况（同一路径重复登记）下保证结果唯一，从而使 plan_hash 稳定。
func sortPlanArtifacts(items []planArtifact) []planArtifact {
	if len(items) == 0 {
		return items
	}
	sorted := make([]planArtifact, len(items))
	copy(sorted, items)
	sort.SliceStable(sorted, func(left, right int) bool {
		if sorted[left].TargetRelPath != sorted[right].TargetRelPath {
			return sorted[left].TargetRelPath < sorted[right].TargetRelPath
		}
		if sorted[left].Kind != sorted[right].Kind {
			return sorted[left].Kind < sorted[right].Kind
		}
		if sorted[left].LogicalKey != sorted[right].LogicalKey {
			return sorted[left].LogicalKey < sorted[right].LogicalKey
		}
		return sorted[left].SHA1 < sorted[right].SHA1
	})
	return sorted
}

// normalizePlanWarnings 按 code 排序并去重告警。
//
// 去重键为 (code, message)：同一码的相同说明只保留一条，
// 避免继承链重复声明同一库时告警数量随层级增长而抖动。
func normalizePlanWarnings(items []planWarning) []planWarning {
	if len(items) == 0 {
		return items
	}
	normalized := make([]planWarning, len(items))
	copy(normalized, items)
	sort.SliceStable(normalized, func(left, right int) bool {
		if normalized[left].Code != normalized[right].Code {
			return normalized[left].Code < normalized[right].Code
		}
		return normalized[left].Message < normalized[right].Message
	})

	unique := normalized[:0]
	var previous planWarning
	for index, item := range normalized {
		if index > 0 && item.Code == previous.Code && item.Message == previous.Message {
			continue
		}
		unique = append(unique, item)
		previous = item
	}
	return unique
}

// canonicalPlan 是参与哈希的规范化计划视图。
//
// 刻意**排除**：generatedAtMs（时间戳）、sourceUri（可被镜像重写）、
// metadataSize（与内容身份无关）。字段顺序即 JSON 序列化顺序。
type canonicalPlan struct {
	VersionID       string              `json:"versionId"`
	ResolverVersion string              `json:"resolverVersion"`
	ArtifactCount   int                 `json:"artifactCount"`
	TotalBytes      int64               `json:"totalBytes"`
	Artifacts       []canonicalArtifact `json:"artifacts"`
	Warnings        []canonicalWarning  `json:"warnings"`
	Compatibility   canonicalCompat     `json:"compatibility"`
	JavaVersion     canonicalJava       `json:"javaVersion"`
}

// canonicalArtifact 是参与哈希的制品视图（制品内容身份的全部字段）。
type canonicalArtifact struct {
	Kind              string   `json:"kind"`
	LogicalKey        string   `json:"logicalKey"`
	Version           string   `json:"version"`
	SourceID          string   `json:"sourceId"`
	ExpectedSize      int64    `json:"expectedSize"`
	SHA1              string   `json:"sha1"`
	SHA256            string   `json:"sha256,omitempty"`
	TargetRelPath     string   `json:"targetRelPath"`
	Immutable         bool     `json:"immutable"`
	Extract           bool     `json:"extract"`
	NativesClassifier string   `json:"nativesClassifier,omitempty"`
	ExtractExclude    []string `json:"extractExclude,omitempty"`
}

// canonicalWarning 是参与哈希的告警视图（只含码，不含说明文本）。
type canonicalWarning struct {
	Code string `json:"code"`
}

// canonicalCompat 是参与哈希的兼容状态视图。
type canonicalCompat struct {
	MetadataSupported bool `json:"metadataSupported"`
	InstallSupported  bool `json:"installSupported"`
	LaunchSupported   bool `json:"launchSupported"`
	LoaderSupported   bool `json:"loaderSupported"`
}

// canonicalJava 是参与哈希的 Java 要求视图。
type canonicalJava struct {
	Major     int    `json:"major"`
	Component string `json:"component,omitempty"`
	Platform  string `json:"platform"`
	Arch      string `json:"arch"`
	Source    string `json:"source"`
}

// computePlanHash 计算计划哈希。
//
// 算法：sha256(版本 id + "\n" + 解析器版本 + "\n" + 规范化 JSON)。
// 前缀使不同版本/不同解析器的同一份内容也不会得到相同哈希；
// 规范化 JSON 由稳定排序的结果序列化而来，因此同一输入必然得到同一哈希。
func computePlanHash(plan *installPlan) string {
	canonical := canonicalPlan{
		VersionID:       plan.VersionID,
		ResolverVersion: plan.ResolverVersion,
		ArtifactCount:   plan.ArtifactCount,
		TotalBytes:      plan.TotalBytes,
		Compatibility: canonicalCompat{
			MetadataSupported: plan.Compatibility.MetadataSupported,
			InstallSupported:  plan.Compatibility.InstallSupported,
			LaunchSupported:   plan.Compatibility.LaunchSupported,
			LoaderSupported:   plan.Compatibility.LoaderSupported,
		},
		JavaVersion: canonicalJava{
			Major:     plan.JavaVersion.Major,
			Component: plan.JavaVersion.Component,
			Platform:  plan.JavaVersion.Platform,
			Arch:      plan.JavaVersion.Arch,
			Source:    plan.JavaVersion.Source,
		},
	}

	for _, item := range plan.Artifacts {
		canonical.Artifacts = append(canonical.Artifacts, canonicalArtifact{
			Kind:              item.Kind,
			LogicalKey:        item.LogicalKey,
			Version:           item.Version,
			SourceID:          item.SourceID,
			ExpectedSize:      item.ExpectedSize,
			SHA1:              item.SHA1,
			SHA256:            item.SHA256,
			TargetRelPath:     item.TargetRelPath,
			Immutable:         item.Immutable,
			Extract:           item.Extract,
			NativesClassifier: item.NativesClassifier,
			ExtractExclude:    item.ExtractExclude,
		})
	}
	for _, warning := range plan.Warnings {
		canonical.Warnings = append(canonical.Warnings, canonicalWarning{Code: warning.Code})
	}

	encoded, err := json.Marshal(canonical)
	if err != nil {
		// 结构体只含可序列化字段，此处不可达；返回空哈希让上层显式到错误。
		return ""
	}
	prefix := plan.VersionID + "\n" + plan.ResolverVersion + "\n"
	sum := sha256.Sum256(append([]byte(prefix), encoded...))
	return hex.EncodeToString(sum[:])
}

// toSummary 把计划转换为摘要。
func (p *installPlan) toSummary() InstallPlanSummary {
	return InstallPlanSummary{
		VersionID:       p.VersionID,
		ResolverVersion: p.ResolverVersion,
		PlanHash:        p.PlanHash,
		ArtifactCount:   p.ArtifactCount,
		TotalBytes:      p.TotalBytes,
		GeneratedAtMs:   p.GeneratedAtMs,
	}
}

// toCompatReport 把计划转换为兼容状态报告。
func (p *installPlan) toCompatReport() CompatReport {
	return CompatReport{
		VersionID: p.VersionID,
		Layers: CompatLayers{
			MetadataSupported: p.Compatibility.MetadataSupported,
			InstallSupported:  p.Compatibility.InstallSupported,
			LaunchSupported:   p.Compatibility.LaunchSupported,
			LoaderSupported:   p.Compatibility.LoaderSupported,
		},
		Status: p.Status,
	}
}

// toRuntimeRequirement 把 Java 要求转换为对外 DTO。
func (p *installPlan) toRuntimeRequirement() RuntimeRequirement {
	return RuntimeRequirement{
		Major:     p.JavaVersion.Major,
		Component: p.JavaVersion.Component,
		Platform:  p.JavaVersion.Platform,
		Arch:      p.JavaVersion.Arch,
		Source:    p.JavaVersion.Source,
	}
}

// toWarningsDTO 把内部告警转换为对外 DTO 列表。
func (p *installPlan) toWarningsDTO() []PlanWarning {
	if len(p.Warnings) == 0 {
		return []PlanWarning{}
	}
	items := make([]PlanWarning, 0, len(p.Warnings))
	for _, warning := range p.Warnings {
		items = append(items, PlanWarning{Code: warning.Code, Message: warning.Message})
	}
	return items
}

// toDTO 把计划转换为对外的安装计划 DTO。
//
// 安全口径（接口冻结清单 §2.1）：sourceUri 仅供后端下载使用，
// 不在面向前端的 DTO 中返回，因此这里留空而不拷贝内部地址。
func (p *installPlan) toDTO() InstallPlanDTO {
	artifacts := make([]PlanArtifactView, 0, len(p.Artifacts))
	for _, item := range p.Artifacts {
		artifacts = append(artifacts, PlanArtifactView{
			Kind:              item.Kind,
			LogicalKey:        item.LogicalKey,
			Version:           item.Version,
			SourceID:          item.SourceID,
			SourceURI:         "",
			ExpectedSize:      item.ExpectedSize,
			Hashes:            PlanArtifactHashes{SHA1: item.SHA1, SHA256: item.SHA256},
			TargetRelPath:     item.TargetRelPath,
			Immutable:         item.Immutable,
			Extract:           item.Extract,
			NativesClassifier: item.NativesClassifier,
			ExtractExclude:    append([]string(nil), item.ExtractExclude...),
		})
	}
	return InstallPlanDTO{
		Summary:       p.toSummary(),
		Artifacts:     artifacts,
		Warnings:      p.toWarningsDTO(),
		Compatibility: p.toCompatReport(),
	}
}

// getVersionDetail 解析版本详情。
//
// 步骤：解析数据根 → 确保版本元数据就位（必要时按需获取）→ 读取并合并版本元数据
// （含继承链）→ 构建安装计划 → 组装 DTO。
// 元数据不可信时返回 VERSION_JSON_001；数据根不可用时返回 VERSION_LAYOUT_001；
// 清单缓存不可用（无法得知下载地址）时返回可重试的 VERSION_MANIFEST_001。
func getVersionDetail(req VersionDetailRequest) (VersionDetail, *AppError) {
	root, rootErr := resolveDataRoot()
	if rootErr != nil {
		return VersionDetail{}, rootErr
	}
	if ensureErr := ensureVersionMetadata(root, req.VersionID); ensureErr != nil {
		return VersionDetail{}, ensureErr
	}
	plan, planErr := buildInstallPlan(root, req.VersionID)
	if planErr != nil {
		return VersionDetail{}, planErr
	}
	return VersionDetail{
		ID:              plan.VersionID,
		Type:            plan.VersionType,
		ReleaseTime:     plan.ReleaseTime,
		Size:            plan.MetadataSize,
		JavaRequirement: plan.toRuntimeRequirement(),
		Plan:            plan.toSummary(),
		Compat:          plan.toCompatReport(),
		Warnings:        plan.toWarningsDTO(),
	}, nil
}

// resolveInstallPlan 解析完整的安装计划。
//
// 与 getVersionDetail 共用同一构建流程（含「确保元数据就位」这一步），
// 保证详情页与计划页看到同一个 plan_hash。
func resolveInstallPlan(req ResolveInstallPlanRequest) (InstallPlanDTO, *AppError) {
	root, rootErr := resolveDataRoot()
	if rootErr != nil {
		return InstallPlanDTO{}, rootErr
	}
	if ensureErr := ensureVersionMetadata(root, req.VersionID); ensureErr != nil {
		return InstallPlanDTO{}, ensureErr
	}
	plan, planErr := buildInstallPlan(root, req.VersionID)
	if planErr != nil {
		return InstallPlanDTO{}, planErr
	}
	return plan.toDTO(), nil
}
