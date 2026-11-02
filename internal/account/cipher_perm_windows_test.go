//go:build windows

package account

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// assertCipherKeyPermissions 在 Windows 上直接读回密钥文件与密钥目录的 DACL：
// 二者必须是受保护的 DACL（不继承父目录），且受托者集合恰好是
// 「当前登录用户 + SYSTEM」——既不能缺少当前用户（否则自己读不到密钥），
// 也不能出现 Everyone / Authenticated Users / Users 等宽泛组。
func assertCipherKeyPermissions(t *testing.T, keyPath string) {
	t.Helper()
	assertCipherPathACL(t, filepath.Dir(keyPath), true)
	assertCipherPathACL(t, keyPath, false)
}

// assertCipherPathACL 校验单个路径的 DACL。
func assertCipherPathACL(t *testing.T, path string, isDir bool) {
	t.Helper()

	kind := "密钥文件"
	if isDir {
		kind = "密钥目录"
	}

	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatalf("%s读取安全描述符失败: %v", kind, err)
	}
	if sd == nil {
		t.Fatalf("%s没有安全描述符", kind)
	}

	control, _, err := sd.Control()
	if err != nil {
		t.Fatalf("%s读取安全描述符控制位失败: %v", kind, err)
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		t.Fatalf("%s的 DACL 未受保护，仍会合并父目录继承来的 ACE", kind)
	}

	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatalf("%s读取 DACL 失败: %v", kind, err)
	}
	if dacl == nil {
		t.Fatalf("%s的 DACL 为空，等同于完全放行", kind)
	}

	userSID, err := currentUserSIDString()
	if err != nil {
		t.Fatalf("读取当前用户 SID 失败: %v", err)
	}
	expected := map[string]bool{
		userSID:         true,
		systemSIDString: true,
	}

	seen := make(map[string]bool, len(expected))
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			t.Fatalf("%s读取第 %d 个 ACE 失败: %v", kind, i, err)
		}
		if ace == nil {
			t.Fatalf("%s第 %d 个 ACE 为空", kind, i)
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			t.Fatalf("%s第 %d 个 ACE 类型期望允许（0），实际 %d", kind, i, ace.Header.AceType)
		}
		// 至少要有读写权限，否则当前用户自己都打不开密钥文件。
		if ace.Mask&windows.FILE_READ_DATA == 0 || ace.Mask&windows.FILE_WRITE_DATA == 0 {
			t.Fatalf("%s第 %d 个 ACE 缺少读写权限: 掩码 0x%x", kind, i, uint32(ace.Mask))
		}

		// ACCESS_ALLOWED_ACE 的尾部就是 SID 本身（见 Win32 ACCESS_ALLOWED_ACE 布局）。
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		name := sid.String()
		if !expected[name] {
			t.Fatalf("%s的 DACL 含有不应出现的受托者 %s（宽泛组或未知账户）", kind, name)
		}
		if seen[name] {
			t.Fatalf("%s的 DACL 对 %s 重复授权", kind, name)
		}
		seen[name] = true
	}

	if len(seen) != len(expected) {
		t.Fatalf("%s的 DACL 受托者集合不完整：期望 %d 个，实际 %d 个", kind, len(expected), len(seen))
	}
}

// TestCipherSecuresExistingKeyACLOnWindows 验证加载已存在的密钥时会幂等收敛 ACL。
//
// 先把密钥文件与目录的 DACL 人为放宽成 Everyone 完全控制（模拟历史版本或外部
// 工具留下的宽泛 ACL），再次 OpenCipher 后必须重新收紧到当前用户 + SYSTEM，
// 且密钥内容保持不变。
func TestCipherSecuresExistingKeyACLOnWindows(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "secrets", "account.key")

	if _, cryptoErr := OpenCipher(keyPath); cryptoErr != nil {
		t.Fatalf("首次 OpenCipher 失败: %v", cryptoErr)
	}
	keyBefore, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("读取密钥文件失败: %v", err)
	}

	if err := widenDACLToEveryone(filepath.Dir(keyPath), true); err != nil {
		t.Fatalf("放宽密钥目录 DACL 失败: %v", err)
	}
	if err := widenDACLToEveryone(keyPath, false); err != nil {
		t.Fatalf("放宽密钥文件 DACL 失败: %v", err)
	}

	// 再次打开必须把 ACL 收敛回来，而不是只在首次创建时收紧。
	if _, cryptoErr := OpenCipher(keyPath); cryptoErr != nil {
		t.Fatalf("再次 OpenCipher 失败: %v", cryptoErr)
	}
	assertCipherKeyPermissions(t, keyPath)

	keyAfter, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("再次读取密钥文件失败: %v", err)
	}
	if !bytes.Equal(keyBefore, keyAfter) {
		t.Fatal("收敛 ACL 时改写了密钥文件内容")
	}
}

// widenDACLToEveryone 把路径的 DACL 设成「Everyone 完全控制」的受保护 DACL，
// 用于模拟被外部放宽的 ACL。
func widenDACLToEveryone(path string, isDir bool) error {
	inherit := ""
	if isDir {
		inherit = "OICI"
	}
	sd, err := windows.SecurityDescriptorFromString(fmt.Sprintf("D:P(A;%s;FA;;;WD)", inherit))
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	info := windows.SECURITY_INFORMATION(
		windows.DACL_SECURITY_INFORMATION | windows.PROTECTED_DACL_SECURITY_INFORMATION,
	)
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, info, nil, nil, dacl, nil)
}
