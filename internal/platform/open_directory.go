package platform

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// directoryOpener is replaceable in tests so no desktop window is opened.
var directoryOpener = openDirectoryNative

// OpenDirectory opens an existing directory with the platform file manager.
// The native implementation is selected per platform.
func OpenDirectory(target string) error {
	target = strings.TrimSpace(target)
	if target == "" {
		return errors.New("目录路径为空")
	}
	absolute, err := filepath.Abs(target)
	if err != nil {
		return errors.New("目录路径无效")
	}
	info, err := os.Stat(absolute)
	if err != nil || !info.IsDir() {
		return errors.New("目录不存在")
	}
	return directoryOpener(absolute)
}
