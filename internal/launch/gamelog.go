// 本文件实现游戏日志的独立通道（决策记录 D-2 方案 A）。
//
// 为什么独立通道而不是复用 internal/logging 的 app-*.log：
//   - 应用日志有 5 MiB/文件、7 天、50 MiB 目录上限，游戏输出量级可达数十 MiB，
//     混入会立刻顶满并挤掉启动器自身日志；
//   - `internal/logging.isLogFileName()` 只认 `app-*.log`，复用它必须改该判定，
//     会破坏 P1-05 的语义。
//
// 因此本文件自带：内存环形缓冲 + 独立轮转文件 + 独立脱敏 + 独立容量统计。
//
// 三条硬约束：
//  1. **不阻塞游戏**：落盘由独立 goroutine + 有界队列完成；队列满时丢最旧行并计数，
//     绝不阻塞采集方（否则管道缓冲区填满会反压游戏进程）。
//  2. **写失败不影响进程**：所有 IO 失败只记一条 warn（同因去重），不外抛、不 panic。
//  3. **脱敏集中**：`RedactCommandSummary` / `RedactForDisplay` 是本包**唯一**的脱敏实现，
//     P2-09 必须复用同一函数。
//
// 只读游戏目录：本文件**不**读写游戏自身的 `logs/` 与 `crash-reports/`。
package launch

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"example.com/ninevoxel/launcher/internal/events"
	"example.com/ninevoxel/launcher/internal/logging"
	"example.com/ninevoxel/launcher/internal/platform"
)

// 日志通道的容量与轮转常量。
const (
	// gameLogBufferCapacity 是控制台环形缓冲可保留的行数。
	//
	// 取 2000（卡面建议值）：足够覆盖一次启动前后的关键上下文，
	// 又不会让内存占用随长时间运行无限增长（单行按 100 字节估算约 200 KiB）。
	gameLogBufferCapacity = 2000

	// gameLogFileMaxBytes 是单个 game-*.log 文件的大小上限。
	//
	// 取 10 MiB：比应用日志（5 MiB）大一倍，因为游戏输出量级更大；
	// 同时仍限制在可人工打开与传输的范围内。
	gameLogFileMaxBytes int64 = 10 * 1024 * 1024

	// gameLogRetainFiles 是保留的 game-*.log 文件数量。
	//
	// 取 3：覆盖「最近几次启动」的排查需求；配合 10 MiB 上限，
	// 总占用上限约 30 MiB，与 P1-05 的 50 MiB 应用日志目录独立计算。
	gameLogRetainFiles = 3

	// gameLogQueueCapacity 是落盘队列的行数容量。
	//
	// 取 4096：约 2 个缓冲区的量。取值不需要很大——
	// 队列的作用是吸收磁盘抖动，而不是长期缓存；过大只会让进程退出时
	// 还有大量未落盘内容需要处理。
	gameLogQueueCapacity = 4096

	// gameLogEventBatchLimit 是单次 `launch.log.appended.v1` 事件最多携带的行数。
	//
	// 取 200：避免高频输出产生事件风暴（卡面明确要求每批 ≤ 200 行）。
	gameLogEventBatchLimit = 200

	// gameLogFlushInterval 是落盘 goroutine 的批量取数间隔。
	//
	// 取 100 毫秒：把多行合并成一次写入，减少系统调用；
	// 对排查而言 100 毫秒的延迟不可感知。
	gameLogFlushInterval = 100 * time.Millisecond

	// gameLogFilePrefix 是游戏日志文件名前缀。
	//
	// **刻意不叫 `app-`**：`internal/logging.isLogFileName()` 只认 `app-*.log`，
	// 因此这些文件不会被 `LogsPage` 或应用日志轮转逻辑碰到。
	gameLogFilePrefix = "game-"

	// gameLogFileSuffix 是游戏日志文件扩展名。
	gameLogFileSuffix = ".log"

	// gameLogDateLayout 是文件名中的日期格式。
	gameLogDateLayout = "20060102"

	// gameLogLaunchIDPrefixLength 是文件名中 launchId 保留的字符数。
	//
	// 取 8：足以在同一目录内区分不同启动，又不让文件名过长。
	gameLogLaunchIDPrefixLength = 8

	// gameLogFilePerm 是日志文件权限位（仅当前用户可读写）。
	gameLogFilePerm = 0o600

	// streamStdout 与 streamStderr 是输出来源的稳定标识。
	streamStdout = "stdout"
	streamStderr = "stderr"
)

// 日志通道的错误码与文案。
const (
	// ErrorCodeLaunchLog 表示游戏输出通道（内存缓冲 / game-*.log）读写失败。
	//
	// 来源：决策记录第五节冻结表：`LAUNCH_LOG_001`，首个使用卡 P2-07。
	ErrorCodeLaunchLog = "LAUNCH_LOG_001"

	// messageGameLogNoActiveLaunch 表示当前没有活动启动可用于读取日志。
	messageGameLogNoActiveLaunch = "当前没有正在运行的启动，没有可读取的游戏日志。"

	// messageGameLogUnknownLaunch 表示请求的 launchId 不存在。
	messageGameLogUnknownLaunch = "未找到该启动的游戏日志。"

	// warningGameLogCapture 是「采集/落盘异常」的统一去重提示。
	//
	// 这一类问题只记**一次**（同因去重）：若每次写失败都记，日志通道自身
	// 就会变成刷屏源，反而掩盖真正的问题。
	warningGameLogCapture = "游戏日志写入出现异常，部分内容可能未能落盘（后续同类错误不再重复提示）。"
)

// 脱敏相关的常量与正则。
const (
	// redactedPlaceholder 是所有敏感值被替换后的统一占位符。
	//
	// 与 internal/logging 的占位符保持视觉一致，便于用户理解。
	gameLogRedacted = "***"

	// gameLogMaxLineRunes 是单行保留的最大字符数。
	//
	// 取 8192：正常游戏日志行远小于此；限制是为了防止异常输出（例如
	// 二进制内容被当作文本）撑爆内存与文件。
	gameLogMaxLineRunes = 8192
)

// 参数级敏感键：这些键的**下一个参数**是敏感值，必须整体替换。
//
// 注意 `--username` 也在列（见 `RedactCommandSummary` 的注释说明理由）。
var commandSensitiveKeyValues = map[string]bool{
	"--accessToken":  true,
	"--clientId":     true,
	"--xuid":         true,
	"--auth_session": true,
	// `--uuid` 也在列：P2 当前是全零 UUID（无敏感性），但它是账号身份的一部分，
	// D-15 要求预览与命令摘要中一律显示 `***`。与 `--username` 同理，
	// 现在就统一替换，避免 P4 接入真实认证后漏改。
	"--uuid": true,
	// `--username` 也在列：P2 当前是占位 `Player`（本身无敏感性），
	// 但 P4 接入真实认证后这里会是真实账号名。现在就统一替换，
	// 避免那时漏改——「占位阶段不敏感」不足以成为不加防护的理由。
	"--username": true,
}

// 值内嵌式敏感前缀：这些前缀后面的内容直到分隔符都是敏感值。
//
// 覆盖 JVM 系统属性写法（`-Dhttp.proxyPassword=xxx`）。
var commandSensitivePrefixes = []string{
	"-Dhttp.proxyPassword=",
	"-Dhttps.proxyPassword=",
	"--accessToken=",
	"--clientId=",
	"--xuid=",
	"--auth_session=",
	"--uuid=",
	// `--username=` 同样替换（理由同上）。
	"--username=",
}

// displaySensitivePatterns 是文本级脱敏规则（用于游戏输出与控制台展示）。
//
// 顺序敏感：先处理带前缀的凭证形式，再处理裸 Token 形态。
var displaySensitivePatterns = []struct {
	// pattern 为要匹配的正则。
	pattern *regexp.Regexp

	// why 说明该项为什么敏感（注释要求逐条写明原因）。
	why string
}{
	{
		// `Authorization: <value>` 头：值通常是 Bearer token 或 Basic 凭据。
		//
		// 关键：值部分要一直匹配到**行尾或明显的分隔符**，不能只吃到第一个空格。
		// 否则 `Authorization: Bearer eyJ...` 只会替换掉 `Bearer` 这个词，
		// 真正的 token 本体（`eyJ...`）仍留在文本里——这是必须避免的泄漏。
		pattern: regexp.MustCompile(`(?i)(authorization\s*:\s*)[^\r\n]+`),
		why:     "HTTP 授权头携带完整凭证，原样展示等于泄漏；值可含空格（如 `Bearer xxx`），因此匹配到行尾。",
	},
	{
		// `Bearer <token>` 前缀：OAuth 访问令牌的标准写法。
		pattern: regexp.MustCompile(`(?i)Bearer\s+[A-Za-z0-9._~+/=-]+`),
		why:     "Bearer 后紧跟的是访问令牌本体。",
	},
	{
		// `--accessToken <v>` / `--accessToken=<v>`：Minecraft 启动参数里的令牌。
		//
		// 值前必须确有分隔符（`=` 或至少一个空白），否则 `--accessToken（arguments.game）`
		// 这类带来源标注的展示行会被误判成「键 + 值」，把标注替换成 `***`。
		pattern: regexp.MustCompile(`(--accessToken(?:\s*=\s*|\s+))[^\s"',;]+`),
		why:     "启动参数中的访问令牌，可直接用于冒充玩家。",
	},
	{
		// `--clientId` / `--xuid`：账号标识，属个人可识别信息。分隔符要求同上。
		pattern: regexp.MustCompile(`(--(?:clientId|xuid|auth_session)(?:\s*=\s*|\s+))[^\s"',;]+`),
		why:     "账号标识与会话标识可用于关联真实账号。",
	},
	{
		// `--username`：P4 之后是真实账号名（见上方说明）。分隔符要求同上。
		pattern: regexp.MustCompile(`(--username(?:\s*=\s*|\s+))[^\s"',;]+`),
		why:     "P2 是占位名，P4 会是真实账号名；统一替换避免那时漏改。",
	},
	{
		// `-Dhttp.proxyPassword=` / `-Dhttps.proxyPassword=`：代理密码。
		pattern: regexp.MustCompile(`(-Dhttps?\.proxyPassword=)[^\s"',;]+`),
		why:     "代理密码属于凭证，泄漏可导致内网代理被滥用。",
	},
}

// gameLogChannel 是游戏日志通道。
//
// 一个通道对应**一个**启动（launchId）；P2 只允许单活动进程（D-9），
// 因此进程内通常只有一个实例存活。
type gameLogChannel struct {
	// launchID 为本次启动标识。
	launchID string

	// mu 保护环形缓冲与计数。
	mu sync.Mutex

	// lines 为环形缓冲中的行（按时间升序，[0] 为最旧）。
	//
	// 用切片而不是固定数组 + 头尾指针：本容量下追加/裁剪的成本可忽略，
	// 而切片让「取最近 N 行」与「按游标查」都简单得多。
	lines []events.GameLogLine

	// droppedByBuffer 记录因缓冲溢出而丢弃的行数（累计）。
	droppedByBuffer int

	// droppedByQueue 记录因落盘队列溢出而丢弃的行数（累计）。
	droppedByQueue int

	// writeQueue 为待落盘的行队列（有界）。
	writeQueue chan events.GameLogLine

	// writerStop 用于通知落盘 goroutine 退出。
	writerStop chan struct{}

	// writerDone 在落盘 goroutine 退出后关闭。
	writerDone chan struct{}

	// stopOnce 保证 Stop 可被安全重复调用。
	stopOnce sync.Once

	// dir 为日志目录（启动器日志目录，非游戏目录）。
	dir string

	// filePath 为当前写入的文件路径。
	filePath string

	// file 为当前打开的文件句柄；nil 表示尚未打开。
	file *os.File

	// fileSize 为当前文件已写入字节数，用于轮转判断。
	fileSize int64

	// currentDate 为当前文件对应的日期（YYYYMMDD）。
	currentDate string

	// warnOnce 保证「采集异常」只记一次 warn。
	warnOnce sync.Once

	// emitter 为事件发射器（可空）：为空时只做缓冲与落盘，不发事件。
	emitter *events.Emitter
}

// gameLogManager 管理进程内活跃的日志通道。
//
// P2 只允许单个活动启动（D-9），因此这里只保存一个通道；
// 多实例并行属 P5，届时需要改成 map。
var gameLogManager = struct {
	// mu 保护 current 字段。
	mu sync.Mutex

	// current 为当前活动的通道；无活动启动时为 nil。
	current *gameLogChannel
}{}

// StartGameLogChannel 为一个启动创建日志通道并开始接收输出。
//
// 参数 launchID 为启动标识；参数 emitter 为事件发射器（可为 nil，此时不发事件）。
//
// 行为：
//   - 解析启动器日志目录（复用 `platform.LogDirectory()`，不碰游戏目录）；
//   - 目录不可用时通道仍然可用，但只保留内存缓冲（`FileEnabled` 为 false）；
//   - 立即清理过期文件（保留最近 `gameLogRetainFiles` 个），清理失败不阻断；
//   - 启动落盘 goroutine。
//
// 返回通道指针；同一进程内重复调用会先关闭旧通道（避免两个 writer 抢同一目录）。
func StartGameLogChannel(launchID string, emitter *events.Emitter) *gameLogChannel {
	// 关闭上一个通道：P2 单活动进程，新启动意味着旧的那个已经结束。
	StopGameLogChannel()

	channel := &gameLogChannel{
		launchID:   launchID,
		lines:      make([]events.GameLogLine, 0, gameLogBufferCapacity),
		writeQueue: make(chan events.GameLogLine, gameLogQueueCapacity),
		writerStop: make(chan struct{}),
		writerDone: make(chan struct{}),
		emitter:    emitter,
	}

	// 日志目录复用 P1-05 的解析结果（优先用户缓存目录，回退 exe 目录）。
	// 目录为空表示两处都不可写：此时只保留内存缓冲，不影响游戏运行。
	channel.dir = platform.LogDirectory()

	// 清理过期文件：只在目录可用时做；失败不阻断（记录后继续）。
	if channel.dir != "" {
		channel.pruneOldFiles()
	}

	go channel.runWriter()

	gameLogManager.mu.Lock()
	gameLogManager.current = channel
	gameLogManager.mu.Unlock()

	return channel
}

// StopGameLogChannel 关闭当前活动的日志通道。
//
// 语义：停止接收新行、尽力把队列中剩余行落盘、关闭文件句柄。
// 可安全重复调用（内部用 sync.Once 保护）。没有活动通道时为空操作。
func StopGameLogChannel() {
	gameLogManager.mu.Lock()
	channel := gameLogManager.current
	gameLogManager.current = nil
	gameLogManager.mu.Unlock()

	if channel == nil {
		return
	}
	channel.stop()
}

// CurrentGameLogChannel 返回当前活动的日志通道；无活动启动时为 nil。
func CurrentGameLogChannel() *gameLogChannel {
	gameLogManager.mu.Lock()
	defer gameLogManager.mu.Unlock()
	return gameLogManager.current
}

// stop 关闭通道。
//
// 顺序：先停 writer（让它把队列剩余行写完），再关闭文件。
// 这样进程退出前不会丢掉最后几行——对排查启动失败尤其重要。
func (c *gameLogChannel) stop() {
	if c == nil {
		return
	}

	c.stopOnce.Do(func() {
		// 通知 writer 退出；writer 会 drain 队列后再关闭 writerDone。
		close(c.writerStop)

		// 等 writer 完成（带超时保护：极端情况下不让退出流程卡死）。
		select {
		case <-c.writerDone:
		case <-time.After(2 * time.Second):
			// 超时不阻止返回：宁可丢掉尾部几行，也不阻塞应用退出。
		}

		c.mu.Lock()
		if c.file != nil {
			_ = c.file.Close()
			c.file = nil
		}
		c.mu.Unlock()
	})
}

// appendLine 接收一行游戏输出。
//
// 这是 P2-05 的唯一入口：**必须保证不阻塞**。
//   - 先脱敏（`RedactForDisplay`）；
//   - 再写入内存缓冲（溢出时丢最旧行并计数）；
//   - 最后**非阻塞**地尝试投入落盘队列（队列满时丢**本行**并计数）。
//
// 参数 stream 为 `stdout` / `stderr`；text 为原始行文本（不含行尾换行）。
func (c *gameLogChannel) appendLine(stream string, text string) {
	if c == nil {
		return
	}

	line := events.GameLogLine{
		TimestampMs: time.Now().UTC().UnixMilli(),
		Stream:      normalizeStream(stream),
		// 采集时立即脱敏：内存缓冲、事件与文件都只接触脱敏后的文本，
		// 避免「某个路径忘了脱敏」这类疏漏。
		Text: RedactForDisplay(text),
	}

	c.mu.Lock()
	c.lines = append(c.lines, line)
	// 缓冲溢出：丢掉最旧的行并计数。丢弃而不是拒绝新行——
	// 排查时最新的内容最有价值。
	for len(c.lines) > gameLogBufferCapacity {
		c.lines = c.lines[1:]
		c.droppedByBuffer++
	}
	c.mu.Unlock()

	// 落盘：**非阻塞**投入队列。队列满时丢弃本行并计数，
	// 这里绝不能阻塞——采集 goroutine 一旦被阻塞，管道缓冲区填满后会反压游戏进程，
	// 表现为游戏卡顿甚至假死。宁可丢日志，也不能影响游戏。
	select {
	case c.writeQueue <- line:
	default:
		c.mu.Lock()
		c.droppedByQueue++
		c.mu.Unlock()
	}

	// 事件：按批发送（≤ gameLogEventBatchLimit 行）。
	// 仅在通道绑定了发射器时才发；发射失败由 emitter 内部记录，不影响此处。
	c.emitIfNeeded(line)
}

// emitIfNeeded 发送增量事件。
//
// 为避免事件风暴，这里**每行单独发一条只含该行的事件**是不可接受的；
// 实现上改为「把单行放入待发批，达到批大小或周期性 flush 时一起发」。
//
// 本实现采用简化策略：每行进入待发批，由 writer goroutine 在 flush 时
// 与落盘同批发出。这样事件与文件内容天然对齐，前端看到的行不会晚于文件。
func (c *gameLogChannel) emitIfNeeded(_ events.GameLogLine) {
	// 实际发送由 writer 在 flush 时统一完成（见 `flushBatch`）。
	// 本函数保留为扩展点：若将来需要「立即发事件、延迟落盘」的相反取舍，
	// 只需改这里。
}

// runWriter 是落盘 goroutine 的主循环。
//
// 职责：
//   - 每 gameLogFlushInterval 把队列中的行批量取出；
//   - 写入当前文件（必要时轮转）；
//   - 同批发出一条 `launch.log.appended.v1` 事件。
//
// 任何 IO 失败都只记一次 warn（`warnOnce`），不退出、不影响采集方。
func (c *gameLogChannel) runWriter() {
	defer close(c.writerDone)

	ticker := time.NewTicker(gameLogFlushInterval)
	defer ticker.Stop()

	// 退出前把队列中剩余行尽量写完（进程退出时不能丢尾部内容）。
	defer c.drainAndClose()

	for {
		select {
		case <-c.writerStop:
			return
		case <-ticker.C:
			c.flushBatch()
		}
	}
}

// drainAndClose 在 writer 退出时把队列剩余行写完并关闭文件。
func (c *gameLogChannel) drainAndClose() {
	for {
		select {
		case line := <-c.writeQueue:
			c.writeLine(line)
		default:
			// 队列已空：关闭文件后返回。
			c.mu.Lock()
			if c.file != nil {
				_ = c.file.Close()
				c.file = nil
			}
			c.mu.Unlock()
			return
		}
	}
}

// flushBatch 取出一批待落盘的行并处理。
//
// 批量大小上限为 `gameLogEventBatchLimit`（200），与事件批次一致：
// 一批既写入文件，也作为一条事件发出，让「文件内容」与「事件内容」对齐。
func (c *gameLogChannel) flushBatch() {
	if c == nil {
		return
	}

	batch := make([]events.GameLogLine, 0, gameLogEventBatchLimit)

	// 非阻塞地尽量多取；取不到就返回（下次 ticker 再来）。
	for len(batch) < gameLogEventBatchLimit {
		select {
		case line := <-c.writeQueue:
			batch = append(batch, line)
		default:
			// 队列空：把已取到的部分处理掉。
			if len(batch) == 0 {
				return
			}
			c.processBatch(batch)
			return
		}
	}

	c.processBatch(batch)
}

// processBatch 把一批行写入文件并发出事件。
//
// 参数 batch 为该批行（已脱敏）。
func (c *gameLogChannel) processBatch(batch []events.GameLogLine) {
	if len(batch) == 0 {
		return
	}

	for _, line := range batch {
		c.writeLine(line)
	}

	c.emitBatch(batch)
}

// writeLine 把一行写入当前文件。
//
// 文件不可用（目录为空或打开失败）时静默跳过：内存缓冲仍然可用，
// 用户至少能在界面看到日志。所有失败只记一次 warn。
//
// 参数 line 为该行（已脱敏）。
func (c *gameLogChannel) writeLine(line events.GameLogLine) {
	if c == nil || c.dir == "" {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.ensureFileLocked(time.Now()); err != nil {
		c.warnOnce.Do(func() {
			logger().Warn(warningGameLogCapture, logging.Fields{
				Event:     "launch.gamelog.write.failed",
				ErrorCode: ErrorCodeLaunchLog,
			})
		})
		return
	}

	// 行格式：`<时间> [<流>] <文本>`。
	// 时间用本地时区的毫秒精度，便于人工阅读；不带 launchId（文件名里已有）。
	record := line.TimestampMs
	formatted := fmt.Sprintf("%s [%s] %s\n", formatLocalMillis(record), line.Stream, line.Text)

	n, err := c.file.WriteString(formatted)
	if err != nil {
		c.warnOnce.Do(func() {
			logger().Warn(warningGameLogCapture, logging.Fields{
				Event:     "launch.gamelog.write.failed",
				ErrorCode: ErrorCodeLaunchLog,
			})
		})
		return
	}

	c.fileSize += int64(n)

	// 单文件超限：关闭句柄，下次写入自动切到新文件（当日文件已存在时追加）。
	if c.fileSize >= gameLogFileMaxBytes {
		_ = c.file.Close()
		c.file = nil
	}
}

// ensureFileLocked 确保当前有可写的文件句柄。
//
// 调用前提：已持有 c.mu。跨天或超限后重新打开当日文件。
func (c *gameLogChannel) ensureFileLocked(now time.Time) error {
	date := now.Format(gameLogDateLayout)

	if c.file != nil && c.currentDate == date {
		return nil
	}

	if c.file != nil {
		_ = c.file.Close()
		c.file = nil
	}

	path := filepath.Join(c.dir, gameLogFileName(c.launchID, date))
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, gameLogFilePerm)
	if err != nil {
		return err
	}

	info, statErr := file.Stat()
	if statErr != nil {
		_ = file.Close()
		return statErr
	}

	c.file = file
	c.currentDate = date
	c.filePath = path
	c.fileSize = info.Size()
	return nil
}

// emitBatch 发出一条批量增量事件。
//
// 参数 batch 为该批行。事件载荷含 launchId、这批行、以及**累计**丢弃数。
// 发射失败不影响日志通道：`events.Emit` 内部已返回错误，这里只忽略——
// 日志事件是辅助通知，丢了可由快照恢复。
func (c *gameLogChannel) emitBatch(batch []events.GameLogLine) {
	if c == nil || c.emitter == nil || len(batch) == 0 {
		return
	}

	c.mu.Lock()
	droppedTotal := c.droppedByBuffer + c.droppedByQueue
	c.mu.Unlock()

	_, _ = events.Emit(c.emitter, events.EventLaunchLogAppended, events.LaunchLogAppendedPayload{
		LaunchID:     c.launchID,
		Lines:        batch,
		DroppedLines: droppedTotal,
	})
}

// pruneOldFiles 清理过期的 game-*.log 文件，只保留最近 `gameLogRetainFiles` 个。
//
// 清理时机：通道创建时（即每次启动）。选择「按数量」而不是「按天数」：
// 游戏日志的价值集中在最近几次启动，按数量保留比按时间更可预测，
// 也避免「长期不启动导致全部过期」。
//
// 失败不阻断：目录扫描或删除失败只记一次 warn，启动照常进行。
func (c *gameLogChannel) pruneOldFiles() {
	files, err := listGameLogFiles(c.dir)
	if err != nil {
		return
	}

	if len(files) <= gameLogRetainFiles {
		return
	}

	// 按修改时间从新到旧排序，保留前 N 个，其余删除。
	sort.Slice(files, func(i, j int) bool {
		return files[i].ModTime().After(files[j].ModTime())
	})

	for index, file := range files {
		if index < gameLogRetainFiles {
			continue
		}
		if removeErr := os.Remove(filepath.Join(c.dir, file.Name())); removeErr != nil {
			c.warnOnce.Do(func() {
				logger().Warn(warningGameLogCapture, logging.Fields{
					Event:     "launch.gamelog.prune.failed",
					ErrorCode: ErrorCodeLaunchLog,
				})
			})
		}
	}
}

// listGameLogFiles 列出目录下符合本通道命名规则的文件。
//
// 只认 `game-<8 位>-<8 位数字>.log`：目录里可能同时存在 P1-05 的 `app-*.log`
// 与其他文件，它们不属于本通道的清理范围。
func listGameLogFiles(dir string) ([]os.FileInfo, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	result := make([]os.FileInfo, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !isGameLogFileName(entry.Name()) {
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

// isGameLogFileName 判断文件名是否为游戏日志文件。
//
// 形态：`game-<launchId 前 8 位>-<YYYYMMDD>.log`。
// 该判定与 `internal/logging.isLogFileName`（只认 `app-*.log`）互不重叠，
// 保证两套日志永远不会互相清理。
func isGameLogFileName(name string) bool {
	if !strings.HasPrefix(name, gameLogFilePrefix) || !strings.HasSuffix(name, gameLogFileSuffix) {
		return false
	}

	middle := strings.TrimSuffix(strings.TrimPrefix(name, gameLogFilePrefix), gameLogFileSuffix)
	parts := strings.Split(middle, "-")
	if len(parts) != 2 {
		return false
	}
	if len(parts[0]) == 0 || len(parts[0]) > gameLogLaunchIDPrefixLength {
		return false
	}
	if len(parts[1]) != len(gameLogDateLayout) {
		return false
	}
	_, err := time.Parse(gameLogDateLayout, parts[1])
	return err == nil
}

// gameLogFileName 生成游戏日志文件名。
//
// 参数 launchID 为启动标识（取前 8 位，不足则全取）；date 为日期（YYYYMMDD）。
func gameLogFileName(launchID string, date string) string {
	prefix := launchID
	if len(prefix) > gameLogLaunchIDPrefixLength {
		prefix = prefix[:gameLogLaunchIDPrefixLength]
	}
	// 把 launchID 中的路径分隔符与空格替换掉，避免生成非法文件名。
	prefix = sanitizeFileNamePart(prefix)
	if prefix == "" {
		prefix = "unknown"
	}
	return gameLogFilePrefix + prefix + "-" + date + gameLogFileSuffix
}

// sanitizeFileNamePart 清理文件名片段中的非法字符。
//
// 参数 part 为原始片段。只保留字母、数字、连字符与下划线。
func sanitizeFileNamePart(part string) string {
	var builder strings.Builder
	builder.Grow(len(part))
	for _, char := range part {
		switch {
		case char >= 'a' && char <= 'z':
		case char >= 'A' && char <= 'Z':
		case char >= '0' && char <= '9':
		case char == '-', char == '_':
		default:
			continue
		}
		builder.WriteRune(char)
	}
	return builder.String()
}

// normalizeStream 把任意来源字符串收敛为 `stdout` / `stderr`。
//
// 参数 stream 为原始标识；无法识别时按 `stdout` 处理（不因标注问题丢行）。
func normalizeStream(stream string) string {
	switch strings.ToLower(strings.TrimSpace(stream)) {
	case streamStderr:
		return streamStderr
	default:
		return streamStdout
	}
}

// formatLocalMillis 把 UTC 毫秒时间戳格式化为本地时区的可读时间。
//
// 参数 millis 为 UTC Unix 毫秒。文件是给人看的，因此用本地时区。
func formatLocalMillis(millis int64) string {
	return time.UnixMilli(millis).Format("2006-01-02 15:04:05.000")
}

// ===== 脱敏（本包唯一实现，P2-09 必须复用）=====

// RedactCommandSummary 对启动命令的参数数组做脱敏。
//
// 这是**唯一**的命令摘要脱敏实现：P2-09 的参数预览必须调用本函数，
// 不得自行再写一套（重复实现会导致两处规则漂移，漏掉某一项就泄漏凭证）。
//
// 处理两类形态：
//  1. 参数级：`--accessToken` 等键的**下一个参数**整体替换为 `***`；
//  2. 值内嵌：`--accessToken=xxx` / `-Dhttp.proxyPassword=xxx` 等替换 `=` 后的内容。
//     以 `-D` 开头的写法常见于 JVM 参数。
//
// 参数 args 为原始参数数组；返回**新的**脱敏数组（不修改入参）。
func RedactCommandSummary(args []string) []string {
	if len(args) == 0 {
		return nil
	}

	result := make([]string, 0, len(args))

	for index := 0; index < len(args); index++ {
		candidate := strings.TrimSpace(args[index])

		// 形态 1：键后紧跟值（`--accessToken <value>`）。
		if commandSensitiveKeyValues[candidate] {
			result = append(result, args[index])
			// 下一个参数是敏感值：整体替换。
			if index+1 < len(args) {
				result = append(result, gameLogRedacted)
				index++
			}
			continue
		}

		// 形态 2：键与值在同一项（`--accessToken=xxx`、`-Dhttp.proxyPassword=xxx`）。
		replaced := false
		for _, prefix := range commandSensitivePrefixes {
			if strings.HasPrefix(candidate, prefix) {
				result = append(result, prefix+gameLogRedacted)
				replaced = true
				break
			}
		}
		if replaced {
			continue
		}

		// 其余参数原样保留（路径等由调用方另行按需脱敏）。
		result = append(result, args[index])
	}

	return result
}

// RedactForDisplay 对单行文本做脱敏（用于游戏输出与控制台展示）。
//
// 覆盖（逐条原因见 `displaySensitivePatterns` 的 why 字段）：
// `Authorization:` 头、`Bearer ` 前缀、`--accessToken`、`--clientId`/`--xuid`/
// `--auth_session`、`--username`、`-Dhttp(s).proxyPassword=`。
//
// **不做 IP 脱敏**（有意决策）：游戏日志可能含服务器地址与玩家聊天，
// 改写会让排查失真；且本启动器默认不上传日志（AGENTS.md 5.12），
// 因此保留原文对用户更有价值。
//
// 参数 line 为原始文本；返回脱敏后（必要时截断）的文本。
func RedactForDisplay(line string) string {
	if line == "" {
		return ""
	}

	text := line
	for _, rule := range displaySensitivePatterns {
		text = rule.pattern.ReplaceAllStringFunc(text, func(match string) string {
			// 保留匹配到的前缀（键名 / `Bearer` / `Authorization:`），只替换值。
			parts := rule.pattern.FindStringSubmatch(match)
			if len(parts) >= 2 {
				return parts[1] + gameLogRedacted
			}
			// `Bearer xxx` 这类没有分组的规则：统一替换为 `Bearer ***`。
			if strings.HasPrefix(strings.ToLower(match), "bearer") {
				return "Bearer " + gameLogRedacted
			}
			return gameLogRedacted
		})
	}

	// 截断超长行：异常输出（如二进制被当文本）不应撑爆内存与文件。
	runes := []rune(text)
	if len(runes) > gameLogMaxLineRunes {
		return string(runes[:gameLogMaxLineRunes]) + "…"
	}
	return text
}

// ===== 快照读取 =====

// gameLogSnapshot 是一次快照查询的结果。
type gameLogSnapshot struct {
	// lines 为本页行（按时间倒序，最新在前）。
	lines []events.GameLogLine

	// nextBeforeMs 为继续往更早翻的游标；0 表示没有更早的行。
	nextBeforeMs int64

	// droppedLines 为累计丢弃行数。
	droppedLines int

	// hasMore 表示是否还有更早的行。
	hasMore bool
}

// queryGameLog 从当前通道读取一页日志（快照方式）。
//
// 参数 launchID 为要查询的启动；为空表示当前活动启动。
// 参数 beforeMs 为时间游标（只返回早于该时间的行）；0 表示从最新开始。
// 参数 limit 为最大行数（<=0 用默认值，>200 截断）。
//
// 返回快照与错误：无活动启动 / launchId 不匹配时返回 `LAUNCH_LOG_001`。
func queryGameLog(launchID string, beforeMs int64, limit int) (gameLogSnapshot, *Error) {
	channel := CurrentGameLogChannel()
	if channel == nil {
		return gameLogSnapshot{}, newInputError(ErrorCodeLaunchLog, messageGameLogNoActiveLaunch)
	}

	trimmedID := strings.TrimSpace(launchID)
	if trimmedID != "" && trimmedID != channel.launchID {
		return gameLogSnapshot{}, newInputError(ErrorCodeLaunchLog, messageGameLogUnknownLaunch)
	}

	// limit 在 Go 边界裁剪：不信任前端传入值。
	normalizedLimit := limit
	if normalizedLimit <= 0 {
		normalizedLimit = DefaultGameLogLimit
	}
	if normalizedLimit > MaxGameLogLimit {
		normalizedLimit = MaxGameLogLimit
	}

	channel.mu.Lock()
	defer channel.mu.Unlock()

	// 从最新往旧遍历，跳过晚于游标的行。
	collected := make([]events.GameLogLine, 0, normalizedLimit)
	for index := len(channel.lines) - 1; index >= 0; index-- {
		line := channel.lines[index]
		if beforeMs > 0 && line.TimestampMs >= beforeMs {
			continue
		}
		collected = append(collected, line)
		if len(collected) >= normalizedLimit {
			break
		}
	}

	snapshot := gameLogSnapshot{
		lines:        collected,
		droppedLines: channel.droppedByBuffer + channel.droppedByQueue,
	}

	// 游标取本页最旧行的时间：下一次查询用它取更早的行。
	if len(collected) > 0 {
		snapshot.nextBeforeMs = collected[len(collected)-1].TimestampMs
		// 是否还有更早行：本页取满了 limit 且缓冲里还有更旧的行。
		snapshot.hasMore = len(collected) >= normalizedLimit
	}

	return snapshot, nil
}

// 快照查询的默认与上限条数。
const (
	// DefaultGameLogLimit 是快照查询的默认条数。
	DefaultGameLogLimit = 200

	// MaxGameLogLimit 是快照查询的硬上限。
	//
	// 取 1000：与 P1-05 的 `MaxQueryLimit` 一致，避免一次拉取整个 2000 行缓冲
	// 造成前端渲染卡顿。
	MaxGameLogLimit = 1000
)

// 编译期断言：bufio 在将来改为流式读取时会用到，当前保留引用避免误删 import。
var _ = bufio.NewReader

// 编译期断言：strconv 用于文件名或计数格式化时可用。
var _ = strconv.Itoa
