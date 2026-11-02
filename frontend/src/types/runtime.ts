/**
 * Runtime（Java 运行时）相关的前端类型与常量。
 *
 * 数据来源：`internal/runtime` 的 RuntimeService。
 * 安全约束：后端 DTO 不返回绝对路径，前端只能拿到 displayName 与稳定 runtimeId。
 */

/** Java 安装形态。 */
export type RuntimeKind = 'jdk' | 'jre' | 'unknown'

/** Java 发行商标识（后端归一化后的枚举值）。 */
export type RuntimeVendor =
  | 'oracle'
  | 'adoptium'
  | 'microsoft'
  | 'zulu'
  | 'openjdk'
  | 'other'
  | 'unknown'

/** CPU 架构标识。 */
export type RuntimeArch = 'x86' | 'x64' | 'aarch64' | 'unknown'

/** 位宽标识；空字符串表示无法确认。 */
export type RuntimeBitness = '32' | '64' | ''

/** 运行时来源：本机探测或后续下载安装。 */
export type RuntimeSource = 'local' | 'downloaded'

/** 需求来源：版本元数据声明，或无法确认。 */
export type RuntimeRequirementSource = 'metadata' | 'unknown'

/** 探测相关的稳定错误码。 */
export const RUNTIME_ERROR_CODE = {
  /** 权限拒绝，无法执行目标文件。 */
  permissionDenied: 'RUNTIME_PROBE_001',
  /** 目标不是可用的 Java 程序。 */
  notJava: 'RUNTIME_PROBE_002',
  /** 探测超时（5 秒）。 */
  timeout: 'RUNTIME_PROBE_003',
  /** 输出格式无法识别。 */
  formatUnsupported: 'RUNTIME_PROBE_004',
  /** 未找到满足需求的运行时。 */
  notFound: 'RUNTIME_NOT_FOUND_001',
  /** 版本元数据不可读或不可解析。 */
  versionMetadata: 'VERSION_METADATA_001',
  /** 安装过程中的下载/校验/提交失败（视阶段可重试）。 */
  installFailed: 'RUNTIME_INSTALL_001',
  /** 解压或路径防护拒绝（不可重试）。 */
  unpackRejected: 'RUNTIME_UNPACK_001',
  /** 所选运行时或需求不满足（不可重试）。 */
  requirementUnmet: 'RUNTIME_REQUIREMENT_001',
  /** 磁盘空间不足（可重试）。 */
  spaceInsufficient: 'DOWNLOAD_SPACE_001',
} as const

/** 探测告警码。 */
export const RUNTIME_WARNING_CODE = {
  /** 元数据未声明 Java 版本，需求来源为 unknown。 */
  javaVersionUnknown: 'JAVA_VERSION_UNKNOWN',
} as const

/** 探测相关上限与默认值（与后端常量保持一致）。 */
export const RUNTIME_PROBE_LIMITS = {
  /** 探测子进程超时（毫秒）。 */
  timeoutMs: 5_000,
  /** 输出读取上限（字节）。 */
  maxOutputBytes: 64 * 1024,
  /** 探测结果内存缓存时长（毫秒）。 */
  cacheTtlMs: 5 * 60 * 1000,
  /** 候选枚举数量上限。 */
  maxCandidates: 32,
  /** 安装根扫描深度。 */
  maxScanDepth: 2,
} as const

/** 探测请求。 */
export interface DetectRuntimeRequest {
  /** 为 true 时绕过 5 分钟缓存重新探测。 */
  refresh?: boolean
}

/** 探测告警。 */
export interface RuntimeProbeWarning {
  /** 稳定告警码。 */
  code: string
  /** 简体中文说明。 */
  message: string
}

/** 单个运行时的对外视图（不含绝对路径）。 */
export interface RuntimeSummary {
  /** 稳定标识，由路径与版本派生，不含路径片段。 */
  runtimeId: string
  /** 用户可见名称，例如 `Adoptium 21 (x64)`。 */
  displayName: string
  /** 安装形态。 */
  kind: RuntimeKind
  /** 发行商。 */
  vendor: RuntimeVendor
  /** 主版本号；0 表示无法识别。 */
  majorVersion: number
  /** 完整版本号文本。 */
  fullVersion: string
  /** CPU 架构。 */
  arch: RuntimeArch
  /** 位宽。 */
  bitness: RuntimeBitness
  /** 来源。 */
  source: RuntimeSource
  /** 是否与当前进程架构兼容。 */
  compatible: boolean
  /** 不兼容原因（兼容时为空）。 */
  compatibleReason: string
  /** 是否被选为当前运行时（P3-09 才会真正选择，本卡恒为 false）。 */
  selected: boolean
  /** 探测完成时间（毫秒时间戳）；0 表示未知。 */
  verifiedAtMs: number
}

/** 探测结果。 */
export interface RuntimeProbeResult {
  /** 可用运行时列表。 */
  items: RuntimeSummary[]
  /** 本次探测时间（毫秒时间戳）。 */
  probedAtMs: number
  /** 是否命中内存缓存。 */
  cacheHit: boolean
  /** 探测过程中的非致命告警。 */
  warnings: RuntimeProbeWarning[]
}

/** Java 版本要求（只读元数据；缺失时 major=0、source='unknown'）。 */
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
  source: RuntimeRequirementSource
  /** 需求推导过程中的提示（例如元数据未声明 Java 版本）。 */
  warnings: RuntimeProbeWarning[]
}

/** 需求查询请求。 */
export interface RuntimeRequirementRequest {
  /** 版本标识。 */
  versionId: string
}

/** 运行时列表视图。 */
export interface RuntimeList {
  /** 可用运行时列表。 */
  items: RuntimeSummary[]
  /** 当前选中的运行时标识（P3-09 之前为空）。 */
  selectedRuntimeId: string
  /** 可选：最近一次需求查询结果。 */
  requirement?: RuntimeRequirement
}

/** 安装官方运行时的请求（P3-09）。 */
export interface RuntimeInstallRequest {
  /**
   * 官方组件名，例如 `java-runtime-gamma`。
   *
   * 必须来自版本元数据 `javaVersion.component`；元数据没有声明时，
   * 界面应引导用户手动选择 Java，而不是让用户猜一个组件（D-39）。
   */
  component: string
  /** 期望平台；空字符串表示按当前进程平台选择。 */
  platform?: string
  /** 是否已阅读并接受许可提示；必须为 true 才会开始安装。 */
  acceptLicense: boolean
}

/**
 * 安装受理结果。
 *
 * `taskId` 是进度事件的关联键（`runtime-install-<runtimeId>`），
 * 不是下载队列中的任务 id：Runtime 安装不经过下载队列（D-48）。
 */
export interface RuntimeInstallAccepted {
  /** 进度事件关联键。 */
  taskId: string
  /** 父任务 id；Runtime 安装没有父任务，恒为空。 */
  parentTaskId?: string
  /** 初始状态；安装是同步完成的，因此总是 `completed`。 */
  state: string
  /** 为 true 表示已安装且校验通过，本次没有重新下载。 */
  duplicate: boolean
  /** 受理时间（毫秒时间戳）。 */
  createdAtMs: number
}

/** 选择运行时的请求（P3-09）。 */
export interface RuntimeSelectRequest {
  /** 要选中的运行时标识。 */
  runtimeId: string
  /** 为 true 时允许选中与当前系统架构不匹配的运行时。 */
  force?: boolean
}

/** 安装进度事件的状态取值（与后端 `download.task.changed.v1` 载荷约定一致）。 */
export const RUNTIME_INSTALL_STATE = {
  /** 下载中。 */
  downloading: 'downloading',
  /** 提交中（暂存目录整体改名）。 */
  committing: 'committing',
  /** 安装完成。 */
  completed: 'completed',
  /** 安装失败。 */
  failed: 'failed',
} as const

/** 安装进度事件的任务 id 前缀（与后端常量保持一致）。 */
export const RUNTIME_INSTALL_TASK_PREFIX = 'runtime-install-'

/**
 * 安装进度的阶段枚举（P3-12 视图层使用）。
 *
 * `idle` 表示本页尚未发起任何安装；其余四个取值与
 * `download.task.changed.v1` 中 Runtime 安装任务的 `state` 一一对应。
 */
export type RuntimeInstallPhase = 'idle' | 'downloading' | 'committing' | 'completed' | 'failed'

/**
 * 需求比对结果（D-49：比对逻辑在前端）。
 *
 * 后端 `SelectRuntimeRequest` 只有 `runtimeId` 与 `force`，没有 `versionId`，
 * 因此「该 Runtime 是否满足目标版本的 Java 需求」必须由前端先
 * `ResolveRuntimeRequirement(versionId)` 拿到 `major`，再与 `RuntimeSummary.majorVersion` 比对。
 */
export interface RuntimeRequirementMatch {
  /** 比对结论：`unknown` 表示目标版本元数据未声明 Java 需求。 */
  status: 'unknown' | 'met' | 'unmet'
  /** 目标版本要求的 Java 主版本；0 表示未知。 */
  requiredMajor: number
  /** 该 Runtime 的主版本；0 表示无法识别。 */
  runtimeMajor: number
}

/**
 * Runtime 安装进度的前端视图。
 *
 * 只保留事件真实携带的字段，不编造「已下载字节」与「下载速度」——
 * 后端事件载荷只有 `{taskId, state, progress, errorCode}`。
 */
export interface RuntimeInstallProgress {
  /** 进度事件关联键（`runtime-install-<runtimeId>`）。 */
  taskId: string
  /** 当前阶段。 */
  phase: RuntimeInstallPhase
  /** 0..1 的进度比例；取值超出范围时按边界裁剪。 */
  progress: number
  /** 失败时的稳定错误码；其余情况为空串。 */
  errorCode: string
}

// ===== 本卡（P3-16）追加：更新可用性与第三方来源信息 =====

/**
 * 已安装 Runtime 的更新可用性（P3-14 `RuntimeUpdateInfo` 的前端视图）。
 *
 * 只保留后端真实返回的字段：后端只对 `source=downloaded` 且来源为 adoptium
 * 的记录做检查，本机探测到的 Java 不会出现在这个列表里（那是本机自维护的安装）。
 */
export interface RuntimeUpdateInfo {
  /** 已安装 Runtime 的稳定标识。 */
  runtimeId: string
  /** 主版本号。 */
  majorVersion: number
  /** 本地已安装的完整版本串。 */
  installedFullVersion: string
  /** 远端最新完整版本串；检查失败时为空串。 */
  latestFullVersion: string
  /** 远端版本是否严格大于本地版本。 */
  updateAvailable: boolean
  /** 远端归档字节数；检查失败时为 0。 */
  latestSizeBytes: number
  /** 本次检查时间（毫秒时间戳）；0 表示未知。 */
  checkedAtMs: number
  /** 检查失败原因（后端中文文案）；空串表示检查成功。 */
  checkError: string
}

/**
 * 第三方来源上某个大版本的最新构建（P3-14 `GetRuntimeSourceInfo`）。
 *
 * 安全约束：`downloadHosts` 只有 host，没有完整下载地址，前端不得自行拼接 URL。
 */
export interface RuntimeSourceInfo {
  /** 来源标识，后端当前固定为 `adoptium`。 */
  source: string
  /** 主版本号。 */
  majorVersion: number
  /** 语义化版本（展示用），例如 `21.0.13+9.0.LTS`。 */
  semver: string
  /** OpenJDK 版本串（落库与比较用）。 */
  fullVersion: string
  /** 镜像路径里的系统段。 */
  os: string
  /** 架构段。 */
  arch: string
  /** 归档字节数；0 表示后端未提供。 */
  sizeBytes: number
  /** 归档的 SHA-256（小写十六进制）；空串表示未提供。 */
  sha256: string
  /** 候选来源 host 列表（不含协议与路径）。 */
  downloadHosts: string[]
}

/**
 * 「目标版本 Java 需求」与「已安装运行时」的绑定结论。
 *
 * - `unknown`：目标版本元数据没声明 Java 需求，无法判断；
 * - `bound`：已找到同大版本且已选中；
 * - `available`：已找到同大版本但未选中；
 * - `missing`：没有任何同大版本的运行时。
 */
export type RuntimeBindingStatus = 'unknown' | 'bound' | 'available' | 'missing'

/** 绑定结论的完整视图（含命中的运行时，便于界面展示与选用）。 */
export interface RuntimeBindingView {
  /** 结论。 */
  status: RuntimeBindingStatus
  /** 目标版本要求的 Java 主版本；0 表示未知。 */
  requiredMajor: number
  /** 命中的运行时（仅 `bound` / `available` 时非 null）。 */
  runtime: RuntimeSummary | null
}

// ===== 本卡（P3-16）追加：第三方来源安装请求 =====

/**
 * 从第三方来源安装 Runtime 的请求（对应 `RuntimeService.InstallRuntimeSource`）。
 *
 * 只按大版本安装：来源侧取该大版本的最新构建，安装成功后由后端落库并切换绑定。
 * `acceptLicense` 必须来自用户的勾选动作，界面不得硬编码为 true 绕过许可确认。
 */
export interface RuntimeSourceInstallRequest {
  /** 要安装的 Java 主版本（必须 > 0；元数据未声明时不得猜，D-39）。 */
  majorVersion: number
  /** 用户是否已勾选并接受许可提示。 */
  acceptLicense: boolean
}

