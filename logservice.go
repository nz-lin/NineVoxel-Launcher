// Package main 的日志服务入口。
//
// 本文件是 internal/logging 的 Wails 薄封装：只做参数校验、错误码转换与结构体适配，
// 不实现日志写入、轮转与脱敏（这些都在 internal/logging 内）。
//
// 为什么单独一个文件而不是放进 appservice.go：
// appservice.go 归 P1-06（事件协议）所有，本卡不得修改；日志服务有独立的
// 生命周期与契约，独立成文件便于两侧并行演进。
package main

import (
	"fmt"

	"example.com/ninevoxel/launcher/internal/logging"
)

// 日志相关的稳定错误码。
//
// LOG_IO_001 由接口冻结清单 §8.3 冻结：「日志目录不可写 / 日志读取失败」。
// 该域独立于 APP 域：日志是跨模块基础设施，复用 APP 域会与配置类错误混淆。
const (
	// errorCodeLogIO 表示日志读写失败。
	errorCodeLogIO = "LOG_IO_001"
)

// LogEntry 表示一条已脱敏的结构化日志记录。
//
// 字段与《错误码与日志规范》A 节一致，JSON 使用 lowerCamelCase。
// 本类型是 internal/logging.Record 的对外镜像：服务层保持独立类型，
// 避免 internal 包的结构调整直接影响已冻结的前端契约。
type LogEntry struct {
	// TimestampMs 为记录时间（UTC Unix 毫秒），供前端按本地时区展示。
	TimestampMs int64 `json:"timestampMs"`

	// Level 为日志级别：debug / info / warn / error。
	Level string `json:"level"`

	// Component 为产生日志的组件名。
	Component string `json:"component"`

	// Event 为结构化事件名；无事件时为空。
	Event string `json:"event"`

	// CorrelationID 为关联一次用户操作的追踪 ID。
	CorrelationID string `json:"correlationId"`

	// OperationID 为一次操作或长任务的 ID。
	OperationID string `json:"operationId"`

	// ErrorCode 为稳定错误码；成功路径为空。
	ErrorCode string `json:"errorCode"`

	// Message 为脱敏后的可读说明。
	Message string `json:"message"`

	// Attributes 为扩展信息，扁平字符串键值对。
	Attributes map[string]string `json:"attributes"`
}

// LogQuery 描述一次日志查询的条件。
//
// 与接口冻结清单 §8.6 的 LogQuery 字段逐字一致。
type LogQuery struct {
	// Level 为级别过滤，空表示全部级别。
	Level string `json:"level"`

	// Component 为组件过滤，空表示全部组件。
	Component string `json:"component"`

	// Limit 为最大返回条数；缺省 200，Go 边界裁剪到上限 1000。
	Limit int `json:"limit"`

	// BeforeMs 为时间游标（UTC Unix 毫秒），只返回早于该时间的记录；0 表示不限。
	BeforeMs int64 `json:"beforeMs"`
}

// LogQueryResult 返回查询结果与下一页游标。
//
// 与接口冻结清单 §8.6 的 LogQueryResult 字段逐字一致。
type LogQueryResult struct {
	// Entries 为本页记录，按时间倒序。
	Entries []LogEntry `json:"entries"`

	// NextBeforeMs 为继续加载更早日志时使用的游标；0 表示没有更多记录。
	NextBeforeMs int64 `json:"nextBeforeMs"`
}

// LogStatus 返回日志子系统状态，不暴露绝对路径。
//
// 与接口冻结清单 §8.6 的 LogStatus 字段逐字一致：
// enabled / directoryKind / fileCount / latestFileName / totalBytes。
// 禁止增加路径字段：前端只需要知道目录是否可用与来源类别。
type LogStatus struct {
	// Enabled 表示日志目录是否可写；false 时本次运行仅输出到控制台。
	Enabled bool `json:"enabled"`

	// DirectoryKind 为目录来源类别：userConfig / userCache / exeDir / unavailable。
	// userConfig 为 macOS（Application Support），userCache 为 Windows（%LocalAppData%）。
	DirectoryKind string `json:"directoryKind"`

	// FileCount 为结构化日志文件数量。
	FileCount int `json:"fileCount"`

	// LatestFileName 为最新的结构化日志文件名；无文件时为空。
	LatestFileName string `json:"latestFileName"`

	// TotalBytes 为结构化日志文件占用的总字节数。
	TotalBytes int64 `json:"totalBytes"`
}

// LogService 是日志子系统的 Wails 服务。
//
// 本类型只持有 internal/logging 的 Logger，不自己维护文件句柄与状态，
// 因此可以在服务注册阶段创建、无需额外的启动/关闭钩子。
type LogService struct {
	// logger 为进程级结构化日志写入器。
	logger *logging.Logger
}

// newLogService 创建日志服务实例。
//
// 参数 logger 为进程级 Logger（通常传 logging.Default）。
// 传入 nil 时服务仍然可用：所有读取返回空结果，所有写入被静默丢弃，
// 避免因装配顺序问题导致服务方法 panic。
func newLogService(logger *logging.Logger) *LogService {
	return &LogService{logger: logger}
}

// Query 按条件读取已脱敏日志，按时间倒序返回。
//
// 参数 req 为查询条件；Limit 由 Go 边界做上限裁剪，不信任前端传入值。
// 读取失败时返回包装为 LOG_IO_001 的错误，前端据错误码展示稳定提示。
func (s *LogService) Query(req LogQuery) (LogQueryResult, error) {
	if s == nil || s.logger == nil {
		return LogQueryResult{Entries: []LogEntry{}}, nil
	}

	result, err := logging.Query(s.logger, logging.QueryRequest{
		Level:     req.Level,
		Component: req.Component,
		Limit:     req.Limit,
		BeforeMs:  req.BeforeMs,
	})
	if err != nil {
		// 错误信息不含路径：只给出稳定错误码与可读说明，
		// 详细原因由 logging 包在控制台与自身告警中记录。
		return LogQueryResult{}, fmt.Errorf("%s: 读取日志失败", errorCodeLogIO)
	}

	entries := make([]LogEntry, 0, len(result.Entries))
	for _, record := range result.Entries {
		entries = append(entries, toLogEntry(record))
	}

	return LogQueryResult{
		Entries:      entries,
		NextBeforeMs: result.NextBeforeMs,
	}, nil
}

// Status 返回日志子系统状态。
//
// 返回结构不含绝对路径（接口冻结清单 §8.5 明令禁止）。
func (s *LogService) Status() (LogStatus, error) {
	if s == nil || s.logger == nil {
		return LogStatus{
			Enabled:       false,
			DirectoryKind: logging.DirectoryKindUnavailable,
		}, nil
	}

	status := s.logger.Status()
	return LogStatus{
		Enabled:        status.Enabled,
		DirectoryKind:  status.DirectoryKind,
		FileCount:      status.FileCount,
		LatestFileName: status.LatestFileName,
		TotalBytes:     status.TotalBytes,
	}, nil
}

// toLogEntry 把内部记录转换为对外的日志条目。
//
// 转换过程不修改内容：脱敏已在写入与读取两侧完成，这里只做结构映射。
// TimestampMs 由时间字符串解析为 Unix 毫秒，供前端按本地时区展示。
func toLogEntry(record logging.Record) LogEntry {
	return LogEntry{
		TimestampMs:   record.TimestampMillis(),
		Level:         record.Level,
		Component:     record.Component,
		Event:         record.Event,
		CorrelationID: record.CorrelationID,
		OperationID:   record.OperationID,
		ErrorCode:     record.ErrorCode,
		Message:       record.Message,
		Attributes:    record.Attributes,
	}
}
