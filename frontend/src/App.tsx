/**
 * @license
 * SPDX-License-Identifier: Apache-2.0
 */

import React, { useState, useEffect, useMemo, useRef } from 'react';
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
import { InstanceTrashModal } from './components/InstanceTrashModal';
import { DownloadsDrawer } from './components/DownloadsDrawer';
import { ConfirmDialog } from './components/ConfirmDialog';
import {
  Instance,
  Account,
  DownloadTask,
  LauncherSettings,
  ModItem
} from './types/launcher';
import type { AccountView } from './types/account';
import { ACCOUNT_KIND_LABELS } from './types/account';
import { CheckCircle2, Coffee } from 'lucide-react';
import { useAppRuntime } from './hooks/useAppRuntime';
import { useLauncherSettings } from './hooks/useLauncherSettings';
import { useJavaRuntimes } from './hooks/useJavaRuntimes';
import { useDownloadTasks } from './hooks/useDownloadTasks';
import { useInstances } from './hooks/useInstances';
import { useInstanceGroups } from './hooks/useInstanceGroups';
import { useLaunchProcess } from './hooks/useLaunchProcess';
import { instanceService, type InstanceConfig } from './services/instance';
import { useAccounts } from './hooks/useAccounts';
import { runtimeService } from './services/runtime';
import { pickJavaExecutable, validateJavaPath } from './services/launch';
import { SETTINGS_KEY, updateSingleSetting } from './services/settings';

/** 未登录时的展示占位：只用于顶栏/侧栏文案，不参与任何启动逻辑。 */
const GUEST_ACCOUNT: Account = {
  id: '',
  username: '未登录',
  uuid: '',
  type: 'offline',
  skinUrl: '',
  active: true,
  createdDate: ''
};

/**
 * 把账号子系统的 AccountView 映射成旧展示组件使用的 Account 模型。
 *
 * 展示组件（顶栏/侧栏/启动页）目前仍消费 `types/launcher` 的 Account；
 * 真实账号数据来自后端，凭证不在此结构中。等这些组件迁移到
 * `AccountView` 后即可删除本函数。
 *
 * @param view 后端账号摘要；null 表示当前没有账号
 * @returns 展示用账号模型
 */
function toDisplayAccount(view: AccountView | null): Account {
  if (!view) {
    return GUEST_ACCOUNT;
  }
  const type: Account['type'] =
    view.kind === 'microsoft' ? 'microsoft' : view.kind === 'offline' ? 'offline' : 'third_party';
  return {
    id: view.id,
    username: view.displayName || view.profile.name,
    uuid: view.profile.id,
    type,
    authServerName: view.kind === 'yggdrasil' ? view.providerId : undefined,
    authServerUrl: view.kind === 'yggdrasil' ? view.providerId : undefined,
    skinUrl: view.profile.skinUrl ?? '',
    capeUrl: view.profile.capeUrl ?? '',
    active: view.selected,
    createdDate: ''
  };
}

export default function App() {
  // 真实运行时信息：顶栏版本号与侧栏下载源状态都来自这里（读不到时如实显示「—」/「未知」）。
  const appRuntime = useAppRuntime();
  const [currentTab, setCurrentTab] = useState<NavTab>('launch');
  const instancesState = useInstances();
  const groupsState = useInstanceGroups();
  const instances = instancesState.instances;
  const currentInstanceId = instancesState.selectedId;
  // 账号改为真实后端：列表、当前账号、登录流程全部来自 AccountService（凭证不出后端）。
  const accountsState = useAccounts();
  // 设置改为走后端持久化：主题与下载线路会真正写进设置库，其余字段仅本次运行有效。
  const launcherSettings = useLauncherSettings({
    // 保存成功后立刻让顶栏与侧栏重新拉快照，不依赖事件通道是否可用。
    onPersisted: () => { void appRuntime.refreshSnapshot(); },
  });
  const settings = launcherSettings.settings;
  // Java 运行时改为真实探测：列表、选中项与重新探测都来自后端 RuntimeService。
  const javaRuntimesState = useJavaRuntimes();
  const javaRuntimes = javaRuntimesState.runtimes;
  // 真实游戏进程：状态快照是唯一真相源，事件只推动刷新。
  const launchProcess = useLaunchProcess();

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

  const [isMaximized, setIsMaximized] = useState(false);

  // Modals & Drawers
  const [createModalOpen, setCreateModalOpen] = useState(false);
  const [detailInstance, setDetailInstance] = useState<Instance | null>(null);
  const [pendingDeleteInstance, setPendingDeleteInstance] = useState<Instance | null>(null);
  const [pendingForceStop, setPendingForceStop] = useState(false);
  // 只有本次界面真正发起并观察到的启动才提示退出，避免应用重启后把
  // 上一次会话的终态快照误当成刚结束的游戏。
  const activeLaunchObservedRef = useRef(false);
  const [accountModalOpen, setAccountModalOpen] = useState(false);
  const [javaManagerOpen, setJavaManagerOpen] = useState(false);
  const [trashOpen, setTrashOpen] = useState(false);
  const [downloadsOpen, setDownloadsOpen] = useState(false);

  // Toast notification
  const [toastMessage, setToastMessage] = useState<string | null>(null);

  const showToast = (msg: string) => {
    setToastMessage(msg);
    setTimeout(() => {
      setToastMessage((prev) => (prev === msg ? null : prev));
    }, 2800);
  };

  /**
   * 让用户手动指定外部 Java 可执行文件。
   *
   * 外部 Java 不会被自动发现（不读 JAVA_HOME、不扫 PATH、不遍历安装根），
   * 这是唯一入口：选择 → 后端三层校验 → 写入设置 → 重新探测。
   *
   * 用户取消对话框属正常路径，静默保持原值，不弹错误。
   */
  const handleChooseJavaPath = async (): Promise<{ ok: boolean; message: string }> => {
    let picked;
    try {
      picked = await pickJavaExecutable();
    } catch (cause) {
      return {
        ok: false,
        message: `无法打开文件选择器：${cause instanceof Error ? cause.message : String(cause)}`,
      };
    }

    if (picked.canceled) {
      return { ok: true, message: '已取消选择，保持原有设置。' };
    }
    if (picked.errorCode) {
      return { ok: false, message: picked.errorMessage || `文件选择器不可用（${picked.errorCode}）` };
    }
    if (!picked.javaPath) {
      return { ok: false, message: '未取得 Java 路径，请重试。' };
    }

    // 校验全部在后端完成：存在性 → 可执行性 → java -version。前端不重复判定。
    const validation = await validateJavaPath(picked.javaPath);
    if (!validation.valid) {
      return {
        ok: false,
        message: validation.errorMessage || '该路径未通过 Java 校验，请确认它指向 java 可执行文件。',
      };
    }

    try {
      await updateSingleSetting(SETTINGS_KEY.launchJavaPath, picked.javaPath);
    } catch (cause) {
      return {
        ok: false,
        message: `写入设置失败：${cause instanceof Error ? cause.message : String(cause)}`,
      };
    }

    await javaRuntimesState.reload(true);
    const label =
      validation.majorVersion > 0
        ? `Java ${validation.majorVersion}`
        : validation.javaDisplay || picked.javaDisplay;
    return { ok: true, message: `已指定 ${label}：${picked.javaDisplay || picked.javaPath}` };
  };

  /** 清除手动指定的 Java 路径，回到「只用启动器安装的运行时」。 */
  const handleClearJavaPath = async (): Promise<void> => {
    await updateSingleSetting(SETTINGS_KEY.launchJavaPath, '');
    await javaRuntimesState.reload(true);
  };

  const handleToggleTheme = () => {
    const nextTheme = settings.theme === 'dark' ? 'light' : 'dark';
    launcherSettings.updateSettings({ ...settings, theme: nextTheme });
    showToast(nextTheme === 'light' ? '已切换为明亮界面 (Light Theme)' : '已切换为暗黑界面 (Dark Theme)');
  };

  const currentInstance = instances.find((i) => i.id === currentInstanceId) || instances[0];
  // 真实账号 → 旧展示模型；没有账号时显示「未登录」占位（不参与启动逻辑）。
  const currentAccount = useMemo(
    () => toDisplayAccount(accountsState.currentAccount),
    [accountsState.currentAccount],
  );

  const reportError = (cause: unknown) => {
    if (cause instanceof Error) {
      showToast(cause.message);
      return;
    }
    if (cause && typeof cause === 'object' && 'message' in cause) {
      showToast(String((cause as { message: unknown }).message));
      return;
    }
    showToast(String(cause));
  };

  const startInstanceGame = async (instance: Instance) => {
    if (instance.installationState !== 'installed') {
      showToast('请先在实例列表完成游戏资源安装。');
      setCurrentTab('instances');
      return;
    }
    try {
      activeLaunchObservedRef.current = true;
      await launchProcess.start(instance.id, currentAccount.id);
      showToast(`正在启动 ${instance.name}。`);
    } catch (cause) {
      reportError(cause);
    }
  };

  const handleLaunchGame = () => {
    if (currentInstance) void startInstanceGame(currentInstance);
  };

  const handleStopGame = () => {
    void launchProcess.stop(false)
      .then(() => showToast('已请求停止游戏，正在等待进程退出。'))
      .catch(reportError);
  };

  const confirmForceStop = () => {
    setPendingForceStop(false);
    void launchProcess.stop(true)
      .then(() => showToast('已强制结束游戏进程。'))
      .catch(reportError);
  };

  const lastLaunchNoticeRef = useRef('');
  useEffect(() => {
    const status = launchProcess.status;
    if (!status || !status.launchId || status.state !== 'exited') return;
    if (!activeLaunchObservedRef.current) return;
    activeLaunchObservedRef.current = false;
    const noticeKey = `${status.launchId}:${status.stateChangedAtMs}`;
    if (lastLaunchNoticeRef.current === noticeKey) return;
    lastLaunchNoticeRef.current = noticeKey;
    if (status.outcome === 'crashed') {
      const code = status.hasExitCode ? `（退出码 ${status.exitCode}）` : '';
      showToast(`游戏异常退出${code}，可在控制台查看日志。`);
    } else if (status.outcome === 'exited_normally') {
      showToast('游戏已退出。');
    } else {
      showToast('游戏进程已停止。');
    }
  }, [launchProcess.status]);

  useEffect(() => {
    const status = launchProcess.status;
    if (!status || !status.launchId) return;
    if (status.state !== 'exited' && status.state !== 'failed' && status.state !== 'cancelled') {
      activeLaunchObservedRef.current = true;
    }
  }, [launchProcess.status]);

  const handleSelectInstance = (inst: Instance) => {
    void instanceService.select(inst.id).then(instancesState.reload).catch(reportError);
  };

  const handleCreateInstance = async (config: InstanceConfig) => {
    const created = await instanceService.create(config);
    // Creation is durable even if starting the download fails; retain a retry entry.
    await instancesState.reload();
    void groupsState.reload();
    try {
      await instanceService.select(created.id);
      await instanceService.install(created.id);
      showToast(`已创建 ${created.name}，开始安装游戏资源。`);
    } catch (cause) {
      reportError(cause);
    }
    await instancesState.reload();
    setCurrentTab('instances');
  };

  const handleDeleteInstance = (id: string) => {
    const target = instances.find((item) => item.id === id);
    if (target) setPendingDeleteInstance(target);
  };

  const confirmDeleteInstance = () => {
    const target = pendingDeleteInstance;
    if (!target) return;
    setPendingDeleteInstance(null);
    void (async () => {
      try {
        await instanceService.remove(target.id);
        if (detailInstance?.id === target.id) setDetailInstance(null);
        await instancesState.reload();
        void groupsState.reload();
        showToast('已移入实例回收站，可随时还原。');
      } catch (cause) { reportError(cause); }
    })();
  };

  const handleDuplicateInstance = (inst: Instance) => {
    void handleCreateInstance({ ...inst, name: `${inst.name.slice(0, 70)} (副本)`, isFavorite: false }).catch(reportError);
  };

  const handleUpdateInstance = async (updated: Instance) => {
    const persisted = await instanceService.update(updated);
    setDetailInstance(persisted);
    await instancesState.reload();
    void groupsState.reload();
    showToast('实例配置已保存。');
  };

  const handleDetailInstanceChanged = (updated: Instance) => {
    setDetailInstance(updated);
    void instancesState.reload();
  };

  useEffect(() => {
    setDetailInstance((current) => {
      if (!current) return current;
      const latest = instances.find((item) => item.id === current.id);
      if (!latest) return current;
      if (
        current.loader === latest.loader &&
        current.loaderVersion === latest.loaderVersion &&
        current.versionId === latest.versionId &&
        current.installationState === latest.installationState &&
        current.installationProgress === latest.installationProgress &&
        current.installationError === latest.installationError &&
        current.installTaskId === latest.installTaskId
      ) {
        return current;
      }
      return latest;
    });
  }, [instances]);

  const handleCreateGroup = async (name: string) => {
    await groupsState.createGroup(name);
    showToast(`已创建分组「${name}」。`);
  };

  const handleDeleteGroup = async (name: string, moveTo?: string) => {
    await groupsState.deleteGroup(name, moveTo);
    await instancesState.reload();
    showToast(moveTo ? `已将实例迁移到「${moveTo}」并删除分组「${name}」。` : `已删除分组「${name}」。`);
  };

  const handleRetryInstall = (inst: Instance) => {
    void instanceService.install(inst.id).then(instancesState.reload).catch(reportError);
  };

  const handleOpenFolder = (folderPathOrId: string) => {
    const inst = instances.find(item => item.id === folderPathOrId || item.folderPath === folderPathOrId);
    if (inst) { void instanceService.openFolder(inst.id).catch(reportError); }
    else { showToast('此目录的打开入口尚未接入。'); }
  };

  const handleTrashChanged = async () => {
    await instancesState.reload();
    void groupsState.reload();
  };

  const handleInstallMod = (_mod: ModItem) => {
    showToast('模组安装服务尚未接入。');
  };

  // Manage Java Runtimes：安装走真实下载（Adoptium 元数据 + SHA-256 校验），
  // 这里只负责发起与提示，进度由后端 download.task.changed.v1 事件驱动。
  const handleDownloadRuntime = (majorVersion: number, vendor: string) => {
    void (async () => {
      try {
        const accepted = await runtimeService.installSource({
          majorVersion,
          vendor,
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

  // 卸载启动器自己安装的运行时：后端先删目录再删记录，返回最新列表。
  // 本机探测到的 Java 会被后端明确拒绝（不属于启动器管理）。
  const handleDeleteRuntime = (id: string) => {
    void (async () => {
      try {
        await runtimeService.uninstall(id);
        showToast('已删除该 Java 运行时');
        void javaRuntimesState.reload(false);
      } catch (cause) {
        const message = cause instanceof Error ? cause.message : String(cause);
        showToast(`删除失败：${message}`);
      }
    })();
  };

  // Downloads drawer：操作全部走后端，不再本地改状态。
  const handleClearCompletedDownloads = () => {
    void downloadTasksState.clearCompleted()
      .then(() => showToast('已清除已结束的下载任务记录'))
      .catch(reportError);
  };

  const handleToggleTaskPause = (id: string) => {
    const task = downloadTasks.find((t) => t.id === id);
    if (!task) return;
    // 计划任务的父状态会一直是 queued，实际由子任务推进；这里按展示状态判断，
    // 仅在校验/提交阶段禁止暂停。
    const rawState = task.rawState ?? '';
    if (task.status === 'downloading' && rawState !== 'verifying' && rawState !== 'committing') {
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
          instanceCount={instances.length}
          mirrorEnabled={appRuntime.snapshot?.downloadMirrorEnabled ?? null}
        />

        {/* Dynamic Main View */}
        <main className="flex-1 flex flex-col overflow-hidden relative bg-slate-100 dark:bg-neutral-950">
          {instancesState.error && <div role="alert" className="px-6 py-3 text-xs text-rose-600 bg-rose-500/10">实例服务：{instancesState.error}<button onClick={() => { void instancesState.reload(); }} className="ml-3 underline">重试</button></div>}
          {!currentInstance && ['launch', 'mods', 'console'].includes(currentTab) && (
            <div className="flex-1 flex flex-col items-center justify-center gap-4 p-8 text-center">
              <h2 className="font-bold text-lg">{instancesState.loading ? '正在读取实例…' : '创建你的第一个 Minecraft 实例'}</h2>
              <p className="text-sm text-slate-500">原版游戏资源会安装到独立目录，实例配置会自动保存。</p>
              <button disabled={instancesState.loading || !!instancesState.error} onClick={() => setCreateModalOpen(true)} className="px-5 py-2 rounded-lg bg-emerald-500 text-neutral-950 text-sm font-bold disabled:opacity-40">新建实例</button>
            </div>
          )}

          {currentTab === 'launch' && currentInstance && (
            <LaunchView
              currentInstance={currentInstance}
              allInstances={instances}
              currentAccount={currentAccount}
              onSelectInstance={handleSelectInstance}
              onLaunchGame={handleLaunchGame}
              onStopGame={handleStopGame}
              onForceStopGame={() => setPendingForceStop(true)}
              launchPhase={launchProcess.phase}
              launchError={launchProcess.error}
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
                void (async () => {
                  try {
                    await instanceService.select(inst.id);
                    await instancesState.reload();
                  } catch (cause) {
                    reportError(cause);
                    return;
                  }
                  setCurrentTab('launch');
                  await startInstanceGame(inst);
                })();
              }}
              onOpenManageInstance={(inst) => setDetailInstance(inst)}
              onOpenCreateModal={() => setCreateModalOpen(true)}
              onOpenTrash={() => setTrashOpen(true)}
              onDeleteInstance={handleDeleteInstance}
              onDuplicateInstance={handleDuplicateInstance}
              onRetryInstall={handleRetryInstall}
              onOpenFolder={handleOpenFolder}
              groups={groupsState.groups}
              defaultGroup={groupsState.defaultGroup}
              onCreateGroup={handleCreateGroup}
              onDeleteGroup={handleDeleteGroup}
            />
          )}

          {currentTab === 'mods' && currentInstance && (
            <ModsHubView
              currentInstance={currentInstance}
              onInstallMod={handleInstallMod}
            />
          )}

          {currentTab === 'console' && currentInstance && (
            <ConsoleView
              currentInstance={currentInstance}
              launchId={launchProcess.status?.launchId ?? ''}
            />
          )}

          {currentTab === 'accounts' && (
            <div className="flex-1 flex items-center justify-center p-6 bg-slate-100 dark:bg-neutral-950">
              <div className="text-center space-y-4 max-w-md bg-white dark:bg-neutral-900 border border-slate-200 dark:border-neutral-800 p-8 rounded-2xl shadow-sm">
                <div className="w-16 h-16 rounded-2xl bg-emerald-500/15 border border-emerald-500/30 flex items-center justify-center text-emerald-600 dark:text-emerald-400 mx-auto text-2xl">
                  👤
                </div>
                <div>
                  <h2 className="text-base font-bold text-slate-900 dark:text-neutral-100">账号档案中心</h2>
                  <p className="text-xs text-slate-500 dark:text-neutral-400 mt-1">
                    当前选用账户: <strong className="text-slate-800 dark:text-neutral-200">{currentAccount.username}</strong>
                    {accountsState.currentAccount && ` (${ACCOUNT_KIND_LABELS[accountsState.currentAccount.kind as keyof typeof ACCOUNT_KIND_LABELS] ?? accountsState.currentAccount.kind})`}
                  </p>
                  <p className="text-[11px] text-slate-500 dark:text-neutral-500 mt-1">
                    已保存 {accountsState.accounts.length} 个账号 · 支持微软、离线与自定 Yggdrasil
                  </p>
                  {accountsState.loading && (
                    <p className="text-[11px] text-slate-500 dark:text-neutral-500 mt-1">正在读取账号…</p>
                  )}
                  {accountsState.error && (
                    <p className="text-[11px] text-rose-600 dark:text-rose-400 mt-1">
                      账号服务错误：{accountsState.error.message}
                    </p>
                  )}
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
              totalMemoryMb={appRuntime.appInfo?.totalMemoryMb}
            />
          )}
        </main>
      </div>

      {/* Modals & Slide-over Drawers */}
      {/* 弹窗按需挂载：关闭即卸载，避免 hooks 调用数量变化导致白屏，
          同时保证每次打开都用当前实例重新初始化状态（不串数据）。 */}
      {createModalOpen && (
        <CreateInstanceModal
          onClose={() => setCreateModalOpen(false)}
          onCreate={handleCreateInstance}
          groups={groupsState.groups}
          defaultGroup={groupsState.defaultGroup}
        />
      )}

      {detailInstance && (
        <InstanceDetailModal
          // key 保证切换到另一个实例时组件重新挂载，state 不会沿用上一个实例。
          key={detailInstance.id}
          instance={detailInstance}
          onClose={() => setDetailInstance(null)}
          onUpdateInstance={handleUpdateInstance}
          onInstanceChanged={handleDetailInstanceChanged}
          onOpenFolder={handleOpenFolder}
          javaRuntimes={javaRuntimes}
          groups={groupsState.groups}
          defaultGroup={groupsState.defaultGroup}
        />
      )}

      <AccountManagerModal
        isOpen={accountModalOpen}
        onClose={() => setAccountModalOpen(false)}
        controller={accountsState}
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
        javaPath={settings.javaPath}
        onChooseJavaPath={handleChooseJavaPath}
        onClearJavaPath={handleClearJavaPath}
        installProgress={javaRuntimesState.installProgress}
      />

      {trashOpen && (
        <InstanceTrashModal
          isOpen={trashOpen}
          onClose={() => setTrashOpen(false)}
          onChanged={handleTrashChanged}
        />
      )}

      {pendingDeleteInstance && (
        <ConfirmDialog
          title="移入实例回收站"
          message={`确认把「${pendingDeleteInstance.name}」移入回收站？游戏存档会保留，可随时还原或永久删除。`}
          confirmLabel="移入回收站"
          danger
          onConfirm={confirmDeleteInstance}
          onCancel={() => setPendingDeleteInstance(null)}
        />
      )}

      {pendingForceStop && (
        <ConfirmDialog
          title="强制结束游戏"
          message="强制结束会直接终止 JVM，游戏可能来不及保存世界数据或配置。仅在普通停止无响应时使用，确认继续？"
          confirmLabel="强制结束"
          danger
          onConfirm={confirmForceStop}
          onCancel={() => setPendingForceStop(false)}
        />
      )}

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
