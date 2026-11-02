//go:build !windows

package account

import (
	"os"
	"path/filepath"
	"testing"
)

// assertCipherKeyPermissions 在非 Windows 平台保留原有的权限位断言：
// 密钥文件必须是 0600、密钥目录必须是 0700。
func assertCipherKeyPermissions(t *testing.T, keyPath string) {
	t.Helper()

	fileInfo, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("密钥文件未创建: %v", err)
	}
	if perm := fileInfo.Mode().Perm(); perm != cipherKeyFileMode {
		t.Fatalf("密钥文件权限期望 %o，实际 %o", cipherKeyFileMode, perm)
	}

	dirInfo, err := os.Stat(filepath.Dir(keyPath))
	if err != nil {
		t.Fatalf("密钥目录未创建: %v", err)
	}
	if perm := dirInfo.Mode().Perm(); perm != cipherKeyDirMode {
		t.Fatalf("密钥目录权限期望 %o，实际 %o", cipherKeyDirMode, perm)
	}
}
