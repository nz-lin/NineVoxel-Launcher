//go:build windows

package platform

import (
	"errors"
	"os/exec"
)

func openDirectoryNative(target string) error {
	command := exec.Command("explorer.exe", target)
	// explorer.exe 是 GUI 进程；设置 CREATE_NO_WINDOW 会导致它无法显示窗口。
	if err := command.Start(); err != nil {
		return errors.New("无法启动资源管理器")
	}
	go func() { _ = command.Wait() }()
	return nil
}
