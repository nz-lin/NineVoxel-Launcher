import React, { useEffect, useState } from 'react';
import {
  X,
  Coffee,
  Download,
  FolderOpen,
  CheckCircle2,
  Trash2,
  RefreshCw,
  Cpu,
  Play,
  Sparkles,
  AlertCircle,
  Check
} from 'lucide-react';
import { JavaRuntime } from '../types/launcher';
import type { RuntimeInstallProgress } from '../types/runtime';
import { runtimeService } from '../services/runtime';

interface JavaRuntimeManagerModalProps {
  isOpen: boolean;
  onClose: () => void;
  runtimes: JavaRuntime[];
  onDownloadRuntime: (majorVersion: number, vendor: string) => void;
  onDeleteRuntime: (id: string) => void;
  onOpenFolder: (path: string) => void;
  /** 当前选中的运行时标识（来自后端 java_runtimes.selected）。 */
  selectedRuntimeId?: string;
  /** 选择某个运行时；不传则渲染为只读列表。 */
  onSelectRuntime?: (runtimeId: string) => void;
  /** 重新探测设置中指定的 Java 路径；不传则「环境测试」按钮不可用。 */
  onReload?: () => Promise<void>;
  /** 当前已指定的外部 Java 路径；空串表示未指定（只用启动器安装的运行时）。 */
  javaPath?: string;
  /** 让用户选择并校验一个 Java 可执行文件；返回是否成功与提示文案。 */
  onChooseJavaPath?: () => Promise<{ ok: boolean; message: string }>;
  /** 清除已指定的路径，回到「只用启动器安装的运行时」。 */
  onClearJavaPath?: () => Promise<void>;
  /** 正在进行的安装进度；null 或未提供表示当前没有安装任务。 */
  installProgress?: RuntimeInstallProgress | null;
}

/** 安装进度视图：主弹窗与下载弹窗共用，避免两处文案/进度条不一致。 */
const RuntimeInstallProgressView: React.FC<{ progress: RuntimeInstallProgress }> = ({ progress }) => {
  if (progress.phase === 'failed') {
    return (
      <div className="text-[11px] text-red-600 dark:text-red-400 flex items-center gap-1.5">
        <AlertCircle className="w-3.5 h-3.5 shrink-0" />
        <span>
          安装失败
          {progress.errorCode ? `（${progress.errorCode}）` : ''}
        </span>
      </div>
    );
  }
  return (
    <>
      <div className="flex items-center justify-between text-[11px] mb-1.5">
        <span className="text-slate-700 dark:text-neutral-300 font-semibold">
          {progress.phase === 'completed'
            ? '安装完成'
            : progress.phase === 'committing'
              ? '校验并解压…'
              : '正在下载 Java 运行时…'}
        </span>
        <span className="font-mono text-emerald-600 dark:text-emerald-400 font-bold">
          {Math.round(progress.progress * 100)}%
        </span>
      </div>
      <div className="h-1.5 rounded-full bg-slate-200 dark:bg-neutral-800 overflow-hidden">
        <div
          className="h-full rounded-full bg-emerald-500 transition-all duration-300"
          style={{
            width: `${Math.min(100, Math.max(2, Math.round(progress.progress * 100)))}%`,
          }}
        />
      </div>
    </>
  );
};

export const JavaRuntimeManagerModal: React.FC<JavaRuntimeManagerModalProps> = ({
  isOpen,
  onClose,
  runtimes,
  onDownloadRuntime,
  onDeleteRuntime,
  onOpenFolder,
  selectedRuntimeId,
  onSelectRuntime,
  onReload,
  javaPath,
  onChooseJavaPath,
  onClearJavaPath,
  installProgress
}) => {
  const [testingId, setTestingId] = useState<string | null>(null);
  const [testResult, setTestResult] = useState<{ id: string; success: boolean; msg: string } | null>(null);
  const [downloadModalOpen, setDownloadModalOpen] = useState(false);
  const [bindModalOpen, setBindModalOpen] = useState(false);
  const [selectedMajor, setSelectedMajor] = useState<number>(21);
  const [selectedVendor, setSelectedVendor] = useState('adoptium');
  // 许可必须由用户显式勾选：后端在 acceptLicense 为 false 时直接拒绝安装。
  const [licenseAccepted, setLicenseAccepted] = useState(false);
  // LTS 列表来自后端，不在前端写死；加载失败时保留错误并允许重试。
  const [ltsVersions, setLtsVersions] = useState<number[]>([]);
  const [mostRecentLts, setMostRecentLts] = useState(0);
  const [ltsLoading, setLtsLoading] = useState(false);
  const [ltsError, setLtsError] = useState<string | null>(null);
  const [ltsReloadToken, setLtsReloadToken] = useState(0);
  // 手动指定 Java 的交互结果：成功与失败都回显，不做静默处理。
  const [javaPathMessage, setJavaPathMessage] = useState<{ ok: boolean; text: string } | null>(null);
  const [choosingPath, setChoosingPath] = useState(false);

  // 下载弹窗打开或切换发行版时，拉取该发行版的 LTS 列表；后端按来源缓存 1 小时。
  useEffect(() => {
    if (!downloadModalOpen) return;
    let cancelled = false;
    setLtsLoading(true);
    setLtsError(null);
    void runtimeService
      .getLtsVersions(selectedVendor)
      .then((result) => {
        if (cancelled) return;
        setLtsVersions(result.versions);
        setMostRecentLts(result.mostRecentLts || result.versions[0] || 0);
        if (result.versions.length > 0 && !result.versions.includes(selectedMajor)) {
          setSelectedMajor(result.versions[0]);
        }
      })
      .catch((cause) => {
        if (cancelled) return;
        setLtsError(cause instanceof Error ? cause.message : String(cause));
      })
      .finally(() => {
        if (!cancelled) setLtsLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [downloadModalOpen, ltsReloadToken, selectedVendor]);

  // 安装列表按实际存在的大版本分组，不再展示固定的 8/17/21/25 卡片。
  const installedByMajor = runtimes.reduce<Record<number, JavaRuntime[]>>((groups, runtime) => {
    const key = runtime.majorVersion;
    if (!groups[key]) groups[key] = [];
    groups[key].push(runtime);
    return groups;
  }, {});
  const groupedMajors = Object.keys(installedByMajor)
    .map((value) => Number(value))
    .sort((left, right) => right - left);

  const vendorOptions = [
    { id: 'adoptium', label: 'Eclipse Temurin (Adoptium)', license: 'GPLv2 + Classpath Exception' },
    { id: 'zulu', label: 'Azul Zulu', license: 'GPLv2 + Classpath Exception' },
  ];
  const activeVendor = vendorOptions.find((item) => item.id === selectedVendor) ?? vendorOptions[0];
  const installing = installProgress != null
    && installProgress.phase !== 'completed'
    && installProgress.phase !== 'failed';

  const handleTestJava = async (runtime: JavaRuntime) => {
    setTestingId(runtime.id);
    setTestResult(null);
    try {
      // 后端没有「执行 java -version」的接口，这里调用真实重新探测，
      // 并把探测到的事实回显，不编造命令行输出。
      await onReload?.();
      setTestResult({
        id: runtime.id,
        success: true,
        msg: `已重新探测：${runtime.vendor} ${runtime.versionString}（${runtime.architecture}）`,
      });
    } catch (cause) {
      const message = cause instanceof Error ? cause.message : String(cause);
      setTestResult({ id: runtime.id, success: false, msg: `重新探测失败：${message}` });
    } finally {
      setTestingId(null);
    }
  };

  const handleChooseJavaPath = async () => {
    if (!onChooseJavaPath) return;
    setChoosingPath(true);
    setJavaPathMessage(null);
    try {
      const result = await onChooseJavaPath();
      setJavaPathMessage({ ok: result.ok, text: result.message });
    } catch (cause) {
      const message = cause instanceof Error ? cause.message : String(cause);
      setJavaPathMessage({ ok: false, text: `选择 Java 失败：${message}` });
    } finally {
      setChoosingPath(false);
    }
  };

  const handleClearJavaPath = async () => {
    if (!onClearJavaPath) return;
    setJavaPathMessage(null);
    try {
      await onClearJavaPath();
      setJavaPathMessage({ ok: true, text: '已清除指定路径，只使用启动器安装的运行时。' });
    } catch (cause) {
      const message = cause instanceof Error ? cause.message : String(cause);
      setJavaPathMessage({ ok: false, text: `清除失败：${message}` });
    }
  };

  const handleTriggerDownload = () => {
    // 未勾选许可时按钮本就禁用，这里再守一道，避免以任何方式绕过。
    if (!licenseAccepted) return;
    onDownloadRuntime(selectedMajor, selectedVendor);
    // 不关闭弹窗：下载进度要在这个弹窗内展示，避免用户关掉后看不到结果。
  };

  // 提前返回必须放在所有 hooks 之后：否则弹窗开合会改变 hook 调用数量，
  // React 会抛出「Rendered more hooks than during the previous render」。
  if (!isOpen) return null;

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center p-4 bg-black/60 dark:bg-black/75 backdrop-blur-sm animate-in fade-in duration-200">
      <div className="w-full max-w-3xl bg-white dark:bg-neutral-900 border border-slate-200 dark:border-neutral-700/80 rounded-2xl shadow-2xl flex flex-col max-h-[85vh] overflow-hidden">
        {/* Header */}
        <div className="px-6 py-4 border-b border-slate-200 dark:border-neutral-800 flex items-center justify-between bg-slate-50 dark:bg-neutral-950/60">
          <div className="flex items-center gap-2.5 min-w-0 flex-1">
            <div className="w-8 h-8 shrink-0 rounded-lg bg-emerald-500/20 border border-emerald-500/30 flex items-center justify-center text-emerald-600 dark:text-emerald-400">
              <Coffee className="w-4 h-4" />
            </div>
            <div className="min-w-0">
              <h2 className="text-sm font-bold text-slate-900 dark:text-neutral-100">Java Runtime 独立管理中心</h2>
              <p className="text-[11px] text-slate-500 dark:text-neutral-400 line-clamp-2">
                由启动器下载的运行时存放于数据根下的 <code className="text-emerald-600 dark:text-emerald-400 font-mono font-bold">runtimes/</code>；外部 Java 可通过「绑定本地 Java」添加。
              </p>
            </div>
          </div>

          <div className="flex items-center gap-2 shrink-0">
            <button
              onClick={() => onOpenFolder('Ninevoxel/javaruntime')}
              className="px-2.5 py-1.5 rounded-lg bg-slate-100 dark:bg-neutral-800 hover:bg-slate-200 dark:hover:bg-neutral-700 text-xs text-slate-700 dark:text-neutral-300 flex items-center gap-1.5 transition-colors cursor-pointer"
            >
              <FolderOpen className="w-3.5 h-3.5 text-slate-400 dark:text-neutral-400" />
              <span>打开运行时目录</span>
            </button>
            <button
              onClick={onClose}
              className="p-1.5 rounded-lg text-slate-400 dark:text-neutral-400 hover:text-slate-800 dark:hover:text-white hover:bg-slate-100 dark:hover:bg-neutral-800 transition-colors cursor-pointer"
            >
              <X className="w-4 h-4" />
            </button>
          </div>
        </div>

        {/* Action strip */}
        <div className="px-6 py-3 border-b border-slate-200 dark:border-neutral-800/80 bg-slate-100/70 dark:bg-neutral-950/40 text-xs">
          <div className="flex flex-wrap items-center justify-between gap-3">
            <span className="text-slate-600 dark:text-neutral-400">
              启动器会自动根据游戏实例选择对应大版本的 <code className="text-slate-900 dark:text-neutral-200 font-bold">bin/java</code>
            </span>

            <div className="flex items-center gap-2 shrink-0">
              <button
                onClick={() => {
                  setJavaPathMessage(null);
                  setBindModalOpen(true);
                }}
                className="px-3.5 py-1.5 rounded-lg bg-slate-200 dark:bg-neutral-800 hover:bg-slate-300 dark:hover:bg-neutral-700 text-slate-700 dark:text-neutral-200 font-semibold text-xs flex items-center gap-1.5 transition-colors cursor-pointer"
              >
                <FolderOpen className="w-3.5 h-3.5" />
                <span>绑定本地 Java</span>
              </button>
              <button
                onClick={() => setDownloadModalOpen(true)}
                className="px-3.5 py-1.5 rounded-lg bg-emerald-500 hover:bg-emerald-400 text-neutral-950 font-bold text-xs flex items-center gap-1.5 transition-all shadow-sm cursor-pointer"
              >
                <Download className="w-3.5 h-3.5 stroke-[2.5]" />
                <span>下载Java运行时</span>
              </button>
            </div>
          </div>
        </div>

        {/* 安装进度：后端复用 download.task.changed.v1 推进度，taskId 形如
            runtime-install-<id>。这里只渲染事件真实携带的 progress 与阶段，
            不编造已下载字节数与速度。 */}
        {installProgress ? (
          <div className="px-6 py-2.5 border-b border-slate-200 dark:border-neutral-800/80 bg-white dark:bg-neutral-900">
            <RuntimeInstallProgressView progress={installProgress} />
          </div>
        ) : null}

        {/* Installed runtimes */}
        <div className="p-6 flex-1 overflow-y-auto space-y-4">
          {groupedMajors.length === 0 ? (
            <div className="p-8 rounded-xl bg-slate-50 dark:bg-neutral-950/60 border border-dashed border-slate-300 dark:border-neutral-700 flex flex-col items-center justify-center gap-3 text-center">
              <Coffee className="w-8 h-8 text-slate-400 dark:text-neutral-500" />
              <div className="space-y-1">
                <div className="text-sm font-semibold text-slate-800 dark:text-neutral-200">还没有可用的 Java 运行时</div>
                <div className="text-[11px] text-slate-500 dark:text-neutral-400">下载一个 LTS 版本，或绑定本机已安装的 Java。</div>
              </div>
              <button
                onClick={() => setDownloadModalOpen(true)}
                className="px-3.5 py-1.5 rounded-lg bg-emerald-500 hover:bg-emerald-400 text-neutral-950 font-bold text-xs flex items-center gap-1.5 transition-all shadow-sm cursor-pointer"
              >
                <Download className="w-3.5 h-3.5" />
                <span>下载Java运行时</span>
              </button>
            </div>
          ) : (
            groupedMajors.map((major) => {
              const groupRuntimes = installedByMajor[major];
              return (
                <div key={major} className="space-y-2">
                  <div className="flex items-center gap-2">
                    <div className="w-8 h-8 rounded-lg bg-emerald-500/15 text-emerald-600 dark:text-emerald-300 border border-emerald-500/30 flex items-center justify-center font-mono font-bold text-xs">
                      J{major}
                    </div>
                    <h3 className="text-sm font-bold text-slate-900 dark:text-neutral-100">Java {major} 运行时</h3>
                    <span className="text-[10px] text-slate-500 dark:text-neutral-500">{groupRuntimes.length} 个</span>
                  </div>

                  {groupRuntimes.map((r) => (
                    <div
                      key={r.id}
                      className="p-3 rounded-lg bg-slate-50 dark:bg-neutral-950/60 border border-slate-200 dark:border-neutral-800 flex items-center justify-between text-xs"
                    >
                      <div className="space-y-0.5 min-w-0">
                        <div className="flex items-center gap-2">
                          <span className="font-semibold text-slate-800 dark:text-neutral-200">{r.vendor}</span>
                          <span className="text-slate-500 dark:text-neutral-500 font-mono text-[11px]">v{r.versionString}</span>
                          <span className="text-[10px] font-mono text-slate-600 dark:text-neutral-400 bg-slate-100 dark:bg-neutral-800 px-1 py-0.5 rounded">
                            {r.architecture}
                          </span>
                          {selectedRuntimeId === r.id && (
                            <span className="text-[10px] font-semibold text-emerald-700 dark:text-emerald-300 bg-emerald-500/15 border border-emerald-500/30 px-1.5 py-0.5 rounded">
                              当前选用
                            </span>
                          )}
                          {r.status === 'error' && (
                            <span className="text-[10px] text-red-600 dark:text-red-400">
                              {r.incompatibleReason ?? '与当前系统不兼容'}
                            </span>
                          )}
                        </div>
                        <div className="text-[11px] text-slate-500 dark:text-neutral-500 font-mono truncate max-w-xl">
                          {r.binaryPath
                            ? `路径: ${r.binaryPath}`
                            : r.isDownloadedByLauncher
                              ? '由启动器安装'
                              : '手动指定'}
                        </div>
                      </div>

                      <div className="flex items-center gap-2 shrink-0">
                        {onSelectRuntime && (
                          <button
                            onClick={() => onSelectRuntime(r.id)}
                            disabled={selectedRuntimeId === r.id}
                            className="px-2.5 py-1 rounded bg-emerald-500/15 hover:bg-emerald-500 text-emerald-700 dark:text-emerald-300 hover:text-white dark:hover:text-neutral-950 font-semibold flex items-center gap-1 transition-colors cursor-pointer disabled:opacity-50 disabled:cursor-not-allowed disabled:hover:bg-emerald-500/15 disabled:hover:text-emerald-700 dark:disabled:hover:text-emerald-300"
                            title={selectedRuntimeId === r.id ? '当前已选用' : '设为本次启动使用的运行时'}
                          >
                            <Check className="w-3 h-3" />
                            <span>{selectedRuntimeId === r.id ? '已选用' : '选用'}</span>
                          </button>
                        )}

                        <button
                          onClick={() => void handleTestJava(r)}
                          disabled={testingId === r.id || !onReload}
                          className="px-2.5 py-1 rounded bg-slate-100 dark:bg-neutral-800 hover:bg-slate-200 dark:hover:bg-neutral-700 text-slate-700 dark:text-neutral-300 hover:text-slate-900 dark:hover:text-white flex items-center gap-1 transition-colors cursor-pointer disabled:opacity-50 disabled:cursor-not-allowed"
                          title="重新探测设置中指定的 Java 路径（后端不提供执行 java -version 的接口）"
                        >
                          {testingId === r.id ? (
                            <RefreshCw className="w-3 h-3 animate-spin text-emerald-500 dark:text-emerald-400" />
                          ) : (
                            <Play className="w-3 h-3 text-emerald-500 dark:text-emerald-400 fill-current" />
                          )}
                          <span>重新探测</span>
                        </button>

                        <button
                          onClick={() => r.folderPath && onOpenFolder(r.folderPath)}
                          disabled={!r.folderPath}
                          className="p-1.5 rounded bg-slate-100 dark:bg-neutral-800 hover:bg-slate-200 dark:hover:bg-neutral-700 text-slate-500 dark:text-neutral-400 hover:text-slate-800 dark:hover:text-white transition-colors cursor-pointer disabled:opacity-40 disabled:cursor-not-allowed"
                          title={r.folderPath ? '打开此版本文件夹' : '未记录此运行的安装目录，无法直接打开'}
                        >
                          <FolderOpen className="w-3.5 h-3.5" />
                        </button>

                        {runtimes.length > 1 && (
                          <button
                            onClick={() => onDeleteRuntime(r.id)}
                            className="p-1.5 rounded bg-slate-100 dark:bg-neutral-800 hover:bg-red-500/15 dark:hover:bg-red-500/20 text-slate-400 dark:text-neutral-500 hover:text-red-600 dark:hover:text-red-400 transition-colors cursor-pointer"
                            title="从本地 javaruntime 移除"
                          >
                            <Trash2 className="w-3.5 h-3.5" />
                          </button>
                        )}
                      </div>
                    </div>
                  ))}

                  {testResult && groupRuntimes.some((r) => r.id === testResult.id) && (
                    <div className="p-2.5 rounded-lg bg-emerald-950/20 dark:bg-neutral-900 border border-emerald-500/30 font-mono text-[10px] text-emerald-700 dark:text-emerald-400 whitespace-pre-wrap leading-tight">
                      {testResult.msg}
                    </div>
                  )}
                </div>
              );
            })
          )}
        </div>

        {/* Modal footer */}
        <div className="px-6 py-4 border-t border-slate-200 dark:border-neutral-800 flex items-center justify-between bg-slate-50 dark:bg-neutral-950/60">
          <div className="text-xs text-slate-500 dark:text-neutral-400 flex items-center gap-1.5">
            <CheckCircle2 className="w-4 h-4 text-emerald-500 dark:text-emerald-400" />
            <span>所有由启动器下载的 JRE 均独立封包于本地，不污染操作系统环境变量</span>
          </div>

          <button
            onClick={onClose}
            className="px-4 py-2 rounded-lg bg-slate-200 dark:bg-neutral-800 hover:bg-slate-300 dark:hover:bg-neutral-700 text-xs font-semibold text-slate-800 dark:text-neutral-200 transition-colors cursor-pointer"
          >
            完成
          </button>
        </div>
      </div>

      {/* Sub-modal: Bind external Java */}
      {bindModalOpen && (
        <div className="fixed inset-0 z-60 flex items-center justify-center p-4 bg-black/60 dark:bg-black/80 backdrop-blur-sm animate-in fade-in duration-150">
          <div className="w-full max-w-md bg-white dark:bg-neutral-900 border border-slate-200 dark:border-neutral-700 rounded-2xl shadow-2xl p-6 space-y-4">
            <div className="flex items-center justify-between pb-3 border-b border-slate-200 dark:border-neutral-800">
              <h3 className="text-sm font-bold text-slate-900 dark:text-neutral-100 flex items-center gap-2">
                <FolderOpen className="w-4 h-4 text-emerald-500 dark:text-emerald-400" />
                <span>绑定本地 Java</span>
              </h3>
              <button onClick={() => setBindModalOpen(false)} className="text-slate-400 hover:text-slate-700 dark:text-neutral-400 dark:hover:text-white cursor-pointer">
                <X className="w-4 h-4" />
              </button>
            </div>

            <p className="text-[11px] text-slate-500 dark:text-neutral-400 leading-relaxed">
              外部 Java 不会被自动发现（不读 JAVA_HOME、不扫 PATH）；选择后会校验可执行性并写入设置，只记录位置、不复制文件。
            </p>

            <div>
              <label className="text-xs font-semibold text-slate-700 dark:text-neutral-300 block mb-1.5">
                当前指定路径
              </label>
              <code className="block w-full truncate font-mono text-[11px] px-2 py-1.5 rounded-lg bg-slate-100 dark:bg-neutral-950 border border-slate-200 dark:border-neutral-800 text-slate-700 dark:text-neutral-300">
                {javaPath && javaPath.length > 0 ? javaPath : '未指定（只用启动器安装的运行时）'}
              </code>
            </div>

            {javaPathMessage ? (
              <div
                className={`font-mono text-[11px] leading-relaxed whitespace-pre-wrap ${
                  javaPathMessage.ok
                    ? 'text-emerald-700 dark:text-emerald-400'
                    : 'text-red-600 dark:text-red-400'
                }`}
              >
                {javaPathMessage.text}
              </div>
            ) : null}

            <div className="flex items-center justify-end gap-2 pt-1">
              {javaPath && javaPath.length > 0 ? (
                <button
                  onClick={() => void handleClearJavaPath()}
                  disabled={!onClearJavaPath}
                  className="px-3 py-1.5 rounded-lg text-slate-600 dark:text-neutral-400 hover:text-red-600 dark:hover:text-red-400 hover:bg-red-500/10 font-semibold text-xs transition-colors cursor-pointer disabled:opacity-50 disabled:cursor-not-allowed"
                  title="清除指定路径，回到只使用启动器安装的运行时"
                >
                  清除
                </button>
              ) : null}
              <button
                onClick={() => void handleChooseJavaPath()}
                disabled={choosingPath || !onChooseJavaPath}
                className="px-3.5 py-1.5 rounded-lg bg-emerald-500 hover:bg-emerald-400 text-neutral-950 font-bold text-xs flex items-center gap-1.5 transition-colors cursor-pointer disabled:opacity-50 disabled:cursor-not-allowed"
                title="选择一个 java 可执行文件；选定后会校验可执行性并写入设置"
              >
                <FolderOpen className="w-3.5 h-3.5" />
                <span>{choosingPath ? '选择中…' : '选择 Java…'}</span>
              </button>
              <button
                onClick={() => setBindModalOpen(false)}
                className="px-4 py-1.5 rounded-lg bg-slate-200 dark:bg-neutral-800 hover:bg-slate-300 dark:hover:bg-neutral-700 text-xs font-semibold text-slate-800 dark:text-neutral-200 transition-colors cursor-pointer"
              >
                完成
              </button>
            </div>
          </div>
        </div>
      )}

      {/* Sub-modal: Download specific runtime */}
      {downloadModalOpen && (
        <div className="fixed inset-0 z-60 flex items-center justify-center p-4 bg-black/60 dark:bg-black/80 backdrop-blur-sm animate-in fade-in duration-150">
          <div className="w-full max-w-md bg-white dark:bg-neutral-900 border border-slate-200 dark:border-neutral-700 rounded-2xl shadow-2xl p-6 space-y-4">
            <div className="flex items-center justify-between pb-3 border-b border-slate-200 dark:border-neutral-800">
              <h3 className="text-sm font-bold text-slate-900 dark:text-neutral-100 flex items-center gap-2">
                <Download className="w-4 h-4 text-emerald-500 dark:text-emerald-400" />
                <span>一键下载本地 Java 运行时</span>
              </h3>
              <button onClick={() => setDownloadModalOpen(false)} className="text-slate-400 hover:text-slate-700 dark:text-neutral-400 dark:hover:text-white cursor-pointer">
                <X className="w-4 h-4" />
              </button>
            </div>

            {installProgress ? (
              <div className="p-3 rounded-xl bg-slate-50 dark:bg-neutral-950 border border-slate-200 dark:border-neutral-800">
                <RuntimeInstallProgressView progress={installProgress} />
              </div>
            ) : null}

            <div className="space-y-3">
              <div>
                <label className="text-xs font-semibold text-slate-700 dark:text-neutral-300 block mb-1.5">
                  发行版供应商 (Vendor)
                </label>
                <select
                  value={selectedVendor}
                  onChange={(e) => {
                    setSelectedVendor(e.target.value);
                    setLicenseAccepted(false);
                  }}
                  className="w-full px-3 py-2 bg-slate-50 dark:bg-neutral-950 border border-slate-300 dark:border-neutral-800 rounded-lg text-xs text-slate-800 dark:text-neutral-200 focus:outline-none focus:border-emerald-500"
                >
                  {vendorOptions.map((vendor) => (
                    <option key={vendor.id} value={vendor.id}>
                      {vendor.label}
                    </option>
                  ))}
                </select>
                <p className="mt-1 text-[10px] text-slate-500 dark:text-neutral-500">
                  {selectedVendor === 'zulu'
                    ? 'LTS 列表与下载地址来自 Azul Metadata API。'
                    : 'LTS 列表与下载地址来自 Adoptium API。'}
                </p>
              </div>

              <div>
                <label className="text-xs font-semibold text-slate-700 dark:text-neutral-300 block mb-1.5">
                  Java LTS 版本
                </label>
                {ltsLoading ? (
                  <div className="flex items-center gap-2 px-3 py-2.5 rounded-lg bg-slate-100 dark:bg-neutral-950 border border-slate-200 dark:border-neutral-800 text-[11px] text-slate-500 dark:text-neutral-400">
                    <RefreshCw className="w-3.5 h-3.5 animate-spin text-emerald-500 dark:text-emerald-400" />
                    <span>正在从 {activeVendor.label} 获取 LTS 版本…</span>
                  </div>
                ) : ltsError ? (
                  <div className="p-3 rounded-lg bg-red-500/10 border border-red-500/30 text-[11px] text-red-600 dark:text-red-400 space-y-2">
                    <div>获取 LTS 版本失败：{ltsError}</div>
                    <button
                      type="button"
                      onClick={() => setLtsReloadToken((value) => value + 1)}
                      className="px-2.5 py-1 rounded bg-red-500/15 hover:bg-red-500/25 font-semibold transition-colors cursor-pointer"
                    >
                      重试
                    </button>
                  </div>
                ) : (
                  <>
                    <select
                      value={selectedMajor}
                      onChange={(e) => setSelectedMajor(Number(e.target.value))}
                      disabled={ltsVersions.length === 0}
                      className="w-full px-3 py-2 bg-slate-50 dark:bg-neutral-950 border border-slate-300 dark:border-neutral-800 rounded-lg text-xs text-slate-800 dark:text-neutral-200 focus:outline-none focus:border-emerald-500 disabled:opacity-50 disabled:cursor-not-allowed"
                    >
                      {ltsVersions.map((version) => (
                        <option key={version} value={version}>
                          Java {version}
                          {version === mostRecentLts ? '（最新 LTS）' : ''}
                          {runtimes.some((runtime) => runtime.majorVersion === version) ? '（已安装）' : ''}
                        </option>
                      ))}
                    </select>
                    {ltsVersions.length === 0 && (
                      <p className="mt-1 text-[10px] text-slate-500 dark:text-neutral-400">
                        {activeVendor.label} 当前没有返回 LTS 版本。
                      </p>
                    )}
                    {ltsVersions.length > 0 && (
                      <p className="mt-1 text-[10px] text-slate-500 dark:text-neutral-500">
                        {runtimes.some((runtime) => runtime.majorVersion === selectedMajor)
                          ? '该版本已安装，下载会按来源侧最新构建更新。'
                          : '该版本尚未安装。'}
                      </p>
                    )}
                  </>
                )}
              </div>

              <div className="p-3 rounded-xl bg-slate-100 dark:bg-neutral-950 border border-slate-200 dark:border-neutral-800 text-[11px] text-slate-600 dark:text-neutral-400 space-y-1">
                <div>保存位置：后端数据根下的运行时目录（由后端分配，界面不拼路径）</div>
                <div>校验：元数据里的 SHA-256 必须匹配，不匹配不落盘。</div>
                <div>
                  镜像：
                  {selectedVendor === 'adoptium'
                    ? '开启镜像后优先走国内镜像，失败回落官方。'
                    : 'Zulu 暂不接入镜像，直接走官方 CDN。'}
                </div>
              </div>

              <label className="flex items-start gap-2 text-[11px] text-slate-600 dark:text-neutral-400 cursor-pointer">
                <input
                  type="checkbox"
                  checked={licenseAccepted}
                  onChange={(e) => setLicenseAccepted(e.target.checked)}
                  className="mt-0.5 w-3.5 h-3.5 accent-emerald-500 cursor-pointer"
                />
                <span>
                  我已阅读并同意该发行版的许可条款（{activeVendor.label}，{activeVendor.license}）。
                  未勾选时后端会拒绝安装。
                </span>
              </label>

              <button
                onClick={handleTriggerDownload}
                disabled={installing || !licenseAccepted || ltsLoading || Boolean(ltsError) || ltsVersions.length === 0}
                className="w-full py-2.5 rounded-xl bg-emerald-500 hover:bg-emerald-400 text-neutral-950 font-bold text-xs shadow-md transition-all cursor-pointer disabled:opacity-50 disabled:cursor-not-allowed disabled:hover:bg-emerald-500"
              >
                {installing ? '正在安装…' : `立即开始下载 ${activeVendor.label} Java ${selectedMajor}`}
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
};
