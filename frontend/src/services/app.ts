import { AppService } from '../../bindings/example.com/ninevoxel/launcher'
import type { AppInfo } from '../types/app'
import { toAppError } from './errors'

/**
 * Go Bindings 的服务封装层。
 *
 * 前端只有本文件可以直接 `import` 生成目录中的 `AppService`；
 * 页面与组件必须通过这里暴露的语义化函数访问后端（AGENTS.md 8.2）。
 */

/** 冻结后的 AppService 快照方法契约（`launcher/docs/tasks/P1/接口冻结清单.md` §8.4：`Snapshot() (AppInfo, error)`）。 */
interface AppServiceSnapshotBinding {
  /** 返回当前应用信息快照；Go 侧返回 error 时 Promise 以错误拒绝。 */
  readonly Snapshot: () => Promise<AppInfo>
}

/** 快照方法尚未就绪时对用户展示的说明，不包含任何技术细节。 */
const APP_INFO_UNAVAILABLE_MESSAGE = '应用信息服务尚未就绪，请稍后重试。'

/**
 * 判断绑定产物是否已经导出冻结后的 `Snapshot` 方法。
 *
 * 现状：Go 侧 `appservice.go` 仍是 P0 的 `PublishRuntimeInfo`（接口冻结清单 §8.4 已决定删除），
 * `Snapshot` 由后续 Go 侧任务卡实现并重新生成 `bindings/**`；在绑定重新生成之前，
 * 本函数返回 false，前端据实报错，不伪造应用信息。
 *
 * @param value 待判断的绑定值
 * @returns 已导出 `Snapshot` 方法时返回 true
 */
function hasSnapshotBinding(value: unknown): value is AppServiceSnapshotBinding {
  if (typeof value !== 'object' || value === null) {
    return false
  }
  const candidate = value as { Snapshot?: unknown }
  return typeof candidate.Snapshot === 'function'
}

/**
 * 读取应用信息快照。
 *
 * @returns 应用信息快照
 * @throws AppError 绑定尚未提供快照方法，或 Go 侧返回错误时的归一化错误
 */
export async function fetchAppInfo(): Promise<AppInfo> {
  const binding: unknown = AppService
  if (!hasSnapshotBinding(binding)) {
    throw toAppError(new Error(APP_INFO_UNAVAILABLE_MESSAGE))
  }
  try {
    return await binding.Snapshot()
  } catch (error) {
    throw toAppError(error)
  }
}

/**
 * 说明：主窗口显示策略已调整，前端不再需要主动请求显示窗口。
 *
 * 2026-11-01 曾实现过「窗口隐藏创建 + 前端调用 RevealWindow 显示 + Go 侧 1.5 秒兜底」，
 * 用来消除浅色主题下的深色闪屏。实测兜底定时器总是先于前端回调触发：前端必须等
 * 设置从数据库读回来才能确定主题，这段 IPC 往返加 WebView 首帧绘制超过 1.5 秒，
 * 用户感知到的是「窗口迟迟不出现」。
 *
 * 现在的做法：窗口立即显示，底色由 Go 侧按系统外观（Windows 读注册表的
 * AppsUseLightTheme）选择，首帧就不会闪；前端读回设置后如有差异再校正。
 */

