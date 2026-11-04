import { WailsService } from '../../bindings/example.com/ninevoxel/launcher/internal/settings'
import type { SnapshotDTO, UpdateDTO } from '../../bindings/example.com/ninevoxel/launcher/internal/settings'
import { toAppError } from './errors'
import type { AppError } from '../types/app'

/**
 * 设置服务的绑定封装层。
 *
 * 前端只有本文件可以 import 生成目录中的设置绑定；页面与组件必须通过这里
 * 暴露的语义化函数访问后端（AGENTS.md 8.2、P1-04 要求 E.5）。
 *
 * 绑定路径说明：设置服务定义在 `internal/settings` 包内（因为本卡禁止修改
 * `main.go`，服务类型必须放在 `internal/app/bootstrap.go` 可引用的位置），
 * 因此生成物位于 `bindings/…/launcher/internal/settings/`。
 * `bindings/**` 是生成目录（见 `.gitignore`），不手工修改。
 */

/** 设置项 key 常量，与 Go 侧 `internal/settings/schema.go` 逐字一致。 */
export const SETTINGS_KEY = {
  /** 界面主题，取值 light / dark / system。 */
  theme: 'appearance.theme',
  /** 界面语言，取值 zh-Hans / zh-Hant / en。 */
  language: 'appearance.language',
  /** 窗口宽度（像素）。 */
  windowWidth: 'window.width',
  /** 窗口高度（像素）。 */
  windowHeight: 'window.height',
  /** 窗口是否最大化。 */
  windowMaximized: 'window.maximized',
  /** 更新渠道，只读占位。 */
  updateChannel: 'update.channel',
  /**
   * 下载镜像开关（D-37 / D-54）。
   *
   * 打开后：游戏内容下载走 BMCLAPI 镜像，第三方 Java 运行时（Adoptium）走清华 TUNA 镜像；
   * Mojang 官方 Java 运行时组件不读该开关（D-50）。
   */
  downloadMirrorEnabled: 'download.mirrorEnabled',
  /**
   * 用户显式指定的 Java 可执行文件路径。
   *
   * 空串表示未指定：此时启动器只使用自己下载安装的运行时，不探测本机任何 Java
   * （不读 JAVA_HOME、不扫 PATH、不遍历常见安装根）。
   */
  launchJavaPath: 'launch.javaPath',
  /**
   * 全局默认最大分配内存（MiB）。
   *
   * 存储层只保证闭区间；真实可用上限由界面按本机物理内存动态计算，
   * 因此回显值可能大于当前机器容量（换机器后读到旧值）。
   */
  launchMaxMemoryMb: 'launch.maxMemoryMb',
} as const

/** 设置项 key 的联合类型。 */
export type SettingsKey = (typeof SETTINGS_KEY)[keyof typeof SETTINGS_KEY]

/** 主题取值。 */
export type ThemeValue = 'light' | 'dark' | 'system'

/** 语言取值。 */
export type LanguageValue = 'zh-Hans' | 'zh-Hant' | 'en'

/** 更新渠道取值。 */
export type UpdateChannelValue = 'stable' | 'beta' | 'nightly'

/**
 * 设置快照（前端视图模型）。
 *
 * 字段与 Go 侧 `settings.SnapshotDTO` 逐字段一致；`enabled` 为 false 时
 * 界面必须提示「本次运行仅保存在内存」，不得显示「重启后仍然有效」。
 */
export interface SettingsSnapshot {
  /** 主题。 */
  theme: string
  /** 语言。 */
  language: string
  /** 窗口宽度（像素）。 */
  windowWidth: number
  /** 窗口高度（像素）。 */
  windowHeight: number
  /** 窗口是否最大化。 */
  windowMaximized: boolean
  /** 更新渠道（只读）。 */
  updateChannel: string
  /**
   * 下载镜像开关（D-37 / D-54）。
   *
   * 该值只能来自后端快照的真实回显：界面不得用本地默认值兜底，
   * 否则用户会看到「保存成功但永远是关闭」，而下载实际上已走镜像。
   */
  downloadMirrorEnabled: boolean
  /**
   * 用户显式指定的 Java 可执行文件路径（`launch.javaPath`）。
   *
   * 空串表示未指定：启动器只使用自己下载安装的运行时，不探测本机任何 Java。
   */
  launchJavaPath: string
  /** 全局默认最大分配内存（MiB）。 */
  launchMaxMemoryMb: number
  /** 最近一次写入时间（UTC Unix 毫秒）；从未写入为 0。 */
  updatedAtMs: number
  /** 是否处于持久化模式。 */
  enabled: boolean
  /** 存储来源：sqlite / memory。 */
  storageKind: string
  /** 数据库目录来源类别：appRoot / exeDir / unavailable（不是路径）。 */
  pathKind: string
  /** 初始化失败的稳定错误码；成功时为空。 */
  initErrorCode: string
}

/** 窗口尺寸约束，与 Go 侧 `MinWindowWidth` / `MaxWindowWidth` 等常量一致。 */
export const WINDOW_LIMITS = {
  /** window.width 的下限。 */
  minWidth: 800,
  /** window.width 的上限。 */
  maxWidth: 7680,
  /** window.height 的下限。 */
  minHeight: 600,
  /** window.height 的上限。 */
  maxHeight: 4320,
} as const

/**
 * 全局默认最大分配内存的存储层范围（MiB）。
 *
 * 与 Go 侧 `MinLaunchMemoryMb` / `MaxLaunchMemoryMb` 保持一致：
 * 下限是 Minecraft 能启动的门槛，上限只用于挡住垃圾输入。
 * 真实可用上限由界面按本机物理内存计算，不在这里。
 */
export const MEMORY_LIMITS = {
  /** launch.maxMemoryMb 的下限。 */
  minMemoryMb: 2048,
  /** launch.maxMemoryMb 的上限（1 TiB）。 */
  maxMemoryMb: 1024 * 1024,
} as const

/** 主题允许值。 */
export const THEME_VALUES: readonly ThemeValue[] = ['system', 'light', 'dark']

/** 语言允许值。 */
export const LANGUAGE_VALUES: readonly LanguageValue[] = ['zh-Hans', 'zh-Hant', 'en']

/** 更新渠道允许值（只读展示用）。 */
export const UPDATE_CHANNEL_VALUES: readonly UpdateChannelValue[] = ['stable', 'beta', 'nightly']

/** 设置服务不可用时对用户展示的说明，不含技术细节。 */
const SETTINGS_UNAVAILABLE_MESSAGE = '设置服务尚未就绪，请稍后重试。'

/** 设置项字段与后端契约不一致时使用的稳定错误码（见接口冻结清单 §8.3）。 */
const SETTINGS_FIELD_MISMATCH_CODE = 'APP_CONFIG_002'

/**
 * 把生成物的快照归一化为前端视图模型。
 *
 * 归一化只做类型收敛，不改数值：字符串字段为空时回退默认值，
 * 数值字段非法时回退默认值，避免界面出现 `undefined` 或 NaN。
 *
 * 例外：`downloadMirrorEnabled` **不做默认值兜底**。它是「用户能否确认镜像已生效」的唯一
 * 依据，若绑定缺该字段却静默按 false 处理，界面会永远显示「关闭」而下载实际已走镜像，
 * 属于界面与真实状态不符。因此字段缺失或类型不符时抛稳定错误码，由界面显示错误态。
 *
 * @param raw 生成物返回的原始快照
 * @returns 归一化后的快照
 * @throws AppError 快照缺少下载镜像开关字段时
 */
function normalizeSnapshot(raw: SnapshotDTO): SettingsSnapshot {
  // 逐字段读取：绑定尚未重新生成时该字段在运行期不存在，不能依赖类型声明存在就一定存在。
  const rawRecord = raw as unknown as Record<string, unknown>
  const mirrorEnabled = rawRecord['downloadMirrorEnabled']
  if (typeof mirrorEnabled !== 'boolean') {
    throw toAppError({
      code: SETTINGS_FIELD_MISMATCH_CODE,
      message: '设置快照缺少下载镜像开关字段，请更新后端或重新生成绑定后再试。',
      retryable: false,
    })
  }

  return {
    theme: isThemeValue(raw.theme) ? raw.theme : 'system',
    language: isLanguageValue(raw.language) ? raw.language : 'zh-Hans',
    windowWidth: normalizeDimension(raw.windowWidth, WINDOW_LIMITS.minWidth, WINDOW_LIMITS.maxWidth, 1120),
    windowHeight: normalizeDimension(raw.windowHeight, WINDOW_LIMITS.minHeight, WINDOW_LIMITS.maxHeight, 760),
    windowMaximized: typeof raw.windowMaximized === 'boolean' ? raw.windowMaximized : false,
    updateChannel: isUpdateChannelValue(raw.updateChannel) ? raw.updateChannel : 'stable',
    downloadMirrorEnabled: mirrorEnabled,
    // 路径型字段缺失时回退空串：空串本身就是「未指定」的合法语义，
    // 与 downloadMirrorEnabled 不同，这里不需要把缺字段当成契约破坏。
    launchJavaPath:
      typeof rawRecord['launchJavaPath'] === 'string' ? (rawRecord['launchJavaPath'] as string) : '',
    launchMaxMemoryMb: normalizeDimension(
      typeof rawRecord['launchMaxMemoryMb'] === 'number'
        ? (rawRecord['launchMaxMemoryMb'] as number)
        : Number.NaN,
      MEMORY_LIMITS.minMemoryMb,
      MEMORY_LIMITS.maxMemoryMb,
      8192
    ),
    updatedAtMs: typeof raw.updatedAtMs === 'number' && raw.updatedAtMs > 0 ? raw.updatedAtMs : 0,
    enabled: typeof raw.enabled === 'boolean' ? raw.enabled : false,
    storageKind: typeof raw.storageKind === 'string' ? raw.storageKind : 'memory',
    pathKind: typeof raw.pathKind === 'string' ? raw.pathKind : 'unavailable',
    initErrorCode: typeof raw.initErrorCode === 'string' ? raw.initErrorCode : '',
  }
}

/**
 * 判断值是否为合法主题。
 *
 * @param value 待判断的值
 */
function isThemeValue(value: unknown): value is ThemeValue {
  return value === 'light' || value === 'dark' || value === 'system'
}

/**
 * 判断值是否为合法语言。
 *
 * @param value 待判断的值
 */
function isLanguageValue(value: unknown): value is LanguageValue {
  return value === 'zh-Hans' || value === 'zh-Hant' || value === 'en'
}

/**
 * 判断值是否为合法更新渠道。
 *
 * @param value 待判断的值
 */
function isUpdateChannelValue(value: unknown): value is UpdateChannelValue {
  return value === 'stable' || value === 'beta' || value === 'nightly'
}

/**
 * 把数值字段收敛到合法区间。
 *
 * @param value 原始值
 * @param min 下限
 * @param max 上限
 * @param fallback 非法时使用的默认值
 */
function normalizeDimension(value: unknown, min: number, max: number, fallback: number): number {
  if (typeof value !== 'number' || !Number.isFinite(value)) {
    return fallback
  }
  const rounded = Math.trunc(value)
  if (rounded < min || rounded > max) {
    return fallback
  }
  return rounded
}

/**
 * 读取当前设置快照。
 *
 * 这是设置页初始化与「事件丢失后恢复」的权威数据源（事件可能丢失或重复）。
 *
 * @returns 归一化后的设置快照
 * @throws AppError 后端返回错误时的归一化错误
 */
export async function fetchSettingsSnapshot(): Promise<SettingsSnapshot> {
  try {
    return normalizeSnapshot(await WailsService.Snapshot())
  } catch (error) {
    throw toAppError(error)
  }
}

/**
 * 批量更新设置项。
 *
 * 只传需要修改的 key；未出现的 key 保持不变。Go 边界会逐 key 校验
 * 白名单、类型与取值范围，任一不合法则整批不落盘并返回 `APP_CONFIG_002`。
 *
 * @param changes key → 目标值
 * @returns 更新后的快照
 * @throws AppError 校验失败或写入失败时的归一化错误
 */
export async function updateSettings(changes: Record<string, unknown>): Promise<SettingsSnapshot> {
  if (Object.keys(changes).length === 0) {
    // 空更新是合法的空操作：直接读当前快照，避免无意义的写盘与事件。
    return fetchSettingsSnapshot()
  }

  try {
    const request: UpdateDTO = { changes }
    return normalizeSnapshot(await WailsService.Update(request))
  } catch (error) {
    throw toAppError(error)
  }
}

/**
 * 只更新单个设置项。
 *
 * 语义糖衣：供主题/语言切换这类「一次只改一个 key」的调用点使用。
 *
 * @param key 设置项 key
 * @param value 目标值
 * @returns 更新后的快照
 * @throws AppError 校验失败或写入失败时的归一化错误
 */
export async function updateSingleSetting(key: SettingsKey, value: unknown): Promise<SettingsSnapshot> {
  return updateSettings({ [key]: value })
}

/**
 * 尝试从后端读取某个设置项的值，失败时返回 null。
 *
 * 供主题与语言的持久化改造使用：它们需要「后端不可用则降级」的语义，
 * 而不是把错误抛给界面造成功能整体失效。错误通过返回值表达，
 * 调用方降级后自行决定是否提示（当前策略：静默降级，不打扰用户）。
 *
 * @param key 设置项 key
 * @param pick 从快照中取目标字段的函数
 * @returns 目标值；后端不可用时为 null
 */
export async function readSettingOrNull<T>(
  key: SettingsKey,
  pick: (snapshot: SettingsSnapshot) => T,
): Promise<T | null> {
  try {
    const snapshot = await fetchSettingsSnapshot()
    // 快照里没有该 key 对应的有效值时，同样视为不可读。
    if (!snapshot.enabled && key !== SETTINGS_KEY.updateChannel) {
      // 内存存储模式下仍能读到本次会话值，因此不在这里直接返回 null；
      // 保留读取能力，由调用方结合 enabled 决定是否需要持久化提示。
    }
    return pick(snapshot)
  } catch {
    return null
  }
}

/**
 * 尝试把单个设置项写入后端，失败时返回 false。
 *
 * 与 `updateSingleSetting` 的区别：本函数**吞掉错误**，专供降级场景使用。
 * 主题与语言切换不能因为后端不可用就整个失效，因此它们走这条路径：
 * 写失败 → 返回 false → 调用方回退到 `localStorage`。
 *
 * @param key 设置项 key
 * @param value 目标值
 * @returns 写入成功返回 true
 */
export async function tryPersistSetting(key: SettingsKey, value: unknown): Promise<boolean> {
  try {
    await updateSingleSetting(key, value)
    return true
  } catch {
    // 后端不可用：调用方降级处理，这里不抛错、不打扰用户。
    return false
  }
}

/**
 * 把主题值写入后端。
 *
 * @param theme 主题值
 * @returns 写入成功返回 true；服务不可用返回 false（调用方降级）
 */
export async function persistTheme(theme: ThemeValue): Promise<boolean> {
  return tryPersistSetting(SETTINGS_KEY.theme, theme)
}

/**
 * 把语言值写入后端。
 *
 * @param language 语言值
 * @returns 写入成功返回 true；服务不可用返回 false（调用方降级）
 */
export async function persistLanguage(language: LanguageValue): Promise<boolean> {
  return tryPersistSetting(SETTINGS_KEY.language, language)
}

/**
 * 读取已持久化的主题；不可用时返回 null。
 *
 * @returns 主题值或 null
 */
export async function loadPersistedTheme(): Promise<ThemeValue | null> {
  const value = await readSettingOrNull(SETTINGS_KEY.theme, (snapshot) => snapshot.theme)
  return isThemeValue(value) ? value : null
}

/**
 * 读取已持久化的语言；不可用时返回 null。
 *
 * @returns 语言值或 null
 */
export async function loadPersistedLanguage(): Promise<LanguageValue | null> {
  const value = await readSettingOrNull(SETTINGS_KEY.language, (snapshot) => snapshot.language)
  return isLanguageValue(value) ? value : null
}

/** 字段级校验结果。 */
export interface FieldValidationResult {
  /** 是否通过校验。 */
  valid: boolean
  /** 失败时的中文原因；通过时为空字符串。 */
  message: string
}

/**
 * 校验窗口宽度（字段级校验，供设置页就地提示）。
 *
 * @param value 用户输入值
 * @returns 校验结果
 */
export function validateWindowWidth(value: number): FieldValidationResult {
  if (!Number.isFinite(value)) {
    return { valid: false, message: '请输入数字。' }
  }
  if (Math.trunc(value) !== value) {
    return { valid: false, message: '必须是整数。' }
  }
  if (value < WINDOW_LIMITS.minWidth || value > WINDOW_LIMITS.maxWidth) {
    return {
      valid: false,
      message: `取值范围 ${WINDOW_LIMITS.minWidth}–${WINDOW_LIMITS.maxWidth}。`,
    }
  }
  return { valid: true, message: '' }
}

/**
 * 校验窗口高度（字段级校验，供设置页就地提示）。
 *
 * @param value 用户输入值
 * @returns 校验结果
 */
export function validateWindowHeight(value: number): FieldValidationResult {
  if (!Number.isFinite(value)) {
    return { valid: false, message: '请输入数字。' }
  }
  if (Math.trunc(value) !== value) {
    return { valid: false, message: '必须是整数。' }
  }
  if (value < WINDOW_LIMITS.minHeight || value > WINDOW_LIMITS.maxHeight) {
    return {
      valid: false,
      message: `取值范围 ${WINDOW_LIMITS.minHeight}–${WINDOW_LIMITS.maxHeight}。`,
    }
  }
  return { valid: true, message: '' }
}

/**
 * 供界面判断「设置服务是否可用」的辅助函数。
 *
 * @param snapshot 设置快照
 * @returns 不可用时返回稳定错误码说明，可用时返回空字符串
 */
export function describeUnavailable(snapshot: SettingsSnapshot): string {
  if (snapshot.enabled) {
    return ''
  }
  if (snapshot.initErrorCode) {
    return `${SETTINGS_UNAVAILABLE_MESSAGE}（${snapshot.initErrorCode}）`
  }
  return SETTINGS_UNAVAILABLE_MESSAGE
}

/**
 * 把任意异常归一化为 AppError（转发自 `services/errors.ts`）。
 *
 * 导出该别名是为了让 `features/settings/**` 不必直接依赖 errors 模块路径，
 * 保持「设置相关只经 services/settings.ts」的分层约定。
 *
 * @param input 原始异常
 * @returns 归一化错误
 */
export function toSettingsError(input: unknown): AppError {
  return toAppError(input)
}
