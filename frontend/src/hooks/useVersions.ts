/**
 * Minecraft 版本清单的数据钩子。
 *
 * 数据来源：Go 侧 `VersionService`（P3-01 清单 + P3-13 按需获取）。
 * 该钩子只负责「取清单、暴露状态」，不做任何缓存或假数据兜底：
 * 清单没拉到就显示错误码，让用户知道是网络问题还是元数据问题。
 */

import { useCallback, useEffect, useRef, useState } from 'react'

import {
  getManifestStatus,
  listVersions,
  refreshManifest,
} from '../services/version'
import type { ManifestStatus, VersionSummary, VersionType } from '../types/version'

/** 版本类型过滤值，空字符串表示「正式版 + 快照」的默认集合；支持逗号分隔多类型。 */
export type VersionTypeFilter = VersionType | '' | string

/** 清单区块需要的全部状态。 */
export interface VersionCatalogState {
  /** 当前页版本条目。 */
  items: VersionSummary[]
  /** 过滤后的总数。 */
  total: number
  /** 清单是否来自过期缓存或离线降级。 */
  stale: boolean
  /** 清单获取时间（UTC 毫秒），0 表示没有清单。 */
  fetchedAtMs: number
  /** 非致命告警码（目前只有 MANIFEST_STALE）。 */
  warningCode: string
  /** 清单可用性摘要，null 表示还没查过。 */
  status: ManifestStatus | null
  /** 是否正在加载。 */
  loading: boolean
  /** 稳定错误码，null 表示没有错误。 */
  errorCode: string | null
  /** 错误提示文本。 */
  errorMessage: string | null
  /** 当前过滤的版本类型。 */
  typeFilter: VersionTypeFilter
  /** 单页条数。 */
  limit: number
}

/** 钩子返回值：状态 + 操作。 */
export interface VersionCatalogController extends VersionCatalogState {
  /** 切换版本类型过滤并重新取数。 */
  setTypeFilter: (next: VersionTypeFilter) => void
  /** 重新拉取清单（首次会联网刷新，失败时保留错误码）。 */
  reload: (options?: { forceRefresh?: boolean }) => void
}

/** 默认单页条数：足够填满一屏且不需要分页控件。 */
const DEFAULT_LIMIT = 60

/**
 * 把服务层抛出的错误整理成稳定错误码与可读文本。
 *
 * 服务层已经做过脱敏，这里只负责在缺码时兜底，不拼接任何路径信息。
 */
function toErrorInfo(error: unknown): { code: string; message: string } {
  if (error && typeof error === 'object') {
    const candidate = error as { code?: unknown; message?: unknown }
    const code = typeof candidate.code === 'string' ? candidate.code : ''
    const message = typeof candidate.message === 'string' ? candidate.message : ''
    if (code) {
      return { code, message: message || '读取版本清单失败，请稍后重试。' }
    }
    if (message) {
      return { code: 'VERSION_UNKNOWN', message }
    }
  }
  return { code: 'VERSION_UNKNOWN', message: '读取版本清单失败，请稍后重试。' }
}

/**
 * 管理版本清单的加载与过滤状态。
 *
 * 首次加载流程：先查清单状态，不可用就触发一次联网刷新，再取列表。
 * 这样用户第一次打开页面就能看到数据，而不用自己去点「刷新清单」。
 */
export function useVersions(pageSize = DEFAULT_LIMIT, initialType: VersionTypeFilter = ''): VersionCatalogController {
  const [items, setItems] = useState<VersionSummary[]>([])
  const [total, setTotal] = useState(0)
  const [stale, setStale] = useState(false)
  const [fetchedAtMs, setFetchedAtMs] = useState(0)
  const [warningCode, setWarningCode] = useState('')
  const [status, setStatus] = useState<ManifestStatus | null>(null)
  const [loading, setLoading] = useState(true)
  const [errorCode, setErrorCode] = useState<string | null>(null)
  const [errorMessage, setErrorMessage] = useState<string | null>(null)
  const [typeFilter, setTypeFilterState] = useState<VersionTypeFilter>(initialType)
  const [limit] = useState(pageSize)

  // 用于丢弃过期请求的回写：快速切换过滤时只认最后一次。
  const requestSeqRef = useRef(0)
  const mountedRef = useRef(true)

  useEffect(() => {
    mountedRef.current = true
    return () => {
      mountedRef.current = false
    }
  }, [])

  const load = useCallback(
    async (currentType: VersionTypeFilter, forceRefresh: boolean) => {
      const seq = requestSeqRef.current + 1
      requestSeqRef.current = seq

      setLoading(true)
      setErrorCode(null)
      setErrorMessage(null)

      try {
        // 先看清单是否可用；不可用时自动联网刷新一次，避免用户面对空列表。
        let current = await getManifestStatus()
        if (forceRefresh || !current.available) {
          current = await refreshManifest()
        }
        if (!mountedRef.current || requestSeqRef.current !== seq) {
          return
        }
        setStatus(current)

        if (!current.available) {
          setItems([])
          setTotal(0)
          setStale(true)
          setFetchedAtMs(0)
          setWarningCode('')
          setErrorCode(current.lastErrorCode || 'VERSION_MANIFEST_003')
          setErrorMessage('暂时没有可用的版本清单，请检查网络后重试。')
          return
        }

        const result = await listVersions({
          type: currentType,
          offset: 0,
          limit,
        })
        if (!mountedRef.current || requestSeqRef.current !== seq) {
          return
        }

        setItems(result.items)
        setTotal(result.total)
        setStale(result.stale)
        setFetchedAtMs(result.fetchedAtMs)
        setWarningCode(result.warningCode)
      } catch (error) {
        if (!mountedRef.current || requestSeqRef.current !== seq) {
          return
        }
        const info = toErrorInfo(error)
        setItems([])
        setTotal(0)
        setErrorCode(info.code)
        setErrorMessage(info.message)
      } finally {
        if (mountedRef.current && requestSeqRef.current === seq) {
          setLoading(false)
        }
      }
    },
    [limit],
  )

  useEffect(() => {
    void load(initialType, false)
    // 只在挂载时跑一次；后续由 reload / setTypeFilter 显式触发。
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const setTypeFilter = useCallback(
    (next: VersionTypeFilter) => {
      setTypeFilterState(next)
      void load(next, false)
    },
    [load],
  )

  const reload = useCallback(
    (options?: { forceRefresh?: boolean }) => {
      void load(typeFilter, options?.forceRefresh ?? false)
    },
    [load, typeFilter],
  )

  return {
    items,
    total,
    stale,
    fetchedAtMs,
    warningCode,
    status,
    loading,
    errorCode,
    errorMessage,
    typeFilter,
    limit,
    setTypeFilter,
    reload,
  }
}
