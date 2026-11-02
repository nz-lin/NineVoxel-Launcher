// 本文件实现运行时模块的 Wails 服务包装。
//
// 为什么服务类型定义在 internal/runtime：注册入口只有
// `internal/app/bootstrap.go`（D-23 / C-13 允许追加服务注册），而 internal/app
// 无法引用 package main 的类型，因此服务类型必须在本包内。
//
// 绑定发现性：wails3 的绑定生成器扫描所有被加载包中 `application.NewService`
// 的调用点，因此这里的调用点会生成前端绑定，路径为
// `bindings/example.com/ninevoxel/launcher/internal/runtime`。
//
// 本卡不新增事件（P3 事件只属于下载任务），因此没有 BindEmitterToApp。
package runtime

import (
	"example.com/ninevoxel/launcher/internal/download"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// RuntimeService 是 Java 运行时子系统的 Wails 服务包装。
//
// 对外只暴露接口冻结清单 §1.4 中属于 P3-08 的三个只读方法；
// `InstallRuntime` 与 `SelectRuntime` 属 P3-09，本卡不实现，也不会被绑定生成。
type RuntimeService struct{}

// NewWailsService 创建运行时服务，返回值可直接加入 application.Options.Services。
//
// 服务无状态：探测结果保存在包级缓存里（5 分钟），由前端按需查询。
func NewWailsService() application.Service {
	return application.NewService(&RuntimeService{})
}

// DetectRuntimes 探测本机 Java 并返回脱敏摘要列表。
//
// 参数 req.Refresh 为 true 时绕过 5 分钟缓存重新探测。
// 探测失败不会让整个调用失败：失败候选以 warnings 返回，界面应折叠展示。
// 没有找到任何 Java 时返回空列表 + `RUNTIME_NOT_FOUND_001` 告警。
func (s *RuntimeService) DetectRuntimes(req DetectRuntimeRequest) (RuntimeProbeResult, error) {
	result, appErr := DetectRuntimes(req)
	if appErr != nil {
		return RuntimeProbeResult{}, appErr
	}
	return result, nil
}

// ListRuntimes 返回本机 Runtime 列表（复用探测缓存）。
//
// 列表包含两类来源：本机探测到的 Java（`source=local`）与启动器已安装的
// Runtime（`source=downloaded`，P3-09 追加）。`selectedRuntimeId` 来自
// `java_runtimes.selected`，同一时刻至多一个。
//
// 本机探测失败时仍然返回已安装项：已安装的 Runtime 不依赖探测成功。
func (s *RuntimeService) ListRuntimes() (RuntimeList, error) {
	list, appErr := listRuntimesWithInstalled()
	if appErr != nil {
		return RuntimeList{}, appErr
	}
	return list, nil
}

// UninstallRuntime 卸载启动器自己安装的 Java 运行时。
//
// 返回卸载后的最新列表，调用方不需要再调一次 ListRuntimes。
// 只接受 `kind = downloaded` 的记录：本机探测到的 Java 由用户自行管理，
// 启动器不会去删用户环境里的东西。
func (s *RuntimeService) UninstallRuntime(req UninstallRuntimeRequest) (RuntimeList, error) {
	list, appErr := UninstallRuntime(req)
	if appErr != nil {
		return RuntimeList{}, appErr
	}
	return list, nil
}

// ResolveRuntimeRequirement 按版本元数据解析 Java 需求。//
// 只读本地版本 JSON，不联网、不安装。元数据未声明 Java 版本时返回
// `source=unknown`（不是错误），界面必须提示用户手动选择。
// 版本 id 为空、元数据不可读或非法时返回 `VERSION_METADATA_001`。
func (s *RuntimeService) ResolveRuntimeRequirement(req RuntimeRequirementRequest) (RuntimeRequirement, error) {
	requirement, appErr := ResolveRuntimeRequirement(req)
	if appErr != nil {
		return RuntimeRequirement{}, appErr
	}
	return requirement, nil
}

// InstallRuntime 安装官方 Java Runtime（P3-09）。
//
// 这是 P3 唯一会真实联网的入口之一：web 请求只发往官方清单与官方归档地址，
// 且全部经过来源白名单校验。真实下载由用户在功能验收时执行。
//
// 返回的 taskId 是进度事件的关联键（`runtime-install-<runtimeId>`），
// 它不是下载队列里的任务 id：Runtime 安装不经过 P3-04 的队列（D-48）。
func (s *RuntimeService) InstallRuntime(req InstallRuntimeRequest) (download.TaskAccepted, error) {
	accepted, appErr := InstallRuntime(req)
	if appErr != nil {
		return download.TaskAccepted{}, appErr
	}
	return accepted, nil
}

// SelectRuntime 选择要使用的 Runtime（P3-09）。
//
// 只写 `java_runtimes.selected`，不新增设置 key（D-28）；
// 与当前系统架构不匹配的 Runtime 需要 force=true 才能选中。
func (s *RuntimeService) SelectRuntime(req SelectRuntimeRequest) (RuntimeList, error) {
	list, appErr := SelectRuntime(req)
	if appErr != nil {
		return RuntimeList{}, appErr
	}
	return list, nil
}

// GetRuntimeSourceInfo 返回某个 Java 大版本在第三方来源上的最新构建（P3-14）。
//
// 只查询 Adoptium 的元数据，不下载归档、不安装。用于「Java 运行时」页展示
// 「可下载版本」与校验值；major 非法或当前平台不受支持时返回
// `RUNTIME_REQUIREMENT_001`，来源请求失败返回 `RUNTIME_SOURCE_001`。
func (s *RuntimeService) GetRuntimeSourceInfo(major int) (RuntimeSourceInfo, error) {
	info, appErr := GetRuntimeSourceInfo(major)
	if appErr != nil {
		return RuntimeSourceInfo{}, appErr
	}
	return info, nil
}

// ListRuntimeUpdates 返回上次更新检查的缓存结果（P3-14）。
//
// 只读数据库，**不触发任何网络请求**：页面打开时先渲染上次结论，
// 由用户显式点击「检查更新」再走 CheckRuntimeUpdates。
func (s *RuntimeService) ListRuntimeUpdates() ([]RuntimeUpdateInfo, error) {
	updates, appErr := ListRuntimeUpdates()
	if appErr != nil {
		return nil, appErr
	}
	return updates, nil
}

// CheckRuntimeUpdates 检查已安装第三方 Runtime 的小版本更新（P3-14）。
//
// 只处理 `kind='downloaded'` 且 `source_name='adoptium'` 的记录；
// 同一大版本只请求一次第三方 API，任一失败不影响其它项（失败项 CheckError 非空）。
func (s *RuntimeService) CheckRuntimeUpdates() ([]RuntimeUpdateInfo, error) {
	updates, appErr := CheckRuntimeUpdates()
	if appErr != nil {
		return nil, appErr
	}
	return updates, nil
}

// InstallRuntimeSource 从第三方来源（Adoptium/Temurin）安装指定大版本的 Java。
//
// 这是「更新到 21.0.13」与「按缺失的大版本安装」两条界面路径的落点，
// 与 InstallRuntime（官方 Mojang 组件）是两套来源，互不替代。
//
// 许可必须由调用方显式确认（req.AcceptLicense 为 false 时返回 RUNTIME_INSTALL_001）；
// 元数据或归档获取失败返回 RUNTIME_SOURCE_001；解压防护拒绝返回 RUNTIME_UNPACK_001。
// 本方法内部完成「下载 → SHA-256 校验 → 解压 → 原子改名 → 落库」全流程，
// 期间通过 download.task.changed.v1 推送进度，返回的 TaskAccepted 只表示已受理。
func (s *RuntimeService) InstallRuntimeSource(req InstallRuntimeSourceRequest) (download.TaskAccepted, error) {
	accepted, appErr := InstallRuntimeFromSource(req)
	if appErr != nil {
		return download.TaskAccepted{}, appErr
	}
	return accepted, nil
}
