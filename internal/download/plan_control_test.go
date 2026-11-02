package download

import (
	"context"
	"testing"
	"time"
)

func TestPlanPauseResumeAndCancelControlsChildren(t *testing.T) {
	q := newTestQueue(t, QueueOptions{Commit: instantCommit, Transfer: func(ctx context.Context, _ Task) TransferResult { <-ctx.Done(); return TransferResult{Err: ctx.Err()} }})
	plan := TransferPlan{VersionID: "1.21", PlanHash: "pause-plan", Items: []PlanItem{
		{TaskType: TaskTypeLibrary, SourceURI: "https://example.com/a.jar", TargetRelPath: "artifacts/objects/aa/a", ExpectedSize: 10},
		{TaskType: TaskTypeClientJar, SourceURI: "https://example.com/b.jar", TargetRelPath: "versions/1.21/1.21.jar", ExpectedSize: 10},
	}}
	accepted, err := q.EnqueueTransferPlan(context.Background(), StartDownloadPlanRequest{VersionID: plan.VersionID}, plan)
	if err != nil {
		t.Fatal(err)
	}
	waitUntil(t, time.Second, "children downloading", func() bool {
		count := 0
		for _, task := range childTasks(snapshotTasks(q)) {
			if task.State == StateDownloading {
				count++
			}
		}
		return count == 2
	})
	paused, pauseErr := q.Pause(accepted.TaskID)
	if pauseErr != nil || paused.State != "paused" {
		t.Fatalf("parent pause: %+v %v", paused, pauseErr)
	}
	for _, task := range childTasks(snapshotTasks(q)) {
		if task.State != StatePaused {
			t.Fatalf("child not paused: %+v", task)
		}
	}
	if _, err := q.Resume(accepted.TaskID); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, time.Second, "resumed children downloading", func() bool {
		count := 0
		for _, task := range childTasks(snapshotTasks(q)) {
			if task.State == StateDownloading {
				count++
			}
		}
		return count == 2
	})
	if _, err := q.Cancel(accepted.TaskID); err != nil {
		t.Fatal(err)
	}
	for _, task := range childTasks(snapshotTasks(q)) {
		if task.State != StateCancelled {
			t.Fatalf("child not cancelled: %+v", task)
		}
	}
}

func TestSharedLibraryDoesNotDeduplicateDifferentVersions(t *testing.T) {
	q := newTestQueue(t, QueueOptions{Commit: instantCommit, Transfer: func(ctx context.Context, _ Task) TransferResult { <-ctx.Done(); return TransferResult{Err: ctx.Err()} }})
	common := PlanItem{TaskType: TaskTypeLibrary, SourceURI: "https://example.com/library", TargetRelPath: "artifacts/objects/aa/shared", ExpectedSize: 10, ExpectedSHA1: "a"}
	first := TransferPlan{VersionID: "1.20", PlanHash: "first", Items: []PlanItem{common}}
	second := TransferPlan{VersionID: "1.21", PlanHash: "second", Items: []PlanItem{common, {TaskType: TaskTypeClientJar, SourceURI: "https://example.com/client", TargetRelPath: "versions/1.21/1.21.jar", ExpectedSize: 10}}}
	accepted, err := q.EnqueueTransferPlan(context.Background(), StartDownloadPlanRequest{VersionID: first.VersionID}, first)
	if err != nil {
		t.Fatal(err)
	}
	waitUntil(t, time.Second, "first transfer started", func() bool {
		for _, task := range childTasks(snapshotTasks(q)) {
			if task.State == StateDownloading {
				return true
			}
		}
		return false
	})
	next, err := q.EnqueueTransferPlan(context.Background(), StartDownloadPlanRequest{VersionID: second.VersionID}, second)
	if err != nil {
		t.Fatal(err)
	}
	if next.Duplicate || next.TaskID == accepted.TaskID {
		t.Fatal("different version reused an incomplete plan")
	}
}

func TestRestoredPlanDoesNotBlockInstallationRetry(t *testing.T) {
	q := newTestQueue(t, QueueOptions{})
	q.mu.Lock()
	q.tasks["old-parent"] = &Task{ID: "old-parent", TaskType: TaskTypePlan, State: StateQueued, VersionID: "1.21", PlanHash: "retry", ExpectedSize: 1}
	q.tasks["old-child"] = &Task{ID: "old-child", ParentTaskID: "old-parent", TaskType: TaskTypeLibrary, State: StatePaused, TargetRelPath: "artifacts/objects/aa/a", ExpectedSize: 10}
	q.mu.Unlock()
	plan := TransferPlan{VersionID: "1.21", PlanHash: "retry", Items: []PlanItem{{TaskType: TaskTypeLibrary, SourceURI: "https://example.com/lib", TargetRelPath: "artifacts/objects/aa/a", ExpectedSize: 10}}}
	next, err := q.EnqueueTransferPlan(context.Background(), StartDownloadPlanRequest{VersionID: plan.VersionID}, plan)
	if err != nil {
		t.Fatal(err)
	}
	if next.Duplicate || next.TaskID == "old-parent" {
		t.Fatal("retry reused a restored plan with no scheduled workers")
	}
}

func TestPartialCancellationCannotMarkPlanCompleted(t *testing.T) {
	q := newTestQueue(t, QueueOptions{})
	q.mu.Lock()
	q.tasks["parent"] = &Task{ID: "parent", TaskType: TaskTypePlan, State: StateQueued}
	q.tasks["completed"] = &Task{ID: "completed", ParentTaskID: "parent", State: StateCompleted}
	q.tasks["cancelled"] = &Task{ID: "cancelled", ParentTaskID: "parent", State: StateCancelled}
	q.mu.Unlock()
	q.bumpParent("cancelled")
	parent, _ := q.snapshotOf("parent")
	if parent.State != StateCancelled {
		t.Fatalf("incomplete plan marked %s", parent.State)
	}
}
