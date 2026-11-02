/**
 * 账号子系统的服务封装层。
 *
 * 职责与其它 `services/*.ts` 一致：
 *  - 前端只有本文件可以直接 import 生成的 `AccountService` 绑定；
 *  - 所有异常统一归一化成 `AppError`，不把后端原始错误直接抛给界面；
 *  - 事件订阅只用于「推动刷新」，权威状态永远来自 `ListAccounts` 快照。
 *
 * 安全约定：账号 DTO 不含任何凭证字段；本层也不保存、不缓存凭证。
 */
import { Events } from '@wailsio/runtime'

import { AccountService } from '../../bindings/example.com/ninevoxel/launcher/internal/account'
import type { AccountChangedPayload } from '../../bindings/example.com/ninevoxel/launcher/internal/events/models'
import { AccountChangedV1 } from '../../bindings/events'
import type {
  AccountList,
  AccountView,
  LoginSessionView,
  ProviderView,
  SiteInfoView,
  StartLoginRequest,
} from '../types/account'
import type { EventEnvelopeBody } from '../types/events'
import { toAppError } from './errors'

/** 订阅函数返回的解绑函数。 */
export type Unsubscribe = () => void

/**
 * 统一执行一次绑定调用并归一化错误。
 *
 * 成功后原样返回；失败时抛 `AppError`（含稳定错误码与脱敏文案）。
 */
async function call<T>(operation: () => Promise<T>): Promise<T> {
  try {
    return await operation()
  } catch (cause) {
    throw toAppError(cause)
  }
}

/** 读取全部账号来源（微软/离线/Yggdrasil/Ninevoxel 占位）。 */
export async function fetchProviders(): Promise<ProviderView[]> {
  const list = await call(() => AccountService.ListProviders())
  return list.providers ?? []
}

/** 读取账号列表与当前选中账号（事件丢失后的权威快照）。 */
export async function fetchAccounts(): Promise<AccountList> {
  return call(() => AccountService.ListAccounts())
}

/** 开始一次登录并返回会话快照。 */
export async function startLogin(request: StartLoginRequest): Promise<LoginSessionView> {
  return call(() => AccountService.StartLogin(request))
}

/** 轮询一次登录会话。 */
export async function pollLogin(sessionId: string): Promise<LoginSessionView> {
  return call(() => AccountService.PollLogin({ sessionId }))
}

/** 为多档案账号选择档案。 */
export async function selectProfile(sessionId: string, profileId: string): Promise<LoginSessionView> {
  return call(() => AccountService.SelectProfile({ sessionId, profileId }))
}

/** 取消登录会话（幂等）。 */
export async function cancelLogin(sessionId: string): Promise<LoginSessionView> {
  return call(() => AccountService.CancelLogin({ sessionId }))
}

/** 切换当前账号。 */
export async function selectAccount(accountId: string): Promise<AccountList> {
  return call(() => AccountService.SelectAccount({ accountId }))
}

/** 删除账号（后端会尽力在服务端注销）。 */
export async function deleteAccount(accountId: string): Promise<AccountList> {
  return call(() => AccountService.DeleteAccount({ accountId }))
}

/** 刷新账号凭证。 */
export async function refreshAccount(accountId: string): Promise<AccountView> {
  return call(() => AccountService.RefreshAccount({ accountId }))
}

/** 解析自定义 Yggdrasil 地址（ALI 发现 + 元数据）。 */
export async function resolveYggdrasilSite(apiUrl: string): Promise<SiteInfoView> {
  return call(() => AccountService.ResolveYggdrasilSite({ apiUrl }))
}

/**
 * 订阅 `account.changed.v1`。
 *
 * 载荷只带动作与数量：收到事件后必须重新 `ListAccounts` 覆盖本地状态，
 * 不得把事件当作数据真相（事件可能乱序、重复或丢失）。
 *
 * @param handler 事件处理函数
 * @returns 幂等的解绑函数
 */
export function subscribeAccountChanged(
  handler: (event: EventEnvelopeBody<AccountChangedPayload>) => void,
): Unsubscribe {
  let active = true

  const release = Events.On(AccountChangedV1, (event) => {
    if (!active) {
      return
    }
    const data = event?.data as unknown as EventEnvelopeBody<AccountChangedPayload> | undefined
    if (!data || data.payload === undefined || data.payload === null) {
      // 载荷缺失时忽略：事件可能重复或损坏，界面不应因此报错。
      return
    }
    handler(data)
  })

  return () => {
    if (!active) {
      return
    }
    // 先置位再解绑：即使底层抛错也不会让解绑函数被重复执行。
    active = false
    release()
  }
}
