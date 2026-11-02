//go:build windows

// 本文件提供 Windows 下的子进程「不弹控制台窗口」能力。
//
// 背景（2026-11-01 真机验收）：Java 探测与校验用 `exec.CommandContext` 启动
// `java.exe -version`，Windows 会给这个控制台程序分配一个新的控制台窗口，
// 表现为「打开 Java 运行时页面时闪出一个黑框，随即消失」。
// 隐藏窗口只影响「是否显示控制台」，不改变进程行为、退出码与输出采集。
package platform

import (
	"os/exec"
	"syscall"
)

// creationFlagsNoWindow 是 Windows 的 CREATE_NO_WINDOW（0x08000000）标志。
// 与 HideWindow 一起使用：HideWindow 管窗口显示方式，该标志确保不创建控制台。
const creationFlagsNoWindow = 0x08000000

// HideWindow 让 cmd 在 Windows 下不弹出控制台窗口。
// 参数 cmd 为待启动的子进程；传 nil 时安全返回。
// 注意：本函数必须在 cmd.Start / cmd.Run 之前调用，否则不生效。
func HideWindow(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= creationFlagsNoWindow
}
