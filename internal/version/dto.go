// 本文件定义 VersionService 的请求/响应 DTO 与内部清单模型。
//
// 对外 DTO 的字段名与《P3 接口冻结清单》§2.1 逐字对齐（JSON 采用 lowerCamelCase）；
// 安全约束：任何面向前端的 DTO 都不得包含绝对路径、用户名、完整 URL 查询串。
// 因此 VersionSummary **不包含**清单条目里的 url 字段——该地址只在后端流转，
// 用于 P3-02 拉取版本 JSON，前端没有获取它的接口。
package version

// VersionListRequest 是版本列表查询请求。
//
// Type 为空时只返回 release + snapshot（默认不展示 old_beta / old_alpha）；
// 显式传 old_beta / old_alpha 才会返回对应旧预览版。
// Type 支持用逗号分隔多个取值（例如 "release,snapshot"），未知取值不匹配任何版本。
type VersionListRequest struct {
	// Type 为版本类型过滤：release / snapshot / old_beta / old_alpha，可逗号分隔。
	Type string `json:"type"`

	// Offset 为分页起始下标；负值按 0 处理。
	Offset int `json:"offset"`

	// Limit 为单页条数；0 按默认值 50，超过上限按 200 处理。
	Limit int `json:"limit"`
}

// VersionSummary 是单个版本的展示信息。
//
// 字段固定（接口冻结 §2.1）；Installed 与 Compat 在本卡给出保守默认值，
// 真实值分别由 P3-P5 的安装记录与 P3-10 的兼容性矩阵填充。
type VersionSummary struct {
	// ID 为版本标识，例如 "1.21.4" 或 "25w02a"。
	ID string `json:"id"`

	// Type 为版本类型：release / snapshot / old_beta / old_alpha。
	Type string `json:"type"`

	// ReleaseTime 为发布时间（RFC3339 文本，原样取自清单）。
	ReleaseTime string `json:"releaseTime"`

	// SHA1 为版本 JSON 的 SHA-1（清单提供，供 P3-02 校验使用）。
	SHA1 string `json:"sha1"`

	// Size 为版本 JSON 的字节数（清单提供）。
	Size int64 `json:"size"`

	// Installed 表示该版本是否已安装；本卡恒为 false（安装记录属后续卡）。
	Installed bool `json:"installed"`

	// Compat 为兼容性四层判定；本卡给出保守默认值（见 CompatLayer 说明）。
	Compat CompatLayer `json:"compat"`

	// Verified 表示清单内容是否经过哈希校验。
	// 官方清单本身没有自校验哈希，本卡恒为 false（已知信任缺口）。
	Verified bool `json:"verified"`
}

// CompatLayer 是对外的兼容性四层判定（D-24 已冻结）。
//
// 四层回答「能走到哪一步」：元数据可读 / 可安装 / 可启动 / 加载器可用。
// P2 的四态（launch_supported 等）属于 P2 启动模块的内部判定，
// 不得直接透出到本包 DTO，因此这里只用四个布尔字段。
type CompatLayer struct {
	// Evaluated 表示这一组四层判定是否已基于真实元数据计算过。
	//
	// 列表页不做逐版本解析（2000 多个版本会打爆上游），因此为 false，
	// 此时四个层级字段没有可信语义，前端不得据此展示「支持 / 不支持」。
	Evaluated bool `json:"evaluated"`

	// MetadataSupported 表示版本 JSON 元数据可正常读取。
	MetadataSupported bool `json:"metadataSupported"`

	// InstallSupported 表示本阶段支持为该版本创建下载任务。
	InstallSupported bool `json:"installSupported"`

	// LaunchSupported 表示该版本可被启动器启动。
	LaunchSupported bool `json:"launchSupported"`

	// LoaderSupported 表示加载器可用；P3 恒为 false（加载器属 P5-B）。
	LoaderSupported bool `json:"loaderSupported"`
}

// VersionListResult 是版本列表查询结果。
type VersionListResult struct {
	// Items 为当前页的版本列表。
	Items []VersionSummary `json:"items"`

	// Total 为过滤后的总条数（不受分页影响）。
	Total int `json:"total"`

	// Offset 为本次实际使用的分页起点（已做下限纠正）。
	Offset int `json:"offset"`

	// Limit 为本次实际使用的单页条数（已做上限纠正）。
	Limit int `json:"limit"`

	// Stale 表示数据来自过期缓存（离线降级或刷新失败）。
	Stale bool `json:"stale"`

	// FetchedAtMs 为清单的获取时间（UTC Unix 毫秒）。
	FetchedAtMs int64 `json:"fetchedAtMs"`

	// Verified 表示清单是否经过哈希校验；本卡恒为 false。
	Verified bool `json:"verified"`

	// WarningCode 为非致命告警码，目前只有 MANIFEST_STALE。
	WarningCode string `json:"warningCode"`
}

// ManifestStatus 是版本清单的可用性状态（不含任何清单条目）。
type ManifestStatus struct {
	// Available 表示当前是否有可用的清单（内存或缓存）。
	Available bool `json:"available"`

	// FetchedAtMs 为清单获取时间（UTC Unix 毫秒）；不可用时为 0。
	FetchedAtMs int64 `json:"fetchedAtMs"`

	// Stale 表示当前清单已过期或来自离线降级。
	Stale bool `json:"stale"`

	// Count 为清单中的版本条数。
	Count int `json:"count"`

	// SourceHost 为清单来源 host（只有 host，不含路径与查询串）。
	SourceHost string `json:"sourceHost"`

	// LastErrorCode 为最近一次获取失败的稳定错误码；成功时为 ""。
	LastErrorCode string `json:"lastErrorCode"`

	// Verified 表示清单是否经过哈希校验；本卡恒为 false。
	Verified bool `json:"verified"`
}

// manifestDocument 是 version_manifest_v2.json 的 JSON 映射。
//
// 只声明本包需要的字段：官方清单还有 `complianceLevel` 等字段，
// 本卡不使用，因此不声明（encoding/json 会忽略未知字段）。
type manifestDocument struct {
	// Latest 为最新正式版与最新快照的 id。
	Latest struct {
		// Release 为最新正式版 id。
		Release string `json:"release"`

		// Snapshot 为最新快照 id。
		Snapshot string `json:"snapshot"`
	} `json:"latest"`

	// Versions 为全部版本条目（官方约数百条）。
	Versions []manifestEntry `json:"versions"`
}

// manifestEntry 是清单中的单条版本记录。
type manifestEntry struct {
	// ID 为版本标识。
	ID string `json:"id"`

	// Type 为版本类型。
	Type string `json:"type"`

	// URL 为该版本 JSON 的下载地址；只在后端流转，不进 DTO。
	URL string `json:"url"`

	// ReleaseTime 为发布时间（RFC3339 文本）。
	ReleaseTime string `json:"releaseTime"`

	// SHA1 为版本 JSON 的 SHA-1。
	SHA1 string `json:"sha1"`

	// Size 为版本 JSON 的字节数。
	Size int64 `json:"size"`
}

// manifestMeta 是缓存旁边 `.meta.json` 的内容。
//
// 与清单文件分开保存：清单文件保持官方原始字节（便于后续卡按官方格式复用），
// 元数据记录获取时间、ETag 与内容摘要，用于 TTL 判定与排查。
type manifestMeta struct {
	// FetchedAtMs 为获取时间（UTC Unix 毫秒）。
	FetchedAtMs int64 `json:"fetchedAtMs"`

	// ETag 为远端返回的 ETag（可能为空），用于下次条件请求。
	ETag string `json:"etag,omitempty"`

	// SHA256 为清单原始字节的 SHA-256（本包自算，仅用于排查，不构成信任链）。
	SHA256 string `json:"sha256"`

	// Size 为清单原始字节数。
	Size int64 `json:"size"`

	// SourceHost 为来源 host（默认 piston-meta.mojang.com）。
	SourceHost string `json:"sourceHost"`
}

// manifestSnapshot 是包内持有的清单状态。
//
// 它由内存状态或磁盘缓存构建；对外只通过 DTO 暴露，调用方不得修改其字段。
type manifestSnapshot struct {
	// versions 为全部版本条目（已校验）。
	versions []manifestVersion

	// latestRelease 为最新正式版 id。
	latestRelease string

	// latestSnapshot 为最新快照 id。
	latestSnapshot string

	// fetchedAtMs 为获取时间（UTC Unix 毫秒）。
	fetchedAtMs int64

	// stale 表示数据已过期或来自离线降级。
	stale bool

	// verified 表示是否经过哈希校验；本包恒为 false。
	verified bool

	// sourceHost 为来源 host。
	sourceHost string

	// lastErrorCode 为最近一次获取失败的稳定错误码。
	lastErrorCode string

	// warningCode 为非致命告警码（MANIFEST_STALE）。
	warningCode string

	// etag 为远端 ETag，供条件请求使用。
	etag string
}

// manifestVersion 是校验后的版本条目。
type manifestVersion struct {
	// id 为版本标识。
	id string

	// versionType 为版本类型。
	versionType string

	// url 为版本 JSON 地址（只在后端流转）。
	url string

	// releaseTime 为发布时间文本。
	releaseTime string

	// releaseTimeUnixMs 为发布时间的时间戳，用于排序；解析失败时为 0。
	releaseTimeUnixMs int64

	// sha1 为版本 JSON 的 SHA-1。
	sha1 string

	// size 为版本 JSON 的字节数。
	size int64
}

// VersionDetailRequest 是 GetVersionDetail 的请求。
type VersionDetailRequest struct {
	// VersionID 为要查询的版本标识。
	VersionID string `json:"versionId"`
}

// ResolveInstallPlanRequest 是 ResolveInstallPlan 的请求。
type ResolveInstallPlanRequest struct {
	// VersionID 为要生成安装计划的版本标识。
	VersionID string `json:"versionId"`
}

// VersionDetail 是版本详情（对外 DTO）。
//
// 字段口径见接口冻结清单 §2.1：不含任何绝对路径与下载地址；
// plan 只给摘要，完整制品清单由 ResolveInstallPlan 提供。
type VersionDetail struct {
	// ID 为版本标识。
	ID string `json:"id"`

	// Type 为版本类型。
	Type string `json:"type"`

	// ReleaseTime 为发布时间（ISO8601 文本）。
	ReleaseTime string `json:"releaseTime"`

	// Size 为版本 JSON 文件的字节数。
	Size int64 `json:"size"`

	// JavaRequirement 为 Java 版本要求。
	JavaRequirement RuntimeRequirement `json:"javaRequirement"`

	// Plan 为安装计划摘要。
	Plan InstallPlanSummary `json:"plan"`

	// Compat 为兼容状态报告。
	Compat CompatReport `json:"compat"`

	// Warnings 为非致命告警。
	Warnings []PlanWarning `json:"warnings"`
}

// RuntimeRequirement 描述 Java 运行时要求。
type RuntimeRequirement struct {
	// Major 为主版本号；0 表示元数据未声明（不得猜测）。
	Major int `json:"major"`

	// Component 为运行时组件标识（如 jre-legacy）。
	Component string `json:"component,omitempty"`

	// Platform 为平台名（windows/osx/linux）。
	Platform string `json:"platform"`

	// Arch 为架构名。
	Arch string `json:"arch"`

	// Bitness 为位数（本阶段不推断，留空）。
	Bitness string `json:"bitness,omitempty"`

	// Source 为取值来源：metadata（元数据声明）或 unknown。
	Source string `json:"source"`
}

// InstallPlanSummary 是安装计划摘要。
type InstallPlanSummary struct {
	// VersionID 为版本标识。
	VersionID string `json:"versionId"`

	// ResolverVersion 为解析器版本。
	ResolverVersion string `json:"resolverVersion"`

	// PlanHash 为计划哈希（幂等键与诊断依据）。
	PlanHash string `json:"planHash"`

	// ArtifactCount 为制品数量。
	ArtifactCount int `json:"artifactCount"`

	// TotalBytes 为全部制品的期望字节总数。
	TotalBytes int64 `json:"totalBytes"`

	// GeneratedAtMs 为生成时间戳（不参与哈希）。
	GeneratedAtMs int64 `json:"generatedAtMs"`
}

// InstallPlanDTO 是完整的安装计划（对外 DTO）。
type InstallPlanDTO struct {
	// Summary 为计划摘要。
	Summary InstallPlanSummary `json:"summary"`

	// Artifacts 为已按稳定顺序排序的制品列表。
	Artifacts []PlanArtifactView `json:"artifacts"`

	// Warnings 为非致命告警。
	Warnings []PlanWarning `json:"warnings"`

	// Compatibility 为兼容状态报告。
	Compatibility CompatReport `json:"compatibility"`
}

// PlanArtifactView 是单个制品（对外 DTO）。
//
// SourceURI 仅供后端下载使用：接口冻结清单 §2.1 要求面向前端的 DTO
// 不含下载地址，因此构造时一律留空。
type PlanArtifactView struct {
	// Kind 为制品类别。
	Kind string `json:"kind"`

	// LogicalKey 为逻辑键。
	LogicalKey string `json:"logicalKey"`

	// Version 为归属版本。
	Version string `json:"version"`

	// SourceID 为来源标识。
	SourceID string `json:"sourceId"`

	// SourceURI 为下载地址；对前端恒为空。
	SourceURI string `json:"sourceUri,omitempty"`

	// ExpectedSize 为期望字节数。
	ExpectedSize int64 `json:"expectedSize"`

	// Hashes 为内容摘要。
	Hashes PlanArtifactHashes `json:"hashes"`

	// TargetRelPath 为数据根内的目标相对路径（不含绝对路径）。
	TargetRelPath string `json:"targetRelPath"`

	// Immutable 表示内容寻址制品不可变。
	Immutable bool `json:"immutable"`

	// Extract 表示需要解压。
	Extract bool `json:"extract"`

	// NativesClassifier 为本地库 classifier（仅本地库制品）。
	NativesClassifier string `json:"nativesClassifier,omitempty"`

	// ExtractExclude 为解压时必须排除的条目（原样保留自元数据）。
	ExtractExclude []string `json:"extractExclude,omitempty"`
}

// PlanArtifactHashes 是制品的内容摘要。
type PlanArtifactHashes struct {
	// SHA1 为 SHA-1 摘要（P3 必填）。
	SHA1 string `json:"sha1,omitempty"`

	// SHA256 为 SHA-256 摘要（元数据不提供时留空）。
	SHA256 string `json:"sha256,omitempty"`
}

// PlanWarning 是计划中的非致命告警。
type PlanWarning struct {
	// Code 为稳定告警码。
	Code string `json:"code"`

	// Message 为面向用户的中文说明。
	Message string `json:"message"`
}

// CompatReport 是四层兼容状态报告。
type CompatReport struct {
	// VersionID 为版本标识。
	VersionID string `json:"versionId"`

	// Layers 为四层兼容状态。
	Layers CompatLayers `json:"layers"`

	// Status 为状态词（launch_supported/metadata_only/unsupported）。
	Status string `json:"status"`

	// Notes 为补充说明（本卡不产出，保留字段）。
	Notes []PlanWarning `json:"notes,omitempty"`
}

// CompatLayers 是四层兼容状态。
//
// loaderSupported 在 P3 恒为 false：加载器安装属后续阶段。
type CompatLayers struct {
	// MetadataSupported 表示元数据可读。
	MetadataSupported bool `json:"metadataSupported"`

	// InstallSupported 表示可生成完整安装计划。
	InstallSupported bool `json:"installSupported"`

	// LaunchSupported 表示可用于启动。
	LaunchSupported bool `json:"launchSupported"`

	// LoaderSupported 表示支持加载器安装。
	LoaderSupported bool `json:"loaderSupported"`
}

// 兼容性结论状态词（《兼容性矩阵》§6，P3-10 卡面要求只能取这六个词）。
const (
	// CompatStatusPlanned 表示该版本尚在计划中（P3 不产生，保留给后续阶段）。
	CompatStatusPlanned = "planned"

	// CompatStatusExperimental 表示可安装但存在旧格式差异，需按提示观察。
	CompatStatusExperimental = "experimental"

	// CompatStatusMetadataOnly 表示只能读取元数据，缺安装或启动所需信息。
	CompatStatusMetadataOnly = "metadata_only"

	// CompatStatusSupported 表示元数据、安装计划与启动路径均可用。
	CompatStatusSupported = "supported"

	// CompatStatusDeprecated 表示版本已停止维护（P3 不产生，保留词）。
	CompatStatusDeprecated = "deprecated"

	// CompatStatusUnsupported 表示元数据不可读或结构不受支持。
	CompatStatusUnsupported = "unsupported"
)

// CompatReportRequest 是兼容性报告请求。
//
// VersionID 为版本标识（必填）；GameDirectory 为用户选择的游戏目录（必填），
// 因为兼容性判定需要读取本地版本 JSON 与检查 jar 是否存在。
type CompatReportRequest struct {
	// VersionID 为版本目录名。
	VersionID string `json:"versionId"`

	// GameDirectory 为游戏根目录（绝对路径）。
	GameDirectory string `json:"gameDirectory"`
}

// CompatEvidence 是一条兼容性判定依据。
//
// Code 为稳定依据码（例如 COMPAT_JAVA_UNKNOWN）；Message 为中文说明；
// Empirical 为 true 表示该依据来自本机可验证事实，false 表示规则推导。
type CompatEvidence struct {
	// Code 为依据码。
	Code string `json:"code"`

	// Message 为中文说明。
	Message string `json:"message"`

	// Empirical 表示是否已被本机事实实证。
	Empirical bool `json:"empirical"`
}

// CompatDifferences 是旧版本差异判定的结构化结果。
type CompatDifferences struct {
	// AssetsLayout 为资源索引布局：virtual / objects / unknown。
	AssetsLayout string `json:"assetsLayout"`

	// NativesConvention 为 natives 命名约定：legacy / modern / mixed / none。
	NativesConvention string `json:"nativesConvention"`

	// ArgumentsFormat 为启动参数格式：minecraft_arguments / arguments / both / none。
	ArgumentsFormat string `json:"argumentsFormat"`

	// LoggingConfigured 表示元数据声明了 logging.client。
	LoggingConfigured bool `json:"loggingConfigured"`

	// LibrariesWithoutRules 为缺 rules 的库数量（缺 rules 视为全平台）。
	LibrariesWithoutRules int `json:"librariesWithoutRules"`

	// MapToResources 表示元数据要求资源落到版本私有目录（P3 不启用）。
	MapToResources bool `json:"mapToResources"`
}

// CompatReportView 是兼容性报告（对外 DTO，只含四层布尔与六状态词）。
//
// 不含 P2 内部四态字符串：P2 的 broken / unsupported / metadata_only / launch_supported
// 只作为推导输入，经 Layers 与 Status 表达。
type CompatReportView struct {
	// VersionID 为版本标识。
	VersionID string `json:"versionId"`

	// Evaluated 表示四层判定已基于真实元数据计算（详情页与兼容性报告恒为 true）。
	Evaluated bool `json:"evaluated"`

	// Layers 为四层兼容状态。
	Layers CompatLayers `json:"layers"`

	// Status 为六个结论状态词之一。
	Status string `json:"status"`

	// Evidence 为依据列表（按 code 稳定排序）。
	Evidence []CompatEvidence `json:"evidence"`

	// Differences 为旧版本差异判定结果。
	Differences CompatDifferences `json:"differences"`

	// InheritChain 为继承链（子在前）；无继承时为空数组。
	InheritChain []string `json:"inheritChain"`

	// JavaRequirement 为 Java 需求（缺失时 source=unknown、major=0）。
	JavaRequirement RuntimeRequirement `json:"javaRequirement"`

	// Summary 为一句中文结论摘要。
	Summary string `json:"summary"`
}
