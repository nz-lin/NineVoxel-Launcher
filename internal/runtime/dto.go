// 本文件定义运行时模块对外暴露的数据结构。
//
// 字段与《P3 接口冻结清单》§2.3 逐字段一致（JSON lowerCamelCase），
// 属于该表的冻结契约：不得在前端另立同名结构，也不得在这里新增绝对路径字段。
//
// 安全约束：所有面向前端的 DTO **不得**包含绝对路径、用户名与完整 URL 查询串。
// 因此这里只有 `displayName`（脱敏展示名）与 `runtimeID`（由路径派生但不可逆），
// 原始路径只在本包内部使用，且写入日志前必须先过 `logging.Redact`。
package runtime

// 探测结果的取值常量。
//
// 取值用英文标识而不是中文：它们会进入 JSON、日志与后续卡的数据库字段，
// 稳定标识比展示文案更重要；中文展示由前端映射。
const (
	// KindJDK 表示 Java Development Kit。
	KindJDK = "jdk"
	// KindJRE 表示 Java Runtime Environment。
	KindJRE = "jre"
	// KindUnknown 表示 `java -version` 输出不足以判定 JDK/JRE。
	//
	// 不猜：多数发行版的版本行同时包含 `JDK` 与 `Runtime Environment` 字样，
	// 从版本行推断 JDK/JRE 不可靠，因此只在输出明确出现 `JDK` 时才判定为 jdk。
	KindUnknown = "unknown"
)

// Vendor 取值。未知厂商用 `unknown`，界面显示为「Java」。
const (
	VendorOracle    = "oracle"
	VendorAdoptium  = "adoptium"
	VendorMicrosoft = "microsoft"
	VendorZulu      = "zulu"
	VendorOpenJDK   = "openjdk"
	VendorOther     = "other"
	VendorUnknown   = "unknown"
)

// 架构取值。
const (
	// ArchX86 表示 32 位 x86。
	ArchX86 = "x86"
	// ArchX64 表示 64 位 x86（`x86_64` / `amd64`）。
	ArchX64 = "x64"
	// ArchAarch64 表示 64 位 ARM（`aarch64` / `arm64`）。
	ArchAarch64 = "aarch64"
	// ArchUnknown 表示输出里没有可识别的架构信息。
	ArchUnknown = "unknown"
)

// 位数取值。
//
// 用字符串而不是整数：与 `internal/version` 的 `RuntimeRequirement.bitness`
// 保持同一类型，避免同一个前端字段在不同模块里出现两种类型。
const (
	// Bitness32 表示 32 位。
	Bitness32 = "32"
	// Bitness64 表示 64 位。
	Bitness64 = "64"
	// BitnessUnknown 表示位数未知（空字符串，JSON 中省略）。
	BitnessUnknown = ""
)

// Runtime 来源取值。
const (
	// SourceLocal 表示探测到的本机 Java。
	SourceLocal = "local"
	// SourceDownloaded 表示启动器安装的 Runtime（P3-09 使用，本卡不产生）。
	SourceDownloaded = "downloaded"
)

// 需求来源取值。
const (
	// RequirementSourceMetadata 表示需求来自版本元数据的明确声明。
	RequirementSourceMetadata = "metadata"
	// RequirementSourceUnknown 表示元数据没有声明，界面必须提示用户手动选择。
	RequirementSourceUnknown = "unknown"
)

// WarningJavaVersionUnknown 是「元数据未声明 Java 版本」的警告码。
//
// 与 `internal/version` 的 `WarningJavaVersionUnknown` 同名同值：
// 同一现象在两个模块里必须用同一个码，否则前端要写两套映射。
const WarningJavaVersionUnknown = "JAVA_VERSION_UNKNOWN"

// DetectRuntimeRequest 是探测请求。
type DetectRuntimeRequest struct {
	// Refresh 为 true 时绕过 5 分钟缓存重新探测。
	//
	// 默认 false：界面上反复切换页面不应该反复启动子进程。
	Refresh bool `json:"refresh"`
}

// RuntimeProbeWarning 是一条探测告警。
//
// 告警不阻断整体结果：某个候选失败时其余候选仍然返回，
// 失败原因以告警形式附带，界面可折叠展示。
type RuntimeProbeWarning struct {
	// Code 为稳定告警码（探测失败时复用错误码，例如 `RUNTIME_PROBE_002`）。
	Code string `json:"code"`
	// Message 为可直接展示的简体中文文案。
	Message string `json:"message"`
}

// RuntimeSummary 是单个 Java 运行时的脱敏摘要。
//
// 不含绝对路径：`displayName` 由厂商 + 主版本 + 架构拼成，
// `runtimeID` 由「归一化路径 + 完整版本 + 架构」的 SHA-256 前缀派生。
type RuntimeSummary struct {
	// RuntimeID 为稳定标识；同一份 Java 在路径不变时 id 不变。
	RuntimeID string `json:"runtimeId"`
	// DisplayName 为展示名，例如 `Adoptium 21 (x64)`。
	DisplayName string `json:"displayName"`
	// Kind 为 `jdk` / `jre` / `unknown`。
	Kind string `json:"kind"`
	// Vendor 为厂商标识（`adoptium` 等），未知为 `unknown`。
	Vendor string `json:"vendor"`
	// MajorVersion 为主版本号（`1.8.0_401` → 8，`21.0.1` → 21）。
	MajorVersion int `json:"majorVersion"`
	// FullVersion 为完整版本串（来自引号内文本或 build 串）。
	FullVersion string `json:"fullVersion"`
	// Arch 为 `x86` / `x64` / `aarch64` / `unknown`。
	Arch string `json:"arch"`
	// Bitness 为 `32` / `64`；未知时为空字符串（JSON 省略）。
	Bitness string `json:"bitness,omitempty"`
	// Source 为 `local` / `downloaded`；本卡恒为 `local`。
	Source string `json:"source"`
	// Compatible 表示该 Java 能否在当前进程环境里使用。
	Compatible bool `json:"compatible"`
	// CompatibleReason 为不兼容原因；兼容时为空。
	CompatibleReason string `json:"compatibleReason,omitempty"`
	// Selected 表示是否为当前选中的 Runtime；P3-08 恒为 false（选中属 P3-09）。
	Selected bool `json:"selected"`
	// VerifiedAtMs 为最近一次探测成功的时间戳；本卡不落库，仅本次结果有效。
	VerifiedAtMs int64 `json:"verifiedAtMs,omitempty"`
}

// RuntimeProbeResult 是一次探测的完整结果。
type RuntimeProbeResult struct {
	// Items 为探测成功的 Java 列表（按主版本降序、同版本按展示名排序）。
	Items []RuntimeSummary `json:"items"`
	// ProbedAtMs 为本次结果的时间戳（毫秒）。
	ProbedAtMs int64 `json:"probedAtMs"`
	// CacheHit 表示本次是否直接命中缓存（未重新执行探测）。
	CacheHit bool `json:"cacheHit"`
	// Warnings 为候选级失败原因与聚合提示。
	Warnings []RuntimeProbeWarning `json:"warnings"`
}

// RuntimeRequirement 是某个 Minecraft 版本对 Java 的需求。
//
// 只从元数据派生：`source=unknown` 时 `major` 为 0，界面必须提示用户手动选择，
// 不得按 Minecraft 版本号猜测（D-1 / D-39）。
type RuntimeRequirement struct {
	// Major 为主版本号；0 表示元数据未声明。
	Major int `json:"major"`
	// Component 为官方组件名，例如 `java-runtime-gamma`；未声明时为空。
	Component string `json:"component"`
	// Platform 为当前平台标识（`windows` / `darwin` / `linux`）。
	Platform string `json:"platform"`
	// Arch 为当前进程架构标识（`x64` / `x86` / `aarch64`）。
	Arch string `json:"arch"`
	// Bitness 为当前进程位数（`32` / `64`）。
	Bitness string `json:"bitness,omitempty"`
	// Source 为 `metadata` / `unknown`。
	Source string `json:"source"`
	// Warnings 为需求映射过程中的提示（例如元数据未声明版本）。
	Warnings []RuntimeProbeWarning `json:"warnings,omitempty"`
}

// RuntimeRequirementRequest 是需求解析请求。
type RuntimeRequirementRequest struct {
	// VersionID 为 Minecraft 版本 id（例如 `1.21.4`）。
	VersionID string `json:"versionId"`
}

// RuntimeList 是本机 Runtime 列表。
//
// P3-08 只返回「探测到的本机 Java」；已安装 Runtime 由 P3-09 追加。
type RuntimeList struct {
	// Items 为 Runtime 列表。
	Items []RuntimeSummary `json:"items"`
	// SelectedRuntimeID 为当前选中的 Runtime；P3-08 恒为空。
	SelectedRuntimeID string `json:"selectedRuntimeId,omitempty"`
	// Requirement 为该列表对应的需求快照；P3-08 不绑定版本，恒为 nil。
	Requirement *RuntimeRequirement `json:"requirement,omitempty"`
}

// InstallRuntimeRequest 是安装官方 Runtime 的请求（冻结清单 §2.3）。
type InstallRuntimeRequest struct {
	// Component 为官方组件名，例如 `java-runtime-gamma`。
	//
	// 必须来自版本元数据 `javaVersion.component`；元数据没有声明时
	// 不得猜（D-39），界面应引导用户手动选择 Java 而不是自动安装。
	Component string `json:"component"`

	// Platform 为期望平台；空值表示按当前进程平台选择。
	//
	// 保留该字段是为了让界面在诊断时能显式指定平台（跨平台排查），
	// 但当前实现只接受与当前进程一致的值。
	Platform string `json:"platform"`

	// AcceptLicense 表示用户已阅读并接受许可提示。
	//
	// 必须为 true：不持久化豁免（D-28：不新增设置 key），每次安装都要重新确认。
	AcceptLicense bool `json:"acceptLicense"`
}

// SelectRuntimeRequest 是选择 Runtime 的请求（冻结清单 §2.3）。
type SelectRuntimeRequest struct {
	// RuntimeID 为要选中的 Runtime 标识。
	RuntimeID string `json:"runtimeId"`

	// Force 为 true 时允许选择与当前系统架构不匹配的 Runtime。
	Force bool `json:"force"`
}

// InstallRuntimeSourceRequest 是从第三方来源安装 Runtime 的请求（P3-14）。
type InstallRuntimeSourceRequest struct {
	// MajorVersion 为要安装的 Java 大版本（必须 > 0）。
	//
	// 通常来自版本元数据的 `javaVersion.majorVersion`；元数据未声明时不得猜（D-39）。
	MajorVersion int `json:"majorVersion"`

	// AcceptLicense 表示用户已阅读并接受许可提示。
	//
	// 必须为 true：与官方 Runtime 安装一致，不持久化豁免（D-28）。
	AcceptLicense bool `json:"acceptLicense"`
}

// RuntimeSourceInfo 描述某个 Java 大版本可从第三方源获取的最新构建（P3-14）。
//
// 安全约束：DownloadHosts 只放 host，不放完整下载地址——前端展示「来源」用得着
// host 就足够了，完整地址一旦下发就可能被用于绕过后端校验。
type RuntimeSourceInfo struct {
	// Source 为来源标识，固定为 `adoptium`。
	Source string `json:"source"`

	// MajorVersion 为主版本号。
	MajorVersion int `json:"majorVersion"`

	// Semver 为语义化版本，例如 `21.0.13+9.0.LTS`（展示用）。
	Semver string `json:"semver"`

	// FullVersion 为 OpenJDK 版本串，例如 `21.0.13+9-LTS`（落库与比较用）。
	FullVersion string `json:"fullVersion"`

	// OS 为镜像路径里的 os 段（windows / mac / linux）。
	OS string `json:"os"`

	// Arch 为架构段（x64 / aarch64）。
	Arch string `json:"arch"`

	// SizeBytes 为归档字节数。
	SizeBytes int64 `json:"sizeBytes"`

	// SHA256 为归档的 SHA-256（小写十六进制）。
	SHA256 string `json:"sha256"`

	// DownloadHosts 为候选来源的 host 列表（不含协议与路径）。
	DownloadHosts []string `json:"downloadHosts"`
}

// RuntimeUpdateInfo 描述一个已安装 Runtime 的更新可用性（P3-14）。
type RuntimeUpdateInfo struct {
	// RuntimeID 为已安装 Runtime 的标识。
	RuntimeID string `json:"runtimeId"`

	// MajorVersion 为主版本号。
	MajorVersion int `json:"majorVersion"`

	// InstalledFullVersion 为本地已安装的完整版本串。
	InstalledFullVersion string `json:"installedFullVersion"`

	// LatestFullVersion 为远端最新完整版本串；检查失败时为空。
	LatestFullVersion string `json:"latestFullVersion"`

	// UpdateAvailable 表示远端版本严格大于本地版本。
	UpdateAvailable bool `json:"updateAvailable"`

	// LatestSizeBytes 为远端归档字节数；检查失败时为 0。
	LatestSizeBytes int64 `json:"latestSizeBytes"`

	// CheckedAtMs 为本次检查的时间（毫秒）。
	CheckedAtMs int64 `json:"checkedAtMs"`

	// CheckError 为空表示检查成功；非空为中文失败原因。
	CheckError string `json:"checkError"`
}
