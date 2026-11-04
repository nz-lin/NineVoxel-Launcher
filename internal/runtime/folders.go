package runtime

import (
	"errors"
	"os"
	"path/filepath"

	"example.com/ninevoxel/launcher/internal/artifact"
	"example.com/ninevoxel/launcher/internal/platform"
)

// openRuntimeDirectory is replaceable in tests so no desktop window is opened.
var openRuntimeDirectory = platform.OpenDirectory

// OpenRuntimeFolder 打开数据根下的 runtimes 目录。
//
// 目录不存在时先创建：用户刚安装启动器、还没下载任何运行时也能打开。
func (s *RuntimeService) OpenRuntimeFolder() error {
	root, appErr := runtimeDataRoot()
	if appErr != nil {
		return appErr
	}
	target := filepath.Join(root, artifact.DirRuntimes)
	if err := os.MkdirAll(target, 0o755); err != nil {
		return errors.New("无法创建运行时目录")
	}
	return openRuntimeDirectory(target)
}
