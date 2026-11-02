import React from 'react';
import { Window } from '@wailsio/runtime';
import { Minus, Square, X, Download, Terminal, Cpu, Coffee, Sun, Moon } from 'lucide-react';
import { Instance, Account, DownloadTask } from '../types/launcher';

interface TitleBarProps {
  currentInstance?: Instance;
  currentAccount: Account;
  downloadTasks: DownloadTask[];
  onOpenDownloads: () => void;
  onOpenConsole: () => void;
  onOpenJavaManager?: () => void;
  onToggleMaximize?: () => void;
  isMaximized?: boolean;
  theme: 'dark' | 'light';
  onToggleTheme: () => void;
  /** 启动器版本号，来自后端 `AppService.Snapshot()`；读取失败时为 undefined。 */
  appVersion?: string;
}

export const TitleBar: React.FC<TitleBarProps> = ({
  currentInstance,
  currentAccount,
  downloadTasks,
  onOpenDownloads,
  onOpenConsole,
  onOpenJavaManager,
  onToggleMaximize,
  isMaximized = false,
  theme,
  onToggleTheme,
  appVersion
}) => {
  const activeDownloads = downloadTasks.filter(t => t.status === 'downloading');

  return (
    <div className="h-10 bg-white/90 dark:bg-neutral-950/80 backdrop-blur-md border-b border-slate-200/80 dark:border-neutral-800/60 select-none flex items-center justify-between px-3 text-xs text-slate-700 dark:text-neutral-300 z-50 shrink-0 transition-colors duration-150 [--wails-draggable:drag]">
      {/* Left: Brand & Instance Breadcrumb */}
      <div className="flex items-center gap-3">
        <div className="flex items-center gap-2">
          {/* Ninevoxel stylized logo: 3x3 cubic voxel cluster */}
          <div className="w-5 h-5 bg-gradient-to-br from-emerald-500 to-teal-600 rounded flex items-center justify-center shadow-xs shadow-emerald-500/20">
            <div className="grid grid-cols-2 gap-0.5 w-3 h-3">
              <span className="bg-white/90 dark:bg-neutral-950/80 rounded-[1px]"></span>
              <span className="bg-white/40 dark:bg-neutral-950/40 rounded-[1px]"></span>
              <span className="bg-white/40 dark:bg-neutral-950/40 rounded-[1px]"></span>
              <span className="bg-white/90 rounded-[1px]"></span>
            </div>
          </div>
          <span className="font-bold tracking-tight text-slate-900 dark:text-neutral-100 text-sm">Ninevoxel</span>
          <span className="text-slate-400 dark:text-neutral-500 font-mono text-[11px]">
            {/* 版本号来自后端 AppService.Snapshot()；取不到时显示占位，不写死假版本。 */}
            {appVersion ? `v${appVersion}` : 'v—'}
          </span>
        </div>

        <div className="h-3.5 w-px bg-slate-200 dark:bg-neutral-800" />

        <div className="flex items-center gap-2 text-slate-500 dark:text-neutral-400 text-xs">
          <span>当前实例</span>
          <span className="text-slate-300 dark:text-neutral-600">/</span>
          <span className="text-slate-900 dark:text-neutral-200 font-medium truncate max-w-[220px]">
            {currentInstance?.name ?? '尚无实例'}
          </span>
          <span className="text-slate-500 dark:text-neutral-500 font-mono text-[11px] bg-slate-100 dark:bg-neutral-900 border border-slate-200 dark:border-neutral-800 px-1.5 py-0.5 rounded">
            {currentInstance?.mcVersion ?? '—'}
          </span>
        </div>
      </div>

      {/* Middle/Right Quick Indicators & Window Controls */}
      <div className="flex items-center gap-2">
        {/* System RAM info */}
        <div className="hidden lg:flex items-center gap-1.5 px-2 py-1 rounded bg-slate-100 dark:bg-neutral-900/60 border border-slate-200 dark:border-neutral-800/50 text-slate-600 dark:text-neutral-400 text-[11px]">
          <Cpu className="w-3.5 h-3.5 text-emerald-600 dark:text-emerald-400" />
          <span>分配内存</span>
          <span className="text-slate-900 dark:text-neutral-200 font-mono font-medium">{currentInstance ? `${currentInstance.memoryMb} MB` : '—'}</span>
        </div>

        {/* Java Runtime Manager quick toggle */}
        {onOpenJavaManager && (
          <button
            onClick={onOpenJavaManager}
            className="hidden md:flex items-center gap-1.5 px-2 py-1 rounded bg-slate-100 dark:bg-neutral-900/60 border border-slate-200 dark:border-neutral-800/50 hover:bg-slate-200 dark:hover:bg-neutral-800/80 hover:text-slate-900 dark:hover:text-neutral-100 text-slate-600 dark:text-neutral-300 transition-colors text-[11px] cursor-pointer [--wails-draggable:no-drag]"
            title="管理本地 javaruntime/ 运行时"
          >
            <Coffee className="w-3.5 h-3.5 text-amber-500 dark:text-amber-400" />
            <span>Java 环境</span>
          </button>
        )}

        {/* Download Manager Toggle */}
        <button
          onClick={onOpenDownloads}
          className={`flex items-center gap-1.5 px-2.5 py-1 rounded transition-colors text-xs border cursor-pointer [--wails-draggable:no-drag] ${
            activeDownloads.length > 0
              ? 'bg-emerald-500/10 border-emerald-500/30 text-emerald-700 dark:text-emerald-300 animate-pulse'
              : 'bg-slate-100 dark:bg-neutral-900/60 border-slate-200 dark:border-neutral-800/60 text-slate-700 dark:text-neutral-300 hover:bg-slate-200 dark:hover:bg-neutral-800/80 hover:text-slate-900 dark:hover:text-neutral-100'
          }`}
          title="下载任务中心"
        >
          <Download className="w-3.5 h-3.5" />
          <span>下载</span>
          {activeDownloads.length > 0 && (
            <span className="font-mono text-[10px] bg-emerald-500 text-neutral-950 font-bold px-1 rounded-full">
              {activeDownloads.length}
            </span>
          )}
        </button>

        {/* Console / Log Viewer */}
        <button
          onClick={onOpenConsole}
          className="flex items-center gap-1.5 px-2.5 py-1 rounded bg-slate-100 dark:bg-neutral-900/60 border border-slate-200 dark:border-neutral-800/60 text-slate-700 dark:text-neutral-300 hover:bg-slate-200 dark:hover:bg-neutral-800/80 hover:text-slate-900 dark:hover:text-neutral-100 transition-colors text-xs cursor-pointer [--wails-draggable:no-drag]"
          title="游戏运行终端与崩溃诊断"
        >
          <Terminal className="w-3.5 h-3.5 text-slate-500 dark:text-neutral-400" />
          <span>控制台</span>
        </button>

        {/* Light / Dark Mode Toggle Button */}
        <button
          onClick={onToggleTheme}
          className="flex items-center justify-center w-7 h-7 rounded-lg bg-slate-100 dark:bg-neutral-900/60 border border-slate-200 dark:border-neutral-800/60 text-slate-600 dark:text-neutral-300 hover:bg-slate-200 dark:hover:bg-neutral-800/80 hover:text-slate-900 dark:hover:text-neutral-100 transition-colors cursor-pointer [--wails-draggable:no-drag]"
          title={theme === 'dark' ? '切换为浅色主题 (Light)' : '切换为深色主题 (Dark)'}
          aria-label="切换主题外观"
        >
          {theme === 'dark' ? (
            <Sun className="w-3.5 h-3.5 text-amber-400" />
          ) : (
            <Moon className="w-3.5 h-3.5 text-indigo-500" />
          )}
        </button>

        <div className="h-3.5 w-px bg-slate-200 dark:bg-neutral-800 mx-0.5" />

        {/* Window controls (Minimize, Maximize, Close) */}
        <div className="flex items-center">
          <button
            onClick={() => void Window.Minimise()}
            className="w-7 h-7 flex items-center justify-center text-slate-400 dark:text-neutral-400 hover:text-slate-700 dark:hover:text-neutral-200 hover:bg-slate-200/80 dark:hover:bg-neutral-800/70 rounded transition-colors [--wails-draggable:no-drag]"
            title="最小化"
          >
            <Minus className="w-3.5 h-3.5" />
          </button>
          <button
            onClick={() => {
              void Window.ToggleMaximise();
              onToggleMaximize?.();
            }}
            className="w-7 h-7 flex items-center justify-center text-slate-400 dark:text-neutral-400 hover:text-slate-700 dark:hover:text-neutral-200 hover:bg-slate-200/80 dark:hover:bg-neutral-800/70 rounded transition-colors [--wails-draggable:no-drag]"
            title={isMaximized ? "还原" : "最大化"}
          >
            <Square className="w-3 h-3" />
          </button>
          <button
            onClick={() => void Window.Close()}
            className="w-7 h-7 flex items-center justify-center text-slate-400 dark:text-neutral-400 hover:text-red-600 dark:hover:text-red-400 hover:bg-red-500/10 dark:hover:bg-red-500/20 rounded transition-colors [--wails-draggable:no-drag]"
            title="关闭启动器"
          >
            <X className="w-3.5 h-3.5" />
          </button>
        </div>
      </div>
    </div>
  );
};
