//go:build !windows

package account

// secureCipherKeyPath 在非 Windows 平台上不做任何额外处理。
//
// 这些平台上 os.OpenFile/os.MkdirAll 的 0600/0700 权限位本身就会生效，
// 加载已存在的密钥时也不改变既有语义：保持原有行为，只由 Windows 实现负责
// 收紧 DACL。
func secureCipherKeyPath(path string, isDir bool) error {
	return nil
}
