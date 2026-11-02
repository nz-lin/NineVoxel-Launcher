// Package logging 提供 NineVoxel Launcher 的结构化日志能力。
//
// 设计目标（对应 launcher/docs/tasks/P1/P1-05.md 与《错误码与日志规范》）：
//   - 每条日志为一行 JSON（JSON Lines，UTF-8），字段集合固定，不允许扩展一级字段；
//   - 按大小与保留天数轮转，并对整个日志目录设容量上限；
//   - 写入侧按级别过滤（默认 info，debug 需显式开启）；
//   - 写入前统一脱敏，任何情况下不落 Token / Cookie / 密码 / 邮箱 / 绝对路径；
//   - 日志写入失败不得影响主业务：只降级到 stderr，不 panic、不把错误抛给业务调用方。
//
// 日志目录**不**在本包内重新解析：一律复用 internal/platform 的 LogDirectory，
// 保证与 P0 既定优先级（ADR-011 同族策略）完全一致。
//
// 本文件（fields.go）定义日志字段名、级别枚举与结构化记录结构，
// 这些名字是冻结契约的一部分：前端 LogsPage 与 bindings 生成物直接依赖它们。
package logging

import (
	"fmt"
	"strings"
)

// 日志记录的一级字段名。
//
// 这些常量就是 JSON Lines 每条记录允许出现的全部一级键，
// 新增一级字段必须先改本文档与接口冻结清单，不得在写入处临时加键。
// 扩展信息一律放入 attributes（字符串键值对）。
const (
	// FieldTimestamp 为记录时间，UTC RFC3339 毫秒精度。
	FieldTimestamp = "timestamp"

	// FieldLevel 为日志级别，取值见 Level 枚举。
	FieldLevel = "level"

	// FieldComponent 为产生日志的组件名，例如 logging、app、settings。
	FieldComponent = "component"

	// FieldEvent 为结构化事件名，建议沿用 domain.resource.action.v1 风格。
	FieldEvent = "event"

	// FieldCorrelationID 为关联一次用户操作的追踪 ID。
	FieldCorrelationID = "correlationId"

	// FieldOperationID 为长任务或一次批处理内的操作 ID。
	FieldOperationID = "operationId"

	// FieldErrorCode 为稳定错误码，格式 <DOMAIN>_<CATEGORY>_<NNN>。
	FieldErrorCode = "errorCode"

	// FieldMessage 为脱敏后的可读说明，是唯一面向人的自由文本字段。
	FieldMessage = "message"

	// FieldAttributes 为扩展信息，值是字符串键值对的扁平对象。
	FieldAttributes = "attributes"
)

// Level 表示日志级别。
//
// 级别是字符串类型而非整数：JSON 中直接输出可读值，避免前后端再维护一张映射表。
type Level string

// 允许的日志级别取值，与《错误码与日志规范》逐字一致。
const (
	// LevelDebug 为调试级别，默认不写入，仅供排查问题时显式开启。
	LevelDebug Level = "debug"

	// LevelInfo 为常规信息级别，是默认生效级别。
	LevelInfo Level = "info"

	// LevelWarn 为警告级别：功能已降级但仍可继续运行。
	LevelWarn Level = "warn"

	// LevelError 为错误级别：操作失败且需要用户或开发者关注。
	LevelError Level = "error"
)

// 包级默认值：与接口冻结清单 §8.7 的 logging.level 默认值保持一致。
const (
	// defaultLevel 是未显式配置时生效的级别。
	// 依据「默认 info」：debug 会产生较多细节，默认关闭以免日志膨胀。
	defaultLevel = LevelInfo
)

// ParseLevel 把外部传入的级别字符串解析为 Level。
//
// 解析规则：忽略大小写与首尾空白；空串与无法识别的值一律回退到 defaultLevel（info），
// 不返回错误——级别配置属于非关键配置，配置错误不应阻断日志系统工作。
func ParseLevel(value string) Level {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case string(LevelDebug):
		return LevelDebug
	case string(LevelInfo):
		return LevelInfo
	case string(LevelWarn):
		return LevelWarn
	case string(LevelError):
		return LevelError
	default:
		return defaultLevel
	}
}

// severity 返回级别的高低次序，数值越大越严重。
//
// 该次序用于写入侧过滤：只有级别不低于当前阈值的记录才会真正写入。
// 未知级别按 info 处理，与 ParseLevel 的回退规则保持一致。
func (l Level) severity() int {
	normalized := Level(strings.ToLower(strings.TrimSpace(string(l))))
	switch normalized {
	case LevelDebug:
		return 0
	case LevelWarn:
		return 2
	case LevelError:
		return 3
	default:
		return 1
	}
}

// Enabled 判断当前级别在给定阈值下是否应当被写入。
//
// 参数 threshold 为当前生效的最低写入级别；返回 true 表示该记录需要写入。
func (l Level) Enabled(threshold Level) bool {
	return l.severity() >= threshold.severity()
}

// IsValid 判断级别是否为允许的四个取值之一。
func (l Level) IsValid() bool {
	normalized := Level(strings.ToLower(strings.TrimSpace(string(l))))
	switch normalized {
	case LevelDebug, LevelInfo, LevelWarn, LevelError:
		return true
	default:
		return false
	}
}

// Record 表示一条待写入或已读取的结构化日志记录。
//
// 字段集合与《错误码与日志规范》A 节一致：
// timestamp / level / component / event / correlationId / operationId /
// errorCode / message / attributes。
// 其中 timestamp、level、component、message 始终存在；其余字段为空时在 JSON 中省略。
//
// 本结构同时作为前端 LogsPage 的数据载体，因此 JSON 标签使用 lowerCamelCase，
// 与 bindings 生成的前端模型字段名保持一致。
type Record struct {
	// Timestamp 为记录时间，UTC RFC3339 毫秒精度，例如 2026-11-01T00:00:00.000Z。
	Timestamp string `json:"timestamp"`

	// Level 为日志级别，取值 debug / info / warn / error。
	Level string `json:"level"`

	// Component 为产生日志的组件名。
	Component string `json:"component"`

	// Event 为结构化事件名；无事件时省略。
	Event string `json:"event,omitempty"`

	// CorrelationID 为关联一次用户操作的追踪 ID；无关联时省略。
	CorrelationID string `json:"correlationId,omitempty"`

	// OperationID 为一次操作或长任务的 ID；无关联时省略。
	OperationID string `json:"operationId,omitempty"`

	// ErrorCode 为稳定错误码；成功路径省略该字段。
	ErrorCode string `json:"errorCode,omitempty"`

	// Message 为脱敏后的可读说明，始终存在。
	Message string `json:"message"`

	// Attributes 为扩展信息，扁平字符串键值对，可为空对象。
	Attributes map[string]string `json:"attributes,omitempty"`
}

// sanitizeForWrite 归一化记录中的必填字段，保证 JSON 一级字段约定不被破坏。
//
// 规则：
//   - timestamp 为空时补当前时间（由调用方提前取值，本函数只兜底）；
//   - level 非法时回退到 info；
//   - component 为空时回退到 unknownComponent，避免出现空组件名导致前端分组失效；
//   - message 与 attributes 的脱敏由 redact 包内函数负责，这里不重复处理。
func (r *Record) normalize(fallbackTimestamp string) {
	if strings.TrimSpace(r.Timestamp) == "" {
		r.Timestamp = fallbackTimestamp
	}
	if !Level(r.Level).IsValid() {
		r.Level = string(LevelInfo)
	}
	if strings.TrimSpace(r.Component) == "" {
		r.Component = unknownComponent
	}
}

// unknownComponent 是组件名缺失时的兜底值。
//
// 使用固定英文标识而不是中文：组件名是日志的机器可读维度，
// 前端展示时另行本地化，避免日志文件内容随界面语言变化。
const unknownComponent = "unknown"

// String 返回记录的简短描述，仅用于调试与错误信息拼接。
//
// 注意：该方法输出的是**已脱敏**内容（调用方必须先用 redact 处理），
// 不得把原始 message 直接交给本方法。
func (r Record) String() string {
	return fmt.Sprintf("%s [%s] %s: %s", r.Timestamp, r.Level, r.Component, r.Message)
}
