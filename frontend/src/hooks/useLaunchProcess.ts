import { useCallback, useEffect, useRef, useState } from 'react'
import type { LaunchAcceptedResult, LaunchPhase, LaunchState, LaunchStatus } from '../types/launch'
import {
  fetchLaunchStatus,
  startLaunchProcess,
  stopLaunchProcess,
  subscribeLaunchStateChanged,
} from '../services/launch'
import { toAppError } from '../services/errors'

const ACTIVE_STATES: ReadonlySet<LaunchState> = new Set([
  'preparing',
  'resolving_runtime',
  'building_arguments',
  'starting_process',
  'running',
  'stopping',
])

/** Map the backend state machine to the small set of launch-page phases. */
export function toLaunchPhase(state: LaunchState | undefined): LaunchPhase {
  switch (state) {
    case 'preparing':
    case 'resolving_runtime':
    case 'building_arguments':
    case 'starting_process':
      return 'preparing'
    case 'running':
      return 'running'
    case 'stopping':
      return 'stopping'
    case 'exited':
    case 'failed':
    case 'cancelled':
      return 'exited'
    default:
      return 'idle'
  }
}

export interface LaunchProcessState {
  status: LaunchStatus | null
  phase: LaunchPhase
  loading: boolean
  error: string | null
  start: (instanceId: string, accountId?: string) => Promise<LaunchStatus>
  stop: (force?: boolean) => Promise<LaunchStatus>
  refresh: () => Promise<void>
}

/**
 * Own the real launch lifecycle. Events only trigger a snapshot refresh; the
 * snapshot remains the source of truth so dropped events cannot leave the UI
 * stuck in a stale state.
 */
export function useLaunchProcess(): LaunchProcessState {
  const [status, setStatus] = useState<LaunchStatus | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const statusRef = useRef<LaunchStatus | null>(null)
  const mountedRef = useRef(false)
  const refreshTimerRef = useRef<number | null>(null)

  const applyStatus = useCallback((next: LaunchStatus) => {
    statusRef.current = next
    setStatus(next)
  }, [])

  const refresh = useCallback(async () => {
    try {
      const next = await fetchLaunchStatus('')
      if (!mountedRef.current) return
      applyStatus(next)
      setError(null)
    } catch (cause) {
      if (!mountedRef.current) return
      setError(toAppError(cause).message)
    } finally {
      if (mountedRef.current) setLoading(false)
    }
  }, [applyStatus])

  const scheduleRefresh = useCallback(() => {
    if (refreshTimerRef.current !== null) return
    refreshTimerRef.current = window.setTimeout(() => {
      refreshTimerRef.current = null
      void refresh()
    }, 60)
  }, [refresh])

  useEffect(() => {
    mountedRef.current = true
    void refresh()
    const unsubscribe = subscribeLaunchStateChanged(() => scheduleRefresh())
    return () => {
      mountedRef.current = false
      unsubscribe()
      if (refreshTimerRef.current !== null) {
        window.clearTimeout(refreshTimerRef.current)
        refreshTimerRef.current = null
      }
    }
  }, [refresh, scheduleRefresh])

  useEffect(() => {
    if (!status || !ACTIVE_STATES.has(status.state)) return
    const timer = window.setInterval(() => { void refresh() }, 1000)
    return () => window.clearInterval(timer)
  }, [status?.state, refresh])

  const start = useCallback(async (instanceId: string, accountId = ''): Promise<LaunchStatus> => {
    setError(null)
    setLoading(true)
    let accepted: LaunchAcceptedResult
    try {
      accepted = await startLaunchProcess({
        instanceId,
        accountId,
        additionalJvmArgs: null,
        additionalGameArgs: null,
      })
    } catch (cause) {
      const appError = toAppError(cause)
      if (mountedRef.current) setError(appError.message)
      throw appError
    } finally {
      if (mountedRef.current) setLoading(false)
    }

    // The process has been accepted at this point. A snapshot failure must not
    // be reported as a launch failure; fall back to the accepted state and let
    // the next event/poll recover the full snapshot.
    try {
      const next = await fetchLaunchStatus(accepted.launchId)
      if (mountedRef.current) applyStatus(next)
      return next
    } catch {
      const fallback: LaunchStatus = {
        launchId: accepted.launchId,
        state: accepted.state,
        versionId: '',
        startedAtMs: 0,
        endedAtMs: 0,
        exitCode: 0,
        hasExitCode: false,
        stopReason: '',
        stopResult: '',
        outcome: '',
        stateChangedAtMs: accepted.acceptedAtMs,
        errorCode: '',
        errorMessage: '',
      }
      if (mountedRef.current) applyStatus(fallback)
      return fallback
    }
  }, [applyStatus])

  const stop = useCallback(async (force = false): Promise<LaunchStatus> => {
    setError(null)
    try {
      const next = await stopLaunchProcess({
        launchId: statusRef.current?.launchId ?? '',
        force,
      })
      if (mountedRef.current) applyStatus(next)
      return next
    } catch (cause) {
      const appError = toAppError(cause)
      if (mountedRef.current) setError(appError.message)
      throw appError
    }
  }, [applyStatus])

  return {
    status,
    phase: toLaunchPhase(status?.state),
    loading,
    error,
    start,
    stop,
    refresh,
  }
}
