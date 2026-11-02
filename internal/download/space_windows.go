//go:build windows

package download

import (
	"math"
	"syscall"
	"unsafe"
)

// kernel32 的动态库句柄与过程地址在包级只解析一次：
// 每次创建下载任务都重新 LoadLibrary 是没必要的系统调用开销。
var (
	kernel32DLL            = syscall.NewLazyDLL("kernel32.dll")
	getDiskFreeSpaceExProc = kernel32DLL.NewProc("GetDiskFreeSpaceExW")
)

// availableBytes 读取 Windows 上 path 所在卷的可用空间。
//
// 卡面要求用 GetDiskFreeSpaceEx，但标准库 syscall 包在 Windows 上并未导出
// 该函数（只有部分 API 被包了一层），因此这里用 syscall.NewLazyDLL 直接调用
// kernel32 的 GetDiskFreeSpaceExW——同样只用标准库，不引入任何新依赖。
//
// 取的是「调用者可用空间」（lpFreeBytesAvailableToCaller）而不是总剩余空间：
// 磁盘配额场景下两者不同，配额才是真正能写多少。
func availableBytes(path string) (int64, *Error) {
	pointer, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0, newSystemError(ErrorCodeSpace, messageDiskSpaceProbeFailed, false)
	}
	var availableToCaller uint64
	var totalBytes uint64
	var freeBytes uint64
	result, _, _ := getDiskFreeSpaceExProc.Call(
		uintptr(unsafe.Pointer(pointer)),
		uintptr(unsafe.Pointer(&availableToCaller)),
		uintptr(unsafe.Pointer(&totalBytes)),
		uintptr(unsafe.Pointer(&freeBytes)),
	)
	if result == 0 {
		return 0, newSystemError(ErrorCodeSpace, messageDiskSpaceProbeFailed, false)
	}
	if availableToCaller > math.MaxInt64 {
		return math.MaxInt64, nil
	}
	return int64(availableToCaller), nil
}
