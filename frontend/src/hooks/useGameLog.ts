import { useCallback, useEffect, useRef, useState } from 'react'
import type { LogEntry } from '../types/launcher'
import type { GameLogLineResult } from '../types/launch'
import { fetchGameLog, subscribeLaunchLogAppended } from '../services/launch'
import { toAppError } from '../services/errors'

const GAME_LOG_PAGE_SIZE = 1000

export interface GameLogState {
  entries: LogEntry[]
  loading: boolean
  loadingMore: boolean
  error: string | null
  droppedLines: number
  hasMore: boolean
  reload: () => Promise<void>
  loadOlder: () => Promise<void>
  clear: () => void
}

function formatTimestamp(timestampMs: number): string {
  const date = new Date(timestampMs)
  const pad = (value: number, width = 2) => String(value).padStart(width, '0')
  return `${pad(date.getHours())}:${pad(date.getMinutes())}:${pad(date.getSeconds())}.${pad(date.getMilliseconds(), 3)}`
}

function toLogEntry(line: GameLogLineResult, fallbackSequence: number): LogEntry {
  const identity = line.sequence > 0 ? String(line.sequence) : String(fallbackSequence)
  return {
    id: `game-${line.timestampMs}-${identity}`,
    timestamp: formatTimestamp(line.timestampMs),
    level: line.stream === 'stderr' ? 'WARN' : 'INFO',
    source: line.stream,
    message: line.text,
  }
}

/**
 * Read real Minecraft process output. Snapshot reads recover from lost events;
 * `launch.log.appended.v1` only provides incremental lines.
 *
 * The two paths can overlap when a snapshot is in flight. Every line carries a
 * channel-local sequence, so the hook can merge buffered events without
 * rendering duplicate rows.
 */
export function useGameLog(launchId: string): GameLogState {
  const [entries, setEntries] = useState<LogEntry[]>([])
  const [loading, setLoading] = useState(false)
  const [loadingMore, setLoadingMore] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [droppedLines, setDroppedLines] = useState(0)
  const [hasMore, setHasMore] = useState(false)
  const nextBeforeMsRef = useRef(0)
  const fallbackSequenceRef = useRef(0)
  const seenSequencesRef = useRef<Set<number>>(new Set())
  const seenEventsRef = useRef<Set<string>>(new Set())
  const pendingLinesRef = useRef<GameLogLineResult[]>([])
  const syncingRef = useRef(false)
  const generationRef = useRef(0)
  const currentLaunchIdRef = useRef('')
  const mountedRef = useRef(false)

  const takeUnseen = useCallback((lines: GameLogLineResult[]): GameLogLineResult[] => {
    const fresh: GameLogLineResult[] = []
    for (const line of lines) {
      if (line.sequence > 0) {
        if (seenSequencesRef.current.has(line.sequence)) continue
        seenSequencesRef.current.add(line.sequence)
      }
      fresh.push(line)
    }
    return fresh
  }, [])

  const toEntries = useCallback((lines: GameLogLineResult[]): LogEntry[] => {
    return lines.map((line) => toLogEntry(line, ++fallbackSequenceRef.current))
  }, [])

  const appendLines = useCallback((lines: GameLogLineResult[]) => {
    const fresh = takeUnseen(lines)
    if (fresh.length === 0) return
    const appended = toEntries(fresh)
    setEntries((current) => [...current, ...appended])
  }, [takeUnseen, toEntries])

  const reload = useCallback(async () => {
    const launchChanged = currentLaunchIdRef.current !== launchId
    if (launchChanged) {
      currentLaunchIdRef.current = launchId
      seenSequencesRef.current = new Set()
      seenEventsRef.current = new Set()
      pendingLinesRef.current = []
      fallbackSequenceRef.current = 0
      nextBeforeMsRef.current = 0
      setEntries([])
      setDroppedLines(0)
      setHasMore(false)
    }

    if (!launchId) {
      syncingRef.current = false
      setLoading(false)
      setError(null)
      return
    }

    const generation = ++generationRef.current
    syncingRef.current = true
    pendingLinesRef.current = []
    setLoading(true)
    setError(null)
    try {
      const result = await fetchGameLog({
        launchId,
        beforeMs: 0,
        limit: GAME_LOG_PAGE_SIZE,
      })
      if (!mountedRef.current || generation !== generationRef.current) return

      const ordered = result.lines.slice().reverse()
      seenSequencesRef.current = new Set()
      fallbackSequenceRef.current = 0
      const snapshotEntries = toEntries(takeUnseen(ordered))

      // Lines emitted while the snapshot request was in flight may or may not
      // already be present in the snapshot. Merge only unseen sequences.
      const pending = takeUnseen(pendingLinesRef.current)
      pendingLinesRef.current = []

      setEntries([...snapshotEntries, ...toEntries(pending)])
      setDroppedLines(result.droppedLines)
      setHasMore(result.hasMore)
      nextBeforeMsRef.current = result.nextBeforeMs
    } catch (cause) {
      if (!mountedRef.current || generation !== generationRef.current) return
      // A failed refresh must not discard live lines that arrived meanwhile.
      appendLines(pendingLinesRef.current)
      pendingLinesRef.current = []
      setError(toAppError(cause).message)
    } finally {
      if (generation === generationRef.current) syncingRef.current = false
      if (mountedRef.current && generation === generationRef.current) setLoading(false)
    }
  }, [appendLines, launchId, takeUnseen, toEntries])

  useEffect(() => {
    mountedRef.current = true
    void reload()
    return () => {
      mountedRef.current = false
    }
  }, [reload])

  useEffect(() => {
    const unsubscribe = subscribeLaunchLogAppended((event) => {
      const payload = event.payload
      if (!launchId || payload.launchId !== launchId) return
      if (event.eventId && seenEventsRef.current.has(event.eventId)) return
      if (event.eventId) seenEventsRef.current.add(event.eventId)
      setDroppedLines(payload.droppedLines)
      if (syncingRef.current) {
        pendingLinesRef.current.push(...payload.lines)
        return
      }
      appendLines(payload.lines)
    })
    return unsubscribe
  }, [appendLines, launchId])

  const loadOlder = useCallback(async () => {
    if (!launchId || loadingMore || !hasMore || nextBeforeMsRef.current <= 0 || syncingRef.current) return
    const generation = generationRef.current
    setLoadingMore(true)
    try {
      const result = await fetchGameLog({
        launchId,
        beforeMs: nextBeforeMsRef.current,
        limit: GAME_LOG_PAGE_SIZE,
      })
      if (!mountedRef.current || generation !== generationRef.current) return
      const older = takeUnseen(result.lines.slice().reverse())
      if (older.length > 0) {
        const olderEntries = toEntries(older)
        setEntries((current) => [...olderEntries, ...current])
      }
      setDroppedLines(result.droppedLines)
      setHasMore(result.hasMore)
      nextBeforeMsRef.current = result.nextBeforeMs
      setError(null)
    } catch (cause) {
      if (mountedRef.current && generation === generationRef.current) {
        setError(toAppError(cause).message)
      }
    } finally {
      if (mountedRef.current && generation === generationRef.current) setLoadingMore(false)
    }
  }, [hasMore, launchId, loadingMore, takeUnseen, toEntries])

  const clear = useCallback(() => {
    setEntries([])
    setError(null)
  }, [])

  return { entries, loading, loadingMore, error, droppedLines, hasMore, reload, loadOlder, clear }
}
