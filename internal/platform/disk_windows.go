//go:build windows

package platform

import (
	"errors"
	"math"
	"syscall"
	"unsafe"
)

var (
	kernel32DLL            = syscall.NewLazyDLL("kernel32.dll")
	getDiskFreeSpaceExProc = kernel32DLL.NewProc("GetDiskFreeSpaceExW")
)

// availableBytes 读取 Windows 上 path 所在卷的可用空间。
//
// 取 lpFreeBytesAvailableToCaller 而不是总剩余空间：配额场景下两者不同，
// 配额才是调用者真正能写多少。
func availableBytes(path string) (int64, error) {
	pointer, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0, errors.New("磁盘路径无效")
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
		return 0, errors.New("无法读取磁盘可用空间")
	}
	if availableToCaller > math.MaxInt64 {
		return math.MaxInt64, nil
	}
	return int64(availableToCaller), nil
}
