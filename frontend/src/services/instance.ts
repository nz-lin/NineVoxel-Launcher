import { InstanceService } from '../../bindings/example.com/ninevoxel/launcher/internal/instance'
import type { Config, Instance as StoredInstance } from '../../bindings/example.com/ninevoxel/launcher/internal/instance/models'
import type { Instance } from '../types/launcher'
import { toAppError } from './errors'

export type InstanceConfig = Pick<Instance, 'name' | 'mcVersion' | 'loader' | 'partition' | 'icon' | 'memoryMb' | 'jvmArgs' | 'description' | 'isFavorite' | 'customJavaPath'>

function config(input: InstanceConfig): Config {
  return {
    name: input.name, mcVersion: input.mcVersion, loader: input.loader,
    partition: input.partition, icon: input.icon, memoryMb: input.memoryMb,
    jvmArgs: input.jvmArgs, description: input.description, isFavorite: input.isFavorite,
    customJavaPath: input.customJavaPath ?? '',
  }
}

function toInstance(item: StoredInstance): Instance {
  return {
    ...item, loader: 'vanilla', loaderVersion: '', installedLoaders: [],
    playTimeMinutes: 0, lastPlayed: '从未运行', modsCount: 0,
    status: 'ready', isolatedDirectory: true,
    installationState: item.installationState as Instance['installationState'],
  }
}

async function call<T>(operation: () => Promise<T>): Promise<T> {
  try { return await operation() } catch (cause) { throw toAppError(cause) }
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
}
