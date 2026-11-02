import { LaunchService } from '../../bindings/example.com/ninevoxel/launcher/internal/launch'
import type {
  JavaValidationResult as BoundJavaValidationResult,
  PickJavaResult as BoundPickJavaResult,
  GameDirectoryCheckResult as BoundGameDirectoryCheckResult,
  PickGameDirectoryResult as BoundPickGameDirectoryResult,
  VersionListResult as BoundVersionListResult,
  VersionCheckResult as BoundVersionCheckResult,
  LaunchPlanPreview as BoundLaunchPlanPreview,
  PreviewArgumentPart as BoundPreviewArgumentPart,
  // 本卡（P2-08）追加：启动状态、日志快照与两类事件载荷的绑定类型。
  LaunchStatusResult as BoundLaunchStatusResult,
  GameLogQueryResult as BoundGameLogQueryResult,
  GameLogLineResult as BoundGameLogLineResult,
  LaunchStateChangedPayload as BoundLaunchStateChangedPayload,
  // 本卡（P2-13）追加：启动配置摘要与保存请求的绑定类型。
  LaunchProfileSummary as BoundLaunchProfileSummary,
  SaveLaunchProfileRequest as BoundSaveLaunchProfileRequest,
} from '../../bindings/example.com/ninevoxel/launcher/internal/launch'
// 日志增量事件载荷由 events 绑定模块导出，不在 launch 模块内。
import type { LaunchLogAppendedPayload as BoundLaunchLogAppendedPayload } from '../../bindings/example.com/ninevoxel/launcher/internal/events'
import { toAppError } from './errors'
import type {
  JavaValidationResult,
  PickJavaResult,
  ValidateJavaRequest,
  GameDirectoryCheckResult,
  PickGameDirectoryResult,
  GameDirectoryRequest,
  GameDirCheckItem,
  GameDirCheckStatus,
  VersionListResult,
  VersionCheckResult,
  VersionListRequest,
  VersionCheckRequest,
  VersionSummary,
  VersionStatus,
  LaunchPlanPreview,
  LaunchPlanPreviewRequest,
  PreviewArgumentPart,
  ArgumentWarningSeverity,
} from '../types/launch'
import { JAVA_ERROR_CODE, VERSION_METADATA_ERROR_CODE } from '../types/launch'
// 本卡（P2-08）追加：启动状态与游戏日志的封装需要事件订阅能力与对应前端类型。
import { Events } from '@wailsio/runtime'
import type { EventEnvelopeBody } from '../types/events'
import type { Unsubscribe } from './events'
import { EVENT_LAUNCH_LOG_APPENDED, EVENT_LAUNCH_STATE_CHANGED } from '../types/launch'
import type {
  GameLogLineResult,
  GameLogQueryRequest,
  GameLogQueryResult,
  GameLogStream,
  LaunchAcceptedResult,
  LaunchLogAppendedPayload,
  LaunchOutcome,
  LaunchState,
  LaunchStateChangedPayload,
  LaunchStatus,
  LaunchStopReason,
  LaunchStopResult,
  StartLaunchRequest,
  StopLaunchRequest,
  // 本卡（P2-13）追加：启动配置的前端类型。
  LaunchProfileSummary,
  SaveLaunchProfileRequest,
} from '../types/launch'

/**
 * Java 校验服务的绑定封装层（P2-01）。
 *
 * 前端只有本文件可以 import 生成目录中的 LaunchService；
 * 页面与组件必须通过这里暴露的语义化函数访问后端（AGENTS.md 8.2）。
 *
 * 安全约束（决策记录第六节）：`javaPath` 允许在本文件内传递一次
 * （对话框返回 → 回传校验），但**不得渲染**、不得写入 `localStorage`、
 * 不得写进日志、不得放进 `AppError.details`。界面展示一律用 `javaDisplay`。
 */

/** 对话框不可用时的兜底错误码，与 Go 侧保持一致。 */
const DIALOG_UNAVAILABLE_CODE = JAVA_ERROR_CODE.pathInvalid

/**
 * 把绑定生成物的校验结果归一化为前端类型。
 *
 * 归一化只做类型收敛，不改语义：`warnings` 为 `null` 时收敛为空数组，
 * 让界面无需处理 null；其余字段原样保留。
 *
 * @param raw 绑定返回的原始结果
 * @returns 归一化后的校验结果
 */
function normalizeValidationResult(raw: BoundJavaValidationResult): JavaValidationResult {
  return {
    valid: raw.valid === true,
    javaDisplay: typeof raw.javaDisplay === 'string' ? raw.javaDisplay : '',
    executableName: typeof raw.executableName === 'string' ? raw.executableName : '',
    majorVersion: typeof raw.majorVersion === 'number' ? raw.majorVersion : 0,
    versionOutputSummary: typeof raw.versionOutputSummary === 'string' ? raw.versionOutputSummary : '',
    errorCode: typeof raw.errorCode === 'string' ? raw.errorCode : '',
    errorMessage: typeof raw.errorMessage === 'string' ? raw.errorMessage : '',
    // Go 侧切片可能为 null（生成物类型为 `string[] | null`），统一收敛为空数组。
    warnings: Array.isArray(raw.warnings) ? raw.warnings.filter((item): item is string => typeof item === 'string') : [],
  }
}

/**
 * 把绑定生成物的选择结果归一化为前端类型。
 *
 * @param raw 绑定返回的原始结果
 * @returns 归一化后的选择结果
 */
function normalizePickResult(raw: BoundPickJavaResult): PickJavaResult {
  return {
    canceled: raw.canceled === true,
    javaPath: typeof raw.javaPath === 'string' ? raw.javaPath : '',
    javaDisplay: typeof raw.javaDisplay === 'string' ? raw.javaDisplay : '',
    errorCode: typeof raw.errorCode === 'string' ? raw.errorCode : '',
    errorMessage: typeof raw.errorMessage === 'string' ? raw.errorMessage : '',
  }
}

/**
 * 调用后端校验一个 Java 可执行文件路径。
 *
 * 三层校验（存在性 → 可执行性 → `java -version`）全部在 Go 侧完成；
 * 前端不做任何本地判定，避免出现「界面说通过、后端说失败」的分歧。
 *
 * 注意：本函数**不抛错**校验失败——校验失败是业务结果（`valid=false` +
 * `errorCode`），不是调用异常。只有 Binding 本身失败（服务不可用）才会抛出。
 *
 * @param javaPath 用户提供的 Java 路径（手动输入或对话框返回）
 * @returns 归一化后的校验结果
 * @throws AppError 服务不可用或 Binding 调用失败时的归一化错误
 */
export async function validateJavaPath(javaPath: string): Promise<JavaValidationResult> {
  const request: ValidateJavaRequest = { javaPath }

  try {
    return normalizeValidationResult(await LaunchService.ValidateJavaPath(request))
  } catch (error) {
    throw toAppError(error)
  }
}

/**
 * 弹出系统原生文件对话框，让用户选择 Java 可执行文件。
 *
 * 返回语义（决策记录第四节硬性要求）：
 * - 用户选中：`canceled=false`，`javaPath` 供立即回传 `validateJavaPath`，
 *   `javaDisplay` 供界面展示；
 * - **用户取消**：`canceled=true`，调用方应**静默保持原值**，不得弹错误提示；
 * - 对话框不可用：`errorCode` 非空，调用方应提示「请手动填写路径」并保留文本框。
 *
 * @returns 归一化后的选择结果
 * @throws AppError Binding 调用失败时的归一化错误
 */
export async function pickJavaExecutable(): Promise<PickJavaResult> {
  try {
    return normalizePickResult(await LaunchService.PickJavaExecutable())
  } catch (error) {
    throw toAppError(error)
  }
}

/**
 * 判断校验结果是否携带稳定错误码。
 *
 * @param result 校验结果
 * @returns 失败且带错误码时返回 true
 */
export function hasJavaError(result: JavaValidationResult): boolean {
  return !result.valid && result.errorCode !== ''
}

/**
 * 判断错误码是否属于「用户可自行修正」的输入类错误。
 *
 * 路径类错误（`PATH_INVALID_001` / `JAVA_NOT_FOUND_001`）通常改一下路径就好，
 * 界面应就地提示；`JAVA_VERSION_001` 则是选到了非 Java 程序，需要用户换一个文件。
 *
 * @param code 稳定错误码
 * @returns 属于输入类错误时返回 true
 */
export function isJavaInputError(code: string): boolean {
  return code === JAVA_ERROR_CODE.pathInvalid || code === JAVA_ERROR_CODE.notFound
}

/**
 * 对话框不可用的判定。
 *
 * 供界面决定「是否提示用户改用手动输入」：`errorCode` 非空即表示
 * 对话框没能正常返回路径（既不是选中也不是取消）。
 *
 * @param result 选择结果
 * @returns 对话框不可用时返回 true
 */
export function isDialogUnavailable(result: PickJavaResult): boolean {
  return !result.canceled && result.errorCode === DIALOG_UNAVAILABLE_CODE
}

// ===== P2-02 游戏目录检查（本卡追加，不修改上方 P2-01 封装）=====

/**
 * 调用后端以只读方式检查游戏目录结构。
 *
 * 语义（P2-02 卡面第 6 条）：
 * - 目录不存在 / 不是目录 / 路径非法 → `valid=false` 且 `errorCode` 非空；
 * - **目录存在但结构缺失** → `valid=false` 但 `errorCode` 为空，
 *   由 `launchableHint` 说明缺什么。界面应把这种情况当作「用户选错了目录」
 *   的就地提示，而不是「操作失败」的错误弹窗。
 *
 * @param gameDirectory 游戏根目录路径（手动输入或对话框返回）
 * @returns 归一化后的结构检查结果
 * @throws AppError Binding 调用失败时的归一化错误
 */
export async function checkGameDirectory(gameDirectory: string): Promise<GameDirectoryCheckResult> {
  const request: GameDirectoryRequest = { gameDirectory }

  try {
    return normalizeGameDirectoryCheck(await LaunchService.CheckGameDirectory(request))
  } catch (error) {
    throw toAppError(error)
  }
}

/**
 * 弹出系统原生目录选择对话框，让用户选择游戏目录。
 *
 * 返回语义与 `pickJavaExecutable` 一致：
 * - 用户选中：`canceled=false`，`gameDirectory` 供立即回传检查，
 *   `gameRootDisplay` 供界面展示；
 * - **用户取消**：`canceled=true`，调用方应**静默保持原值**，不得弹错误提示；
 * - 对话框不可用：`errorCode` 非空，应提示「请手动填写路径」并保留文本框。
 *
 * @returns 归一化后的选择结果
 * @throws AppError Binding 调用失败时的归一化错误
 */
export async function pickGameDirectory(): Promise<PickGameDirectoryResult> {
  try {
    return normalizePickGameDirectory(await LaunchService.PickGameDirectory())
  } catch (error) {
    throw toAppError(error)
  }
}

/**
 * 把绑定生成物的结构检查结果归一化为前端类型。
 *
 * 归一化只做类型收敛：`checks` / `warnings` 为 null 时收敛为空数组，
 * 未知状态值收敛为 `missing`（不会误报为 `present`）。
 *
 * @param raw 绑定返回的原始结果
 * @returns 归一化后的结果
 */
function normalizeGameDirectoryCheck(raw: BoundGameDirectoryCheckResult): GameDirectoryCheckResult {
  const checks: GameDirCheckItem[] = Array.isArray(raw.checks)
    ? raw.checks.map((item) => ({
        name: typeof item.name === 'string' ? item.name : '',
        status: normalizeCheckStatus(item.status),
        detail: typeof item.detail === 'string' ? item.detail : '',
      }))
    : []

  return {
    valid: raw.valid === true,
    gameRootDisplay: typeof raw.gameRootDisplay === 'string' ? raw.gameRootDisplay : '',
    checks,
    versionsCount: typeof raw.versionsCount === 'number' ? raw.versionsCount : 0,
    launchableHint: typeof raw.launchableHint === 'string' ? raw.launchableHint : '',
    errorCode: typeof raw.errorCode === 'string' ? raw.errorCode : '',
    errorMessage: typeof raw.errorMessage === 'string' ? raw.errorMessage : '',
    warnings: Array.isArray(raw.warnings) ? raw.warnings.filter((item): item is string => typeof item === 'string') : [],
  }
}

/**
 * 把绑定生成物的目录选择结果归一化为前端类型。
 *
 * @param raw 绑定返回的原始结果
 * @returns 归一化后的选择结果
 */
function normalizePickGameDirectory(raw: BoundPickGameDirectoryResult): PickGameDirectoryResult {
  return {
    canceled: raw.canceled === true,
    gameDirectory: typeof raw.gameDirectory === 'string' ? raw.gameDirectory : '',
    gameRootDisplay: typeof raw.gameRootDisplay === 'string' ? raw.gameRootDisplay : '',
    errorCode: typeof raw.errorCode === 'string' ? raw.errorCode : '',
    errorMessage: typeof raw.errorMessage === 'string' ? raw.errorMessage : '',
  }
}

/**
 * 把任意状态字符串收敛为合法三态。
 *
 * 后端理论上只返回三个合法值，但收敛可以防止旧版本或被改写的数据让
 * 界面出现未定义状态；无法识别时按 `missing` 处理（不会误报为可用）。
 *
 * @param value 原始状态字符串
 * @returns 合法的三态值
 */
function normalizeCheckStatus(value: string): GameDirCheckStatus {
  switch (value) {
    case 'present':
      return 'present'
    case 'not_a_directory':
      return 'not_a_directory'
    default:
      return 'missing'
  }
}

/**
 * 判断检查结果是否因「路径本身不可用」而失败。
 *
 * 与「结构缺失」区分：结构缺失时 `errorCode` 为空，应展示 `launchableHint`；
 * 本函数为 true 时才应展示错误码与错误文案。
 *
 * @param result 结构检查结果
 * @returns 路径不可用时返回 true
 */
export function hasGameDirectoryError(result: GameDirectoryCheckResult): boolean {
  return !result.valid && result.errorCode !== ''
}

/**
 * 判断检查结果是否为「目录可用但结构缺失」。
 *
 * 这类结果应引导用户确认是不是选错了层级（例如选成了 `.minecraft/versions`），
 * 而不是显示「失败」。
 *
 * @param result 结构检查结果
 * @returns 结构缺失时返回 true
 */
export function isGameDirectoryIncomplete(result: GameDirectoryCheckResult): boolean {
  return !result.valid && result.errorCode === ''
}

/**
 * 按检查项名称查找单项结果。
 *
 * @param result 结构检查结果
 * @param name 检查项稳定标识（取值见 `GAME_DIR_CHECK_NAME`）
 * @returns 找到时返回该项；未找到返回 undefined
 */
export function findGameDirCheck(result: GameDirectoryCheckResult, name: string): GameDirCheckItem | undefined {
  return result.checks.find((item) => item.name === name)
}

// ===== P2-03 版本识别（本卡追加，不修改上方 P2-01 / P2-02 封装）=====

/**
 * 列举游戏根目录下的本地版本（**纯读取**）。
 *
 * 后端只读 `versions/` 的一层子目录，不递归、不跟随符号链接；
 * 每个版本都会给出四态判定与原因。
 *
 * @param gameDirectory 游戏根目录路径
 * @returns 归一化后的版本列表
 * @throws AppError Binding 调用失败时的归一化错误
 */
export async function listLocalVersions(gameDirectory: string): Promise<VersionListResult> {
  const request: VersionListRequest = { gameDirectory }

  try {
    return normalizeVersionListResult(await LaunchService.ListLocalVersions(request))
  } catch (error) {
    throw toAppError(error)
  }
}

/**
 * 检查单个本地版本能否启动（**纯读取**）。
 *
 * 注意：**缺 jar / 缺库 / 缺 natives 不是错误**，而是 `metadata_only` 的
 * 正常业务结论（此时 `errorCode` 为空，界面应展示 `reason` 与缺失清单）。
 *
 * @param gameDirectory 游戏根目录路径
 * @param versionId 版本 id
 * @returns 归一化后的检查结果
 * @throws AppError Binding 调用失败时的归一化错误
 */
export async function checkLocalVersion(
  gameDirectory: string,
  versionId: string,
): Promise<VersionCheckResult> {
  const request: VersionCheckRequest = { gameDirectory, versionId }

  try {
    return normalizeVersionCheckResult(await LaunchService.CheckLocalVersion(request))
  } catch (error) {
    throw toAppError(error)
  }
}

/**
 * 把绑定生成物的版本列表结果归一化为前端类型。
 *
 * 归一化只做类型收敛：数组为 null 时收敛为空数组，未知状态收敛为 `broken`
 * （不会误报为可启动）。
 *
 * @param raw 绑定返回的原始结果
 * @returns 归一化后的版本列表
 */
function normalizeVersionListResult(raw: BoundVersionListResult): VersionListResult {
  const versions: VersionSummary[] = Array.isArray(raw.versions)
    ? raw.versions.map((item) => ({
        id: typeof item.id === 'string' ? item.id : '',
        type: typeof item.type === 'string' ? item.type : '',
        status: normalizeVersionStatus(item.status),
        reason: typeof item.reason === 'string' ? item.reason : '',
      }))
    : []

  return {
    versions,
    gameRootDisplay: typeof raw.gameRootDisplay === 'string' ? raw.gameRootDisplay : '',
    skippedIds: Array.isArray(raw.skippedIds)
      ? raw.skippedIds.filter((item): item is string => typeof item === 'string')
      : [],
    errorCode: typeof raw.errorCode === 'string' ? raw.errorCode : '',
    errorMessage: typeof raw.errorMessage === 'string' ? raw.errorMessage : '',
    warnings: Array.isArray(raw.warnings) ? raw.warnings.filter((item): item is string => typeof item === 'string') : [],
  }
}

/**
 * 把绑定生成物的版本检查结果归一化为前端类型。
 *
 * `nativesPresent` 保持三态语义：`null` 表示未检查（版本无 natives 声明），
 * `false` 表示声明了但缺失。归一化不得把 `null` 折叠成 `false`，
 * 否则界面会误报「缺 natives」。
 *
 * @param raw 绑定返回的原始结果
 * @returns 归一化后的检查结果
 */
function normalizeVersionCheckResult(raw: BoundVersionCheckResult): VersionCheckResult {
  let nativesPresent: boolean | null = null
  if (typeof raw.nativesPresent === 'boolean') {
    nativesPresent = raw.nativesPresent
  }

  return {
    id: typeof raw.id === 'string' ? raw.id : '',
    type: typeof raw.type === 'string' ? raw.type : '',
    status: normalizeVersionStatus(raw.status),
    reason: typeof raw.reason === 'string' ? raw.reason : '',
    mainClass: typeof raw.mainClass === 'string' ? raw.mainClass : '',
    clientJarPresent: raw.clientJarPresent === true,
    librariesRequired: typeof raw.librariesRequired === 'number' ? raw.librariesRequired : 0,
    librariesPresent: typeof raw.librariesPresent === 'number' ? raw.librariesPresent : 0,
    librariesMissing: Array.isArray(raw.librariesMissing)
      ? raw.librariesMissing.filter((item): item is string => typeof item === 'string')
      : [],
    nativesPresent,
    javaMajorVersionRequired:
      typeof raw.javaMajorVersionRequired === 'number' ? raw.javaMajorVersionRequired : 0,
    warnings: Array.isArray(raw.warnings) ? raw.warnings.filter((item): item is string => typeof item === 'string') : [],
    slow: raw.slow === true,
    errorCode: typeof raw.errorCode === 'string' ? raw.errorCode : '',
    errorMessage: typeof raw.errorMessage === 'string' ? raw.errorMessage : '',
  }
}

/**
 * 把任意状态字符串收敛为合法四态。
 *
 * 无法识别时收敛为 `broken`：宁可提示「无法识别」也不要误报为可启动。
 *
 * @param value 原始状态字符串
 * @returns 合法的四态值
 */
function normalizeVersionStatus(value: string): VersionStatus {
  switch (value) {
    case 'launch_supported':
      return 'launch_supported'
    case 'metadata_only':
      return 'metadata_only'
    case 'unsupported':
      return 'unsupported'
    default:
      return 'broken'
  }
}

/**
 * 判断版本检查结果是否为「元数据可读但缺资源」。
 *
 * 这类结果应展示缺失清单与原因，而不是错误码 —— 用户能据此知道「差什么」。
 *
 * @param result 版本检查结果
 * @returns 属于 metadata_only 且无错误码时返回 true
 */
export function isVersionMetadataOnly(result: VersionCheckResult): boolean {
  return result.status === 'metadata_only' && result.errorCode === ''
}

/**
 * 判断版本检查结果是否因元数据问题而失败（JSON 损坏 / 继承环 / 缺 mainClass）。
 *
 * @param result 版本检查结果
 * @returns 带 VERSION_METADATA_001 时返回 true
 */
export function isVersionMetadataBroken(result: VersionCheckResult): boolean {
  return result.errorCode === VERSION_METADATA_ERROR_CODE
}

/**
 * 判断版本是否可尝试启动。
 *
 * @param result 版本检查结果
 * @returns 状态为 launch_supported 时返回 true
 */
export function isVersionLaunchable(result: VersionCheckResult): boolean {
  return result.status === 'launch_supported'
}

// ===== P2-04 启动计划预览（本卡追加，不修改上方既有封装）=====

/**
 * 构建但**不启动**游戏，返回脱敏后的启动计划预览。
 *
 * 返回结构四路分离（classpath / mainClass / JVM 参数 / 游戏参数），
 * 且**不含任何未脱敏绝对路径**。
 *
 * 注意：本函数**不抛错**构建失败——失败通过 `warnings`（`blocking` 级）
 * 与 `safeToLaunch=false` 表达，这样界面仍能展示「为什么不能启动」。
 *
 * @param gameDirectory 游戏根目录路径
 * @param versionId 版本 id
 * @param additionalJvmArgs 附加 JVM 参数（可选）
 * @param additionalGameArgs 附加游戏参数（可选）
 * @returns 归一化后的启动计划预览
 * @throws AppError Binding 调用失败时的归一化错误
 */
export async function previewLaunchPlan(
  gameDirectory: string,
  versionId: string,
  additionalJvmArgs?: string[],
  additionalGameArgs?: string[],
): Promise<LaunchPlanPreview> {
  // 生成物的字段类型是 `string[] | null`（Go 切片可为 nil），
  // 因此这里把省略的参数显式置为 null，而不是 undefined。
  const request: LaunchPlanPreviewRequest = {
    gameDirectory,
    versionId,
    additionalJvmArgs: additionalJvmArgs ?? null,
    additionalGameArgs: additionalGameArgs ?? null,
  }

  try {
    return normalizeLaunchPlanPreview(await LaunchService.PreviewLaunchPlan(request))
  } catch (error) {
    throw toAppError(error)
  }
}

/**
 * 把绑定生成物的预览结果归一化为前端类型。
 *
 * 归一化只做类型收敛：数组为 null 时收敛为空数组，未知严重级别收敛为 `warn`
 * （既不会误判为可启动，也不会过度阻断）。
 *
 * @param raw 绑定返回的原始结果
 * @returns 归一化后的预览
 */
function normalizeLaunchPlanPreview(raw: BoundLaunchPlanPreview): LaunchPlanPreview {
  return {
    versionId: typeof raw.versionId === 'string' ? raw.versionId : '',
    mainClass: typeof raw.mainClass === 'string' ? raw.mainClass : '',
    classPathEntryCount: typeof raw.classPathEntryCount === 'number' ? raw.classPathEntryCount : 0,
    classPathEntries: Array.isArray(raw.classPathEntries)
      ? raw.classPathEntries.filter((item): item is string => typeof item === 'string')
      : [],
    jvmArgs: normalizePreviewArgs(raw.jvmArgs),
    gameArgs: normalizePreviewArgs(raw.gameArgs),
    nativesDirectoryDisplay: typeof raw.nativesDirectoryDisplay === 'string' ? raw.nativesDirectoryDisplay : '',
    assetsDirectoryDisplay: typeof raw.assetsDirectoryDisplay === 'string' ? raw.assetsDirectoryDisplay : '',
    workingDirectoryDisplay:
      typeof raw.workingDirectoryDisplay === 'string' ? raw.workingDirectoryDisplay : '',
    assetIndexID: typeof raw.assetIndexID === 'string' ? raw.assetIndexID : '',
    warnings: Array.isArray(raw.warnings)
      ? raw.warnings.map((item) => ({
          code: typeof item.code === 'string' ? item.code : '',
          message: typeof item.message === 'string' ? item.message : '',
          severity: normalizeWarningSeverity(item.severity),
        }))
      : [],
    unresolvedPlaceholders: Array.isArray(raw.unresolvedPlaceholders)
      ? raw.unresolvedPlaceholders.filter((item): item is string => typeof item === 'string')
      : [],
    safeToLaunch: raw.safeToLaunch === true,
    offlineIdentityNotice: typeof raw.offlineIdentityNotice === 'string' ? raw.offlineIdentityNotice : '',

    // P2-09 追加字段：只做类型收敛，不补造数据。
    versionType: typeof raw.versionType === 'string' ? raw.versionType : '',
    javaDisplay: typeof raw.javaDisplay === 'string' ? raw.javaDisplay : '',
    javaMajorVersion: typeof raw.javaMajorVersion === 'number' ? raw.javaMajorVersion : 0,
    javaCheckStale: raw.javaCheckStale === true,
    classPathMissingCount: typeof raw.classPathMissingCount === 'number' ? raw.classPathMissingCount : 0,
    blockingWarnings: Array.isArray(raw.blockingWarnings)
      ? raw.blockingWarnings.map((item) => ({
          code: typeof item.code === 'string' ? item.code : '',
          message: typeof item.message === 'string' ? item.message : '',
          severity: normalizeWarningSeverity(item.severity),
        }))
      : [],
    buildFailed: raw.buildFailed === true,
    buildErrorCode: typeof raw.buildErrorCode === 'string' ? raw.buildErrorCode : '',
    buildErrorMessage: typeof raw.buildErrorMessage === 'string' ? raw.buildErrorMessage : '',
    diagnosticsText: typeof raw.diagnosticsText === 'string' ? raw.diagnosticsText : '',
  }
}

/**
 * 归一化预览参数数组。
 *
 * @param raw 绑定返回的参数数组（可能为 null）
 * @returns 归一化后的参数数组
 */
function normalizePreviewArgs(raw: BoundPreviewArgumentPart[] | null): PreviewArgumentPart[] {
  if (!Array.isArray(raw)) {
    return []
  }
  return raw.map((item) => ({
    value: typeof item.value === 'string' ? item.value : '',
    source: typeof item.source === 'string' ? item.source : '',
    redacted: item.redacted === true,
    // P2-09：补充标注（目前用于「离线占位身份」），缺省为空串。
    note: typeof item.note === 'string' ? item.note : '',
  }))
}

/**
 * 把任意严重级别字符串收敛为合法值。
 *
 * @param value 原始级别字符串
 * @returns 合法严重级别；无法识别时按 `warn` 处理
 */
function normalizeWarningSeverity(value: string): ArgumentWarningSeverity {
  switch (value) {
    case 'blocking':
      return 'blocking'
    case 'info':
      return 'info'
    default:
      return 'warn'
  }
}

/**
 * 判断预览是否因存在阻断项而不可启动。
 *
 * @param preview 启动计划预览
 * @returns 存在 blocking 警告时为 true
 */
export function hasBlockingWarning(preview: LaunchPlanPreview): boolean {
  return preview.warnings.some((warning) => warning.severity === 'blocking')
}

/**
 * 判断预览是否可安全交给进程层执行。
 *
 * 依据后端结论：`safeToLaunch` 已综合「未解析占位符」与「blocking 警告」。
 *
 * @param preview 启动计划预览
 * @returns 可启动时返回 true
 */
export function isPlanSafeToLaunch(preview: LaunchPlanPreview): boolean {
  return preview.safeToLaunch
}

/**
 * 从预览中提取「为什么不能启动」的简短原因列表（供界面逐条展示）。
 *
 * @param preview 启动计划预览
 * @returns 原因文本数组；可启动时为空数组
 */
export function describePlanBlockers(preview: LaunchPlanPreview): string[] {
  const reasons: string[] = []
  if (preview.unresolvedPlaceholders.length > 0) {
    reasons.push(`存在未解析的占位符：${preview.unresolvedPlaceholders.join('、')}`)
  }
  for (const warning of preview.warnings) {
    if (warning.severity === 'blocking') {
      reasons.push(warning.message)
    }
  }
  return reasons
}

// ===== 本卡（P2-08）追加：启动状态、游戏日志与事件订阅 =====
// 说明：以下内容全部追加在文件末尾，上方 P2-01~P2-04 的函数语义一字未改。

/**
 * 把任意字符串收敛为合法的启动状态值。
 *
 * 后端新增状态时前端不会因此崩掉：无法识别的值收敛为空字符串（按「无状态」处理），
 * 界面再按快照的 `errorCode`/`errorMessage` 给出说明，而不是猜测一个状态。
 *
 * @param value 原始状态字符串
 * @returns 合法的启动状态
 */
function toLaunchState(value: unknown): LaunchState {
  switch (value) {
    case 'preparing':
    case 'resolving_runtime':
    case 'building_arguments':
    case 'starting_process':
    case 'running':
    case 'stopping':
    case 'exited':
    case 'failed':
    case 'cancelled':
      return value
    default:
      return ''
  }
}

/**
 * 把任意字符串收敛为合法的停止原因。
 *
 * @param value 原始字符串
 * @returns 合法停止原因；无法识别时为空字符串
 */
function toLaunchStopReason(value: unknown): LaunchStopReason {
  switch (value) {
    case 'user_requested':
    case 'app_exiting':
    case 'startup_failed':
      return value
    default:
      return ''
  }
}

/**
 * 把任意字符串收敛为合法的停止结果。
 *
 * @param value 原始字符串
 * @returns 合法停止结果；无法识别时为空字符串
 */
function toLaunchStopResult(value: unknown): LaunchStopResult {
  switch (value) {
    case 'graceful':
    case 'killed':
    case 'timeout':
      return value
    default:
      return ''
  }
}

/**
 * 把任意字符串收敛为合法的退出结局。
 *
 * @param value 原始字符串
 * @returns 合法结局；无法识别时为空字符串
 */
function toLaunchOutcome(value: unknown): LaunchOutcome {
  switch (value) {
    case 'exited_normally':
    case 'stopped_by_user':
    case 'crashed':
      return value
    default:
      return ''
  }
}

/**
 * 把任意字符串收敛为合法的输出来源。
 *
 * @param value 原始字符串
 * @returns 合法来源；无法识别时按 stdout 处理（不把未知输出当错误）
 */
function toGameLogStream(value: unknown): GameLogStream {
  return value === 'stderr' ? 'stderr' : 'stdout'
}

/**
 * 把绑定返回的状态快照归一化为前端类型。
 *
 * 只收敛类型，不改语义：数值缺失按 0、字符串缺失按空串处理，
 * 让界面无需逐字段判空。
 *
 * @param raw 绑定返回的快照
 * @returns 归一化后的状态快照
 */
function normalizeLaunchStatus(raw: BoundLaunchStatusResult): LaunchStatus {
  return {
    launchId: typeof raw.launchId === 'string' ? raw.launchId : '',
    state: toLaunchState(raw.state),
    versionId: typeof raw.versionId === 'string' ? raw.versionId : '',
    startedAtMs: typeof raw.startedAtMs === 'number' ? raw.startedAtMs : 0,
    endedAtMs: typeof raw.endedAtMs === 'number' ? raw.endedAtMs : 0,
    exitCode: typeof raw.exitCode === 'number' ? raw.exitCode : 0,
    hasExitCode: raw.hasExitCode === true,
    stopReason: toLaunchStopReason(raw.stopReason),
    stopResult: toLaunchStopResult(raw.stopResult),
    outcome: toLaunchOutcome(raw.outcome),
    stateChangedAtMs: typeof raw.stateChangedAtMs === 'number' ? raw.stateChangedAtMs : 0,
    errorCode: typeof raw.errorCode === 'string' ? raw.errorCode : '',
    errorMessage: typeof raw.errorMessage === 'string' ? raw.errorMessage : '',
  }
}

/**
 * 把一行日志归一化为前端类型。
 *
 * @param raw 绑定返回的日志行
 * @returns 归一化后的日志行
 */
function normalizeGameLogLine(raw: BoundGameLogLineResult): GameLogLineResult {
  return {
    timestampMs: typeof raw.timestampMs === 'number' ? raw.timestampMs : 0,
    stream: toGameLogStream(raw.stream),
    text: typeof raw.text === 'string' ? raw.text : '',
  }
}

/**
 * 把日志快照归一化为前端类型。
 *
 * `lines` 在 Go 侧可能为 nil（生成物类型为 `GameLogLineResult[] | null`），
 * 这里统一收敛为空数组，避免界面处理 null。
 *
 * @param raw 绑定返回的查询结果
 * @returns 归一化后的查询结果
 */
function normalizeGameLogQueryResult(raw: BoundGameLogQueryResult): GameLogQueryResult {
  return {
    lines: Array.isArray(raw.lines) ? raw.lines.map(normalizeGameLogLine) : [],
    nextBeforeMs: typeof raw.nextBeforeMs === 'number' ? raw.nextBeforeMs : 0,
    droppedLines: typeof raw.droppedLines === 'number' ? raw.droppedLines : 0,
    hasMore: raw.hasMore === true,
    errorCode: typeof raw.errorCode === 'string' ? raw.errorCode : '',
    errorMessage: typeof raw.errorMessage === 'string' ? raw.errorMessage : '',
  }
}

/**
 * 调用后端启动一个本地 Minecraft 进程。
 *
 * 只传结构化参数（Java 路径、游戏目录、版本 id、可选附加参数），不接受任意命令行。
 * 后端按 D-9 只允许一个活动进程：已有活动进程时返回 `LAUNCH_STATE_001`，
 * 由调用方如实展示，不做排队重试。
 *
 * @param request 启动请求
 * @returns 受理结果（launchId、受理时间、初始状态）
 * @throws AppError 后端拒绝启动或调用失败时的归一化错误
 */
export async function startLaunchProcess(request: StartLaunchRequest): Promise<LaunchAcceptedResult> {
  try {
    const raw = await LaunchService.StartLaunch(request)
    return {
      launchId: typeof raw.launchId === 'string' ? raw.launchId : '',
      acceptedAtMs: typeof raw.acceptedAtMs === 'number' ? raw.acceptedAtMs : 0,
      state: toLaunchState(raw.state),
    }
  } catch (error) {
    throw toAppError(error)
  }
}

/**
 * 请求停止由本启动器托管的进程。
 *
 * `force=true` 表示直接强制终止；调用方必须已经过二次确认（D-3：
 * Windows 上强制终止不执行 JVM 关闭钩子，Minecraft 可能来不及保存世界数据）。
 *
 * 注意返回的是**停止中**的快照而不是「已退出」：进程退出要等后续事件或下一次快照查询，
 * 界面必须能区分 `stopping` 与 `exited`。
 *
 * @param request 停止请求
 * @returns 停止后的状态快照
 * @throws AppError 调用失败时的归一化错误
 */
export async function stopLaunchProcess(request: StopLaunchRequest): Promise<LaunchStatus> {
  try {
    return normalizeLaunchStatus(await LaunchService.StopLaunch(request))
  } catch (error) {
    throw toAppError(error)
  }
}

/**
 * 读取启动状态快照（**界面的唯一真相源**）。
 *
 * @param launchId 要查询的启动标识；空字符串表示当前活动启动（无活动时回到最近终态记录）
 * @returns 状态快照
 * @throws AppError 查询失败（快照自带 `LAUNCH_STATE_001`）时的归一化错误
 */
export async function fetchLaunchStatus(launchId = ''): Promise<LaunchStatus> {
  try {
    const status = normalizeLaunchStatus(await LaunchService.GetLaunchStatus({ launchId }))
    if (status.errorCode !== '') {
      // 后端约定「查询失败也返回同一种 DTO」，这里把错误码转成异常，
      // 让调用方只有一条错误路径（try/catch + AppError），不必再判 DTO 字段。
      throw toAppError({ code: status.errorCode, message: status.errorMessage })
    }
    return status
  } catch (error) {
    throw toAppError(error)
  }
}

/**
 * 读取游戏日志的一页快照（事件丢失后的恢复入口）。
 *
 * @param request 查询条件（launchId / beforeMs / limit）
 * @returns 日志页（按时间倒序，最新在前）
 * @throws AppError 读取失败（`LAUNCH_LOG_001`）时的归一化错误
 */
export async function fetchGameLog(request: GameLogQueryRequest): Promise<GameLogQueryResult> {
  try {
    const result = normalizeGameLogQueryResult(await LaunchService.GetGameLog(request))
    if (result.errorCode !== '') {
      throw toAppError({ code: result.errorCode, message: result.errorMessage })
    }
    return result
  } catch (error) {
    throw toAppError(error)
  }
}

/**
 * 把 Wails 事件对象转换为本模块使用的事件记录。
 *
 * 与 `services/events.ts` 中的同名私有函数同构；这里另写一份是因为
 * `services/events.ts` 属 P1-06 的冻结文件，本卡不允许改动，也不应把
 * launch 事件塞进那个「P1 时代」的订阅表里。
 *
 * @param source 后端推送的事件对象
 * @returns 扁平化后的事件记录
 */
function toEventRecord<T>(source: {
  eventId: string
  eventName: string
  schemaVersion: number
  occurredAt: number
  correlationId: string
  payload: T
}): EventEnvelopeBody<T> {
  return {
    eventId: source.eventId,
    eventName: source.eventName,
    schemaVersion: source.schemaVersion,
    occurredAt: source.occurredAt,
    correlationId: source.correlationId,
    payload: source.payload,
  }
}

/**
 * 订阅 `launch.state.changed.v1`。
 *
 * 事件只用于推动界面刷新：载荷可能乱序或缺失，**不得**把它当作状态真相。
 * 调用方收到事件后应重新调用 `fetchLaunchStatus`，以快照覆盖本地状态。
 *
 * @param handler 事件处理函数
 * @returns 幂等的解绑函数
 */
export function subscribeLaunchStateChanged(
  handler: (event: EventEnvelopeBody<LaunchStateChangedPayload>) => void,
): Unsubscribe {
  let active = true

  const release = Events.On(EVENT_LAUNCH_STATE_CHANGED, (event) => {
    if (!active) {
      return
    }
    const payload = event?.data?.payload
    if (!payload) {
      // 载荷缺失时忽略：事件可能重复或损坏，界面不应因此报错。
      return
    }
    handler(
      toEventRecord({
        ...event.data,
        payload: normalizeLaunchStateChangedPayload(payload),
      }),
    )
  })

  return () => {
    if (!active) {
      return
    }
    // 先置位再解绑：即使底层抛错也不会让解绑函数被重复执行。
    active = false
    release()
  }
}

/**
 * 把状态迁移事件的载荷归一化为前端类型。
 *
 * 缺失字段按空值处理，`hasExitCode` 默认 false——「没有退出码」比「退出码 0」更保守。
 *
 * @param raw 绑定返回的事件载荷
 * @returns 归一化后的载荷
 */
function normalizeLaunchStateChangedPayload(raw: BoundLaunchStateChangedPayload): LaunchStateChangedPayload {
  const payload: LaunchStateChangedPayload = {
    launchId: typeof raw.launchId === 'string' ? raw.launchId : '',
    state: toLaunchState(raw.state),
    hasExitCode: raw.hasExitCode === true,
    occurredAtMs: typeof raw.occurredAtMs === 'number' ? raw.occurredAtMs : 0,
  }
  if (typeof raw.stopReason === 'string') {
    payload.stopReason = toLaunchStopReason(raw.stopReason)
  }
  if (typeof raw.exitCode === 'number') {
    payload.exitCode = raw.exitCode
  }
  return payload
}

/**
 * 订阅 `launch.log.appended.v1`。
 *
 * 每批携带若干行；`droppedLines` 是**累计值**，前端直接展示即可感知日志不完整，
 * 不需要自己累加（事件丢失时也不会算错）。
 *
 * @param handler 事件处理函数
 * @returns 幂等的解绑函数
 */
export function subscribeLaunchLogAppended(
  handler: (event: EventEnvelopeBody<LaunchLogAppendedPayload>) => void,
): Unsubscribe {
  let active = true

  const release = Events.On(EVENT_LAUNCH_LOG_APPENDED, (event) => {
    if (!active) {
      return
    }
    const payload = event?.data?.payload
    if (!payload) {
      return
    }
    handler(
      toEventRecord({
        ...event.data,
        payload: normalizeLaunchLogAppendedPayload(payload),
      }),
    )
  })

  return () => {
    if (!active) {
      return
    }
    active = false
    release()
  }
}

/**
 * 把日志增量事件的载荷归一化为前端类型。
 *
 * @param raw 绑定返回的事件载荷
 * @returns 归一化后的载荷
 */
function normalizeLaunchLogAppendedPayload(raw: BoundLaunchLogAppendedPayload): LaunchLogAppendedPayload {
  return {
    launchId: typeof raw.launchId === 'string' ? raw.launchId : '',
    lines: Array.isArray(raw.lines) ? raw.lines.map(normalizeGameLogLine) : [],
    droppedLines: typeof raw.droppedLines === 'number' ? raw.droppedLines : 0,
  }
}
// ===== P2-13 启动配置持久化（本卡追加，不修改上方既有函数）=====

/**
 * 把绑定返回的启动配置摘要归一化为前端类型。
 *
 * 归一化只做类型收敛，不改语义：缺失字段收敛为空串或 0，
 * 让界面无需处理 `undefined`。**本函数不补任何路径字段**：
 * 后端摘要本来就没有绝对路径。
 *
 * @param raw 绑定返回的原始摘要
 * @returns 归一化后的摘要
 */
function normalizeLaunchProfileSummary(raw: BoundLaunchProfileSummary): LaunchProfileSummary {
  return {
    id: typeof raw.id === 'string' ? raw.id : '',
    name: typeof raw.name === 'string' ? raw.name : '',
    javaDisplay: typeof raw.javaDisplay === 'string' ? raw.javaDisplay : '',
    gameRootDisplay: typeof raw.gameRootDisplay === 'string' ? raw.gameRootDisplay : '',
    versionId: typeof raw.versionId === 'string' ? raw.versionId : '',
    lastUsedAtMs: typeof raw.lastUsedAtMs === 'number' ? raw.lastUsedAtMs : 0,
  }
}

/**
 * 读取最近使用的一条启动配置。
 *
 * 返回 `null` 表示「还没有任何配置」（首次使用），不是错误：
 * 后端用零值摘要表达这个状态，这里收敛为 `null` 让调用方只判断一种空。
 *
 * 注意：后端会**重新校验**历史路径是否仍然存在；配置已失效时抛出
 * `LAUNCH_PROFILE_001`，调用方应如实提示用户重新选择。
 *
 * @returns 最近配置的脱敏摘要，或 null
 * @throws AppError 读取失败或历史配置已失效时
 */
export async function fetchMostRecentLaunchProfile(): Promise<LaunchProfileSummary | null> {
  try {
    const raw = await LaunchService.GetMostRecentLaunchProfile()
    const summary = normalizeLaunchProfileSummary(raw)
    // 零值摘要的 id 为空串：以此判定「没有任何配置」。
    return summary.id === '' ? null : summary
  } catch (error) {
    throw toAppError(error)
  }
}

/**
 * 保存一条启动配置。
 *
 * 参数 request 中的路径只上行，不入回包；返回值是脱敏摘要。
 *
 * @param request 保存请求（含绝对路径）
 * @returns 保存后的脱敏摘要
 * @throws AppError 保存失败时
 */
export async function saveLaunchProfile(
  request: SaveLaunchProfileRequest,
): Promise<LaunchProfileSummary> {
  try {
    const raw = await LaunchService.SaveLaunchProfile(request as BoundSaveLaunchProfileRequest)
    return normalizeLaunchProfileSummary(raw)
  } catch (error) {
    throw toAppError(error)
  }
}

/**
 * 列出全部启动配置（按最近使用时间倒序）。
 *
 * P2 阶段没有配置列表界面，本函数供后续卡与排查使用。
 *
 * @returns 脱敏摘要数组（可能为空数组）
 * @throws AppError 读取失败时
 */
export async function listLaunchProfiles(): Promise<LaunchProfileSummary[]> {
  try {
    const raw = await LaunchService.ListLaunchProfiles()
    return (raw ?? []).map(normalizeLaunchProfileSummary)
  } catch (error) {
    throw toAppError(error)
  }
}

/**
 * 删除指定启动配置（幂等：不存在也算成功）。
 *
 * @param id 配置标识
 * @returns 配置是否已不存在
 * @throws AppError 删除失败时
 */
export async function deleteLaunchProfile(id: string): Promise<boolean> {
  try {
    const raw = await LaunchService.DeleteLaunchProfile({ id })
    return raw.deleted === true
  } catch (error) {
    throw toAppError(error)
  }
}