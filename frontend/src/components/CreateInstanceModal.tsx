import React, { useState } from 'react';
import { X, Download, AlertCircle, ListChecks } from 'lucide-react';
import type { InstanceConfig } from '../services/instance';
import type { InstanceGroup } from '../types/launcher';
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
  const [icon, setIcon] = useState('🟩');
  const [partition, setPartition] = useState(defaultGroup);
  const [memoryMb, setMemoryMb] = useState(4096);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [selectorOpen, setSelectorOpen] = useState(false);
  const field = 'w-full px-3 py-2 rounded-lg bg-slate-50 dark:bg-neutral-950 border border-slate-300 dark:border-neutral-700 text-sm';

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
      await onCreate({ name: name.trim(), mcVersion: version.trim(), loader: 'vanilla', partition, icon, memoryMb, jvmArgs: '-XX:+UseG1GC', description: '', isFavorite: false, customJavaPath: '' });
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

          <div className="rounded-lg bg-slate-50 dark:bg-neutral-950 border border-slate-200 dark:border-neutral-800 p-3 text-xs space-y-1"><p className="font-semibold">Vanilla 原版</p><p className="text-slate-500">Fabric、Forge、NeoForge 和 Quilt 安装尚未接入。</p></div>

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
          <button type="submit" disabled={saving || !name.trim() || !version.trim()} className="px-4 py-2 rounded-lg bg-emerald-500 text-neutral-950 font-bold text-xs flex gap-2 items-center disabled:opacity-40 cursor-pointer"><Download className="w-4 h-4" />{saving ? '正在创建…' : '创建并安装'}</button>
        </div>
      </form>

      {selectorOpen && (
        <VersionSelectorModal
          selectedVersion={version}
          onSelect={setVersion}
          onClose={() => setSelectorOpen(false)}
        />
      )}
    </div>
  );
};
