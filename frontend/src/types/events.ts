/**
 * 前端事件订阅的类型定义。
 *
 * 与 Go 侧 `launcher/internal/events/envelope.go` 的 `AppEvent[T]` 逐字段一致，
 * 与 `launcher/frontend/src/types/app.ts` 中 P1-03 冻结的 `AppEvent<T>` 保持同形。
 * 本文件只定义事件相关载荷，不重复定义 `AppEvent` 本身（P1-03 已冻结）。
 */

/** `app.lifecycle.ready.v1` 的载荷，与 Go 侧 `AppLifecycleReadyPayload` 一致。 */
export interface AppLifecycleReadyPayload {
  /** 启动器版本号。 */
  appVersion: string

  /** 数据目录可用状态：`ok` / `read-only` / `unavailable`。 */
  dataDirectoryStatus: string
}

/** `settings.changed.v1` 的载荷，与 Go 侧 `SettingsChangedPayload` 一致。 */
export interface SettingsChangedPayload {
  /** 本次被修改的设置项 key 列表（白名单内的 key，不含值）。 */
  keys: string[]

  /** 写入完成时间，UTC Unix 毫秒。 */
  updatedAt: number
}

/**
 * 前端事件名常量。
 *
 * 必须与 Go 侧 `internal/events` 包中的同名常量保持一致；
 * 订阅与发射两侧都只使用常量，禁止散落字符串字面量，
 * 否则改名会在两端静默漂移而无法在编译期发现。
 */
export const EVENT_APP_LIFECYCLE_READY = 'app.lifecycle.ready.v1'
export const EVENT_SETTINGS_CHANGED = 'settings.changed.v1'

/** 允许订阅的事件名集合，供类型约束与运行时校验复用。 */
export const KNOWN_EVENT_NAMES = [EVENT_APP_LIFECYCLE_READY, EVENT_SETTINGS_CHANGED] as const

/** 已知事件名的联合类型。 */
export type KnownEventName = (typeof KNOWN_EVENT_NAMES)[number]

/**
 * 数据目录状态的可枚举取值，与 Go 侧常量一一对应。
 *
 * 这些值是状态摘要而不是路径：界面只能据此展示「可用/只读/不可用」，
 * 不得尝试还原或拼接任何目录路径。
 */
export const DATA_DIRECTORY_STATUS_OK = 'ok'
export const DATA_DIRECTORY_STATUS_READ_ONLY = 'read-only'
export const DATA_DIRECTORY_STATUS_UNAVAILABLE = 'unavailable'

/** 数据目录状态联合类型。 */
export type DataDirectoryStatus =
  | typeof DATA_DIRECTORY_STATUS_OK
  | typeof DATA_DIRECTORY_STATUS_READ_ONLY
  | typeof DATA_DIRECTORY_STATUS_UNAVAILABLE

/**
 * 把任意字符串收敛为数据目录状态联合类型。
 *
 * 后端理论上只会返回三个合法值，但事件载荷可能来自旧版本或被人工修改的日志，
 * 因此界面侧仍需要一个安全的收敛函数，避免未知值让展示逻辑落空。
 *
 * @param value 原始状态字符串
 * @returns 合法状态值；无法识别时按 `unavailable` 处理
 */
export function normalizeDataDirectoryStatus(value: string): DataDirectoryStatus {
  switch (value) {
    case DATA_DIRECTORY_STATUS_OK:
      return DATA_DIRECTORY_STATUS_OK
    case DATA_DIRECTORY_STATUS_READ_ONLY:
      return DATA_DIRECTORY_STATUS_READ_ONLY
    default:
      return DATA_DIRECTORY_STATUS_UNAVAILABLE
  }
}

/** Wails 事件回调收到的参数结构（载荷包在 `data` 字段内）。 */
export interface EventEnvelope<T> {
  /** 后端推送的事件外壳，字段与 `AppEvent<T>` 一致。 */
  data: EventEnvelopeBody<T>
}

/** Wails 事件外壳的主体部分，对应 Go 侧 `AppEvent[T]`。 */
export interface EventEnvelopeBody<T> {
  /** 事件唯一标识。 */
  eventId: string

  /** 事件名，格式为 `domain.resource.action.v1`。 */
  eventName: string

  /** 负载结构版本，P1 固定为 1。 */
  schemaVersion: number

  /** 事件发生时间，UTC Unix 毫秒。 */
  occurredAt: number

  /** 关联一次用户操作的追踪 ID。 */
  correlationId: string

  /** 事件负载。 */
  payload: T
}
