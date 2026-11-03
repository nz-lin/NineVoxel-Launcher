import { LoaderService } from '../../bindings/example.com/ninevoxel/launcher/internal/loader'
import type { LoaderVersion, LoaderVersionList, ModLoaderType } from '../types/loader'
import { toAppError } from './errors'

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null
}

function asString(value: unknown, fallback = ''): string {
  return typeof value === 'string' ? value : fallback
}

function asBoolean(value: unknown, fallback = false): boolean {
  return typeof value === 'boolean' ? value : fallback
}

function normalizeVersion(raw: unknown): LoaderVersion | null {
  if (!isRecord(raw)) return null
  const version = asString(raw.version)
  if (!version) return null
  return {
    version,
    stable: asBoolean(raw.stable, true),
    recommended: asBoolean(raw.recommended, false),
  }
}

function normalizeList(raw: unknown, loader: ModLoaderType, minecraftVersion: string): LoaderVersionList {
  const record = isRecord(raw) ? raw : {}
  const versions = Array.isArray(record.versions)
    ? record.versions.map(normalizeVersion).filter((item): item is LoaderVersion => item !== null)
    : []
  return {
    loader: asString(record.loader, loader) as ModLoaderType,
    minecraftVersion: asString(record.minecraftVersion, minecraftVersion),
    versions,
    recommended: asString(record.recommended),
    stale: asBoolean(record.stale),
  }
}

/** 查询某 Minecraft 版本可用的官方加载器版本。 */
export async function listLoaderVersions(loader: ModLoaderType, minecraftVersion: string): Promise<LoaderVersionList> {
  try {
    return normalizeList(
      await LoaderService.ListVersions({ loader, minecraftVersion }),
      loader,
      minecraftVersion,
    )
  } catch (cause) {
    throw toAppError(cause, 'LOADER_METADATA_001')
  }
}
