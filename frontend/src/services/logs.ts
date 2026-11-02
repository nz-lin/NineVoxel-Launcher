import { LogService } from '../../bindings/example.com/ninevoxel/launcher'
import type { LogEntry as LogEntryFromBindings } from '../../bindings/example.com/ninevoxel/launcher'
import { toAppError } from './errors'

/**
 * LogService 的绑定封装层。
 *
 * 前端只有本文件可以直接 `import` 生成目录中的日志绑定；页面与组件必须通过
 * 这里暴露的语义化函数访问后端（AGENTS.md 8.2、P1-05 要求 E 节第 6 条）。
 *
 * `bindings/**` 由 `wails3 generate bindings` 在构建时生成（见 `launcher/.gitignore`），
 * 生成物已导出 `LogService.Query` 与 `LogService.Status`（本轮生成结果：
 * `Processed: 256 Packages, 2 Services, 3 Methods, 5 Models`）。
 */

/** 日志级别取值，与《错误码与日志规范》逐字一致。 */
export type LogLevel = 'debug' | 'info' | 'warn' | 'error'

/** 允许的日志级别清单，用于筛选栏渲染。 */
export const LOG_LEVELS: readonly LogLevel[] = ['debug', 'info', 'warn', 'error']

/**
 * 一条已脱敏的结构化日志记录。
 *
 * 字段来源：Go 侧 `logservice.go` 的 `LogEntry`，JSON 使用 lowerCamelCase。
 * 所有内容均已由后端脱敏，前端不做二次加工，也不补造字段。
 */
export interface LogEntry {
  /** 记录时间（UTC Unix 毫秒），展示时由前端转换为本地时区。 */
  timestampMs: number

  /** 日志级别。 */
  level: LogLevel

  /** 产生日志的组件名。 */
  component: string

  /** 结构化事件名；无事件时为空字符串。 */
  event: string

  /** 关联一次用户操作的追踪 ID；无关联时为空字符串。 */
  correlationId: string

  /** 一次操作或长任务的 ID；无关联时为空字符串。 */
  operationId: string

  /** 稳定错误码；成功路径为空字符串。 */
  errorCode: string

  /** 已脱敏的可读说明。 */
  message: string

  /** 扩展信息；后端未提供时为 undefined。 */
  attributes?: Record<string, string>
}

/**
 * 日志子系统状态。
 *
 * 字段来源：Go 侧 `logservice.go` 的 `LogStatus`。
 * 注意：结构中不含任何路径字段，前端也不得尝试还原日志目录位置。
 */
export interface LogStatus {
  /** 日志目录是否可写；false 时本次运行仅输出到控制台。 */
  enabled: boolean

  /** 目录来源类别：userConfig（macOS Application Support）/ userCache（Windows %LocalAppData%）/ exeDir / unavailable。 */
  directoryKind: string

  /** 结构化日志文件数量。 */
  fileCount: number

  /** 最新结构化日志文件名；无文件时为空字符串。 */
  latestFileName: string

  /** 结构化日志文件占用的总字节数。 */
  totalBytes: number
}

/** 日志查询条件，字段与接口冻结清单 §8.6 的 LogQuery 一致。 */
export interface LogQuery {
  /** 级别过滤；空字符串表示全部级别。 */
  level: string

  /** 组件过滤；空字符串表示全部组件。 */
  component: string

  /** 最大返回条数；缺省 200，上限由后端裁剪到 1000。 */
  limit: number

  /** 时间游标（UTC Unix 毫秒）；0 表示不限。 */
  beforeMs: number
}

/** 日志查询结果，字段与接口冻结清单 §8.6 的 LogQueryResult 一致。 */
export interface LogQueryResult {
  /** 本页记录，按时间倒序（最新在前）。 */
  entries: LogEntry[]

  /** 继续加载更早日志时使用的游标；0 表示没有更多记录。 */
  nextBeforeMs: number
}

/** 单页请求条数：与后端缺省值保持一致，便于对账。 */
export const DEFAULT_PAGE_SIZE = 200

/**
 * 读取日志子系统状态。
 *
 * @returns 日志状态快照
 * @throws AppError 日志服务不可用或 Go 侧返回错误时的归一化错误
 */
export async function fetchLogStatus(): Promise<LogStatus> {
  try {
    return await LogService.Status()
  } catch (error) {
    throw toAppError(error)
  }
}

/**
 * 按条件查询日志。
 *
 * 参数原样透传给 Go：级别与组件过滤、时间游标与条数上限都由后端裁决，
 * 前端不做任何本地过滤，避免「界面显示有日志但后端其实没有」的假象。
 *
 * @param query 查询条件
 * @returns 查询结果与下一页游标
 * @throws AppError 日志服务不可用或 Go 侧返回错误时的归一化错误
 */
export async function queryLogEntries(query: LogQuery): Promise<LogQueryResult> {
  try {
    const result = await LogService.Query(query)
    return {
      // Go 侧切片可能为 nil（生成物类型为 `LogEntry[] | null`），
      // 这里统一归一化为空数组，页面无需再处理 null。
      entries: (result.entries ?? []).map(normalizeEntry),
      nextBeforeMs: result.nextBeforeMs,
    }
  } catch (error) {
    throw toAppError(error)
  }
}

/**
 * 把生成物中的日志记录归一化为本模块的 `LogEntry`。
 *
 * 归一化只做类型收敛，不修改内容：级别收敛为四个合法值之一（非法值按 info），
 * attributes 的 null 收敛为 undefined，其余字段原样保留。
 *
 * @param raw 生成物中的原始记录
 * @returns 归一化后的记录
 */
function normalizeEntry(raw: LogEntryFromBindings): LogEntry {
  const attributes: Record<string, string> = {}
  if (raw.attributes) {
    for (const [key, value] of Object.entries(raw.attributes)) {
      // 生成的映射类型值可能为 undefined（`{ [k in string]?: string }`），跳过缺失项。
      if (typeof value === 'string') {
        attributes[key] = value
      }
    }
  }

  const entry: LogEntry = {
    timestampMs: raw.timestampMs,
    level: normalizeLevel(raw.level),
    component: raw.component,
    event: raw.event,
    correlationId: raw.correlationId,
    operationId: raw.operationId,
    errorCode: raw.errorCode,
    message: raw.message,
  }
  // 只有确实存在属性时才带该字段，避免界面出现「展开属性」却无内容。
  if (Object.keys(attributes).length > 0) {
    entry.attributes = attributes
  }
  return entry
}

/**
 * 把任意字符串收敛为合法日志级别。
 *
 * @param value 原始级别字符串
 * @returns 四个合法级别之一；无法识别时回退为 info
 */
function normalizeLevel(value: string): LogLevel {
  switch (value) {
    case 'debug':
    case 'info':
    case 'warn':
    case 'error':
      return value
    default:
      return 'info'
  }
}
