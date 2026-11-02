package download

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 本文件覆盖 P3-06 的五件事：校验、提交、隔离、磁盘预检、恢复。
//
// 全部用例只使用 t.TempDir() 提供的临时目录，绝不触碰真实数据根或用户游戏目录；
// 也不发起任何网络请求（本卡没有任何网络代码路径）。

// testHashes 计算测试内容对应的 SHA-1 与 SHA-256 十六进制串。
func testHashes(content []byte) (string, string) {
	sum1 := sha1.Sum(content)
	sum256 := sha256.Sum256(content)
	return hex.EncodeToString(sum1[:]), hex.EncodeToString(sum256[:])
}

// commitTask 构造一个准备提交的任务。
func commitTask(id, targetRel string, content []byte) Task {
	sum1, sum256 := testHashes(content)
	return Task{
		ID:             id,
		TaskType:       TaskTypeClientJar,
		State:          StateCommitting,
		TargetRelPath:  targetRel,
		TargetKind:     TargetKindVersion,
		ExpectedSize:   int64(len(content)),
		ExpectedSHA1:   sum1,
		ExpectedSHA256: sum256,
		ETag:           `"etag-1"`,
		LastModified:   "Mon, 01 Jan 2024 00:00:00 GMT",
	}
}

// writeStagedContent 通过正式的 staging 接缝写入暂存文件与续传记录。
func writeStagedContent(t *testing.T, dataRoot string, task Task, content []byte) {
	t.Helper()
	sink := NewStagingSink(dataRoot)
	writer, err := sink.Open(context.Background(), task, 0, true)
	if err != nil {
		t.Fatalf("打开暂存文件失败: %v", err)
	}
	if _, err := writer.Write(content); err != nil {
		t.Fatalf("写入暂存内容失败: %v", err)
	}
	if err := writer.Flush(); err != nil {
		t.Fatalf("刷新暂存内容失败: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("关闭暂存文件失败: %v", err)
	}
}

// TestVerifyFileAcceptsMatchingContent 校验通过时需要给出实测大小与两个哈希。
func TestVerifyFileAcceptsMatchingContent(t *testing.T) {
	dir := t.TempDir()
	content := []byte("client jar content for verification")
	path := filepath.Join(dir, "artifact.bin")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("写入测试文件失败: %v", err)
	}
	sum1, sum256 := testHashes(content)

	result, verifyErr := VerifyFile(path, VerifyExpectation{
		ExpectedSize:   int64(len(content)),
		ExpectedSHA1:   sum1,
		ExpectedSHA256: sum256,
	})
	if verifyErr != nil {
		t.Fatalf("校验应当通过，实际失败: %v", verifyErr)
	}
	if result.Size != int64(len(content)) {
		t.Fatalf("实测大小 = %d，期望 %d", result.Size, len(content))
	}
	if result.SHA1 != sum1 || result.SHA256 != sum256 {
		t.Fatalf("实测哈希 = %s/%s，期望 %s/%s", result.SHA1, result.SHA256, sum1, sum256)
	}
}

// TestVerifyFileRejectsSizeMismatch 大小不符时返回 DOWNLOAD_SIZE_001。
func TestVerifyFileRejectsSizeMismatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "artifact.bin")
	if err := os.WriteFile(path, []byte("short"), 0o644); err != nil {
		t.Fatalf("写入测试文件失败: %v", err)
	}

	_, verifyErr := VerifyFile(path, VerifyExpectation{ExpectedSize: 999})
	if verifyErr == nil {
		t.Fatal("大小不符时应当返回错误")
	}
	if verifyErr.Code != ErrorCodeSize {
		t.Fatalf("错误码 = %s，期望 %s", verifyErr.Code, ErrorCodeSize)
	}
	if verifyErr.Retryable {
		t.Fatal("大小不符不应标记为可重试")
	}
}

// TestVerifyFileRejectsHashMismatch 哈希不符时返回 DOWNLOAD_VERIFY_001。
func TestVerifyFileRejectsHashMismatch(t *testing.T) {
	dir := t.TempDir()
	content := []byte("payload")
	path := filepath.Join(dir, "artifact.bin")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("写入测试文件失败: %v", err)
	}

	_, verifyErr := VerifyFile(path, VerifyExpectation{
		ExpectedSize:   int64(len(content)),
		ExpectedSHA256: strings.Repeat("0", 64),
	})
	if verifyErr == nil {
		t.Fatal("哈希不符时应当返回错误")
	}
	if verifyErr.Code != ErrorCodeVerify {
		t.Fatalf("错误码 = %s，期望 %s", verifyErr.Code, ErrorCodeVerify)
	}
}

// TestVerifyFileRequiresExpectedSize 缺少预期大小时拒绝校验。
func TestVerifyFileRequiresExpectedSize(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "artifact.bin")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatalf("写入测试文件失败: %v", err)
	}

	_, verifyErr := VerifyFile(path, VerifyExpectation{})
	if verifyErr == nil {
		t.Fatal("缺少预期大小时应当返回错误")
	}
	if verifyErr.Code != ErrorCodeVerify {
		t.Fatalf("错误码 = %s，期望 %s", verifyErr.Code, ErrorCodeVerify)
	}
}

// TestRequiredSpaceKeepsMargin 预留空间必须是预期大小的 1.1 倍。
func TestRequiredSpaceKeepsMargin(t *testing.T) {
	cases := []struct {
		total int64
		want  int64
	}{
		{total: 0, want: 0},
		{total: -5, want: 0},
		{total: 1000, want: 1100},
		{total: 1001, want: 1102}, // 向上取整，不能少算
	}
	for _, tc := range cases {
		if got := RequiredSpace(tc.total); got != tc.want {
			t.Fatalf("RequiredSpace(%d) = %d，期望 %d", tc.total, got, tc.want)
		}
	}
}

// TestCheckDiskSpaceAcceptsSmallRequirement 小文件在临时目录上应当通过。
func TestCheckDiskSpaceAcceptsSmallRequirement(t *testing.T) {
	dir := t.TempDir()
	check, spaceErr := CheckDiskSpace(dir, 4096)
	if spaceErr != nil {
		t.Fatalf("小文件应当通过空间预检: %v", spaceErr)
	}
	if !check.OK() {
		t.Fatalf("空间预检应当判定为可用：%+v", check)
	}
	if check.RequiredBytes != RequiredSpace(4096) {
		t.Fatalf("预检记录的需求空间 = %d", check.RequiredBytes)
	}
}

// TestCheckDiskSpaceRejectsOversizedRequirement 需求远超可用空间时必须拒绝。
func TestCheckDiskSpaceRejectsOversizedRequirement(t *testing.T) {
	dir := t.TempDir()
	_, spaceErr := CheckDiskSpace(dir, 1<<62)
	if spaceErr == nil {
		t.Fatal("超大需求应当被拒绝")
	}
	if spaceErr.Code != ErrorCodeSpace {
		t.Fatalf("错误码 = %s，期望 %s", spaceErr.Code, ErrorCodeSpace)
	}
	if spaceErr.Retryable {
		t.Fatal("空间不足不应标记为可重试")
	}
}

// TestCheckDiskSpaceProbesMissingRoot 数据根不存在时也要能给出结论。
func TestCheckDiskSpaceProbesMissingRoot(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "not-created-yet", "deeper")
	if _, spaceErr := CheckDiskSpace(missing, 1024); spaceErr != nil {
		t.Fatalf("向上找到存在的祖先即可探测，实际失败: %v", spaceErr)
	}
	// 只探测、不创建：路径在预检之后仍不应存在。
	if _, statErr := os.Stat(missing); statErr == nil {
		t.Fatal("空间预检不得创建目录")
	}
}

// TestQuarantineFileWritesReasonRecord 隔离文件必须同时写 .reason.json。
func TestQuarantineFileWritesReasonRecord(t *testing.T) {
	dataRoot := t.TempDir()
	sourceRoot := t.TempDir()
	source := filepath.Join(sourceRoot, "broken.jar")
	if err := os.WriteFile(source, []byte("broken"), 0o644); err != nil {
		t.Fatalf("写入待隔离文件失败: %v", err)
	}

	reason := QuarantineReason{
		TaskID:          "task-quarantine-1",
		ErrorCode:       ErrorCodeVerify,
		Message:         messageVerifyHashMismatch,
		ExpectedSize:    1024,
		ActualSize:      6,
		ExpectedSHA256:  strings.Repeat("a", 64),
		ActualSHA256:    strings.Repeat("b", 64),
		TargetRelPath:   "versions/1.20.1/1.20.1.jar",
		QuarantinedAtMs: 1700000000000,
	}
	quarantined, quarantineErr := QuarantineFile(dataRoot, source, reason)
	if quarantineErr != nil {
		t.Fatalf("隔离文件失败: %v", quarantineErr)
	}
	if !strings.HasSuffix(quarantined.RelPath, quarantineBadSuffix) {
		t.Fatalf("隔离文件后缀不对: %s", quarantined.RelPath)
	}
	if _, statErr := os.Stat(filepath.Join(dataRoot, filepath.FromSlash(quarantined.RelPath))); statErr != nil {
		t.Fatalf("隔离文件不存在: %v", statErr)
	}
	reasonAbs := filepath.Join(dataRoot, filepath.FromSlash(quarantined.ReasonRelPath))
	payload, readErr := os.ReadFile(reasonAbs)
	if readErr != nil {
		t.Fatalf("读取隔离原因失败: %v", readErr)
	}
	var decoded QuarantineReason
	if unmarshalErr := json.Unmarshal(payload, &decoded); unmarshalErr != nil {
		t.Fatalf("隔离原因不是合法 JSON: %v", unmarshalErr)
	}
	if decoded.ErrorCode != ErrorCodeVerify || decoded.ActualSize != 6 {
		t.Fatalf("隔离原因内容不正确: %+v", decoded)
	}
	if _, statErr := os.Stat(source); !os.IsNotExist(statErr) {
		t.Fatal("原文件应当已被移动走")
	}
}

// TestRetentionPolicyDescribesOnly 清理策略只描述、不执行。
func TestRetentionPolicyDescribesOnly(t *testing.T) {
	policy := DefaultRetentionPolicy()
	if policy.KeepDays <= 0 || policy.KeepRecent <= 0 {
		t.Fatalf("默认策略应当有正数保留阈值: %+v", policy)
	}
	description := policy.Describe()
	if description == "" {
		t.Fatal("策略描述不应为空")
	}
}

// TestCommitterCommitsStagingToTarget 正常提交：目标出现、暂存被清理。
func TestCommitterCommitsStagingToTarget(t *testing.T) {
	dataRoot := t.TempDir()
	content := []byte("client jar payload for commit")
	task := commitTask("task-commit-1", "versions/1.20.1/1.20.1.jar", content)
	writeStagedContent(t, dataRoot, task, content)

	committer := NewCommitter(CommitOptions{DataRoot: dataRoot})
	if commitErr := committer.Commit(context.Background(), task); commitErr != nil {
		t.Fatalf("提交失败: %v", commitErr)
	}

	targetAbs := filepath.Join(dataRoot, filepath.FromSlash(task.TargetRelPath))
	written, readErr := os.ReadFile(targetAbs)
	if readErr != nil {
		t.Fatalf("读取目标文件失败: %v", readErr)
	}
	if string(written) != string(content) {
		t.Fatalf("目标内容 = %q，期望 %q", string(written), string(content))
	}

	paths, pathErr := resolveStagingPaths(dataRoot, task.ID)
	if pathErr != nil {
		t.Fatalf("解析暂存路径失败: %v", pathErr)
	}
	for _, leftover := range []string{paths.PartAbs, paths.RecordAbs, paths.JournalAbs} {
		if _, statErr := os.Stat(leftover); !os.IsNotExist(statErr) {
			t.Fatalf("提交后不应残留 %s", filepath.Base(leftover))
		}
	}
}

// TestCommitterIsIdempotentWhenTargetValid 目标已存在且校验通过时不覆盖、直接成功。
func TestCommitterIsIdempotentWhenTargetValid(t *testing.T) {
	dataRoot := t.TempDir()
	content := []byte("already committed payload")
	task := commitTask("task-commit-2", "versions/1.20.1/1.20.1.jar", content)
	writeStagedContent(t, dataRoot, task, content)

	targetAbs := filepath.Join(dataRoot, filepath.FromSlash(task.TargetRelPath))
	if err := os.MkdirAll(filepath.Dir(targetAbs), 0o755); err != nil {
		t.Fatalf("创建目标目录失败: %v", err)
	}
	if err := os.WriteFile(targetAbs, content, 0o644); err != nil {
		t.Fatalf("预置目标文件失败: %v", err)
	}
	before, statErr := os.Stat(targetAbs)
	if statErr != nil {
		t.Fatalf("读取目标文件信息失败: %v", statErr)
	}

	committer := NewCommitter(CommitOptions{DataRoot: dataRoot})
	if commitErr := committer.Commit(context.Background(), task); commitErr != nil {
		t.Fatalf("幂等提交应当成功: %v", commitErr)
	}
	after, statErr := os.Stat(targetAbs)
	if statErr != nil {
		t.Fatalf("幂等提交后目标文件丢失: %v", statErr)
	}
	if !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("幂等提交不应重写目标文件")
	}

	paths, pathErr := resolveStagingPaths(dataRoot, task.ID)
	if pathErr != nil {
		t.Fatalf("解析暂存路径失败: %v", pathErr)
	}
	if _, statErr := os.Stat(paths.PartAbs); !os.IsNotExist(statErr) {
		t.Fatal("幂等提交后应清理暂存文件")
	}
}

// TestCommitterRejectsOccupiedInvalidTarget 目标内容不对时拒绝覆盖并隔离新文件。
func TestCommitterRejectsOccupiedInvalidTarget(t *testing.T) {
	dataRoot := t.TempDir()
	content := []byte("fresh payload")
	task := commitTask("task-commit-3", "versions/1.20.1/1.20.1.jar", content)
	writeStagedContent(t, dataRoot, task, content)

	targetAbs := filepath.Join(dataRoot, filepath.FromSlash(task.TargetRelPath))
	if err := os.MkdirAll(filepath.Dir(targetAbs), 0o755); err != nil {
		t.Fatalf("创建目标目录失败: %v", err)
	}
	occupied := []byte("someone else's file")
	if err := os.WriteFile(targetAbs, occupied, 0o644); err != nil {
		t.Fatalf("预置占用文件失败: %v", err)
	}

	committer := NewCommitter(CommitOptions{DataRoot: dataRoot})
	commitErr := committer.Commit(context.Background(), task)
	if commitErr == nil {
		t.Fatal("目标内容不符时应当拒绝提交")
	}
	if commitErr.Code != ErrorCodeVerify {
		t.Fatalf("错误码 = %s，期望 %s", commitErr.Code, ErrorCodeVerify)
	}

	kept, readErr := os.ReadFile(targetAbs)
	if readErr != nil {
		t.Fatalf("读取目标文件失败: %v", readErr)
	}
	if string(kept) != string(occupied) {
		t.Fatal("绝不能覆盖已有但内容不同的目标文件")
	}

	// 新下载的暂存内容必须进隔离区，不能继续占着 staging。
	badFiles, globErr := filepath.Glob(filepath.Join(dataRoot, "downloads", "quarantine", "*", "*"+quarantineBadSuffix))
	if globErr != nil {
		t.Fatalf("扫描隔离目录失败: %v", globErr)
	}
	if len(badFiles) != 1 {
		t.Fatalf("隔离文件数量 = %d，期望 1", len(badFiles))
	}
}

// TestCommitterFallsBackToCopyAcrossVolumes 注入跨卷模式时走「复制 + rename」。
func TestCommitterFallsBackToCopyAcrossVolumes(t *testing.T) {
	dataRoot := t.TempDir()
	content := []byte("cross volume payload")
	task := commitTask("task-commit-4", "runtimes/java-21/bin/java.dll", content)
	writeStagedContent(t, dataRoot, task, content)

	committer := NewCommitter(CommitOptions{DataRoot: dataRoot, ForceCrossVolume: true})
	if commitErr := committer.Commit(context.Background(), task); commitErr != nil {
		t.Fatalf("跨卷提交失败: %v", commitErr)
	}
	targetAbs := filepath.Join(dataRoot, filepath.FromSlash(task.TargetRelPath))
	written, readErr := os.ReadFile(targetAbs)
	if readErr != nil {
		t.Fatalf("读取跨卷提交结果失败: %v", readErr)
	}
	if string(written) != string(content) {
		t.Fatalf("跨卷提交内容不一致: %q", string(written))
	}
	// 临时文件必须清理干净。
	temps, globErr := filepath.Glob(filepath.Join(filepath.Dir(targetAbs), "*"+crossVolumeTempSuffix+"*"))
	if globErr != nil {
		t.Fatalf("扫描临时文件失败: %v", globErr)
	}
	if len(temps) != 0 {
		t.Fatalf("跨卷提交后残留临时文件: %v", temps)
	}
}

// TestCommitterFailsWhenStagingMissing 暂存丢失时报 DOWNLOAD_COMMIT_001。
func TestCommitterFailsWhenStagingMissing(t *testing.T) {
	dataRoot := t.TempDir()
	content := []byte("never staged")
	task := commitTask("task-commit-5", "versions/1.20.1/1.20.1.jar", content)

	committer := NewCommitter(CommitOptions{DataRoot: dataRoot})
	commitErr := committer.Commit(context.Background(), task)
	if commitErr == nil {
		t.Fatal("暂存缺失时应当失败")
	}
	if commitErr.Code != ErrorCodeCommit {
		t.Fatalf("错误码 = %s，期望 %s", commitErr.Code, ErrorCodeCommit)
	}
}

// TestCommitterQuarantinesVerificationFailure 校验失败时隔离暂存并拒绝提交。
func TestCommitterQuarantinesVerificationFailure(t *testing.T) {
	dataRoot := t.TempDir()
	content := []byte("corrupted payload")
	task := commitTask("task-commit-6", "versions/1.20.1/1.20.1.jar", content)
	writeStagedContent(t, dataRoot, task, []byte("tampered payload!"))

	committer := NewCommitter(CommitOptions{DataRoot: dataRoot})
	commitErr := committer.Commit(context.Background(), task)
	if commitErr == nil {
		t.Fatal("校验失败时应当拒绝提交")
	}
	if commitErr.Code != ErrorCodeVerify && commitErr.Code != ErrorCodeSize {
		t.Fatalf("错误码 = %s，期望校验类错误", commitErr.Code)
	}
	targetAbs := filepath.Join(dataRoot, filepath.FromSlash(task.TargetRelPath))
	if _, statErr := os.Stat(targetAbs); !os.IsNotExist(statErr) {
		t.Fatal("校验失败的暂存内容绝不能出现在目标位置")
	}
	badFiles, globErr := filepath.Glob(filepath.Join(dataRoot, "downloads", "quarantine", "*", "*"+quarantineBadSuffix))
	if globErr != nil {
		t.Fatalf("扫描隔离目录失败: %v", globErr)
	}
	if len(badFiles) != 1 {
		t.Fatalf("隔离文件数量 = %d，期望 1", len(badFiles))
	}
}

// TestDecideRecoveryMatrix 用表驱动穷举崩溃恢复判定矩阵。
func TestDecideRecoveryMatrix(t *testing.T) {
	resumable := DiskObservation{
		StagingExists:    true,
		PartRecordExists: true,
		PartOffset:       512,
		PartSize:         512,
		PartETag:         `"etag-1"`,
	}
	brokenStaging := DiskObservation{
		StagingExists:    true,
		PartRecordExists: true,
		PartOffset:       512,
		PartSize:         256,
		PartETag:         `"etag-1"`,
	}
	validTarget := DiskObservation{TargetExists: true, TargetValid: true}
	invalidTarget := DiskObservation{TargetExists: true}

	cases := []struct {
		name        string
		state       State
		observation DiskObservation
		wantAction  RecoveryAction
		wantState   State
		wantDrop    bool
	}{
		{name: "completed 终态不介入", state: StateCompleted, observation: validTarget, wantAction: RecoveryActionKeep, wantState: StateCompleted},
		{name: "failed 终态不介入", state: StateFailed, wantAction: RecoveryActionKeep, wantState: StateFailed},
		{name: "cancelled 终态不介入", state: StateCancelled, wantAction: RecoveryActionKeep, wantState: StateCancelled},
		{name: "queued 保持排队", state: StateQueued, wantAction: RecoveryActionKeep, wantState: StateQueued},
		{name: "resolving 回队列", state: StateResolving, wantAction: RecoveryActionRequeue, wantState: StateQueued, wantDrop: true},
		{name: "downloading 可续传转暂停", state: StateDownloading, observation: resumable, wantAction: RecoveryActionPause, wantState: StatePaused},
		{name: "downloading 不可续传回队列", state: StateDownloading, observation: brokenStaging, wantAction: RecoveryActionRequeue, wantState: StateQueued, wantDrop: true},
		{name: "verifying 可续传转暂停", state: StateVerifying, observation: resumable, wantAction: RecoveryActionPause, wantState: StatePaused},
		{name: "verifying 无暂存回队列", state: StateVerifying, observation: DiskObservation{}, wantAction: RecoveryActionRequeue, wantState: StateQueued, wantDrop: true},
		{name: "paused 完整保持暂停", state: StatePaused, observation: resumable, wantAction: RecoveryActionKeep, wantState: StatePaused},
		{name: "paused 不完整回队列", state: StatePaused, observation: DiskObservation{}, wantAction: RecoveryActionRequeue, wantState: StateQueued, wantDrop: true},
		{name: "committing 目标有效补记完成", state: StateCommitting, observation: validTarget, wantAction: RecoveryActionComplete, wantState: StateCompleted, wantDrop: true},
		{name: "committing 目标无效回队列", state: StateCommitting, observation: invalidTarget, wantAction: RecoveryActionRequeue, wantState: StateQueued, wantDrop: true},
		{name: "committing 无目标回队列", state: StateCommitting, observation: DiskObservation{}, wantAction: RecoveryActionRequeue, wantState: StateQueued, wantDrop: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			task := Task{ID: "task-matrix", State: tc.state, TargetRelPath: "versions/1.20.1/1.20.1.jar"}
			decision := DecideRecovery(task, tc.observation)
			if decision.Action != tc.wantAction {
				t.Fatalf("动作 = %s，期望 %s", decision.Action, tc.wantAction)
			}
			if decision.NextState != tc.wantState {
				t.Fatalf("下一状态 = %s，期望 %s", decision.NextState, tc.wantState)
			}
			if decision.DropStaging != tc.wantDrop {
				t.Fatalf("丢弃暂存 = %v，期望 %v", decision.DropStaging, tc.wantDrop)
			}
			if decision.Reason == "" {
				t.Fatal("判定必须给出中文原因")
			}
		})
	}
}

// TestRecoverAppliesDecisionsAndCleansOrphans 端到端：收敛状态并清掉孤儿暂存。
func TestRecoverAppliesDecisionsAndCleansOrphans(t *testing.T) {
	dataRoot := t.TempDir()
	store := newMemoryTaskStore()
	ctx := context.Background()

	// 任务一：提交中断，但目标文件已经落盘且校验通过 → 应补记为完成。
	committedContent := []byte("already on disk")
	committingTask := commitTask("task-recover-1", "versions/1.20.1/1.20.1.jar", committedContent)
	committingTask.State = StateCommitting
	targetAbs := filepath.Join(dataRoot, filepath.FromSlash(committingTask.TargetRelPath))
	if err := os.MkdirAll(filepath.Dir(targetAbs), 0o755); err != nil {
		t.Fatalf("创建目标目录失败: %v", err)
	}
	if err := os.WriteFile(targetAbs, committedContent, 0o644); err != nil {
		t.Fatalf("预置目标文件失败: %v", err)
	}
	if err := store.Save(ctx, committingTask); err != nil {
		t.Fatalf("保存任务失败: %v", err)
	}

	// 任务二：下载中断，暂存与记录一致 → 应转为暂停。
	partialContent := []byte("partial bytes")
	partialTask := commitTask("task-recover-2", "versions/1.20.1/lib.jar", partialContent)
	partialTask.State = StateDownloading
	writeStagedContent(t, dataRoot, partialTask, partialContent)
	if err := store.Save(ctx, partialTask); err != nil {
		t.Fatalf("保存任务失败: %v", err)
	}

	// 孤儿暂存：没有任何任务对应它。
	stagingDir := filepath.Join(dataRoot, artifactDirStagingForTest())
	if err := os.MkdirAll(stagingDir, 0o755); err != nil {
		t.Fatalf("创建暂存目录失败: %v", err)
	}
	orphans := []string{"orphan-task.part", "orphan-task.part.json"}
	for _, name := range orphans {
		if err := os.WriteFile(filepath.Join(stagingDir, name), []byte("stale"), 0o644); err != nil {
			t.Fatalf("写入孤儿暂存失败: %v", err)
		}
	}

	result, recoverErr := Recover(ctx, RecoveryOptions{
		DataRoot: dataRoot,
		Store:    store,
		Now:      func() int64 { return 1700000000000 },
	})
	if recoverErr != nil {
		t.Fatalf("恢复失败: %v", recoverErr)
	}
	if result.Recovered != 2 {
		t.Fatalf("收敛任务数 = %d，期望 2", result.Recovered)
	}
	if len(result.Orphans) != len(orphans) {
		t.Fatalf("清理孤儿数 = %d，期望 %d（%v）", len(result.Orphans), len(orphans), result.Orphans)
	}

	tasks, loadErr := store.LoadAll(ctx)
	if loadErr != nil {
		t.Fatalf("读取恢复后的任务失败: %v", loadErr)
	}
	states := make(map[string]State, len(tasks))
	for _, task := range tasks {
		states[task.ID] = task.State
	}
	if states["task-recover-1"] != StateCompleted {
		t.Fatalf("提交中断的任务应补记为完成，实际 %s", states["task-recover-1"])
	}
	if states["task-recover-2"] != StatePaused {
		t.Fatalf("可续传的任务应转为暂停，实际 %s", states["task-recover-2"])
	}

	for _, name := range orphans {
		if _, statErr := os.Stat(filepath.Join(stagingDir, name)); !os.IsNotExist(statErr) {
			t.Fatalf("孤儿暂存 %s 应当被删除", name)
		}
	}
}

// artifactDirStagingForTest 返回暂存目录的相对路径（与 artifact 包的常量保持一致）。
func artifactDirStagingForTest() string {
	return filepath.FromSlash("downloads/staging")
}
