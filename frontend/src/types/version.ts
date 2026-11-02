/**
 * 版本模块的前端类型与常量。
 *
 * 字段与 Go 侧 `internal/version/dto.go` 的 JSON 字段逐字段一致（lowerCamelCase），
 * 属于《P3 接口冻结清单》§2.1 的冻结契约：不得在前端另立同名结构。
 *
 * 安全约束：DTO 不包含版本 JSON 的 `url` 字段，前端也就没有拿到它的途径；
 * 需要下载某个版本时由后端按 id 重新解析，不经过前端转发地址。
 */

/** 版本类型取值。 */
export type VersionType = 'release' | 'snapshot' | 'old_beta' | 'old_alpha'

/**
 * 兼容性四层判定（D-24 已冻结）。
 *
 * 四层回答「能走到哪一步」：元数据可读 / 可安装 / 可启动 / 加载器可用。
 * P3-01 只提供清单数据，因此后端返回的是保守默认值：
 * `metadataSupported` 为 true，其余三层为 false，真实值由 P3-10 填充。
 */
export interface CompatLayer {
  /**
   * 兼容性是否真的算过。
   *
   * 列表页（VersionSummary.compat）恒为 false：后端不逐版本解析版本 JSON，
   * 此时四层字段没有可信语义，前端不得据此展示「支持 / 不支持」。
   * 版本详情与兼容性报告里的 layers 不带该字段（视为已评估）。
   */
  evaluated?: boolean
  /** 版本 JSON 元数据是否可读取。 */
  metadataSupported: boolean
  /** 本阶段是否支持为该版本创建下载任务。 */
  installSupported: boolean
  /** 该版本是否可被启动器启动。 */
  launchSupported: boolean
  /** 加载器是否可用；P3 恒为 false。 */
  loaderSupported: boolean
}

/** 单个版本的展示信息。 */
export interface VersionSummary {
  /** 版本标识，例如 `1.21.4` 或 `25w02a`。 */
  id: string
  /** 版本类型。 */
  type: VersionType
  /** 发布时间（RFC3339 文本）。 */
  releaseTime: string
  /** 版本 JSON 的 SHA-1（供后端校验使用，前端只做展示）。 */
  sha1: string
  /** 版本 JSON 的字节数。 */
  size: number
  /** 是否已安装；P3-01 阶段恒为 false。 */
  installed: boolean
  /** 兼容性四层判定。 */
  compat: CompatLayer
  /** 是否经过哈希校验；官方清单没有自校验哈希，恒为 false。 */
  verified: boolean
}

/** 版本列表查询请求。 */
export interface VersionListRequest {
  /** 类型过滤，空字符串表示默认的 release + snapshot。 */
  type?: VersionType | string
  /** 分页起始下标。 */
  offset?: number
  /** 单页条数；0 或省略按默认 50，上限 200。 */
  limit?: number
}

/** 版本列表查询结果。 */
export interface VersionListResult {
  /** 当前页的版本列表。 */
  items: VersionSummary[]
  /** 过滤后的总条数（不受分页影响）。 */
  total: number
  /** 本次实际使用的分页起点。 */
  offset: number
  /** 本次实际使用的单页条数。 */
  limit: number
  /** 是否来自过期缓存（离线降级或刷新失败）。 */
  stale: boolean
  /** 清单获取时间（UTC Unix 毫秒）。 */
  fetchedAtMs: number
  /** 是否经过哈希校验；恒为 false。 */
  verified: boolean
  /** 非致命告警码，目前只有 MANIFEST_STALE；无告警时为空字符串。 */
  warningCode: string
}

/** 版本清单的可用性状态（不含清单条目）。 */
export interface ManifestStatus {
  /** 当前是否有可用清单。 */
  available: boolean
  /** 清单获取时间（UTC Unix 毫秒）；不可用时为 0。 */
  fetchedAtMs: number
  /** 清单是否过期或来自离线降级。 */
  stale: boolean
  /** 清单中的版本条数。 */
  count: number
  /** 清单来源 host（只有 host，不含路径）。 */
  sourceHost: string
  /** 最近一次获取失败的稳定错误码；成功时为空字符串。 */
  lastErrorCode: string
  /** 是否经过哈希校验；恒为 false。 */
  verified: boolean
}

/** 版本模块使用的稳定错误码（与 Go 侧 `internal/version/errors.go` 逐字一致）。 */
export const VERSION_ERROR_CODE = {
  /** 清单获取失败（网络、状态码、超时），可重试。 */
  manifestFetch: 'VERSION_MANIFEST_001',
  /** 清单结构非法，不可重试。 */
  manifestInvalid: 'VERSION_MANIFEST_002',
  /** 清单地址非法（非 https、host 不在白名单、重定向越界）。 */
  urlInvalid: 'VERSION_URL_001',
  /** 数据根不可用，缓存无法读写。 */
  dataRootUnavailable: 'VERSION_LAYOUT_001',
} as const

/** 版本模块的非致命告警码。 */
export const VERSION_WARNING_CODE = {
  /** 当前列表来自过期缓存（离线降级）。 */
  manifestStale: 'MANIFEST_STALE',
} as const

/** 版本列表分页上限，与 Go 侧 `versionListMaxLimit` 一致。 */
export const VERSION_LIST_LIMITS = {
  /** 默认单页条数。 */
  defaultLimit: 50,
  /** 单页条数上限。 */
  maxLimit: 200,
} as const

/** 默认展示的版本类型（旧预览版需要显式请求）。 */
export const DEFAULT_VERSION_TYPES: readonly VersionType[] = ['release', 'snapshot']

/** 版本详情查询请求（P3-02）。 */
export interface VersionDetailRequest {
  /** 版本标识，例如 `1.20.1`。 */
  versionId: string
}

/** 安装计划解析请求（P3-02）。 */
export interface ResolveInstallPlanRequest {
  /** 版本标识，例如 `1.20.1`。 */
  versionId: string
}

/** 制品的哈希摘要（D-27：内容寻址使用 SHA-1）。 */
export interface PlanArtifactHashes {
  /** SHA-1 摘要（40 位小写十六进制）；安装制品必有。 */
  sha1?: string
  /** SHA-256 摘要（64 位小写十六进制）；官方元数据通常不提供。 */
  sha256?: string
}

/**
 * 安装计划中的单个制品。
 *
 * 与 Go 侧 `PlanArtifactView` 一致。`sourceUri` 只用于后端内部流转，
 * 后端构造前端 DTO 时一律置空，界面不得依赖它渲染下载地址。
 */
export interface PlanArtifactView {
  /** 制品类别，取值见 {@link PLAN_ARTIFACT_KIND}。 */
  kind: string
  /** 逻辑键，用于在计划内唯一定位一个来源条目。 */
  logicalKey: string
  /** 所属版本标识。 */
  version: string
  /** 来源标识；P3 恒为 `mojang`。 */
  sourceId: string
  /** 来源地址；仅后端使用，前端恒为空。 */
  sourceUri?: string
  /** 期望字节数。 */
  expectedSize: number
  /** 哈希摘要。 */
  hashes: PlanArtifactHashes
  /** 相对数据根的落盘路径（不含盘符、不含 `..`）。 */
  targetRelPath: string
  /** 是否为不可变制品（可安全去重）。 */
  immutable: boolean
  /** 是否需要解压到目标目录。 */
  extract: boolean
  /** 本地库的 classifier（仅 kind=native 时非空）。 */
  nativesClassifier?: string
  /** 解压时排除的路径前缀（仅 kind=native 时可能非空）。 */
  extractExclude?: string[]
}

/** 计划告警：不阻断安装，但界面必须展示。 */
export interface PlanWarning {
  /** 稳定告警码，取值见 {@link PLAN_WARNING_CODE}。 */
  code: string
  /** 面向用户的中文说明。 */
  message: string
}

/** 兼容状态词（四层判定的归约结果，D-24）。 */
export type CompatStatus = 'launch_supported' | 'metadata_only' | 'unsupported' | 'broken'

/** 兼容性报告：四层判定 + 状态词 + 说明。 */
export interface CompatReport {
  /** 版本标识。 */
  versionId: string
  /** 四层判定。 */
  layers: CompatLayer
  /** 归约后的状态词。 */
  status: CompatStatus
  /** 判定说明（例如为何不可安装）。 */
  notes: PlanWarning[]
}

/** 安装计划摘要。 */
export interface InstallPlanSummary {
  /** 版本标识。 */
  versionId: string
  /** 解析器版本；语义变化时递增，用于解释 plan_hash 差异。 */
  resolverVersion: string
  /** 计划哈希（64 位小写十六进制），同一输入必得同一值。 */
  planHash: string
  /** 制品数量。 */
  artifactCount: number
  /** 制品总字节数。 */
  totalBytes: number
  /** 计划生成时间（UTC Unix 毫秒）；不参与 plan_hash。 */
  generatedAtMs: number
}

/** 安装计划（P3-02 只返回结构，不落库、不下载）。 */
export interface InstallPlanDTO {
  /** 计划摘要。 */
  summary: InstallPlanSummary
  /** 制品列表（按落盘路径稳定排序）。 */
  artifacts: PlanArtifactView[]
  /** 计划告警。 */
  warnings: PlanWarning[]
  /** 兼容性报告。 */
  compatibility: CompatReport
}

/** Java 版本要求（元数据缺失时 major=0、source=unknown，不做猜测）。 */
export interface RuntimeRequirement {
  /** 主版本号；0 表示元数据未声明。 */
  major: number
  /** JRE 组件名，例如 `java-runtime-gamma`。 */
  component: string
  /** 目标平台标识。 */
  platform: string
  /** 目标架构标识。 */
  arch: string
  /** 位宽标识；空字符串表示未声明。 */
  bitness: string
  /** 来源：`metadata` 或 `unknown`。 */
  source: string
}

/** 版本详情：元数据 + 计划摘要 + 兼容性。 */
export interface VersionDetail {
  /** 版本标识。 */
  id: string
  /** 版本类型。 */
  type: VersionType
  /** 发布时间（RFC3339 文本）。 */
  releaseTime: string
  /** 版本 JSON 的字节数。 */
  size: number
  /** Java 版本要求。 */
  javaRequirement: RuntimeRequirement
  /** 安装计划摘要。 */
  plan: InstallPlanSummary
  /** 兼容性报告。 */
  compat: CompatReport
  /** 版本级告警（继承链缺失、层级超限等）。 */
  warnings: PlanWarning[]
}

/** 计划中的制品类别。 */
export const PLAN_ARTIFACT_KIND = {
  /** 客户端 jar。 */
  client: 'client',
  /** 普通库。 */
  library: 'library',
  /** 资源索引本身（对象由 P3-07 展开）。 */
  asset: 'asset',
  /** 本地库（需要解压）。 */
  native: 'native',
  /** 日志配置。 */
  logging: 'logging',
} as const

/** 安装计划使用的稳定告警码（与 Go 侧 `internal/version/plan.go` 逐字一致）。 */
export const PLAN_WARNING_CODE = {
  /** 缺少客户端 jar 下载信息，无法安装。 */
  clientDownloadMissing: 'CLIENT_DOWNLOAD_MISSING',
  /** 缺少资源索引，无法安装。 */
  assetIndexMissing: 'ASSET_INDEX_MISSING',
  /** 库条目信息不可用，已跳过。 */
  librarySkipped: 'LIBRARY_SKIPPED',
  /** 本地库条目不可用，已跳过。 */
  nativeSkipped: 'NATIVE_SKIPPED',
  /** 元数据未声明 Java 版本要求。 */
  javaVersionUnknown: 'JAVA_VERSION_UNKNOWN',
  /** 继承链上的父版本缺失。 */
  inheritParentMissing: 'INHERIT_PARENT_MISSING',
  /** 继承层级超过上限。 */
  inheritTooDeep: 'INHERIT_TOO_DEEP',
} as const


/** 兼容性结论状态词（《兼容性矩阵》§6）；P3-10 起对外只用这六个词。 */
export type CompatStatusWord =
  | 'planned'
  | 'experimental'
  | 'metadata_only'
  | 'supported'
  | 'deprecated'
  | 'unsupported'

/** 状态词取值表（与 Go 侧 `internal/version/dto.go` 的 CompatStatus* 常量逐字一致）。 */
export const COMPAT_STATUS_WORD = {
  /** 尚在计划中（P3 不产生）。 */
  planned: 'planned',
  /** 可安装但存在旧格式差异。 */
  experimental: 'experimental',
  /** 只能读取元数据。 */
  metadataOnly: 'metadata_only',
  /** 元数据、安装计划与启动路径均可用。 */
  supported: 'supported',
  /** 版本已停止维护（P3 不产生）。 */
  deprecated: 'deprecated',
  /** 元数据不可读或结构不受支持。 */
  unsupported: 'unsupported',
} as const

/** 兼容性判定依据。 */
export interface CompatEvidence {
  /** 稳定依据码，见 COMPAT_NOTE_CODE。 */
  code: string
  /** 面向用户的中文说明。 */
  message: string
  /** 是否已被本机事实实证；false 表示规则推导。 */
  empirical: boolean
}

/** 旧版本差异判定结果。 */
export interface CompatDifferences {
  /** 资源索引布局：virtual / objects / unknown。 */
  assetsLayout: string
  /** natives 命名约定：legacy / modern / mixed / none。 */
  nativesConvention: string
  /** 启动参数格式：minecraft_arguments / arguments / both / none。 */
  argumentsFormat: string
  /** 元数据是否声明 logging.client。 */
  loggingConfigured: boolean
  /** 缺 rules 的库数量（缺 rules 视为全平台）。 */
  librariesWithoutRules: number
  /** 元数据是否要求资源落到版本私有目录（P3 不启用）。 */
  mapToResources: boolean
}

/** 兼容性报告请求。 */
export interface CompatReportRequest {
  /** 版本目录名。 */
  versionId: string
  /** 游戏根目录（绝对路径）。 */
  gameDirectory: string
}

/** 兼容性报告（四层布尔 + 六状态词 + 依据）。 */
export interface CompatReportView {
  /** 版本标识。 */
  versionId: string
  /** 本报告的兼容性是否真的算过；报告一定算过，恒为 true。 */
  evaluated: boolean
  /** 四层兼容状态。 */
  layers: CompatLayer
  /** 六个结论状态词之一。 */
  status: CompatStatusWord
  /** 判定依据（按 code 稳定排序）。 */
  evidence: CompatEvidence[]
  /** 旧版本差异判定。 */
  differences: CompatDifferences
  /** 继承链（子在前）；无继承时为空数组。 */
  inheritChain: string[]
  /** Java 需求（缺失时 major=0、source=unknown）。 */
  javaRequirement: RuntimeRequirement
  /** 一句中文结论摘要。 */
  summary: string
}

/** 兼容性依据码（与 Go 侧 `internal/launch/compat.go` 逐字一致）。 */
export const COMPAT_NOTE_CODE = {
  /** 版本 JSON 能被 P2 解析器成功合并。 */
  metadataOk: 'COMPAT_METADATA_OK',
  /** 版本 JSON 无法解析或继承链不可用。 */
  metadataFailed: 'COMPAT_METADATA_FAILED',
  /** 本地缺少客户端 JAR（只影响启动）。 */
  clientJarMissing: 'COMPAT_CLIENT_JAR_MISSING',
  /** 缺少资源索引标识。 */
  assetIndexMissing: 'COMPAT_ASSET_INDEX_MISSING',
  /** 旧式虚拟资源索引。 */
  assetsVirtual: 'COMPAT_ASSETS_VIRTUAL',
  /** 现代内容寻址资源索引。 */
  assetsObjects: 'COMPAT_ASSETS_OBJECTS',
  /** 资源需组装进版本私有目录（P3 不落盘）。 */
  mapToResources: 'COMPAT_MAP_TO_RESOURCES',
  /** 旧式 natives 命名。 */
  nativesLegacy: 'COMPAT_NATIVES_LEGACY',
  /** 现代 classifier。 */
  nativesModern: 'COMPAT_NATIVES_MODERN',
  /** 旧式 minecraftArguments。 */
  argumentsLegacy: 'COMPAT_ARGUMENTS_LEGACY',
  /** 新式 arguments 结构。 */
  argumentsModern: 'COMPAT_ARGUMENTS_MODERN',
  /** 两套启动参数字段都缺失。 */
  argumentsMissing: 'COMPAT_ARGUMENTS_MISSING',
  /** Java 大版本来自元数据。 */
  javaMetadata: 'COMPAT_JAVA_METADATA',
  /** 元数据未声明 Java 大版本。 */
  javaUnknown: 'COMPAT_JAVA_UNKNOWN',
  /** 元数据没有 logging.client。 */
  loggingAbsent: 'COMPAT_LOGGING_ABSENT',
  /** 元数据声明了 logging.client。 */
  loggingConfigured: 'COMPAT_LOGGING_CONFIGURED',
  /** 存在缺 rules 的库。 */
  librariesWithoutRules: 'COMPAT_LIBRARIES_WITHOUT_RULES',
  /** 存在继承链。 */
  inheritChain: 'COMPAT_INHERIT_CHAIN',
  /** 继承链读取失败。 */
  inheritUnreadable: 'COMPAT_INHERIT_UNREADABLE',
} as const
