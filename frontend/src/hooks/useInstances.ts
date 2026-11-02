import { useCallback, useEffect, useRef, useState } from 'react'
import type { Instance } from '../types/launcher'
import { instanceService } from '../services/instance'

export function useInstances() {
  const [instances, setInstances] = useState<Instance[]>([])
  const [selectedId, setSelectedId] = useState('')
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const mounted = useRef(false)
  const sequence = useRef(0)

  const reload = useCallback(async () => {
    const request = ++sequence.current
    try {
      const list = await instanceService.list()
      if (!mounted.current || request !== sequence.current) return
      setInstances(list.items)
      setSelectedId(list.selectedInstanceId)
      setError(null)
    } catch (cause) {
      if (mounted.current && request === sequence.current) setError(cause instanceof Error ? cause.message : String(cause))
    } finally {
      if (mounted.current && request === sequence.current) setLoading(false)
    }
  }, [])

  useEffect(() => {
    mounted.current = true
    void reload()
    return () => { mounted.current = false; sequence.current++ }
  }, [reload])

  const installing = instances.some(item => item.installationState === 'installing')
  useEffect(() => {
    if (!installing) return
    const timer = window.setInterval(() => { void reload() }, 1500)
    return () => window.clearInterval(timer)
  }, [installing, reload])

  return { instances, selectedId, loading, error, reload }
}
