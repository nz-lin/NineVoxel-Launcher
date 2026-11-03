import { useCallback, useEffect, useRef, useState } from 'react'
import type { InstanceGroup } from '../types/launcher'
import { instanceService } from '../services/instance'

export interface InstanceGroupsState {
  groups: InstanceGroup[]
  defaultGroup: string
  loading: boolean
  error: string | null
  reload: () => Promise<void>
  createGroup: (name: string) => Promise<void>
  deleteGroup: (name: string, moveTo?: string) => Promise<void>
}

export function useInstanceGroups(): InstanceGroupsState {
  const [groups, setGroups] = useState<InstanceGroup[]>([])
  const [defaultGroup, setDefaultGroup] = useState('默认分组')
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const mounted = useRef(false)
  const sequence = useRef(0)

  const apply = useCallback((items: InstanceGroup[], nextDefault: string) => {
    setGroups(items)
    setDefaultGroup(nextDefault || items.find((group) => group.isDefault)?.name || '默认分组')
    setError(null)
  }, [])

  const reload = useCallback(async () => {
    const request = ++sequence.current
    try {
      const list = await instanceService.listGroups()
      if (!mounted.current || request !== sequence.current) return
      apply(list.items, list.defaultGroup)
    } catch (cause) {
      if (mounted.current && request === sequence.current) {
        setError(cause instanceof Error ? cause.message : String(cause))
      }
    } finally {
      if (mounted.current && request === sequence.current) setLoading(false)
    }
  }, [apply])

  useEffect(() => {
    mounted.current = true
    void reload()
    return () => {
      mounted.current = false
      sequence.current++
    }
  }, [reload])

  const createGroup = useCallback(async (name: string) => {
    const list = await instanceService.createGroup(name)
    apply(list.items, list.defaultGroup)
  }, [apply])

  const deleteGroup = useCallback(async (name: string, moveTo = '') => {
    const list = await instanceService.deleteGroup(name, moveTo)
    apply(list.items, list.defaultGroup)
  }, [apply])

  return { groups, defaultGroup, loading, error, reload, createGroup, deleteGroup }
}
