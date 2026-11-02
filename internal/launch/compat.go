// 本文件实现旧版本兼容性判定：基准版本差异规则、旧格式适配标记与依据生成。
//
// 边界（P3-10 卡面）：
//   - 不改变 P2 已验收的解析与启动路径，所有既有函数只读调用；
//   - 不联网、不下载、不创建文件（P3 不启用 map_to_resources，只做只读判定）；
//   - 判定全部来自「元数据 + 本地文件存在性」两类证据，缺证据时给 unknown 与原因，
//     不按版本号硬编码结论。
package launch

import (
	"encoding/json"
	"os"
	"sort"
	"strconv"
	"strings"
)

// 资源索引布局取值（P3-07 的三种资产布局中的前两种；map_to_resources 只做只读判定）。
const (
	// CompatAssetLayoutVirtual 表示旧式虚拟资源索引（1.6.x 及更早，需按逻辑名组装）。
	CompatAssetLayoutVirtual = "virtual"

	// CompatAssetLayoutObjects 表示现代内容寻址（1.7.3+，artifacts/objects/<前2位>/<hash>）。
	CompatAssetLayoutObjects = "objects"

	// CompatAssetLayoutUnknown 表示元数据未给出资源索引标识，无法判定布局。
	CompatAssetLayoutUnknown = "unknown"
)

// Natives 命名约定取值。
const (
	// CompatNativesLegacy 表示旧式 `natives-windows` 等命名 + extract.exclude。
	CompatNativesLegacy = "legacy"

	// CompatNativesModern 表示现代 classifier（`natives-windows-x86_64` 等）。
	CompatNativesModern = "modern"

	// CompatNativesMixed 表示同一版本内两套约定并存。
	CompatNativesMixed = "mixed"

	// CompatNativesNone 表示该版本不声明 natives。
	CompatNativesNone = "none"
)

// 启动参数格式取值。
const (
	// CompatArgumentsLegacy 表示只有旧式 `minecraftArguments` 单串。
	CompatArgumentsLegacy = "minecraft_arguments"

	// CompatArgumentsModern 表示只有新式 `arguments` 结构。
	CompatArgumentsModern = "arguments"

	// CompatArgumentsBoth 表示两套字段同时存在。
	CompatArgumentsBoth = "both"

	// CompatArgumentsNone 表示两套字段都缺失（无法构造启动参数）。
	CompatArgumentsNone = "none"
)

// Java 需求来源取值（D-39：只按元数据，不按 MC 版本号猜）。
const (
	// CompatJavaSourceMetadata 表示 javaVersion.majorVersion 来自元数据。
	CompatJavaSourceMetadata = "metadata"

	// CompatJavaSourceUnknown 表示元数据未声明 Java 大版本。
	CompatJavaSourceUnknown = "unknown"
)

// 兼容性依据码。对外作为 notes[].code 与错误排查键使用，取值稳定。
const (
	// CompatNoteMetadataOK 表示版本 JSON 能被 P2 解析器成功合并。
	CompatNoteMetadataOK = "COMPAT_METADATA_OK"

	// CompatNoteMetadataFailed 表示版本 JSON 无法解析或继承链不可用。
	CompatNoteMetadataFailed = "COMPAT_METADATA_FAILED"

	// CompatNoteClientJarMissing 表示本地缺少客户端 JAR（只影响启动，不影响计划生成）。
	CompatNoteClientJarMissing = "COMPAT_CLIENT_JAR_MISSING"

	// CompatNoteAssetIndexMissing 表示元数据既无 assetIndex.id 也无 assets 标识。
	CompatNoteAssetIndexMissing = "COMPAT_ASSET_INDEX_MISSING"

	// CompatNoteAssetsVirtual 表示资源索引为旧式虚拟索引。
	CompatNoteAssetsVirtual = "COMPAT_ASSETS_VIRTUAL"

	// CompatNoteAssetsObjects 表示资源索引为现代内容寻址。
	CompatNoteAssetsObjects = "COMPAT_ASSETS_OBJECTS"

	// CompatNoteMapToResources 表示元数据要求把资源落到版本私有目录（P3 不启用）。
	CompatNoteMapToResources = "COMPAT_MAP_TO_RESOURCES"

	// CompatNoteNativesLegacy 表示使用旧式 natives 命名。
	CompatNoteNativesLegacy = "COMPAT_NATIVES_LEGACY"

	// CompatNoteNativesModern 表示使用现代 classifier。
	CompatNoteNativesModern = "COMPAT_NATIVES_MODERN"

	// CompatNoteArgumentsLegacy 表示使用旧式 minecraftArguments。
	CompatNoteArgumentsLegacy = "COMPAT_ARGUMENTS_LEGACY"

	// CompatNoteArgumentsModern 表示使用新式 arguments 结构。
	CompatNoteArgumentsModern = "COMPAT_ARGUMENTS_MODERN"

	// CompatNoteArgumentsMissing 表示两套启动参数字段都缺失。
	CompatNoteArgumentsMissing = "COMPAT_ARGUMENTS_MISSING"

	// CompatNoteJavaMetadata 表示 Java 大版本来自元数据。
	CompatNoteJavaMetadata = "COMPAT_JAVA_METADATA"

	// CompatNoteJavaUnknown 表示元数据未声明 Java 大版本（按 D-39 标 unknown）。
	CompatNoteJavaUnknown = "COMPAT_JAVA_UNKNOWN"

	// CompatNoteLoggingAbsent 表示元数据没有 logging.client（旧版常见，不影响安装）。
	CompatNoteLoggingAbsent = "COMPAT_LOGGING_ABSENT"

	// CompatNoteLoggingConfigured 表示元数据声明了 logging.client。
	CompatNoteLoggingConfigured = "COMPAT_LOGGING_CONFIGURED"

	// CompatNoteLibrariesWithoutRules 表示存在缺 rules 的库（按「缺 rules 视为全平台」处理）。
	CompatNoteLibrariesWithoutRules = "COMPAT_LIBRARIES_WITHOUT_RULES"

	// CompatNoteInheritChain 表示该版本存在继承链，合并结果已包含父版本内容。
	CompatNoteInheritChain = "COMPAT_INHERIT_CHAIN"

	// CompatNoteInheritUnreadable 表示继承链读取失败（父版本缺失或过深）。
	CompatNoteInheritUnreadable = "COMPAT_INHERIT_UNREADABLE"
)

// CompatNote 是一条兼容性依据。
//
// Empirical 为 true 表示该结论被本机可验证的事实支持（例如读取到了版本 JSON、
// 检查了本地文件）；为 false 表示依据为规则推导，未在真实环境实证。
type CompatNote struct {
	// Code 为稳定依据码。
	Code string

	// Message 为面向用户的中文说明。
	Message string

	// Empirical 表示该依据是否已被本机事实实证。
	Empirical bool
}

// VersionCompatTraits 是单个版本的兼容性特征（内部推导结果，不含对外状态词）。
type VersionCompatTraits struct {
	// VersionID 为版本标识。
	VersionID string

	// MetadataSupported 表示版本 JSON 能被 P2 解析器成功合并。
	MetadataSupported bool

	// MetadataStatus 为 P2 内部四态取值（仅作推导输入，不得直接透出前端）。
	MetadataStatus string

	// MetadataReason 为元数据不可用的原因。
	MetadataReason string

	// InstallViable 表示元数据足以生成确定性安装计划。
	InstallViable bool

	// LaunchViable 表示 P2 启动链路对该版本可行（只看元数据完整性）。
	LaunchViable bool

	// LaunchReason 为启动不可行的原因。
	LaunchReason string

	// InheritChain 为继承链（子在前）；无继承时为空。
	InheritChain []string

	// AssetLayout 为资源索引布局：virtual / objects / unknown。
	AssetLayout string

	// MapToResources 表示元数据声明 map_to_resources=true。
	MapToResources bool

	// NativesConvention 为 natives 命名约定：legacy / modern / mixed / none。
	NativesConvention string

	// ArgumentsFormat 为启动参数格式：minecraft_arguments / arguments / both / none。
	ArgumentsFormat string

	// JavaMajorVersion 为元数据声明的 Java 大版本；0 表示未声明。
	JavaMajorVersion int

	// JavaSource 为 Java 需求来源：metadata / unknown。
	JavaSource string

	// LoggingConfigured 表示元数据声明了 logging.client。
	LoggingConfigured bool

	// LibrariesWithoutRules 为缺 rules 的库数量（缺 rules 视为全平台）。
	LibrariesWithoutRules int

	// Notes 为依据列表，按 Code 稳定排序。
	Notes []CompatNote
}

// versionCompatProbe 是版本 JSON 中 P2 未声明的字段探测结果。
//
// P2 的 versionJSON 只声明启动所需字段（见 version.go 注释），
// 因此 map_to_resources / logging 只能从原始 JSON 另行读取。
type versionCompatProbe struct {
	// Assets 是资源索引标识的原始值（旧版可能为 legacy / pre-1.6）。
	Assets string `json:"assets"`

	// MapToResources 表示资源需落到版本私有目录。
	MapToResources bool `json:"map_to_resources"`

	// Logging 是日志配置（旧版通常缺失）。
	Logging *struct {
		// Client 是客户端日志配置。
		Client *struct {
			// Argument 是日志参数模板。
			Argument string `json:"argument"`
		} `json:"client"`
	} `json:"logging"`
}

// AssessVersionTraits 评估单个版本的兼容性特征。
//
// 参数 rawRoot 为用户选择的游戏目录（内部会归一化）；versionID 为版本目录名。
// 返回的特征只含事实与规则推导，不含对外状态词；元数据不可用时返回零值特征与 nil 错误，
// 由调用方按 MetadataSupported 映射状态词（对任意版本 id 调用都不 panic）。
func AssessVersionTraits(rawRoot, versionID string) (VersionCompatTraits, *Error) {
	traits := VersionCompatTraits{
		VersionID:         versionID,
		AssetLayout:       CompatAssetLayoutUnknown,
		NativesConvention: CompatNativesNone,
		ArgumentsFormat:   CompatArgumentsNone,
		JavaSource:        CompatJavaSourceUnknown,
		Notes:             make([]CompatNote, 0, 8),
	}

	if !isVersionIDValid(versionID) {
		return traits, newInputError(ErrorCodeVersionMetadata, messageVersionIDInvalid)
	}

	root, _, rootErr := normalizeGameRoot(rawRoot)
	if rootErr != nil {
		return traits, rootErr
	}

	outcome := loadMergedVersion(root, versionID)
	traits.MetadataStatus = outcome.status
	if outcome.status != versionStatusLaunchSupported && outcome.status != versionStatusMetadataOnly {
		traits.MetadataReason = outcome.reason
		traits.LaunchReason = outcome.reason
		traits.MetadataSupported = false
		traits.Notes = append(traits.Notes, CompatNote{
			Code:      CompatNoteMetadataFailed,
			Message:   describeMetadataFailure(outcome),
			Empirical: true,
		})
		return traits, nil
	}

	merged := outcome.merged
	if merged == nil {
		traits.MetadataSupported = false
		traits.Notes = append(traits.Notes, CompatNote{
			Code:      CompatNoteMetadataFailed,
			Message:   "版本元数据合并结果为空，无法评估兼容性。",
			Empirical: true,
		})
		return traits, nil
	}

	traits.MetadataSupported = true
	traits.InheritChain = append([]string{}, merged.InheritChain...)
	traits.Notes = append(traits.Notes, CompatNote{
		Code: "COMPAT_METADATA_OK",
		Message: "版本 JSON 已被 P2 解析器成功合并（" +
			mergeChainSummary(merged.InheritChain) + "）。",
		Empirical: true,
	})
	if len(merged.InheritChain) > 1 {
		traits.Notes = append(traits.Notes, CompatNote{
			Code:      CompatNoteInheritChain,
			Message:   "该版本存在继承链，判定基于合并后的结果：" + mergeChainSummary(merged.InheritChain) + "。",
			Empirical: true,
		})
	}

	probe := readVersionCompatProbe(root, versionID)

	// 资源索引布局与 map_to_resources。
	traits.AssetLayout = detectAssetLayout(merged, probe)
	switch traits.AssetLayout {
	case CompatAssetLayoutVirtual:
		traits.Notes = append(traits.Notes, CompatNote{
			Code:      CompatNoteAssetsVirtual,
			Message:   "资源索引为旧式虚拟索引，安装时需按逻辑名组装到版本私有目录（P3-07 提供能力）。",
			Empirical: true,
		})
	case CompatAssetLayoutObjects:
		traits.Notes = append(traits.Notes, CompatNote{
			Code:      CompatNoteAssetsObjects,
			Message:   "资源索引为现代内容寻址，对象按 SHA-1 落到共享 artifacts/objects。",
			Empirical: true,
		})
	}
	if probe != nil && probe.MapToResources {
		traits.MapToResources = true
		traits.Notes = append(traits.Notes, CompatNote{
			Code:      CompatNoteMapToResources,
			Message:   "元数据声明 map_to_resources=true，资源需组装进 versions/<id>/resources/；P3 不落盘（留 P5）。",
			Empirical: true,
		})
	}

	// Natives 约定。
	traits.NativesConvention = detectNativesConvention(merged.Libraries)
	switch traits.NativesConvention {
	case CompatNativesLegacy:
		traits.Notes = append(traits.Notes, CompatNote{
			Code:      CompatNoteNativesLegacy,
			Message:   "使用旧式 natives 命名（natives-windows / natives-osx / natives-linux）与 extract.exclude，计划中原样保留。",
			Empirical: true,
		})
	case CompatNativesModern:
		traits.Notes = append(traits.Notes, CompatNote{
			Code:      CompatNoteNativesModern,
			Message:   "使用现代 classifier（natives-*）声明本地库，计划中原样保留。",
			Empirical: true,
		})
	case CompatNativesMixed:
		traits.Notes = append(traits.Notes, CompatNote{
			Code:      CompatNoteNativesLegacy,
			Message:   "同一版本内旧式 natives 与现代 classifier 并存，计划中原样保留两类信息。",
			Empirical: true,
		})
	}

	// 启动参数格式。
	traits.ArgumentsFormat = detectArgumentsFormat(merged)
	switch traits.ArgumentsFormat {
	case CompatArgumentsLegacy:
		traits.Notes = append(traits.Notes, CompatNote{
			Code:      CompatNoteArgumentsLegacy,
			Message:   "使用旧式 minecraftArguments 单串，P2 已能解析（P3-10 不改变构建逻辑）。",
			Empirical: true,
		})
	case CompatArgumentsModern:
		traits.Notes = append(traits.Notes, CompatNote{
			Code:      CompatNoteArgumentsModern,
			Message:   "使用新式 arguments 结构（game/jvm 分离），P2 已能解析。",
			Empirical: true,
		})
	case CompatArgumentsBoth:
		traits.Notes = append(traits.Notes, CompatNote{
			Code:      CompatNoteArgumentsLegacy,
			Message:   "新旧两套启动参数字段并存，P2 以 arguments 为准。",
			Empirical: true,
		})
	case CompatArgumentsNone:
		traits.Notes = append(traits.Notes, CompatNote{
			Code:      CompatNoteArgumentsMissing,
			Message:   "元数据既无 minecraftArguments 也无 arguments，无法构造启动命令。",
			Empirical: true,
		})
	}

	// Java 需求（只按元数据，不猜）。
	if merged.JavaMajorVersion > 0 {
		traits.JavaMajorVersion = merged.JavaMajorVersion
		traits.JavaSource = CompatJavaSourceMetadata
		traits.Notes = append(traits.Notes, CompatNote{
			Code: "COMPAT_JAVA_METADATA",
			Message: "元数据声明需要 Java " +
				formatJavaMajor(traits.JavaMajorVersion) + "。",
			Empirical: true,
		})
	} else {
		traits.Notes = append(traits.Notes, CompatNote{
			Code:      CompatNoteJavaUnknown,
			Message:   "元数据未声明 javaVersion.majorVersion，Java 需求标为 unknown（不按版本号猜）。",
			Empirical: true,
		})
	}

	// logging 配置。
	if probe != nil && probe.Logging != nil && probe.Logging.Client != nil {
		traits.LoggingConfigured = true
		traits.Notes = append(traits.Notes, CompatNote{
			Code:      CompatNoteLoggingConfigured,
			Message:   "元数据声明 logging.client，日志参数可解析。",
			Empirical: true,
		})
	} else {
		traits.Notes = append(traits.Notes, CompatNote{
			Code:      CompatNoteLoggingAbsent,
			Message:   "元数据没有 logging.client（旧版常见），不影响安装与启动。",
			Empirical: true,
		})
	}

	// 缺 rules 的库（旧版库坐标：缺 rules 视为全平台）。
	traits.LibrariesWithoutRules = countLibrariesWithoutRules(merged.Libraries)
	if traits.LibrariesWithoutRules > 0 {
		traits.Notes = append(traits.Notes, CompatNote{
			Code: CompatNoteLibrariesWithoutRules,
			Message: "有 " + formatCount(traits.LibrariesWithoutRules) +
				" 个库未声明 rules，按「缺 rules 视为全平台」处理。",
			Empirical: true,
		})
	}

	// 客户端 JAR：本地存在性只影响启动，不影响计划生成。
	if merged.ClientJarID != "" {
		if !clientJarPresent(root, merged) {
			traits.Notes = append(traits.Notes, CompatNote{
				Code:      CompatNoteClientJarMissing,
				Message:   "本地尚未找到客户端 JAR，启动前需先安装；计划生成不受影响。",
				Empirical: true,
			})
		}
	}

	// 安装可行性：客户端 JAR 可定位 + 资源索引可定位。
	traits.InstallViable = merged.ClientJarID != "" && (merged.AssetIndexID != "" || merged.Assets != "")
	if !traits.InstallViable {
		reason := "元数据缺少客户端 JAR 标识，无法生成安装计划。"
		if merged.ClientJarID != "" {
			reason = "元数据既无 assetIndex.id 也无 assets，无法生成资源索引制品。"
		}
		traits.Notes = append(traits.Notes, CompatNote{
			Code:      CompatNoteAssetIndexMissing,
			Message:   reason,
			Empirical: true,
		})
	}

	// 启动可行性：主类 + 启动参数齐备。
	switch {
	case merged.MainClass == "":
		traits.LaunchViable = false
		traits.LaunchReason = "元数据缺少 mainClass，无法构造启动命令。"
	case traits.ArgumentsFormat == CompatArgumentsNone:
		traits.LaunchViable = false
		traits.LaunchReason = "元数据缺少启动参数，无法构造启动命令。"
	case !traits.MetadataSupported:
		traits.LaunchViable = false
		traits.LaunchReason = traits.MetadataReason
	default:
		traits.LaunchViable = true
	}

	sortCompatNotes(traits.Notes)
	return traits, nil
}

// readVersionCompatProbe 读取版本 JSON 中 P2 未声明的字段。
//
// 读取失败（文件缺失、超限、JSON 非法）时返回 nil：兼容性判定继续基于已合并的元数据，
// 只在日志侧体现，不影响主结论（探测属补充证据）。
func readVersionCompatProbe(root, versionID string) *versionCompatProbe {
	relative := dirVersions + fileSeparatorSlash + versionID + fileSeparatorSlash + versionID + ".json"
	absolute, err := resolveInside(root, relative)
	if err != nil {
		return nil
	}
	info, statErr := os.Stat(absolute)
	if statErr != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxVersionJSONBytes {
		return nil
	}
	raw, readErr := os.ReadFile(absolute)
	if readErr != nil {
		return nil
	}
	probe := &versionCompatProbe{}
	if jsonErr := json.Unmarshal(raw, probe); jsonErr != nil {
		return nil
	}
	return probe
}

// detectAssetLayout 判定资源索引布局。
//
// 规则（卡面第 2 条）：assets 为 legacy / pre-1.6 / pre-1.7 属旧式虚拟索引，
// 其余非空标识（数字版本号或 assetIndex.id）属现代内容寻址。
func detectAssetLayout(merged *mergedVersion, probe *versionCompatProbe) string {
	identifier := merged.Assets
	if probe != nil && probe.Assets != "" {
		identifier = probe.Assets
	}
	if identifier == "" && merged.AssetIndexID == "" {
		return CompatAssetLayoutUnknown
	}
	switch strings.ToLower(identifier) {
	case "legacy", "pre-1.6", "pre-1.7":
		return CompatAssetLayoutVirtual
	}
	return CompatAssetLayoutObjects
}

// detectNativesConvention 判定 natives 命名约定。
func detectNativesConvention(libraries []libraryJSON) string {
	legacy := false
	modern := false
	for _, library := range libraries {
		if len(library.Natives) > 0 {
			legacy = true
		}
		if library.Downloads != nil {
			for classifier := range library.Downloads.Classifiers {
				if strings.Contains(classifier, "natives") {
					modern = true
				}
			}
		}
	}
	switch {
	case legacy && modern:
		return CompatNativesMixed
	case legacy:
		return CompatNativesLegacy
	case modern:
		return CompatNativesModern
	default:
		return CompatNativesNone
	}
}

// detectArgumentsFormat 判定启动参数格式。
func detectArgumentsFormat(merged *mergedVersion) string {
	legacy := strings.TrimSpace(merged.MinecraftArguments) != ""
	modern := len(merged.GameArguments) > 0 || len(merged.JVMMajorArguments) > 0
	switch {
	case legacy && modern:
		return CompatArgumentsBoth
	case legacy:
		return CompatArgumentsLegacy
	case modern:
		return CompatArgumentsModern
	default:
		return CompatArgumentsNone
	}
}

// countLibrariesWithoutRules 统计未声明 rules 的库数量。
//
// 「缺 rules 视为全平台」是 P2 已冻结的判定（D-31 旧版库坐标），本卡只记录数量。
func countLibrariesWithoutRules(libraries []libraryJSON) int {
	count := 0
	for _, library := range libraries {
		trimmed := strings.TrimSpace(string(library.Rules))
		if trimmed == "" || trimmed == "null" || trimmed == "[]" {
			count++
		}
	}
	return count
}

// clientJarPresent 检查客户端 JAR 是否已在本地就位。
//
// 两种位置都检查：`versions/<id>/<jar>.jar` 与 `versions/<id>/<id>.jar`（旧版布局）。
func clientJarPresent(root string, merged *mergedVersion) bool {
	candidates := []string{
		dirVersions + fileSeparatorSlash + merged.ID + fileSeparatorSlash + merged.ClientJarID + ".jar",
		dirVersions + fileSeparatorSlash + merged.ID + fileSeparatorSlash + merged.ID + ".jar",
	}
	for _, relative := range candidates {
		if absolute, err := resolveInside(root, relative); err == nil && isRegularNonEmptyFile(absolute) {
			return true
		}
	}
	return false
}

// describeMetadataFailure 生成元数据失败的中文依据。
func describeMetadataFailure(outcome versionLoadOutcome) string {
	reason := outcome.reason
	if reason == "" {
		reason = "版本元数据不可读或结构不受支持。"
	}
	if outcome.errorCode != "" {
		return reason + "（" + outcome.errorCode + "）"
	}
	return reason
}

// mergeChainSummary 生成继承链的中文摘要。
func mergeChainSummary(chain []string) string {
	if len(chain) == 0 {
		return "无继承链"
	}
	return "继承链 " + strings.Join(chain, " → ")
}

// formatJavaMajor 把 Java 大版本格式化为中文可读文本。
func formatJavaMajor(major int) string {
	return strconv.Itoa(major)
}

// formatCount 把计数格式化为中文可读文本。
func formatCount(count int) string {
	return strconv.Itoa(count)
}

// sortCompatNotes 按依据码稳定排序，保证同一输入得到同一输出。
func sortCompatNotes(notes []CompatNote) {
	sort.SliceStable(notes, func(i, j int) bool {
		return notes[i].Code < notes[j].Code
	})
}
