// 本文件覆盖 P2-06「Process Supervisor 与启动生命周期」的工程检查用例。
//
// 测试策略（**严禁启动真实 Minecraft / Java**）：
//   - 所有进程都是「测试二进制自身再执行一次」得到的假进程替身（Go test helper 进程），
//     通过环境变量 NV_SUPERVISOR_HELPER_MODE 选择行为；
//   - 因此本文件不会启动 Minecraft、Java 或任何外部程序，也不会写入游戏目录；
//   - 日志通道在本文件中始终为空：`StopGameLogChannel()` 在无通道时是 no-op，
//     所以 `finalize` 的收尾路径可安全走通而不产生 game-*.log。
//
// 已知不可观测项：`launch.state.changed.v1` 的真实发射需要真实的 `*application.App`
// （`events.Emit` 在未 BindApp 时直接返回错误、不发事件），因此本文件只断言
// 「发射失败不回滚状态」与“先写状态、后发事件”的代码结构，详见交付报告的「遗留问题」。
package launch

import (
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"example.com/ninevoxel/launcher/internal/events"
)

// 假进程替身使用的环境变量名与行为模式。
const (
	// helperModeEnv 是传给子进程的模式变量名。
	helperModeEnv = "NV_SUPERVISOR_HELPER_MODE"

	// helperModeExit0 表示子进程立即以退出码 0 结束（模拟游戏正常结束）。
	helperModeExit0 = "exit0"

	// helperModeExit3 表示子进程立即以退出码 3 结束（模拟游戏崩溃）。
	helperModeExit3 = "exit3"

	// helperModeWaitStdin 表示子进程读到 stdin EOF 后以 0 退出（模拟响应优雅停止）。
	helperModeWaitStdin = "wait-stdin"

	// helperModeHang 表示子进程长时间不退出（模拟不理会优雅停止的游戏）。
	helperModeHang = "hang"
)

// TestSupervisorHelperProcess 是假进程替身的入口。
//
// 父进程中环境变量为空，直接返回（等价于空操作，不影响真实用例结果）；
// 子进程由 newFakeGameProcess 带 NV_SUPERVISOR_HELPER_MODE 启动，行为见上方模式常量。
//
// 注意：这里只做 os.Exit，避免测试框架输出干扰父进程读到的管道内容。
func TestSupervisorHelperProcess(t *testing.T) {
	mode := os.Getenv(helperModeEnv)
	if mode == "" {
		return
	}

	switch mode {
	case helperModeExit0:
		os.Exit(0)
	case helperModeExit3:
		os.Exit(3)
	case helperModeWaitStdin:
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
	case helperModeHang:
		time.Sleep(60 * time.Second)
		os.Exit(0)
	}

	// 未知模式：用独立的退出码暴露配置错误，避免静默通过。
	os.Exit(2)
}

// fakeLaunch 汇总一次假启动的记录、进程与监视线程完成信号。
type fakeLaunch struct {
	// record 是被测的启动记录。
	record *launchRecord

	// process 是假进程替身。
	process *GameProcess

	// done 在 watchProcess 返回后关闭，用于清理时等待 goroutine 结束。
	done chan struct{}
}

// newFakeGameProcess 启动一个假进程替身并包装为 GameProcess。
//
// 参数 t 用于失败即停；mode 为替身行为（见 helperMode* 常量）。
// 返回值与 StartGameProcess 的产物同构，但**绝不启动 Java 或 Minecraft**。
func newFakeGameProcess(t *testing.T, mode string) *GameProcess {
	t.Helper()

	// 再执行测试二进制自身：这是跨平台可控进程的最小代价方案。
	cmd := exec.Command(os.Args[0], "-test.run=TestSupervisorHelperProcess")
	cmd.Env = append(os.Environ(), helperModeEnv+"="+mode)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard

	stdin, pipeErr := cmd.StdinPipe()
	if pipeErr != nil {
		t.Fatalf("创建假进程 stdin 管道失败: %v", pipeErr)
	}
	if startErr := cmd.Start(); startErr != nil {
		t.Fatalf("启动假进程失败: %v", startErr)
	}

	now := time.Now().UTC()
	return &GameProcess{
		Cmd:         cmd,
		Process:     cmd.Process,
		Stdin:       stdin,
		StartedAt:   now,
		startedAtMs: now.UnixMilli(),
	}
}

// resetSupervisorForTest 把全局 supervisor 重置到干净状态，并登记收尾清理。
//
// 由于 supervisor 的状态是进程级全局变量，用例之间必须显式隔离；
// 该清理**最先注册**，因此按 t.Cleanup 的后进先出顺序**最后执行**，
// 保证其它清理（例如强杀假进程）跑完之后再清空全局状态。
func resetSupervisorForTest(t *testing.T) {
	t.Helper()

	clear := func() {
		globalSupervisor.mu.Lock()
		globalSupervisor.active = nil
		globalSupervisor.lastFinished = nil
		globalSupervisor.emitter = nil
		globalSupervisor.mu.Unlock()
		// 无活动通道时是 no-op；这里只做防御性清理。
		StopGameLogChannel()
	}

	t.Cleanup(clear)
	// 立即清理一次：上一个用例（可能失败中断）的残留不应影响本用例的起点。
	clear()
}

// newRunningLaunchForTest 造一个处于 running 的假启动并开始监视。
//
// 状态链通过 transitionAndEmit 逐级推进（preparing → resolving_runtime →
// building_arguments → starting_process → running），因此顺带验证了迁移表；
// 之后把假进程挂到记录上并放进 globalSupervisor.active，模拟 StartLaunchProcess 的收尾。
func newRunningLaunchForTest(t *testing.T, mode string) *fakeLaunch {
	t.Helper()
	resetSupervisorForTest(t)

	launchID, idErr := newLaunchID()
	if idErr != nil {
		t.Fatalf("生成 launchId 失败: %v", idErr)
	}

	record := &launchRecord{
		launchID:         launchID,
		state:            StatePreparing,
		stateChangedAtMs: time.Now().UTC().UnixMilli(),
		versionID:        "1.20.1",
	}
	for _, next := range []string{
		StateResolvingRuntime,
		StateBuildingArguments,
		StateStartingProcess,
		StateRunning,
	} {
		if transErr := transitionAndEmit(record, next, ""); transErr != nil {
			t.Fatalf("迁移到 %s 失败: %s", next, transErr.Message)
		}
	}

	process := newFakeGameProcess(t, mode)
	record.mu.Lock()
	record.process = process
	record.startedAtMs = process.StartedAtMillis()
	record.mu.Unlock()

	globalSupervisor.mu.Lock()
	globalSupervisor.active = record
	globalSupervisor.mu.Unlock()

	fake := &fakeLaunch{record: record, process: process, done: make(chan struct{})}
	go func() {
		defer close(fake.done)
		globalSupervisor.watchProcess(record, process)
	}()

	// 兜底清理：用例失败时也必须让假进程结束，否则监视线程会泄漏到后续用例。
	t.Cleanup(func() {
		if process.Process != nil {
			_ = process.Process.Kill()
		}
		select {
		case <-fake.done:
		case <-time.After(5 * time.Second):
			t.Errorf("监视线程未在 5 秒内结束，可能存在 goroutine 泄漏")
		}
	})

	return fake
}

// recordState 在持锁状态下读取记录的状态。
func recordState(record *launchRecord) string {
	record.mu.Lock()
	defer record.mu.Unlock()
	return record.state
}

// recordStopResult 在持锁状态下读取记录的停止结果。
func recordStopResult(record *launchRecord) string {
	record.mu.Lock()
	defer record.mu.Unlock()
	return record.stopResult
}

// recordStopReason 在持锁状态下读取记录的停止原因。
func recordStopReason(record *launchRecord) string {
	record.mu.Lock()
	defer record.mu.Unlock()
	return record.stopReason
}

// recordOutcome 在持锁状态下读取记录的退出结局。
func recordOutcome(record *launchRecord) string {
	record.mu.Lock()
	defer record.mu.Unlock()
	return record.outcome
}

// recordEndedAtMs 在持锁状态下读取记录的结束时间戳（0 表示仍在运行）。
func recordEndedAtMs(record *launchRecord) int64 {
	record.mu.Lock()
	defer record.mu.Unlock()
	return record.endedAtMs
}

// recordFinishedAtMs 在持锁状态下读取记录的终态时间戳。
func recordFinishedAtMs(record *launchRecord) int64 {
	record.mu.Lock()
	defer record.mu.Unlock()
	return record.finishedAtMs
}

// globalActiveLaunchID 返回当前活动启动的 launchId（无活动时为空串）。
func globalActiveLaunchID() string {
	globalSupervisor.mu.Lock()
	defer globalSupervisor.mu.Unlock()
	if globalSupervisor.active == nil {
		return ""
	}
	return globalSupervisor.active.launchID
}

// waitFor 轮询等待条件成立，超时则让用例失败。
//
// 参数 timeout 为最长等待时间，description 用于失败信息，condition 为待验证条件。
// 采用 10 毫秒间隔轮询：既快又不会空转 CPU。
func waitFor(t *testing.T, timeout time.Duration, description string, condition func() bool) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("等待超时：%s", description)
}

// TestLaunchLifecycleConstantsFrozen 冻结状态、停止原因、结局与超时参数的字面值。
//
// 这些字符串是前端契约（事件载荷与 DTO 共用），且决策记录 D-3/D-6 已批准，
// 任何改动都必须先改文档再改代码，因此这里逐字锁定。
func TestLaunchLifecycleConstantsFrozen(t *testing.T) {
	states := map[string]string{
		StatePreparing:         "preparing",
		StateResolvingRuntime:  "resolving_runtime",
		StateBuildingArguments: "building_arguments",
		StateStartingProcess:   "starting_process",
		StateRunning:           "running",
		StateStopping:          "stopping",
		StateExited:            "exited",
		StateFailed:            "failed",
		StateCancelled:         "cancelled",
	}
	for actual, expected := range states {
		if actual != expected {
			t.Fatalf("状态常量值被改动：期望 %q，实际 %q", expected, actual)
		}
	}

	stopReasons := map[string]string{
		StopReasonUserRequested: "user_requested",
		StopReasonAppExiting:    "app_exiting",
		StopReasonStartupFailed: "startup_failed",
	}
	for actual, expected := range stopReasons {
		if actual != expected {
			t.Fatalf("停止原因常量值被改动：期望 %q，实际 %q", expected, actual)
		}
	}

	stopResults := map[string]string{
		StopResultGraceful: "graceful",
		StopResultKilled:   "killed",
		StopResultTimeout:  "timeout",
	}
	for actual, expected := range stopResults {
		if actual != expected {
			t.Fatalf("停止结果常量值被改动：期望 %q，实际 %q", expected, actual)
		}
	}

	outcomes := map[string]string{
		OutcomeExitedNormally: "exited_normally",
		OutcomeStoppedByUser:  "stopped_by_user",
		OutcomeCrashed:        "crashed",
	}
	for actual, expected := range outcomes {
		if actual != expected {
			t.Fatalf("结局常量值被改动：期望 %q，实际 %q", expected, actual)
		}
	}

	if EventLaunchStateChanged != "launch.state.changed.v1" {
		t.Fatalf("状态事件名被改动：实际 %q", EventLaunchStateChanged)
	}
	if gracefulStopTimeout != 10*time.Second {
		t.Fatalf("优雅停止超时应为 10 秒（D-3），实际 %s", gracefulStopTimeout)
	}
	if finishedRetention != 5*time.Minute {
		t.Fatalf("终态保留时长应为 5 分钟，实际 %s", finishedRetention)
	}
}

// TestLaunchStateChangedPayloadJSONKeys 校验事件载荷的 JSON 键与可选字段行为。
//
// 前端按这些键取值，且载荷**不得**携带 instanceId（卡面明确要求），
// 因此这里同时断言必选键存在、零值可选键被省略、instanceId 不出现。
func TestLaunchStateChangedPayloadJSONKeys(t *testing.T) {
	plain := LaunchStateChangedPayload{
		LaunchID:     "launch-1",
		State:        StateRunning,
		OccurredAtMs: 1700000000000,
	}
	encoded, marshalErr := json.Marshal(plain)
	if marshalErr != nil {
		t.Fatalf("序列化事件载荷失败: %v", marshalErr)
	}

	var decoded map[string]any
	if unmarshalErr := json.Unmarshal(encoded, &decoded); unmarshalErr != nil {
		t.Fatalf("反序列化事件载荷失败: %v", unmarshalErr)
	}

	for _, key := range []string{"launchId", "state", "hasExitCode", "occurredAtMs"} {
		if _, exists := decoded[key]; !exists {
			t.Fatalf("事件载荷缺少必需键 %q，实际内容 %s", key, string(encoded))
		}
	}
	// stopReason / exitCode 为零值时省略：前端据 hasExitCode 判断退出码是否有效。
	for _, key := range []string{"stopReason", "exitCode"} {
		if _, exists := decoded[key]; exists {
			t.Fatalf("零值可选键 %q 不应出现，实际内容 %s", key, string(encoded))
		}
	}
	if _, exists := decoded["instanceId"]; exists {
		t.Fatalf("事件载荷不得携带 instanceId，实际内容 %s", string(encoded))
	}

	stopped := LaunchStateChangedPayload{
		LaunchID:     "launch-1",
		State:        StateExited,
		StopReason:   StopReasonUserRequested,
		ExitCode:     3,
		HasExitCode:  true,
		OccurredAtMs: 1700000001000,
	}
	stoppedJSON, marshalErr := json.Marshal(stopped)
	if marshalErr != nil {
		t.Fatalf("序列化停止载荷失败: %v", marshalErr)
	}
	var stoppedDecoded map[string]any
	if unmarshalErr := json.Unmarshal(stoppedJSON, &stoppedDecoded); unmarshalErr != nil {
		t.Fatalf("反序列化停止载荷失败: %v", unmarshalErr)
	}
	if stoppedDecoded["stopReason"] != StopReasonUserRequested {
		t.Fatalf("stopReason 应透传为 %q，实际 %v", StopReasonUserRequested, stoppedDecoded["stopReason"])
	}
	if stoppedDecoded["exitCode"] != float64(3) {
		t.Fatalf("exitCode 应为 3，实际 %v", stoppedDecoded["exitCode"])
	}
}

// TestTransitionLockedHappyPath 验证正状态链能逐级通过并写入时间戳。
//
// 链条覆盖卡面要求的 preparing → … → running → stopping → exited。
func TestTransitionLockedHappyPath(t *testing.T) {
	resetSupervisorForTest(t)

	record := &launchRecord{
		launchID:         "launch-happy",
		state:            StatePreparing,
		stateChangedAtMs: time.Now().UTC().UnixMilli(),
	}

	steps := []string{
		StateResolvingRuntime,
		StateBuildingArguments,
		StateStartingProcess,
		StateRunning,
		StateStopping,
		StateExited,
	}

	previousMs := record.stateChangedAtMs
	for _, next := range steps {
		if transErr := transitionAndEmit(record, next, ""); transErr != nil {
			t.Fatalf("合法迁移 %s 被拒绝: %s", next, transErr.Message)
		}
		if actual := recordState(record); actual != next {
			t.Fatalf("迁移后状态应为 %q，实际 %q", next, actual)
		}

		record.mu.Lock()
		currentMs := record.stateChangedAtMs
		record.mu.Unlock()
		if currentMs < previousMs {
			t.Fatalf("状态迁移时间戳倒退：%d < %d", currentMs, previousMs)
		}
		previousMs = currentMs
	}

	if finished := recordFinishedAtMs(record); finished == 0 {
		t.Fatalf("进入终态 exited 后应写入 finishedAtMs")
	}
}

// TestIsTransitionAllowedRejectsIllegalMoves 表驱动校验迁移表的拒绝分支。
//
// 覆盖跳级（preparing→running）、回退（running→preparing）、终态出发（exited→running）
// 与未知起始状态，确保非法迁移一律被拒。
func TestIsTransitionAllowedRejectsIllegalMoves(t *testing.T) {
	cases := []struct {
		name string
		from string
		to   string
	}{
		{name: "跳级", from: StatePreparing, to: StateRunning},
		{name: "回退", from: StateRunning, to: StatePreparing},
		{name: "终态不可再出发", from: StateExited, to: StateRunning},
		{name: "跳过 stopping", from: StateStopping, to: StateRunning},
		{name: "failed 不可再出发", from: StateFailed, to: StateRunning},
		{name: "cancelled 不可再出发", from: StateCancelled, to: StateExited},
		{name: "未知起始状态", from: "unknown_state", to: StateRunning},
	}
	for _, testCase := range cases {
		if isTransitionAllowed(testCase.from, testCase.to) {
			t.Fatalf("%s：%s → %s 不应被允许", testCase.name, testCase.from, testCase.to)
		}
	}

	allowed := []struct {
		from string
		to   string
	}{
		{from: StatePreparing, to: StateFailed},
		{from: StatePreparing, to: StateCancelled},
		{from: StateStartingProcess, to: StateRunning},
		{from: StateRunning, to: StateStopping},
		{from: StateRunning, to: StateExited},
		{from: StateRunning, to: StateFailed},
		{from: StateStopping, to: StateExited},
		{from: StateStopping, to: StateFailed},
	}
	for _, pair := range allowed {
		if !isTransitionAllowed(pair.from, pair.to) {
			t.Fatalf("%s → %s 应被允许", pair.from, pair.to)
		}
	}
}

// TestTransitionLockedRejectsIllegalMoveKeepingState 验证非法迁移报 LAUNCH_STATE_001 且不改状态。
func TestTransitionLockedRejectsIllegalMoveKeepingState(t *testing.T) {
	resetSupervisorForTest(t)

	record := &launchRecord{
		launchID:         "launch-illegal",
		state:            StatePreparing,
		stateChangedAtMs: 1700000000000,
	}

	transErr := transitionAndEmit(record, StateRunning, "")
	if transErr == nil {
		t.Fatalf("preparing → running 应被拒绝")
	}
	if transErr.Code != ErrorCodeLaunchState {
		t.Fatalf("非法迁移应返回 %q，实际 %q", ErrorCodeLaunchState, transErr.Code)
	}
	if !strings.Contains(transErr.Message, StatePreparing) || !strings.Contains(transErr.Message, StateRunning) {
		t.Fatalf("错误文案应同时说明源状态与目标状态，实际 %q", transErr.Message)
	}
	if actual := recordState(record); actual != StatePreparing {
		t.Fatalf("被拒绝的迁移不得改变状态，实际 %q", actual)
	}
	if finished := recordFinishedAtMs(record); finished != 0 {
		t.Fatalf("被拒绝的迁移不得写入 finishedAtMs")
	}

	record.mu.Lock()
	unchangedMs := record.stateChangedAtMs
	record.mu.Unlock()
	if unchangedMs != 1700000000000 {
		t.Fatalf("被拒绝的迁移不得刷新 stateChangedAtMs，实际 %d", unchangedMs)
	}
}

// TestTransitionLockedSurvivesEmitterFailure 验证「事件发射失败不回滚状态」。
//
// 用一个未 BindApp 的发射器：`events.Emit` 必然返回错误（不会真的发事件），
// 但迁移必须照常成功——事件只是通知，状态才是真相源。
func TestTransitionLockedSurvivesEmitterFailure(t *testing.T) {
	resetSupervisorForTest(t)

	globalSupervisor.mu.Lock()
	globalSupervisor.emitter = events.NewEmitter()
	globalSupervisor.mu.Unlock()

	record := &launchRecord{
		launchID:         "launch-emitter",
		state:            StatePreparing,
		stateChangedAtMs: time.Now().UTC().UnixMilli(),
	}

	if transErr := transitionAndEmit(record, StateResolvingRuntime, ""); transErr != nil {
		t.Fatalf("发射失败不得影响状态迁移，实际错误: %s", transErr.Message)
	}
	if actual := recordState(record); actual != StateResolvingRuntime {
		t.Fatalf("发射失败后状态仍应前进，实际 %q", actual)
	}
}

// TestStartLaunchProcessRejectsEmptyJavaPath 验证启动前失败落到 failed。
//
// 空 JavaPath 在 buildPlanForLaunch 阶段就被拒（LAUNCH_REQUEST_001），
// 此时记录必须进入 failed + startup_failed，并且不占用活动位（可直接重试）。
func TestStartLaunchProcessRejectsEmptyJavaPath(t *testing.T) {
	resetSupervisorForTest(t)

	accepted, launchErr := StartLaunchProcess(StartLaunchRequest{})
	if launchErr == nil {
		t.Fatalf("空 JavaPath 应被拒绝")
	}
	if launchErr.Code != ErrorCodeLaunchRequest {
		t.Fatalf("应返回 %q，实际 %q", ErrorCodeLaunchRequest, launchErr.Code)
	}
	if accepted.LaunchID != "" {
		t.Fatalf("被拒绝的启动不得返回 launchId，实际 %q", accepted.LaunchID)
	}

	status, statusErr := GetLaunchStatusSnapshot(LaunchStatusRequest{})
	if statusErr != nil {
		t.Fatalf("失败记录应可通过状态查询读取: %s", statusErr.Message)
	}
	if status.State != StateFailed {
		t.Fatalf("启动失败后状态应为 %q，实际 %q", StateFailed, status.State)
	}
	if status.StopReason != StopReasonStartupFailed {
		t.Fatalf("启动失败原因应为 %q，实际 %q", StopReasonStartupFailed, status.StopReason)
	}
	if status.Outcome != "" {
		t.Fatalf("启动失败不应有退出结局，实际 %q", status.Outcome)
	}
	// 进程从未启动，因此 EndedAtMs（进程结束时刻）保持 0；
	// 终态时刻记录在 StateChangedAtMs 上，前端据此展示「启动失败于何时」。
	if status.StateChangedAtMs == 0 {
		t.Fatalf("启动失败应写入状态变更时间")
	}
	if status.EndedAtMs != 0 {
		t.Fatalf("未起进程的启动失败不应有进程结束时间，实际 %d", status.EndedAtMs)
	}
	if active := globalActiveLaunchID(); active != "" {
		t.Fatalf("启动失败不得占用活动位，实际 %q", active)
	}
}

// TestStartLaunchProcessRejectsMissingJavaFile 验证 Java 文件不存在时落到 failed。
//
// 路径指向 t.TempDir() 下不存在的文件：只做路径校验，**不会执行 java -version**，
// 因此该用例仍然不启动任何真实进程。
func TestStartLaunchProcessRejectsMissingJavaFile(t *testing.T) {
	resetSupervisorForTest(t)

	missingJava := filepath.Join(t.TempDir(), "missing-java.exe")
	_, launchErr := StartLaunchProcess(StartLaunchRequest{
		JavaPath:      missingJava,
		GameDirectory: t.TempDir(),
		VersionID:     "1.20.1",
	})
	if launchErr == nil {
		t.Fatalf("不存在的 Java 文件应被拒绝")
	}
	if launchErr.Code != ErrorCodeJavaNotFound {
		t.Fatalf("应返回 %q，实际 %q", ErrorCodeJavaNotFound, launchErr.Code)
	}

	status, statusErr := GetLaunchStatusSnapshot(LaunchStatusRequest{})
	if statusErr != nil {
		t.Fatalf("失败记录应可通过状态查询读取: %s", statusErr.Message)
	}
	if status.State != StateFailed {
		t.Fatalf("启动失败后状态应为 %q，实际 %q", StateFailed, status.State)
	}
	if status.StopReason != StopReasonStartupFailed {
		t.Fatalf("启动失败原因应为 %q，实际 %q", StopReasonStartupFailed, status.StopReason)
	}
}

// TestStartLaunchProcessRejectsSecondActiveLaunch 验证 D-9 单活动进程。
//
// 活动记录处于 running 时报「已有实例在运行」；处于 stopping 时报「正在停止」；
// 两种情况都必须返回 LAUNCH_STATE_001，且不得改动既有记录。
func TestStartLaunchProcessRejectsSecondActiveLaunch(t *testing.T) {
	cases := []struct {
		name         string
		activeState  string
		expectedText string
	}{
		{name: "运行中", activeState: StateRunning, expectedText: messageLaunchAlreadyRunning},
		{name: "停止中", activeState: StateStopping, expectedText: messageLaunchAlreadyStopping},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			resetSupervisorForTest(t)

			existing := &launchRecord{
				launchID: "launch-existing",
				state:    testCase.activeState,
			}
			globalSupervisor.mu.Lock()
			globalSupervisor.active = existing
			globalSupervisor.mu.Unlock()

			accepted, launchErr := StartLaunchProcess(StartLaunchRequest{
				JavaPath:      filepath.Join(t.TempDir(), "missing-java.exe"),
				GameDirectory: t.TempDir(),
				VersionID:     "1.20.1",
			})
			if launchErr == nil {
				t.Fatalf("%s：第二个活动启动应被拒绝", testCase.name)
			}
			if launchErr.Code != ErrorCodeLaunchState {
				t.Fatalf("%s：应返回 %q，实际 %q", testCase.name, ErrorCodeLaunchState, launchErr.Code)
			}
			if launchErr.Message != testCase.expectedText {
				t.Fatalf("%s：错误文案应为 %q，实际 %q", testCase.name, testCase.expectedText, launchErr.Message)
			}
			if accepted.LaunchID != "" {
				t.Fatalf("%s：被拒绝的启动不得返回 launchId", testCase.name)
			}
			if actual := recordState(existing); actual != testCase.activeState {
				t.Fatalf("%s：既有记录状态不得被改动，实际 %q", testCase.name, actual)
			}
			if active := globalActiveLaunchID(); active != "launch-existing" {
				t.Fatalf("%s：活动位仍应是既有启动，实际 %q", testCase.name, active)
			}
		})
	}
}

// TestWatchProcessClassifiesExitedNormally 验证退出码 0 且非用户停止时判为 exited_normally。
func TestWatchProcessClassifiesExitedNormally(t *testing.T) {
	fake := newRunningLaunchForTest(t, helperModeExit0)

	waitFor(t, 5*time.Second, "假进程自行退出后记录进入 exited", func() bool {
		return recordState(fake.record) == StateExited
	})
	<-fake.done

	status, statusErr := GetLaunchStatusSnapshot(LaunchStatusRequest{LaunchID: fake.record.launchID})
	if statusErr != nil {
		t.Fatalf("状态查询失败: %s", statusErr.Message)
	}
	if status.Outcome != OutcomeExitedNormally {
		t.Fatalf("退出码 0 且非用户停止应判为 %q，实际 %q", OutcomeExitedNormally, status.Outcome)
	}
	if !status.HasExitCode {
		t.Fatalf("进程已退出时必须带有效退出码标记")
	}
	if status.ExitCode != 0 {
		t.Fatalf("正常退出的退出码应为 0，实际 %d", status.ExitCode)
	}
	if status.StopReason != "" {
		t.Fatalf("非用户停止不应写停止原因，实际 %q", status.StopReason)
	}
	if status.StopResult != "" {
		t.Fatalf("非用户停止不应写停止结果，实际 %q", status.StopResult)
	}
}

// TestWatchProcessClassifiesCrashed 验证非 0 退出码且非用户停止时判为 crashed。
func TestWatchProcessClassifiesCrashed(t *testing.T) {
	fake := newRunningLaunchForTest(t, helperModeExit3)

	waitFor(t, 5*time.Second, "假进程崩溃后记录进入 exited", func() bool {
		return recordState(fake.record) == StateExited
	})
	<-fake.done

	status, statusErr := GetLaunchStatusSnapshot(LaunchStatusRequest{LaunchID: fake.record.launchID})
	if statusErr != nil {
		t.Fatalf("状态查询失败: %s", statusErr.Message)
	}
	if status.Outcome != OutcomeCrashed {
		t.Fatalf("退出码非 0 且非用户停止应判为 %q，实际 %q", OutcomeCrashed, status.Outcome)
	}
	if !status.HasExitCode || status.ExitCode != 3 {
		t.Fatalf("退出码应为 3 且标记有效，实际 hasExitCode=%v exitCode=%d", status.HasExitCode, status.ExitCode)
	}
	// 崩溃不是「停止」，不得借用错误码表达（决策记录第五节）。
	if status.ErrorCode != "" {
		t.Fatalf("崩溃不得用错误码表达，实际 %q", status.ErrorCode)
	}
}

// TestStopLaunchProcessGraceful 验证优雅停止分支：关 stdin → 进程自行退出 → graceful。
func TestStopLaunchProcessGraceful(t *testing.T) {
	fake := newRunningLaunchForTest(t, helperModeWaitStdin)

	snapshot, launchErr := StopLaunchProcess(StopLaunchRequest{LaunchID: fake.record.launchID})
	if launchErr != nil {
		t.Fatalf("优雅停止请求不应失败: %s", launchErr.Message)
	}
	if snapshot.LaunchID != fake.record.launchID {
		t.Fatalf("快照应携带 launchId %q，实际 %q", fake.record.launchID, snapshot.LaunchID)
	}
	// 竞态安全的不变量：进程尚未结束时状态必须是 stopping（UI 点击 ≠ 进程已退出）。
	if snapshot.EndedAtMs == 0 && snapshot.State != StateStopping {
		t.Fatalf("停止受理后、进程未退出时状态必须是 %q，实际 %q", StateStopping, snapshot.State)
	}

	waitFor(t, 5*time.Second, "关闭 stdin 后假进程自行退出", func() bool {
		return recordEndedAtMs(fake.record) != 0
	})
	waitFor(t, 5*time.Second, "停止结果记为 graceful", func() bool {
		return recordStopResult(fake.record) == StopResultGraceful
	})
	// 收尾在状态迁移之后执行，需要等 finalize 释放活动位。
	waitFor(t, 5*time.Second, "收尾后活动位被释放", func() bool {
		return globalActiveLaunchID() == ""
	})

	status, statusErr := GetLaunchStatusSnapshot(LaunchStatusRequest{LaunchID: fake.record.launchID})
	if statusErr != nil {
		t.Fatalf("状态查询失败: %s", statusErr.Message)
	}
	if status.State != StateExited {
		t.Fatalf("最终状态应为 %q，实际 %q", StateExited, status.State)
	}
	if status.StopReason != StopReasonUserRequested {
		t.Fatalf("停止原因应为 %q，实际 %q", StopReasonUserRequested, status.StopReason)
	}
	if status.StopResult != StopResultGraceful {
		t.Fatalf("停止结果应为 %q，实际 %q", StopResultGraceful, status.StopResult)
	}
	if status.Outcome != OutcomeStoppedByUser {
		t.Fatalf("用户停止的结局应为 %q，实际 %q", OutcomeStoppedByUser, status.Outcome)
	}
	if status.HasExitCode && status.ExitCode != 0 {
		t.Fatalf("正常退出路径的退出码应为 0，实际 %d", status.ExitCode)
	}
}

// TestStopLaunchProcessForceKilled 验证 force=true 直接 Kill 并记 killed。
func TestStopLaunchProcessForceKilled(t *testing.T) {
	fake := newRunningLaunchForTest(t, helperModeHang)

	snapshot, launchErr := StopLaunchProcess(StopLaunchRequest{
		LaunchID: fake.record.launchID,
		Force:    true,
	})
	if launchErr != nil {
		t.Fatalf("强制停止请求不应失败: %s", launchErr.Message)
	}
	if snapshot.EndedAtMs == 0 && snapshot.State != StateStopping {
		t.Fatalf("停止受理后、进程未退出时状态必须是 %q，实际 %q", StateStopping, snapshot.State)
	}

	waitFor(t, 5*time.Second, "强制终止后记录进入 exited", func() bool {
		return recordState(fake.record) == StateExited
	})
	waitFor(t, 5*time.Second, "停止结果记为 killed", func() bool {
		return recordStopResult(fake.record) == StopResultKilled
	})

	status, statusErr := GetLaunchStatusSnapshot(LaunchStatusRequest{LaunchID: fake.record.launchID})
	if statusErr != nil {
		t.Fatalf("状态查询失败: %s", statusErr.Message)
	}
	if status.StopResult != StopResultKilled {
		t.Fatalf("停止结果应为 %q，实际 %q", StopResultKilled, status.StopResult)
	}
	if status.Outcome != OutcomeStoppedByUser {
		t.Fatalf("用户强制停止的结局应为 %q，实际 %q", OutcomeStoppedByUser, status.Outcome)
	}
}

// TestStopLaunchProcessTimeoutEscalation 验证优雅超时后升级为 Kill 并记 timeout（D-3）。
//
// 该用例必须真实等待 gracefulStopTimeout（10 秒），因此 -short 模式下跳过。
func TestStopLaunchProcessTimeoutEscalation(t *testing.T) {
	if testing.Short() {
		t.Skip("超时升级用例需要等待 10 秒，-short 模式跳过")
	}

	fake := newRunningLaunchForTest(t, helperModeHang)

	snapshot, launchErr := StopLaunchProcess(StopLaunchRequest{LaunchID: fake.record.launchID})
	if launchErr != nil {
		t.Fatalf("优雅停止请求不应失败: %s", launchErr.Message)
	}
	if snapshot.EndedAtMs == 0 && snapshot.State != StateStopping {
		t.Fatalf("超时前状态应为 %q，实际 %q", StateStopping, snapshot.State)
	}

	// 假进程忽略 stdin，必须等到 10 秒超时后才被 Kill。
	waitFor(t, gracefulStopTimeout+5*time.Second, "超时后停止结果升级为 timeout", func() bool {
		return recordStopResult(fake.record) == StopResultTimeout
	})
	waitFor(t, 5*time.Second, "强制终止后记录进入 exited", func() bool {
		return recordState(fake.record) == StateExited
	})

	status, statusErr := GetLaunchStatusSnapshot(LaunchStatusRequest{LaunchID: fake.record.launchID})
	if statusErr != nil {
		t.Fatalf("状态查询失败: %s", statusErr.Message)
	}
	if status.StopResult != StopResultTimeout {
		t.Fatalf("停止结果应为 %q，实际 %q", StopResultTimeout, status.StopResult)
	}
	if status.Outcome != OutcomeStoppedByUser {
		t.Fatalf("用户停止（含超时升级）的结局应为 %q，实际 %q", OutcomeStoppedByUser, status.Outcome)
	}
}

// TestStopLaunchProcessIdempotent 验证重复 StopLaunch 幂等（不发第二次终止）。
func TestStopLaunchProcessIdempotent(t *testing.T) {
	fake := newRunningLaunchForTest(t, helperModeHang)

	first, firstErr := StopLaunchProcess(StopLaunchRequest{LaunchID: fake.record.launchID})
	if firstErr != nil {
		t.Fatalf("首次停止请求不应失败: %s", firstErr.Message)
	}
	if first.State != StateStopping {
		t.Fatalf("首次停止受理后状态应为 %q，实际 %q", StateStopping, first.State)
	}

	second, secondErr := StopLaunchProcess(StopLaunchRequest{LaunchID: fake.record.launchID})
	if secondErr != nil {
		t.Fatalf("重复停止必须幂等返回，实际错误 %q", secondErr.Code)
	}
	if second.State != StateStopping {
		// 假进程会顶到 10 秒超时，用例内不可能已退出，因此这里必须是 stopping。
		t.Fatalf("重复停止时状态应仍为 %q，实际 %q", StateStopping, second.State)
	}
	if second.LaunchID != first.LaunchID {
		t.Fatalf("重复停止应返回同一启动，期望 %q，实际 %q", first.LaunchID, second.LaunchID)
	}
	if reason := recordStopReason(fake.record); reason != StopReasonUserRequested {
		t.Fatalf("停止原因应保持 %q，实际 %q", StopReasonUserRequested, reason)
	}
}

// TestStopLaunchProcessWithoutActiveLaunch 验证无活动启动时返回 LAUNCH_STATE_001。
func TestStopLaunchProcessWithoutActiveLaunch(t *testing.T) {
	resetSupervisorForTest(t)

	_, launchErr := StopLaunchProcess(StopLaunchRequest{})
	if launchErr == nil {
		t.Fatalf("无活动启动时停止应失败")
	}
	if launchErr.Code != ErrorCodeLaunchState {
		t.Fatalf("应返回 %q，实际 %q", ErrorCodeLaunchState, launchErr.Code)
	}
	if launchErr.Message != messageLaunchNotRunning {
		t.Fatalf("错误文案应为 %q，实际 %q", messageLaunchNotRunning, launchErr.Message)
	}
}

// TestStopLaunchProcessRejectsNonRunningState 验证非运行态停止返回 LAUNCH_STATE_001。
//
// 用一条仍在 preparing 的活动记录（尚未起进程）代表「不可停止」的状态。
func TestStopLaunchProcessRejectsNonRunningState(t *testing.T) {
	resetSupervisorForTest(t)

	record := &launchRecord{launchID: "launch-preparing", state: StatePreparing}
	globalSupervisor.mu.Lock()
	globalSupervisor.active = record
	globalSupervisor.mu.Unlock()

	_, launchErr := StopLaunchProcess(StopLaunchRequest{LaunchID: "launch-preparing"})
	if launchErr == nil {
		t.Fatalf("非运行态停止应失败")
	}
	if launchErr.Code != ErrorCodeLaunchState {
		t.Fatalf("应返回 %q，实际 %q", ErrorCodeLaunchState, launchErr.Code)
	}
	if !strings.Contains(launchErr.Message, StatePreparing) {
		t.Fatalf("错误文案应说明当前状态，实际 %q", launchErr.Message)
	}
	if actual := recordState(record); actual != StatePreparing {
		t.Fatalf("被拒绝的停止不得改动状态，实际 %q", actual)
	}
	if recordStopResult(record) != "" {
		t.Fatalf("被拒绝的停止不得写停止结果")
	}
}

// TestShutdownActiveLaunchStopsGracefully 验证应用退出钩子的收尾（先优雅、必要时强杀）。
func TestShutdownActiveLaunchStopsGracefully(t *testing.T) {
	fake := newRunningLaunchForTest(t, helperModeWaitStdin)

	ShutdownActiveLaunch()

	if state := recordState(fake.record); state != StateExited && state != StateStopping {
		t.Fatalf("退出钩子后状态应为 %q 或 %q，实际 %q", StateStopping, StateExited, state)
	}
	if reason := recordStopReason(fake.record); reason != StopReasonAppExiting {
		t.Fatalf("停止原因应为 %q，实际 %q", StopReasonAppExiting, reason)
	}

	// ShutdownActiveLaunch 返回时应已确认进程结束（否则就是孤儿进程）。
	waitFor(t, 5*time.Second, "退出钩子返回后进程已结束", func() bool {
		return recordEndedAtMs(fake.record) != 0
	})

	status, statusErr := GetLaunchStatusSnapshot(LaunchStatusRequest{})
	if statusErr != nil {
		t.Fatalf("状态查询失败: %s", statusErr.Message)
	}
	if status.State != StateExited {
		t.Fatalf("最终状态应为 %q，实际 %q", StateExited, status.State)
	}
	if status.StopReason != StopReasonAppExiting {
		t.Fatalf("快照中的停止原因应为 %q，实际 %q", StopReasonAppExiting, status.StopReason)
	}
	if status.Outcome != OutcomeStoppedByUser {
		t.Fatalf("应用退出导致的停止结局应为 %q，实际 %q", OutcomeStoppedByUser, status.Outcome)
	}
}

// TestShutdownActiveLaunchWithoutActiveLaunch 验证无活动启动时退出钩子是空操作。
func TestShutdownActiveLaunchWithoutActiveLaunch(t *testing.T) {
	resetSupervisorForTest(t)

	// 连续调用两次：不得 panic，也不得产生任何记录。
	ShutdownActiveLaunch()
	ShutdownActiveLaunch()

	if active := globalActiveLaunchID(); active != "" {
		t.Fatalf("无活动启动时不应产生活动记录，实际 %q", active)
	}

	// 记录存在但进程尚未创建（preparing）时同样应安全返回。
	record := &launchRecord{launchID: "launch-preparing", state: StatePreparing}
	globalSupervisor.mu.Lock()
	globalSupervisor.active = record
	globalSupervisor.mu.Unlock()

	ShutdownActiveLaunch()

	if actual := recordState(record); actual != StatePreparing {
		t.Fatalf("无进程时不应改动状态，实际 %q", actual)
	}
}

// TestExpireStaleFinished 验证终态记录的过期清理边界。
//
// 超过 finishedRetention 的记录被清理；未到期与 finishedAtMs 为 0 的记录保留。
func TestExpireStaleFinished(t *testing.T) {
	now := time.Now().UTC()

	expired := &supervisor{
		lastFinished: &launchRecord{
			launchID:     "launch-expired",
			state:        StateExited,
			finishedAtMs: now.Add(-finishedRetention - time.Minute).UnixMilli(),
		},
	}
	expired.mu.Lock()
	expired.expireStaleFinished(now)
	expired.mu.Unlock()
	if expired.lastFinished != nil {
		t.Fatalf("超过保留期的终态记录应被清理")
	}

	retained := &supervisor{
		lastFinished: &launchRecord{
			launchID:     "launch-retained",
			state:        StateExited,
			finishedAtMs: now.Add(-time.Minute).UnixMilli(),
		},
	}
	retained.mu.Lock()
	retained.expireStaleFinished(now)
	retained.mu.Unlock()
	if retained.lastFinished == nil {
		t.Fatalf("未到期的终态记录不应被清理")
	}

	undeclared := &supervisor{
		lastFinished: &launchRecord{launchID: "launch-unknown", state: StateExited},
	}
	undeclared.mu.Lock()
	undeclared.expireStaleFinished(now)
	undeclared.mu.Unlock()
	if undeclared.lastFinished == nil {
		t.Fatalf("finishedAtMs 为 0 的记录不应被清理")
	}

	empty := &supervisor{}
	empty.mu.Lock()
	empty.expireStaleFinished(now)
	empty.mu.Unlock()
	if empty.lastFinished != nil {
		t.Fatalf("无终态记录时清理应保持为空")
	}
}

// trackingStdin 是记录「关闭时机」的 stdin 替身。
//
// finalize 的契约是「先 WaitReads、后关 stdin」，用该替身可以观测顺序：
// 若 Close 发生在 readsDone 关闭之前，closedAfterReads 为 false。
type trackingStdin struct {
	// readsDone 是读取 goroutine 的完成信号。
	readsDone chan struct{}

	// mu 保护下列字段（Close 在 finalize goroutine 中执行）。
	mu sync.Mutex

	// closed 表示 Close 已被调用。
	closed bool

	// closedAfterReads 表示 Close 发生时 readsDone 已关闭。
	closedAfterReads bool
}

// Write 实现 io.Writer：丢弃内容但返回成功。
func (s *trackingStdin) Write(p []byte) (int, error) {
	return len(p), nil
}

// Close 记录关闭时机并返回成功。
func (s *trackingStdin) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.closed = true
	select {
	case <-s.readsDone:
		s.closedAfterReads = true
	default:
	}
	return nil
}

// observed 返回（是否已关闭，关闭时是否已结束读取）。
func (s *trackingStdin) observed() (bool, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed, s.closedAfterReads
}

// TestFinalizeWaitsForReadsBeforeClosingStdin 验证收尾顺序：
// WaitReads 结束之前**不得**关闭 stdin，否则会丢掉尾部日志。
func TestFinalizeWaitsForReadsBeforeClosingStdin(t *testing.T) {
	resetSupervisorForTest(t)

	readsDone := make(chan struct{})
	stdin := &trackingStdin{readsDone: readsDone}
	process := &GameProcess{Stdin: stdin, readsDone: readsDone}
	record := &launchRecord{
		launchID:     "launch-finalize",
		state:        StateExited,
		finishedAtMs: time.Now().UTC().UnixMilli(),
	}
	globalSupervisor.mu.Lock()
	globalSupervisor.active = record
	globalSupervisor.mu.Unlock()

	done := make(chan struct{})
	go func() {
		defer close(done)
		globalSupervisor.finalize(record, process)
	}()

	// 读取尚未结束：finalize 必须阻塞在 WaitReads，此时 stdin 不能已关闭。
	time.Sleep(50 * time.Millisecond)
	if closed, _ := stdin.observed(); closed {
		t.Fatalf("WaitReads 未结束时不得关闭 stdin（会丢失尾部日志）")
	}

	close(readsDone)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("finalize 未在 5 秒内返回")
	}

	closed, closedAfterReads := stdin.observed()
	if !closed {
		t.Fatalf("收尾时必须关闭 stdin")
	}
	if !closedAfterReads {
		t.Fatalf("stdin 必须在 WaitReads 之后关闭")
	}

	// 收尾后活动位释放、记录移入终态槽。
	if active := globalActiveLaunchID(); active != "" {
		t.Fatalf("收尾后活动位应被释放，实际 %q", active)
	}
	globalSupervisor.mu.Lock()
	last := globalSupervisor.lastFinished
	globalSupervisor.mu.Unlock()
	if last != record {
		t.Fatalf("收尾后记录应进入终态槽")
	}
}

// TestGetLaunchStatusSnapshotUsesActiveWhenLaunchIDEmpty 验证空 launchId 时返回活动启动。
func TestGetLaunchStatusSnapshotUsesActiveWhenLaunchIDEmpty(t *testing.T) {
	fake := newRunningLaunchForTest(t, helperModeHang)

	status, statusErr := GetLaunchStatusSnapshot(LaunchStatusRequest{})
	if statusErr != nil {
		t.Fatalf("状态查询失败: %s", statusErr.Message)
	}
	if status.LaunchID != fake.record.launchID {
		t.Fatalf("空 launchId 应返回活动启动 %q，实际 %q", fake.record.launchID, status.LaunchID)
	}
	if status.State != StateRunning {
		t.Fatalf("状态应为 %q，实际 %q", StateRunning, status.State)
	}
	if status.VersionID != "1.20.1" {
		t.Fatalf("快照应携带版本 id，实际 %q", status.VersionID)
	}
	if status.StartedAtMs == 0 {
		t.Fatalf("运行中记录应携带开始时间")
	}
	if status.EndedAtMs != 0 {
		t.Fatalf("运行中记录不应有结束时间，实际 %d", status.EndedAtMs)
	}
	if status.StopReason != "" || status.StopResult != "" || status.Outcome != "" {
		t.Fatalf("运行中记录不应有停止/结局字段，实际 reason=%q result=%q outcome=%q",
			status.StopReason, status.StopResult, status.Outcome)
	}

	// 指定 launchId 同样能查到同一记录。
	same, sameErr := GetLaunchStatusSnapshot(LaunchStatusRequest{LaunchID: fake.record.launchID})
	if sameErr != nil {
		t.Fatalf("按 launchId 查询失败: %s", sameErr.Message)
	}
	if same.LaunchID != status.LaunchID {
		t.Fatalf("两种查询方式应命中同一记录")
	}
}

// TestGetLaunchStatusSnapshotUnknownLaunchID 验证未知 launchId 返回 LAUNCH_STATE_001。
//
// 这是「唯一真相源」的边界：查不到不是“空快照”，而是明确错误，
// 前端据此提示「记录已过期清理」。
func TestGetLaunchStatusSnapshotUnknownLaunchID(t *testing.T) {
	resetSupervisorForTest(t)

	_, statusErr := GetLaunchStatusSnapshot(LaunchStatusRequest{LaunchID: "launch-not-exist"})
	if statusErr == nil {
		t.Fatalf("未知 launchId 应返回错误")
	}
	if statusErr.Code != ErrorCodeLaunchState {
		t.Fatalf("应返回 %q，实际 %q", ErrorCodeLaunchState, statusErr.Code)
	}
	if statusErr.Message != messageLaunchNotFound {
		t.Fatalf("错误文案应为 %q，实际 %q", messageLaunchNotFound, statusErr.Message)
	}
}

// TestGetLaunchStatusSnapshotNoRecordAndEmptyLaunchID 验证「从未启动过」不是错误。
//
// 首次打开启动页时前端会调用一次 GetLaunchStatus 且不带 launchId。
// 此时既没有活动启动也没有历史记录，属于完全正常的初始状态：
// 必须返回零值快照且不报错，让前端把它归入 idle；
// 若这里返回 LAUNCH_STATE_001，页面一打开就会顶着一个错误横幅（P2 验收缺陷 1）。
func TestGetLaunchStatusSnapshotNoRecordAndEmptyLaunchID(t *testing.T) {
	resetSupervisorForTest(t)

	status, statusErr := GetLaunchStatusSnapshot(LaunchStatusRequest{})
	if statusErr != nil {
		t.Fatalf("无记录且未指定 launchId 时不应报错，实际 %q: %s", statusErr.Code, statusErr.Message)
	}
	if status.LaunchID != "" {
		t.Fatalf("无记录时应返回空 launchId，实际 %q", status.LaunchID)
	}
	if status.State != "" {
		t.Fatalf("无记录时状态应为空串（前端归入 idle），实际 %q", status.State)
	}
}

func TestNewLaunchIDIsUUIDv4(t *testing.T) {
	pattern := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

	first, firstErr := newLaunchID()
	if firstErr != nil {
		t.Fatalf("生成 launchId 失败: %v", firstErr)
	}
	second, secondErr := newLaunchID()
	if secondErr != nil {
		t.Fatalf("生成 launchId 失败: %v", secondErr)
	}

	if !pattern.MatchString(first) {
		t.Fatalf("launchId 应为小写 UUID v4，实际 %q", first)
	}
	if !pattern.MatchString(second) {
		t.Fatalf("launchId 应为小写 UUID v4，实际 %q", second)
	}
	if first == second {
		t.Fatalf("两次生成的 launchId 不得相同，实际都是 %q", first)
	}
}
