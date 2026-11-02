// 本文件实现日志文件的写入与轮转策略。
//
// 轮转的三个关键参数（单文件上限、保留天数、目录总容量上限）集中定义在常量区，
// 并带中文取值理由，便于后续按用户验收结果调整（见 P1-05 遗留问题 4）。
//
// 设计约束：
//   - 写入失败不得影响主业务：本文件所有导出函数都不把错误抛给业务调用方；
//   - 目录容量检查不得在每条日志时同步全目录扫描：用写入计数 + 时间间隔节流；
//   - 文件名模式为 app-<YYYYMMDD>.log 与 app-<YYYYMMDD>-<序号>.log，
//     同一天内超过单文件上限时改用下一个序号，保证每个文件都真正有界。
package logging

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"example.com/ninevoxel/launcher/internal/platform"
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

	// maxRotationAttempts 是同一天内寻找可写序号文件的最大尝试次数。
	//
	// 目录容量上限 50 MiB、单文件上限 5 MiB，理论最多 10 个文件；取 64 留足余量。
	// 超过该次数说明目录状态异常（例如残留大量同名文件），此时接受当前序号，
	// 保证日志写入不会无限循环。
	maxRotationAttempts = 64

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
// 当日期变化或文件超限时切换到下一个文件（同一天用序号区分）。
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

	// closed 表示写入器已被显式关闭；此后 write 不再打开新文件。
	//
	// 为什么要这个标记：仅把 file 置 nil 不够——下一次 write 会重新打开文件，
	// 关闭动作形同虚设，Windows 上仍会因句柄占用无法删除日志文件。
	closed bool

	// fileDate 为当前文件对应的日期（YYYYMMDD），用于判断是否跨天。
	fileDate string

	// fileName 为当前文件的文件名（不含目录），用于轮转清理时保护当前文件。
	//
	// 不能像旧实现那样用 fileDate 反推文件名：同一天内会有多个序号文件，
	// 反推得到的是「首个文件」，会把正在写的序号文件误判为可删除。
	fileName string

	// fileSize 为当前文件已写入的字节数，用于判断是否超过单文件上限。
	fileSize int64

	// maxFileSize 为单文件大小上限；默认取 maxFileSizeBytes，测试可注入更小值。
	maxFileSize int64

	// maxDirectoryBytes 为日志目录容量上限；默认取 maxDirectoryBytes 常量，
	// 测试可注入更小值。
	maxDirectoryBytes int64

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
		dir:               dir,
		threshold:         threshold,
		maxFileSize:       maxFileSizeBytes,
		maxDirectoryBytes: maxDirectoryBytes,
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

	// 已显式关闭：后续写入静默丢弃，绝不重新打开文件。
	// 否则 Close 只关掉当前句柄，下一条日志又把它打开，关闭就没有实际效果。
	if w.closed {
		return
	}

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

	// 单文件超限时不在本次写入里关闭句柄：下一次 ensureFileLocked 会切换到
	// 当天下一个序号文件。保持句柄打开可以让轮转清理始终知道「当前文件是谁」，
	// 避免像旧实现那样在 fileDate 清空后误删正在写入的文件。

	// 目录容量与保留天数检查：按时间或写入计步触发，避免每条日志都扫描目录。
	w.maybeMaintainLocked(time.Now())
}

// ensureFileLocked 确保当前有可写的文件句柄，必要时切换到新文件。
//
// 调用前提：已持有 w.mu。
//
// 轮转规则：
//   - 仍可写（同一天、未超单文件上限）→ 直接复用；
//   - 跨天 → 使用新日期的文件；
//   - 同一天但已超上限 → 使用 <date>-<下一个序号>.log。
//
// 打开文件后还会再 stat 一次：进程重启时可能已有当天文件且恰好在重启前写满，
// 此时继续往满文件追加就突破了单文件上限，必须顺延到下一个序号。
func (w *writer) ensureFileLocked(now time.Time) error {
	// 手动构造 writer（测试）时字段可能为零值，这里兜底到生产默认值。
	if w.maxFileSize <= 0 {
		w.maxFileSize = maxFileSizeBytes
	}
	if w.maxDirectoryBytes <= 0 {
		w.maxDirectoryBytes = maxDirectoryBytes
	}

	date := now.UTC().Format(logFileDateLayout)

	if w.file != nil && w.fileDate == date && w.fileSize < w.maxFileSize {
		return nil
	}

	// 轮转时关闭旧句柄：关闭失败也继续打开新文件，不能因为旧句柄不可用就放弃写入。
	_ = w.closeFileLocked()

	// 从当天已有的最大序号开始：既避免覆盖历史文件，也让进程重启后
	// 能继续追加未写满的最后一个文件。
	seq := w.highestSeqLocked(date)

	for attempt := 0; attempt < maxRotationAttempts; attempt++ {
		path := filepath.Join(w.dir, logFileNameFor(date, seq))
		file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, filePerm)
		if err != nil {
			return err
		}

		info, statErr := file.Stat()
		if statErr != nil {
			file.Close()
			return statErr
		}

		if info.Size() >= w.maxFileSize && attempt < maxRotationAttempts-1 {
			file.Close()
			seq++
			continue
		}

		w.file = file
		w.fileDate = date
		w.fileName = filepath.Base(path)
		w.fileSize = info.Size()
		return nil
	}

	// 理论上不可达：每个尝试都会打开文件，除非目录里存在大量写满的序号文件。
	return fmt.Errorf("logging: 同一天日志文件轮转次数超过上限（%d）", maxRotationAttempts)
}

// highestSeqLocked 返回指定日期已存在的最大文件序号。
//
// 序号 0 对应 app-<date>.log，1 及以上对应 app-<date>-<seq>.log。
// 目录扫描失败时按 0 处理：最坏情况是接着首个文件写，不影响日志可用性。
// 调用前提：已持有 w.mu。
func (w *writer) highestSeqLocked(date string) int {
	files, err := listLogFiles(w.dir)
	if err != nil {
		return 0
	}

	highest := 0
	for _, file := range files {
		fileDate, seq, ok := parseLogFileSeq(file.Name())
		if ok && fileDate == date && seq > highest {
			highest = seq
		}
	}
	return highest
}

// Close 关闭写入器并释放日志文件句柄。
//
// 幂等：可重复调用，也可在从未打开文件时调用；关闭后写入变为空操作，
// 不会重新打开文件。并发安全。
//
// 为什么需要显式关闭：Windows 不允许删除仍被进程打开的文件，
// 测试的 TempDir 清理、以及用户迁移/删除日志目录都会因句柄占用而失败。
// Linux 上 unlink 允许已打开文件，因此这一点只在 Windows 暴露。
func (w *writer) Close() error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()

	w.closed = true
	return w.closeFileLocked()
}

// closeFileLocked 关闭当前文件句柄并清空当前文件名。
//
// 调用前提：已持有 w.mu。句柄为 nil 时为空操作，可安全重复调用。
// 返回 Close 的错误：显式 Close 需要把「句柄没关掉」这个事实报告给调用方；
// 轮转路径可以忽略该错误（旧句柄不可用时仍要继续写新文件）。
func (w *writer) closeFileLocked() error {
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	w.fileName = ""
	w.fileSize = 0
	return err
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

	currentName := w.fileName

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

	limit := w.maxDirectoryBytes
	if limit <= 0 {
		limit = maxDirectoryBytes
	}

	var total int64
	for _, file := range files {
		total += file.Size()
	}
	if total <= limit {
		return
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].ModTime().Before(files[j].ModTime())
	})

	for _, file := range files {
		if total <= limit {
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
// 只认可 app-<YYYYMMDD>.log 与 app-<YYYYMMDD>-<序号>.log 形式的文件：
// 目录里可能存在 P0 的 launcher.log 或其他无关文件，这些不属于本模块的轮转范围，必须排除。
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
//
// 认可两种形态：app-<YYYYMMDD>.log 与 app-<YYYYMMDD>-<序号>.log。
func isLogFileName(name string) bool {
	_, _, ok := parseLogFileSeq(name)
	return ok
}

// parseLogFileSeq 解析结构化日志文件名中的日期与序号。
//
// 形态：app-<YYYYMMDD>.log（序号 0）或 app-<YYYYMMDD>-<序号>.log（序号 ≥ 1）。
// 返回 false 表示不是本模块的日志文件。
func parseLogFileSeq(name string) (string, int, bool) {
	if !strings.HasPrefix(name, logFilePrefix) || !strings.HasSuffix(name, logFileSuffix) {
		return "", 0, false
	}

	middle := strings.TrimSuffix(strings.TrimPrefix(name, logFilePrefix), logFileSuffix)

	// 形态一：只有日期。
	if len(middle) == len(logFileDateLayout) {
		if _, err := time.Parse(logFileDateLayout, middle); err != nil {
			return "", 0, false
		}
		return middle, 0, true
	}

	// 形态二：日期 + `-` + 十进制序号。
	if len(middle) <= len(logFileDateLayout)+1 {
		return "", 0, false
	}

	date := middle[:len(logFileDateLayout)]
	if middle[len(logFileDateLayout)] != '-' {
		return "", 0, false
	}
	if _, err := time.Parse(logFileDateLayout, date); err != nil {
		return "", 0, false
	}

	seqText := middle[len(logFileDateLayout)+1:]
	for _, char := range seqText {
		if char < '0' || char > '9' {
			return "", 0, false
		}
	}

	seq, err := strconv.Atoi(seqText)
	if err != nil || seq <= 0 {
		return "", 0, false
	}
	return date, seq, true
}

// logFileNameFor 按日期与序号生成日志文件名。
//
// 序号 0 为 app-<YYYYMMDD>.log；序号 ≥ 1 为 app-<YYYYMMDD>-<序号>.log。
func logFileNameFor(date string, seq int) string {
	if seq <= 0 {
		return logFilePrefix + date + logFileSuffix
	}
	return logFilePrefix + date + "-" + strconv.Itoa(seq) + logFileSuffix
}

// DirectoryStatus 汇总日志目录的可读状态，供 LogService.Status 使用。
//
// 返回的字段均不含绝对路径：directoryKind 只给出目录来源的类别，
// 具体路径留在进程内部（接口冻结清单 §8.5）。
type DirectoryStatus struct {
	// Enabled 表示日志目录是否可用；false 时本次运行只能输出到控制台。
	Enabled bool `json:"enabled"`

	// DirectoryKind 表示目录来源类别：userConfig / userCache / exeDir / unavailable。
	DirectoryKind string `json:"directoryKind"`

	// FileCount 为目录中结构化日志文件的数量。
	FileCount int `json:"fileCount"`

	// LatestFileName 为最新的结构化日志文件名；无文件时为空。
	LatestFileName string `json:"latestFileName"`

	// TotalBytes 为结构化日志文件占用的总字节数。
	TotalBytes int64 `json:"totalBytes"`
}

// 目录来源类别常量：取值由 platform 包定义（日志目录的解析归 platform 所有）。
//
// userConfig 为新增取值（macOS 使用 Application Support），其余与 P1-05 D 节一致。
const (
	// DirectoryKindUserConfig 表示日志落在用户配置目录（macOS：Application Support）。
	DirectoryKindUserConfig = platform.LogDirectoryKindUserConfig

	// DirectoryKindUserCache 表示日志落在用户缓存目录（Windows：%LocalAppData%）。
	DirectoryKindUserCache = platform.LogDirectoryKindUserCache

	// DirectoryKindExeDir 表示日志回退到可执行文件同级目录。
	DirectoryKindExeDir = platform.LogDirectoryKindExeDir

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
