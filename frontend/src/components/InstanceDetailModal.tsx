import React, { useState } from 'react';
import {
  X,
  Package,
  Sparkles,
  Save,
  Trash2,
  FolderOpen,
  Sliders,
  CheckCircle2,
  HardDrive,
  UploadCloud,
  Layers,
  ArrowUp,
  ArrowDown,
  RefreshCw,
  Plus,
  AlertTriangle,
  Download,
  Sun,
  Palette,
  Coffee,
  Check
} from 'lucide-react';
import {
  Instance,
  ModItem,
  LoaderType,
  ShaderPackItem,
  ResourcePackItem,
  JavaRuntime,
  InstanceGroup
} from '../types/launcher';
import { AppSelect } from './AppSelect';
import { MOCK_MODS, MOCK_SHADERS, MOCK_RESOURCEPACKS } from '../data/mockData';

interface InstanceDetailModalProps {
  /** 当前要管理的实例；组件只在实例存在时挂载，因此这里是必填。 */
  instance: Instance;
  onClose: () => void;
  onUpdateInstance: (updated: Instance) => Promise<void>;
  onOpenFolder: (path: string) => void;
  javaRuntimes: JavaRuntime[];
  groups: InstanceGroup[];
  defaultGroup: string;
}

export const InstanceDetailModal: React.FC<InstanceDetailModalProps> = ({
  instance,
  onClose,
  onUpdateInstance,
  onOpenFolder,
  javaRuntimes,
  groups,
  defaultGroup
}) => {
  const [activeTab, setActiveTab] = useState<'loaders' | 'mods' | 'shaders' | 'resourcepacks' | 'saves' | 'settings'>('settings');

  const [name, setName] = useState(instance.name);
  const [description, setDescription] = useState(instance.description);
  const [favorite, setFavorite] = useState(instance.isFavorite);
  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState<string | null>(null);

  // Instance State
  const [currentLoader, setCurrentLoader] = useState<LoaderType>(instance.loader);
  const [loaderVersion, setLoaderVersion] = useState(instance.loaderVersion);
  const [modsList, setModsList] = useState<ModItem[]>([]);
  const [shaderList, setShaderList] = useState<ShaderPackItem[]>([]);
  const [resourcePackList, setResourcePackList] = useState<ResourcePackItem[]>([]);
  const [memoryMb, setMemoryMb] = useState(instance.memoryMb);
  const [jvmArgs, setJvmArgs] = useState(instance.jvmArgs);
  const [selectedJavaPath, setSelectedJavaPath] = useState(instance.customJavaPath || '');
  const [partitionName, setPartitionName] = useState(instance.partition || defaultGroup);

  // Mod Search & Filter
  const [modSearch, setModSearch] = useState('');
  const [modFilter, setModFilter] = useState<'all' | 'enabled' | 'disabled' | 'update'>('all');

  // Loaders upgrade / switch state
  const [isUpdatingLoader, setIsUpdatingLoader] = useState(false);
  const [loaderInstallOpen, setLoaderInstallOpen] = useState(false);
  const [newLoaderType, setNewLoaderType] = useState<LoaderType>('fabric');
  const [newLoaderVer, setNewLoaderVer] = useState('0.16.14');

  const [notification, setNotification] = useState<string | null>(null);

  const showNotify = (msg: string) => {
    setNotification(msg);
    setTimeout(() => setNotification(null), 2500);
  };

  // --- Loader Operations ---
  const handleUpdateLoader = (targetVer: string) => {
    setIsUpdatingLoader(true);
    setTimeout(() => {
      setIsUpdatingLoader(false);
      setLoaderVersion(targetVer);
      showNotify(`已将 ${currentLoader.toUpperCase()} 加载器升级至 v${targetVer}`);
    }, 1000);
  };

  const handleInstallLoader = () => {
    setCurrentLoader(newLoaderType);
    setLoaderVersion(newLoaderVer);
    setLoaderInstallOpen(false);
    showNotify(`已成功为实例安装 ${newLoaderType.toUpperCase()} v${newLoaderVer} 加载器`);
  };

  const handleUninstallLoader = () => {
    if (confirm('确认卸载当前模组加载器并回退为原版 (Vanilla) 吗？已有模组将被自动停用。')) {
      setCurrentLoader('vanilla');
      setLoaderVersion('Release');
      setModsList(prev => prev.map(m => ({ ...m, enabled: false })));
      showNotify('已卸载加载器，实例已切换为原版纯净模式');
    }
  };

  // --- Mod Operations ---
  const toggleMod = (modId: string) => {
    setModsList(prev =>
      prev.map(m => (m.id === modId ? { ...m, enabled: !m.enabled } : m))
    );
  };

  const removeMod = (modId: string) => {
    const mod = modsList.find(m => m.id === modId);
    setModsList(prev => prev.filter(m => m.id !== modId));
    showNotify(`已从实例移除模组: ${mod?.name || ''}`);
  };

  const updateMod = (modId: string) => {
    setModsList(prev =>
      prev.map(m =>
        m.id === modId
          ? { ...m, installedVersion: m.latestVersion, updateAvailable: false }
          : m
      )
    );
    showNotify('模组已更新至最新稳定版');
  };

  const handleEnableAllMods = (enable: boolean) => {
    setModsList(prev => prev.map(m => ({ ...m, enabled: enable })));
    showNotify(enable ? '已启用全部模组' : '已停用全部模组');
  };

  const handleAddNewMod = () => {
    const newMod: ModItem = {
      id: `mod-${Date.now()}`,
      name: 'Just Enough Items (JEI)',
      fileName: 'jei-1.21.4-fabric-19.8.0.75.jar',
      slug: 'jei',
      author: 'mezz',
      summary: '物品与配方查阅工具',
      downloads: '240M',
      follows: '890K',
      category: 'tech',
      loaders: ['fabric', 'forge'],
      gameVersions: ['1.21.4'],
      iconUrl: '📖',
      installed: true,
      enabled: true,
      installedVersion: '19.8.0.75',
      latestVersion: '19.8.0.75',
      fileSizeBytes: 1850000,
      source: 'modrinth'
    };
    setModsList(prev => [newMod, ...prev]);
    showNotify('已添加新模组: Just Enough Items');
  };

  // --- Shaderpack Operations ---
  const toggleShader = (shaderId: string) => {
    setShaderList(prev =>
      prev.map(s => ({
        ...s,
        enabled: s.id === shaderId ? !s.enabled : false // only 1 active shader
      }))
    );
  };

  const removeShader = (shaderId: string) => {
    setShaderList(prev => prev.filter(s => s.id !== shaderId));
    showNotify('已删除光影包');
  };

  const changeShaderProfile = (shaderId: string, profile: 'Low' | 'Medium' | 'High' | 'Ultra') => {
    setShaderList(prev =>
      prev.map(s => (s.id === shaderId ? { ...s, profile } : s))
    );
  };

  const handleAddShader = () => {
    const newShader: ShaderPackItem = {
      id: `shader-${Date.now()}`,
      name: 'SEUS Renewed v1.0.1',
      fileName: 'SEUS_Renewed_v1.0.1.zip',
      author: 'Sonic Ether',
      description: '老牌经典超逼真阳光与水面反射光影。',
      enabled: true,
      profile: 'High',
      fileSizeBytes: 16800000
    };
    setShaderList(prev => [newShader, ...prev.map(s => ({ ...s, enabled: false }))]);
    showNotify('已导入并启用 SEUS Renewed 光影包');
  };

  // --- Resourcepack Operations ---
  const toggleResourcePack = (packId: string) => {
    setResourcePackList(prev =>
      prev.map(p => (p.id === packId ? { ...p, enabled: !p.enabled } : p))
    );
  };

  const moveResourcePackPriority = (index: number, direction: 'up' | 'down') => {
    const newList = [...resourcePackList];
    const targetIndex = direction === 'up' ? index - 1 : index + 1;
    if (targetIndex < 0 || targetIndex >= newList.length) return;

    const temp = newList[index];
    newList[index] = newList[targetIndex];
    newList[targetIndex] = temp;

    // re-assign priority
    setResourcePackList(newList.map((item, idx) => ({ ...item, priority: idx + 1 })));
  };

  const removeResourcePack = (packId: string) => {
    setResourcePackList(prev => prev.filter(p => p.id !== packId));
    showNotify('已删除材质包');
  };

  const handleAddResourcePack = () => {
    const newPack: ResourcePackItem = {
      id: `rp-${Date.now()}`,
      name: 'Default 3D (3D 方块立体模型)',
      fileName: 'Default3D_High_1.21.zip',
      description: '为原版方块添加真实的 3D 凹凸立体几何模型。',
      resolution: '32x',
      enabled: true,
      priority: 1,
      icon: '🧊',
      fileSizeBytes: 38200000
    };
    setResourcePackList(prev => [newPack, ...prev]);
    showNotify('已导入 Default 3D 材质包');
  };

  // --- Save / Persist All Changes ---
  const handleSaveAll = async () => {
    if (saving) return;
    setSaving(true);
    setSaveError(null);
    try {
      await onUpdateInstance({ ...instance, name, description, isFavorite: favorite, partition: partitionName, memoryMb, jvmArgs, customJavaPath: selectedJavaPath });
      onClose();
    } catch (cause) {
      setSaveError(cause instanceof Error ? cause.message : String(cause));
      setSaving(false);
    }
  };

  const filteredMods = modsList.filter(m => {
    const matchesSearch = m.name.toLowerCase().includes(modSearch.toLowerCase()) ||
      m.fileName.toLowerCase().includes(modSearch.toLowerCase());
    if (modFilter === 'enabled') return matchesSearch && m.enabled;
    if (modFilter === 'disabled') return matchesSearch && !m.enabled;
    if (modFilter === 'update') return matchesSearch && m.updateAvailable;
    return matchesSearch;
  });

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center p-4 bg-black/60 dark:bg-black/80 backdrop-blur-sm animate-in fade-in duration-200">
      <div className="w-full max-w-4xl bg-white dark:bg-neutral-900 border border-slate-200 dark:border-neutral-700/80 rounded-2xl shadow-2xl flex flex-col max-h-[88vh] overflow-hidden">
        {/* Header */}
        <div className="px-6 py-4 border-b border-slate-200 dark:border-neutral-800 flex items-center justify-between bg-slate-50 dark:bg-neutral-950/70">
          <div className="flex items-center gap-3">
            <span className="text-3xl p-1.5 bg-slate-100 dark:bg-neutral-800 rounded-xl border border-slate-200 dark:border-neutral-700">{instance.icon}</span>
            <div>
              <div className="flex items-center gap-2">
                <h2 className="text-base font-bold text-slate-900 dark:text-neutral-100">{instance.name}</h2>
                <span className="text-[10px] font-mono uppercase bg-emerald-500/20 text-emerald-800 dark:text-emerald-300 px-2 py-0.5 rounded font-bold">
                  {currentLoader} {loaderVersion}
                </span>
                <span className="text-[10px] bg-slate-200 dark:bg-neutral-800 text-slate-700 dark:text-neutral-400 px-2 py-0.5 rounded font-medium">
                  分区: {partitionName}
                </span>
              </div>
              <p className="text-xs text-slate-500 dark:text-neutral-400 mt-1 font-mono flex items-center gap-2">
                <span>隔离沙盒: {instance.folderPath}</span>
                <span className="text-emerald-600 dark:text-emerald-400 font-sans font-medium">· 互不影响</span>
              </p>
            </div>
          </div>

          <div className="flex items-center gap-2">
            <button
              onClick={() => onOpenFolder(instance.folderPath)}
              className="px-2.5 py-1.5 rounded-lg bg-slate-100 dark:bg-neutral-800 hover:bg-slate-200 dark:hover:bg-neutral-700 text-xs text-slate-700 dark:text-neutral-300 flex items-center gap-1.5 transition-colors cursor-pointer"
            >
              <FolderOpen className="w-3.5 h-3.5 text-slate-400 dark:text-neutral-400" />
              <span>打开实例文件夹</span>
            </button>
            <button
              onClick={onClose}
              className="p-1.5 rounded-lg text-slate-400 dark:text-neutral-400 hover:text-slate-800 dark:hover:text-white hover:bg-slate-100 dark:hover:bg-neutral-800 transition-colors cursor-pointer"
            >
              <X className="w-4 h-4" />
            </button>
          </div>
        </div>

        {/* Tab Navigation */}
        <div className="flex items-center gap-1 px-6 border-b border-slate-200 dark:border-neutral-800 bg-slate-100/60 dark:bg-neutral-950/40 text-xs font-medium overflow-x-auto">
          {[
            { id: 'loaders' as const, label: `加载器 (${currentLoader.toUpperCase()})`, icon: Layers },
            { id: 'mods' as const, label: `模组管理 (${modsList.length})`, icon: Package },
            { id: 'shaders' as const, label: `光影包 (${shaderList.length})`, icon: Sun },
            { id: 'resourcepacks' as const, label: `资源材质包 (${resourcePackList.length})`, icon: Palette },
            { id: 'settings' as const, label: '实例独立设置与 Java', icon: Sliders },
          ].map((tab) => {
            const Icon = tab.icon;
            const isActive = activeTab === tab.id;
            return (
              <button
                key={tab.id}
                disabled={tab.id !== 'settings'}
                title={tab.id === 'settings' ? '修改持久化实例配置' : '尚未接入'}
                onClick={() => setActiveTab(tab.id)}
                className={`py-3 px-3.5 border-b-2 flex items-center gap-2 transition-all cursor-pointer whitespace-nowrap disabled:opacity-40 disabled:cursor-not-allowed ${
                  isActive
                    ? 'border-emerald-500 text-emerald-600 dark:text-emerald-400 font-bold'
                    : 'border-transparent text-slate-600 dark:text-neutral-400 hover:text-slate-900 dark:hover:text-neutral-200'
                }`}
              >
                <Icon className={`w-3.5 h-3.5 ${isActive ? 'text-emerald-600 dark:text-emerald-400' : 'text-slate-400 dark:text-neutral-500'}`} />
                <span>{tab.label}</span>
              </button>
            );
          })}
        </div>

        {/* Tab Body */}
        <div className="p-6 flex-1 overflow-y-auto">
          {/* TAB 1: LOADERS MANAGEMENT (Install, Uninstall, Update) */}
          {activeTab === 'loaders' && (
            <div className="space-y-5">
              <div className="p-4 rounded-xl bg-slate-50 dark:bg-neutral-950/60 border border-slate-200 dark:border-neutral-800 space-y-4">
                <div className="flex items-start justify-between gap-4">
                  <div>
                    <span className="text-[11px] text-slate-500 dark:text-neutral-500 block uppercase tracking-wider font-semibold">
                      当前已安装的核心模组加载器
                    </span>
                    <div className="flex items-center gap-2.5 mt-1">
                      <span className="text-lg font-bold text-slate-900 dark:text-neutral-100">
                        {currentLoader === 'vanilla' ? '原版纯净核心 (Vanilla)' : `${currentLoader.toUpperCase()} 加载器`}
                      </span>
                      <span className="text-xs font-mono bg-emerald-500/15 text-emerald-700 dark:text-emerald-300 border border-emerald-500/30 px-2 py-0.5 rounded font-bold">
                        v{loaderVersion}
                      </span>
                    </div>
                    <p className="text-xs text-slate-600 dark:text-neutral-400 mt-1">
                      基于独立实例目录隔离运行，更换或卸载加载器不会影响其它游戏版本。
                    </p>
                  </div>

                  <div className="flex items-center gap-2">
                    <button
                      onClick={() => setLoaderInstallOpen(true)}
                      className="px-3 py-1.5 rounded-lg bg-emerald-500 hover:bg-emerald-400 text-neutral-950 font-bold text-xs flex items-center gap-1.5 transition-all cursor-pointer shadow-sm"
                    >
                      <Layers className="w-3.5 h-3.5" />
                      <span>更换/安装新加载器</span>
                    </button>

                    {currentLoader !== 'vanilla' && (
                      <button
                        onClick={handleUninstallLoader}
                        className="px-3 py-1.5 rounded-lg bg-red-500/15 hover:bg-red-500 text-red-600 dark:text-red-300 hover:text-white font-semibold text-xs transition-colors cursor-pointer"
                      >
                        卸载加载器 (回退原版)
                      </button>
                    )}
                  </div>
                </div>

                {/* Update Available Banner if applicable */}
                {currentLoader === 'fabric' && loaderVersion === '0.16.10' && (
                  <div className="p-3 rounded-lg bg-amber-500/10 border border-amber-500/30 flex items-center justify-between text-xs">
                    <div className="flex items-center gap-2 text-amber-800 dark:text-amber-300">
                      <AlertTriangle className="w-4 h-4 shrink-0 text-amber-500" />
                      <span>检测到 Fabric Loader 官方发布了最新稳定版本 <strong>v0.16.14</strong></span>
                    </div>
                    <button
                      onClick={() => handleUpdateLoader('0.16.14')}
                      disabled={isUpdatingLoader}
                      className="px-3 py-1 rounded bg-amber-500 hover:bg-amber-400 text-neutral-950 font-bold text-xs flex items-center gap-1.5 cursor-pointer shadow-sm"
                    >
                      {isUpdatingLoader ? (
                        <>
                          <RefreshCw className="w-3 h-3 animate-spin" />
                          <span>更新中...</span>
                        </>
                      ) : (
                        <>
                          <Download className="w-3 h-3" />
                          <span>一键更新加载器</span>
                        </>
                      )}
                    </button>
                  </div>
                )}
              </div>

              {/* Supported Loaders Overview for this Minecraft Version */}
              <div>
                <h3 className="text-xs font-semibold text-slate-700 dark:text-neutral-300 mb-2">
                  MC {instance.mcVersion} 可用的主要加载器生态支持
                </h3>
                <div className="grid grid-cols-1 sm:grid-cols-3 gap-3 text-xs">
                  {[
                    { name: 'Fabric', ver: '0.16.14', desc: '轻量高效，加载迅速，适合现代优化与客户端功能', rec: true },
                    { name: 'NeoForge', ver: '21.1.80', desc: 'Forge 新分支生态，兼容大型复杂系统模组', rec: false },
                    { name: 'Forge', ver: '47.3.12', desc: '传统工业与大型科技模组常用加载器', rec: false },
                  ].map((item, idx) => (
                    <div key={idx} className="p-3 rounded-xl bg-slate-50 dark:bg-neutral-950/40 border border-slate-200 dark:border-neutral-800 space-y-1">
                      <div className="flex items-center justify-between font-bold text-slate-800 dark:text-neutral-200">
                        <span>{item.name}</span>
                        <span className="font-mono text-[11px] text-slate-500 dark:text-neutral-400">v{item.ver}</span>
                      </div>
                      <p className="text-[11px] text-slate-500 dark:text-neutral-400">{item.desc}</p>
                    </div>
                  ))}
                </div>
              </div>
            </div>
          )}

          {/* TAB 2: MODS MANAGEMENT (Enable, Disable, Delete, Update, Add) */}
          {activeTab === 'mods' && (
            <div className="space-y-4">
              {/* Filter and Action bar */}
              <div className="flex flex-col sm:flex-row sm:items-center justify-between gap-3 text-xs">
                <div className="flex items-center gap-2">
                  <input
                    type="text"
                    value={modSearch}
                    onChange={(e) => setModSearch(e.target.value)}
                    placeholder="过滤模组名称或文件名..."
                    className="px-3 py-1.5 bg-slate-50 dark:bg-neutral-950 border border-slate-300 dark:border-neutral-800 rounded-lg text-xs text-slate-800 dark:text-neutral-200 focus:outline-none focus:border-emerald-500 w-52"
                  />
                  <div className="flex items-center gap-1 bg-slate-100 dark:bg-neutral-950 p-0.5 rounded-lg border border-slate-200 dark:border-neutral-800">
                    <button
                      onClick={() => setModFilter('all')}
                      className={`px-2 py-1 rounded text-[11px] cursor-pointer transition-colors ${modFilter === 'all' ? 'bg-white dark:bg-neutral-800 text-slate-900 dark:text-white font-bold shadow-xs' : 'text-slate-500 dark:text-neutral-400'}`}
                    >
                      全部 ({modsList.length})
                    </button>
                    <button
                      onClick={() => setModFilter('enabled')}
                      className={`px-2 py-1 rounded text-[11px] cursor-pointer transition-colors ${modFilter === 'enabled' ? 'bg-white dark:bg-neutral-800 text-slate-900 dark:text-white font-bold shadow-xs' : 'text-slate-500 dark:text-neutral-400'}`}
                    >
                      已启用 ({modsList.filter(m => m.enabled).length})
                    </button>
                    <button
                      onClick={() => setModFilter('update')}
                      className={`px-2 py-1 rounded text-[11px] cursor-pointer transition-colors ${modFilter === 'update' ? 'bg-amber-500/20 text-amber-800 dark:text-amber-300 font-bold shadow-xs' : 'text-slate-500 dark:text-neutral-400'}`}
                    >
                      可更新 ({modsList.filter(m => m.updateAvailable).length})
                    </button>
                  </div>
                </div>

                <div className="flex items-center gap-2">
                  <button
                    onClick={() => handleEnableAllMods(true)}
                    className="px-2.5 py-1.5 rounded-lg bg-slate-100 dark:bg-neutral-800 hover:bg-slate-200 dark:hover:bg-neutral-700 text-slate-700 dark:text-neutral-300 transition-colors cursor-pointer"
                  >
                    全部启用
                  </button>
                  <button
                    onClick={() => handleEnableAllMods(false)}
                    className="px-2.5 py-1.5 rounded-lg bg-slate-100 dark:bg-neutral-800 hover:bg-slate-200 dark:hover:bg-neutral-700 text-slate-700 dark:text-neutral-300 transition-colors cursor-pointer"
                  >
                    全部停用
                  </button>
                  <button
                    onClick={handleAddNewMod}
                    className="px-3 py-1.5 rounded-lg bg-emerald-500 hover:bg-emerald-400 text-neutral-950 font-bold flex items-center gap-1 transition-all cursor-pointer shadow-sm"
                  >
                    <Plus className="w-3.5 h-3.5" />
                    <span>添加模组</span>
                  </button>
                </div>
              </div>

              {/* Mods List */}
              <div className="space-y-2">
                {filteredMods.map((mod) => (
                  <div
                    key={mod.id}
                    className={`p-3 rounded-xl border flex items-center justify-between gap-3 transition-colors ${
                      mod.enabled
                        ? 'bg-slate-50 dark:bg-neutral-950/60 border-slate-200 dark:border-neutral-800'
                        : 'bg-slate-100/50 dark:bg-neutral-950/20 border-slate-200/60 dark:border-neutral-800/40 opacity-50'
                    }`}
                  >
                    <div className="flex items-center gap-3 min-w-0">
                      <span className="text-xl shrink-0">{mod.iconUrl}</span>
                      <div className="min-w-0">
                        <div className="flex items-center gap-2">
                          <span className="text-xs font-bold text-slate-900 dark:text-neutral-200 truncate">{mod.name}</span>
                          <span className="text-[10px] font-mono text-slate-600 dark:text-neutral-400 bg-slate-200 dark:bg-neutral-800 px-1.5 py-0.5 rounded">
                            v{mod.installedVersion || mod.latestVersion}
                          </span>
                          {mod.updateAvailable && (
                            <span className="text-[10px] font-semibold text-amber-700 dark:text-amber-400 bg-amber-500/10 border border-amber-500/30 px-1.5 py-0.5 rounded">
                              可更新至 v{mod.latestVersion}
                            </span>
                          )}
                        </div>
                        <div className="text-[11px] text-slate-500 dark:text-neutral-500 font-mono truncate mt-0.5">
                          {mod.fileName} · {(mod.fileSizeBytes / 1024 / 1024).toFixed(2)} MB
                        </div>
                      </div>
                    </div>

                    <div className="flex items-center gap-2 shrink-0">
                      {mod.updateAvailable && (
                        <button
                          onClick={() => updateMod(mod.id)}
                          className="px-2.5 py-1 rounded bg-amber-500 hover:bg-amber-400 text-neutral-950 font-bold text-[11px] flex items-center gap-1 cursor-pointer transition-colors shadow-xs"
                        >
                          <Download className="w-3 h-3" />
                          <span>更新</span>
                        </button>
                      )}

                      <button
                        onClick={() => toggleMod(mod.id)}
                        className={`text-xs px-2.5 py-1 rounded-md font-medium transition-colors cursor-pointer ${
                          mod.enabled
                            ? 'bg-emerald-500/15 text-emerald-700 dark:text-emerald-300 border border-emerald-500/30 hover:bg-emerald-500/25'
                            : 'bg-slate-200 dark:bg-neutral-800 text-slate-600 dark:text-neutral-400 border border-slate-300 dark:border-neutral-700 hover:bg-slate-300 dark:hover:bg-neutral-700'
                        }`}
                      >
                        {mod.enabled ? '已启用' : '已停用'}
                      </button>

                      <button
                        onClick={() => removeMod(mod.id)}
                        className="p-1.5 text-slate-400 dark:text-neutral-500 hover:text-red-500 dark:hover:text-red-400 rounded hover:bg-slate-100 dark:hover:bg-neutral-800 transition-colors cursor-pointer"
                        title="删除模组"
                      >
                        <Trash2 className="w-3.5 h-3.5" />
                      </button>
                    </div>
                  </div>
                ))}

                {filteredMods.length === 0 && (
                  <div className="text-center py-12 text-slate-400 dark:text-neutral-600 text-xs">
                    未找到匹配的模组文件
                  </div>
                )}
              </div>
            </div>
          )}

          {/* TAB 3: SHADERPACKS (Enable, Disable, Delete, Profile) */}
          {activeTab === 'shaders' && (
            <div className="space-y-4">
              <div className="flex items-center justify-between text-xs">
                <div>
                  <span className="text-slate-700 dark:text-neutral-200 font-semibold">专属光影目录: </span>
                  <code className="text-emerald-600 dark:text-emerald-400 font-mono">{instance.folderPath}/shaderpacks/</code>
                </div>

                <button
                  onClick={handleAddShader}
                  className="px-3 py-1.5 rounded-lg bg-emerald-500 hover:bg-emerald-400 text-neutral-950 font-bold text-xs flex items-center gap-1 transition-all cursor-pointer shadow-sm"
                >
                  <Plus className="w-3.5 h-3.5" />
                  <span>添加光影包 (.zip)</span>
                </button>
              </div>

              <div className="space-y-2.5">
                {shaderList.map((shader) => (
                  <div
                    key={shader.id}
                    className={`p-3.5 rounded-xl border flex items-center justify-between gap-4 transition-all ${
                      shader.enabled
                        ? 'bg-emerald-500/5 dark:bg-neutral-950/80 border-emerald-500/50 ring-1 ring-emerald-500/20'
                        : 'bg-slate-50 dark:bg-neutral-950/40 border-slate-200 dark:border-neutral-800'
                    }`}
                  >
                    <div className="min-w-0">
                      <div className="flex items-center gap-2">
                        <span className="text-xs font-bold text-slate-900 dark:text-neutral-200">{shader.name}</span>
                        <span className="text-[10px] text-slate-500 dark:text-neutral-500 font-mono">作者: {shader.author}</span>
                        {shader.enabled && (
                          <span className="text-[10px] font-bold text-emerald-600 dark:text-emerald-400 bg-emerald-500/10 px-1.5 py-0.5 rounded">
                            当前生效
                          </span>
                        )}
                      </div>
                      <p className="text-[11px] text-slate-600 dark:text-neutral-400 mt-1">{shader.description}</p>
                      <div className="text-[10px] text-slate-400 dark:text-neutral-500 font-mono mt-0.5">
                        {shader.fileName} · {(shader.fileSizeBytes / 1024 / 1024).toFixed(1)} MB
                      </div>
                    </div>

                    <div className="flex items-center gap-3 shrink-0">
                      {/* Profile Selector */}
                      <select
                        value={shader.profile}
                        onChange={(e) => changeShaderProfile(shader.id, e.target.value as any)}
                        disabled={!shader.enabled}
                        className="px-2 py-1 bg-white dark:bg-neutral-900 border border-slate-300 dark:border-neutral-700 rounded text-[11px] text-slate-800 dark:text-neutral-300 disabled:opacity-40"
                      >
                        <option value="Low">预设: 低画质 (Low)</option>
                        <option value="Medium">预设: 中画质 (Medium)</option>
                        <option value="High">预设: 高画质 (High)</option>
                        <option value="Ultra">预设: 极致 (Ultra)</option>
                      </select>

                      <button
                        onClick={() => toggleShader(shader.id)}
                        className={`text-xs px-3 py-1 rounded font-medium transition-colors cursor-pointer ${
                          shader.enabled
                            ? 'bg-emerald-500 text-neutral-950 font-bold'
                            : 'bg-slate-200 dark:bg-neutral-800 hover:bg-slate-300 dark:hover:bg-neutral-700 text-slate-700 dark:text-neutral-300'
                        }`}
                      >
                        {shader.enabled ? '已启用' : '启用'}
                      </button>

                      <button
                        onClick={() => removeShader(shader.id)}
                        className="p-1.5 text-slate-400 dark:text-neutral-500 hover:text-red-500 dark:hover:text-red-400 rounded hover:bg-slate-100 dark:hover:bg-neutral-800 transition-colors cursor-pointer"
                        title="删除光影文件"
                      >
                        <Trash2 className="w-3.5 h-3.5" />
                      </button>
                    </div>
                  </div>
                ))}
              </div>
            </div>
          )}

          {/* TAB 4: RESOURCEPACKS (Priority reordering, Enable, Disable, Delete) */}
          {activeTab === 'resourcepacks' && (
            <div className="space-y-4">
              <div className="flex items-center justify-between text-xs">
                <div>
                  <span className="text-slate-700 dark:text-neutral-200 font-semibold">专属材质包目录: </span>
                  <code className="text-emerald-600 dark:text-emerald-400 font-mono">{instance.folderPath}/resourcepacks/</code>
                </div>

                <button
                  onClick={handleAddResourcePack}
                  className="px-3 py-1.5 rounded-lg bg-emerald-500 hover:bg-emerald-400 text-neutral-950 font-bold text-xs flex items-center gap-1 transition-all cursor-pointer shadow-sm"
                >
                  <Plus className="w-3.5 h-3.5" />
                  <span>添加材质包 (.zip)</span>
                </button>
              </div>

              <div className="p-2.5 rounded-lg bg-slate-100 dark:bg-neutral-950 border border-slate-200 dark:border-neutral-800 text-[11px] text-slate-600 dark:text-neutral-400">
                提示: 处于列表顶部的材质包优先级最高，将优先覆盖下层方块与贴图资源。
              </div>

              <div className="space-y-2">
                {resourcePackList.map((pack, idx) => (
                  <div
                    key={pack.id}
                    className={`p-3 rounded-xl border flex items-center justify-between gap-3 ${
                      pack.enabled
                        ? 'bg-white dark:bg-neutral-950/80 border-slate-200 dark:border-neutral-700 shadow-xs'
                        : 'bg-slate-100/50 dark:bg-neutral-950/30 border-slate-200/60 dark:border-neutral-800/40 opacity-60'
                    }`}
                  >
                    <div className="flex items-center gap-3 min-w-0">
                      <div className="flex flex-col items-center gap-1 shrink-0">
                        <button
                          onClick={() => moveResourcePackPriority(idx, 'up')}
                          disabled={idx === 0}
                          className="p-1 hover:text-slate-900 dark:hover:text-white disabled:opacity-20 text-slate-400 dark:text-neutral-400 cursor-pointer"
                          title="上移优先级"
                        >
                          <ArrowUp className="w-3.5 h-3.5" />
                        </button>
                        <button
                          onClick={() => moveResourcePackPriority(idx, 'down')}
                          disabled={idx === resourcePackList.length - 1}
                          className="p-1 hover:text-slate-900 dark:hover:text-white disabled:opacity-20 text-slate-400 dark:text-neutral-400 cursor-pointer"
                          title="下移优先级"
                        >
                          <ArrowDown className="w-3.5 h-3.5" />
                        </button>
                      </div>

                      <span className="text-2xl shrink-0">{pack.icon}</span>

                      <div className="min-w-0">
                        <div className="flex items-center gap-2">
                          <span className="text-xs font-bold text-slate-900 dark:text-neutral-200">{pack.name}</span>
                          <span className="text-[10px] font-mono bg-slate-100 dark:bg-neutral-800 text-slate-600 dark:text-neutral-400 px-1.5 py-0.5 rounded">
                            {pack.resolution}
                          </span>
                          <span className="text-[10px] text-slate-500 dark:text-neutral-500 font-mono">
                            优先级 #{pack.priority}
                          </span>
                        </div>
                        <p className="text-[11px] text-slate-500 dark:text-neutral-400 mt-0.5">{pack.description}</p>
                      </div>
                    </div>

                    <div className="flex items-center gap-2 shrink-0">
                      <button
                        onClick={() => toggleResourcePack(pack.id)}
                        className={`text-xs px-3 py-1 rounded font-medium transition-colors cursor-pointer ${
                          pack.enabled
                            ? 'bg-emerald-500/15 text-emerald-700 dark:text-emerald-300 border border-emerald-500/30'
                            : 'bg-slate-200 dark:bg-neutral-800 text-slate-600 dark:text-neutral-400'
                        }`}
                      >
                        {pack.enabled ? '已启用' : '已停用'}
                      </button>

                      <button
                        onClick={() => removeResourcePack(pack.id)}
                        className="p-1.5 text-slate-400 dark:text-neutral-500 hover:text-red-500 dark:hover:text-red-400 rounded hover:bg-slate-100 dark:hover:bg-neutral-800 transition-colors cursor-pointer"
                        title="删除材质包"
                      >
                        <Trash2 className="w-3.5 h-3.5" />
                      </button>
                    </div>
                  </div>
                ))}
              </div>
            </div>
          )}

          {/* TAB 5: INSTANCE SETTINGS & JAVA RUNTIME */}
          {activeTab === 'settings' && (
            <div className="space-y-5">
              <p className="text-xs text-slate-500">当前支持原版资源安装和配置持久化，加载器、模组、光影与材质包管理尚未接入。</p>
              <label className="block text-xs space-y-2"><span>实例名称</span><input required maxLength={80} value={name} onChange={e => setName(e.target.value)} className="w-full px-3 py-2 rounded-lg bg-slate-50 dark:bg-neutral-950 border border-slate-300 dark:border-neutral-800" /></label>
              <label className="block text-xs space-y-2"><span>说明</span><textarea maxLength={4096} value={description} onChange={e => setDescription(e.target.value)} className="w-full px-3 py-2 rounded-lg bg-slate-50 dark:bg-neutral-950 border border-slate-300 dark:border-neutral-800" /></label>
              <label className="flex gap-2 text-xs items-center"><input type="checkbox" checked={favorite} onChange={e => setFavorite(e.target.checked)} />收藏此实例</label>
              {/* Partition / Grouping */}
              <div className="p-4 rounded-xl bg-slate-50 dark:bg-neutral-950/60 border border-slate-200 dark:border-neutral-800 space-y-2">
                <label className="text-xs font-semibold text-slate-800 dark:text-neutral-200 block">
                  实例分组
                </label>
                <AppSelect
                  value={partitionName}
                  onChange={setPartitionName}
                  options={groups.map((group) => ({
                    value: group.name,
                    label: group.name,
                    hint: group.isDefault ? '默认分组' : `${group.instanceCount} 个实例`,
                  }))}
                  ariaLabel="实例分组"
                  placeholder={defaultGroup || '选择分组'}
                />
                <p className="text-[11px] text-slate-500 dark:text-neutral-500">
                  分组在版本实例页的「分组管理」中创建和删除。
                </p>
              </div>

              <p className="text-xs text-slate-500">Java 运行时请在启动器的 Java 管理中选择，实例专属 Java 选择尚待启动链路接入。</p>

              {/* Dedicated RAM Allocation */}
              <div className="p-4 rounded-xl bg-slate-50 dark:bg-neutral-950/60 border border-slate-200 dark:border-neutral-800 space-y-3">
                <div className="flex items-center justify-between">
                  <label className="text-xs font-semibold text-slate-700 dark:text-neutral-300">
                    此实例专属内存 (Override RAM)
                  </label>
                  <span className="font-mono text-xs font-bold text-emerald-600 dark:text-emerald-400">
                    {memoryMb} MB ({(memoryMb / 1024).toFixed(1)} GB)
                  </span>
                </div>
                <input
                  type="range"
                  min="512"
                  max="65536"
                  step="256"
                  value={memoryMb}
                  onChange={(e) => setMemoryMb(Number(e.target.value))}
                  className="w-full accent-emerald-500 cursor-pointer"
                />
              </div>

              {/* Custom JVM Args */}
              <div className="p-4 rounded-xl bg-slate-50 dark:bg-neutral-950/60 border border-slate-200 dark:border-neutral-800 space-y-2">
                <label className="text-xs font-semibold text-slate-700 dark:text-neutral-300 block">
                  自定义 JVM 启动参数
                </label>
                <textarea
                  value={jvmArgs}
                  onChange={(e) => setJvmArgs(e.target.value)}
                  rows={2}
                  className="w-full p-2.5 bg-white dark:bg-neutral-950 border border-slate-300 dark:border-neutral-800 rounded-lg text-xs font-mono text-slate-800 dark:text-neutral-200 focus:outline-none focus:border-emerald-500"
                />
              </div>
            </div>
          )}
        </div>

        {/* Footer */}
        <div className="px-6 py-4 border-t border-slate-200 dark:border-neutral-800 flex items-center justify-between bg-slate-50 dark:bg-neutral-950/70">
          <div className="text-xs">
            {saveError && <p role="alert" className="text-rose-500">{saveError}</p>}
            {notification && (
              <span className="text-emerald-600 dark:text-emerald-400 flex items-center gap-1 font-medium animate-in fade-in">
                <CheckCircle2 className="w-3.5 h-3.5" /> {notification}
              </span>
            )}
          </div>

          <div className="flex items-center gap-2">
            <button
              onClick={onClose}
              className="px-4 py-2 rounded-lg text-xs font-medium text-slate-600 dark:text-neutral-400 hover:text-slate-900 dark:hover:text-white cursor-pointer transition-colors"
            >
              取消
            </button>
            <button
              onClick={handleSaveAll}
              disabled={saving || !name.trim()}
              className="px-5 py-2 rounded-lg bg-emerald-500 hover:bg-emerald-400 text-neutral-950 font-bold text-xs flex items-center gap-1.5 shadow-md shadow-emerald-500/20 active:scale-98 transition-all cursor-pointer"
            >
              <Save className="w-3.5 h-3.5" />
              <span>{saving ? '保存中…' : '保存实例所有修改'}</span>
            </button>
          </div>
        </div>
      </div>

      {/* Sub-modal: Install new loader */}
      {loaderInstallOpen && (
        <div className="fixed inset-0 z-60 flex items-center justify-center p-4 bg-black/60 dark:bg-black/80 backdrop-blur-sm animate-in fade-in duration-150">
          <div className="w-full max-w-md bg-white dark:bg-neutral-900 border border-slate-200 dark:border-neutral-700 rounded-2xl shadow-2xl p-6 space-y-4">
            <div className="flex items-center justify-between pb-3 border-b border-slate-200 dark:border-neutral-800">
              <h3 className="text-sm font-bold text-slate-900 dark:text-neutral-100 flex items-center gap-2">
                <Layers className="w-4 h-4 text-emerald-500 dark:text-emerald-400" />
                <span>安装 / 更换模组加载器</span>
              </h3>
              <button onClick={() => setLoaderInstallOpen(false)} className="text-slate-400 hover:text-slate-700 dark:text-neutral-400 dark:hover:text-white cursor-pointer">
                <X className="w-4 h-4" />
              </button>
            </div>

            <div className="space-y-3">
              <div>
                <label className="text-xs font-semibold text-slate-700 dark:text-neutral-300 block mb-1.5">
                  选择加载器类型
                </label>
                <div className="grid grid-cols-2 gap-2">
                  {[
                    { id: 'fabric' as LoaderType, name: 'Fabric (推荐)' },
                    { id: 'neoforge' as LoaderType, name: 'NeoForge' },
                    { id: 'forge' as LoaderType, name: 'Forge' },
                    { id: 'quilt' as LoaderType, name: 'Quilt' },
                  ].map((l) => (
                    <button
                      key={l.id}
                      type="button"
                      onClick={() => setNewLoaderType(l.id)}
                      className={`p-2.5 rounded-lg text-xs font-bold text-left transition-all cursor-pointer ${
                        newLoaderType === l.id
                          ? 'bg-emerald-500/20 border border-emerald-500 dark:border-emerald-400 text-emerald-700 dark:text-emerald-300'
                          : 'bg-slate-100 dark:bg-neutral-950 border border-slate-200 dark:border-neutral-800 text-slate-700 dark:text-neutral-400 hover:text-slate-900 dark:hover:text-white'
                      }`}
                    >
                      {l.name}
                    </button>
                  ))}
                </div>
              </div>

              <div>
                <label className="text-xs font-semibold text-slate-700 dark:text-neutral-300 block mb-1.5">
                  版本选择
                </label>
                <input
                  type="text"
                  value={newLoaderVer}
                  onChange={(e) => setNewLoaderVer(e.target.value)}
                  placeholder="如: 0.16.14"
                  className="w-full px-3 py-2 bg-slate-50 dark:bg-neutral-950 border border-slate-300 dark:border-neutral-800 rounded-lg text-xs text-slate-800 dark:text-neutral-200 font-mono focus:outline-none focus:border-emerald-500"
                />
              </div>

              <div className="p-3 rounded-lg bg-slate-100 dark:bg-neutral-950 border border-slate-200 dark:border-neutral-800 text-[11px] text-slate-600 dark:text-neutral-400">
                安装后将自动重构实例启动配置文件，旧模组若存在兼容性问题可在模组列表中停用。
              </div>

              <button
                onClick={handleInstallLoader}
                className="w-full py-2.5 rounded-xl bg-emerald-500 hover:bg-emerald-400 text-neutral-950 font-bold text-xs transition-all cursor-pointer shadow-md"
              >
                立即开始下载并安装加载器
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
};
