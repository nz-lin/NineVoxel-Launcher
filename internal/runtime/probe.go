// 本文件实现 Java 探测：候选枚举、`java -version` 执行、输出解析与结果缓存。
//
// 全包**唯一**的子进程创建点在 `runJavaVersion`：参数以数组形式传入，
// 不经过任何 shell 解析；超时 5 秒；输出上限 64 KiB；执行结束立即收集输出并释放。
//
// 本文件不做需求映射（见 requirement.go），也不下载、不安装、不修改环境变量。
package runtime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	goruntime "runtime"

	"example.com/ninevoxel/launcher/internal/logging"
	"example.com/ninevoxel/launcher/internal/platform"
)

const (
	// probeTimeout 是单次 `java -version` 的执行超时。
	//
	// 与 P2 的校验器取同一值（5 秒）：正常 Java 打印版本在 1 秒内完成，
	// 5 秒既容忍冷启动，又不至于让界面长时间无响应。
	probeTimeout = 5 * time.Second

	// probeVersionArgument 是查询版本使用的唯一参数。
	probeVersionArgument = "-version"

	// probeMaxOutputBytes 是两路输出的合并上限（64 KiB）。
	//
	// 正常输出只有 3–5 行；上限只用于防御异常程序刷屏，
	// 避免把内存与日志拖垮。
	probeMaxOutputBytes = 64 << 10

	// probeCacheTTL 是探测结果的缓存有效期。
	//
	// 5 分钟：足够覆盖一次「打开设置页 → 选 Java → 再打开」的交互，
	// 又不会让用户在装了新 JDK 之后迟迟看不到。
	probeCacheTTL = 5 * time.Minute

	// probeMaxCandidates 是单次探测的候选上限。
	//
	// 候选来源现在只剩「用户显式指定的路径」，正常情况下恒为 0 或 1；
	// 上限保留为防御性约束，避免将来新增来源时失控地启动多个子进程。
	probeMaxCandidates = 32

	// javaExecutableNameWindows 是 Windows 下 Java 可执行文件名。
	javaExecutableNameWindows = "java.exe"

	// javaExecutableNameUnix 是 POSIX 平台下的 Java 可执行文件名。
	javaExecutableNameUnix = "java"

	// displayUnknownVersion 是主版本号未知时的展示占位。
	displayUnknownVersion = "未知版本"

	// displayUnknownArch 是架构未知时的展示占位。
	displayUnknownArch = "未知架构"

	// displayUnknownVendor 是厂商未知时的展示占位。
	displayUnknownVendor = "Java"
)

// 解析 `java -version` 输出使用的正则。
//
// 与 P2 `internal/launch/java.go` 的规则**逐条一致**（见 doc.go 的说明）：
//   - 第一优先级：带引号的 `java|openjdk version "..."`；
//   - 第二优先级：`(build ...)` 行（部分实现只给这一行）；
//   - `1.8.0_x` 按旧式语义取第二段；其余取开头数字段。
var (
	// javaVersionPattern 匹配带引号的版本行。
	javaVersionPattern = regexp.MustCompile(`(?:java|openjdk)\s+version\s+"([^"]+)"`)

	// buildPattern 匹配 `(build 21.0.1+12-LTS-29)` 一类构建行。
	buildPattern = regexp.MustCompile(`(?:Runtime Environment|Server VM|64-Bit Server VM|32-Bit Server VM)\s+\(build\s+([^)]+)\)`)

	// leadingVersionPattern 匹配版本串开头的数字段。
	leadingVersionPattern = regexp.MustCompile(`^([0-9]+)`)

	// legacyVersionPrefixPattern 匹配 `1.8`、`1.7` 这类旧式前缀。
	legacyVersionPrefixPattern = regexp.MustCompile(`^1\.([0-9]+)`)

	// urlLikePattern 用于从版本行里剔除可能的 URL 片段（防御异常输出）。
	urlLikePattern = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://[^\s"'<>|]*`)

	// kindJDKPattern / kindJREPattern 只匹配独立的 `JDK` / `JRE` 词，
	// 避免把 `openjdk` 这类发行包名误当成安装形态。
	kindJDKPattern = regexp.MustCompile(`(?i)\bjdk\b`)
	kindJREPattern = regexp.MustCompile(`(?i)\bjre\b`)
)

// JavaVersionInfo 是从 `java -version` 输出解析出的结构化信息。
//
// 只包含可直接从输出观察到的字段：缺失的信息一律留空或标为 unknown，
// 不做任何推断（架构例外：64 位且无架构关键词时按 x64 处理，见 detectArch）。
type JavaVersionInfo struct {
	// Kind 为 `jdk` / `jre` / `unknown`。
	Kind string
	// MajorVersion 为主版本号；解析失败时为 0。
	MajorVersion int
	// FullVersion 为完整版本串（引号内文本或 build 串）。
	FullVersion string
	// Vendor 为厂商标识。
	Vendor string
	// Arch 为架构标识。
	Arch string
	// Bitness 为位数标识。
	Bitness string
	// FirstLine 为脱敏后的首行摘要，供日志与界面提示使用。
	FirstLine string
}

// ParseJavaVersionOutput 解析合并后的 `java -version` 输出。
//
// 参数 output 为 stdout 与 stderr 合并后的文本（stderr 在前，见 runJavaVersion）。
// 返回 false 表示没有任何一行能解析出版本号——调用方据此返回
// `RUNTIME_PROBE_004`（格式不认识）。
//
// 该函数是纯函数：不访问文件系统、不启动进程，测试可以直接喂字符串。
func ParseJavaVersionOutput(output string) (JavaVersionInfo, bool) {
	lines := strings.Split(output, "\n")

	// 第一优先级：带引号的版本行。
	for _, line := range lines {
		if matches := javaVersionPattern.FindStringSubmatch(line); len(matches) >= 2 {
			if major, ok := majorFromVersionString(matches[1]); ok {
				info := buildJavaVersionInfo(output, line, matches[1], major)
				return info, true
			}
		}
	}

	// 第二优先级：`(build ...)` 行。
	for _, line := range lines {
		if matches := buildPattern.FindStringSubmatch(line); len(matches) >= 2 {
			if major, ok := majorFromVersionString(matches[1]); ok {
				info := buildJavaVersionInfo(output, line, matches[1], major)
				return info, true
			}
		}
	}

	return JavaVersionInfo{}, false
}

// ParseJavaVersionString 把版本串转换为主版本号。
//
// 规则与 P2 一致：
//   - 以 `1.` 开头且第二段 ≥ 2（`1.8.0_401`）→ 取第二段；
//   - 其余取开头第一段数字（`17.0.9` → 17、`21` → 21）。
//
// 用于测试与 `ParseJavaVersionOutput` 内部复用。
func ParseJavaVersionString(version string) (int, bool) {
	return majorFromVersionString(version)
}

// majorFromVersionString 把版本串转换为主版本号（内部实现）。
func majorFromVersionString(version string) (int, bool) {
	trimmed := strings.TrimSpace(version)
	if trimmed == "" {
		return 0, false
	}

	if matches := legacyVersionPrefixPattern.FindStringSubmatch(trimmed); len(matches) >= 2 {
		major, err := strconv.Atoi(matches[1])
		if err != nil {
			return 0, false
		}
		// `1.0` / `1.1` 不是旧式命名，按现代语义走下面的分支。
		if major >= 2 {
			return major, true
		}
	}

	if matches := leadingVersionPattern.FindStringSubmatch(trimmed); len(matches) >= 2 {
		major, err := strconv.Atoi(matches[1])
		if err != nil {
			return 0, false
		}
		return major, true
	}

	return 0, false
}

// buildJavaVersionInfo 汇总单行命中后的完整解析结果。
func buildJavaVersionInfo(output, line, version string, major int) JavaVersionInfo {
	info := JavaVersionInfo{
		Kind:         detectKind(output),
		MajorVersion: major,
		FullVersion:  strings.TrimSpace(version),
		Vendor:       detectVendor(output),
	}
	info.Arch, info.Bitness = detectArch(output)
	info.FirstLine = sanitizeVersionLine(line)
	return info
}

// detectKind 判定 JDK / JRE。
//
// 只在输出明确出现 `JDK` 字样时判定为 jdk；出现 `JRE` 字样时判定为 jre；
// 其余一律 unknown。不从版本行推断——多数发行版的 JRE 也打印
// `OpenJDK Runtime Environment`，据此推断会把 JRE 误判成 JDK。
func detectKind(output string) string {
	// 用词边界而不是子串匹配：`openjdk version "21.0.1"` 里的 `openjdk`
	// 并不代表这是一个 JDK 发行包（OpenJDK 也提供纯 JRE 构建），
	// 只有独立的 `JDK` / `JRE` 字样才算形态证据。
	if kindJDKPattern.MatchString(output) {
		return KindJDK
	}
	if kindJREPattern.MatchString(output) {
		return KindJRE
	}
	return KindUnknown
}

// detectVendor 按输出中的厂商关键词判定发行商。
//
// 顺序即优先级：Adoptium 的构建同时含 `OpenJDK` 字样，若先判 OpenJDK
// 就会把 Temurin 归到「其他」。未知厂商返回 `unknown`。
func detectVendor(output string) string {
	lower := strings.ToLower(output)
	switch {
	case strings.Contains(lower, "adoptium"), strings.Contains(lower, "temurin"):
		return VendorAdoptium
	case strings.Contains(lower, "microsoft"):
		return VendorMicrosoft
	case strings.Contains(lower, "zulu"), strings.Contains(lower, "azul"):
		return VendorZulu
	case strings.Contains(lower, "oracle"), strings.Contains(lower, "java(tm)"):
		return VendorOracle
	case strings.Contains(lower, "openjdk"):
		return VendorOpenJDK
	default:
		return VendorUnknown
	}
}

// detectArch 从输出里判定架构与位数。
//
// 位数优先看 `64-Bit` / `32-Bit` 字样；架构关键词看 `aarch64` / `arm64`、
// `x86_64` / `amd64`、`i386` / `i686`。
//
// 例外说明（卡面遗留问题 6）：ARM64 的 JVM 也打印 `64-Bit Server VM`，
// 没有架构关键词时无法区分 x64 与 aarch64，此时按 x64 处理并在报告里注明；
// 一旦输出出现 `aarch64`/`arm64` 就以关键词为准。
func detectArch(output string) (arch string, bitness string) {
	lower := strings.ToLower(output)

	if strings.Contains(lower, "64-bit") || strings.Contains(lower, "64 bit") {
		bitness = Bitness64
	} else if strings.Contains(lower, "32-bit") || strings.Contains(lower, "32 bit") {
		bitness = Bitness32
	} else {
		bitness = BitnessUnknown
	}

	switch {
	case strings.Contains(lower, "aarch64"), strings.Contains(lower, "arm64"):
		arch = ArchAarch64
		if bitness == BitnessUnknown {
			bitness = Bitness64
		}
	case strings.Contains(lower, "x86_64"), strings.Contains(lower, "amd64"):
		arch = ArchX64
	case strings.Contains(lower, "i386"), strings.Contains(lower, "i686"):
		arch = ArchX86
		if bitness == BitnessUnknown {
			bitness = Bitness32
		}
	case bitness == Bitness64:
		// 只声明 64 位而没有架构关键词：按 x86 家族 64 位处理（见函数注释）。
		arch = ArchX64
	case bitness == Bitness32:
		arch = ArchX86
	default:
		arch = ArchUnknown
	}

	return arch, bitness
}

// sanitizeVersionLine 把版本行整理成可安全展示的一行摘要。
//
// 去掉 URL 片段与多余空白，并限制长度：异常输出（例如错误信息里带路径或链接）
// 不应该原样进入界面与日志。
func sanitizeVersionLine(line string) string {
	trimmed := strings.TrimSpace(line)
	trimmed = urlLikePattern.ReplaceAllString(trimmed, " ")
	trimmed = strings.Join(strings.Fields(trimmed), " ")
	if runes := []rune(trimmed); len(runes) > 200 {
		return string(runes[:200])
	}
	return trimmed
}

// javaCandidate 是一个待探测的候选路径。
type javaCandidate struct {
	// path 为绝对路径；只在本包内部使用，不进入 DTO。
	path string
	// origin 为来源标识（preferred/java_home/path/scan），只用于日志统计。
	origin string
}

// preferredJavaPathProvider 返回启动配置里显式指定的 Java 路径。
//
// 默认 nil：本卡不接 launch 配置（`internal/launch` 禁改），
// 由后续卡在装配层调用 `SetPreferredJavaPathProvider` 接入，属预留接口而非死代码。
var preferredJavaPathProvider func() string

// SetPreferredJavaPathProvider 注入「用户显式指定的 Java 路径」来源。
//
// 传 nil 可清除注入。探测时会把它作为优先级最高的候选。
func SetPreferredJavaPathProvider(provider func() string) {
	preferredJavaPathProvider = provider
}

// nowFunc 返回当前时间；测试可替换。
var nowFunc = time.Now

// enumerateJavaCandidates 返回候选 Java 路径：只有「用户显式指定的路径」一个来源。
//
// 本启动器只管理自己下载安装的运行时。外部 JDK 不自动发现：
//   - 不读 `JAVA_HOME`；
//   - 不扫 `PATH`；
//   - 不遍历 `%ProgramFiles%\Java`、`/usr/lib/jvm` 这类常见安装根。
//
// 这样界面上的 Java 列表只反映「启动器装了什么」与「用户明确指定了什么」，
// 不会因为机器上碰巧装了别的 JDK 就多出几项、也不会让启动参数悄悄指向
// 一个用户没有选择过的运行时。用户要复用外部 JDK 时，必须在设置里显式填路径。
//
// 未注入 preferredJavaPathProvider 时返回空列表——「没有可用 Java」是正常状态，
// 由 runProbeSession 转成引导性告警，而不是伪造一个默认值。
//
// 不修改任何环境变量；本函数只读取显式配置，连环境变量都不读。
func enumerateJavaCandidates() []javaCandidate {
	candidates := make([]javaCandidate, 0, 1)
	seen := make(map[string]bool, 1)

	add := func(path, origin string) {
		if path == "" || len(candidates) >= probeMaxCandidates {
			return
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			return
		}
		clean := filepath.Clean(abs)
		// Windows 路径不区分大小写：用小写做去重键，避免同一文件出现两次。
		key := strings.ToLower(clean)
		if seen[key] {
			return
		}
		seen[key] = true
		candidates = append(candidates, javaCandidate{path: clean, origin: origin})
	}

	if preferredJavaPathProvider != nil {
		add(preferredJavaPathProvider(), "preferred")
	}

	return candidates
}

// javaExecutableName 返回当前平台的 Java 可执行文件名。
func javaExecutableName() string {
	if goruntime.GOOS == "windows" {
		return javaExecutableNameWindows
	}
	return javaExecutableNameUnix
}

// probeJavaExecutable 对单个候选做完整探测。
//
// 步骤：存在性 → 平台可执行形态 → 执行 `java -version` → 解析输出。
// 任何一步失败都返回带稳定错误码的 *AppError，由调用方转成告警。
func probeJavaExecutable(path string) (JavaVersionInfo, *AppError) {
	info, statErr := os.Stat(path)
	if statErr != nil {
		if errors.Is(statErr, fs.ErrPermission) {
			return JavaVersionInfo{}, newInputError(ErrorCodeProbePermission, messageProbePermission)
		}
		return JavaVersionInfo{}, newInputError(ErrorCodeProbeNotJava, messageProbeNotFound)
	}
	if info.IsDir() {
		return JavaVersionInfo{}, newInputError(ErrorCodeProbeNotJava, messageProbeNotFound)
	}

	if appErr := checkExecutableForm(path, info); appErr != nil {
		return JavaVersionInfo{}, appErr
	}

	output, appErr := runJavaVersion(path)
	if appErr != nil {
		return JavaVersionInfo{}, appErr
	}

	parsed, ok := ParseJavaVersionOutput(output)
	if !ok {
		return JavaVersionInfo{}, newExternalError(ErrorCodeProbeFormat, messageProbeFormat, false)
	}
	return parsed, nil
}

// checkExecutableForm 检查文件在当前平台上是否可能作为程序执行。
//
// Windows：扩展名必须是 `.exe` 或没有扩展名（后者交给执行阶段判定）；
// POSIX：必须有可执行位。检查失败不返回 nil，也不假装成功。
func checkExecutableForm(path string, info fs.FileInfo) *AppError {
	if goruntime.GOOS == "windows" {
		ext := filepath.Ext(path)
		if ext != "" && !strings.EqualFold(ext, ".exe") {
			return newInputError(ErrorCodeProbeNotJava, messageProbeWindowsExtension)
		}
		return nil
	}
	if info.Mode()&0o111 == 0 {
		return newInputError(ErrorCodeProbePermission, messageProbePermission)
	}
	return nil
}

// cappedBuffer 是带容量上限的字节缓冲。
//
// 超过上限后继续返回成功（丢弃多余内容），避免让子进程因写错误而阻塞。
type cappedBuffer struct {
	buffer    bytes.Buffer
	limit     int
	truncated bool
}

// Write 实现 io.Writer。
func (b *cappedBuffer) Write(p []byte) (int, error) {
	remaining := b.limit - b.buffer.Len()
	if remaining <= 0 {
		b.truncated = true
		return len(p), nil
	}
	if len(p) > remaining {
		b.buffer.Write(p[:remaining])
		b.truncated = true
		return len(p), nil
	}
	return b.buffer.Write(p)
}

// String 返回已写入的内容。
func (b *cappedBuffer) String() string {
	return b.buffer.String()
}

// runJavaVersion 执行一次 `java -version` 并返回合并后的输出。
//
// 这是本包唯一的子进程创建点：
//   - 路径作为独立参数传入，不经过 shell；
//   - 超时 5 秒，超时后由 context 杀进程；
//   - 两路输出各限额 64 KiB，合并后再截断；
//   - 不设置 Stdin，避免进程等待输入。
//
// 错误分类见 classifyExecFailure：权限 / 不是 Java / 超时 / 其他。
func runJavaVersion(path string) (string, *AppError) {
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()

	// 参数数组，禁止 shell 拼接（AGENTS.md 9.1）。
	command := exec.CommandContext(ctx, path, probeVersionArgument)
	// 隐藏控制台窗口：否则 Windows 会为 `java.exe -version` 闪出一个黑框。
	platform.HideWindow(command)
	stdoutBuffer := &cappedBuffer{limit: probeMaxOutputBytes}
	stderrBuffer := &cappedBuffer{limit: probeMaxOutputBytes}
	command.Stdout = stdoutBuffer
	command.Stderr = stderrBuffer
	command.Stdin = nil

	runErr := command.Run()

	// 超时优先判定：超时后 Run 返回的错误形态不可靠，必须看 ctx.Err()。
	if ctx.Err() == context.DeadlineExceeded {
		return "", newExternalError(ErrorCodeProbeTimeout, messageProbeTimeout, true)
	}

	// `java -version` 在部分发行版写 stdout、多数写 stderr；
	// 合并时把 stdout 放前面，保证「带引号版本行」仍是首行候选。
	combined := strings.TrimSpace(strings.TrimSpace(stdoutBuffer.String()) + "\n" + strings.TrimSpace(stderrBuffer.String()))
	if len(combined) > probeMaxOutputBytes {
		combined = combined[:probeMaxOutputBytes]
	}
	combined = strings.ToValidUTF8(combined, "\uFFFD")

	if appErr := classifyExecFailure(runErr, combined); appErr != nil {
		return "", appErr
	}

	return combined, nil
}

// classifyExecFailure 把执行错误归类为稳定的探测错误码。
//
// 规则与 P2 一致：
//   - 进程返回非零退出码但输出可解析时**不算失败**（部分发行版会打印告警后
//     仍以非零码退出），由调用方继续解析；
//   - 完全没有输出且进程报错 → 不是 Java；
//   - 权限错误 → 权限拒绝；
//   - 其余启动失败（不是可执行格式）→ 不是 Java。
func classifyExecFailure(runErr error, output string) *AppError {
	if runErr == nil {
		return nil
	}

	if errors.Is(runErr, fs.ErrPermission) {
		return newInputError(ErrorCodeProbePermission, messageProbePermission)
	}
	if errors.Is(runErr, exec.ErrNotFound) {
		return newInputError(ErrorCodeProbeNotJava, messageProbeNotFound)
	}

	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		if strings.TrimSpace(output) != "" {
			// 有输出就交给解析器判断；解析失败会得到 RUNTIME_PROBE_004。
			return nil
		}
		return newExternalError(ErrorCodeProbeNotJava, messageProbeOutputEmpty, false)
	}

	return newExternalError(ErrorCodeProbeNotJava, messageProbeLaunchFailed, false)
}

// runtimeSummaryFromProbe 把解析结果整理为面向前端的摘要。
//
// 参数 path 只用于派生 runtimeID 与日志，不进入 DTO。
func runtimeSummaryFromProbe(path string, info JavaVersionInfo, verifiedAtMs int64) RuntimeSummary {
	compatible, reason := evaluateCompatibility(info)
	return RuntimeSummary{
		RuntimeID:        runtimeIDFor(path, info),
		DisplayName:      displayNameFor(info),
		Kind:             info.Kind,
		Vendor:           info.Vendor,
		MajorVersion:     info.MajorVersion,
		FullVersion:      info.FullVersion,
		Arch:             info.Arch,
		Bitness:          info.Bitness,
		Source:           SourceLocal,
		Compatible:       compatible,
		CompatibleReason: reason,
		Selected:         false,
		VerifiedAtMs:     verifiedAtMs,
	}
}

// runtimeIDFor 派生稳定的 runtimeId（不可逆，不泄露路径）。
//
// 由「归一化路径 + 完整版本 + 架构」的 SHA-256 前缀组成：
// 同一份 Java 在路径不变时 id 稳定；路径变化会导致 id 变化（卡面遗留问题 3，
// 需在 P3-09 对齐）。
func runtimeIDFor(path string, info JavaVersionInfo) string {
	key := strings.ToLower(filepath.Clean(path)) + "|" + info.FullVersion + "|" + info.Arch
	sum := sha256.Sum256([]byte(key))
	return "local-" + hex.EncodeToString(sum[:8])
}

// displayNameFor 构造脱敏展示名，例如 `Adoptium 21 (x64)`。
func displayNameFor(info JavaVersionInfo) string {
	vendor := vendorLabel(info.Vendor)
	version := strconv.Itoa(info.MajorVersion)
	if info.MajorVersion <= 0 {
		version = displayUnknownVersion
	}
	arch := info.Arch
	if arch == "" || arch == ArchUnknown {
		arch = displayUnknownArch
	}
	return fmt.Sprintf("%s %s (%s)", vendor, version, arch)
}

// vendorLabel 把厂商标识转成展示名。
func vendorLabel(vendor string) string {
	switch vendor {
	case VendorAdoptium:
		return "Adoptium"
	case VendorOracle:
		return "Oracle"
	case VendorMicrosoft:
		return "Microsoft"
	case VendorZulu:
		return "Zulu"
	case VendorOpenJDK:
		return "OpenJDK"
	case VendorOther:
		return "Java"
	default:
		return displayUnknownVendor
	}
}

// evaluateCompatibility 判断该 Java 能否在当前进程环境里使用。
//
// 只比对位数与架构，**不**猜任何版本需求（卡面第 5 条）：
//   - 32 位 Java 无法满足 64 位游戏要求 → 不兼容；
//   - 架构与当前进程不一致（例如在 x64 机器上探测到 aarch64 Java）→ 不兼容；
//   - 架构未知 → 不兼容并给出原因，让用户自己确认。
func evaluateCompatibility(info JavaVersionInfo) (bool, string) {
	if info.Arch == "" || info.Arch == ArchUnknown {
		return false, "无法确认该 Java 的架构，请手动确认后再使用。"
	}
	if info.Bitness == Bitness32 && strconv.IntSize == 64 {
		return false, "32 位 Java 无法在 64 位游戏要求下使用。"
	}
	current := currentArch()
	if info.Arch != current {
		return false, fmt.Sprintf("该 Java 为 %s 架构，与当前进程的 %s 不一致。", info.Arch, current)
	}
	return true, ""
}

// currentArch 返回当前进程的架构标识。
func currentArch() string {
	switch goruntime.GOARCH {
	case "amd64":
		return ArchX64
	case "386":
		return ArchX86
	case "arm64":
		return ArchAarch64
	default:
		return ArchUnknown
	}
}

// currentPlatformName 返回当前平台标识（windows/darwin/linux）。
func currentPlatformName() string {
	switch goruntime.GOOS {
	case "windows":
		return "windows"
	case "darwin":
		return "darwin"
	default:
		return "linux"
	}
}

// currentBitness 返回当前进程位数的字符串标识。
func currentBitness() string {
	if strconv.IntSize == 64 {
		return Bitness64
	}
	return Bitness32
}

// probeCache 保存最近一次探测结果（进程内缓存，5 分钟有效）。
var probeCache struct {
	mu       sync.Mutex
	result   RuntimeProbeResult
	probedAt time.Time
	valid    bool
}

// DetectRuntimes 探测本机 Java，命中未过期缓存时直接返回缓存副本。
//
// 探测串行执行：每个候选最多 5 秒，候选上限 32 个；失败候选以告警返回，
// 不影响其余候选。没有任何候选时返回空列表 + `RUNTIME_NOT_FOUND_001` 告警，
// 而不是返回错误——「本机没装 Java」是正常状态，界面需要它来引导用户。
func DetectRuntimes(req DetectRuntimeRequest) (RuntimeProbeResult, *AppError) {
	probeCache.mu.Lock()
	defer probeCache.mu.Unlock()

	now := nowFunc()
	if !req.Refresh && probeCache.valid && now.Sub(probeCache.probedAt) < probeCacheTTL {
		cached := cloneProbeResult(probeCache.result)
		cached.CacheHit = true
		return cached, nil
	}

	started := now
	result := runProbeSession(now)
	result.CacheHit = false

	probeCache.result = cloneProbeResult(result)
	probeCache.probedAt = now
	probeCache.valid = true

	runtimeLog().Info("完成 Java 探测", logging.Fields{
		Event: "runtime.probe.completed",
		Attributes: map[string]string{
			"count":        strconv.Itoa(len(result.Items)),
			"warningCount": strconv.Itoa(len(result.Warnings)),
			"durationMs":   strconv.FormatInt(now.Sub(started).Milliseconds(), 10),
		},
	})

	return result, nil
}

// runProbeSession 执行一次完整探测（不读缓存）。
func runProbeSession(now time.Time) RuntimeProbeResult {
	candidates := enumerateJavaCandidates()
	items := make([]RuntimeSummary, 0, len(candidates))
	warnings := make([]RuntimeProbeWarning, 0)

	for _, candidate := range candidates {
		info, appErr := probeJavaExecutable(candidate.path)
		if appErr != nil {
			warnings = append(warnings, RuntimeProbeWarning{Code: appErr.Code, Message: appErr.Message})
			// 只记来源与错误码，不记路径（路径含用户名）。
			runtimeLog().Warn("候选 Java 探测失败", logging.Fields{
				Event:     "runtime.probe.candidate_failed",
				ErrorCode: appErr.Code,
				Attributes: map[string]string{
					"origin": candidate.origin,
				},
			})
			continue
		}
		items = append(items, runtimeSummaryFromProbe(candidate.path, info, now.UnixMilli()))
	}

	if len(candidates) == 0 {
		warnings = append(warnings, RuntimeProbeWarning{
			Code:    ErrorCodeRuntimeNotFound,
			Message: "启动器尚未安装任何运行时，也没有在设置中指定 Java 路径。请先下载运行时，或在设置里显式指定外部 Java。",
		})
	}

	sortRuntimeSummaries(items)

	return RuntimeProbeResult{
		Items:      items,
		ProbedAtMs: now.UnixMilli(),
		CacheHit:   false,
		Warnings:   warnings,
	}
}

// sortRuntimeSummaries 按主版本降序、同版本按展示名排序。
//
// 排序稳定是前端可预期的前提：同一台机器两次探测的顺序必须一致。
func sortRuntimeSummaries(items []RuntimeSummary) {
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].MajorVersion != items[j].MajorVersion {
			return items[i].MajorVersion > items[j].MajorVersion
		}
		return items[i].DisplayName < items[j].DisplayName
	})
}

// ListRuntimes 返回本机 Runtime 列表（复用探测缓存）。
//
// P3-08 只返回「探测到的本机 Java」：已安装 Runtime 由 P3-09 追加，
// 选中状态也为 P3-09 的职责，因此 `SelectedRuntimeID` 与 `Requirement` 留空。
func ListRuntimes() (RuntimeList, *AppError) {
	result, appErr := DetectRuntimes(DetectRuntimeRequest{})
	if appErr != nil {
		return RuntimeList{}, appErr
	}
	items := result.Items
	if items == nil {
		items = []RuntimeSummary{}
	}
	return RuntimeList{Items: items}, nil
}

// cloneProbeResult 复制探测结果，避免调用方修改缓存内容。
func cloneProbeResult(source RuntimeProbeResult) RuntimeProbeResult {
	cloned := source
	if source.Items != nil {
		cloned.Items = make([]RuntimeSummary, len(source.Items))
		copy(cloned.Items, source.Items)
	}
	if source.Warnings != nil {
		cloned.Warnings = make([]RuntimeProbeWarning, len(source.Warnings))
		copy(cloned.Warnings, source.Warnings)
	}
	return cloned
}

// runtimeLogger 是惰性创建的组件日志器。
//
// 组件名固定为 `runtime`：日志检索面板按组件过滤，名字漂移会让过滤失效。
var (
	runtimeLoggerOnce sync.Once
	runtimeLogger     *logging.Logger
)

// runtimeLog 返回带组件标签的日志器。
func runtimeLog() *logging.Logger {
	runtimeLoggerOnce.Do(func() {
		runtimeLogger = logging.Default.WithComponent("runtime")
	})
	return runtimeLogger
}
