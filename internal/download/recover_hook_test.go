package download

// 本文件验证「启动期恢复接线」这一个薄层：钩子恒返回 nil、恢复确实落库、
// 连续两次启动幂等、超时不会拖住启动。
//
// 恢复本身的判定矩阵已在 commit_test.go 覆盖，这里不重复。
// 所有用例都注入临时数据根，不触碰真实数据目录。

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"example.com/ninevoxel/launcher/internal/artifact"
	"github.com/wailsapp/wails/v3/pkg/application"
)

// hookHashes 计算测试内容的 SHA-1 与 SHA-256。
func hookHashes(content []byte) (string, string) {
	sum1 := sha1.Sum(content)
	sum256 := sha256.Sum256(content)
	return hex.EncodeToString(sum1[:]), hex.EncodeToString(sum256[:])
}

// hookTask 构造一个处于提交中断状态的任务。
func hookTask(id, targetRel string, content []byte) Task {
	sum1, sum256 := hookHashes(content)
	return Task{
		ID:             id,
		TaskType:       TaskTypeClientJar,
		State:          StateCommitting,
		TargetRelPath:  targetRel,
		TargetKind:     TargetKindVersion,
		ExpectedSize:   int64(len(content)),
		ExpectedSHA1:   sum1,
		ExpectedSHA256: sum256,
		CreatedAtMs:    1700000000000,
		UpdatedAtMs:    1700000000000,
	}
}

// stubStartupRecovery 替换启动恢复的依赖，并在用例结束时还原。
//
// 必须还原：这些是包级变量，残留会污染同包其它测试。
func stubStartupRecovery(
	t *testing.T,
	dataRoot func() (string, *artifact.Error),
	store func(context.Context) (TaskStore, *Error),
) {
	t.Helper()
	previousRoot := startupRecoveryDataRoot
	previousStore := startupRecoveryStore
	previousRun := startupRecoveryRun
	previousTimeout := startupRecoveryTimeout
	t.Cleanup(func() {
		startupRecoveryDataRoot = previousRoot
		startupRecoveryStore = previousStore
		startupRecoveryRun = previousRun
		startupRecoveryTimeout = previousTimeout
	})
	if dataRoot != nil {
		startupRecoveryDataRoot = dataRoot
	}
	if store != nil {
		startupRecoveryStore = store
	}
	// 测试里用不到 30 秒的长时限，但也不能短到误伤正常磁盘操作。
	startupRecoveryTimeout = 5 * time.Second
}

// TestServiceStartupReturnsNilWhenDataRootUnavailable 数据根不可用时钩子仍返回 nil。
func TestServiceStartupReturnsNilWhenDataRootUnavailable(t *testing.T) {
	stubStartupRecovery(t,
		func() (string, *artifact.Error) {
			return "", &artifact.Error{Code: "ARTIFACT_LAYOUT_001", Message: "数据根不可用"}
		},
		func(context.Context) (TaskStore, *Error) {
			t.Fatal("数据根不可用时不应再去打开存储")
			return nil, nil
		},
	)

	service := &DownloadService{}
	if err := service.ServiceStartup(context.Background(), application.ServiceOptions{}); err != nil {
		t.Fatalf("启动钩子必须返回 nil，实际返回 %v", err)
	}
}

// TestServiceStartupReturnsNilWhenStoreUnavailable 存储不可用时钩子仍返回 nil。
func TestServiceStartupReturnsNilWhenStoreUnavailable(t *testing.T) {
	stubStartupRecovery(t,
		func() (string, *artifact.Error) { return t.TempDir(), nil },
		func(context.Context) (TaskStore, *Error) {
			return nil, newSystemError(ErrorCodeDatabaseMigration, messageDatabaseUnavailable, false)
		},
	)

	service := &DownloadService{}
	if err := service.ServiceStartup(context.Background(), application.ServiceOptions{}); err != nil {
		t.Fatalf("启动钩子必须返回 nil，实际返回 %v", err)
	}
}

// TestServiceStartupRecoversPersistedTask 提交中断且目标已落盘的任务应被补记为完成。
func TestServiceStartupRecoversPersistedTask(t *testing.T) {
	dataRoot := t.TempDir()
	store := newMemoryTaskStore()
	content := []byte("downloaded client jar")
	task := hookTask("task-hook-1", "versions/1.20.1/1.20.1.jar", content)

	targetAbs := filepath.Join(dataRoot, filepath.FromSlash(task.TargetRelPath))
	if err := os.MkdirAll(filepath.Dir(targetAbs), 0o755); err != nil {
		t.Fatalf("创建目标目录失败: %v", err)
	}
	if err := os.WriteFile(targetAbs, content, 0o644); err != nil {
		t.Fatalf("预置目标文件失败: %v", err)
	}
	if err := store.Save(context.Background(), task); err != nil {
		t.Fatalf("预置任务失败: %v", err)
	}

	stubStartupRecovery(t,
		func() (string, *artifact.Error) { return dataRoot, nil },
		func(context.Context) (TaskStore, *Error) { return store, nil },
	)

	service := &DownloadService{}
	if err := service.ServiceStartup(context.Background(), application.ServiceOptions{}); err != nil {
		t.Fatalf("启动钩子必须返回 nil，实际返回 %v", err)
	}

	tasks, loadErr := store.LoadAll(context.Background())
	if loadErr != nil {
		t.Fatalf("读取恢复后的任务失败: %v", loadErr)
	}
	if len(tasks) != 1 {
		t.Fatalf("任务数量 = %d，期望 1", len(tasks))
	}
	if tasks[0].State != StateCompleted {
		t.Fatalf("恢复后状态 = %s，期望 %s", tasks[0].State, StateCompleted)
	}
	if tasks[0].VerifiedAtMs == 0 {
		t.Fatal("补记为完成时必须同时写入校验时间")
	}
}

// TestStartupRecoveryIsIdempotent 连续两次启动不得重复或破坏任务。
func TestStartupRecoveryIsIdempotent(t *testing.T) {
	dataRoot := t.TempDir()
	store := newMemoryTaskStore()
	content := []byte("idempotent payload")
	task := hookTask("task-hook-2", "versions/1.20.1/1.20.1.jar", content)

	targetAbs := filepath.Join(dataRoot, filepath.FromSlash(task.TargetRelPath))
	if err := os.MkdirAll(filepath.Dir(targetAbs), 0o755); err != nil {
		t.Fatalf("创建目标目录失败: %v", err)
	}
	if err := os.WriteFile(targetAbs, content, 0o644); err != nil {
		t.Fatalf("预置目标文件失败: %v", err)
	}
	if err := store.Save(context.Background(), task); err != nil {
		t.Fatalf("预置任务失败: %v", err)
	}

	stubStartupRecovery(t,
		func() (string, *artifact.Error) { return dataRoot, nil },
		func(context.Context) (TaskStore, *Error) { return store, nil },
	)

	service := &DownloadService{}
	if err := service.ServiceStartup(context.Background(), application.ServiceOptions{}); err != nil {
		t.Fatalf("第一次启动失败: %v", err)
	}
	first, loadErr := store.LoadAll(context.Background())
	if loadErr != nil {
		t.Fatalf("第一次读取任务失败: %v", loadErr)
	}

	if err := service.ServiceStartup(context.Background(), application.ServiceOptions{}); err != nil {
		t.Fatalf("第二次启动失败: %v", err)
	}
	second, loadErr := store.LoadAll(context.Background())
	if loadErr != nil {
		t.Fatalf("第二次读取任务失败: %v", loadErr)
	}

	if len(second) != len(first) {
		t.Fatalf("两次启动后任务数量从 %d 变为 %d", len(first), len(second))
	}
	if second[0].ID != task.ID || second[0].State != StateCompleted {
		t.Fatalf("第二次启动破坏了任务：id=%s state=%s", second[0].ID, second[0].State)
	}
	if second[0].CompletedAtMs != first[0].CompletedAtMs {
		t.Fatal("幂等的二次启动不应改写完成时间")
	}
}

// TestStartupRecoveryHonorsTimeout 恢复卡住时必须在超时后放弃并返回。
func TestStartupRecoveryHonorsTimeout(t *testing.T) {
	block := make(chan struct{})
	defer close(block)

	stubStartupRecovery(t,
		func() (string, *artifact.Error) { return t.TempDir(), nil },
		func(context.Context) (TaskStore, *Error) { return newMemoryTaskStore(), nil },
	)
	startupRecoveryRun = func(_ context.Context, _ RecoveryOptions) (RecoveryResult, *Error) {
		// 模拟一个卡在磁盘上的恢复：直到用例结束才返回。
		<-block
		return RecoveryResult{}, nil
	}
	startupRecoveryTimeout = 50 * time.Millisecond

	started := time.Now()
	runStartupRecovery()
	elapsed := time.Since(started)

	if elapsed > 3*time.Second {
		t.Fatalf("启动期恢复必须受超时约束，实际耗时 %s", elapsed)
	}
	if elapsed < startupRecoveryTimeout {
		t.Fatalf("耗时 %s 短于超时设定，可能没有真正等待恢复结果", elapsed)
	}
}

// TestStartupRecoverySwallowsRecoveryError 恢复返回错误时不得向上传播。
func TestStartupRecoverySwallowsRecoveryError(t *testing.T) {
	stubStartupRecovery(t,
		func() (string, *artifact.Error) { return t.TempDir(), nil },
		func(context.Context) (TaskStore, *Error) { return newMemoryTaskStore(), nil },
	)
	startupRecoveryRun = func(_ context.Context, _ RecoveryOptions) (RecoveryResult, *Error) {
		return RecoveryResult{Recovered: 3}, newSystemError(ErrorCodeCommit, messageRecoverySaveFailed, false)
	}

	// 只要不 panic、不长时间阻塞即可；错误由日志承载。
	done := make(chan struct{})
	go func() {
		defer close(done)
		runStartupRecovery()
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("恢复失败时应当立即返回，不得阻塞启动")
	}
}
