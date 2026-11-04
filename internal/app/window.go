// Package app 负责 NineVoxel Launcher 桌面应用的窗口描述与服务装配。
//
// 本包只做两件事：描述主窗口选项（window.go）、组装 Wails 应用启动选项
// （bootstrap.go）。业务服务的实现不在本包内：具体服务由 launcher/ 根目录的
// package main 构造后传入，因为 Wails v3 的绑定生成器按调用点静态分析
// application.NewService，绑定范围必须留在调用方代码里。
package app

import (
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"

	"example.com/ninevoxel/launcher/internal/platform"
	"example.com/ninevoxel/launcher/internal/settings"
)

const (
	// windowTitle 是主窗口标题，也是 macOS 菜单栏显示的应用名。
	windowTitle = "NineVoxel Launcher"

	// initialWindowWidth 与 initialWindowHeight 是主窗口初始尺寸。
	// 2026-11-01 前端改用设计稿（React 19 + Tailwind 4，`gemini design new/`）后，
	// 尺寸按设计稿 1800×1120 的宽高比（约 1.607）等比缩到 1280×800；
	// 设计稿用 `lg:` / `xl:` 断点排布，窗口低于约 1024 宽会掉到单列布局。
	initialWindowWidth  = 1280
	initialWindowHeight = 800

	// minWindowWidth 与 minWindowHeight 是主窗口最小尺寸。
	// 取值依据见窗口选项函数注释：保证侧边导航与内容区的最小可用空间。
	minWindowWidth  = 960
	minWindowHeight = 640

	// darkBackgroundRed / Green / Blue 是深色外观下的窗口背景色（#0a0a0a）。
	// 与前端深色主题的画布色一致。
	darkBackgroundRed   = 10
	darkBackgroundGreen = 10
	darkBackgroundBlue  = 10

	// lightBackgroundRed / Green / Blue 是浅色外观下的窗口背景色（#f1f5f9）。
	// 与前端浅色主题的画布色（slate-100）一致。
	lightBackgroundRed   = 241
	lightBackgroundGreen = 245
	lightBackgroundBlue  = 249
)

// WindowOptions 构造主窗口的选项。
//
// 参数 initialTheme 是应用启动早期从设置库读到的外观偏好
// （light / dark / system，取自 settings.LoadThemePreference）。
// 窗口背景色据此决定：这决定了 WebView 从创建到绘制第一帧之间露出的底色，
// 也是浅色主题用户此前看到「黑底闪一下」的根源。
//
// 字段可用性以本机模块缓存中 github.com/wailsapp/wails/v3@v3.0.0 的
// application.WebviewWindowOptions 定义为准（pkg/application/webview_window_options.go）：
//   - Title / Width / Height / BackgroundColour / URL 均为真实字段；
//   - MinWidth / MinHeight 为真实字段，Windows 平台由 WM_GETMINMAXINFO 与
//     enableSizeConstraints 执行，macOS 平台映射到 NSWindow 的 contentMinSize；
//   - InitialPosition 为真实字段，配合常量 WindowCentered 可让窗口在首个
//     显示器上居中（Windows 走 CenterWindow，macOS 走屏幕可见区域计算），
//     因此不需要自行计算坐标，也不引入任何平台原生调用。
//
// 最小尺寸 960×640 的取值依据：
//   - 宽度 960 = 侧边导航固定宽 208（P1-02 计划值）+ 内容区最小可用宽 704
//     （容纳三列诊断卡，每列不低于 234）+ 左右内边距与分隔余量 48；
//   - 高度 640 = 顶栏 72 + 内容区最小可用高 480（由面板标题 86、连接行 52、
//     数值行 90 与上下留白构成，仍能完整看到一屏卡片）与上下内边距 88。
//
// 上述取值只在本卡内固定，窗口尺寸恢复由 P1-04 冻结设置服务后追加实现。
func WindowOptions(initialTheme string) application.WebviewWindowOptions {
	return application.WebviewWindowOptions{
		// 窗口标题固定为应用名，避免平台标题与产品名不一致。
		Title: windowTitle,
		// 初始尺寸沿用 P0 已验证值。
		Width:  initialWindowWidth,
		Height: initialWindowHeight,
		// 最小尺寸防止用户把窗口压缩到内容区不可用的程度。
		MinWidth:  minWindowWidth,
		MinHeight: minWindowHeight,
		// 首次启动居中显示；WindowCentered 是 v3.0.0 提供的真实取值。
		InitialPosition: application.WindowCentered,
		// 去掉系统标题栏，改由应用内顶栏承载品牌、当前实例与窗口控制按钮。
		//
		// 这是 2026-11-01 用户明确要求的形态（照设计稿 AppTopBar：品牌与版本号、当前实例、
		// 分配内存、Java 环境、下载、控制台、最小化/最大化/关闭全部在同一行）。
		// 保留系统标题栏会让同一行出现两次品牌与两层顶部结构，视觉上无法对齐设计稿。
		//
		// 代价（已评估并接受）：
		//   - 窗口拖动、双击最大化由前端 `--wails-draggable: drag` 区域承担；
		//   - 边缘缩放仍由 Wails 的 WS_THICKFRAME 处理，不需要自绘 8 向热区；
		//   - 关闭行为不变：仍由 AttachCloseBehaviour 的 WindowClosing 钩子决定。
		Frameless: true,
		// frameless 下保留 Windows 11 的圆角与投影；关闭它只会得到一个直角无影的窗口。
		Windows: application.WindowsWindow{
			DisableFramelessWindowDecorations: false,
		},
		// 背景色跟随系统外观，避免 WebView 首帧闪出刺眼底色。
		//
		// 背景色的可见时机：WebView 从创建到绘制第一帧之间，用户看到的就是它。
		//
		// 2026-11-01 曾尝试「以 hidden 方式创建窗口，等前端应用完主题后再显示」，
		// 实测兜底定时器（1500ms）总是先于前端回调触发——前端要等设置从数据库读回
		// 才能确定主题，IPC 往返加上 WebView 首帧耗时超过 1.5 秒，用户看到的是
		// 「窗口迟迟不出现」，比闪一下底色更糟。因此改回立即显示，并把底色本身
		// 修正为与系统外观一致：系统深色给深色底、系统浅色给浅色底，
		// 绝大多数用户在第一帧就不会看到颜色跳变；前端读回设置后再校正差异情形。
		BackgroundColour: backgroundColourForTheme(initialTheme),
		// 主窗口只加载应用自身资源（根路径），不加载任何插件或外部 HTML。
		URL: "/",
	}
}

// backgroundColourForTheme 按用户主题偏好返回窗口背景色。
//
// 参数 initialTheme 取自 settings.LoadThemePreference，取值 light / dark / system。
// 只有「跟随系统」时才去查平台外观；用户明确选了浅色或深色时以用户选择为准——
// 这正是之前出错的地方：用户设了浅色但系统是深色，若按系统外观取色，
// 窗口仍会先闪一块深色底。
func backgroundColourForTheme(initialTheme string) application.RGBA {
	if settings.ResolveThemeIsDark(initialTheme, platform.Current.Theme.PrefersDark()) {
		return application.NewRGB(darkBackgroundRed, darkBackgroundGreen, darkBackgroundBlue)
	}
	return application.NewRGB(lightBackgroundRed, lightBackgroundGreen, lightBackgroundBlue)
}

// AttachCloseBehaviour 为主窗口注册关闭行为钩子，并预留「关闭到托盘」的策略位。
//
// 当前行为（本卡不改变现状）：
//   - 关闭主窗口即关闭该窗口；
//   - macOS 侧由 application.MacOptions.ApplicationShouldTerminateAfterLastWindowClosed
//     为 true，最后一个窗口关闭后进程一并退出；
//   - Windows 侧不设置 WindowsOptions.DisableQuitOnLastWindowClosed，保持
//     Wails 默认的「最后一个窗口关闭即退出进程」。
//
// 预留方式：v3.0.0 的 WebviewWindow.RegisterHook 支持在
// events.Common.WindowClosing 阶段调用 WindowEvent.Cancel() 取消关闭。
// 后续实现系统托盘（P8 范围）时，只需把下面的钩子体改为「隐藏窗口 + 取消关闭」，
// 不需要改动本包之外任何调用点。
//
// 约束：本卡不实现托盘、通知或任何 Win32 / Cocoa 调用，钩子体保持空实现。
func AttachCloseBehaviour(window *application.WebviewWindow) {
	// 防御性判断：窗口创建失败时不应在此处引发空指针。
	if window == nil {
		return
	}

	// 注册关闭钩子：当前不取消事件，保持关窗即关闭窗口的既有行为。
	window.RegisterHook(events.Common.WindowClosing, func(*application.WindowEvent) {
		// 关闭到托盘的策略位：改为托盘行为时在此调用 e.Cancel() 并隐藏窗口。
		// 未实现托盘前，这里必须保持空实现，否则会改变用户已经验收的关闭行为。
	})
}
