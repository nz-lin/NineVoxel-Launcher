// 本文件定义下载任务的状态机、任务模型与面向前端的 DTO。
//
// 状态机严格对应《接口冻结清单》§7 的转移表。之所以把转移表写成显式的
// 字符串映射而不是用位运算或数值区间：下载状态是跨端契约（前端按 state
// 渲染），显式映射能一眼与文档逐行对照，也便于表驱动测试。
package download

import (
	"fmt"
	"sort"
)

// State 表示下载任务的调度状态。
type State string

// 九个下载状态（冻结清单 §7）。
const (
	// StateQueued 表示任务已入队，等待调度。
	StateQueued State = "queued"

	// StateResolving 表示正在解析下载地址与元数据（不许发字节）。
	StateResolving State = "resolving"

	// StateDownloading 表示正在传输字节；重试期间也保持该状态。
	StateDownloading State = "downloading"

	// StatePaused 表示用户暂停，已下载字节保留，可恢复。
	StatePaused State = "paused"

	// StateVerifying 表示传输结束，正在校验哈希与大小。
	StateVerifying State = "verifying"

	// StateCommitting 表示正在把 staging 文件原子提交到目标位置。
	StateCommitting State = "committing"

	// StateCompleted 表示任务成功完成（终态）。
	StateCompleted State = "completed"

	// StateFailed 表示任务失败且不再重试（终态）。
	StateFailed State = "failed"

	// StateCancelled 表示任务被取消（终态，不可恢复）。
	StateCancelled State = "cancelled"
)

// 任务类型常量，与迁移文件与冻结清单的 task_type 取值一致。
const (
	// TaskTypePlan 表示计划级任务（聚合子任务进度）。
	TaskTypePlan = "plan"

	// TaskTypeVersionJSON 表示版本元数据文件。
	TaskTypeVersionJSON = "version_json"

	// TaskTypeClientJar 表示客户端 JAR。
	TaskTypeClientJar = "client_jar"

	// TaskTypeLibrary 表示依赖库。
	TaskTypeLibrary = "library"

	// TaskTypeAssetIndex 表示资源索引。
	TaskTypeAssetIndex = "asset_index"

	// TaskTypeAsset 表示资源对象。
	TaskTypeAsset = "asset"

	// TaskTypeLoader 表示 Forge / NeoForge 官方安装器。
	TaskTypeLoader = "loader"
)

// 目标路径根类别，与迁移文件中 target_kind 的取值一致。
const (
	// TargetKindShared 表示共享内容寻址缓存（libraries / assets / runtime）。
	TargetKindShared = "shared"

	// TargetKindVersion 表示版本私有目录（versions/<id>/）。
	TargetKindVersion = "version"
)

// 调度优先级：数值越小越先执行。
//
// 顺序依据卡面第 2 条：元数据 > 版本 JSON > 客户端 JAR > 库 > 资源索引 > 资源对象。
// 先取小文件、先拿元数据，是为了在下载大 JAR 之前就暴露「版本不存在」这类
// 确定性失败，避免用户等几分钟才看到错误。
const (
	// PriorityMetadata 表示版本元数据相关任务（最高优先级）。
	PriorityMetadata = 0

	// PriorityVersionJSON 表示版本 JSON 文件。
	PriorityVersionJSON = 1

	// PriorityClientJar 表示客户端 JAR。
	PriorityClientJar = 2

	// PriorityLibrary 表示依赖库。
	PriorityLibrary = 3

	// PriorityAssetIndex 表示资源索引。
	PriorityAssetIndex = 4

	// PriorityAssetObject 表示资源对象。
	PriorityAssetObject = 5
)

// legalTransitions 是状态机的唯一真相源。
//
// 键为当前状态，值为允许迁移到的状态集合。终态不出现在键里，
// 因此任何从终态出发的迁移都会被 CanTransit 判为非法。
var legalTransitions = map[State][]State{
	StateQueued:      {StateResolving, StateDownloading, StatePaused, StateCancelled},
	StateResolving:   {StateDownloading, StateFailed, StateCancelled},
	StateDownloading: {StatePaused, StateVerifying, StateFailed, StateCancelled},
	StatePaused:      {StateQueued, StateDownloading, StateCancelled},
	StateVerifying:   {StateCommitting, StateFailed, StateCancelled},
	StateCommitting:  {StateCompleted, StateFailed},
}

// allStates 按固定顺序列出全部状态，供界面与测试枚举。
//
// 排序固定而非从 map 迭代：map 迭代顺序随机，会让人机读到的
// 「状态列表」每次都不一样，测试也会随机失败。
var allStates = []State{
	StateQueued,
	StateResolving,
	StateDownloading,
	StatePaused,
	StateVerifying,
	StateCommitting,
	StateCompleted,
	StateFailed,
	StateCancelled,
}

// AllStates 返回全部状态（副本），顺序固定。
func AllStates() []State {
	result := make([]State, len(allStates))
	copy(result, allStates)
	return result
}

// IsTerminal 判断状态是否为终态（completed / failed / cancelled）。
//
// 终态任务不再被调度，也不再接受暂停/恢复/取消操作。
func IsTerminal(state State) bool {
	switch state {
	case StateCompleted, StateFailed, StateCancelled:
		return true
	default:
		return false
	}
}

// IsValidState 判断状态值是否属于状态机定义的状态集合。
func IsValidState(state State) bool {
	for _, candidate := range allStates {
		if candidate == state {
			return true
		}
	}
	return false
}

// CanTransit 判断从 from 到 to 的迁移是否合法。
//
// 相同状态的「原地不动」返回 false：状态变化事件只在真的变化时发射，
// 把 A→A 判为合法会让去重逻辑失效。
func CanTransit(from State, to State) bool {
	allowed, ok := legalTransitions[from]
	if !ok {
		return false
	}
	for _, candidate := range allowed {
		if candidate == to {
			return true
		}
	}
	return false
}

// ValidateTransition 校验迁移并在非法时返回 DOWNLOAD_STATE_001。
func ValidateTransition(from State, to State) *Error {
	if CanTransit(from, to) {
		return nil
	}
	return newInputError(ErrorCodeState, fmt.Sprintf("%s（%s → %s）", messageStateNotAllowed, describeState(from), describeState(to)))
}

// 允许被取消的状态集合（committing 之后不可取消，见 messageCancelDuringCommit）。
var cancellableStates = map[State]bool{
	StateQueued:      true,
	StateResolving:   true,
	StateDownloading: true,
	StatePaused:      true,
	StateVerifying:   true,
}

// CanCancel 判断当前状态是否允许取消。
//
// 与状态机的关系：状态机允许 verifying→cancelled，但 committing 不允许；
// 本函数只做「语义层」判定，真正的转移仍走 ValidateTransition。
func CanCancel(state State) bool {
	return cancellableStates[state]
}

// CanPause 判断当前状态是否允许暂停。
//
// 只有正在传输的任务需要暂停；queued 已经是「未开始」，
// 暂停它没有意义（用户想要的是取消）。
func CanPause(state State) bool {
	return state == StateDownloading
}

// CanResume 判断当前状态是否允许恢复。
func CanResume(state State) bool {
	return state == StatePaused
}

// priorityOf 按任务类型给出调度优先级。
func priorityOf(taskType string) int {
	switch taskType {
	case TaskTypePlan:
		return PriorityMetadata
	case TaskTypeVersionJSON:
		return PriorityVersionJSON
	case TaskTypeClientJar:
		return PriorityClientJar
	case TaskTypeLibrary:
		return PriorityLibrary
	case TaskTypeAssetIndex:
		return PriorityAssetIndex
	case TaskTypeAsset:
		return PriorityAssetObject
	case TaskTypeLoader:
		return PriorityLibrary
	default:
		// 未知类型排到最后：调度器不应因为类型表扩展而崩溃。
		return PriorityAssetObject + 1
	}
}

// taskTypeTitle 返回任务类型的中文展示名（用于 DTO 的 title）。
//
// 说明：title 目前由后端拼中文，多语言化时前端可改为按 taskType 自行渲染。
func taskTypeTitle(taskType string) string {
	switch taskType {
	case TaskTypePlan:
		return "版本文件"
	case TaskTypeVersionJSON:
		return "版本元数据"
	case TaskTypeClientJar:
		return "客户端 JAR"
	case TaskTypeLibrary:
		return "依赖库"
	case TaskTypeAssetIndex:
		return "资源索引"
	case TaskTypeAsset:
		return "资源文件"
	case TaskTypeLoader:
		return "模组加载器安装器"
	default:
		return "下载任务"
	}
}

// Task 是下载任务的完整后端模型（与 download_tasks 表列一一对应）。
type Task struct {
	// ID 为任务唯一标识。
	ID string

	// ParentTaskID 指向父任务（计划级任务为空）。
	ParentTaskID string

	// ArtifactID 关联制品标识（计划级任务为空）。
	ArtifactID string

	// TaskType 为任务类型，取值见 TaskType* 常量。
	TaskType string

	// SourceURI 为下载源地址，**只在后端流转**，绝不进入 DTO。
	SourceURI string

	// State 为当前调度状态。
	State State

	// StagingRelPath 为 staging 相对路径（P3-06 写入，本卡为空）。
	StagingRelPath string

	// TargetRelPath 为最终目标相对路径（由 P3-03 的 ResolveTarget 解析）。
	TargetRelPath string

	// TargetKind 为目标根类别（shared / version）。
	TargetKind string

	// ExpectedSize 为期望字节数，0 表示未知。
	ExpectedSize int64

	// DownloadedSize 为已下载字节数（续传起点）。
	DownloadedSize int64

	// RangeStart 为已有 .part 字节偏移（P3-06 写入）。
	RangeStart int64

	// ExpectedSHA1 为期望 SHA-1（资产以 SHA-1 为寻址键）。
	ExpectedSHA1 string

	// ExpectedSHA256 为期望 SHA-256，可能为空。
	ExpectedSHA256 string

	// ETag 为服务端 ETag（P3-05 写入）。
	ETag string

	// LastModified 为服务端 Last-Modified（P3-05 写入）。
	LastModified string

	// PlanHash 为计划哈希，幂等键之一。
	PlanHash string

	// RetryCount 为已重试次数。
	RetryCount int

	// ErrorCode 为最近一次失败的稳定错误码，成功路径为空。
	ErrorCode string

	// ErrorMessageSafe 为已脱敏的失败文案。
	ErrorMessageSafe string

	// VerifiedAtMs 为提交后校验通过的时间（P3-06 写入）。
	VerifiedAtMs int64

	// CreatedAtMs / UpdatedAtMs / CompletedAtMs 为 UTC Unix 毫秒时间戳。
	CreatedAtMs   int64
	UpdatedAtMs   int64
	CompletedAtMs int64

	// VersionID 为所属版本 ID。
	//
	// 注：download_tasks 表按 D-25 的列集没有 version_id 列，
	// 该字段只在内存中维护（用于过滤与标题）；跨进程恢复时由父任务的
	// plan_hash 关联得到，属 P3-07 的恢复矩阵范围。
	VersionID string

	// OperationID 把同一次安装产生的多个计划任务归为一组。
	//
	// 例如实例安装会拆成「版本文件」与「资源文件」两个计划任务；
	// 二者共享同一个 OperationID，界面据此合并为一个下载条目。
	OperationID string

	// SpeedBytesPerSecond 为最近一次采样得到的瞬时速度（仅内存）。
	SpeedBytesPerSecond int64

	// lastSampleAtMs / lastSampleBytes 是速度采样的内部基线。
	lastSampleAtMs  int64
	lastSampleBytes int64
}

// Priority 返回任务的调度优先级。
func (t Task) Priority() int {
	if t.TaskType == TaskTypePlan {
		return PriorityMetadata
	}
	return priorityOf(t.TaskType)
}

// Title 返回任务的中文标题（形如 `1.20.1 · 客户端 JAR`）。
func (t Task) Title() string {
	name := taskTypeTitle(t.TaskType)
	if t.VersionID == "" {
		return name
	}
	return t.VersionID + " · " + name
}

// TransferResult 是传输接缝的返回值（由 P3-05 实现填充）。
type TransferResult struct {
	// Downloaded 为本轮写入的字节数（用于进度累计）。
	Downloaded int64

	// TotalSize 为服务端声明的总字节数，0 表示未知。
	TotalSize int64

	// ETag / LastModified 为响应头，供续传一致性校验（P3-05 使用）。
	ETag         string
	LastModified string

	// Retryable 表示失败是否值得重试（网络错误、超时、5xx、429 为 true）。
	Retryable bool

	// Err 为传输错误；非空表示本轮失败。
	Err error

	// ErrorCode 为 Err 对应的稳定错误码，Err 为空时忽略。
	ErrorCode string
}

// StartDownloadPlanRequest 是「按安装计划创建下载任务」的入参。
type StartDownloadPlanRequest struct {
	// VersionID 为要下载的版本 ID。
	VersionID string `json:"versionId"`

	// PlanHash 为前端持有的计划哈希。
	//
	// 用于检测计划漂移：与后端重新解析出的 planHash 不一致时返回 ARTIFACT_PLAN_001，
	// 提示用户刷新——拿旧计划下载会把新版本的文件放进旧目录。
	PlanHash string `json:"planHash"`

	// OperationID 把同一次安装的多个计划归为一组；空串表示不分组。
	OperationID string `json:"operationId"`

	// TargetKind 为共享/版本私有，空值按 shared 处理。
	TargetKind string `json:"targetKind"`
}

// TaskIDRequest 是按任务 ID 操作的入参。
type TaskIDRequest struct {
	// TaskID 为任务唯一标识。
	TaskID string `json:"taskId"`
}

// DownloadTaskFilter 是任务列表查询条件。
type DownloadTaskFilter struct {
	ParentsOnly bool `json:"parentsOnly"`
	// States 为空表示不限状态。
	States []string `json:"states"`

	// VersionID 为空表示不限版本。
	VersionID string `json:"versionId"`

	// Offset / Limit 为分页参数，Limit 上限 100。
	Offset int `json:"offset"`
	Limit  int `json:"limit"`
}

// DownloadTaskError 是面向前端的错误信息。
type DownloadTaskError struct {
	// Code 为稳定错误码。
	Code string `json:"code"`

	// Message 为已脱敏的中文文案。
	Message string `json:"message"`

	// Retryable 表示界面是否应提示「可重试」。
	Retryable bool `json:"retryable"`
}

// TaskAccepted 是创建任务后的返回体。
type TaskAccepted struct {
	// TaskID 为新任务（或命中去重时已存在任务）的标识。
	TaskID string `json:"taskId"`

	// ParentTaskID 为父任务标识，无父任务时为空。
	ParentTaskID string `json:"parentTaskId,omitempty"`

	// State 为任务当前状态。
	State string `json:"state"`

	// Duplicate 表示因去重规则复用了已存在的任务。
	Duplicate bool `json:"duplicate"`

	// CreatedAtMs 为创建时间。
	CreatedAtMs int64 `json:"createdAtMs"`
}

// DownloadTaskView 是面向前端的任务视图。
//
// 安全约束（冻结清单 §2.3）：不含绝对路径、不含完整 URL 查询串、
// 不含凭证；source_uri / staging_rel_path 等后端字段一律不出现。
type DownloadTaskView struct {
	// ID 为任务标识。
	ID string `json:"id"`

	// Title 为展示标题。
	Title string `json:"title"`

	// State 为当前状态。
	State string `json:"state"`

	// DownloadedBytes 为已下载字节数。
	DownloadedBytes int64 `json:"downloadedBytes"`

	// TotalBytes 为总字节数，未知时为 nil。
	TotalBytes *int64 `json:"totalBytes,omitempty"`

	// SpeedBytesPerSecond 为瞬时速度（仅运行中提供）。
	SpeedBytesPerSecond *int64 `json:"speedBytesPerSecond,omitempty"`

	// OperationID 把同一次安装产生的多个任务归为一组；空串表示不分组。
	OperationID string `json:"operationId,omitempty"`

	// Progress 为 0~1 的进度，总大小未知时为 nil。
	Progress *float64 `json:"progress,omitempty"`

	// Error 为失败信息，非失败状态为 nil。
	Error *DownloadTaskError `json:"error,omitempty"`

	// ArtifactCount / CompletedCount 为计划内制品数与已完成数。
	ArtifactCount  int `json:"artifactCount"`
	CompletedCount int `json:"completedCount"`

	// CreatedAtMs / UpdatedAtMs / CompletedAtMs 为时间戳，未完成为 0。
	CreatedAtMs   int64 `json:"createdAtMs"`
	UpdatedAtMs   int64 `json:"updatedAtMs"`
	CompletedAtMs int64 `json:"completedAtMs"`
}

// DownloadTaskList 是任务列表返回体。
type DownloadTaskList struct {
	// Tasks 为当前页任务。
	Tasks []DownloadTaskView `json:"tasks"`

	// Total 为过滤后的总数（分页前）。
	Total int `json:"total"`
}

// taskToView 把后端任务模型转成前端视图。
//
// 转换过程同时承担脱敏职责：只挑选允许暴露的字段，
// 因此即使 Task 结构以后加了敏感字段，也不会意外泄漏给前端。
func taskToView(task Task) DownloadTaskView {
	view := DownloadTaskView{
		ID:              task.ID,
		Title:           task.Title(),
		State:           string(task.State),
		DownloadedBytes: task.DownloadedSize,
		OperationID:     task.OperationID,
		CreatedAtMs:     task.CreatedAtMs,
		UpdatedAtMs:     task.UpdatedAtMs,
		CompletedAtMs:   task.CompletedAtMs,
	}
	if task.SpeedBytesPerSecond > 0 && (task.State == StateDownloading || task.TaskType == TaskTypePlan) {
		speed := task.SpeedBytesPerSecond
		view.SpeedBytesPerSecond = &speed
	}
	if task.ExpectedSize > 0 {
		total := task.ExpectedSize
		view.TotalBytes = &total
		progress := float64(task.DownloadedSize) / float64(task.ExpectedSize)
		if progress < 0 {
			progress = 0
		}
		if progress > 1 {
			progress = 1
		}
		view.Progress = &progress
	}
	if task.ErrorCode != "" {
		view.Error = &DownloadTaskError{
			Code:      task.ErrorCode,
			Message:   task.ErrorMessageSafe,
			Retryable: false,
		}
	}
	return view
}

// sortTasksForDisplay 按「未结束优先、创建时间倒序」排序，供列表接口使用。
//
// 未结束任务排在前面：用户打开界面最关心的是「还在跑什么」。
func sortTasksForDisplay(tasks []Task) {
	sort.SliceStable(tasks, func(i, j int) bool {
		iTerminal := IsTerminal(tasks[i].State)
		jTerminal := IsTerminal(tasks[j].State)
		if iTerminal != jTerminal {
			return !iTerminal
		}
		if tasks[i].CreatedAtMs != tasks[j].CreatedAtMs {
			return tasks[i].CreatedAtMs > tasks[j].CreatedAtMs
		}
		return tasks[i].ID < tasks[j].ID
	})
}
