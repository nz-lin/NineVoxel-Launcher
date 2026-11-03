import { useCallback, useEffect, useRef, useState } from 'react'
import { listLoaderVersions } from '../services/loader'
import type { LoaderVersionList, ModLoaderType } from '../types/loader'

/** 按 Minecraft 版本加载官方加载器目录，并处理切换与卸载后的过期请求。 */
export function useLoaderVersions(loader: ModLoaderType | '', minecraftVersion: string) {
  const [data, setData] = useState<LoaderVersionList | null>(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const sequence = useRef(0)
  const mounted = useRef(false)
  const currentKey = useRef('')

  const reload = useCallback(async () => {
    if (!loader || !minecraftVersion) {
      setData(null)
      setError(null)
      setLoading(false)
      return
    }
    const key = `${loader}:${minecraftVersion}`
    if (currentKey.current !== key) {
      currentKey.current = key
      setData(null)
      setError(null)
    }
    const request = ++sequence.current
    setLoading(true)
    setError(null)
    try {
      const result = await listLoaderVersions(loader, minecraftVersion)
      if (!mounted.current || request !== sequence.current) return
      setData(result)
    } catch (cause) {
      if (!mounted.current || request !== sequence.current) return
      setData(null)
      setError(cause instanceof Error ? cause.message : String(cause))
    } finally {
      if (mounted.current && request === sequence.current) setLoading(false)
    }
  }, [loader, minecraftVersion])

  useEffect(() => {
    mounted.current = true
    void reload()
    return () => {
      mounted.current = false
      sequence.current++
    }
  }, [reload])

  return { data, loading, error, reload }
}
