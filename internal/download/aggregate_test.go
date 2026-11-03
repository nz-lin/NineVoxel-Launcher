package download

import (
	"context"
	"testing"
	"time"
)

// TestPlanViewAggregatesChildBytesAndSpeed 验证父任务视图用子任务字节数聚合。
func TestPlanViewAggregatesChildBytesAndSpeed(t *testing.T) {
	queue := newTestQueue(t, QueueOptions{
		PlanSource: fakePlanSource(
			PlanItem{TaskType: TaskTypeClientJar, SourceURI: "https://a.example/a.jar", TargetRelPath: "versions/a.jar", ExpectedSize: 100},
			PlanItem{TaskType: TaskTypeLibrary, SourceURI: "https://b.example/b.jar", TargetRelPath: "libraries/b.jar", ExpectedSize: 200},
		),
		Transfer: func(_ context.Context, task Task) TransferResult {
			return TransferResult{Downloaded: task.ExpectedSize, TotalSize: task.ExpectedSize}
		},
		Commit: instantCommit,
	})
	accepted, enqueueErr := queue.EnqueueTransferPlan(
		context.Background(),
		StartDownloadPlanRequest{VersionID: "1.20.1", OperationID: "op-1"},
		TransferPlan{VersionID: "1.20.1", PlanHash: "plan-hash-1", OperationID: "op-1", Items: []PlanItem{
			{TaskType: TaskTypeClientJar, SourceURI: "https://a.example/a.jar", TargetRelPath: "versions/a.jar", ExpectedSize: 100},
			{TaskType: TaskTypeLibrary, SourceURI: "https://b.example/b.jar", TargetRelPath: "libraries/b.jar", ExpectedSize: 200},
		}},
	)
	if enqueueErr != nil {
		t.Fatalf("入队失败：%s", enqueueErr.Code)
	}
	waitForAllChildrenTerminal(t, queue, 10*time.Second)

	view, appErr := queue.Get(accepted.TaskID)
	if appErr != nil {
		t.Fatalf("读取父任务失败：%s", appErr.Code)
	}
	if view.TotalBytes == nil || *view.TotalBytes != 300 {
		t.Fatalf("总字节数 = %v，期望 300", view.TotalBytes)
	}
	if view.DownloadedBytes != 300 {
		t.Fatalf("已下载字节数 = %d，期望 300", view.DownloadedBytes)
	}
	if view.Progress == nil || *view.Progress != 1 {
		t.Fatalf("进度 = %v，期望 1", view.Progress)
	}
	if view.OperationID != "op-1" {
		t.Fatalf("operationId = %q，期望 op-1", view.OperationID)
	}
	if view.ArtifactCount != 2 || view.CompletedCount != 2 {
		t.Fatalf("制品进度 = %d/%d，期望 2/2", view.CompletedCount, view.ArtifactCount)
	}
}

// TestApplyTransferResultComputesSpeed 验证单任务速度采样。
func TestApplyTransferResultComputesSpeed(t *testing.T) {
	nowMs := int64(1000)
	queue := NewQueue(QueueOptions{Now: func() int64 { return nowMs }})
	queue.tasks["task-1"] = &Task{
		ID:              "task-1",
		State:           StateDownloading,
		ExpectedSize:    2048,
		DownloadedSize:  0,
		lastSampleAtMs:  1000,
		lastSampleBytes: 0,
	}
	nowMs = 2000
	queue.applyTransferResult("task-1", TransferResult{Downloaded: 2048, TotalSize: 2048})

	queue.mu.Lock()
	speed := queue.tasks["task-1"].SpeedBytesPerSecond
	queue.mu.Unlock()
	if speed != 2048 {
		t.Fatalf("速度 = %d B/s，期望 2048", speed)
	}
}

// TestClearCompletedRemovesCompletedTasks 验证清除已完成任务会同时清理父子记录。
func TestClearCompletedRemovesCompletedTasks(t *testing.T) {
	queue := newTestQueue(t, QueueOptions{
		PlanSource: singleItemPlan("https://clear.example/client.jar"),
		Transfer: func(_ context.Context, task Task) TransferResult {
			return TransferResult{Downloaded: task.ExpectedSize, TotalSize: task.ExpectedSize}
		},
		Commit: instantCommit,
	})
	if _, err := queue.EnqueuePlan(context.Background(), StartDownloadPlanRequest{VersionID: "1.20.1"}); err != nil {
		t.Fatalf("入队失败：%s", err.Code)
	}
	waitForAllChildrenTerminal(t, queue, 10*time.Second)
	if total := queue.List(DownloadTaskFilter{}).Total; total != 2 {
		t.Fatalf("清理前任务数 = %d，期望 2", total)
	}
	removed := queue.ClearCompleted(context.Background())
	if removed != 2 {
		t.Fatalf("清理数量 = %d，期望 2", removed)
	}
	if total := queue.List(DownloadTaskFilter{}).Total; total != 0 {
		t.Fatalf("清理后任务数 = %d，期望 0", total)
	}
}

// TestCancelOperationCancelsPlan 验证按 operationId 取消整次安装。
func TestCancelOperationCancelsPlan(t *testing.T) {
	queue := newTestQueue(t, QueueOptions{
		PlanSource: singleItemPlan("https://cancel.example/client.jar"),
		Transfer: func(ctx context.Context, _ Task) TransferResult {
			<-ctx.Done()
			return TransferResult{Err: ctx.Err(), ErrorCode: ErrorCodeNetwork, Retryable: false}
		},
		Commit: instantCommit,
	})
	accepted, enqueueErr := queue.EnqueueTransferPlan(
		context.Background(),
		StartDownloadPlanRequest{VersionID: "1.20.1", OperationID: "op-cancel"},
		TransferPlan{VersionID: "1.20.1", PlanHash: "plan-hash-1", OperationID: "op-cancel", Items: []PlanItem{
			{TaskType: TaskTypeClientJar, SourceURI: "https://cancel.example/client.jar", TargetRelPath: "versions/client.jar", ExpectedSize: 1024},
		}},
	)
	if enqueueErr != nil {
		t.Fatalf("入队失败：%s", enqueueErr.Code)
	}
	time.Sleep(20 * time.Millisecond)
	if cancelled := queue.CancelOperation("op-cancel"); cancelled != 1 {
		t.Fatalf("取消计划数 = %d，期望 1", cancelled)
	}
	waitForAllChildrenTerminal(t, queue, 10*time.Second)
	view, appErr := queue.Get(accepted.TaskID)
	if appErr != nil {
		t.Fatalf("读取父任务失败：%s", appErr.Code)
	}
	if view.State != string(StateCancelled) {
		t.Fatalf("父任务状态 = %q，期望 cancelled", view.State)
	}
}
