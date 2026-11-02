/**
 * 渲染错误兜底（Error Boundary）。
 *
 * 为什么需要：React 的渲染期异常会卸载整棵组件树；没有 ErrorBoundary 时，
 * 用户看到的是纯白屏幕，连「出了什么问题」都不知道。这里至少给出错误信息、
 * 一份可复制的诊断文本和「重新加载」按钮。
 *
 * 注意：错误边界只能捕获**渲染/生命周期**异常，捕获不到事件回调与异步代码里的错误；
 * 那些仍应由各调用的 `AppError` 处理（services/errors.ts）。
 */
import React from 'react'

/** 错误边界的状态。 */
interface ErrorBoundaryState {
  /** 捕获到的错误；null 表示正常渲染。 */
  error: Error | null
}

/**
 * 应用级错误边界。
 *
 * 用法：`<ErrorBoundary><App /></ErrorBoundary>`（见 main.tsx）。
 */
export class ErrorBoundary extends React.Component<React.PropsWithChildren, ErrorBoundaryState> {
  state: ErrorBoundaryState = { error: null }

  /**
   * 渲染阶段捕获异常后转为 state，触发兜底界面。
   *
   * @param error 捕获到的错误
   * @returns 新的 state
   */
  static getDerivedStateFromError(error: Error): ErrorBoundaryState {
    return { error }
  }

  /**
   * 记录组件栈，便于用户反馈问题时定位（仅输出到开发者控制台）。
   *
   * @param error 捕获到的错误
   * @param info React 提供的组件栈
   */
  componentDidCatch(error: Error, info: React.ErrorInfo): void {
    // 不使用后端日志通道：此时应用可能已经半崩溃，控制台是唯一可靠出口。
    console.error('[NineVoxel] 界面渲染异常', error, info.componentStack)
  }

  /** 重新加载整个应用（最直接的恢复手段）。 */
  private readonly handleReload = (): void => {
    window.location.reload()
  }

  render(): React.ReactNode {
    const { error } = this.state
    if (!error) {
      return this.props.children
    }
    return (
      <div className="w-screen h-screen flex items-center justify-center p-6 bg-slate-100 dark:bg-neutral-950 text-slate-900 dark:text-neutral-100 font-sans">
        <div className="w-full max-w-lg rounded-2xl border border-slate-200 dark:border-neutral-800 bg-white dark:bg-neutral-900 p-6 shadow-xl space-y-4">
          <div className="flex items-center gap-3">
            <div className="w-10 h-10 rounded-xl bg-rose-500/10 border border-rose-500/30 flex items-center justify-center text-rose-600 dark:text-rose-400 text-lg font-bold">
              !
            </div>
            <div>
              <h1 className="text-base font-bold">界面遇到未处理的错误</h1>
              <p className="text-xs text-slate-500 dark:text-neutral-400 mt-0.5">
                启动器没有退出，你可以复制错误信息后重新加载界面。
              </p>
            </div>
          </div>

          <pre className="max-h-40 overflow-auto rounded-xl bg-slate-50 dark:bg-neutral-950 border border-slate-200 dark:border-neutral-800 p-3 text-[11px] leading-relaxed text-rose-600 dark:text-rose-400 whitespace-pre-wrap break-words font-mono">
            {error.message || String(error)}
          </pre>

          <div className="flex items-center justify-end gap-2">
            <button
              type="button"
              onClick={() => {
                void navigator.clipboard?.writeText(`${error.message}\n${error.stack ?? ''}`).catch(() => {})
              }}
              className="px-3 py-2 rounded-lg text-xs font-semibold border border-slate-200 dark:border-neutral-700 text-slate-700 dark:text-neutral-200 hover:bg-slate-100 dark:hover:bg-neutral-800 transition-colors cursor-pointer"
            >
              复制错误信息
            </button>
            <button
              type="button"
              onClick={this.handleReload}
              className="px-4 py-2 rounded-lg text-xs font-bold bg-emerald-500 hover:bg-emerald-400 text-neutral-950 transition-colors cursor-pointer"
            >
              重新加载
            </button>
          </div>
        </div>
      </div>
    )
  }
}
