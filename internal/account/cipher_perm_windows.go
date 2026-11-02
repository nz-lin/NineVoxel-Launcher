//go:build windows

package account

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// systemSIDString 是本地 SYSTEM 账户的 SID 字符串。
//
// 密钥文件的 DACL 保留 SYSTEM 访问：系统服务/备份需要它，而 SYSTEM 本身
// 具备绕过 ACL 的调试特权，保留它不会扩大实际攻击面。
const systemSIDString = "S-1-5-18"

// secureCipherKeyPath 在 Windows 上用安全描述符把 path 的 DACL 收紧为
// 「仅当前登录用户 + SYSTEM」且 DACL 受保护（不再继承父目录的 ACE）。
//
// Windows 完全忽略 os.OpenFile/os.MkdirAll 的 0600/0700 权限位：权限位只影响
// Go 侧 Mode() 的呈现，新对象实际继承父目录的 ACE（通常含 Authenticated Users、
// Users 等宽泛组），密钥对同机其他用户可读。因此必须显式设置 DACL。
//
// isDir 为 true 时给 ACE 加上 OI/CI 继承标志，使目录中后续创建的密钥文件也
// 自动只对当前用户与 SYSTEM 开放。
//
// 本函数幂等：对已存在的文件/目录重复调用得到相同 ACL，因此加载已存在的密钥时
// 也会调用它，收敛可能被外部放宽或历史版本留下的 ACL。
func secureCipherKeyPath(path string, isDir bool) error {
	userSID, err := currentUserSIDString()
	if err != nil {
		return err
	}

	// SDDL 说明：D:P 表示受保护 DACL（不继承父目录）；A 表示允许 ACE；
	// 第三段 FA 是 FILE_ALL_ACCESS；第五段为空（不限定对象类型）；
	// 第六段是受托者 SID。
	inherit := ""
	if isDir {
		inherit = "OICI" // OBJECT_INHERIT_ACE | CONTAINER_INHERIT_ACE
	}
	sddl := fmt.Sprintf(
		"D:P(A;%s;FA;;;%s)(A;%s;FA;;;%s)",
		inherit, userSID,
		inherit, systemSIDString,
	)

	descriptor, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return fmt.Errorf("构造密钥路径安全描述符失败: %w", err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return fmt.Errorf("读取密钥路径安全描述符 DACL 失败: %w", err)
	}

	// PROTECTED_DACL_SECURITY_INFORMATION 必须与 DACL_SECURITY_INFORMATION 一起给，
	// 否则父目录继承来的宽泛 ACE 会与显式 ACE 合并，收紧不彻底。
	info := windows.SECURITY_INFORMATION(
		windows.DACL_SECURITY_INFORMATION | windows.PROTECTED_DACL_SECURITY_INFORMATION,
	)
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, info, nil, nil, dacl, nil); err != nil {
		return fmt.Errorf("设置密钥路径 DACL 失败: %w", err)
	}
	return nil
}

// currentUserSIDString 返回当前进程令牌所属用户的 SID 字符串。
func currentUserSIDString() (string, error) {
	tokenUser, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", fmt.Errorf("读取当前用户令牌失败: %w", err)
	}
	if tokenUser.User.Sid == nil {
		return "", fmt.Errorf("当前用户令牌缺少 SID")
	}
	return tokenUser.User.Sid.String(), nil
}
