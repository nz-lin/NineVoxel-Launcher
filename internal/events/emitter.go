// 本文件实现事件发射器。
//
// 发射器的职责边界（刻意保持窄）：
//   - 在**发射前**校验事件名，非法名返回错误且不发射；
//   - 为每条事件生成 eventId、correlationId、occurredAt 与 schemaVersion；
//   - 把 envelope 交给 Wails 的事件总线。
//
// 发射器**不**做重试、不缓存、不排队：规范假设事件可能丢失或重复，
// 可靠性由「前端查快照」承担，而不是由发射器堆砌补发机制。
package events

import (
	"fmt"
	"sync"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// Emitter 把 AppEvent 推送到 Wails 事件总线。
//
// 一个进程持有一个 Emitter（应用级），各模块通过 WithCorrelation 派生带关联 ID
// 的视图，避免在每个调用处重复传参。
//
// 零值不可用：必须由 NewEmitter 构造，否则 app 为 nil 时发射会返回错误而不发出。
//
// 说明：发射入口是包级泛型函数 Emit / EmitWithCorrelation，而不是本类型的方法。
// 原因是 Go 的泛型方法需要 go1.27 及以上的语言版本，而本项目 go.mod 声明为
// go 1.26.0（本卡不允许修改 go.mod）。泛型函数不受该限制，且调用形式同样简洁。
type Emitter struct {
	// app 为 Wails 应用实例，通过其 Event.Emit 推送到前端。
	// 该字段在 application.New 之后才能设置，因此提供 BindApp 延迟绑定。
	app *application.App

	// mu 保护 app 字段：绑定发生在启动阶段，发射可能来自其他 goroutine。
	mu sync.RWMutex

	// correlationID 为派生视图默认携带的关联 ID；空表示每次发射单独生成。
	correlationID string
}

// NewEmitter 构造尚未绑定应用实例的发射器。
//
// 之所以不在构造时传入 application.App：应用实例由 application.New 创建，
// 而服务与发射器需要在它之前构造（服务要先加入 Options.Services）。
// 因此装配分两步：先构造发射器，再在应用实例就绪后调用 BindApp。
func NewEmitter() *Emitter {
	return &Emitter{}
}

// BindApp 记录 Wails 应用实例。
//
// 参数 wailsApp 为 application.New 的返回值。传入 nil 时视为解绑，
// 后续发射会返回错误（不影响业务流程）。
func (e *Emitter) BindApp(wailsApp *application.App) {
	if e == nil {
		return
	}
	e.mu.Lock()
	e.app = wailsApp
	e.mu.Unlock()
}

// WithCorrelation 返回一个携带固定关联 ID 的派生 Emitter。
//
// 派生实例与原实例共享同一个应用实例，只替换默认的 correlationId：
// 这样一次用户操作的多个事件可以使用同一关联 ID，便于前后端对齐排查。
// 参数 correlationID 为空时退化为「每次发射生成新 ID」。
func (e *Emitter) WithCorrelation(correlationID string) *Emitter {
	if e == nil {
		return nil
	}

	// 读取应用实例需要读锁：派生本身不改变绑定，只复制引用。
	e.mu.RLock()
	wailsApp := e.app
	e.mu.RUnlock()

	return &Emitter{
		app:           wailsApp,
		correlationID: correlationID,
	}
}

// appRef 返回当前绑定的应用实例，未绑定时为 nil。
//
// 读取路径统一走本方法，保证所有调用点都持有读锁。
func (e *Emitter) appRef() *application.App {
	if e == nil {
		return nil
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.app
}

// Emit 校验事件名并发射一条事件。
//
// 参数 emitter 为发射器（可为 nil，此时返回错误而不是 panic，便于装配缺省时安全降级）；
// eventName 必须符合 `domain.resource.action.v1`；payload 为载荷值。
// 行为：
//   - 事件名非法 → 返回包装 ErrInvalidEventName 的错误，**不发射**；
//   - 发射器未初始化或未绑定应用实例 → 返回错误，同样不发射（避免静默丢失）；
//   - ID 生成失败 → 返回错误，不发射；
//   - 成功 → 返回构造好的 envelope，调用方可用它写日志或返回给调用者。
//
// 返回生成的事件而不是仅返回错误：调用方需要 eventId 与前端收到的记录对照，
// 避免再查一次事件总线。
func Emit[T any](emitter *Emitter, eventName string, payload T) (AppEvent[T], error) {
	var envelope AppEvent[T]

	// 事件名校验必须先于任何副作用：非法名不得触发 ID 生成与总线调用。
	if err := ValidateEventName(eventName); err != nil {
		return envelope, err
	}

	if emitter == nil {
		return envelope, fmt.Errorf("events: 发射器未初始化，事件 %q 未发送", eventName)
	}

	wailsApp := emitter.appRef()
	if wailsApp == nil {
		// 应用实例尚未就绪时不能吞掉错误：静默跳过会让调用方误判为「已发送」。
		return envelope, fmt.Errorf("events: 发射器尚未绑定应用实例，事件 %q 未发送", eventName)
	}

	eventID, err := NewEventID()
	if err != nil {
		return envelope, err
	}

	correlationID := emitter.correlationID
	if correlationID == "" {
		// 未指定关联 ID 时每次单独生成：保证每条事件都可用于排查。
		correlationID, err = NewCorrelationID()
		if err != nil {
			return envelope, err
		}
	}

	envelope = AppEvent[T]{
		EventID:       eventID,
		EventName:     eventName,
		SchemaVersion: SchemaVersion,
		OccurredAt:    NowMillis(),
		CorrelationID: correlationID,
		Payload:       payload,
	}

	// 交给 Wails 的事件总线：载荷类型须由调用方在 main/init 中注册（见 RegisterPayloads），
	// 若类型不匹配 Wails 会记录警告但不会中断进程。
	wailsApp.Event.Emit(eventName, envelope)

	return envelope, nil
}

// EmitWithCorrelation 使用指定关联 ID 发射一条事件，不改动发射器自身的默认值。
//
// 适用场景：一次操作内需要与已生成的关联 ID 对齐，但不想派生新 Emitter。
// 参数 correlationID 为空时等价于 Emit（每次生成新 ID）。
func EmitWithCorrelation[T any](emitter *Emitter, eventName string, payload T, correlationID string) (AppEvent[T], error) {
	if correlationID == "" {
		return Emit(emitter, eventName, payload)
	}

	derived := emitter.WithCorrelation(correlationID)
	return Emit(derived, eventName, payload)
}
