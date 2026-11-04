//go:build linux

package platform

import (
	"bufio"
	"os"
	"strconv"
	"strings"
)

// TotalMemoryBytes 返回 Linux 的本机物理内存总量（字节）。
//
// 读取 /proc/meminfo 的 MemTotal（单位 kB）；解析失败时返回 0 表示未知。
func TotalMemoryBytes() uint64 {
	file, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0
	}
	defer func() { _ = file.Close() }()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "MemTotal:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0
		}
		kib, convErr := strconv.ParseUint(fields[1], 10, 64)
		if convErr != nil {
			return 0
		}
		return kib * 1024
	}
	return 0
}
