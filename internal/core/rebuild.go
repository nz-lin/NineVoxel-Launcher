package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"example.com/ninevoxel/launcher/internal/artifact"
)

// 本文件实现核心重建：把全部核心资源重建到暂存目录，校验通过后再切换。
//
// 为什么需要事务日志：核心由 libraries / assets / versions / core-index 四个
// **并列目录**组成，文件系统无法把它们作为一个整体原子替换。逐个 Rename 的
// 过程中若进程崩溃，会留下「一半新、一半旧」的混合状态——这正是重建最不能
// 出现的后果。因此每一步都写日志，崩溃后按日志回滚。
//
// 明确不做：不假装这是原子操作。文档与错误文案都按「可恢复的多目录切换」
// 表述，避免后来者误以为可以省掉日志。

const (
	// rebuildStagingDir 是重建暂存目录（相对核心根）。
	rebuildStagingDir = ".nv-core-staging"
	// rebuildBackupDir 是旧目录的备份区（相对核心根）。
	rebuildBackupDir = ".nv-core-backup"

	// journalSchemaVersion 是事务日志结构版本。
	journalSchemaVersion = 1
)

// 重建涉及的组件名。顺序即切换顺序：索引必须最后切换，
// 否则中途崩溃时索引会指向尚未就位的版本目录。
var rebuildComponents = []string{
	librariesDirName,
	assetsDirName,
	versionsDirName,
	indexDirName,
}

// 事务状态。
const (
	// stateStaging 表示正在构建暂存目录。
	stateStaging = "staging"
	// stateVerifying 表示正在校验暂存目录。
	stateVerifying = "verifying"
	// stateSwitching 表示正在逐目录切换。
	stateSwitching = "switching"
	// stateVerifyingNew 表示已切换完成、正在校验新核心。
	stateVerifyingNew = "verifying-new"
	// stateCommitted 表示切换成功、等待清理。
	stateCommitted = "committed"
)

// 组件切换状态。
const (
	// componentPending 表示尚未切换。
	componentPending = "pending"
	// componentMovedOut 表示旧目录已移到备份区。
	componentMovedOut = "moved-out"
	// componentMovedIn 表示新目录已就位。
	componentMovedIn = "moved-in"
)

// rebuildJournal 是重建事务的持久化记录。
type rebuildJournal struct {
	SchemaVersion int    `json:"schemaVersion"`
	OperationID   string `json:"operationId"`
	State         string `json:"state"`
	// Components 记录每个组件的切换进度；键为组件名。
	Components map[string]string `json:"components"`
	// Step 记录最近一次进入的子步骤，供崩溃后定位。
	Step        string `json:"step,omitempty"`
	CreatedAtMs int64  `json:"createdAtMs"`
}

// RebuildOptions 是核心重建的入参。
type RebuildOptions struct {
	// Root 为核心根（数据根）。
	Root string
	// Locks 为核心锁管理器；为空时使用独立实例（调用方应在外部保证互斥）。
	Locks *Locks
	// Populate 把全部核心资源构建到 stagingRoot 下。
	//
	// 参数为暂存目录的绝对路径：调用方在其中创建 libraries / assets /
	// versions / core-index 四个子目录。
	Populate func(stagingRoot string) error
	// Verify 校验给定核心根是否完整；为空时跳过（仅做结构校验）。
	Verify func(root string) error
	// Progress 可选，用于上报阶段。
	Progress func(phase string, ratio float64)
}

// rebuildCrashHook 是测试用的崩溃注入点。
//
// 返回非 nil 表示「进程在这里崩溃」：Rebuild 会直接返回该错误，**不执行
// 回滚**，并在磁盘上留下事务日志，供恢复流程验证。
var rebuildCrashHook func(step string) error

// crashPoint 在指定步骤触发崩溃注入。
func crashPoint(step string) error {
	if rebuildCrashHook == nil {
		return nil
	}
	return rebuildCrashHook(step)
}

// Rebuild 事务化重建核心。
//
// 流程：
//  1. 清理未完成的历史事务（若有且可恢复）；
//  2. 确认没有游戏正在运行；
//  3. 在暂存目录构建全部资源；
//  4. 校验暂存目录；
//  5. 逐目录切换（索引最后），每步写日志；
//  6. 校验新核心，成功后标记完成并清理备份。
//
// 任一步失败都会尝试回滚；回滚失败时保留日志，由下次启动的恢复流程处理。
func Rebuild(opts RebuildOptions) error {
	if opts.Root == "" {
		return errors.New("核心根目录为空，无法重建")
	}
	if opts.Populate == nil {
		return errors.New("未提供核心构建步骤，无法重建")
	}
	root, absErr := filepath.Abs(opts.Root)
	if absErr != nil {
		return errors.New("核心根目录无效")
	}

	if recoverErr := RecoverRebuild(root); recoverErr != nil {
		return fmt.Errorf("存在未完成的重建事务且无法自动恢复：%w", recoverErr)
	}
	if busy, busyErr := coreInUse(root); busyErr != nil {
		return busyErr
	} else if busy {
		return errors.New("有游戏正在运行，请退出游戏后再重建核心")
	}

	var unlock func()
	if opts.Locks != nil {
		unlock = opts.Locks.GlobalWrite()
		defer unlock()
	}

	operationID := fmt.Sprintf("rebuild-%d", time.Now().UTC().UnixMilli())
	stagingRoot := filepath.Join(root, rebuildStagingDir, operationID)
	backupRoot := filepath.Join(root, rebuildBackupDir, operationID)
	journal := &rebuildJournal{
		SchemaVersion: journalSchemaVersion,
		OperationID:   operationID,
		State:         stateStaging,
		Components:    map[string]string{},
		CreatedAtMs:   time.Now().UTC().UnixMilli(),
	}
	for _, component := range rebuildComponents {
		journal.Components[component] = componentPending
	}
	if writeErr := writeJournal(root, journal); writeErr != nil {
		return writeErr
	}

	// 暂存目录必须能创建成功；失败说明磁盘不可写或路径被占用。
	if mkErr := os.MkdirAll(stagingRoot, 0o755); mkErr != nil {
		_ = removeJournal(root, operationID)
		return errors.New("无法创建重建暂存目录")
	}
	// 暂存目录与目标目录必须同卷：跨卷时 Rename 会退化为复制，
	// 「切换到一半」的语义就不再成立。
	if sameVolumeErr := ensureSameVolume(root, stagingRoot); sameVolumeErr != nil {
		_ = os.RemoveAll(stagingRoot)
		_ = removeJournal(root, operationID)
		return sameVolumeErr
	}

	report(opts.Progress, "正在构建核心资源", 0.05)
	if populateErr := opts.Populate(stagingRoot); populateErr != nil {
		_ = os.RemoveAll(stagingRoot)
		_ = removeJournal(root, operationID)
		return populateErr
	}
	if err := crashPoint("populated"); err != nil {
		return err
	}

	journal.State = stateVerifying
	journal.Step = "verify-staging"
	if writeErr := writeJournal(root, journal); writeErr != nil {
		return writeErr
	}
	if opts.Verify != nil {
		report(opts.Progress, "正在校验暂存核心", 0.6)
		if verifyErr := opts.Verify(stagingRoot); verifyErr != nil {
			_ = os.RemoveAll(stagingRoot)
			_ = removeJournal(root, operationID)
			return verifyErr
		}
	}

	// 开始切换。此后失败一律走回滚。
	//
	// 切换前再检查一次租约：构建与校验可能耗时数分钟，期间用户完全可能启动
	// 游戏。只在入口检查一次会留下一个很大的窗口，让切换发生在游戏运行时。
	if busy, busyErr := coreInUse(root); busyErr != nil {
		_ = os.RemoveAll(stagingRoot)
		_ = removeJournal(root, operationID)
		return busyErr
	} else if busy {
		_ = os.RemoveAll(stagingRoot)
		_ = removeJournal(root, operationID)
		return errors.New("构建期间有游戏启动，已取消重建；请退出游戏后重试")
	}

	journal.State = stateSwitching
	journal.Step = ""
	if writeErr := writeJournal(root, journal); writeErr != nil {
		return writeErr
	}

	for index, component := range rebuildComponents {
		report(opts.Progress, "正在切换 "+component, 0.7+0.1*float64(index)/float64(len(rebuildComponents)))

		if err := crashPoint("switch-" + component); err != nil {
			return err
		}

		target := filepath.Join(root, component)
		backup := filepath.Join(backupRoot, component)
		staged := filepath.Join(stagingRoot, component)

		// 旧目录存在时先移到备份区。不存在（首次重建或该组件从未生成）
		// 时跳过这一步，直接切换新目录。
		if _, statErr := os.Stat(target); statErr == nil {
			if mkErr := os.MkdirAll(filepath.Dir(backup), 0o755); mkErr != nil {
				return rollbackAndReport(root, journal, backupRoot, stagingRoot, mkErr)
			}
			if renameErr := os.Rename(target, backup); renameErr != nil {
				return rollbackAndReport(root, journal, backupRoot, stagingRoot, renameErr)
			}
		}
		journal.Components[component] = componentMovedOut
		if writeErr := writeJournal(root, journal); writeErr != nil {
			return rollbackAndReport(root, journal, backupRoot, stagingRoot, writeErr)
		}

		// 暂存目录可能没有该组件（例如版本不需要 assets）：此时视为
		// 「新核心不含该组件」，保持目标目录缺失即可。
		if _, statErr := os.Stat(staged); statErr == nil {
			if renameErr := os.Rename(staged, target); renameErr != nil {
				return rollbackAndReport(root, journal, backupRoot, stagingRoot, renameErr)
			}
		}
		journal.Components[component] = componentMovedIn
		if writeErr := writeJournal(root, journal); writeErr != nil {
			return rollbackAndReport(root, journal, backupRoot, stagingRoot, writeErr)
		}
	}

	journal.State = stateVerifyingNew
	journal.Step = "verify-new"
	if writeErr := writeJournal(root, journal); writeErr != nil {
		return rollbackAndReport(root, journal, backupRoot, stagingRoot, writeErr)
	}
	if err := crashPoint("switched"); err != nil {
		return err
	}

	if opts.Verify != nil {
		report(opts.Progress, "正在校验新核心", 0.95)
		if verifyErr := opts.Verify(root); verifyErr != nil {
			return rollbackAndReport(root, journal, backupRoot, stagingRoot, verifyErr)
		}
	}

	journal.State = stateCommitted
	journal.Step = ""
	if writeErr := writeJournal(root, journal); writeErr != nil {
		// 已切换成功，只是日志没写上；清理仍然可以进行。
		_ = cleanupAfterCommit(root, operationID, backupRoot, stagingRoot)
		return nil
	}
	if err := crashPoint("committed"); err != nil {
		return err
	}

	report(opts.Progress, "重建完成", 1)
	return cleanupAfterCommit(root, operationID, backupRoot, stagingRoot)
}

// rollbackAndReport 回滚并包装原始错误。
//
// 回滚失败时保留日志与备份：下次启动的恢复流程会继续处理，
// 而不是把用户留在混合状态里。
func rollbackAndReport(root string, journal *rebuildJournal, backupRoot, stagingRoot string, cause error) error {
	if rollbackErr := rollback(root, journal, backupRoot); rollbackErr != nil {
		return fmt.Errorf("%w（回滚失败：%v，将在下次启动时继续恢复）", cause, rollbackErr)
	}
	_ = os.RemoveAll(stagingRoot)
	_ = os.RemoveAll(backupRoot)
	_ = removeJournal(root, journal.OperationID)
	return cause
}

// rollback 按日志把已切换的目录改回原状。
//
// 逆序处理：索引最先被回滚，这样它不会指向已被回滚的版本目录。
func rollback(root string, journal *rebuildJournal, backupRoot string) error {
	for index := len(rebuildComponents) - 1; index >= 0; index-- {
		component := rebuildComponents[index]
		target := filepath.Join(root, component)
		backup := filepath.Join(backupRoot, component)

		switch journal.Components[component] {
		case componentMovedIn:
			// 新目录已就位：先挪走，再把备份换回来。
			if _, statErr := os.Stat(target); statErr == nil {
				if removeErr := os.RemoveAll(target); removeErr != nil {
					return removeErr
				}
			}
			if _, statErr := os.Stat(backup); statErr == nil {
				if renameErr := os.Rename(backup, target); renameErr != nil {
					return renameErr
				}
			}
		case componentMovedOut:
			// 旧目录已移走但新目录未就位：把备份换回来即可。
			if _, statErr := os.Stat(backup); statErr == nil {
				if renameErr := os.Rename(backup, target); renameErr != nil {
					return renameErr
				}
			}
		}
	}
	return nil
}

// cleanupAfterCommit 删除备份、暂存与事务日志。
func cleanupAfterCommit(root, operationID, backupRoot, stagingRoot string) error {
	_ = os.RemoveAll(stagingRoot)
	_ = os.RemoveAll(backupRoot)
	// 事务目录本身没有内容时一并删除，避免留下空目录。
	_ = os.Remove(filepath.Join(root, rebuildBackupDir))
	_ = os.Remove(filepath.Join(root, rebuildStagingDir))
	return removeJournal(root, operationID)
}

// RebuildRecoverReport 是一次重建事务恢复的结果。
type RebuildRecoverReport struct {
	// Recovered 是被处理的事务数量。
	Recovered int
	// OperationIDs 是被处理的事务标识。
	OperationIDs []string
}

// RecoverRebuildDetailed 与 RecoverRebuild 相同，但同时返回处理统计。
//
// 启动流程据此判断是否需要记录日志：没有事务时静默返回，
// 处理过事务时把操作标识写进日志，便于事后对应用户反馈。
func RecoverRebuildDetailed(root string) (RebuildRecoverReport, error) {
	return recoverRebuild(root)
}

// RecoverRebuild 处理所有未完成的重建事务。
//
// 应在应用启动时、允许任何安装或启动之前调用一次。
//
// 判定规则：
//   - `staging` / `verifying`：尚未切换，删掉暂存目录即可；
//   - `switching`：按组件状态回滚到旧核心；
//   - `verifying-new`：新核心已就位但未确认，保守回滚（旧核心仍在备份区）；
//   - `committed`：切换已完成，清理备份与日志。
func RecoverRebuild(root string) error {
	_, err := recoverRebuild(root)
	return err
}

// recoverRebuild 是 RecoverRebuild 的实现，额外返回处理统计。
func recoverRebuild(root string) (RebuildRecoverReport, error) {
	report := RebuildRecoverReport{}
	journalDir := filepath.Join(root, journalDirName)
	entries, readErr := os.ReadDir(journalDir)
	if readErr != nil {
		if os.IsNotExist(readErr) {
			return report, nil
		}
		return report, errors.New("无法读取重建事务目录")
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		journal, loadErr := readJournal(filepath.Join(journalDir, entry.Name()))
		if loadErr != nil {
			// 损坏的日志无从判断进度：保留它并报警，让用户知道需要人工处理。
			return report, fmt.Errorf("重建事务日志损坏（%s）：%w", entry.Name(), loadErr)
		}
		if recoverErr := recoverOne(root, journal); recoverErr != nil {
			return report, recoverErr
		}
		report.Recovered++
		report.OperationIDs = append(report.OperationIDs, journal.OperationID)
	}
	return report, nil
}

// recoverOne 按状态处理单个事务。
func recoverOne(root string, journal *rebuildJournal) error {
	stagingRoot := filepath.Join(root, rebuildStagingDir, journal.OperationID)
	backupRoot := filepath.Join(root, rebuildBackupDir, journal.OperationID)

	switch journal.State {
	case stateStaging, stateVerifying:
		// 尚未开始切换：旧核心完好，删掉暂存目录即可。
		_ = os.RemoveAll(stagingRoot)
		pruneEmptyDir(filepath.Join(root, rebuildStagingDir))
		return removeJournal(root, journal.OperationID)
	case stateCommitted:
		return cleanupAfterCommit(root, journal.OperationID, backupRoot, stagingRoot)
	case stateSwitching, stateVerifyingNew:
		if rollbackErr := rollback(root, journal, backupRoot); rollbackErr != nil {
			return fmt.Errorf("回滚重建事务 %s 失败：%w", journal.OperationID, rollbackErr)
		}
		_ = os.RemoveAll(stagingRoot)
		_ = os.RemoveAll(backupRoot)
		pruneEmptyDir(filepath.Join(root, rebuildStagingDir))
		pruneEmptyDir(filepath.Join(root, rebuildBackupDir))
		return removeJournal(root, journal.OperationID)
	default:
		return fmt.Errorf("重建事务 %s 的状态未知：%q", journal.OperationID, journal.State)
	}
}

// pruneEmptyDir 删除空目录（非空时静默保留）。
//
// 用于清理 staging / backup 的父目录：事务目录本身没有内容时应一并删除，
// 否则每次崩溃恢复都会留下一个空壳。
func pruneEmptyDir(path string) {
	entries, err := os.ReadDir(path)
	if err != nil || len(entries) > 0 {
		return
	}
	_ = os.Remove(path)
}

// writeJournal 原子写入事务日志。
func writeJournal(root string, journal *rebuildJournal) error {
	directory := filepath.Join(root, journalDirName)
	if mkErr := os.MkdirAll(directory, 0o755); mkErr != nil {
		return errors.New("无法创建重建事务目录")
	}
	encoded, marshalErr := json.MarshalIndent(journal, "", "  ")
	if marshalErr != nil {
		return errors.New("重建事务日志无法编码")
	}
	path := filepath.Join(directory, journal.OperationID+".json")
	temp, createErr := os.CreateTemp(directory, ".journal-*.json")
	if createErr != nil {
		return errors.New("无法创建事务日志临时文件")
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)

	if _, writeErr := temp.Write(encoded); writeErr != nil {
		temp.Close()
		return errors.New("无法写入事务日志")
	}
	if syncErr := temp.Sync(); syncErr != nil {
		temp.Close()
		return errors.New("无法同步事务日志")
	}
	if closeErr := temp.Close(); closeErr != nil {
		return errors.New("无法关闭事务日志临时文件")
	}
	if renameErr := os.Rename(tempPath, path); renameErr != nil {
		return errors.New("无法提交事务日志")
	}
	return nil
}

// readJournal 读取并校验事务日志。
func readJournal(path string) (*rebuildJournal, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(content) == 0 || len(content) > maxIndexBytes {
		return nil, errors.New("事务日志大小异常")
	}
	var journal rebuildJournal
	if unmarshalErr := json.Unmarshal(content, &journal); unmarshalErr != nil {
		return nil, errors.New("事务日志格式损坏")
	}
	if journal.SchemaVersion != journalSchemaVersion {
		return nil, fmt.Errorf("事务日志版本不受支持：%d", journal.SchemaVersion)
	}
	if journal.OperationID == "" || journal.State == "" {
		return nil, errors.New("事务日志缺少必要字段")
	}
	if journal.Components == nil {
		journal.Components = map[string]string{}
	}
	return &journal, nil
}

// removeJournal 删除指定事务的日志。
//
// 事务目录在清空后一并删除，避免每次重建或恢复都留下一个空壳目录。
func removeJournal(root, operationID string) error {
	path := filepath.Join(root, journalDirName, operationID+".json")
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return errors.New("无法删除事务日志")
	}
	pruneEmptyDir(filepath.Join(root, journalDirName))
	return nil
}

// ensureSameVolume 确认暂存目录与目标目录在同一卷。
//
// 跨卷时 Rename 会失败或被系统退化为复制，事务的「逐目录切换」语义
// 不再成立，因此必须在构建前拦住。
func ensureSameVolume(root, stagingRoot string) error {
	rootInfo, rootErr := os.Stat(root)
	if rootErr != nil {
		return errors.New("核心根目录不可用")
	}
	stagingInfo, stagingErr := os.Stat(stagingRoot)
	if stagingErr != nil {
		return errors.New("暂存目录不可用")
	}
	// 用同一文件系统的设备标识判断；Windows 上文件系统卷由系统调用处理，
	// 这里采用「临时文件改名」的方式做实际探测，避免平台相关代码。
	probe := filepath.Join(root, ".nv-volume-probe")
	if writeErr := os.WriteFile(probe, []byte("probe"), 0o600); writeErr != nil {
		return errors.New("核心目录不可写，无法重建")
	}
	defer os.Remove(probe)

	renamed := filepath.Join(stagingRoot, ".nv-volume-probe")
	if renameErr := os.Rename(probe, renamed); renameErr != nil {
		return errors.New("暂存目录与核心目录不在同一磁盘卷，无法原子切换")
	}
	_ = os.Remove(renamed)
	_ = rootInfo
	_ = stagingInfo
	return nil
}

// coreInUse 判断是否有游戏正在使用核心资源。
func coreInUse(root string) (bool, error) {
	leases, leaseErr := ReadActiveLeases(root)
	if leaseErr != nil {
		// 租约表不可读时保守拒绝：重建会替换全部核心目录，
		// 在无法确认是否有游戏运行时执行它风险太高。
		return true, errors.New("无法读取运行时租约，已中止重建")
	}
	if len(leases) == 0 {
		return false, nil
	}
	return true, nil
}

// report 安全地上报进度。
func report(progress func(string, float64), phase string, ratio float64) {
	if progress == nil {
		return
	}
	progress(phase, ratio)
}

var _ = artifact.DataRoot
