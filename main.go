// Package main 负责 NineVoxel Launcher 桌面应用的启动。
//
// 本文件只保留启动流程的骨架：启动日志 → 装配 → 创建主窗口 → app.Run()。
// 窗口选项、应用级装配、路径解析分别位于 internal/app 与 internal/platform。
package main

import (
	"context"
	"embed"
	"io"
	"log"
	"os"
	"path/filepath"

	"example.com/ninevoxel/launcher/internal/app"
	"example.com/ninevoxel/launcher/internal/database"
	"example.com/ninevoxel/launcher/internal/events"
	"example.com/ninevoxel/launcher/internal/launch"
	"example.com/ninevoxel/launcher/internal/logging"
	"example.com/ninevoxel/launcher/internal/platform"
	"example.com/ninevoxel/launcher/internal/settings"
	"github.com/wailsapp/wails/v3/pkg/application"
)

//go:embed all:frontend/dist
var assets embed.FS

// initStartupLogging 把标准日志同时输出到控制台与启动日志文件。
//
// Windows 生产构建使用 -H windowsgui 链接参数，进程没有可用的标准输出，
// 如果启动阶段发生错误，用户只会看到进程静默退出而没有任何提示。
// 因此这里把标准日志额外写入日志目录，便于定位启动失败原因。
// 日志目录的优先级由 platform.LogDirectory 统一维护，本函数不再自行解析路径。
// 该函数不返回错误：日志初始化失败不应阻止启动器启动。
func initStartupLogging() {
	logDir := platform.LogDirectory()
	if logDir == "" {
		return
	}

	logFile, err := os.OpenFile(
		filepath.Join(logDir, "launcher.log"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND,
		0o644,
	)
	if err != nil {
		return
	}

	// 顺序必须是「文件在前、stderr 在后」：io.MultiWriter 按顺序写入，
	// 一旦某个 writer 返回错误就停止写入后面的 writer。
	// 本程序用 -H windowsgui 链接、没有控制台，此时 os.Stderr 是无效句柄，
	// 写入必定失败；如果把 stderr 放在前面，日志将永远写不进文件
	// （表现为 launcher.log 一直是 0 字节，2026-11-01 实测确认）。
	log.SetOutput(io.MultiWriter(logFile, os.Stderr))
}

// newApplication 构造 Wails 应用实例并完成服务装配。
//
// 该函数是 package main 内唯一的装配入口，承接三件事：
//  1. 构造 P1 的桌面服务实例；
//  2. 通过 app.Options 生成应用级启动选项（应用名、资源、平台参数、服务注册）；
//  3. 把应用实例回写给服务，供服务向前端发送事件。
//
// 后续任务卡（P1-04 设置、P1-05 日志、P1-06 事件）在这里追加服务注册：
// 在 services 切片中追加一条 application.NewService(新服务实例) 即可。
// 注意：application.NewService 的调用点必须留在 package main，
// 因为 Wails v3 的绑定生成器按调用点静态分析并生成前端 bindings，
// 放到 internal/app 会导致该方法无法被生成到前端。
//
// 参数 initialTheme 为启动早期从设置库读到的外观偏好，用于窗口背景色。
//
// 返回值只含应用实例：AppService 在函数内部完成装配与就绪钩子注册后即可丢弃，
// 它不需要在 main 中继续持有（窗口显示策略已不依赖该服务，见 internal/app 的
// WindowOptions 注释）。
func newApplication(initialTheme string) *application.App {
	appService := newAppService()

	services := []application.Service{
		application.NewService(appService),
		application.NewService(newLogService(logging.Default)),
	}

	wailsApp := application.New(app.Options(app.Bootstrap{
		Services: services,
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		},
		// WebView2 用户数据目录由 platform 包统一解析，优先级受 ADR-011 约束。
		WebviewUserDataPath: platform.WebviewDataPath(),
		// 启动早期读取一次用户主题偏好：主窗口此刻就要创建，设置服务尚未启动，
		// 因此由 main 直接读库后传入。耗时毫秒级，换来的是窗口第一帧就是正确底色。
		InitialTheme: initialTheme,
	}))

	// 应用实例创建后才能回写，服务借此向前端发送事件。
	appService.bindApp(wailsApp)

	// 注册启动完成钩子：应用就绪时发射一次 `app.lifecycle.ready.v1`（P1-06）。
	// 传入未导出方法 emitAppReady：Wails 会把服务的导出方法映射为前端绑定，
	// 就绪事件只应在进程内发射一次，不能作为前端可调用的接口暴露。
	// 事件可能丢失，前端同时可用 AppService.Snapshot 取同一份信息恢复状态。
	app.RegisterStartupEvents(wailsApp, appService.emitAppReady)

	return wailsApp
}

// main 初始化 NineVoxel Launcher 桌面应用并启动主窗口。
func main() {
	initStartupLogging()

	// 目录策略迁移：Windows 从 %AppData% 迁到 %LocalAppData%，Linux 从 ~/.config
	// 迁到 XDG_DATA_HOME。必须在读取主题/设置之前完成，否则会新建空数据库。
	if err := platform.MigrateLegacyAppData(); err != nil {
		log.Fatalf("迁移应用数据目录失败：%v", err)
	}

	// 数据库连接治理：在装配任何服务之前打开进程级共享池并完成迁移。
	// 各服务不再各自打开 app.db；失败时只记录日志，由各服务按既有策略降级，
	// 不阻塞窗口（设置/账号/实例会如实提示不可用）。
	// 这一步同时触发旧布局搬迁（<数据根>/app.db → <数据根>/db/app.db）。
	if _, dbErr := database.Default(context.Background()); dbErr != nil {
		log.Printf("数据库不可用，相关功能将降级：%v", dbErr)
	}

	// 注册正式事件协议的载荷类型，供绑定生成器生成前端类型定义（P1-06 迁移）。
	// 事件名已从 P0 的 `app:runtime` 迁移为 `app.lifecycle.ready.v1`（domain.resource.action.v1 规范）；
	// application.RegisterEvent 必须在 application.New 之前调用，因此注册点保留在本函数。
	application.RegisterEvent[events.AppEvent[events.AppLifecycleReadyPayload]](events.EventAppLifecycleReady)
	application.RegisterEvent[events.AppEvent[events.SettingsChangedPayload]](events.EventSettingsChanged)
	// P2-06：注册启动生命周期的两条事件载荷类型（必须在 application.New 之前）。
	// 注意：这两条事件由 `internal/launch` 发射，其载荷类型也定义在 launch 包内
	// （`LaunchStateChangedPayload` 在 supervisor.go、`LaunchLogAppendedPayload` 在 events 包）。
	application.RegisterEvent[events.AppEvent[launch.LaunchStateChangedPayload]](launch.EventLaunchStateChanged)
	application.RegisterEvent[events.AppEvent[events.LaunchLogAppendedPayload]](events.EventLaunchLogAppended)
	// 账号 SPI 首期：注册账号变更事件载荷类型（账号子系统由 internal/account 发射）。
	application.RegisterEvent[events.AppEvent[events.AccountChangedPayload]](events.EventAccountChanged)

	// 启动早期读一次主题偏好：设置服务尚未启动，但窗口背景色必须当场确定。
	initialTheme := settings.LoadThemePreference(context.Background())

	wailsApp := newApplication(initialTheme)

	// 创建主窗口并注册关闭行为钩子（托盘策略位预留，未实现托盘）。
	// 窗口立即显示；底色按用户设置的主题选择，避免浅色主题下先闪深色底。
	app.NewMainWindow(wailsApp, initialTheme)

	if err := wailsApp.Run(); err != nil {
		// 启动失败必须留下可查记录并以非零状态退出；
		// 错误码 APP_START_001 已在接口冻结清单第 8.3 节冻结，日志接入见 P1-05。
		log.Fatalf("启动 NineVoxel Launcher 失败：%v", err)
	}
}
