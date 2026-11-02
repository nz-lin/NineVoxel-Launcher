package download

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"

	"example.com/ninevoxel/launcher/internal/artifact"
	"example.com/ninevoxel/launcher/internal/logging"
)

// 本文件负责「进程意外退出后的收敛」，是 P3-06 的第五件事。
//
// 设计原则是把「判定」和「执行」彻底分开：
//   - DecideRecovery 是纯函数，只吃任务快照与磁盘观察，不碰文件系统；
//   - Recover 才真正读目录、删文件、写数据库、发事件。
//
// 这样判定矩阵可以在测试里用表驱动穷举，而不需要在测试中伪造磁盘状态。
// 判定依据**只允许**是持久化状态（download_tasks 行 + .part.json + 目标文件），
// 不使用内存中的运行状态：进程重启后内存状态已经不存在了。

// RecoveryAction 描述恢复时对单个任务采取的动作。
type RecoveryAction string

const (
	// RecoveryActionKeep 表示保持原状态，不做任何改动。
	RecoveryActionKeep RecoveryAction = "keep"

	// RecoveryActionComplete 表示目标已存在且校验通过，直接补记为完成。
	RecoveryActionComplete RecoveryAction = "complete"

	// RecoveryActionPause 表示暂存可续传，转为暂停等待重新排期。
	RecoveryActionPause RecoveryAction = "pause"

	// RecoveryActionRequeue 表示暂存不可用，丢弃后回到队列重新下载。
	RecoveryActionRequeue RecoveryAction = "requeue"

	// RecoveryActionFail 表示无法自动恢复，标记失败交由用户处理。
	RecoveryActionFail RecoveryAction = "fail"
)

// DiskObservation 是恢复判定所依赖的磁盘事实。
//
// 全部字段都来自「持久化状态」的文件：目标文件、暂存 .part、.part.json 记录。
type DiskObservation struct {
	// TargetExists 表示目标文件已存在于目标位置。
	TargetExists bool

	// TargetValid 表示目标文件已通过大小与哈希校验（仅当 TargetExists 时可信）。
	TargetValid bool

	// StagingExists 表示暂存文件存在。
	StagingExists bool

	// PartSize 是暂存文件的实际字节数。
	PartSize int64

	// PartRecordExists 表示 .part.json 记录存在。
	PartRecordExists bool

	// PartOffset 是 .part.json 记录中的已写入字节数。
	PartOffset int64

	// PartETag / PartLastModified 是记录中的实体标记，用于续传一致性判定。
	PartETag         string
	PartLastModified string
}

// RecoveryDecision 是单个任务的恢复决策。
type RecoveryDecision struct {
	// TaskID 为任务标识。
	TaskID string

	// FromState 是恢复前持久化的状态。
	FromState State

	// Action 是建议动作。
	Action RecoveryAction

	// NextState 是动作执行后的状态。
	NextState State

	// DropStaging 表示需要删除该任务的暂存文件与记录。
	DropStaging bool

	// Reason 是中文原因说明，仅写入日志，不展示给用户。
	Reason string
}

// DecideRecovery 根据持久化状态与磁盘观察给出恢复决策。
//
// 完整矩阵见 doc.go 的「崩溃恢复矩阵」一节。这里的顺序很重要：
// 先判终态（终态永不回退），再按状态分组处理。
func DecideRecovery(task Task, observation DiskObservation) RecoveryDecision {
	decision := RecoveryDecision{
		TaskID:    task.ID,
		FromState: task.State,
		Action:    RecoveryActionKeep,
		NextState: task.State,
	}

	if IsTerminal(task.State) {
		decision.Reason = "任务已处于终态，恢复过程不介入"
		return decision
	}

	switch task.State {
	case StateCommitting:
		// 提交是唯一可能「文件已落盘但状态没写完」的阶段。
		// 只要目标通过校验就补记为完成，避免重复下载。
		if observation.TargetExists && observation.TargetValid {
			decision.Action = RecoveryActionComplete
			decision.NextState = StateCompleted
			decision.DropStaging = true
			decision.Reason = "目标已存在且校验通过，补记为完成"
			return decision
		}
		decision.Action = RecoveryActionRequeue
		decision.NextState = StateQueued
		decision.DropStaging = true
		decision.Reason = "提交中断且目标不可用，丢弃暂存后重新下载"
		return decision

	case StateDownloading, StateVerifying:
		// 下载与校验中断时，只有「记录与实际字节完全一致」才允许续传。
		if canResumeFromDisk(observation) {
			decision.Action = RecoveryActionPause
			decision.NextState = StatePaused
			decision.Reason = "暂存字节与记录一致，转为暂停等待恢复"
			return decision
		}
		decision.Action = RecoveryActionRequeue
		decision.NextState = StateQueued
		decision.DropStaging = true
		decision.Reason = "暂存字节与记录不一致，丢弃后重新下载"
		return decision

	case StatePaused:
		if canResumeFromDisk(observation) {
			decision.Reason = "暂停状态且暂存完整，保持暂停"
			return decision
		}
		decision.Action = RecoveryActionRequeue
		decision.NextState = StateQueued
		decision.DropStaging = true
		decision.Reason = "暂停状态但暂存不完整，回到队列重新下载"
		return decision

	case StateResolving:
		// 解析阶段还没开始写暂存，直接回队列即可。
		decision.Action = RecoveryActionRequeue
		decision.NextState = StateQueued
		decision.DropStaging = true
		decision.Reason = "解析阶段中断，回到队列"
		return decision

	case StateQueued:
		decision.Reason = "任务仍在队列中，保持原状"
		return decision
	}

	decision.Action = RecoveryActionFail
	decision.NextState = StateFailed
	decision.Reason = "状态值无法识别，标记失败"
	return decision
}

// canResumeFromDisk 判断暂存是否具备续传条件。
//
// 三个条件缺一不可：暂存存在、记录存在、记录偏移与实际字节一致；
// 另外必须有实体标记（ETag 或 Last-Modified），否则服务器无法确认内容未变。
func canResumeFromDisk(observation DiskObservation) bool {
	if !observation.StagingExists || !observation.PartRecordExists {
		return false
	}
	if observation.PartOffset <= 0 || observation.PartSize != observation.PartOffset {
		return false
	}
	return observation.PartETag != "" || observation.PartLastModified != ""
}

// RecoveryOptions 是执行恢复所需的依赖。
type RecoveryOptions struct {
	// DataRoot 是数据根绝对路径。
	DataRoot string

	// Store 是任务持久化存储，用于读取与写回状态。
	Store TaskStore

	// Logger 可选，用于记录恢复过程中的脱敏日志。
	Logger TransportLogger

	// Now 可选，返回当前毫秒时间戳，便于测试注入。
	Now func() int64
}

// RecoveryResult 汇总一次恢复执行的结果。
type RecoveryResult struct {
	// Decisions 是每个任务的判定结果（含未改动的任务）。
	Decisions []RecoveryDecision

	// Orphans 是被清理的孤儿暂存文件相对路径。
	Orphans []string

	// Recovered 是被实际改写状态的任务数。
	Recovered int
}

// Recover 执行启动期恢复：读取持久化任务、观察磁盘、应用决策、清理孤儿。
//
// 该函数在应用启动时调用一次；多次调用是幂等的（第二次通常全部是 keep）。
// 参数 ctx 只影响数据库操作，不影响文件删除。
func Recover(ctx context.Context, options RecoveryOptions) (RecoveryResult, *Error) {
	result := RecoveryResult{}
	if options.Store == nil {
		return result, newSystemError(ErrorCodeCommit, messageRecoveryStoreMissing, false)
	}
	if options.DataRoot == "" {
		return result, newSystemError(ErrorCodeCommit, messageCommitFailed, false)
	}
	now := options.Now
	if now == nil {
		now = currentMillis
	}

	tasks, loadErr := options.Store.LoadAll(ctx)
	if loadErr != nil {
		return result, newSystemError(ErrorCodeCommit, messageRecoveryLoadFailed, false)
	}

	stagingRoot := filepath.Join(options.DataRoot, artifact.DirStaging)
	liveTaskIDs := make(map[string]struct{}, len(tasks))
	terminalTaskIDs := make(map[string]struct{}, len(tasks))

	for _, task := range tasks {
		liveTaskIDs[task.ID] = struct{}{}
		if IsTerminal(task.State) {
			terminalTaskIDs[task.ID] = struct{}{}
		}

		observation, observeErr := observeDisk(options.DataRoot, task)
		if observeErr != nil {
			// 观察失败不应中断整体恢复：记日志后按「磁盘不可用」判定。
			logRecoveryWarn(options.Logger, task, "download.recovery.observe_failed", "无法读取任务对应的磁盘状态")
			observation = DiskObservation{}
		}
		decision := DecideRecovery(task, observation)
		result.Decisions = append(result.Decisions, decision)

		if decision.Action == RecoveryActionKeep && !decision.DropStaging {
			continue
		}
		if decision.DropStaging {
			// 丢弃暂存是「恢复到可用状态」的前置条件，失败只记录不阻断。
			if dropErr := dropStaging(options.DataRoot, task.ID); dropErr != nil {
				logRecoveryWarn(options.Logger, task, "download.recovery.drop_failed", "丢弃暂存文件失败")
			}
		}
		if decision.NextState == task.State {
			continue
		}

		updated := task
		updated.State = decision.NextState
		updated.UpdatedAtMs = now()
		if decision.NextState == StateCompleted {
			updated.CompletedAtMs = updated.UpdatedAtMs
			updated.VerifiedAtMs = updated.UpdatedAtMs
		}
		if decision.NextState == StateQueued {
			// 回到队列意味着要从头开始，进度字段必须一并归零。
			updated.DownloadedSize = 0
			updated.RangeStart = 0
		}
		if saveErr := options.Store.Save(ctx, updated); saveErr != nil {
			return result, newSystemError(ErrorCodeCommit, messageRecoverySaveFailed, false)
		}
		result.Recovered++
		logRecoveryTask(options.Logger, updated, decision)
		// 先落库再生效事件：与队列的「先落状态再发事件」保持一致。
		emitTaskChanged(updated, 0)
	}

	orphans, orphanErr := cleanOrphanStaging(stagingRoot, liveTaskIDs, terminalTaskIDs)
	if orphanErr != nil {
		logRecoveryMessage(options.Logger, "download.recovery.orphan_failed", "清理孤儿暂存文件失败")
	}
	result.Orphans = orphans
	return result, nil
}

// observeDisk 采集单个任务在磁盘上的实际情形。
func observeDisk(dataRoot string, task Task) (DiskObservation, *Error) {
	observation := DiskObservation{}
	paths, pathErr := resolveStagingPaths(dataRoot, task.ID)
	if pathErr != nil {
		return observation, pathErr
	}

	if info, statErr := os.Stat(paths.PartAbs); statErr == nil && info.Mode().IsRegular() {
		observation.StagingExists = true
		observation.PartSize = info.Size()
	}
	if record, ok := readStagingPartRecord(paths.RecordAbs); ok {
		observation.PartRecordExists = true
		observation.PartOffset = record.Offset
		observation.PartETag = record.ETag
		observation.PartLastModified = record.LastModified
	}

	if task.TargetRelPath == "" {
		return observation, nil
	}
	targetAbs, resolveErr := artifact.ResolveInsideRoot(dataRoot, task.TargetRelPath)
	if resolveErr != nil {
		// 目标路径非法时不做任何判定，交由上层按「目标不可用」处理。
		return observation, nil
	}
	info, statErr := os.Stat(targetAbs)
	if statErr != nil || !info.Mode().IsRegular() {
		return observation, nil
	}
	observation.TargetExists = true
	expect := VerifyExpectation{
		ExpectedSize:   task.ExpectedSize,
		ExpectedSHA1:   task.ExpectedSHA1,
		ExpectedSHA256: task.ExpectedSHA256,
	}
	if _, verifyErr := VerifyFile(targetAbs, expect); verifyErr == nil {
		observation.TargetValid = true
	}
	return observation, nil
}

// readStagingPartRecord 读取 .part.json 记录，失败时返回 false。
func readStagingPartRecord(path string) (StagingPartRecord, bool) {
	payload, readErr := os.ReadFile(path)
	if readErr != nil {
		return StagingPartRecord{}, false
	}
	var record StagingPartRecord
	if unmarshalErr := json.Unmarshal(payload, &record); unmarshalErr != nil {
		return StagingPartRecord{}, false
	}
	return record, true
}

// dropStaging 删除某个任务的暂存文件与记录（不存在时视为成功）。
func dropStaging(dataRoot, taskID string) *Error {
	paths, pathErr := resolveStagingPaths(dataRoot, taskID)
	if pathErr != nil {
		return pathErr
	}
	for _, path := range []string{paths.PartAbs, paths.RecordAbs, paths.JournalAbs} {
		if removeErr := os.Remove(path); removeErr != nil && !os.IsNotExist(removeErr) {
			return newSystemError(ErrorCodeCommit, messageCommitFailed, false)
		}
	}
	return nil
}

// cleanOrphanStaging 删除不属于活跃任务的暂存文件，返回被删除的相对路径。
//
// 只删 downloads/staging 下的 `.part` 与 `.part.json`：
// 这两个后缀是下载过程的中间产物，失去任务归属就再无价值。
// quarantine 目录不在此处清理（隔离文件只增不删，见 D-29）。
func cleanOrphanStaging(stagingRoot string, liveTaskIDs, terminalTaskIDs map[string]struct{}) ([]string, *Error) {
	entries, readErr := os.ReadDir(stagingRoot)
	if readErr != nil {
		if os.IsNotExist(readErr) {
			return nil, nil
		}
		return nil, newSystemError(ErrorCodeCommit, messageCommitFailed, false)
	}

	removed := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		fileName := entry.Name()
		taskID, ok := taskIDFromStagingFileName(fileName)
		if !ok {
			continue
		}
		if _, alive := liveTaskIDs[taskID]; alive {
			if _, terminal := terminalTaskIDs[taskID]; !terminal {
				continue
			}
		}
		absPath := filepath.Join(stagingRoot, fileName)
		if removeErr := os.Remove(absPath); removeErr != nil {
			continue
		}
		removed = append(removed, filepath.ToSlash(filepath.Join(artifact.DirStaging, fileName)))
	}
	sort.Strings(removed)
	return removed, nil
}

// taskIDFromStagingFileName 从暂存文件名反解任务 ID。
//
// 命名规则来自 commit.go：`<taskId>.part`、`<taskId>.part.json`、`<taskId>.part.commit.json`。
func taskIDFromStagingFileName(fileName string) (string, bool) {
	const partSuffix = ".part"
	index := len(fileName)
	for _, suffix := range []string{partSuffix + partRecordSuffix, partSuffix + commitJournalSuffix, partSuffix} {
		if len(fileName) > len(suffix) && fileName[len(fileName)-len(suffix):] == suffix {
			index = len(fileName) - len(suffix)
			break
		}
	}
	if index <= 0 || index == len(fileName) {
		return "", false
	}
	return fileName[:index], true
}

// logRecoveryTask 记录一次状态收敛（只含任务 ID 与状态，不含路径）。
func logRecoveryTask(logger TransportLogger, task Task, decision RecoveryDecision) {
	if logger == nil {
		return
	}
	logger.Info("恢复下载任务状态", loggingFields("download.recovery.applied", map[string]string{
		"taskId":    task.ID,
		"fromState": string(decision.FromState),
		"toState":   string(decision.NextState),
		"action":    string(decision.Action),
	}))
}

// logRecoveryWarn 记录恢复过程中的非致命问题。
func logRecoveryWarn(logger TransportLogger, task Task, event, message string) {
	if logger == nil {
		return
	}
	logger.Warn(message, loggingFields(event, map[string]string{"taskId": task.ID}))
}

// logRecoveryMessage 记录与具体任务无关的恢复日志。
func logRecoveryMessage(logger TransportLogger, event, message string) {
	if logger == nil {
		return
	}
	logger.Warn(message, loggingFields(event, nil))
}

// loggingFields 统一构造脱敏日志字段。
//
// 恢复过程只允许记录任务 ID 与状态，不得记录任何路径，
// 因此这里的 attributes 一律由调用方显式给出、不做自动补全。
func loggingFields(event string, attributes map[string]string) logging.Fields {
	return logging.Fields{Event: event, Attributes: attributes}
}
