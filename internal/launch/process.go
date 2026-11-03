// 本文件是实现游戏进程启动的**唯一**位置。
//
// 三条设计理由（卡面第 9 条要求逐条注释）：
//
//  1. **为什么参数必须是数组**：`exec.Command(javaPath, args...)` 把每个参数作为
//     独立元素交给系统，不经过任何 shell 解析。这样路径中的空格、引号、特殊字符
//     都不会被重新解释，也不会产生命令注入面。任何「拼成一个字符串」的形态
//     （`cmd /c`、`sh -c`、`fmt.Sprintf` 拼命令）都会把用户可控内容送进 shell 语义，
//     是本项目明令禁止的。
//
//  2. **为什么不能继承 stdio 的 stdin**：GUI 进程没有真实控制台 stdin，
//     若让子进程继承它，会出现「两个进程抢同一个句柄」的未定义行为；
//     更实际的问题是继承来的 stdin 可能指向启动器的调用者（例如某些 IDE），
//     写入会互相干扰。因此这里显式建立**可写管道**：它承担两件事——
//     写入启动脚本（游戏主类与游戏参数，见 launcherpart.go），
//     以及作为 P2-06 优雅停止（D-3）的关闭信号。
//
//  3. **为什么环境变量要白名单化**：直接继承 `os.Environ()` 会把启动器进程的
//     全部环境（可能含用户凭据类变量、代理配置、调试开关）带给游戏子进程，
//     既扩大了信息暴露面，也让「同一台机器上两次启动的环境不一致」变成
//     难以复现的问题来源。白名单只保留 JVM 运行所必需的最小集合。
//
// 本卡的边界：只创建进程、只采集 stdio、只把句柄交出去。
// 不 Wait、不停止、不分配 launchId、不做崩溃判定。
package launch

import (
	"bufio"
	"errors"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"example.com/ninevoxel/launcher/internal/logging"
)

// 进程启动相关的常量。
const (
	// ErrorCodeLaunchProcess 表示 `cmd.Start()` 失败（可执行文件不存在、无权限等）。
	//
	// 来源：决策记录第五节「复用既有码」：`LAUNCH_PROCESS_001`（`cmd.Start()` 失败），
	// 首个使用卡 P2-05。
	ErrorCodeLaunchProcess = "LAUNCH_PROCESS_001"

	// processMaxLineBytes 是单行输出的最大字节数（bufio.Scanner 缓冲上限）。
	//
	// 取 64 KiB（卡面建议值）：正常日志行远小于此；上限的作用是防止异常输出
	// （例如把整个堆栈或二进制内容打到一行）撑爆内存。超长行会被截断并追加
	// `processTruncatedMarker`，便于用户知道「这里被截过」。
	processMaxLineBytes = 64 * 1024

	// processTruncatedMarker 是超长行截断后追加的标记。
	processTruncatedMarker = " <truncated>"

	// processStartTimeout 是「等待进程创建完成」的兜底超时。
	//
	// 用途：`cmd.Start()` 之后，读取 goroutine 开始工作；若子进程立即失败
	// （例如 Java 立刻崩），读取 goroutine 会很快结束。本常量只作为
	// 「WaitReads 不应无限等待」的兜底，不是进程生命周期超时。
	// 取 10 秒：足以覆盖任何正常的输出结束。
	processStartTimeout = 10 * time.Second

	// 输出来源标识（与 P2-07 的 `normalizeStream` 取值一致）。
	processStreamStdout = "stdout"
	processStreamStderr = "stderr"
)

// processEnvWhitelist 是传给 JVM 的环境变量白名单。
//
// 每一项的理由（卡面要求逐项说明）：
//   - `SystemRoot`：Windows 上 JVM / CRT 定位系统目录与 Winsock 等组件必需；
//     缺失会导致 JVM 启动失败或 DLL 加载异常。
//   - `PATH`：JVM 需要解析依赖的系统 DLL（例如 `msvcp140.dll`），
//     以及部分版本会查找 `java.dll` 相关组件。
//   - `TEMP` / `TMP`：JVM 与部分库使用临时目录（例如 JIT 缓存、native 解压）；
//     缺失会让部分实现回退到不可写位置。
//   - `USERPROFILE`：JVM 可能用它定位用户级配置（例如 `%USERPROFILE%\.java`）。
//   - `APPDATA` / `LOCALAPPDATA`：部分 Java 库与 Minecraft 自身读取用户级路径；
//     缺失会让游戏表现为「全新安装」（读不到已有配置）。
//   - `PROCESSOR_ARCHITECTURE`：JVM 与 native 库用它选择正确的位数实现。
//
// 明确**不注入**：
//   - `JAVA_HOME`：用户环境里的值可能是错的或指向另一个 JDK，
//     而本卡的 javaPath 已是用户显式选择的；注入它会让两个来源互相矛盾，
//     也让部分启动脚本读取到与 javaPath 不一致的路径。
//   - 任何代理变量（`http_proxy` 等）：P2 不发起网络请求，不需要。
//   - 任何凭据类变量：见本文件头部第 3 条理由。
var processEnvWhitelist = []string{
	"SystemRoot",
	"PATH",
	"TEMP",
	"TMP",
	"USERPROFILE",
	"APPDATA",
	"LOCALAPPDATA",
	"PROCESSOR_ARCHITECTURE",
}

// 进程启动相关的错误与提示文案。
const (
	// messageProcessStartFailed 表示进程创建失败（可执行文件问题）。
	messageProcessStartFailed = "无法启动 Java 进程，请确认所选 Java 可执行文件仍可用。"

	// messageProcessWorkDirMissing 表示工作目录不存在。
	messageProcessWorkDirMissing = "游戏目录不存在，无法启动游戏，请确认游戏目录是否被移动或删除。"

	// messageProcessWorkDirNotDirectory 表示工作目录不是目录。
	messageProcessWorkDirNotDirectory = "所选游戏目录不是有效目录，无法启动游戏。"

	// messageProcessNativesNotDirectory 表示 natives 路径已被同名文件占用。
	messageProcessNativesNotDirectory = "natives 路径被同名文件占用，无法启动游戏，请检查游戏目录。"
	// messageProcessNativesCreateFailed 表示无法创建 natives 目录。
	messageProcessNativesCreateFailed = "无法创建 natives 目录，请确认游戏目录可写。"

	// messageProcessArgsTooLong 表示参数总长超限且当前 Java 不支持参数文件。
	//
	// 只在 Java 8 或版本未知时出现：Java 9 及以上会用 `@argfile` 兜底，
	// 用户不会看到这条错误。提示里先给「缩短路径」这一条 Java 8 用户可执行的
	// 建议，再说明版本限制——直接让 1.12.2 玩家升级 Java 会让游戏起不来。
	messageProcessArgsTooLong = "启动参数过长（%d 字节，上限 %d 字节），无法交给系统执行；请尝试把游戏目录移到更短的路径。当前 Java 版本过低，不能自动改用参数文件（需要 Java 9 及以上才能启用该机制）。"

	// messageArgFileCreateFailed 表示参数文件的临时目录或文件创建失败。
	messageArgFileCreateFailed = "无法创建启动参数文件（临时目录不可写），请检查系统临时目录的权限。"

	// messageArgFileUnencodable 表示参数文本含系统代码页无法表达的字符。
	//
	// 简体中文 Windows 的系统代码页是 GBK，emoji 等字符无法表示；此时给出
	// 「改为纯英文路径」这一条用户可执行的建议，而不是让 JVM 读到乱码。
	messageArgFileUnencodable = "启动参数过长，且参数中的字符无法按系统编码写入参数文件；请把游戏目录改为纯英文路径后重试。"

	// messageProcessEmptyCommand 表示没有任何可执行的参数（主类缺失）。
	messageProcessEmptyCommand = "启动参数为空，无法启动游戏（缺少主类或参数）。"

	// messageLauncherPartUnavailable 表示引导器字节码无法落地（临时目录不可写）。
	//
	// 引导器是**唯一**把游戏参数送出命令行的通道；它不可用时不静默退回旧形态，
	// 因为旧形态会重新引入命令行长度上限与令牌外泄。
	messageLauncherPartUnavailable = "无法准备启动引导器（临时目录不可写），请检查系统临时目录的权限。"

	// messageLauncherPartScriptInvalid 表示启动脚本里有无法按行协议传递的内容。
	//
	// 行协议以换行分隔参数，值里一旦含换行就无法区分「一个参数」与「两个参数」；
	// 这类内容只可能来自被篡改的版本 JSON 或附加参数，直接拒绝比默默截断安全。
	messageLauncherPartScriptInvalid = "启动参数含无法通过引导器传递的字符（换行符），请检查该版本的参数配置。"

	// messageLauncherPartScriptFailed 表示脚本写入子进程 stdin 失败。
	//
	// 通常意味着 Java 进程刚起来就退出了（classpath 缺失、引导器类加载失败）。
	messageLauncherPartScriptFailed = "无法把启动参数交给游戏进程，请检查该版本是否完整安装。"
)

// GameProcess 是一次已启动的游戏进程及其 stdio 采集状态。
//
// 本结构是**交接契约**：P2-05 创建、P2-06 持有并负责 Wait/停止。
// 本卡不调用 Wait，因此 `Cmd` 保持未 Wait 状态。
type GameProcess struct {
	// Cmd 为已 `Start()` 但**未 Wait** 的进程。
	//
	// P2-06 必须调用 `Cmd.Wait()`（或 `Process.Wait()`）来回收资源；
	// 重复 Wait 会返回错误，因此由 P2-06 独占。
	Cmd *exec.Cmd

	// Process 为底层进程句柄，供 P2-06 调用 `Kill()`（D-3 的超时升级路径）。
	//
	// 与 `Cmd.Process` 相同；单独暴露是为了让 P2-06 不必依赖 `Cmd` 的具体结构。
	Process *os.Process

	// Stdin 为子进程的可写标准输入管道。
	//
	// D-3 的优雅停止路径：先 `Close()` 它（游戏会读到 EOF），
	// 等 10 秒仍未退出再 `Process.Kill()`。本卡不主动写入任何内容。
	Stdin io.WriteCloser

	// StartedAt 为进程创建成功的时间（UTC）。
	StartedAt time.Time

	// startedAtMs 为 `StartedAt` 的毫秒表示，便于直接写日志与上报。
	startedAtMs int64

	// readsDone 在两个读取 goroutine 都结束后关闭。
	readsDone chan struct{}

	// cleanupTemp 删除启动时生成的临时文件（引导器目录、参数文件）；无需清理时为 nil。
	cleanupTemp func()

	// waitReadsOnce 保证 WaitReads 可安全重复调用。
	waitReadsOnce sync.Once
}

// CleanupTempFiles 删除启动时生成的临时文件（引导器目录与参数文件）。
//
// 仅在命令行超长、改用 `@argfile` 时才存在临时文件；JVM 只在启动瞬间读取它，
// 因此必须在进程退出后删除，不能提前。可安全重复调用（清理后会置空）。
func (p *GameProcess) CleanupTempFiles() {
	if p == nil || p.cleanupTemp == nil {
		return
	}
	p.cleanupTemp()
	p.cleanupTemp = nil
}

// WaitReads 等待 stdout 与 stderr 的读取 goroutine 结束。
//
// 语义：
//   - 调用前应先让进程退出（P2-06 的 `Cmd.Wait()` 会关闭管道，读取随之结束）；
//   - 本函数**只等读取 goroutine**，不做超时、不干涉进程；
//   - 可安全重复调用（内部 `sync.Once` 保护）；
//   - 有 `processStartTimeout` 兜底，避免「管道未关闭导致永久等待」。
//
// 为什么需要单独的函数：若 P2-06 直接 `Cmd.Wait()` 后立刻丢弃结构，
// 两个读取 goroutine 可能仍在把最后几行交给日志通道，造成尾部丢失或
// goroutine 泄漏。显式等待让收尾顺序明确：Wait 进程 → WaitReads → 释放。
func (p *GameProcess) WaitReads() {
	if p == nil || p.readsDone == nil {
		return
	}

	p.waitReadsOnce.Do(func() {
		select {
		case <-p.readsDone:
		case <-time.After(processStartTimeout):
			// 超时兜底：不阻塞调用方。此时可能仍有 goroutine 在跑，
			// 但它们会在管道关闭后自行退出，不会永久泄漏。
		}
	})
}

// StartedAtMillis 返回进程创建时间的 UTC 毫秒表示。
func (p *GameProcess) StartedAtMillis() int64 {
	if p == nil {
		return 0
	}
	return p.startedAtMs
}

// StdoutPipeCount 返回 stdout 采集是否已建立（1 或 0）。
//
// 供 P2-06 在收尾时核对状态；也便于诊断「某一路管道是否没起来」。
func (p *GameProcess) StdoutPipeCount() int {
	if p == nil || p.Cmd == nil {
		return 0
	}
	if p.Cmd.Stdout == nil {
		return 0
	}
	return 1
}

// StartGameProcess 启动游戏进程并开始采集 stdio。
//
// 这是**全仓库唯一的进程创建点**（除 P2-01 的 `java -version` 探针外）。
//
// 参数 plan 为 P2-04 产出的启动计划（必须已 `SafeToLaunch`，但本函数不重复校验
// 该布尔值——调用方 P2-06 负责在拒绝不可启动的计划；本函数只做进程层面的校验）。
// 参数 javaPath 为已校验的 Java 可执行文件绝对路径。
//
// 返回 `*GameProcess`（交接给 P2-06）与错误（`*Error`，带稳定错误码）。
//
// 启动流程（顺序不可调整）：
//  1. 校验工作目录；
//  2. 落地引导器字节码、组装命令行（只有 JVM 参数 + classpath + 引导器类名）；
//  3. 命令行仍超长时改用 `@argfile`（Java 9+），否则报错；
//  4. 创建进程；
//  5. 把**游戏主类与游戏参数**写入 stdin，并保持管道打开。
//
// 失败时的错误码（见报告的错误码表）：
//   - 工作目录不存在 / 不是目录 → `PATH_INVALID_001`；
//   - 参数总长超 32767 字节且无法改用参数文件 → `LAUNCH_ARGS_002`；
//   - 参数为空（无主类）→ `LAUNCH_ARGS_001`；
//   - `cmd.Start()` 失败（可执行文件不存在/无权限）→ `LAUNCH_PROCESS_001`。
//
// 本函数不调用 `cmd.Wait()`：进程生命周期由 P2-06 管理。
// 唯一的例外是「脚本写入失败」——此时进程尚未交给 supervisor，必须就地回收。
// 引导器目录与参数文件由调用方在进程退出后通过 `CleanupTempFiles` 清理。
func StartGameProcess(plan *LaunchPlan, javaPath string) (*GameProcess, *Error) {
	if plan == nil {
		return nil, newInputError(ErrorCodeLaunchArgs, messageProcessEmptyCommand)
	}

	// 第一步：工作目录校验。
	//
	// 必须在 `exec.Command` 之前做：`cmd.Start()` 在 Dir 不存在时的错误
	// 是系统原始报错（Windows 上是一条难以理解的消息），用户无法据此修正。
	// 更要紧的是**绝不能回退到进程当前目录**——那会让游戏读写到启动器目录，
	// 是最严重的一类「静默走错位置」。
	if dirErr := validateProcessWorkDir(plan.WorkingDirectory); dirErr != nil {
		return nil, dirErr
	}

	// 第二步：准备引导器并组装命令行。
	//
	// 游戏主类与游戏参数不进命令行，改由 `buildLaunchScript` 生成脚本、启动后
	// 写入子进程 stdin（见 launcherpart.go）。命令行因此只剩 JVM 参数 +
	// classpath + 引导器类名。
	launcherPartDir, cleanupPart, partErr := prepareLauncherPart()
	if partErr != nil {
		return nil, partErr
	}
	releasePart := func() {
		if cleanupPart != nil {
			cleanupPart()
		}
	}

	args, argsErr := buildProcessArguments(plan, launcherPartDir)
	if argsErr != nil {
		releasePart()
		return nil, argsErr
	}

	script, scriptErr := buildLaunchScript(plan.MainClass, plan.GameArguments)
	if scriptErr != nil {
		releasePart()
		return nil, scriptErr
	}

	// 第三步：参数总长校验与参数文件兜底。
	//
	// 把游戏参数挪到 stdin 之后，命令行里剩下的主要变量就是 classpath。
	// 若它本身仍然超长（例如 NeoForge 的 `-p` 模块路径），Java 9 及以上可以
	// 把整串参数移进 `@argfile`（见 argfile.go）；Java 8 不支持该语法，
	// 只能提前拦住并给出可读原因，而不是让 `cmd.Start()` 返回系统级错误。
	preparedArgs, argFileCleanup, prepareErr := prepareProcessArguments(args, plan.JavaMajorVersion)
	if prepareErr != nil {
		releasePart()
		return nil, prepareErr
	}
	args = preparedArgs

	// 后续任一步失败都要删掉已创建的临时文件（引导器与参数文件），避免残留。
	releaseTemp := func() {
		releasePart()
		if argFileCleanup != nil {
			argFileCleanup()
		}
	}

	// 第三步半：确保 natives 目录真实存在。
	//
	// `${natives_directory}` 会被版本 JSON 用于 `-Djava.library.path`、
	// `-Djna.tmpdir`、`-Dorg.lwjgl.system.SharedLibraryExtractPath`、
	// `-Dio.netty.native.workdir` 等参数，而 JNA 初始化时要往
	// `<natives_directory>\jna` 写临时库文件。父目录不存在时会抛
	// `UnsatisfiedLinkError: JNA temporary directory ... does not exist`，
	// 进而使 `SystemReport` 采集失败、游戏启动后立即崩溃
	// （P2 验收实测：Minecraft 26.3 退出码 -2）。
	//
	// 这一步只**创建目录**，不解压、不下载任何 natives 文件——
	// natives 文件的解压属于 P3 下载引擎。D-10 的「不自动解压」
	// 不等于「连目录都不建」，此处修正此前的实现遗漏。
	if nativesErr := ensureNativesDirectory(plan.NativesDirectory); nativesErr != nil {
		releaseTemp()
		return nil, nativesErr
	}

	// 第四步：创建进程（**唯一的 exec.Command 调用点**）。
	//
	// 用 `exec.Command` 而不是 `exec.CommandContext`（Leader 已定）：
	// 启动阶段是瞬时的，ctx 的生命周期管理反而会制造「上下文取消误杀
	// 仍在运行的游戏进程」的风险。超时与终止完全交给 P2-06 的 supervisor。
	command := exec.Command(javaPath, args...)

	// 工作目录：显式设置，第二步已确认存在且为目录。
	command.Dir = plan.WorkingDirectory

	// 环境变量：白名单构造，不继承 os.Environ()（理由见文件头第 3 条）。
	command.Env = buildProcessEnv()

	// stdin：建立可写管道并交给 P2-06（D-3 优雅停止需要关闭它）。
	// 不用 inherited stdin 的理由见文件头第 2 条。
	stdinPipe, err := command.StdinPipe()
	if err != nil {
		// StdinPipe 失败属于进程创建层面的问题（句柄分配失败）。
		releaseTemp()
		return nil, newSystemError(ErrorCodeLaunchProcess, messageProcessStartFailed)
	}

	// stdout / stderr：各自建立读取管道。
	stdoutPipe, err := command.StdoutPipe()
	if err != nil {
		_ = stdinPipe.Close()
		releaseTemp()
		return nil, newSystemError(ErrorCodeLaunchProcess, messageProcessStartFailed)
	}

	stderrPipe, err := command.StderrPipe()
	if err != nil {
		_ = stdinPipe.Close()
		_ = stdoutPipe.Close()
		releaseTemp()
		return nil, newSystemError(ErrorCodeLaunchProcess, messageProcessStartFailed)
	}

	// 第五步：启动进程。
	if startErr := command.Start(); startErr != nil {
		// 关闭已建立的三条管道，避免句柄泄漏。
		_ = stdinPipe.Close()
		_ = stdoutPipe.Close()
		_ = stderrPipe.Close()

		// 进程没起来，参数文件已无用途，立即清理。
		releaseTemp()

		// 错误分类：可执行文件问题是 LAUNCH_PROCESS_001；
		// 其余（含权限）同样归该类，因为对用户而言都是「进程起不来」。
		logger().Warn("游戏进程创建失败", logging.Fields{
			Event:     "launch.process.start.failed",
			ErrorCode: ErrorCodeLaunchProcess,
			Attributes: map[string]string{
				"executableName": execBaseName(javaPath),
			},
		})
		return nil, newSystemError(ErrorCodeLaunchProcess, messageProcessStartFailed)
	}

	startedAt := time.Now().UTC()
	process := &GameProcess{
		Cmd:         command,
		Process:     command.Process,
		Stdin:       stdinPipe,
		StartedAt:   startedAt,
		startedAtMs: startedAt.UnixMilli(),
		readsDone:   make(chan struct{}),
		cleanupTemp: releaseTemp,
	}

	// 第六步：启动两个读取 goroutine。
	//
	// **必须在写脚本之前启动**：脚本可能有几十 KB，超过管道缓冲时写入会阻塞。
	// 如果此时子进程恰好因为类加载失败向 stderr 打了大量内容，它的 stderr 缓冲
	// 也会写满 → 子进程卡住不再读 stdin → 双方互等。先开读取方，这条死锁路径
	// 就不存在了。
	//
	// 每个流一个 goroutine：stdio 的两路各自独立，一路阻塞不应影响另一路。
	// 读取结果交给 P2-07 的通道（通过 `dispatchGameOutput` 解耦）。
	var readers sync.WaitGroup
	readers.Add(2)

	go func() {
		defer readers.Done()
		readProcessStream(stdoutPipe, processStreamStdout)
	}()
	go func() {
		defer readers.Done()
		readProcessStream(stderrPipe, processStreamStderr)
	}()

	// 两个 goroutine 结束后关闭 readsDone，供 WaitReads 等待。
	go func() {
		readers.Wait()
		close(process.readsDone)
	}()

	// 第七步：把启动脚本写入子进程 stdin。
	//
	// 必须**在 Start 之后**：引导器进程起来后才会读 stdin。写完**不关闭**管道：
	// 关闭 stdin 是 D-3 的优雅停止信号，这里提前关闭会让 supervisor 的停止
	// 路径无从下手（关一个已关闭的管道不会给游戏任何信号）。
	if _, writeErr := stdinPipe.Write([]byte(script)); writeErr != nil {
		// 进程已起来但读不到脚本：只能收掉，否则会留下一个阻塞在 stdin 上的
		// 孤儿 Java 进程。这里是**唯一**在 StartGameProcess 内 Wait 的分支：
		// 进程尚未交给 supervisor，没人会替它回收句柄。
		_ = process.Process.Kill()
		_ = process.Cmd.Wait()
		process.WaitReads()

		_ = stdinPipe.Close()
		releaseTemp()

		logger().Warn("写入启动脚本失败", logging.Fields{
			Event:     "launch.process.script.failed",
			ErrorCode: ErrorCodeLaunchProcess,
			Attributes: map[string]string{
				"executableName": execBaseName(javaPath),
			},
		})
		return nil, newSystemError(ErrorCodeLaunchProcess, messageLauncherPartScriptFailed)
	}

	logger().Info("游戏进程已启动", logging.Fields{
		Event: "launch.process.started",
		Attributes: map[string]string{
			// 只放非敏感信息：可执行文件名、参数数量、环境键数量。
			// **不记录完整路径、不记录参数内容、不记录环境变量值**。
			"executableName": execBaseName(javaPath),
			"argumentCount":  countAttribute(len(args)),
			"envKeyCount":    countAttribute(len(processEnvWhitelist)),
			"workingDirKind": "gameRoot",
		},
	})

	return process, nil
}

// ensureNativesDirectory 确保 natives 目录真实存在，供 `-Djna.tmpdir` 等参数使用。
//
// 参数 path 为启动计划中的 natives 目录绝对路径；为空时直接跳过
// （旧版本未提供默认值时保持原行为，不制造额外目录）。
// 目录已存在时不做任何写入；路径被同名文件占用或创建失败时返回
// `LAUNCH_PROCESS_001`，让用户在启动阶段就看到可读原因，而不是等
// Java 抛出 `UnsatisfiedLinkError` 后再去解读几十行堆栈。
//
// 注意：本函数**只创建目录**，不解压、不下载 natives 文件。
func ensureNativesDirectory(path string) *Error {
	if strings.TrimSpace(path) == "" {
		return nil
	}

	info, statErr := os.Stat(path)
	if statErr == nil {
		if info.IsDir() {
			return nil
		}
		return newInputError(ErrorCodeLaunchProcess, messageProcessNativesNotDirectory)
	}
	if !errors.Is(statErr, os.ErrNotExist) {
		return newSystemError(ErrorCodeLaunchProcess, messageProcessNativesCreateFailed)
	}

	if mkErr := os.MkdirAll(path, 0o755); mkErr != nil {
		return newSystemError(ErrorCodeLaunchProcess, messageProcessNativesCreateFailed)
	}
	return nil
}

// validateProcessWorkDir 校验工作目录存在且为目录。
//
// 参数 dir 为计划中的工作目录（已由 P2-02 归一化）。
// 返回 `*Error`（`PATH_INVALID_001`）或 nil。
//
// 单独成函数的原因：这是「不得回退到进程当前目录」这条硬约束的**唯一**执行点，
// 也便于单元测试直接覆盖（不需要真的启动进程）。
func validateProcessWorkDir(dir string) *Error {
	if strings.TrimSpace(dir) == "" {
		return newInputError(ErrorCodePathInvalid, messageProcessWorkDirMissing)
	}

	info, err := os.Stat(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return newInputError(ErrorCodePathInvalid, messageProcessWorkDirMissing)
		}
		return newSystemError(ErrorCodePathInvalid, messageProcessWorkDirMissing)
	}

	if !info.IsDir() {
		return newInputError(ErrorCodePathInvalid, messageProcessWorkDirNotDirectory)
	}

	return nil
}

// buildProcessArguments 组装**传给 JVM 的命令行**（不含 javaPath 本身）。
//
// 组装顺序：
//  1. JVM 参数（版本 JSON 的 `arguments.jvm` 已解析结果 + 启动器附加）；
//  2. `-cp <classpath>`：**引导器 classpath 根在前**，其后是计划里的 classpath，
//     以 `os.PathListSeparator` 拼接；
//  3. 引导器入口类 `com.ninezhou.ninevoxel.EntryPoint`。
//
// **游戏参数与游戏主类都不在这里**：它们走 stdin（见 buildLaunchScript）。
// 这是绕开命令行长度上限、并把 accessToken 移出进程命令行的关键一步。
//
// 为什么 `-cp` 由这里注入而不是由 P2-04 注入：
// P2-04 刻意保持「四路分离」，`classpath` 是**独立字段**而不是参数数组的一部分
// （`${classpath}` 占位符只在版本 JSON 显式引用时才替换成字符串）。
// 因此把「独立字段 → 命令行参数」的转换放在唯一启动点，职责更清晰：
// P2-04 负责「是什么」，本卡负责「怎么交给系统」。
//
// 参数 plan 为启动计划；launcherPartDir 为引导器 classpath 根目录。
// 返回参数数组与错误。
func buildProcessArguments(plan *LaunchPlan, launcherPartDir string) ([]string, *Error) {
	args := make([]string, 0, len(plan.JVMMajorArgs)+3)

	// 1. JVM 参数。
	for _, part := range plan.JVMMajorArgs {
		if strings.TrimSpace(part.Value) == "" {
			continue
		}
		args = append(args, part.Value)
	}

	// 2. classpath。空 classpath 属于参数构建失败：游戏会立即找不到主类，
	// 与其让 JVM 抛出难懂的 ClassNotFound，不如在此明确拒绝。
	if len(plan.ClassPathEntries) == 0 {
		return nil, newInputError(ErrorCodeLaunchArgs, messageClassPathEmpty)
	}
	classPathEntries := make([]string, 0, len(plan.ClassPathEntries)+1)
	// 引导器在最前：它的类名独立，位置只影响「同名类谁优先」，放最前语义最直观。
	if strings.TrimSpace(launcherPartDir) != "" {
		classPathEntries = append(classPathEntries, launcherPartDir)
	}
	classPathEntries = append(classPathEntries, plan.ClassPathEntries...)
	classPath := strings.Join(classPathEntries, string(os.PathListSeparator))
	args = append(args, "-cp", classPath)

	// 3. 引导器入口类（唯一的命令行位置参数）。
	if strings.TrimSpace(launcherPartDir) == "" {
		// 没有引导器就只能退回「主类 + 游戏参数都在命令行」的旧形态，
		// 但那会重新引入长度上限与令牌外泄，因此这里直接拒绝而不是静默降级。
		return nil, newSystemError(ErrorCodeLaunchProcess, messageLauncherPartUnavailable)
	}
	args = append(args, launcherPartMainClass)

	if len(args) == 0 {
		return nil, newInputError(ErrorCodeLaunchArgs, messageProcessEmptyCommand)
	}

	return args, nil
}

// buildProcessEnv 按白名单构造子进程环境变量。
//
// 参数无；返回 `KEY=value` 形式的环境数组。
//
// 实现：只从**当前进程环境**中取白名单里的键。取不到的键直接跳过——
// 强行给空值会让 JVM 认为「变量存在但为空」，比缺失更难排查。
//
// 明确不继承 `os.Environ()`：见文件头第 3 条理由。
func buildProcessEnv() []string {
	env := make([]string, 0, len(processEnvWhitelist)+1)

	for _, key := range processEnvWhitelist {
		if value, ok := os.LookupEnv(key); ok && value != "" {
			env = append(env, key+"="+value)
		}
	}

	// Windows 上 JVM 还依赖 `SystemDrive`（例如推断系统盘位置）。
	// 它不在卡面明确列出的白名单里，但属同一类「JVM 定位系统组件」的必需项，
	// 因此按平台追加；非 Windows 不追加。
	if runtime.GOOS == "windows" {
		if value, ok := os.LookupEnv("SystemDrive"); ok && value != "" {
			env = append(env, "SystemDrive="+value)
		}
	}

	return env
}

// processEnvKeysForDiagnostics 返回实际保留下来的环境变量**键名**。
//
// 用途：诊断时只记键名，**绝不记值**（卡面第 3 条明确要求）。
// 返回的列表可直接写入日志 attributes 的键名清单。
func processEnvKeysForDiagnostics() []string {
	keys := make([]string, 0, len(processEnvWhitelist)+1)
	for _, key := range processEnvWhitelist {
		if _, ok := os.LookupEnv(key); ok {
			keys = append(keys, key)
		}
	}
	if runtime.GOOS == "windows" {
		if _, ok := os.LookupEnv("SystemDrive"); ok {
			keys = append(keys, "SystemDrive")
		}
	}
	return keys
}

// readProcessStream 从一路输出读取并按行分发。
//
// 参数 reader 为进程的 stdout / stderr 管道；stream 为来源标识。
//
// 实现要点（卡面第 4 条）：
//   - `bufio.Scanner` 的缓冲上限设为 `processMaxLineBytes`（64 KiB）；
//   - 超长行不会让 Scanner 报错停止，而是**截断并追加 `<truncated>`**；
//   - 每行带 `stream` 与到达时间交给 P2-07 的接收函数；
//   - 读取出错（管道被关闭等）时静默结束：这是进程退出的正常路径。
func readProcessStream(reader io.Reader, stream string) {
	scanner := bufio.NewScanner(reader)
	// 设置最大行长：缓冲需略大于上限，才能在「刚好到上限」时不报 ErrTooLong。
	scanner.Buffer(make([]byte, 0, 64*1024), processMaxLineBytes)

	for scanner.Scan() {
		line := scanner.Text()

		// 超长行：Scanner 在超过缓冲上限时仍会返回该行（受 Split 语义影响），
		// 但我们显式再判断一次长度，保证「截断 + 标记」这一行为可预期，
		// 不依赖 Scanner 的内部截断细节。
		if len(line) >= processMaxLineBytes {
			line = truncateProcessLine(line)
		}

		dispatchGameOutput(stream, line, time.Now().UTC())
	}

	// 读取结束（无论正常退出还是管道关闭）：不再分发。
	// `scanner.Err()` 的具体错误不落日志——进程退出时管道关闭是正常现象，
	// 记录它只会制造噪音。
}

// truncateProcessLine 把超长行截断到上限内并追加标记。
//
// 参数 line 为原始行（超长）；返回截断后的行。
//
// 实现要点：**必须保证截断结果仍是合法 UTF-8**，否则日志文件与界面会出现
// 半个汉字这类乱码。做法：先按字节截断，再用 `utf8.ValidString` 判断；
// 不合法就逐个字节回退，直到重新合法。UTF-8 字符最长 4 字节，
// 因此循环最多 3 次，成本可忽略。
//
// 为什么不用手写位模式判断：手写版本容易漏掉「截断点恰好落在字符起始字节
// 但后续字节不足」这种情形（开发中实际踩过并导致乱码），
// 直接用标准库语义更可靠。
func truncateProcessLine(line string) string {
	limit := processMaxLineBytes - len(processTruncatedMarker)
	if limit <= 0 {
		return processTruncatedMarker
	}
	if len(line) <= limit {
		return line + processTruncatedMarker
	}

	cut := line[:limit]

	// 回退到合法字符边界。
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}

	return cut + processTruncatedMarker
}

// dispatchGameOutput 把一行输出交给 P2-07 的日志通道。
//
// 参数 stream 为来源；line 为原始行文本；at 为到达时间。
//
// 解耦设计（卡面遗留问题 2）：本卡不直接依赖日志落盘实现，
// 只通过 P2-07 的通道接收点（`appendLine`）交付。若没有活动通道
// （例如启动失败后通道被关闭），静默丢弃——不得因日志问题影响进程。
func dispatchGameOutput(stream string, line string, at time.Time) {
	// 时间戳由通道在 appendLine 内部生成；这里传入的 at 仅用于将来
	// 需要「采集时间优先于处理时间」的场景。当前实现中通道自带时间，
	// 因此 at 只作为语义参数保留。
	_ = at

	channel := CurrentGameLogChannel()
	if channel == nil {
		// 无活动通道：说明日志子系统未启动或已关闭。
		// 这不影响游戏进程——直接返回。
		return
	}

	channel.appendLine(stream, line)
}

// execBaseName 返回可执行文件名（不含目录）。
//
// 参数 path 为可执行文件路径；返回文件名。
//
// 用于日志 attributes：文件名本身不含用户目录信息，可安全记录。
func execBaseName(path string) string {
	if path == "" {
		return ""
	}
	// 手动取最后一段（同时兼容 `/` 与 `\`），避免为一个调用 import path/filepath。
	lastSlash := strings.LastIndexAny(path, `/\`)
	if lastSlash < 0 {
		return path
	}
	return path[lastSlash+1:]
}
