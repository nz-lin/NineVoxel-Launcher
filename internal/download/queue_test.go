// 本文件是下载队列的调度行为测试：并发闸门、重试退避、暂停/恢复/取消、
// 事件顺序、去重与列表分页，以及任务表的持久化往返。
//
// 全部用例注入假接缝（PlanSource / TransferFunc / CommitFunc）与极小的
// 退避基准，因此不联网、不真正等待数秒、不写业务文件。
package download

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"example.com/ninevoxel/launcher/internal/database"
)

// newTestQueue 构造一个用内存任务存储的队列并启动它。
func newTestQueue(t *testing.T, options QueueOptions) *Queue {
	t.Helper()
	originalFactory := taskStoreFactory
	taskStoreFactory = func(context.Context) (TaskStore, *Error) {
		return newMemoryTaskStore(), nil
	}
	t.Cleanup(func() { taskStoreFactory = originalFactory })

	if options.BackoffBase == 0 {
		options.BackoffBase = time.Millisecond
	}
	if options.BackoffMax == 0 {
		options.BackoffMax = 20 * time.Millisecond
	}
	if options.Sink == nil {
		options.Sink = func(Task, float64) {}
	}
	if options.Logf == nil {
		options.Logf = func(string, ...any) {}
	}
	queue := NewQueue(options)
	if startErr := queue.Start(context.Background()); startErr != nil {
		t.Fatalf("队列启动失败：%s", startErr.Code)
	}
	t.Cleanup(func() {
		_ = queue.Close()
	})
	return queue
}

// fakePlanSource 返回固定条目的计划来源。
func fakePlanSource(items ...PlanItem) PlanSource {
	return func(_ context.Context, req StartDownloadPlanRequest) (TransferPlan, *Error) {
		return TransferPlan{VersionID: req.VersionID, PlanHash: "plan-hash-1", Items: items}, nil
	}
}

// singleItemPlan 构造一个单条目计划来源。
func singleItemPlan(uri string) PlanSource {
	return fakePlanSource(PlanItem{
		TaskType:       TaskTypeClientJar,
		SourceURI:      uri,
		TargetRelPath:  "versions/1.20.1/1.20.1.jar",
		TargetKind:     TargetKindVersion,
		ExpectedSize:   1024,
		ExpectedSHA1:   "0123456789abcdef0123456789abcdef01234567",
		ExpectedSHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	})
}

// instantCommit 是一个总是成功的提交实现。
func instantCommit(context.Context, Task) *Error { return nil }

// snapshotTasks 读取队列内部任务副本（同包测试可见）。
func snapshotTasks(queue *Queue) []Task {
	queue.mu.Lock()
	defer queue.mu.Unlock()
	result := make([]Task, 0, len(queue.tasks))
	for _, task := range queue.tasks {
		result = append(result, *task)
	}
	return result
}

// childTasks 过滤出子任务（父任务 TaskType 为 plan）。
func childTasks(tasks []Task) []Task {
	result := make([]Task, 0, len(tasks))
	for _, task := range tasks {
		if task.TaskType == TaskTypePlan {
			continue
		}
		result = append(result, task)
	}
	return result
}

// waitUntil 轮询等待条件成立，超时即失败。
func waitUntil(t *testing.T, timeout time.Duration, describe string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("等待超时（%s）：%s", timeout, describe)
}

// waitForAllChildrenTerminal 等待全部子任务进入终态并返回它们。
func waitForAllChildrenTerminal(t *testing.T, queue *Queue, timeout time.Duration) []Task {
	t.Helper()
	var children []Task
	waitUntil(t, timeout, "全部子任务进入终态", func() bool {
		children = childTasks(snapshotTasks(queue))
		if len(children) == 0 {
			return false
		}
		for _, child := range children {
			if !IsTerminal(child.State) {
				return false
			}
		}
		return true
	})
	return children
}

// concurrencyTracker 记录并发峰值。
type concurrencyTracker struct {
	mu      sync.Mutex
	current int
	peak    int
}

// enter 标记一次进入并更新峰值。
func (c *concurrencyTracker) enter() {
	c.mu.Lock()
	c.current++
	if c.current > c.peak {
		c.peak = c.current
	}
	c.mu.Unlock()
}

// exit 标记一次退出。
func (c *concurrencyTracker) exit() {
	c.mu.Lock()
	c.current--
	c.mu.Unlock()
}

// Peak 返回观测到的并发峰值。
func (c *concurrencyTracker) Peak() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.peak
}

// TestPerHostConcurrencyLimit 投 10 个同主机任务，同时运行数不得超过 2。
func TestPerHostConcurrencyLimit(t *testing.T) {
	tracker := &concurrencyTracker{}
	items := make([]PlanItem, 0, 10)
	for index := 0; index < 10; index++ {
		items = append(items, PlanItem{
			TaskType:      TaskTypeAsset,
			SourceURI:     fmt.Sprintf("https://same-host.example/assets/%d", index),
			TargetRelPath: fmt.Sprintf("artifacts/objects/aa/asset-%d", index),
			ExpectedSize:  16,
		})
	}
	queue := newTestQueue(t, QueueOptions{
		PlanSource: fakePlanSource(items...),
		Commit:     instantCommit,
		Transfer: func(context.Context, Task) TransferResult {
			tracker.enter()
			defer tracker.exit()
			time.Sleep(2 * time.Millisecond)
			return TransferResult{Downloaded: 16, TotalSize: 16}
		},
	})

	if _, enqueueErr := queue.EnqueuePlan(context.Background(), StartDownloadPlanRequest{VersionID: "1.20.1"}); enqueueErr != nil {
		t.Fatalf("入队失败：%s", enqueueErr.Code)
	}
	children := waitForAllChildrenTerminal(t, queue, 10*time.Second)
	if len(children) != 10 {
		t.Fatalf("子任务数量 = %d，期望 10", len(children))
	}
	if peak := tracker.Peak(); peak > 2 {
		t.Fatalf("同主机并发峰值 = %d，超过上限 2", peak)
	}
}

// TestGlobalConcurrencyLimit 投 10 个不同主机任务，同时运行数不得超过 4。
func TestGlobalConcurrencyLimit(t *testing.T) {
	tracker := &concurrencyTracker{}
	items := make([]PlanItem, 0, 10)
	for index := 0; index < 10; index++ {
		items = append(items, PlanItem{
			TaskType:      TaskTypeLibrary,
			SourceURI:     fmt.Sprintf("https://host-%d.example/lib.jar", index),
			TargetRelPath: fmt.Sprintf("artifacts/objects/bb/lib-%d", index),
			ExpectedSize:  16,
		})
	}
	queue := newTestQueue(t, QueueOptions{
		PlanSource: fakePlanSource(items...),
		Commit:     instantCommit,
		Transfer: func(context.Context, Task) TransferResult {
			tracker.enter()
			defer tracker.exit()
			time.Sleep(2 * time.Millisecond)
			return TransferResult{Downloaded: 16, TotalSize: 16}
		},
	})

	if _, enqueueErr := queue.EnqueuePlan(context.Background(), StartDownloadPlanRequest{VersionID: "1.20.1"}); enqueueErr != nil {
		t.Fatalf("入队失败：%s", enqueueErr.Code)
	}
	waitForAllChildrenTerminal(t, queue, 10*time.Second)
	if peak := tracker.Peak(); peak > 4 {
		t.Fatalf("全局并发峰值 = %d，超过上限 4", peak)
	}
	if peak := tracker.Peak(); peak < 2 {
		t.Fatalf("并发峰值 = %d，调度几乎串行，说明闸门实现有误", peak)
	}
}

// TestRetryBackoffThenSuccess 校验失败重试后成功，且退避基准可注入。
func TestRetryBackoffThenSuccess(t *testing.T) {
	var attempts int32
	queue := newTestQueue(t, QueueOptions{
		PlanSource:  singleItemPlan("https://retry.example/client.jar"),
		Commit:      instantCommit,
		MaxRetries:  defaultMaxRetries,
		BackoffBase: time.Millisecond,
		Transfer: func(context.Context, Task) TransferResult {
			current := atomic.AddInt32(&attempts, 1)
			if current < 3 {
				return TransferResult{Err: context.DeadlineExceeded, Retryable: true, ErrorCode: ErrorCodeNetwork}
			}
			return TransferResult{Downloaded: 1024, TotalSize: 1024}
		},
	})
	if queue.BackoffBase() != time.Millisecond {
		t.Fatalf("退避基准未生效：%s", queue.BackoffBase())
	}

	if _, enqueueErr := queue.EnqueuePlan(context.Background(), StartDownloadPlanRequest{VersionID: "1.20.1"}); enqueueErr != nil {
		t.Fatalf("入队失败：%s", enqueueErr.Code)
	}
	children := waitForAllChildrenTerminal(t, queue, 10*time.Second)
	if children[0].State != StateCompleted {
		t.Fatalf("最终状态 = %s，期望 completed", children[0].State)
	}
	if got := atomic.LoadInt32(&attempts); got != 3 {
		t.Fatalf("传输调用次数 = %d，期望 3", got)
	}
}

// TestRetryExhaustedMarksFailed 校验重试次数用尽后任务失败。
func TestRetryExhaustedMarksFailed(t *testing.T) {
	var attempts int32
	queue := newTestQueue(t, QueueOptions{
		PlanSource:  singleItemPlan("https://retry.example/client.jar"),
		Commit:      instantCommit,
		MaxRetries:  2,
		BackoffBase: time.Millisecond,
		Transfer: func(context.Context, Task) TransferResult {
			atomic.AddInt32(&attempts, 1)
			return TransferResult{Err: context.DeadlineExceeded, Retryable: true, ErrorCode: ErrorCodeNetwork}
		},
	})
	if _, enqueueErr := queue.EnqueuePlan(context.Background(), StartDownloadPlanRequest{VersionID: "1.20.1"}); enqueueErr != nil {
		t.Fatalf("入队失败：%s", enqueueErr.Code)
	}
	children := waitForAllChildrenTerminal(t, queue, 10*time.Second)
	child := children[0]
	if child.State != StateFailed {
		t.Fatalf("最终状态 = %s，期望 failed", child.State)
	}
	if child.ErrorCode != ErrorCodeNetwork {
		t.Fatalf("错误码 = %s，期望 %s", child.ErrorCode, ErrorCodeNetwork)
	}
	// 首次调用 + 2 次重试 = 3 次。
	if got := atomic.LoadInt32(&attempts); got != 3 {
		t.Fatalf("传输调用次数 = %d，期望 3", got)
	}
}

// TestNonRetryableFailureStopsImmediately 校验不可重试错误只尝试一次。
func TestNonRetryableFailureStopsImmediately(t *testing.T) {
	var attempts int32
	queue := newTestQueue(t, QueueOptions{
		PlanSource: singleItemPlan("https://retry.example/client.jar"),
		Commit:     instantCommit,
		Transfer: func(context.Context, Task) TransferResult {
			atomic.AddInt32(&attempts, 1)
			return TransferResult{Err: errTestNotFound, Retryable: false, ErrorCode: ErrorCodeSize}
		},
	})
	if _, enqueueErr := queue.EnqueuePlan(context.Background(), StartDownloadPlanRequest{VersionID: "1.20.1"}); enqueueErr != nil {
		t.Fatalf("入队失败：%s", enqueueErr.Code)
	}
	children := waitForAllChildrenTerminal(t, queue, 10*time.Second)
	if children[0].State != StateFailed {
		t.Fatalf("最终状态 = %s，期望 failed", children[0].State)
	}
	if got := atomic.LoadInt32(&attempts); got != 1 {
		t.Fatalf("传输调用次数 = %d，期望 1", got)
	}
}

// TestTransferFailurePreservesStructuredError 校验队列不会用「传输未接入」
// 覆盖真实传输错误，否则 DOWNLOAD_SIZE_001 等可操作错误会被误导性文案掩盖。
func TestTransferFailurePreservesStructuredError(t *testing.T) {
	queue := newTestQueue(t, QueueOptions{
		PlanSource: singleItemPlan("https://retry.example/client.jar"),
		Commit:     instantCommit,
		Transfer: func(context.Context, Task) TransferResult {
			return TransferResult{
				Err:       newInputError(ErrorCodeSize, messageExpectedSizeInvalid),
				ErrorCode: ErrorCodeSize,
				Retryable: false,
			}
		},
	})
	if _, enqueueErr := queue.EnqueuePlan(context.Background(), StartDownloadPlanRequest{VersionID: "1.20.1"}); enqueueErr != nil {
		t.Fatalf("入队失败：%s", enqueueErr.Code)
	}
	children := waitForAllChildrenTerminal(t, queue, 10*time.Second)
	child := children[0]
	if child.State != StateFailed || child.ErrorCode != ErrorCodeSize {
		t.Fatalf("最终状态 = %s，错误码 = %s", child.State, child.ErrorCode)
	}
	if child.ErrorMessageSafe != messageExpectedSizeInvalid {
		t.Fatalf("错误文案 = %q，期望保留传输层的明确说明", child.ErrorMessageSafe)
	}
	if strings.Contains(child.ErrorMessageSafe, "传输尚未接入") {
		t.Fatalf("真实传输错误被覆盖：%q", child.ErrorMessageSafe)
	}
}

// TestTransferNotInjectedFailsExplicitly 校验未接入传输实现时任务明确失败。
func TestTransferNotInjectedFailsExplicitly(t *testing.T) {
	queue := newTestQueue(t, QueueOptions{
		PlanSource: singleItemPlan("https://local.example/client.jar"),
		Commit:     instantCommit,
	})
	if _, enqueueErr := queue.EnqueuePlan(context.Background(), StartDownloadPlanRequest{VersionID: "1.20.1"}); enqueueErr != nil {
		t.Fatalf("入队失败：%s", enqueueErr.Code)
	}
	children := waitForAllChildrenTerminal(t, queue, 10*time.Second)
	child := children[0]
	if child.State != StateFailed {
		t.Fatalf("最终状态 = %s，期望 failed", child.State)
	}
	if child.ErrorCode != ErrorCodeNetwork {
		t.Fatalf("错误码 = %s，期望 %s", child.ErrorCode, ErrorCodeNetwork)
	}
	if child.ErrorMessageSafe != messageTransferNotImplemented {
		t.Fatalf("错误文案 = %s，期望「传输尚未接入」的明确说明", child.ErrorMessageSafe)
	}
}

// TestCommitNotInjectedFailsExplicitly 校验未接入提交实现时任务不假装成功。
func TestCommitNotInjectedFailsExplicitly(t *testing.T) {
	queue := newTestQueue(t, QueueOptions{
		PlanSource: singleItemPlan("https://local.example/client.jar"),
		Transfer: func(context.Context, Task) TransferResult {
			return TransferResult{Downloaded: 1024, TotalSize: 1024}
		},
	})
	if _, enqueueErr := queue.EnqueuePlan(context.Background(), StartDownloadPlanRequest{VersionID: "1.20.1"}); enqueueErr != nil {
		t.Fatalf("入队失败：%s", enqueueErr.Code)
	}
	children := waitForAllChildrenTerminal(t, queue, 10*time.Second)
	if children[0].State != StateFailed {
		t.Fatalf("最终状态 = %s，期望 failed（未接入提交不得标记 completed）", children[0].State)
	}
}

// TestPlanSourceMissingReturnsArtifactPlanError 校验计划来源未接入时的错误。
func TestPlanSourceMissingReturnsArtifactPlanError(t *testing.T) {
	queue := newTestQueue(t, QueueOptions{Commit: instantCommit})
	_, planErr := queue.EnqueuePlan(context.Background(), StartDownloadPlanRequest{VersionID: "1.20.1"})
	if planErr == nil {
		t.Fatalf("期望报错")
	}
	if planErr.Code != ErrorCodePlan {
		t.Fatalf("错误码 = %s，期望 %s", planErr.Code, ErrorCodePlan)
	}
}

// TestPlanHashMismatchRejected 校验计划漂移检测。
func TestPlanHashMismatchRejected(t *testing.T) {
	queue := newTestQueue(t, QueueOptions{
		PlanSource: singleItemPlan("https://local.example/client.jar"),
		Commit:     instantCommit,
	})
	_, planErr := queue.EnqueuePlan(context.Background(), StartDownloadPlanRequest{VersionID: "1.20.1", PlanHash: "different"})
	if planErr == nil || planErr.Code != ErrorCodePlan {
		t.Fatalf("计划漂移应返回 %s，实际 %v", ErrorCodePlan, planErr)
	}
}

// TestEmptyPlanRejected 校验空计划被拒绝。
func TestEmptyPlanRejected(t *testing.T) {
	queue := newTestQueue(t, QueueOptions{PlanSource: fakePlanSource(), Commit: instantCommit})
	_, planErr := queue.EnqueuePlan(context.Background(), StartDownloadPlanRequest{VersionID: "1.20.1"})
	if planErr == nil || planErr.Code != ErrorCodePlan {
		t.Fatalf("空计划应返回 %s，实际 %v", ErrorCodePlan, planErr)
	}
}

// TestEnqueueDeduplicatesInFlightTask 校验运行中任务不再重复入队。
func TestEnqueueDeduplicatesInFlightTask(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	queue := newTestQueue(t, QueueOptions{
		PlanSource: singleItemPlan("https://dedupe.example/client.jar"),
		Commit:     instantCommit,
		Transfer: func(ctx context.Context, _ Task) TransferResult {
			select {
			case <-release:
			case <-ctx.Done():
			}
			return TransferResult{Downloaded: 1024, TotalSize: 1024}
		},
	})

	first, firstErr := queue.EnqueuePlan(context.Background(), StartDownloadPlanRequest{VersionID: "1.20.1"})
	if firstErr != nil {
		t.Fatalf("首次入队失败：%s", firstErr.Code)
	}
	if first.Duplicate {
		t.Fatalf("首次入队不应标记 duplicate")
	}
	waitUntil(t, 5*time.Second, "任务进入 downloading", func() bool {
		for _, child := range childTasks(snapshotTasks(queue)) {
			if child.State == StateDownloading {
				return true
			}
		}
		return false
	})

	second, secondErr := queue.EnqueuePlan(context.Background(), StartDownloadPlanRequest{VersionID: "1.20.1"})
	if secondErr != nil {
		t.Fatalf("重复入队失败：%s", secondErr.Code)
	}
	if !second.Duplicate {
		t.Fatalf("重复入队应标记 duplicate")
	}
	if second.TaskID != first.TaskID {
		t.Fatalf("重复入队应复用父任务：%s != %s", second.TaskID, first.TaskID)
	}
}

// TestPauseResumeCancelSemantics 校验暂停可恢复、取消是终态。
func TestPauseResumeCancelSemantics(t *testing.T) {
	queue := newTestQueue(t, QueueOptions{
		PlanSource: singleItemPlan("https://pause.example/client.jar"),
		Commit:     instantCommit,
		Transfer: func(ctx context.Context, _ Task) TransferResult {
			<-ctx.Done()
			return TransferResult{Err: ctx.Err(), Retryable: false, ErrorCode: ErrorCodeNetwork}
		},
	})
	accepted, enqueueErr := queue.EnqueuePlan(context.Background(), StartDownloadPlanRequest{VersionID: "1.20.1"})
	if enqueueErr != nil {
		t.Fatalf("入队失败：%s", enqueueErr.Code)
	}
	childID := ""
	waitUntil(t, 5*time.Second, "子任务进入 downloading", func() bool {
		children := childTasks(snapshotTasks(queue))
		if len(children) == 1 && children[0].State == StateDownloading {
			childID = children[0].ID
			return true
		}
		return false
	})
	if accepted.TaskID == "" {
		t.Fatalf("父任务 ID 为空")
	}

	paused, pauseErr := queue.Pause(childID)
	if pauseErr != nil {
		t.Fatalf("暂停失败：%s", pauseErr.Code)
	}
	if paused.State != string(StatePaused) {
		t.Fatalf("暂停后状态 = %s，期望 paused", paused.State)
	}
	if _, secondPauseErr := queue.Pause(childID); secondPauseErr == nil {
		t.Fatalf("重复暂停应被拒绝")
	}

	resumed, resumeErr := queue.Resume(childID)
	if resumeErr != nil {
		t.Fatalf("恢复失败：%s", resumeErr.Code)
	}
	if resumed.State != string(StateQueued) {
		t.Fatalf("恢复后状态 = %s，期望 queued", resumed.State)
	}

	cancelled, cancelErr := queue.Cancel(childID)
	if cancelErr != nil {
		t.Fatalf("取消失败：%s", cancelErr.Code)
	}
	if cancelled.State != string(StateCancelled) {
		t.Fatalf("取消后状态 = %s，期望 cancelled", cancelled.State)
	}
	// 取消是终态：再次恢复必须失败。
	if _, resumeAgainErr := queue.Resume(childID); resumeAgainErr == nil {
		t.Fatalf("按已取消任务恢复应被拒绝")
	}
	if _, cancelAgainErr := queue.Cancel(childID); cancelAgainErr == nil {
		t.Fatalf("重复取消应被拒绝")
	}
}

// TestCancelDuringCommitRejected 校验提交阶段不可取消（DOWNLOAD_CANCEL_001）。
func TestCancelDuringCommitRejected(t *testing.T) {
	commitRelease := make(chan struct{})
	defer close(commitRelease)
	queue := newTestQueue(t, QueueOptions{
		PlanSource: singleItemPlan("https://commit.example/client.jar"),
		Transfer: func(context.Context, Task) TransferResult {
			return TransferResult{Downloaded: 1024, TotalSize: 1024}
		},
		Commit: func(ctx context.Context, _ Task) *Error {
			select {
			case <-commitRelease:
			case <-ctx.Done():
			}
			return nil
		},
	})
	if _, enqueueErr := queue.EnqueuePlan(context.Background(), StartDownloadPlanRequest{VersionID: "1.20.1"}); enqueueErr != nil {
		t.Fatalf("入队失败：%s", enqueueErr.Code)
	}
	childID := ""
	waitUntil(t, 5*time.Second, "子任务进入 committing", func() bool {
		children := childTasks(snapshotTasks(queue))
		if len(children) == 1 && children[0].State == StateCommitting {
			childID = children[0].ID
			return true
		}
		return false
	})
	_, cancelErr := queue.Cancel(childID)
	if cancelErr == nil {
		t.Fatalf("提交阶段取消应被拒绝")
	}
	if cancelErr.Code != ErrorCodeCancel {
		t.Fatalf("错误码 = %s，期望 %s", cancelErr.Code, ErrorCodeCancel)
	}
}

// TestEventFiresAfterStatePersisted 校验「先落状态再发事件」。
func TestEventFiresAfterStatePersisted(t *testing.T) {
	var mu sync.Mutex
	mismatches := make([]string, 0, 4)
	// 先声明后赋值：事件回调需要读同一队列的当前状态。
	var queue *Queue
	queue = newTestQueue(t, QueueOptions{
		PlanSource: singleItemPlan("https://events.example/client.jar"),
		Commit:     instantCommit,
		Transfer: func(context.Context, Task) TransferResult {
			return TransferResult{Downloaded: 1024, TotalSize: 1024}
		},
		Sink: func(task Task, _ float64) {
			current, exists := queueSnapshotState(queue, task.ID)
			if !exists {
				return
			}
			if current != task.State {
				mu.Lock()
				mismatches = append(mismatches, fmt.Sprintf("事件状态 %s，实际 %s", task.State, current))
				mu.Unlock()
			}
		},
	})
	if _, enqueueErr := queue.EnqueuePlan(context.Background(), StartDownloadPlanRequest{VersionID: "1.20.1"}); enqueueErr != nil {
		t.Fatalf("入队失败：%s", enqueueErr.Code)
	}
	children := waitForAllChildrenTerminal(t, queue, 10*time.Second)
	if children[0].State != StateCompleted {
		t.Fatalf("最终状态 = %s，期望 completed", children[0].State)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(mismatches) > 0 {
		t.Fatalf("事件早于状态落库：%v", mismatches)
	}
}

// queueSnapshotState 读取队列内部某任务的当前状态。
func queueSnapshotState(queue *Queue, id string) (State, bool) {
	queue.mu.Lock()
	defer queue.mu.Unlock()
	task, exists := queue.tasks[id]
	if !exists {
		return "", false
	}
	return task.State, true
}

// TestListFilterAndLimit 校验列表过滤、排序与分页上限。
func TestListFilterAndLimit(t *testing.T) {
	queue := newTestQueue(t, QueueOptions{
		PlanSource: singleItemPlan("https://list.example/client.jar"),
		Commit:     instantCommit,
		Transfer: func(context.Context, Task) TransferResult {
			return TransferResult{Downloaded: 1024, TotalSize: 1024}
		},
	})
	if _, enqueueErr := queue.EnqueuePlan(context.Background(), StartDownloadPlanRequest{VersionID: "1.20.1"}); enqueueErr != nil {
		t.Fatalf("入队失败：%s", enqueueErr.Code)
	}
	waitForAllChildrenTerminal(t, queue, 10*time.Second)

	all := queue.List(DownloadTaskFilter{})
	// 1 个父任务 + 1 个子任务。
	if all.Total != 2 {
		t.Fatalf("任务总数 = %d，期望 2", all.Total)
	}
	completedOnly := queue.List(DownloadTaskFilter{States: []string{string(StateCompleted)}})
	// 父任务在全部子任务成功后也会进入 completed，因此这里有两条（父 + 子）。
	if completedOnly.Total != 2 {
		t.Fatalf("completed 过滤结果 = %d，期望 2", completedOnly.Total)
	}
	byteViews := 0
	for _, view := range completedOnly.Tasks {
		if view.TotalBytes != nil {
			byteViews++
			if *view.TotalBytes != 1024 {
				t.Fatalf("任务总字节数 = %d，期望 1024", *view.TotalBytes)
			}
			if view.ArtifactCount != 1 || view.CompletedCount != 1 {
				t.Fatalf("任务视图字段不正确：%+v", view)
			}
		}
	}
	// 父任务现在也会聚合子任务的字节数，因此父、子两条视图都带 TotalBytes。
	if byteViews != 2 {
		t.Fatalf("带总字节数的任务视图数 = %d，期望 2", byteViews)
	}
	// limit 超过上限时被收敛到 100，不会返回错误。
	capped := queue.List(DownloadTaskFilter{Limit: 500})
	if capped.Total != 2 {
		t.Fatalf("Total = %d，期望 2", capped.Total)
	}
	// offset 超过总数时返回空页而不是 panic。
	empty := queue.List(DownloadTaskFilter{Offset: 99})
	if len(empty.Tasks) != 0 || empty.Total != 2 {
		t.Fatalf("越界分页结果不正确：%+v", empty)
	}
}

// TestBackoffDelaySequence 校验 1s → 2s → 4s → 上限的退避序列。
func TestBackoffDelaySequence(t *testing.T) {
	queue := NewQueue(QueueOptions{BackoffBase: time.Second, BackoffMax: 5 * time.Second})
	cases := []struct {
		attempt int
		want    time.Duration
	}{
		{attempt: 1, want: time.Second},
		{attempt: 2, want: 2 * time.Second},
		{attempt: 3, want: 4 * time.Second},
		{attempt: 4, want: 5 * time.Second},
	}
	for _, testCase := range cases {
		if got := queue.backoffDelay(testCase.attempt); got != testCase.want {
			t.Errorf("第 %d 次退避 = %s，期望 %s", testCase.attempt, got, testCase.want)
		}
	}
}

func TestMaxRetriesZeroDisablesRetry(t *testing.T) {
	var attempts int32
	queue := newTestQueue(t, QueueOptions{
		PlanSource: singleItemPlan("https://retry.example/client.jar"),
		Commit:     instantCommit,
		MaxRetries: 0,
		Transfer: func(context.Context, Task) TransferResult {
			atomic.AddInt32(&attempts, 1)
			return TransferResult{Err: context.DeadlineExceeded, Retryable: true, ErrorCode: ErrorCodeNetwork}
		},
	})
	if _, enqueueErr := queue.EnqueuePlan(context.Background(), StartDownloadPlanRequest{VersionID: "1.20.1"}); enqueueErr != nil {
		t.Fatalf("入队失败：%s", enqueueErr.Code)
	}
	children := waitForAllChildrenTerminal(t, queue, 10*time.Second)
	if children[0].State != StateFailed {
		t.Fatalf("最终状态 = %s，期望 failed", children[0].State)
	}
	if got := atomic.LoadInt32(&attempts); got != 1 {
		t.Fatalf("传输调用次数 = %d，期望 1", got)
	}
}

// TestQueueDegradesWhenStoreUnavailable 校验存储不可用时降级为内存模式。
func TestQueueDegradesWhenStoreUnavailable(t *testing.T) {
	originalFactory := taskStoreFactory
	taskStoreFactory = func(context.Context) (TaskStore, *Error) {
		return nil, newSystemError(ErrorCodeDatabaseMigration, messageDatabaseUnavailable, false)
	}
	t.Cleanup(func() { taskStoreFactory = originalFactory })

	queue := NewQueue(QueueOptions{
		PlanSource:  singleItemPlan("https://degrade.example/client.jar"),
		Commit:      instantCommit,
		BackoffBase: time.Millisecond,
		Transfer: func(context.Context, Task) TransferResult {
			return TransferResult{Downloaded: 1024, TotalSize: 1024}
		},
	})
	if startErr := queue.Start(context.Background()); startErr != nil {
		t.Fatalf("存储不可用时队列应照常启动，实际失败：%s", startErr.Code)
	}
	t.Cleanup(func() { _ = queue.Close() })
	if !queue.StorageDegraded() {
		t.Fatalf("应标记为降级模式")
	}
	if _, enqueueErr := queue.EnqueuePlan(context.Background(), StartDownloadPlanRequest{VersionID: "1.20.1"}); enqueueErr != nil {
		t.Fatalf("降级模式下仍应能入队：%s", enqueueErr.Code)
	}
	waitForAllChildrenTerminal(t, queue, 10*time.Second)
}

// TestSQLiteTaskStoreRoundTrip 校验任务表读写往返与幂等更新。
func TestSQLiteTaskStoreRoundTrip(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "app.db")
	db, openErr := database.Open(dbPath)
	if openErr != nil {
		t.Fatalf("打开数据库失败：%v", openErr)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, migrateErr := database.Migrate(context.Background(), db); migrateErr != nil {
		t.Fatalf("迁移失败：%v", migrateErr)
	}

	store := NewSQLiteTaskStore(db)
	ctx := context.Background()
	task := Task{
		ID:               "task-round-trip",
		ParentTaskID:     "parent-1",
		ArtifactID:       "artifact-1",
		OperationID:      "inst-demo",
		TaskType:         TaskTypeClientJar,
		State:            StateQueued,
		SourceURI:        "https://source.example/client.jar",
		TargetRelPath:    "versions/1.20.1/1.20.1.jar",
		TargetKind:       TargetKindVersion,
		ExpectedSize:     2048,
		DownloadedSize:   0,
		ExpectedSHA1:     "sha1-value",
		ExpectedSHA256:   "sha256-value",
		PlanHash:         "plan-hash",
		CreatedAtMs:      1000,
		UpdatedAtMs:      1000,
		ErrorMessageSafe: "",
	}
	if saveErr := store.Save(ctx, task); saveErr != nil {
		t.Fatalf("首次保存失败：%v", saveErr)
	}
	task.State = StateDownloading
	task.DownloadedSize = 1024
	task.RetryCount = 1
	task.UpdatedAtMs = 2000
	if saveErr := store.Save(ctx, task); saveErr != nil {
		t.Fatalf("更新保存失败：%v", saveErr)
	}

	loaded, loadErr := store.LoadAll(ctx)
	if loadErr != nil {
		t.Fatalf("读取失败：%v", loadErr)
	}
	if len(loaded) != 1 {
		t.Fatalf("任务行数 = %d，期望 1（重复保存不得插第二行）", len(loaded))
	}
	got := loaded[0]
	if got.State != StateDownloading || got.DownloadedSize != 1024 || got.RetryCount != 1 {
		t.Fatalf("读回的任务未反映更新：%+v", got)
	}
	if got.SourceURI != "https://source.example/client.jar" {
		t.Fatalf("源地址未持久化：%s", got.SourceURI)
	}
	if got.ExpectedSHA1 != "sha1-value" || got.ExpectedSHA256 != "sha256-value" {
		t.Fatalf("哈希未持久化：%+v", got)
	}
	if got.OperationID != "inst-demo" {
		t.Fatalf("operationId 未持久化：%q", got.OperationID)
	}
}

// errTestNotFound 是一个用于「不可重试失败」的哨兵错误。
var errTestNotFound = fmt.Errorf("下载源返回 404")
