//go:build !windows

// 非 Windows 平台的系统外观查询实现。

package platform

// SystemPrefersDark 判断当前平台是否处于深色模式。
//
// 用途：主窗口创建时据此选择背景色，避免前端读回主题前闪出刺眼的底色。
// macOS 与 Linux 目前没有可靠的、零依赖的同步查询方式，因此统一返回 false；
// 这两个平台的主窗口会使用浅色底，前端读到真实主题后立即校正。
// 后续若需要精确支持，可分别用 NSUserDefaults（macOS）与
// org.freedesktop.portal.Settings（Linux）实现，属于独立技术验证任务。
func SystemPrefersDark() bool {
	return false
}
