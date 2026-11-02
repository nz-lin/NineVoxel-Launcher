import type { AppError } from '../types/app'

/**
 * 错误归一化工具。
 *
 * 职责：把任意来源的异常（Wails Binding 拒绝值、`Error`、字符串、对象）转换为
 * `AppError`，并保证 `message` 不含堆栈、磁盘绝对路径、Token 或第三方原始响应体
 * （《服务接口与DTO规范》第 3 节、《错误码与日志规范》第 3 节）。
 *
 * 本文件由 P1-03 冻结，后续任务卡只读使用，不得在前端另立同名工具。
 */

/** 未知错误兜底码：域 `APP`、类别 `UNKNOWN`（见 `launcher/docs/tasks/P1/接口冻结清单.md` §8.1）。 */
export const UNKNOWN_ERROR_CODE = 'APP_UNKNOWN_001'

/** 无法从原始错误中提取有效说明时，对用户展示的安全兜底文案。 */
const FALLBACK_MESSAGE = '操作失败，请稍后重试。'

/** 单条 `message` 的最大字符数，避免把第三方原始响应整段带到界面。 */
const MAX_MESSAGE_LENGTH = 200

/** `details` 最多保留的条目数，超出部分直接丢弃。 */
const MAX_DETAIL_ENTRIES = 20

/** `Error.cause` 的最大追溯深度，避免异常引用链导致递归失控。 */
const MAX_CAUSE_DEPTH = 3

/** 错误码格式：`<DOMAIN>_<CATEGORY>_<NNN>`，必须以三位数字结尾。 */
const ERROR_CODE_PATTERN = /^[A-Z][A-Z0-9]*(?:_[A-Z0-9]+)*_\d{3}$/

/** 形如 `APP_CONFIG_001: 配置读取失败` 的原始错误文本。 */
const CODE_PREFIX_PATTERN = /^([A-Z][A-Z0-9]*(?:_[A-Z0-9]+)*_\d{3})\s*[:：]\s*([\s\S]+)$/

/** 从原始异常中提取出的候选字段；缺失字段在最终归一化时补齐。 */
interface AppErrorFields {
  /** 原始错误码，可能是非法格式，最终由调用方校验。 */
  code?: string
  /** 原始错误说明，尚未脱敏。 */
  message?: string
  /** 原始可重试标记。 */
  retryable?: boolean
  /** 原始关联 ID。 */
  correlationId?: string
  /** 原始诊断字段，尚未脱敏。 */
  details?: Record<string, unknown>
}

/**
 * 生成一个关联 ID，用于把界面错误与后端脱敏日志关联起来。
 *
 * @returns 关联 ID 字符串
 */
function createCorrelationId(): string {
  const cryptoObject: Crypto | undefined = globalThis.crypto
  if (typeof cryptoObject?.randomUUID === 'function') {
    return cryptoObject.randomUUID()
  }
  // 回退方案：部分 WebView 环境不提供 randomUUID，使用时间戳与随机数组合，保证仍可追踪。
  const randomPart = Math.random().toString(36).slice(2, 10)
  return `corr-${Date.now().toString(36)}-${randomPart}`
}

/**
 * 把原始错误文本清洗为可展示的短说明。
 *
 * 清洗内容：堆栈行、Windows 盘符与 UNC 路径、`file://` 与常见 POSIX 绝对目录、
 * 凭证类键值对、Bearer 头、JWT 形态 Token，以及超长内容。
 *
 * @param raw 原始错误文本
 * @returns 清洗后的用户可读说明；内容全部被清洗时返回空字符串
 */
function sanitizeMessage(raw: string): string {
  if (!raw) {
    return ''
  }
  // 只保留首行，丢弃 Go / JavaScript 堆栈的后续行。
  const firstLine = raw.split(/\r?\n/, 1)[0] ?? ''
  let text = firstLine
  // 隐藏 Windows 盘符路径与 UNC 共享路径。
  text = text.replace(/[A-Za-z]:[\\/][^\s"'<>|]*/g, '（已隐藏的路径）')
  text = text.replace(/\\\\[^\s"'<>|]*/g, '（已隐藏的路径）')
  // 隐藏 file:// 协议地址与常见 POSIX 绝对目录。
  text = text.replace(/file:\/\/[^\s"']*/gi, '（已隐藏的路径）')
  text = text.replace(/\/(?:home|Users|root|tmp|var|usr|opt|mnt|private)\/[^\s"']*/g, '（已隐藏的路径）')
  // 隐藏凭证类键值对、Bearer 头与 JWT 形态的 Token。
  text = text.replace(/((?:access_token|refresh_token|id_token|token|password|passwd|secret|authorization)\s*[=:]\s*)[^\s&,;"']+/gi, '$1***')
  text = text.replace(/Bearer\s+[A-Za-z0-9._~+/=-]+/gi, 'Bearer ***')
  text = text.replace(/eyJ[A-Za-z0-9._-]{10,}/g, '***')
  // 折叠空白并截断，避免第三方原始响应整段进入界面。
  text = text.replace(/\s+/g, ' ').trim()
  if (text.length > MAX_MESSAGE_LENGTH) {
    text = `${text.slice(0, MAX_MESSAGE_LENGTH)}…`
  }
  return text
}

/**
 * 判断一个值是否为普通对象（排除 null、数组与 Error）。
 *
 * @param value 待判断的值
 * @returns 是普通对象时返回 true
 */
function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value) && !(value instanceof Error)
}

/**
 * 尝试把文本解析为 JSON 对象，用于识别 Go 侧序列化后的结构化错误。
 *
 * @param text 待解析文本
 * @returns 解析出的对象；不是 JSON 对象时返回 null
 */
function parseJsonRecord(text: string): Record<string, unknown> | null {
  if (!text.startsWith('{') || !text.endsWith('}')) {
    return null
  }
  try {
    const parsed: unknown = JSON.parse(text)
    return isRecord(parsed) ? parsed : null
  } catch {
    return null
  }
}

/**
 * 清洗结构化错误中的 `details` 字段。
 *
 * 只保留字符串（脱敏后）、数字、布尔与 null；嵌套对象与数组一律丢弃，
 * 避免把第三方原始响应体或未脱敏路径带进界面。
 *
 * @param value 原始 `details` 值
 * @returns 清洗后的诊断字段；无可保留内容时返回 undefined
 */
function sanitizeDetails(value: unknown): Record<string, unknown> | undefined {
  if (!isRecord(value)) {
    return undefined
  }
  const details: Record<string, unknown> = {}
  for (const [key, entry] of Object.entries(value).slice(0, MAX_DETAIL_ENTRIES)) {
    if (typeof entry === 'string') {
      const sanitized = sanitizeMessage(entry)
      if (sanitized) {
        details[key] = sanitized
      }
      continue
    }
    if (typeof entry === 'number' || typeof entry === 'boolean' || entry === null) {
      details[key] = entry
    }
  }
  return Object.keys(details).length > 0 ? details : undefined
}

/**
 * 从结构化对象中提取候选错误字段。
 *
 * @param record 结构化错误对象
 * @returns 提取到的候选字段；一个字段都没有时返回 null
 */
function extractFieldsFromRecord(record: Record<string, unknown>): AppErrorFields | null {
  const fields: AppErrorFields = {}
  if (typeof record.code === 'string' && ERROR_CODE_PATTERN.test(record.code.trim())) {
    fields.code = record.code.trim()
  }
  if (typeof record.message === 'string' && record.message.trim()) {
    fields.message = record.message
  }
  if (typeof record.retryable === 'boolean') {
    fields.retryable = record.retryable
  }
  if (typeof record.correlationId === 'string' && record.correlationId.trim()) {
    fields.correlationId = record.correlationId.trim()
  }
  const details = sanitizeDetails(record.details)
  if (details) {
    fields.details = details
  }
  return Object.keys(fields).length > 0 ? fields : null
}

/**
 * 从文本中提取候选错误字段，支持三种形态：
 * JSON 对象、`错误码: 说明`、纯文本说明。
 *
 * @param text 原始错误文本
 * @returns 提取到的候选字段；空文本返回 null
 */
function extractFieldsFromText(text: string): AppErrorFields | null {
  const trimmed = text.trim()
  if (!trimmed) {
    return null
  }
  const parsed = parseJsonRecord(trimmed)
  if (parsed) {
    const fromRecord = extractFieldsFromRecord(parsed)
    if (fromRecord) {
      return fromRecord
    }
  }
  const prefixed = CODE_PREFIX_PATTERN.exec(trimmed)
  if (prefixed) {
    const [, code, message] = prefixed
    return { code, message }
  }
  return { message: trimmed }
}

/**
 * 递归提取任意异常的候选字段。
 *
 * @param input 原始异常
 * @param depth 当前递归深度（从 0 开始）
 * @returns 提取到的候选字段；无法识别时返回 null
 */
function extractFields(input: unknown, depth: number): AppErrorFields | null {
  if (input instanceof Error) {
    // Wails v3 的 Binding 失败以 `RuntimeError` 拒绝，Go 侧错误值可能挂在 `cause` 上。
    if (depth < MAX_CAUSE_DEPTH && input.cause !== undefined && input.cause !== null) {
      const nested = extractFields(input.cause, depth + 1)
      if (nested && (nested.code !== undefined || nested.message !== undefined)) {
        return nested
      }
    }
    return extractFieldsFromText(input.message)
  }
  if (typeof input === 'string') {
    return extractFieldsFromText(input)
  }
  if (isRecord(input)) {
    return extractFieldsFromRecord(input)
  }
  return null
}

/**
 * 把任意来源的异常归一化为可展示的 `AppError`。
 *
 * 识别顺序：`Error`（含 `cause` 追溯）→ 字符串 → 普通对象（可能来自 JSON 序列化）。
 * 无法识别时使用 `fallbackCode`（默认 `APP_UNKNOWN_001`）与安全兜底文案。
 *
 * @param input 原始异常（Binding 拒绝值、`Error`、字符串或对象）
 * @param fallbackCode 无法识别错误码时使用的兜底码，默认 `APP_UNKNOWN_001`
 * @returns 归一化后的安全错误
 */
export function toAppError(input: unknown, fallbackCode: string = UNKNOWN_ERROR_CODE): AppError {
  const normalizedFallback = ERROR_CODE_PATTERN.test(fallbackCode) ? fallbackCode : UNKNOWN_ERROR_CODE
  const fields = extractFields(input, 0)
  const code = fields?.code !== undefined && ERROR_CODE_PATTERN.test(fields.code) ? fields.code : normalizedFallback
  const sanitized = fields?.message !== undefined ? sanitizeMessage(fields.message) : ''
  const message = sanitized || FALLBACK_MESSAGE
  const retryable = fields?.retryable ?? false
  const correlationId = fields?.correlationId ?? createCorrelationId()
  const details = fields?.details
  if (details) {
    return { code, message, retryable, correlationId, details }
  }
  return { code, message, retryable, correlationId }
}
