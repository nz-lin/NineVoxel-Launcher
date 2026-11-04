import { Events } from '@wailsio/runtime'
import { InstanceService } from '../../bindings/example.com/ninevoxel/launcher/internal/instance'
import { JavaMode, JVMMode, MemoryMode } from '../../bindings/example.com/ninevoxel/launcher/internal/launch/models'
import type { Config, Instance as StoredInstance, TrashItem as StoredTrashItem } from '../../bindings/example.com/ninevoxel/launcher/internal/instance/models'
import type { Instance, InstanceGroup, InstanceGroupList, InstanceTrashItem } from '../types/launcher'
import type { Unsubscribe } from './events'
import { toAppError } from './errors'

/** `core.rebuild.progress.v1` 的事件名，与后端 `events.EventCoreRebuildProgress` 一致。 */
const CORE_REBUILD_PROGRESS_EVENT = 'core.rebuild.progress.v1'

/** 一次核心重建的进度快照。 */
export interface CoreRebuildProgress {
  /** 当前阶段的用户可读说明，由后端给出。 */
  phase: string
  /** 0~1 的整体进度；只用于画进度条，不承诺线性。 */
  progress: number
}

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

// 本模块封装实例相关后端调用，并把绑定类型转换成前端视图模型。
// 重建核心是低频、长耗时操作，返回统计而不是进度流。
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
  /** 校验实例依赖的核心资源完整性（逐文件哈希）。 */
  async verifyCore(instanceId: string) {
    return call(() => InstanceService.VerifyInstance({ instanceId }))
  },
  /**
   * 修复实例依赖的核心资源：只补齐缺失或损坏的文件。
   *
   * 返回 `needsReinstall` 时说明问题超出修复能力（加载器产物或版本元数据
   * 损坏），界面应引导用户重新安装而不是重复点击修复。
   */
  async repairCore(instanceId: string) {
    return call(() => InstanceService.RepairInstanceForUser({ instanceId }))
  },
  /**
   * 重建共享游戏核心：按已安装清单生成干净核心后事务化切换。
   *
   * 会先校验并修复参与重建的版本；文件无法修复时明确要求重新安装，
   * 而不是复制一份不完整的数据。
   */
  async rebuildCore() {
    return call(() => InstanceService.RebuildCoreForUser())
  },
  /**
   * 回收无人引用的核心版本。
   *
   * 只删「由启动器安装」且没有任何实例（含回收站）引用的版本；用户手动放入
   * 的目录、运行中的版本一律跳过并返回原因。
   */
  async cleanupUnusedCore() {
    return call(() => InstanceService.CleanupUnusedCoreForUser())
  },
  /** 读取实例的版本声明覆盖（patches/<versionId>.json）。 */
  async readVersionPatch(instanceId: string) {
    return call(() => InstanceService.ReadVersionPatch({ instanceId }))
  },
  /** 保存版本声明覆盖；后端会在落盘前做合并校验。 */
  async saveVersionPatch(instanceId: string, content: string) {
    return call(() => InstanceService.SaveVersionPatch({ instanceId, content }))
  },
  /** 删除版本声明覆盖，恢复核心声明。 */
  async deleteVersionPatch(instanceId: string) {
    return call(() => InstanceService.DeleteVersionPatch({ instanceId }))
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

/**
 * 订阅核心重建进度。
 *
 * 重建是一次同步绑定调用，最终结果来自 `rebuildCore()` 的返回值；本订阅只负责
 * 让界面在等待期间有反馈。事件可能丢失或重复（规范前提），因此界面不得把进度
 * 当成「是否完成」的判据。
 *
 * @param handler 进度处理函数
 * @returns 幂等的解绑函数
 */
export function subscribeCoreRebuildProgress(handler: (progress: CoreRebuildProgress) => void): Unsubscribe {
  let active = true

  const release = Events.On(CORE_REBUILD_PROGRESS_EVENT, (event) => {
    if (!active) {
      return
    }
    // 该事件未登记在生成的 `CustomEvents` 声明里，按 unknown 取载荷后再归一化。
    const payload = (event?.data as { payload?: Partial<CoreRebuildProgress> } | undefined)?.payload
    if (!payload || typeof payload.phase !== 'string') {
      return
    }
    const raw = typeof payload.progress === 'number' ? payload.progress : 0
    // 夹紧到 [0,1]：越界值会让进度条宽度超出容器。
    handler({ phase: payload.phase, progress: Math.min(Math.max(raw, 0), 1) })
  })

  return () => {
    if (!active) {
      return
    }
    active = false
    release()
  }
}
