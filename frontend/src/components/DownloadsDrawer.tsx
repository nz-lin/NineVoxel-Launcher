import React, { useMemo } from 'react';
import { X, Download, Play, Pause, CheckCircle2 } from 'lucide-react';
import { DownloadTask } from '../types/launcher';

/** 把字节速率格式化成人类可读文本。 */
function formatBytesPerSecond(bytesPerSecond: number): string {
  if (!Number.isFinite(bytesPerSecond) || bytesPerSecond <= 0) return '';
  const units = ['B/s', 'KB/s', 'MB/s', 'GB/s'];
  let value = bytesPerSecond;
  let index = 0;
  while (value >= 1024 && index < units.length - 1) {
    value /= 1024;
    index += 1;
  }
  return `${value.toFixed(index === 0 ? 0 : 1)} ${units[index]}`;
}

/**
 * 把同一次安装的多个计划任务合并成一条。
 *
 * 后端用 operationId 标记「同一次安装」（例如版本文件 + 资源文件），
 * 没有 operationId 的任务保持独立。
 */
function mergeByOperation(tasks: DownloadTask[]): DownloadTask[] {
  const groups = new Map<string, DownloadTask>();
  const order: string[] = [];

  for (const task of tasks) {
    const key = task.operationId || `task:${task.id}`;
    const existing = groups.get(key);
    if (!existing) {
      groups.set(key, { ...task });
      order.push(key);
      continue;
    }

    const totalBytes = existing.totalBytes + task.totalBytes;
    const downloadedBytes = existing.downloadedBytes + task.downloadedBytes;
    const speedBytesPerSecond = (existing.speedBytesPerSecond ?? 0) + (task.speedBytesPerSecond ?? 0);
    const artifactCount = (existing.artifactCount ?? 0) + (task.artifactCount ?? 0);
    const completedCount = (existing.completedCount ?? 0) + (task.completedCount ?? 0);
    let progress = existing.progress;
    if (totalBytes > 0) {
      progress = Math.min(100, Math.round((downloadedBytes / totalBytes) * 100));
    } else if (artifactCount > 0) {
      progress = Math.min(100, Math.round((completedCount / artifactCount) * 100));
    } else {
      progress = Math.max(existing.progress, task.progress);
    }

    const status: DownloadTask['status'] =
      existing.status === 'failed' || task.status === 'failed'
        ? 'failed'
        : existing.status === 'downloading' || task.status === 'downloading'
          ? 'downloading'
          : existing.status === 'paused' || task.status === 'paused'
            ? 'paused'
            : 'completed';

    const representativeId = (existing.status === 'completed' || existing.status === 'failed') &&
      (task.status === 'downloading' || task.status === 'paused')
      ? task.id
      : existing.id;

    groups.set(key, {
      ...existing,
      id: representativeId,
      progress,
      downloadedBytes,
      totalBytes,
      speedBytesPerSecond,
      speed: formatBytesPerSecond(speedBytesPerSecond),
      status,
      artifactCount,
      completedCount,
      errorMessage: existing.errorMessage ?? task.errorMessage,
    });
  }

  return order.map((key) => groups.get(key)!).filter(Boolean);
}

interface DownloadsDrawerProps {
  isOpen: boolean;
  onClose: () => void;
  tasks: DownloadTask[];
  onClearCompleted: () => void;
  onToggleTaskPause: (id: string) => void;
}

export const DownloadsDrawer: React.FC<DownloadsDrawerProps> = ({
  isOpen,
  onClose,
  tasks,
  onClearCompleted,
  onToggleTaskPause
}) => {
  const groupedTasks = useMemo(() => mergeByOperation(tasks), [tasks]);
  const totalSpeed = groupedTasks
    .filter((task) => task.status === 'downloading')
    .reduce((sum, task) => sum + (task.speedBytesPerSecond ?? 0), 0);

  if (!isOpen) return null;

  return (
    <div
      className="fixed inset-0 z-50 flex justify-end bg-black/40 dark:bg-black/60 backdrop-blur-xs animate-in fade-in duration-150"
      onClick={onClose}
    >
      <div
        className="w-full max-w-md bg-white dark:bg-neutral-900 border-l border-slate-200 dark:border-neutral-800 shadow-2xl flex flex-col h-full animate-in slide-in-from-right duration-200"
        onClick={(event) => event.stopPropagation()}
      >
        {/* Header */}
        <div className="p-4 border-b border-slate-200 dark:border-neutral-800 flex items-center justify-between bg-slate-50 dark:bg-neutral-950/60">
          <div className="flex items-center gap-2">
            <Download className="w-4 h-4 text-emerald-600 dark:text-emerald-400" />
            <h2 className="text-sm font-bold text-slate-900 dark:text-neutral-100">下载管理中心</h2>
            <span className="text-[11px] font-mono text-slate-600 dark:text-neutral-500 bg-slate-200 dark:bg-neutral-800 px-1.5 py-0.2 rounded font-semibold">
              {groupedTasks.length} 项
            </span>
          </div>

          <div className="flex items-center gap-2">
            <button
              onClick={onClearCompleted}
              className="text-[11px] text-slate-500 dark:text-neutral-400 hover:text-slate-900 dark:hover:text-white transition-colors cursor-pointer"
            >
              清除已结束
            </button>
            <button
              onClick={onClose}
              className="p-1 rounded text-slate-400 dark:text-neutral-400 hover:text-slate-800 dark:hover:text-white cursor-pointer"
            >
              <X className="w-4 h-4" />
            </button>
          </div>
        </div>

        {/* Task List */}
        <div className="p-4 flex-1 overflow-y-auto space-y-3">
          {groupedTasks.map((task) => (
            <div
              key={task.id}
              className="p-3.5 rounded-xl bg-slate-50 dark:bg-neutral-950/60 border border-slate-200 dark:border-neutral-800 space-y-2.5 shadow-xs"
            >
              <div className="flex items-start justify-between gap-2">
                <div className="min-w-0">
                  <div className="text-xs font-semibold text-slate-900 dark:text-neutral-200 truncate">{task.name}</div>
                  <div className="text-[10px] text-slate-500 dark:text-neutral-500 font-mono mt-0.5 uppercase">
                    分类: {task.category}
                    {task.artifactCount !== undefined && task.artifactCount > 1 ? ` · ${task.completedCount ?? 0}/${task.artifactCount} 项` : ''}
                  </div>
                </div>

                <div className="flex items-center gap-1.5 shrink-0">
                  {task.status === 'completed' ? (
                    <span className="text-[11px] font-medium text-emerald-600 dark:text-emerald-400 flex items-center gap-1">
                      <CheckCircle2 className="w-3.5 h-3.5" /> 已完成
                    </span>
                  ) : (
                    <button
                      onClick={() => onToggleTaskPause(task.id)}
                      className="p-1 rounded bg-slate-200 dark:bg-neutral-800 hover:bg-slate-300 dark:hover:bg-neutral-700 text-slate-700 dark:text-neutral-300 cursor-pointer"
                    >
                      {task.status === 'paused' ? (
                        <Play className="w-3 h-3 fill-current" />
                      ) : (
                        <Pause className="w-3 h-3" />
                      )}
                    </button>
                  )}
                </div>
              </div>

              {/* Progress bar */}
              <div className="w-full bg-slate-200 dark:bg-neutral-800 rounded-full h-1.5 overflow-hidden">
                <div
                  className={`h-full transition-all duration-300 ${
                    task.status === 'completed'
                      ? 'bg-emerald-500'
                      : task.status === 'failed'
                        ? 'bg-rose-500'
                        : task.status === 'paused'
                          ? 'bg-slate-400 dark:bg-neutral-600'
                          : 'bg-emerald-500 animate-pulse'
                  }`}
                  style={{ width: `${Math.min(100, Math.max(0, task.progress))}%` }}
                />
              </div>

              <div className="flex items-center justify-between text-[11px] font-mono text-slate-500 dark:text-neutral-500">
                <span>
                  {task.totalBytes > 0
                    ? `${(task.downloadedBytes / 1024 / 1024).toFixed(1)} / ${(task.totalBytes / 1024 / 1024).toFixed(1)} MB`
                    : `${task.completedCount ?? 0} / ${task.artifactCount ?? 0} 项`}
                </span>
                <span className={task.status === 'failed' ? 'text-rose-500' : ''}>
                  {task.status === 'downloading'
                    ? task.speed || '—'
                    : task.status === 'paused'
                      ? '已暂停'
                      : task.status === 'failed'
                        ? task.errorMessage || '失败'
                        : '校验通过'}
                </span>
              </div>
            </div>
          ))}

          {groupedTasks.length === 0 && (
            <div className="text-center py-20 text-slate-400 dark:text-neutral-600 space-y-2">
              <Download className="w-8 h-8 mx-auto stroke-1" />
              <p className="text-xs">当前没有正在进行的下载任务</p>
            </div>
          )}
        </div>

        {/* 底部状态：并发与总速率均按真实任务统计，没有任务时如实显示 */}
        <div className="p-3 border-t border-slate-200 dark:border-neutral-800 bg-slate-50 dark:bg-neutral-950/70 text-[11px] text-slate-600 dark:text-neutral-400 flex items-center justify-between">
          <span>
            进行中 {groupedTasks.filter((task) => task.status === 'downloading').length} 项 · 共 {groupedTasks.length} 项
          </span>
          <span className="font-mono text-emerald-600 dark:text-emerald-400 font-bold">
            总速率: {totalSpeed > 0 ? formatBytesPerSecond(totalSpeed) : '—'}
          </span>
        </div>
      </div>
    </div>
  );
};
