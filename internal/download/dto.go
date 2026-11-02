package download

// 本文件存放资源（Assets）管理与后续卡共用的对外 DTO。
//
// 背景：P3-04/05/06 把对外结构直接定义在 task.go、wails.go 等处，
// 本卡（P3-07）需要新增 `AssetIndexSummary` 与 `AssetTaskProgress`，
// 而卡面指定的追加位置 `internal/download/dto.go` 在实现时并不存在，
// 因此本卡新建该文件（新增文件，不改动既有源文件）。
//
// 约束：
//   - JSON 字段一律 lowerCamelCase，与前端 `types/download.ts` 对齐；
//   - 不得出现绝对路径、用户名、完整 URL 查询串或凭证；
//   - 只放「交给前端 / 跨模块流转」的结构，内部模型留在 assets.go。

// AssetIndexWarning 是解析资产索引时产生的非致命提示。
//
// 索引里的声明的 totalSize 与实际累加值不一致、虚拟布局回退复制等
// 情形都不应让整次安装失败，但必须让用户与排障者看得见。
type AssetIndexWarning struct {
	// Code 为稳定告警码，取值见 assets.go 中的 WarningAsset* 常量。
	Code string `json:"code"`

	// Message 为面向用户的中文说明，不含路径与哈希原文以外的敏感信息。
	Message string `json:"message"`
}

// AssetIndexSummary 是资产索引的摘要。
//
// 该结构同时用于「索引任务完成后的展示」与「安装计划诊断」：
// 对象总数与去重后的哈希数一起给出，才能解释为什么子任务数量
// 比 objects 条数少（大量逻辑名共享同一个对象）。
type AssetIndexSummary struct {
	// IndexID 是索引标识（例如 "1.21"），不是文件路径。
	IndexID string `json:"indexId"`

	// ObjectCount 是索引中声明的逻辑条目数（含重复哈希）。
	ObjectCount int `json:"objectCount"`

	// UniqueHashes 是去重后的对象数，即真正需要下载/校验的次数。
	UniqueHashes int `json:"uniqueHashes"`

	// TotalBytes 是全部逻辑条目声明大小之和。
	TotalBytes int64 `json:"totalBytes"`

	// DeclaredTotalSize 是版本元数据里声明的 totalSize；0 表示未声明。
	DeclaredTotalSize int64 `json:"declaredTotalSize"`

	// TotalSizeMismatch 表示声明的 totalSize 与实际累加值不一致。
	TotalSizeMismatch bool `json:"totalSizeMismatch"`

	// IndexSizeBytes 是索引文件自身的字节数（用于诊断「索引多大」）。
	IndexSizeBytes int64 `json:"indexSizeBytes"`

	// Virtual 表示该索引要求使用虚拟布局组装。
	Virtual bool `json:"virtual"`

	// MapToResources 表示索引声明了 map_to_resources。
	//
	// D-29 已冻结：P3 不启用该布局，本字段只用于兼容矩阵展示，
	// 上层**不得**据此写入 `versions/<id>/resources/`。
	MapToResources bool `json:"mapToResources"`

	// Warnings 是解析期非致命提示。
	Warnings []AssetIndexWarning `json:"warnings"`
}

// AssetTaskProgress 是资产父任务的聚合进度载荷。
//
// 事件风暴控制（D-45）：父任务进度统一走 `download.task.changed.v1`，
// 载荷里带 progress 字段；本结构是 `payload.progress` 的展开形态，
// 由 `AssetProgressTracker` 在「首条 / 距上次 ≥500ms / 终态」时产出，
// 子任务本身完全不发事件。
type AssetTaskProgress struct {
	// TaskID 是父任务（索引任务）标识。
	TaskID string `json:"taskId"`

	// CompletedObjects 是已确认完成的对象数。
	CompletedObjects int `json:"completedObjects"`

	// FailedObjects 是失败的对象数。
	FailedObjects int `json:"failedObjects"`

	// TotalObjects 是去重后需要处理的唯一对象总数。
	TotalObjects int `json:"totalObjects"`

	// CompletedBytes 是已完成对象声明的字节数之和。
	CompletedBytes int64 `json:"completedBytes"`

	// TotalBytes 是去重对象声明的字节数之和。
	TotalBytes int64 `json:"totalBytes"`

	// Progress 是 0..1 的进度；按已完成字节 / 总字节计算，
	// 总字节为 0 时退化为对象数比例。
	Progress float64 `json:"progress"`

	// FailureRate 是失败对象占比（0..1）。
	FailureRate float64 `json:"failureRate"`

	// State 是父任务聚合状态（downloading / failed / completed）。
	State string `json:"state"`

	// FailureSamples 是最多 5 个失败对象的哈希前缀，供用户定位。
	FailureSamples []string `json:"failureSamples"`

	// UpdatedAtMs 是本条进度的采样时间（毫秒时间戳）。
	UpdatedAtMs int64 `json:"updatedAtMs"`
}
