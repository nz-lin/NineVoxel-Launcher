package download

import (
	"os"
	"path/filepath"
)

// spaceSafetyMarginPercent 是磁盘预检的默认余量百分比。
//
// 决策 C-7 要求「可用空间 ≥ totalBytes × 1.1」。这里用整数百分比而不是
// 浮点乘法：单文件上限是 1 GiB（见 maxTransferSize），整数不会溢出，
// 而浮点在「刚好够 / 刚好不够」的边界上会产生不可断言的舍入误差。
const spaceSafetyMarginPercent = 10

// SpaceCheck 是一次磁盘空间预检的结果。
//
// 之所以带上 RootDir：预检的对象是「目标所在卷」，不是数据根本身；
// 排查问题时需要知道当初探测的是哪个目录（日志里只写相对形态）。
type SpaceCheck struct {
	// RootDir 是被探测的目录（由调用方传入，通常为数据根或目标父目录）。
	RootDir string

	// RequiredBytes 是本次所需的预留空间（含余量）。
	RequiredBytes int64

	// AvailableBytes 是探测到的可用空间。
	AvailableBytes int64
}

// OK 判断本次预检是否通过。
func (c SpaceCheck) OK() bool {
	return c.AvailableBytes >= c.RequiredBytes
}

// RequiredSpace 计算一次下载需要预留的字节数（含 10% 余量，向上取整）。
//
// totalBytes 为 0 或负数表示「大小未知」，此时无法做安全预检，
// 返回 0 并由 CheckDiskSpace 明确报错，而不是「当作够用」。
func RequiredSpace(totalBytes int64) int64 {
	if totalBytes <= 0 {
		return 0
	}
	margin := (totalBytes*spaceSafetyMarginPercent + 99) / 100
	return totalBytes + margin
}

// CheckDiskSpace 在执行一次下载前检查目标卷的可用空间。
//
// 关键约定（卡面第 7 条）：预检必须在**创建任务之前**完成，空间不足时
// 直接返回 DOWNLOAD_SPACE_001 并拒绝创建，避免「先建任务、再失败」留下
// 一条永远排队不动的任务。
//
// rootDir 允许尚不存在（P3 不创建目录），此时向最近的已存在祖先探测，
// 因为同一卷的可用空间与目录是否已创建无关。
func CheckDiskSpace(rootDir string, totalBytes int64) (SpaceCheck, *Error) {
	if totalBytes <= 0 {
		return SpaceCheck{}, newInputError(ErrorCodeSpace, messageExpectedSizeInvalid)
	}
	required := RequiredSpace(totalBytes)
	probeDir, ok := existingAncestor(rootDir)
	if !ok {
		return SpaceCheck{}, newSystemError(ErrorCodeSpace, messageDiskSpaceProbeFailed, false)
	}
	available, probeErr := availableBytes(probeDir)
	if probeErr != nil {
		return SpaceCheck{}, probeErr
	}
	check := SpaceCheck{
		RootDir:        rootDir,
		RequiredBytes:  required,
		AvailableBytes: available,
	}
	if !check.OK() {
		return check, newSystemError(ErrorCodeSpace, messageDiskSpaceInsufficient, false)
	}
	return check, nil
}

// existingAncestor 返回 path 自身或最近的已存在祖先目录。
//
// 不用 os.MkdirAll 试探：预检是只读操作，不得因为「检查空间」而创建目录。
func existingAncestor(path string) (string, bool) {
	current := filepath.Clean(path)
	if current == "" {
		return "", false
	}
	for {
		info, err := os.Stat(current)
		if err == nil && info.IsDir() {
			return current, true
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", false
		}
		current = parent
	}
}
