import React, { useState } from 'react';
import { AlertCircle, FolderPlus, Layers, Trash2, X } from 'lucide-react';
import type { InstanceGroup } from '../types/launcher';
import { AppSelect } from './AppSelect';

interface GroupManagerModalProps {
  onClose: () => void;
  groups: InstanceGroup[];
  defaultGroup: string;
  onCreateGroup: (name: string) => Promise<void>;
  onDeleteGroup: (name: string, moveTo?: string) => Promise<void>;
}

export const GroupManagerModal: React.FC<GroupManagerModalProps> = ({
  onClose,
  groups,
  defaultGroup,
  onCreateGroup,
  onDeleteGroup,
}) => {
  const [newName, setNewName] = useState('');
  const [creating, setCreating] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<InstanceGroup | null>(null);
  const [moveTo, setMoveTo] = useState('');
  const [deleting, setDeleting] = useState(false);

  const startDelete = (group: InstanceGroup) => {
    if (group.isDefault) return;
    setDeleteTarget(group);
    setMoveTo(groups.find((item) => item.name !== group.name)?.name ?? defaultGroup);
    setError(null);
  };

  const handleCreate = async (event: React.FormEvent) => {
    event.preventDefault();
    const name = newName.trim();
    if (!name || creating) return;
    setCreating(true);
    setError(null);
    try {
      await onCreateGroup(name);
      setNewName('');
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setCreating(false);
    }
  };

  const handleDelete = async () => {
    if (!deleteTarget || deleting) return;
    if (deleteTarget.instanceCount > 0 && !moveTo) {
      setError('请选择要迁移到的目标分组。');
      return;
    }
    setDeleting(true);
    setError(null);
    try {
      await onDeleteGroup(deleteTarget.name, deleteTarget.instanceCount > 0 ? moveTo : '');
      setDeleteTarget(null);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setDeleting(false);
    }
  };

  const moveOptions = groups
    .filter((group) => group.name !== deleteTarget?.name)
    .map((group) => ({
      value: group.name,
      label: group.name,
      hint: group.isDefault ? '默认分组' : `${group.instanceCount} 个实例`,
    }));

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center p-4 bg-black/60 dark:bg-black/75 backdrop-blur-sm animate-in fade-in duration-200">
      <div className="w-full max-w-lg max-h-[85vh] bg-white dark:bg-neutral-900 border border-slate-200 dark:border-neutral-700/80 rounded-2xl shadow-2xl flex flex-col overflow-hidden">
        <div className="px-5 py-4 border-b border-slate-200 dark:border-neutral-800 flex items-center justify-between bg-slate-50 dark:bg-neutral-950/60">
          <div className="flex items-center gap-2.5">
            <div className="w-8 h-8 rounded-lg bg-emerald-500/15 border border-emerald-500/30 flex items-center justify-center text-emerald-600 dark:text-emerald-400">
              <Layers className="w-4 h-4" />
            </div>
            <div>
              <h2 className="text-sm font-bold text-slate-900 dark:text-neutral-100">分组管理</h2>
              <p className="text-[11px] text-slate-500 dark:text-neutral-400">创建分组、查看实例数量、删除前迁移实例</p>
            </div>
          </div>
          <button
            type="button"
            onClick={onClose}
            aria-label="关闭分组管理"
            className="p-1.5 rounded-lg text-slate-400 hover:text-slate-800 dark:hover:text-white hover:bg-slate-100 dark:hover:bg-neutral-800 transition-colors cursor-pointer"
          >
            <X className="w-4 h-4" />
          </button>
        </div>

        <div className="p-5 space-y-4 overflow-y-auto">
          <form onSubmit={handleCreate} className="flex items-center gap-2">
            <input
              value={newName}
              onChange={(event) => setNewName(event.target.value)}
              maxLength={80}
              placeholder="输入新分组名称"
              className="flex-1 px-3 py-2 rounded-lg bg-slate-50 dark:bg-neutral-950 border border-slate-300 dark:border-neutral-800 text-xs text-slate-800 dark:text-neutral-200 focus:outline-none focus:border-emerald-500"
            />
            <button
              type="submit"
              disabled={creating || !newName.trim()}
              className="px-3.5 py-2 rounded-lg bg-emerald-500 hover:bg-emerald-400 text-neutral-950 font-bold text-xs flex items-center gap-1.5 transition-colors cursor-pointer disabled:opacity-40 disabled:cursor-not-allowed"
            >
              <FolderPlus className="w-3.5 h-3.5" />
              {creating ? '创建中…' : '创建分组'}
            </button>
          </form>

          <div className="space-y-2">
            {groups.map((group) => (
              <div
                key={group.name}
                className="px-3 py-2.5 rounded-xl bg-slate-50 dark:bg-neutral-950/60 border border-slate-200 dark:border-neutral-800 flex items-center justify-between gap-3"
              >
                <div className="min-w-0">
                  <div className="flex items-center gap-2">
                    <span className="text-xs font-semibold text-slate-800 dark:text-neutral-200 truncate">{group.name}</span>
                    {group.isDefault && (
                      <span className="text-[10px] font-semibold text-emerald-700 dark:text-emerald-300 bg-emerald-500/15 border border-emerald-500/30 px-1.5 py-0.5 rounded">
                        默认分组
                      </span>
                    )}
                  </div>
                  <span className="text-[11px] text-slate-500 dark:text-neutral-500">{group.instanceCount} 个实例</span>
                </div>
                <button
                  type="button"
                  disabled={group.isDefault}
                  onClick={() => startDelete(group)}
                  title={group.isDefault ? '默认分组不能删除' : '删除分组'}
                  className="p-1.5 rounded-lg bg-slate-100 dark:bg-neutral-800 hover:bg-red-500/15 text-slate-400 dark:text-neutral-500 hover:text-red-600 dark:hover:text-red-400 transition-colors cursor-pointer disabled:opacity-30 disabled:cursor-not-allowed"
                >
                  <Trash2 className="w-3.5 h-3.5" />
                </button>
              </div>
            ))}
          </div>

          {deleteTarget && (
            <div className="p-3 rounded-xl border border-red-500/30 bg-red-500/5 space-y-3">
              <div className="text-xs font-semibold text-red-700 dark:text-red-400 flex items-center gap-1.5">
                <AlertCircle className="w-4 h-4" />
                删除分组「{deleteTarget.name}」
              </div>
              {deleteTarget.instanceCount > 0 ? (
                <>
                  <p className="text-[11px] text-slate-600 dark:text-neutral-400">
                    该分组内还有 {deleteTarget.instanceCount} 个实例，需先选择目标分组，确认后会自动迁移。
                  </p>
                  <AppSelect
                    value={moveTo}
                    onChange={setMoveTo}
                    options={moveOptions}
                    ariaLabel="迁移目标分组"
                    placeholder="选择目标分组"
                  />
                </>
              ) : (
                <p className="text-[11px] text-slate-600 dark:text-neutral-400">该分组为空，确认后直接删除。</p>
              )}
              <div className="flex justify-end gap-2">
                <button
                  type="button"
                  onClick={() => setDeleteTarget(null)}
                  className="px-3 py-1.5 rounded-lg text-xs font-semibold text-slate-700 dark:text-neutral-300 hover:bg-slate-200 dark:hover:bg-neutral-800 transition-colors cursor-pointer"
                >
                  取消
                </button>
                <button
                  type="button"
                  onClick={() => void handleDelete()}
                  disabled={deleting || (deleteTarget.instanceCount > 0 && !moveTo)}
                  className="px-3.5 py-1.5 rounded-lg bg-red-500 hover:bg-red-400 text-white font-bold text-xs transition-colors cursor-pointer disabled:opacity-40 disabled:cursor-not-allowed"
                >
                  {deleting ? '删除中…' : deleteTarget.instanceCount > 0 ? '迁移并删除' : '确认删除'}
                </button>
              </div>
            </div>
          )}

          {error && (
            <div className="text-[11px] text-red-600 dark:text-red-400 flex items-center gap-1.5">
              <AlertCircle className="w-3.5 h-3.5 shrink-0" />
              {error}
            </div>
          )}
        </div>
      </div>
    </div>
  );
};
