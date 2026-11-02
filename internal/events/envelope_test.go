package events

import (
	"errors"
	"strings"
	"testing"
)

// TestValidateEventNameAcceptsFrozenNames 验证冻结的两个事件名能通过校验。
//
// 这两个名字来自接口冻结清单 §2.2；若校验规则把合法名拒掉，
// 本卡的所有发射调用都会失败，因此这是最先要守住的用例。
func TestValidateEventNameAcceptsFrozenNames(t *testing.T) {
	cases := []string{
		EventAppLifecycleReady,
		EventSettingsChanged,
		"download.task.started.v1",
		"plugin.registry.updated.v2",
	}

	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			if err := ValidateEventName(name); err != nil {
				t.Fatalf("ValidateEventName(%q) 期望通过，实际返回错误: %v", name, err)
			}
		})
	}
}

// TestValidateEventNameRejectsInvalid 验证各类非法事件名都被拒绝。
//
// 每个子用例对应规范中的一条具体约束：段数、字符集、版本后缀。
func TestValidateEventNameRejectsInvalid(t *testing.T) {
	cases := []struct {
		reason string
		name   string
	}{
		{"空名", ""},
		{"P0 旧名冒号分隔", "app:runtime"},
		{"段数不足", "app.v1"},
		{"段中有大写", "App.lifecycle.ready.v1"},
		{"段中有连字符", "app-lifecycle.ready.v1"},
		{"段中有下划线", "app.lifecycle.ready_v1.v1"},
		{"版本段无 v 前缀", "app.lifecycle.ready.1"},
		{"版本段只有 v", "app.lifecycle.ready.v"},
		{"版本段含字母", "app.lifecycle.ready.vx"},
		{"版本段大写 V", "app.lifecycle.ready.V1"},
		{"空段", "app..ready.v1"},
		{"段含中文", "app.生命周期.ready.v1"},
		{"超长", strings.Repeat("a", 60) + ".lifecycle.ready.v1"},
	}

	for _, tc := range cases {
		t.Run(tc.reason, func(t *testing.T) {
			err := ValidateEventName(tc.name)
			if err == nil {
				t.Fatalf("ValidateEventName(%q) 期望返回错误，实际通过", tc.name)
			}
			// 所有失败都必须可被 errors.Is 识别，便于调用方区分「名字不合法」与其它错误。
			if !errors.Is(err, ErrInvalidEventName) {
				t.Fatalf("ValidateEventName(%q) 的错误未包装 ErrInvalidEventName: %v", tc.name, err)
			}
		})
	}
}

// TestEmitRejectsInvalidName 验证非法事件名在发射前被拦截。
//
// 关键断言：发射器未绑定应用实例时，**非法名**应优先报「名字非法」，
// 而不是报「未绑定」——这说明校验发生在任何副作用之前。
func TestEmitRejectsInvalidName(t *testing.T) {
	emitter := NewEmitter()

	_, err := Emit(emitter, "app:runtime", AppLifecycleReadyPayload{AppVersion: "0.1.0"})
	if err == nil {
		t.Fatal("非法事件名期望返回错误，实际通过")
	}
	if !errors.Is(err, ErrInvalidEventName) {
		t.Fatalf("期望 ErrInvalidEventName，实际: %v", err)
	}
}

// TestEmitWithoutBoundAppReturnsError 验证未绑定应用实例时返回错误而不静默丢弃。
//
// 静默丢弃会让调用方误判为「已发送」，进而让前端等待一个永远不会到的事件。
func TestEmitWithoutBoundAppReturnsError(t *testing.T) {
	emitter := NewEmitter()

	_, err := Emit(emitter, EventAppLifecycleReady, AppLifecycleReadyPayload{AppVersion: "0.1.0"})
	if err == nil {
		t.Fatal("未绑定应用实例时期望返回错误，实际通过")
	}
	if errors.Is(err, ErrInvalidEventName) {
		t.Fatalf("错误不应被误判为名字非法: %v", err)
	}
}

// TestEmitNilEmitterReturnsError 验证 nil 发射器不会 panic。
//
// 装配缺省时 emitter 可能为 nil，此时应返回错误而不是空指针崩溃。
func TestEmitNilEmitterReturnsError(t *testing.T) {
	_, err := Emit[AppLifecycleReadyPayload](nil, EventAppLifecycleReady, AppLifecycleReadyPayload{})
	if err == nil {
		t.Fatal("nil 发射器期望返回错误，实际通过")
	}
}

// TestNewUUIDv4Format 验证生成的 UUID 形如 v4 UUID（36 字符、连字符位置、版本位）。
func TestNewUUIDv4Format(t *testing.T) {
	const samples = 100

	seen := make(map[string]bool, samples)
	for i := 0; i < samples; i++ {
		id, err := NewEventID()
		if err != nil {
			t.Fatalf("生成事件 ID 失败: %v", err)
		}
		if len(id) != 36 {
			t.Fatalf("UUID 长度期望 36，实际 %d: %q", len(id), id)
		}
		// 连字符位置固定：8-4-4-4-12。
		for _, index := range []int{8, 13, 18, 23} {
			if id[index] != '-' {
				t.Fatalf("UUID %q 在位置 %d 期望连字符，实际 %q", id, index, id[index])
			}
		}
		// 版本位必须是 4（UUID v4）。
		if id[14] != '4' {
			t.Fatalf("UUID %q 的版本位期望 '4'，实际 %q", id, id[14])
		}
		// 变体位必须是 8/9/a/b 之一（RFC 4122）。
		if !strings.ContainsRune("89ab", rune(id[19])) {
			t.Fatalf("UUID %q 的变体位期望 8/9/a/b，实际 %q", id, id[19])
		}
		if seen[id] {
			t.Fatalf("UUID %q 重复，随机性不足", id)
		}
		seen[id] = true
	}
}

// TestWithCorrelationPreservesCorrelationID 验证派生发射器沿用指定关联 ID。
//
// 该用例只验证派生语义，不依赖 WiAS 应用实例：通过检查 EmitWithCorrelation
// 在未绑定应用时返回错误、但错误信息提到的是同一条事件名来间接确认参数被吃透。
func TestWithCorrelationReturnsEmitter(t *testing.T) {
	base := NewEmitter()
	derived := base.WithCorrelation("corr-123")
	if derived == nil {
		t.Fatal("WithCorrelation 返回 nil")
	}
	if derived.correlationID != "corr-123" {
		t.Fatalf("派生发射器关联 ID 期望 corr-123，实际 %q", derived.correlationID)
	}
	// 派生自 nil 发射器时应返回 nil 而不是 panic。
	if nilEmitter := (*Emitter)(nil).WithCorrelation("corr-456"); nilEmitter != nil {
		t.Fatalf("从 nil 派生应返回 nil，实际 %v", nilEmitter)
	}
}

// TestSchemaVersionIsOne 验证 envelope 结构版本固定为 1。
//
// 该常量与前端 `types/events.ts` 的 `schemaVersion: 1` 是对端契约，
// 变更必须先更新接口冻结清单。
func TestSchemaVersionIsOne(t *testing.T) {
	if SchemaVersion != 1 {
		t.Fatalf("SchemaVersion 期望 1，实际 %d", SchemaVersion)
	}
}

// TestDataDirectoryStatusConstants 验证数据目录状态取值是可枚举的稳定字符串。
//
// 这些值会进入事件载荷与前端展示；不允许出现路径形态的值。
func TestDataDirectoryStatusConstants(t *testing.T) {
	values := []string{
		DataDirectoryStatusOK,
		DataDirectoryStatusReadOnly,
		DataDirectoryStatusUnavailable,
	}

	for _, value := range values {
		if value == "" {
			t.Fatal("数据目录状态常量不能为空")
		}
		// 不得包含路径分隔符：本字段是状态摘要，不是路径。
		if strings.ContainsAny(value, `/\\:`) {
			t.Fatalf("数据目录状态 %q 不应包含路径分隔符", value)
		}
	}
}

// TestEventNameConstantsAreValid 验证集中定义的事件名常量本身符合规范。
//
// 防止「常量写错但没人发现」：常量是唯一出口，它不合法就等于整条链路失效。
func TestEventNameConstantsAreValid(t *testing.T) {
	names := []string{EventAppLifecycleReady, EventSettingsChanged}

	for _, name := range names {
		if err := ValidateEventName(name); err != nil {
			t.Fatalf("事件名常量 %q 未通过规范校验: %v", name, err)
		}
	}
}
