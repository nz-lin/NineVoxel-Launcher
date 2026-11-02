/**
 * 前端共享类型。
 *
 * 字段与《服务接口与DTO规范》《事件与状态机规范》原文逐字对齐，属于冻结契约：
 * 后续任务卡只读使用，不得在前端另立同名结构。
 * 本文件只定义类型，不含任何运行时代码。
 */

/**
 * Binding 调用失败后供 UI 使用的安全错误结构。
 *
 * 来源：`docs/NineVoxel-Launcher-开发方案/docs/architecture/服务接口与DTO规范.md` 第 3 节。
 * 约束：`message` 面向用户，不得暴露堆栈、磁盘绝对路径、Token 或第三方服务原始响应；
 * 详细技术原因写入脱敏日志，并用 `correlationId` 关联。
 */
export interface AppError {
  /** 稳定错误码，格式为 `<DOMAIN>_<CATEGORY>_<NNN>`，例如 `APP_CONFIG_001`。 */
  code: string

  /** 面向用户的脱敏说明。 */
  message: string

  /** 同一操作重试是否可能成功。 */
  retryable: boolean

  /** 关联一次用户操作的追踪 ID，用于与脱敏日志对照。 */
  correlationId: string

  /** 可选的白名单诊断字段，不得包含秘密或未脱敏路径。 */
  details?: Record<string, unknown>
}

/**
 * Go 推送到 UI 的统一事件外壳。
 *
 * 来源：`docs/NineVoxel-Launcher-开发方案/docs/architecture/事件与状态机规范.md` 第 2 节。
 * 事件可能丢失或重复，UI 必须能用 Binding 查询快照恢复状态。
 */
export interface AppEvent<T> {
  /** 事件唯一标识。 */
  eventId: string

  /** 事件名，格式为 `domain.resource.action.v1`。 */
  eventName: string

  /** 事件负载结构版本，P1 固定为 1。 */
  schemaVersion: 1

  /** 事件发生时间，UTC Unix 毫秒。 */
  occurredAt: number

  /** 关联一次用户操作的追踪 ID。 */
  correlationId: string

  /** 事件负载，不得包含 Token、密码、完整私有 URL 查询参数或未脱敏绝对路径。 */
  payload: T
}

/**
 * 启动器的非敏感应用信息，供首页与诊断区域展示。
 *
 * 来源：`launcher/docs/tasks/P1/接口冻结清单.md` §8.4（P0 的 `RuntimeInfo` 已废弃，
 * 其三个字段并入本结构）。
 */
export interface AppInfo {
  /** 启动器版本号。 */
  appVersion: string

  /** 构建应用所用的 Go 运行时版本。 */
  goVersion: string

  /** 构建目标操作系统（Go 的 GOOS）。 */
  goos: string

  /** 构建目标处理器架构（Go 的 GOARCH）。 */
  goarch: string

  /** 数据目录可用状态，用于诊断数据目录是否可读写。 */
  dataDirectoryStatus: string
}
