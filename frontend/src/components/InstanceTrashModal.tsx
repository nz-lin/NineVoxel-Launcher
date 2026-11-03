import React, { useState } from 'react';
import { AlertCircle, ArchiveRestore, HardDrive, Loader2, RotateCcw, Trash2, X } from 'lucide-react';
import type { InstanceTrashItem } from '../types/launcher';
import { useInstanceTrash } from '../hooks/useInstanceTrash';
import { ConfirmDialog } from './ConfirmDialog';

interface InstanceTrashModalProps {
  isOpen: boolean;
  onClose: () => void;
  onChanged: () => void | Promise<void>;
}

type PendingAction =
  | { kind: 'delete'; item: InstanceTrashItem }
  | { kind: 'empty' };

function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return '0 B';
  if (bytes >= 1024 ** 3) return `${(bytes / 1024 ** 3).toFixed(1)} GB`;
  if (bytes >= 1024 ** 2) return `${(bytes / 1024 ** 2).toFixed(1)} MB`;
  if (bytes >= 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${bytes} B`;
}

function formatDeletedAt(value: number): string {
  if (!value) return '时间未知';
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? '时间未知' : date.toLocaleString('zh-CN');
}

export const InstanceTrashModal: React.FC<InstanceTrashModalProps> = ({ isOpen, onClose, onChanged }) => {
  const trash = useInstanceTrash(isOpen);
  const [pending, setPending] = useState<PendingAction | null>(null);

  const restore = async (item: InstanceTrashItem) => {
    try {
      await trash.restore(item.trashId);
      await onChanged();
    } catch {
      // The hook exposes the backend message in its error banner.
    }
  };

  const confirmPending = async () => {
    const action = pending;
    if (!action) return;
    setPending(null);
    try {
      if (action.kind === 'empty') await trash.empty();
      else await trash.remove(action.item.trashId);
      await onChanged();
    } catch {
      // The hook exposes partial-failure and file-lock messages in its banner.
    }
  };

  const busy = trash.busy !== '';

  return (
    <>
      <div className="fixed inset-0 z-[60] flex items-center justify-center p-4 bg-black/60 dark:bg-black/75 backdrop-blur-sm">
        <div className="w-full max-w-3xl max-h-[86vh] flex flex-col bg-white dark:bg-neutral-900 border border-slate-200 dark:border-neutral-700 rounded-2xl shadow-2xl overflow-hidden">
          <div className="px-5 py-4 flex items-start justify-between gap-4 border-b border-slate-200 dark:border-neutral-800">
            <div className="flex items-start gap-3">
              <div className="w-9 h-9 rounded-xl bg-amber-500/15 text-amber-600 dark:text-amber-400 flex items-center justify-center shrink-0">
                <ArchiveRestore className="w-4 h-4" />
              </div>
              <div>
                <h2 className="text-sm font-bold text-slate-900 dark:text-neutral-100">实例回收站</h2>
                <p className="mt-1 text-xs text-slate-500 dark:text-neutral-400">
                  移入回收站的实例仍保留在磁盘上，可还原或永久删除。
                </p>
              </div>
            </div>
            <div className="flex items-center gap-2 shrink-0">
              {trash.items.length > 0 && (
                <button
                  type="button"
                  disabled={busy}
                  onClick={() => setPending({ kind: 'empty' })}
                  className="px-3 py-1.5 rounded-lg text-xs font-semibold text-red-600 dark:text-red-400 hover:bg-red-500/10 disabled:opacity-40 transition-colors cursor-pointer"
                >
                  清空回收站
                </button>
              )}
              <button
                type="button"
                onClick={onClose}
                aria-label="关闭回收站"
                className="p-1.5 rounded-lg text-slate-400 hover:text-slate-800 dark:hover:text-white hover:bg-slate-100 dark:hover:bg-neutral-800 transition-colors cursor-pointer"
              >
                <X className="w-4 h-4" />
              </button>
            </div>
          </div>

          <div className="flex-1 overflow-y-auto p-5 space-y-4">
            {trash.error && (
              <div role="alert" className="flex items-start gap-2 rounded-xl border border-red-500/30 bg-red-500/10 px-3 py-2.5 text-xs text-red-700 dark:text-red-300">
                <AlertCircle className="w-4 h-4 shrink-0 mt-0.5" />
                <span className="leading-relaxed">{trash.error}</span>
              </div>
            )}

            {trash.loading ? (
              <div className="py-16 flex flex-col items-center gap-3 text-xs text-slate-500 dark:text-neutral-400">
                <Loader2 className="w-5 h-5 animate-spin text-emerald-500" />
                正在读取回收站…
              </div>
            ) : trash.items.length === 0 ? (
              <div className="py-16 flex flex-col items-center gap-3 text-center">
                <div className="w-12 h-12 rounded-2xl bg-slate-100 dark:bg-neutral-800 flex items-center justify-center text-slate-400 dark:text-neutral-500">
                  <ArchiveRestore className="w-6 h-6" />
                </div>
                <div>
                  <h3 className="text-sm font-semibold text-slate-800 dark:text-neutral-200">回收站是空的</h3>
                  <p className="mt-1 text-xs text-slate-500 dark:text-neutral-500">删除的实例会先出现在这里，不会立即永久丢失。</p>
                </div>
              </div>
            ) : (
              <div className="space-y-3">
                {trash.items.map((item) => {
                  const restoring = trash.busy === `restore:${item.trashId}`;
                  const deleting = trash.busy === `delete:${item.trashId}`;
                  return (
                    <div key={item.trashId} className="rounded-xl border border-slate-200 dark:border-neutral-800 bg-slate-50/70 dark:bg-neutral-950/40 p-4">
                      <div className="flex flex-col sm:flex-row sm:items-start justify-between gap-3">
                        <div className="flex items-start gap-3 min-w-0">
                          <div className="w-10 h-10 rounded-xl bg-white dark:bg-neutral-800 border border-slate-200 dark:border-neutral-700 flex items-center justify-center text-xl shrink-0">
                            {item.icon || '🟩'}
                          </div>
                          <div className="min-w-0">
                            <h3 className="text-sm font-bold text-slate-900 dark:text-neutral-100 truncate">
                              {item.name || '无法识别的实例'}
                            </h3>
                            <div className="mt-1 flex flex-wrap items-center gap-x-2 gap-y-1 text-[11px] text-slate-500 dark:text-neutral-400">
                              {item.mcVersion && <span>MC {item.mcVersion}</span>}
                              {item.loader && <span className="font-mono uppercase">{item.loader}</span>}
                              <span className="inline-flex items-center gap-1"><HardDrive className="w-3 h-3" />{formatBytes(item.sizeBytes)}</span>
                              <span>{formatDeletedAt(item.deletedAtMs)}</span>
                            </div>
                            {item.errorMessage && (
                              <p className="mt-2 text-[11px] text-amber-700 dark:text-amber-400">{item.errorMessage}</p>
                            )}
                          </div>
                        </div>

                        <div className="flex items-center gap-2 shrink-0 sm:ml-4">
                          <button
                            type="button"
                            disabled={busy || !item.restorable}
                            onClick={() => void restore(item)}
                            className="px-3 py-1.5 rounded-lg bg-emerald-500/15 hover:bg-emerald-500 text-emerald-700 dark:text-emerald-300 hover:text-neutral-950 text-xs font-bold flex items-center gap-1.5 disabled:opacity-35 disabled:hover:bg-emerald-500/15 disabled:hover:text-emerald-700 disabled:cursor-not-allowed transition-colors cursor-pointer"
                            title={item.restorable ? '还原实例' : '该条目缺少可恢复的实例清单'}
                          >
                            {restoring ? <Loader2 className="w-3.5 h-3.5 animate-spin" /> : <RotateCcw className="w-3.5 h-3.5" />}
                            还原
                          </button>
                          <button
                            type="button"
                            disabled={busy}
                            onClick={() => setPending({ kind: 'delete', item })}
                            className="p-1.5 rounded-lg text-slate-500 dark:text-neutral-400 hover:text-red-600 dark:hover:text-red-400 hover:bg-red-500/10 disabled:opacity-35 transition-colors cursor-pointer"
                            title="永久删除"
                          >
                            {deleting ? <Loader2 className="w-3.5 h-3.5 animate-spin" /> : <Trash2 className="w-3.5 h-3.5" />}
                          </button>
                        </div>
                      </div>
                    </div>
                  );
                })}
              </div>
            )}
          </div>
        </div>
      </div>

      {pending && (
        <ConfirmDialog
          title={pending.kind === 'empty' ? '清空实例回收站' : '永久删除实例'}
          message={pending.kind === 'empty'
            ? `确认永久删除回收站中的 ${trash.items.length} 个实例？游戏存档和配置将无法恢复。`
            : `确认永久删除「${pending.item.name || '该实例'}」？游戏存档和配置将无法恢复。`}
          confirmLabel={pending.kind === 'empty' ? '永久清空' : '永久删除'}
          danger
          onConfirm={() => { void confirmPending(); }}
          onCancel={() => setPending(null)}
        />
      )}
    </>
  );
};
