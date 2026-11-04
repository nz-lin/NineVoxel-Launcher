//go:build darwin

package platform

import (
	"os"
	"syscall"
)

// processState 查询 macOS 进程是否存在及其启动时间。
//
// 存在性用 `signal 0` 判定；启动时间在本实现中不读取——读取它需要
// `KERN_PROC` sysctl 或 `proc_pidinfo`，两者都要在 syscall 层做结构体布局适配，
// 在未正式支持 macOS 的租约恢复之前，返回「存在但启动时间未知」让调用方
// 走保守分支（宽限期后回收），比读取错误的值更安全。
func processState(pid uint32) (ProcessState, int64) {
	if pid == 0 {
		return ProcessGone, 0
	}
	process, err := os.FindProcess(int(pid))
	if err != nil {
		return ProcessGone, 0
	}
	if signalErr := process.Signal(syscall.Signal(0)); signalErr != nil {
		if signalErr == syscall.ESRCH {
			return ProcessGone, 0
		}
		if signalErr == syscall.EPERM {
			return ProcessAlive, 0
		}
		return ProcessUnknown, 0
	}
	return ProcessAlive, 0
}
