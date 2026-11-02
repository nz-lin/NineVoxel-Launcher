/**
 * 启动器运行时信息的读取钩子。
 *
 * 设计稿里的顶栏版本号、侧栏「网络源状态」原本都是硬编码常量
 * （`v3.2.0`、`BMCLAPI`），这里换成后端真实回显：
 *
 * - 版本号来自 `AppService.Snapshot()` 的 `appVersion`；
 * - 下载源状态来自 `SettingsService.Snapshot()` 的 `downloadMirrorEnabled`。
 *
 * 两条读取都是**只读**、失败不影响界面：取不到时对应字段保持 `null`，
 * 界面按「未知」渲染，绝不用本地默认值兜底——否则会出现
 * 「界面显示官方源、实际下载已走镜像」这种自相矛盾的状态（D-59）。
 */
import { useCallback, useEffect, useState } from 'react';
import { fetchAppInfo } from '../services/app';
import { subscribeSettingsChanged } from '../services/events';
import { fetchSettingsSnapshot, type SettingsSnapshot } from '../services/settings';
import type { AppInfo } from '../types/app';

/** 启动器运行时信息的读取结果。 */
export interface AppRuntimeState {
  /** 应用信息；尚未读取成功时为 null。 */
  appInfo: AppInfo | null;
  /** 设置快照；尚未读取成功时为 null。 */
  snapshot: SettingsSnapshot | null;
  /** 首轮读取是否仍在进行。 */
  loading: boolean;
  /** 读取失败的说明（已脱敏）；成功或尚未完成时为 null。 */
  error: string | null;
  /** 重新读取一次（界面上的「重试」入口使用）。 */
  reload: () => void;
  /** 只刷新设置快照，不改动加载状态（事件回调与保存后使用）。 */
  refreshSnapshot: () => Promise<void>;
}

/**
 * 读取应用信息与设置快照。
 *
 * 两个请求相互独立：其中一个失败不阻塞另一个，界面可以分别降级显示。
 */
export function useAppRuntime(): AppRuntimeState {
  const [appInfo, setAppInfo] = useState<AppInfo | null>(null);
  const [snapshot, setSnapshot] = useState<SettingsSnapshot | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [reloadToken, setReloadToken] = useState(0);

  const reload = useCallback(() => {
    setLoading(true);
    setReloadToken((token) => token + 1);
  }, []);

  useEffect(() => {
    // React 严格模式下 effect 会执行两次，用标记避免旧请求回写新状态。
    let cancelled = false;

    void (async () => {
      const failures: string[] = [];

      try {
        const info = await fetchAppInfo();
        if (!cancelled) {
          setAppInfo(info);
        }
      } catch (cause) {
        failures.push(cause instanceof Error ? cause.message : String(cause));
      }

      try {
        const next = await fetchSettingsSnapshot();
        if (!cancelled) {
          setSnapshot(next);
        }
      } catch (cause) {
        failures.push(cause instanceof Error ? cause.message : String(cause));
      }

      if (cancelled) {
        return;
      }
      setError(failures.length > 0 ? failures.join('；') : null);
      setLoading(false);
    })();

    return () => {
      cancelled = true;
    };
  }, [reloadToken]);

  /**
   * 重新读取设置快照，但不改动首轮加载状态。
   *
   * 用于设置被其它路径修改后同步界面（例如事件回调里）。
   */
  const refreshSnapshot = useCallback(async () => {
    try {
      const next = await fetchSettingsSnapshot();
      setSnapshot(next);
    } catch {
      // 刷新失败保留上一次的值：界面上宁可显示旧值，也不要闪成「未知」。
    }
  }, []);

  useEffect(() => {
    // 订阅后端的设置变更事件：任何一处写入设置库（设置页、主题切换、将来的
    // 命令行工具）都会触发一次快照刷新，界面因此始终反映真实状态，而不是
    // 只在挂载时读一次导致「改完不回显」。
    const unsubscribe = subscribeSettingsChanged(() => {
      void refreshSnapshot();
    });
    return unsubscribe;
  }, [refreshSnapshot]);

  return { appInfo, snapshot, loading, error, reload, refreshSnapshot };
}
