import { Events } from '@wailsio/runtime'
import type { AppLifecycleReadyPayload, EventEnvelopeBody, KnownEventName, SettingsChangedPayload } from '../types/events'
import { EVENT_APP_LIFECYCLE_READY, EVENT_SETTINGS_CHANGED } from '../types/events'

/**
 * 事件订阅封装层。
 *
 * 本文件是**唯一**允许 import `@wailsio/runtime` 事件 API 的业务层：
 * 页面与组件必须通过这里订阅（P1-06 要求 E.4），这样解绑逻辑只有一处实现，
 * 不会出现某个组件忘了退订而泄漏回调的情况。
 *
 * 设计前提（《事件与状态机规范》）：**事件可能丢失或重复**。
 * 因此本层只负责「把事件送到回调」，不承诺一定送达、也不去重；
 * 需要确定状态的场景一律走 Binding 查询快照（例如 `fetchAppInfo()`）。
 *
 * 类型来源：`wails3 generate bindings` 会在 `bindings/events/index.ts` 与
 * `eventdata.d.ts` 中生成 `CustomEvents` 声明，因此 `Events.On` 的回调参数
 * 已被强类型约束为对应事件的 `AppEvent<T>`；本层只需把外壳解包成 `payload`。
 */

/** 订阅函数返回的解绑函数。 */
export type Unsubscribe = () => void

/** 订阅回调：只关心事件外壳主体，不暴露 Wails 的传输细节。 */
export type EventRecordHandler<T> = (event: EventEnvelopeBody<T>) => void

/**
 * 把 Wails 事件对象转换为本模块的记录结构。
 *
 * Wails 的 `WailsEvent<E>` 在结构上与 `EventEnvelopeBody<T>` 一致（含 eventId、
 * eventName、schemaVersion、occurredAt、correlationId、payload），
 * 但 `@wailsio/runtime` 的声明使用 `readonly` 与类实例形式，
 * 因此这里做一次显式字段拷贝，避免类型依赖穿透到业务层。
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
 * 订阅 `app.lifecycle.ready.v1`。
 *
 * 该事件在应用就绪时由后端发射一次；事件可能丢失，界面的权威状态仍应取自
 * `AppService.Snapshot()`（经 `services/app.ts` 的 `fetchAppInfo()`）。
 *
 * @param handler 事件处理函数
 * @returns 幂等的解绑函数
 */
export function subscribeAppLifecycleReady(handler: EventRecordHandler<AppLifecycleReadyPayload>): Unsubscribe {
  let active = true

  const release = Events.On(EVENT_APP_LIFECYCLE_READY, (event) => {
    if (!active) {
      return
    }
    const payload = event?.data?.payload
    if (!payload) {
      // 载荷缺失时忽略：事件可能重复或损坏，界面不应因此报错。
      return
    }
    handler(toEventRecord({ ...event.data, payload }))
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
 * 订阅 `settings.changed.v1`。
 *
 * 该事件由 P1-04 在设置写入成功后发射；本卡只提供订阅能力，不产生该事件。
 *
 * @param handler 事件处理函数
 * @returns 幂等的解绑函数
 */
export function subscribeSettingsChanged(handler: EventRecordHandler<SettingsChangedPayload>): Unsubscribe {
  let active = true

  const release = Events.On(EVENT_SETTINGS_CHANGED, (event) => {
    if (!active) {
      return
    }
    const payload = event?.data?.payload
    if (!payload) {
      return
    }
    // Go 侧切片可能为 nil（生成物类型为 `string[] | null`），
    // 这里归一化为空数组，避免界面需要处理 null。
    handler(
      toEventRecord({
        ...event.data,
        payload: { keys: payload.keys ?? [], updatedAt: payload.updatedAt },
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
 * 判断事件名是否为已定义的可订阅事件。
 *
 * 供需要按名字分派订阅的调用方使用（例如调试时枚举）。
 *
 * @param name 待判断的事件名
 * @returns 属于已知事件时返回 true
 */
export function isKnownEventName(name: string): name is KnownEventName {
  return name === EVENT_APP_LIFECYCLE_READY || name === EVENT_SETTINGS_CHANGED
}
