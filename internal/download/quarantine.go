package download

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"example.com/ninevoxel/launcher/internal/artifact"
)

// 隔离区相关的固定后缀与默认保留策略。
const (
	// quarantineBadSuffix 是坏文件后缀（与 artifact.QuarantineRelPath 一致）。
	quarantineBadSuffix = ".bad"

	// quarantineReasonSuffix 是隔离原因文件名后缀。
	//
	// 用「同名 .reason.json」而不是共用一个 reasons.json：后者在并发下载时
	// 需要额外加锁与合并，而且一旦写坏会连带丢掉全部任务的隔离原因。
	quarantineReasonSuffix = ".reason.json"

	// defaultQuarantineKeepDays 是默认保留天数（只记录、不自动执行）。
	defaultQuarantineKeepDays = 7

	// defaultQuarantineKeepRecent 是默认保留最近条数（只记录、不自动执行）。
	defaultQuarantineKeepRecent = 10
)

// QuarantineReason 是隔离原因，写入 `<taskId>.bad.reason.json`。
//
// 字段只包含「判断这次为什么失败」所需的信息：
// 错误码、期望与实测的大小/哈希、以及**相对**路径。
// 绝对路径一律不写，避免隔离目录成为泄漏用户目录名的载体。
type QuarantineReason struct {
	// TaskID 是任务标识，同时决定隔离文件所在的分桶。
	TaskID string `json:"taskId"`

	// ErrorCode 是判定失败时使用的稳定错误码。
	ErrorCode string `json:"errorCode"`

	// Message 是已脱敏的中文说明。
	Message string `json:"message,omitempty"`

	// ExpectedSize / ActualSize 是期望与实测字节数（未知时为 0）。
	ExpectedSize int64 `json:"expectedSize,omitempty"`
	ActualSize   int64 `json:"actualSize,omitempty"`

	// ExpectedSHA1 / ActualSHA1 是期望与实测 SHA-1。
	ExpectedSHA1 string `json:"expectedSha1,omitempty"`
	ActualSHA1   string `json:"actualSha1,omitempty"`

	// ExpectedSHA256 / ActualSHA256 是期望与实测 SHA-256。
	ExpectedSHA256 string `json:"expectedSha256,omitempty"`
	ActualSHA256   string `json:"actualSha256,omitempty"`

	// SourceRelPath 是被隔离文件在数据根内的相对路径（如 downloads/staging/xxx.part）。
	SourceRelPath string `json:"sourceRelPath,omitempty"`

	// TargetRelPath 是原本要提交到的目标相对路径。
	TargetRelPath string `json:"targetRelPath,omitempty"`

	// QuarantinedAtMs 是隔离发生的 UTC Unix 毫秒时间戳。
	QuarantinedAtMs int64 `json:"quarantinedAtMs"`
}

// QuarantinedFile 是隔离完成后的结果路径（相对路径用于展示与日志）。
type QuarantinedFile struct {
	// TaskID 是被隔离文件所属任务。
	TaskID string

	// RelPath 是坏文件相对路径（downloads/quarantine/<前2位>/<taskId>.bad）。
	RelPath string

	// ReasonRelPath 是隔离原因相对路径（RelPath + ".reason.json"）。
	ReasonRelPath string
}

// RetentionPolicy 是隔离区的清理策略。
//
// 卡面第 4 条要求「清理策略只记录不执行」：P3 阶段任何自动删除都可能
// 在用户还没看到失败原因时就把证据抹掉，因此本类型只提供描述，
// 真正的清理由后续阶段在用户确认后实现。
type RetentionPolicy struct {
	// KeepDays 为保留天数。
	KeepDays int

	// KeepRecent 为保底保留条数（即使超期也至少留这么多条）。
	KeepRecent int
}

// DefaultRetentionPolicy 返回默认清理策略（7 天或最近 10 个）。
func DefaultRetentionPolicy() RetentionPolicy {
	return RetentionPolicy{KeepDays: defaultQuarantineKeepDays, KeepRecent: defaultQuarantineKeepRecent}
}

// Describe 返回可展示的策略描述，供界面与文档引用。
func (p RetentionPolicy) Describe() string {
	return fmt.Sprintf("隔离区默认保留 %d 天或最近 %d 个文件（本阶段只记录，不自动删除）", p.KeepDays, p.KeepRecent)
}

// QuarantineFile 把校验失败的文件移动到隔离区并写入原因文件。
//
// 隔离目录只增不删（卡面第 4 条）：本函数不删除任何已有隔离文件，
// 同名文件已存在时直接覆盖那一个（同一任务重复失败时以最新一次为准）。
//
// sourceAbs 必须已经位于数据根之内，调用方负责保证这一点；
// 本函数只把目标路径拼在隔离区下，不信任任何外部输入的绝对路径。
func QuarantineFile(dataRoot, sourceAbs string, reason QuarantineReason) (QuarantinedFile, *Error) {
	if dataRoot == "" {
		return QuarantinedFile{}, newInputError(ErrorCodeVerify, messageStagingPathInvalid)
	}
	relPath, relErr := artifact.QuarantineRelPath(reason.TaskID)
	if relErr != nil {
		return QuarantinedFile{}, newInputError(ErrorCodeVerify, messageStagingPathInvalid)
	}
	destAbs, resolveErr := artifact.ResolveInsideRoot(dataRoot, relPath)
	if resolveErr != nil {
		return QuarantinedFile{}, newInputError(ErrorCodeVerify, messageStagingPathInvalid)
	}
	if mkdirErr := os.MkdirAll(filepath.Dir(destAbs), quarantineDirPermission); mkdirErr != nil {
		return QuarantinedFile{}, newSystemError(ErrorCodeVerify, messageQuarantineFailed, false)
	}
	if moveErr := movePath(sourceAbs, destAbs); moveErr != nil {
		return QuarantinedFile{}, newSystemError(ErrorCodeVerify, messageQuarantineFailed, false)
	}

	reasonRelPath := relPath + quarantineReasonSuffix
	reasonAbs, reasonResolveErr := artifact.ResolveInsideRoot(dataRoot, reasonRelPath)
	if reasonResolveErr != nil {
		return QuarantinedFile{}, newInputError(ErrorCodeVerify, messageStagingPathInvalid)
	}
	if writeErr := writeQuarantineReason(reasonAbs, reason); writeErr != nil {
		return QuarantinedFile{}, newSystemError(ErrorCodeVerify, messageQuarantineFailed, false)
	}
	return QuarantinedFile{TaskID: reason.TaskID, RelPath: relPath, ReasonRelPath: reasonRelPath}, nil
}

// writeQuarantineReason 把隔离原因写成 JSON 文件。
//
// 先写临时文件再 rename：半截 JSON 会让事后复盘彻底失效，
// 而 rename 在 Windows 与 Unix 上都是原子替换。
func writeQuarantineReason(path string, reason QuarantineReason) *Error {
	payload, marshalErr := json.MarshalIndent(reason, "", "  ")
	if marshalErr != nil {
		return newSystemError(ErrorCodeVerify, messageQuarantineFailed, false)
	}
	payload = append(payload, '\n')
	if writeErr := writeFileAtomically(path, payload); writeErr != nil {
		return writeErr
	}
	return nil
}

// movePath 把 src 移动到 dst，跨卷时退化为「复制 + 删除源」。
//
// 先尝试 os.Rename：同卷时这是一次元数据操作，快且原子；
// 跨卷时 rename 会返回 EXDEV，此时只能复制。复制失败不删除源文件，
// 宁可留下重复数据，也不能在移动失败时把用户唯一的一份文件删掉。
func movePath(src, dst string) *Error {
	if renameErr := os.Rename(src, dst); renameErr == nil {
		return nil
	}
	if copyErr := copyPath(src, dst); copyErr != nil {
		return copyErr
	}
	_ = os.Remove(src)
	return nil
}

// copyPath 复制文件并 fsync 目标，确保内容真正落盘。
func copyPath(src, dst string) *Error {
	source, openErr := os.Open(src)
	if openErr != nil {
		return newSystemError(ErrorCodeVerify, messageCommitFailed, false)
	}
	defer func() {
		_ = source.Close()
	}()

	target, createErr := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, filePermission)
	if createErr != nil {
		return newSystemError(ErrorCodeVerify, messageCommitFailed, false)
	}
	buffer := make([]byte, verifyBufferSize)
	if _, copyErr := io.CopyBuffer(target, source, buffer); copyErr != nil {
		_ = target.Close()
		return newSystemError(ErrorCodeVerify, messageCommitFailed, false)
	}
	if syncErr := target.Sync(); syncErr != nil {
		_ = target.Close()
		return newSystemError(ErrorCodeVerify, messageCommitFailed, false)
	}
	if closeErr := target.Close(); closeErr != nil {
		return newSystemError(ErrorCodeVerify, messageCommitFailed, false)
	}
	return nil
}

// writeFileAtomically 以「临时文件 + rename」方式写文件。
func writeFileAtomically(path string, payload []byte) *Error {
	dir := filepath.Dir(path)
	temp, tempErr := os.CreateTemp(dir, ".tmp-*")
	if tempErr != nil {
		return newSystemError(ErrorCodeVerify, messageQuarantineFailed, false)
	}
	tempPath := temp.Name()
	if _, writeErr := temp.Write(payload); writeErr != nil {
		_ = temp.Close()
		_ = os.Remove(tempPath)
		return newSystemError(ErrorCodeVerify, messageQuarantineFailed, false)
	}
	if syncErr := temp.Sync(); syncErr != nil {
		_ = temp.Close()
		_ = os.Remove(tempPath)
		return newSystemError(ErrorCodeVerify, messageQuarantineFailed, false)
	}
	if closeErr := temp.Close(); closeErr != nil {
		_ = os.Remove(tempPath)
		return newSystemError(ErrorCodeVerify, messageQuarantineFailed, false)
	}
	if renameErr := os.Rename(tempPath, path); renameErr != nil {
		_ = os.Remove(tempPath)
		return newSystemError(ErrorCodeVerify, messageQuarantineFailed, false)
	}
	return nil
}
