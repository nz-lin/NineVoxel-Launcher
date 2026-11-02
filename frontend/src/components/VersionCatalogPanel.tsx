/**
 * Minecraft 版本清单区块。
 *
 * 数据来自 Go 侧 `VersionService`（官方 version_manifest_v2.json，P3-01/P3-13）。
 * 这里刻意不展示任何推测值：
 *  - 兼容性四层在 `compat.evaluated === false` 时显示「未评估」，不冒充支持/不支持；
 *  - `installed` 为 false 时不显示「已安装」，因为实例能力还没接（P5-A）；
 *  - 清单获取时间、条数、是否过期都来自后端返回，不在前端编造。
 */

import React from 'react'
import { AlertTriangle, CalendarDays, Loader2, RefreshCw, Server, Tag } from 'lucide-react'

import { useVersions, type VersionTypeFilter } from '../hooks/useVersions'
import type { VersionSummary, VersionType } from '../types/version'

/** 版本类型的中文标签。 */
const TYPE_LABELS: Record<VersionType, string> = {
  release: '正式版',
  snapshot: '快照版',
  old_beta: '古老 Beta',
  old_alpha: '古老 Alpha',
}

/** 过滤选项。 */
const FILTERS: Array<{ value: VersionTypeFilter; label: string }> = [
  { value: '', label: '正式版 + 快照' },
  { value: 'release', label: '仅正式版' },
  { value: 'snapshot', label: '仅快照' },
  { value: 'old_beta', label: '古老 Beta' },
  { value: 'old_alpha', label: '古老 Alpha' },
]

/**
 * 把 UTC 毫秒格式化为本地时间。
 *
 * 传入 0 或非法值时返回「—」，不猜测时间。
 */
function formatTimestamp(ms: number): string {
  if (!Number.isFinite(ms) || ms <= 0) {
    return '—'
  }
  const date = new Date(ms)
  if (Number.isNaN(date.getTime())) {
    return '—'
  }
  const pad = (value: number) => String(value).padStart(2, '0')
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${pad(date.getHours())}:${pad(date.getMinutes())}`
}

/**
 * 把版本 JSON 里的 RFC3339 发布时间格式化为「年-月-日」。
 */
function formatReleaseDate(raw: string): string {
  if (!raw) {
    return '—'
  }
  const date = new Date(raw)
  if (Number.isNaN(date.getTime())) {
    return '—'
  }
  const pad = (value: number) => String(value).padStart(2, '0')
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}`
}

/**
 * 产出一行版本的兼容性摘要。
 *
 * 未评估时明确写「未评估」，避免把后端保守默认值当成结论展示。
 */
function describeCompat(item: VersionSummary): { text: string; tone: 'ok' | 'muted' } {
  const compat = item.compat
  if (!compat || compat.evaluated !== true) {
    return { text: '兼容性未评估', tone: 'muted' }
  }
  if (compat.launchSupported) {
    return { text: '可启动', tone: 'ok' }
  }
  if (compat.installSupported) {
    return { text: '可安装（不可启动）', tone: 'muted' }
  }
  if (compat.metadataSupported) {
    return { text: '仅元数据可读', tone: 'muted' }
  }
  return { text: '不支持', tone: 'muted' }
}

/**
 * 版本清单区块组件。
 *
 * 自包含：自己取数、自己处理加载与错误，调用方只需放到页面里。
 */
export const VersionCatalogPanel: React.FC = () => {
  const catalog = useVersions()

  return (
    <section className="bg-white dark:bg-neutral-900 border border-slate-200 dark:border-neutral-800 rounded-2xl shadow-sm overflow-hidden">
      {/* 标题行：清单来源状态与刷新入口 */}
      <div className="flex flex-col lg:flex-row lg:items-center justify-between gap-3 p-5 border-b border-slate-200 dark:border-neutral-800">
        <div>
          <div className="flex items-center gap-2">
            <Server className="w-4 h-4 text-emerald-600 dark:text-emerald-400" />
            <h2 className="text-sm font-bold text-slate-900 dark:text-neutral-100">Minecraft 版本清单</h2>
            <span className="text-[11px] font-mono text-slate-500 dark:text-neutral-400">
              {catalog.total} 个版本
            </span>
          </div>
          <p className="mt-1 text-[11px] text-slate-500 dark:text-neutral-400">
            官方 version_manifest_v2 清单
            {catalog.status?.sourceHost ? ` · 来源 ${catalog.status.sourceHost}` : ''}
            {catalog.fetchedAtMs > 0 ? ` · 获取于 ${formatTimestamp(catalog.fetchedAtMs)}` : ''}
          </p>
        </div>

        <div className="flex items-center gap-2">
          {catalog.stale && (
            <span className="text-[11px] px-2 py-1 rounded-lg bg-amber-500/10 border border-amber-500/30 text-amber-700 dark:text-amber-400 flex items-center gap-1">
              <AlertTriangle className="w-3 h-3" /> 清单已过期
            </span>
          )}
          <button
            type="button"
            onClick={() => catalog.reload({ forceRefresh: true })}
            disabled={catalog.loading}
            className="inline-flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-xs font-medium border border-slate-200 dark:border-neutral-700 text-slate-700 dark:text-neutral-300 hover:bg-slate-100 dark:hover:bg-neutral-800 disabled:opacity-50 transition-colors"
          >
            {catalog.loading ? (
              <Loader2 className="w-3.5 h-3.5 animate-spin" />
            ) : (
              <RefreshCw className="w-3.5 h-3.5" />
            )}
            刷新清单
          </button>
        </div>
      </div>

      {/* 过滤行 */}
      <div className="flex flex-wrap items-center gap-2 px-5 py-3 border-b border-slate-200 dark:border-neutral-800 bg-slate-50 dark:bg-neutral-900/60">
        {FILTERS.map((filter) => {
          const active = catalog.typeFilter === filter.value
          return (
            <button
              key={filter.value || 'all'}
              type="button"
              onClick={() => catalog.setTypeFilter(filter.value)}
              className={[
                'px-2.5 py-1 rounded-lg text-[11px] font-medium border transition-colors',
                active
                  ? 'bg-emerald-500/10 border-emerald-500/40 text-emerald-700 dark:text-emerald-400'
                  : 'border-slate-200 dark:border-neutral-700 text-slate-600 dark:text-neutral-400 hover:bg-slate-100 dark:hover:bg-neutral-800',
              ].join(' ')}
            >
              {filter.label}
            </button>
          )
        })}
      </div>

      {/* 错误态：显示稳定错误码与重试，不做假数据兜底 */}
      {catalog.errorCode && (
        <div className="p-5">
          <div className="rounded-xl border border-rose-500/30 bg-rose-500/5 p-4">
            <div className="flex items-center gap-2 text-sm font-semibold text-rose-600 dark:text-rose-400">
              <AlertTriangle className="w-4 h-4" />
              读取版本清单失败
            </div>
            <p className="mt-1.5 text-xs text-slate-600 dark:text-neutral-400">
              {catalog.errorMessage}
            </p>
            <p className="mt-1 text-[11px] font-mono text-slate-500 dark:text-neutral-500">
              错误码：{catalog.errorCode}
            </p>
            <button
              type="button"
              onClick={() => catalog.reload({ forceRefresh: true })}
              disabled={catalog.loading}
              className="mt-3 inline-flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-xs font-medium bg-emerald-600 hover:bg-emerald-500 text-white disabled:opacity-50 transition-colors"
            >
              {catalog.loading ? <Loader2 className="w-3.5 h-3.5 animate-spin" /> : <RefreshCw className="w-3.5 h-3.5" />}
              重试
            </button>
          </div>
        </div>
      )}

      {/* 加载骨架 */}
      {!catalog.errorCode && catalog.loading && catalog.items.length === 0 && (
        <div className="p-5 space-y-2">
          {Array.from({ length: 6 }).map((_, index) => (
            <div
              key={index}
              className="h-9 rounded-lg bg-slate-100 dark:bg-neutral-800/60 animate-pulse"
            />
          ))}
        </div>
      )}

      {/* 列表 */}
      {!catalog.errorCode && catalog.items.length > 0 && (
        <div className="max-h-[420px] overflow-y-auto">
          <table className="w-full text-left">
            <thead className="sticky top-0 bg-white dark:bg-neutral-900 z-10">
              <tr className="text-[11px] uppercase tracking-wide text-slate-500 dark:text-neutral-500">
                <th className="px-5 py-2 font-medium">版本</th>
                <th className="px-3 py-2 font-medium">类型</th>
                <th className="px-3 py-2 font-medium">发布日期</th>
                <th className="px-3 py-2 font-medium">兼容性</th>
                <th className="px-5 py-2 font-medium text-right">状态</th>
              </tr>
            </thead>
            <tbody>
              {catalog.items.map((item) => {
                const compat = describeCompat(item)
                return (
                  <tr
                    key={item.id}
                    className="border-t border-slate-100 dark:border-neutral-800/70 hover:bg-slate-50 dark:hover:bg-neutral-800/40 transition-colors"
                  >
                    <td className="px-5 py-2.5">
                      <span className="font-mono text-xs font-semibold text-slate-900 dark:text-neutral-100">
                        {item.id}
                      </span>
                    </td>
                    <td className="px-3 py-2.5">
                      <span className="inline-flex items-center gap-1 text-[11px] text-slate-600 dark:text-neutral-400">
                        <Tag className="w-3 h-3" />
                        {TYPE_LABELS[item.type] ?? item.type}
                      </span>
                    </td>
                    <td className="px-3 py-2.5">
                      <span className="inline-flex items-center gap-1 text-[11px] text-slate-600 dark:text-neutral-400">
                        <CalendarDays className="w-3 h-3" />
                        {formatReleaseDate(item.releaseTime)}
                      </span>
                    </td>
                    <td className="px-3 py-2.5">
                      <span
                        className={[
                          'text-[11px] px-2 py-0.5 rounded-md border',
                          compat.tone === 'ok'
                            ? 'border-emerald-500/40 text-emerald-700 dark:text-emerald-400 bg-emerald-500/10'
                            : 'border-slate-200 dark:border-neutral-700 text-slate-500 dark:text-neutral-500',
                        ].join(' ')}
                      >
                        {compat.text}
                      </span>
                    </td>
                    <td className="px-5 py-2.5 text-right">
                      <span className="text-[11px] text-slate-500 dark:text-neutral-500">
                        {item.installed ? '已安装' : '未安装'}
                      </span>
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      )}

      {/* 空态 */}
      {!catalog.errorCode && !catalog.loading && catalog.items.length === 0 && (
        <div className="p-8 text-center">
          <p className="text-sm text-slate-600 dark:text-neutral-400">当前过滤条件下没有版本</p>
          <p className="mt-1 text-[11px] text-slate-500 dark:text-neutral-500">
            换个类型再试，或点右上角「刷新清单」重新拉取。
          </p>
        </div>
      )}

      {/* 底部说明：讲清楚这一块能做什么、不能做什么 */}
      <div className="px-5 py-3 border-t border-slate-200 dark:border-neutral-800 bg-slate-50 dark:bg-neutral-900/60">
        <p className="text-[11px] text-slate-500 dark:text-neutral-500">
          清单只提供版本元数据；安装与启动能力尚未接入本页面，「未安装」是当前真实状态。
        </p>
      </div>
    </section>
  )
}
