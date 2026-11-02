import React, { useState } from 'react';
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

interface JavaRuntimeManagerModalProps {
  isOpen: boolean;
  onClose: () => void;
  runtimes: JavaRuntime[];
  onDownloadRuntime: (majorVersion: 8 | 17 | 21 | 25, vendor: string) => void;
  onDeleteRuntime: (id: string) => void;
  onOpenFolder: (path: string) => void;
  /** 当前选中的运行时标识（来自后端 java_runtimes.selected）。 */
  selectedRuntimeId?: string;
  /** 选择某个运行时；不传则渲染为只读列表。 */
  onSelectRuntime?: (runtimeId: string) => void;
  /** 重新探测本机 Java；不传则「环境测试」按钮不可用。 */
  onReload?: () => Promise<void>;
}

export const JavaRuntimeManagerModal: React.FC<JavaRuntimeManagerModalProps> = ({
  isOpen,
  onClose,
  runtimes,
  onDownloadRuntime,
  onDeleteRuntime,
  onOpenFolder,
  selectedRuntimeId,
  onSelectRuntime,
  onReload
}) => {
  const [testingId, setTestingId] = useState<string | null>(null);
  const [testResult, setTestResult] = useState<{ id: string; success: boolean; msg: string } | null>(null);
  const [downloadModalOpen, setDownloadModalOpen] = useState(false);
  const [selectedMajor, setSelectedMajor] = useState<8 | 17 | 21 | 25>(21);
  const [selectedVendor, setSelectedVendor] = useState('Eclipse Temurin (Adoptium)');
  // 许可必须由用户显式勾选：后端在 acceptLicense 为 false 时直接拒绝安装。
  const [licenseAccepted, setLicenseAccepted] = useState(false);

  const majorVersions: { version: 8 | 17 | 21 | 25; mcMatch: string; desc: string }[] = [
    { version: 21, mcMatch: 'MC 1.20.5 ~ 1.21.4+', desc: '现代 Minecraft 正式版强制要求，提供最先进的 ZGC 垃圾回收' },
    { version: 17, mcMatch: 'MC 1.17 ~ 1.20.4', desc: '过渡期主流版本必备运行时，兼顾稳定与模组生态' },
    { version: 8, mcMatch: 'MC 1.7.10 ~ 1.16.5', desc: '怀旧黄金时代模组包（如 1.7.10/1.12.2）唯一兼容环境' },
    { version: 25, mcMatch: '未来快照 / 实验分支', desc: 'Java 25 Early Access，探索前沿高性能 JIT 编译' }
  ];

  // 本机真实装的可能不在这四档里（例如 Java 11、16），必须另起一组单独展示，
  // 否则探测到了却看不见。
  const extraRuntimes = runtimes.filter(
    (r) => !majorVersions.some((item) => item.version === r.majorVersion)
  );

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

  const handleTriggerDownload = () => {
    // 未勾选许可时按钮本就禁用，这里再守一道，避免以任何方式绕过。
    if (!licenseAccepted) return;
    onDownloadRuntime(selectedMajor, selectedVendor);
    setDownloadModalOpen(false);
    setLicenseAccepted(false);
  };

  // 提前返回必须放在所有 hooks 之后：否则弹窗开合会改变 hook 调用数量，
  // React 会抛出「Rendered more hooks than during the previous render」。
  if (!isOpen) return null;

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center p-4 bg-black/60 dark:bg-black/75 backdrop-blur-sm animate-in fade-in duration-200">
      <div className="w-full max-w-3xl bg-white dark:bg-neutral-900 border border-slate-200 dark:border-neutral-700/80 rounded-2xl shadow-2xl flex flex-col max-h-[85vh] overflow-hidden">
        {/* Header */}
        <div className="px-6 py-4 border-b border-slate-200 dark:border-neutral-800 flex items-center justify-between bg-slate-50 dark:bg-neutral-950/60">
          <div className="flex items-center gap-2.5">
            <div className="w-8 h-8 rounded-lg bg-emerald-500/20 border border-emerald-500/30 flex items-center justify-center text-emerald-600 dark:text-emerald-400">
              <Coffee className="w-4 h-4" />
            </div>
            <div>
              <h2 className="text-sm font-bold text-slate-900 dark:text-neutral-100">Java Runtime 独立管理中心</h2>
              <p className="text-[11px] text-slate-500 dark:text-neutral-400">
                本机已安装的 Java 只记录位置、不复制文件；由启动器下载的运行时放在数据根下的 <code className="text-emerald-600 dark:text-emerald-400 font-mono font-bold">runtimes/</code>
              </p>
            </div>
          </div>

          <div className="flex items-center gap-2">
            <button
              onClick={() => onOpenFolder('Ninevoxel/javaruntime')}
              className="px-2.5 py-1.5 rounded-lg bg-slate-100 dark:bg-neutral-800 hover:bg-slate-200 dark:hover:bg-neutral-700 text-xs text-slate-700 dark:text-neutral-300 flex items-center gap-1.5 transition-colors cursor-pointer"
            >
              <FolderOpen className="w-3.5 h-3.5 text-slate-400 dark:text-neutral-400" />
              <span>打开本地 javaruntime 目录</span>
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
        <div className="px-6 py-3 border-b border-slate-200 dark:border-neutral-800/80 bg-slate-100/70 dark:bg-neutral-950/40 flex items-center justify-between text-xs">
          <span className="text-slate-600 dark:text-neutral-400">
            启动器会自动根据游戏实例选择对应大版本的 <code className="text-slate-900 dark:text-neutral-200 font-bold">bin/java</code>
          </span>

          <button
            onClick={() => setDownloadModalOpen(true)}
            className="px-3.5 py-1.5 rounded-lg bg-emerald-500 hover:bg-emerald-400 text-neutral-950 font-bold text-xs flex items-center gap-1.5 transition-all shadow-sm cursor-pointer"
          >
            <Download className="w-3.5 h-3.5 stroke-[2.5]" />
            <span>下载缺失的 Java 运行时</span>
          </button>
        </div>

        {/* Runtime Major Versions List */}
        <div className="p-6 flex-1 overflow-y-auto space-y-4">
          {majorVersions.map((item) => {
            const installedRuntimes = runtimes.filter(r => r.majorVersion === item.version);
            const isInstalled = installedRuntimes.length > 0;

            return (
              <div
                key={item.version}
                className="p-4 rounded-xl bg-slate-50 dark:bg-neutral-950/60 border border-slate-200 dark:border-neutral-800 space-y-3"
              >
                <div className="flex items-start justify-between gap-3">
                  <div className="flex items-center gap-3">
                    <div className={`w-10 h-10 rounded-xl flex items-center justify-center font-mono font-bold text-sm ${
                      isInstalled
                        ? 'bg-emerald-500/15 text-emerald-600 dark:text-emerald-300 border border-emerald-500/30'
                        : 'bg-slate-200 dark:bg-neutral-800 text-slate-500 dark:text-neutral-500 border border-slate-300 dark:border-neutral-700'
                    }`}>
                      J{item.version}
                    </div>

                    <div>
                      <div className="flex items-center gap-2">
                        <h3 className="text-sm font-bold text-slate-900 dark:text-neutral-100">Java {item.version} 运行时</h3>
                        <span className="text-[11px] font-mono text-slate-600 dark:text-neutral-400 bg-slate-200 dark:bg-neutral-800 px-2 py-0.5 rounded">
                          {item.mcMatch}
                        </span>
                        {isInstalled && (
                          <span className="text-[10px] text-emerald-600 dark:text-emerald-400 font-semibold bg-emerald-500/10 border border-emerald-500/20 px-1.5 py-0.5 rounded">
                            已就绪
                          </span>
                        )}
                      </div>
                      <p className="text-[11px] text-slate-500 dark:text-neutral-400 mt-0.5">{item.desc}</p>
                    </div>
                  </div>

                  {!isInstalled && (
                    <button
                      onClick={() => {
                        setSelectedMajor(item.version);
                        setDownloadModalOpen(true);
                      }}
                      className="px-3 py-1.5 rounded-lg bg-emerald-500/15 hover:bg-emerald-500 text-emerald-700 dark:text-emerald-300 hover:text-white dark:hover:text-neutral-950 font-semibold text-xs flex items-center gap-1.5 transition-all cursor-pointer"
                    >
                      <Download className="w-3.5 h-3.5" />
                      <span>安装 J{item.version}</span>
                    </button>
                  )}
                </div>

                {/* Sub items: Installed instances in javaruntime/java-X/ */}
                {installedRuntimes.map((r) => (
                  <div
                    key={r.id}
                    className="p-3 rounded-lg bg-white dark:bg-neutral-900 border border-slate-200 dark:border-neutral-800 flex items-center justify-between text-xs"
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
                            ? '由启动器安装（后端不下发具体路径）'
                            : '本机已安装（后端不下发具体路径）'}
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
                        title="重新探测本机 Java（后端不提供执行 java -version 的接口）"
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
                        title={r.folderPath ? '打开此版本文件夹' : '后端不下发运行时路径，无法直接打开'}
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

                {testResult && installedRuntimes.some(r => r.id === testResult.id) && (
                  <div className="p-2.5 rounded-lg bg-emerald-950/20 dark:bg-neutral-900 border border-emerald-500/30 font-mono text-[10px] text-emerald-700 dark:text-emerald-400 whitespace-pre-wrap leading-tight">
                    {testResult.msg}
                  </div>
                )}
              </div>
            );
          })}

          {/* 本机真实装但不在上述四档内的版本（如 Java 11、16）单独列出，
              否则探测到了却看不见。 */}
          {extraRuntimes.length > 0 && (
            <div className="p-4 rounded-xl bg-slate-50 dark:bg-neutral-950/40 border border-slate-200 dark:border-neutral-800 space-y-2">
              <div className="flex items-center justify-between">
                <div className="text-xs font-semibold text-slate-800 dark:text-neutral-200">
                  其他已检测到的版本
                </div>
                <span className="text-[10px] text-slate-500 dark:text-neutral-500">
                  不在 Java 8 / 17 / 21 / 25 四档内
                </span>
              </div>
              {extraRuntimes.map((r) => (
                <div
                  key={r.id}
                  className="p-3 rounded-lg bg-white dark:bg-neutral-900 border border-slate-200 dark:border-neutral-800 flex items-center justify-between text-xs"
                >
                  <div className="space-y-0.5 min-w-0">
                    <div className="flex items-center gap-2">
                      <span className="font-semibold text-slate-800 dark:text-neutral-200">{r.vendor}</span>
                      <span className="text-slate-500 dark:text-neutral-500 font-mono text-[11px]">
                        Java {r.majorVersion} · v{r.versionString}
                      </span>
                      <span className="text-[10px] font-mono text-slate-600 dark:text-neutral-400 bg-slate-100 dark:bg-neutral-800 px-1 py-0.5 rounded">
                        {r.architecture}
                      </span>
                    </div>
                    <div className="text-[11px] text-slate-500 dark:text-neutral-500 font-mono truncate max-w-xl">
                      {r.isDownloadedByLauncher ? '由启动器安装（后端不下发具体路径）' : '本机已安装（后端不下发具体路径）'}
                    </div>
                  </div>
                  {onSelectRuntime && (
                    <button
                      onClick={() => onSelectRuntime(r.id)}
                      disabled={selectedRuntimeId === r.id}
                      className="px-2.5 py-1 rounded bg-emerald-500/15 hover:bg-emerald-500 text-emerald-700 dark:text-emerald-300 hover:text-white dark:hover:text-neutral-950 font-semibold flex items-center gap-1 transition-colors cursor-pointer disabled:opacity-50 disabled:cursor-not-allowed"
                    >
                      <Check className="w-3 h-3" />
                      <span>{selectedRuntimeId === r.id ? '已选用' : '选用'}</span>
                    </button>
                  )}
                </div>
              ))}
            </div>
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

            <div className="space-y-3">
              <div>
                <label className="text-xs font-semibold text-slate-700 dark:text-neutral-300 block mb-1.5">
                  目标大版本分类
                </label>
                <div className="grid grid-cols-4 gap-2">
                  {[8, 17, 21, 25].map((v) => (
                    <button
                      key={v}
                      type="button"
                      onClick={() => setSelectedMajor(v as any)}
                      className={`py-2 rounded-lg text-xs font-mono font-bold transition-all cursor-pointer ${
                        selectedMajor === v
                          ? 'bg-emerald-500/20 border border-emerald-500 dark:border-emerald-400 text-emerald-700 dark:text-emerald-300'
                          : 'bg-slate-100 dark:bg-neutral-950 border border-slate-200 dark:border-neutral-800 text-slate-600 dark:text-neutral-400 hover:text-slate-900 dark:hover:text-white'
                      }`}
                    >
                      Java {v}
                    </button>
                  ))}
                </div>
              </div>

              <div>
                <label className="text-xs font-semibold text-slate-700 dark:text-neutral-300 block mb-1.5">
                  发行版供应商 (Vendor)
                </label>
                <select
                  value={selectedVendor}
                  onChange={(e) => setSelectedVendor(e.target.value)}
                  className="w-full px-3 py-2 bg-slate-50 dark:bg-neutral-950 border border-slate-300 dark:border-neutral-800 rounded-lg text-xs text-slate-800 dark:text-neutral-200 focus:outline-none focus:border-emerald-500"
                >
                  <option value="Eclipse Temurin (Adoptium)">Eclipse Temurin (Adoptium，当前唯一已接入来源)</option>
                </select>
                <p className="mt-1 text-[10px] text-slate-500 dark:text-neutral-500">
                  其他发行版尚未接入：接入需要各自的版本检索接口与校验文件，不能凭空拼下载地址。
                </p>
              </div>

              <div className="p-3 rounded-xl bg-slate-100 dark:bg-neutral-950 border border-slate-200 dark:border-neutral-800 text-[11px] text-slate-600 dark:text-neutral-400 space-y-1">
                <div>保存位置：后端数据根下的运行时目录（由后端分配，界面不拼路径）</div>
                <div>校验：元数据里的 SHA-256 必须匹配，不匹配不落盘。</div>
                <div>镜像：地址取自元数据，开启镜像后优先走国内镜像，失败回落官方。</div>
              </div>

              <label className="flex items-start gap-2 text-[11px] text-slate-600 dark:text-neutral-400 cursor-pointer">
                <input
                  type="checkbox"
                  checked={licenseAccepted}
                  onChange={(e) => setLicenseAccepted(e.target.checked)}
                  className="mt-0.5 w-3.5 h-3.5 accent-emerald-500 cursor-pointer"
                />
                <span>
                  我已阅读并同意该发行版的许可条款（Adoptium / Eclipse Temurin，GPLv2 + Classpath Exception）。
                  未勾选时后端会拒绝安装。
                </span>
              </label>

              <button
                onClick={handleTriggerDownload}
                disabled={!licenseAccepted}
                className="w-full py-2.5 rounded-xl bg-emerald-500 hover:bg-emerald-400 text-neutral-950 font-bold text-xs shadow-md transition-all cursor-pointer disabled:opacity-50 disabled:cursor-not-allowed disabled:hover:bg-emerald-500"
              >
                立即开始下载 Java {selectedMajor}
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
};
