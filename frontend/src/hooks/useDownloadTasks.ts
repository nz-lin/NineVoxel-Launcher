/**
 * 下载任务数据钩子。
 *
 * 数据来自后端 `DownloadService`：
 * - `list()` 拉取任务列表；
 * - `download.task.changed.v1` 事件只带 taskId 与状态，收到后按 taskId
 *   重新拉一次快照覆盖本地，避免用事件里的片段拼进度。
 */
import { useCallback, useEffect, useRef, useState } from 'react';
import { downloadService, subscribeDownloadTaskChanged } from '../services/download';
import { RUNTIME_INSTALL_TASK_PREFIX } from '../types/runtime';
import type { DownloadTask } from '../types/launcher';
import type { DownloadTaskView } from '../types/download';

/** 把字节速率格式化成人类可读文本。 */
function formatSpeed(bytesPerSecond: number | null): string {
  if (bytesPerSecond === null || bytesPerSecond <= 0) return '';
  const units = ['B/s', 'KB/s', 'MB/s', 'GB/s'];
  let value = bytesPerSecond;
  let index = 0;
  while (value >= 1024 && index < units.length - 1) {
    value /= 1024;
    index += 1;
  }
  return `${value.toFixed(index === 0 ? 0 : 1)} ${units[index]}`;
}

/**
 * 把后端任务视图映射成设计稿的下载任务结构。
 *
 * 后端有九种状态，设计稿只区分四类；这里把排队/校验/提交都归入「下载中」，
 * 取消归入「失败」，并在名称后保留原始状态，避免信息丢失。
 *
 * @param view 后端任务视图
 * @returns 界面可直接渲染的下载任务
 */
function toDesignTask(view: DownloadTaskView): DownloadTask {
  const state = view.state;
  const status: DownloadTask['status'] =
    state === 'completed'
      ? 'completed'
      : state === 'paused'
        ? 'paused'
        : state === 'failed' || state === 'cancelled'
          ? 'failed'
          : 'downloading';

  // 后端进度是 0~1 的小数且可能为 null，界面要 0~100 的整数。
  const progress =
    view.progress === null ? (status === 'completed' ? 100 : 0) : Math.round(view.progress * 100);

  return {
    id: view.id,
    name: view.title,
    // 后端不区分内容类型，P3 阶段下载的都是游戏本体与资源文件。
    category: 'core',
    progress,
    downloadedBytes: view.downloadedBytes,
    totalBytes: view.totalBytes ?? 0,
    speed: formatSpeed(view.speedBytesPerSecond),
    status,
    targetFolder: undefined,
    // 保留后端原始状态与计数，界面可据此显示「3/75」这类细节。
    rawState: state,
    completedCount: view.completedCount,
    artifactCount: view.artifactCount,
    errorMessage: view.error?.message ?? undefined,
  };
}

/** 下载任务钩子的返回值。 */
export interface DownloadTasksState {
  /** 任务列表（新的在前）。 */
  tasks: DownloadTask[];
  /** 是否正在加载。 */
  loading: boolean;
  /** 错误文案；为空表示无错误。 */
  error: string | null;
  /** 重新拉取列表。 */
  reload: () => Promise<void>;
  /** 暂停任务。 */
  pause: (taskId: string) => Promise<void>;
  /** 恢复任务。 */
  resume: (taskId: string) => Promise<void>;
  /** 取消任务（终态，不可恢复）。 */
  cancel: (taskId: string) => Promise<void>;
}

/**
 * 管理下载任务列表与事件订阅。
 *
 * @returns 任务列表、加载与错误状态，以及暂停/恢复/取消方法
 */
export function useDownloadTasks(): DownloadTasksState {
  const [tasks, setTasks] = useState<DownloadTask[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const cancelledRef = useRef(false);

  /** 拉取完整列表。 */
  const reload = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const list = await downloadService.list({ limit: 100, parentsOnly: true });
      if (cancelledRef.current) return;
      // 后端按创建时间升序返回时，界面要最新在前。
      setTasks(list.tasks.map(toDesignTask));
    } catch (cause) {
      if (cancelledRef.current) return;
      setError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      if (!cancelledRef.current) setLoading(false);
    }
  }, []);

  /** 按 taskId 拉单任务快照并覆盖本地对应项。 */
  const refreshOne = useCallback(async (taskId: string) => {
    try {
      const view = await downloadService.get(taskId);
      if (cancelledRef.current) return;
      setTasks((prev) => {
        const next = toDesignTask(view);
        const index = prev.findIndex((t) => t.id === taskId);
        if (index < 0) return [next, ...prev];
        const copy = prev.slice();
        copy[index] = next;
        return copy;
      });
    } catch {
      // 单条刷新失败不打断界面：下一轮事件或手动刷新会补上。
    }
  }, []);

  /**
   * 执行一个会改变任务状态的操作。
   *
   * @param action 后端操作
   */
  const runAction = useCallback(
    async (action: () => Promise<DownloadTaskView>) => {
      setError(null);
      try {
        const view = await action();
        if (cancelledRef.current) return;
        setTasks((prev) => {
          const next = toDesignTask(view);
          const index = prev.findIndex((t) => t.id === view.id);
          if (index < 0) return [next, ...prev];
          const copy = prev.slice();
          copy[index] = next;
          return copy;
        });
      } catch (cause) {
        if (cancelledRef.current) return;
        setError(cause instanceof Error ? cause.message : String(cause));
      }
    },
    [],
  );

  useEffect(() => {
    cancelledRef.current = false;
    void reload();

    // 事件只用来知道「哪个任务变了」，具体数值仍然以后端快照为准。
    const unsubscribe = subscribeDownloadTaskChanged((envelope) => {
      const taskId = envelope.payload.taskId;
      // 运行时安装复用 `download.task.changed.v1` 推送进度（D-45），
      // 但它的 `runtime-install-*` 任务不在下载队列里。不过滤就会拿这个 id
      // 去调 GetDownloadTask，每次进度事件都在 Wails 日志里留下一条
      // DOWNLOAD_STATE_001「下载任务不存在或已被清理」。
      if (!taskId || taskId.startsWith(RUNTIME_INSTALL_TASK_PREFIX)) {
        return;
      }
      void refreshOne(taskId);
    });

    return () => {
      cancelledRef.current = true;
      unsubscribe();
    };
  }, [reload, refreshOne]);

  return {
    tasks,
    loading,
    error,
    reload,
    pause: (taskId) => runAction(() => downloadService.pause(taskId)),
    resume: (taskId) => runAction(() => downloadService.resume(taskId)),
    cancel: (taskId) => runAction(() => downloadService.cancel(taskId)),
  };
}
