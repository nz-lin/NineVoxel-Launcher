package platform

import (
	"os"
	"path/filepath"
	"runtime"
)

// dataDirName 是 AppRoot 下的数据子目录名。
const dataDirName = "data"

// AppRoot 返回应用根目录。
//
// 目录策略（ADR-011 的平台修正版）：
//  1. 便携模式：可执行文件同级已存在 `data/` 时，AppRoot 就是可执行文件目录；
//  2. macOS：`~/Library/Application Support/NineVoxel-Launcher`；
//  3. Windows：`%LocalAppData%\NineVoxel-Launcher`（避免大文件进入 Roaming）；
//  4. Linux：`$XDG_DATA_HOME/NineVoxel-Launcher` 或 `~/.local/share/NineVoxel-Launcher`；
//  5. 其他平台：回退用户配置目录。
//
// 返回空串表示当前环境无法解析应用根目录。
func AppRoot() string {
	exeDir := ""
	portable := false
	if exePath, err := os.Executable(); err == nil && exePath != "" {
		exeDir = filepath.Dir(exePath)
		if info, statErr := os.Stat(filepath.Join(exeDir, dataDirName)); statErr == nil && info.IsDir() {
			portable = true
		}
	}
	if portable {
		return exeDir
	}

	base := appRootBase()
	if base == "" {
		return ""
	}
	return filepath.Join(base, appDirName)
}

// appRootBase 返回当前平台的用户级应用基目录。
func appRootBase() string {
	switch runtime.GOOS {
	case "darwin":
		if dir, err := os.UserConfigDir(); err == nil && dir != "" {
			return dir
		}
	case "windows":
		if dir, err := os.UserCacheDir(); err == nil && dir != "" {
			return dir
		}
		if dir, err := os.UserConfigDir(); err == nil && dir != "" {
			return dir
		}
	case "linux":
		if dir := xdgDataHome(); dir != "" {
			return dir
		}
		if dir, err := os.UserConfigDir(); err == nil && dir != "" {
			return dir
		}
	default:
		if dir, err := os.UserConfigDir(); err == nil && dir != "" {
			return dir
		}
		if dir, err := os.UserCacheDir(); err == nil && dir != "" {
			return dir
		}
	}
	return ""
}

// xdgDataHome 返回 XDG_DATA_HOME 或 ~/.local/share。
func xdgDataHome() string {
	if dir := os.Getenv("XDG_DATA_HOME"); dir != "" && filepath.IsAbs(dir) {
		return dir
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".local", "share")
	}
	return ""
}

// legacyAppRoots 返回需要迁移的旧 AppRoot 列表。
//
// macOS 的旧路径与新路径一致；便携模式没有迁移需求。
func legacyAppRoots() []string {
	current := AppRoot()
	if current == "" {
		return nil
	}
	roots := make([]string, 0, 1)
	add := func(base string) {
		if base == "" {
			return
		}
		candidate := filepath.Join(base, appDirName)
		if filepath.Clean(candidate) == filepath.Clean(current) {
			return
		}
		roots = append(roots, candidate)
	}
	switch runtime.GOOS {
	case "windows":
		if dir, err := os.UserConfigDir(); err == nil {
			add(dir)
		}
	case "linux":
		if dir, err := os.UserConfigDir(); err == nil {
			add(dir)
		}
	}
	return roots
}
