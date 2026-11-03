import React, { useMemo, useState } from 'react';
import { AlertTriangle, CalendarDays, Check, Loader2, RefreshCw, Search, Tag, X } from 'lucide-react';
import { useVersions, type VersionTypeFilter } from '../hooks/useVersions';
import type { VersionSummary, VersionType } from '../types/version';

interface VersionSelectorModalProps {
  selectedVersion: string;
  onSelect: (version: string) => void;
  onClose: () => void;
}

type SelectorTab = 'all' | 'release' | 'snapshot';

const TAB_FILTERS: Record<SelectorTab, VersionTypeFilter> = {
  all: 'release,snapshot,old_beta,old_alpha',
  release: 'release',
  snapshot: 'snapshot',
};

const TABS: Array<{ id: SelectorTab; label: string }> = [
  { id: 'all', label: '全部' },
  { id: 'release', label: '正式版' },
  { id: 'snapshot', label: '快照' },
];

const TYPE_LABELS: Record<VersionType, string> = {
  release: '正式版',
  snapshot: '快照',
  old_beta: '古老 Beta',
  old_alpha: '古老 Alpha',
};

function formatReleaseDate(raw: string): string {
  if (!raw) return '—';
  const date = new Date(raw);
  if (Number.isNaN(date.getTime())) return '—';
  const pad = (value: number) => String(value).padStart(2, '0');
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}`;
}

function describeCompat(item: VersionSummary): { text: string; ok: boolean } {
  const compat = item.compat;
  if (!compat || compat.evaluated !== true) return { text: '兼容性未评估', ok: false };
  if (compat.launchSupported) return { text: '可启动', ok: true };
  if (compat.installSupported) return { text: '可安装（不可启动）', ok: false };
  if (compat.metadataSupported) return { text: '仅元数据可读', ok: false };
  return { text: '不支持', ok: false };
}

export const VersionSelectorModal: React.FC<VersionSelectorModalProps> = ({
  selectedVersion,
  onSelect,
  onClose,
}) => {
  const catalog = useVersions(200, 'release');
  const [tab, setTab] = useState<SelectorTab>('release');
  const [search, setSearch] = useState('');
  const [pending, setPending] = useState(selectedVersion);

  const filtered = useMemo(() => {
    const keyword = search.trim().toLowerCase();
    if (!keyword) return catalog.items;
    return catalog.items.filter((item) => item.id.toLowerCase().includes(keyword));
  }, [catalog.items, search]);

  const switchTab = (next: SelectorTab) => {
    setTab(next);
    catalog.setTypeFilter(TAB_FILTERS[next]);
  };

  return (
    <div className="fixed inset-0 z-[60] flex items-center justify-center p-4 bg-black/60 dark:bg-black/80 backdrop-blur-sm animate-in fade-in duration-150">
      <div className="w-full max-w-2xl max-h-[85vh] bg-white dark:bg-neutral-900 border border-slate-200 dark:border-neutral-700 rounded-2xl shadow-2xl flex flex-col overflow-hidden">
        <div className="px-5 py-4 border-b border-slate-200 dark:border-neutral-800 flex items-center justify-between bg-slate-50 dark:bg-neutral-950/60">
          <div>
            <h3 className="text-sm font-bold text-slate-900 dark:text-neutral-100">选择 Minecraft 版本</h3>
            <p className="mt-0.5 text-[11px] text-slate-500 dark:text-neutral-400">
              数据来自官方 version_manifest_v2 清单
            </p>
          </div>
          <button
            type="button"
            onClick={onClose}
            aria-label="关闭版本选择器"
            className="p-1.5 rounded-lg text-slate-400 hover:text-slate-800 dark:hover:text-white hover:bg-slate-100 dark:hover:bg-neutral-800 transition-colors cursor-pointer"
          >
            <X className="w-4 h-4" />
          </button>
        </div>

        <div className="px-5 py-3 border-b border-slate-200 dark:border-neutral-800 space-y-3">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <div className="flex items-center gap-1.5">
              {TABS.map((item) => (
                <button
                  key={item.id}
                  type="button"
                  onClick={() => switchTab(item.id)}
                  className={`px-3 py-1.5 rounded-lg text-xs font-medium border transition-colors cursor-pointer ${
                    tab === item.id
                      ? 'bg-emerald-500/10 border-emerald-500/40 text-emerald-700 dark:text-emerald-400 font-bold'
                      : 'border-slate-200 dark:border-neutral-700 text-slate-600 dark:text-neutral-400 hover:bg-slate-100 dark:hover:bg-neutral-800'
                  }`}
                >
                  {item.label}
                </button>
              ))}
            </div>
            <button
              type="button"
              onClick={() => catalog.reload({ forceRefresh: true })}
              disabled={catalog.loading}
              className="inline-flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-xs font-medium border border-slate-200 dark:border-neutral-700 text-slate-700 dark:text-neutral-300 hover:bg-slate-100 dark:hover:bg-neutral-800 disabled:opacity-50 transition-colors cursor-pointer"
            >
              {catalog.loading ? <Loader2 className="w-3.5 h-3.5 animate-spin" /> : <RefreshCw className="w-3.5 h-3.5" />}
              刷新清单
            </button>
          </div>

          <div className="relative">
            <Search className="w-4 h-4 text-slate-400 dark:text-neutral-500 absolute left-3 top-1/2 -translate-y-1/2" />
            <input
              value={search}
              onChange={(event) => setSearch(event.target.value)}
              placeholder="搜索版本号，例如 1.21.4 或 25w02a"
              className="w-full pl-9 pr-3 py-2 rounded-lg bg-slate-50 dark:bg-neutral-950 border border-slate-300 dark:border-neutral-800 text-xs text-slate-800 dark:text-neutral-200 focus:outline-none focus:border-emerald-500"
            />
          </div>
          {catalog.stale && catalog.items.length > 0 && (
            <p className="text-[11px] text-amber-600 dark:text-amber-400">当前显示缓存清单，安装仍需联网获取缺失文件。</p>
          )}
        </div>

        <div className="flex-1 overflow-y-auto min-h-[240px]">
          {catalog.errorCode ? (
            <div className="p-5">
              <div className="rounded-xl border border-rose-500/30 bg-rose-500/5 p-4 text-xs">
                <div className="flex items-center gap-2 font-semibold text-rose-600 dark:text-rose-400">
                  <AlertTriangle className="w-4 h-4" />
                  读取版本清单失败（{catalog.errorCode}）
                </div>
                <p className="mt-1.5 text-slate-600 dark:text-neutral-400">{catalog.errorMessage}</p>
              </div>
            </div>
          ) : catalog.loading && catalog.items.length === 0 ? (
            <div className="p-5 space-y-2">
              {Array.from({ length: 6 }).map((_, index) => (
                <div key={index} className="h-10 rounded-lg bg-slate-100 dark:bg-neutral-800/60 animate-pulse" />
              ))}
            </div>
          ) : filtered.length === 0 ? (
            <div className="p-10 text-center text-xs text-slate-500 dark:text-neutral-400">
              当前分类或搜索条件下没有版本。
            </div>
          ) : (
            <ul className="divide-y divide-slate-100 dark:divide-neutral-800">
              {filtered.map((item) => {
                const active = pending === item.id;
                const compat = describeCompat(item);
                return (
                  <li key={item.id}>
                    <button
                      type="button"
                      onClick={() => setPending(item.id)}
                      onDoubleClick={() => {
                        onSelect(item.id);
                        onClose();
                      }}
                      className={`w-full px-5 py-3 text-left flex items-center gap-3 transition-colors cursor-pointer ${
                        active ? 'bg-emerald-500/10' : 'hover:bg-slate-50 dark:hover:bg-neutral-800/40'
                      }`}
                    >
                      <span className={`w-4 h-4 rounded-full border flex items-center justify-center shrink-0 ${
                        active ? 'border-emerald-500 bg-emerald-500 text-neutral-950' : 'border-slate-300 dark:border-neutral-600'
                      }`}>
                        {active && <Check className="w-3 h-3" />}
                      </span>
                      <span className="font-mono text-sm font-semibold text-slate-900 dark:text-neutral-100 min-w-0 truncate">
                        {item.id}
                      </span>
                      <span className="inline-flex items-center gap-1 text-[11px] text-slate-600 dark:text-neutral-400">
                        <Tag className="w-3 h-3" />
                        {TYPE_LABELS[item.type] ?? item.type}
                      </span>
                      <span className="inline-flex items-center gap-1 text-[11px] text-slate-500 dark:text-neutral-500">
                        <CalendarDays className="w-3 h-3" />
                        {formatReleaseDate(item.releaseTime)}
                      </span>
                      <span className={`ml-auto text-[10px] px-2 py-0.5 rounded-md border shrink-0 ${
                        compat.ok
                          ? 'border-emerald-500/40 text-emerald-700 dark:text-emerald-400 bg-emerald-500/10'
                          : 'border-slate-200 dark:border-neutral-700 text-slate-500 dark:text-neutral-500'
                      }`}>
                        {compat.text}
                      </span>
                    </button>
                  </li>
                );
              })}
            </ul>
          )}
        </div>

        <div className="px-5 py-3 border-t border-slate-200 dark:border-neutral-800 bg-slate-50 dark:bg-neutral-950/60 flex items-center justify-between gap-3">
          <span className="text-[11px] text-slate-500 dark:text-neutral-400 font-mono">
            {pending ? `已选择：${pending}` : '尚未选择版本'}
          </span>
          <div className="flex items-center gap-2">
            <button
              type="button"
              onClick={onClose}
              className="px-3 py-1.5 rounded-lg text-xs font-semibold text-slate-700 dark:text-neutral-300 hover:bg-slate-200 dark:hover:bg-neutral-800 transition-colors cursor-pointer"
            >
              取消
            </button>
            <button
              type="button"
              disabled={!pending}
              onClick={() => {
                onSelect(pending);
                onClose();
              }}
              className="px-4 py-1.5 rounded-lg bg-emerald-500 hover:bg-emerald-400 text-neutral-950 font-bold text-xs transition-colors cursor-pointer disabled:opacity-40 disabled:cursor-not-allowed"
            >
              使用此版本
            </button>
          </div>
        </div>
      </div>
    </div>
  );
};
