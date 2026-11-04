//go:build linux

package platform

import (
	"os"
	"strconv"
	"strings"
	"syscall"
)

// processState 查询 Linux 进程是否存在及其启动时间。
//
// 实现要点：
//   - 先用 `signal 0` 判断存在性：`ESRCH` 表示不存在，`EPERM` 表示存在但无权限；
//   - 启动时间读 `/proc/<pid>/stat` 的第 22 个字段（自开机的时钟滴答数），
//     乘以滴答周期换算为毫秒，再加上开机时间得到绝对时间。
//
// 不单独依赖 `/proc`：在受限容器中它可能不可读，此时退化到「只判存在性」，
// 由调用方按「无法确认启动时间」保守处理。
func processState(pid uint32) (ProcessState, int64) {
	if pid == 0 {
		return ProcessGone, 0
	}
	process, err := os.FindProcess(int(pid))
	if err != nil {
		return ProcessGone, 0
	}
	if signalErr := process.Signal(syscall.Signal(0)); signalErr != nil {
		if signalErr == syscall.ESRCH {
			return ProcessGone, 0
		}
		if signalErr == syscall.EPERM {
			return ProcessAlive, 0
		}
		return ProcessUnknown, 0
	}
	return ProcessAlive, linuxProcessStartMs(pid)
}

// linuxProcessStartMs 返回进程启动的绝对时间（UTC 毫秒）；无法获取时返回 0。
func linuxProcessStartMs(pid uint32) int64 {
	content, err := os.ReadFile("/proc/" + strconv.FormatUint(uint64(pid), 10) + "/stat")
	if err != nil {
		return 0
	}
	// 进程名可能包含空格与括号，因此从最后一个 ')' 之后开始切分字段。
	text := string(content)
	closing := strings.LastIndexByte(text, ')')
	if closing < 0 || closing+2 >= len(text) {
		return 0
	}
	fields := strings.Fields(text[closing+2:])
	// fields[0] 对应 stat 的第 3 个字段（state），starttime 是第 22 个字段。
	const startTimeOffset = 22 - 3
	if len(fields) <= startTimeOffset {
		return 0
	}
	ticks, convErr := strconv.ParseInt(fields[startTimeOffset], 10, 64)
	if convErr != nil {
		return 0
	}
	upMs := linuxUptimeMs()
	if upMs <= 0 {
		return 0
	}
	return upMs - (ticks*1000)/clockTicksPerSecond()
}

// linuxUptimeMs 返回开机至今的毫秒数。
func linuxUptimeMs() int64 {
	content, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(content))
	if len(fields) == 0 {
		return 0
	}
	seconds, convErr := strconv.ParseFloat(fields[0], 64)
	if convErr != nil || seconds <= 0 {
		return 0
	}
	return int64(seconds * 1000)
}

// clockTicksPerSecond 返回内核的时钟滴答频率。
//
// Linux 用户态无法直接读取 HZ，通常为 100；取 100 与绝大多数发行版一致，
// 换算误差最多几秒，对于「识别 PID 复用」这个用途足够。
func clockTicksPerSecond() int64 { return 100 }
