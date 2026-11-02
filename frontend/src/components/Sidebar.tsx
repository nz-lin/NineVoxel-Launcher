import React from 'react';
import { AccountAvatar } from './AccountAvatar';
import {
  PlayCircle,
  Layers,
  Package,
  Terminal,
  User,
  Settings,
  ShieldCheck,
  Globe
} from 'lucide-react';
import { Account, Instance } from '../types/launcher';

export type NavTab = 'launch' | 'instances' | 'mods' | 'console' | 'accounts' | 'settings';

interface SidebarProps {
  currentTab: NavTab;
  onSelectTab: (tab: NavTab) => void;
  currentAccount: Account;
  onOpenAccountModal: () => void;
  activeInstance?: Instance;
  /**
   * 下载镜像开关的真实状态。
   *
   * `true` 走 BMCLAPI 镜像、`false` 走官方源、`null` 表示尚未读取到
   * （后端不可用或首轮请求未完成）。不允许用本地默认值兜底（D-59）。
   */
  mirrorEnabled?: boolean | null;
}

export const Sidebar: React.FC<SidebarProps> = ({
  currentTab,
  onSelectTab,
  currentAccount,
  onOpenAccountModal,
  activeInstance,
  mirrorEnabled = null
}) => {
  const navItems = [
    { id: 'launch' as NavTab, label: '启动主页', icon: PlayCircle },
    { id: 'instances' as NavTab, label: '版本实例', icon: Layers },
    { id: 'mods' as NavTab, label: '模组工坊', icon: Package },
    { id: 'console' as NavTab, label: '运行终端', icon: Terminal },
    { id: 'accounts' as NavTab, label: '账号档案', icon: User },
    { id: 'settings' as NavTab, label: '启动设置', icon: Settings },
  ];

  return (
    <aside className="w-56 bg-slate-50/90 dark:bg-neutral-900/70 border-r border-slate-200/80 dark:border-neutral-800/70 backdrop-blur-xl flex flex-col justify-between p-3 select-none shrink-0 z-20 transition-colors duration-150">
      {/* Top Navigation Links */}
      <div className="space-y-6">
        {/* Navigation list */}
        <nav className="space-y-1">
          {navItems.map((item) => {
            const Icon = item.icon;
            const isActive = currentTab === item.id;
            return (
              <button
                key={item.id}
                onClick={() => onSelectTab(item.id)}
                className={`w-full flex items-center gap-3 px-3 py-2.5 rounded-lg text-sm font-medium transition-all cursor-pointer ${
                  isActive
                    ? 'bg-emerald-500/15 text-emerald-700 dark:text-emerald-400 font-semibold shadow-xs shadow-emerald-500/10 border border-emerald-500/30'
                    : 'text-slate-600 dark:text-neutral-400 hover:text-slate-900 dark:hover:text-neutral-200 hover:bg-slate-200/60 dark:hover:bg-neutral-800/60 border border-transparent'
                }`}
              >
                <Icon className={`w-4 h-4 ${isActive ? 'text-emerald-600 dark:text-emerald-400' : 'text-slate-400 dark:text-neutral-500'}`} />
                <span>{item.label}</span>
                {item.id === 'instances' && (
                  <span className="ml-auto text-[11px] font-mono text-slate-500 dark:text-neutral-500 bg-slate-200/80 dark:bg-neutral-800/80 px-1.5 py-0.5 rounded">
                    5
                  </span>
                )}
                {item.id === 'console' && activeInstance?.status === 'running' && (
                  <span className="ml-auto w-2 h-2 rounded-full bg-emerald-500 dark:bg-emerald-400 animate-ping" />
                )}
              </button>
            );
          })}
        </nav>

        {/* Quick status widget */}
        <div className="p-3 rounded-lg bg-white dark:bg-neutral-950/60 border border-slate-200/80 dark:border-neutral-800/60 text-xs space-y-2 shadow-xs">
          <div className="flex items-center justify-between text-slate-500 dark:text-neutral-400">
            <span className="text-[11px]">网络源状态</span>
            {/* 取值来自后端 SettingsSnapshot.downloadMirrorEnabled；未知时如实显示「未知」。 */}
            <span
              className={`flex items-center gap-1 text-[11px] font-medium ${
                mirrorEnabled === true
                  ? 'text-emerald-600 dark:text-emerald-400'
                  : mirrorEnabled === false
                    ? 'text-slate-600 dark:text-neutral-300'
                    : 'text-slate-400 dark:text-neutral-500'
              }`}
            >
              <Globe className="w-3 h-3" />
              {mirrorEnabled === true ? 'BMCLAPI 镜像' : mirrorEnabled === false ? '官方源' : '未知'}
            </span>
          </div>
          <div className="flex items-center justify-between text-slate-500 dark:text-neutral-400">
            <span className="text-[11px]">安全验证</span>
            {/* P3 起下载产物按官方元数据 SHA-1 校验（D-27），这里展示的是既成事实。 */}
            <span className="flex items-center gap-1 text-[11px] text-teal-600 dark:text-teal-400 font-medium">
              <ShieldCheck className="w-3 h-3" /> SHA-1 校验
            </span>
          </div>
        </div>
      </div>

      {/* Bottom Account Card */}
      <div className="pt-3 border-t border-slate-200/80 dark:border-neutral-800/60">
        <button
          onClick={onOpenAccountModal}
          className="w-full flex items-center gap-2.5 p-2 rounded-lg bg-white/70 dark:bg-neutral-950/40 hover:bg-slate-200/60 dark:hover:bg-neutral-800/70 border border-slate-200 dark:border-neutral-800/50 hover:border-slate-300 dark:hover:border-neutral-700 transition-all text-left group cursor-pointer shadow-xs"
        >
          {/* 账号头像：从皮肤纹理裁剪头部 */}
          <div className="relative w-8 h-8 shrink-0">
            <AccountAvatar
              name={currentAccount.username}
              skinUrl={currentAccount.skinUrl}
              size={32}
              className="rounded border border-slate-300 dark:border-neutral-700/80"
            />
            <div className="absolute -bottom-0.5 -right-0.5 w-2.5 h-2.5 bg-emerald-500 rounded-full border border-white dark:border-neutral-900" />
          </div>

          <div className="min-w-0 flex-1">
            <div className="text-xs font-semibold text-slate-800 dark:text-neutral-200 truncate group-hover:text-emerald-600 dark:group-hover:text-emerald-300 transition-colors">
              {currentAccount.username}
            </div>
            <div className="text-[11px] text-slate-500 dark:text-neutral-500 truncate flex items-center gap-1">
              <span>{currentAccount.type === 'microsoft' ? '微软正版' : currentAccount.type === 'offline' ? '离线账户' : '第三方皮肤站'}</span>
            </div>
          </div>
        </button>
      </div>
    </aside>
  );
};
