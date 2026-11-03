import React, { useEffect, useRef, useState } from 'react';
import { Check, ChevronDown } from 'lucide-react';

export interface AppSelectOption {
  value: string;
  label: string;
  hint?: string;
  disabled?: boolean;
}

interface AppSelectProps {
  value: string;
  options: AppSelectOption[];
  onChange: (value: string) => void;
  placeholder?: string;
  disabled?: boolean;
  ariaLabel?: string;
  className?: string;
}

/**
 * 应用内下拉选择器。
 *
 * 不使用原生 <select>，避免 macOS 上出现与整体界面风格不一致的系统弹层。
 */
export const AppSelect: React.FC<AppSelectProps> = ({
  value,
  options,
  onChange,
  placeholder = '请选择',
  disabled = false,
  ariaLabel,
  className = ''
}) => {
  const [open, setOpen] = useState(false);
  const rootRef = useRef<HTMLDivElement>(null);
  const selected = options.find((option) => option.value === value);

  useEffect(() => {
    if (!open) return;
    const handlePointerDown = (event: MouseEvent) => {
      if (rootRef.current && !rootRef.current.contains(event.target as Node)) {
        setOpen(false);
      }
    };
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') setOpen(false);
    };
    document.addEventListener('mousedown', handlePointerDown);
    document.addEventListener('keydown', handleKeyDown);
    return () => {
      document.removeEventListener('mousedown', handlePointerDown);
      document.removeEventListener('keydown', handleKeyDown);
    };
  }, [open]);

  return (
    <div ref={rootRef} className={`relative ${className}`}>
      <button
        type="button"
        disabled={disabled}
        aria-label={ariaLabel}
        aria-haspopup="listbox"
        aria-expanded={open}
        onClick={() => setOpen((current) => !current)}
        className="w-full px-3 py-2 rounded-lg bg-slate-50 dark:bg-neutral-950 border border-slate-300 dark:border-neutral-800 text-sm text-slate-800 dark:text-neutral-200 flex items-center justify-between gap-2 focus:outline-none focus:border-emerald-500 disabled:opacity-50 disabled:cursor-not-allowed cursor-pointer"
      >
        <span className={`truncate ${selected ? '' : 'text-slate-400 dark:text-neutral-500'}`}>
          {selected?.label ?? placeholder}
        </span>
        <ChevronDown className={`w-4 h-4 shrink-0 text-slate-400 transition-transform ${open ? 'rotate-180' : ''}`} />
      </button>

      {open && (
        <div
          role="listbox"
          aria-label={ariaLabel}
          className="absolute z-50 mt-1 w-full max-h-56 overflow-y-auto rounded-xl bg-white dark:bg-neutral-900 border border-slate-200 dark:border-neutral-700 shadow-2xl p-1"
        >
          {options.map((option) => {
            const active = option.value === value;
            return (
              <button
                key={option.value}
                type="button"
                role="option"
                aria-selected={active}
                disabled={option.disabled}
                onClick={() => {
                  if (option.disabled) return;
                  onChange(option.value);
                  setOpen(false);
                }}
                className={`w-full px-2.5 py-2 rounded-lg text-left text-xs transition-colors flex items-start gap-2 ${
                  active
                    ? 'bg-emerald-500/15 text-emerald-700 dark:text-emerald-300 font-semibold'
                    : 'text-slate-700 dark:text-neutral-300 hover:bg-slate-100 dark:hover:bg-neutral-800'
                } disabled:opacity-40 disabled:cursor-not-allowed cursor-pointer`}
              >
                <Check className={`w-3.5 h-3.5 mt-0.5 shrink-0 ${active ? 'opacity-100' : 'opacity-0'}`} />
                <span className="min-w-0">
                  <span className="block truncate">{option.label}</span>
                  {option.hint && (
                    <span className="block text-[10px] text-slate-500 dark:text-neutral-500 font-normal mt-0.5">
                      {option.hint}
                    </span>
                  )}
                </span>
              </button>
            );
          })}
        </div>
      )}
    </div>
  );
};
