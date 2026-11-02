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
import { runtimeService } from '../services/runtime';
import type { JavaRuntime } from '../types/launcher';
import type { RuntimeProbeWarning, RuntimeSummary } from '../types/runtime';

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

  // 卸载后不再回写状态，避免 React 严格模式下旧请求覆盖新结果。
  const cancelledRef = useRef(false);

  /**
   * 读取运行时列表。
   *
   * @param refresh 为 true 时绕过缓存重新扫盘
   */
  const reload = useCallback(async (refresh = false) => {
    setLoading(true);
    setError(null);
    try {
      if (refresh) {
        // 用户显式要求重新探测：真正扫盘，返回的是探测结果（含告警与探测时间）。
        const probe = await runtimeService.detect(true);
        if (cancelledRef.current) return;
        setRuntimes(probe.items.map(toDesignRuntime));
        setProbedAtMs(probe.probedAtMs);
        setWarnings(probe.warnings);
      } else {
        // 首次进入：只读已缓存的结果，不触发新的磁盘扫描。
        const list = await runtimeService.list();
        if (cancelledRef.current) return;
        setRuntimes(list.items.map(toDesignRuntime));
        setSelectedRuntimeId(list.selectedRuntimeId);
        // 列表接口不返回探测时间，这里记录的是本次读取的时刻。
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

  return {
    runtimes,
    selectedRuntimeId,
    loading,
    error,
    probedAtMs,
    warnings,
    reload,
    selectRuntime,
  };
}
