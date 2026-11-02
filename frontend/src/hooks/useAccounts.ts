/**
 * 账号状态钩子。
 *
 * 把后端账号子系统接成界面可用的控制器：
 *  - 账号列表/来源来自 `ListAccounts` / `ListProviders` 真实快照；
 *  - 登录流程按状态自动轮询（微软设备码），终态后自动刷新列表；
 *  - 失败统一写入 `error`（AppError），不向组件抛异常；
 *  - 订阅 `account.changed.v1` 推动列表刷新（事件可能丢失，快照才是真相）。
 *
 * 安全约定：本钩子只接触账号摘要，不接触任何凭证。
 */
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'

import * as accountService from '../services/account'
import type { AppError } from '../types/app'
import {
  isLoginTerminal,
  type AccountsController,
  type AccountView,
  type LoginSessionView,
  type ProviderView,
  type SiteInfoView,
  type StartLoginRequest,
} from '../types/account'
import { toAppError } from '../services/errors'

/** 轮询默认间隔（秒）：微软设备码通常返回 5 秒。 */
const DEFAULT_POLL_INTERVAL_SECONDS = 5

/** 轮询间隔下限（秒）：避免服务端返回 0/负数时把界面打成高频请求。 */
const MIN_POLL_INTERVAL_SECONDS = 3

/**
 * 账号状态钩子。
 *
 * @returns 账号控制器（状态 + 操作），可直接传给账号弹窗。
 */
export function useAccounts(): AccountsController {
  const [providers, setProviders] = useState<ProviderView[]>([])
  const [accounts, setAccounts] = useState<AccountView[]>([])
  const [selectedAccountId, setSelectedAccountId] = useState('')
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<AppError | null>(null)
  const [login, setLogin] = useState<LoginSessionView | null>(null)
  const [loginBusy, setLoginBusy] = useState(false)

  // 保存最近一次登录会话：轮询与取消都基于它，避免闭包读到旧值。
  const loginRef = useRef<LoginSessionView | null>(null)
  // 轮询互斥：定时器触发时若上一次请求还没结束就跳过本轮。
  const pollBusyRef = useRef(false)
  // 组件卸载后不再回写状态。
  const mountedRef = useRef(true)

  useEffect(() => {
    mountedRef.current = true
    return () => {
      mountedRef.current = false
    }
  }, [])

  /**
   * 重新加载来源与账号列表。
   *
   * @param silent 为 true 时不切换 loading（用于事件驱动的后台刷新，避免闪烁）
   */
  const reload = useCallback(async (silent = false) => {
    if (!silent) {
      setLoading(true)
    }
    try {
      const [nextProviders, list] = await Promise.all([
        accountService.fetchProviders(),
        accountService.fetchAccounts(),
      ])
      if (!mountedRef.current) {
        return
      }
      setProviders(nextProviders)
      setAccounts(list.accounts ?? [])
      setSelectedAccountId(list.selectedAccountId ?? '')
    } catch (cause) {
      if (mountedRef.current) {
        setError(toAppError(cause))
      }
    } finally {
      if (mountedRef.current && !silent) {
        setLoading(false)
      }
    }
  }, [])

  useEffect(() => {
    void reload()
  }, [reload])

  // 账号变更事件：只作为刷新信号，载荷不作为数据真相。
  useEffect(() => {
    const unsubscribe = accountService.subscribeAccountChanged(() => {
      void reload(true)
    })
    return unsubscribe
  }, [reload])

  /** 清空错误提示。 */
  const clearError = useCallback(() => {
    setError(null)
  }, [])

  /** 更新当前登录会话（同时维护 ref）。 */
  const applySession = useCallback((session: LoginSessionView | null) => {
    loginRef.current = session
    setLogin(session)
  }, [])

  /** 轮询一次登录会话。 */
  const pollLogin = useCallback(async () => {
    const session = loginRef.current
    if (!session || isLoginTerminal(session.state) || session.state === 'awaiting_profile') {
      return
    }
    if (pollBusyRef.current) {
      return
    }
    pollBusyRef.current = true
    setLoginBusy(true)
    try {
      const next = await accountService.pollLogin(session.sessionId)
      if (!mountedRef.current) {
        return
      }
      applySession(next)
      if (next.state === 'succeeded') {
        await reload(true)
      }
    } catch (cause) {
      // 网络类错误保留会话，定时器会继续重试；错误同时提示用户。
      if (mountedRef.current) {
        setError(toAppError(cause))
      }
    } finally {
      pollBusyRef.current = false
      if (mountedRef.current) {
        setLoginBusy(false)
      }
    }
  }, [applySession, reload])

  // 设备码轮询：仅对 pending / awaiting_user 生效；终态或待选档案时停止。
  useEffect(() => {
    if (!login || isLoginTerminal(login.state) || login.state === 'awaiting_profile') {
      return
    }
    const interval = login.challenge?.intervalSeconds ?? 0
    const seconds = Math.max(MIN_POLL_INTERVAL_SECONDS, interval || DEFAULT_POLL_INTERVAL_SECONDS)
    const timer = window.setInterval(() => {
      void pollLogin()
    }, seconds * 1000)
    return () => {
      window.clearInterval(timer)
    }
  }, [login, pollLogin])

  /** 开始登录。 */
  const startLogin = useCallback(
    async (request: StartLoginRequest) => {
      setError(null)
      setLoginBusy(true)
      // 切换登录方式（PKCE ↔ 设备码）时先结束上一个未完成的会话：
      // 否则旧的本地回环服务/轮询会残留，直到 5 分钟超时才释放。
      const previous = loginRef.current
      if (previous && !isLoginTerminal(previous.state)) {
        try {
          await accountService.cancelLogin(previous.sessionId)
        } catch {
          // 取消失败不阻断新登录：后端会话会随 TTL 过期。
        }
        applySession(null)
      }
      try {
        const session = await accountService.startLogin(request)
        if (!mountedRef.current) {
          return
        }
        applySession(session)
        if (session.state === 'succeeded') {
          await reload(true)
        }
      } catch (cause) {
        if (mountedRef.current) {
          setError(toAppError(cause))
        }
      } finally {
        if (mountedRef.current) {
          setLoginBusy(false)
        }
      }
    },
    [applySession, reload],
  )

  /** 为多档案账号选择档案。 */
  const selectProfile = useCallback(
    async (profileId: string) => {
      const session = loginRef.current
      if (!session) {
        return
      }
      setError(null)
      setLoginBusy(true)
      try {
        const next = await accountService.selectProfile(session.sessionId, profileId)
        if (!mountedRef.current) {
          return
        }
        applySession(next)
        if (next.state === 'succeeded') {
          await reload(true)
        }
      } catch (cause) {
        if (mountedRef.current) {
          setError(toAppError(cause))
        }
      } finally {
        if (mountedRef.current) {
          setLoginBusy(false)
        }
      }
    },
    [applySession, reload],
  )

  /**
   * 取消/结束登录。
   *
   * 语义分两种：
   *   - 进行中的会话：调用后端 CancelLogin 释放会话，再清空本地状态；
   *   - 已成功的会话：后端没有「结束成功登录」的概念，直接清空本地视图即可
   *     （弹窗的「完成」按钮也走这里）。若对终态会话再调后端，只会拿到
   *     「会话不存在/已结束」的错误，反而把成功提示变成红色错误。
   */
  const cancelLogin = useCallback(async () => {
    const session = loginRef.current
    if (!session) {
      applySession(null)
      return
    }
    const terminal = isLoginTerminal(session.state)
    if (!terminal) {
      try {
        await accountService.cancelLogin(session.sessionId)
      } catch (cause) {
        if (mountedRef.current) {
          setError(toAppError(cause))
        }
      }
    }
    if (mountedRef.current) {
      applySession(null)
      setLoginBusy(false)
    }
  }, [applySession])

  /** 切换当前账号。 */
  const selectAccount = useCallback(async (accountId: string) => {
    try {
      const list = await accountService.selectAccount(accountId)
      if (!mountedRef.current) {
        return
      }
      setAccounts(list.accounts ?? [])
      setSelectedAccountId(list.selectedAccountId ?? '')
      setError(null)
    } catch (cause) {
      if (mountedRef.current) {
        setError(toAppError(cause))
      }
    }
  }, [])

  /** 删除账号。 */
  const deleteAccount = useCallback(async (accountId: string) => {
    try {
      const list = await accountService.deleteAccount(accountId)
      if (!mountedRef.current) {
        return
      }
      setAccounts(list.accounts ?? [])
      setSelectedAccountId(list.selectedAccountId ?? '')
      setError(null)
    } catch (cause) {
      if (mountedRef.current) {
        setError(toAppError(cause))
      }
    }
  }, [])

  /** 刷新账号凭证。 */
  const refreshAccount = useCallback(
    async (accountId: string) => {
      try {
        await accountService.refreshAccount(accountId)
        if (!mountedRef.current) {
          return
        }
        setError(null)
        await reload(true)
      } catch (cause) {
        if (mountedRef.current) {
          setError(toAppError(cause))
        }
      }
    },
    [reload],
  )

  /** 解析自定义 Yggdrasil 地址。 */
  const resolveSite = useCallback(async (apiUrl: string): Promise<SiteInfoView | null> => {
    try {
      const site = await accountService.resolveYggdrasilSite(apiUrl)
      if (mountedRef.current) {
        setError(null)
      }
      return site
    } catch (cause) {
      if (mountedRef.current) {
        setError(toAppError(cause))
      }
      return null
    }
  }, [])

  const currentAccount = useMemo<AccountView | null>(() => {
    if (accounts.length === 0) {
      return null
    }
    const selected = accounts.find((item) => item.id === selectedAccountId)
    if (selected) {
      return selected
    }
    return accounts.find((item) => item.selected) ?? accounts[0]
  }, [accounts, selectedAccountId])

  return useMemo<AccountsController>(
    () => ({
      providers,
      accounts,
      selectedAccountId,
      currentAccount,
      loading,
      error,
      login,
      loginBusy,
      clearError,
      reload,
      startLogin,
      pollLogin,
      selectProfile,
      cancelLogin,
      selectAccount,
      deleteAccount,
      refreshAccount,
      resolveSite,
    }),
    [
      providers,
      accounts,
      selectedAccountId,
      currentAccount,
      loading,
      error,
      login,
      loginBusy,
      clearError,
      reload,
      startLogin,
      pollLogin,
      selectProfile,
      cancelLogin,
      selectAccount,
      deleteAccount,
      refreshAccount,
      resolveSite,
    ],
  )
}
