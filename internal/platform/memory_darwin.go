//go:build darwin

package platform

import "golang.org/x/sys/unix"

// TotalMemoryBytes 返回 macOS 的本机物理内存总量（字节）。
//
// 使用 sysctl `hw.memsize`，读取失败时返回 0 表示未知，不猜数值。
func TotalMemoryBytes() uint64 {
	value, err := unix.SysctlUint64("hw.memsize")
	if err != nil {
		return 0
	}
	return value
}
