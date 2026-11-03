import React, { useEffect, useState } from 'react';
import {
  Plus,
  Search,
  Play,
  Settings,
  FolderOpen,
  Copy,
  Trash2,
  ArchiveRestore,
  Star,
  HardDrive,
  ShieldCheck,
  Grid,
  Layers
} from 'lucide-react';
import { Instance, InstanceGroup } from '../types/launcher';
import { loaderDisplayName } from '../types/loader';
import { GroupManagerModal } from './GroupManagerModal';

interface InstancesViewProps {
  instances: Instance[];
  currentInstanceId: string;
  onSelectCurrent: (instance: Instance) => void;
  onLaunchInstance: (instance: Instance) => void;
  onOpenManageInstance: (instance: Instance) => void;
  onOpenCreateModal: () => void;
  onOpenTrash: () => void;
  onDeleteInstance: (id: string) => void;
  onRetryInstall: (instance: Instance) => void;
  onDuplicateInstance: (instance: Instance) => void;
  onOpenFolder: (path: string) => void;
  groups: InstanceGroup[];
  defaultGroup: string;
  onCreateGroup: (name: string) => Promise<void>;
  onDeleteGroup: (name: string, moveTo?: string) => Promise<void>;
}

export const InstancesView: React.FC<InstancesViewProps> = ({
  instances,
  currentInstanceId,
  onSelectCurrent,
  onLaunchInstance,
  onOpenManageInstance,
  onOpenCreateModal,
  onOpenTrash,
  onDeleteInstance,
  onDuplicateInstance,
  onRetryInstall,
  onOpenFolder,
  groups,
  defaultGroup,
  onCreateGroup,
  onDeleteGroup
}) => {
  const [searchQuery, setSearchQuery] = useState('');
  const [selectedPartition, setSelectedPartition] = useState<string>('all');
  const [selectedLoader, setSelectedLoader] = useState<string>('all');
  const [viewMode, setViewMode] = useState<'grouped' | 'grid'>('grouped');
  const [groupModalOpen, setGroupModalOpen] = useState(false);

  const groupNames = groups.length > 0
    ? groups.map((group) => group.name)
    : Array.from(new Set(instances.map((instance) => instance.partition || defaultGroup)));
  const countForGroup = (name: string) => instances.filter((instance) => (instance.partition || defaultGroup) === name).length;

  useEffect(() => {
    if (selectedPartition !== 'all' && !groupNames.includes(selectedPartition)) {
      setSelectedPartition('all');
    }
  }, [groupNames, selectedPartition]);

  const filteredInstances = instances.filter((inst) => {
    const matchesSearch =
      inst.name.toLowerCase().includes(searchQuery.toLowerCase()) ||
      inst.mcVersion.includes(searchQuery) ||
      inst.loader.toLowerCase().includes(searchQuery.toLowerCase()) ||
      (inst.partition && inst.partition.toLowerCase().includes(searchQuery.toLowerCase()));
    const matchesPartition = selectedPartition === 'all' || (inst.partition || defaultGroup) === selectedPartition;
    const matchesLoader = selectedLoader === 'all' || inst.loader === selectedLoader;
    return matchesSearch && matchesPartition && matchesLoader;
  });

  return (
    <div className="flex-1 overflow-y-auto bg-slate-100 dark:bg-neutral-950 p-6 flex flex-col space-y-6 transition-colors duration-150">
      {/* Top Header & Toolbar */}
      <div className="flex flex-col md:flex-row md:items-center justify-between gap-4 pb-4 border-b border-slate-200 dark:border-neutral-800/80">
        <div>
          <div className="flex items-center gap-2.5">
            <h1 className="text-xl font-bold text-slate-900 dark:text-neutral-100">实例管理</h1>
            <span className="text-xs font-mono font-semibold text-emerald-700 dark:text-emerald-400 bg-emerald-500/10 border border-emerald-500/30 px-2 py-0.5 rounded-full flex items-center gap-1">
              <ShieldCheck className="w-3.5 h-3.5" /> 实例隔离
            </span>
          </div>
        </div>

        {/* Primary Action: Create Instance */}
        <div className="flex items-center gap-2">
          {/* View mode toggle */}
          <div className="flex items-center bg-white dark:bg-neutral-900 border border-slate-200 dark:border-neutral-800 p-1 rounded-xl text-xs shadow-xs">
            <button
              onClick={() => setViewMode('grouped')}
              className={`px-2.5 py-1 rounded-lg transition-colors flex items-center gap-1.5 cursor-pointer ${
                viewMode === 'grouped' ? 'bg-slate-100 dark:bg-neutral-800 text-slate-900 dark:text-white font-bold' : 'text-slate-500 dark:text-neutral-400 hover:text-slate-900 dark:hover:text-white'
              }`}
              title="按分组模块聚合展示"
            >
              <Layers className="w-3.5 h-3.5" />
              <span>分组聚合</span>
            </button>
            <button
              onClick={() => setViewMode('grid')}
              className={`px-2.5 py-1 rounded-lg transition-colors flex items-center gap-1.5 cursor-pointer ${
                viewMode === 'grid' ? 'bg-slate-100 dark:bg-neutral-800 text-slate-900 dark:text-white font-bold' : 'text-slate-500 dark:text-neutral-400 hover:text-slate-900 dark:hover:text-white'
              }`}
              title="普通网格展示"
            >
              <Grid className="w-3.5 h-3.5" />
              <span>全量平铺</span>
            </button>
          </div>

          <button
            onClick={() => setGroupModalOpen(true)}
            className="px-3.5 py-2.5 rounded-xl bg-white dark:bg-neutral-900 border border-slate-200 dark:border-neutral-800 hover:bg-slate-50 dark:hover:bg-neutral-800 text-slate-700 dark:text-neutral-200 font-bold text-xs flex items-center gap-2 transition-colors shrink-0 cursor-pointer"
          >
            <Layers className="w-4 h-4" />
            <span>分组管理</span>
          </button>

          <button
            onClick={onOpenTrash}
            className="px-3.5 py-2.5 rounded-xl bg-white dark:bg-neutral-900 border border-slate-200 dark:border-neutral-800 hover:bg-slate-50 dark:hover:bg-neutral-800 text-slate-700 dark:text-neutral-200 font-bold text-xs flex items-center gap-2 transition-colors shrink-0 cursor-pointer"
            title="查看、还原或永久删除已移除的实例"
          >
            <ArchiveRestore className="w-4 h-4" />
            <span>实例回收站</span>
          </button>

          <button
            onClick={onOpenCreateModal}
            className="px-4 py-2.5 rounded-xl bg-emerald-500 hover:bg-emerald-400 text-neutral-950 font-bold text-xs flex items-center gap-2 shadow-lg shadow-emerald-500/20 active:scale-98 transition-all shrink-0 cursor-pointer"
          >
            <Plus className="w-4 h-4 stroke-[2.5]" />
            <span>新建隔离实例</span>
          </button>
        </div>
      </div>

      {/* Filter and Search Bar */}
      <div className="flex flex-col lg:flex-row lg:items-center justify-between gap-3">
        {/* Search input */}
        <div className="relative flex-1 max-w-md">
          <Search className="w-4 h-4 text-slate-400 dark:text-neutral-500 absolute left-3 top-1/2 -translate-y-1/2" />
          <input
            type="text"
            value={searchQuery}
            onChange={(e) => setSearchQuery(e.target.value)}
            placeholder="搜索实例名称、分组、MC 版本或加载器..."
            className="w-full pl-9 pr-4 py-2 bg-white dark:bg-neutral-900/80 border border-slate-200 dark:border-neutral-800 rounded-lg text-xs text-slate-900 dark:text-neutral-200 placeholder-slate-400 dark:placeholder-neutral-500 focus:outline-none focus:border-emerald-500 transition-colors shadow-xs"
          />
        </div>

        {/* Partition Tabs */}
        <div className="flex items-center gap-1.5 flex-wrap">
          <button
            onClick={() => setSelectedPartition('all')}
            className={`px-3 py-1.5 rounded-lg text-xs font-medium transition-colors cursor-pointer ${
              selectedPartition === 'all'
                ? 'bg-emerald-500/15 border border-emerald-500/30 text-emerald-800 dark:text-emerald-300 font-bold'
                : 'text-slate-600 dark:text-neutral-400 hover:text-slate-900 dark:hover:text-neutral-200 hover:bg-slate-200/60 dark:hover:bg-neutral-900/60'
            }`}
          >
            全部分组 ({instances.length})
          </button>
          {groupNames.map((p) => (
            <button
              key={p}
              onClick={() => setSelectedPartition(p)}
              className={`px-3 py-1.5 rounded-lg text-xs font-medium transition-colors cursor-pointer ${
                selectedPartition === p
                  ? 'bg-slate-200 dark:bg-neutral-800 text-slate-900 dark:text-neutral-100 font-semibold border border-slate-300 dark:border-neutral-700'
                  : 'text-slate-600 dark:text-neutral-400 hover:text-slate-900 dark:hover:text-neutral-200 hover:bg-slate-200/60 dark:hover:bg-neutral-900/60'
              }`}
            >
              {p} ({countForGroup(p)})
            </button>
          ))}
        </div>
      </div>

      {/* INSTANCES RENDERING: GROUPED OR GRID */}
      {viewMode === 'grouped' ? (
        <div className="space-y-8">
          {(selectedPartition === 'all' ? groupNames : [selectedPartition]).map((partitionName) => {
            const partitionInstances = filteredInstances.filter(i => (i.partition || defaultGroup) === partitionName);
            if (partitionInstances.length === 0 && searchQuery.trim() !== '') return null;

            return (
              <div key={partitionName} className="space-y-3">
                <div className="flex items-center justify-between pb-1.5 border-b border-slate-200 dark:border-neutral-800/60">
                  <div className="flex items-center gap-2">
                    <span className="w-2.5 h-2.5 rounded-full bg-emerald-500"></span>
                    <h2 className="text-sm font-bold text-slate-900 dark:text-neutral-200">{partitionName}</h2>
                    <span className="text-[11px] font-mono text-slate-400 dark:text-neutral-500">
                      ({partitionInstances.length} 个独立版本)
                    </span>
                  </div>
                  <span className="text-[11px] text-slate-500 dark:text-neutral-500 font-mono">
                    实例分组：{partitionName}
                  </span>
                </div>

                {partitionInstances.length === 0 ? (
                  <div className="rounded-xl border border-dashed border-slate-300 dark:border-neutral-700 p-6 text-center text-xs text-slate-500 dark:text-neutral-400">
                    该分组暂无实例
                  </div>
                ) : (
                  <div className="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-3 gap-4">
                    {partitionInstances.map((inst) => renderInstanceCard(inst))}
                  </div>
                )}
              </div>
            );
          })}
        </div>
      ) : (
        <div className="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-3 gap-4">
          {filteredInstances.map((inst) => renderInstanceCard(inst))}
        </div>
      )}

      {filteredInstances.length === 0 && (
        <div className="text-center py-16 space-y-3 bg-white dark:bg-neutral-900/30 rounded-2xl border border-slate-200 dark:border-neutral-800/60 shadow-xs">
          <HardDrive className="w-8 h-8 text-slate-400 dark:text-neutral-600 mx-auto" />
          <h3 className="text-sm font-semibold text-slate-800 dark:text-neutral-300">此分组暂无匹配实例</h3>
          <p className="text-xs text-slate-500 dark:text-neutral-500 max-w-sm mx-auto">
            点击右上角创建原版实例，游戏资源会下载到独立目录。
          </p>
        </div>
      )}

      {groupModalOpen && (
        <GroupManagerModal
          onClose={() => setGroupModalOpen(false)}
          groups={groups}
          defaultGroup={defaultGroup}
          onCreateGroup={onCreateGroup}
          onDeleteGroup={onDeleteGroup}
        />
      )}
    </div>
  );

  function renderInstanceCard(inst: Instance) {
    const isCurrent = inst.id === currentInstanceId;

    return (
      <div
        key={inst.id}
        className={`group rounded-xl border p-4 flex flex-col justify-between transition-all bg-white dark:bg-neutral-900/50 hover:bg-slate-50/90 dark:hover:bg-neutral-900/90 relative shadow-xs ${
          isCurrent
            ? 'border-emerald-500/60 ring-1 ring-emerald-500/30 shadow-md shadow-emerald-500/10'
            : 'border-slate-200 dark:border-neutral-800 hover:border-slate-300 dark:hover:border-neutral-700'
        }`}
      >
        {/* Card Header */}
        <div className="flex items-start justify-between gap-3">
          <div className="flex items-center gap-3">
            <div className="w-10 h-10 rounded-xl bg-slate-100 dark:bg-neutral-800/90 border border-slate-200 dark:border-neutral-700 flex items-center justify-center text-xl shrink-0 shadow-inner">
              {inst.icon}
            </div>
            <div className="min-w-0">
              <div className="flex items-center gap-2">
                <h3 className="text-sm font-bold text-slate-900 dark:text-neutral-100 truncate group-hover:text-emerald-600 dark:group-hover:text-emerald-400 transition-colors">
                  {inst.name}
                </h3>
                {inst.isFavorite && (
                  <Star className="w-3.5 h-3.5 text-amber-500 fill-amber-500 shrink-0" />
                )}
              </div>
              <div className="flex flex-wrap items-center gap-2 text-[11px] text-slate-500 dark:text-neutral-400 mt-0.5">
                <span className="font-semibold text-slate-700 dark:text-neutral-300 whitespace-nowrap">MC {inst.mcVersion}</span>
                <span>·</span>
                <span className="font-mono text-emerald-600 dark:text-emerald-400 uppercase text-[10px] font-bold whitespace-nowrap">
                  {loaderDisplayName(inst.loader)}{inst.loader !== 'vanilla' && inst.loaderVersion ? ` ${inst.loaderVersion}` : ''}
                </span>
                <span>·</span>
                <span className="text-slate-500 dark:text-neutral-500 text-[10px] bg-slate-100 dark:bg-neutral-800 px-1 rounded whitespace-nowrap">
                  {inst.partition}
                </span>
              </div>
            </div>
          </div>

          {isCurrent ? (
            <span className="text-[10px] font-semibold text-emerald-700 dark:text-emerald-400 bg-emerald-500/10 border border-emerald-500/30 px-2 py-0.5 rounded-full shrink-0">
              当前选用
            </span>
          ) : (
            <button
              onClick={() => onSelectCurrent(inst)}
              className="opacity-0 group-hover:opacity-100 text-[10px] text-slate-600 dark:text-neutral-400 hover:text-slate-900 dark:hover:text-white px-2 py-0.5 rounded bg-slate-100 dark:bg-neutral-800 hover:bg-slate-200 dark:hover:bg-neutral-700 border border-slate-200 dark:border-neutral-700 transition-all shrink-0 cursor-pointer"
            >
              设为当前
            </button>
          )}
        </div>

        {/* Description & Sandbox path */}
        <div className="mt-3">
          <p className="text-xs text-slate-600 dark:text-neutral-400 line-clamp-2 leading-relaxed">
            {inst.description}
          </p>
          <div className="mt-2 text-[10px] font-mono text-slate-500 dark:text-neutral-500 truncate flex items-center gap-1">
            <ShieldCheck className="w-3 h-3 text-emerald-600 dark:text-emerald-400 shrink-0" />
            <span>独立沙盒: {inst.folderPath}</span>
          </div>
        </div>

        {/* Spec list */}
        <div className="grid grid-cols-3 gap-2 py-2.5 my-3 border-y border-slate-100 dark:border-neutral-800/60 text-[11px]">
          <div>
            <span className="text-slate-400 dark:text-neutral-500 block text-[10px]">模组加载</span>
            <span className="font-semibold text-slate-800 dark:text-neutral-200">{inst.modsCount} 个已启用</span>
          </div>
          <div>
            <span className="text-slate-400 dark:text-neutral-500 block text-[10px]">分配内存</span>
            <span className="font-semibold text-slate-800 dark:text-neutral-200">{inst.memoryMb} MB</span>
          </div>
          <div>
            <span className="text-slate-400 dark:text-neutral-500 block text-[10px]">本地 Java</span>
            <span className="font-semibold text-slate-800 dark:text-neutral-200 truncate block">
              {inst.javaVersion.split(' ')[0]} {inst.javaVersion.split(' ')[1]}
            </span>
          </div>
        </div>

        <div className="mb-3 text-xs space-y-2" role="status">
          <p className={inst.installationState === 'failed' ? 'text-rose-500' : 'text-slate-500'}>
            {inst.installationState === 'installing' ? `正在安装 ${Math.round((inst.installationProgress ?? 0) * 100)}%` : inst.installationState === 'installed' ? '游戏资源已安装' : inst.installationState === 'failed' ? `安装失败：${inst.installationError}` : '尚未安装游戏资源'}
          </p>
          {inst.installationState === 'installing' && <progress aria-label="实例安装进度" className="w-full h-1.5 accent-emerald-500" max={1} value={inst.installationProgress ?? 0} />}
        </div>
        {/* Bottom Actions */}
        <div className="flex flex-wrap items-center justify-between gap-2 pt-1">
          {/* Secondary tools */}
          <div className="flex items-center gap-1 text-slate-500 dark:text-neutral-400">
            <button
              onClick={() => onOpenManageInstance(inst)}
              className="px-2 py-1 rounded-lg bg-slate-100 dark:bg-neutral-800 hover:bg-slate-200 dark:hover:bg-neutral-700 text-xs text-slate-800 dark:text-neutral-200 flex items-center gap-1 whitespace-nowrap shrink-0 transition-colors cursor-pointer"
              title="修改实例配置"
            >
              <Settings className="w-3.5 h-3.5 text-emerald-600 dark:text-emerald-400" />
              <span>管理版本配置</span>
            </button>

            <button
              onClick={() => onOpenFolder(inst.id)}
              className="p-1.5 rounded-lg hover:text-slate-900 dark:hover:text-neutral-200 hover:bg-slate-100 dark:hover:bg-neutral-800 transition-colors cursor-pointer"
              title="打开实例专属文件夹"
            >
              <FolderOpen className="w-3.5 h-3.5" />
            </button>

            <button
              onClick={() => onDuplicateInstance(inst)}
              className="p-1.5 rounded-lg hover:text-slate-900 dark:hover:text-neutral-200 hover:bg-slate-100 dark:hover:bg-neutral-800 transition-colors cursor-pointer"
              title="复制配置并安装新实例（不复制存档）"
            >
              <Copy className="w-3.5 h-3.5" />
            </button>

            {(
              <button
                onClick={() => onDeleteInstance(inst.id)}
                className="p-1.5 rounded-lg hover:text-red-600 dark:hover:text-red-400 hover:bg-red-500/10 transition-colors cursor-pointer"
                title="移入实例回收站"
              >
                <Trash2 className="w-3.5 h-3.5" />
              </button>
            )}
          </div>

          {/* Primary Launch Action */}
          <button
            onClick={() => inst.installationState === 'installed' ? onLaunchInstance(inst) : onRetryInstall(inst)}
            disabled={inst.installationState === 'installing'}
            className="px-3.5 py-1.5 rounded-lg bg-emerald-500/15 hover:bg-emerald-500 text-emerald-800 dark:text-emerald-300 hover:text-neutral-950 font-bold text-xs flex items-center gap-1.5 whitespace-nowrap shrink-0 transition-all cursor-pointer"
          >
            <Play className="w-3.5 h-3.5 fill-current" />
            <span>{inst.installationState === 'installing' ? '安装中…' : inst.installationState === 'failed' ? '重试安装' : inst.installationState === 'installed' ? '启动' : '安装'}</span>
          </button>
        </div>
      </div>
    );
  }
};
