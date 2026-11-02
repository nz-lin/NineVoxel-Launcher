import React, { useState } from 'react';
import { X, Download, AlertCircle } from 'lucide-react';
import { useVersions } from '../hooks/useVersions';
import type { InstanceConfig } from '../services/instance';

interface CreateInstanceModalProps {
  onClose: () => void;
  onCreate: (config: InstanceConfig) => Promise<void>;
}

export const CreateInstanceModal: React.FC<CreateInstanceModalProps> = ({ onClose, onCreate }) => {
  const catalog = useVersions(200);
  const [name, setName] = useState('我的全新世界');
  const [version, setVersion] = useState('');
  const [icon, setIcon] = useState('🟩');
  const [partition, setPartition] = useState('默认分区');
  const [memoryMb, setMemoryMb] = useState(4096);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const field = 'w-full px-3 py-2 rounded-lg bg-slate-50 dark:bg-neutral-950 border border-slate-300 dark:border-neutral-700 text-sm';

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
          <button type="button" disabled={saving} onClick={onClose} aria-label="关闭" className="p-2 disabled:opacity-40"><X className="w-4 h-4" /></button>
        </div>
        <fieldset disabled={saving} className="p-6 space-y-4 disabled:opacity-60">
          <label className="block text-xs space-y-1.5"><span>实例名称</span><input required maxLength={80} value={name} onChange={e => setName(e.target.value)} className={field} /></label>
          <div className="space-y-2">
            <div className="flex items-center justify-between text-xs"><span>Minecraft 版本</span><button type="button" onClick={() => catalog.reload({ forceRefresh: true })} disabled={catalog.loading} className="text-emerald-600 disabled:opacity-40">{catalog.loading ? '正在读取清单…' : '刷新清单'}</button></div>
            <div className="flex gap-2 text-xs">
              {([['', '正式版与快照'], ['release', '正式版'], ['snapshot', '快照']] as const).map(([type, label]) => <button key={type} type="button" onClick={() => catalog.setTypeFilter(type)} className={`px-2 py-1 rounded ${catalog.typeFilter === type ? 'bg-emerald-500/20 text-emerald-600' : 'bg-slate-100 dark:bg-neutral-800'}`}>{label}</button>)}
            </div>
            <input required aria-label="Minecraft 版本号" list="minecraft-versions" value={version} onChange={e => setVersion(e.target.value)} placeholder="输入完整版本号，或选择清单中的版本" className={field} />
            <datalist id="minecraft-versions">{catalog.items.map(item => <option key={item.id} value={item.id}>{item.type} · {item.releaseTime.slice(0, 10)}</option>)}</datalist>
            {catalog.errorMessage && <p className="text-xs text-rose-500">{catalog.errorMessage}</p>}
            {catalog.stale && catalog.items.length > 0 && <p className="text-xs text-amber-600">当前显示缓存清单，安装仍需联网获取缺失文件。</p>}
          </div>
          <div className="rounded-lg bg-slate-50 dark:bg-neutral-950 border border-slate-200 dark:border-neutral-800 p-3 text-xs space-y-1"><p className="font-semibold">Vanilla 原版</p><p className="text-slate-500">Fabric、Forge、NeoForge 和 Quilt 安装尚未接入。</p></div>
          <label className="block text-xs space-y-1.5"><span>分组</span><input maxLength={80} value={partition} onChange={e => setPartition(e.target.value)} className={field} /></label>
          <label className="block text-xs space-y-1.5"><span>图标</span><select value={icon} onChange={e => setIcon(e.target.value)} className={field}>{['🟩', '💎', '⚡', '⚙️', '🔮', '🌲', '🚀', '⭐'].map(value => <option key={value}>{value}</option>)}</select></label>
          <label className="block text-xs space-y-1.5"><span>最大内存（MB）</span><input type="number" required min={512} max={1048576} step={256} value={memoryMb} onChange={e => setMemoryMb(Number(e.target.value))} className={field} /></label>
          <p className="text-xs text-slate-500">安装将下载客户端、当前平台依赖库和资源文件；下载线路遵循启动器设置。进度与失败信息会保存在实例列表中。</p>
        </fieldset>
        {error && <div role="alert" className="px-6 pb-4 flex gap-2 text-xs text-rose-500"><AlertCircle className="w-4 h-4 shrink-0" />{error}</div>}
        <div className="px-6 py-4 border-t border-slate-200 dark:border-neutral-800 flex justify-end gap-3">
          <button type="button" disabled={saving} onClick={onClose} className="text-xs px-3 py-2">取消</button>
          <button type="submit" disabled={saving || !name.trim() || !version.trim()} className="px-4 py-2 rounded-lg bg-emerald-500 text-neutral-950 font-bold text-xs flex gap-2 items-center disabled:opacity-40"><Download className="w-4 h-4" />{saving ? '正在创建…' : '创建并安装'}</button>
        </div>
      </form>
    </div>
  );
};
