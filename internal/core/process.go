package core

import "example.com/ninevoxel/launcher/internal/platform"

// 进程状态在 core 包内的别名。
//
// 之所以不直接把 platform.ProcessState 用作核心概念：core 的判定逻辑
// （残留租约回收）不应依赖具体的平台枚举名，转一层便于将来替换探测方式。
type processStateValue int

const (
	// processUnknown 表示无法判定进程状态。
	processUnknown processStateValue = iota
	// processGone 表示进程已不存在。
	processGone
	// processAlive 表示进程存在。
	processAlive
)

// processStartTime 返回进程状态与启动时间（UTC 毫秒）。
//
// 单独抽成变量是为了让租约恢复的单元测试可以替换探测实现，不必真正启动进程。
var processStartTime = probeProcess

// probeProcess 是默认实现，转发到平台能力。
func probeProcess(pid uint32) (processStateValue, int64) {
	state, startMs := platform.Current.Identity.ProcessState(pid)
	switch state {
	case platform.ProcessGone:
		return processGone, startMs
	case platform.ProcessAlive:
		return processAlive, startMs
	default:
		return processUnknown, startMs
	}
}
