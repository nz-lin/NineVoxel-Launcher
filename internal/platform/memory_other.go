//go:build !windows

package platform

// TotalMemoryBytes 返回本机物理内存总量（字节）。
//
// 非 Windows 平台尚未实现（Linux 可读 /proc/meminfo，macOS 需要 sysctl，
// 两者都依赖各自的系统接口，属于独立的一张卡）。这里如实返回 0 表示「未知」，
// 而不是猜一个数字：调用方必须回退到保守默认值。
func TotalMemoryBytes() uint64 {
	return 0
}
