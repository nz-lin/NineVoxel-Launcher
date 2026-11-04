package platform

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// MigrateLegacyAppData 把旧平台目录中的数据与日志迁移到 AppRoot。
//
// 只处理「新版目录尚不存在或为空」的情形；如果新目录已经有数据，视为用户
// 已经完成迁移，不合并、不覆盖。迁移失败返回错误，由启动流程中止，
// 避免在新目录静默创建一个空实例库让用户以为数据丢失。
func MigrateLegacyAppData() error {
	root := AppRoot()
	if root == "" {
		return nil
	}
	return migrateLegacyAppData(root, legacyAppRoots())
}

// migrateLegacyAppData 是 MigrateLegacyAppData 的可测试实现。
func migrateLegacyAppData(newRoot string, legacyRoots []string) error {
	if newRoot == "" {
		return nil
	}
	newData := filepath.Join(newRoot, dataDirName)
	newDataExists, newDataEmpty, statErr := inspectDirectory(newData)
	if statErr != nil {
		return statErr
	}
	if newDataExists && !newDataEmpty {
		// 新目录已经有数据：只补旧日志，不碰数据。
		copyLegacyLogs(newRoot, legacyRoots)
		return nil
	}

	for _, oldRoot := range legacyRoots {
		oldData := filepath.Join(oldRoot, dataDirName)
		exists, empty, err := inspectDirectory(oldData)
		if err != nil || !exists || empty {
			continue
		}
		if filepath.Clean(oldData) == filepath.Clean(newData) {
			break
		}
		if err := os.MkdirAll(filepath.Dir(newData), 0o755); err != nil {
			return fmt.Errorf("迁移数据目录失败：无法创建 %s", filepath.Dir(newData))
		}
		if newDataExists {
			if err := os.Remove(newData); err != nil {
				return fmt.Errorf("迁移数据目录失败：无法清理空目录 %s", newData)
			}
		}
		if err := movePath(oldData, newData); err != nil {
			return fmt.Errorf("迁移数据目录失败：%w", err)
		}
		break
	}

	copyLegacyLogs(newRoot, legacyRoots)
	return nil
}

// inspectDirectory 返回目录是否存在、是否为空；不存在不视为错误。
func inspectDirectory(path string) (exists bool, empty bool, err error) {
	info, statErr := os.Stat(path)
	if errors.Is(statErr, os.ErrNotExist) {
		return false, false, nil
	}
	if statErr != nil {
		return false, false, statErr
	}
	if !info.IsDir() {
		return true, false, fmt.Errorf("%s 不是目录", path)
	}
	entries, readErr := os.ReadDir(path)
	if readErr != nil {
		return true, false, readErr
	}
	return true, len(entries) == 0, nil
}

// copyLegacyLogs 把旧目录中的日志复制到新日志目录，不覆盖已有文件。
func copyLegacyLogs(newRoot string, legacyRoots []string) {
	newLogs := filepath.Join(newRoot, logDirName)
	for _, oldRoot := range legacyRoots {
		oldLogs := filepath.Join(oldRoot, logDirName)
		if filepath.Clean(oldLogs) == filepath.Clean(newLogs) {
			continue
		}
		_ = copyMissing(oldLogs, newLogs)
	}
}

// movePath 优先用 rename 移动目录；跨卷时先复制到临时目录再原子改名。
//
// 直接复制到 target 的风险：中途失败会留下半份数据，下一次启动看到
// target 非空就误以为迁移成功。因此先写 `.migrating-*`，成功后再改名。
func movePath(source, target string) error {
	if err := os.Rename(source, target); err == nil {
		return nil
	}
	parent := filepath.Dir(target)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	temp, err := os.MkdirTemp(parent, ".data-migrating-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(temp) }()
	if err := copyDirectory(source, temp); err != nil {
		return err
	}
	if err := os.Rename(temp, target); err != nil {
		return err
	}
	_ = os.RemoveAll(source)
	return nil
}

// copyDirectory 递归复制目录，保留普通文件权限与符号链接。
func copyDirectory(source, target string) error {
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		destination := filepath.Join(target, relative)
		if entry.IsDir() {
			return os.MkdirAll(destination, 0o755)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			linkTarget, readErr := os.Readlink(path)
			if readErr != nil {
				return readErr
			}
			return os.Symlink(linkTarget, destination)
		}
		return copyFileWithMode(path, destination)
	})
}

// copyMissing 复制目录中 target 不存在的文件，不覆盖已有日志。
func copyMissing(source, target string) error {
	if _, err := os.Stat(source); err != nil {
		return nil
	}
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		destination := filepath.Join(target, relative)
		if entry.IsDir() {
			return os.MkdirAll(destination, 0o755)
		}
		if _, statErr := os.Lstat(destination); statErr == nil {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			linkTarget, readErr := os.Readlink(path)
			if readErr != nil {
				return readErr
			}
			return os.Symlink(linkTarget, destination)
		}
		return copyFileWithMode(path, destination)
	})
}

// copyFileWithMode 复制一个普通文件并保留权限。
func copyFileWithMode(source, target string) error {
	info, err := os.Stat(source)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s 不是普通文件", source)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() { _ = input.Close() }()
	output, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(output, input); err != nil {
		_ = output.Close()
		return err
	}
	return output.Close()
}
