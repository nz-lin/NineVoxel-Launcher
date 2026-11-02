import React from 'react';
import { X, Download, Play, Pause, Trash2, CheckCircle2 } from 'lucide-react';
import { DownloadTask } from '../types/launcher';

/**
 * 把速度字符串（如 `1.5 MB/s`）解析成字节/秒。
 *
 * 后端的速度是格式化好的文本，统计总速率时必须先还原成数值；
 * 解析不出来的按 0 处理，避免把「—」当成真实值参与求和。
 */
function parseSpeedToBytes(speed: string): number {
  const match = /([\d.]+)\s*(B|KB|MB|GB)\/s/i.exec(speed);
  if (!match) return 0;
  const value = Number.parseFloat(match[1]);
  if (!Number.isFinite(value)) return 0;
  const unit = match[2].toUpperCase();
  const factor = unit === 'GB' ? 1024 ** 3 : unit === 'MB' ? 1024 ** 2 : unit === 'KB' ? 1024 : 1;
  return value * factor;
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
  if (!isOpen) return null;

  return (
    <div className="fixed inset-0 z-50 flex justify-end bg-black/40 dark:bg-black/60 backdrop-blur-xs animate-in fade-in duration-150">
      <div className="w-full max-w-md bg-white dark:bg-neutral-900 border-l border-slate-200 dark:border-neutral-800 shadow-2xl flex flex-col h-full animate-in slide-in-from-right duration-200">
        {/* Header */}
        <div className="p-4 border-b border-slate-200 dark:border-neutral-800 flex items-center justify-between bg-slate-50 dark:bg-neutral-950/60">
          <div className="flex items-center gap-2">
            <Download className="w-4 h-4 text-emerald-600 dark:text-emerald-400" />
            <h2 className="text-sm font-bold text-slate-900 dark:text-neutral-100">下载管理中心</h2>
            <span className="text-[11px] font-mono text-slate-600 dark:text-neutral-500 bg-slate-200 dark:bg-neutral-800 px-1.5 py-0.2 rounded font-semibold">
              {tasks.length} 项
            </span>
          </div>

          <div className="flex items-center gap-2">
            <button
              onClick={onClearCompleted}
              className="text-[11px] text-slate-500 dark:text-neutral-400 hover:text-slate-900 dark:hover:text-white transition-colors cursor-pointer"
            >
              清除已完成
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
          {tasks.map((task) => (
            <div
              key={task.id}
              className="p-3.5 rounded-xl bg-slate-50 dark:bg-neutral-950/60 border border-slate-200 dark:border-neutral-800 space-y-2.5 shadow-xs"
            >
              <div className="flex items-start justify-between gap-2">
                <div className="min-w-0">
                  <div className="text-xs font-semibold text-slate-900 dark:text-neutral-200 truncate">{task.name}</div>
                  <div className="text-[10px] text-slate-500 dark:text-neutral-500 font-mono mt-0.5 uppercase">
                    分类: {task.category}
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
                      : task.status === 'paused'
                      ? 'bg-slate-400 dark:bg-neutral-600'
                      : 'bg-emerald-500 animate-pulse'
                  }`}
                  style={{ width: `${task.progress}%` }}
                />
              </div>

              <div className="flex items-center justify-between text-[11px] font-mono text-slate-500 dark:text-neutral-500">
                <span>
                  {(task.downloadedBytes / 1024 / 1024).toFixed(1)} / {(task.totalBytes / 1024 / 1024).toFixed(1)} MB
                </span>
                <span>{task.status === 'downloading' ? task.speed : task.status === 'paused' ? '已暂停' : '校验通过'}</span>
              </div>
            </div>
          ))}

          {tasks.length === 0 && (
            <div className="text-center py-20 text-slate-400 dark:text-neutral-600 space-y-2">
              <Download className="w-8 h-8 mx-auto stroke-1" />
              <p className="text-xs">当前没有正在进行的下载任务</p>
            </div>
          )}
        </div>

        {/* 底部状态：并发与总速率均按真实任务统计，没有任务时如实显示 */}
        <div className="p-3 border-t border-slate-200 dark:border-neutral-800 bg-slate-50 dark:bg-neutral-950/70 text-[11px] text-slate-600 dark:text-neutral-400 flex items-center justify-between">
          <span>
            进行中 {tasks.filter((task) => task.status === 'downloading').length} 项 · 共 {tasks.length} 项
          </span>
          <span className="font-mono text-emerald-600 dark:text-emerald-400 font-bold">
            总速率: {' '}
            {(() => {
              // 后端不保证每个任务都有速度值，缺失按 0 计算。
              const totalBytesPerSecond = tasks
                .filter((task) => task.status === 'downloading')
                .reduce((sum, task) => sum + parseSpeedToBytes(task.speed), 0);
              if (totalBytesPerSecond <= 0) return '—';
              const megabytes = totalBytesPerSecond / 1024 / 1024;
              return `${megabytes >= 1 ? megabytes.toFixed(2) : (totalBytesPerSecond / 1024).toFixed(1)} ${megabytes >= 1 ? 'MB/s' : 'KB/s'}`;
            })()}
          </span>
        </div>
      </div>
    </div>
  );
};
