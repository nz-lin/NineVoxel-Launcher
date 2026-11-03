import React from 'react';
import { AlertTriangle, X } from 'lucide-react';

interface ConfirmDialogProps {
  title: string;
  message: string;
  confirmLabel?: string;
  cancelLabel?: string;
  danger?: boolean;
  onConfirm: () => void;
  onCancel: () => void;
}

/** 应用内确认框，避免依赖 WebView 可能不支持的 window.confirm。 */
export const ConfirmDialog: React.FC<ConfirmDialogProps> = ({
  title,
  message,
  confirmLabel = '确认',
  cancelLabel = '取消',
  danger = false,
  onConfirm,
  onCancel,
}) => {
  return (
    <div className="fixed inset-0 z-[70] flex items-center justify-center p-4 bg-black/60 dark:bg-black/75 backdrop-blur-sm">
      <div className="w-full max-w-sm bg-white dark:bg-neutral-900 border border-slate-200 dark:border-neutral-700 rounded-2xl shadow-2xl overflow-hidden">
        <div className="px-5 py-4 flex items-start justify-between gap-3 border-b border-slate-200 dark:border-neutral-800">
          <div className="flex items-start gap-3">
            <div className={`w-8 h-8 rounded-lg flex items-center justify-center shrink-0 ${
              danger
                ? 'bg-red-500/15 text-red-600 dark:text-red-400'
                : 'bg-emerald-500/15 text-emerald-600 dark:text-emerald-400'
            }`}>
              <AlertTriangle className="w-4 h-4" />
            </div>
            <div>
              <h3 className="text-sm font-bold text-slate-900 dark:text-neutral-100">{title}</h3>
              <p className="mt-1 text-xs text-slate-500 dark:text-neutral-400 leading-relaxed">{message}</p>
            </div>
          </div>
          <button
            type="button"
            onClick={onCancel}
            aria-label="关闭"
            className="p-1 rounded text-slate-400 hover:text-slate-800 dark:hover:text-white cursor-pointer"
          >
            <X className="w-4 h-4" />
          </button>
        </div>
        <div className="px-5 py-3 flex justify-end gap-2 bg-slate-50 dark:bg-neutral-950/60">
          <button
            type="button"
            onClick={onCancel}
            className="px-3.5 py-1.5 rounded-lg text-xs font-semibold text-slate-700 dark:text-neutral-300 hover:bg-slate-200 dark:hover:bg-neutral-800 transition-colors cursor-pointer"
          >
            {cancelLabel}
          </button>
          <button
            type="button"
            onClick={onConfirm}
            className={`px-3.5 py-1.5 rounded-lg text-xs font-bold transition-colors cursor-pointer ${
              danger
                ? 'bg-red-500 hover:bg-red-400 text-white'
                : 'bg-emerald-500 hover:bg-emerald-400 text-neutral-950'
            }`}
          >
            {confirmLabel}
          </button>
        </div>
      </div>
    </div>
  );
};
