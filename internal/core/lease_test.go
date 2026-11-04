package core

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// stubProcess 替换进程探测，避免测试依赖真实进程。
//
// 返回 restore 供 defer 调用。
func stubProcess(fn func(pid uint32) (processStateValue, int64)) func() {
	original := processStartTime
	processStartTime = fn
	return func() { processStartTime = original }
}

// ===== 租约登记与释放 =====

// TestLeaseAcquireAndRelease 验证租约的登记与释放。
func TestLeaseAcquireAndRelease(t *testing.T) {
	manager := NewLeaseManager(t.TempDir())

	lease, err := manager.Acquire("26.3", LeaseRecord{PID: 100, ProcessStartMs: 1000})
	if err != nil {
		t.Fatalf("登记租约失败: %v", err)
	}
	if !manager.Active("26.3") {
		t.Fatal("登记后应处于活动状态")
	}
	if lease.VersionID() != "26.3" {
		t.Fatalf("租约版本期望 26.3，实际 %q", lease.VersionID())
	}

	lease.Release()
	if manager.Active("26.3") {
		t.Fatal("释放后不应再处于活动状态")
	}
}

// TestLeaseReleaseIdempotent 验证重复释放安全。
func TestLeaseReleaseIdempotent(t *testing.T) {
	manager := NewLeaseManager(t.TempDir())
	lease, err := manager.Acquire("26.3", LeaseRecord{PID: 100, ProcessStartMs: 1000})
	if err != nil {
		t.Fatalf("登记租约失败: %v", err)
	}
	lease.Release()
	lease.Release()
}

// TestLeaseRejectsDuplicateVersion 验证同一版本不能重复登记。
//
// 无法区分「上次残留」与「并发启动」时应保守拒绝，让调用方提示用户。
func TestLeaseRejectsDuplicateVersion(t *testing.T) {
	manager := NewLeaseManager(t.TempDir())
	if _, err := manager.Acquire("26.3", LeaseRecord{PID: 100, ProcessStartMs: 1000}); err != nil {
		t.Fatalf("首次登记失败: %v", err)
	}
	if _, err := manager.Acquire("26.3", LeaseRecord{PID: 200, ProcessStartMs: 2000}); err == nil {
		t.Fatal("重复登记期望被拒绝")
	}
}

// TestLeaseRejectsEmptyVersion 验证空版本标识被拒绝。
func TestLeaseRejectsEmptyVersion(t *testing.T) {
	manager := NewLeaseManager(t.TempDir())
	if _, err := manager.Acquire("", LeaseRecord{PID: 1}); err == nil {
		t.Fatal("空版本标识期望被拒绝")
	}
}

// TestLeasePersistsAndReloads 验证租约落盘后可被恢复读取。
func TestLeasePersistsAndReloads(t *testing.T) {
	root := t.TempDir()
	manager := NewLeaseManager(root)
	lease, err := manager.Acquire("26.3", LeaseRecord{PID: 4242, ProcessStartMs: 777000})
	if err != nil {
		t.Fatalf("登记租约失败: %v", err)
	}
	defer lease.Release()

	absolute := filepath.Join(root, indexDirName, leaseFileName)
	if _, statErr := os.Stat(absolute); statErr != nil {
		t.Fatalf("租约表应已落盘: %v", statErr)
	}

	// 恢复时模拟进程仍存活且启动时间一致。
	restore := stubProcess(func(uint32) (processStateValue, int64) { return processAlive, 777000 })
	defer restore()

	reopened := NewLeaseManager(root)
	report, recoverErr := reopened.Recover(time.Now().UTC().UnixMilli())
	if recoverErr != nil {
		t.Fatalf("恢复失败: %v", recoverErr)
	}
	if len(report.Kept) != 1 {
		t.Fatalf("期望保留 1 条租约，实际 %+v", report)
	}
	if !reopened.Active("26.3") {
		t.Fatal("恢复后租约应处于活动状态")
	}
}

// ===== 残留租约回收 =====

// TestLeaseRecoverClearsGoneProcess 验证进程已退出时清除租约。
func TestLeaseRecoverClearsGoneProcess(t *testing.T) {
	root := t.TempDir()
	manager := NewLeaseManager(root)
	if _, err := manager.Acquire("26.3", LeaseRecord{PID: 4242, ProcessStartMs: 777000}); err != nil {
		t.Fatalf("登记租约失败: %v", err)
	}

	restore := stubProcess(func(uint32) (processStateValue, int64) { return processGone, 0 })
	defer restore()

	reopened := NewLeaseManager(root)
	report, err := reopened.Recover(time.Now().UTC().UnixMilli())
	if err != nil {
		t.Fatalf("恢复失败: %v", err)
	}
	if len(report.Cleared) != 1 {
		t.Fatalf("期望清除 1 条租约，实际 %+v", report)
	}
	if reopened.Active("26.3") {
		t.Fatal("清除后不应仍处于活动状态")
	}
}

// TestLeaseRecoverClearsReusedPID 验证 PID 被复用时清除租约。
//
// 这是「不能只看 PID」的核心用例：进程存在，但启动时间不符，
// 说明是另一个进程复用了这个 PID。
func TestLeaseRecoverClearsReusedPID(t *testing.T) {
	root := t.TempDir()
	manager := NewLeaseManager(root)
	if _, err := manager.Acquire("26.3", LeaseRecord{PID: 4242, ProcessStartMs: 777000}); err != nil {
		t.Fatalf("登记租约失败: %v", err)
	}

	// 进程存在，但启动时间不同 → PID 复用。
	restore := stubProcess(func(uint32) (processStateValue, int64) { return processAlive, 999000 })
	defer restore()

	reopened := NewLeaseManager(root)
	report, err := reopened.Recover(time.Now().UTC().UnixMilli())
	if err != nil {
		t.Fatalf("恢复失败: %v", err)
	}
	if len(report.Cleared) != 1 {
		t.Fatalf("PID 复用时期望清除租约，实际 %+v", report)
	}
}

// TestLeaseRecoverKeepsUncertainWithinGrace 验证无法判定时在宽限期内保留。
func TestLeaseRecoverKeepsUncertainWithinGrace(t *testing.T) {
	root := t.TempDir()
	manager := NewLeaseManager(root)
	if _, err := manager.Acquire("26.3", LeaseRecord{PID: 4242, ProcessStartMs: 777000}); err != nil {
		t.Fatalf("登记租约失败: %v", err)
	}

	restore := stubProcess(func(uint32) (processStateValue, int64) { return processUnknown, 0 })
	defer restore()

	reopened := NewLeaseManager(root)
	report, err := reopened.Recover(time.Now().UTC().UnixMilli())
	if err != nil {
		t.Fatalf("恢复失败: %v", err)
	}
	if len(report.Uncertain) != 1 {
		t.Fatalf("期望 1 条无法判定的租约，实际 %+v", report)
	}
	if !reopened.Active("26.3") {
		t.Fatal("宽限期内应保留租约")
	}
}

// TestLeaseRecoverClearsUncertainAfterGrace 验证无法判定且超过宽限期后清除。
func TestLeaseRecoverClearsUncertainAfterGrace(t *testing.T) {
	root := t.TempDir()
	manager := NewLeaseManager(root)
	if _, err := manager.Acquire("26.3", LeaseRecord{PID: 4242, ProcessStartMs: 777000}); err != nil {
		t.Fatalf("登记租约失败: %v", err)
	}

	restore := stubProcess(func(uint32) (processStateValue, int64) { return processUnknown, 0 })
	defer restore()

	// 把当前时间推到宽限期之后。
	reopened := NewLeaseManager(root)
	future := time.Now().UTC().Add(leaseOrphanGrace + time.Minute).UnixMilli()
	report, err := reopened.Recover(future)
	if err != nil {
		t.Fatalf("恢复失败: %v", err)
	}
	if len(report.Cleared) != 1 {
		t.Fatalf("超过宽限期后期望清除租约，实际 %+v", report)
	}
}

// TestLeaseRecoverWithoutFile 验证没有租约表时恢复为空操作。
func TestLeaseRecoverWithoutFile(t *testing.T) {
	manager := NewLeaseManager(t.TempDir())
	report, err := manager.Recover(time.Now().UTC().UnixMilli())
	if err != nil {
		t.Fatalf("无租约表时不应报错: %v", err)
	}
	if len(report.Kept)+len(report.Cleared)+len(report.Uncertain) != 0 {
		t.Fatalf("期望空报告，实际 %+v", report)
	}
}

// TestLeaseActiveVersionsSorted 验证活动版本列表有序。
func TestLeaseActiveVersionsSorted(t *testing.T) {
	manager := NewLeaseManager(t.TempDir())
	first, err := manager.Acquire("b-version", LeaseRecord{PID: 1, ProcessStartMs: 1})
	if err != nil {
		t.Fatalf("登记失败: %v", err)
	}
	defer first.Release()
	second, err := manager.Acquire("a-version", LeaseRecord{PID: 2, ProcessStartMs: 2})
	if err != nil {
		t.Fatalf("登记失败: %v", err)
	}
	defer second.Release()

	versions := manager.ActiveVersions()
	if len(versions) != 2 || versions[0] != "a-version" || versions[1] != "b-version" {
		t.Fatalf("活动版本期望 [a-version b-version]，实际 %v", versions)
	}
}
