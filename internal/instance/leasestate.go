package instance

import (
	"example.com/ninevoxel/launcher/internal/core"
)

// versionInUseByRunningGame 判断某个版本是否正被游戏进程使用。
//
// 读磁盘上的租约表而不是内存快照：实例侧从不登记租约，它的内存快照永远
// 是空的，用内存判定会误判为「没有游戏在运行」。
//
// 无法读取租约表时按「正在使用」处理：宁可留下一个无人引用的版本目录，
// 也不能删掉游戏正在加载的 JAR。
func (s *InstanceService) versionInUseByRunningGame(versionID string) bool {
	if versionID == "" || s == nil || s.root == "" {
		return false
	}
	leases, err := core.ReadActiveLeases(s.root)
	if err != nil {
		return true
	}
	_, active := leases[versionID]
	return active
}
