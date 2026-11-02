// 本文件是 package app 的装配集中点：把应用启动选项与主窗口创建收拢到一处，
// 使 main() 只保留「启动日志 → 装配 → 创建窗口 → app.Run()」四步。
//
// 为什么服务列表由调用方传入：
// Wails v3 的绑定生成器按调用点静态分析 application.NewService，服务构造函数
// 必须留在 package main（launcher/ 根目录）才能被生成器发现；同时 internal/app
// 无法反向引用 package main 中的 AppService 类型。因此本包接收已经构造好的
// application.Service 列表，而不在这里 import 主包。
package app

import (
	"example.com/ninevoxel/launcher/internal/artifact"
	"example.com/ninevoxel/launcher/internal/download"
	"example.com/ninevoxel/launcher/internal/launch"
	"example.com/ninevoxel/launcher/internal/runtime"
	"example.com/ninevoxel/launcher/internal/settings"
	"example.com/ninevoxel/launcher/internal/version"
	"github.com/wailsapp/wails/v3/pkg/application"
	wailsEvents "github.com/wailsapp/wails/v3/pkg/events"
)

// RegisterLaunchService 把启动服务追加到装配输入并返回扩充后的输入。
//
// 这是 P2-01 在共享装配文件中的**唯一**改动点：卡面授权在 bootstrap.go
// 追加一次 RegisterLaunchService，不得改动已有注册顺序与已有字段。
//
// 参数 in 为 package main 构造的装配输入；返回值为追加启动服务后的副本。
func RegisterLaunchService(in Bootstrap) Bootstrap {
	// 追加而不是替换：AppService、LogService、SettingsService 的注册顺序保持不变
	// （Wails 按注册顺序调用 ServiceStartup，顺序变化会影响启动语义）。
	in.Services = append(in.Services, launch.NewWailsService())
	return in
}

// RegisterSettingsService 把设置服务追加到装配输入并返回扩充后的输入。
//
// 这是 P1-04 在共享装配文件中的**唯一**改动点：卡面明确要求在 bootstrap.go
// 追加一行 SettingsService 注册。由于本卡禁止修改 main.go，而服务列表由
// main.go 构造后传入，这里采用「在装配输入上追加」而不是「改 main.go」的做法：
// main() 传给 Options 的 Bootstrap 会先在 Options 内部被补齐设置服务。
//
// 参数 in 为 package main 构造的装配输入；返回值为追加设置服务后的副本。
func RegisterSettingsService(in Bootstrap) Bootstrap {
	// 追加而不是替换：保证 AppService、LogService 的注册顺序不变
	// （Wails 按注册顺序调用 ServiceStartup，顺序变化会影响启动语义）。
	in.Services = append(in.Services, settings.NewWailsService())
	return in
}

// RegisterVersionService 把版本清单服务追加到装配输入并返回扩充后的输入。
//
// 这是 P3-01 在共享装配文件中的**唯一**改动点：卡面（决策记录 D-40）要求
// `internal/version/wails.go` 归 P3-01 独占，服务注册由装配层在这里追加。
//
// 为什么数据根也要在这里注入：
// P3-01 与 P3-03 是并行开发的两张卡，`internal/version` 在编译期**不依赖**
// `internal/artifact`（否则两张卡会互相等编译）。`internal/artifact` 才是数据根
// 的唯一权威（便携版 `data` 优先、回退用户数据目录，见其 `DataRoot`），所以
// 由装配层把两者接上，避免在 version 包里复制一份目录优先级逻辑。
//
// 参数 in 为 package main 构造的装配输入；返回值为追加版本服务后的副本。
func RegisterVersionService(in Bootstrap) Bootstrap {
	// 只在装配阶段注入一次数据根解析：SetDataRootProvider 是包级状态，
	// 重复注入会覆盖前一次，而 Options 在进程生命周期内只调用一次。
	version.SetDataRootProvider(func() (string, error) {
		root, err := artifact.DataRoot()
		// 必须显式判空后返回 nil：artifact.DataRoot 返回的是 *artifact.Error，
		// 直接 `return "", err` 会把「typed nil」装进 error 接口变成非 nil，
		// 让调用方误以为数据根解析失败。
		if err != nil {
			return "", err
		}
		return root, nil
	})

	// 追加而不是替换：AppService、LogService、SettingsService、LaunchService
	// 的注册顺序保持不变（Wails 按注册顺序调用 ServiceStartup）。
	in.Services = append(in.Services, version.NewWailsService())
	return in
}

// RegisterDownloadService 把下载队列服务追加到装配输入并返回扩充后的输入。
//
// 这是 P3-04 在共享装配文件中的**唯一**改动点：卡面只授权追加一行注册，
// 因此下载模块的数据根解析与事件发射都在包内自行惰性完成（见
// internal/download/store.go 与 wails.go），不在这里再接线。
//
// 参数 in 为 package main 构造的装配输入；返回值为追加下载服务后的副本。
func RegisterDownloadService(in Bootstrap) Bootstrap {
	// P3-15（D-57）：把设置服务里的镜像开关注入下载模块。
	//
	// 为什么用 Provider 而不是让 internal/download import internal/settings：
	// 下载是能力层，依赖设置服务会把「业务配置」的依赖方向倒置。这里注入的
	// 是一个闭包，读的是设置服务的内存快照（不做磁盘 IO），因此下载在每次
	// 选择候选地址前调用它都能拿到用户最新的开关值。
	//
	// 注入必须在注册服务之前完成：runtime 包（P3-14）的镜像判断也读同一个
	// Provider，两处共用一份读法，不留第二套「自己开数据库读 key」的实现。
	download.SetMirrorEnabledProvider(func() bool {
		return settings.BoolSetting(settings.KeyDownloadMirrorEnabled, false)
	})

	// 追加而不是替换：AppService、LogService、SettingsService、LaunchService、
	// VersionService 的注册顺序保持不变（Wails 按注册顺序调用 ServiceStartup）。
	in.Services = append(in.Services, download.NewWailsService())
	return in
}

// RegisterRuntimeService 把运行时探测服务追加到装配输入并返回扩充后的输入。
//
// 这是 P3-08 在共享装配文件中的**唯一**改动点：卡面只授权追加一行注册。
// 探测与需求映射都是只读能力（探测缓存留在进程内存、需求直接读版本元数据），
// 因此不需要像版本服务那样在这里注入数据根 Provider。
//
// 参数 in 为 package main 构造的装配输入；返回值为追加运行时服务后的副本。
func RegisterRuntimeService(in Bootstrap) Bootstrap {
	// 追加而不是替换：AppService、LogService、SettingsService、LaunchService、
	// VersionService、DownloadService 的注册顺序保持不变
	// （Wails 按注册顺序调用 ServiceStartup）。
	in.Services = append(in.Services, runtime.NewWailsService())
	return in
}

// Options 把装配输入转换为 Wails 应用启动选项。
//
// 装配职责集中在本函数：
//   - 应用名与描述；
//   - 服务注册（绑定到前端的服务列表，含 P1-04 追加的设置服务）；
//   - 内嵌前端资源；
//   - Windows 平台参数（WebView2 用户数据目录）；
//   - macOS 平台参数（关闭最后一个窗口即退出进程）。
//
// 后续任务卡（P1-05 日志、P1-06 事件）的服务注册一律在这里接线：
// 把新增服务加入 Bootstrap.Services，并在 main() 的 newApplication 中追加
// 对应的 application.NewService 调用。该调用必须留在 package main（launcher/ 根目录），
// 因为 Wails v3 的绑定生成器按调用点静态分析并生成前端 bindings；本包只接收
// 已构造好的服务列表，不自行创建服务实例，否则绑定的方法不会出现在前端。
//
// 参数 in 为已构造好的装配输入；返回值为可直接传给 application.New 的选项。
func Options(in Bootstrap) application.Options {
	// P1-04：在既有服务之后追加设置服务，不改变已有注册顺序。
	in = RegisterSettingsService(in)
	// P2-01：在设置服务之后追加启动服务，同样不改变已有顺序。
	in = RegisterLaunchService(in)
	// P3-01：在启动服务之后追加版本清单服务，并注入数据根解析。
	in = RegisterVersionService(in)
	// P3-04：在版本服务之后追加下载队列服务（只追加注册，不改变已有顺序）。
	in = RegisterDownloadService(in)
	// P3-08：在下载服务之后追加运行时探测服务（只追加注册，不改变已有顺序）。
	in = RegisterRuntimeService(in)

	return application.Options{
		// 应用名与描述沿用 P0 文案，不随阶段变化。
		Name:        appName,
		Description: appDescription,
		// 服务列表：由调用方构造，本包只负责注册，不创建服务实例。
		Services: in.Services,
		// 前端资源：以 embed.FS 形式交给 Wails 的资源服务。
		Assets: in.Assets,
		// Windows 平台：显式指定 WebView2 用户数据目录，避免默认目录
		// 在受限环境下创建失败导致进程静默退出（见 ADR-011）。
		Windows: application.WindowsOptions{
			WebviewUserDataPath: in.WebviewUserDataPath,
		},
		// macOS 平台：关闭最后一个窗口即退出进程，与 P0 已验收行为一致。
		// 该字段是 v3.0.0 的真实字段（pkg/application/application_options.go）。
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
	}
}

// RegisterStartupEvents 把启动阶段的事件接线挂到应用实例上。
//
// 本函数只做「注册动作」，不创建任何服务或发射器实例：
// 服务的发射器由 package main 构造并随服务注册，本函数只负责把它的生命周期
// 钩子接到 Wails 的应用事件上，因此不会与 internal/app 的既有装配结构冲突。
//
// 当前注册内容：
//   - 应用启动完成（events.Common.ApplicationStarted）时，调用 AppService 的就绪钩子，
//     触发一次 `app.lifecycle.ready.v1`。
//
// 为什么不在本包内发射：事件的载荷类型与业务含义归 package main 的 AppService 所有，
// internal/app 无法引用 package main 的类型（反向 import 会构成循环）。
// 因此这里接收一个无参回调，把「何时发射」与「发射什么」解耦。
//
// 参数 wailsApp 为 application.New 返回的应用实例（nil 时直接返回，不注册）。
// 参数 onReady 为应用就绪时的回调；nil 时同样跳过注册，避免空指针调用。
func RegisterStartupEvents(wailsApp *application.App, onReady func()) {
	if wailsApp == nil {
		return
	}

	// P2-01：先绑定 launch 服务的应用实例，供 `PickJavaExecutable` 使用对话框。
	// 该绑定必须在应用创建之后完成；放在本函数内可以复用既有的启动时机，
	// 无需改动 main.go 的装配顺序。
	launch.BindAppToLaunchService(wailsApp)

	// P2-06：把事件发射器绑定给启动管理器，使状态变更能推送到前端。
	// 使用 launch 包自己的发射器：生命周期的关联 ID 语义与日志/设置各自独立。
	launch.BindSupervisorEmitter(launch.NewSupervisorEmitter(wailsApp))

	// P2-06：注册应用退出钩子，确保关闭应用时不会留下孤儿游戏进程。
	//
	// 挂载点说明：`application.Options.OnShutdown` 在应用终止前被调用，
	// 且**会阻塞直到回调返回**（见 wails v3.0.0 `application_options.go`
	// 对 OnShutdown 的注释）。这正是需要的语义——必须在退出前把游戏进程
	// 停掉或强行终止，否则启动器先退出、游戏仍在跑，就成了孤儿进程。
	//
	// 为什么不用窗口关闭钩子（`window.go` 的 `AttachCloseBehaviour`）：
	// 关窗在部分场景不触发（系统关机、进程被强制结束），
	// 且该文件属 P1-01 授权范围，本卡不得修改。
	// OnShutdown 是应用级的最后一个可执行点，覆盖面更完整。
	wailsApp.OnShutdown(func() {
		launch.ShutdownActiveLaunch()
	})

	// 就绪回调为 nil 时只做绑定，不注册事件监听（避免无意义的空回调）。
	if onReady == nil {
		return
	}

	// 挂到应用级启动事件：此时 WebView 已就绪，前端订阅能收到事件。
	// 即便前端错过（规范允许事件丢失），它仍可用 AppService.Snapshot 恢复状态。
	wailsApp.Event.OnApplicationEvent(wailsEvents.Common.ApplicationStarted, func(*application.ApplicationEvent) {
		onReady()
	})
}

// 应用元信息常量：应用名同时用于 macOS 菜单栏与默认关于窗口。
const (
	// appName 为应用名称，必须在所有平台保持一致。
	appName = "NineVoxel Launcher"

	// appDescription 为应用描述，用于默认关于窗口与平台元数据。
	appDescription = "Minecraft Java Edition 桌面启动器工程基础"
)

// Bootstrap 汇总装配所需的输入。
//
// 该结构体只承载装配参数，不持有任何业务状态：
// 由 package main 的 newApplication 填充，随后交给 Options 转换。
type Bootstrap struct {
	// Services 为需要绑定到前端的全部 Wails 服务。
	// 当前阶段只含 AppService；后续任务卡在此字段所对应的构造位置追加服务。
	Services []application.Service

	// Assets 为内嵌的前端构建产物，由 package main 的 embed.FS 提供。
	Assets application.AssetOptions

	// WebviewUserDataPath 为 WebView2 用户数据目录。
	// 取值必须来自 platform.WebviewDataPath()，保证 ADR-011 的优先级不变。
	WebviewUserDataPath string

	// InitialTheme 为应用启动早期从设置库读到的外观偏好（light / dark / system）。
	//
	// 用途：主窗口在设置服务启动之前创建，窗口背景色必须当场确定，
	// 否则 WebView 从创建到绘制第一帧之间会露出与用户预期不符的底色。
	// 取值来自 settings.LoadThemePreference；为空时按「跟随系统」处理。
	InitialTheme string
}

// NewMainWindow 创建主窗口并注册关闭行为钩子。
//
// 窗口选项（标题、初始尺寸、最小尺寸、居中、背景色）由 WindowOptions 提供；
// 关闭行为在 AttachCloseBehaviour 中注册，当前保持「关窗即关闭窗口」，
// 并为后续托盘策略留出唯一的替换点。
//
// 窗口创建后立即可见，不做延迟显示：2026-11-01 曾用「hidden 创建 + 前端回调显示
// + 1.5 秒兜底」来消除浅色主题下的深色闪屏，实测兜底定时器总是先于前端回调触发
// （前端要等设置从数据库读回才能确定主题），用户看到的是「窗口迟迟不出现」。
// 现在改为把窗口背景色换成用户真正选择的主题色（见 WindowOptions 的 BackgroundColour），
// 首帧就不闪，同时启动速度不受影响。注意「用户选择」优先于「系统外观」：
// 用户设了浅色而系统是深色时，必须按用户的浅色取底色，否则仍会闪一下深色。
//
// 参数 wailsApp 为 application.New 返回的应用实例；参数 initialTheme 为启动早期
// 从设置库读到的外观偏好（light / dark / system）；返回值为主窗口指针，
// 调用方可用于后续（例如日志页或事件页）按需引用窗口实例。
func NewMainWindow(wailsApp *application.App, initialTheme string) *application.WebviewWindow {
	window := wailsApp.Window.NewWithOptions(WindowOptions(initialTheme))

	// 注册关闭钩子：本卡只做策略位预留，不改变现有关闭行为。
	AttachCloseBehaviour(window)

	return window
}
