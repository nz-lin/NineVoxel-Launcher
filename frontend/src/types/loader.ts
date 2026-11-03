import type { LoaderType } from './launcher'

/** 可由官方目录查询版本的模组加载器。 */
export type ModLoaderType = Extract<LoaderType, 'fabric' | 'quilt' | 'forge' | 'neoforge'>

/** 判断一个核心类型是否具有官方加载器版本目录。 */
export function isModLoaderType(loader: LoaderType): loader is ModLoaderType {
  return MOD_LOADER_OPTIONS.some((item) => item.value === loader)
}

/** 加载器官方版本列表中的单个条目。 */
export interface LoaderVersion {
  version: string
  stable: boolean
  recommended: boolean
}

/** 与一个 Minecraft 版本兼容的加载器目录。 */
export interface LoaderVersionList {
  loader: ModLoaderType
  minecraftVersion: string
  versions: LoaderVersion[]
  recommended: string
  stale: boolean
}

export const MOD_LOADER_OPTIONS: Array<{ value: ModLoaderType; label: string; description: string }> = [
  { value: 'fabric', label: 'Fabric', description: '轻量、启动快，适合优化与客户端模组' },
  { value: 'quilt', label: 'Quilt', description: '兼容 Fabric 生态，支持 Quilt 扩展' },
  { value: 'forge', label: 'Forge', description: '传统大型整合包与科技模组生态' },
  { value: 'neoforge', label: 'NeoForge', description: 'Forge 新分支，支持现代模组生态' },
]

export function loaderDisplayName(loader: LoaderType): string {
  if (loader === 'vanilla') return 'Vanilla 原版'
  return MOD_LOADER_OPTIONS.find((item) => item.value === loader)?.label ?? loader
}
