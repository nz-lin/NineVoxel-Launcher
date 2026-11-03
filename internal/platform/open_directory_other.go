//go:build !windows

package platform

import (
	"errors"
	"os/exec"
	"runtime"
)

func openDirectoryNative(target string) error {
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	}
	command := exec.Command(name, target)
	HideWindow(command)
	if err := command.Start(); err != nil {
		return errors.New("无法启动文件管理器")
	}
	go func() { _ = command.Wait() }()
	return nil
}
