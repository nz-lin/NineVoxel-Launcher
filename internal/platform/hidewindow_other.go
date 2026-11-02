//go:build !windows

// 本文件是非 Windows 平台下「不弹控制台窗口」的空实现。
//
// macOS 与 Linux 不会为子进程额外创建控制台窗口，
// 因此这里只需要保持与 Windows 版本相同的函数签名，调用方无需按平台分支。
package platform

import "os/exec"

// HideWindow 在非 Windows 平台是空实现。
// 参数 cmd 未被使用，保留形参是为了让调用方写一份跨平台代码。
func HideWindow(cmd *exec.Cmd) {
	_ = cmd
}
