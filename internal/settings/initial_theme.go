// 本文件提供「应用启动早期读取用户主题偏好」的入口。
//
// 为什么需要在服务之外单独读一次：
// 主窗口在 application.New 之后、Run 之前创建，那一刻设置服务还没启动、
// 内存快照还是默认值。而窗口背景色必须在创建时确定，否则 WebView 从创建到
// 绘制第一帧之间会露出一个与用户预期不符的底色（浅色主题用户看到黑底）。
// 读取一次 SQLite 是毫秒级操作，不影响启动速度。

package settings

import (
	"context"
)

// 说明：主题取值常量 ThemeLight / ThemeDark / ThemeSystem 定义在 schema.go，
// 与 KeyAppearanceTheme 的允许值同源，本文件直接复用，不重复声明。

// LoadThemePreference 在应用启动早期读取用户选择的外观主题。
//
// 返回值只会是 ThemeLight / ThemeDark / ThemeSystem 三者之一：
// 数据库打不开、读取失败、值不认识时一律返回 ThemeSystem（跟随系统），
// 这样窗口至少会与系统外观一致，不会出现明显的冲突。
//
// 该函数复用进程级共享连接（database.Default），不关闭连接池：提前 Close
// 会让随后初始化的设置服务拿到已关闭的池。
// 调用方应在窗口创建之前调用。
func LoadThemePreference(ctx context.Context) string {
	if ctx == nil {
		ctx = context.Background()
	}

	store, err := OpenSQLiteStore(ctx)
	if err != nil || store == nil {
		return ThemeSystem
	}

	stored, err := store.Load(ctx)
	if err != nil {
		return ThemeSystem
	}

	raw, ok := stored[KeyAppearanceTheme]
	if !ok {
		return ThemeSystem
	}

	theme, ok := raw.(string)
	if !ok {
		return ThemeSystem
	}

	switch theme {
	case ThemeLight:
		return ThemeLight
	case ThemeDark:
		return ThemeDark
	default:
		return ThemeSystem
	}
}

// ResolveThemeIsDark 把主题偏好解析为「是否使用深色底」。
//
// 参数 theme 取自 LoadThemePreference；systemLike 表示系统当前是否为深色。
// 参数 systemLike 由调用方通过平台层查询得到，本包不直接接触操作系统 API。
func ResolveThemeIsDark(theme string, systemLike bool) bool {
	switch theme {
	case ThemeLight:
		return false
	case ThemeDark:
		return true
	default:
		return systemLike
	}
}
