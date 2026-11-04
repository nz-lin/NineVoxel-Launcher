//go:build windows

package platform

import (
	"syscall"
	"unsafe"
)

var (
	openProcessProc     = kernel32DLL.NewProc("OpenProcess")
	getProcessTimesProc = kernel32DLL.NewProc("GetProcessTimes")
	closeHandleProc     = kernel32DLL.NewProc("CloseHandle")
)

// Windows 常量。syscall 包未导出它们，这里按 Win32 头文件声明。
const (
	// processQueryLimited 允许查询进程基本信息（含创建时间）。
	processQueryLimited = 0x1000
	// errorInvalidParameter 是 OpenProcess 在目标进程不存在时返回的错误码。
	errorInvalidParameter syscall.Errno = 87
)

// processState 查询 Windows 进程是否存在及其启动时间。
//
// 实现要点：
//   - `OpenProcess` 返回 ERROR_INVALID_PARAMETER 表示进程不存在；
//   - 其他失败（典型是拒绝访问）无法区分「已退出」与「无权限」，返回
//     ProcessUnknown 由调用方保守处理；
//   - 启动时间取 `GetProcessTimes` 的创建时间，转换为 UTC 毫秒。
func processState(pid uint32) (ProcessState, int64) {
	if pid == 0 {
		return ProcessGone, 0
	}
	handle, _, callErr := openProcessProc.Call(
		uintptr(processQueryLimited),
		0,
		uintptr(pid),
	)
	if handle == 0 {
		if errno, ok := callErr.(syscall.Errno); ok && errno == errorInvalidParameter {
			return ProcessGone, 0
		}
		return ProcessUnknown, 0
	}
	defer closeHandleProc.Call(handle)

	var creationTime, exitTime, kernelTime, userTime syscall.Filetime
	result, _, _ := getProcessTimesProc.Call(
		handle,
		uintptr(unsafe.Pointer(&creationTime)),
		uintptr(unsafe.Pointer(&exitTime)),
		uintptr(unsafe.Pointer(&kernelTime)),
		uintptr(unsafe.Pointer(&userTime)),
	)
	if result == 0 {
		return ProcessUnknown, 0
	}

	// Filetime 以 1601-01-01 为纪元，单位 100ns。
	const (
		ticksPerMillisecond = 10000
		epochDiffMs         = 11644473600000
	)
	ticks := int64(creationTime.HighDateTime)<<32 | int64(creationTime.LowDateTime)
	return ProcessAlive, ticks/ticksPerMillisecond - epochDiffMs
}
