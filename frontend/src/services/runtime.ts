/**
 * RuntimeService 的前端门面。
 *
 * 与 `services/download.ts` 一致，这里用「绑定接口注入」而不是直接 import
 * `bindings/.../internal/runtime`：绑定文件由 Leader 统一串行执行
 * `wails3 generate bindings` 生成，注入写法在绑定尚未生成时也不会破坏前端类型检查。
 */
import { toAppError } from './errors'
import { RuntimeService } from '../../bindings/example.com/ninevoxel/launcher/internal/runtime'
import { subscribeDownloadTaskChanged } from './download'
import type { Unsubscribe } from './events'
import type { DownloadTaskChangedPayload } from '../types/download'
import type {
  DetectRuntimeRequest,
  RuntimeInstallAccepted,
  RuntimeInstallPhase,
  RuntimeInstallProgress,
  RuntimeInstallRequest,
  RuntimeList,
  RuntimeLtsVersions,
  RuntimeProbeResult,
  RuntimeProbeWarning,
  RuntimeRequirement,
  RuntimeSelectRequest,
  RuntimeSourceInfo,
  RuntimeSourceInstallRequest,
  RuntimeSummary,
  RuntimeUpdateInfo,
} from '../types/runtime'
import {
  RUNTIME_ERROR_CODE,
  RUNTIME_INSTALL_STATE,
  RUNTIME_INSTALL_TASK_PREFIX,
} from '../types/runtime'

/** 后端绑定函数集合，与 RuntimeService 的八个方法一一对应。 */
export interface RuntimeBindingsApi {
  /** 对应 RuntimeService.DetectRuntimes。 */
  DetectRuntimes: (request: DetectRuntimeRequest) => Promise<unknown>

  /** 对应 RuntimeService.ListRuntimes。 */
  ListRuntimes: () => Promise<unknown>

  /** 对应 RuntimeService.ResolveRuntimeRequirement。 */
  ResolveRuntimeRequirement: (request: { versionId: string }) => Promise<unknown>

  /** 对应 RuntimeService.InstallRuntime（P3-09）。 */
  InstallRuntime: (request: RuntimeInstallRequest) => Promise<unknown>

  /** 对应 RuntimeService.SelectRuntime（P3-09）。 */
  SelectRuntime: (request: RuntimeSelectRequest) => Promise<unknown>

  /** 对应 RuntimeService.GetRuntimeSourceInfo（P3-14，只查元数据不下载）。 */
  GetRuntimeSourceInfo: (majorVersion: number) => Promise<unknown>

  /** 对应 RuntimeService.ListRuntimeUpdates（P3-14，只读缓存不联网）。 */
  ListRuntimeUpdates: () => Promise<unknown>

  /** 对应 RuntimeService.CheckRuntimeUpdates（P3-14，会联网）。 */
  CheckRuntimeUpdates: () => Promise<unknown>

  /** 对应 RuntimeService.ListRuntimeLtsVersions（动态 LTS 列表）。 */
  ListRuntimeLtsVersions: (vendor: string) => Promise<unknown>

  /** 对应 RuntimeService.InstallRuntimeSource（P3-16，会联网下载）。 */
  InstallRuntimeSource: (request: RuntimeSourceInstallRequest) => Promise<unknown>

  /** 对应 RuntimeService.UninstallRuntime。 */
  UninstallRuntime: (request: { runtimeId: string }) => Promise<unknown>
}

/** 运行时服务门面。 */
export interface RuntimeService {
  /** 探测设置中指定的 Java 路径；refresh 为 true 时绕过 5 分钟缓存。 */
  detect: (refresh?: boolean) => Promise<RuntimeProbeResult>

  /** 列出可用运行时（命中缓存时不重新探测）。 */
  list: () => Promise<RuntimeList>

  /** 解析某个版本的 Java 版本要求（只读元数据）。 */
  resolveRequirement: (versionId: string) => Promise<RuntimeRequirement>

  /** 安装官方 Java 运行时（真实联网，必须由用户勾选许可后调用）。 */
  install: (request: RuntimeInstallRequest) => Promise<RuntimeInstallAccepted>

  /** 选择要使用的运行时。 */
  select: (request: RuntimeSelectRequest) => Promise<RuntimeList>

  /** 查询某个大版本在第三方来源上的最新构建（只查元数据，不下载）。 */
  getSourceInfo: (majorVersion: number) => Promise<RuntimeSourceInfo>

  /** 读取上次更新检查的缓存结果（不联网）。 */
  listUpdates: () => Promise<RuntimeUpdateInfo[]>

  /** 获取第三方来源当前提供的 LTS 大版本列表（会联网，成功结果由后端缓存）。 */
  getLtsVersions: (vendor: string) => Promise<RuntimeLtsVersions>

  /** 检查已安装运行时的小版本更新（会联网，由用户显式触发）。 */
  checkUpdates: () => Promise<RuntimeUpdateInfo[]>

  /** 从第三方来源安装某个大版本的最新构建（真实联网，必须由用户勾选许可后调用）。 */
  installSource: (request: RuntimeSourceInstallRequest) => Promise<RuntimeInstallAccepted>

  /** 卸载启动器自己安装的运行时；本机 Java 会被后端拒绝。返回最新列表。 */
  uninstall: (runtimeId: string) => Promise<RuntimeList>
}

/** 字符串兜底：非字符串一律按空串处理。 */
function toText(value: unknown): string {
  return typeof value === 'string' ? value : ''
}

/** 数值兜底：非有限数一律按 0 处理。 */
function toNumber(value: unknown): number {
  return typeof value === 'number' && Number.isFinite(value) ? value : 0
}

/** 布尔兜底：只有严格 true 才算真。 */
function toBoolean(value: unknown): boolean {
  return value === true
}

/** 判断是否为可索引对象。 */
function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null
}

/**
 * 归一化单条探测告警。
 *
 * @param raw 后端返回的告警对象
 * @returns 归一化后的告警
 */
export function normalizeRuntimeProbeWarning(raw: unknown): RuntimeProbeWarning {
  const record = isRecord(raw) ? raw : {}
  return {
    code: toText(record.code),
    message: toText(record.message),
  }
}

/**
 * 归一化告警数组，剔除结构不可用的条目。
 *
 * @param raw 后端返回的告警数组
 * @returns 归一化后的告警数组（恒为数组）
 */
function normalizeWarnings(raw: unknown): RuntimeProbeWarning[] {
  if (!Array.isArray(raw)) {
    return []
  }
  return raw.filter(isRecord).map(normalizeRuntimeProbeWarning)
}

/**
 * 归一化单个运行时视图。
 *
 * @param raw 后端返回的运行时对象
 * @returns 归一化后的运行时视图
 */
export function normalizeRuntimeSummary(raw: unknown): RuntimeSummary {
  const record = isRecord(raw) ? raw : {}
  return {
    runtimeId: toText(record.runtimeId),
    displayName: toText(record.displayName),
    kind: toText(record.kind) as RuntimeSummary['kind'],
    vendor: toText(record.vendor) as RuntimeSummary['vendor'],
    majorVersion: toNumber(record.majorVersion),
    fullVersion: toText(record.fullVersion),
    arch: toText(record.arch) as RuntimeSummary['arch'],
    bitness: toText(record.bitness) as RuntimeSummary['bitness'],
    source: toText(record.source) as RuntimeSummary['source'],
    compatible: toBoolean(record.compatible),
    compatibleReason: toText(record.compatibleReason),
    selected: toBoolean(record.selected),
    verifiedAtMs: toNumber(record.verifiedAtMs),
  }
}

/**
 * 归一化探测结果。
 *
 * @param raw 后端返回的探测结果
 * @returns 归一化后的探测结果（items 恒为数组）
 */
export function normalizeRuntimeProbeResult(raw: unknown): RuntimeProbeResult {
  const record = isRecord(raw) ? raw : {}
  const items = Array.isArray(record.items) ? record.items.filter(isRecord).map(normalizeRuntimeSummary) : []
  return {
    items,
    probedAtMs: toNumber(record.probedAtMs),
    cacheHit: toBoolean(record.cacheHit),
    warnings: normalizeWarnings(record.warnings),
  }
}

/**
 * 归一化 Java 版本要求。
 *
 * @param raw 后端返回的需求对象
 * @returns 归一化后的需求；结构不可用时按「未知」处理
 */
export function normalizeRuntimeRequirement(raw: unknown): RuntimeRequirement {
  const record = isRecord(raw) ? raw : {}
  return {
    major: toNumber(record.major),
    component: toText(record.component),
    platform: toText(record.platform),
    arch: toText(record.arch),
    bitness: toText(record.bitness),
    source: (toText(record.source) || 'unknown') as RuntimeRequirement['source'],
    warnings: normalizeWarnings(record.warnings),
  }
}

/**
 * 归一化运行时列表。
 *
 * @param raw 后端返回的列表对象
 * @returns 归一化后的列表（items 恒为数组）
 */
export function normalizeRuntimeList(raw: unknown): RuntimeList {
  const record = isRecord(raw) ? raw : {}
  const items = Array.isArray(record.items) ? record.items.filter(isRecord).map(normalizeRuntimeSummary) : []
  const list: RuntimeList = {
    items,
    selectedRuntimeId: toText(record.selectedRuntimeId),
  }
  if (isRecord(record.requirement)) {
    list.requirement = normalizeRuntimeRequirement(record.requirement)
  }
  return list
}

/**
 * 归一化安装受理结果。
 *
 * @param raw 后端返回的受理对象
 * @returns 归一化后的受理结果（字段缺失时给安全默认值）
 */
export function normalizeRuntimeInstallAccepted(raw: unknown): RuntimeInstallAccepted {
  const record = isRecord(raw) ? raw : {}
  const accepted: RuntimeInstallAccepted = {
    taskId: toText(record.taskId),
    state: toText(record.state),
    duplicate: toBoolean(record.duplicate),
    createdAtMs: toNumber(record.createdAtMs),
  }
  const parentTaskId = toText(record.parentTaskId)
  if (parentTaskId !== '') {
    accepted.parentTaskId = parentTaskId
  }
  return accepted
}

/**
 * 归一化单条更新可用性。
 *
 * 后端字段缺失时给安全默认值：版本串空、`updateAvailable=false`、
 * `sizeBytes=0`——界面据此显示「未提供」，绝不把未知当成「有更新」。
 *
 * @param raw 后端返回的更新条目
 * @returns 前端视图
 */
export function normalizeRuntimeUpdateInfo(raw: unknown): RuntimeUpdateInfo {
  const record = isRecord(raw) ? raw : {}
  return {
    runtimeId: toText(record.runtimeId),
    majorVersion: toNumber(record.majorVersion),
    installedFullVersion: toText(record.installedFullVersion),
    latestFullVersion: toText(record.latestFullVersion),
    updateAvailable: toBoolean(record.updateAvailable),
    latestSizeBytes: toNumber(record.latestSizeBytes),
    checkedAtMs: toNumber(record.checkedAtMs),
    checkError: toText(record.checkError),
  }
}

/**
 * 归一化更新列表。
 *
 * @param raw 后端返回的更新列表
 * @returns 只保留带 runtimeId 的条目，避免出现无法对应的空行
 */
export function normalizeRuntimeUpdateInfoList(raw: unknown): RuntimeUpdateInfo[] {
  if (!Array.isArray(raw)) {
    return []
  }
  return raw
    .filter(isRecord)
    .map(normalizeRuntimeUpdateInfo)
    .filter((info) => info.runtimeId !== '')
}

/**
 * 归一化第三方来源信息。
 *
 * `sizeBytes` 保留后端原值：0 表示后端未提供（界面显示「未提供」而不是 `0 B`）。
 *
 * @param raw 后端返回的来源信息
 * @returns 前端视图
 */
export function normalizeRuntimeSourceInfo(raw: unknown): RuntimeSourceInfo {
  const record = isRecord(raw) ? raw : {}
  const hosts = Array.isArray(record.downloadHosts)
    ? record.downloadHosts.filter((host): host is string => typeof host === 'string')
    : []
  return {
    source: toText(record.source),
    majorVersion: toNumber(record.majorVersion),
    semver: toText(record.semver),
    fullVersion: toText(record.fullVersion),
    os: toText(record.os),
    arch: toText(record.arch),
    sizeBytes: toNumber(record.sizeBytes),
    sha256: toText(record.sha256),
    downloadHosts: hosts,
  }
}

/**
 * 归一化动态 LTS 列表。
 *
 * 只保留正整数并去重；排序以后端为准，前端不重新排序，避免两侧结论不一致。
 *
 * @param raw 后端返回的 LTS 列表
 * @returns 前端视图
 */
export function normalizeRuntimeLtsVersions(raw: unknown): RuntimeLtsVersions {
  const record = isRecord(raw) ? raw : {}
  const versions = Array.isArray(record.versions)
    ? record.versions
        .map((version) => toNumber(version))
        .filter((version) => Number.isFinite(version) && version > 0)
    : []
  return {
    versions: Array.from(new Set(versions)),
    mostRecentLts: toNumber(record.mostRecentLts),
  }
}

/**
 * 用后端绑定接口创建运行时服务门面。
 *
 * 所有调用失败都会转成统一的 AppError，界面按
 * `<错误码>: <说明>` 展示；探测失败码见 `RUNTIME_ERROR_CODE`。
 *
 * @param api 由装配层提供的绑定函数集合
 * @returns 运行时服务门面
 */
export function createRuntimeService(api: RuntimeBindingsApi): RuntimeService {
  /** 统一包装：调用绑定 + 归一化，异常转 AppError。 */
  const call = async <T>(invoke: () => Promise<unknown>, normalize: (raw: unknown) => T): Promise<T> => {
    try {
      const raw = await invoke()
      return normalize(raw)
    } catch (error) {
      throw toAppError(error)
    }
  }

  return {
    /** 探测设置中指定的 Java 路径。 */
    async detect(refresh = false): Promise<RuntimeProbeResult> {
      return call(() => api.DetectRuntimes({ refresh }), normalizeRuntimeProbeResult)
    },

    /** 列出可用运行时。 */
    async list(): Promise<RuntimeList> {
      return call(() => api.ListRuntimes(), normalizeRuntimeList)
    },

    /** 解析版本要求。 */
    async resolveRequirement(versionId: string): Promise<RuntimeRequirement> {
      if (versionId.trim() === '') {
        throw toAppError({
          code: RUNTIME_ERROR_CODE.versionMetadata,
          message: '版本标识不能为空。',
          retryable: false,
        })
      }
      return call(
        () => api.ResolveRuntimeRequirement({ versionId }),
        normalizeRuntimeRequirement,
      )
    },

    /** 安装官方运行时（真实联网；未接受许可时后端会直接拒绝）。 */
    async install(request: RuntimeInstallRequest): Promise<RuntimeInstallAccepted> {
      if (request.component.trim() === '') {
        throw toAppError({
          code: RUNTIME_ERROR_CODE.installFailed,
          message: '请先选择要安装的 Java 组件。',
          retryable: false,
        })
      }
      if (!request.acceptLicense) {
        throw toAppError({
          code: RUNTIME_ERROR_CODE.installFailed,
          message: '安装 Java 运行时前必须勾选并接受许可提示。',
          retryable: false,
        })
      }
      return call(
        () =>
          api.InstallRuntime({
            component: request.component,
            platform: request.platform ?? '',
            acceptLicense: true,
          }),
        normalizeRuntimeInstallAccepted,
      )
    },

    /** 选择运行时；不兼容的运行时需要显式 force。 */
    async select(request: RuntimeSelectRequest): Promise<RuntimeList> {
      if (request.runtimeId.trim() === '') {
        throw toAppError({
          code: RUNTIME_ERROR_CODE.requirementUnmet,
          message: '请先选择一个 Java 运行时。',
          retryable: false,
        })
      }
      return call(
        () => api.SelectRuntime({ runtimeId: request.runtimeId, force: request.force === true }),
        normalizeRuntimeList,
      )
    },

    /** 卸载启动器自己安装的运行时；返回卸载后的最新列表。 */
    async uninstall(runtimeId: string): Promise<RuntimeList> {
      if (runtimeId.trim() === '') {
        throw toAppError({
          code: RUNTIME_ERROR_CODE.requirementUnmet,
          message: '请先选择要卸载的 Java 运行时。',
          retryable: false,
        })
      }
      return call(() => api.UninstallRuntime({ runtimeId }), normalizeRuntimeList)
    },

    /** 查询第三方来源上的最新构建（只查元数据，不下载、不安装）。 */
    async getSourceInfo(majorVersion: number): Promise<RuntimeSourceInfo> {
      if (!Number.isFinite(majorVersion) || majorVersion <= 0) {
        throw toAppError({
          code: RUNTIME_ERROR_CODE.requirementUnmet,
          message: '要查询的 Java 大版本无效。',
          retryable: false,
        })
      }
      return call(() => api.GetRuntimeSourceInfo(majorVersion), normalizeRuntimeSourceInfo)
    },

    /** 读取上次更新检查的缓存结果（不联网）。 */
    async listUpdates(): Promise<RuntimeUpdateInfo[]> {
      return call(() => api.ListRuntimeUpdates(), normalizeRuntimeUpdateInfoList)
    },

    /** 获取第三方来源当前提供的 LTS 大版本列表。 */
    async getLtsVersions(vendor: string): Promise<RuntimeLtsVersions> {
      const normalized = vendor.trim()
      if (normalized === '') {
        throw toAppError({
          code: RUNTIME_ERROR_CODE.requirementUnmet,
          message: '请先选择 Java 发行版。',
          retryable: false,
        })
      }
      return call(() => api.ListRuntimeLtsVersions(normalized), normalizeRuntimeLtsVersions)
    },

    /** 检查小版本更新（会联网；由用户显式点击触发）。 */
    async checkUpdates(): Promise<RuntimeUpdateInfo[]> {
      return call(() => api.CheckRuntimeUpdates(), normalizeRuntimeUpdateInfoList)
    },

    /**
     * 从第三方来源安装某个大版本的最新构建。
     *
     * 「更新」与「安装」共用这一条链路：更新就是把同一大版本重新安装成来源侧最新构建，
     * 因此请求只需要大版本号，不需要新旧版本字符串比较。
     */
    async installSource(request: RuntimeSourceInstallRequest): Promise<RuntimeInstallAccepted> {
      if (!Number.isFinite(request.majorVersion) || request.majorVersion <= 0) {
        throw toAppError({
          code: RUNTIME_ERROR_CODE.requirementUnmet,
          message: '要安装的 Java 大版本无效。',
          retryable: false,
        })
      }
      if (!request.acceptLicense) {
        throw toAppError({
          code: RUNTIME_ERROR_CODE.installFailed,
          message: '安装 Java 运行时前必须勾选并接受许可提示。',
          retryable: false,
        })
      }
      return call(
        () =>
          api.InstallRuntimeSource({
            majorVersion: request.majorVersion,
            vendor: request.vendor.trim() || 'adoptium',
            acceptLicense: true,
          }),
        normalizeRuntimeInstallAccepted,
      )
    },
  }
}

// ===== 本卡（P3-12）追加：绑定装配、安装进度收敛与事件订阅 =====

/**
 * 由生成绑定提供的函数集合。
 *
 * 与 `services/download.ts` 同样的写法：逐个方法显式搬运参数，
 * 把「生成物类型 → 前端类型」的收敛集中在这一处，页面层不接触 bindings。
 */
const runtimeBindingsApi: RuntimeBindingsApi = {
  /** 对应 RuntimeService.DetectRuntimes。 */
  DetectRuntimes: (request: DetectRuntimeRequest) =>
    RuntimeService.DetectRuntimes({ refresh: request.refresh === true }),

  /** 对应 RuntimeService.ListRuntimes。 */
  ListRuntimes: () => RuntimeService.ListRuntimes(),

  /** 对应 RuntimeService.ResolveRuntimeRequirement。 */
  ResolveRuntimeRequirement: (request: { versionId: string }) =>
    RuntimeService.ResolveRuntimeRequirement({ versionId: request.versionId }),

  /** 对应 RuntimeService.InstallRuntime；`platform` 空串表示按当前平台。 */
  InstallRuntime: (request: RuntimeInstallRequest) =>
    RuntimeService.InstallRuntime({
      component: request.component,
      platform: request.platform ?? '',
      acceptLicense: request.acceptLicense === true,
    }),

  /** 对应 RuntimeService.SelectRuntime。 */
  SelectRuntime: (request: RuntimeSelectRequest) =>
    RuntimeService.SelectRuntime({
      runtimeId: request.runtimeId,
      force: request.force === true,
    }),

  /** 对应 RuntimeService.GetRuntimeSourceInfo；只查元数据，不发起下载。 */
  GetRuntimeSourceInfo: (majorVersion: number) => RuntimeService.GetRuntimeSourceInfo(majorVersion),

  /** 对应 RuntimeService.ListRuntimeUpdates；只读缓存，不联网。 */
  ListRuntimeUpdates: () => RuntimeService.ListRuntimeUpdates(),

  /** 对应 RuntimeService.CheckRuntimeUpdates；会联网，由用户显式触发。 */
  CheckRuntimeUpdates: () => RuntimeService.CheckRuntimeUpdates(),

  /** 对应 RuntimeService.ListRuntimeLtsVersions；动态 LTS 列表。 */
  ListRuntimeLtsVersions: (vendor: string) => RuntimeService.ListRuntimeLtsVersions(vendor),

  /**
   * 对应 RuntimeService.InstallRuntimeSource（P3-16 新增的 wails 包装）。
   *
   * 生成物签名是「收一个 `InstallRuntimeSourceRequest` 结构体」，
   * 返回 `download.TaskAccepted`；这里只搬运 `majorVersion` 与 `acceptLicense` 两个字段，
   * 不额外拼参数，也不改字段名。
   */
  InstallRuntimeSource: (request: RuntimeSourceInstallRequest) =>
    RuntimeService.InstallRuntimeSource({
      majorVersion: request.majorVersion,
      vendor: request.vendor,
      acceptLicense: request.acceptLicense === true,
    }),

  /** 对应 RuntimeService.UninstallRuntime；只搬运 runtimeId，不额外拼参数。 */
  UninstallRuntime: (request: { runtimeId: string }) =>
    RuntimeService.UninstallRuntime({ runtimeId: request.runtimeId }),
}

/** 已装配的运行时服务实例：页面与 composable 只依赖它，不直接 import bindings。 */
export const runtimeService: RuntimeService = createRuntimeService(runtimeBindingsApi)

/**
 * 把事件里的 `state` 收敛成前端阶段。
 *
 * 后端只可能发出 `downloading` / `committing` / `completed` / `failed` 四种取值
 * （见 `RUNTIME_INSTALL_STATE`）；出现其它值时按 `failed` 处理并保留原始错误码，
 * 避免把未知状态当成「成功」。
 *
 * @param state 事件载荷中的 state
 * @returns 前端阶段
 */
export function toRuntimeInstallPhase(state: string): RuntimeInstallPhase {
  switch (state) {
    case RUNTIME_INSTALL_STATE.downloading:
      return 'downloading'
    case RUNTIME_INSTALL_STATE.committing:
      return 'committing'
    case RUNTIME_INSTALL_STATE.completed:
      return 'completed'
    case RUNTIME_INSTALL_STATE.failed:
      return 'failed'
    default:
      return 'failed'
  }
}

/**
 * 把 `download.task.changed.v1` 的载荷收敛成安装进度视图。
 *
 * 载荷只有 `{taskId, state, progress, errorCode}`，因此这里**不编造**
 * 已下载字节或下载速度，只把 progress 裁剪到 0..1。
 *
 * @param payload 事件载荷
 * @returns 安装进度视图
 */
export function normalizeRuntimeInstallProgress(
  payload: DownloadTaskChangedPayload,
): RuntimeInstallProgress {
  const raw = typeof payload.progress === 'number' && Number.isFinite(payload.progress) ? payload.progress : 0
  return {
    taskId: typeof payload.taskId === 'string' ? payload.taskId : '',
    phase: toRuntimeInstallPhase(typeof payload.state === 'string' ? payload.state : ''),
    progress: Math.min(1, Math.max(0, raw)),
    errorCode: typeof payload.errorCode === 'string' ? payload.errorCode : '',
  }
}

/**
 * 订阅 Runtime 安装进度。
 *
 * Runtime 安装复用下载队列的事件通道 `download.task.changed.v1`
 * （D-48：不经过下载队列，但事件名相同），任务 id 形如
 * `runtime-install-<runtimeId>`。这里复用 `services/download.ts` 已验证的
 * 订阅闸门，只按前缀过滤，不重复实现 `@wailsio/runtime` 的订阅逻辑。
 *
 * @param handler 收到安装进度时的回调
 * @returns 取消订阅函数
 */
export function subscribeRuntimeInstallProgress(
  handler: (progress: RuntimeInstallProgress) => void,
): Unsubscribe {
  return subscribeDownloadTaskChanged((event) => {
    const payload = event.payload
    if (!payload || typeof payload.taskId !== 'string') {
      return
    }
    if (!payload.taskId.startsWith(RUNTIME_INSTALL_TASK_PREFIX)) {
      return
    }
    handler(normalizeRuntimeInstallProgress(payload))
  })
}
