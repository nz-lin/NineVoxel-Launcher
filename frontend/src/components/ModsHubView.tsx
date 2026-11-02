import React, { useState } from 'react';
import {
  Search,
  Download,
  Check,
  Star,
  ExternalLink,
  Filter,
  Sparkles,
  Zap,
  Globe
} from 'lucide-react';
import { ModItem, Instance } from '../types/launcher';
import { MOCK_MODS } from '../data/mockData';

interface ModsHubViewProps {
  currentInstance: Instance;
  onInstallMod: (mod: ModItem) => void;
}

export const ModsHubView: React.FC<ModsHubViewProps> = ({
  currentInstance,
  onInstallMod
}) => {
  const [search, setSearch] = useState('');
  const [activeCategory, setActiveCategory] = useState<string>('all');
  const [selectedSource, setSelectedSource] = useState<'all' | 'modrinth' | 'curseforge'>('all');
  const [modsList, setModsList] = useState<ModItem[]>(MOCK_MODS);
  const [installingIds, setInstallingIds] = useState<string[]>([]);

  const categories = [
    { id: 'all', label: '全部模组' },
    { id: 'performance', label: '性能优化' },
    { id: 'tech', label: '科技机械' },
    { id: 'magic', label: '魔法探险' },
    { id: 'shaders', label: '光影包' },
    { id: 'adventure', label: '冒险世界' },
  ];

  const filteredMods = modsList.filter((m) => {
    const matchesSearch =
      m.name.toLowerCase().includes(search.toLowerCase()) ||
      m.author.toLowerCase().includes(search.toLowerCase()) ||
      m.summary.toLowerCase().includes(search.toLowerCase());
    const matchesCategory = activeCategory === 'all' || m.category === activeCategory;
    const matchesSource = selectedSource === 'all' || m.source === selectedSource;
    return matchesSearch && matchesCategory && matchesSource;
  });

  const handleInstallClick = (mod: ModItem) => {
    if (mod.installed) return;
    setInstallingIds(prev => [...prev, mod.id]);

    setTimeout(() => {
      setModsList(prev =>
        prev.map(item => (item.id === mod.id ? { ...item, installed: true, enabled: true } : item))
      );
      setInstallingIds(prev => prev.filter(id => id !== mod.id));
      onInstallMod(mod);
    }, 1200);
  };

  return (
    <div className="flex-1 overflow-y-auto bg-slate-100 dark:bg-neutral-950 p-6 flex flex-col space-y-6 transition-colors duration-150">
      {/* Header and Source Switcher */}
      <div className="flex flex-col md:flex-row md:items-center justify-between gap-4 pb-4 border-b border-slate-200 dark:border-neutral-800/80">
        <div>
          <h1 className="text-xl font-bold text-slate-900 dark:text-neutral-100 flex items-center gap-2">
            <span>模组工坊 (Mod Hub)</span>
            <span className="text-xs font-semibold text-emerald-700 dark:text-emerald-400 bg-emerald-500/10 border border-emerald-500/30 px-2 py-0.5 rounded-full">
              目标: {currentInstance.name}
            </span>
          </h1>
          <p className="text-xs text-slate-500 dark:text-neutral-400 mt-1">
            集成 Modrinth 与 CurseForge 官方源，支持一键依赖解析与极速镜像下载
          </p>
        </div>

        {/* Source Switcher */}
        <div className="flex items-center gap-1.5 p-1 bg-white dark:bg-neutral-900 border border-slate-200 dark:border-neutral-800 rounded-xl shadow-xs">
          <button
            onClick={() => setSelectedSource('all')}
            className={`px-3 py-1.5 rounded-lg text-xs font-medium transition-colors cursor-pointer ${
              selectedSource === 'all'
                ? 'bg-slate-100 dark:bg-neutral-800 text-slate-900 dark:text-white font-bold'
                : 'text-slate-500 dark:text-neutral-400 hover:text-slate-900 dark:hover:text-neutral-200'
            }`}
          >
            全网索引
          </button>
          <button
            onClick={() => setSelectedSource('modrinth')}
            className={`px-3 py-1.5 rounded-lg text-xs font-medium transition-colors cursor-pointer ${
              selectedSource === 'modrinth'
                ? 'bg-emerald-500/20 text-emerald-800 dark:text-emerald-300 font-bold'
                : 'text-slate-500 dark:text-neutral-400 hover:text-slate-900 dark:hover:text-neutral-200'
            }`}
          >
            Modrinth
          </button>
          <button
            onClick={() => setSelectedSource('curseforge')}
            className={`px-3 py-1.5 rounded-lg text-xs font-medium transition-colors cursor-pointer ${
              selectedSource === 'curseforge'
                ? 'bg-amber-500/20 text-amber-800 dark:text-amber-300 font-bold'
                : 'text-slate-500 dark:text-neutral-400 hover:text-slate-900 dark:hover:text-neutral-200'
            }`}
          >
            CurseForge
          </button>
        </div>
      </div>

      {/* Search and Category Filter Toolbar */}
      <div className="flex flex-col lg:flex-row lg:items-center justify-between gap-3">
        {/* Search Input */}
        <div className="relative flex-1 max-w-md">
          <Search className="w-4 h-4 text-slate-400 dark:text-neutral-500 absolute left-3 top-1/2 -translate-y-1/2" />
          <input
            type="text"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder="搜索模组名称、关键词或作者 (如: Sodium, 机械动力)..."
            className="w-full pl-9 pr-4 py-2 bg-white dark:bg-neutral-900/80 border border-slate-200 dark:border-neutral-800 rounded-lg text-xs text-slate-900 dark:text-neutral-200 placeholder-slate-400 dark:placeholder-neutral-500 focus:outline-none focus:border-emerald-500 transition-colors shadow-xs"
          />
        </div>

        {/* Categories */}
        <div className="flex items-center gap-1.5 flex-wrap">
          {categories.map((c) => (
            <button
              key={c.id}
              onClick={() => setActiveCategory(c.id)}
              className={`px-3 py-1.5 rounded-lg text-xs font-medium transition-colors cursor-pointer ${
                activeCategory === c.id
                  ? 'bg-slate-200 dark:bg-neutral-800 text-slate-900 dark:text-neutral-100 font-bold border border-slate-300 dark:border-neutral-700'
                  : 'text-slate-600 dark:text-neutral-400 hover:text-slate-900 dark:hover:text-neutral-200 hover:bg-slate-200/60 dark:hover:bg-neutral-900/60'
              }`}
            >
              {c.label}
            </button>
          ))}
        </div>
      </div>

      {/* Mods Grid */}
      <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
        {filteredMods.map((mod) => {
          const isInstalling = installingIds.includes(mod.id);
          const isCompatible = mod.loaders.includes(currentInstance.loader);

          return (
            <div
              key={mod.id}
              className="p-4 rounded-xl bg-white dark:bg-neutral-900/60 border border-slate-200 dark:border-neutral-800 hover:border-slate-300 dark:hover:border-neutral-700 transition-all flex flex-col justify-between group shadow-xs hover:shadow-sm"
            >
              <div>
                {/* Header info */}
                <div className="flex items-start justify-between gap-3">
                  <div className="flex items-center gap-3">
                    <div className="w-10 h-10 rounded-xl bg-slate-100 dark:bg-neutral-800 border border-slate-200 dark:border-neutral-700 flex items-center justify-center text-xl shrink-0 shadow-inner">
                      {mod.iconUrl}
                    </div>
                    <div className="min-w-0">
                      <h3 className="text-xs font-bold text-slate-900 dark:text-neutral-100 truncate group-hover:text-emerald-600 dark:group-hover:text-emerald-400 transition-colors">
                        {mod.name}
                      </h3>
                      <div className="text-[11px] text-slate-500 dark:text-neutral-500 flex items-center gap-2 mt-0.5">
                        <span>作者: {mod.author}</span>
                        <span>·</span>
                        <span className="capitalize text-slate-500 dark:text-neutral-400 font-mono text-[10px]">
                          {mod.source}
                        </span>
                      </div>
                    </div>
                  </div>
                </div>

                {/* Summary */}
                <p className="text-xs text-slate-600 dark:text-neutral-400 mt-3 line-clamp-2 leading-relaxed">
                  {mod.summary}
                </p>

                {/* Compatibility Badges */}
                <div className="mt-3 flex flex-wrap items-center gap-1.5 text-[10px]">
                  <span className="bg-slate-100 dark:bg-neutral-800 text-slate-600 dark:text-neutral-400 px-1.5 py-0.5 rounded font-mono">
                    下载量: {mod.downloads}
                  </span>
                  <span className="bg-slate-100 dark:bg-neutral-800 text-slate-600 dark:text-neutral-400 px-1.5 py-0.5 rounded font-mono">
                    关注: {mod.follows}
                  </span>
                  <span className={`px-1.5 py-0.5 rounded font-mono font-medium ${
                    isCompatible
                      ? 'bg-emerald-500/10 text-emerald-700 dark:text-emerald-400'
                      : 'bg-amber-500/10 text-amber-700 dark:text-amber-400'
                  }`}>
                    {isCompatible ? '适配当前实例' : '加载器需注意'}
                  </span>
                </div>
              </div>

              {/* Install Action Bar */}
              <div className="mt-4 pt-3 border-t border-slate-100 dark:border-neutral-800/60 flex items-center justify-between">
                <span className="text-[11px] font-mono text-slate-400 dark:text-neutral-500">
                  {(mod.fileSizeBytes / 1024 / 1024).toFixed(2)} MB
                </span>

                {mod.installed ? (
                  <span className="text-xs font-semibold text-emerald-600 dark:text-emerald-400 flex items-center gap-1">
                    <Check className="w-3.5 h-3.5" /> 已装入当前实例
                  </span>
                ) : (
                  <button
                    onClick={() => handleInstallClick(mod)}
                    disabled={isInstalling}
                    className="px-3.5 py-1.5 rounded-lg bg-emerald-500 hover:bg-emerald-400 disabled:bg-slate-200 dark:disabled:bg-neutral-800 text-neutral-950 disabled:text-slate-400 dark:disabled:text-neutral-500 font-bold text-xs flex items-center gap-1.5 shadow-sm transition-all cursor-pointer"
                  >
                    {isInstalling ? (
                      <>
                        <div className="w-3 h-3 border-2 border-neutral-950 border-t-transparent rounded-full animate-spin" />
                        <span>下载中...</span>
                      </>
                    ) : (
                      <>
                        <Download className="w-3.5 h-3.5" />
                        <span>一键安装</span>
                      </>
                    )}
                  </button>
                )}
              </div>
            </div>
          );
        })}
      </div>
    </div>
  );
};
