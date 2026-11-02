import React, { useEffect, useState } from 'react';
import {
  Settings,
  Cpu,
  Coffee,
  Globe,
  Sliders,
  Sparkles,
  Check,
  RefreshCw,
  Monitor,
  FolderOpen,
  HardDrive,
  Sun,
  Moon,
  Palette
} from 'lucide-react';
import { LauncherSettings, JavaRuntime } from '../types/launcher';

/** 全局默认最大分配内存的下限（MB）：低于 2 GB 的堆无法启动现代 Minecraft。 */
const MIN_MEMORY_MB = 2048;

/** 物理内存探测失败时的回退上限（MB）：保守值，避免在未知机器上鼓励过度分配。 */
const FALLBACK_MAX_MEMORY_MB = 8192;

/**
 * 把物理内存总量换算成滑块上限。
 *
 * @param physicalMb 后端探测到的物理内存（MiB）；0 或负数表示未知
 * @returns 向下取整到整 GB 的上限，且不低于下限
 *
 * 向下取整而不是向上：上限一旦超过物理内存，滑块最右端就是「一定触发换页」的位置，
 * 用户拖到头反而得到最差的结果。硬件保留也会让报告值比标称容量略小
 * （例如 32 GB 机器报告 31.9 GB），取整正好抹平这点差异。
 */
function resolveMemoryCeilingMb(physicalMb?: number): number {
  if (!physicalMb || physicalMb <= 0) {
    return FALLBACK_MAX_MEMORY_MB;
  }
  const wholeGb = Math.floor(physicalMb / 1024) * 1024;
  return Math.max(MIN_MEMORY_MB, wholeGb);
}

/**
 * 按上限生成刻度：取 25% / 50% / 75% / 100% 四个点，并对齐到整 GB。
 *
 * 标签位置由 `mb` 换算成百分比定位，**不能**用 justify-between 均分：
 * 均分会把 8 GB 画到 33% 处，而它在滑块上的真实位置只有约 20%，
 * 导致「滑块停在 8G 上、标签却指向别处」。
 *
 * @param maxMb 滑块上限
 * @returns 递增的刻度列表，含下限点
 */
function buildMemoryTicks(maxMb: number): Array<{ mb: number; label: string }> {
  // 兜底：上限必须是有限正数，否则下面的取整会算出 NaN，标签直接显示「NaN GB」。
  const safeMax = Number.isFinite(maxMb) && maxMb > 0 ? maxMb : FALLBACK_MAX_MEMORY_MB;
  // 对齐到 4 GB：1 GB 粒度会在大内存机器上产出「95 GB」这类不圆整的标签，
  // 4 GB 对 8 GB 机器仍够细（25% 恰好是 2 GB），对大机器则整得好看。
  const granularity = 4 * 1024;
  const ratios = [0.25, 0.5, 0.75, 1];
  const points = ratios.map((ratio) => Math.round((safeMax * ratio) / granularity) * granularity);
  // 去重并夹在 [下限, 上限] 内：小内存机器上 25% 可能低于下限。
  const unique = Array.from(new Set([MIN_MEMORY_MB, ...points]))
    .map((mb) => Math.min(Math.max(mb, MIN_MEMORY_MB), safeMax))
    .sort((left, right) => left - right);
  return unique.map((mb) => ({
    mb,
    label: mb % 1024 === 0 ? `${mb / 1024} GB` : `${(mb / 1024).toFixed(1)} GB`,
  }));
}

interface SettingsViewProps {
  settings: LauncherSettings;
  onUpdateSettings: (newSettings: LauncherSettings) => void;
  /** 是否正在写入设置库。 */
  saving?: boolean;
  /** 写入失败的说明；为空表示没有错误。 */
  saveError?: string | null;
  /** 最近一次成功写入后端的时间戳；变化即表示刚刚保存成功。 */
  lastSavedAt?: number | null;
  javaRuntimes: JavaRuntime[];
  onOpenJavaManager: () => void;
  onOpenFolder: (path: string) => void;
  /**
   * 本机物理内存总量（MiB），来自 `AppService.Snapshot()` 的真实探测。
   *
   * 0 或未提供表示未知：滑块回退到保守上限，界面不得假装知道机器有多大。
   */
  totalMemoryMb?: number;
}

export const SettingsView: React.FC<SettingsViewProps> = ({
  settings,
  onUpdateSettings,
  saving = false,
  saveError = null,
  lastSavedAt = null,
  javaRuntimes,
  onOpenJavaManager,
  onOpenFolder,
  totalMemoryMb
}) => {
  const [localSettings, setLocalSettings] = useState<LauncherSettings>(settings);
  const [savedBadge, setSavedBadge] = useState(false);

  // 滑块上限与刻度按真实物理内存计算，不再写死一个数字。
  const memoryCeilingMb = resolveMemoryCeilingMb(totalMemoryMb);
  const memoryTicks = buildMemoryTicks(memoryCeilingMb);
  // 已存的值可能超出当前上限（换了机器，或之前设得更大）：显示时夹紧，
  // 否则滑块 thumb 会跑到轨道外面。
  // 兜底：上游任何一环给出非有限值（旧快照、脏数据、HMR 混合状态）时，
  // 回落默认值，绝不让 NaN 泄漏到界面。
  const storedMemoryMb = Number.isFinite(localSettings.maxMemoryMb)
    ? localSettings.maxMemoryMb
    : FALLBACK_MAX_MEMORY_MB;
  const memoryValueMb = Math.min(Math.max(storedMemoryMb, MIN_MEMORY_MB), memoryCeilingMb);

  // 后端的真实回显会覆盖本地草稿：例如首次读取到设置库后，主题与下载线路
  // 应当以库里的值为准，而不是停留在初始默认值。
  useEffect(() => {
    setLocalSettings(settings);
  }, [settings]);

  // 只在后端确认写入成功（时间戳变化）后才提示「已自动保存」。
  // 早先用 saving 的 true→false 边沿判定，两次 setState 被 React 合并时
  // 边沿会丢失，导致保存成功却没有任何提示——改用时间戳这种不会被合并的信号。
  useEffect(() => {
    if (lastSavedAt === null) {
      return undefined;
    }
    setSavedBadge(true);
    const timer = setTimeout(() => setSavedBadge(false), 1500);
    return () => clearTimeout(timer);
  }, [lastSavedAt]);

  const handleSave = (updated: LauncherSettings) => {
    setLocalSettings(updated);
    onUpdateSettings(updated);
  };

  return (
    <div className="flex-1 overflow-y-auto bg-slate-100 dark:bg-neutral-950 p-6 flex flex-col space-y-6 transition-colors duration-150">
      {/* Header */}
      <div className="flex flex-col md:flex-row md:items-center justify-between gap-4 pb-4 border-b border-slate-200 dark:border-neutral-800/80">
        <div>
          <h1 className="text-xl font-bold text-slate-900 dark:text-neutral-100 flex items-center gap-2">
            <span>启动器全局设置</span>
            {savedBadge && (
              <span className="text-xs text-emerald-700 dark:text-emerald-400 font-semibold bg-emerald-500/10 border border-emerald-500/30 px-2 py-0.5 rounded-full animate-in fade-in">
                已自动保存
              </span>
            )}
            {saving && (
              <span className="text-xs text-slate-500 dark:text-neutral-400 font-medium">保存中…</span>
            )}
            {saveError && (
              <span className="text-xs text-rose-600 dark:text-rose-400 font-medium bg-rose-500/10 border border-rose-500/30 px-2 py-0.5 rounded-full">
                保存失败：{saveError}
              </span>
            )}
          </h1>
          <p className="text-xs text-slate-500 dark:text-neutral-400 mt-1">
            配置主题外观、Java 运行路径、实例沙盒目录、全局物理内存分配与镜像下载线路
          </p>
        </div>
      </div>

      <div className="max-w-4xl space-y-6">
        {/* Section: Appearance & Theme (New!) */}
        <div className="p-5 rounded-2xl bg-white dark:bg-neutral-900/50 border border-slate-200 dark:border-neutral-800 space-y-4 shadow-xs">
          <div className="flex items-center gap-2.5">
            <Palette className="w-5 h-5 text-emerald-600 dark:text-emerald-400" />
            <div>
              <h2 className="text-sm font-bold text-slate-900 dark:text-neutral-100">界面主题外观 (Theme Appearance)</h2>
              <p className="text-[11px] text-slate-500 dark:text-neutral-400">
                支持深色（Dark）与浅色（Light）模式即时无缝切换
              </p>
            </div>
          </div>

          <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
            {/* Dark Theme Card */}
            <div
              onClick={() => handleSave({ ...localSettings, theme: 'dark' })}
              className={`p-4 rounded-xl border cursor-pointer transition-all flex items-center justify-between ${
                localSettings.theme === 'dark'
                  ? 'bg-neutral-900 text-white border-emerald-500/60 ring-1 ring-emerald-500/30 shadow-md'
                  : 'bg-slate-50 dark:bg-neutral-950/60 border-slate-200 dark:border-neutral-800 hover:border-slate-300 dark:hover:border-neutral-700'
              }`}
            >
              <div className="flex items-center gap-3">
                <div className="w-10 h-10 rounded-xl bg-neutral-950 flex items-center justify-center text-amber-400 border border-neutral-800 shadow-inner">
                  <Moon className="w-5 h-5" />
                </div>
                <div>
                  <div className="text-xs font-bold text-slate-900 dark:text-white">深色模式 (Dark Theme)</div>
                  <div className="text-[11px] text-slate-500 dark:text-neutral-400 mt-0.5">
                    暗色护眼，体素光影质感，适合长时间沉浸
                  </div>
                </div>
              </div>

              {localSettings.theme === 'dark' && (
                <span className="text-emerald-400 text-xs flex items-center gap-1 font-semibold">
                  <Check className="w-4 h-4" /> 选用
                </span>
              )}
            </div>

            {/* Light Theme Card */}
            <div
              onClick={() => handleSave({ ...localSettings, theme: 'light' })}
              className={`p-4 rounded-xl border cursor-pointer transition-all flex items-center justify-between ${
                localSettings.theme === 'light'
                  ? 'bg-white text-slate-900 border-emerald-500/60 ring-1 ring-emerald-500/30 shadow-md'
                  : 'bg-slate-50 dark:bg-neutral-950/60 border-slate-200 dark:border-neutral-800 hover:border-slate-300 dark:hover:border-neutral-700'
              }`}
            >
              <div className="flex items-center gap-3">
                <div className="w-10 h-10 rounded-xl bg-amber-50 dark:bg-neutral-800 flex items-center justify-center text-amber-500 border border-amber-200 dark:border-neutral-700 shadow-inner">
                  <Sun className="w-5 h-5" />
                </div>
                <div>
                  <div className="text-xs font-bold text-slate-900 dark:text-white">浅色模式 (Light Theme)</div>
                  <div className="text-[11px] text-slate-500 dark:text-neutral-400 mt-0.5">
                    清爽明亮，高对比度排版，明亮日光下清晰易读
                  </div>
                </div>
              </div>

              {localSettings.theme === 'light' && (
                <span className="text-emerald-600 dark:text-emerald-400 text-xs flex items-center gap-1 font-semibold">
                  <Check className="w-4 h-4" /> 选用
                </span>
              )}
            </div>
          </div>
        </div>

        {/* Section 1: Java Runtime Management */}
        <div className="p-5 rounded-2xl bg-white dark:bg-neutral-900/50 border border-slate-200 dark:border-neutral-800 space-y-4 shadow-xs">
          <div className="flex items-center justify-between">
            <div className="flex items-center gap-2.5">
              <Coffee className="w-5 h-5 text-emerald-600 dark:text-emerald-400" />
              <div>
                <h2 className="text-sm font-bold text-slate-900 dark:text-neutral-100">Java Runtime 独立隔离环境</h2>
                <p className="text-[11px] text-slate-500 dark:text-neutral-400">
                  本地存储根目录: <code className="text-emerald-600 dark:text-emerald-400 font-mono font-bold">Ninevoxel/javaruntime/</code>
                </p>
              </div>
            </div>

            <div className="flex items-center gap-2">
              <button
                onClick={() => onOpenFolder('Ninevoxel/javaruntime')}
                className="px-2.5 py-1.5 rounded-lg bg-slate-100 dark:bg-neutral-800 hover:bg-slate-200 dark:hover:bg-neutral-700 text-slate-700 dark:text-neutral-300 text-xs flex items-center gap-1.5 transition-colors cursor-pointer"
              >
                <FolderOpen className="w-3.5 h-3.5 text-slate-400 dark:text-neutral-400" />
                <span>打开目录</span>
              </button>
              <button
                onClick={onOpenJavaManager}
                className="px-3 py-1.5 rounded-lg bg-emerald-500 hover:bg-emerald-400 text-neutral-950 font-bold text-xs flex items-center gap-1.5 transition-colors cursor-pointer shadow-sm"
              >
                <Coffee className="w-3.5 h-3.5" />
                <span>进入 Java 管理中心</span>
              </button>
            </div>
          </div>

          {/* Java List preview */}
          <div className="grid grid-cols-1 sm:grid-cols-2 gap-2">
            {javaRuntimes.map((j) => (
              <div
                key={j.id}
                className="p-3 rounded-xl bg-slate-50 dark:bg-neutral-950/60 border border-slate-200 dark:border-neutral-800 flex items-center justify-between text-xs"
              >
                <div>
                  <div className="flex items-center gap-2">
                    <span className="font-bold text-slate-800 dark:text-neutral-200">{j.vendor}</span>
                    <span className="text-[10px] font-mono bg-emerald-500/10 text-emerald-700 dark:text-emerald-300 border border-emerald-500/30 px-1.5 rounded font-semibold">
                      Java {j.majorVersion}
                    </span>
                  </div>
                  <div className="text-[10px] text-slate-500 dark:text-neutral-500 font-mono mt-0.5 truncate max-w-[240px]">
                    {j.folderPath ?? (j.isDownloadedByLauncher ? '由启动器安装' : '手动指定')}
                  </div>
                </div>
                <span
                  className={`text-[10px] font-semibold ${
                    j.status === 'ready'
                      ? 'text-emerald-600 dark:text-emerald-400'
                      : j.status === 'downloading'
                        ? 'text-amber-600 dark:text-amber-400'
                        : 'text-red-600 dark:text-red-400'
                  }`}
                >
                  {j.status === 'ready' ? '就绪' : j.status === 'downloading' ? '下载中' : '不可用'}
                </span>
              </div>
            ))}
          </div>

          <div className="flex items-center justify-between pt-2">
            <label className="flex items-center gap-2 text-xs text-slate-700 dark:text-neutral-300 cursor-pointer">
              <input
                type="checkbox"
                checked={localSettings.autoDownloadJava}
                onChange={(e) => handleSave({ ...localSettings, autoDownloadJava: e.target.checked })}
                className="accent-emerald-500"
              />
              <span>启动游戏时若缺失指定大版本 Java，自动从镜像源静默下载到 javaruntime/</span>
            </label>
          </div>
        </div>

        {/* Section 2: Instance Sandbox Folder */}
        <div className="p-5 rounded-2xl bg-white dark:bg-neutral-900/50 border border-slate-200 dark:border-neutral-800 space-y-4 shadow-xs">
          <div className="flex items-center justify-between">
            <div className="flex items-center gap-2.5">
              <HardDrive className="w-5 h-5 text-emerald-600 dark:text-emerald-400" />
              <div>
                <h2 className="text-sm font-bold text-slate-900 dark:text-neutral-100">多实例沙盒存储路径</h2>
                <p className="text-[11px] text-slate-500 dark:text-neutral-400">
                  每个实例在此目录下拥有专属隔离文件夹，存档、模组、配置相互独立
                </p>
              </div>
            </div>

            <button
              onClick={() => onOpenFolder(localSettings.instancesDirectory)}
              className="px-2.5 py-1.5 rounded-lg bg-slate-100 dark:bg-neutral-800 hover:bg-slate-200 dark:hover:bg-neutral-700 text-slate-700 dark:text-neutral-300 text-xs flex items-center gap-1.5 transition-colors cursor-pointer"
            >
              <FolderOpen className="w-3.5 h-3.5 text-slate-400 dark:text-neutral-400" />
              <span>打开 instances 文件夹</span>
            </button>
          </div>

          <div className="p-3 rounded-xl bg-slate-50 dark:bg-neutral-950 border border-slate-200 dark:border-neutral-800 flex items-center justify-between text-xs">
            <div className="space-y-0.5 font-mono">
              <span className="text-slate-500 dark:text-neutral-400">实例根目录: </span>
              <span className="text-slate-800 dark:text-neutral-200 font-semibold">{localSettings.instancesDirectory}</span>
            </div>
            <span className="text-[11px] text-slate-500 dark:text-neutral-500">Prism / PCL2 兼容沙盒结构</span>
          </div>
        </div>

        {/* Section 3: Memory Allocation & GC */}
        <div className="p-5 rounded-2xl bg-white dark:bg-neutral-900/50 border border-slate-200 dark:border-neutral-800 space-y-4 shadow-xs">
          <div className="flex items-center gap-2.5">
            <Cpu className="w-5 h-5 text-emerald-600 dark:text-emerald-400" />
            <div>
              <h2 className="text-sm font-bold text-slate-900 dark:text-neutral-100">默认内存分配与 JVM 垃圾回收优化</h2>
              <p className="text-[11px] text-slate-500 dark:text-neutral-400">
                {totalMemoryMb && totalMemoryMb > 0
                  ? `本机物理内存总计: ${totalMemoryMb} MB (${(totalMemoryMb / 1024).toFixed(1)} GB)`
                  : '本机物理内存: 未知（按 8 GB 保守上限显示）'}
              </p>
            </div>
          </div>

          <div className="space-y-2">
            <div className="flex items-center justify-between">
              <span className="text-xs text-slate-700 dark:text-neutral-300 font-semibold">全局默认最大分配内存</span>
              <span className="font-mono text-xs font-bold text-emerald-600 dark:text-emerald-400">
                {memoryValueMb} MB ({(memoryValueMb / 1024).toFixed(1)} GB)
              </span>
            </div>

            {/* 拖动过程中只更新本地草稿：range 每移动一格都会触发 onChange，
                若每格都写设置库，拖一次会产生几十次写入。松手或键盘抬起时才落盘。 */}
            <input
              type="range"
              min={MIN_MEMORY_MB}
              max={memoryCeilingMb}
              step="512"
              value={memoryValueMb}
              onChange={(e) => setLocalSettings({ ...localSettings, maxMemoryMb: Number(e.target.value) })}
              onPointerUp={(e) =>
                handleSave({ ...localSettings, maxMemoryMb: Number(e.currentTarget.value) })
              }
              onKeyUp={(e) =>
                handleSave({ ...localSettings, maxMemoryMb: Number(e.currentTarget.value) })
              }
              className="w-full accent-emerald-500 cursor-pointer"
            />

            {/* 刻度按真实数值比例定位；首尾贴边，避免文字被容器裁掉。 */}
            <div className="relative h-4">
              {memoryTicks.map((tick, index) => {
                const ratio = (tick.mb - MIN_MEMORY_MB) / (memoryCeilingMb - MIN_MEMORY_MB);
                const shift =
                  index === 0
                    ? 'translateX(0)'
                    : index === memoryTicks.length - 1
                      ? 'translateX(-100%)'
                      : 'translateX(-50%)';
                return (
                  <span
                    key={tick.mb}
                    className="absolute top-0 text-[10px] text-slate-400 dark:text-neutral-500 font-mono whitespace-nowrap"
                    style={{ left: `${ratio * 100}%`, transform: shift }}
                  >
                    {tick.label}
                  </span>
                );
              })}
            </div>
          </div>

          {/* JVM Presets */}
          <div className="space-y-2 pt-2 border-t border-slate-100 dark:border-neutral-800">
            <label className="text-xs font-semibold text-slate-700 dark:text-neutral-300 block">
              垃圾收集器预设 (GC Preset)
            </label>
            <div className="grid grid-cols-1 sm:grid-cols-3 gap-2">
              {[
                { id: 'aikar' as const, name: "Aikar's Flags", desc: '经典 Minecraft 调优，稳定帧率' },
                { id: 'zgc' as const, name: 'Generational ZGC', desc: 'Java 21 现代化低延迟，停顿 <1ms' },
                { id: 'standard' as const, name: '标准 G1GC', desc: '官方默认回收策略' },
              ].map((preset) => (
                <button
                  key={preset.id}
                  type="button"
                  onClick={() => handleSave({ ...localSettings, jvmPreset: preset.id })}
                  className={`p-3 rounded-xl border text-left transition-all cursor-pointer ${
                    localSettings.jvmPreset === preset.id
                      ? 'bg-emerald-500/15 border-emerald-500/50 text-emerald-800 dark:text-emerald-300 ring-1 ring-emerald-500/20 font-semibold'
                      : 'bg-slate-50 dark:bg-neutral-950/40 border-slate-200 dark:border-neutral-800 hover:border-slate-300 dark:hover:border-neutral-700 text-slate-700 dark:text-neutral-300'
                  }`}
                >
                  <div className="text-xs font-bold">{preset.name}</div>
                  <div className="text-[10px] text-slate-500 dark:text-neutral-500 mt-1">{preset.desc}</div>
                </button>
              ))}
            </div>
          </div>
        </div>

        {/* Section 4: Download Sources */}
        <div className="p-5 rounded-2xl bg-white dark:bg-neutral-900/50 border border-slate-200 dark:border-neutral-800 space-y-4 shadow-xs">
          <div className="flex items-center gap-2.5">
            <Globe className="w-5 h-5 text-emerald-600 dark:text-emerald-400" />
            <div>
              <h2 className="text-sm font-bold text-slate-900 dark:text-neutral-100">下载源与网络镜像</h2>
              <p className="text-[11px] text-slate-500 dark:text-neutral-400">选择距离您网络延迟最低的资源同步线路</p>
            </div>
          </div>

          <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
            {[
              { id: 'bmclapi' as const, name: 'BMCLAPI 智能镜像', speed: '5-15 ms', desc: '中国大陆多线 BGP 节点 (推荐)' },
              { id: 'official' as const, name: 'Mojang 官方源', speed: '120-180 ms', desc: '官方全量资源库，海外直连' },
            ].map((src) => (
              <div
                key={src.id}
                onClick={() => handleSave({ ...localSettings, downloadSource: src.id })}
                className={`p-3.5 rounded-xl border cursor-pointer transition-all ${
                  localSettings.downloadSource === src.id
                    ? 'bg-emerald-500/15 border-emerald-500/50 ring-1 ring-emerald-500/20'
                    : 'bg-slate-50 dark:bg-neutral-950/40 border-slate-200 dark:border-neutral-800 hover:border-slate-300 dark:hover:border-neutral-700'
                }`}
              >
                <div className="flex items-center justify-between">
                  <span className="text-xs font-bold text-slate-800 dark:text-neutral-200">{src.name}</span>
                  <span className="text-[10px] font-mono text-emerald-600 dark:text-emerald-400 font-semibold">{src.speed}</span>
                </div>
                <div className="text-[11px] text-slate-500 dark:text-neutral-400 mt-1">{src.desc}</div>
              </div>
            ))}
          </div>
        </div>

        {/* Section 5: Window & Behavior */}
        <div className="p-5 rounded-2xl bg-white dark:bg-neutral-900/50 border border-slate-200 dark:border-neutral-800 space-y-4 shadow-xs">
          <div className="flex items-center gap-2.5">
            <Monitor className="w-5 h-5 text-emerald-600 dark:text-emerald-400" />
            <div>
              <h2 className="text-sm font-bold text-slate-900 dark:text-neutral-100">游戏视窗与启动器行为</h2>
              <p className="text-[11px] text-slate-500 dark:text-neutral-400">控制游戏启动时的分辨率以及启动器后台状态</p>
            </div>
          </div>

          <div className="grid grid-cols-1 sm:grid-cols-2 gap-4 text-xs">
            <label className="flex items-center gap-2.5 text-slate-700 dark:text-neutral-300 cursor-pointer">
              <input
                type="checkbox"
                checked={localSettings.fullscreen}
                onChange={(e) => handleSave({ ...localSettings, fullscreen: e.target.checked })}
                className="accent-emerald-500"
              />
              <span>启动游戏时自动全屏模式运行</span>
            </label>

            <label className="flex items-center gap-2.5 text-slate-700 dark:text-neutral-300 cursor-pointer">
              <input
                type="checkbox"
                checked={localSettings.closeOnLaunch}
                onChange={(e) => handleSave({ ...localSettings, closeOnLaunch: e.target.checked })}
                className="accent-emerald-500"
              />
              <span>游戏启动后自动最小化到系统托盘</span>
            </label>

            <label className="flex items-center gap-2.5 text-slate-700 dark:text-neutral-300 cursor-pointer">
              <input
                type="checkbox"
                checked={localSettings.enableDiscordRPC}
                onChange={(e) => handleSave({ ...localSettings, enableDiscordRPC: e.target.checked })}
                className="accent-emerald-500"
              />
              <span>启用 Discord Rich Presence 状态同步</span>
            </label>
          </div>
        </div>
      </div>
    </div>
  );
};
