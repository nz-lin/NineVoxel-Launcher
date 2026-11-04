package core

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setupCoreFixture 构造一个「旧核心」：四个组件目录各含一个标记文件。
func setupCoreFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, component := range rebuildComponents {
		writeComponentFile(t, root, component, "old")
	}
	return root
}

// writeComponentFile 在某个组件目录下写入标记文件。
func writeComponentFile(t *testing.T, root, component, content string) {
	t.Helper()
	directory := filepath.Join(root, component)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "marker.txt"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// readComponentFile 读取组件的标记文件内容；不存在时返回空串。
func readComponentFile(t *testing.T, root, component string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(root, component, "marker.txt"))
	if err != nil {
		return ""
	}
	return string(content)
}

// populateNew 返回一个把所有组件写成 "new" 的构建步骤。
func populateNew(t *testing.T) func(string) error {
	t.Helper()
	return func(stagingRoot string) error {
		for _, component := range rebuildComponents {
			writeComponentFile(t, stagingRoot, component, "new")
		}
		return nil
	}
}

// withCrashAt 注入崩溃点并返回恢复函数。
func withCrashAt(t *testing.T, step string) func() {
	t.Helper()
	original := rebuildCrashHook
	rebuildCrashHook = func(current string) error {
		if current == step {
			return errors.New("simulated crash")
		}
		return nil
	}
	return func() { rebuildCrashHook = original }
}

// journals 返回核心中的事务日志数量。
func journalCount(t *testing.T, root string) int {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, journalDirName))
	if err != nil {
		return 0
	}
	count := 0
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
			count++
		}
	}
	return count
}

// ===== 正常路径 =====

// TestRebuildSwapsAllComponents 验证重建后全部组件被替换、备份被清理。
func TestRebuildSwapsAllComponents(t *testing.T) {
	root := setupCoreFixture(t)

	if err := Rebuild(RebuildOptions{Root: root, Populate: populateNew(t)}); err != nil {
		t.Fatalf("重建失败: %v", err)
	}

	for _, component := range rebuildComponents {
		if got := readComponentFile(t, root, component); got != "new" {
			t.Fatalf("%s 内容期望 new，实际 %q", component, got)
		}
	}
	// 备份、暂存与日志都应清理干净。
	if _, err := os.Stat(filepath.Join(root, rebuildBackupDir)); !os.IsNotExist(err) {
		t.Fatalf("备份目录未清理: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, rebuildStagingDir)); !os.IsNotExist(err) {
		t.Fatalf("暂存目录未清理: %v", err)
	}
	if count := journalCount(t, root); count != 0 {
		t.Fatalf("事务日志未清理，剩 %d 个", count)
	}
}

// TestRebuildCreatesMissingComponents 验证首次重建（旧组件不存在）也能成功。
func TestRebuildCreatesMissingComponents(t *testing.T) {
	root := t.TempDir()

	if err := Rebuild(RebuildOptions{Root: root, Populate: populateNew(t)}); err != nil {
		t.Fatalf("重建失败: %v", err)
	}
	for _, component := range rebuildComponents {
		if got := readComponentFile(t, root, component); got != "new" {
			t.Fatalf("%s 内容期望 new，实际 %q", component, got)
		}
	}
}

// ===== 失败路径 =====

// TestRebuildPopulateFailureKeepsOldCore 验证构建失败时旧核心不受影响。
func TestRebuildPopulateFailureKeepsOldCore(t *testing.T) {
	root := setupCoreFixture(t)

	err := Rebuild(RebuildOptions{
		Root: root,
		Populate: func(string) error {
			return errors.New("download failed")
		},
	})
	if err == nil {
		t.Fatal("构建失败时应返回错误")
	}
	for _, component := range rebuildComponents {
		if got := readComponentFile(t, root, component); got != "old" {
			t.Fatalf("%s 应保持旧内容，实际 %q", component, got)
		}
	}
	if count := journalCount(t, root); count != 0 {
		t.Fatalf("失败后应清理事务日志，剩 %d 个", count)
	}
}

// TestRebuildVerifyFailureKeepsOldCore 验证暂存校验失败时旧核心不受影响。
//
// 这是「宁可不切换，也不要切到一半」的核心保证。
func TestRebuildVerifyFailureKeepsOldCore(t *testing.T) {
	root := setupCoreFixture(t)

	err := Rebuild(RebuildOptions{
		Root:     root,
		Populate: populateNew(t),
		Verify: func(target string) error {
			if target != root {
				return errors.New("staging incomplete")
			}
			return nil
		},
	})
	if err == nil {
		t.Fatal("暂存校验失败时应返回错误")
	}
	for _, component := range rebuildComponents {
		if got := readComponentFile(t, root, component); got != "old" {
			t.Fatalf("%s 应保持旧内容，实际 %q", component, got)
		}
	}
	if count := journalCount(t, root); count != 0 {
		t.Fatalf("失败后应清理事务日志，剩 %d 个", count)
	}
}

// TestRebuildRefusesWhenGameRunning 验证有游戏运行时拒绝重建。
//
// 重建会替换全部核心目录，运行中的游戏会立刻丢失正在加载的 JAR。
func TestRebuildRefusesWhenGameRunning(t *testing.T) {
	root := setupCoreFixture(t)
	writeLeaseBody(t, root, "26.3")

	err := Rebuild(RebuildOptions{Root: root, Populate: populateNew(t)})
	if err == nil {
		t.Fatal("有游戏运行时应拒绝重建")
	}
	if !strings.Contains(err.Error(), "正在运行") {
		t.Fatalf("错误文案应说明原因，实际 %v", err)
	}
	for _, component := range rebuildComponents {
		if got := readComponentFile(t, root, component); got != "old" {
			t.Fatalf("%s 不应被改动，实际 %q", component, got)
		}
	}
}

// writeLeaseBody 写入一条运行时租约，模拟「游戏正在运行」。
func writeLeaseBody(t *testing.T, root, versionID string) {
	t.Helper()
	table := leaseTable{
		SchemaVersion: leaseSchemaVersion,
		Leases: []LeaseRecord{{
			VersionID:      versionID,
			PID:            4242,
			ProcessStartMs: 1000,
			AcquiredAtMs:   1000,
		}},
	}
	encoded, err := json.Marshal(table)
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(root, indexDirName)
	if mkErr := os.MkdirAll(directory, 0o755); mkErr != nil {
		t.Fatal(mkErr)
	}
	if writeErr := os.WriteFile(filepath.Join(directory, leaseFileName), encoded, 0o644); writeErr != nil {
		t.Fatal(writeErr)
	}
}

// TestRebuildRefusesWhenGameStartsDuringBuild 验证构建期间有游戏启动时取消切换。
//
// 构建与校验可能耗时数分钟，只在入口检查一次会留下很大的竞态窗口。
func TestRebuildRefusesWhenGameStartsDuringBuild(t *testing.T) {
	root := setupCoreFixture(t)

	err := Rebuild(RebuildOptions{
		Root:     root,
		Populate: populateNew(t),
		Verify: func(target string) error {
			if target != root {
				// 校验暂存核心期间，模拟用户启动了游戏。
				writeLeaseBody(t, root, "26.3")
			}
			return nil
		},
	})
	if err == nil {
		t.Fatal("构建期间有游戏启动时应取消重建")
	}
	if !strings.Contains(err.Error(), "构建期间") {
		t.Fatalf("错误文案应说明原因，实际 %v", err)
	}
	for _, component := range rebuildComponents {
		if got := readComponentFile(t, root, component); got != "old" {
			t.Fatalf("%s 不应被改动，实际 %q", component, got)
		}
	}
}

// TestRecoverPendingComponentRestoresMovedBackup 验证「改名完成、日志未写」也能回滚。
//
// 崩溃可能发生在 os.Rename 与 writeJournal 之间：日志仍写着 pending，
// 但旧目录其实已经被移进备份区。恢复若只信日志，会把整个组件留在缺失状态。
func TestRecoverPendingComponentRestoresMovedBackup(t *testing.T) {
	root := setupCoreFixture(t)
	operationID := "rebuild-crash-window"
	journal := &rebuildJournal{
		SchemaVersion: journalSchemaVersion,
		OperationID:   operationID,
		State:         stateSwitching,
		Components:    map[string]string{},
	}
	for _, component := range rebuildComponents {
		journal.Components[component] = componentPending
	}
	// 模拟 libraries 已完成改名但日志尚未记录：旧内容在备份区，目标缺失。
	backup := filepath.Join(root, rebuildBackupDir, operationID, librariesDirName)
	if err := os.MkdirAll(backup, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(backup, "marker.txt"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(root, librariesDirName)); err != nil {
		t.Fatal(err)
	}
	if err := writeJournal(root, journal); err != nil {
		t.Fatal(err)
	}

	if err := RecoverRebuild(root); err != nil {
		t.Fatalf("恢复失败: %v", err)
	}
	if got := readComponentFile(t, root, librariesDirName); got != "old" {
		t.Fatalf("libraries 应恢复旧内容，实际 %q", got)
	}
}

// TestRecoverMovedOutWithNewTargetRestoresOld 验证「新目录已就位、日志仍是 moved-out」的回滚。
func TestRecoverMovedOutWithNewTargetRestoresOld(t *testing.T) {
	root := setupCoreFixture(t)
	operationID := "rebuild-crash-window-2"
	journal := &rebuildJournal{
		SchemaVersion: journalSchemaVersion,
		OperationID:   operationID,
		State:         stateSwitching,
		Components:    map[string]string{},
	}
	for _, component := range rebuildComponents {
		journal.Components[component] = componentPending
	}
	journal.Components[librariesDirName] = componentMovedOut

	backup := filepath.Join(root, rebuildBackupDir, operationID, librariesDirName)
	if err := os.MkdirAll(backup, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(backup, "marker.txt"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 新目录已改名到目标位置，但日志还没来得及更新为 moved-in。
	writeComponentFile(t, root, librariesDirName, "new")
	if err := writeJournal(root, journal); err != nil {
		t.Fatal(err)
	}

	if err := RecoverRebuild(root); err != nil {
		t.Fatalf("恢复失败: %v", err)
	}
	if got := readComponentFile(t, root, librariesDirName); got != "old" {
		t.Fatalf("libraries 应恢复旧内容，实际 %q", got)
	}
}

// TestRecoverKeepsNewCoreWhenBackupsAlreadyCleaned 验证备份已清理时不误删新核心。
//
// 提交时若日志写入失败、清理又中断，磁盘上会留下 verifying-new 日志但没有
// 备份。此时删除「没有备份的目标目录」会把新核心整个抹掉。
func TestRecoverKeepsNewCoreWhenBackupsAlreadyCleaned(t *testing.T) {
	root := t.TempDir()
	operationID := "rebuild-cleaned-backup"
	journal := &rebuildJournal{
		SchemaVersion: journalSchemaVersion,
		OperationID:   operationID,
		State:         stateVerifyingNew,
		Components:    map[string]string{},
	}
	for _, component := range rebuildComponents {
		journal.Components[component] = componentMovedIn
		writeComponentFile(t, root, component, "new")
	}
	if err := writeJournal(root, journal); err != nil {
		t.Fatal(err)
	}

	if err := RecoverRebuild(root); err != nil {
		t.Fatalf("恢复失败: %v", err)
	}
	for _, component := range rebuildComponents {
		if got := readComponentFile(t, root, component); got != "new" {
			t.Fatalf("%s 不应被删除，实际 %q", component, got)
		}
	}
	if count := journalCount(t, root); count != 0 {
		t.Fatalf("恢复后应清理日志，剩 %d 个", count)
	}
}

// ===== 崩溃与恢复 =====

// TestRebuildCrashBeforeSwitchIsRecoverable 验证切换前崩溃可恢复。
func TestRebuildCrashBeforeSwitchIsRecoverable(t *testing.T) {
	root := setupCoreFixture(t)
	restore := withCrashAt(t, "populated")
	defer restore()

	if err := Rebuild(RebuildOptions{Root: root, Populate: populateNew(t)}); err == nil {
		t.Fatal("崩溃点应返回错误")
	}
	// 旧核心完好，暂存目录保留，日志保留。
	for _, component := range rebuildComponents {
		if got := readComponentFile(t, root, component); got != "old" {
			t.Fatalf("%s 应保持旧内容，实际 %q", component, got)
		}
	}
	if count := journalCount(t, root); count != 1 {
		t.Fatalf("崩溃后应保留 1 个事务日志，实际 %d", count)
	}

	if err := RecoverRebuild(root); err != nil {
		t.Fatalf("恢复失败: %v", err)
	}
	if count := journalCount(t, root); count != 0 {
		t.Fatalf("恢复后应清理日志，剩 %d 个", count)
	}
	if _, err := os.Stat(filepath.Join(root, rebuildStagingDir)); !os.IsNotExist(err) {
		t.Fatalf("恢复后应清理暂存目录: %v", err)
	}
	for _, component := range rebuildComponents {
		if got := readComponentFile(t, root, component); got != "old" {
			t.Fatalf("%s 应保持旧内容，实际 %q", component, got)
		}
	}
}

// TestRebuildCrashMidSwitchRollsBack 验证切换中途崩溃后回滚到旧核心。
//
// 这是重建最危险的窗口：部分组件已换成新内容、部分仍是旧内容。
// 恢复必须把它们全部还原，而不是接受混合状态。
func TestRebuildCrashMidSwitchRollsBack(t *testing.T) {
	// 在倒数第二个组件（versions）切换前崩溃：索引尚未切换。
	restore := withCrashAt(t, "switch-"+versionsDirName)
	defer restore()

	root := setupCoreFixture(t)
	if err := Rebuild(RebuildOptions{Root: root, Populate: populateNew(t)}); err == nil {
		t.Fatal("崩溃点应返回错误")
	}

	// 此时 libraries 与 assets 已换成新内容，versions 与 core-index 仍是旧内容。
	if got := readComponentFile(t, root, librariesDirName); got != "new" {
		t.Fatalf("前置条件不成立：libraries 应为 new，实际 %q", got)
	}

	if err := RecoverRebuild(root); err != nil {
		t.Fatalf("恢复失败: %v", err)
	}
	for _, component := range rebuildComponents {
		if got := readComponentFile(t, root, component); got != "old" {
			t.Fatalf("%s 回滚后期望 old，实际 %q", component, got)
		}
	}
	if count := journalCount(t, root); count != 0 {
		t.Fatalf("恢复后应清理日志，剩 %d 个", count)
	}
}

// TestRebuildCrashAfterSwitchRollsBack 验证切换完成但未确认时回滚。
//
// 保守回滚的理由：新核心尚未校验，而旧核心完整地留在备份区。
func TestRebuildCrashAfterSwitchRollsBack(t *testing.T) {
	restore := withCrashAt(t, "switched")
	defer restore()

	root := setupCoreFixture(t)
	if err := Rebuild(RebuildOptions{Root: root, Populate: populateNew(t)}); err == nil {
		t.Fatal("崩溃点应返回错误")
	}
	for _, component := range rebuildComponents {
		if got := readComponentFile(t, root, component); got != "new" {
			t.Fatalf("前置条件不成立：%s 应为 new，实际 %q", component, got)
		}
	}

	if err := RecoverRebuild(root); err != nil {
		t.Fatalf("恢复失败: %v", err)
	}
	for _, component := range rebuildComponents {
		if got := readComponentFile(t, root, component); got != "old" {
			t.Fatalf("%s 回滚后期望 old，实际 %q", component, got)
		}
	}
}

// TestRebuildCrashAfterCommitKeepsNewCore 验证已提交的事务只做清理。
func TestRebuildCrashAfterCommitKeepsNewCore(t *testing.T) {
	restore := withCrashAt(t, "committed")
	defer restore()

	root := setupCoreFixture(t)
	if err := Rebuild(RebuildOptions{Root: root, Populate: populateNew(t)}); err == nil {
		t.Fatal("崩溃点应返回错误")
	}

	if err := RecoverRebuild(root); err != nil {
		t.Fatalf("恢复失败: %v", err)
	}
	// 已提交的事务不应被回滚：新核心继续生效。
	for _, component := range rebuildComponents {
		if got := readComponentFile(t, root, component); got != "new" {
			t.Fatalf("%s 期望保持 new，实际 %q", component, got)
		}
	}
	if count := journalCount(t, root); count != 0 {
		t.Fatalf("恢复后应清理日志，剩 %d 个", count)
	}
	if _, err := os.Stat(filepath.Join(root, rebuildBackupDir)); !os.IsNotExist(err) {
		t.Fatalf("恢复后应清理备份: %v", err)
	}
}

// TestRecoverRebuildWithoutJournal 验证没有事务时恢复为空操作。
func TestRecoverRebuildWithoutJournal(t *testing.T) {
	if err := RecoverRebuild(t.TempDir()); err != nil {
		t.Fatalf("无事务时不应报错: %v", err)
	}
}

// TestRebuildRetriesAfterRecovery 验证恢复后可以重新执行重建。
func TestRebuildRetriesAfterRecovery(t *testing.T) {
	restore := withCrashAt(t, "switch-"+versionsDirName)
	root := setupCoreFixture(t)
	if err := Rebuild(RebuildOptions{Root: root, Populate: populateNew(t)}); err == nil {
		t.Fatal("崩溃点应返回错误")
	}
	restore()

	if err := RecoverRebuild(root); err != nil {
		t.Fatalf("恢复失败: %v", err)
	}
	// 再次重建应成功。
	if err := Rebuild(RebuildOptions{Root: root, Populate: populateNew(t)}); err != nil {
		t.Fatalf("恢复后重建失败: %v", err)
	}
	for _, component := range rebuildComponents {
		if got := readComponentFile(t, root, component); got != "new" {
			t.Fatalf("%s 期望 new，实际 %q", component, got)
		}
	}
}
