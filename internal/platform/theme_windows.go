//go:build windows

// Windows 平台的系统外观查询实现。

package platform

import (
	"syscall"
	"unsafe"
)

// personalizeKeyPath 是 Windows 记录「应用与系统外观」的注册表路径。
const personalizeKeyPath = `Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`

// appsUseLightThemeValue 是「应用是否使用浅色主题」的值名。
//
// 该值为 DWORD：1 表示应用使用浅色主题，0 表示应用使用深色主题。
// 注意它和 SystemUsesLightTheme（任务栏、开始菜单）是两个独立的值，
// 启动器属于「应用」，因此读 AppsUseLightTheme。
const appsUseLightThemeValue = "AppsUseLightTheme"

// regDWORDType 是注册表 REG_DWORD 的类型常量。
const regDWORDType = 4

// 注意：以下常量与 syscall.Handle 配合使用，不引入 golang.org/x/sys，
// 保持 P3 阶段确立的「零新增依赖」约束。
const (
	hkeyCurrentUser = 0x80000001
	keyQueryValue   = 0x0001
)

// SystemPrefersDark 判断 Windows 当前是否处于深色模式。
//
// 用途：主窗口在创建时就把背景色设成与系统外观一致的颜色，
// 这样即使前端还没把主题从设置库读回来，用户也不会先看到刺眼的深色底
// 或纯白底。读取失败时一律按浅色处理：浅色是 Windows 的默认外观，
// 猜错的视觉代价也比深色小。
func SystemPrefersDark() bool {
	var key syscall.Handle
	pathPtr, err := syscall.UTF16PtrFromString(personalizeKeyPath)
	if err != nil {
		return false
	}

	if err := syscall.RegOpenKeyEx(hkeyCurrentUser, pathPtr, 0, keyQueryValue, &key); err != nil {
		return false
	}
	defer func() {
		// 关句柄失败不影响外观判断结果，忽略返回值。
		_ = syscall.RegCloseKey(key)
	}()

	valuePtr, err := syscall.UTF16PtrFromString(appsUseLightThemeValue)
	if err != nil {
		return false
	}

	var (
		// DWORD 只有 4 字节，按 4 字节读够用。
		buffer  [4]byte
		size    uint32 = uint32(len(buffer))
		valType uint32
	)

	if err := syscall.RegQueryValueEx(key, valuePtr, nil, &valType, &buffer[0], &size); err != nil {
		return false
	}
	if valType != regDWORDType || size < 4 {
		return false
	}

	useLight := *(*uint32)(unsafe.Pointer(&buffer[0]))
	// 值 1 表示浅色，0 表示深色。
	return useLight == 0
}
