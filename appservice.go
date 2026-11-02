package main

import (
	"log"
	"runtime"

	"example.com/ninevoxel/launcher/internal/events"
	"example.com/ninevoxel/launcher/internal/platform"
	"github.com/wailsapp/wails/v3/pkg/application"
)

// appVersion 是启动器版本号。
//
// 该值必须与 launcher/build/config.yml 的 `info.version` 保持一致：
// 前者用于运行期展示与事件载荷，后者用于安装包与二进制元数据。
// 二者是同一版本号的两个出现位置，升版本时须同时修改。本卡不能改 build/**，
// 因此在此注释中显式记录该耦合关系，避免后续只改一处。
const appVersion = "0.1.0"

// AppInfo 表示启动器的非敏感应用信息，供首页与诊断区域展示。
//
// 字段与接口冻结清单 §8.4 逐字一致，JSON 使用 lowerCamelCase；
// 同时与前端 `launcher/frontend/src/types/app.ts` 的 `AppInfo` 逐字段对应。
// 该结构替代 P0 的 `RuntimeInfo`（其三个字段已并入本结构）。
type AppInfo struct {
	// AppVersion 为启动器版本号。
	AppVersion string `json:"appVersion"`

	// GoVersion 为构建应用所用的 Go 运行时版本。
	GoVersion string `json:"goVersion"`

	// GOOS 为构建目标操作系统。
	GOOS string `json:"goos"`

	// GOARCH 为构建目标处理器架构。
	GOARCH string `json:"goarch"`

	// DataDirectoryStatus 为数据目录可用状态，取值见 events 包中的
	// DataDirectoryStatus* 常量（ok / read-only / unavailable）。
	// 该字段是状态摘要，**不含任何绝对路径**。
	DataDirectoryStatus string `json:"dataDirectoryStatus"`
}

// AppService 提供启动器基础桌面服务。
//
// 对外只暴露一个绑定方法 Snapshot：Wails v3 会把服务的每个导出方法映射成
// 前端可调用接口，因此其余「内部动作」必须保持未导出或使用 Wails 预留的服务接口名
// （ServiceName / ServiceStartup / ServiceShutdown / ServeHTTP 四个名字会被框架排除）。
type AppService struct {
	// emitter 为事件发射器，负责把正式协议事件推送到前端。
	// 该字段在构造时初始化，应用实例随后由 bindApp 绑定到发射器内部。
	emitter *events.Emitter

	// dataDirectoryStatus 为启动阶段解析出的数据目录状态摘要。
	// 在构造时计算一次：目录可用性在整个进程生命周期内不会变化。
	dataDirectoryStatus string
}

// newAppService 创建 AppService 实例。
//
// 构造函数独立于 Wails 的应用创建流程：应用实例在 application.New 之后才存在，
// 因此服务先构造、后通过 bindApp 绑定发射器。这样装配顺序（服务 → 应用 → 绑定）
// 才能在 internal/app 的装配点保持清晰，也便于后续任务卡追加服务。
// 数据目录状态在构造时计算一次，避免每次 Snapshot 都做磁盘探测。
func newAppService() *AppService {
	return &AppService{
		// 独立发射器：不与日志服务共享，避免两个模块的关联 ID 语义混在一起。
		emitter:             events.NewEmitter(),
		dataDirectoryStatus: resolveDataDirectoryStatus(),
	}
}

// bindApp 记录 Wails 应用实例，使服务能够向前端发送事件。
//
// 参数 wailsApp 为 application.New 返回的应用实例。
// 传入 nil 时不做任何处理：发射器保持未绑定状态，对外方法仍可安全调用。
func (s *AppService) bindApp(wailsApp *application.App) {
	if wailsApp == nil {
		return
	}
	s.emitter.BindApp(wailsApp)
}

// 说明：窗口显示策略见 internal/app 的 WindowOptions。
// 2026-11-01 曾实现过「窗口 hidden 创建 + 前端回调 RevealWindow 显示 + 兜底定时器」，
// 实测兜底定时器总是先于前端回调触发（前端要等设置从数据库读回才能确定主题），
// 用户看到的是「窗口迟迟不出现」，因此回退为「窗口立即显示 + 背景色跟随系统外观」。
// 本服务不再持有窗口引用。

// ServiceName 返回服务名，便于 Wails 日志中识别本服务。
//
// 该方法名属于 Wails 预留的服务接口名（ServiceName / ServiceStartup /
// ServiceShutdown / ServeHTTP），不会出现在前端绑定中。
func (s *AppService) ServiceName() string {
	return "AppService"
}

// 说明：就绪事件不在 ServiceStartup 中发射，而是由 internal/app 的启动钩子
// （RegisterStartupEvents，挂在 events.Common.ApplicationStarted 上）调用
// emitAppReady 触发。原因有两点：
//  1. ServiceStartup 相对较早，前端 WebView 可能尚未完成订阅；
//  2. 应用级启动事件的时序更贴近「应用就绪」，语义更准确。
// 两条路径若同时存在会导致同一事件被发射两次，因此这里刻意只保留一条。

// Snapshot 返回当前应用信息快照，供前端在事件丢失后恢复状态。
//
// 规范假设事件可能丢失或重复，因此界面状态**不能**只依赖事件：
// 前端在任何需要确定状态的时候都应调用本方法重新取值。
// 返回值为纯内存读取，不做磁盘探测，因此可以安全地频繁调用。
func (s *AppService) Snapshot() (AppInfo, error) {
	return AppInfo{
		AppVersion:          appVersion,
		GoVersion:           runtime.Version(),
		GOOS:                runtime.GOOS,
		GOARCH:              runtime.GOARCH,
		DataDirectoryStatus: s.dataDirectoryStatus,
	}, nil
}

// resolveDataDirectoryStatus 把数据目录的可写性映射为可枚举的状态摘要。
//
// 取值规则（与 events 包中的常量一一对应）：
//   - 日志/数据目录解析为空 → unavailable（两处候选目录都不可写）；
//   - 目录可写 → ok；
//   - 目录存在但写入探测失败 → read-only。
//
// 目录**路径本身不进入返回值**：前端只需要知道状态
// （《错误码与日志规范》禁止把未脱敏绝对路径带到前端或事件载荷中）。
func resolveDataDirectoryStatus() string {
	dir := platform.LogDirectory()
	if dir == "" {
		return events.DataDirectoryStatusUnavailable
	}
	if platform.ProbeWritable(dir) {
		return events.DataDirectoryStatusOK
	}
	return events.DataDirectoryStatusReadOnly
}

// emitAppReady 发射一次 `app.lifecycle.ready.v1` 事件。
//
// 触发时机：应用启动完成、服务就绪时调用一次（由 ServiceStartup 驱动）。
// 载荷只含非敏感摘要（版本号与数据目录状态），不含任何路径。
//
// 发射失败不返回给业务：就绪事件是「锦上添花」，前端随时可以用 Snapshot 取到同样的信息，
// 因此这里把错误记录到标准日志而不是中断启动流程。
func (s *AppService) emitAppReady() {
	if s == nil || s.emitter == nil {
		return
	}

	envelope, err := events.Emit(s.emitter, events.EventAppLifecycleReady, events.AppLifecycleReadyPayload{
		AppVersion:          appVersion,
		DataDirectoryStatus: s.dataDirectoryStatus,
	})
	if err != nil {
		// 就绪事件发射失败不影响可用性：前端通过 Snapshot 取同一份信息。
		logEventEmissionFailure(events.EventAppLifecycleReady, envelope.EventID, err)
	}
}

// logEventEmissionFailure 记录一次事件发射失败。
//
// 使用标准库 log 而不是 internal/logging：本卡不允许修改日志服务，
// 且该路径只在异常时触发，标准日志已足够；结构化日志接入由后续任务卡统一处理。
func logEventEmissionFailure(eventName string, eventID string, err error) {
	log.Printf("事件发射失败：name=%q eventId=%q err=%v", eventName, eventID, err)
}
