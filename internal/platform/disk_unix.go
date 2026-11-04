//go:build !windows

package platform

import (
	"errors"
	"math"
	"syscall"
)

// availableBytes 读取类 Unix 系统上 path 所在卷的可用空间。
//
// 使用 Bavail（普通用户可用块数）而不是 Bfree：Bfree 含 root 保留块，
// 用它会让预检在接近写满时给出过于乐观的结论。
func availableBytes(path string) (int64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, errors.New("无法读取磁盘可用空间")
	}
	available := int64(stat.Bavail) * int64(stat.Bsize)
	if available < 0 || uint64(available) > math.MaxInt64 {
		return math.MaxInt64, nil
	}
	return available, nil
}
