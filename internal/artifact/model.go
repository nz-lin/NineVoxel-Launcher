package artifact

// ArtifactKind 是制品的类别。
//
// 取值与《总体架构》第 6 节、`接口冻结清单.md` §2.1 的 `PlanArtifactView.kind` 一致。
// P3 只落盘其中的 client / library / asset / native / runtime；
// mod / resource_pack / shader / plugin 属实例级内容（P5），P3 只建模、不解析目标路径。
type ArtifactKind string

const (
	// KindClient 是版本客户端 JAR（版本私有，落 versions/<id>/）。
	KindClient ArtifactKind = "client"

	// KindLibrary 是版本依赖库 JAR（共享缓存，内容寻址）。
	KindLibrary ArtifactKind = "library"

	// KindAsset 是游戏资源对象（共享缓存；寻址键为 SHA-1，见决策 D-27）。
	KindAsset ArtifactKind = "asset"

	// KindNative 是本地库（natives）压缩包或解压产物（共享缓存）。
	KindNative ArtifactKind = "native"

	// KindRuntime 是 Java Runtime 组件（共享缓存，落 runtimes/<runtimeId>/）。
	KindRuntime ArtifactKind = "runtime"

	// KindMod 是 Mod（实例级可变内容，P5）。
	KindMod ArtifactKind = "mod"

	// KindResourcePack 是资源包（实例级可变内容，P5）。
	KindResourcePack ArtifactKind = "resource_pack"

	// KindShader 是光影包（实例级可变内容，P5）。
	KindShader ArtifactKind = "shader"

	// KindPlugin 是启动器插件包（P6 运行时负责，P3 不落盘）。
	KindPlugin ArtifactKind = "plugin"
)

// HashAlgorithm 是哈希算法标识。
//
// 该值必须显式标注在制品上：决策 D-27 要求「严禁把 SHA-1 值填进 SHA-256 字段」，
// 因此内容寻址路径的生成必须由调用方声明算法，而不是靠字符串长度猜测。
type HashAlgorithm string

const (
	// HashAlgorithmSHA1 是 SHA-1（资产寻址键、libraries 校验）。
	HashAlgorithmSHA1 HashAlgorithm = "sha1"

	// HashAlgorithmSHA256 是 SHA-256（内容寻址的首选算法）。
	HashAlgorithmSHA256 HashAlgorithm = "sha256"

	// HashAlgorithmSHA512 是 SHA-512。
	//
	// P3 只把它记录在 Hashes 里作为附加校验信息，**不用于内容寻址**：
	// 方案库中部分制品元数据只给 SHA-512，没有 SHA-256，无法作为寻址键。
	HashAlgorithmSHA512 HashAlgorithm = "sha512"
)

// Hashes 汇总一个制品的哈希值。
//
// 字段为空的语义是「元数据没有提供该算法」，不是「校验失败」。
// 是否可校验由 IsVerifiable 判定，是否可进共享缓存由 CanShare 判定。
type Hashes struct {
	// SHA1 是 SHA-1 十六进制小写串；资产与 libraries 有该值。
	SHA1 string

	// SHA256 是 SHA-256 十六进制小写串；客户端 JAR 等部分制品可能缺失。
	SHA256 string

	// SHA512 是 SHA-512 十六进制小写串，仅记录、不参与寻址（见 HashAlgorithmSHA512）。
	SHA512 string
}

// Compatibility 描述制品对平台、架构与 Minecraft 版本的约束。
//
// 空切片表示「无约束」，而不是「全不支持」。
// P3 只做静态判定，不做真实运行验证。
type Compatibility struct {
	// Platforms 是允许的操作系统，取值 `windows` / `darwin` / `linux`；空表示不限。
	Platforms []string

	// Arches 是允许的 CPU 架构，取值 `x64` / `x86` / `arm64`；空表示不限。
	Arches []string

	// MinMinecraftVersion 是允许的最低 Minecraft 版本；空表示无下限。
	MinMinecraftVersion string

	// MaxMinecraftVersion 是允许的最高 Minecraft 版本；空表示无上限。
	MaxMinecraftVersion string
}

// Artifact 是一个可安装制品。
//
// 字段与《总体架构》第 6 节、《数据库设计与迁移规范》§3.9 对齐，
// 并与 `接口冻结清单.md` §5.1 的 `artifacts` 表列一一对应。
//
// 关于「P3 不填」的字段（卡面第 1 条要求逐项写明理由，不得静默省略）：
//   - ReferenceCount 不在 Artifact 里：引用计数语义属 P5 的实例引用（决策 D-29），
//     P3 只预留语义（见 CountReferences 注释），不落列、不参与判定。
//   - OwnerInstanceID 同理留空：P3 没有实例概念，实例绑定由 P5 定义。
type Artifact struct {
	// ID 是制品唯一标识（由调用方按稳定规则生成，不含路径）。
	ID string

	// Kind 是制品类别，决定目标根与默认不可变性。
	Kind ArtifactKind

	// LogicalKey 是逻辑键：libraries 为 maven 坐标（`group:artifact:version`），
	// assets 为对象相对路径，client 为 `client`，runtime 为 component 名。
	LogicalKey string

	// Version 是制品版本串（libraries 的 maven 版本、runtime 的组件版本等）。
	Version string

	// SourceID 是来源标识，对应 ArtifactSource.ID。
	SourceID string

	// SourceURI 是下载地址。
	//
	// 该字段**只在后端流转**，不得透出到前端 DTO 或日志（冻结清单 §2.3 安全约束）；
	// 写入数据库前必须去掉查询串。
	SourceURI string

	// ExpectedSize 是期望字节数；0 表示元数据未提供（未知，不等于空文件）。
	ExpectedSize int64

	// Hashes 是元数据提供的哈希集合。
	Hashes Hashes

	// HashAlgorithm 是内容寻址使用的算法，必须与 Hashes 中非空字段一致。
	HashAlgorithm HashAlgorithm

	// TargetRelPath 是相对数据根的目标路径（由 ResolveTarget 或 ObjectRelPath 生成）。
	TargetRelPath string

	// Compatibility 是平台 / 架构 / 版本约束；零值表示无约束。
	Compatibility Compatibility

	// Immutable 表示该制品落盘后是否不可变。
	//
	// 默认值由 DefaultImmutable 按 kind 给出：mod / resource_pack / shader 等
	// 用户可变内容为 false，其余（client / library / asset / native / runtime）为 true。
	Immutable bool

	// VerifiedAtMs 是最近一次校验通过的时间（毫秒时间戳）。
	//
	// 0 表示尚未校验。共享缓存要求 `Immutable && VerifiedAtMs != 0`（卡面第 50 条），
	// 因此它是 CanShare 的硬条件之一。
	VerifiedAtMs int64

	// Extract 表示该制品下载后需要解压（natives、runtime 组件）。
	Extract bool

	// NativesClassifier 是 natives 解压时的分类子目录（如 `natives-windows`），
	// 非 native 制品为空。
	NativesClassifier string
}

// ArtifactSource 是一个制品来源。
//
// 来源只有两类：Mojang 官方源与可选镜像源（`ADR-013`）。
// 官方元数据接口永远只有官方一个候选，镜像只加速制品文件。
type ArtifactSource struct {
	// ID 是来源唯一标识（如 `mojang` / `bmclapi`）。
	ID string

	// DisplayName 是面向用户的来源名称，不含 URL 凭据。
	DisplayName string

	// BaseURL 是来源基地址（镜像带挂载前缀）；只用于后端拼接，不透出前端。
	BaseURL string

	// Official 表示是否为官方源（官方元数据接口只允许官方源）。
	Official bool

	// Priority 是候选顺序，数值越小越优先；相同值按 ID 字典序稳定排序。
	Priority int
}

// ArtifactReference 是「版本 / 安装计划」对某个制品的引用。
//
// 引用本身不持有文件内容，只描述「谁需要它」。P3 用它统计共享制品的引用数，
// 引用计数语义在 P5 落地（决策 D-29：字段预留、语义由 P5 承担）。
type ArtifactReference struct {
	// ArtifactID 是被引用制品的 ID。
	ArtifactID string

	// Kind 是被引用制品的类别（冗余字段，便于按类别统计而不必回查 Artifact）。
	Kind ArtifactKind

	// LogicalKey 是被引用制品的逻辑键（冗余字段，同上）。
	LogicalKey string

	// OwnerVersionID 是引用方版本 id（P3 用版本 id 代替实例 id）。
	OwnerVersionID string

	// Required 表示该引用是否为必需项（false 时缺失不阻断启动）。
	Required bool

	// NativesClassifier 是引用方要求的 natives 分类（仅 native 制品有意义）。
	NativesClassifier string
}

// InstallRecord 是一次安装的结果记录。
//
// **P3 不落库**（决策 D-26：不建 install_records 表，安装结果记在 artifacts 的
// verified_at_ms 上，幂等由 plan_hash 承担）。本结构体保留给 P5：
// 届时 instance_id 的引用语义由 P5 定义，P3 不得提前填值。
type InstallRecord struct {
	// VersionID 是被安装的 Minecraft 版本 id。
	VersionID string

	// PlanHash 是安装计划的幂等键：sha256(版本 id + 解析器版本 + 规范化计划 JSON)。
	PlanHash string

	// ArtifactCount 是本次安装涉及的制品数量。
	ArtifactCount int

	// InstalledAtMs 是安装开始时间（毫秒时间戳）。
	InstalledAtMs int64

	// VerifiedAtMs 是最近一次全量校验通过的时间；0 表示未校验。
	VerifiedAtMs int64

	// InstanceID 是实例标识。
	//
	// **P3 恒为空**：P3 没有实例概念，该字段由 P5 的实例目录承担。
	InstanceID string
}
