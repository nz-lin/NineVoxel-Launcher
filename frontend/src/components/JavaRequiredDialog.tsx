import React, { useEffect, useState } from 'react';
import { Coffee, Download, FolderOpen, AlertTriangle, X, CheckCircle2, AlertCircle } from 'lucide-react';
import type { RuntimeInstallProgress } from '../types/runtime';

/**
 * 启动前「缺少 Java」对话框。
 *
 * 为什么需要它：启动失败的原始报错只有一句「未找到可用的 Java」，用户既不知道
 * 该装哪个大版本，也不知道装完要不要重新选路径。这里把版本要求直接摆出来，
 * 并给出两条即时可用的出路：下载对应大版本，或指定本机已有的 Java。
 *
 * 下载必须经过用户显式勾选许可：后端在 acceptLicense 为 false 时直接拒绝，
 * 界面不得硬编码为 true 绕过确认。
 */
export interface JavaRequiredDialogProps {
  /** 是否显示；false 时不渲染任何内容。 */
  isOpen: boolean;
  /** 实例名，让用户确认是哪一次启动被拦下。 */
  instanceName: string;
  /** 版本声明的 Java 大版本；0 表示元数据未声明。 */
  requiredMajor: number;
  /** 后端返回的补充说明（例如「实例指定的 Java 运行时不可用」）。 */
  reason?: string;
  /** 正在进行的下载进度；来自 useJavaRuntimes。 */
  installProgress: RuntimeInstallProgress | null;
  /** 请求下载指定大版本（已由调用方确认许可）。 */
  onDownload: (major: number) => void;
  /** 选择本机 Java 可执行文件；成功后由调用方重新尝试启动。 */
  onChooseLocal: () => Promise<{ ok: boolean; message: string }>;
  /** 打开 Java 运行时管理器（大版本未知时的兜底入口）。 */
  onOpenManager: () => void;
  /** 关闭对话框，本次启动不再重试。 */
  onClose: () => void;
}

/** 下载进度视图：只展示后端事件真实携带的阶段与比例。 */
const InstallProgressView: React.FC<{ progress: RuntimeInstallProgress }> = ({ progress }) => {
  if (progress.phase === 'failed') {
    return (
      <div className="flex items-center gap-1.5 text-[11px] text-rose-600 dark:text-rose-400">
        <AlertCircle className="w-3.5 h-3.5 shrink-0" />
        <span>下载失败{progress.errorCode ? `（${progress.errorCode}）` : ''}，可重试或改用本机 Java。</span>
      </div>
    );
  }
  return (
    <div className="space-y-1.5">
      <div className="flex items-center justify-between text-[11px]">
        <span className="text-slate-600 dark:text-neutral-300">
          {progress.phase === 'completed'
            ? '安装完成，正在重新启动…'
            : progress.phase === 'committing'
              ? '正在校验并解压…'
              : '正在下载 Java 运行时…'}
        </span>
        <span className="font-mono text-emerald-600 dark:text-emerald-400">
          {Math.round(progress.progress * 100)}%
        </span>
      </div>
      <div className="h-1.5 w-full rounded-full bg-slate-200 dark:bg-neutral-800 overflow-hidden">
        <div
          className="h-full rounded-full bg-emerald-500 transition-all duration-300"
          style={{ width: `${Math.min(100, Math.max(2, Math.round(progress.progress * 100)))}%` }}
        />
      </div>
    </div>
  );
};

export const JavaRequiredDialog: React.FC<JavaRequiredDialogProps> = ({
  isOpen,
  instanceName,
  requiredMajor,
  reason,
  installProgress,
  onDownload,
  onChooseLocal,
  onOpenManager,
  onClose,
}) => {
  const [licenseAccepted, setLicenseAccepted] = useState(false);
  const [choosing, setChoosing] = useState(false);
  const [localMessage, setLocalMessage] = useState<{ ok: boolean; text: string } | null>(null);

  // 每次重新弹出都回到「未勾选」：许可确认必须针对本次操作，
  // 沿用上一次的勾选等于替用户做了决定。
  useEffect(() => {
    if (isOpen) {
      setLicenseAccepted(false);
      setLocalMessage(null);
      setChoosing(false);
    }
  }, [isOpen, requiredMajor]);

  if (!isOpen) {
    return null;
  }

  const downloading = installProgress !== null && installProgress.phase !== 'failed' && installProgress.phase !== 'completed';
  const canDownload = requiredMajor > 0 && licenseAccepted && !downloading;

  const handleChooseLocal = () => {
    if (choosing) return;
    setChoosing(true);
    setLocalMessage(null);
    void onChooseLocal()
      .then((result) => {
        // 成功时调用方会关闭对话框并重启游戏，这里只在失败时留提示。
        if (!result.ok) setLocalMessage({ ok: false, text: result.message });
      })
      .catch((cause: unknown) => {
        setLocalMessage({ ok: false, text: cause instanceof Error ? cause.message : String(cause) });
      })
      .finally(() => setChoosing(false));
  };

  return (
    <div className="fixed inset-0 z-[75] flex items-center justify-center p-4 bg-black/60 dark:bg-black/75 backdrop-blur-sm">
      <div className="w-full max-w-md bg-white dark:bg-neutral-900 border border-slate-200 dark:border-neutral-700 rounded-2xl shadow-2xl overflow-hidden">
        <div className="px-5 py-4 flex items-start justify-between gap-3 border-b border-slate-200 dark:border-neutral-800">
          <div className="flex items-start gap-3">
            <div className="w-8 h-8 rounded-lg bg-amber-500/15 text-amber-600 dark:text-amber-400 flex items-center justify-center shrink-0">
              <Coffee className="w-4 h-4" />
            </div>
            <div>
              <h3 className="text-sm font-bold text-slate-900 dark:text-neutral-100">
                {requiredMajor > 0 ? `缺少 Java ${requiredMajor}` : '找不到可用的 Java'}
              </h3>
              <p className="mt-1 text-xs text-slate-500 dark:text-neutral-400 leading-relaxed">
                {requiredMajor > 0
                  ? `「${instanceName}」需要 Java ${requiredMajor}，但本机没有找到可用运行时。`
                  : `「${instanceName}」需要一个 Java 运行时，但本机没有找到。`}
              </p>
            </div>
          </div>
          <button
            type="button"
            onClick={onClose}
            className="p-1.5 rounded-lg text-slate-400 dark:text-neutral-400 hover:text-slate-800 dark:hover:text-white hover:bg-slate-100 dark:hover:bg-neutral-800 transition-colors cursor-pointer"
            aria-label="关闭"
          >
            <X className="w-4 h-4" />
          </button>
        </div>

        <div className="px-5 py-4 space-y-4">
          {reason && (
            <div className="flex items-start gap-2 p-3 rounded-lg bg-amber-500/10 border border-amber-500/30 text-[11px] text-amber-700 dark:text-amber-300">
              <AlertTriangle className="w-3.5 h-3.5 shrink-0 mt-0.5" />
              <span>{reason}</span>
            </div>
          )}

          {requiredMajor > 0 && (
            <div className="space-y-2.5">
              <div className="flex items-center justify-between">
                <span className="text-xs font-semibold text-slate-700 dark:text-neutral-300">
                  下载 Eclipse Temurin Java {requiredMajor}
                </span>
                <Download className="w-4 h-4 text-slate-400" />
              </div>
              <label className="flex items-start gap-2 text-[11px] text-slate-600 dark:text-neutral-400 cursor-pointer">
                <input
                  type="checkbox"
                  checked={licenseAccepted}
                  onChange={(event) => setLicenseAccepted(event.target.checked)}
                  disabled={downloading}
                  className="mt-0.5 accent-emerald-500"
                />
                <span>我已阅读并接受 Eclipse Temurin（Adoptium）的许可条款；下载源为官方发布渠道。</span>
              </label>
              <button
                type="button"
                onClick={() => onDownload(requiredMajor)}
                disabled={!canDownload}
                className="w-full px-3 py-2 rounded-lg bg-emerald-600 hover:bg-emerald-500 disabled:opacity-50 disabled:cursor-not-allowed text-white text-xs font-semibold transition-colors cursor-pointer"
              >
                {downloading ? '正在下载…' : `下载并安装 Java ${requiredMajor}`}
              </button>
              {installProgress && <InstallProgressView progress={installProgress} />}
            </div>
          )}

          <div className="flex items-center gap-2 text-[11px] text-slate-400 dark:text-neutral-600">
            <div className="flex-1 h-px bg-slate-200 dark:bg-neutral-800" />
            <span>或者</span>
            <div className="flex-1 h-px bg-slate-200 dark:bg-neutral-800" />
          </div>

          <div className="space-y-2">
            <button
              type="button"
              onClick={handleChooseLocal}
              disabled={choosing || downloading}
              className="w-full px-3 py-2 rounded-lg bg-slate-100 hover:bg-slate-200 dark:bg-neutral-800 dark:hover:bg-neutral-700 disabled:opacity-50 disabled:cursor-not-allowed text-slate-700 dark:text-neutral-200 text-xs font-semibold transition-colors cursor-pointer flex items-center justify-center gap-2"
            >
              <FolderOpen className="w-3.5 h-3.5" />
              {choosing ? '正在校验…' : '指定本机已有的 Java'}
            </button>
            {requiredMajor === 0 && (
              <button
                type="button"
                onClick={onOpenManager}
                disabled={downloading}
                className="w-full px-3 py-2 rounded-lg bg-slate-100 hover:bg-slate-200 dark:bg-neutral-800 dark:hover:bg-neutral-700 disabled:opacity-50 text-slate-700 dark:text-neutral-200 text-xs transition-colors cursor-pointer"
              >
                打开 Java 运行时管理器
              </button>
            )}
            {localMessage && (
              <p className={`text-[11px] ${localMessage.ok ? 'text-emerald-600 dark:text-emerald-400' : 'text-rose-600 dark:text-rose-400'}`}>
                {localMessage.ok ? <CheckCircle2 className="w-3.5 h-3.5 inline mr-1" /> : null}
                {localMessage.text}
              </p>
            )}
          </div>
        </div>
      </div>
    </div>
  );
};
