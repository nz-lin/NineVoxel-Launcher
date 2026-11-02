// 本文件是下载任务状态机与 DTO 的单元测试。
//
// 只测纯函数（状态机判定、优先级、视图转换、错误匹配），
// 不触碰文件系统、网络与进程——调度行为在 queue_test.go 中验证。
package download

import (
	"errors"
	"testing"
)

// frozenTransitions 是《接口冻结清单》§7 的转移表副本。
//
// 刻意在测试里再写一遍而不是复用实现的 legalTransitions：
// 若两边共用一份数据，转移表被误改时测试会一起改，测不出偏差。
var frozenTransitions = map[State][]State{
	StateQueued:      {StateResolving, StateDownloading, StatePaused, StateCancelled},
	StateResolving:   {StateDownloading, StateFailed, StateCancelled},
	StateDownloading: {StatePaused, StateVerifying, StateFailed, StateCancelled},
	StatePaused:      {StateQueued, StateDownloading, StateCancelled},
	StateVerifying:   {StateCommitting, StateFailed, StateCancelled},
	StateCommitting:  {StateCompleted, StateFailed},
}

// containsState 判断切片是否包含指定状态。
func containsState(states []State, target State) bool {
	for _, state := range states {
		if state == target {
			return true
		}
	}
	return false
}

// TestStateMachineMatchesFrozenTable 逐条比对转移表：合法集必须完全一致，
// 冻结表未列出的组合（含终态出发、自环）一律非法。
func TestStateMachineMatchesFrozenTable(t *testing.T) {
	for _, from := range AllStates() {
		for _, to := range AllStates() {
			want := containsState(frozenTransitions[from], to)
			got := CanTransit(from, to)
			if got != want {
				t.Errorf("CanTransit(%s, %s) = %v，期望 %v", from, to, got, want)
			}
		}
	}
}

// TestValidateTransitionRejectsIllegalMoves 覆盖至少 5 条典型非法转移。
func TestValidateTransitionRejectsIllegalMoves(t *testing.T) {
	cases := []struct {
		name string
		from State
		to   State
	}{
		{name: "终态 completed 不可再转移", from: StateCompleted, to: StateQueued},
		{name: "终态 failed 不可再转移", from: StateFailed, to: StateDownloading},
		{name: "终态 cancelled 不可恢复", from: StateCancelled, to: StateQueued},
		{name: "queued 不可直接校验", from: StateQueued, to: StateVerifying},
		{name: "downloading 不可直接提交", from: StateDownloading, to: StateCommitting},
		{name: "resolving 不可暂停", from: StateResolving, to: StatePaused},
		{name: "paused 不可直接校验", from: StatePaused, to: StateVerifying},
		{name: "verifying 不可回到下载", from: StateVerifying, to: StateDownloading},
		{name: "相同状态不算转移", from: StateQueued, to: StateQueued},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			transitionErr := ValidateTransition(testCase.from, testCase.to)
			if transitionErr == nil {
				t.Fatalf("期望 %s → %s 被拒绝，实际通过", testCase.from, testCase.to)
			}
			if transitionErr.Code != ErrorCodeState {
				t.Fatalf("错误码 = %s，期望 %s", transitionErr.Code, ErrorCodeState)
			}
			if transitionErr.Retryable {
				t.Fatalf("状态错误不应标记为可重试")
			}
		})
	}
}

// TestValidateTransitionAcceptsLegalMoves 确保合法转移不返回错误。
func TestValidateTransitionAcceptsLegalMoves(t *testing.T) {
	for from, targets := range frozenTransitions {
		for _, to := range targets {
			if transitionErr := ValidateTransition(from, to); transitionErr != nil {
				t.Errorf("%s → %s 应合法，实际错误 %s", from, to, transitionErr.Code)
			}
		}
	}
}

// TestIsTerminal 校验终态判定。
func TestIsTerminal(t *testing.T) {
	terminal := map[State]bool{
		StateQueued:      false,
		StateResolving:   false,
		StateDownloading: false,
		StatePaused:      false,
		StateVerifying:   false,
		StateCommitting:  false,
		StateCompleted:   true,
		StateFailed:      true,
		StateCancelled:   true,
	}
	for state, expected := range terminal {
		if got := IsTerminal(state); got != expected {
			t.Errorf("IsTerminal(%s) = %v，期望 %v", state, got, expected)
		}
	}
}

// TestCancelPauseResumeEligibility 校验三个语义判定与冻结表一致。
func TestCancelPauseResumeEligibility(t *testing.T) {
	if CanCancel(StateCommitting) {
		t.Fatalf("提交阶段不可取消（DOWNLOAD_CANCEL_001）")
	}
	for _, state := range []State{StateQueued, StateResolving, StateDownloading, StatePaused, StateVerifying} {
		if !CanCancel(state) {
			t.Errorf("%s 应允许取消", state)
		}
	}
	for _, state := range []State{StateCompleted, StateFailed, StateCancelled} {
		if CanCancel(state) || CanPause(state) || CanResume(state) {
			t.Errorf("终态 %s 不应允许任何操作", state)
		}
	}
	if !CanPause(StateDownloading) || CanPause(StateQueued) {
		t.Fatalf("只有 downloading 可暂停")
	}
	if !CanResume(StatePaused) || CanResume(StateQueued) {
		t.Fatalf("只有 paused 可恢复")
	}
}

// TestPriorityOrder 校验卡面要求的优先级顺序。
func TestPriorityOrder(t *testing.T) {
	ordered := []string{TaskTypePlan, TaskTypeVersionJSON, TaskTypeClientJar, TaskTypeLibrary, TaskTypeAssetIndex, TaskTypeAsset}
	previous := -1
	for _, taskType := range ordered {
		current := priorityOf(taskType)
		if current <= previous {
			t.Fatalf("优先级顺序错误：%s = %d，不应小于等于前一项 %d", taskType, current, previous)
		}
		previous = current
	}
}

// TestTaskToViewHidesSourceURI 校验 DTO 不含下载地址与后端内部字段。
func TestTaskToViewHidesSourceURI(t *testing.T) {
	task := Task{
		ID:             "task-1",
		TaskType:       TaskTypeClientJar,
		State:          StateDownloading,
		SourceURI:      "https://example.com/client.jar?token=secret",
		TargetRelPath:  "artifacts/objects/ab/abcd",
		DownloadedSize: 512,
		ExpectedSize:   1024,
		VersionID:      "1.20.1",
		CreatedAtMs:    1000,
		UpdatedAtMs:    2000,
	}
	view := taskToView(task)
	if view.ID != "task-1" {
		t.Fatalf("ID = %s", view.ID)
	}
	if view.Title != "1.20.1 · 客户端 JAR" {
		t.Fatalf("Title = %s", view.Title)
	}
	if view.Progress == nil || *view.Progress != 0.5 {
		t.Fatalf("Progress 期望 0.5，实际 %v", view.Progress)
	}
	if view.TotalBytes == nil || *view.TotalBytes != 1024 {
		t.Fatalf("TotalBytes 期望 1024，实际 %v", view.TotalBytes)
	}
	// DTO 结构里根本没有 SourceURI / TargetRelPath 字段，这里断言的是
	// 「视图不因任务字段而携带地址」——通过字段清单不能被隐式赋值来保证。
	if view.Error != nil {
		t.Fatalf("非失败状态不应带错误")
	}
}

// TestTaskToViewProgressWhenSizeUnknown 校验总大小未知时不给出进度。
func TestTaskToViewProgressWhenSizeUnknown(t *testing.T) {
	view := taskToView(Task{ID: "task-2", TaskType: TaskTypeAsset, State: StateQueued})
	if view.Progress != nil || view.TotalBytes != nil {
		t.Fatalf("未知大小不应有进度与总字节")
	}
}

// TestErrorIsMatchesByCode 校验错误按码匹配。
func TestErrorIsMatchesByCode(t *testing.T) {
	cause := newInputError(ErrorCodeState, messageStateNotAllowed)
	target := &Error{Code: ErrorCodeState}
	if !errors.Is(cause, target) {
		t.Fatalf("errors.Is 应按下错误码匹配")
	}
	other := &Error{Code: ErrorCodeCancel}
	if errors.Is(cause, other) {
		t.Fatalf("不同错误码不应匹配")
	}
	if cause.Error() != "DOWNLOAD_STATE_001: "+messageStateNotAllowed {
		t.Fatalf("Error() = %s", cause.Error())
	}
}

// TestAllStatesIsCopy 校验 AllStates 返回副本，调用方修改不影响内部数据。
func TestAllStatesIsCopy(t *testing.T) {
	first := AllStates()
	if len(first) != 9 {
		t.Fatalf("状态数量 = %d，期望 9", len(first))
	}
	first[0] = "tampered"
	if AllStates()[0] != StateQueued {
		t.Fatalf("AllStates 应返回副本")
	}
}

// TestSortTasksForDisplay 校验未结束任务优先、时间倒序。
func TestSortTasksForDisplay(t *testing.T) {
	tasks := []Task{
		{ID: "c", State: StateCompleted, CreatedAtMs: 300},
		{ID: "a", State: StateDownloading, CreatedAtMs: 100},
		{ID: "b", State: StateFailed, CreatedAtMs: 200},
		{ID: "d", State: StateQueued, CreatedAtMs: 400},
	}
	sortTasksForDisplay(tasks)
	got := []string{tasks[0].ID, tasks[1].ID, tasks[2].ID, tasks[3].ID}
	want := []string{"d", "a", "c", "b"}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("排序结果 = %v，期望 %v", got, want)
		}
	}
}
