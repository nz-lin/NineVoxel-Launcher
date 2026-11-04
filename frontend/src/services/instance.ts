import { InstanceService } from '../../bindings/example.com/ninevoxel/launcher/internal/instance'
import { JavaMode, JVMMode, MemoryMode } from '../../bindings/example.com/ninevoxel/launcher/internal/launch/models'
import type { Config, Instance as StoredInstance, TrashItem as StoredTrashItem } from '../../bindings/example.com/ninevoxel/launcher/internal/instance/models'
import type { Instance, InstanceGroup, InstanceGroupList, InstanceTrashItem } from '../types/launcher'
import { toAppError } from './errors'

export type InstanceConfig = Pick<Instance, 'name' | 'mcVersion' | 'loader' | 'loaderVersion' | 'partition' | 'icon' | 'memoryMode' | 'memoryMb' | 'jvmMode' | 'jvmArgs' | 'javaMode' | 'description' | 'isFavorite' | 'customJavaPath' | 'customJavaRuntimeId'>

function config(input: InstanceConfig): Config {
  return {
    name: input.name, mcVersion: input.mcVersion, loader: input.loader, loaderVersion: input.loaderVersion,
    partition: input.partition, icon: input.icon,
    memoryMode: input.memoryMode as MemoryMode, memoryMb: input.memoryMb,
    jvmMode: input.jvmMode as JVMMode, jvmArgs: input.jvmArgs,
    javaMode: input.javaMode as JavaMode,
    description: input.description, isFavorite: input.isFavorite,
    customJavaPath: input.customJavaPath ?? '',
    customJavaRuntimeId: input.customJavaRuntimeId ?? '',
  }
}

function toInstance(item: StoredInstance): Instance {
  return {
    ...item, loader: item.loader as Instance['loader'], loaderVersion: item.loaderVersion || '', installedLoaders: [],
    playTimeMinutes: 0, lastPlayed: '从未运行', modsCount: 0,
    status: 'ready', isolatedDirectory: true,
    installationState: item.installationState as Instance['installationState'],
    // 后端用枚举存储，前端用字符串字面量做展示与分支判断；
    // 空值（旧数据）统一归为「跟随全局」，与后端归一化行为一致。
    memoryMode: item.memoryMode === 'custom' ? 'custom' : 'global',
    jvmMode: item.jvmMode === 'custom' ? 'custom' : 'global',
    javaMode: item.javaMode === 'managed' || item.javaMode === 'custom' ? item.javaMode : 'global',
  }
}

function toTrashItem(item: StoredTrashItem): InstanceTrashItem {
  return {
    trashId: item.trashId,
    instanceId: item.instanceId,
    name: item.name,
    mcVersion: item.mcVersion,
    loader: item.loader,
    icon: item.icon,
    deletedAtMs: item.deletedAtMs,
    sizeBytes: item.sizeBytes,
    restorable: item.restorable,
    errorMessage: item.errorMessage || undefined,
  }
}

function toTrashItems(raw: { items: StoredTrashItem[] | null }) {
  return (raw.items ?? []).map(toTrashItem)
}

async function call<T>(operation: () => Promise<T>): Promise<T> {
  try { return await operation() } catch (cause) { throw toAppError(cause) }
}

function toGroupList(raw: { items: InstanceGroup[] | null; defaultGroup: string }): InstanceGroupList {
  return {
    items: (raw.items ?? []).map((group) => ({
      name: group.name,
      isDefault: group.isDefault,
      instanceCount: group.instanceCount,
      createdAtMs: group.createdAtMs,
    })),
    defaultGroup: raw.defaultGroup,
  }
}

export const instanceService = {
  async list() {
    const result = await call(() => InstanceService.ListInstances())
    return { items: (result.items ?? []).map(toInstance), selectedInstanceId: result.selectedInstanceId }
  },
  async create(input: InstanceConfig) { return toInstance(await call(() => InstanceService.CreateInstance(config(input)))) },
  async update(item: Instance) { return toInstance(await call(() => InstanceService.UpdateInstance({ ...config(item), instanceId: item.id }))) },
  async select(id: string) { await call(() => InstanceService.SelectInstance({ instanceId: id })) },
  async openFolder(id: string) { await call(() => InstanceService.OpenInstanceFolder({ instanceId: id })) },
  async remove(id: string) { await call(() => InstanceService.DeleteInstance({ instanceId: id })) },
  async install(id: string) { return toInstance(await call(() => InstanceService.InstallInstance({ instanceId: id }))) },
  async updateLoader(id: string, loader: Instance['loader'], loaderVersion: string) {
    return toInstance(await call(() => InstanceService.UpdateLoader({ instanceId: id, loader, loaderVersion })))
  },
  async uninstallLoader(id: string) {
    return toInstance(await call(() => InstanceService.UninstallLoader({ instanceId: id })))
  },
  async listTrash() {
    return toTrashItems(await call(() => InstanceService.ListInstanceTrash()))
  },
  async restoreTrash(trashId: string) {
    const result = await call(() => InstanceService.RestoreInstanceFromTrash({ trashId }))
    return { items: (result.items ?? []).map(toInstance), selectedInstanceId: result.selectedInstanceId }
  },
  async deleteTrash(trashId: string) {
    return toTrashItems(await call(() => InstanceService.DeleteInstancePermanently({ trashId })))
  },
  async emptyTrash() {
    return toTrashItems(await call(() => InstanceService.EmptyInstanceTrash()))
  },
  async listGroups() {
    return toGroupList(await call(() => InstanceService.ListInstanceGroups()))
  },
  async createGroup(name: string) {
    return toGroupList(await call(() => InstanceService.CreateInstanceGroup({ name })))
  },
  async deleteGroup(name: string, moveTo = '') {
    return toGroupList(await call(() => InstanceService.DeleteInstanceGroup({ name, moveTo })))
  },
}
