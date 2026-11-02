//go:build !windows

package download

import (
	"math"
	"syscall"
)

// availableBytes 读取类 Unix 系统上 path 所在卷的可用空间。
//
// 按卡面第 7 条要求使用 syscall.Statfs，不引入任何新依赖。
// 取 Bavail（普通用户可用块数）而不是 Bfree：Bfree 含 root 保留块，
// 用它会让预检在接近写满时给出过于乐观的结论。
//
// 注意：darwin 与 linux 上 Statfs_t.Bavail / Bsize 的底层类型不同
// （uint64 / uint32 混用），因此统一显式转换为 int64 再相乘。
func availableBytes(path string) (int64, *Error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, newSystemError(ErrorCodeSpace, messageDiskSpaceProbeFailed, false)
	}
	available := int64(stat.Bavail) * int64(stat.Bsize)
	if available < 0 || uint64(available) > math.MaxInt64 {
		return math.MaxInt64, nil
	}
	return available, nil
}
