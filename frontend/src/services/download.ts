import { toAppError } from './errors'
import type {
  DownloadTaskError,
  DownloadTaskFilter,
  DownloadTaskList,
  DownloadTaskView,
  StartDownloadPlanRequest,
  TaskAccepted,
  TaskIDRequest,
} from '../types/download'
import { DOWNLOAD_MAX_LIST_LIMIT } from '../types/download'
// P3-11 追加：真实绑定装配与下载任务事件订阅所需的 import。
// 既有注入式接口与 `createDownloadService` 保持不变，本卡只在文件末尾追加装配与订阅实现。
import { Events } from '@wailsio/runtime'
import { DownloadService } from '../../bindings/example.com/ninevoxel/launcher/internal/download'
import type { DownloadTaskChangedPayload } from '../types/download'
import { DOWNLOAD_TASK_CHANGED_EVENT } from '../types/download'
import type { EventEnvelopeBody } from '../types/events'
import type { Unsubscribe } from './events'

/**
 * 下载队列的后端绑定接口。
 *
 * 之所以用接口注入而不是直接 import bindings：
 * `wails3 generate bindings` 由 Leader 统一串行执行，
 * 本卡先落地类型与服务，绑定模块生成后由装配层把
 * `DownloadService.StartDownloadPlan` 等函数按此签名传入即可，
 * 不会在绑定尚未生成时破坏前端类型检查。
 */
export interface DownloadBindingsApi {
  /** 对应 DownloadService.StartDownloadPlan。 */
  StartDownloadPlan: (request: StartDownloadPlanRequest) => Promise<unknown>

  /** 对应 DownloadService.PauseDownloadTask。 */
  PauseDownloadTask: (request: TaskIDRequest) => Promise<unknown>

  /** 对应 DownloadService.ResumeDownloadTask。 */
  ResumeDownloadTask: (request: TaskIDRequest) => Promise<unknown>

  /** 对应 DownloadService.CancelDownloadTask。 */
  CancelDownloadTask: (request: TaskIDRequest) => Promise<unknown>

  /** 对应 DownloadService.GetDownloadTask。 */
  GetDownloadTask: (request: TaskIDRequest) => Promise<unknown>

  /** 对应 DownloadService.ListDownloadTasks。 */
  ListDownloadTasks: (filter: DownloadTaskFilter) => Promise<unknown>
}

/** 下载队列服务门面。 */
export interface DownloadService {
  /** 按安装计划入队（命中在途同目标时返回已有任务并标记 duplicate）。 */
  start: (request: StartDownloadPlanRequest) => Promise<TaskAccepted>

  /** 暂停任务：仅 downloading 可暂停，已下载字节保留。 */
  pause: (taskId: string) => Promise<DownloadTaskView>

  /** 恢复任务：paused 回到 queued 重新排队。 */
  resume: (taskId: string) => Promise<DownloadTaskView>

  /** 取消任务：进入终态且不可恢复；提交阶段会失败。 */
  cancel: (taskId: string) => Promise<DownloadTaskView>

  /** 查询单个任务。 */
  get: (taskId: string) => Promise<DownloadTaskView>

  /** 列出任务（分页，limit 会被收敛到 100）。 */
  list: (filter?: Partial<DownloadTaskFilter>) => Promise<DownloadTaskList>
}

/** 数值兜底：非有限数一律按 0 处理，避免 undefined 渗入视图。 */
function toNumber(value: unknown): number {
  return typeof value === 'number' && Number.isFinite(value) ? value : 0
}

/** 可空数值兜底：undefined/null 统一成 null。 */
function toOptionalNumber(value: unknown): number | null {
  return typeof value === 'number' && Number.isFinite(value) ? value : null
}

/** 字符串兜底：非字符串一律按空串处理。 */
function toText(value: unknown): string {
  return typeof value === 'string' ? value : ''
}

/**
 * 归一化任务错误摘要。
 *
 * @param raw 后端返回值中的 error 字段
 * @returns 归一化后的错误；结构不可用时返回 null
 */
export function normalizeDownloadTaskError(raw: unknown): DownloadTaskError | null {
  if (raw === null || typeof raw !== 'object') {
    return null
  }
  const record = raw as Record<string, unknown>
  return {
    code: toText(record.code),
    message: toText(record.message),
    retryable: record.retryable === true,
  }
}

/**
 * 归一化单个任务视图。
 *
 * @param raw 后端返回的任务对象
 * @returns 归一化后的任务视图
 */
export function normalizeDownloadTaskView(raw: unknown): DownloadTaskView {
  const record = (raw ?? {}) as Record<string, unknown>
  return {
    id: toText(record.id),
    title: toText(record.title),
    state: toText(record.state),
    downloadedBytes: toNumber(record.downloadedBytes),
    totalBytes: toOptionalNumber(record.totalBytes),
    speedBytesPerSecond: toOptionalNumber(record.speedBytesPerSecond),
    progress: toOptionalNumber(record.progress),
    error: normalizeDownloadTaskError(record.error),
    artifactCount: toNumber(record.artifactCount),
    completedCount: toNumber(record.completedCount),
    createdAtMs: toNumber(record.createdAtMs),
    updatedAtMs: toNumber(record.updatedAtMs),
    completedAtMs: toNumber(record.completedAtMs),
  }
}

/**
 * 归一化任务列表。
 *
 * @param raw 后端返回的列表对象
 * @returns 归一化后的列表（tasks 恒为数组）
 */
export function normalizeDownloadTaskList(raw: unknown): DownloadTaskList {
  const record = (raw ?? {}) as Record<string, unknown>
  const rawTasks = Array.isArray(record.tasks) ? record.tasks : []
  return {
    tasks: rawTasks.map(normalizeDownloadTaskView),
    total: toNumber(record.total),
  }
}

/**
 * 归一化入队结果。
 *
 * @param raw 后端返回的 TaskAccepted
 * @returns 归一化后的入队结果
 */
export function normalizeTaskAccepted(raw: unknown): TaskAccepted {
  const record = (raw ?? {}) as Record<string, unknown>
  return {
    taskId: toText(record.taskId),
    parentTaskId: toText(record.parentTaskId),
    state: toText(record.state),
    duplicate: record.duplicate === true,
    createdAtMs: toNumber(record.createdAtMs),
  }
}

/**
 * 补全列表过滤条件：缺省字段按「不过滤、第一页、默认上限」处理。
 *
 * @param filter 调用方给出的部分条件
 * @returns 完整的过滤条件
 */
export function normalizeDownloadTaskFilter(filter?: Partial<DownloadTaskFilter>): DownloadTaskFilter {
  const states = Array.isArray(filter?.states) ? filter?.states.filter((item) => typeof item === 'string') : []
  const limit = filter?.limit ?? DOWNLOAD_MAX_LIST_LIMIT
  return {
    states: states ?? [],
    versionId: filter?.versionId ?? '',
    parentsOnly: filter?.parentsOnly === true,
    offset: Math.max(0, filter?.offset ?? 0),
    limit: limit > 0 ? Math.min(limit, DOWNLOAD_MAX_LIST_LIMIT) : DOWNLOAD_MAX_LIST_LIMIT,
  }
}

/**
 * 用后端绑定接口创建下载队列服务门面。
 *
 * 所有调用失败都会转成统一的 AppError，界面按
 * `<错误码>: <说明>` 展示，不需要另写一套错误映射。
 *
 * @param api 由装配层提供的绑定函数集合
 * @returns 下载队列服务门面
 */
export function createDownloadService(api: DownloadBindingsApi): DownloadService {
  /** 统一包装：调用绑定 + 归一化，异常转 AppError。 */
  const call = async <T>(
    invoke: () => Promise<unknown>,
    normalize: (raw: unknown) => T,
  ): Promise<T> => {
    try {
      const raw = await invoke()
      return normalize(raw)
    } catch (error) {
      throw toAppError(error)
    }
  }

  return {
    /** 按安装计划入队。 */
    async start(request: StartDownloadPlanRequest): Promise<TaskAccepted> {
      return call(() => api.StartDownloadPlan(request), normalizeTaskAccepted)
    },

    /** 暂停任务。 */
    async pause(taskId: string): Promise<DownloadTaskView> {
      return call(() => api.PauseDownloadTask({ taskId }), normalizeDownloadTaskView)
    },

    /** 恢复任务。 */
    async resume(taskId: string): Promise<DownloadTaskView> {
      return call(() => api.ResumeDownloadTask({ taskId }), normalizeDownloadTaskView)
    },

    /** 取消任务。 */
    async cancel(taskId: string): Promise<DownloadTaskView> {
      return call(() => api.CancelDownloadTask({ taskId }), normalizeDownloadTaskView)
    },

    /** 查询单个任务。 */
    async get(taskId: string): Promise<DownloadTaskView> {
      return call(() => api.GetDownloadTask({ taskId }), normalizeDownloadTaskView)
    },

    /** 列出任务。 */
    async list(filter?: Partial<DownloadTaskFilter>): Promise<DownloadTaskList> {
      const normalized = normalizeDownloadTaskFilter(filter)
      return call(() => api.ListDownloadTasks(normalized), normalizeDownloadTaskList)
    },
  }
}

// ===== 以下为 P3-11 追加：真实绑定装配与下载任务事件订阅 =====

/**
 * 用生成的绑定函数装配注入式接口。
 *
 * 之所以在这里做一次字段级搬运：前端的 `DownloadTaskFilter` 与生成的
 * `models.DownloadTaskFilter` 在可空性上不同（生成物是 `string[] | null`），
 * 直接强转会把空数组与「不过滤」混为一谈，因此显式转换。
 */
const downloadBindingsApi: DownloadBindingsApi = {
  /** 对应 DownloadService.StartDownloadPlan。 */
  StartDownloadPlan: (request: StartDownloadPlanRequest) =>
    DownloadService.StartDownloadPlan({
      versionId: request.versionId,
      planHash: request.planHash,
      targetKind: request.targetKind,
    }),

  /** 对应 DownloadService.PauseDownloadTask。 */
  PauseDownloadTask: (request: TaskIDRequest) =>
    DownloadService.PauseDownloadTask({ taskId: request.taskId }),

  /** 对应 DownloadService.ResumeDownloadTask。 */
  ResumeDownloadTask: (request: TaskIDRequest) =>
    DownloadService.ResumeDownloadTask({ taskId: request.taskId }),

  /** 对应 DownloadService.CancelDownloadTask。 */
  CancelDownloadTask: (request: TaskIDRequest) =>
    DownloadService.CancelDownloadTask({ taskId: request.taskId }),

  /** 对应 DownloadService.GetDownloadTask。 */
  GetDownloadTask: (request: TaskIDRequest) => DownloadService.GetDownloadTask({ taskId: request.taskId }),

  /** 对应 DownloadService.ListDownloadTasks。 */
  ListDownloadTasks: (filter: DownloadTaskFilter) =>
    DownloadService.ListDownloadTasks({
      // 空数组表示「不限状态」，生成物用 null 表达同一语义。
      states: filter.states.length > 0 ? filter.states : null,
      parentsOnly: filter.parentsOnly,
      versionId: filter.versionId,
      offset: filter.offset,
      limit: filter.limit,
    }),
}

/**
 * 页面使用的下载队列服务实例（已接入真实绑定）。
 *
 * 组件与 composable 只 import 本常量，不直接触碰 `bindings/**`（AGENTS.md 8.2）。
 */
export const downloadService: DownloadService = createDownloadService(downloadBindingsApi)

/**
 * 把事件外壳收敛为 `EventEnvelopeBody`，丢弃 Wails 运行时附加的字段。
 *
 * @param source 运行时给出的事件外壳
 * @returns 只含冻结字段的事件对象
 */
function toEventRecord<T>(source: EventEnvelopeBody<T>): EventEnvelopeBody<T> {
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
 * 归一化 `download.task.changed.v1` 的载荷。
 *
 * 载荷刻意只有四个字段：前端收到后按 `taskId` 再调 `GetDownloadTask`
 * 取完整视图（含字节数、速度与错误详情）。
 *
 * @param raw 事件携带的原始载荷
 * @returns 归一化后的载荷
 */
function normalizeDownloadTaskChangedPayload(raw: unknown): DownloadTaskChangedPayload {
  const record = (raw ?? {}) as Record<string, unknown>
  const payload: DownloadTaskChangedPayload = {
    taskId: toText(record.taskId),
    state: toText(record.state),
    progress: toNumber(record.progress),
  }
  const errorCode = toText(record.errorCode)
  if (errorCode !== '') {
    payload.errorCode = errorCode
  }
  return payload
}

/**
 * 订阅 `download.task.changed.v1`。
 *
 * 事件只用于推动界面刷新：载荷可能乱序、重复或缺失，**不得**当作状态真相。
 * 调用方收到事件后应按 `taskId` 重新调用 `GetDownloadTask` 以快照覆盖本地状态。
 *
 * @param handler 事件处理函数
 * @returns 幂等的解绑函数
 */
export function subscribeDownloadTaskChanged(
  handler: (event: EventEnvelopeBody<DownloadTaskChangedPayload>) => void,
): Unsubscribe {
  let active = true

  const release = Events.On(DOWNLOAD_TASK_CHANGED_EVENT, (event) => {
    if (!active) {
      return
    }
    // 该事件尚未在生成的 `CustomEvents` 声明中登记，运行时数据按 unknown 收敛。
    const envelope = event?.data as EventEnvelopeBody<unknown> | undefined
    if (!envelope || envelope.payload === undefined || envelope.payload === null) {
      // 载荷缺失时忽略：事件可能重复或损坏，界面不应因此报错。
      return
    }
    handler(
      toEventRecord({
        ...envelope,
        payload: normalizeDownloadTaskChangedPayload(envelope.payload),
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
