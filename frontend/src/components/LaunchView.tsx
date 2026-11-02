import ninevoxelHero from '../assets/images/ninevoxel_hero_bg_1793533498523.jpg';
import React, { useState } from 'react';
import {
  Play,
  CheckCircle2,
  AlertCircle,
  FolderOpen,
  Sliders,
  ChevronDown,
  Layers,
  Sparkles,
  ExternalLink,
  Square
} from 'lucide-react';
import { Instance, Account } from '../types/launcher';

interface LaunchViewProps {
  currentInstance: Instance;
  allInstances: Instance[];
  currentAccount: Account;
  onSelectInstance: (instance: Instance) => void;
  onLaunchGame: () => void;
  onKillGame: () => void;
  launchState: 'ready' | 'preparing' | 'checking_assets' | 'starting_jvm' | 'running';
  onNavigateTab: (tab: any) => void;
  onOpenFolder: (path: string) => void;
}

export const LaunchView: React.FC<LaunchViewProps> = ({
  currentInstance,
  allInstances,
  currentAccount,
  onSelectInstance,
  onLaunchGame,
  onKillGame,
  launchState,
  onNavigateTab,
  onOpenFolder
}) => {
  const [selectorOpen, setSelectorOpen] = useState(false);

  const isLaunching = launchState === 'preparing' || launchState === 'checking_assets' || launchState === 'starting_jvm';
  const isRunning = launchState === 'running';

  const getStatusText = () => {
    switch (launchState) {
      case 'preparing':
        return '正在准备 Java 运行环境...';
      case 'checking_assets':
        return '校验游戏资源与模组依赖...';
      case 'starting_jvm':
        return '构建 JVM 参数并启动核心...';
      case 'running':
        return 'Minecraft 正在运行中';
      default:
        return '启动游戏';
    }
  };

  const newsItems = [
    {
      id: 'news-1',
      title: 'Minecraft 1.21.4 正式版现已发布',
      date: '2024-12-03',
      category: '官方更新',
      summary: '重锤战斗平衡性调整、试炼密室更新以及大量漏洞修复。'
    },
    {
      id: 'news-2',
      title: 'Fabric Loader 0.16.10 带来显著启动加速',
      date: '2025-01-14',
      category: '模组生态',
      summary: '类加载管线重构，大型模组包平均冷启动时间缩短约 35%。'
    },
    {
      id: 'news-3',
      title: 'Ninevoxel 核心云端镜像同步提速',
      date: '2025-02-18',
      category: '启动器快讯',
      summary: '全节点接入 BMCLAPI 智能路由，资源文件下载支持断点续传。'
    }
  ];

  return (
    <div className="flex-1 overflow-y-auto bg-slate-100 dark:bg-neutral-950 flex flex-col transition-colors duration-150">
      {/* Hero Banner Area */}
      <div className="relative h-[340px] w-full overflow-hidden shrink-0 select-none">
        {/* Background image with fallback */}
        <img
          src={currentInstance.bannerUrl || ninevoxelHero}
          alt={currentInstance.name}
          referrerPolicy="no-referrer"
          className="w-full h-full object-cover object-center filter brightness-[0.8] contrast-[1.05]"
        />

        {/* Gradient Scrim for WCAG AA readability */}
        <div className="absolute inset-0 bg-gradient-to-t from-slate-100/90 dark:from-neutral-950 via-slate-900/60 dark:via-neutral-950/60 to-transparent" />
        <div className="absolute inset-0 bg-gradient-to-r from-slate-950/80 dark:from-neutral-950/80 via-transparent to-transparent" />

        {/* Top Floating Selector Overlay */}
        <div className="absolute top-5 left-8 z-30">
          <div className="relative">
            <button
              onClick={() => setSelectorOpen(!selectorOpen)}
              className="flex items-center gap-2.5 px-3 py-1.5 rounded-lg bg-white/90 dark:bg-neutral-900/80 hover:bg-white dark:hover:bg-neutral-900 border border-slate-200 dark:border-neutral-700/80 backdrop-blur-md text-xs text-slate-800 dark:text-neutral-200 transition-all shadow-md group cursor-pointer"
            >
              <span className="text-base">{currentInstance.icon}</span>
              <span className="font-semibold text-slate-900 dark:text-neutral-100">{currentInstance.name}</span>
              <span className="text-slate-400 dark:text-neutral-500">|</span>
              <span className="text-emerald-600 dark:text-emerald-400 font-mono text-[11px] uppercase font-bold">{currentInstance.loader}</span>
              <ChevronDown className={`w-3.5 h-3.5 text-slate-400 dark:text-neutral-400 group-hover:text-slate-700 dark:group-hover:text-neutral-200 transition-transform ${selectorOpen ? 'rotate-180' : ''}`} />
            </button>

            {/* Quick Switch Dropdown */}
            {selectorOpen && (
              <div className="absolute top-full left-0 mt-2 w-80 bg-white/95 dark:bg-neutral-900/95 border border-slate-200 dark:border-neutral-700/80 rounded-xl shadow-2xl backdrop-blur-xl p-2 z-50 animate-in fade-in zoom-in-95 duration-150">
                <div className="px-2 py-1.5 text-[11px] font-semibold text-slate-500 dark:text-neutral-400 uppercase tracking-wider flex items-center justify-between">
                  <span>选择游戏实例</span>
                  <button
                    onClick={() => {
                      setSelectorOpen(false);
                      onNavigateTab('instances');
                    }}
                    className="text-emerald-600 dark:text-emerald-400 hover:underline cursor-pointer"
                  >
                    查看全部 ({allInstances.length})
                  </button>
                </div>
                <div className="space-y-1 mt-1 max-h-60 overflow-y-auto">
                  {allInstances.map((inst) => (
                    <button
                      key={inst.id}
                      onClick={() => {
                        onSelectInstance(inst);
                        setSelectorOpen(false);
                      }}
                      className={`w-full flex items-center gap-2.5 px-2.5 py-2 rounded-lg text-left text-xs transition-colors cursor-pointer ${
                        inst.id === currentInstance.id
                          ? 'bg-emerald-500/15 text-emerald-800 dark:text-emerald-300 font-medium border border-emerald-500/30'
                          : 'text-slate-700 dark:text-neutral-300 hover:bg-slate-100 dark:hover:bg-neutral-800/80 hover:text-slate-900 dark:hover:text-neutral-100'
                      }`}
                    >
                      <span className="text-sm">{inst.icon}</span>
                      <div className="min-w-0 flex-1">
                        <div className="truncate font-medium">{inst.name}</div>
                        <div className="text-[11px] text-slate-500 dark:text-neutral-500 flex items-center gap-1.5 mt-0.5">
                          <span>{inst.mcVersion}</span>
                          <span>·</span>
                          <span className="capitalize">{inst.loader}</span>
                          <span>·</span>
                          <span>{inst.modsCount} 模组</span>
                        </div>
                      </div>
                      {inst.id === currentInstance.id && (
                        <CheckCircle2 className="w-3.5 h-3.5 text-emerald-600 dark:text-emerald-400 shrink-0" />
                      )}
                    </button>
                  ))}
                </div>
              </div>
            )}
          </div>
        </div>

        {/* Hero Content Information */}
        <div className="absolute bottom-6 left-8 right-8 flex flex-col md:flex-row md:items-end justify-between gap-6 z-20">
          <div className="space-y-2 max-w-2xl">
            <div className="flex items-center gap-2 text-xs text-neutral-300">
              <span className="font-semibold text-emerald-400 uppercase tracking-wide">
                {currentInstance.loader.toUpperCase()} LOADER {currentInstance.loaderVersion}
              </span>
              <span>·</span>
              <span className="text-neutral-200">MC {currentInstance.mcVersion}</span>
              <span>·</span>
              <span>累计游玩 {Math.floor(currentInstance.playTimeMinutes / 60)}小时{currentInstance.playTimeMinutes % 60}分</span>
            </div>

            <h1 className="text-2xl md:text-3xl font-extrabold text-white tracking-tight text-balance drop-shadow-sm">
              {currentInstance.name}
            </h1>

            <p className="text-xs md:text-sm text-neutral-200 line-clamp-2 leading-relaxed drop-shadow-xs">
              {currentInstance.description}
            </p>

            {/* Micro spec tags */}
            <div className="flex flex-wrap items-center gap-3 pt-1 text-xs text-neutral-300">
              <span className="text-neutral-100 font-medium">Java: {currentInstance.javaVersion.split(' ')[0]} {currentInstance.javaVersion.split(' ')[1]}</span>
              <span>/</span>
              <span className="text-neutral-100 font-medium">内存: {currentInstance.memoryMb} MB</span>
              <span>/</span>
              <span className="text-neutral-100 font-medium">已装载 {currentInstance.modsCount} 个模组</span>
              <span>/</span>
              <button
                onClick={() => onOpenFolder(currentInstance.folderPath)}
                className="text-neutral-200 hover:text-white flex items-center gap-1 transition-colors cursor-pointer underline underline-offset-2"
              >
                <FolderOpen className="w-3 h-3 text-neutral-300" /> 打开目录
              </button>
            </div>
          </div>

          {/* Primary Launch Action Area */}
          <div className="flex flex-col items-end gap-2 shrink-0">
            {isRunning ? (
              <div className="flex items-center gap-2">
                <button
                  onClick={() => onNavigateTab('console')}
                  className="px-5 py-3 rounded-xl bg-slate-900/90 dark:bg-neutral-800 hover:bg-slate-800 dark:hover:bg-neutral-700 text-white font-semibold text-sm transition-all border border-slate-700 dark:border-neutral-700 flex items-center gap-2 shadow-lg cursor-pointer"
                >
                  <Sliders className="w-4 h-4 text-emerald-400" />
                  <span>控制台 (运行中)</span>
                </button>
                <button
                  onClick={onKillGame}
                  className="px-4 py-3 rounded-xl bg-red-600 hover:bg-red-500 text-white font-semibold text-sm transition-all flex items-center gap-2 shadow-lg cursor-pointer"
                  title="强制终止游戏进程"
                >
                  <Square className="w-4 h-4 fill-white" />
                  <span>结束进程</span>
                </button>
              </div>
            ) : (
              <button
                onClick={onLaunchGame}
                disabled={isLaunching}
                className={`group relative px-8 py-3.5 rounded-xl font-bold text-sm tracking-wide transition-all shadow-xl flex items-center gap-3 cursor-pointer ${
                  isLaunching
                    ? 'bg-amber-600 text-white cursor-wait'
                    : 'bg-emerald-500 hover:bg-emerald-400 active:scale-98 text-neutral-950 shadow-emerald-500/25 hover:shadow-emerald-500/40'
                }`}
              >
                {isLaunching ? (
                  <div className="w-4 h-4 border-2 border-white border-t-transparent rounded-full animate-spin" />
                ) : (
                  <Play className="w-5 h-5 fill-neutral-950 group-hover:scale-110 transition-transform" />
                )}
                <span className="text-base">{getStatusText()}</span>
              </button>
            )}

            <div className="text-[11px] text-neutral-300 dark:text-neutral-400 flex items-center gap-1.5 drop-shadow-xs">
              <span>登录身份:</span>
              <span className="font-semibold text-white dark:text-neutral-200">{currentAccount.username}</span>
              <span className="text-neutral-300 dark:text-neutral-500">({currentAccount.type === 'microsoft' ? '微软' : '离线'})</span>
            </div>
          </div>
        </div>
      </div>

      {/* Main Bottom Grid: Quick Launch Bar + News & Tips */}
      <div className="p-8 space-y-6">
        {/* Quick Launch Profiles Bar */}
        <div>
          <div className="flex items-center justify-between mb-3">
            <h2 className="text-sm font-bold text-slate-900 dark:text-neutral-200 flex items-center gap-2">
              <Layers className="w-4 h-4 text-emerald-600 dark:text-emerald-400" />
              <span>快速切换实例</span>
            </h2>
            <button
              onClick={() => onNavigateTab('instances')}
              className="text-xs text-slate-500 dark:text-neutral-400 hover:text-emerald-600 dark:hover:text-emerald-400 transition-colors cursor-pointer"
            >
              管理全部实例 →
            </button>
          </div>

          <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-3">
            {allInstances.slice(0, 4).map((inst) => {
              const isSelected = inst.id === currentInstance.id;
              return (
                <div
                  key={inst.id}
                  onClick={() => onSelectInstance(inst)}
                  className={`p-3.5 rounded-xl border transition-all cursor-pointer text-left flex flex-col justify-between ${
                    isSelected
                      ? 'bg-white dark:bg-neutral-900 border-emerald-500/60 shadow-md shadow-emerald-500/10'
                      : 'bg-white/80 dark:bg-neutral-900/40 hover:bg-white dark:hover:bg-neutral-900/80 border-slate-200 dark:border-neutral-800/80 hover:border-slate-300 dark:hover:border-neutral-700 shadow-xs'
                  }`}
                >
                  <div className="flex items-start justify-between gap-2">
                    <span className="text-xl">{inst.icon}</span>
                    <span className="text-[10px] font-mono uppercase bg-slate-100 dark:bg-neutral-800 text-slate-600 dark:text-neutral-400 px-1.5 py-0.5 rounded font-medium">
                      {inst.loader}
                    </span>
                  </div>

                  <div className="mt-3">
                    <div className="text-xs font-semibold text-slate-900 dark:text-neutral-200 truncate">{inst.name}</div>
                    <div className="text-[11px] text-slate-500 dark:text-neutral-500 mt-0.5">
                      MC {inst.mcVersion} · {inst.modsCount} 模组
                    </div>
                  </div>

                  <div className="mt-3 pt-2 border-t border-slate-100 dark:border-neutral-800/60 flex items-center justify-between text-[10px] text-slate-500 dark:text-neutral-500">
                    <span>上次游玩: {inst.lastPlayed}</span>
                    {isSelected && (
                      <span className="text-emerald-600 dark:text-emerald-400 font-semibold">当前选用</span>
                    )}
                  </div>
                </div>
              );
            })}
          </div>
        </div>

        {/* Lower Split: Community News & Performance Tips */}
        <div className="grid grid-cols-1 lg:grid-cols-3 gap-6 pt-2">
          {/* News Feed */}
          <div className="lg:col-span-2 space-y-3">
            <h2 className="text-sm font-bold text-slate-900 dark:text-neutral-200 flex items-center gap-2">
              <Sparkles className="w-4 h-4 text-emerald-600 dark:text-emerald-400" />
              <span>官方公告与社区快讯</span>
            </h2>

            <div className="space-y-2.5">
              {newsItems.map((news) => (
                <div
                  key={news.id}
                  className="p-3.5 rounded-xl bg-white dark:bg-neutral-900/50 border border-slate-200 dark:border-neutral-800/70 hover:border-slate-300 dark:hover:border-neutral-700 transition-colors flex items-start justify-between gap-4 shadow-xs"
                >
                  <div className="space-y-1">
                    <div className="flex items-center gap-2 text-xs">
                      <span className="text-slate-500 dark:text-neutral-500 font-mono">{news.date}</span>
                      <span className="text-slate-300 dark:text-neutral-600">·</span>
                      <span className="text-emerald-600 dark:text-emerald-400 font-medium">{news.category}</span>
                    </div>
                    <h3 className="text-xs font-semibold text-slate-900 dark:text-neutral-200 hover:text-emerald-600 dark:hover:text-white transition-colors cursor-pointer">
                      {news.title}
                    </h3>
                    <p className="text-[11px] text-slate-600 dark:text-neutral-400 leading-relaxed">
                      {news.summary}
                    </p>
                  </div>
                  <button className="text-slate-400 dark:text-neutral-500 hover:text-slate-700 dark:hover:text-neutral-300 p-1 shrink-0 cursor-pointer" title="阅读详情">
                    <ExternalLink className="w-3.5 h-3.5" />
                  </button>
                </div>
              ))}
            </div>
          </div>

          {/* Quick Launcher Health & Optimization Tips */}
          <div className="space-y-3">
            <h2 className="text-sm font-bold text-slate-900 dark:text-neutral-200">运行环境建议</h2>

            <div className="p-4 rounded-xl bg-white dark:bg-neutral-900/50 border border-slate-200 dark:border-neutral-800/70 space-y-3 text-xs shadow-xs">
              <div className="flex items-start gap-2.5">
                <CheckCircle2 className="w-4 h-4 text-emerald-600 dark:text-emerald-400 shrink-0 mt-0.5" />
                <div>
                  <div className="font-semibold text-slate-900 dark:text-neutral-200">GraalVM 21 运行时良好</div>
                  <div className="text-[11px] text-slate-600 dark:text-neutral-400 mt-0.5">
                    已启用 ZGC 低延迟垃圾回收器，大内存模组包下 GC 停顿通常低于 1ms。
                  </div>
                </div>
              </div>

              <div className="h-px bg-slate-100 dark:bg-neutral-800/60" />

              <div className="flex items-start gap-2.5">
                <AlertCircle className="w-4 h-4 text-amber-500 dark:text-amber-400 shrink-0 mt-0.5" />
                <div>
                  <div className="font-semibold text-slate-900 dark:text-neutral-200">光影驱动支持</div>
                  <div className="text-[11px] text-slate-600 dark:text-neutral-400 mt-0.5">
                    检测到独立显卡驱动支持 OpenGL 4.6，可流畅加载 Complementary 与 Bliss 光影包。
                  </div>
                </div>
              </div>

              <button
                onClick={() => onNavigateTab('settings')}
                className="w-full mt-2 py-2 rounded-lg bg-slate-100 dark:bg-neutral-800 hover:bg-slate-200 dark:hover:bg-neutral-700/80 text-slate-800 dark:text-neutral-200 text-xs font-semibold transition-colors text-center cursor-pointer"
              >
                配置全局 JVM 优化参数
              </button>
            </div>
          </div>
        </div>
      </div>
    </div>
  );
};
