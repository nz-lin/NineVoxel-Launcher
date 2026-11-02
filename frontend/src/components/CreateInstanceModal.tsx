import React, { useState } from 'react';
import { X, Check, Layers, Cpu, Sparkles, Download, AlertCircle } from 'lucide-react';
import { Instance, LoaderType } from '../types/launcher';
import { MC_VERSIONS_LIST } from '../data/mockData';

interface CreateInstanceModalProps {
  isOpen: boolean;
  onClose: () => void;
  onCreate: (newInstance: Instance) => void;
}

export const CreateInstanceModal: React.FC<CreateInstanceModalProps> = ({
  isOpen,
  onClose,
  onCreate
}) => {
  if (!isOpen) return null;

  const [step, setStep] = useState<1 | 2>(1);
  const [name, setName] = useState('我的全新世界');
  const [icon, setIcon] = useState('💎');
  const [selectedVersion, setSelectedVersion] = useState('1.21.4');
  const [selectedLoader, setSelectedLoader] = useState<LoaderType>('fabric');
  const [loaderVersion, setLoaderVersion] = useState('0.16.10');
  const [memoryMb, setMemoryMb] = useState(6144);
  const [partition, setPartition] = useState('常用模组包');
  const [versionTypeFilter, setVersionTypeFilter] = useState<'all' | 'release' | 'snapshot'>('release');

  const availableIcons = ['💎', '⚡', '⚙️', '🔮', '🟩', '🛡️', '🗡️', '🌲', '🚀', '⭐', '🔥', '🧊'];

  const filteredVersions = MC_VERSIONS_LIST.filter((v) => {
    if (versionTypeFilter === 'all') return true;
    return v.type === versionTypeFilter;
  });

  const handleNextOrFinish = () => {
    if (step === 1) {
      setStep(2);
    } else {
      const sanitizedName = name.trim().replace(/[/\\?%*:|"<>]/g, '-') || `MC-${selectedVersion}`;
      const newInst: Instance = {
        id: `inst-${Date.now()}`,
        name: name.trim() || `MC ${selectedVersion} (${selectedLoader})`,
        mcVersion: selectedVersion,
        loader: selectedLoader,
        loaderVersion: loaderVersion,
        installedLoaders: [
          { type: selectedLoader, version: loaderVersion, isPrimary: true, installDate: new Date().toISOString().split('T')[0] }
        ],
        partition: partition,
        icon: icon,
        playTimeMinutes: 0,
        lastPlayed: '从未运行',
        javaVersion: selectedVersion.startsWith('1.7') ? 'Java 8' : 'Java 21',
        customJavaPath: selectedVersion.startsWith('1.7') ? 'Ninevoxel/javaruntime/java-8/bin/java' : 'Ninevoxel/javaruntime/java-21/bin/java',
        memoryMb: memoryMb,
        jvmArgs: '-XX:+UseG1GC',
        modsCount: 0,
        description: `基于 ${selectedLoader.toUpperCase()} 平台自动配置的新实例，专属沙盒目录隔离，多版本互不污染。`,
        isFavorite: false,
        status: 'ready',
        folderPath: `instances/${sanitizedName}/.minecraft`,
        isolatedDirectory: true
      };
      onCreate(newInst);
      onClose();
    }
  };

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center p-4 bg-black/50 dark:bg-black/70 backdrop-blur-sm animate-in fade-in duration-200">
      <div className="w-full max-w-xl bg-white dark:bg-neutral-900 border border-slate-200 dark:border-neutral-700/80 rounded-2xl shadow-2xl overflow-hidden flex flex-col">
        {/* Modal Header */}
        <div className="px-6 py-4 border-b border-slate-200 dark:border-neutral-800 flex items-center justify-between bg-slate-50 dark:bg-neutral-950/60">
          <div className="flex items-center gap-2.5">
            <div className="w-8 h-8 rounded-lg bg-emerald-500/20 border border-emerald-500/30 flex items-center justify-center text-emerald-600 dark:text-emerald-400">
              <Layers className="w-4 h-4" />
            </div>
            <div>
              <h2 className="text-sm font-bold text-slate-900 dark:text-neutral-100">新建 Minecraft 游戏实例</h2>
              <p className="text-[11px] text-slate-500 dark:text-neutral-400">
                {step === 1 ? '步骤 1/2: 选择核心版本与模组加载器' : '步骤 2/2: 设置实例名称、图标与运行内存'}
              </p>
            </div>
          </div>
          <button
            onClick={onClose}
            className="p-1.5 rounded-lg text-slate-400 dark:text-neutral-400 hover:text-slate-800 dark:hover:text-white hover:bg-slate-100 dark:hover:bg-neutral-800 transition-colors cursor-pointer"
          >
            <X className="w-4 h-4" />
          </button>
        </div>

        {/* Modal Body */}
        <div className="p-6 space-y-5 flex-1 overflow-y-auto max-h-[70vh]">
          {step === 1 ? (
            <>
              {/* Version Type tabs */}
              <div className="space-y-2">
                <label className="text-xs font-semibold text-slate-700 dark:text-neutral-300 block">
                  1. 选择 Minecraft 游戏版本
                </label>
                <div className="flex items-center gap-2 pb-1">
                  <button
                    type="button"
                    onClick={() => setVersionTypeFilter('release')}
                    className={`px-3 py-1 rounded text-xs transition-colors cursor-pointer ${
                      versionTypeFilter === 'release'
                        ? 'bg-slate-200 dark:bg-neutral-800 text-slate-900 dark:text-white font-bold border border-slate-300 dark:border-neutral-700'
                        : 'text-slate-500 dark:text-neutral-400 hover:text-slate-900 dark:hover:text-neutral-200'
                    }`}
                  >
                    正式发布版 (Release)
                  </button>
                  <button
                    type="button"
                    onClick={() => setVersionTypeFilter('snapshot')}
                    className={`px-3 py-1 rounded text-xs transition-colors cursor-pointer ${
                      versionTypeFilter === 'snapshot'
                        ? 'bg-slate-200 dark:bg-neutral-800 text-slate-900 dark:text-white font-bold border border-slate-300 dark:border-neutral-700'
                        : 'text-slate-500 dark:text-neutral-400 hover:text-slate-900 dark:hover:text-neutral-200'
                    }`}
                  >
                    快照预览版 (Snapshot)
                  </button>
                  <button
                    type="button"
                    onClick={() => setVersionTypeFilter('all')}
                    className={`px-3 py-1 rounded text-xs transition-colors cursor-pointer ${
                      versionTypeFilter === 'all'
                        ? 'bg-slate-200 dark:bg-neutral-800 text-slate-900 dark:text-white font-bold border border-slate-300 dark:border-neutral-700'
                        : 'text-slate-500 dark:text-neutral-400 hover:text-slate-900 dark:hover:text-neutral-200'
                    }`}
                  >
                    全部版本
                  </button>
                </div>

                <div className="grid grid-cols-2 sm:grid-cols-3 gap-2 max-h-36 overflow-y-auto pr-1">
                  {filteredVersions.map((v) => (
                    <button
                      key={v.id}
                      type="button"
                      onClick={() => setSelectedVersion(v.id)}
                      className={`p-2.5 rounded-lg border text-left text-xs transition-all cursor-pointer ${
                        selectedVersion === v.id
                          ? 'bg-emerald-500/15 border-emerald-500/50 text-emerald-800 dark:text-emerald-300 font-bold'
                          : 'bg-slate-50 dark:bg-neutral-950/50 border-slate-200 dark:border-neutral-800 hover:border-slate-300 dark:hover:border-neutral-700 text-slate-700 dark:text-neutral-300'
                      }`}
                    >
                      <div className="font-bold">{v.id}</div>
                      <div className="text-[10px] text-slate-500 dark:text-neutral-500 mt-0.5">{v.date}</div>
                    </button>
                  ))}
                </div>
              </div>

              {/* Loader Selection */}
              <div className="space-y-2 pt-2 border-t border-slate-200 dark:border-neutral-800/80">
                <label className="text-xs font-semibold text-slate-700 dark:text-neutral-300 block">
                  2. 选择模组加载器 (Mod Loader)
                </label>
                <div className="grid grid-cols-2 sm:grid-cols-3 gap-2">
                  {[
                    { id: 'fabric' as LoaderType, name: 'Fabric', desc: '轻量极速，生态活跃', tag: '推荐' },
                    { id: 'neoforge' as LoaderType, name: 'NeoForge', desc: 'Forge 新分支，兼顾高版本' },
                    { id: 'forge' as LoaderType, name: 'Forge', desc: '传统大型模组标准' },
                    { id: 'quilt' as LoaderType, name: 'Quilt', desc: '开源社区驱动分支' },
                    { id: 'vanilla' as LoaderType, name: 'Vanilla 原版', desc: '无模组纯净体验' },
                  ].map((ldr) => (
                    <button
                      key={ldr.id}
                      type="button"
                      onClick={() => setSelectedLoader(ldr.id)}
                      className={`p-3 rounded-xl border text-left transition-all cursor-pointer ${
                        selectedLoader === ldr.id
                          ? 'bg-emerald-500/15 border-emerald-500/50 text-emerald-900 dark:text-white ring-1 ring-emerald-500/30'
                          : 'bg-slate-50 dark:bg-neutral-950/50 border-slate-200 dark:border-neutral-800 hover:border-slate-300 dark:hover:border-neutral-700 text-slate-700 dark:text-neutral-300'
                      }`}
                    >
                      <div className="flex items-center justify-between">
                        <span className="font-bold text-xs">{ldr.name}</span>
                        {ldr.tag && (
                          <span className="text-[9px] bg-emerald-500/20 text-emerald-800 dark:text-emerald-300 px-1.5 py-0.2 rounded font-medium">
                            {ldr.tag}
                          </span>
                        )}
                      </div>
                      <div className="text-[10px] text-slate-500 dark:text-neutral-400 mt-1 line-clamp-1">{ldr.desc}</div>
                    </button>
                  ))}
                </div>
              </div>
            </>
          ) : (
            <>
              {/* Step 2: Name, Icon, Memory */}
              <div className="space-y-4">
                <div>
                  <label className="text-xs font-semibold text-slate-700 dark:text-neutral-300 block mb-1.5">
                    实例名称
                  </label>
                  <input
                    type="text"
                    value={name}
                    onChange={(e) => setName(e.target.value)}
                    placeholder="输入实例名称..."
                    className="w-full px-3 py-2 bg-slate-50 dark:bg-neutral-950 border border-slate-300 dark:border-neutral-800 rounded-lg text-xs text-slate-900 dark:text-neutral-200 focus:outline-none focus:border-emerald-500"
                  />
                </div>

                <div>
                  <label className="text-xs font-semibold text-slate-700 dark:text-neutral-300 block mb-1.5">
                    所属隔离分区 (Partition / Group)
                  </label>
                  <div className="flex gap-2">
                    <input
                      type="text"
                      value={partition}
                      onChange={(e) => setPartition(e.target.value)}
                      placeholder="如: 常用模组包、大型科技服..."
                      className="flex-1 px-3 py-2 bg-slate-50 dark:bg-neutral-950 border border-slate-300 dark:border-neutral-800 rounded-lg text-xs text-slate-900 dark:text-neutral-200 focus:outline-none focus:border-emerald-500"
                    />
                    <div className="flex gap-1">
                      {['常用模组包', '大型科技服', '纯净与测试'].map(p => (
                        <button
                          key={p}
                          type="button"
                          onClick={() => setPartition(p)}
                          className="px-2 py-1 text-[11px] rounded bg-slate-100 dark:bg-neutral-800 hover:bg-slate-200 dark:hover:bg-neutral-700 text-slate-700 dark:text-neutral-300 cursor-pointer"
                        >
                          {p}
                        </button>
                      ))}
                    </div>
                  </div>
                </div>

                <div>
                  <label className="text-xs font-semibold text-slate-700 dark:text-neutral-300 block mb-1.5">
                    选择实例图标
                  </label>
                  <div className="flex flex-wrap gap-2">
                    {availableIcons.map((ic) => (
                      <button
                        key={ic}
                        type="button"
                        onClick={() => setIcon(ic)}
                        className={`w-9 h-9 rounded-lg flex items-center justify-center text-lg transition-all cursor-pointer ${
                          icon === ic
                            ? 'bg-emerald-500/20 border-2 border-emerald-500 scale-105'
                            : 'bg-slate-50 dark:bg-neutral-950 border border-slate-200 dark:border-neutral-800 hover:border-slate-300 dark:hover:border-neutral-700'
                        }`}
                      >
                        {ic}
                      </button>
                    ))}
                  </div>
                </div>

                <div>
                  <div className="flex items-center justify-between mb-1.5">
                    <label className="text-xs font-semibold text-slate-700 dark:text-neutral-300">
                      独立内存分配 (Max RAM)
                    </label>
                    <span className="font-mono text-xs font-bold text-emerald-600 dark:text-emerald-400">
                      {memoryMb} MB ({(memoryMb / 1024).toFixed(1)} GB)
                    </span>
                  </div>
                  <input
                    type="range"
                    min="2048"
                    max="16384"
                    step="512"
                    value={memoryMb}
                    onChange={(e) => setMemoryMb(Number(e.target.value))}
                    className="w-full accent-emerald-500 cursor-pointer"
                  />
                  <div className="flex justify-between text-[10px] text-slate-400 dark:text-neutral-500 mt-1 font-mono">
                    <span>2048 MB (轻量)</span>
                    <span>6144 MB (推荐)</span>
                    <span>16384 MB (大型包)</span>
                  </div>
                </div>

                <div className="p-3.5 rounded-xl bg-slate-50 dark:bg-neutral-950/70 border border-slate-200 dark:border-neutral-800/80 text-xs space-y-1.5 text-slate-600 dark:text-neutral-400">
                  <div className="text-slate-900 dark:text-neutral-200 font-semibold flex items-center gap-1.5">
                    <Sparkles className="w-3.5 h-3.5 text-emerald-600 dark:text-emerald-400" />
                    <span>自动配置与下载概览</span>
                  </div>
                  <div className="text-[11px] leading-relaxed">
                    将自动从 BMCLAPI 高速镜像下载 Minecraft {selectedVersion} 客户端核心与 {selectedLoader.toUpperCase()} 加载器文件。创建后可立即在“模组工坊”中一键安装优化模组。
                  </div>
                </div>
              </div>
            </>
          )}
        </div>

        {/* Modal Footer */}
        <div className="px-6 py-4 border-t border-slate-200 dark:border-neutral-800 flex items-center justify-between bg-slate-50 dark:bg-neutral-950/50">
          {step === 2 ? (
            <button
              type="button"
              onClick={() => setStep(1)}
              className="px-4 py-2 rounded-lg text-xs font-medium text-slate-600 dark:text-neutral-400 hover:text-slate-900 dark:hover:text-white cursor-pointer"
            >
              ← 上一步
            </button>
          ) : (
            <div />
          )}

          <div className="flex items-center gap-2">
            <button
              type="button"
              onClick={onClose}
              className="px-4 py-2 rounded-lg text-xs font-medium text-slate-600 dark:text-neutral-400 hover:text-slate-900 dark:hover:text-white cursor-pointer"
            >
              取消
            </button>
            <button
              type="button"
              onClick={handleNextOrFinish}
              className="px-5 py-2 rounded-lg bg-emerald-500 hover:bg-emerald-400 text-neutral-950 font-bold text-xs flex items-center gap-1.5 shadow-md shadow-emerald-500/20 active:scale-98 transition-all cursor-pointer"
            >
              {step === 1 ? (
                <span>下一步: 实例配置 →</span>
              ) : (
                <>
                  <Download className="w-3.5 h-3.5 stroke-[2.5]" />
                  <span>立即创建并下载</span>
                </>
              )}
            </button>
          </div>
        </div>
      </div>
    </div>
  );
};
