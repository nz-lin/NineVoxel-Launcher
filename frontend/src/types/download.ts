/**
 * 下载队列与调度的前端类型定义（P3-04）。
 *
 * 本文件只描述 DTO 与常量，不含任何调用逻辑：
 * 状态机、并发闸门与重试退避全部在 Go 后端实现，
 * 前端只按后端返回的 `state` 展示，不自行推导合法转移。
 */

/** 下载任务的 9 个状态，顺序与后端冻结的状态机一致。 */
export const DOWNLOAD_TASK_STATES = [
  'queued',
  'resolving',
  'downloading',
  'paused',
  'verifying',
  'committing',
  'completed',
  'failed',
  'cancelled',
] as const

/** 下载任务状态。 */
export type DownloadTaskState = (typeof DOWNLOAD_TASK_STATES)[number]

/** 终态集合：进入这些状态后不可再恢复。 */
export const DOWNLOAD_TERMINAL_STATES: readonly DownloadTaskState[] = ['completed', 'failed', 'cancelled']

/**
 * 判断状态是否为终态。
 *
 * 终态任务不能暂停、恢复或取消，后端会返回 DOWNLOAD_STATE_001。
 *
 * @param state 下载任务状态
 * @returns 是否为终态
 */
export function isTerminalDownloadState(state: DownloadTaskState): boolean {
  return DOWNLOAD_TERMINAL_STATES.includes(state)
}

/** 下载队列的错误码常量（与后端 errors.go 逐字一致）。 */
export const DOWNLOAD_ERROR_CODES = {
  /** 网络错误：可重试。 */
  network: 'DOWNLOAD_NETWORK_001',
  /** 断点续传失败：需重新开始。 */
  resume: 'DOWNLOAD_RESUME_001',
  /** 文件大小或哈希不匹配。 */
  size: 'DOWNLOAD_SIZE_001',
  /** 状态机不允许该操作。 */
  state: 'DOWNLOAD_STATE_001',
  /** 提交阶段不可取消。 */
  cancel: 'DOWNLOAD_CANCEL_001',
  /** 下载计划不可用或校验失败。 */
  plan: 'ARTIFACT_PLAN_001',
  /** 数据库无法打开。 */
  databaseOpen: 'DB_OPEN_001',
  /** 数据库迁移失败。 */
  databaseMigration: 'DB_MIGRATION_001',
} as const

/** 下载队列错误码。 */
export type DownloadErrorCode = (typeof DOWNLOAD_ERROR_CODES)[keyof typeof DOWNLOAD_ERROR_CODES]

/** 任务状态变化事件名。 */
export const DOWNLOAD_TASK_CHANGED_EVENT = 'download.task.changed.v1'

/**
 * 任务状态变化事件的载荷。
 *
 * 事件只携带任务标识、状态、进度与错误码：
 * 不含 URL、路径与凭证，可以直接展示。
 */
export interface DownloadTaskChangedPayload {
  /** 任务标识。 */
  taskId: string

  /** 变化后的状态（后端先落状态再发事件）。 */
  state: string

  /** 进度，0~1；总量未知时为 0。 */
  progress: number

  /** 错误码，仅在任务失败时出现。 */
  errorCode?: string
}

/** 任务错误摘要。 */
export interface DownloadTaskError {
  /** 稳定错误码。 */
  code: string

  /** 面向用户的中文说明（后端已做脱敏）。 */
  message: string

  /** 是否可重试。 */
  retryable: boolean
}

/**
 * 下载任务的对外视图。
 *
 * 不含源地址与落盘路径：前端只展示标题、状态与进度。
 */
export interface DownloadTaskView {
  /** 任务标识。 */
  id: string

  /** 展示标题（版本 id + 任务类型）。 */
  title: string

  /** 当前状态。 */
  state: string

  /** 已下载字节数。 */
  downloadedBytes: number

  /** 预期总字节数；未知时为 null。 */
  totalBytes: number | null

  /** 瞬时速度（字节/秒）；未知时为 null。 */
  speedBytesPerSecond: number | null

  /** 进度 0~1，由后端计算；未知时为 null。 */
  progress: number | null

  /** 失败信息，仅在失败时出现。 */
  error: DownloadTaskError | null

  /** 制品总数（父任务为子任务数，子任务为 1）。 */
  artifactCount: number

  /** 已完成数（父任务为已完成子任务数）。 */
  completedCount: number

  /** 创建时间，UTC Unix 毫秒。 */
  createdAtMs: number

  /** 最近更新时间，UTC Unix 毫秒。 */
  updatedAtMs: number

  /** 完成时间，UTC Unix 毫秒；未完成时为 0。 */
  completedAtMs: number
}

/** 任务分页列表。 */
export interface DownloadTaskList {
  /** 当前页任务。 */
  tasks: DownloadTaskView[]

  /** 过滤后的总数。 */
  total: number
}

/** 入队结果。 */
export interface TaskAccepted {
  /** 任务标识（去重命中时为已有任务）。 */
  taskId: string

  /** 父任务标识；普通任务为空字符串。 */
  parentTaskId: string

  /** 入队后的状态。 */
  state: string

  /** 是否命中「同目标 + 同哈希」的去重。 */
  duplicate: boolean

  /** 创建时间，UTC Unix 毫秒。 */
  createdAtMs: number
}

/** 计划入队请求。 */
export interface StartDownloadPlanRequest {
  /** 版本 id。 */
  versionId: string

  /** 安装计划哈希（由计划构建阶段给出）。 */
  planHash: string

  /** 目标位置：shared（共享缓存）或 version（版本私有目录）。 */
  targetKind: string
}

/** 单任务操作请求。 */
export interface TaskIDRequest {
  /** 任务标识。 */
  taskId: string
}

/** 任务列表过滤条件。 */
export interface DownloadTaskFilter {
  /** 状态白名单；空数组表示不过滤。 */
  states: string[]

  /** 只列出该版本的任务；空字符串表示不过滤。 */
  versionId: string

  /** 分页偏移。 */
  offset: number

  /** 分页大小，上限 100。 */
  limit: number
}

/** 列表单页上限（与后端 maxListLimit 一致）。 */
export const DOWNLOAD_MAX_LIST_LIMIT = 100
