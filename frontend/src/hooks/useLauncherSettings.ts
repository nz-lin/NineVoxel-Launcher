/**
 * 启动器设置的状态钩子。
 *
 * 设计稿的 `SettingsView` 原本是纯本地状态（`INITIAL_SETTINGS` 里写死
 * `downloadSource: 'bmclapi'`、`theme: 'dark'`，改完只更新 React state，
 * 关掉应用就丢）。这里把它接到后端 `SettingsService`：
 *
 * - 主题 → `appearance.theme`
 * - 下载线路 → `download.mirrorEnabled`
 *
 * 其余字段（内存分配、JVM 参数、实例目录、分辨率等）后端目前没有对应的
 * 设置项——它们属于实例能力（P5-A）或尚未进入阶段的模块。这些字段**保持
 * 本地状态**，并在 `unsupportedFields` 里如实列出，不做「看起来保存成功」
 * 的假象。
 */
import { useCallback, useEffect, useRef, useState } from 'react';
import { INITIAL_SETTINGS } from '../data/mockData';
import type { LauncherSettings } from '../types/launcher';
import {
  SETTINGS_KEY,
  fetchSettingsSnapshot,
  persistTheme,
  updateSingleSetting,
  type SettingsSnapshot,
} from '../services/settings';

/**
 * 只在本会话生效、后端暂无对应设置项的字段。
 *
 * 这些字段属于实例能力（P5-A）或尚未进入阶段的模块。列出来是为了让界面
 * 可以如实提示「此项仅本次运行有效」，而不是让用户以为已经存进设置库。
 * （会真正持久化的只有 `theme` 与 `downloadSource` 两项。）
 */
const LOCAL_ONLY_FIELDS = [
  'javaPath',
  'maxMemoryMb',
  'minMemoryMb',
  'autoDownloadJava',
  'instancesDirectory',
  'javaRuntimeDirectory',
  'accentTheme',
  'fullscreen',
  'resolutionWidth',
  'resolutionHeight',
  'jvmPreset',
  'enableDiscordRPC',
  'closeOnLaunch',
] as const;

/** 设置钩子的返回结构。 */
export interface LauncherSettingsState {
  /** 当前设置（已合并后端回显）。 */
  settings: LauncherSettings;
  /** 更新设置；会按需写入后端。 */
  updateSettings: (next: LauncherSettings) => void;
  /** 是否正在写入后端。 */
  saving: boolean;
  /** 写入失败的说明；成功或尚未保存时为 null。 */
  saveError: string | null;
  /** 是否已完成首轮回显（未完成时界面可用初始值，但不应提示「已保存」）。 */
  loaded: boolean;
  /** 仅存在于本地、后端没有对应设置项的字段名。 */
  unsupportedFields: readonly string[];
  /** 最近一次成功写入后端的时间戳；从未成功写入时为 null。 */
  lastSavedAt: number | null;
}

/**
 * 把后端快照里的值合并进启动器设置。
 *
 * @param base 作为基准的本地设置
 * @param snapshot 后端快照
 * @returns 合并后的设置
 */
function mergeSnapshot(base: LauncherSettings, snapshot: SettingsSnapshot): LauncherSettings {
  return {
    ...base,
    // 后端支持 system（跟随系统）；设计稿只有两态，这里把 system 解析成当前系统
    // 实际呈现的主题，避免界面显示与实际观感不一致。
    theme: resolveTheme(snapshot.theme),
    downloadSource: snapshot.downloadMirrorEnabled ? 'bmclapi' : 'official',
  };
}

/**
 * 把后端的三态主题解析成设计稿的两态主题。
 *
 * @param theme 后端主题值
 * @returns 实际生效的主题
 */
function resolveTheme(theme: string): 'dark' | 'light' {
  if (theme === 'light' || theme === 'dark') {
    return theme;
  }
  // system：跟随操作系统。渲染进程可用 matchMedia 判定。
  if (typeof window !== 'undefined' && typeof window.matchMedia === 'function') {
    return window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
  }
  return 'dark';
}

/**
 * 管理启动器设置并与后端同步。
 *
 * @returns 设置状态与更新函数
 */
/**
 * 设置钩子的可选参数。
 */
export interface UseLauncherSettingsOptions {
  /**
   * 写入成功后触发。
   *
   * 由调用方负责让其它读取方（顶栏、侧栏）重新拉取快照。后端也会广播
   * settings.changed.v1，但那依赖事件通道可用；回调是本地必达的兜底。
   */
  onPersisted?: () => void
}

export function useLauncherSettings(options: UseLauncherSettingsOptions = {}): LauncherSettingsState {
  // 用 ref 保存回调，避免调用方每次渲染传新函数导致 persistChanges 反复重建。
  const onPersistedRef = useRef(options.onPersisted)
  onPersistedRef.current = options.onPersisted
  const [settings, setSettings] = useState<LauncherSettings>(INITIAL_SETTINGS);
  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState<string | null>(null);
  const [loaded, setLoaded] = useState(false);
  const [lastSavedAt, setLastSavedAt] = useState<number | null>(null);
  // 记录最后一次写入成功的值，用于判断是否需要再次写盘，避免无意义的重复更新。
  const persistedRef = useRef<{ theme: string; mirror: boolean } | null>(null);
  // 串行化写入：设置项连续变化时按顺序落盘，避免后发先至覆盖。
  const writeChainRef = useRef<Promise<void>>(Promise.resolve());

  useEffect(() => {
    let cancelled = false;

    void (async () => {
      try {
        const snapshot = await fetchSettingsSnapshot();
        if (cancelled) {
          return;
        }
        setSettings((prev) => mergeSnapshot(prev, snapshot));
        persistedRef.current = {
          theme: snapshot.theme,
          mirror: snapshot.downloadMirrorEnabled,
        };
      } catch {
        // 读取失败时保留初始值，界面不提示「已保存」；错误由调用方的
        // 其它读取路径（useAppRuntime）统一呈现，这里不重复报错。
      } finally {
        if (!cancelled) {
          setLoaded(true);
        }
      }
    })();

    return () => {
      cancelled = true;
    };
  }, []);

  /**
   * 把与前次不同的字段写入后端。
   *
   * @param next 新设置
   */
  const persistChanges = useCallback((next: LauncherSettings) => {
    const previous = persistedRef.current;
    const tasks: Array<() => Promise<void>> = [];

    if (previous === null || previous.theme !== next.theme) {
      tasks.push(async () => {
        const ok = await persistTheme(next.theme);
        if (!ok) {
          throw new Error('主题未能写入设置库');
        }
      });
    }

    const wantsMirror = next.downloadSource === 'bmclapi';
    if (previous === null || previous.mirror !== wantsMirror) {
      tasks.push(async () => {
        await updateSingleSetting(SETTINGS_KEY.downloadMirrorEnabled, wantsMirror);
      });
    }

    if (tasks.length === 0) {
      return;
    }

    setSaving(true);
    setSaveError(null);
    writeChainRef.current = writeChainRef.current
      .then(async () => {
        for (const task of tasks) {
          await task();
        }
        persistedRef.current = { theme: next.theme, mirror: wantsMirror };
        // 写入确实成功才打时间戳；界面据此提示「已自动保存」，
        // 失败时不会走到这里（异常被下面的 catch 接住）。
        setLastSavedAt(Date.now());
        onPersistedRef.current?.();
      })
      .catch((cause: unknown) => {
        setSaveError(cause instanceof Error ? cause.message : String(cause));
      })
      .finally(() => {
        setSaving(false);
      });
  }, []);

  /**
   * 更新设置。
   *
   * 只把后端支持的字段写入设置库；其余字段仅在本会话生效，并通过
   * `unsupportedFields` 告知调用方。
   *
   * @param next 新设置
   */
  const updateSettings = useCallback(
    (next: LauncherSettings) => {
      setSettings(next);
      if (next.downloadSource === 'modrinth') {
        // 后端没有 Modrinth 源的对应设置项（属 P5/P6 内容生态），如实拒绝而不是静默丢弃。
        setSaveError('Modrinth 下载源尚未接入，该项未保存');
        return;
      }
      persistChanges(next);
    },
    [persistChanges],
  );

  return {
    settings,
    updateSettings,
    saving,
    saveError,
    loaded,
    unsupportedFields: LOCAL_ONLY_FIELDS,
    lastSavedAt,
  };
}
