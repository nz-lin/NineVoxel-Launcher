/**
 * Java 运行时数据钩子。
 *
 * 把后端 `RuntimeService` 的真实探测结果接到设计稿的界面上：
 * - `list()` 只读缓存、不联网（命中 5 分钟探测缓存时不重新探测）；
 * - `detect(true)` 会重新探测设置中指定的 Java 路径。
 *
 * 后端 DTO 有意不下发绝对路径与安装体积，因此这两个字段在映射后为空，
 * 界面必须显示「—」而不是编造内容。
 */
import { useCallback, useEffect, useRef, useState } from 'react';
import { runtimeService, subscribeRuntimeInstallProgress } from '../services/runtime';
import type { JavaRuntime } from '../types/launcher';
import type {
  RuntimeInstallProgress,
  RuntimeProbeWarning,
  RuntimeSummary,
} from '../types/runtime';

/** 发行商标识到可读名称的映射。 */
const VENDOR_LABELS: Record<string, string> = {
  oracle: 'Oracle JDK',
  adoptium: 'Eclipse Temurin',
  microsoft: 'Microsoft OpenJDK',
  zulu: 'Azul Zulu',
  openjdk: 'OpenJDK',
  other: '未知发行商',
  unknown: '未知发行商',
};

/** 架构标识到展示文本的映射。 */
const ARCH_LABELS: Record<string, string> = {
  x64: 'x64',
  x86: 'x86',
  aarch64: 'arm64',
  unknown: '未知架构',
};

/**
 * 把后端的运行时摘要映射成界面使用的结构。
 *
 * @param item 后端探测结果中的单条运行时
 * @returns 界面可直接渲染的运行时
 */
function toDesignRuntime(item: RuntimeSummary): JavaRuntime {
  return {
    id: item.runtimeId,
    majorVersion: item.majorVersion,
    vendor: VENDOR_LABELS[item.vendor] ?? item.vendor,
    versionString: item.fullVersion || '未知版本',
    architecture: ARCH_LABELS[item.arch] ?? item.arch,
    // 后端不下发路径与体积，这里留空，由界面显示「—」。
    folderPath: undefined,
    binaryPath: undefined,
    sizeMb: undefined,
    isDownloadedByLauncher: item.source === 'downloaded',
    // 不兼容的运行时仍列出，但标记为 error 以便界面提示。
    status: item.compatible ? 'ready' : 'error',
    incompatibleReason: item.compatible ? undefined : item.compatibleReason,
  };
}

/** Java 运行时钩子的返回值。 */
export interface JavaRuntimesState {
  /** 探测到的指定路径运行时。 */
  runtimes: JavaRuntime[];
  /** 当前选中的运行时标识；空串表示尚未选择。 */
  selectedRuntimeId: string;
  /** 是否正在探测或读取。 */
  loading: boolean;
  /** 错误文案；为空表示无错误。 */
  error: string | null;
  /** 最近一次探测时间（毫秒时间戳）；0 表示未探测。 */
  probedAtMs: number;
  /** 探测过程中的非致命告警。 */
  warnings: RuntimeProbeWarning[];
  /** 重新读取（refresh 为 true 时绕过缓存重新扫盘）。 */
  reload: (refresh?: boolean) => Promise<void>;
  /** 选择要使用的运行时。 */
  selectRuntime: (runtimeId: string) => Promise<void>;
  /** 正在进行的安装进度；null 表示当前没有安装任务。 */
  installProgress: RuntimeInstallProgress | null;
}

/**
 * 管理 Java 运行时列表与选中状态。
 *
 * @returns 运行时列表、选中项、加载与错误状态，以及重新读取和选择的方法
 */
export function useJavaRuntimes(): JavaRuntimesState {
  const [runtimes, setRuntimes] = useState<JavaRuntime[]>([]);
  const [selectedRuntimeId, setSelectedRuntimeId] = useState('');
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [probedAtMs, setProbedAtMs] = useState(0);
  const [warnings, setWarnings] = useState<RuntimeProbeWarning[]>([]);
  const [installProgress, setInstallProgress] = useState<RuntimeInstallProgress | null>(null);

  // 卸载后不再回写状态，避免 React 严格模式下旧请求覆盖新结果。
  const cancelledRef = useRef(false);

  /**
   * 读取运行时列表。
   *
   * @param refresh 为 true 时先绕过缓存重新探测，再读完整列表
   */
  const reload = useCallback(async (refresh = false) => {
    setLoading(true);
    setError(null);
    try {
      if (refresh) {
        // 先刷新探测缓存。探测失败不影响列表：list() 仍能返回已安装项。
        try {
          const probe = await runtimeService.detect(true);
          if (cancelledRef.current) return;
          setProbedAtMs(probe.probedAtMs);
          setWarnings(probe.warnings);
        } catch {
          // 探测失败时保留上一次的探测时间与告警，由下面的 list() 兜底。
        }
      }

      // 列表一律走 list()：detect 只返回「探测到的本机 Java」，**不含启动器
      // 自己安装的运行时**。此前 refresh 分支直接拿 detect 的结果覆盖列表，
      // 导致安装完成后（或点「重新探测」）已安装项全部消失，只剩手动指定的那个。
      const list = await runtimeService.list();
      if (cancelledRef.current) return;
      setRuntimes(list.items.map(toDesignRuntime));
      setSelectedRuntimeId(list.selectedRuntimeId);
      if (!refresh) {
        // 首次进入只读缓存，探测时间未知，记录的是本次读取的时刻。
        setProbedAtMs(Date.now());
        setWarnings([]);
      }
    } catch (cause) {
      if (cancelledRef.current) return;
      const message = cause instanceof Error ? cause.message : String(cause);
      setError(message);
    } finally {
      if (!cancelledRef.current) setLoading(false);
    }
  }, []);

  /**
   * 选择运行时并同步后端返回的最新列表。
   *
   * @param runtimeId 要选中的运行时标识
   */
  const selectRuntime = useCallback(async (runtimeId: string) => {
    setError(null);
    try {
      const list = await runtimeService.select({ runtimeId });
      if (cancelledRef.current) return;
      setRuntimes(list.items.map(toDesignRuntime));
      setSelectedRuntimeId(list.selectedRuntimeId);
    } catch (cause) {
      if (cancelledRef.current) return;
      const message = cause instanceof Error ? cause.message : String(cause);
      setError(message);
    }
  }, []);

  useEffect(() => {
    cancelledRef.current = false;
    void reload(false);
    return () => {
      cancelledRef.current = true;
    };
  }, [reload]);

  // 订阅安装进度：后端复用 download.task.changed.v1 推进度，taskId 形如
  // runtime-install-<runtimeId>。此前没有任何组件订阅这个通道，所以下载
  // 过程中界面一片空白；终态时刷新列表，让新装的运行时立刻出现。
  useEffect(() => {
    const unsubscribe = subscribeRuntimeInstallProgress((progress) => {
      if (cancelledRef.current) return;
      setInstallProgress(progress);
      if (progress.phase === 'completed') {
        void reload(false);
        // 「安装完成」留 4 秒让用户看见，然后回到常态，避免进度条一直挂着。
        window.setTimeout(() => {
          if (!cancelledRef.current) setInstallProgress(null);
        }, 4000);
      } else if (progress.phase === 'failed') {
        // 失败态保留在界面上，让用户能看到错误码并决定是否重试。
        void reload(false);
      }
    });
    return () => {
      unsubscribe();
    };
  }, [reload]);

  return {
    runtimes,
    selectedRuntimeId,
    loading,
    error,
    probedAtMs,
    warnings,
    reload,
    selectRuntime,
    installProgress,
  };
}
