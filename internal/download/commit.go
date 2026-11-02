package download

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"example.com/ninevoxel/launcher/internal/artifact"
	"example.com/ninevoxel/launcher/internal/logging"
)

// 写盘相关权限与缓冲参数。
//
// 文件权限固定 0644、目录 0755：下载产物是游戏内容而非用户私有数据，
// 需要让同一台机器上的其他工具（例如启动器多进程实例）也能读取。
const (
	// filePermission 是下载产物与记录的权限。
	filePermission os.FileMode = 0o644

	// stagingDirPermission 是 staging 目录权限。
	stagingDirPermission os.FileMode = 0o755

	// quarantineDirPermission 是隔离目录权限。
	quarantineDirPermission os.FileMode = 0o755

	// targetDirPermission 是目标目录权限。
	targetDirPermission os.FileMode = 0o755

	// stagingWriteBufferSize 是 staging 写入缓冲大小。
	//
	// 与 P3-05 的 4 MiB flush 阈值配合：缓冲 64 KiB 保证 flush 时
	// 真正落盘的是「已完整写出的数据」，而不是把 4 MiB 一次性压进内存。
	stagingWriteBufferSize = 64 * 1024

	// partRecordSuffix 是续传记录后缀（<taskId>.part.json）。
	partRecordSuffix = ".json"

	// commitJournalSuffix 是提交 journal 后缀（<taskId>.commit.json）。
	commitJournalSuffix = ".commit.json"

	// crossVolumeTempSuffix 是跨卷提交时目标目录内临时文件的后缀。
	crossVolumeTempSuffix = ".tmp-"
)

// 提交阶段的落库语句（全部只更新列，不插入行：任务行由 P3-04 创建）。
//
// 之所以集中成常量：这三条语句是本卡唯一直接写 download_tasks 的地方，
// 与 P3-04 的整行 UPSERT 并存，审查时需要能一眼看到全部落库点。
const (
	// markCommittingSQL 在提交前补齐 staging/target/哈希三列。
	markCommittingSQL = `UPDATE download_tasks
	SET staging_rel_path = ?, target_rel_path = ?, expected_sha256 = ?, updated_at_ms = ?
	WHERE id = ?`

	// markCompletedSQL 在提交成功后写入完成状态与校验时间。
	markCompletedSQL = `UPDATE download_tasks
	SET state = ?, staging_rel_path = ?, completed_at_ms = ?, verified_at_ms = ?, updated_at_ms = ?
	WHERE id = ?`

	// markTargetInvalidSQL 在「目标已存在但校验不通过」时记录失效标记。
	markTargetInvalidSQL = `UPDATE download_tasks
	SET verified_at_ms = 0, error_code = ?, error_message_safe = ?, updated_at_ms = ?
	WHERE id = ?`
)

// StagingPartRecord 是 `<taskId>.part.json` 的内容。
//
// 它是断点续传与崩溃恢复的唯一持久化依据：进程重启后只能靠这里的
// offset/etag/lastModified 判断「已写多少、还能不能续」。
type StagingPartRecord struct {
	// TaskID 是任务标识。
	TaskID string `json:"taskId"`

	// Offset 是已落盘字节数（与 .part 实际大小一致才算一致）。
	Offset int64 `json:"offset"`

	// ETag 是服务端 ETag（可能为空）。
	ETag string `json:"etag,omitempty"`

	// LastModified 是服务端 Last-Modified（可能为空）。
	LastModified string `json:"lastModified,omitempty"`

	// ExpectedSize 是期望总字节数。
	ExpectedSize int64 `json:"expectedSize"`

	// ExpectedSHA256 是期望 SHA-256（可为空）。
	ExpectedSHA256 string `json:"expectedSha256,omitempty"`

	// CreatedAtMs 是记录创建时间。
	CreatedAtMs int64 `json:"createdAtMs"`

	// UpdatedAtMs 是记录最后更新时间。
	UpdatedAtMs int64 `json:"updatedAtMs"`
}

// commitJournal 是提交前写下的 journal 内容。
//
// 卡面第 5 条要求「先写 journal 再提交」：崩溃若发生在 rename 之后、
// 状态落库之前，只有 journal 能证明「这个 staging 文件当时打算提交到哪」。
// 恢复流程据此判断 committing 任务该变 completed 还是退回 queued。
type commitJournal struct {
	// TaskID 是任务标识。
	TaskID string `json:"taskId"`

	// StagingRelPath 是 staging 相对路径。
	StagingRelPath string `json:"stagingRelPath"`

	// TargetRelPath 是提交目标相对路径。
	TargetRelPath string `json:"targetRelPath"`

	// ExpectedSize 是期望字节数。
	ExpectedSize int64 `json:"expectedSize"`

	// ExpectedSHA1 / ExpectedSHA256 是期望哈希。
	ExpectedSHA1   string `json:"expectedSha1,omitempty"`
	ExpectedSHA256 string `json:"expectedSha256,omitempty"`

	// CrossVolume 表示本次提交走的是跨卷复制路径。
	CrossVolume bool `json:"crossVolume"`

	// CreatedAtMs 是 journal 写入时间。
	CreatedAtMs int64 `json:"createdAtMs"`
}

// stagingPaths 汇总一个任务在 staging 目录下的三条路径。
type stagingPaths struct {
	// PartRel / PartAbs 是数据文件。
	PartRel string
	PartAbs string

	// RecordRel / RecordAbs 是续传记录。
	RecordRel string
	RecordAbs string

	// JournalRel / JournalAbs 是提交 journal。
	JournalRel string
	JournalAbs string
}

// resolveStagingPaths 解析任务的 staging 路径。
//
// 路径一律由 artifact.StagingRelPath 派生并经过根内校验（artifact.ResolveInsideRoot），
// 不使用任务 ID 直接拼文件名以外的任何外部输入：taskId 若被污染成
// `../../x` 会在这一步被拒绝。
func resolveStagingPaths(dataRoot, taskID string) (stagingPaths, *Error) {
	partRel, relErr := artifact.StagingRelPath(taskID)
	if relErr != nil {
		return stagingPaths{}, newInputError(ErrorCodeVerify, messageStagingPathInvalid)
	}
	partAbs, resolveErr := artifact.ResolveInsideRoot(dataRoot, partRel)
	if resolveErr != nil {
		return stagingPaths{}, newInputError(ErrorCodeVerify, messageStagingPathInvalid)
	}
	recordRel := partRel + partRecordSuffix
	recordAbs, recordErr := artifact.ResolveInsideRoot(dataRoot, recordRel)
	if recordErr != nil {
		return stagingPaths{}, newInputError(ErrorCodeVerify, messageStagingPathInvalid)
	}
	journalRel := filepath.ToSlash(filepath.Join(artifact.DirStaging, taskID+commitJournalSuffix))
	journalAbs, journalErr := artifact.ResolveInsideRoot(dataRoot, journalRel)
	if journalErr != nil {
		return stagingPaths{}, newInputError(ErrorCodeVerify, messageStagingPathInvalid)
	}
	return stagingPaths{
		PartRel:    partRel,
		PartAbs:    partAbs,
		RecordRel:  recordRel,
		RecordAbs:  recordAbs,
		JournalRel: journalRel,
		JournalAbs: journalAbs,
	}, nil
}

// stagingSink 是 transport.StagingSink 的真实落盘实现。
//
// 它是本卡（P3-06）唯一允许新增的「下载过程写盘」位置：只写
// `<DataRoot>/downloads/staging/` 下的 `.part` 与 `.part.json`，
// 绝不触碰 artifacts/objects 或 versions/<id>/（那些属于提交阶段）。
type stagingSink struct {
	// dataRoot 是数据根（绝对路径，由调用方解析后注入）。
	dataRoot string

	// now 返回 UTC Unix 毫秒时间戳（可注入以便测试）。
	now func() int64
}

// NewStagingSink 构造 staging 写入接缝。
func NewStagingSink(dataRoot string) StagingSink {
	return &stagingSink{dataRoot: dataRoot, now: currentMillis}
}

// Open 打开 staging 写入句柄。
//
// truncate 为 true 时丢弃已有内容（服务器忽略 Range、或续传位点不可信），
// 否则从 offset 处继续写。两种情况都会先确保 staging 目录存在。
func (s *stagingSink) Open(ctx context.Context, task Task, offset int64, truncate bool) (StagingWriter, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	paths, pathErr := resolveStagingPaths(s.dataRoot, task.ID)
	if pathErr != nil {
		return nil, pathErr
	}
	if mkdirErr := os.MkdirAll(filepath.Dir(paths.PartAbs), stagingDirPermission); mkdirErr != nil {
		return nil, newSystemError(ErrorCodeVerify, messageStagingPathInvalid, false)
	}

	flags := os.O_WRONLY | os.O_CREATE
	startOffset := offset
	if truncate || startOffset < 0 {
		flags |= os.O_TRUNC
		startOffset = 0
	}
	file, openErr := os.OpenFile(paths.PartAbs, flags, filePermission)
	if openErr != nil {
		return nil, newSystemError(ErrorCodeVerify, messageStagingPathInvalid, false)
	}
	if _, seekErr := file.Seek(startOffset, 0); seekErr != nil {
		_ = file.Close()
		return nil, newSystemError(ErrorCodeVerify, messageStagingPathInvalid, false)
	}

	writer := &stagingWriter{
		file:        file,
		paths:       paths,
		task:        task,
		now:         s.now,
		buffer:      bufio.NewWriterSize(file, stagingWriteBufferSize),
		startOffset: startOffset,
		createdAtMs: s.now(),
	}
	// 打开即写一次记录：这样「有 .part 却没有 .part.json」只剩崩溃中途一种解释，
	// 恢复流程据此把它当成不可续传，而不是误以为从未下载过。
	if recordErr := writer.writeRecord(); recordErr != nil {
		_ = file.Close()
		return nil, recordErr
	}
	return writer, nil
}

// stagingWriter 是 staging 写入句柄的实现。
type stagingWriter struct {
	// file 是底层文件。
	file *os.File

	// paths 是三条 staging 路径。
	paths stagingPaths

	// task 是任务快照（用于记录 etag/lastModified/期望值）。
	task Task

	// now 返回当前毫秒时间戳。
	now func() int64

	// buffer 是写缓冲。
	buffer *bufio.Writer

	// startOffset 是本次打开时的起始偏移。
	startOffset int64

	// written 是本次打开后写入的字节数。
	written int64

	// createdAtMs 是记录创建时间。
	createdAtMs int64
}

// Write 写入一段字节。
func (w *stagingWriter) Write(payload []byte) (int, error) {
	written, err := w.buffer.Write(payload)
	w.written += int64(written)
	return written, err
}

// Flush 把缓冲内容刷入文件并落盘，随后更新续传记录。
//
// 顺序必须是「先 flush + fsync，再写记录」：反过序会让记录里的 offset
// 大于真实落盘字节数，恢复时按它续传就会在文件中间留下空洞。
func (w *stagingWriter) Flush() error {
	if err := w.buffer.Flush(); err != nil {
		return err
	}
	if err := w.file.Sync(); err != nil {
		return err
	}
	if recordErr := w.writeRecord(); recordErr != nil {
		return recordErr
	}
	return nil
}

// Close 关闭句柄（先 flush 再关闭）。
func (w *stagingWriter) Close() error {
	flushErr := w.buffer.Flush()
	syncErr := w.file.Sync()
	recordErr := w.writeRecord()
	closeErr := w.file.Close()
	switch {
	case flushErr != nil:
		return flushErr
	case syncErr != nil:
		return syncErr
	case recordErr != nil:
		return recordErr
	default:
		return closeErr
	}
}

// writeRecord 写入 `.part.json` 续传记录。
func (w *stagingWriter) writeRecord() error {
	info, statErr := w.file.Stat()
	var size int64
	if statErr == nil {
		size = info.Size()
	} else {
		size = w.startOffset + w.written
	}
	record := StagingPartRecord{
		TaskID:         w.task.ID,
		Offset:         size,
		ETag:           w.task.ETag,
		LastModified:   w.task.LastModified,
		ExpectedSize:   w.task.ExpectedSize,
		ExpectedSHA256: w.task.ExpectedSHA256,
		CreatedAtMs:    w.createdAtMs,
		UpdatedAtMs:    w.now(),
	}
	payload, marshalErr := json.MarshalIndent(record, "", "  ")
	if marshalErr != nil {
		return newSystemError(ErrorCodeVerify, messageStagingPathInvalid, false)
	}
	payload = append(payload, '\n')
	if writeErr := writeFileAtomically(w.paths.RecordAbs, payload); writeErr != nil {
		return writeErr
	}
	return nil
}

// CommitOptions 是提交器构造参数。
type CommitOptions struct {
	// DataRoot 是数据根绝对路径（调用方通过 artifact.DataRoot 解析后注入）。
	DataRoot string

	// Now 返回 UTC Unix 毫秒时间戳（可注入）。
	Now func() int64

	// Logger 是日志接缝（*logging.Logger 天然满足）。
	Logger TransportLogger

	// DB 是可选的数据库句柄，用于把 staging/target/哈希与完成状态落库。
	//
	// 为 nil 时提交照常执行，只是不写库：文件系统与本卡的 journal 才是
	// 提交行为的真相源，数据库只是索引。
	DB *sql.DB

	// ForceCrossVolume 强制走跨卷复制路径（仅测试使用）。
	ForceCrossVolume bool
}

// Committer 是提交实现，满足 queue.CommitFunc 签名。
type Committer struct {
	// options 是构造参数快照。
	options CommitOptions

	// now 返回当前毫秒时间戳。
	now func() int64
}

// NewCommitter 构造提交器。
//
// 返回值可直接方法值使用：`queueOptions.Commit = NewCommitter(opts).Commit`。
func NewCommitter(options CommitOptions) *Committer {
	now := options.Now
	if now == nil {
		now = currentMillis
	}
	return &Committer{options: options, now: now}
}

// Commit 校验 staging 文件并原子提交到目标路径。
//
// 判定顺序（卡面第 3、5 条）：
//  1. 目标已存在且校验通过 → 不覆盖、直接成功（幂等）；
//  2. 目标已存在但校验不通过 → 保留下载内容到隔离区，拒绝覆盖（不回退删目标）；
//  3. staging 校验不通过 → 进隔离区，不提交；
//  4. 校验通过 → 写 journal → 落库三列 → 同卷 rename / 跨卷复制+rename。
//
// 任何路径都不会用「未校验文件」覆盖「已验证文件」。
func (c *Committer) Commit(ctx context.Context, task Task) *Error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return newInputError(ErrorCodeCancel, messageCancelled)
		}
	}
	if c.options.DataRoot == "" {
		return newSystemError(ErrorCodeCommit, messageCommitFailed, false)
	}
	targetRel, targetErr := artifact.ValidateRelativePath(task.TargetRelPath)
	if targetErr != nil || strings.TrimSpace(targetRel) == "" {
		return newInputError(ErrorCodeVerify, messageTargetPathInvalid)
	}
	paths, pathErr := resolveStagingPaths(c.options.DataRoot, task.ID)
	if pathErr != nil {
		return pathErr
	}
	stagingRel := task.StagingRelPath
	if strings.TrimSpace(stagingRel) == "" {
		stagingRel = paths.PartRel
	}
	stagingAbs, stagingResolveErr := artifact.ResolveInsideRoot(c.options.DataRoot, stagingRel)
	if stagingResolveErr != nil {
		return newInputError(ErrorCodeVerify, messageStagingPathInvalid)
	}
	targetAbs, targetResolveErr := artifact.ResolveInsideRoot(c.options.DataRoot, targetRel)
	if targetResolveErr != nil {
		return newInputError(ErrorCodeVerify, messageTargetPathInvalid)
	}
	expect := VerifyExpectation{
		ExpectedSize:   task.ExpectedSize,
		ExpectedSHA1:   task.ExpectedSHA1,
		ExpectedSHA256: task.ExpectedSHA256,
	}

	// 第 1、2 条：目标已经存在时的幂等与「不覆盖」判定。
	if targetInfo, statErr := os.Stat(targetAbs); statErr == nil && targetInfo.Mode().IsRegular() {
		return c.handleExistingTarget(task, paths, stagingAbs, targetAbs, targetRel, expect)
	}

	// 第 3 条：staging 必须存在且校验通过。
	// 暂存缺失属于「提交阶段文件系统事实」，因此用 DOWNLOAD_COMMIT_001，
	// 与「内容校验不通过」（DOWNLOAD_VERIFY_001）区分开。
	if _, statErr := os.Stat(stagingAbs); statErr != nil {
		return newSystemError(ErrorCodeCommit, messageStagingMissing, false)
	}
	verified, verifyErr := VerifyFile(stagingAbs, expect)
	if verifyErr != nil {
		c.quarantineStaging(task, stagingAbs, paths.PartRel, targetRel, verifyErr, verified, expect)
		return verifyErr
	}

	// 第 4 条：先写 journal 再提交（崩溃后由 recovery 依此判定）。
	crossVolume := c.options.ForceCrossVolume
	journal := commitJournal{
		TaskID:         task.ID,
		StagingRelPath: paths.PartRel,
		TargetRelPath:  targetRel,
		ExpectedSize:   task.ExpectedSize,
		ExpectedSHA1:   task.ExpectedSHA1,
		ExpectedSHA256: task.ExpectedSHA256,
		CrossVolume:    crossVolume,
		CreatedAtMs:    c.now(),
	}
	if journalErr := c.writeJournal(paths, journal); journalErr != nil {
		return journalErr
	}
	c.recordCommitting(task, paths.PartRel, targetRel)

	if mkdirErr := os.MkdirAll(filepath.Dir(targetAbs), targetDirPermission); mkdirErr != nil {
		c.logWarn(task, "download.commit.mkdir_failed", "无法创建目标目录")
		return newSystemError(ErrorCodeCommit, messageCommitFailed, false)
	}

	committed := false
	if !crossVolume {
		// 同卷：fsync 后 rename，一次元数据操作完成可见性切换。
		if syncErr := syncFile(stagingAbs); syncErr == nil {
			if renameErr := os.Rename(stagingAbs, targetAbs); renameErr == nil {
				committed = true
			}
		}
	}
	if !committed {
		// 跨卷（或同卷 rename 失败，例如 Unix 的 EXDEV）：复制 → fsync → rename 覆盖。
		if copyErr := c.commitByCopy(stagingAbs, targetAbs, task.ID); copyErr != nil {
			c.logWarn(task, "download.commit.copy_failed", "跨卷提交失败，已保留日志以便恢复")
			return copyErr
		}
		_ = os.Remove(stagingAbs)
		committed = true
	}
	if !committed {
		return newSystemError(ErrorCodeCommit, messageCommitFailed, false)
	}

	// 提交成功：清理 journal 与续传记录，落库完成状态。
	_ = os.Remove(paths.JournalAbs)
	_ = os.Remove(paths.RecordAbs)
	c.markCompleted(task, verified.Size)
	c.logInfo(task, "download.commit.completed", "已提交下载文件", map[string]string{
		"crossVolume": boolText(crossVolume),
		"bytes":       strconv.FormatInt(verified.Size, 10),
	})
	return nil
}

// handleExistingTarget 处理「目标已存在」的两种情形（卡面第 3 条）。
//
// 判定对象必须是**目标文件本身**：目标已存在说明这个制品此前已经提交过，
// 本次下载只是重复劳动。因此先校验目标，通过就直接成功；
// 不通过才考虑把本次下载的新文件隔离，绝不用新文件覆盖旧文件。
func (c *Committer) handleExistingTarget(
	task Task,
	paths stagingPaths,
	stagingAbs string,
	targetAbs string,
	targetRel string,
	expect VerifyExpectation,
) *Error {
	if verified, verifyErr := VerifyFile(targetAbs, expect); verifyErr == nil {
		// 目标存在且校验通过：幂等完成，不重写、不覆盖。
		_ = os.Remove(paths.JournalAbs)
		_ = os.Remove(paths.RecordAbs)
		_ = os.Remove(stagingAbs)
		c.markCompleted(task, verified.Size)
		c.logInfo(task, "download.commit.idempotent", "目标已存在且校验通过，跳过覆盖", nil)
		return nil
	}

	// 目标存在但校验不通过：旧文件保持原样（不自动删除），
	// 本次下载的新文件若能定位到就送进隔离区，避免它继续占着 staging。
	result := VerifyResult{}
	if _, statErr := os.Stat(stagingAbs); statErr == nil {
		if stagingResult, stagingErr := VerifyFile(stagingAbs, expect); stagingErr != nil {
			result = stagingResult
		}
		c.quarantineStaging(task, stagingAbs, paths.PartRel, targetRel,
			newInputError(ErrorCodeVerify, messageCommitTargetOccupied), result, expect)
	}
	_ = os.Remove(paths.JournalAbs)
	_ = os.Remove(paths.RecordAbs)
	c.markTargetInvalid(task)
	c.logWarn(task, "download.commit.target_occupied", "目标已有文件且校验不通过，已拒绝覆盖")
	return newInputError(ErrorCodeVerify, messageCommitTargetOccupied)
}

// commitByCopy 用「复制到目标目录临时文件 → fsync → rename」完成跨卷提交。
func (c *Committer) commitByCopy(stagingAbs, targetAbs, taskID string) *Error {
	tempAbs := targetAbs + crossVolumeTempSuffix + taskID
	if copyErr := copyPath(stagingAbs, tempAbs); copyErr != nil {
		_ = os.Remove(tempAbs)
		return copyErr
	}
	if renameErr := os.Rename(tempAbs, targetAbs); renameErr != nil {
		_ = os.Remove(tempAbs)
		return newSystemError(ErrorCodeCommit, messageCommitFailed, false)
	}
	return nil
}

// quarantineStaging 把校验失败的 staging 文件移入隔离区并记录原因。
//
// 隔离失败不属于「致命」：此时文件仍留在 staging 目录，恢复流程会把它
// 当作孤儿再次处理，因此这里只记日志，不把隔离失败升级成新的失败原因。
func (c *Committer) quarantineStaging(
	task Task,
	stagingAbs string,
	stagingRel string,
	targetRel string,
	cause *Error,
	result VerifyResult,
	expect VerifyExpectation,
) {
	if _, statErr := os.Stat(stagingAbs); statErr != nil {
		return
	}
	reason := QuarantineReason{
		TaskID:          task.ID,
		ErrorCode:       cause.Code,
		Message:         cause.Message,
		ExpectedSize:    expect.ExpectedSize,
		ActualSize:      result.Size,
		ExpectedSHA1:    normalizeExpectedHash(expect.ExpectedSHA1),
		ActualSHA1:      result.SHA1,
		ExpectedSHA256:  normalizeExpectedHash(expect.ExpectedSHA256),
		ActualSHA256:    result.SHA256,
		SourceRelPath:   stagingRel,
		TargetRelPath:   targetRel,
		QuarantinedAtMs: c.now(),
	}
	if _, quarantineErr := QuarantineFile(c.options.DataRoot, stagingAbs, reason); quarantineErr != nil {
		c.logWarn(task, "download.quarantine.failed", "隔离失败文件时出错")
		return
	}
	c.logWarn(task, "download.quarantine.moved", "校验失败的文件已移入隔离区")
}

// writeJournal 写入提交 journal 并 fsync。
func (c *Committer) writeJournal(paths stagingPaths, journal commitJournal) *Error {
	payload, marshalErr := json.MarshalIndent(journal, "", "  ")
	if marshalErr != nil {
		return newSystemError(ErrorCodeCommit, messageCommitFailed, false)
	}
	payload = append(payload, '\n')
	if writeErr := writeFileAtomically(paths.JournalAbs, payload); writeErr != nil {
		return newSystemError(ErrorCodeCommit, messageCommitFailed, false)
	}
	return nil
}

// recordCommitting 把 staging/target/哈希落库（best-effort）。
//
// 之所以是 best-effort：数据库是索引而非真相源（P3-04 已如此约定），
// 文件系统与 journal 才是。库不可用时提交仍然必须能完成。
func (c *Committer) recordCommitting(task Task, stagingRel, targetRel string) {
	db := c.options.DB
	if db == nil {
		return
	}
	_, err := db.ExecContext(
		context.Background(),
		markCommittingSQL,
		stagingRel,
		targetRel,
		task.ExpectedSHA256,
		c.now(),
		task.ID,
	)
	if err != nil {
		c.logWarn(task, "download.commit.persist_failed", "提交前落库失败（数据库作为索引，不影响文件提交）")
	}
}

// markCompleted 落库完成状态（best-effort）。
func (c *Committer) markCompleted(task Task, size int64) {
	db := c.options.DB
	if db == nil {
		return
	}
	now := c.now()
	_, err := db.ExecContext(
		context.Background(),
		markCompletedSQL,
		string(StateCompleted),
		"",
		now,
		now,
		now,
		task.ID,
	)
	if err != nil {
		c.logWarn(task, "download.commit.persist_failed", "提交完成状态落库失败（数据库作为索引）")
	}
	_ = size
}

// markTargetInvalid 记录「目标已存在但校验不通过」的失效标记（best-effort）。
func (c *Committer) markTargetInvalid(task Task) {
	db := c.options.DB
	if db == nil {
		return
	}
	_, err := db.ExecContext(
		context.Background(),
		markTargetInvalidSQL,
		ErrorCodeVerify,
		messageCommitTargetOccupied,
		c.now(),
		task.ID,
	)
	if err != nil {
		c.logWarn(task, "download.commit.persist_failed", "目标失效标记落库失败（数据库作为索引）")
	}
}

// logInfo / logWarn 写入脱敏日志（只含任务 ID、目标类别与字节数）。
func (c *Committer) logInfo(task Task, event, message string, extra map[string]string) {
	if c.options.Logger == nil {
		return
	}
	attributes := map[string]string{"taskId": task.ID}
	for key, value := range extra {
		attributes[key] = value
	}
	c.options.Logger.Info(message, logging.Fields{Event: event, Attributes: attributes})
}

// logWarn 写入 warn 级脱敏日志。
func (c *Committer) logWarn(task Task, event, message string) {
	if c.options.Logger == nil {
		return
	}
	c.options.Logger.Warn(message, logging.Fields{
		Event:      event,
		Attributes: map[string]string{"taskId": task.ID},
	})
}

// syncFile 打开文件并 fsync，确保内容真正落盘。
func syncFile(path string) *Error {
	file, openErr := os.OpenFile(path, os.O_RDWR, filePermission)
	if openErr != nil {
		return newSystemError(ErrorCodeCommit, messageCommitFailed, false)
	}
	syncErr := file.Sync()
	closeErr := file.Close()
	if syncErr != nil || closeErr != nil {
		return newSystemError(ErrorCodeCommit, messageCommitFailed, false)
	}
	return nil
}

// currentMillis 返回 UTC Unix 毫秒时间戳。
func currentMillis() int64 {
	return time.Now().UnixMilli()
}
