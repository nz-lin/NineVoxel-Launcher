//go:build !windows && !linux && !darwin

package platform

import (
	"os"
	"syscall"
)

// processState 在其他平台上只判断进程是否存在。
//
// 启动时间无法获取时返回 0，调用方据此走保守分支（宽限期后回收租约）。
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
