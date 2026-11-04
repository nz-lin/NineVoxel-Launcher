// 本文件实现 Process Supervisor：把 P2-01 ~ P2-07 的能力串成一条可控的启动生命周期。
//
// 职责边界：
//   - 分配与持有 launchId（UUID，只在内存中存在，进程重启后不恢复）；
//   - 维护启动状态机（D-6 批准的 `stopping` 中间态）；
//   - 提供停止能力（D-3 的「优雅优先 + 超时升级」）；
//   - 单一 goroutine 调用 `Cmd.Wait()` 判定退出结局（三类：正常 / 用户停止 / 崩溃）；
//   - 严格按 P2-05 的契约收尾（Wait → WaitReads → 关 Stdin → 释放通道与状态）。
//
// 本包不做的事：不写游戏日志文件（P2-07）、不实现 UI（P2-08）、不做崩溃报告分析（P8）。
//
// **规范要求记在注释里**：UI 点击「停止」≠ 进程已退出。
// 因此存在独立的 `stopping` 状态：点击后立即进入 `stopping`，
// 只有 `Cmd.Wait()` 返回后才进入 `exited`。任何停止路径都**不得**提前置为 `exited`。
package launch

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"example.com/ninevoxel/launcher/internal/events"
	"example.com/ninevoxel/launcher/internal/logging"
	"github.com/wailsapp/wails/v3/pkg/application"
)

// ===== 事件常量与载荷（**定义位置说明**）=====
//
// 任务说明称 `LaunchStateChangedPayload` 已由 P2-07 在 `internal/events/envelope.go`
// 定义、本卡只读使用。实际核验：该文件只有 `EventLaunchLogAppended` 与
// `LaunchLogAppendedPayload`，**不存在** `launch.state.changed.v1` 的常量与载荷。
// 而 `envelope.go` 在本卡的禁止修改清单内，`main.go` 的两行注册又必须能编译。
//
// 因此这两个定义放在本卡授权新增的 `supervisor.go` 里（仍在 `internal/events` 之外，
// 不触碰他人文件），并在交付报告中如实记录该偏差。若后续决定统一迁入 events 包，
// 只需移动这两段并调整 import。

// EventLaunchStateChanged 表示启动状态发生变化。
//
// 命名遵循 `domain.resource.action.v1`；载荷为 `LaunchStateChangedPayload`。
const EventLaunchStateChanged = "launch.state.changed.v1"

// LaunchStateChangedPayload 是 `launch.state.changed.v1` 的载荷。
//
// 字段依据 `接口冻结清单.md` 第 2 节：`launchId`、`state`、`stopReason?`、
// `exitCode?`、`occurredAtMs`。
// **不含 `instanceId`**：P2 无实例语义（实例属 P5，见 C-1），
// 按 Q-1 的结论用 `launchProfileId` 作为标识——而本卡分配的 `launchId`
// 就是 P2 阶段的实际标识，故载荷只带 `launchId`。
type LaunchStateChangedPayload struct {
	// LaunchID 为本次启动标识。
	LaunchID string `json:"launchId"`

	// State 为迁移后的当前状态。
	State string `json:"state"`

	// StopReason 为停止原因；非停止路径为空字符串。
	//
	// 取值：`user_requested` / `app_exiting` / `startup_failed`。
	StopReason string `json:"stopReason,omitempty"`

	// ExitCode 为进程退出码；进程尚未退出或退出码不可用时为 0 且 `HasExitCode` 为 false。
	ExitCode int `json:"exitCode,omitempty"`

	// HasExitCode 区分「退出码 0」与「尚无退出码」。
	//
	// 单靠 `exitCode` 无法表达这个差异（0 既是正常退出也是未退出）。
	HasExitCode bool `json:"hasExitCode"`

	// OccurredAtMs 为状态迁移时间，UTC Unix 毫秒。
	OccurredAtMs int64 `json:"occurredAtMs"`
}

// ===== 状态枚举（与冻结清单第 4.4 节的 TS 联合类型逐字一致）=====

// 启动状态的稳定字符串取值。
const (
	// StatePreparing 表示已受理，正在做启动前准备。
	StatePreparing = "preparing"

	// StateResolvingRuntime 表示正在解析运行时（P2 为瞬时通过态）。
	StateResolvingRuntime = "resolving_runtime"

	// StateBuildingArguments 表示正在构建启动参数。
	StateBuildingArguments = "building_arguments"

	// StateStartingProcess 表示正在创建进程。
	StateStartingProcess = "starting_process"

	// StateRunning 表示进程已创建并运行中。
	StateRunning = "running"

	// StateStopping 表示停止请求已发出、进程仍在收尾（D-6）。
	//
	// 该状态存在的唯一目的：让前端能区分「点了停止」与「已经退出」。
	StateStopping = "stopping"

	// StateExited 表示进程已退出。**只由 `Cmd.Wait()` 返回后写入。**
	StateExited = "exited"

	// StateFailed 表示启动失败或异常终止。
	StateFailed = "failed"

	// StateCancelled 表示启动被取消。
	StateCancelled = "cancelled"
)

// 停止原因取值（冻结清单第 2 节）。
const (
	// StopReasonUserRequested 表示用户主动请求停止。
	StopReasonUserRequested = "user_requested"

	// StopReasonAppExiting 表示应用退出导致停止。
	StopReasonAppExiting = "app_exiting"

	// StopReasonStartupFailed 表示启动失败导致的清理。
	StopReasonStartupFailed = "startup_failed"
)

// 停止结果取值（D-3 / 冻结清单）。
const (
	// StopResultGraceful 表示关闭 stdin 后进程自行退出。
	StopResultGraceful = "graceful"

	// StopResultKilled 表示被强制终止。
	StopResultKilled = "killed"

	// StopResultTimeout 表示优雅等待超时后升级为强制终止。
	StopResultTimeout = "timeout"
)

// 退出结局取值（三类）。
const (
	// OutcomeExitedNormally 表示退出码为 0 且非用户请求停止。
	OutcomeExitedNormally = "exited_normally"

	// OutcomeStoppedByUser 表示用户请求停止后退出。
	OutcomeStoppedByUser = "stopped_by_user"

	// OutcomeCrashed 表示非零退出且非用户请求。
	OutcomeCrashed = "crashed"
)

// 生命周期相关的常量。
const (
	// ErrorCodeLaunchState 表示当前状态不允许该操作。
	//
	// 来源：决策记录第五节冻结表：`LAUNCH_STATE_001`（重复启动、非运行态停止、重复停止），
	// 首个使用卡 P2-06。
	ErrorCodeLaunchState = "LAUNCH_STATE_001"

	// ErrorCodeLaunchRequest 表示启动被拒绝且非状态原因。
	//
	// 来源：决策记录第五节冻结表：`LAUNCH_REQUEST_001`，使用方 P2-06 / P2-08。
	ErrorCodeLaunchRequest = "LAUNCH_REQUEST_001"

	// gracefulStopTimeout 是优雅停止的等待上限。
	//
	// 取 10 秒（D-3 冻结值）：给 Minecraft 客户端留出关闭存档的时间；
	// 超过则以「世界数据可能未保存」的风险为前提强制终止。
	gracefulStopTimeout = 10 * time.Second

	// finishedRetention 是退出后状态快照的保留时长。
	//
	// 取 5 分钟（卡面建议值）：足够用户查看退出原因，又不会让内存无限累积。
	// 快照清理是显式的（由 GetLaunchStatus 触发），不依赖 GC。
	finishedRetention = 5 * time.Minute
)

// 生命周期相关的文案。
const (
	// messageLaunchAlreadyRunning 表示已有活动启动。
	//
	// D-9 冻结：P2 只允许单个活动启动。
	messageLaunchAlreadyRunning = "已有一个实例在运行，请先停止它再启动新的实例。"

	// messageLaunchNotRunning 表示当前没有可停止的活动启动。
	messageLaunchNotRunning = "当前没有正在运行的启动，无法停止。"

	// messageLaunchAlreadyStopping 表示已在停止中（幂等返回）。
	messageLaunchAlreadyStopping = "该启动已在停止中，请等待退出。"

	// messageLaunchLogChannelFailed 表示日志通道创建失败。
	messageLaunchLogChannelFailed = "游戏日志通道创建失败，已中止启动（避免启动后丢失全部输出）。"

	// messageLaunchBuildPlanFailed 表示参数构建失败。
	messageLaunchBuildPlanFailed = "启动参数构建失败，无法启动游戏。"

	// messageLaunchNotFound 表示指定 launchId 不存在。
	messageLaunchNotFound = "未找到该启动记录，可能已过期清理。"

	// warningStopTimeout 是优雅停止超时后升级为强制终止的提示。
	warningStopTimeout = "等待游戏自行退出超时，已强制结束进程。游戏内未保存的数据可能丢失。"
)

// ===== 状态机 =====

// allowedTransitions 是合法状态迁移表。
//
// 键为当前状态，值为允许到达的下一状态集合。
// **只允许前进，不允许跳级或回退**（卡面第 2 条）；非法迁移返回 `LAUNCH_STATE_001`。
//
// 说明：`preparing → resolving_runtime → building_arguments → starting_process → running`
// 是正常启动路径；`stopping` 只能从 `running` 进入（D-6）；
// `exited` 只能从 `running` / `stopping` 进入，**且只由 `Cmd.Wait()` 触发**。
var allowedTransitions = map[string][]string{
	StatePreparing:         {StateResolvingRuntime, StateFailed, StateCancelled},
	StateResolvingRuntime:  {StateBuildingArguments, StateFailed, StateCancelled},
	StateBuildingArguments: {StateStartingProcess, StateFailed, StateCancelled},
	StateStartingProcess:   {StateRunning, StateFailed, StateCancelled},
	StateRunning:           {StateStopping, StateExited, StateFailed},
	StateStopping:          {StateExited, StateFailed},
	// 终态：不再允许迁移。
	StateExited:    nil,
	StateFailed:    nil,
	StateCancelled: nil,
}

// isTransitionAllowed 判断从 from 到 to 的迁移是否合法。
//
// 参数 from 为当前状态；to 为目标状态。返回 true 表示允许。
func isTransitionAllowed(from, to string) bool {
	allowed, ok := allowedTransitions[from]
	if !ok {
		// 未知的当前状态：不做乐观放行。
		return false
	}
	for _, candidate := range allowed {
		if candidate == to {
			return true
		}
	}
	return false
}

// launchRecord 是一次启动的完整内存记录。
//
// 该记录是 `GetLaunchStatus` 的**唯一真相源**：事件可能丢失，
// 前端必须以本记录的快照恢复界面。
type launchRecord struct {
	// mu 保护本记录的全部可变字段。
	mu sync.Mutex

	// launchID 为本次启动标识（UUID v4）。
	launchID string

	// state 为当前状态。
	state string

	// stateChangedAtMs 为最近一次状态迁移时间（UTC 毫秒）。
	stateChangedAtMs int64

	// startedAtMs 为进程创建成功时间；尚未创建时 0。
	startedAtMs int64

	// endedAtMs 为进程退出时间；尚未退出时 0。
	endedAtMs int64

	// exitCode 为进程退出码；尚无时 0。
	exitCode int

	// hasExitCode 区分「退出码 0」与「尚无退出码」。
	hasExitCode bool

	// stopReason 为停止原因；非停止路径为空。
	stopReason string

	// stopResult 为停止结果；未执行停止时为空。
	stopResult string

	// outcome 为退出结局；尚未退出时为空。
	outcome string

	// versionID 为本次启动的版本 id（供 UI 展示）。
	versionID string

	// gameDirectory 为游戏根目录（仅供内部日志，不返回前端）。
	gameDirectory string

	// process 为已启动的进程；尚未启动时为 nil。
	process *GameProcess

	// stopRequested 表示已收到停止请求（幂等判定用）。
	stopRequested bool

	// finishedAtMs 为进入终态的时间，用于快照过期清理。
	finishedAtMs int64
}

// supervisor 是进程内唯一的启动管理器。
//
// D-9 冻结「只允许单个活动启动」，因此这里只保存一个活动记录 + 一个最近的终态记录。
type supervisor struct {
	// mu 保护 active 与 lastFinished。
	mu sync.Mutex

	// active 为当前活动启动记录；无活动时为 nil。
	active *launchRecord

	// lastFinished 为最近一条终态记录，供 `GetLaunchStatus` 在退出后查询。
	lastFinished *launchRecord

	// emitter 为事件发射器（可为 nil：为 nil 时只维护状态、不发事件）。
	emitter *events.Emitter
}

// globalSupervisor 是进程级 supervisor 单例。
var globalSupervisor = &supervisor{}

// BindSupervisorEmitter 把事件发射器绑定给 supervisor。
//
// 供装配层在应用实例就绪后调用；参数为 nil 时视为解绑（此后不发事件）。
func BindSupervisorEmitter(emitter *events.Emitter) {
	globalSupervisor.mu.Lock()
	globalSupervisor.emitter = emitter
	globalSupervisor.mu.Unlock()
}

// newLaunchID 生成一个 launchId（UUID v4）。
//
// 复用 `events.NewEventID`：同一套 UUID v4 实现，避免两份随机逻辑。
func newLaunchID() (string, error) {
	return events.NewEventID()
}

// transitionLocked 在持有记录锁的前提下执行一次状态迁移。
//
// 调用前提：已持有 record.mu。本函数**先写状态记录、再发事件**（卡面第 3 条）：
// 事件发射失败不回滚状态，只记 warn（状态以快照为准）。
//
// 参数 record 为启动记录；to 为目标状态；stopReason 为停止原因（可空）。
// 返回 `*Error`（非法迁移时 `LAUNCH_STATE_001`）或 nil。
func (s *supervisor) transitionLocked(record *launchRecord, to string, stopReason string) *Error {
	if !isTransitionAllowed(record.state, to) {
		// 非法迁移：记录并返回错误，不 panic。
		logger().Warn("非法状态迁移被拒绝", logging.Fields{
			Event:     "launch.state.transition.rejected",
			ErrorCode: ErrorCodeLaunchState,
			Attributes: map[string]string{
				"from": record.state,
				"to":   to,
			},
		})
		return newInputError(ErrorCodeLaunchState,
			fmt.Sprintf("当前状态 %s 不允许迁移到 %s。", record.state, to))
	}

	// 第一步：写状态记录（含时间戳与原因）。
	record.state = to
	record.stateChangedAtMs = time.Now().UTC().UnixMilli()
	if stopReason != "" {
		record.stopReason = stopReason
	}

	// 终态：记录完成时间，供快照过期清理。
	switch to {
	case StateExited, StateFailed, StateCancelled:
		record.finishedAtMs = record.stateChangedAtMs
	}

	// 第二步：发事件。失败不回滚（事件可能丢失，状态以快照为准）。
	s.emitStateLocked(record)

	return nil
}

// emitStateLocked 发射一条状态变更事件。
//
// 调用前提：已持有 record.mu。
// 发射失败只记 warn：事件是辅助通知，`GetLaunchStatus` 才是唯一真相源。
func (s *supervisor) emitStateLocked(record *launchRecord) {
	if s.emitter == nil {
		return
	}

	payload := LaunchStateChangedPayload{
		LaunchID:     record.launchID,
		State:        record.state,
		StopReason:   record.stopReason,
		ExitCode:     record.exitCode,
		HasExitCode:  record.hasExitCode,
		OccurredAtMs: record.stateChangedAtMs,
	}

	if _, err := events.Emit(s.emitter, EventLaunchStateChanged, payload); err != nil {
		logger().Warn("启动状态事件发射失败", logging.Fields{
			Event:     "launch.state.emit.failed",
			ErrorCode: ErrorCodeLaunchState,
			Attributes: map[string]string{
				"state": record.state,
			},
		})
	}
}

// snapshotLocked 由记录构造对外快照。
//
// 调用前提：已持有 record.mu。
//
// 返回的结构**不含绝对路径**：`gameDirectory` 字段仅内部使用，不进入快照。
func (r *launchRecord) snapshotLocked() LaunchStatusResult {
	return LaunchStatusResult{
		LaunchID:         r.launchID,
		State:            r.state,
		VersionID:        r.versionID,
		StartedAtMs:      r.startedAtMs,
		EndedAtMs:        r.endedAtMs,
		ExitCode:         r.exitCode,
		HasExitCode:      r.hasExitCode,
		StopReason:       r.stopReason,
		StopResult:       r.stopResult,
		Outcome:          r.outcome,
		StateChangedAtMs: r.stateChangedAtMs,
	}
}

// expireStaleFinished 清理过期的终态记录。
//
// 清理是**显式**的（由 `GetLaunchStatus` 调用），不依赖 GC（卡面第 5 条）。
//
// 调用前提：已持有 s.mu。
func (s *supervisor) expireStaleFinished(now time.Time) {
	if s.lastFinished == nil {
		return
	}

	s.lastFinished.mu.Lock()
	finishedAt := s.lastFinished.finishedAtMs
	s.lastFinished.mu.Unlock()

	if finishedAt == 0 {
		return
	}

	if now.Sub(time.UnixMilli(finishedAt)) >= finishedRetention {
		s.lastFinished = nil
	}
}

// StartLaunchProcess 执行一次完整的启动流程。
//
// 步骤与顺序（顺序本身是正确性要求）：
//  1. 状态 `preparing`：检查是否已有活动启动（D-9 单活动进程）；
//  2. 状态 `resolving_runtime`：P2 为**瞬时通过态**（无账号体系，见卡面第 2 条）；
//  3. 状态 `building_arguments`：构建 `LaunchPlan`；
//  4. **建日志通道**（必须在启动进程之前——P2-05 的 dispatchGameOutput 依赖它，
//     没有通道时所有输出会被静默丢弃）；
//  5. 状态 `starting_process`：调用 `StartGameProcess`；
//  6. 状态 `running`：启动退出监视 goroutine。
//
// 参数 req 为启动请求；返回受理结果（launchId、受理时间、初始状态）。
// 任何一步失败都会把记录置为 `failed` 并返回错误（状态记录保留，供查询失败原因）。
func StartLaunchProcess(req StartLaunchRequest) (LaunchAcceptedResult, *Error) {
	// 第一步：生成 launchId。生成失败（随机源不可用）属于系统错误。
	launchID, err := newLaunchID()
	if err != nil {
		return LaunchAcceptedResult{}, newSystemError(ErrorCodeLaunchRequest,
			"无法生成启动标识，请重试。")
	}

	record := &launchRecord{
		launchID:         launchID,
		state:            StatePreparing,
		stateChangedAtMs: time.Now().UTC().UnixMilli(),
		versionID:        strings.TrimSpace(req.VersionID),
		gameDirectory:    strings.TrimSpace(req.GameDirectory),
	}

	// D-9：只允许单个活动启动。这里在 s.mu 下同时完成「检查 + 占位」，
	// 避免两个并发请求都通过检查。
	globalSupervisor.mu.Lock()
	globalSupervisor.expireStaleFinished(time.Now().UTC())

	if active := globalSupervisor.active; active != nil {
		// 已有活动启动：按当前状态给出精确文案。
		active.mu.Lock()
		state := active.state
		active.mu.Unlock()
		globalSupervisor.mu.Unlock()

		if state == StateStopping {
			// 已在停止中：仍然拒绝新启动（旧进程还没退出），但文案要说清楚。
			return LaunchAcceptedResult{}, newInputError(ErrorCodeLaunchState,
				messageLaunchAlreadyStopping)
		}
		return LaunchAcceptedResult{}, newInputError(ErrorCodeLaunchState,
			messageLaunchAlreadyRunning)
	}

	// 占位：后续步骤都在这个记录上迁移状态。
	globalSupervisor.active = record
	globalSupervisor.mu.Unlock()

	// 记录初始状态的事件：`preparing` 已经是构造时的状态，
	// 但事件必须从第一次迁移开始发。这里直接从 preparing 迁到 resolving_runtime。
	if transErr := transitionAndEmit(record, StateResolvingRuntime, ""); transErr != nil {
		return failLaunch(record, transErr)
	}

	// 第二步：解析运行时与实例配置。
	//
	// 按实例启动时，绝对游戏目录、实例 Java、内存上限与 JVM 参数都在后端解析；
	// 前端只传 instanceId，避免把绝对路径暴露到界面或浏览器存储。
	resolvedReq, resolveErr := resolveStartLaunchRequest(req)
	if resolveErr != nil {
		return failLaunch(record, resolveErr)
	}
	req = resolvedReq
	record.mu.Lock()
	record.versionID = req.VersionID
	record.gameDirectory = req.GameDirectory
	record.mu.Unlock()

	// 第三步：构建启动计划。
	if transErr := transitionAndEmit(record, StateBuildingArguments, ""); transErr != nil {
		return failLaunch(record, transErr)
	}

	plan, planErr := buildPlanForLaunch(req)
	if planErr != nil {
		return failLaunch(record, planErr)
	}

	// 第四步：**建日志通道（必须在启动进程之前）**。
	//
	// P2-05 的 `dispatchGameOutput` 依赖 `CurrentGameLogChannel()`，
	// 没有活动通道时**静默丢弃所有输出**。因此顺序必须是「建通道 → 启动进程」，
	// 且通道不可用时中止启动——带着空通道启动等于让用户事后什么都看不到。
	channel := StartGameLogChannel(launchID, globalSupervisor.currentEmitter())
	if channel == nil || !channel.available() {
		// 通道不可用（日志目录不可写）：中止启动。
		StopGameLogChannel()
		return failLaunch(record, newSystemError(ErrorCodeLaunchLog,
			messageLaunchLogChannelFailed))
	}

	// 第五步：创建进程。
	if transErr := transitionAndEmit(record, StateStartingProcess, ""); transErr != nil {
		StopGameLogChannel()
		return failLaunch(record, transErr)
	}

	process, startErr := StartGameProcess(plan, plan.JavaExecutablePath)
	if startErr != nil {
		StopGameLogChannel()
		return failLaunch(record, startErr)
	}

	// 第六步：进入 running，并启动退出监视。
	record.mu.Lock()
	record.process = process
	record.startedAtMs = process.StartedAtMillis()
	record.mu.Unlock()

	if transErr := transitionAndEmit(record, StateRunning, ""); transErr != nil {
		// 极端情形：进程已起但状态迁移失败。仍要收尾，避免留下孤儿进程。
		go globalSupervisor.finishRecord(record, process, StateStopping, StopReasonStartupFailed, StopResultKilled, true)
		return failLaunch(record, transErr)
	}

	// 退出监视：**单一 goroutine** 调用 Cmd.Wait()（卡面第 5 条）。
	go globalSupervisor.watchProcess(record, process)

	// 返回受理结果。
	record.mu.Lock()
	accepted := LaunchAcceptedResult{
		LaunchID:     record.launchID,
		AcceptedAtMs: record.stateChangedAtMs,
		State:        record.state,
	}
	record.mu.Unlock()

	logger().Info("启动已受理", logging.Fields{
		Event: "launch.start.accepted",
		Attributes: map[string]string{
			"versionId": record.versionID,
			"state":     accepted.State,
		},
	})

	return accepted, nil
}

// failLaunch 把记录置为失败并返回错误。
//
// 参数 record 为启动记录；cause 为失败原因。
//
// 失败记录**保留**在 active 位置（不回退），供 `GetLaunchStatus` 查询失败原因。
// 但进程未启动，因此不会阻塞下一次启动——由 `StopLaunchProcess` 或下一次
// `StartLaunchProcess` 前的清理处理（见 `globalSupervisor.active` 的处理逻辑）。
func failLaunch(record *launchRecord, cause *Error) (LaunchAcceptedResult, *Error) {
	record.mu.Lock()
	// 若当前状态已是终态（例如迁移被拒后又在别处失败），不重复迁移。
	if record.state != StateFailed && record.state != StateCancelled && record.state != StateExited {
		if transErr := globalSupervisor.transitionLocked(record, StateFailed, StopReasonStartupFailed); transErr != nil {
			// 迁移本身失败：直接设置状态，保证记录处于可清理的终态。
			record.state = StateFailed
			record.finishedAtMs = time.Now().UTC().UnixMilli()
		}
	}
	record.mu.Unlock()

	// 释放 active 占位：失败记录移到 lastFinished，允许用户重试。
	globalSupervisor.mu.Lock()
	if globalSupervisor.active == record {
		globalSupervisor.active = nil
		globalSupervisor.lastFinished = record
	}
	globalSupervisor.mu.Unlock()

	return LaunchAcceptedResult{}, cause
}

// watchProcess 监视进程退出（**单一 goroutine** 调用 Wait）。
//
// 参数 record 为启动记录；process 为已启动的进程。
//
// 这是全包**唯一**调用 `Cmd.Wait()` 的地方（卡面第 5 条）：
// 其他路径（停止请求、应用退出）都只发出停止信号，不 Wait。
func (s *supervisor) watchProcess(record *launchRecord, process *GameProcess) {
	if process == nil || process.Cmd == nil {
		return
	}

	// 阻塞等待进程退出。这是唯一允许阻塞的 goroutine。
	waitErr := process.Cmd.Wait()

	// 退出码：正常退出时 `ExitCode()` 有效；被信号终止时在部分平台为 -1。
	// 按实际值记录，不做猜测（卡面第 5 条）。
	exitCode := -1
	if process.Cmd.ProcessState != nil {
		exitCode = process.Cmd.ProcessState.ExitCode()
	}
	_ = waitErr

	record.mu.Lock()
	record.exitCode = exitCode
	record.hasExitCode = true
	record.endedAtMs = time.Now().UTC().UnixMilli()
	stopRequested := record.stopRequested
	record.mu.Unlock()

	// 判定退出结局（卡面第 5 条的三类）。
	outcome := OutcomeExitedNormally
	switch {
	case stopRequested:
		// 用户（或应用退出）请求过停止：无论退出码如何都算「用户停止」。
		outcome = OutcomeStoppedByUser
	case exitCode != 0:
		outcome = OutcomeCrashed
	}

	record.mu.Lock()
	record.outcome = outcome
	record.mu.Unlock()

	// **exited 只在 Wait 返回后写入**（卡面第 4 条的硬约束）。
	// 若当前是 stopping，则 stopping → exited；若当前是 running，则 running → exited。
	record.mu.Lock()
	if isTransitionAllowed(record.state, StateExited) {
		if transErr := s.transitionLocked(record, StateExited, ""); transErr != nil {
			// 极端情形：迁移被拒。直接设置状态，保证记录可达终态。
			record.state = StateExited
			record.finishedAtMs = time.Now().UTC().UnixMilli()
			s.emitStateLocked(record)
		}
	}
	record.mu.Unlock()

	// 收尾：严格按 P2-05 契约。
	s.finalize(record, process)
}

// finalize 按 P2-05 契约收尾。
//
// **顺序不可颠倒**（卡面第 4 条）：
//  1. `Cmd.Wait()` —— 已在 `watchProcess` 中完成（本函数不再调用）；
//  2. `WaitReads()` —— 等两个读取 goroutine 结束，否则尾部日志会丢、goroutine 会泄漏；
//  3. 关闭 `Stdin` —— 释放输入管道；
//  4. 释放通道与记录 —— 关闭日志通道、把记录移到 lastFinished。
//
// 参数 record 为启动记录；process 为已退出的进程。
func (s *supervisor) finalize(record *launchRecord, process *GameProcess) {
	// 第 2 步：等读取 goroutine 结束。这一步在关闭通道之前做，
	// 保证最后几行仍能写进通道与文件。
	if process != nil {
		process.WaitReads()
	}

	// 第 2 步半：删除启动时生成的临时文件（引导器目录、参数文件）。
	//
	// 进程已退出，JVM 不再需要它们；提前删除（进程存活期间）可能让尚未打开
	// 文件的 JVM 读不到参数。
	if process != nil {
		process.CleanupTempFiles()
	}

	// 第 3 步：关闭 stdin 写端。此时进程已退出，关闭不会影响它。
	if process != nil && process.Stdin != nil {
		if closeErr := process.Stdin.Close(); closeErr != nil {
			// 关闭失败只记 warn：不影响进程（已退出），也不影响后续清理。
			logger().Warn("关闭游戏进程 stdin 失败", logging.Fields{
				Event:     "launch.stdin.close.failed",
				ErrorCode: ErrorCodeLaunchLog,
			})
		}
	}

	// 第 4 步：关闭日志通道（会把队列剩余行尽量落盘）。
	StopGameLogChannel()

	// 释放记录：从 active 移到 lastFinished，允许再次启动。
	s.mu.Lock()
	if s.active == record {
		s.active = nil
	}
	s.lastFinished = record
	s.mu.Unlock()

	logger().Info("启动已收尾", logging.Fields{
		Event: "launch.finalized",
		Attributes: map[string]string{
			"state":   record.state,
			"outcome": record.outcome,
		},
	})
}

// finishRecord 是 `finalize` 的同步包装，供 `StartLaunchProcess` 的异常路径使用。
//
// 参数 record 为记录；process 为进程；targetState 为要写入的状态；
// stopReason 为停止原因；stopResult 为停止结果；kill 表示是否需要强制终止。
//
// 该函数由独立 goroutine 调用（避免阻塞受理流程）。
func (s *supervisor) finishRecord(record *launchRecord, process *GameProcess, targetState string, stopReason string, stopResult string, kill bool) {
	if process == nil {
		return
	}

	if kill && process.Process != nil {
		_ = process.Process.Kill()
	}

	record.mu.Lock()
	record.stopResult = stopResult
	record.mu.Unlock()

	s.watchProcess(record, process)
}

// currentEmitter 返回当前绑定的发射器（可能为 nil）。
func (s *supervisor) currentEmitter() *events.Emitter {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.emitter
}

// transitionAndEmit 在持有记录锁的情况下执行一次状态迁移。
//
// 参数 record 为记录；to 为目标状态；stopReason 为停止原因。
// 返回 `*Error` 或 nil。
func transitionAndEmit(record *launchRecord, to string, stopReason string) *Error {
	record.mu.Lock()
	defer record.mu.Unlock()
	return globalSupervisor.transitionLocked(record, to, stopReason)
}

// buildPlanForLaunch 由启动请求构建 LaunchPlan。
//
// 参数 req 为启动请求。
// 返回计划（含 JavaExecutablePath）或错误。
//
// **校验链不可绕过**：javaPath 经 `validateJavaPath`（P2-01 的三层校验：
// 存在性 → 可执行性 → `java -version` 可解析）；gameDirectory / versionId
// 经 P2-03 的版本加载与继承合并，再由 P2-04 的 `buildLaunchPlan` 构建参数。
//
// 为什么复用 `validateJavaPath` 而不是只看「文件是否存在」：
// 启动前必须确认「这个 Java 真能跑起来」，否则进程会以难懂的系统错误失败。
// 代价是每次启动多做一次 `java -version` 探针（约 1 秒），
// 相对「启动后立刻崩溃」是可接受的。
func buildPlanForLaunch(req StartLaunchRequest) (*LaunchPlan, *Error) {
	javaPath := strings.TrimSpace(req.JavaPath)
	if javaPath == "" {
		return nil, newInputError(ErrorCodeLaunchRequest, "请先选择 Java 可执行文件。")
	}

	// P2-01：Java 三层校验。
	javaResult := validateJavaPath(javaPath)
	if !javaResult.Valid {
		// 把 Java 校验的错误码与文案原样透传（用户需要知道是哪个问题）。
		code := javaResult.ErrorCode
		if code == "" {
			code = ErrorCodeLaunchRequest
		}
		return nil, newInputError(code, javaResult.ErrorMessage)
	}

	// 游戏根目录归一化（P2-02）。这是实例的可变状态目录。
	root, _, pathErr := normalizeGameRoot(req.GameDirectory)
	if pathErr != nil {
		return nil, pathErr
	}

	// 共享核心根：版本 JSON、libraries、assets、versions 都在这里。
	//
	// 版本元数据必须从核心根读取：实例目录在共享布局下只剩可变状态，
	// 从实例目录找 versions/<id>/<id>.json 必然落空。
	resourceRoot, rootErr := resolveResourceRoot()
	if rootErr != nil {
		return nil, rootErr
	}

	versionID := strings.TrimSpace(req.VersionID)
	if !isVersionIDValid(versionID) {
		return nil, newInputError(ErrorCodeVersionMetadata, messageVersionIDInvalid)
	}

	// P2-03：加载并合并版本。
	outcome := loadMergedVersion(resourceRoot, versionID)
	if outcome.merged == nil {
		code := outcome.errorCode
		if code == "" {
			code = ErrorCodeVersionMetadata
		}
		return nil, newInputError(code, outcome.reason)
	}

	// P2-04：构建参数。
	//
	// P3 账号接入：先解析所选账号身份，再决定走哪条构建路径。
	// 解析放在 Java 校验与版本加载之后：账号失效不该掩盖「Java / 版本根本不可用」
	// 这类更基础、更省时间的错误；也避免为一次注定失败的启动去刷新凭证。
	identity, identityErr := resolveAccountIdentity(context.Background(), req.AccountID)
	if identityErr != nil {
		// 原样透传 `LAUNCH_ACCOUNT_001`（failLaunch 会记录，文案已脱敏）。
		return nil, identityErr
	}

	var plan *LaunchPlan
	var planErr *Error
	// AccountIdentity 全部字段为字符串，可直接与零值比较；
	// 若将来加入切片/映射字段，这里必须改成显式判断（见 identity.go）。
	if identity == (AccountIdentity{}) {
		// 未选账号：沿用 P2 的离线占位身份（兼容旧调用）。
		plan, planErr = buildLaunchPlan(root, resourceRoot, versionID, outcome.merged,
			req.AdditionalJvmArgs, req.AdditionalGameArgs)
	} else {
		plan, planErr = buildLaunchPlanWithIdentity(root, resourceRoot, versionID, outcome.merged,
			req.AdditionalJvmArgs, req.AdditionalGameArgs, identity)
	}
	if planErr != nil {
		return nil, planErr
	}

	// 计划不可安全启动时拒绝：缺库 / 缺 natives / 有未解析占位符。
	//
	// 为什么在这里拒绝而不是交给进程层：带着缺库的 classpath 启动，
	// 游戏会以 `NoClassDefFoundError` 之类的堆栈失败，用户完全无法据此修正。
	// 提前拒绝并给出 blocking 警告的原因，比事后看崩溃日志更有用。
	if !plan.SafeToLaunch {
		reason := messageLaunchBuildPlanFailed
		for _, warning := range plan.Warnings {
			if warning.Severity == warningSeverityBlocking {
				reason = warning.Message
				break
			}
		}
		if len(plan.UnresolvedPlaceholders) > 0 {
			reason = "启动参数含无法解析的占位符：" + plan.UnresolvedPlaceholders[0].Placeholder
		}
		return nil, newInputError(ErrorCodeLaunchRequest, reason)
	}

	// 记录 Java 路径与主版本供进程层使用（JSON 标签为 `-`，不会外泄）。
	// 主版本用于「命令行超长时能否改用 @argfile」的判断（见 argfile.go）。
	plan.JavaExecutablePath = javaPath
	plan.JavaMajorVersion = javaResult.MajorVersion

	return plan, nil
}

// available 判断日志通道是否具备落盘能力。
//
// 供 supervisor 在启动进程前校验：通道不可用时必须中止启动
// （P2-05 的 `dispatchGameOutput` 在没有活动通道时会静默丢弃所有输出）。
//
// 判定标准：通道非 nil 且日志目录可用（`dir` 非空）。
// 目录不可写时 `gamelog.go` 的 `writeLine` 会静默跳过，但内存缓冲仍可用——
// 因此这里**只**在 dir 为空（两处候选目录都不可写）时才判为不可用。
func (c *gameLogChannel) available() bool {
	return c != nil && c.dir != ""
}

// ===== 对外操作 =====

// StopLaunchProcess 请求停止当前活动的启动。
//
// 参数 req 为停止请求（含 `launchId` 与 `force`）。
// 返回停止后的状态快照。
//
// 行为（D-3 + 卡面第 4 条）：
//   - 状态必须是 `running` 才受理；`stopping` 时幂等返回（不重复终止）；
//   - 其他状态返回 `LAUNCH_STATE_001`；
//   - `force=true` 直接 `Process.Kill()`；否则先关 stdin、等 10 秒、再 Kill。
//
// **任何路径都不提前写 `exited`**：`exited` 只由 `watchProcess` 的 Wait 结果写入。
func StopLaunchProcess(req StopLaunchRequest) (LaunchStatusResult, *Error) {
	record := globalSupervisor.findRecordForStop(req.LaunchID)
	if record == nil {
		return LaunchStatusResult{}, newInputError(ErrorCodeLaunchState, messageLaunchNotRunning)
	}

	record.mu.Lock()
	state := record.state
	process := record.process
	alreadyRequested := record.stopRequested
	record.mu.Unlock()

	// 幂等：已在停止中时不重复终止。
	if state == StateStopping || alreadyRequested {
		record.mu.Lock()
		snapshot := record.snapshotLocked()
		record.mu.Unlock()
		return snapshot, nil
	}

	// 非运行态：返回明确错误。
	if state != StateRunning {
		return LaunchStatusResult{}, newInputError(ErrorCodeLaunchState,
			fmt.Sprintf("当前状态 %s 不可停止。", state))
	}

	// 标记停止请求并发起停止流程。
	record.mu.Lock()
	record.stopRequested = true
	record.stopReason = StopReasonUserRequested
	record.mu.Unlock()

	// running → stopping（D-6）。
	if transErr := transitionAndEmit(record, StateStopping, StopReasonUserRequested); transErr != nil {
		return LaunchStatusResult{}, transErr
	}

	// 停止流程放在独立 goroutine：graceful 最长需要 10 秒，
	// 不能阻塞 Wails 的服务调用（否则 UI 会卡住 10 秒）。
	go globalSupervisor.performStop(record, process, req.Force)

	record.mu.Lock()
	snapshot := record.snapshotLocked()
	record.mu.Unlock()
	return snapshot, nil
}

// performStop 执行停止的实际动作（在独立 goroutine 中）。
//
// 参数 record 为记录；process 为进程；force 表示是否直接强制终止。
//
// 时序（D-3）：
//   - force=true：立即 Kill，stopResult=`killed`；
//   - force=false：先关闭 stdin（游戏可读到 EOF），等待最长 10 秒；
//     期间若进程自行退出则 `graceful`，超时后 Kill 并置 `timeout`。
//
// 注意：本函数**不调用 Wait**（Wait 由 `watchProcess` 独占）；
// 它只发出信号，然后轮询「是否已退出」来判断 graceful/timeout。
func (s *supervisor) performStop(record *launchRecord, process *GameProcess, force bool) {
	if process == nil {
		return
	}

	if force {
		// 强制档：直接终止。
		//
		// **风险必须明示**（D-3）：Windows 无 SIGTERM 语义，`Kill()` 不会让 JVM
		// 执行关闭钩子，Minecraft 可能来不及保存世界数据。
		if process.Process != nil {
			_ = process.Process.Kill()
		}
		record.mu.Lock()
		record.stopResult = StopResultKilled
		record.mu.Unlock()
		logger().Warn("已强制结束游戏进程（可能丢失未保存数据）", logging.Fields{
			Event:     "launch.stop.forced",
			ErrorCode: ErrorCodeLaunchState,
		})
		return
	}

	// 优雅档：先关闭 stdin。
	//
	// Minecraft **服务端**会响应 stdin 上的 `stop` 指令；
	// **客户端**在 1.16+ 不保证对 stdin 关闭有响应（D-3 明确记录了这一限制）。
	// 因此这里只关管道、不写入内容——写入未定义内容的指令可能被游戏误解释。
	if process.Stdin != nil {
		_ = process.Stdin.Close()
	}

	// 等待进程退出：轮询记录里的 endedAtMs（由 watchProcess 写入）。
	deadline := time.Now().Add(gracefulStopTimeout)
	for time.Now().Before(deadline) {
		record.mu.Lock()
		ended := record.endedAtMs != 0
		record.mu.Unlock()
		if ended {
			// 进程已自行退出：优雅成功。
			record.mu.Lock()
			if record.stopResult == "" {
				record.stopResult = StopResultGraceful
			}
			record.mu.Unlock()
			return
		}
		// 轮询间隔：100 毫秒。足够及时，又不会空转 CPU。
		time.Sleep(100 * time.Millisecond)
	}

	// 超时：升级为强制终止。
	if process.Process != nil {
		_ = process.Process.Kill()
	}
	record.mu.Lock()
	record.stopResult = StopResultTimeout
	record.mu.Unlock()

	logger().Warn(warningStopTimeout, logging.Fields{
		Event:     "launch.stop.timeout",
		ErrorCode: ErrorCodeLaunchState,
	})
}

// GetLaunchStatusSnapshot 返回启动状态快照。
//
// 参数 req 为查询请求（`launchId` 可为空）。
// 返回快照；`launchId` 为空时返回当前活动启动或最近的终态记录。
//
// **本方法是唯一真相源**（卡面第 9 条）：事件可能丢失，
// 前端必须能在完全丢失事件后靠它恢复界面。
//
// 快照过期清理在这里显式执行（不依赖 GC）。
func GetLaunchStatusSnapshot(req LaunchStatusRequest) (LaunchStatusResult, *Error) {
	record := globalSupervisor.findRecordForStatus(req.LaunchID)
	if record == nil {
		// 未指定 launchId 且没有任何记录：这是「本次会话还没启动过游戏」的正常状态，
		// 不是错误。返回零值快照，前端按 `state == ""` 归入 idle。
		//
		// 只有**指定了** launchId 却找不到记录时才是真正的 LAUNCH_STATE_001
		// （记录可能已被过期清理）。把「从未启动」也当成错误，会让启动页一打开就
		// 顶着一个错误横幅，用户会被误导。
		if strings.TrimSpace(req.LaunchID) == "" {
			return LaunchStatusResult{}, nil
		}
		return LaunchStatusResult{}, newInputError(ErrorCodeLaunchState, messageLaunchNotFound)
	}

	record.mu.Lock()
	snapshot := record.snapshotLocked()
	record.mu.Unlock()
	return snapshot, nil
}

// findRecordForStatus 按 launchId 查找记录；为空时返回活动或最近的终态记录。
//
// 参数 launchID 为用户指定的标识（可空）。
func (s *supervisor) findRecordForStatus(launchID string) *launchRecord {
	trimmed := strings.TrimSpace(launchID)

	s.mu.Lock()
	defer s.mu.Unlock()

	if trimmed == "" {
		// 空 id：优先返回活动记录；无活动时返回最近的终态记录。
		if s.active != nil {
			return s.active
		}
		s.expireStaleFinished(time.Now().UTC())
		return s.lastFinished
	}

	// 指定 id：先比对活动记录，再比对终态记录。
	if s.active != nil && s.active.launchID == trimmed {
		return s.active
	}
	if s.lastFinished != nil && s.lastFinished.launchID == trimmed {
		s.expireStaleFinished(time.Now().UTC())
		return s.lastFinished
	}
	return nil
}

// findRecordForStop 按 launchId 查找可停止的记录。
//
// 参数 launchID 为用户指定的标识（可空时取活动记录）。
func (s *supervisor) findRecordForStop(launchID string) *launchRecord {
	trimmed := strings.TrimSpace(launchID)

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.active == nil {
		return nil
	}
	if trimmed == "" || s.active.launchID == trimmed {
		return s.active
	}
	return nil
}

// ShutdownActiveLaunch 在应用退出时停掉活动进程。
//
// 供 `internal/app` 的退出钩子调用（挂载点在报告里说明）。
//
// 行为（卡面第 6 条）：先 graceful、超时再 Kill；**不能留下孤儿进程**。
//
// 实现要点：本函数必须在应用退出流程中被**同步等待**完成，
// 否则应用先退出、进程仍在跑，就成了孤儿。因此内部用 `WaitReads` 与
// 终止流程的实际等待来保证「返回时进程已结束或已被杀」。
func ShutdownActiveLaunch() {
	globalSupervisor.mu.Lock()
	record := globalSupervisor.active
	globalSupervisor.mu.Unlock()

	if record == nil {
		return
	}

	record.mu.Lock()
	state := record.state
	process := record.process
	record.stopReason = StopReasonAppExiting
	record.mu.Unlock()

	// 已经是终态或未起进程：无需停止。
	if process == nil || state == StateExited || state == StateFailed || state == StateCancelled {
		return
	}

	// 若还在 running，先进 stopping（记录可见的状态变化）。
	record.mu.Lock()
	if isTransitionAllowed(record.state, StateStopping) {
		_ = globalSupervisor.transitionLocked(record, StateStopping, StopReasonAppExiting)
	}
	record.stopRequested = true
	record.mu.Unlock()

	// 同步执行停止（graceful 优先，超时 Kill）。
	globalSupervisor.performStop(record, process, false)

	// 等进程真正退出：最多再等优雅超时的余量。
	// 应用退出不能无限等待，因此在 gracefulStopTimeout + 2 秒后强制收尾。
	deadline := time.Now().Add(gracefulStopTimeout + 2*time.Second)
	for time.Now().Before(deadline) {
		record.mu.Lock()
		ended := record.endedAtMs != 0
		record.mu.Unlock()
		if ended {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}

	// 仍未退出：最后强制终止一次，避免孤儿进程。
	if process.Process != nil {
		_ = process.Process.Kill()
	}
	logger().Warn("应用退出时强制结束了仍在运行的游戏进程", logging.Fields{
		Event:     "launch.appexit.forced",
		ErrorCode: ErrorCodeLaunchState,
	})
}

// ===== 发射器构造（供装配层使用）=====

// NewSupervisorEmitter 构造一个用于启动生命周期事件的发射器。
//
// 参数 wailsApp 为应用实例；返回已绑定的发射器。
//
// 为什么单独构造而不是复用 `events.Default`：
// 生命周期的关联 ID 语义与日志、设置各自独立；共享发射器会让同一进程内
// 不同模块的事件带上同一个 correlationId，反而破坏排查时的区分度。
// 且三者的 binding 时机不同（本包的发射器在应用创建后才需要）。
func NewSupervisorEmitter(wailsApp *application.App) *events.Emitter {
	emitter := events.NewEmitter()
	if wailsApp != nil {
		emitter.BindApp(wailsApp)
	}
	return emitter
}
