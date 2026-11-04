import React, { useEffect, useState } from 'react';
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
import { ConfirmDialog } from './ConfirmDialog';
import { MOCK_MODS, MOCK_SHADERS, MOCK_RESOURCEPACKS } from '../data/mockData';
import { instanceService } from '../services/instance';
import { useLoaderVersions } from '../hooks/useLoaderVersions';
import {
  MOD_LOADER_OPTIONS,
  isModLoaderType,
  loaderDisplayName,
  type ModLoaderType
} from '../types/loader';

interface InstanceDetailModalProps {
  /** 当前要管理的实例；组件只在实例存在时挂载，因此这里是必填。 */
  instance: Instance;
  onClose: () => void;
  onUpdateInstance: (updated: Instance) => Promise<void>;
  onInstanceChanged: (updated: Instance) => void;
  onOpenFolder: (path: string) => void;
  javaRuntimes: JavaRuntime[];
  groups: InstanceGroup[];
  defaultGroup: string;
}

/**
 * 二选一模式开关。
 *
 * 用两个互斥按钮而不是复选框：模式的语义是「二选一」，
 * 复选框会让人以为可以同时开启。
 */
const ModeToggle: React.FC<{ active: boolean; onClick: () => void; label: string }> = ({
  active,
  onClick,
  label,
}) => (
  <button
    type="button"
    onClick={onClick}
    className={`px-2 py-1 rounded-md text-[11px] transition-colors cursor-pointer ${
      active
        ? 'bg-emerald-600 text-white'
        : 'bg-slate-100 dark:bg-neutral-800 text-slate-600 dark:text-neutral-400 hover:bg-slate-200 dark:hover:bg-neutral-700'
    }`}
  >
    {label}
  </button>
)

export const InstanceDetailModal: React.FC<InstanceDetailModalProps> = ({
  instance,
  onClose,
  onUpdateInstance,
  onInstanceChanged,
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
  /** 内存来源：默认跟随全局，与后端「模式为空即跟随全局」保持一致。 */
  const [memoryMode, setMemoryMode] = useState<'global' | 'custom'>(instance.memoryMode ?? 'global');
  const [memoryMb, setMemoryMb] = useState(instance.memoryMb || 4096);
  /** JVM 参数来源：默认跟随全局。 */
  const [jvmMode, setJvmMode] = useState<'global' | 'custom'>(instance.jvmMode ?? 'global');
  const [jvmArgs, setJvmArgs] = useState(instance.jvmArgs);
  /** Java 来源：已有运行时或路径的旧实例自动归入对应模式，避免升级后丢配置。 */
  const [javaMode, setJavaMode] = useState<'global' | 'managed' | 'custom'>(
    instance.javaMode
      ?? (instance.customJavaRuntimeId ? 'managed' : instance.customJavaPath ? 'custom' : 'global'),
  );
  const [selectedJavaPath, setSelectedJavaPath] = useState(instance.customJavaPath || '');
  const [selectedJavaRuntimeId, setSelectedJavaRuntimeId] = useState(instance.customJavaRuntimeId || '');
  const [partitionName, setPartitionName] = useState(instance.partition || defaultGroup);

  // Mod Search & Filter
  const [modSearch, setModSearch] = useState('');
  const [modFilter, setModFilter] = useState<'all' | 'enabled' | 'disabled' | 'update'>('all');

  // Loaders upgrade / switch state
  const [loaderActionBusy, setLoaderActionBusy] = useState(false);
  const [loaderActionError, setLoaderActionError] = useState<string | null>(null);
  /** 核心资源校验与修复的进行状态与结果提示。 */
  const [repairBusy, setRepairBusy] = useState(false);
  const [repairMessage, setRepairMessage] = useState<string | null>(null);
  const [loaderInstallOpen, setLoaderInstallOpen] = useState(false);
  const [newLoaderType, setNewLoaderType] = useState<ModLoaderType>('fabric');
  const [newLoaderVer, setNewLoaderVer] = useState('');
  const currentModLoader: ModLoaderType | '' = isModLoaderType(currentLoader) ? currentLoader : '';
  const catalogLoader: ModLoaderType | '' = loaderInstallOpen
    ? newLoaderType
    : activeTab === 'loaders'
      ? currentModLoader
      : '';
  const loaderCatalog = useLoaderVersions(catalogLoader, instance.mcVersion);
  const currentCatalog = !loaderInstallOpen &&
    loaderCatalog.data?.loader === currentModLoader &&
    loaderCatalog.data?.minecraftVersion === instance.mcVersion
    ? loaderCatalog.data
    : null;
  const installCatalog = loaderInstallOpen &&
    loaderCatalog.data?.loader === newLoaderType &&
    loaderCatalog.data?.minecraftVersion === instance.mcVersion
    ? loaderCatalog.data
    : null;
  const installVersions = installCatalog?.versions ?? [];
  const isInstalling = instance.installationState === 'installing';
  const isLoaderBusy = loaderActionBusy || isInstalling;

  const [notification, setNotification] = useState<string | null>(null);
  const [confirmUninstallOpen, setConfirmUninstallOpen] = useState(false);

  const showNotify = (msg: string) => {
    setNotification(msg);
    setTimeout(() => setNotification(null), 2500);
  };

  useEffect(() => {
    setCurrentLoader(instance.loader);
    setLoaderVersion(instance.loaderVersion);
  }, [instance.loader, instance.loaderVersion]);

  useEffect(() => {
    if (!loaderInstallOpen) return;
    setNewLoaderType(isModLoaderType(instance.loader) ? instance.loader : 'fabric');
    setNewLoaderVer('');
    setLoaderActionError(null);
  }, [loaderInstallOpen]);

  useEffect(() => {
    if (!loaderInstallOpen || !installCatalog) return;
    if (installCatalog.versions.some((item) => item.version === newLoaderVer)) return;
    setNewLoaderVer(installCatalog.recommended || installCatalog.versions[0]?.version || '');
  }, [installCatalog, loaderInstallOpen, newLoaderVer]);

  // --- Loader Operations ---
  const changeLoader = async (kind: ModLoaderType, version: string, successMessage: string) => {
    if (isLoaderBusy) return;
    setLoaderActionBusy(true);
    setLoaderActionError(null);
    try {
      const updated = await instanceService.updateLoader(instance.id, kind, version);
      setCurrentLoader(updated.loader);
      setLoaderVersion(updated.loaderVersion);
      onInstanceChanged(updated);
      setLoaderInstallOpen(false);
      showNotify(successMessage);
    } catch (cause) {
      setLoaderActionError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setLoaderActionBusy(false);
    }
  };

  const handleUpdateLoader = (targetVersion: string) => {
    if (!isModLoaderType(currentLoader) || !targetVersion) return;
    void changeLoader(currentLoader, targetVersion, `已开始将 ${loaderDisplayName(currentLoader)} 更新至 v${targetVersion}`);
  };

  const handleInstallLoader = () => {
    if (!installCatalog || !newLoaderVer) return;
    void changeLoader(newLoaderType, newLoaderVer, `已开始安装 ${loaderDisplayName(newLoaderType)} v${newLoaderVer}`);
  };

  const handleUninstallLoader = () => {
    if (isLoaderBusy) return;
    setConfirmUninstallOpen(true);
  };

  const confirmUninstallLoader = () => {
    setConfirmUninstallOpen(false);
    void (async () => {
      setLoaderActionBusy(true);
      setLoaderActionError(null);
      try {
        const updated = await instanceService.uninstallLoader(instance.id);
        setCurrentLoader(updated.loader);
        setLoaderVersion(updated.loaderVersion);
        onInstanceChanged(updated);
        showNotify('已卸载加载器，实例已切换为原版纯净模式');
      } catch (cause) {
        setLoaderActionError(cause instanceof Error ? cause.message : String(cause));
      } finally {
        setLoaderActionBusy(false);
      }
    })();
  };

  /**
   * 校验并修复核心资源。
   *
   * 先校验再修复：修复本身也会校验，但先跑一次能让用户知道「到底坏没坏」，
   * 而不是点一次修复等半天才发现本来就没问题。
   */
  const handleVerifyAndRepair = () => {
    if (repairBusy) return;
    void (async () => {
      setRepairBusy(true);
      setRepairMessage(null);
      try {
        const check = await instanceService.verifyCore(instance.id);
        if (!check.hasManifest) {
          setRepairMessage('该版本缺少安装记录，无法校验，请重新安装。');
          return;
        }
        if (check.complete) {
          setRepairMessage(`已校验 ${check.checked} 个文件，全部完整。`);
          return;
        }
        const report = await instanceService.repairCore(instance.id);
        if (report.needsReinstall) {
          setRepairMessage(report.reason || '部分文件无法自动修复，请重新安装。');
          return;
        }
        setRepairMessage(`已修复 ${report.repaired} 个文件，共校验 ${report.checked} 个。`);
      } catch (cause) {
        setRepairMessage(cause instanceof Error ? cause.message : String(cause));
      } finally {
        setRepairBusy(false);
      }
    })();
  };

  const handleRetryLoaderInstall = () => {
    if (isLoaderBusy) return;
    void (async () => {
      setLoaderActionBusy(true);
      setLoaderActionError(null);
      try {
        const updated = await instanceService.install(instance.id);
        onInstanceChanged(updated);
        showNotify('已重新开始安装加载器。');
      } catch (cause) {
        setLoaderActionError(cause instanceof Error ? cause.message : String(cause));
      } finally {
        setLoaderActionBusy(false);
      }
    })();
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
      await onUpdateInstance({
        ...instance,
        name,
        description,
        isFavorite: favorite,
        partition: partitionName,
        memoryMode,
        memoryMb,
        jvmMode,
        jvmArgs,
        javaMode,
        customJavaPath: javaMode === 'custom' ? selectedJavaPath : '',
        customJavaRuntimeId: javaMode === 'managed' ? selectedJavaRuntimeId : '',
      });
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
                  {loaderDisplayName(currentLoader)}{currentLoader !== 'vanilla' && loaderVersion ? ` ${loaderVersion}` : ''}
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
              onClick={() => onOpenFolder(instance.id)}
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
            { id: 'loaders' as const, label: `加载器 (${currentLoader === 'vanilla' ? '原版' : currentLoader.toUpperCase()})`, icon: Layers },
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
                disabled={tab.id !== 'settings' && tab.id !== 'loaders'}
                title={tab.id === 'settings' ? '修改持久化实例配置' : tab.id === 'loaders' ? '安装、更新或卸载模组加载器' : '尚未接入'}
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
              {/* 核心资源校验与修复：文件损坏时不必重装整个版本 */}
              <div className="p-4 rounded-xl bg-slate-50 dark:bg-neutral-950/60 border border-slate-200 dark:border-neutral-800 space-y-2">
                <div className="flex items-center justify-between gap-3">
                  <div className="space-y-0.5">
                    <p className="text-xs font-semibold text-slate-700 dark:text-neutral-300">核心资源校验</p>
                    <p className="text-[11px] text-slate-500 dark:text-neutral-400">
                      逐文件校验并补齐缺失或损坏的依赖库、资源与原生库；不会改动存档与模组。
                    </p>
                  </div>
                  <button
                    type="button"
                    onClick={handleVerifyAndRepair}
                    disabled={repairBusy}
                    className="shrink-0 px-2.5 py-1.5 rounded-lg bg-emerald-600 hover:bg-emerald-500 disabled:opacity-60 disabled:cursor-not-allowed text-white text-xs transition-colors cursor-pointer"
                  >
                    {repairBusy ? '处理中…' : '校验并修复'}
                  </button>
                </div>
                {repairMessage && (
                  <p className="text-[11px] text-slate-600 dark:text-neutral-400">{repairMessage}</p>
                )}
              </div>

              {loaderActionError && (
                <div role="alert" className="flex items-start gap-2 p-3 rounded-lg bg-rose-500/10 border border-rose-500/30 text-xs text-rose-700 dark:text-rose-300">
                  <AlertTriangle className="w-4 h-4 shrink-0" />
                  <span>{loaderActionError}</span>
                </div>
              )}
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
                      {currentLoader !== 'vanilla' && (
                        <span className="text-xs font-mono bg-emerald-500/15 text-emerald-700 dark:text-emerald-300 border border-emerald-500/30 px-2 py-0.5 rounded font-bold">
                          v{loaderVersion || '—'}
                        </span>
                      )}
                    </div>
                    <p className="text-xs text-slate-600 dark:text-neutral-400 mt-1">
                      基于独立实例目录隔离运行，更换或卸载加载器不会影响其它游戏版本。
                    </p>
                  </div>

                  <div className="flex items-center gap-2">
                    <button
                      onClick={() => setLoaderInstallOpen(true)}
                      disabled={isLoaderBusy}
                      className="px-3 py-1.5 rounded-lg bg-emerald-500 hover:bg-emerald-400 text-neutral-950 font-bold text-xs flex items-center gap-1.5 transition-all cursor-pointer shadow-sm disabled:opacity-40 disabled:cursor-not-allowed"
                    >
                      <Layers className="w-3.5 h-3.5" />
                      <span>更换/安装新加载器</span>
                    </button>

                    {currentLoader !== 'vanilla' && (
                      <button
                        onClick={handleUninstallLoader}
                        disabled={isLoaderBusy}
                        className="px-3 py-1.5 rounded-lg bg-red-500/15 hover:bg-red-500 text-red-600 dark:text-red-300 hover:text-white font-semibold text-xs transition-colors cursor-pointer disabled:opacity-40 disabled:cursor-not-allowed"
                      >
                        卸载加载器 (回退原版)
                      </button>
                    )}
                  </div>
                </div>

                {isInstalling && (
                  <div className="p-3 rounded-lg bg-emerald-500/10 border border-emerald-500/30 text-xs space-y-2" role="status">
                    <div className="flex items-center gap-2 text-emerald-800 dark:text-emerald-300 font-semibold">
                      <RefreshCw className="w-4 h-4 animate-spin" />
                      <span>正在安装 {loaderDisplayName(currentLoader)}，已完成 {Math.round((instance.installationProgress ?? 0) * 100)}%</span>
                    </div>
                    <progress aria-label="加载器安装进度" className="w-full h-1.5 accent-emerald-500" max={1} value={instance.installationProgress ?? 0} />
                    <p className="text-slate-500 dark:text-neutral-400">请勿关闭启动器。下载与安装进度也会显示在下载中心。</p>
                  </div>
                )}

                {instance.installationState === 'failed' && (
                  <div className="p-3 rounded-lg bg-rose-500/10 border border-rose-500/30 flex items-center justify-between gap-3 text-xs">
                    <div className="flex items-start gap-2 text-rose-700 dark:text-rose-300">
                      <AlertTriangle className="w-4 h-4 shrink-0" />
                      <span>加载器安装失败：{instance.installationError || '请重试'}</span>
                    </div>
                    <button onClick={handleRetryLoaderInstall} disabled={isLoaderBusy} className="px-3 py-1 rounded bg-rose-500 hover:bg-rose-400 text-white font-bold text-xs cursor-pointer disabled:opacity-40 disabled:cursor-not-allowed">
                      重试安装
                    </button>
                  </div>
                )}

                {!isInstalling && currentLoader !== 'vanilla' && currentCatalog?.recommended && currentCatalog.recommended !== loaderVersion && (
                  <div className="p-3 rounded-lg bg-amber-500/10 border border-amber-500/30 flex items-center justify-between text-xs">
                    <div className="flex items-center gap-2 text-amber-800 dark:text-amber-300">
                      <AlertTriangle className="w-4 h-4 shrink-0 text-amber-500" />
                      <span>{loaderDisplayName(currentLoader)} 官方推荐稳定版为 <strong>v{currentCatalog.recommended}</strong></span>
                    </div>
                    <button
                      onClick={() => handleUpdateLoader(currentCatalog.recommended)}
                      disabled={isLoaderBusy}
                      className="px-3 py-1 rounded bg-amber-500 hover:bg-amber-400 text-neutral-950 font-bold text-xs flex items-center gap-1.5 cursor-pointer shadow-sm disabled:opacity-40 disabled:cursor-not-allowed"
                    >
                      <Download className="w-3 h-3" />
                      <span>切换到推荐版本</span>
                    </button>
                  </div>
                )}
              </div>

              {currentLoader !== 'vanilla' && !loaderInstallOpen && loaderCatalog.error && (
                <div className="p-3 rounded-lg bg-amber-500/10 border border-amber-500/30 flex items-center justify-between gap-3 text-xs text-amber-800 dark:text-amber-300">
                  <span>无法读取 {loaderDisplayName(currentLoader)} 官方版本：{loaderCatalog.error}</span>
                  <button type="button" onClick={() => { void loaderCatalog.reload(); }} className="underline font-semibold shrink-0 cursor-pointer">重试</button>
                </div>
              )}

              <div className="p-4 rounded-xl bg-slate-50 dark:bg-neutral-950/40 border border-slate-200 dark:border-neutral-800 text-xs text-slate-600 dark:text-neutral-400 space-y-2">
                <p className="font-semibold text-slate-800 dark:text-neutral-200">关于加载器版本</p>
                <p>版本列表来自 Fabric、Quilt、Forge 与 NeoForge 官方元数据。安装与更新会真实下载依赖；Forge/NeoForge 还会执行官方安装器。</p>
                <p>Fabric/Quilt 的加载器版本独立于游戏版本，官方目录会为当前 Minecraft 版本生成对应 profile；Forge/NeoForge 的候选版本则按游戏版本筛选。</p>
                <p>新加载器安装成功前不会删除旧版本目录；卸载加载器会移除加载器专属版本文件，并把 .jar 模组重命名停用，不会删除存档。</p>
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

              <div className="p-4 rounded-xl bg-slate-50 dark:bg-neutral-950/60 border border-slate-200 dark:border-neutral-800 space-y-2">
                <label className="text-xs font-semibold text-slate-800 dark:text-neutral-200 block">
                  实例专属 Java
                </label>
                <AppSelect
                  value={selectedJavaRuntimeId}
                  onChange={setSelectedJavaRuntimeId}
                  options={[
                    { value: '', label: '跟随全局 Java 设置', hint: '使用设置页或 Java 管理器选择的运行时' },
                    ...javaRuntimes
                      .filter((runtime) => runtime.status === 'ready')
                      .map((runtime) => ({
                        value: runtime.id,
                        label: `${runtime.vendor} ${runtime.majorVersion}`,
                        hint: `${runtime.versionString} · ${runtime.architecture}`,
                      })),
                  ]}
                  ariaLabel="实例专属 Java"
                  placeholder="跟随全局 Java 设置"
                />
                <p className="text-[11px] text-slate-500 dark:text-neutral-500">
                  仅保存运行时标识；实际可执行文件路径由后端解析，不会下发到界面。
                  {!selectedJavaRuntimeId && selectedJavaPath && ' 当前另有已保存的实例 Java 路径，将在跟随全局时继续使用。'}
                </p>
              </div>

              {/* Dedicated RAM Allocation */}
              <div className="p-4 rounded-xl bg-slate-50 dark:bg-neutral-950/60 border border-slate-200 dark:border-neutral-800 space-y-3">
                <div className="flex items-center justify-between gap-3">
                  <label className="text-xs font-semibold text-slate-700 dark:text-neutral-300">
                    内存分配
                  </label>
                  <div className="flex items-center gap-1.5">
                    <ModeToggle
                      active={memoryMode === 'global'}
                      onClick={() => setMemoryMode('global')}
                      label="跟随全局"
                    />
                    <ModeToggle
                      active={memoryMode === 'custom'}
                      onClick={() => setMemoryMode('custom')}
                      label="自定义"
                    />
                  </div>
                </div>
                {memoryMode === 'global' ? (
                  <p className="text-[11px] text-slate-500 dark:text-neutral-400">
                    使用全局设置中的最大内存。切换为自定义可为该实例单独分配。
                  </p>
                ) : (
                  <>
                    <div className="flex items-center justify-between">
                      <span className="text-[11px] text-slate-500 dark:text-neutral-400">此实例专属内存</span>
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
                  </>
                )}
              </div>

              {/* Custom JVM Args */}
              <div className="p-4 rounded-xl bg-slate-50 dark:bg-neutral-950/60 border border-slate-200 dark:border-neutral-800 space-y-2">
                <div className="flex items-center justify-between gap-3">
                  <label className="text-xs font-semibold text-slate-700 dark:text-neutral-300">
                    JVM 启动参数
                  </label>
                  <div className="flex items-center gap-1.5">
                    <ModeToggle
                      active={jvmMode === 'global'}
                      onClick={() => setJvmMode('global')}
                      label="跟随全局"
                    />
                    <ModeToggle
                      active={jvmMode === 'custom'}
                      onClick={() => setJvmMode('custom')}
                      label="自定义"
                    />
                  </div>
                </div>
                {jvmMode === 'global' ? (
                  <p className="text-[11px] text-slate-500 dark:text-neutral-400">
                    不添加额外的 JVM 参数。切换为自定义可为该实例追加参数。
                  </p>
                ) : (
                  <textarea
                    value={jvmArgs}
                    onChange={(e) => setJvmArgs(e.target.value)}
                    rows={2}
                    placeholder="留空表示不加任何额外参数"
                    className="w-full p-2.5 bg-white dark:bg-neutral-950 border border-slate-300 dark:border-neutral-800 rounded-lg text-xs font-mono text-slate-800 dark:text-neutral-200 focus:outline-none focus:border-emerald-500"
                  />
                )}
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
              <button onClick={() => setLoaderInstallOpen(false)} disabled={isLoaderBusy} className="text-slate-400 hover:text-slate-700 dark:text-neutral-400 dark:hover:text-white cursor-pointer disabled:opacity-40 disabled:cursor-not-allowed">
                <X className="w-4 h-4" />
              </button>
            </div>

            <div className="space-y-3">
              <div>
                <label className="text-xs font-semibold text-slate-700 dark:text-neutral-300 block mb-1.5">
                  选择加载器类型
                </label>
                <div className="grid grid-cols-2 gap-2">
                  {MOD_LOADER_OPTIONS.map((option) => (
                    <button
                      key={option.value}
                      type="button"
                      disabled={isLoaderBusy}
                      onClick={() => { setNewLoaderType(option.value); setNewLoaderVer(''); }}
                      className={`p-2.5 rounded-lg text-xs font-bold text-left transition-all cursor-pointer ${
                        newLoaderType === option.value
                          ? 'bg-emerald-500/20 border border-emerald-500 dark:border-emerald-400 text-emerald-700 dark:text-emerald-300'
                          : 'bg-slate-100 dark:bg-neutral-950 border border-slate-200 dark:border-neutral-800 text-slate-700 dark:text-neutral-400 hover:text-slate-900 dark:hover:text-white'
                      } disabled:opacity-40 disabled:cursor-not-allowed`}
                    >
                      {option.label}
                    </button>
                  ))}
                </div>
              </div>

              <div>
                <label className="text-xs font-semibold text-slate-700 dark:text-neutral-300 block mb-1.5">
                  版本选择
                </label>
                {loaderCatalog.loading ? (
                  <div className="flex items-center gap-2 px-3 py-2 text-xs text-slate-500 dark:text-neutral-400">
                    <RefreshCw className="w-3.5 h-3.5 animate-spin" />
                    <span>正在读取 {loaderDisplayName(newLoaderType)} 官方版本…</span>
                  </div>
                ) : loaderCatalog.error ? (
                  <div className="p-3 rounded-lg bg-rose-500/10 border border-rose-500/30 text-xs text-rose-700 dark:text-rose-300 space-y-2">
                    <p>{loaderCatalog.error}</p>
                    <button type="button" onClick={() => { void loaderCatalog.reload(); }} className="underline font-semibold cursor-pointer">重新读取</button>
                  </div>
                ) : (
                  <AppSelect
                    value={newLoaderVer}
                    onChange={setNewLoaderVer}
                    options={installVersions.map((item) => ({
                      value: item.version,
                      label: item.version,
                      hint: item.recommended ? '官方推荐' : item.stable ? '稳定版' : '预览版',
                    }))}
                    placeholder={installVersions.length > 0 ? '请选择加载器版本' : '该 Minecraft 版本暂无可用版本'}
                    disabled={isLoaderBusy || installVersions.length === 0}
                    ariaLabel="加载器版本"
                  />
                )}
              </div>

              <div className="p-3 rounded-lg bg-slate-100 dark:bg-neutral-950 border border-slate-200 dark:border-neutral-800 text-[11px] text-slate-600 dark:text-neutral-400">
                将基于 Minecraft {instance.mcVersion} 的官方元数据真实下载并安装。Forge/NeoForge 需要可用的 Java 来运行官方安装器；新版本成功前不会删除当前加载器。
              </div>

              <button
                type="button"
                onClick={handleInstallLoader}
                disabled={isLoaderBusy || !installCatalog || !newLoaderVer}
                className="w-full py-2.5 rounded-xl bg-emerald-500 hover:bg-emerald-400 text-neutral-950 font-bold text-xs transition-all cursor-pointer shadow-md disabled:opacity-40 disabled:cursor-not-allowed"
              >
                {loaderActionBusy ? '正在提交安装…' : `安装 ${loaderDisplayName(newLoaderType)}${newLoaderVer ? ` v${newLoaderVer}` : ''}`}
              </button>
            </div>
          </div>
        </div>
      )}

      {confirmUninstallOpen && (
        <ConfirmDialog
          title="卸载模组加载器"
          message="确认卸载当前模组加载器并回退为原版 (Vanilla) 吗？已有 .jar 模组将被停用，但不会被删除。"
          confirmLabel="卸载"
          danger
          onConfirm={confirmUninstallLoader}
          onCancel={() => setConfirmUninstallOpen(false)}
        />
      )}
    </div>
  );
};
