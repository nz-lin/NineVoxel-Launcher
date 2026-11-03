import React, { useEffect, useState } from 'react';
import { X, Download, AlertCircle, ListChecks } from 'lucide-react';
import type { InstanceConfig } from '../services/instance';
import type { InstanceGroup, LoaderType } from '../types/launcher';
import { MOD_LOADER_OPTIONS, type ModLoaderType } from '../types/loader';
import { useLoaderVersions } from '../hooks/useLoaderVersions';
import { AppSelect } from './AppSelect';
import { VersionSelectorModal } from './VersionSelectorModal';

interface CreateInstanceModalProps {
  onClose: () => void;
  onCreate: (config: InstanceConfig) => Promise<void>;
  groups: InstanceGroup[];
  defaultGroup: string;
}

export const CreateInstanceModal: React.FC<CreateInstanceModalProps> = ({
  onClose,
  onCreate,
  groups,
  defaultGroup
}) => {
  const [name, setName] = useState('我的全新世界');
  const [version, setVersion] = useState('');
  const [loaderType, setLoaderType] = useState<LoaderType>('vanilla');
  const [loaderVersion, setLoaderVersion] = useState('');
  const [icon, setIcon] = useState('🟩');
  const [partition, setPartition] = useState(defaultGroup);
  const [memoryMb, setMemoryMb] = useState(4096);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [selectorOpen, setSelectorOpen] = useState(false);
  const field = 'w-full px-3 py-2 rounded-lg bg-slate-50 dark:bg-neutral-950 border border-slate-300 dark:border-neutral-700 text-sm';
  const modLoader = loaderType === 'vanilla' ? '' : loaderType as ModLoaderType;
  const loaderCatalog = useLoaderVersions(modLoader, version);

  useEffect(() => {
    if (loaderType === 'vanilla') {
      setLoaderVersion('');
      return;
    }
    if (loaderCatalog.data?.loader !== loaderType || loaderCatalog.data?.minecraftVersion !== version) {
      setLoaderVersion('');
      return;
    }
    const available = loaderCatalog.data?.versions ?? [];
    if (available.some((item) => item.version === loaderVersion)) return;
    const recommended = loaderCatalog.data?.recommended;
    setLoaderVersion(recommended || available[0]?.version || '');
  }, [loaderCatalog.data, loaderType, loaderVersion]);

  const groupOptions = groups.map((group) => ({
    value: group.name,
    label: group.name,
    hint: group.isDefault ? '默认分组' : `${group.instanceCount} 个实例`,
  }));

  const submit = async (event: React.FormEvent) => {
    event.preventDefault();
    if (saving) return;
    setSaving(true);
    setError(null);
    try {
      await onCreate({ name: name.trim(), mcVersion: version.trim(), loader: loaderType, loaderVersion: loaderType === 'vanilla' ? '' : loaderVersion, partition, icon, memoryMb, jvmArgs: '-XX:+UseG1GC', description: '', isFavorite: false, customJavaPath: '', customJavaRuntimeId: '' });
      onClose();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
      setSaving(false);
    }
  };

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center p-4 bg-black/60 backdrop-blur-sm">
      <form onSubmit={submit} className="w-full max-w-xl max-h-[85vh] overflow-y-auto bg-white dark:bg-neutral-900 border border-slate-200 dark:border-neutral-700 rounded-2xl shadow-2xl">
        <div className="flex justify-between items-center px-6 py-4 border-b border-slate-200 dark:border-neutral-800">
          <div><h2 className="font-bold">新建 Minecraft 实例</h2><p className="text-xs text-slate-500 mt-1">创建独立游戏目录并安装原版游戏资源</p></div>
          <button type="button" disabled={saving} onClick={onClose} aria-label="关闭" className="p-2 disabled:opacity-40 cursor-pointer"><X className="w-4 h-4" /></button>
        </div>
        <fieldset disabled={saving} className="p-6 space-y-4 disabled:opacity-60">
          <label className="block text-xs space-y-1.5"><span>实例名称</span><input required maxLength={80} value={name} onChange={e => setName(e.target.value)} className={field} /></label>

          <div className="space-y-1.5">
            <span className="text-xs">Minecraft 版本</span>
            <div className="flex items-center gap-2">
              <div className={`${field} flex-1 min-w-0 font-mono flex items-center ${version ? 'text-slate-800 dark:text-neutral-200' : 'text-slate-400 dark:text-neutral-500'}`}>
                <span className="truncate">{version || '未选择版本'}</span>
              </div>
              <button
                type="button"
                onClick={() => setSelectorOpen(true)}
                className="shrink-0 px-3.5 py-2 rounded-lg bg-emerald-500/15 hover:bg-emerald-500 text-emerald-700 dark:text-emerald-300 hover:text-neutral-950 font-bold text-xs flex items-center gap-1.5 transition-colors cursor-pointer"
              >
                <ListChecks className="w-4 h-4" />
                <span>选择版本</span>
              </button>
            </div>
          </div>

          <div className="space-y-2">
            <span className="text-xs">游戏核心 / 模组加载器</span>
            <div className="grid grid-cols-2 sm:grid-cols-5 gap-2">
              <button type="button" onClick={() => { setLoaderType('vanilla'); setLoaderVersion(''); }} className={`p-2.5 rounded-lg border text-xs font-bold transition-colors cursor-pointer ${loaderType === 'vanilla' ? 'bg-emerald-500/15 border-emerald-500 text-emerald-700 dark:text-emerald-300' : 'bg-slate-50 dark:bg-neutral-950 border-slate-200 dark:border-neutral-800 text-slate-600 dark:text-neutral-400'}`}>Vanilla</button>
              {MOD_LOADER_OPTIONS.map((option) => (
                <button key={option.value} type="button" onClick={() => { setLoaderType(option.value); setLoaderVersion(''); }} className={`p-2.5 rounded-lg border text-xs font-bold transition-colors cursor-pointer ${loaderType === option.value ? 'bg-emerald-500/15 border-emerald-500 text-emerald-700 dark:text-emerald-300' : 'bg-slate-50 dark:bg-neutral-950 border-slate-200 dark:border-neutral-800 text-slate-600 dark:text-neutral-400'}`}>{option.label}</button>
              ))}
            </div>
            {loaderType !== 'vanilla' && (
              <div className="rounded-lg bg-slate-50 dark:bg-neutral-950 border border-slate-200 dark:border-neutral-800 p-3 space-y-2 text-xs">
                <div className="flex items-center justify-between gap-2">
                  <span className="font-semibold">{MOD_LOADER_OPTIONS.find((item) => item.value === loaderType)?.label} 版本</span>
                  {loaderCatalog.loading && <span className="text-slate-500">正在读取官方版本…</span>}
                </div>
                {loaderCatalog.error ? (
                  <div className="flex items-start gap-2 text-rose-500"><AlertCircle className="w-3.5 h-3.5 shrink-0 mt-0.5" /><span>{loaderCatalog.error}</span></div>
                ) : (
                  <AppSelect
                    value={loaderVersion}
                    onChange={setLoaderVersion}
                    options={(loaderCatalog.data?.versions ?? []).map((item) => ({
                      value: item.version,
                      label: item.version,
                      hint: item.recommended ? '推荐' : item.stable ? '稳定' : '预览',
                    }))}
                    ariaLabel="加载器版本"
                    placeholder={loaderCatalog.loading ? '正在加载…' : '请选择加载器版本'}
                  />
                )}
                <p className="text-slate-500">将基于 Minecraft {version || '所选版本'} 的官方元数据安装；Fabric/Quilt 加载器版本独立于游戏版本，Forge/NeoForge 候选版本按游戏版本筛选。</p>
              </div>
            )}
          </div>

          <div className="space-y-1.5">
            <span className="text-xs">分组</span>
            <AppSelect
              value={partition}
              onChange={setPartition}
              options={groupOptions}
              ariaLabel="实例分组"
              placeholder={defaultGroup || '选择分组'}
            />
          </div>

          <label className="block text-xs space-y-1.5"><span>图标</span><select value={icon} onChange={e => setIcon(e.target.value)} className={field}>{['🟩', '💎', '⚡', '⚙️', '🔮', '🌲', '🚀', '⭐'].map(value => <option key={value}>{value}</option>)}</select></label>
          <label className="block text-xs space-y-1.5"><span>最大内存（MB）</span><input type="number" required min={512} max={1048576} step={256} value={memoryMb} onChange={e => setMemoryMb(Number(e.target.value))} className={field} /></label>
          <p className="text-xs text-slate-500">安装将下载客户端、当前平台依赖库和资源文件；下载线路遵循启动器设置。进度与失败信息会保存在实例列表中。</p>
        </fieldset>
        {error && <div role="alert" className="px-6 pb-4 flex gap-2 text-xs text-rose-500"><AlertCircle className="w-4 h-4 shrink-0" />{error}</div>}
        <div className="px-6 py-4 border-t border-slate-200 dark:border-neutral-800 flex justify-end gap-3">
          <button type="button" disabled={saving} onClick={onClose} className="text-xs px-3 py-2 cursor-pointer">取消</button>
          <button type="submit" disabled={saving || !name.trim() || !version.trim() || (loaderType !== 'vanilla' && !loaderVersion)} className="px-4 py-2 rounded-lg bg-emerald-500 text-neutral-950 font-bold text-xs flex gap-2 items-center disabled:opacity-40 cursor-pointer"><Download className="w-4 h-4" />{saving ? '正在创建…' : '创建并安装'}</button>
        </div>
      </form>

      {selectorOpen && (
        <VersionSelectorModal
          selectedVersion={version}
          onSelect={(value) => { setVersion(value); setLoaderVersion(''); }}
          onClose={() => setSelectorOpen(false)}
        />
      )}
    </div>
  );
};
