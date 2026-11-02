// 本文件实现「面向安装」的版本元数据解析。
//
// 与 P2 的启动解析（已验收的 loadMergedVersion）并存而不互相替代：
//   - 启动只需要「路径与存在性」，P2 的解析视图刻意不读 sha1 与下载地址；
//   - 安装需要「下载地址 + 大小 + 哈希」，因此本文件是另一套结构体。
//
// 两者的继承合并规则必须一致（深度上限 5 层、环检测、自引用判定、子覆盖父、
// 集合去重追加、arguments 父在前子在后），由 detail_test.go 的 fixture 对照断言保证。
// 本文件只做解析与合并，不下载、不写盘、不访问任何网络地址。
package version

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"example.com/ninevoxel/launcher/internal/artifact"
)

// 解析限制与继承深度上限。
const (
	// installMaxVersionJSONBytes 是单个版本 JSON 的大小上限（8 MiB）。
	//
	// 真实版本 JSON 通常低于 1 MiB（含继承与全部库条目），8 MiB 已留足余量；
	// 超出即拒绝，避免恶意元数据撑爆内存。
	installMaxVersionJSONBytes = 8 << 20

	// installMaxInheritDepth 是继承链的深度上限，与 P2 的 maxInheritDepth 一致。
	//
	// 语义：最多遍历「当前版本 + 5 层父版本」共 6 个节点；仍存在父版本时
	// 判定为不支持（元数据不完整），而不是继续递归。
	installMaxInheritDepth = 5

	// installMavenPrefix 是 Maven 坐标推导路径时的固定前缀。
	installMavenPrefix = "libraries/"
)

// installLoadStatus 表示版本元数据的可安装性判定结果。
//
// 该状态只在本包内部使用，对外一律映射为接口冻结清单 §2.4 的四层兼容状态，
// 不把内部判定透出给前端（D-24）。
type installLoadStatus string

const (
	// installStatusMetadataOnly 表示元数据可读但不足以直接启动（缺少 mainClass）。
	installStatusMetadataOnly installLoadStatus = "metadata_only"

	// installStatusUnsupported 表示元数据可读但继承链不完整，本阶段不支持安装。
	installStatusUnsupported installLoadStatus = "unsupported"

	// installStatusBroken 表示元数据不可信（读取失败、结构非法、继承链成环）。
	installStatusBroken installLoadStatus = "broken"
)

// installVersionJSON 是版本 JSON 的安装视图。
//
// 字段覆盖安装与启动所需的全部信息：客户端 jar、库（含规则与 natives）、
// 资源索引、日志配置、Java 版本要求。未使用的字段（如 server）也解析，
// 便于后续阶段复用而不必再改结构。
type installVersionJSON struct {
	// ID 为版本标识（JSON 中的 id 字段）。
	ID string `json:"id"`

	// Type 为版本类型（release/snapshot/old_beta/old_alpha）。
	Type string `json:"type"`

	// ReleaseTime 为发布时间（ISO8601 文本）。
	ReleaseTime string `json:"releaseTime"`

	// MainClass 为启动主类。
	MainClass string `json:"mainClass"`

	// Assets 为资源版本标识（旧格式可能缺失，改用 assetIndex）。
	Assets string `json:"assets"`

	// Jar 为客户端 jar 的替代 id（缺失时回退为版本 id）。
	Jar string `json:"jar"`

	// InheritsFrom 为父版本标识。
	InheritsFrom string `json:"inheritsFrom"`

	// MinecraftArguments 为旧格式（1.7.10 及更早）的启动参数模板。
	MinecraftArguments string `json:"minecraftArguments"`

	// AssetIndex 为资源索引描述。
	AssetIndex *installAssetIndexJSON `json:"assetIndex"`

	// Downloads 为版本级下载描述（客户端与服务端）。
	Downloads *installDownloadsJSON `json:"downloads"`

	// JavaVersion 为 Java 版本要求。
	JavaVersion *installJavaVersionJSON `json:"javaVersion"`

	// Libraries 为依赖库列表（保持 JSON 顺序，不排序）。
	Libraries []installLibraryJSON `json:"libraries"`

	// Arguments 为新格式（1.13+）的分组参数。
	Arguments *installArgumentsJSON `json:"arguments"`

	// Logging 为日志配置（可选）。
	Logging *installLoggingJSON `json:"logging"`
}

// installAssetIndexJSON 描述资源索引本身（对象清单由 P3-07 展开）。
type installAssetIndexJSON struct {
	// ID 为索引标识。
	ID string `json:"id"`

	// SHA1 为索引文件的 SHA-1。
	SHA1 string `json:"sha1"`

	// Size 为索引文件字节数。
	Size int64 `json:"size"`

	// TotalSize 为索引内全部对象的字节总和（仅信息性字段）。
	TotalSize int64 `json:"totalSize"`

	// URL 为索引下载地址（只在后端流转）。
	URL string `json:"url"`
}

// installDownloadsJSON 是版本级下载描述。
type installDownloadsJSON struct {
	// Client 为客户端 jar 的下载描述。
	Client *installDownloadEntryJSON `json:"client"`

	// Server 为服务端 jar 的下载描述（P3 不使用）。
	Server *installDownloadEntryJSON `json:"server"`
}

// installDownloadEntryJSON 是单个制品的下载描述。
type installDownloadEntryJSON struct {
	// Path 为元数据中声明的相对路径（仅库条目使用）。
	Path string `json:"path"`

	// SHA1 为内容的 SHA-1。
	SHA1 string `json:"sha1"`

	// Size 为内容的字节数。
	Size int64 `json:"size"`

	// URL 为下载地址（只在后端流转，不返回前端）。
	URL string `json:"url"`
}

// installJavaVersionJSON 是 Java 版本要求。
type installJavaVersionJSON struct {
	// MajorVersion 为主版本号（如 8、17、21）；缺失或非正数表示未知。
	MajorVersion int `json:"majorVersion"`

	// Component 为组件标识（如 jre-legacy、java-runtime-gamma）。
	Component string `json:"component"`
}

// installLibraryJSON 是单个依赖库。
type installLibraryJSON struct {
	// Name 为 Maven 坐标（group:artifact:version[:classifier]）。
	Name string `json:"name"`

	// Downloads 为该库的下载描述。
	Downloads *installLibraryDownloadsJSON `json:"downloads"`

	// Rules 为平台适用规则（为空表示全平台适用）。
	Rules []installRuleJSON `json:"rules"`

	// Natives 为平台到 classifier 的映射（旧格式本地库）。
	Natives map[string]string `json:"natives"`

	// Extract 为压缩包解压说明（本地库使用）。
	Extract *installExtractJSON `json:"extract"`
}

// installLibraryDownloadsJSON 是库级下载描述。
type installLibraryDownloadsJSON struct {
	// Artifact 为主 jar 的下载描述。
	Artifact *installDownloadEntryJSON `json:"artifact"`

	// Classifiers 为按 classifier 区分的下载描述（本地库）。
	Classifiers map[string]installDownloadEntryJSON `json:"classifiers"`
}

// installRuleJSON 是单条平台规则。
type installRuleJSON struct {
	// Action 取 allow 或 disallow。
	Action string `json:"action"`

	// OS 为操作系统条件。
	OS *installRuleOSJSON `json:"os"`

	// Features 为特性条件（启动器目前不提供任何特性）。
	Features map[string]bool `json:"features"`
}

// installRuleOSJSON 是规则中的操作系统条件。
type installRuleOSJSON struct {
	// Name 为 windows、osx 或 linux。
	Name string `json:"name"`

	// Arch 为架构（x86、x86_64、arm64）。
	Arch string `json:"arch"`

	// Version 为系统版本条件（本阶段无法判定，命中即视为不匹配）。
	Version string `json:"version"`
}

// installExtractJSON 是本地库解压说明。
type installExtractJSON struct {
	// Exclude 为解压时必须排除的条目，必须原样保留到安装计划中。
	Exclude []string `json:"exclude"`
}

// installArgumentsJSON 是新格式的分组参数。
type installArgumentsJSON struct {
	// Game 为游戏侧参数。
	Game []json.RawMessage `json:"game"`

	// JVM 为虚拟机侧参数。
	JVM []json.RawMessage `json:"jvm"`
}

// installLoggingJSON 是日志配置。
type installLoggingJSON struct {
	// Client 为客户端日志配置。
	Client *installLoggingClientJSON `json:"client"`
}

// installLoggingClientJSON 是客户端日志配置。
type installLoggingClientJSON struct {
	// Argument 为传给 JVM 的参数模板（如 -Dlog4j.configurationFile=...）。
	Argument string `json:"argument"`

	// File 为日志配置文件描述。
	File *installLoggingFileJSON `json:"file"`
}

// installLoggingFileJSON 是日志配置文件描述。
type installLoggingFileJSON struct {
	// ID 为配置标识（如 client-1.12.xml）。
	ID string `json:"id"`

	// SHA1 为内容的 SHA-1。
	SHA1 string `json:"sha1"`

	// Size 为内容的字节数。
	Size int64 `json:"size"`

	// URL 为下载地址（只在后端流转）。
	URL string `json:"url"`
}

// installMergedVersion 是继承链合并后的安装视图。
//
// 合并方向为「子覆盖父」：标量字段子版本优先，缺失时回填父版本；
// 集合字段先去重（按库坐标）再追加父版本的独有条目。
type installMergedVersion struct {
	// ID 为链首版本标识。
	ID string

	// Type 为链首版本类型。
	Type string

	// ReleaseTime 为链首版本发布时间，缺失时取父版本。
	ReleaseTime string

	// MainClass 为合并后的主类。
	MainClass string

	// Assets 为合并后的资源版本标识。
	Assets string

	// AssetIndex 为合并后的资源索引描述。
	AssetIndex *installAssetIndexJSON

	// ClientJarID 为客户端 jar 的标识（`jar` 优先，缺失回退版本 id）。
	ClientJarID string

	// ClientDownload 为客户端 jar 的下载描述。
	ClientDownload *installDownloadEntryJSON

	// Libraries 为合并后的库列表（子版本顺序在前）。
	Libraries []installLibraryJSON

	// MinecraftArguments 为合并后的旧格式参数模板。
	MinecraftArguments string

	// Arguments 为合并后的新格式参数。
	Arguments *installArgumentsJSON

	// JavaMajorVersion 为合并后的 Java 主版本号（0 表示未知）。
	JavaMajorVersion int

	// JavaComponent 为合并后的 Java 组件标识。
	JavaComponent string

	// Logging 为合并后的日志配置。
	Logging *installLoggingJSON

	// InheritChain 为从子到父的版本标识链，用于诊断。
	InheritChain []string

	// JSONSize 为链首版本 JSON 文件的字节数（信息性字段）。
	JSONSize int64
}

// installPlatformFacts 是判定规则时需要的平台事实。
type installPlatformFacts struct {
	// OSName 为 Mojang 口径的系统名（windows/osx/linux）。
	OSName string

	// ArchNames 为可接受的架构别名集合。
	ArchNames []string

	// ArchToken 为 natives classifier 中 `${arch}` 的替换值（32/64）。
	ArchToken string
}

// loadInstallVersion 读取并合并一条版本继承链，得到安装视图。
//
// 参数 root 为数据根绝对路径，versionID 为链首版本标识。
// 返回值中的 warnings 记录非致命问题（父版本缺失、层级超限等），
// 致命问题（读取失败、结构非法、成环）通过 *AppError（VERSION_JSON_001）返回。
// 本函数只读文件，不写盘、不访问网络。
func loadInstallVersion(root, versionID string) (*installMergedVersion, installLoadStatus, []planWarning, *AppError) {
	warnings := make([]planWarning, 0, 4)

	first, firstSize, appErr := readInstallVersionJSON(root, versionID)
	if appErr != nil {
		return nil, installStatusBroken, warnings, appErr
	}

	merged := newInstallMerged(first, versionID)
	merged.JSONSize = firstSize
	merged.InheritChain = []string{chainID(first, versionID)}

	// 无父版本：合并结束。
	if first.InheritsFrom == "" {
		return merged, installStatusMetadataOnly, warnings, nil
	}

	visited := map[string]bool{merged.InheritChain[0]: true}
	currentID := first.InheritsFrom
	status := installStatusMetadataOnly

	for depth := 1; depth <= installMaxInheritDepth; depth++ {
		// 自引用与成环：元数据不可信，按 broken 处理。
		if visited[currentID] {
			if currentID == merged.InheritChain[0] {
				return nil, installStatusBroken, warnings,
					newInputError(ErrorCodeVersionJSON, fmt.Sprintf(messageVersionInheritSelf, truncateForMessage(currentID)))
			}
			return nil, installStatusBroken, warnings,
				newInputError(ErrorCodeVersionJSON, fmt.Sprintf(messageVersionInheritCycle, truncateForMessage(currentID)))
		}

		parent, _, readErr := readInstallVersionJSON(root, currentID)
		if readErr != nil {
			// 父版本缺失：元数据不完整，按不支持安装处理，但保留已合并的部分。
			warnings = append(warnings, planWarning{
				Code:    WarningInheritParentMissing,
				Message: fmt.Sprintf(messageInheritParentMissing, truncateForMessage(merged.ID), truncateForMessage(currentID)),
			})
			return merged, installStatusUnsupported, warnings, nil
		}

		visited[currentID] = true
		merged.InheritChain = append(merged.InheritChain, chainID(parent, currentID))
		mergeInstallParent(merged, parent)
		currentID = parent.InheritsFrom

		if currentID == "" {
			break
		}

		// 已到深度上限但仍有父版本：按不支持处理，避免无限递归。
		if depth == installMaxInheritDepth {
			status = installStatusUnsupported
			warnings = append(warnings, planWarning{
				Code:    WarningInheritTooDeep,
				Message: fmt.Sprintf(messageInheritTooDeep, truncateForMessage(merged.ID), installMaxInheritDepth),
			})
			return merged, status, warnings, nil
		}
	}

	return merged, status, warnings, nil
}

// readInstallVersionJSON 读取单个版本的 JSON 文件并做基础校验。
//
// 文件位置固定为 `<数据根>/versions/<id>/<id>.json`（由 artifact.VersionRelPath 生成），
// 不读取任何远端地址；大小超过 8 MiB 即拒绝。
// 第二个返回值为文件字节数，供详情展示使用。
func readInstallVersionJSON(root, versionID string) (*installVersionJSON, int64, *AppError) {
	if strings.TrimSpace(versionID) == "" {
		return nil, 0, newInputError(ErrorCodeVersionJSON, messageVersionIDRequired)
	}

	relPath, relErr := artifact.VersionRelPath(versionID, versionID+".json")
	if relErr != nil {
		return nil, 0, newInputError(ErrorCodeVersionJSON, messageVersionIDInvalid)
	}

	absolute := filepath.Join(root, filepath.FromSlash(relPath))
	raw, err := os.ReadFile(absolute)
	if err != nil {
		return nil, 0, newInputError(ErrorCodeVersionJSON,
			fmt.Sprintf(messageVersionJSONReadFailed, truncateForMessage(versionID)))
	}
	if len(raw) > installMaxVersionJSONBytes {
		return nil, 0, newInputError(ErrorCodeVersionJSON, messageVersionJSONTooLarge)
	}

	var parsed installVersionJSON
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, 0, newInputError(ErrorCodeVersionJSON,
			fmt.Sprintf(messageVersionJSONMalformed, truncateForMessage(versionID)))
	}
	if strings.TrimSpace(parsed.ID) == "" {
		// JSON 里缺少 id 时以文件名对应的 id 为准，避免链上标识为空。
		parsed.ID = versionID
	}
	return &parsed, int64(len(raw)), nil
}

// chainID 返回节点在继承链中的标识。
func chainID(node *installVersionJSON, fallback string) string {
	if node == nil || strings.TrimSpace(node.ID) == "" {
		return fallback
	}
	return node.ID
}

// newInstallMerged 用链首版本构造合并结果。
func newInstallMerged(first *installVersionJSON, versionID string) *installMergedVersion {
	merged := &installMergedVersion{
		ID:                 chainID(first, versionID),
		Type:               first.Type,
		ReleaseTime:        first.ReleaseTime,
		MainClass:          first.MainClass,
		Assets:             first.Assets,
		AssetIndex:         cloneAssetIndex(first.AssetIndex),
		ClientJarID:        clientJarID(first, versionID),
		ClientDownload:     cloneDownload(first.downloadsClient()),
		Libraries:          cloneLibraries(first.Libraries),
		MinecraftArguments: first.MinecraftArguments,
		Arguments:          cloneArguments(first.Arguments),
		JavaComponent:      componentOf(first.JavaVersion),
		Logging:            cloneLogging(first.Logging),
	}
	if first.JavaVersion != nil && first.JavaVersion.MajorVersion > 0 {
		merged.JavaMajorVersion = first.JavaVersion.MajorVersion
	}
	return merged
}

// mergeInstallParent 把父版本合并进子版本（子覆盖父，缺失回填）。
//
// 合并规则必须与 P2 的 mergeParentIntoChild 一致：
//   - libraries 按坐标去重，子版本已有的坐标不再取父版本；
//   - arguments 父在前、子在后；
//   - 旧格式 minecraftArguments 仅在子版本为空时取父版本；
//   - 标量字段仅在子版本为空时回填。
func mergeInstallParent(child *installMergedVersion, parent *installVersionJSON) {
	if child.ReleaseTime == "" {
		child.ReleaseTime = parent.ReleaseTime
	}
	if child.MainClass == "" {
		child.MainClass = parent.MainClass
	}
	if child.Assets == "" {
		child.Assets = parent.Assets
	}
	if child.AssetIndex == nil {
		child.AssetIndex = cloneAssetIndex(parent.AssetIndex)
	}
	if child.ClientDownload == nil {
		child.ClientDownload = cloneDownload(parent.downloadsClient())
	}
	if child.MinecraftArguments == "" {
		child.MinecraftArguments = parent.MinecraftArguments
	}
	if child.JavaMajorVersion == 0 && parent.JavaVersion != nil && parent.JavaVersion.MajorVersion > 0 {
		child.JavaMajorVersion = parent.JavaVersion.MajorVersion
	}
	if child.JavaComponent == "" {
		child.JavaComponent = componentOf(parent.JavaVersion)
	}
	if child.Logging == nil {
		child.Logging = cloneLogging(parent.Logging)
	}

	// arguments：父版本的参数排在子版本之前。
	if parent.Arguments != nil {
		if child.Arguments == nil {
			child.Arguments = cloneArguments(parent.Arguments)
		} else {
			child.Arguments = &installArgumentsJSON{
				Game: append(cloneRawMessages(parent.Arguments.Game), child.Arguments.Game...),
				JVM:  append(cloneRawMessages(parent.Arguments.JVM), child.Arguments.JVM...),
			}
		}
	}

	// libraries：子版本已有的坐标（含无名条目）保留，父版本独有条目追加到末尾。
	child.Libraries = appendLibraries(child.Libraries, parent.Libraries)
}

// appendLibraries 把父版本的库追加到子版本库列表末尾，坐标相同者跳过。
func appendLibraries(child, parent []installLibraryJSON) []installLibraryJSON {
	merged := make([]installLibraryJSON, 0, len(child)+len(parent))
	merged = append(merged, cloneLibraries(child)...)

	seen := make(map[string]bool, len(child))
	for _, lib := range child {
		if lib.Name != "" {
			seen[lib.Name] = true
		}
	}
	for _, lib := range parent {
		if lib.Name != "" && seen[lib.Name] {
			continue
		}
		merged = append(merged, cloneLibraries([]installLibraryJSON{lib})...)
		if lib.Name != "" {
			seen[lib.Name] = true
		}
	}
	return merged
}

// downloadsClient 返回版本级客户端 jar 的下载描述；缺失时返回 nil。
func (v *installVersionJSON) downloadsClient() *installDownloadEntryJSON {
	if v == nil || v.Downloads == nil {
		return nil
	}
	return v.Downloads.Client
}

// clientJarID 返回客户端 jar 的标识：`jar` 字段优先，缺失回退版本 id。
func clientJarID(node *installVersionJSON, versionID string) string {
	if node != nil && strings.TrimSpace(node.Jar) != "" {
		return node.Jar
	}
	return versionID
}

// componentOf 返回 Java 组件标识；缺失返回空串。
func componentOf(java *installJavaVersionJSON) string {
	if java == nil {
		return ""
	}
	return java.Component
}

// currentPlatformFacts 返回当前平台的规则判定事实。
//
// 映射口径：GOOS=windows→windows、darwin→osx，其余按 linux；
// 架构别名与 Mojang 元数据中的写法并存（x86_64/x64、arm64/aarch64），
// 避免同一份元数据在不同 Go 版本下判定不一致。
func currentPlatformFacts() installPlatformFacts {
	facts := installPlatformFacts{OSName: "linux", ArchToken: "64"}

	switch runtime.GOOS {
	case "windows":
		facts.OSName = "windows"
	case "darwin":
		facts.OSName = "osx"
	}

	switch runtime.GOARCH {
	case "amd64":
		facts.ArchNames = []string{"x86_64", "x64", "amd64"}
	case "386":
		facts.ArchNames = []string{"x86", "i386"}
		facts.ArchToken = "32"
	case "arm64":
		facts.ArchNames = []string{"arm64", "aarch64"}
	default:
		facts.ArchNames = []string{runtime.GOARCH}
	}
	return facts
}

// libraryAppliesToPlatform 按 rules 判定库是否适用于当前平台。
//
// 语义与 Mojang 元数据一致：无规则即适用；有规则时初始不适用，
// 命中 allow 置为适用、命中 disallow 置为不适用，以最后命中的规则为准。
func libraryAppliesToPlatform(rules []installRuleJSON, facts installPlatformFacts) bool {
	if len(rules) == 0 {
		return true
	}
	applies := false
	for _, rule := range rules {
		if !ruleMatchesPlatform(rule, facts) {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(rule.Action)) {
		case "allow":
			applies = true
		case "disallow":
			applies = false
		}
	}
	return applies
}

// ruleMatchesPlatform 报告单条规则的条件是否命中当前平台。
//
// 无法判定的条件（os.version、任何 features）一律视为不命中：
// 元数据要求的能力当前上下文中并不存在，保守跳过比误装更安全。
func ruleMatchesPlatform(rule installRuleJSON, facts installPlatformFacts) bool {
	if rule.OS != nil {
		if rule.OS.Name != "" && !strings.EqualFold(rule.OS.Name, facts.OSName) {
			return false
		}
		if rule.OS.Arch != "" && !containsFold(facts.ArchNames, rule.OS.Arch) {
			return false
		}
		if rule.OS.Version != "" {
			return false
		}
	}
	for _, required := range rule.Features {
		if required {
			return false
		}
	}
	return true
}

// containsFold 报告候选集合中是否存在忽略大小写后相等的值。
func containsFold(candidates []string, value string) bool {
	for _, candidate := range candidates {
		if strings.EqualFold(candidate, value) {
			return true
		}
	}
	return false
}

// selectNativeClassifier 挑选当前平台对应的 natives classifier。
//
// 映射键为 Mojang 口径的系统名（windows/osx/linux）；值中可能含 `${arch}` 占位符，
// 按架构替换为 32 或 64。无匹配时返回空串（该库没有本地库制品）。
func selectNativeClassifier(lib installLibraryJSON, facts installPlatformFacts) string {
	if len(lib.Natives) == 0 {
		return ""
	}
	classifier, ok := lib.Natives[facts.OSName]
	if !ok || strings.TrimSpace(classifier) == "" {
		return ""
	}
	return strings.ReplaceAll(classifier, "${arch}", facts.ArchToken)
}

// mavenLibraryPath 用 Maven 坐标推导库的相对路径（旧格式无 downloads 时使用）。
//
// 坐标格式为 `group:artifact:version[:classifier]`，路径为
// `libraries/<group 以 / 分隔>/<artifact>/<version>/<artifact>-<version>[-<classifier>].jar`。
// 该路径只用于「无哈希可校验」的提示，不进入安装计划。
func mavenLibraryPath(name string) string {
	parts := strings.Split(strings.TrimSpace(name), ":")
	if len(parts) < 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return ""
	}
	groupId := strings.ReplaceAll(parts[0], ".", "/")
	artifactID := parts[1]
	version := parts[2]
	fileName := artifactID + "-" + version
	if len(parts) >= 4 && parts[3] != "" {
		fileName += "-" + parts[3]
	}
	return installMavenPrefix + groupId + "/" + artifactID + "/" + version + "/" + fileName + ".jar"
}

// cloneAssetIndex 复制资源索引描述，避免父子版本共享同一指针。
func cloneAssetIndex(source *installAssetIndexJSON) *installAssetIndexJSON {
	if source == nil {
		return nil
	}
	cloned := *source
	return &cloned
}

// cloneDownload 复制单个下载描述。
func cloneDownload(source *installDownloadEntryJSON) *installDownloadEntryJSON {
	if source == nil {
		return nil
	}
	cloned := *source
	return &cloned
}

// cloneLibraries 深拷贝库列表，避免合并过程修改调用方的结构。
func cloneLibraries(source []installLibraryJSON) []installLibraryJSON {
	if len(source) == 0 {
		return nil
	}
	cloned := make([]installLibraryJSON, len(source))
	for index, lib := range source {
		cloned[index] = cloneLibrary(lib)
	}
	return cloned
}

// cloneLibrary 深拷贝单个库条目。
func cloneLibrary(lib installLibraryJSON) installLibraryJSON {
	cloned := installLibraryJSON{
		Name:    lib.Name,
		Natives: cloneStringMap(lib.Natives),
	}
	if lib.Downloads != nil {
		cloned.Downloads = &installLibraryDownloadsJSON{
			Artifact: cloneDownload(lib.Downloads.Artifact),
		}
		if len(lib.Downloads.Classifiers) > 0 {
			cloned.Downloads.Classifiers = make(map[string]installDownloadEntryJSON, len(lib.Downloads.Classifiers))
			for classifier, entry := range lib.Downloads.Classifiers {
				cloned.Downloads.Classifiers[classifier] = entry
			}
		}
	}
	if len(lib.Rules) > 0 {
		cloned.Rules = make([]installRuleJSON, len(lib.Rules))
		for index, rule := range lib.Rules {
			copied := rule
			if rule.OS != nil {
				osCopy := *rule.OS
				copied.OS = &osCopy
			}
			copied.Features = cloneBoolMap(rule.Features)
			cloned.Rules[index] = copied
		}
	}
	if lib.Extract != nil {
		cloned.Extract = &installExtractJSON{Exclude: append([]string(nil), lib.Extract.Exclude...)}
	}
	return cloned
}

// cloneArguments 深拷贝分组参数。
func cloneArguments(source *installArgumentsJSON) *installArgumentsJSON {
	if source == nil {
		return nil
	}
	return &installArgumentsJSON{
		Game: cloneRawMessages(source.Game),
		JVM:  cloneRawMessages(source.JVM),
	}
}

// cloneRawMessages 深拷贝原始参数片段。
func cloneRawMessages(source []json.RawMessage) []json.RawMessage {
	if len(source) == 0 {
		return nil
	}
	cloned := make([]json.RawMessage, len(source))
	for index, raw := range source {
		cloned[index] = append(json.RawMessage(nil), raw...)
	}
	return cloned
}

// cloneLogging 深拷贝日志配置。
func cloneLogging(source *installLoggingJSON) *installLoggingJSON {
	if source == nil {
		return nil
	}
	cloned := &installLoggingJSON{}
	if source.Client != nil {
		cloned.Client = &installLoggingClientJSON{Argument: source.Client.Argument}
		if source.Client.File != nil {
			fileCopy := *source.Client.File
			cloned.Client.File = &fileCopy
		}
	}
	return cloned
}

// cloneStringMap 复制字符串映射。
func cloneStringMap(source map[string]string) map[string]string {
	if len(source) == 0 {
		return nil
	}
	cloned := make(map[string]string, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

// cloneBoolMap 复制布尔映射。
func cloneBoolMap(source map[string]bool) map[string]bool {
	if len(source) == 0 {
		return nil
	}
	cloned := make(map[string]bool, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}
