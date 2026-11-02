// Package events 提供 NineVoxel Launcher 的正式事件协议实现。
//
// 本包对应《事件与状态机规范》与 launcher/docs/tasks/P1/P1-06.md：
//   - envelope.go：AppEvent 外壳结构与事件名常量；
//   - emitter.go：发射器，负责生成 eventId / correlationId / occurredAt 并在发射前校验事件名。
//
// 设计前提（规范原文）：**事件可能丢失或重复**。因此本包不提供事件序号、
// 不提供重放，也不把事件当作可靠传输通道；前端一律通过 Binding 查询快照恢复状态，
// 事件只用于推动界面增量更新。这条约束决定了下面所有实现都不需要缓存与重试机制。
//
// 事件名集中为常量：业务代码不得手写事件名字符串字面量，
// 否则一旦改名就会在不同模块间静默漂移，而前端订阅只能通过字符串匹配，无法在编译期发现。
package events

import (
	"errors"
	"fmt"
	"strings"
	"unicode"

	crand "crypto/rand"
	"encoding/hex"

	"time"
)

// SchemaVersion 是 AppEvent 负载结构的当前版本，P1 固定为 1。
//
// 该常量与前端 `types/events.ts` 的 `schemaVersion: 1` 必须保持一致；
// 版本变更属于跨端契约变更，须先更新接口冻结清单。
const SchemaVersion = 1

// 事件名常量：集中定义，禁止业务代码手写字符串字面量。
//
// 命名规则见《事件与状态机规范》：`domain.resource.action.v1`，
// 全小写、点分隔、以版本后缀结尾。
const (
	// EventAppLifecycleReady 表示应用已就绪，进程生命周期内发射一次。
	//
	// 载荷为 AppLifecycleReadyPayload：只含非敏感的应用版本与数据目录状态摘要。
	EventAppLifecycleReady = "app.lifecycle.ready.v1"

	// EventSettingsChanged 表示设置写入成功。
	//
	// 本包只提供事件名与载荷类型，实际发射由 P1-04 在设置落盘成功后调用；
	// 事件必须在写入完成之后发出，避免前端收到「已变更」但磁盘尚未落盘的通知。
	EventSettingsChanged = "settings.changed.v1"
)

// 事件名校验相关的常量。
//
// 段数下限取 3 而不是硬编码 4：接口冻结清单 §2.2 的 `settings.changed.v1`
// 实际只有 3 段（domain=settings，resource=changed，version=v1），
// 而 `app.lifecycle.ready.v1` 是 4 段。规范允许省略 resource 或 action 段，
// 因此校验只要求「至少 3 段 + 版本段合法」，不强制固定段数，
// 否则会把已冻结的 settings.changed.v1 拒掉，直接违反契约。
const (
	// eventNameMinSegmentCount 是事件名的最小段数：至少 domain + 一个中间段 + 版本段。
	eventNameMinSegmentCount = 3

	// eventNameVersionSuffix 是事件名最后一段必须使用的版本前缀。
	eventNameVersionSuffix = "v"

	// eventNameMaxLength 是事件名允许的最大长度，防止异常长名进入日志与界面。
	eventNameMaxLength = 64
)

// ErrInvalidEventName 表示事件名不符合 `domain.resource.action.v1` 规范。
//
// 该错误在发射器中被检查：非法名称必须返回错误且**不发射**，
// 不得静默放行，否则前端会收到无法识别的负载类型。
var ErrInvalidEventName = errors.New("events: 事件名不符合 domain.resource.action.v1 规范")

// AppEvent 是推送给前端的事件外壳。
//
// 字段与前端 `AppEvent<T>` 逐字段一致（`launcher/frontend/src/types/app.ts`）；
// JSON 标签使用 lowerCamelCase，与 Wails 的序列化形式一致。
// 泛型参数 T 为载荷类型：Go 侧发射时由具体载荷结构决定，
// 前端订阅时由 `useEventSubscription<T>` 的调用处决定。
type AppEvent[T any] struct {
	// EventID 为事件唯一标识，UUID 形式，用于去重与日志关联。
	EventID string `json:"eventId"`

	// EventName 为事件名，必须符合 `domain.resource.action.v1` 规范。
	EventName string `json:"eventName"`

	// SchemaVersion 为负载结构版本，P1 固定为 1。
	SchemaVersion int `json:"schemaVersion"`

	// OccurredAt 为事件发生时间，UTC Unix 毫秒。
	OccurredAt int64 `json:"occurredAt"`

	// CorrelationID 为关联一次用户操作的追踪 ID（UUID 形式）。
	CorrelationID string `json:"correlationId"`

	// Payload 为事件负载，禁止包含 Token、密码、未脱敏绝对路径。
	Payload T `json:"payload"`
}

// AppLifecycleReadyPayload 是 `app.lifecycle.ready.v1` 的载荷。
//
// 字段来源：接口冻结清单 §2.2（`appVersion`、`dataDirectoryStatus`）。
// 只含非敏感摘要：不含日志目录、WebView 目录等任何绝对路径。
type AppLifecycleReadyPayload struct {
	// AppVersion 为启动器版本号。
	AppVersion string `json:"appVersion"`

	// DataDirectoryStatus 为数据目录可用状态，取值见 DataDirectoryStatus* 常量。
	DataDirectoryStatus string `json:"dataDirectoryStatus"`
}

// SettingsChangedPayload 是 `settings.changed.v1` 的载荷。
//
// 字段来源：接口冻结清单 §2.2（`keys`、`updatedAt`）。
type SettingsChangedPayload struct {
	// Keys 为本次被修改的设置项 key 列表（白名单内的 key，不含值）。
	Keys []string `json:"keys"`

	// UpdatedAt 为写入完成时间，UTC Unix 毫秒。
	UpdatedAt int64 `json:"updatedAt"`
}

// 数据目录状态的可枚举稳定取值。
//
// 这些值是**状态摘要**而不是路径：前端只需知道目录能否使用，
// 不得把绝对路径带给前端（《错误码与日志规范》禁止未脱敏绝对路径）。
const (
	// DataDirectoryStatusOK 表示数据目录可读写。
	DataDirectoryStatusOK = "ok"

	// DataDirectoryStatusReadOnly 表示数据目录可读但不可写。
	DataDirectoryStatusReadOnly = "read-only"

	// DataDirectoryStatusUnavailable 表示数据目录两者皆无。
	DataDirectoryStatusUnavailable = "unavailable"
)

// ValidateEventName 校验事件名是否符合 `domain.resource.action.v1` 规范。
//
// 校验项（全部必须满足）：
//  1. 长度不超过 eventNameMaxLength，且非空；
//  2. 至少 3 段（以 `.` 分隔）——规范允许省略 resource 或 action 段，
//     已冻结的 `settings.changed.v1` 就是 3 段，`app.lifecycle.ready.v1` 是 4 段；
//  3. 除最后一段外，每段非空，且只含小写字母与数字；
//  4. 最后一段为版本号，形如 `v1`、`v2`，字母 v 后必须至少有一位数字。
//
// 参数 name 为待校验的事件名；校验通过返回 nil，否则返回包装了 ErrInvalidEventName 的错误。
func ValidateEventName(name string) error {
	if name == "" {
		return fmt.Errorf("%w：事件名不能为空", ErrInvalidEventName)
	}
	if len(name) > eventNameMaxLength {
		return fmt.Errorf("%w：事件名长度 %d 超过上限 %d", ErrInvalidEventName, len(name), eventNameMaxLength)
	}

	segments := strings.Split(name, ".")
	if len(segments) < eventNameMinSegmentCount {
		return fmt.Errorf("%w：事件名 %q 至少需要 %d 段（domain…version），实际 %d 段",
			ErrInvalidEventName, name, eventNameMinSegmentCount, len(segments))
	}

	// 除版本段外的每一段：小写字母或数字，不允许下划线、连字符、大写。
	for index := 0; index < len(segments)-1; index++ {
		segment := segments[index]
		if segment == "" {
			return fmt.Errorf("%w：事件名 %q 的第 %d 段为空", ErrInvalidEventName, name, index+1)
		}
		if !isLowerAlphaNumeric(segment) {
			return fmt.Errorf("%w：事件名 %q 的第 %d 段 %q 只能包含小写字母与数字",
				ErrInvalidEventName, name, index+1, segment)
		}
	}

	// 最后一段：版本号，必须是 v + 至少一位数字。
	if err := validateVersionSegment(segments[len(segments)-1], name); err != nil {
		return err
	}

	return nil
}

// validateVersionSegment 校验事件名的版本段是否形如 `v1`。
//
// 参数 segment 为第 4 段文本；参数 name 为完整事件名，仅用于错误信息。
func validateVersionSegment(segment string, name string) error {
	if len(segment) < 2 {
		return fmt.Errorf("%w：事件名 %q 的版本段 %q 至少为 v1", ErrInvalidEventName, name, segment)
	}
	if segment[0] != 'v' {
		return fmt.Errorf("%w：事件名 %q 的版本段 %q 必须以 %q 开头",
			ErrInvalidEventName, name, segment, eventNameVersionSuffix)
	}
	for _, char := range segment[1:] {
		if !unicode.IsDigit(char) {
			return fmt.Errorf("%w：事件名 %q 的版本段 %q 在 v 之后只能包含数字",
				ErrInvalidEventName, name, segment)
		}
	}
	return nil
}

// isLowerAlphaNumeric 判断字符串是否只由小写字母与数字组成。
//
// 使用字节级判断而不是 unicode：事件名只允许 ASCII，
// 任何非 ASCII 字符（包括中文、全角字符）都应被拒绝。
func isLowerAlphaNumeric(value string) bool {
	if value == "" {
		return false
	}
	for index := 0; index < len(value); index++ {
		char := value[index]
		switch {
		case char >= 'a' && char <= 'z':
		case char >= '0' && char <= '9':
		default:
			return false
		}
	}
	return true
}

// NewEventID 生成一个 UUID v4 形式的事件 ID。
//
// 使用 crypto/rand：事件 ID 会出现在日志与界面中，随机性差会让不同事件的
// ID 相互碰撞，导致排查时无法区分两条记录。生成失败时返回错误而不是空字符串，
// 由调用方决定是否放弃本次发射。
func NewEventID() (string, error) {
	return newUUIDv4()
}

// NewCorrelationID 生成一个 UUID v4 形式的关联 ID。
//
// 关联 ID 用于把一次用户操作在前后端串起来：同一操作的多个事件应共享同一个
// correlationId，因此该函数由调用方在操作开始时调用一次并向下传递。
func NewCorrelationID() (string, error) {
	return newUUIDv4()
}

// newUUIDv4 生成符合 RFC 4122 的 UUID v4 字符串（小写、带连字符）。
//
// 实现方式：16 字节随机数，按规范写入版本位（第 7 字节高 4 位为 0x4）
// 与变体位（第 9 字节高 2 位为 0b10），再按 4-2-2-2-6 分段拼接。
func newUUIDv4() (string, error) {
	buffer := make([]byte, 16)
	if _, err := crand.Read(buffer); err != nil {
		return "", fmt.Errorf("events: 生成随机 UUID 失败: %w", err)
	}

	// 版本位：v4。
	buffer[6] = (buffer[6] & 0x0f) | 0x40
	// 变体位：RFC 4122。
	buffer[8] = (buffer[8] & 0x3f) | 0x80

	encoded := hex.EncodeToString(buffer)
	// 按 RFC 4122 的 8-4-4-4-12 分段：16 字节 = 32 个十六进制字符。
	return fmt.Sprintf("%s-%s-%s-%s-%s",
		encoded[0:8], encoded[8:12], encoded[12:16], encoded[16:20], encoded[20:32]), nil
}

// NowMillis 返回当前 UTC Unix 毫秒时间。
//
// 抽成函数便于测试替换，也统一了 occurredAt 的时间基准：
// 全部事件使用 UTC 毫秒，前端按本地时区展示。
func NowMillis() int64 {
	return time.Now().UTC().UnixMilli()
}

// ===== P2-07 游戏日志事件（本卡追加，不修改上方 P1 内容）=====

// EventLaunchLogAppended 表示游戏输出新增了一批行。
//
// 命名遵循 `domain.resource.action.v1`；载荷为 `LaunchLogAppendedPayload`。
//
// **刻意不加序号 `seq`**（决策记录 D-2 与本卡任务说明）：用载荷里的
// `droppedLines` 累计值让前端感知丢失，避免「进程重启后序号从 0 开始」
// 造成的歧义——前端无法判断「序号回退」到底是丢行还是重新启动。
const EventLaunchLogAppended = "launch.log.appended.v1"

// LaunchLogAppendedPayload 是 `launch.log.appended.v1` 的载荷。
//
// 每次发射携带**一批**行（而非单行），避免高频输出造成事件风暴；
// 每批行数由 P2-07 的通道实现限制（当前上限 200 行）。
type LaunchLogAppendedPayload struct {
	// LaunchID 为本次启动的标识，供前端把日志归属到对应的启动。
	LaunchID string `json:"launchId"`

	// Lines 为本批次的新增行（已按 `RedactForDisplay` 脱敏）。
	Lines []GameLogLine `json:"lines"`

	// DroppedLines 为**累计**被丢弃的行数（内存缓冲溢出 + 文件队列溢出）。
	//
	// 用累计值而不是「本批丢弃数」，前端只需展示这一个数值即可知道
	// 「日志不完整」；无需自行累加，也不会因事件丢失而算错。
	DroppedLines int `json:"droppedLines"`
}

// GameLogLine 是游戏输出的一行。
//
// 定义在 events 包而不是 launch 包：`launch` 需要 import `events` 来发射事件，
// 若 events 反过来 import launch 取这个类型就会形成 import 循环。
// 因此共享的行结构由 events 提供，launch 直接引用（见 `gamelog.go`）。
type GameLogLine struct {
	// TimestampMs 为该行被采集到的时间，UTC Unix 毫秒。
	//
	// 这是**采集时间**而不是游戏内部时间：游戏输出本身不带可靠时间戳。
	TimestampMs int64 `json:"timestampMs"`

	// Stream 为输出来源：`stdout` 或 `stderr`。
	//
	// P2 不做语义区分（不把 stderr 当错误），只如实标注来源便于排查。
	Stream string `json:"stream"`

	// Text 为该行**已脱敏**的文本（不含行尾换行符）。
	Text string `json:"text"`
}

// ===== P3-04 下载任务事件（本卡追加，不修改上方内容）=====

// EventDownloadTaskChanged 表示下载任务的状态或进度发生变化。
//
// 命名遵循 `domain.resource.action.v1`（与 `launch.log.appended.v1` 同一约定）。
//
// 与 P3-04 任务卡面的 `download.task.state.v1` + `download.task.progress.v1`
// 两个事件名的差异：本实现按 Leader 冻结的正文使用**单一** `changed` 事件。
// 理由是状态与进度本来就同源（同一次状态推进同时改变两者），拆成两个事件会让
// 前端必须先收到 state 再收到 progress 才能渲染，反而多一次竞态窗口。
const EventDownloadTaskChanged = "download.task.changed.v1"

// DownloadTaskChangedPayload 是 `download.task.changed.v1` 的载荷。
//
// 载荷刻意保持极小（只有四个字段）：下载任务推进频率高（进度节流后仍可达
// 每秒 4 次 × 并发 4），把完整任务快照塞进事件会让 IPC 成为瓶颈。
// 前端收到事件后按 taskId 调 GetDownloadTask 取完整视图。
type DownloadTaskChangedPayload struct {
	// TaskID 为发生变化的任务标识。
	TaskID string `json:"taskId"`

	// State 为变化后的状态（取值见 download 包的状态机）。
	State string `json:"state"`

	// Progress 为 0~1 的进度；总大小未知时为 0。
	Progress float64 `json:"progress"`

	// ErrorCode 为失败时的稳定错误码，其余情况为空。
	ErrorCode string `json:"errorCode,omitempty"`
}
