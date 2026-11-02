/**
 * 账号管理弹窗（真实后端版）。
 *
 * 设计约束：
 *  - 本组件只渲染 `AccountsController` 提供的数据，不持有任何账号/设备码/凭证；
 *    旧版的演示账号、写死设备码与定时器模拟登录全部删除。
 *  - 所有操作都调用 controller 的真实方法；失败由 controller 写入 `error`，
 *    这里只负责用红色 banner 展示，不自己编造成功态。
 *  - 密码只在用户输入到 `startLogin` 调用的瞬间存在于组件 state，
 *    发起登录后立即清空；切换来源时也清空。
 *  - 所有 hooks 必须在 `if (!isOpen) return null` 之前：弹窗常驻挂载、
 *    靠 isOpen 控制显隐，提前 return 放在 hooks 之后才不会改变 hook 调用数量。
 */
import React, { useEffect, useState } from 'react';
import { Browser } from '@wailsio/runtime';
import { AccountAvatar } from './AccountAvatar';
import {
  AlertCircle,
  Check,
  CheckCircle2,
  Clock,
  Copy,
  ExternalLink,
  Globe,
  KeyRound,
  Loader2,
  LogIn,
  RefreshCw,
  Server,
  Sparkles,
  Trash2,
  User,
  UserPlus,
  Users,
  X,
} from 'lucide-react';
import {
  ACCOUNT_KIND_LABELS,
  LOGIN_STATE_LABELS,
  isLoginTerminal,
} from '../types/account';
import type {
  AccountKind,
  AccountView,
  AccountsController,
  LoginSessionView,
  LoginState,
  SiteInfoView,
} from '../types/account';

interface AccountManagerModalProps {
  isOpen: boolean;
  onClose: () => void;
  controller: AccountsController;
}

/** 离线玩家名规则：3-16 位字母、数字或下划线（与后端离线来源的输入校验一致）。 */
const OFFLINE_NAME_PATTERN = /^[A-Za-z0-9_]{3,16}$/;

/** 各账号来源的视觉色调（头像兜底与来源标签共用）。 */
const KIND_TONES: Record<
  AccountKind,
  { avatar: string; chip: string }
> = {
  microsoft: {
    avatar:
      'bg-emerald-500/15 text-emerald-700 dark:text-emerald-300 border-emerald-500/30',
    chip:
      'bg-emerald-500/15 text-emerald-700 dark:text-emerald-300 border border-emerald-500/30',
  },
  offline: {
    avatar:
      'bg-slate-500/15 text-slate-700 dark:text-neutral-300 border-slate-400/30',
    chip:
      'bg-slate-200 dark:bg-neutral-800 text-slate-600 dark:text-neutral-400 border border-slate-300 dark:border-neutral-700',
  },
  yggdrasil: {
    avatar:
      'bg-indigo-500/15 text-indigo-700 dark:text-indigo-300 border-indigo-500/30',
    chip:
      'bg-indigo-500/15 text-indigo-700 dark:text-indigo-300 border border-indigo-500/30',
  },
  ninevoxel: {
    avatar:
      'bg-amber-500/15 text-amber-700 dark:text-amber-300 border-amber-500/30',
    chip:
      'bg-amber-500/15 text-amber-700 dark:text-amber-300 border border-amber-500/30',
  },
};

/** 判断后端的来源字符串是否为已知的账号类型。 */
function isAccountKind(kind: string): kind is AccountKind {
  return (
    kind === 'offline' ||
    kind === 'microsoft' ||
    kind === 'yggdrasil' ||
    kind === 'ninevoxel'
  );
}

/** 来源类型的中文名；未知来源原样回显，便于发现后端新增类型。 */
function accountKindLabel(kind: string): string {
  return isAccountKind(kind) ? ACCOUNT_KIND_LABELS[kind] : kind;
}

/** 登录状态的中文说明；未知状态原样回显。 */
function loginStateLabel(state: string): string {
  return LOGIN_STATE_LABELS[state as LoginState] ?? state;
}

/**
 * 取 Yggdrasil 账号 `providerId`（API 根）的 host。
 *
 * providerId 是后端下发的 API 根地址，解析失败时原样返回，
 * 绝不猜测或拼接地址。
 */
function providerHost(providerId: string): string {
  if (!providerId) {
    return '';
  }
  try {
    return new URL(providerId).host;
  } catch {
    return providerId;
  }
}

/** 把设备码剩余毫秒格式化为 m:ss；已过期返回空串由调用方决定文案。 */
function formatCountdown(remainingMs: number): string {
  const totalSeconds = Math.max(0, Math.floor(remainingMs / 1000));
  const minutes = Math.floor(totalSeconds / 60);
  const seconds = totalSeconds % 60;
  return `${minutes}:${String(seconds).padStart(2, '0')}`;
}

/** 来源图标。 */
function ProviderIcon({
  kind,
  className,
}: {
  kind: AccountKind | null;
  className?: string;
}) {
  if (kind === 'microsoft') {
    return <Globe className={className} aria-hidden="true" />;
  }
  if (kind === 'offline') {
    return <User className={className} aria-hidden="true" />;
  }
  if (kind === 'yggdrasil') {
    return <Server className={className} aria-hidden="true" />;
  }
  if (kind === 'ninevoxel') {
    return <Sparkles className={className} aria-hidden="true" />;
  }
  return <Users className={className} aria-hidden="true" />;
}

export const AccountManagerModal: React.FC<AccountManagerModalProps> = ({
  isOpen,
  onClose,
  controller,
}) => {
  // ===== 所有 hooks 必须在提前 return 之前（弹窗常驻挂载，isOpen 只控制显隐）=====
  /** 当前选中的「添加账号」来源；null 表示跟随 providers 的默认第一项。 */
  const [activeKind, setActiveKind] = useState<AccountKind | null>(null);
  /** 离线账号玩家名。 */
  const [offlineName, setOfflineName] = useState('');
  /** Yggdrasil API 地址。 */
  const [yggUrl, setYggUrl] = useState('');
  /** Yggdrasil 用户名/邮箱。 */
  const [yggUser, setYggUser] = useState('');
  /** Yggdrasil 密码：只在发起登录前短暂存在，发起后立即清空。 */
  const [yggPass, setYggPass] = useState('');
  /** 解析成功的站点信息（含规范化 API 根与服务名）。 */
  const [siteInfo, setSiteInfo] = useState<SiteInfoView | null>(null);
  /** 站点解析进行中。 */
  const [resolving, setResolving] = useState(false);
  /** 正在刷新凭证的账号 id。 */
  const [refreshingId, setRefreshingId] = useState<string | null>(null);
  /** 正在删除的账号 id。 */
  const [deletingId, setDeletingId] = useState<string | null>(null);
  /** 正在切换为当前账号的 id。 */
  const [selectingId, setSelectingId] = useState<string | null>(null);
  /** 等待二次确认删除的账号 id（不使用 window.confirm）。 */
  const [pendingDeleteId, setPendingDeleteId] = useState<string | null>(null);
  /** 最近一次复制成功的设备码内容（与当前设备码比对，天然随设备码变化而复位）。 */
  const [copiedCode, setCopiedCode] = useState('');
  /**
   * 最近一次发起的微软登录方式：
   * 默认 PKCE（系统浏览器 + 本地回环），失败可回退设备码。
   * 仅用于失败后「重试」沿用同一方式、以及切换按钮的文案。
   */
  const [microsoftLoginMode, setMicrosoftLoginMode] = useState<
    'pkce' | 'device_code'
  >('pkce');
  /** 设备码倒计时用的本地时钟。 */
  const [nowMs, setNowMs] = useState(() => Date.now());

  /** 登录状态与过期时间提成变量，供倒计时 effect 做依赖。 */
  const loginState = controller.login?.state ?? '';
  const challengeExpiresAtMs = controller.login?.challenge?.expiresAtMs ?? 0;

  // 设备码倒计时：只在「等待用户授权」时每秒推进一次；关闭弹窗后停止。
  useEffect(() => {
    if (!isOpen || loginState !== 'awaiting_user') {
      return;
    }
    setNowMs(Date.now());
    const timer = window.setInterval(() => setNowMs(Date.now()), 1000);
    return () => {
      window.clearInterval(timer);
    };
  }, [isOpen, loginState, challengeExpiresAtMs]);

  // 提前返回必须在所有 hooks 之后：否则弹窗开合会改变 hook 调用数量，
  // React 会抛出「Rendered more hooks than during the previous render」。
  if (!isOpen) return null;

  // ===== 派生状态（不调用任何 hook）=====
  /** providers 中第一个可用来源，作为「添加账号」的默认标签。 */
  const defaultProvider =
    controller.providers.find((provider) => provider.implemented) ??
    controller.providers[0] ??
    null;
  const defaultKind: AccountKind | null =
    defaultProvider && isAccountKind(defaultProvider.kind)
      ? defaultProvider.kind
      : null;
  const effectiveKind: AccountKind | null = activeKind ?? defaultKind;
  const activeProvider =
    controller.providers.find((provider) => provider.kind === effectiveKind) ??
    null;
  /** 与当前来源匹配的登录会话；其它来源的会话不在本标签页展示。 */
  const kindLogin: LoginSessionView | null =
    controller.login &&
    effectiveKind &&
    controller.login.kind === effectiveKind
      ? controller.login
      : null;

  const offlineNameTrimmed = offlineName.trim();
  const offlineNameValid = OFFLINE_NAME_PATTERN.test(offlineNameTrimmed);
  const offlineNameError =
    offlineNameTrimmed.length === 0
      ? null
      : offlineNameValid
        ? null
        : '玩家名需为 3-16 位字母、数字或下划线';
  const yggReady =
    siteInfo !== null && yggUser.trim().length > 0 && yggPass.length > 0;
  const copiedUserCode =
    (kindLogin?.challenge?.userCode ?? '') !== '' &&
    copiedCode === kindLogin?.challenge?.userCode;

  /** 来源能力：offline 明确不支持刷新；其余以后端 Capabilities 为准。 */
  const canRefreshAccount = (account: AccountView): boolean => {
    if (account.kind === 'offline') {
      return false;
    }
    const provider =
      controller.providers.find((item) => item.kind === account.kind) ?? null;
    return provider?.capabilities?.refresh ?? true;
  };

  // ===== 操作（全部调用 controller 的真实方法，失败由 controller.error 呈现）=====
  const handleSelectKind = (kind: AccountKind) => {
    setActiveKind(kind);
    // 切换来源时清空上一个来源的密码输入。
    setYggPass('');
  };

  /**
   * 以 PKCE 方式开始微软登录（默认路径）：
   * 后端会打开系统浏览器并监听本地回环，不传 loginMode 即为 PKCE。
   */
  const startMicrosoftPkce = async () => {
    setMicrosoftLoginMode('pkce');
    await controller.startLogin({
      kind: 'microsoft',
      username: '',
      password: '',
      apiUrl: '',
    });
  };

  /** 以设备码方式开始微软登录（浏览器授权不可用时的回退路径）。 */
  const startMicrosoftDeviceCode = async () => {
    setMicrosoftLoginMode('device_code');
    await controller.startLogin({
      kind: 'microsoft',
      username: '',
      password: '',
      apiUrl: '',
      loginMode: 'device_code',
    });
  };

  /**
   * 显式串行切换微软登录方式：先取消旧会话，再发起新会话。
   *
   * hook 在 startLogin 里也会兜底取消未完成会话，但这里显式取消更稳：
   * 避免旧的本地回环监听/轮询在切换瞬间残留。
   */
  const handleSwitchMicrosoftMode = async (
    mode: 'pkce' | 'device_code',
  ) => {
    setCopiedCode('');
    await controller.cancelLogin();
    if (mode === 'pkce') {
      await startMicrosoftPkce();
    } else {
      await startMicrosoftDeviceCode();
    }
  };

  const handleStartOffline = async () => {
    if (!offlineNameValid) {
      return;
    }
    await controller.startLogin({
      kind: 'offline',
      username: offlineNameTrimmed,
      password: '',
      apiUrl: '',
    });
  };

  const handleResolveSite = async () => {
    const raw = yggUrl.trim();
    if (raw.length === 0 || resolving) {
      return;
    }
    setResolving(true);
    setSiteInfo(null);
    try {
      // resolveSite 失败时返回 null，并把 AppError 写入 controller.error。
      const info = await controller.resolveSite(raw);
      setSiteInfo(info);
    } finally {
      setResolving(false);
    }
  };

  const handleStartYggdrasil = async () => {
    if (!siteInfo || yggUser.trim().length === 0 || yggPass.length === 0) {
      return;
    }
    // 密码只在这一行到 startLogin 之间存在：发起登录后立即从组件 state 清空。
    const password = yggPass;
    setYggPass('');
    try {
      await controller.startLogin({
        kind: 'yggdrasil',
        username: yggUser.trim(),
        password,
        apiUrl: siteInfo.apiUrl,
      });
    } finally {
      setYggPass('');
    }
  };

  /** 失败重试：微软沿用上一次的登录方式；离线直接重发；Yggdrasil 回表单重填。 */
  const handleRetryLogin = async () => {
    if (effectiveKind === 'microsoft') {
      // 失败后重试沿用同一方式（PKCE 仍走浏览器，设备码仍走设备码）。
      await handleSwitchMicrosoftMode(microsoftLoginMode);
      return;
    }
    if (effectiveKind === 'offline') {
      await controller.cancelLogin();
      await handleStartOffline();
      return;
    }
    // Yggdrasil：清掉失败的会话，让用户重新输入密码后再登录。
    await controller.cancelLogin();
  };

  const handleCancelLogin = async () => {
    setCopiedCode('');
    await controller.cancelLogin();
  };

  const handleSelectAccount = async (accountId: string) => {
    setSelectingId(accountId);
    try {
      await controller.selectAccount(accountId);
    } finally {
      setSelectingId(null);
    }
  };

  const handleRefreshAccount = async (accountId: string) => {
    setRefreshingId(accountId);
    try {
      await controller.refreshAccount(accountId);
    } finally {
      setRefreshingId(null);
    }
  };

  const handleDeleteAccount = async (accountId: string) => {
    setDeletingId(accountId);
    try {
      await controller.deleteAccount(accountId);
    } finally {
      setDeletingId(null);
      setPendingDeleteId(null);
    }
  };

  const handleCopyUserCode = async (code: string) => {
    if (code.length === 0) {
      return;
    }
    try {
      await navigator.clipboard.writeText(code);
      setCopiedCode(code);
    } catch {
      // 剪贴板不可用时保持原样，不伪造「已复制」。
    }
  };

  const handleOpenVerification = (uri: string) => {
    if (uri.length === 0) {
      return;
    }
    // 交给系统默认浏览器打开后端下发的真实授权地址。
    void Browser.OpenURL(uri).catch(() => {
      // 纯浏览器 dev 环境没有 Wails 运行时，静默忽略即可。
    });
  };

  // ===== 渲染片段 =====
  /** 头像：用 AccountAvatar 从皮肤纹理裁剪出头部；无皮肤时显示来源色块 + 首字母。 */
  const renderAvatar = (
    name: string,
    skinUrl: string | undefined,
    avatarClass: string,
    pixelSize = 40,
    textClass = 'text-xs',
  ) => (
    <AccountAvatar
      name={name}
      skinUrl={skinUrl}
      size={pixelSize}
      className={`rounded-lg border ${avatarClass} ${textClass}`}
    />
  );

  const renderAccountRow = (account: AccountView) => {
    const selected =
      account.id === controller.selectedAccountId || account.selected;
    const tone = isAccountKind(account.kind)
      ? KIND_TONES[account.kind]
      : KIND_TONES.offline;
    const host =
      account.kind === 'yggdrasil' ? providerHost(account.providerId) : '';
    const isConfirmingDelete = pendingDeleteId === account.id;
    const isRefreshing = refreshingId === account.id;
    const isDeleting = deletingId === account.id;
    const isSelecting = selectingId === account.id;

    return (
      <div
        key={account.id}
        className={`rounded-xl border transition-colors ${
          selected
            ? 'bg-emerald-500/5 dark:bg-neutral-950 border-emerald-500/50 ring-1 ring-emerald-500/20'
            : 'bg-slate-50 dark:bg-neutral-950/40 border-slate-200 dark:border-neutral-800'
        }`}
      >
        <div className="p-3 flex items-start gap-3">
          {renderAvatar(
            account.displayName,
            account.profile?.skinUrl,
            tone.avatar,
          )}
          <div className="min-w-0 flex-1">
            <div className="flex items-center gap-1.5 flex-wrap">
              <span
                className="text-xs font-bold text-slate-900 dark:text-neutral-100 truncate max-w-[12rem]"
                title={account.displayName}
              >
                {account.displayName}
              </span>
              <span
                className={`text-[10px] font-mono px-1.5 py-0.5 rounded ${tone.chip}`}
              >
                {accountKindLabel(account.kind)}
              </span>
              {selected && (
                <span className="text-[10px] font-semibold text-emerald-700 dark:text-emerald-300 bg-emerald-500/15 border border-emerald-500/30 px-1.5 py-0.5 rounded flex items-center gap-1">
                  <CheckCircle2 className="w-2.5 h-2.5" aria-hidden="true" />
                  当前账号
                </span>
              )}
            </div>
            <div className="text-[11px] text-slate-500 dark:text-neutral-500 font-mono mt-0.5 truncate">
              {host
                ? `站点 ${host}`
                : account.profile?.name
                  ? `档案 ${account.profile.name}`
                  : `档案 ${account.profile?.id ?? '未知'}`}
            </div>
          </div>
        </div>

        <div className="px-3 pb-3 flex flex-wrap items-center gap-1.5">
          <button
            type="button"
            onClick={() => void handleSelectAccount(account.id)}
            disabled={selected || isSelecting || isDeleting}
            aria-label={
              selected ? `当前账号 ${account.displayName}` : `设为当前账号 ${account.displayName}`
            }
            title={selected ? '已经是当前账号' : '把该账号设为当前账号'}
            className="px-2 py-1 rounded-md text-[11px] font-medium flex items-center gap-1 transition-colors cursor-pointer bg-emerald-500/15 hover:bg-emerald-500 text-emerald-700 dark:text-emerald-300 hover:text-white dark:hover:text-neutral-950 disabled:opacity-50 disabled:cursor-not-allowed disabled:hover:bg-emerald-500/15 disabled:hover:text-emerald-700 dark:disabled:hover:text-emerald-300"
          >
            {isSelecting ? (
              <Loader2
                className="w-3 h-3 animate-spin"
                aria-hidden="true"
              />
            ) : (
              <Check className="w-3 h-3" aria-hidden="true" />
            )}
            <span>{selected ? '当前账号' : isSelecting ? '切换中' : '设为当前'}</span>
          </button>

          {canRefreshAccount(account) && (
            <button
              type="button"
              onClick={() => void handleRefreshAccount(account.id)}
              disabled={isRefreshing || isDeleting}
              aria-label={`刷新账号凭证 ${account.displayName}`}
              title="调用后端刷新账号凭证"
              className="px-2 py-1 rounded-md text-[11px] font-medium flex items-center gap-1 transition-colors cursor-pointer bg-slate-100 dark:bg-neutral-800 hover:bg-slate-200 dark:hover:bg-neutral-700 text-slate-700 dark:text-neutral-300 disabled:opacity-50 disabled:cursor-not-allowed"
            >
              <RefreshCw
                className={`w-3 h-3 ${isRefreshing ? 'animate-spin' : ''}`}
                aria-hidden="true"
              />
              <span>{isRefreshing ? '刷新中' : '刷新'}</span>
            </button>
          )}

          <button
            type="button"
            onClick={() => setPendingDeleteId(account.id)}
            disabled={isDeleting}
            aria-label={`删除账号 ${account.displayName}`}
            title="从本地移除该账号（服务端凭证尽力注销）"
            className="px-2 py-1 rounded-md text-[11px] font-medium flex items-center gap-1 transition-colors cursor-pointer text-slate-500 dark:text-neutral-400 hover:text-rose-600 dark:hover:text-rose-400 hover:bg-rose-500/10 disabled:opacity-50 disabled:cursor-not-allowed"
          >
            <Trash2 className="w-3 h-3" aria-hidden="true" />
            <span>删除</span>
          </button>
        </div>

        {isConfirmingDelete && (
          <div className="mx-3 mb-3 p-2.5 rounded-lg border border-rose-500/40 bg-rose-500/10 space-y-2">
            <p className="text-[11px] text-rose-700 dark:text-rose-300 leading-relaxed">
              确认删除「{account.displayName}」？本地记录会被移除，服务端凭证只做尽力注销。
            </p>
            <div className="flex items-center gap-2">
              <button
                type="button"
                onClick={() => void handleDeleteAccount(account.id)}
                disabled={isDeleting}
                aria-label={`确认删除账号 ${account.displayName}`}
                className="px-2.5 py-1 rounded-md text-[11px] font-bold text-white bg-rose-600 hover:bg-rose-500 transition-colors cursor-pointer disabled:opacity-50 disabled:cursor-not-allowed"
              >
                {isDeleting ? '删除中…' : '确认删除'}
              </button>
              <button
                type="button"
                onClick={() => setPendingDeleteId(null)}
                disabled={isDeleting}
                className="px-2.5 py-1 rounded-md text-[11px] font-semibold bg-slate-200 dark:bg-neutral-800 text-slate-700 dark:text-neutral-300 hover:bg-slate-300 dark:hover:bg-neutral-700 transition-colors cursor-pointer disabled:opacity-50 disabled:cursor-not-allowed"
              >
                取消
              </button>
            </div>
          </div>
        )}
      </div>
    );
  };

  const renderProviderTabs = () => (
    <div className="space-y-2">
      <div
        role="tablist"
        aria-label="账号来源"
        className="flex flex-wrap gap-1.5"
      >
        {controller.providers.map((provider) => {
          const kind = isAccountKind(provider.kind) ? provider.kind : null;
          const active = kind !== null && kind === effectiveKind;
          const disabled = !provider.implemented;
          return (
            <button
              key={provider.kind}
              type="button"
              role="tab"
              aria-selected={active}
              aria-label={`${provider.displayName}${disabled ? '（即将上线）' : ''}`}
              title={provider.description}
              disabled={disabled}
              onClick={() => {
                if (!kind || disabled) {
                  return;
                }
                handleSelectKind(kind);
              }}
              className={`px-2.5 py-1.5 rounded-lg text-[11px] font-semibold flex items-center gap-1.5 border transition-colors cursor-pointer disabled:opacity-50 disabled:cursor-not-allowed ${
                active
                  ? 'bg-emerald-500/15 border-emerald-500/50 text-emerald-700 dark:text-emerald-300'
                  : 'bg-slate-100 dark:bg-neutral-800 border-slate-200 dark:border-neutral-700 text-slate-600 dark:text-neutral-400 hover:text-slate-900 dark:hover:text-neutral-200'
              }`}
            >
              <ProviderIcon kind={kind} className="w-3.5 h-3.5" />
              <span>{provider.displayName}</span>
              {disabled && (
                <span className="text-[9px] font-mono px-1 py-0.5 rounded bg-amber-500/15 text-amber-700 dark:text-amber-300">
                  即将上线
                </span>
              )}
            </button>
          );
        })}
      </div>
      {activeProvider && (
        <p className="text-[10px] text-slate-500 dark:text-neutral-500 leading-relaxed">
          {activeProvider.description}
        </p>
      )}
    </div>
  );

  const renderLoginFlow = (session: LoginSessionView) => {
    const state = session.state;
    const statusLabel = loginStateLabel(state);
    const terminal = isLoginTerminal(state);
    const pollSeconds = session.challenge?.intervalSeconds ?? 0;
    const expiresAtMs = session.challenge?.expiresAtMs ?? 0;
    const remainingMs = expiresAtMs > 0 ? expiresAtMs - nowMs : 0;

    const statusBadge = (
      <div className="flex items-center justify-between gap-2">
        <span
          className={`text-[10px] font-semibold px-2 py-0.5 rounded-full border ${
            state === 'failed'
              ? 'text-rose-700 dark:text-rose-300 bg-rose-500/10 border-rose-500/30'
              : state === 'succeeded'
                ? 'text-emerald-700 dark:text-emerald-300 bg-emerald-500/10 border-emerald-500/30'
                : 'text-slate-700 dark:text-neutral-300 bg-slate-200/70 dark:bg-neutral-800 border-slate-300 dark:border-neutral-700'
          }`}
        >
          状态：{statusLabel}
          {terminal ? '（流程已结束）' : ''}
        </span>
        {controller.loginBusy && (
          <span className="text-[10px] text-slate-500 dark:text-neutral-400 flex items-center gap-1">
            <Loader2
              className="w-3 h-3 animate-spin"
              aria-hidden="true"
            />
            与账号服务通信中…
          </span>
        )}
      </div>
    );

    // 等待用户授权：微软有两种方式。
    //   - challenge.userCode 为空：PKCE（系统浏览器 + 本地回环），后端已自动打开浏览器；
    //   - challenge.userCode 非空：设备码（回退方式），提示用户在浏览器输入设备码。
    if (state === 'awaiting_user') {
      const userCode = session.challenge?.userCode ?? '';
      const verificationUri = session.challenge?.verificationUri ?? '';

      // PKCE：只等待浏览器授权结果，同时提供手动重开授权页与回退设备码的入口。
      if (userCode.length === 0) {
        return (
          <div className="space-y-3">
            {statusBadge}

            <div className="p-3 rounded-xl bg-slate-50 dark:bg-neutral-950/60 border border-slate-200 dark:border-neutral-800 space-y-1.5">
              <div className="flex items-center gap-1.5 text-xs font-semibold text-slate-800 dark:text-neutral-200">
                <Globe
                  className="w-3.5 h-3.5 text-emerald-500 dark:text-emerald-400"
                  aria-hidden="true"
                />
                浏览器授权中
              </div>
              <p className="text-[11px] text-slate-500 dark:text-neutral-400 leading-relaxed">
                {(session.challenge?.message ?? '').length > 0
                  ? session.challenge.message
                  : '浏览器已自动打开授权页面，完成登录后会自动返回本启动器。'}
              </p>
            </div>

            {verificationUri.length > 0 && (
              <div className="p-3 rounded-xl bg-slate-50 dark:bg-neutral-950/60 border border-slate-200 dark:border-neutral-800 space-y-1.5">
                <div className="text-[10px] font-semibold text-slate-500 dark:text-neutral-500">
                  授权地址
                </div>
                <div className="text-[11px] font-mono text-slate-700 dark:text-neutral-300 break-all select-all">
                  {verificationUri}
                </div>
                <p className="text-[10px] text-slate-500 dark:text-neutral-500 leading-relaxed">
                  若浏览器没有自动打开，可点下方「重新打开登录页」。
                </p>
              </div>
            )}

            <div className="flex items-center gap-1.5 text-[11px] text-slate-600 dark:text-neutral-400">
              <Clock className="w-3.5 h-3.5 shrink-0" aria-hidden="true" />
              {remainingMs > 0 ? (
                <span>
                  授权链接剩余有效期{' '}
                  <span className="font-mono font-semibold text-slate-900 dark:text-neutral-100">
                    {formatCountdown(remainingMs)}
                  </span>
                </span>
              ) : (
                <span className="text-rose-600 dark:text-rose-400">
                  授权链接可能已过期，请取消后重新开始登录
                </span>
              )}
            </div>

            <button
              type="button"
              onClick={() => handleOpenVerification(verificationUri)}
              disabled={verificationUri.length === 0}
              aria-label="重新打开登录页"
              title="用系统默认浏览器重新打开 Microsoft 授权页面"
              className="w-full py-2.5 rounded-xl bg-emerald-500 hover:bg-emerald-400 text-neutral-950 font-bold text-xs flex items-center justify-center gap-2 transition-colors cursor-pointer shadow-sm disabled:opacity-50 disabled:cursor-not-allowed"
            >
              <ExternalLink className="w-3.5 h-3.5" aria-hidden="true" />
              <span>重新打开登录页</span>
            </button>

            <button
              type="button"
              onClick={() => void handleSwitchMicrosoftMode('device_code')}
              disabled={controller.loginBusy}
              aria-label="改用设备码登录"
              title="取消本次浏览器授权，改用设备码登录"
              className="w-full py-2 rounded-lg border border-slate-300 dark:border-neutral-700 bg-white dark:bg-neutral-900 hover:bg-slate-100 dark:hover:bg-neutral-800 text-[11px] font-semibold text-slate-700 dark:text-neutral-300 flex items-center justify-center gap-1.5 transition-colors cursor-pointer disabled:opacity-50 disabled:cursor-not-allowed"
            >
              <KeyRound className="w-3.5 h-3.5" aria-hidden="true" />
              <span>改用设备码登录</span>
            </button>

            <button
              type="button"
              onClick={() => void handleCancelLogin()}
              disabled={controller.loginBusy}
              aria-label="取消登录"
              className="w-full py-2 rounded-lg bg-slate-200 dark:bg-neutral-800 hover:bg-slate-300 dark:hover:bg-neutral-700 text-xs font-semibold text-slate-800 dark:text-neutral-200 transition-colors cursor-pointer disabled:opacity-50 disabled:cursor-not-allowed"
            >
              取消登录
            </button>

            <p className="text-[11px] text-slate-500 dark:text-neutral-400 flex items-center gap-1.5">
              <Loader2
                className="w-3.5 h-3.5 animate-spin text-emerald-500 dark:text-emerald-400"
                aria-hidden="true"
              />
              正在等待浏览器授权结果
              {pollSeconds > 0 ? `（约 ${pollSeconds} 秒轮询一次）` : ''}
              ，无需手动刷新
            </p>
          </div>
        );
      }

      // 设备码（回退）：保持原有完整面板。
      return (
        <div className="space-y-3">
          {statusBadge}
          <p className="text-[11px] text-slate-600 dark:text-neutral-400 leading-relaxed">
            请在浏览器中打开下方授权页面，并输入这个一次性设备码：
          </p>

          <div className="flex items-center justify-between gap-2 p-3 rounded-xl bg-white dark:bg-neutral-950 border border-slate-300 dark:border-neutral-700">
            <span
              className="font-mono text-xl font-bold tracking-[0.18em] text-emerald-600 dark:text-emerald-400 select-all break-all"
              title="点击可全选设备码"
            >
              {userCode || '后端未返回设备码'}
            </span>
            <button
              type="button"
              onClick={() => void handleCopyUserCode(userCode)}
              disabled={userCode.length === 0}
              aria-label="复制设备码"
              title="复制设备码"
              className="shrink-0 px-2.5 py-1.5 rounded-lg bg-slate-100 dark:bg-neutral-800 hover:bg-slate-200 dark:hover:bg-neutral-700 text-[11px] text-slate-700 dark:text-neutral-200 flex items-center gap-1.5 transition-colors cursor-pointer disabled:opacity-50 disabled:cursor-not-allowed"
            >
              {copiedUserCode ? (
                <Check
                  className="w-3.5 h-3.5 text-emerald-600 dark:text-emerald-400"
                  aria-hidden="true"
                />
              ) : (
                <Copy className="w-3.5 h-3.5" aria-hidden="true" />
              )}
              <span>{copiedUserCode ? '已复制' : '复制'}</span>
            </button>
          </div>

          {verificationUri.length > 0 && (
            <div className="p-3 rounded-xl bg-slate-50 dark:bg-neutral-950/60 border border-slate-200 dark:border-neutral-800 space-y-1.5">
              <div className="text-[10px] font-semibold text-slate-500 dark:text-neutral-500">
                授权地址
              </div>
              <div className="text-[11px] font-mono text-slate-700 dark:text-neutral-300 break-all select-all">
                {verificationUri}
              </div>
              <button
                type="button"
                onClick={() => handleOpenVerification(verificationUri)}
                aria-label="用浏览器打开授权页面"
                title="用系统默认浏览器打开授权页面"
                className="px-2.5 py-1.5 rounded-lg bg-emerald-500/15 hover:bg-emerald-500 text-emerald-700 dark:text-emerald-300 hover:text-white dark:hover:text-neutral-950 text-[11px] font-semibold flex items-center gap-1.5 transition-colors cursor-pointer"
              >
                <ExternalLink className="w-3.5 h-3.5" aria-hidden="true" />
                <span>打开浏览器</span>
              </button>
            </div>
          )}

          <div className="flex items-center gap-1.5 text-[11px] text-slate-600 dark:text-neutral-400">
            <Clock className="w-3.5 h-3.5 shrink-0" aria-hidden="true" />
            {remainingMs > 0 ? (
              <span>
                设备码剩余有效期{' '}
                <span className="font-mono font-semibold text-slate-900 dark:text-neutral-100">
                  {formatCountdown(remainingMs)}
                </span>
              </span>
            ) : (
              <span className="text-rose-600 dark:text-rose-400">
                设备码可能已过期，请取消后重新开始登录
              </span>
            )}
          </div>

          {(session.challenge?.message ?? '').length > 0 && (
            <p className="text-[10px] text-slate-500 dark:text-neutral-500 leading-relaxed">
              {session.challenge.message}
            </p>
          )}

          <p className="text-[11px] text-slate-500 dark:text-neutral-400 flex items-center gap-1.5">
            <Loader2
              className="w-3.5 h-3.5 animate-spin text-emerald-500 dark:text-emerald-400"
              aria-hidden="true"
            />
            正在轮询授权结果
            {pollSeconds > 0 ? `（约 ${pollSeconds} 秒一次）` : ''}
            ，无需手动刷新
          </p>

          <button
            type="button"
            onClick={() => void handleSwitchMicrosoftMode('pkce')}
            disabled={controller.loginBusy}
            aria-label="改用浏览器登录"
            title="取消当前设备码登录，改用系统浏览器授权"
            className="w-full py-1.5 text-[10px] text-slate-500 dark:text-neutral-500 hover:text-emerald-600 dark:hover:text-emerald-400 hover:underline cursor-pointer disabled:opacity-50 disabled:cursor-not-allowed"
          >
            改用浏览器登录
          </button>

          <button
            type="button"
            onClick={() => void handleCancelLogin()}
            disabled={controller.loginBusy}
            aria-label="取消登录"
            className="w-full py-2 rounded-lg bg-slate-200 dark:bg-neutral-800 hover:bg-slate-300 dark:hover:bg-neutral-700 text-xs font-semibold text-slate-800 dark:text-neutral-200 transition-colors cursor-pointer disabled:opacity-50 disabled:cursor-not-allowed"
          >
            取消登录
          </button>
        </div>
      );
    }

    // 等待选择游戏档案。
    if (state === 'awaiting_profile') {
      const profiles = session.profiles ?? [];
      return (
        <div className="space-y-3">
          {statusBadge}
          <p className="text-[11px] text-slate-600 dark:text-neutral-400 leading-relaxed">
            该账号下检测到多个游戏档案，请选择本次登录要使用的档案：
          </p>
          <div className="space-y-1.5">
            {profiles.map((profile) => (
              <button
                key={profile.id}
                type="button"
                onClick={() => void controller.selectProfile(profile.id)}
                disabled={controller.loginBusy}
                aria-label={`选择游戏档案 ${profile.name}`}
                title={`档案 UUID：${profile.id}`}
                className="w-full p-2.5 rounded-xl border border-slate-200 dark:border-neutral-800 bg-slate-50 dark:bg-neutral-950/40 hover:border-emerald-500/50 hover:bg-emerald-500/5 flex items-center gap-3 text-left transition-colors cursor-pointer disabled:opacity-50 disabled:cursor-not-allowed"
              >
                {renderAvatar(
                  profile.name,
                  profile.skinUrl,
                  KIND_TONES[effectiveKind ?? 'microsoft'].avatar,
                  32,
                  'text-[11px]',
                )}
                <span className="min-w-0 flex-1">
                  <span className="block text-xs font-semibold text-slate-900 dark:text-neutral-100 truncate">
                    {profile.name}
                  </span>
                  <span className="block text-[10px] font-mono text-slate-500 dark:text-neutral-500 truncate">
                    {profile.id}
                  </span>
                </span>
                <span className="text-[10px] text-emerald-600 dark:text-emerald-400 shrink-0">
                  选择
                </span>
              </button>
            ))}
          </div>
          {profiles.length === 0 && (
            <p className="text-[11px] text-slate-500 dark:text-neutral-500">
              后端没有返回可选档案，请取消后重试。
            </p>
          )}
          <button
            type="button"
            onClick={() => void handleCancelLogin()}
            disabled={controller.loginBusy}
            aria-label="取消登录"
            className="w-full py-2 rounded-lg bg-slate-200 dark:bg-neutral-800 hover:bg-slate-300 dark:hover:bg-neutral-700 text-xs font-semibold text-slate-800 dark:text-neutral-200 transition-colors cursor-pointer disabled:opacity-50 disabled:cursor-not-allowed"
          >
            取消登录
          </button>
        </div>
      );
    }

    // 登录成功。
    if (state === 'succeeded') {
      const accountName =
        session.account?.displayName ?? session.account?.profile?.name ?? '';
      return (
        <div className="space-y-3 text-center py-2">
          {statusBadge}
          <CheckCircle2
            className="w-10 h-10 text-emerald-500 dark:text-emerald-400 mx-auto"
            aria-hidden="true"
          />
          <div className="text-sm font-bold text-slate-900 dark:text-neutral-100">
            {accountName
              ? `登录成功：${accountName}`
              : '登录成功'}
          </div>
          <p className="text-[11px] text-slate-500 dark:text-neutral-400 leading-relaxed">
            账号已保存，左侧列表会由后端事件自动刷新。
          </p>
          <button
            type="button"
            onClick={() => void handleCancelLogin()}
            disabled={controller.loginBusy}
            aria-label="完成并结束本次登录流程"
            title="结束本次登录流程，右侧回到添加账号表单"
            className="w-full py-2.5 rounded-xl bg-emerald-500 hover:bg-emerald-400 text-neutral-950 font-bold text-xs transition-colors cursor-pointer shadow-sm disabled:opacity-50 disabled:cursor-not-allowed"
          >
            完成
          </button>
        </div>
      );
    }

    // 登录失败。
    if (state === 'failed') {
      return (
        <div className="space-y-3">
          {statusBadge}
          <div className="p-3 rounded-xl border border-rose-500/40 bg-rose-500/10 space-y-1.5">
            <div className="flex items-center gap-1.5 text-xs font-semibold text-rose-700 dark:text-rose-300">
              <AlertCircle className="w-3.5 h-3.5" aria-hidden="true" />
              登录失败
            </div>
            {(session.errorCode ?? '').length > 0 && (
              <div className="text-[10px] font-mono text-rose-600 dark:text-rose-400">
                错误码：{session.errorCode}
              </div>
            )}
            <p className="text-[11px] text-rose-700/90 dark:text-rose-300/90 leading-relaxed">
              {(session.errorMessage ?? '').length > 0
                ? session.errorMessage
                : '账号服务未返回具体原因，请稍后重试。'}
            </p>
          </div>
          <div className="flex items-center gap-2">
            <button
              type="button"
              onClick={() => void handleRetryLogin()}
              disabled={controller.loginBusy}
              aria-label="重试登录"
              className="flex-1 py-2 rounded-lg bg-emerald-500 hover:bg-emerald-400 text-neutral-950 font-bold text-xs flex items-center justify-center gap-1.5 transition-colors cursor-pointer disabled:opacity-50 disabled:cursor-not-allowed"
            >
              {controller.loginBusy ? (
                <Loader2 className="w-3.5 h-3.5 animate-spin" aria-hidden="true" />
              ) : (
                <RefreshCw className="w-3.5 h-3.5" aria-hidden="true" />
              )}
              <span>重试</span>
            </button>
            <button
              type="button"
              onClick={() => void handleCancelLogin()}
              disabled={controller.loginBusy}
              aria-label="取消登录"
              className="flex-1 py-2 rounded-lg bg-slate-200 dark:bg-neutral-800 hover:bg-slate-300 dark:hover:bg-neutral-700 text-xs font-semibold text-slate-800 dark:text-neutral-200 transition-colors cursor-pointer disabled:opacity-50 disabled:cursor-not-allowed"
            >
              取消登录
            </button>
          </div>
          {effectiveKind === 'microsoft' && (
            <button
              type="button"
              onClick={() =>
                void handleSwitchMicrosoftMode(
                  microsoftLoginMode === 'device_code' ? 'pkce' : 'device_code',
                )
              }
              disabled={controller.loginBusy}
              aria-label={
                microsoftLoginMode === 'device_code'
                  ? '改用浏览器登录'
                  : '改用设备码登录'
              }
              title="先取消本次失败的登录，再用另一种微软登录方式重试"
              className="w-full py-2 rounded-lg border border-slate-300 dark:border-neutral-700 bg-white dark:bg-neutral-900 hover:bg-slate-100 dark:hover:bg-neutral-800 text-[11px] font-semibold text-slate-700 dark:text-neutral-300 flex items-center justify-center gap-1.5 transition-colors cursor-pointer disabled:opacity-50 disabled:cursor-not-allowed"
            >
              {microsoftLoginMode === 'device_code' ? (
                <Globe className="w-3.5 h-3.5" aria-hidden="true" />
              ) : (
                <KeyRound className="w-3.5 h-3.5" aria-hidden="true" />
              )}
              <span>
                {microsoftLoginMode === 'device_code'
                  ? '改用浏览器登录'
                  : '改用设备码登录'}
              </span>
            </button>
          )}
          {effectiveKind === 'yggdrasil' && (
            <p className="text-[10px] text-slate-500 dark:text-neutral-500 leading-relaxed">
              密码在发起登录时已清空，重试需要重新输入密码。
            </p>
          )}
        </div>
      );
    }

    // 已取消（正常情况下 hook 会直接清空会话，这里做兜底展示）。
    if (state === 'cancelled') {
      return (
        <div className="space-y-3">
          {statusBadge}
          <p className="text-[11px] text-slate-600 dark:text-neutral-400">
            本次登录已取消，可以重新开始。
          </p>
          <button
            type="button"
            onClick={() => void handleCancelLogin()}
            aria-label="结束本次登录流程"
            className="w-full py-2 rounded-lg bg-slate-200 dark:bg-neutral-800 hover:bg-slate-300 dark:hover:bg-neutral-700 text-xs font-semibold text-slate-800 dark:text-neutral-200 transition-colors cursor-pointer"
          >
            返回
          </button>
        </div>
      );
    }

    // pending 或未知状态：如实展示，不假装成功。
    return (
      <div className="space-y-3 text-center py-3">
        {statusBadge}
        <Loader2
          className="w-8 h-8 text-emerald-500 dark:text-emerald-400 animate-spin mx-auto"
          aria-hidden="true"
        />
        <div className="text-xs font-semibold text-slate-800 dark:text-neutral-200">
          {statusLabel}
        </div>
        <p className="text-[11px] text-slate-500 dark:text-neutral-400">
          正在等待账号服务响应，请稍候…
        </p>
        <button
          type="button"
          onClick={() => void handleCancelLogin()}
          disabled={controller.loginBusy}
          aria-label="取消登录"
          className="w-full py-2 rounded-lg bg-slate-200 dark:bg-neutral-800 hover:bg-slate-300 dark:hover:bg-neutral-700 text-xs font-semibold text-slate-800 dark:text-neutral-200 transition-colors cursor-pointer disabled:opacity-50 disabled:cursor-not-allowed"
        >
          取消登录
        </button>
      </div>
    );
  };

  /** 未实现的来源：始终以禁用卡片展示，避免用户以为「点了没反应」。 */
  const renderComingSoonProviders = () => {
    const pendingProviders = controller.providers.filter(
      (provider) => !provider.implemented,
    );
    if (pendingProviders.length === 0) {
      return null;
    }
    return (
      <div className="space-y-2">
        {pendingProviders.map((provider) => {
          const kind = isAccountKind(provider.kind) ? provider.kind : null;
          const message =
            kind === 'ninevoxel'
              ? 'Ninevoxel 账号服务即将上线'
              : `${provider.displayName} 即将上线`;
          return (
            <div
              key={provider.kind}
              aria-disabled="true"
              className="p-3 rounded-xl border border-dashed border-amber-500/40 bg-amber-500/5 space-y-1"
            >
              <div className="flex items-center gap-1.5 flex-wrap">
                <ProviderIcon
                  kind={kind}
                  className="w-3.5 h-3.5 text-amber-600 dark:text-amber-400"
                />
                <span className="text-[11px] font-semibold text-amber-700 dark:text-amber-300">
                  {message}
                </span>
                <span className="text-[9px] font-mono px-1 py-0.5 rounded bg-amber-500/15 text-amber-700 dark:text-amber-300">
                  即将上线
                </span>
              </div>
              <p className="text-[10px] text-slate-500 dark:text-neutral-400 leading-relaxed">
                {(provider.description || message).replace(/[；;。.]+$/, '')}
                ；后端已登记该来源，但登录流程尚未实现，界面不会发起任何模拟登录。
              </p>
            </div>
          );
        })}
      </div>
    );
  };

  const renderAddForm = () => {
    if (controller.providers.length === 0) {
      return (
        <div className="py-8 text-center space-y-2">
          <Loader2
            className="w-6 h-6 text-slate-400 animate-spin mx-auto"
            aria-hidden="true"
          />
          <p className="text-[11px] text-slate-500 dark:text-neutral-400">
            {controller.loading
              ? '正在从后端加载账号来源…'
              : '后端尚未返回任何账号来源'}
          </p>
        </div>
      );
    }

    if (!effectiveKind) {
      return (
        <p className="text-[11px] text-slate-500 dark:text-neutral-400">
          没有可用的账号来源。
        </p>
      );
    }

    // 未实现的来源只展示说明，绝不发起任何假登录。
    if (activeProvider && !activeProvider.implemented) {
      return (
        <div className="p-4 rounded-xl border border-dashed border-slate-300 dark:border-neutral-700 bg-slate-50 dark:bg-neutral-950/40 text-center space-y-2">
          <Sparkles
            className="w-6 h-6 text-amber-500 dark:text-amber-400 mx-auto"
            aria-hidden="true"
          />
          <div className="text-xs font-bold text-slate-800 dark:text-neutral-200">
            Ninevoxel 账号服务即将上线
          </div>
          <p className="text-[11px] text-slate-500 dark:text-neutral-400 leading-relaxed">
            后端已登记该来源但尚未实现登录流程，界面不会在此发起任何模拟登录。
          </p>
        </div>
      );
    }

    if (effectiveKind === 'microsoft') {
      return (
        <div className="space-y-3">
          <div className="p-3 rounded-xl bg-slate-50 dark:bg-neutral-950/60 border border-slate-200 dark:border-neutral-800 flex items-start gap-2">
            <Globe
              className="w-4 h-4 text-emerald-500 dark:text-emerald-400 shrink-0 mt-0.5"
              aria-hidden="true"
            />
            <div className="space-y-1">
              <div className="text-xs font-semibold text-slate-800 dark:text-neutral-200">
                使用 Microsoft 账号登录
              </div>
              <p className="text-[11px] text-slate-500 dark:text-neutral-400 leading-relaxed">
                默认使用浏览器授权（PKCE + 本地回环）：点击主按钮后启动器会自动打开系统浏览器，
                完成登录后自动返回本启动器。若浏览器无法打开或授权失败，可改用设备码登录。
                账号密码只在微软官方页面输入，不经过本启动器。
              </p>
            </div>
          </div>

          <button
            type="button"
            onClick={() => void startMicrosoftPkce()}
            disabled={controller.loginBusy}
            aria-label="使用 Microsoft 登录（浏览器）"
            title="后端会打开系统浏览器并监听本地回环，无需输入设备码"
            className="w-full py-2.5 rounded-xl bg-emerald-500 hover:bg-emerald-400 text-neutral-950 font-bold text-xs flex items-center justify-center gap-2 transition-colors cursor-pointer shadow-sm disabled:opacity-50 disabled:cursor-not-allowed"
          >
            {controller.loginBusy ? (
              <Loader2 className="w-3.5 h-3.5 animate-spin" aria-hidden="true" />
            ) : (
              <LogIn className="w-3.5 h-3.5" aria-hidden="true" />
            )}
            <span>
              {controller.loginBusy
                ? '正在打开浏览器…'
                : '使用 Microsoft 登录（浏览器）'}
            </span>
          </button>

          <button
            type="button"
            onClick={() => void handleSwitchMicrosoftMode('device_code')}
            disabled={controller.loginBusy}
            aria-label="登录遇到问题？改用设备码登录"
            title="取消浏览器授权流程，改用一次性设备码登录"
            className="w-full py-2 rounded-lg border border-slate-300 dark:border-neutral-700 bg-white dark:bg-neutral-900 hover:bg-slate-100 dark:hover:bg-neutral-800 text-[11px] font-semibold text-slate-700 dark:text-neutral-300 flex items-center justify-center gap-1.5 transition-colors cursor-pointer disabled:opacity-50 disabled:cursor-not-allowed"
          >
            <KeyRound className="w-3.5 h-3.5" aria-hidden="true" />
            <span>登录遇到问题？改用设备码登录</span>
          </button>

          <p className="text-[10px] text-slate-500 dark:text-neutral-500 leading-relaxed">
            授权地址与设备码全部来自后端真实响应；授权成功后账号会自动加入左侧列表。
          </p>
        </div>
      );
    }

    if (effectiveKind === 'offline') {
      return (
        <div className="space-y-3">
          <div className="p-3 rounded-xl bg-slate-50 dark:bg-neutral-950/60 border border-slate-200 dark:border-neutral-800 flex items-start gap-2">
            <User
              className="w-4 h-4 text-slate-500 dark:text-neutral-400 shrink-0 mt-0.5"
              aria-hidden="true"
            />
            <p className="text-[11px] text-slate-500 dark:text-neutral-400 leading-relaxed">
              离线账号只用于局域网联机与单机测试，不会连接任何认证服务器；
              UUID 由后端生成。
            </p>
          </div>
          <div>
            <label
              htmlFor="account-offline-name"
              className="text-xs font-semibold text-slate-700 dark:text-neutral-300 block mb-1.5"
            >
              玩家名
            </label>
            <input
              id="account-offline-name"
              type="text"
              value={offlineName}
              onChange={(event) => setOfflineName(event.target.value)}
              onKeyDown={(event) => {
                if (event.key === 'Enter' && offlineNameValid && !controller.loginBusy) {
                  void handleStartOffline();
                }
              }}
              placeholder="例如：Steve_Builder"
              maxLength={16}
              autoComplete="off"
              spellCheck={false}
              aria-invalid={offlineNameError !== null}
              aria-describedby="account-offline-hint"
              className="w-full px-3 py-2 bg-slate-50 dark:bg-neutral-950 border border-slate-300 dark:border-neutral-800 rounded-lg text-xs text-slate-800 dark:text-neutral-200 focus:outline-none focus:border-emerald-500"
            />
            <p
              id="account-offline-hint"
              className="mt-1 text-[10px] min-h-4 leading-relaxed"
            >
              {offlineNameError ? (
                <span className="text-rose-600 dark:text-rose-400">
                  {offlineNameError}
                </span>
              ) : (
                <span className="text-slate-500 dark:text-neutral-500">
                  3-16 位字母、数字或下划线。
                </span>
              )}
            </p>
          </div>
          <button
            type="button"
            onClick={() => void handleStartOffline()}
            disabled={!offlineNameValid || controller.loginBusy}
            aria-label="登录并添加离线账号"
            className="w-full py-2.5 rounded-xl bg-emerald-500 hover:bg-emerald-400 text-neutral-950 font-bold text-xs flex items-center justify-center gap-2 transition-colors cursor-pointer shadow-sm disabled:opacity-50 disabled:cursor-not-allowed"
          >
            {controller.loginBusy ? (
              <Loader2 className="w-3.5 h-3.5 animate-spin" aria-hidden="true" />
            ) : (
              <LogIn className="w-3.5 h-3.5" aria-hidden="true" />
            )}
            <span>登录并添加离线账号</span>
          </button>
        </div>
      );
    }

    if (effectiveKind === 'yggdrasil') {
      return (
        <div className="space-y-3">
          <div className="p-3 rounded-xl bg-slate-50 dark:bg-neutral-950/60 border border-slate-200 dark:border-neutral-800 flex items-start gap-2">
            <Server
              className="w-4 h-4 text-indigo-500 dark:text-indigo-400 shrink-0 mt-0.5"
              aria-hidden="true"
            />
            <p className="text-[11px] text-slate-500 dark:text-neutral-400 leading-relaxed">
              对接 authlib-injector 兼容的皮肤站：先解析地址确认服务名，再输入账号密码。
            </p>
          </div>

          <div>
            <label
              htmlFor="account-ygg-url"
              className="text-xs font-semibold text-slate-700 dark:text-neutral-300 block mb-1.5"
            >
              Yggdrasil API 地址
            </label>
            <div className="flex items-center gap-2">
              <input
                id="account-ygg-url"
                type="text"
                value={yggUrl}
                onChange={(event) => {
                  setYggUrl(event.target.value);
                  // 地址变了，之前解析出的站点信息立即失效。
                  setSiteInfo(null);
                }}
                onKeyDown={(event) => {
                  if (event.key === 'Enter' && !resolving) {
                    void handleResolveSite();
                  }
                }}
                placeholder="https://littleskin.cn/api/yggdrasil"
                autoComplete="off"
                spellCheck={false}
                className="flex-1 min-w-0 px-3 py-2 bg-slate-50 dark:bg-neutral-950 border border-slate-300 dark:border-neutral-800 rounded-lg text-xs font-mono text-slate-800 dark:text-neutral-200 focus:outline-none focus:border-emerald-500"
              />
              <button
                type="button"
                onClick={() => void handleResolveSite()}
                disabled={resolving || yggUrl.trim().length === 0}
                aria-label="解析 Yggdrasil 站点"
                title="向后端解析该地址并读取服务名"
                className="shrink-0 px-2.5 py-2 rounded-lg bg-slate-100 dark:bg-neutral-800 hover:bg-slate-200 dark:hover:bg-neutral-700 text-[11px] font-semibold text-slate-700 dark:text-neutral-300 flex items-center gap-1.5 transition-colors cursor-pointer disabled:opacity-50 disabled:cursor-not-allowed"
              >
                {resolving ? (
                  <Loader2 className="w-3.5 h-3.5 animate-spin" aria-hidden="true" />
                ) : (
                  <Server className="w-3.5 h-3.5" aria-hidden="true" />
                )}
                <span>{resolving ? '解析中' : '解析站点'}</span>
              </button>
            </div>
          </div>

          {siteInfo && (
            <div className="p-2.5 rounded-lg border border-emerald-500/40 bg-emerald-500/10 text-[11px] space-y-1">
              <div className="font-semibold text-emerald-700 dark:text-emerald-300 flex items-center gap-1.5">
                <CheckCircle2 className="w-3.5 h-3.5" aria-hidden="true" />
                {siteInfo.serverName || '未命名站点'}
              </div>
              <div className="font-mono text-slate-600 dark:text-neutral-400 break-all">
                API 根：{siteInfo.apiUrl}
              </div>
              {(siteInfo.implementationName ?? '').length > 0 && (
                <div className="text-slate-500 dark:text-neutral-500">
                  实现：{siteInfo.implementationName}
                </div>
              )}
              {siteInfo.nonEmailLogin && (
                <div className="text-slate-500 dark:text-neutral-500">
                  支持非邮箱登录
                </div>
              )}
            </div>
          )}

          <div>
            <label
              htmlFor="account-ygg-user"
              className="text-xs font-semibold text-slate-700 dark:text-neutral-300 block mb-1.5"
            >
              用户名 / 邮箱
            </label>
            <input
              id="account-ygg-user"
              type="text"
              value={yggUser}
              onChange={(event) => setYggUser(event.target.value)}
              placeholder="user@example.com"
              autoComplete="username"
              spellCheck={false}
              className="w-full px-3 py-2 bg-slate-50 dark:bg-neutral-950 border border-slate-300 dark:border-neutral-800 rounded-lg text-xs text-slate-800 dark:text-neutral-200 focus:outline-none focus:border-emerald-500"
            />
          </div>

          <div>
            <label
              htmlFor="account-ygg-pass"
              className="text-xs font-semibold text-slate-700 dark:text-neutral-300 mb-1.5 flex items-center gap-1.5"
            >
              <KeyRound className="w-3 h-3" aria-hidden="true" />
              密码
            </label>
            <input
              id="account-ygg-pass"
              type="password"
              value={yggPass}
              onChange={(event) => setYggPass(event.target.value)}
              onKeyDown={(event) => {
                if (event.key === 'Enter' && yggReady && !controller.loginBusy) {
                  void handleStartYggdrasil();
                }
              }}
              placeholder="••••••••"
              autoComplete="current-password"
              className="w-full px-3 py-2 bg-slate-50 dark:bg-neutral-950 border border-slate-300 dark:border-neutral-800 rounded-lg text-xs text-slate-800 dark:text-neutral-200 focus:outline-none focus:border-emerald-500"
            />
            <p className="mt-1 text-[10px] text-slate-500 dark:text-neutral-500 leading-relaxed">
              密码只用于本次登录请求，发起后立即从前端内存清空，不落盘、不进日志。
            </p>
          </div>

          <button
            type="button"
            onClick={() => void handleStartYggdrasil()}
            disabled={!yggReady || controller.loginBusy}
            aria-label="登录并添加 Yggdrasil 账号"
            className="w-full py-2.5 rounded-xl bg-emerald-500 hover:bg-emerald-400 text-neutral-950 font-bold text-xs flex items-center justify-center gap-2 transition-colors cursor-pointer shadow-sm disabled:opacity-50 disabled:cursor-not-allowed"
          >
            {controller.loginBusy ? (
              <Loader2 className="w-3.5 h-3.5 animate-spin" aria-hidden="true" />
            ) : (
              <LogIn className="w-3.5 h-3.5" aria-hidden="true" />
            )}
            <span>登录并添加 Yggdrasil 账号</span>
          </button>
          {!siteInfo && (
            <p className="text-[10px] text-slate-500 dark:text-neutral-500 leading-relaxed">
              请先「解析站点」：地址合法且站点可访问后才能继续登录。
            </p>
          )}
        </div>
      );
    }

    // ninevoxel 等未实现来源（activeProvider.implemented 已在上方拦截，这里兜底）。
    return (
      <div className="p-4 rounded-xl border border-dashed border-slate-300 dark:border-neutral-700 bg-slate-50 dark:bg-neutral-950/40 text-center space-y-2">
        <Sparkles
          className="w-6 h-6 text-amber-500 dark:text-amber-400 mx-auto"
          aria-hidden="true"
        />
        <div className="text-xs font-bold text-slate-800 dark:text-neutral-200">
          Ninevoxel 账号服务即将上线
        </div>
      </div>
    );
  };

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center p-4 bg-black/60 dark:bg-black/80 backdrop-blur-sm animate-in fade-in duration-200">
      <div
        role="dialog"
        aria-modal="true"
        aria-labelledby="account-manager-title"
        className="w-full max-w-4xl bg-white dark:bg-neutral-900 border border-slate-200 dark:border-neutral-700/80 rounded-2xl shadow-2xl overflow-hidden flex flex-col max-h-[85vh]"
      >
        {/* Header */}
        <div className="px-6 py-4 border-b border-slate-200 dark:border-neutral-800 flex items-center justify-between gap-3 bg-slate-50 dark:bg-neutral-950/70">
          <div className="flex items-center gap-2.5 min-w-0">
            <div className="w-8 h-8 shrink-0 rounded-lg bg-emerald-500/15 border border-emerald-500/30 flex items-center justify-center text-emerald-600 dark:text-emerald-400">
              <Users className="w-4 h-4" aria-hidden="true" />
            </div>
            <div className="min-w-0">
              <h2
                id="account-manager-title"
                className="text-sm font-bold text-slate-900 dark:text-neutral-100 truncate"
              >
                账号管理 (Account Manager)
              </h2>
              <p className="text-[11px] text-slate-500 dark:text-neutral-400 truncate">
                账号、设备码与登录状态全部来自后端账号服务；凭证不进入前端
              </p>
            </div>
          </div>
          <button
            type="button"
            onClick={onClose}
            aria-label="关闭账号管理"
            title="关闭"
            className="p-1.5 rounded-lg text-slate-400 dark:text-neutral-400 hover:text-slate-800 dark:hover:text-white hover:bg-slate-100 dark:hover:bg-neutral-800 transition-colors cursor-pointer shrink-0"
          >
            <X className="w-4 h-4" aria-hidden="true" />
          </button>
        </div>

        {/* 全局错误 banner：错误来自 controller.error，可手动关闭。 */}
        {controller.error && (
          <div
            role="alert"
            className="mx-6 mt-3 p-3 rounded-xl border border-rose-500/40 bg-rose-500/10 flex items-start justify-between gap-3"
          >
            <div className="flex items-start gap-2 min-w-0">
              <AlertCircle
                className="w-4 h-4 text-rose-600 dark:text-rose-400 shrink-0 mt-0.5"
                aria-hidden="true"
              />
              <div className="min-w-0">
                <div className="text-xs font-semibold text-rose-700 dark:text-rose-300 flex items-center gap-2 flex-wrap">
                  <span>操作失败</span>
                  <span className="text-[10px] font-mono font-normal px-1.5 py-0.5 rounded bg-rose-500/15 text-rose-700 dark:text-rose-300">
                    {controller.error.code}
                  </span>
                  {controller.error.retryable && (
                    <span className="text-[10px] font-normal text-rose-600 dark:text-rose-400">
                      可重试
                    </span>
                  )}
                </div>
                <p className="text-[11px] text-rose-700/90 dark:text-rose-300/90 leading-relaxed mt-0.5 break-words">
                  {controller.error.message}
                </p>
                {controller.error.correlationId && (
                  <p className="text-[10px] font-mono text-rose-600/70 dark:text-rose-400/70 mt-0.5 break-all">
                    追踪 ID：{controller.error.correlationId}
                  </p>
                )}
              </div>
            </div>
            <button
              type="button"
              onClick={controller.clearError}
              aria-label="关闭错误提示"
              title="关闭错误提示"
              className="shrink-0 p-1 rounded text-rose-500 dark:text-rose-400 hover:text-rose-700 dark:hover:text-rose-200 hover:bg-rose-500/15 transition-colors cursor-pointer"
            >
              <X className="w-3.5 h-3.5" aria-hidden="true" />
            </button>
          </div>
        )}

        {/* Body：左侧账号列表，右侧添加账号 */}
        <div className="flex-1 min-h-0 overflow-y-auto md:overflow-hidden grid grid-cols-1 md:grid-cols-2 divide-y md:divide-y-0 md:divide-x divide-slate-200 dark:divide-neutral-800">
          {/* 左：已保存账号 */}
          <section
            aria-label="已保存账号列表"
            className="flex flex-col min-h-0"
          >
            <div className="px-5 py-3 border-b border-slate-200 dark:border-neutral-800 flex items-center justify-between gap-2 bg-slate-50/70 dark:bg-neutral-950/40">
              <h3 className="text-xs font-bold text-slate-900 dark:text-neutral-100 flex items-center gap-1.5">
                <Users
                  className="w-3.5 h-3.5 text-emerald-500 dark:text-emerald-400"
                  aria-hidden="true"
                />
                <span>已保存账号</span>
                <span className="text-[10px] font-mono font-normal text-slate-500 dark:text-neutral-500">
                  ({controller.accounts.length})
                </span>
              </h3>
              <button
                type="button"
                onClick={() => void controller.reload()}
                disabled={controller.loading}
                aria-label="重新加载账号列表"
                title="重新加载账号列表"
                className="p-1 rounded text-slate-400 dark:text-neutral-500 hover:text-slate-700 dark:hover:text-neutral-200 hover:bg-slate-100 dark:hover:bg-neutral-800 transition-colors cursor-pointer disabled:opacity-40 disabled:cursor-not-allowed"
              >
                <RefreshCw
                  className={`w-3.5 h-3.5 ${controller.loading ? 'animate-spin' : ''}`}
                  aria-hidden="true"
                />
              </button>
            </div>

            <div className="flex-1 min-h-0 overflow-y-auto px-5 py-3 space-y-2">
              {controller.loading && controller.accounts.length === 0 ? (
                <div className="py-10 text-center space-y-2">
                  <Loader2
                    className="w-6 h-6 text-slate-400 animate-spin mx-auto"
                    aria-hidden="true"
                  />
                  <p className="text-[11px] text-slate-500 dark:text-neutral-400">
                    正在从后端读取账号列表…
                  </p>
                </div>
              ) : controller.accounts.length === 0 ? (
                <div className="h-full min-h-[12rem] flex flex-col items-center justify-center text-center px-3 py-6 space-y-3">
                  <div className="w-12 h-12 rounded-2xl bg-slate-100 dark:bg-neutral-800 flex items-center justify-center">
                    <UserPlus
                      className="w-6 h-6 text-slate-400 dark:text-neutral-500"
                      aria-hidden="true"
                    />
                  </div>
                  <div className="space-y-1">
                    <div className="text-xs font-semibold text-slate-800 dark:text-neutral-200">
                      还没有任何账号
                    </div>
                    <p className="text-[11px] text-slate-500 dark:text-neutral-400 leading-relaxed max-w-[16rem]">
                      请在右侧选择一种账号来源开始登录。登录成功后账号会出现在这里，
                      第一个账号会自动成为当前账号。
                    </p>
                  </div>
                </div>
              ) : (
                controller.accounts.map((account) => renderAccountRow(account))
              )}
            </div>
          </section>

          {/* 右：添加账号 */}
          <section
            aria-label="添加账号"
            className="flex flex-col min-h-0"
          >
            <div className="px-5 py-3 border-b border-slate-200 dark:border-neutral-800 flex items-center justify-between gap-2 bg-slate-50/70 dark:bg-neutral-950/40">
              <h3 className="text-xs font-bold text-slate-900 dark:text-neutral-100 flex items-center gap-1.5">
                <UserPlus
                  className="w-3.5 h-3.5 text-emerald-500 dark:text-emerald-400"
                  aria-hidden="true"
                />
                <span>添加账号</span>
              </h3>
              {controller.loginBusy && (
                <span className="text-[10px] text-slate-500 dark:text-neutral-400 flex items-center gap-1">
                  <Loader2
                    className="w-3 h-3 animate-spin"
                    aria-hidden="true"
                  />
                  处理中…
                </span>
              )}
            </div>

            <div className="flex-1 min-h-0 overflow-y-auto px-5 py-3 space-y-3">
              {controller.providers.length > 0 && renderProviderTabs()}
              {kindLogin ? renderLoginFlow(kindLogin) : renderAddForm()}
              {renderComingSoonProviders()}
            </div>
          </section>
        </div>

        {/* Footer */}
        <div className="px-6 py-3.5 border-t border-slate-200 dark:border-neutral-800 flex items-center justify-between gap-3 bg-slate-50 dark:bg-neutral-950/70">
          <div className="text-[11px] text-slate-500 dark:text-neutral-400 truncate">
            共 {controller.accounts.length} 个账号
            {controller.currentAccount
              ? ` · 当前：${controller.currentAccount.displayName}`
              : ' · 尚未选择当前账号'}
          </div>
          <button
            type="button"
            onClick={onClose}
            aria-label="关闭账号管理弹窗"
            className="px-4 py-2 rounded-lg bg-slate-200 dark:bg-neutral-800 hover:bg-slate-300 dark:hover:bg-neutral-700 text-xs font-semibold text-slate-800 dark:text-neutral-200 transition-colors cursor-pointer shrink-0"
          >
            关闭
          </button>
        </div>
      </div>
    </div>
  );
};
