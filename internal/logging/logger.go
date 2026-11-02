// 本文件实现结构化日志的对外入口：Logger 类型、级别过滤、字段脱敏与降级策略。
//
// 调用约定：
//   - 业务代码只持有 *Logger，通过 Debug/Info/Warn/Error 写日志；
//   - 级别过滤在写入侧生效，低于阈值时直接返回，不产生任何 IO；
//   - message 与 attributes 在写入前统一脱敏，业务调用方不需要自行处理；
//   - 任何失败都不回传错误：日志是辅助设施，绝不能让主业务因日志而失败。
package logging

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync/atomic"
	"time"

	"example.com/ninevoxel/launcher/internal/platform"
)

// Logger 是结构化日志的写入入口。
//
// 一个进程通常只持有一个 Logger（见 Default）；测试或其他模块可以创建独立实例，
// 各自指向不同的目录，互不影响。
type Logger struct {
	// w 为底层写入器；nil 表示已禁用文件写入。
	w *writer

	// component 为本 Logger 的默认组件名，写入时自动填入。
	component string

	// threshold 为当前生效的最低写入级别，可在运行时调整（atomic 存储）。
	threshold atomic.Value
}

// Default 返回进程级共享的 Logger。
//
// 首次调用时按 platform.LogDirectory 解析目录并建好写入器；目录不可写时
// 该 Logger 仍可用，只是只输出到 stderr（Status 会报告 enabled=false）。
// 并发调用是安全的。
var Default = newDefaultLogger()

// newDefaultLogger 构造进程级默认 Logger。
//
// 目录来源由 platform.LogDirectory 决定，本包不重复实现目录探测逻辑。
// 级别取 defaultLevel（info）；如需 debug，调用 SetLevel。
func newDefaultLogger() *Logger {
	dir := platform.LogDirectory()
	return newLogger(dir, defaultLevel, "app")
}

// resolveDirectoryKind 判断日志目录来自哪一条回退链，返回用于状态展示的类别。
//
// 目录来源的判断归 platform 所有：只有它同时知道各候选基目录与平台差异
// （macOS 用 Application Support、Windows 用 %LocalAppData%），
// 本模块若自行拼路径比较，会在平台策略变化后出现误报。
// 该函数只做路径比较，不重新探测可写性：实际可写性已由 platform 保证。
func resolveDirectoryKind(dir string) string {
	if dir == "" {
		return DirectoryKindUnavailable
	}

	if kind := platform.LogDirectoryKind(dir); kind != "" {
		return kind
	}
	return DirectoryKindExeDir
}

// newLogger 按目录与级别构造 Logger。
//
// 参数 dir 为日志目录（空表示不可写，进入仅 stderr 模式）；
// level 为最低写入级别；component 为默认组件名。
// 目录类别不在此处固定：Status 每次查询时按当前目录重新判定，
// 避免目录在运行期切换（例如用户移动程序目录）后状态长期失真。
func newLogger(dir string, level Level, component string) *Logger {
	logger := &Logger{
		w:         newWriter(dir, level),
		component: component,
	}
	logger.threshold.Store(level)
	return logger
}

// SetLevel 运行时调整最低写入级别。
//
// 参数 level 为新的阈值；非法值回退到 info（见 ParseLevel）。
// 该操作只影响写入侧过滤，不改变已写入内容。
func (l *Logger) SetLevel(level Level) {
	if l == nil {
		return
	}
	normalized := ParseLevel(string(level))
	l.threshold.Store(normalized)
	l.w.mu.Lock()
	l.w.threshold = normalized
	l.w.mu.Unlock()
}

// Level 返回当前生效的最低写入级别。
func (l *Logger) Level() Level {
	if l == nil {
		return defaultLevel
	}
	stored, ok := l.threshold.Load().(Level)
	if !ok {
		return defaultLevel
	}
	return stored
}

// WithComponent 返回一个带指定组件名的派生 Logger。
//
// 派生实例与原实例共享同一个底层写入器（因而共享文件句柄与轮转计数），
// 只替换 component 字段。这样各模块可以按组件名分组，而不会重复打开日志文件。
// 参数 component 为组件名；为空时保留原组件名。
func (l *Logger) WithComponent(component string) *Logger {
	if l == nil {
		return nil
	}
	if component == "" {
		return l
	}
	return &Logger{
		w:         l.w,
		component: component,
		threshold: l.threshold,
	}
}

// Status 返回日志目录的可读状态。
//
// 返回的 DirectoryStatus 只含类别与统计值，不含绝对路径（接口冻结清单 §8.5）。
func (l *Logger) Status() DirectoryStatus {
	if l == nil || l.w == nil {
		return DirectoryStatus{
			Enabled:       false,
			DirectoryKind: DirectoryKindUnavailable,
		}
	}
	return describeDirectory(l.w.dir, resolveDirectoryKind(l.w.dir))
}

// Fields 汇总一次日志调用的可选字段。
//
// 使用结构体而不是变长参数：字段集合固定，编译期即可发现拼写错误，
// 也避免调用处出现一堆难以核对的键值对。
type Fields struct {
	// Event 为结构化事件名，建议使用 domain.resource.action.v1 风格。
	Event string

	// CorrelationID 为关联一次用户操作的追踪 ID。
	CorrelationID string

	// OperationID 为一次操作或长任务的 ID。
	OperationID string

	// ErrorCode 为稳定错误码，成功路径留空。
	ErrorCode string

	// Attributes 为扩展信息；键名命中敏感词时值会被整体替换为 ***。
	Attributes map[string]string
}

// Debug 写入一条 debug 级别日志。
//
// 默认不生效：只有 SetLevel(LevelDebug) 之后才会真正落盘。
func (l *Logger) Debug(message string, fields Fields) {
	l.log(LevelDebug, message, fields)
}

// Info 写入一条 info 级别日志（默认阈值，正常运行动作）。
func (l *Logger) Info(message string, fields Fields) {
	l.log(LevelInfo, message, fields)
}

// Warn 写入一条 warn 级别日志（功能降级但仍可继续）。
func (l *Logger) Warn(message string, fields Fields) {
	l.log(LevelWarn, message, fields)
}

// Error 写入一条 error 级别日志（操作失败，需要关注）。
func (l *Logger) Error(message string, fields Fields) {
	l.log(LevelError, message, fields)
}

// log 是四个级别方法的公共实现。
//
// 流程：级别过滤 → 组装 Record → message 与 attributes 脱敏 → 交给写入器。
// 该函数不返回错误：任何失败都已在写入器内部降级为 stderr 提示。
func (l *Logger) log(level Level, message string, fields Fields) {
	if l == nil || l.w == nil {
		return
	}
	if !level.Enabled(l.Level()) {
		return
	}

	record := &Record{
		Timestamp:     formatTimestamp(time.Now()),
		Level:         string(ParseLevel(string(level))),
		Component:     l.component,
		Event:         Redact(fields.Event),
		CorrelationID: Redact(fields.CorrelationID),
		OperationID:   Redact(fields.OperationID),
		ErrorCode:     Redact(fields.ErrorCode),
		Message:       Redact(message),
	}

	// 敏感键的值整体替换、其余值按文本脱敏；nil 时保持 nil（JSON 中省略）。
	record.Attributes = RedactAttributes(fields.Attributes)

	// 兜底归一化：保证 timestamp / level / component / message 四个必填字段存在。
	record.normalize(formatTimestamp(time.Now()))

	// 目录不可用时仍要输出到 stderr，否则用户彻底看不到日志。
	if !l.w.Enabled() {
		writeToStderr(record)
		return
	}

	l.w.write(record)
}

// writeToStderr 在日志目录不可用时把记录输出到标准错误。
//
// 格式与文件日志保持一致（单行带字段前缀），便于用户从控制台复制内容排查；
// 不输出完整 JSON 是为了控制台可读性，文件日志才是机器可读的 JSON Lines。
func writeToStderr(record *Record) {
	if record == nil {
		return
	}
	fmt.Fprintf(os.Stderr, "%s [%s] %s: %s\n", record.Timestamp, record.Level, record.Component, record.Message)
}

// marshalRecord 把记录序列化为单行 JSON。
//
// 使用标准库 encoding/json：字段集合固定且无嵌套结构，无需额外的自定义编码器。
// 序列化失败时返回错误，由调用方降级处理（不 panic）。
func marshalRecord(record *Record) ([]byte, error) {
	if record == nil {
		return nil, errors.New("logging: 记录为空")
	}

	line, err := json.Marshal(record)
	if err != nil {
		return nil, fmt.Errorf("logging: 序列化日志记录失败: %w", err)
	}
	return line, nil
}

// formatTimestamp 把时间格式化为 UTC RFC3339 毫秒精度。
//
// 统一使用 UTC：跨时区排查时所有日志可直接比较，前端再按本地时区展示。
func formatTimestamp(t time.Time) string {
	return t.UTC().Format(entryTimestampLayout)
}

// RedactMessage 供其他模块直接调用 message 脱敏。
//
// 提供该导出的薄封装，避免其他模块自行实现一套脱敏规则而出现不一致。
func RedactMessage(message string) string {
	return Redact(message)
}
