//go:build darwin

package platform

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"time"
)

// SystemPrefersDark 判断 macOS 当前是否处于深色模式。
//
// 优先读进程环境里的 AppleInterfaceStyle（命令行启动时通常存在），
// 没有时再调用 `/usr/bin/defaults read -g AppleInterfaceStyle`。
// 查询失败一律按浅色处理，避免启动时先闪深色底。
func SystemPrefersDark() bool {
	if strings.EqualFold(strings.TrimSpace(os.Getenv("AppleInterfaceStyle")), "Dark") {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "/usr/bin/defaults", "read", "-g", "AppleInterfaceStyle")
	HideWindow(command)
	output, err := command.Output()
	if err != nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(string(output)), "Dark")
}
