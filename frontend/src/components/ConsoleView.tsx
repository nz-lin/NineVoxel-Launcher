import React, { useState, useRef, useEffect } from 'react';
import {
  Terminal,
  Copy,
  Trash2,
  Search,
  RefreshCw,
  Loader2
} from 'lucide-react';
import { Instance } from '../types/launcher';
import { useGameLog } from '../hooks/useGameLog';

interface ConsoleViewProps {
  currentInstance: Instance;
  launchId: string;
}

export const ConsoleView: React.FC<ConsoleViewProps> = ({
  currentInstance,
  launchId
}) => {
  const [filterLevel, setFilterLevel] = useState<string>('ALL');
  const [searchQuery, setSearchQuery] = useState('');
  const [autoScroll, setAutoScroll] = useState(true);
  const [copied, setCopied] = useState(false);

  // 真实游戏进程输出：快照恢复 + 增量事件，最新一条在最下方。
  const gameLog = useGameLog(launchId);
  const logs = gameLog.entries;

  const logEndRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (autoScroll && logEndRef.current) {
      logEndRef.current.scrollIntoView({ behavior: 'smooth' });
    }
  }, [logs, autoScroll]);

  const filteredLogs = logs.filter((log) => {
    const matchesLevel = filterLevel === 'ALL' || log.level === filterLevel;
    const matchesSearch =
      log.message.toLowerCase().includes(searchQuery.toLowerCase()) ||
      log.source.toLowerCase().includes(searchQuery.toLowerCase());
    return matchesLevel && matchesSearch;
  });

  const reloadLogs = () => {
    void gameLog.reload();
  };
  const copyAllLogs = () => {
    const text = logs.map(l => `[${l.timestamp}] [${l.source}/${l.level}]: ${l.message}`).join('\n');
    navigator.clipboard.writeText(text);
    setCopied(true);
    setTimeout(() => setCopied(false), 2000);
  };

  return (
    <div className="flex-1 bg-slate-100 dark:bg-neutral-950 flex flex-col overflow-hidden transition-colors duration-150">
      {/* Top Console Toolbar */}
      <div className="h-14 px-6 border-b border-slate-200 dark:border-neutral-800 flex items-center justify-between gap-4 bg-white dark:bg-neutral-900/60 shrink-0">
        <div className="flex items-center gap-3">
          <div className="w-8 h-8 rounded-lg bg-emerald-500/15 border border-emerald-500/30 flex items-center justify-center text-emerald-600 dark:text-emerald-400">
            <Terminal className="w-4 h-4" />
          </div>
          <div>
            <h1 className="text-sm font-bold text-slate-900 dark:text-neutral-100 flex items-center gap-2">
              <span>游戏控制台与实时日志</span>
              <span className={`w-2 h-2 rounded-full ${launchId ? 'bg-emerald-500 animate-pulse' : 'bg-neutral-500'}`}></span>
            </h1>
            <p className="text-[11px] text-slate-500 dark:text-neutral-400">
              {currentInstance.name} · 游戏进程输出 · {logs.length} 条
              {gameLog.droppedLines > 0 ? ` · 已丢弃 ${gameLog.droppedLines} 行` : ''}
              {gameLog.error ? ` · ${gameLog.error}` : ''}
            </p>
          </div>
        </div>

        {/* Action buttons */}
        <div className="flex items-center gap-2">
          {/* 刷新日志：重新查询后端日志子系统 */}
          <button
            onClick={reloadLogs}
            disabled={gameLog.loading || !launchId}
            className="px-3.5 py-1.5 rounded-lg bg-emerald-500/15 hover:bg-emerald-500/25 border border-emerald-500/30 text-emerald-800 dark:text-emerald-300 font-bold text-xs flex items-center gap-1.5 transition-all shadow-xs cursor-pointer disabled:opacity-50 disabled:cursor-not-allowed"
          >
            <RefreshCw className={`w-3.5 h-3.5 ${gameLog.loading ? 'animate-spin' : ''}`} />
            <span>{gameLog.loading ? '读取中…' : '刷新日志'}</span>
          </button>

          <div className="h-4 w-px bg-slate-200 dark:bg-neutral-800" />

          {/* Copy Logs */}
          <button
            onClick={copyAllLogs}
            className="p-1.5 rounded-lg bg-slate-100 dark:bg-neutral-800 hover:bg-slate-200 dark:hover:bg-neutral-700 text-slate-600 dark:text-neutral-300 hover:text-slate-900 dark:hover:text-white transition-colors cursor-pointer"
            title="复制全部日志"
          >
            <Copy className="w-3.5 h-3.5" />
          </button>

          {/* 只清空当前界面；后端 game-*.log 不会被删除。 */}
          <button
            onClick={gameLog.clear}
            className="p-1.5 rounded-lg bg-slate-100 dark:bg-neutral-800 hover:bg-slate-200 dark:hover:bg-neutral-700 text-slate-600 dark:text-neutral-300 hover:text-slate-900 dark:hover:text-white transition-colors cursor-pointer"
            title="清空当前显示（不会删除后端日志文件）"
          >
            <Trash2 className="w-3.5 h-3.5" />
          </button>
        </div>
      </div>

      {/* Filter and Search Sub-bar */}
      <div className="px-6 py-2 border-b border-slate-200 dark:border-neutral-800/80 bg-slate-50 dark:bg-neutral-900/30 flex items-center justify-between gap-3 text-xs shrink-0">
        <div className="flex items-center gap-1.5">
          {['ALL', 'INFO', 'WARN', 'ERROR', 'FATAL'].map((lvl) => (
            <button
              key={lvl}
              onClick={() => setFilterLevel(lvl)}
              className={`px-2.5 py-1 rounded text-[11px] font-mono transition-colors cursor-pointer ${
                filterLevel === lvl
                  ? 'bg-slate-200 dark:bg-neutral-800 text-slate-900 dark:text-neutral-100 font-bold border border-slate-300 dark:border-neutral-700'
                  : 'text-slate-500 dark:text-neutral-500 hover:text-slate-900 dark:hover:text-neutral-300'
              }`}
            >
              {lvl}
            </button>
          ))}
        </div>

        <div className="flex items-center gap-4">
          <div className="relative w-48">
            <Search className="w-3 h-3 text-slate-400 dark:text-neutral-500 absolute left-2.5 top-1/2 -translate-y-1/2" />
            <input
              type="text"
              value={searchQuery}
              onChange={(e) => setSearchQuery(e.target.value)}
              placeholder="搜索日志文本..."
              className="w-full pl-7 pr-3 py-1 bg-white dark:bg-neutral-950 border border-slate-200 dark:border-neutral-800 rounded text-[11px] text-slate-800 dark:text-neutral-200 focus:outline-none focus:border-emerald-500"
            />
          </div>

          <label className="flex items-center gap-1.5 text-slate-600 dark:text-neutral-400 text-[11px] cursor-pointer">
            <input
              type="checkbox"
              checked={autoScroll}
              onChange={(e) => setAutoScroll(e.target.checked)}
              className="accent-emerald-500"
            />
            <span>自动滚动到底部</span>
          </label>
        </div>
      </div>

      {/* Terminal Output Area */}
      <div className="flex-1 overflow-y-auto p-4 bg-neutral-950 font-mono text-xs select-text space-y-1">
        {gameLog.hasMore && (
          <div className="flex justify-center pb-2">
            <button
              onClick={() => { void gameLog.loadOlder(); }}
              disabled={gameLog.loadingMore}
              className="px-3 py-1.5 rounded-lg bg-neutral-900 hover:bg-neutral-800 border border-neutral-700 text-neutral-300 text-[11px] font-sans flex items-center gap-1.5 disabled:opacity-50 cursor-pointer"
            >
              {gameLog.loadingMore && <Loader2 className="w-3 h-3 animate-spin" />}
              <span>{gameLog.loadingMore ? '读取更早日志…' : '加载更早日志'}</span>
            </button>
          </div>
        )}

        {filteredLogs.map((log) => {
          let levelColor = 'text-neutral-400';
          if (log.level === 'INFO') levelColor = 'text-emerald-400';
          if (log.level === 'WARN') levelColor = 'text-amber-400';
          if (log.level === 'ERROR') levelColor = 'text-red-400 font-semibold';
          if (log.level === 'FATAL') levelColor = 'text-rose-500 font-bold bg-rose-500/10 px-1 rounded';

          return (
            <div key={log.id} className="leading-relaxed hover:bg-neutral-900/60 px-2 py-0.5 rounded flex items-start gap-2">
              <span className="text-neutral-600 select-none shrink-0 font-mono text-[11px]">
                [{log.timestamp}]
              </span>
              <span className="text-neutral-500 shrink-0 font-mono text-[11px]">
                [{log.source}/{log.level}]
              </span>
              <span className={`${levelColor} break-all flex-1`}>{log.message}</span>
            </div>
          );
        })}

        {filteredLogs.length === 0 && (
          <div className="text-center py-16 text-neutral-600">
            {!launchId
              ? '启动游戏后，这里会实时显示 Minecraft 的标准输出与错误输出。'
              : gameLog.loading
                ? '正在读取游戏日志…'
                : gameLog.error
                  ? gameLog.error
                  : '暂无匹配的游戏日志'}
          </div>
        )}

        <div ref={logEndRef} />
      </div>
    </div>
  );
};
