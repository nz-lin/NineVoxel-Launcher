//go:build !windows && !darwin && !linux

package platform

// TotalMemoryBytes 返回本机物理内存总量（字节）。
//
// 其余平台尚未实现，如实返回 0 表示「未知」，调用方必须回退到保守默认值。
func TotalMemoryBytes() uint64 {
	return 0
}
