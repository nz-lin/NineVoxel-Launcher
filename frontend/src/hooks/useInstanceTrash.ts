import { useCallback, useEffect, useRef, useState } from 'react'
import type { InstanceTrashItem } from '../types/launcher'
import { instanceService } from '../services/instance'

export interface InstanceTrashState {
  items: InstanceTrashItem[]
  loading: boolean
  error: string | null
  busy: string
  reload: () => Promise<void>
  restore: (trashId: string) => Promise<void>
  remove: (trashId: string) => Promise<void>
  empty: () => Promise<void>
}

export function useInstanceTrash(enabled: boolean): InstanceTrashState {
  const [items, setItems] = useState<InstanceTrashItem[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState('')
  const mounted = useRef(false)
  const sequence = useRef(0)

  const reload = useCallback(async () => {
    const request = ++sequence.current
    setLoading(true)
    try {
      const list = await instanceService.listTrash()
      if (!mounted.current || request !== sequence.current) return
      setItems(list)
      setError(null)
    } catch (cause) {
      if (mounted.current && request === sequence.current) {
        setError(cause instanceof Error ? cause.message : String(cause))
      }
    } finally {
      if (mounted.current && request === sequence.current) setLoading(false)
    }
  }, [])

  useEffect(() => {
    if (!enabled) return
    mounted.current = true
    void reload()
    return () => {
      mounted.current = false
      sequence.current++
    }
  }, [enabled, reload])

  const restore = useCallback(async (trashId: string) => {
    setBusy(`restore:${trashId}`)
    try {
      await instanceService.restoreTrash(trashId)
      await reload()
    } catch (cause) {
      const message = cause instanceof Error ? cause.message : String(cause)
      if (mounted.current) setError(message)
      throw cause
    } finally {
      if (mounted.current) setBusy('')
    }
  }, [reload])

  const remove = useCallback(async (trashId: string) => {
    setBusy(`delete:${trashId}`)
    try {
      const list = await instanceService.deleteTrash(trashId)
      if (mounted.current) {
        setItems(list)
        setError(null)
      }
    } catch (cause) {
      const message = cause instanceof Error ? cause.message : String(cause)
      if (mounted.current) setError(message)
      throw cause
    } finally {
      if (mounted.current) setBusy('')
    }
  }, [])

  const empty = useCallback(async () => {
    setBusy('empty')
    try {
      const list = await instanceService.emptyTrash()
      if (mounted.current) {
        setItems(list)
        setError(null)
      }
    } catch (cause) {
      const message = cause instanceof Error ? cause.message : String(cause)
      if (mounted.current) setError(message)
      throw cause
    } finally {
      if (mounted.current) setBusy('')
    }
  }, [])

  return { items, loading, error, busy, reload, restore, remove, empty }
}
