/**
 * 启动器日志钩子。
 *
 * 数据来自后端日志子系统：`fetchLogStatus()` 给出日志目录与文件概况，
 * `queryLogEntries()` 按时间倒序返回条目（最新在前）。界面需要最新在下，
 * 因此这里在映射时反转数组。
 *
 * 注意：日志内容在后端写入前已脱敏，前端只需展示，不做二次处理。
 */
import { useCallback, useEffect, useRef, useState } from 'react';
import { DEFAULT_PAGE_SIZE, fetchLogStatus, queryLogEntries } from '../services/logs';
import type { LogStatus } from '../services/logs';
import type { LogEntry } from '../types/launcher';

/** 后端日志级别到界面级别的大写映射。 */
function toDisplayLevel(level: string): LogEntry['level'] {
  switch (level.toLowerCase()) {
    case 'debug':
      return 'DEBUG';
    case 'warn':
      return 'WARN';
    case 'error':
      return 'ERROR';
    case 'fatal':
      return 'FATAL';
    default:
      return 'INFO';
  }
}

/** 把毫秒时间戳格式化成 `HH:mm:ss.SSS`。 */
function formatTimestamp(timestampMs: number): string {
  const date = new Date(timestampMs);
  const pad = (value: number, width = 2) => String(value).padStart(width, '0');
  return `${pad(date.getHours())}:${pad(date.getMinutes())}:${pad(date.getSeconds())}.${pad(date.getMilliseconds(), 3)}`;
}

/** 日志钩子的返回值。 */
export interface LauncherLogsState {
  /** 日志条目（最新在后，便于滚动到底）。 */
  entries: LogEntry[];
  /** 日志子系统状态（目录是否可写、文件数等）。 */
  status: LogStatus | null;
  /** 是否正在加载。 */
  loading: boolean;
  /** 错误文案；为空表示无错误。 */
  error: string | null;
  /** 重新查询。 */
  reload: () => Promise<void>;
}

/**
 * 读取启动器自身日志。
 *
 * @param level 级别过滤；空字符串表示全部级别
 * @returns 日志条目、子系统状态与加载错误
 */
export function useLauncherLogs(level = ''): LauncherLogsState {
  const [entries, setEntries] = useState<LogEntry[]>([]);
  const [status, setStatus] = useState<LogStatus | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const cancelledRef = useRef(false);

  const reload = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const [result, logStatus] = await Promise.all([
        queryLogEntries({ level, component: '', limit: DEFAULT_PAGE_SIZE, beforeMs: 0 }),
        fetchLogStatus(),
      ]);
      if (cancelledRef.current) return;
      // 后端按时间倒序返回，界面要最新在最下方。
      setEntries(
        result.entries
          .map((entry, index) => ({
            id: `${entry.timestampMs}-${entry.operationId || entry.correlationId || index}`,
            timestamp: formatTimestamp(entry.timestampMs),
            level: toDisplayLevel(entry.level),
            source: entry.component || 'launcher',
            message: entry.errorCode ? `[${entry.errorCode}] ${entry.message}` : entry.message,
          }))
          .reverse(),
      );
      setStatus(logStatus);
    } catch (cause) {
      if (cancelledRef.current) return;
      setError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      if (!cancelledRef.current) setLoading(false);
    }
  }, [level]);

  useEffect(() => {
    cancelledRef.current = false;
    void reload();
    return () => {
      cancelledRef.current = true;
    };
  }, [reload]);

  return { entries, status, loading, error, reload };
}
