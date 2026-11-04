package instance

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 本文件覆盖「删除实例时目录被占用」的三条防护：
//   - 运行中的游戏提前拒绝，而不是等到改名失败
//   - 短暂占用（杀毒扫描）由重试吸收
//   - 失败归因区分「占用」与「权限」

// TestDescribeRenameFailureDistinguishesLockFromPermission 验证归因区分占用与权限。
//
// 「Access is denied」在 Windows 上两种成因共用同一个错误文本，
// 只看文本会把占用误报成权限问题，让用户去查文件权限。
func TestDescribeRenameFailureDistinguishesLockFromPermission(t *testing.T) {
	// 目录可写 → 说明 ACL 没问题，问题在占用。
	writable := t.TempDir()
	got := describeRenameFailure(writable, errors.New("rename a b: Access is denied."))
	if !strings.Contains(got, "占用") {
		t.Fatalf("可写目录的 Access denied 应归因为占用，实际 %q", got)
	}

	// 目录不可写 → 权限。用不存在的路径模拟。
	missing := filepath.Join(t.TempDir(), "absent", "nested")
	got = describeRenameFailure(missing, errors.New("rename a b: Access is denied."))
	if !strings.Contains(got, "写入权限") {
		t.Fatalf("不可写路径应归因为权限，实际 %q", got)
	}

	// 明确的占用文本不需要探测即可归因。
	got = describeRenameFailure(writable,
		errors.New("The process cannot access the file because it is being used by another process."))
	if !strings.Contains(got, "仍被占用") {
		t.Fatalf("占用文本应直接归因为占用，实际 %q", got)
	}

	// 跨卷有专门提示。
	got = describeRenameFailure(writable, errors.New("invalid cross-device link"))
	if !strings.Contains(got, "同一磁盘卷") {
		t.Fatalf("跨卷应单独提示，实际 %q", got)
	}
}

// TestRenameWithRetrySucceedsImmediately 验证正常改名不受重试逻辑拖慢。
func TestRenameWithRetrySucceedsImmediately(t *testing.T) {
	parent := t.TempDir()
	source := filepath.Join(parent, "source")
	if err := os.Mkdir(source, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(parent, "target")

	started := time.Now()
	if err := renameWithRetry(source, target); err != nil {
		t.Fatalf("改名失败：%v", err)
	}
	// 5 次重试的退避总量是 1.2 秒；正常路径必须远小于此，
	// 否则用户每次删除实例都要多等一秒多。
	if elapsed := time.Since(started); elapsed > 300*time.Millisecond {
		t.Fatalf("首次成功耗时 %v，说明无谓地等待了", elapsed)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("目标不存在：%v", err)
	}
}

// TestRenameWithRetryReturnsErrorWhenTargetExists 验证重试用尽后如实返回错误。
func TestRenameWithRetryReturnsErrorWhenTargetExists(t *testing.T) {
	parent := t.TempDir()
	source := filepath.Join(parent, "source")
	target := filepath.Join(parent, "target")
	for _, path := range []string{source, target} {
		if err := os.Mkdir(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := renameWithRetry(source, target); err == nil {
		t.Fatal("目标已存在时应返回错误")
	}
}

// TestDirectoryWritable 验证目录可写探测。
func TestDirectoryWritable(t *testing.T) {
	if !directoryWritable(t.TempDir()) {
		t.Fatal("临时目录应可写")
	}
	if directoryWritable(filepath.Join(t.TempDir(), "absent")) {
		t.Fatal("不存在的目录应判为不可写")
	}
}

// TestDeleteInstanceRefusesWhileGameRunning 验证运行中的游戏会被提前拒绝。
//
// 意义是避免「先标记清单、再改名失败、再回滚」的来回折腾，
// 也避免把占用误报成权限问题。
func TestDeleteInstanceRefusesWhileGameRunning(t *testing.T) {
	s := testService(t)
	redirectLaunchDataRoot(t, s)
	item, err := s.CreateInstance(testConfig("运行中实例"))
	if err != nil {
		t.Fatal(err)
	}
	item.VersionID = "1.20.1-forge-47.3.12"
	item.InstallationState = "installed"
	if saveErr := s.saveLocked(item); saveErr != nil {
		t.Fatal(saveErr)
	}

	// 写入一条活动租约，模拟游戏正在运行。
	writeLease(t, s.root, item.VersionID, 4242, 1000)

	if _, deleteErr := s.deleteInstanceLocked(item); deleteErr == nil {
		t.Fatal("游戏运行时应拒绝删除")
	} else if !strings.Contains(deleteErr.Error(), "仍在运行") {
		t.Fatalf("错误应说明游戏仍在运行，实际 %v", deleteErr)
	}
	// 目录必须完好，不能被标记成已归档。
	instanceDir := filepath.Join(s.root, "instances", item.ID)
	if _, statErr := os.Stat(instanceDir); statErr != nil {
		t.Fatalf("实例目录不应被改动：%v", statErr)
	}
}
