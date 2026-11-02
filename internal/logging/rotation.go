// 本文件实现日志文件的写入与轮转策略。
//
// 轮转的三个关键参数（单文件上限、保留天数、目录总容量上限）集中定义在常量区，
// 并带中文取值理由，便于后续按用户验收结果调整（见 P1-05 遗留问题 4）。
//
// 设计约束：
//   - 写入失败不得影响主业务：本文件所有导出函数都不把错误抛给业务调用方；
//   - 目录容量检查不得在每条日志时同步全目录扫描：用写入计数 + 时间间隔节流；
//   - 文件名模式固定为 app-<YYYYMMDD>.log，便于按日期排序与人工定位。
package logging

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// 轮转与容量参数。三个数值为 P1 初始值，未经真实运行验证（用户验收后按实际调整）。
const (
	// maxFileSizeBytes 是单个日志文件的大小上限，达到后立即切换到新文件。
	//
	// 取 5 MiB 的理由：结构化日志单条约 200–400 字节，5 MiB 约合 1.3–2.5 万条，
	// 足以覆盖一次完整的启动到退出会话；同时小到不会让单文件打开/传输变得笨重。
	maxFileSizeBytes int64 = 5 * 1024 * 1024

	// retentionDays 是日志文件的保留天数，超过该天数的文件会被清理。
	//
	// 取 7 天的理由：覆盖「用户周末遇到问题、周一才反馈」的常见排查窗口，
	// 又不会让磁盘占用随时间无限增长。
	retentionDays = 7

	// maxDirectoryBytes 是整个日志目录的容量上限，超限时按最旧优先删除。
	//
	// 取 50 MiB 的理由：等于 10 个满额单文件，即使出现高频错误也不会失控；
	// 同时远小于常见清理阈值，不会给用户磁盘带来可感知压力。
	maxDirectoryBytes int64 = 50 * 1024 * 1024

	// capacityCheckInterval 是目录容量检查的最小时间间隔。
	//
	// 目录扫描是 O(文件数) 的系统调用，不能每条日志都做；
	// 取 1 分钟：轮转事件本身很少，1 分钟的延迟对容量控制没有实质影响。
	capacityCheckInterval = time.Minute

	// capacityCheckEveryWrites 是触发容量检查的写入计步阈值。
	//
	// 与时间间隔并列使用：低频写入时靠时间触发，高频写入时靠计数提前触发，
	// 避免「短时间内写满 50 MiB 但还没到 1 分钟」的窗口。
	capacityCheckEveryWrites = 200

	// logFilePrefix 是结构化日志文件的名称前缀。
	logFilePrefix = "app-"

	// logFileSuffix 是结构化日志文件的扩展名。
	logFileSuffix = ".log"

	// logFileDateLayout 是日志文件名中的日期格式。
	logFileDateLayout = "20060102"

	// filePerm 是日志文件的权限位：仅当前用户可读写。
	filePerm = 0o600

	// dirPerm 是日志目录的权限位。
	dirPerm = 0o755
)

// entryTimestampLayout 是日志记录中 timestamp 字段的时间格式。
//
// 使用 UTC + 毫秒精度的 RFC3339 变体：毫秒精度足以区分同一秒内的多条记录，
// UTC 避免跨时区排查时对不上时间。
const entryTimestampLayout = "2006-01-02T15:04:05.000Z07:00"

// writer 是结构化日志的写入器。
//
// 一个 writer 对应一个日志目录；内部持有一个已打开的文件句柄，
// 当日期变化或文件超限时切换到新文件。
type writer struct {
	// dir 为日志目录；为空表示目录不可用，此时只写 stderr。
	dir string

	// threshold 为当前生效的最低写入级别。
	threshold Level

	// allowDebug 表示是否允许 debug 级别写入。
	allowDebug bool

	// mu 保护以下可变字段：并发写入时的文件句柄与计数。
	mu sync.Mutex

	// file 为当前打开的文件句柄；nil 表示尚未打开或已关闭。
	file *os.File

	// fileDate 为当前文件对应的日期（YYYYMMDD），用于判断是否跨天。
	fileDate string

	// fileSize 为当前文件已写入的字节数，用于判断是否超过单文件上限。
	fileSize int64

	// writeCount 为自上次容量检查以来的写入条数。
	writeCount int

	// lastCapacityCheck 为上次目录容量检查的时间。
	lastCapacityCheck time.Time

	// selfWarn 记录清理失败等自身问题的写入次数，用于避免递归告警。
	selfWarn int
}

// newWriter 按目录与级别创建写入器。
//
// 参数 dir 必须由调用方来自 platform.LogDirectory()；为空表示目录不可用，
// 此时写入器进入「仅 stderr」降级模式（enabled=false）。
// 参数 threshold 为最低写入级别，debug 是否开启由 threshold 直接决定。
func newWriter(dir string, threshold Level) *writer {
	return &writer{
		dir:       dir,
		threshold: threshold,
		// debug 是否生效完全由阈值决定：阈值设为 debug 时 debug 记录才会被写入。
		allowDebug: threshold.severity() <= LevelDebug.severity(),
	}
}

// Enabled 返回写入器是否具备文件写入能力。
//
// 返回 false 时调用方应把日志仅输出到 stderr，并在界面上显式提示用户
// （P1-05 要求 E 节第 5 条：不得静默留白）。
func (w *writer) Enabled() bool {
	return w != nil && w.dir != ""
}

// Directory 返回日志目录。
//
// 该值**仅供进程内部使用**，禁止通过 LogService.Status 返回给前端：
// 接口冻结清单 §8.5 要求 LogStatus 不得含绝对路径。
func (w *writer) Directory() string {
	if w == nil {
		return ""
	}
	return w.dir
}

// write 把一条已归一化的记录写入当前日志文件。
//
// 调用前提：记录的 message 与 attributes 必须已脱敏；级别过滤已由上层完成。
// 本函数不返回错误：写入失败时静默降级，保证日志系统不会把主业务拖垮。
func (w *writer) write(record *Record) {
	if !w.Enabled() {
		return
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	if err := w.ensureFileLocked(time.Now()); err != nil {
		// 打不开文件时保持静默：记录一条 stderr 提示即可，不改变业务结果。
		w.warnSelfLocked("日志文件不可写，已降级为仅控制台输出")
		return
	}

	line, err := marshalRecord(record)
	if err != nil {
		w.warnSelfLocked("日志序列化失败，本次记录已丢弃")
		return
	}

	n, err := w.file.Write(append(line, '\n'))
	if err != nil {
		w.warnSelfLocked("日志写入失败，本次记录已丢弃")
		return
	}

	w.fileSize += int64(n)
	w.writeCount++

	// 单文件超限：立即切换文件，避免单个文件无限增长。
	if w.fileSize >= maxFileSizeBytes {
		w.closeFileLocked()
		w.fileDate = ""
	}

	// 目录容量与保留天数检查：按时间或写入计步触发，避免每条日志都扫描目录。
	w.maybeMaintainLocked(time.Now())
}

// ensureFileLocked 确保当前有可写的文件句柄，必要时切换到新文件。
//
// 调用前提：已持有 w.mu。跨天时切换到当天的文件；文件被外部删除后新建同名文件。
func (w *writer) ensureFileLocked(now time.Time) error {
	date := now.UTC().Format(logFileDateLayout)

	if w.file != nil && w.fileDate == date && w.fileSize < maxFileSizeBytes {
		return nil
	}

	w.closeFileLocked()

	path := filepath.Join(w.dir, logFileName(date))
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, filePerm)
	if err != nil {
		return err
	}

	info, statErr := file.Stat()
	if statErr != nil {
		file.Close()
		return statErr
	}

	w.file = file
	w.fileDate = date
	w.fileSize = info.Size()
	return nil
}

// closeFileLocked 关闭当前文件句柄并把大小计数清零。
//
// 调用前提：已持有 w.mu。句柄为 nil 时为空操作，可安全重复调用。
func (w *writer) closeFileLocked() {
	if w.file == nil {
		return
	}
	// 忽略 Close 的错误：句柄已经不可用时关闭失败也无需上报。
	_ = w.file.Close()
	w.file = nil
	w.fileSize = 0
}

// maybeMaintainLocked 按节流条件执行轮转清理。
//
// 触发条件：距上次检查超过 capacityCheckInterval，或累计写入达到
// capacityCheckEveryWrites 条。两个条件任一满足即可，兼顾低频与高频场景。
// 调用前提：已持有 w.mu。
func (w *writer) maybeMaintainLocked(now time.Time) {
	if w.writeCount < capacityCheckEveryWrites && now.Sub(w.lastCapacityCheck) < capacityCheckInterval {
		return
	}

	w.writeCount = 0
	w.lastCapacityCheck = now

	// 清理失败不影响后续写入：warnSelfLocked 会写一条 stderr 提示。
	w.maintainLocked(now)
}

// maintainLocked 执行保留天数清理与目录容量清理。
//
// 顺序：先按天数删除过期文件，再按总容量从最旧开始删除，直到回到上限以内。
// 当前正在写入的文件永不被删除——删除它会导致后续写入落到已删除的 inode 上。
// 调用前提：已持有 w.mu。
func (w *writer) maintainLocked(now time.Time) {
	files, err := listLogFiles(w.dir)
	if err != nil {
		w.warnSelfLocked("日志目录扫描失败，本次轮转清理已跳过")
		return
	}
	if len(files) == 0 {
		return
	}

	currentName := ""
	if w.fileDate != "" {
		currentName = logFileName(w.fileDate)
	}

	// 第一步：按保留天数删除过期文件（按修改时间判断，避免依赖文件名格式）。
	cutoff := now.Add(-retentionDays * 24 * time.Hour)
	for _, file := range files {
		if file.Name() == currentName {
			continue
		}
		if file.ModTime().Before(cutoff) {
			if err := os.Remove(filepath.Join(w.dir, file.Name())); err != nil {
				w.warnSelfLocked("过期日志文件删除失败")
			}
		}
	}

	// 第二步：重新统计总量，超限时按最旧优先删除（当前文件除外）。
	files, err = listLogFiles(w.dir)
	if err != nil {
		w.warnSelfLocked("日志目录容量统计失败，本次容量清理已跳过")
		return
	}

	var total int64
	for _, file := range files {
		total += file.Size()
	}
	if total <= maxDirectoryBytes {
		return
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].ModTime().Before(files[j].ModTime())
	})

	for _, file := range files {
		if total <= maxDirectoryBytes {
			return
		}
		if file.Name() == currentName {
			continue
		}
		if err := os.Remove(filepath.Join(w.dir, file.Name())); err != nil {
			w.warnSelfLocked("日志容量清理删除失败")
			continue
		}
		total -= file.Size()
	}
}

// warnSelfLocked 把日志系统自身的故障写到 stderr。
//
// 为什么不写进日志文件：日志系统自身出错时再走文件写入容易形成递归
// （例如写入失败 → 记录失败 → 再次写入失败）。因此这里只落 stderr，
// 并限制总次数，避免频繁刷屏。
// 调用前提：已持有 w.mu。
func (w *writer) warnSelfLocked(message string) {
	// 自身告警最多 3 次：足够让用户与开发者看到问题，又不会污染控制台。
	const maxSelfWarnings = 3
	if w.selfWarn >= maxSelfWarnings {
		return
	}
	w.selfWarn++
	fmt.Fprintf(os.Stderr, "logging: %s\n", message)
}

// listLogFiles 列出日志目录下符合命名规则的文件。
//
// 只认可 app-<YYYYMMDD>.log 形式的文件：目录里可能存在 P0 的 launcher.log
// 或其他无关文件，这些不属于本模块的轮转范围，必须排除。
func listLogFiles(dir string) ([]os.FileInfo, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	result := make([]os.FileInfo, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if !isLogFileName(entry.Name()) {
			continue
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			continue
		}
		result = append(result, info)
	}
	return result, nil
}

// isLogFileName 判断文件名是否为结构化日志文件。
func isLogFileName(name string) bool {
	if !strings.HasPrefix(name, logFilePrefix) || !strings.HasSuffix(name, logFileSuffix) {
		return false
	}
	middle := strings.TrimSuffix(strings.TrimPrefix(name, logFilePrefix), logFileSuffix)
	if len(middle) != len(logFileDateLayout) {
		return false
	}
	_, err := time.Parse(logFileDateLayout, middle)
	return err == nil
}

// logFileName 按日期生成日志文件名，形如 app-20261101.log。
func logFileName(date string) string {
	return logFilePrefix + date + logFileSuffix
}

// DirectoryStatus 汇总日志目录的可读状态，供 LogService.Status 使用。
//
// 返回的字段均不含绝对路径：directoryKind 只给出目录来源的类别，
// 具体路径留在进程内部（接口冻结清单 §8.5）。
type DirectoryStatus struct {
	// Enabled 表示日志目录是否可用；false 时本次运行只能输出到控制台。
	Enabled bool `json:"enabled"`

	// DirectoryKind 表示目录来源类别：userCache / exeDir / unavailable。
	DirectoryKind string `json:"directoryKind"`

	// FileCount 为目录中结构化日志文件的数量。
	FileCount int `json:"fileCount"`

	// LatestFileName 为最新的结构化日志文件名；无文件时为空。
	LatestFileName string `json:"latestFileName"`

	// TotalBytes 为结构化日志文件占用的总字节数。
	TotalBytes int64 `json:"totalBytes"`
}

// 目录来源类别常量：与 P1-05 D 节给出的取值逐字一致。
const (
	// DirectoryKindUserCache 表示日志落在用户缓存目录。
	DirectoryKindUserCache = "userCache"

	// DirectoryKindExeDir 表示日志回退到可执行文件同级目录。
	DirectoryKindExeDir = "exeDir"

	// DirectoryKindUnavailable 表示两处目录都不可写。
	DirectoryKindUnavailable = "unavailable"
)

// describeDirectory 汇总日志目录状态。
//
// 参数 dir 为日志目录（来自 platform.LogDirectory），kind 为目录来源类别。
// 目录为空表示不可写，直接返回 Enabled=false 与 unavailable 类别。
// 扫描失败不作为错误上报：状态查询失败时按「有目录但读不到」返回，
// 由调用方决定是否提示用户重试。
func describeDirectory(dir string, kind string) DirectoryStatus {
	if dir == "" {
		return DirectoryStatus{
			Enabled:       false,
			DirectoryKind: DirectoryKindUnavailable,
		}
	}

	status := DirectoryStatus{
		Enabled:       true,
		DirectoryKind: kind,
	}

	files, err := listLogFiles(dir)
	if err != nil {
		return status
	}

	status.FileCount = len(files)

	var latest os.FileInfo
	for _, file := range files {
		status.TotalBytes += file.Size()
		if latest == nil || file.ModTime().After(latest.ModTime()) {
			latest = file
		}
	}
	if latest != nil {
		status.LatestFileName = latest.Name()
	}
	return status
}
