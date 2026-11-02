/**
 * Java 校验相关的前端类型（P2-01）。
 *
 * 与 Go 侧 `internal/launch/dto.go` 逐字段一致；字段名使用 lowerCamelCase，
 * 与 Wails 绑定生成物的 JSON 标签一致。
 *
 * 重要约束（决策记录 D-8 / 第六节）：
 * - **展示字段**一律使用 `javaDisplay` 这类脱敏形态，不得渲染完整绝对路径；
 * - `javaPath` 只允许在「对话框返回 → 回传校验」之间经过一次，
 *   禁止渲染到界面、禁止写 localStorage、禁止写进日志、禁止放进 `AppError.details`。
 */

/** 三个稳定错误码，与 Go 侧 `internal/launch/errors.go` 逐字一致。 */
export const JAVA_ERROR_CODE = {
  /** 路径非法或越界（空值、相对路径、指向目录）。 */
  pathInvalid: 'PATH_INVALID_001',
  /** 文件不存在、不是普通文件或不可执行。 */
  notFound: 'JAVA_NOT_FOUND_001',
  /** 可执行但版本输出无法解析。 */
  version: 'JAVA_VERSION_001',
} as const

/** Java 校验结果（对应 Go 侧 `JavaValidationResult`）。 */
export interface JavaValidationResult {
  /** 三层校验是否全部通过。 */
  valid: boolean

  /** Java 可执行文件的脱敏展示形态，例如 `Java 安装目录\…\jdk-21\bin\java.exe`。 */
  javaDisplay: string

  /** 可执行文件名，例如 `java.exe`；非敏感，可安全展示与记录。 */
  executableName: string

  /** 解析出的 Java 主版本号；未解析出时为 0。 */
  majorVersion: number

  /** `java -version` 输出的已脱敏首行。 */
  versionOutputSummary: string

  /** 失败时的稳定错误码；成功时为空字符串。 */
  errorCode: string

  /** 失败时的中文用户文案；成功时为空字符串。 */
  errorMessage: string

  /** 不影响通过结论的提示列表（例如「所选文件没有扩展名」）。 */
  warnings: string[]
}

/** Java 选择对话框结果（对应 Go 侧 `PickJavaResult`）。 */
export interface PickJavaResult {
  /** 用户是否取消或关闭了对话框。 */
  canceled: boolean

  /**
   * 用户选中的完整路径。
   *
   * **仅供立即回传给 `validateJavaPath`**：不得渲染、不得持久化、不得记日志。
   * 界面展示必须使用 `javaDisplay`。
   */
  javaPath: string

  /** 同一路径的脱敏展示形态，供界面直接展示。 */
  javaDisplay: string

  /** 对话框不可用时的稳定错误码；正常取消时为空字符串。 */
  errorCode: string

  /** 对话框不可用时的中文说明；正常取消时为空字符串。 */
  errorMessage: string
}

/** `validateJavaPath` 的入参（对应 Go 侧 `ValidateJavaRequest`）。 */
export interface ValidateJavaRequest {
  /**
   * 用户提供的 Java 可执行文件路径。
   *
   * 该字段只用于后端校验；来源可以是手动输入的文本框，也可以是
   * `pickJavaExecutable` 返回的 `javaPath`（一次传递）。
   */
  javaPath: string
}

// ===== P2-02 游戏目录检查（本卡追加，不修改上方 P2-01 类型）=====

/** `GAME_DIR_001`：目录存在但结构缺失。与 Java 相关错误码语义不同，不得混用。 */
export const GAME_DIR_ERROR_CODE = 'GAME_DIR_001'

/**
 * 结构检查项的三态。
 *
 * 三态而不是布尔值：必须能区分「不存在」与「存在但不是目录」——
 * 后者说明路径被同名文件占住了，用户需要删文件而不是重新安装。
 */
export type GameDirCheckStatus = 'present' | 'missing' | 'not_a_directory'

/** 结构检查项名称（稳定标识，前端应按此映射文案，不依赖 detail 措辞）。 */
export const GAME_DIR_CHECK_NAME = {
  /** `versions/` 目录。 */
  versions: 'versions',
  /** `libraries/` 目录。 */
  libraries: 'libraries',
  /** `assets/` 目录。 */
  assets: 'assets',
  /** `assets/indexes/` 目录。 */
  assetsIndexes: 'assets/indexes',
  /** `assets/objects/` 目录。 */
  assetsObjects: 'assets/objects',
  /** 根目录下的 `launcher_profiles.json`。 */
  launcherProfiles: 'launcher_profiles.json',
} as const

/** 单个结构检查项（对应 Go 侧 `GameDirectoryCheckItem`）。 */
export interface GameDirCheckItem {
  /** 检查项稳定标识，取值见 `GAME_DIR_CHECK_NAME`。 */
  name: string

  /** 三态结果。 */
  status: GameDirCheckStatus

  /** 该项的中文说明，面向用户展示。 */
  detail: string
}

/** 游戏目录结构检查结果（对应 Go 侧 `GameDirectoryCheckResult`）。 */
export interface GameDirectoryCheckResult {
  /** 目录是否足以支撑后续版本识别与启动。 */
  valid: boolean

  /** 游戏目录的脱敏展示形态，例如 `游戏目录\…\Roaming\.minecraft`。 */
  gameRootDisplay: string

  /** 六项结构检查结果，顺序固定。 */
  checks: GameDirCheckItem[]

  /** `versions/` 的直接子目录数量（不递归、不读 JSON）。 */
  versionsCount: number

  /** 「为什么不能启动」的中文提示；结构可用时为空字符串。 */
  launchableHint: string

  /** 归一化或访问失败时的稳定错误码；**结构缺失不会**设置该字段。 */
  errorCode: string

  /** 失败时的中文文案；成功时为空字符串。 */
  errorMessage: string

  /** 不影响结论的提示（例如符号链接解析失败）。 */
  warnings: string[]
}

/** 游戏目录选择对话框结果（对应 Go 侧 `PickGameDirectoryResult`）。 */
export interface PickGameDirectoryResult {
  /** 用户是否取消或关闭了对话框。 */
  canceled: boolean

  /**
   * 用户选中的完整路径。
   *
   * **仅供立即回传给 `checkGameDirectory`**：不得渲染、不得持久化、不得记日志。
   * 界面展示必须使用 `gameRootDisplay`。
   */
  gameDirectory: string

  /** 同一路径的脱敏展示形态，供界面直接展示。 */
  gameRootDisplay: string

  /** 对话框不可用时的稳定错误码；正常取消时为空字符串。 */
  errorCode: string

  /** 对话框不可用时的中文说明；正常取消时为空字符串。 */
  errorMessage: string
}

/** `checkGameDirectory` 的入参（对应 Go 侧 `GameDirectoryRequest`）。 */
export interface GameDirectoryRequest {
  /** 游戏根目录路径（手动输入或对话框返回值）。仅用于后端检查。 */
  gameDirectory: string
}

// ===== P2-03 版本识别（本卡追加，不修改上方 P2-01 / P2-02 类型）=====

/** `VERSION_METADATA_001`：版本 JSON 无法解析、必需字段缺失或继承环。 */
export const VERSION_METADATA_ERROR_CODE = 'VERSION_METADATA_001'

/**
 * 版本可启动性四态，与 Go 侧常量及接口冻结清单逐字一致。
 *
 * - `launch_supported`：结构完整，可尝试启动；
 * - `metadata_only`：能识别但缺 jar / 库 / natives，不可启动；
 * - `unsupported`：格式不受支持（缺 mainClass、继承过深等）；
 * - `broken`：JSON 无法解析或继承环。
 */
export type VersionStatus = 'launch_supported' | 'metadata_only' | 'unsupported' | 'broken'

/** 单个版本摘要（列表项，对应 Go 侧 `VersionSummary`）。 */
export interface VersionSummary {
  /** 版本标识（目录名）。 */
  id: string

  /** 版本类型（release / snapshot / old_beta 等）；无法识别时为空串。 */
  type: string

  /** 四态判定。 */
  status: VersionStatus

  /** 不能启动或无法识别的原因；可启动时为空串。 */
  reason: string
}

/** 列出版本的返回（对应 Go 侧 `VersionListResult`）。 */
export interface VersionListResult {
  /** 识别到的版本列表（按 id 升序）。 */
  versions: VersionSummary[]

  /** 游戏目录的脱敏展示形态（不含完整路径）。 */
  gameRootDisplay: string

  /** 被跳过的非法版本目录名（只含目录名，不含路径）。 */
  skippedIds: string[]

  /** 目录级失败时的稳定错误码；成功时为空串。单个版本损坏不设置该字段。 */
  errorCode: string

  /** 目录级失败时的中文文案；成功时为空串。 */
  errorMessage: string

  /** 不影响结论的提示。 */
  warnings: string[]
}

/** 单个版本详细检查结果（对应 Go 侧 `VersionCheckResult`）。 */
export interface VersionCheckResult {
  /** 版本标识（目录名）。 */
  id: string

  /** 版本类型；无法识别时为空串。 */
  type: string

  /** 四态判定。 */
  status: VersionStatus

  /** 不能启动的原因；可启动时为空串。 */
  reason: string

  /** 游戏主类；未解析出时为空串。 */
  mainClass: string

  /** 客户端 JAR 是否存在且非空。 */
  clientJarPresent: boolean

  /** 需要检查的依赖库数量。 */
  librariesRequired: number

  /** 已存在的依赖库数量。 */
  librariesPresent: number

  /**
   * 缺失的依赖库清单。
   *
   * **只含相对路径**（例如 `libraries/net/minecraft/.../x.jar`），
   * 绝不包含绝对路径。
   */
  librariesMissing: string[]

  /**
   * natives 目录是否存在且非空。
   *
   * `null` 表示「该版本没有任何 natives 声明，未做检查」；
   * `false` 表示「声明了 natives 但目录缺失」。
   */
  nativesPresent: boolean | null

  /** 版本 JSON 声明的 Java 主版本；未声明时为 0。 */
  javaMajorVersionRequired: number

  /** 不影响结论的提示。 */
  warnings: string[]

  /** 本次检查耗时是否超过阈值（3 秒）；仅提示，不中断。 */
  slow: boolean

  /** 版本级失败时的稳定错误码；结构缺失（缺 jar / 库 / natives）不设置。 */
  errorCode: string

  /** 版本级失败时的中文文案；正常时为空串。 */
  errorMessage: string
}

/** 列出版本的入参（对应 Go 侧 `VersionListRequest`）。 */
export interface VersionListRequest {
  /** 游戏根目录路径。仅用于后端检查。 */
  gameDirectory: string
}

/** 检查单个版本的入参（对应 Go 侧 `VersionCheckRequest`）。 */
export interface VersionCheckRequest {
  /** 游戏根目录路径。 */
  gameDirectory: string

  /** 要检查的版本 id（`versions/` 下的目录名）。 */
  versionId: string
}

// ===== P2-04 启动计划预览（本卡追加，不修改上方既有类型）=====

/** `LAUNCH_ARGS_001`：启动参数构建失败（占位符无法解析、classpath 为空等）。 */
export const LAUNCH_ARGS_ERROR_CODE = 'LAUNCH_ARGS_001'

/** `LAUNCH_ARGS_002`：参数总长度超限。 */
export const LAUNCH_ARGS_TOO_LONG_ERROR_CODE = 'LAUNCH_ARGS_002'

/** 警告严重级别：`blocking` 阻止启动 / `warn` 提示风险 / `info` 说明。 */
export type ArgumentWarningSeverity = 'blocking' | 'warn' | 'info'

/** 一条参数构建警告（对应 Go 侧 `ArgumentWarning`）。 */
export interface ArgumentWarning {
  /** 稳定错误码。 */
  code: string

  /** 面向用户的中文说明（已脱敏，不含绝对路径）。 */
  message: string

  /** 严重级别。 */
  severity: ArgumentWarningSeverity
}

/** 预览用的参数元素（对应 Go 侧 `PreviewArgumentPart`）。 */
export interface PreviewArgumentPart {
  /** **脱敏后**的参数文本。 */
  value: string

  /** 来源标注：`arguments.jvm` / `arguments.game` / `minecraftArguments` / `launcher`。 */
  source: string

  /** 该参数原本含绝对路径、已被脱敏时为 true。 */
  redacted: boolean

  /** 补充标注（P2-09 追加）：目前用于「离线占位身份」，可直接展示。 */
  note: string
}

/**
 * 预览警告码（P2-09 / D-18）。
 *
 * 注意：这些是 `warnings[].code` 的取值，**不是**错误码，
 * 不要与 `LAUNCH_ARGS_ERROR_CODE` 之类的 `Error.code` 混用。
 */
export const PREVIEW_WARNING_CODES = {
  /** 版本 JSON 中存在无法解析的占位符（blocking）。 */
  placeholderUnresolved: 'PLACEHOLDER_UNRESOLVED',
  /** 依赖库缺失（blocking）。 */
  librariesMissing: 'LIBRARIES_MISSING',
  /** 版本声明了 natives 但目录缺失（blocking）。 */
  nativesMissing: 'NATIVES_MISSING',
  /** classpath 为空（blocking）。 */
  classPathEmpty: 'CLASSPATH_EMPTY',
  /** 当前使用离线占位身份（info）。 */
  offlineIdentity: 'OFFLINE_IDENTITY',
  /** 没有可复用的 Java 版本校验结果（info）。 */
  javaVersionUnknown: 'JAVA_VERSION_UNKNOWN',
} as const

/**
 * 启动计划预览（对应 Go 侧 `LaunchPlanPreview`）。
 *
 * 安全约定（D-8 + 决策记录第六节）：本结构**不含任何未脱敏绝对路径**。
 * 路径类字段只以「类别 + … + 末段」的脱敏形态出现。
 *
 * 结构约定（P2-04 卡面第 1 条）：classpath / mainClass / JVM 参数 / 游戏参数
 * **四路分离**，不存在「一整行命令」的字符串字段。
 */
export interface LaunchPlanPreview {
  /** 预览的版本 id。 */
  versionId: string

  /** 游戏主类（非敏感）。 */
  mainClass: string

  /** classpath 条目数量。 */
  classPathEntryCount: number

  /** classpath 条目的脱敏展示形态列表。 */
  classPathEntries: string[]

  /** JVM 参数（脱敏后，每项独立）。 */
  jvmArgs: PreviewArgumentPart[]

  /** 游戏参数（脱敏后，每项独立）。 */
  gameArgs: PreviewArgumentPart[]

  /** natives 目录的脱敏展示形态；不存在时为空串。 */
  nativesDirectoryDisplay: string

  /** 资源目录的脱敏展示形态。 */
  assetsDirectoryDisplay: string

  /** 工作目录的脱敏展示形态。 */
  workingDirectoryDisplay: string

  /** 资源索引 id（非敏感）。 */
  assetIndexID: string

  /** 警告列表（message 已脱敏）。 */
  warnings: ArgumentWarning[]

  /** 未解析占位符清单（集中列出，例如 `${resolution_width}`）。 */
  unresolvedPlaceholders: string[]

  /** 是否可安全启动。false 时不得交给进程层执行。 */
  safeToLaunch: boolean

  /** 离线身份提示（D-5 要求 UI 必须明示「非真实登录」）。 */
  offlineIdentityNotice: string

  // ===== 以下字段由 P2-09 追加（D-14~D-18）=====

  /** 版本类型（`release` / `snapshot` 等，取自版本 JSON）。 */
  versionType: string

  /** Java 的脱敏展示名（仅「目录类别 + 末两段」，不含盘符与用户名段）。 */
  javaDisplay: string

  /** Java 大版本号；`0` 表示未知（预览不执行版本探测，D-16）。 */
  javaMajorVersion: number

  /** Java 版本信息是否来自历史校验缓存（true 时界面提示「以最近一次校验为准」）。 */
  javaCheckStale: boolean

  /** classpath 缺失的依赖库数量。 */
  classPathMissingCount: number

  /** blocking 级警告子集，供界面直接渲染阻断原因（D-18）。 */
  blockingWarnings: ArgumentWarning[]

  /** 启动计划构建是否失败（失败原因见 `buildErrorCode`）。 */
  buildFailed: boolean

  /** 构建失败时的稳定错误码（P2-04 既有码）。 */
  buildErrorCode: string

  /** 构建失败的脱敏后原因说明。 */
  buildErrorMessage: string

  /** 可复制的诊断信息文本（已二次脱敏，D-17）；前端只复制该字段，不自行拼装。 */
  diagnosticsText: string
}

/** 预览启动计划的入参（对应 Go 侧 `LaunchPlanPreviewRequest`）。 */
export interface LaunchPlanPreviewRequest {
  /** 游戏根目录路径。 */
  gameDirectory: string

  /** 要预览的版本 id。 */
  versionId: string

  /** 附加 JVM 参数（逐项校验后追加）；`null` 表示不提供。 */
  additionalJvmArgs: string[] | null

  /** 附加游戏参数（逐项校验，且不得覆盖受保护参数）；`null` 表示不提供。 */
  additionalGameArgs: string[] | null
}

// ===== P2-07 游戏日志通道（本卡追加，不修改上方既有类型）=====

/** `LAUNCH_LOG_001`：游戏输出通道（内存缓冲 / game-*.log）读写失败。 */
export const LAUNCH_LOG_ERROR_CODE = 'LAUNCH_LOG_001'

/** 事件名：游戏日志增量追加（与 Go 侧 `events.EventLaunchLogAppended` 一致）。 */
export const EVENT_LAUNCH_LOG_APPENDED = 'launch.log.appended.v1'

/** 输出来源。 */
export type GameLogStream = 'stdout' | 'stderr'

/** 一行游戏日志（对应 Go 侧 `GameLogLineResult`）。 */
export interface GameLogLineResult {
  /** 该行被采集到的时间，UTC Unix 毫秒。 */
  timestampMs: number

  /** 输出来源。 */
  stream: GameLogStream

  /** 该行**已脱敏**的文本。 */
  text: string
}

/** 快照读取游戏日志的入参（对应 Go 侧 `GameLogQueryRequest`）。 */
export interface GameLogQueryRequest {
  /** 要查询的启动标识；空字符串表示当前活动启动。 */
  launchId: string

  /** 时间游标（UTC Unix 毫秒），只返回早于该时间的行；0 表示从最新开始。 */
  beforeMs: number

  /** 最大行数；<=0 用默认 200，超过 1000 被裁剪。 */
  limit: number
}

/** 快照读取结果（对应 Go 侧 `GameLogQueryResult`）。 */
export interface GameLogQueryResult {
  /** 本页行，按时间**倒序**（最新在前）。 */
  lines: GameLogLineResult[]

  /** 继续往更早翻的游标；0 表示没有更早的行。 */
  nextBeforeMs: number

  /** **累计**被丢弃的行数（内存缓冲溢出 + 落盘队列溢出）。 */
  droppedLines: number

  /** 缓冲中是否还有更早的行。 */
  hasMore: boolean

  /** 读取失败时的稳定错误码；成功时为空字符串。 */
  errorCode: string

  /** 失败时的中文文案；成功时为空字符串。 */
  errorMessage: string
}

/**
 * 增量事件载荷（对应 Go 侧 `events.LaunchLogAppendedPayload`）。
 *
 * 刻意**不含序号**：用 `droppedLines` 累计值让前端感知丢失，
 * 避免「进程重启后序号归零」造成的歧义。
 */
export interface LaunchLogAppendedPayload {
  /** 本次启动标识。 */
  launchId: string

  /** 本批次的新增行（已脱敏）。 */
  lines: GameLogLineResult[]

  /** 累计被丢弃的行数。 */
  droppedLines: number
}

// ===== P2-06 启动状态机与 P2-08 视图类型（本卡追加，不修改上方既有类型）=====

/** `LAUNCH_STATE_001`：单活动进程限制、状态查询或状态迁移失败。 */
export const LAUNCH_STATE_ERROR_CODE = 'LAUNCH_STATE_001'

/** 事件名：启动状态迁移（与 Go 侧 `launch.EventLaunchStateChanged` 一致）。 */
export const EVENT_LAUNCH_STATE_CHANGED = 'launch.state.changed.v1'

/**
 * 启动状态取值。
 *
 * 与 Go 侧 `internal/launch/supervisor.go` 的 `State*` 常量逐字一致；
 * 末尾的空字符串表示「尚无任何启动记录」，仅用于前端判空，不会由后端返回。
 */
export type LaunchState =
  | 'preparing'
  | 'resolving_runtime'
  | 'building_arguments'
  | 'starting_process'
  | 'running'
  | 'stopping'
  | 'exited'
  | 'failed'
  | 'cancelled'
  | ''

/** 停止原因；非停止路径为空字符串。 */
export type LaunchStopReason = 'user_requested' | 'app_exiting' | 'startup_failed' | ''

/** 停止结果。 */
export type LaunchStopResult = 'graceful' | 'killed' | 'timeout' | ''

/** 退出结局。 */
export type LaunchOutcome = 'exited_normally' | 'stopped_by_user' | 'crashed' | ''

/**
 * 启动状态快照。
 *
 * **本结构是界面的唯一真相源**：事件可能丢失或乱序，界面一律以本结构为准。
 * 字段与 Go 侧 `LaunchStatusResult` 一致，不含任何绝对路径。
 */
export interface LaunchStatus {
  /** 本次启动标识；无启动记录时为空字符串。 */
  launchId: string

  /** 当前状态。 */
  state: LaunchState

  /** 本次启动的版本 id。 */
  versionId: string

  /** 进程创建成功时间；尚未创建时为 0。 */
  startedAtMs: number

  /** 进程退出时间；尚未退出时为 0。 */
  endedAtMs: number

  /** 进程退出码；尚无时为 0。 */
  exitCode: number

  /** 区分「退出码 0」与「尚无退出码」。 */
  hasExitCode: boolean

  /** 停止原因。 */
  stopReason: LaunchStopReason

  /** 停止结果。 */
  stopResult: LaunchStopResult

  /** 退出结局。 */
  outcome: LaunchOutcome

  /** 最近一次状态迁移时间，UTC Unix 毫秒。 */
  stateChangedAtMs: number

  /** 查询失败时的稳定错误码；成功时为空字符串。 */
  errorCode: string

  /** 失败时的中文文案；成功时为空字符串。 */
  errorMessage: string
}

/** 状态查询入参。 */
export interface LaunchStatusRequest {
  /** 要查询的启动标识；空字符串表示当前活动启动（或最近的终态记录）。 */
  launchId: string
}

/** 启动请求：只接受结构化参数，不接受任意命令行。 */
export interface StartLaunchRequest {
  /** 已校验的 Java 可执行文件完整路径；只在内存中存在，不渲染、不持久化。 */
  javaPath: string

  /** 游戏根目录路径。 */
  gameDirectory: string

  /** 要启动的版本 id。 */
  versionId: string

  /** 附加 JVM 参数；`null` 表示不提供。 */
  additionalJvmArgs: string[] | null

  /** 附加游戏参数；`null` 表示不提供。 */
  additionalGameArgs: string[] | null

  /**
   * 所选账号的本地 id；缺省（不传）表示沿用离线占位身份（兼容旧调用）。
   *
   * 后端会用该 id 解析账号档案与凭证，并把真实用户名/UUID/token/userType
   * 写进启动参数；凭证不会经过前端。
   */
  accountId?: string
}

/** 启动受理结果。 */
export interface LaunchAcceptedResult {
  /** 本次启动标识（UUID v4），只在内存中存在。 */
  launchId: string

  /** 受理时间（状态首次迁移时间），UTC Unix 毫秒。 */
  acceptedAtMs: number

  /** 受理后的当前状态。 */
  state: LaunchState
}

/** 停止请求。 */
export interface StopLaunchRequest {
  /** 要停止的启动标识；空字符串表示当前活动启动。 */
  launchId: string

  /** 是否直接强制终止；`true` 前必须经过二次确认（D-3 风险提示）。 */
  force: boolean
}

/** `launch.state.changed.v1` 的载荷。 */
export interface LaunchStateChangedPayload {
  /** 本次启动标识。 */
  launchId: string

  /** 迁移后的当前状态。 */
  state: LaunchState

  /** 停止原因；非停止路径为空字符串。 */
  stopReason?: LaunchStopReason

  /** 进程退出码；尚未退出或不可用时为 0。 */
  exitCode?: number

  /** 区分「退出码 0」与「尚无退出码」。 */
  hasExitCode: boolean

  /** 状态迁移时间，UTC Unix 毫秒。 */
  occurredAtMs: number
}

/**
 * 界面阶段：把后端 9 个状态归并为 5 个用户可理解的阶段。
 *
 * 归并规则见 `features/launch/useLaunchSession.ts` 的 `toLaunchPhase`：
 * 准备中的四个中间状态合并为 `preparing`；`failed` 与 `cancelled` 并入 `exited`，
 * 由「结局」字段区分具体含义，避免导航层出现过多等价状态。
 */
export type LaunchPhase = 'idle' | 'preparing' | 'running' | 'stopping' | 'exited'
// ===== P2-13 启动配置持久化（本卡追加，不修改上方既有类型）=====

/** 启动配置读写失败的错误码（P2-13 新增冻结码）。 */
export const LAUNCH_PROFILE_ERROR_CODE = 'LAUNCH_PROFILE_001'

/**
 * 启动配置的对外摘要。
 *
 * **不含任何绝对路径**：`javaDisplay` / `gameRootDisplay` 是后端按
 * 「目录类别 + 末两段（Java 为末三段）」脱敏后的展示串，
 * 因此前端只能显示、不能把它当成可用路径传给后端。
 */
export interface LaunchProfileSummary {
  /** 配置唯一标识。 */
  id: string

  /** 配置显示名称。 */
  name: string

  /** Java 可执行文件的脱敏展示串（不含盘符与用户名段）。 */
  javaDisplay: string

  /** 游戏目录的脱敏展示串（不含盘符与用户名段）。 */
  gameRootDisplay: string

  /** 保存的版本 id。 */
  versionId: string

  /** 最近一次用于启动的时间，UTC Unix 毫秒。 */
  lastUsedAtMs: number
}

/**
 * 保存启动配置的请求。
 *
 * `javaPath` 与 `gameDirectory` 是绝对路径，只上行不入回包：
 * 后端同样不会把它们写进日志或事件载荷。
 */
export interface SaveLaunchProfileRequest {
  /** Java 可执行文件绝对路径。 */
  javaPath: string

  /** 游戏根目录绝对路径。 */
  gameDirectory: string

  /** 版本 id。 */
  versionId: string

  /** 配置名称；空字符串时由后端按版本 id 生成。 */
  name: string
}