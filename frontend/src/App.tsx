/**
 * @license
 * SPDX-License-Identifier: Apache-2.0
 */

import React, { useState, useEffect } from 'react';
import { TitleBar } from './components/TitleBar';
import { Sidebar, NavTab } from './components/Sidebar';
import { LaunchView } from './components/LaunchView';
import { InstancesView } from './components/InstancesView';
import { ModsHubView } from './components/ModsHubView';
import { ConsoleView } from './components/ConsoleView';
import { SettingsView } from './components/SettingsView';
import { CreateInstanceModal } from './components/CreateInstanceModal';
import { InstanceDetailModal } from './components/InstanceDetailModal';
import { AccountManagerModal } from './components/AccountManagerModal';
import { JavaRuntimeManagerModal } from './components/JavaRuntimeManagerModal';
import { DownloadsDrawer } from './components/DownloadsDrawer';
import {
  INITIAL_INSTANCES,
  INITIAL_ACCOUNTS,
  INITIAL_SETTINGS
} from './data/mockData';
import {
  Instance,
  Account,
  DownloadTask,
  LauncherSettings,
  ModItem
} from './types/launcher';
import { CheckCircle2, Coffee } from 'lucide-react';
import { useAppRuntime } from './hooks/useAppRuntime';
import { useLauncherSettings } from './hooks/useLauncherSettings';
import { useJavaRuntimes } from './hooks/useJavaRuntimes';
import { useDownloadTasks } from './hooks/useDownloadTasks';
import { runtimeService } from './services/runtime';

export default function App() {
  // 真实运行时信息：顶栏版本号与侧栏下载源状态都来自这里（读不到时如实显示「—」/「未知」）。
  const appRuntime = useAppRuntime();
  const [currentTab, setCurrentTab] = useState<NavTab>('launch');
  const [instances, setInstances] = useState<Instance[]>(INITIAL_INSTANCES);
  const [currentInstanceId, setCurrentInstanceId] = useState<string>('inst-1');
  const [accounts, setAccounts] = useState<Account[]>(INITIAL_ACCOUNTS);
  const [currentAccountId, setCurrentAccountId] = useState<string>('acc-1');
  // 设置改为走后端持久化：主题与下载线路会真正写进设置库，其余字段仅本次运行有效。
  const launcherSettings = useLauncherSettings({
    // 保存成功后立刻让顶栏与侧栏重新拉快照，不依赖事件通道是否可用。
    onPersisted: () => { void appRuntime.refreshSnapshot(); },
  });
  const settings = launcherSettings.settings;
  // Java 运行时改为真实探测：列表、选中项与重新探测都来自后端 RuntimeService。
  const javaRuntimesState = useJavaRuntimes();
  const javaRuntimes = javaRuntimesState.runtimes;

  // Synchronize documentElement class for Tailwind CSS v4 dark variant
  useEffect(() => {
    if (settings.theme === 'dark') {
      document.documentElement.classList.add('dark');
    } else {
      document.documentElement.classList.remove('dark');
    }
  }, [settings.theme]);

  // 下载任务改为真实数据：列表来自后端 DownloadService，进度由事件驱动刷新。
  const downloadTasksState = useDownloadTasks();
  const downloadTasks = downloadTasksState.tasks;

  const [launchState, setLaunchState] = useState<'ready' | 'preparing' | 'checking_assets' | 'starting_jvm' | 'running'>('ready');
  const [isMaximized, setIsMaximized] = useState(false);

  // Modals & Drawers
  const [createModalOpen, setCreateModalOpen] = useState(false);
  const [detailInstance, setDetailInstance] = useState<Instance | null>(null);
  const [accountModalOpen, setAccountModalOpen] = useState(false);
  const [javaManagerOpen, setJavaManagerOpen] = useState(false);
  const [downloadsOpen, setDownloadsOpen] = useState(false);

  // Toast notification
  const [toastMessage, setToastMessage] = useState<string | null>(null);

  const showToast = (msg: string) => {
    setToastMessage(msg);
    setTimeout(() => {
      setToastMessage((prev) => (prev === msg ? null : prev));
    }, 2800);
  };

  const handleToggleTheme = () => {
    const nextTheme = settings.theme === 'dark' ? 'light' : 'dark';
    launcherSettings.updateSettings({ ...settings, theme: nextTheme });
    showToast(nextTheme === 'light' ? '已切换为明亮界面 (Light Theme)' : '已切换为暗黑界面 (Dark Theme)');
  };

  const currentInstance = instances.find((i) => i.id === currentInstanceId) || instances[0];
  const currentAccount = accounts.find((a) => a.id === currentAccountId) || accounts[0];

  // Game launch simulation
  const handleLaunchGame = () => {
    if (launchState !== 'ready') return;

    setLaunchState('preparing');
    showToast(`正在准备启动 ${currentInstance.name} (沙盒目录: ${currentInstance.folderPath})...`);

    setTimeout(() => {
      setLaunchState('checking_assets');

      setTimeout(() => {
        setLaunchState('starting_jvm');

        setTimeout(() => {
          setLaunchState('running');
          showToast(`Minecraft ${currentInstance.mcVersion} 游戏已成功启动！`);
        }, 900);
      }, 1000);
    }, 700);
  };

  const handleKillGame = () => {
    setLaunchState('ready');
    showToast('游戏进程已由用户强制终止');
  };

  // Manage Instances
  const handleSelectInstance = (inst: Instance) => {
    setCurrentInstanceId(inst.id);
  };

  const handleCreateInstance = (newInst: Instance) => {
    setInstances((prev) => [newInst, ...prev]);
    setCurrentInstanceId(newInst.id);
    showToast(`成功创建隔离实例: ${newInst.name} (分区: ${newInst.partition})`);
    // 注意：这里不再伪造下载任务。真实的版本下载要等实例能力（P5-A）接入后
    // 由后端 DownloadService 发起，界面只负责展示真实任务。
  };

  const handleDeleteInstance = (id: string) => {
    const instToDelete = instances.find((i) => i.id === id);
    setInstances((prev) => prev.filter((i) => i.id !== id));
    if (currentInstanceId === id) {
      const remaining = instances.filter((i) => i.id !== id);
      if (remaining.length > 0) {
        setCurrentInstanceId(remaining[0].id);
      }
    }
    showToast(`已删除独立实例沙盒: ${instToDelete?.name || ''}`);
  };

  const handleDuplicateInstance = (inst: Instance) => {
    const sanitized = `${inst.name}-Copy`.replace(/\s+/g, '-');
    const cloned: Instance = {
      ...inst,
      id: `inst-${Date.now()}`,
      name: `${inst.name} (副本)`,
      isFavorite: false,
      lastPlayed: '从未运行',
      folderPath: `instances/${sanitized}/.minecraft`
    };
    setInstances((prev) => [cloned, ...prev]);
    showToast(`已成功复制实例沙盒: ${cloned.name}`);
  };

  const handleUpdateInstance = (updated: Instance) => {
    setInstances((prev) => prev.map((i) => (i.id === updated.id ? updated : i)));
    if (detailInstance?.id === updated.id) {
      setDetailInstance(updated);
    }
  };

  const handleOpenFolder = (folderPath: string) => {
    showToast(`已在系统文件管理器中定位: ${folderPath}`);
  };

  // Manage Mods
  const handleInstallMod = (mod: ModItem) => {
    // 模组源（Modrinth/CurseForge）属 P5/P6，后端还没有对应服务；
    // 这里不伪造下载任务，只如实提示。
    setInstances((prev) =>
      prev.map((inst) =>
        inst.id === currentInstanceId
          ? { ...inst, modsCount: inst.modsCount + 1 }
          : inst
      )
    );

    showToast(`模组安装需要模组源服务（P5/P6），当前仅记录在实例信息里：${mod.name}`);
  };

  // Manage Accounts
  const handleSelectAccount = (acc: Account) => {
    setCurrentAccountId(acc.id);
    setAccounts((prev) =>
      prev.map((a) => ({ ...a, active: a.id === acc.id }))
    );
    showToast(`已切换身份至: ${acc.username} (${acc.type === 'microsoft' ? '微软正版' : acc.type === 'offline' ? '离线' : '第三方皮肤站'})`);
  };

  const handleAddAccount = (acc: Account) => {
    setAccounts((prev) => [acc, ...prev.map((a) => ({ ...a, active: false }))]);
    setCurrentAccountId(acc.id);
    showToast(`已成功添加账号: ${acc.username}`);
  };

  const handleDeleteAccount = (id: string) => {
    setAccounts((prev) => prev.filter((a) => a.id !== id));
    if (currentAccountId === id) {
      const remaining = accounts.filter((a) => a.id !== id);
      if (remaining.length > 0) {
        setCurrentAccountId(remaining[0].id);
      }
    }
    showToast('已移除账号');
  };

  // Manage Java Runtimes：安装走真实下载（Adoptium 元数据 + SHA-256 校验），
  // 这里只负责发起与提示，进度由后端 download.task.changed.v1 事件驱动。
  const handleDownloadRuntime = (majorVersion: 8 | 17 | 21 | 25, vendor: string) => {
    void (async () => {
      try {
        const accepted = await runtimeService.installSource({
          majorVersion,
          acceptLicense: true,
        });
        showToast(`已开始下载 ${vendor} Java ${majorVersion}，任务 ${accepted.taskId}`);
        // 安装完成后后端会把运行时写进 java_runtimes，这里先刷新一次列表。
        void javaRuntimesState.reload(true);
      } catch (cause) {
        const message = cause instanceof Error ? cause.message : String(cause);
        showToast(`下载失败：${message}`);
      }
    })();
  };

  // 后端目前没有「删除运行时」的接口，这里如实拒绝而不是假装删掉。
  const handleDeleteRuntime = (_id: string) => {
    showToast('暂不支持在启动器内删除 Java 运行时，请手动删除对应目录后重新探测');
  };

  // Downloads drawer：操作全部走后端，不再本地改状态。
  const handleClearCompletedDownloads = () => {
    // 后端没有「批量清除已完成任务」的接口，任务记录属于审计痕迹，不能由界面删。
    showToast('后端暂不提供清除已完成任务的接口，任务记录会保留');
  };

  const handleToggleTaskPause = (id: string) => {
    const task = downloadTasks.find((t) => t.id === id);
    if (!task) return;
    // downloading → 暂停；paused → 恢复。其余状态（排队中/校验中/已结束）不可直接操作。
    if (task.status === 'downloading' && task.rawState === 'downloading') {
      void downloadTasksState.pause(id);
    } else if (task.status === 'paused') {
      void downloadTasksState.resume(id);
    } else {
      showToast(`任务当前状态为「${task.rawState ?? task.status}」，不能暂停或恢复`);
    }
  };

  return (
    <div className="w-screen h-screen bg-slate-100 dark:bg-neutral-950 text-slate-900 dark:text-neutral-100 flex flex-col overflow-hidden font-sans select-none transition-colors duration-200">
      {/* Desktop Window Frame TitleBar */}
      <TitleBar
        currentInstance={currentInstance}
        currentAccount={currentAccount}
        downloadTasks={downloadTasks}
        onOpenDownloads={() => setDownloadsOpen(true)}
        onOpenConsole={() => setCurrentTab('console')}
        onOpenJavaManager={() => setJavaManagerOpen(true)}
        onToggleMaximize={() => setIsMaximized(!isMaximized)}
        isMaximized={isMaximized}
        theme={settings.theme}
        onToggleTheme={handleToggleTheme}
        appVersion={appRuntime.appInfo?.appVersion}
      />

      {/* Main Workspace: Sidebar + Viewport */}
      <div className="flex-1 flex overflow-hidden relative">
        <Sidebar
          currentTab={currentTab}
          onSelectTab={setCurrentTab}
          currentAccount={currentAccount}
          onOpenAccountModal={() => setAccountModalOpen(true)}
          activeInstance={currentInstance}
          mirrorEnabled={appRuntime.snapshot?.downloadMirrorEnabled ?? null}
        />

        {/* Dynamic Main View */}
        <main className="flex-1 flex flex-col overflow-hidden relative bg-slate-100 dark:bg-neutral-950">
          {currentTab === 'launch' && (
            <LaunchView
              currentInstance={currentInstance}
              allInstances={instances}
              currentAccount={currentAccount}
              onSelectInstance={handleSelectInstance}
              onLaunchGame={handleLaunchGame}
              onKillGame={handleKillGame}
              launchState={launchState}
              onNavigateTab={(tab) => {
                if (tab === 'console') setCurrentTab('console');
                if (tab === 'instances') setCurrentTab('instances');
                if (tab === 'settings') setCurrentTab('settings');
              }}
              onOpenFolder={handleOpenFolder}
            />
          )}

          {currentTab === 'instances' && (
            <InstancesView
              instances={instances}
              currentInstanceId={currentInstanceId}
              onSelectCurrent={handleSelectInstance}
              onLaunchInstance={(inst) => {
                handleSelectInstance(inst);
                setCurrentTab('launch');
                handleLaunchGame();
              }}
              onOpenManageInstance={(inst) => setDetailInstance(inst)}
              onOpenCreateModal={() => setCreateModalOpen(true)}
              onDeleteInstance={handleDeleteInstance}
              onDuplicateInstance={handleDuplicateInstance}
              onOpenFolder={handleOpenFolder}
            />
          )}

          {currentTab === 'mods' && (
            <ModsHubView
              currentInstance={currentInstance}
              onInstallMod={handleInstallMod}
            />
          )}

          {currentTab === 'console' && (
            <ConsoleView
              currentInstance={currentInstance}
              onUpdateInstance={handleUpdateInstance}
            />
          )}

          {currentTab === 'accounts' && (
            <div className="flex-1 flex items-center justify-center p-6 bg-slate-100 dark:bg-neutral-950">
              <div className="text-center space-y-4 max-w-md bg-white dark:bg-neutral-900 border border-slate-200 dark:border-neutral-800 p-8 rounded-2xl shadow-sm">
                <div className="w-16 h-16 rounded-2xl bg-emerald-500/15 border border-emerald-500/30 flex items-center justify-center text-emerald-600 dark:text-emerald-400 mx-auto text-2xl">
                  👤
                </div>
                <div>
                  <h2 className="text-base font-bold text-slate-900 dark:text-neutral-100">账号档案与皮肤外披中心</h2>
                  <p className="text-xs text-slate-500 dark:text-neutral-400 mt-1">
                    当前选用账户: <strong className="text-slate-800 dark:text-neutral-200">{currentAccount.username}</strong> ({currentAccount.type === 'microsoft' ? '微软正版' : currentAccount.type === 'offline' ? '离线' : currentAccount.authServerName || '第三方皮肤站'})
                  </p>
                </div>
                <button
                  onClick={() => setAccountModalOpen(true)}
                  className="px-5 py-2.5 rounded-xl bg-emerald-500 hover:bg-emerald-400 text-neutral-950 font-bold text-xs shadow-md transition-all cursor-pointer"
                >
                  打开完整账号档案管理器
                </button>
              </div>
            </div>
          )}

          {currentTab === 'settings' && (
            <SettingsView
              settings={settings}
              onUpdateSettings={launcherSettings.updateSettings}
          saving={launcherSettings.saving}
          saveError={launcherSettings.saveError}
          lastSavedAt={launcherSettings.lastSavedAt}
              javaRuntimes={javaRuntimes}
              onOpenJavaManager={() => setJavaManagerOpen(true)}
              onOpenFolder={handleOpenFolder}
            />
          )}
        </main>
      </div>

      {/* Modals & Slide-over Drawers */}
      <CreateInstanceModal
        isOpen={createModalOpen}
        onClose={() => setCreateModalOpen(false)}
        onCreate={handleCreateInstance}
      />

      <InstanceDetailModal
        instance={detailInstance}
        isOpen={Boolean(detailInstance)}
        onClose={() => setDetailInstance(null)}
        onUpdateInstance={handleUpdateInstance}
        onOpenFolder={handleOpenFolder}
        javaRuntimes={javaRuntimes}
      />

      <AccountManagerModal
        isOpen={accountModalOpen}
        onClose={() => setAccountModalOpen(false)}
        accounts={accounts}
        currentAccountId={currentAccountId}
        onSelectAccount={handleSelectAccount}
        onAddAccount={handleAddAccount}
        onDeleteAccount={handleDeleteAccount}
      />

      <JavaRuntimeManagerModal
        isOpen={javaManagerOpen}
        onClose={() => setJavaManagerOpen(false)}
        runtimes={javaRuntimes}
        onDownloadRuntime={handleDownloadRuntime}
        onDeleteRuntime={handleDeleteRuntime}
        onOpenFolder={handleOpenFolder}
        selectedRuntimeId={javaRuntimesState.selectedRuntimeId}
        onSelectRuntime={javaRuntimesState.selectRuntime}
        onReload={() => javaRuntimesState.reload(true)}
      />

      <DownloadsDrawer
        isOpen={downloadsOpen}
        onClose={() => setDownloadsOpen(false)}
        tasks={downloadTasks}
        onClearCompleted={handleClearCompletedDownloads}
        onToggleTaskPause={handleToggleTaskPause}
      />

      {/* Floating Toast Notification */}
      {toastMessage && (
        <div className="fixed bottom-6 right-6 z-50 bg-white dark:bg-neutral-900 border border-slate-200 dark:border-neutral-700/80 text-slate-800 dark:text-neutral-100 px-4 py-2.5 rounded-xl shadow-2xl flex items-center gap-2.5 text-xs animate-in slide-in-from-bottom-3 duration-200">
          <CheckCircle2 className="w-4 h-4 text-emerald-500 dark:text-emerald-400 shrink-0" />
          <span className="font-medium">{toastMessage}</span>
        </div>
      )}
    </div>
  );
}
