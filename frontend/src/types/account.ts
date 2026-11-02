/**
 * 账号子系统的前端类型契约。
 *
 * 数据形状全部来自 Go 侧生成的绑定（`frontend/bindings/.../account`），
 * 本文件只做两件事：
 *  1. 重新导出绑定类型，让页面/组件不必直接 import 生成目录（分层约定）；
 *  2. 定义界面使用的 `AccountsController` 接口，把「状态 + 操作」收敛成一个对象，
 *     App 与弹窗都只依赖这个接口，方便替换实现与单测。
 *
 * 安全约定：本文件与整个前端都**没有**凭证字段——后端 DTO 不含 token，
 * 登录成功后的凭证只存在于 Go 进程内。
 */
import type {
  AccountIDRequest,
  AccountList,
  AccountView,
  Capabilities,
  ChallengeView,
  LoginSessionRequest,
  LoginSessionView,
  ProfileView,
  ProviderList,
  ProviderView,
  SelectProfileRequest,
  SiteInfoView,
  StartLoginRequest,
} from '../../bindings/example.com/ninevoxel/launcher/internal/account'
import type { AppError } from './app'

export type {
  AccountIDRequest,
  AccountList,
  AccountView,
  Capabilities,
  ChallengeView,
  LoginSessionRequest,
  LoginSessionView,
  ProfileView,
  ProviderList,
  ProviderView,
  SelectProfileRequest,
  SiteInfoView,
  StartLoginRequest,
}

/** 账号来源类型的稳定取值（与 Go 侧 `account.Kind` 一致）。 */
export type AccountKind = 'offline' | 'microsoft' | 'yggdrasil' | 'ninevoxel'

/** 登录流程状态的稳定取值（与 Go 侧 `account.LoginState` 一致）。 */
export type LoginState =
  | 'pending'
  | 'awaiting_user'
  | 'awaiting_profile'
  | 'succeeded'
  | 'failed'
  | 'cancelled'

/** 账号来源类型的中文展示名（与 Go 侧 `Kind.DisplayName` 保持一致）。 */
export const ACCOUNT_KIND_LABELS: Record<AccountKind, string> = {
  microsoft: '微软账号',
  offline: '离线账号',
  yggdrasil: 'Yggdrasil 账号',
  ninevoxel: 'Ninevoxel 账号',
}

/** 登录状态的中文说明，用于设备码轮询与失败提示。 */
export const LOGIN_STATE_LABELS: Record<LoginState, string> = {
  pending: '正在连接账号服务…',
  awaiting_user: '等待浏览器授权',
  awaiting_profile: '请选择游戏档案',
  succeeded: '登录成功',
  failed: '登录失败',
  cancelled: '已取消登录',
}

/** 判断登录流程是否已结束（终态）。 */
export function isLoginTerminal(state: string): boolean {
  return state === 'succeeded' || state === 'failed' || state === 'cancelled'
}

/**
 * 账号控制器：账号列表、登录流程与全部操作。
 *
 * 由 `useAccounts` 实现；App 把它传给账号弹窗与侧栏。
 * 所有操作失败时不会抛异常，而是写入 `error`，由界面统一展示。
 */
export interface AccountsController {
  /** 账号来源列表（按后端注册顺序）。 */
  providers: ProviderView[]

  /** 已保存账号（当前选中账号在最前）。 */
  accounts: AccountView[]

  /** 当前选中账号 id；没有账号时为空字符串。 */
  selectedAccountId: string

  /** 当前选中账号；没有时 null。 */
  currentAccount: AccountView | null

  /** 列表/来源是否正在加载。 */
  loading: boolean

  /** 最近一次操作错误；成功后清空。 */
  error: AppError | null

  /** 当前登录会话（没有进行中的登录时 null）。 */
  login: LoginSessionView | null

  /** 是否正在发起/轮询登录。 */
  loginBusy: boolean

  /** 清空错误提示。 */
  clearError: () => void

  /** 重新加载来源与账号列表。 */
  reload: () => Promise<void>

  /** 开始登录（离线立即完成；微软返回设备码）。 */
  startLogin: (request: StartLoginRequest) => Promise<void>

  /** 主动轮询一次登录（通常由 hook 自动轮询，弹窗也可手动重试）。 */
  pollLogin: () => Promise<void>

  /** 为多档案账号选择档案。 */
  selectProfile: (profileId: string) => Promise<void>

  /** 取消当前登录流程。 */
  cancelLogin: () => Promise<void>

  /** 切换当前账号。 */
  selectAccount: (accountId: string) => Promise<void>

  /** 删除账号（后端会尽力注销）。 */
  deleteAccount: (accountId: string) => Promise<void>

  /** 刷新账号凭证。 */
  refreshAccount: (accountId: string) => Promise<void>

  /** 解析自定义 Yggdrasil 地址（提交前校验并回显服务名）。 */
  resolveSite: (apiUrl: string) => Promise<SiteInfoView | null>
}
