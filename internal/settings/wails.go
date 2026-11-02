// 本文件实现设置的 Wails 服务包装。
//
// 为什么包装类型放在 internal/settings 而不是 launcher/settingsservice.go：
// 本卡禁止修改 main.go，而注册新服务的唯一可行入口是 internal/app/bootstrap.go
// （卡面允许在它里面追加注册）。internal/app 无法引用 package main 中的类型，
// 因此服务类型必须定义在 internal/settings 内才能被 bootstrap 引用。
//
// 绑定发现性：wails3 的绑定生成器扫描**所有被加载的包**中
// `application.NewService` 的调用点，不限于 package main
// （见 wails v3.0.0 internal/generator/analyse.go 的 FindServices），
// 因此这里的调用点同样会生成前端绑定。
package settings

import (
	"context"
	"fmt"
	"sync"

	"example.com/ninevoxel/launcher/internal/events"
	"github.com/wailsapp/wails/v3/pkg/application"
)

// 服务相关的包级状态。
//
// 之所以需要包级位置：服务在 Options() 阶段构造（此时应用实例还不存在），
// 而发射器必须等应用实例就绪后才能绑定。两者之间通过这两个变量衔接，
// 避免为了传递一个指针而改动 main.go 的装配顺序。
var (
	// sharedService 为进程内唯一的设置服务实例。
	sharedService *Service

	// sharedEmitter 为设置服务使用的发射器，待应用实例就绪后绑定。
	sharedEmitter = events.NewEmitter()

	// sharedOnce 保证服务只初始化一次（Options 可能因测试被多次调用）。
	sharedOnce sync.Once
)

// WailsService 是设置子系统的 Wails 服务包装。
//
// 对外只暴露两个方法（接口冻结清单 §8.5）：Snapshot 与 Update。
// Wails 会把服务的每个导出方法映射为前端绑定，因此其余内部动作必须保持未导出。
type WailsService struct{}

// NewWailsService 创建并注册设置服务。
//
// 返回值可直接加入 application.Options.Services。
// 服务本体在此处初始化：打开数据库、执行迁移；失败时降级为内存存储，
// 并把稳定错误码写入快照，由界面如实提示（设置不可用不应阻止应用启动）。
func NewWailsService() application.Service {
	sharedOnce.Do(func() {
		// 使用 Background 上下文：服务生命周期与进程相同，
		// 迁移与首次读取都在启动阶段完成，不依赖任何请求上下文。
		sharedService = NewService(context.Background(), sharedEmitter)
	})

	return application.NewService(&WailsService{})
}

// BindEmitterToApp 把设置服务的发射器绑定到应用实例。
//
// 必须在 application.New 之后调用（由 internal/app 的启动钩子驱动），
// 否则 `settings.changed.v1` 无法发射。
// 参数 wailsApp 为应用实例；nil 时不做任何处理。
func BindEmitterToApp(wailsApp *application.App) {
	if wailsApp == nil {
		return
	}
	sharedEmitter.BindApp(wailsApp)
}

// Svc 返回进程内的设置服务实例。
//
// 供装配层（例如窗口恢复、关闭钩子）直接使用；前端不可见（未导出到绑定）。
// 尚未初始化时返回 nil。
func Svc() *Service {
	return sharedService
}

// SnapshotDTO 是设置快照的对外结构。
//
// 字段与内部 SettingsSnapshot 逐字段一致，JSON 使用 lowerCamelCase，
// 与前端 `services/settings.ts` 的类型一一对应。
type SnapshotDTO struct {
	// Theme 对应 `appearance.theme`，取值 light / dark / system。
	Theme string `json:"theme"`

	// Language 对应 `appearance.language`，取值 zh-Hans / zh-Hant / en。
	Language string `json:"language"`

	// WindowWidth 对应 `window.width`（像素）。
	WindowWidth int `json:"windowWidth"`

	// WindowHeight 对应 `window.height`（像素）。
	WindowHeight int `json:"windowHeight"`

	// WindowMaximized 对应 `window.maximized`。
	WindowMaximized bool `json:"windowMaximized"`

	// UpdateChannel 对应 `update.channel`，只读占位。
	UpdateChannel string `json:"updateChannel"`

	// DownloadMirrorEnabled 对应 `download.mirrorEnabled`（D-57 / D-54）。
	//
	// 该字段是开关的真实回显来源：缺少它时界面只能写不能读，
	// 用户勾选保存后刷新会回到「关闭」，而下载其实已走镜像。
	DownloadMirrorEnabled bool `json:"downloadMirrorEnabled"`

	// LaunchJavaPath 对应 `launch.javaPath`：用户显式指定的 Java 可执行文件路径。
	//
	// 空串表示未指定，此时启动器只用自己下载安装的运行时，不探测本机 Java。
	// 该字段是设置页与 Java 管理中心回显「当前指定了哪个 Java」的唯一来源。
	LaunchJavaPath string `json:"launchJavaPath"`

	// LaunchMaxMemoryMb 对应 `launch.maxMemoryMb`：全局默认最大分配内存（MiB）。
	LaunchMaxMemoryMb int `json:"launchMaxMemoryMb"`

	// UpdatedAtMs 为最近一次写入时间（UTC Unix 毫秒）；从未写入时为 0。
	UpdatedAtMs int64 `json:"updatedAtMs"`

	// Enabled 表示是否处于持久化模式；false 时设置仅本次会话有效。
	Enabled bool `json:"enabled"`

	// StorageKind 为存储来源：sqlite / memory。
	StorageKind string `json:"storageKind"`

	// PathKind 为数据库目录来源类别：userConfig / exeDir / unavailable。
	// 该字段是类别而非路径，不含任何绝对路径。
	PathKind string `json:"pathKind"`

	// InitErrorCode 为初始化失败的稳定错误码；成功时为空。
	InitErrorCode string `json:"initErrorCode"`
}

// UpdateDTO 是批量更新请求的对外结构。
type UpdateDTO struct {
	// Changes 为 key → 目标值；未出现的 key 保持不变。
	Changes map[string]any `json:"changes"`
}

// Snapshot 返回当前设置的完整快照。
//
// 该方法是前端初始化与「事件丢失后恢复」的权威数据源
// （《事件与状态机规范》：事件可能丢失或重复，状态必须能用快照恢复）。
// 服务不可用时返回全默认值而不是错误：设置页应始终能渲染。
func (w *WailsService) Snapshot() (SnapshotDTO, error) {
	if sharedService == nil {
		return toDTO(SnapshotOrDefault()), nil
	}

	snapshot, err := sharedService.Snapshot()
	if err != nil {
		return toDTO(SnapshotOrDefault()), nil
	}
	return toDTO(snapshot), nil
}

// Update 按 key 批量更新设置项，返回更新后的快照。
//
// 校验在 Go 边界执行（白名单 + 类型 + 范围），任一不合法则整批不落盘
// 并返回 APP_CONFIG_002；落盘成功后由服务层发射 settings.changed.v1。
func (w *WailsService) Update(req UpdateDTO) (SnapshotDTO, error) {
	if sharedService == nil {
		return SnapshotDTO{}, fmt.Errorf("APP_UNKNOWN_001: 设置服务未初始化")
	}

	snapshot, err := sharedService.Update(UpdateRequest{Changes: req.Changes})
	if err != nil {
		// 内部错误已带稳定错误码（APP_CONFIG_002 / DB_OPEN_001 等），原样上抛。
		return SnapshotDTO{}, err
	}
	return toDTO(snapshot), nil
}

// toWailsSnapshot 把内部快照转换为对外结构。
//
// 纯字段映射，不做加工：内部结构已保证类型与取值合法。
func toDTO(source SettingsSnapshot) SnapshotDTO {
	return SnapshotDTO{
		Theme:                 source.Theme,
		Language:              source.Language,
		WindowWidth:           source.WindowWidth,
		WindowHeight:          source.WindowHeight,
		WindowMaximized:       source.WindowMaximized,
		UpdateChannel:         source.UpdateChannel,
		DownloadMirrorEnabled: source.DownloadMirrorEnabled,
		LaunchJavaPath:        source.LaunchJavaPath,
		LaunchMaxMemoryMb:     source.LaunchMaxMemoryMb,
		UpdatedAtMs:           source.UpdatedAtMs,
		Enabled:               source.Enabled,
		StorageKind:           source.StorageKind,
		PathKind:              source.PathKind,
		InitErrorCode:         source.InitErrorCode,
	}
}

// BoolSetting 读取进程内设置快照里的布尔项，供装配层注入为 Provider。
//
// 这是「设置值 → 其它模块」的唯一导出读取口（D-57）：下载镜像开关等业务开关
// 通过它被 provider 读取，从而避免业务模块 import settings、也避免第二套
// 「自己开数据库读同一个 key」的实现。
//
// 参数 key 为设置项标识；fallback 为服务未初始化、key 缺失或类型不符时的回退值。
func BoolSetting(key string, fallback bool) bool {
	if service := Svc(); service != nil {
		return service.BoolSetting(key, fallback)
	}
	return fallback
}

// StringSetting 读取进程内设置快照里的字符串项，供装配层注入为 Provider。
//
// 与 BoolSetting 同一用途与同一约束：业务模块不 import settings，
// 也不自己开数据库读 key。参数 fallback 为服务未初始化、key 缺失或
// 类型不符时的回退值。
func StringSetting(key, fallback string) string {
	if service := Svc(); service != nil {
		return service.StringSetting(key, fallback)
	}
	return fallback
}
