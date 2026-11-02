/**
 * 版本服务的绑定封装层。
 *
 * 前端只有本文件可以 import 生成目录中的版本绑定；页面与组件必须通过这里
 * 暴露的语义化函数访问后端（AGENTS.md 8.2）。
 *
 * 绑定路径说明：版本服务定义在 `internal/version` 包内（注册点是
 * `internal/app/bootstrap.go`），因此生成物位于
 * `bindings/…/launcher/internal/version/`。`bindings/**` 是生成目录，不手工修改。
 *
 * 离线语义：列表查询在缓存过期且网络失败时**不会报错**，而是返回
 * `stale: true` 与 `warningCode: MANIFEST_STALE`；界面必须据此提示
 * 「当前为离线缓存」，不得把过期数据展示成最新数据。
 */
import { VersionService } from '../../bindings/example.com/ninevoxel/launcher/internal/version'
import { toAppError } from './errors'
import type {
  CompatLayer,
  CompatReport,
  InstallPlanDTO,
  InstallPlanSummary,
  ManifestStatus,
  PlanArtifactHashes,
  PlanArtifactView,
  PlanWarning,
  RuntimeRequirement,
  VersionDetail,
  VersionListRequest,
  VersionListResult,
  VersionSummary,
} from '../types/version'

/**
 * 刷新失败时的兜底错误码，与 Go 侧 VERSION_MANIFEST_001 一致。
 *
 * 只有后端未给出可解析错误码时才使用；界面据此提示「刷新失败」。
 */
const VERSION_MANIFEST_FETCH_FALLBACK = 'VERSION_MANIFEST_001'

/**
 * 把任意值收敛为有限数字。
 *
 * @param value 待收敛的值
 * @param fallback 非法时使用的回退值
 */
function asNumber(value: unknown, fallback: number): number {
  return typeof value === 'number' && Number.isFinite(value) ? value : fallback
}

/**
 * 把任意值收敛为布尔值。
 *
 * @param value 待收敛的值
 * @param fallback 非法时使用的回退值
 */
function asBoolean(value: unknown, fallback: boolean): boolean {
  return typeof value === 'boolean' ? value : fallback
}

/**
 * 把任意值收敛为字符串。
 *
 * @param value 待收敛的值
 * @param fallback 非法时使用的回退值
 */
function asString(value: unknown, fallback: string): string {
  return typeof value === 'string' ? value : fallback
}

/**
 * 判断值是否为普通对象。
 *
 * @param value 待判断的值
 */
function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null
}

/**
 * 归一化单个版本条目。
 *
 * 后端已保证字段完整；这里只做类型收敛，避免界面出现 `undefined`。
 * 无法识别的条目返回 null，由调用方丢弃而不是渲染半成品。
 *
 * @param raw 后端返回的原始条目
 */
function normalizeSummary(raw: unknown): VersionSummary | null {
  if (!isRecord(raw)) {
    return null
  }

  const id = asString(raw.id, '')
  if (id === '') {
    return null
  }

  const compat = isRecord(raw.compat) ? raw.compat : {}
  return {
    id,
    type: asString(raw.type, 'release') as VersionSummary['type'],
    releaseTime: asString(raw.releaseTime, ''),
    sha1: asString(raw.sha1, ''),
    size: asNumber(raw.size, 0),
    installed: asBoolean(raw.installed, false),
    compat: {
      // 列表页不做逐版本解析：缺字段时按「未评估」处理，避免误显示为支持。
      evaluated: asBoolean(compat.evaluated, false),
      metadataSupported: asBoolean(compat.metadataSupported, false),
      installSupported: asBoolean(compat.installSupported, false),
      launchSupported: asBoolean(compat.launchSupported, false),
      loaderSupported: asBoolean(compat.loaderSupported, false),
    },
    verified: asBoolean(raw.verified, false),
  }
}

/**
 * 归一化版本列表结果。
 *
 * @param raw 后端返回的原始结果
 */
function normalizeListResult(raw: unknown): VersionListResult {
  const record = isRecord(raw) ? raw : {}
  const items = Array.isArray(record.items) ? record.items : []

  const normalizedItems: VersionSummary[] = []
  for (const item of items) {
    const normalized = normalizeSummary(item)
    if (normalized !== null) {
      normalizedItems.push(normalized)
    }
  }

  return {
    items: normalizedItems,
    total: asNumber(record.total, normalizedItems.length),
    offset: asNumber(record.offset, 0),
    limit: asNumber(record.limit, 0),
    stale: asBoolean(record.stale, false),
    fetchedAtMs: asNumber(record.fetchedAtMs, 0),
    verified: asBoolean(record.verified, false),
    warningCode: asString(record.warningCode, ''),
  }
}

/**
 * 归一化清单状态。
 *
 * @param raw 后端返回的原始状态
 */
function normalizeStatus(raw: unknown): ManifestStatus {
  const record = isRecord(raw) ? raw : {}
  return {
    available: asBoolean(record.available, false),
    fetchedAtMs: asNumber(record.fetchedAtMs, 0),
    stale: asBoolean(record.stale, true),
    count: asNumber(record.count, 0),
    sourceHost: asString(record.sourceHost, ''),
    lastErrorCode: asString(record.lastErrorCode, ''),
    verified: asBoolean(record.verified, false),
  }
}

/**
 * 查询可下载的版本列表。
 *
 * 默认只返回 release + snapshot；需要旧预览版时显式传 `type: 'old_alpha'`
 * 或 `type: 'old_beta'`。
 *
 * @param request 过滤与分页参数
 * @returns 归一化后的版本列表
 * @throws AppError 完全没有缓存且获取失败时的归一化错误
 */
export async function listVersions(request: VersionListRequest = {}): Promise<VersionListResult> {
  try {
    const raw = await VersionService.ListVersions({
      type: request.type ?? '',
      offset: request.offset ?? 0,
      limit: request.limit ?? 0,
    })
    return normalizeListResult(raw)
  } catch (error) {
    throw toAppError(error)
  }
}

/**
 * 查询当前清单的可用性状态。
 *
 * 该调用不联网，适合界面启动时与事件丢失后恢复状态。
 *
 * @returns 归一化后的清单状态
 * @throws AppError 数据根不可用时的归一化错误
 */
export async function getManifestStatus(): Promise<ManifestStatus> {
  try {
    return normalizeStatus(await VersionService.GetManifestStatus())
  } catch (error) {
    throw toAppError(error)
  }
}

/**
 * 强制从官方源刷新版本清单。
 *
 * 与列表查询的降级策略不同：刷新失败会抛出错误，界面应提示
 * 「刷新失败，当前使用缓存」，而不是静默沿用旧数据。
 *
 * @returns 刷新后的清单状态
 * @throws AppError 刷新失败时的归一化错误
 */
export async function refreshManifest(): Promise<ManifestStatus> {
  try {
    return normalizeStatus(await VersionService.RefreshManifest())
  } catch (error) {
    throw toAppError(error, VERSION_MANIFEST_FETCH_FALLBACK)
  }
}

/**
 * 归一化四层兼容判定的原始值。
 *
 * @param raw 后端返回的原始对象
 */
function normalizeCompatLayers(raw: unknown): CompatLayer {
  const record = isRecord(raw) ? raw : {}
  return {
    // 该函数只用于兼容性报告：报告一定经过逐版本解析，缺字段时按已评估处理。
    evaluated: asBoolean(record.evaluated, true),
    metadataSupported: asBoolean(record.metadataSupported, true),
    installSupported: asBoolean(record.installSupported, false),
    launchSupported: asBoolean(record.launchSupported, false),
    loaderSupported: asBoolean(record.loaderSupported, false),
  }
}

/**
 * 归一化单条计划告警。
 *
 * @param raw 后端返回的原始告警
 */
function normalizeWarning(raw: unknown): PlanWarning {
  const record = isRecord(raw) ? raw : {}
  return {
    code: asString(record.code, ''),
    message: asString(record.message, ''),
  }
}

/**
 * 归一化告警列表。
 *
 * @param raw 后端返回的原始告警数组
 */
function normalizeWarnings(raw: unknown): PlanWarning[] {
  if (!Array.isArray(raw)) {
    return []
  }
  const warnings: PlanWarning[] = []
  for (const item of raw) {
    const warning = normalizeWarning(item)
    if (warning.code !== '') {
      warnings.push(warning)
    }
  }
  return warnings
}

/**
 * 归一化兼容性报告。
 *
 * @param raw 后端返回的原始报告
 */
function normalizeCompatReport(raw: unknown): CompatReport {
  const record = isRecord(raw) ? raw : {}
  const status = asString(record.status, 'metadata_only')
  const known =
    status === 'launch_supported' ||
    status === 'metadata_only' ||
    status === 'unsupported' ||
    status === 'broken'
  return {
    versionId: asString(record.versionId, ''),
    layers: normalizeCompatLayers(record.layers),
    status: known ? status : 'metadata_only',
    notes: normalizeWarnings(record.notes),
  }
}

/**
 * 归一化制品哈希摘要。
 *
 * @param raw 后端返回的原始哈希对象
 */
function normalizeHashes(raw: unknown): PlanArtifactHashes {
  const record = isRecord(raw) ? raw : {}
  return {
    sha1: asString(record.sha1, ''),
    sha256: asString(record.sha256, ''),
  }
}

/**
 * 归一化单个计划制品。
 *
 * 缺少目标路径或逻辑键时返回 null，由调用方丢弃，避免界面渲染半成品。
 *
 * @param raw 后端返回的原始制品
 */
function normalizePlanArtifact(raw: unknown): PlanArtifactView | null {
  if (!isRecord(raw)) {
    return null
  }
  const targetRelPath = asString(raw.targetRelPath, '')
  const logicalKey = asString(raw.logicalKey, '')
  if (targetRelPath === '' || logicalKey === '') {
    return null
  }
  const exclude = Array.isArray(raw.extractExclude)
    ? raw.extractExclude.filter((item): item is string => typeof item === 'string')
    : []
  return {
    kind: asString(raw.kind, ''),
    logicalKey,
    version: asString(raw.version, ''),
    sourceId: asString(raw.sourceId, ''),
    sourceUri: asString(raw.sourceUri, ''),
    expectedSize: asNumber(raw.expectedSize, 0),
    hashes: normalizeHashes(raw.hashes),
    targetRelPath,
    immutable: asBoolean(raw.immutable, true),
    extract: asBoolean(raw.extract, false),
    nativesClassifier: asString(raw.nativesClassifier, ''),
    extractExclude: exclude,
  }
}

/**
 * 归一化安装计划摘要。
 *
 * @param raw 后端返回的原始摘要
 */
function normalizePlanSummary(raw: unknown): InstallPlanSummary {
  const record = isRecord(raw) ? raw : {}
  return {
    versionId: asString(record.versionId, ''),
    resolverVersion: asString(record.resolverVersion, ''),
    planHash: asString(record.planHash, ''),
    artifactCount: asNumber(record.artifactCount, 0),
    totalBytes: asNumber(record.totalBytes, 0),
    generatedAtMs: asNumber(record.generatedAtMs, 0),
  }
}

/**
 * 归一化安装计划。
 *
 * @param raw 后端返回的原始计划
 */
function normalizePlan(raw: unknown): InstallPlanDTO {
  const record = isRecord(raw) ? raw : {}
  const artifacts: PlanArtifactView[] = []
  if (Array.isArray(record.artifacts)) {
    for (const item of record.artifacts) {
      const artifact = normalizePlanArtifact(item)
      if (artifact !== null) {
        artifacts.push(artifact)
      }
    }
  }
  return {
    summary: normalizePlanSummary(record.summary),
    artifacts,
    warnings: normalizeWarnings(record.warnings),
    compatibility: normalizeCompatReport(record.compatibility),
  }
}

/**
 * 归一化 Java 版本要求。
 *
 * @param raw 后端返回的原始要求
 */
function normalizeRuntimeRequirement(raw: unknown): RuntimeRequirement {
  const record = isRecord(raw) ? raw : {}
  return {
    major: asNumber(record.major, 0),
    component: asString(record.component, ''),
    platform: asString(record.platform, ''),
    arch: asString(record.arch, ''),
    bitness: asString(record.bitness, ''),
    source: asString(record.source, 'unknown'),
  }
}

/**
 * 归一化版本详情。
 *
 * @param raw 后端返回的原始详情
 */
function normalizeVersionDetail(raw: unknown): VersionDetail {
  const record = isRecord(raw) ? raw : {}
  return {
    id: asString(record.id, ''),
    type: asString(record.type, 'release') as VersionDetail['type'],
    releaseTime: asString(record.releaseTime, ''),
    size: asNumber(record.size, 0),
    javaRequirement: normalizeRuntimeRequirement(record.javaRequirement),
    plan: normalizePlanSummary(record.plan),
    compat: normalizeCompatReport(record.compat),
    warnings: normalizeWarnings(record.warnings),
  }
}

/**
 * 查询单个版本的详情（元数据摘要 + 计划摘要 + 兼容性判定）。
 *
 * 该调用只读本地版本 JSON，不联网、不下载。
 *
 * @param versionId 版本标识
 * @returns 归一化后的版本详情
 * @throws AppError 版本元数据缺失、非法或数据根不可用
 */
export async function getVersionDetail(versionId: string): Promise<VersionDetail> {
  try {
    return normalizeVersionDetail(await VersionService.GetVersionDetail({ versionId }))
  } catch (error) {
    throw toAppError(error)
  }
}

/**
 * 解析某个版本的安装计划。
 *
 * 同一版本 JSON 必得同一 `planHash`；计划只用于展示与后续下载任务创建，
 * 本调用不写数据库、不创建目录、不下载任何制品。
 *
 * @param versionId 版本标识
 * @returns 归一化后的安装计划
 * @throws AppError 版本元数据缺失、非法或数据根不可用
 */
export async function resolveInstallPlan(versionId: string): Promise<InstallPlanDTO> {
  try {
    return normalizePlan(await VersionService.ResolveInstallPlan({ versionId }))
  } catch (error) {
    throw toAppError(error)
  }
}
