// 本文件实现 Java 可执行文件的路径归一化、三层校验与版本输出解析。
//
// 三层校验顺序**固定、失败即停**（P2-01 卡面第 2 条）：
//  1. 存在性：`os.Stat` 成功且为普通文件；
//  2. 可执行性：Windows 检查 `.exe`（无扩展名只给警告，不直接拒绝）；
//  3. 版本输出：`exec.Command(javaPath, "-version")`，参数数组、5 秒超时、
//     同时捕获 stdout 与 stderr（旧版 JDK 把版本写到 stderr）。
//
// 严禁 Shell 拼接：本文件只出现 `exec.Command(javaPath, "-version")` 这一种进程创建方式，
// 不存在 `cmd /c`、`sh -c`，也不存在把路径拼进命令行字符串的代码路径。
//
// 零系统副作用：不修改 PATH / JAVA_HOME / 注册表 / 环境变量；
// 不写任何文件、不创建临时目录、不 `chmod`、不复制文件、不建包装脚本。
package launch

import (
	"bytes"
	"context"
	"errors"

	"example.com/ninevoxel/launcher/internal/platform"

	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// 校验过程中的常量。
const (
	// javaVersionTimeout 是 `java -version` 的执行超时。
	//
	// 取 5 秒：正常 Java 启动并打印版本在 1 秒内完成；
	// 5 秒既容忍冷启动与磁盘慢的机器，又不会让用户界面长时间卡住。
	javaVersionTimeout = 5 * time.Second

	// javaVersionArgument 是查询版本使用的唯一参数。
	//
	// 用参数数组而不是拼字符串：路径含空格或特殊字符时也不会被解释成多个参数。
	javaVersionArgument = "-version"

	// javaExecutableNameWindows 是 Windows 下 Java 可执行文件的名称。
	javaExecutableNameWindows = "java.exe"

	// javaExecutableNameUnix 是其他平台下 Java 可执行文件的名称（无扩展名）。
	javaExecutableNameUnix = "java"

	// 显示分隔符常量。
	//
	// displayEllipsis 是决策记录 D-8 示例中 `类别\…\末段` 的分隔符：
	// 该示例在 Windows 上是 `Java 安装目录\…\jdk-21\bin\java.exe`，
	// 即「类别」+ `\` + `…` + `\` + 尾部三段。
	// 所以省略标记本身只承载 `…`，两侧分隔符由拼接逻辑负责（随平台变化）。
	displayEllipsis = "…"

	// displayCategoryJava 是 Java 安装目录的展示类别名。
	displayCategoryJava = "Java 安装目录"

	// displaySeparatorUnix 是 POSIX 平台的路径分隔符展示形态。
	displaySeparatorUnix = "/"

	// maxSummaryRunes 是版本输出首行的最大字符数。
	//
	// 取 200：单行版本信息通常 40–80 字符，200 足够容纳异常输出，
	// 又能避免把超长内容带进界面与日志。
	maxSummaryRunes = 200

	// noExtensionWarning 是「所选文件没有扩展名」时的提示文案。
	noExtensionWarning = "所选文件没有扩展名，已尝试当作 Java 程序校验。"
)

// javaVersionPattern 匹配 `java -version` 输出中的带引号版本号。
//
// 覆盖形态：`java version "1.8.0_401"`、`openjdk version "17.0.9" 2023-10-17`、
// `openjdk version "21" 2023-09-19`、`java version "21.0.1" 2023-10-17 LTS`。
// 组 1 为引号内的版本号文本。
var javaVersionPattern = regexp.MustCompile(`(?:java|openjdk)\s+version\s+"([^"]+)"`)

// buildPattern 匹配 `OpenJDK Runtime Environment (build 21.0.1+12-LTS-29)` 一类构建行。
//
// 组 1 为 build 后的版本串（可能带 `+` 与后缀）。该格式作为**补充**来源：
// 当带引号版本行缺失时用它提取版本号；两者都存在时以带引号行优先。
var buildPattern = regexp.MustCompile(`(?:Runtime Environment|Server VM|64-Bit Server VM)\s+\(build\s+([^)]+)\)`)

// leadingVersionPattern 匹配版本串开头的数字段（`17.0.9` → `17`、`21` → `21`）。
var leadingVersionPattern = regexp.MustCompile(`^([0-9]+)`)

// legacyVersionPrefixPattern 匹配 `1.8.0_x` 这类旧式版本串的 `1.` 前缀。
//
// 用于把 `1.8.0_401` 的主版本识别为 `8` 而不是 `1`。
var legacyVersionPrefixPattern = regexp.MustCompile(`^1\.([0-9]+)`)

// pathLikePattern 匹配类绝对路径片段，用于版本行脱敏。
//
// 覆盖 Windows 盘符路径与 UNC 路径；版本行本身通常不含路径，
// 但异常输出（例如错误信息里带路径）必须被脱敏。
var pathLikePattern = regexp.MustCompile(`[A-Za-z]:[\\/][^\s"'<>|]*|\\\\[^\s"'<>|]*`)

// normalizeJavaPath 归一化用户提供的 Java 路径。
//
// 规则（P2-01 卡面第 1 条）：
//  1. 空值 → `PATH_INVALID_001`；
//  2. 相对路径 → `PATH_INVALID_001`（只接受绝对路径，避免依赖 cwd）；
//  3. `filepath.Clean` + `filepath.Abs` 归一化；失败 → `PATH_INVALID_001`；
//  4. 归一化结果指向已存在的目录 → `PATH_INVALID_001`（目录不是可执行文件）。
//
// 参数 rawPath 为用户输入或对话框返回的路径。
// 返回归一化后的绝对路径与错误；错误为 *Error，带稳定错误码。
// 注意：返回的绝对路径**只供后续校验使用**，不得进入 DTO 的展示字段或日志。
func normalizeJavaPath(rawPath string) (string, *Error) {
	trimmed := strings.TrimSpace(rawPath)
	if trimmed == "" {
		return "", newInputError(ErrorCodePathInvalid, messagePathEmpty)
	}

	// 判断是否已是绝对路径。Windows 下 `C:\...` 与 `\\server\share\...` 都算绝对。
	if !filepath.IsAbs(trimmed) {
		// `filepath.IsAbs` 在 Windows 上对 `C:/x` 返回 true，但对 `/x` 返回 false；
		// 后者在 Windows 上是「相对当前盘的根」，语义不确定，因此按相对路径拒绝。
		return "", newInputError(ErrorCodePathInvalid, messagePathRelative)
	}

	cleaned := filepath.Clean(trimmed)

	// filepath.Abs 对已是绝对路径的输入基本是空操作，但会统一分隔符形式；
	// 出错说明路径格式异常（例如含非法字符），属于路径非法。
	absPath, err := filepath.Abs(cleaned)
	if err != nil {
		return "", newInputError(ErrorCodePathInvalid, messagePathResolveFailed)
	}

	// 指向目录的情况**在此处提前拦截**：目录必然「存在」，若不在归一化阶段拒绝，
	// 就会落到存在性检查里被误判成 JAVA_NOT_FOUND_001，语义不符。
	//
	// 这里的 Stat 错误被忽略：不存在由下一层「存在性校验」给出更准确的错误码。
	if info, statErr := os.Stat(absPath); statErr == nil && info.IsDir() {
		return "", newInputError(ErrorCodePathInvalid, messagePathIsDirectory)
	}

	return absPath, nil
}

// checkJavaExists 是第一层校验：文件存在且为普通文件。
//
// 参数 absPath 为已归一化的绝对路径。
// 失败返回 *Error（错误码 `JAVA_NOT_FOUND_001`）：
//   - 不存在（`os.ErrNotExist`）；
//   - 不是普通文件（目录已在归一化阶段拦截，这里兜底符号链接、设备文件等）；
//   - 权限不足（`os.ErrPermission`）。
func checkJavaExists(absPath string) *Error {
	info, err := os.Stat(absPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return newInputError(ErrorCodeJavaNotFound, messageJavaNotExist)
		}
		if errors.Is(err, os.ErrPermission) {
			return newSystemError(ErrorCodeJavaNotFound, messageJavaNoPermission)
		}
		// 其他 Stat 失败（例如路径含非法字符）按「未找到」处理：
		// 对用户而言都是「这个路径用不了」，且不需要暴露底层错误细节。
		return newInputError(ErrorCodeJavaNotFound, messageJavaNotExist)
	}

	if !info.Mode().IsRegular() {
		return newInputError(ErrorCodeJavaNotFound, messageJavaNotRegular)
	}

	return nil
}

// checkJavaExecutable 是第二层校验：判定文件是否可执行。
//
// Windows 规则：扩展名必须是 `.exe`；**无扩展名**只加一条警告不直接拒绝
// （卡面第 2.2 条明确要求，因为少数解压后的 java 可能没有扩展名）。
// 其他平台：检查执行位（`mode&0111`）。
//
// 不得 `chmod`、不得复制文件、不得创建包装脚本——本函数只做判定。
//
// 参数 absPath 为已归一化路径；返回警告列表（可能为空）与错误。
func checkJavaExecutable(absPath string) ([]string, *Error) {
	if runtime.GOOS == "windows" {
		ext := strings.ToLower(filepath.Ext(absPath))
		if ext == "" {
			// 无扩展名：给警告不拒绝。用户仍可能选到有效的 Java，
			// 由第三层 `java -version` 给出最终结论。
			return []string{noExtensionWarning}, nil
		}
		if ext != ".exe" {
			return nil, newInputError(ErrorCodeJavaNotFound, messageJavaNotExecutable)
		}
		return nil, nil
	}

	// 非 Windows：检查执行位。只为判定，不做任何权限修改。
	info, err := os.Stat(absPath)
	if err != nil {
		// 第一层已通过，这里的失败只可能是并发删除或竞态；按「未找到」处理。
		return nil, newInputError(ErrorCodeJavaNotFound, messageJavaNotExist)
	}
	if info.Mode().Perm()&0o111 == 0 {
		return nil, newInputError(ErrorCodeJavaNotFound, messageJavaNotExecutable)
	}
	return nil, nil
}

// javaVersionProbe 是 `java -version` 的探针结果。
type javaVersionProbe struct {
	// output 为 stdout 与 stderr 合并后的完整输出（已去除尾部空白）。
	output string

	// firstLine 为合并输出的第一行（未脱敏）。
	firstLine string
}

// runJavaVersion 是第三层校验：执行 `java -version` 并收集输出。
//
// 实现要点（卡面第 2.3 条）：
//   - 使用**参数数组** `exec.Command(javaPath, javaVersionArgument)`，绝不拼接命令行；
//   - 5 秒超时，超时后通过 `context` 终止进程；
//   - **同时捕获 stdout 与 stderr**：旧版 JDK（1.8 及更早）把版本行写到 stderr，
//     只读 stdout 会得到空输出；
//   - 退出码非 0 但输出可解析时按「可解析即通过 + 记录警告」处理（部分厂商实现如此）。
//
// 零副作用：不设置 `cmd.Dir`（`-version` 不依赖 cwd），不修改环境变量，
// 不使用 shell（`exec.Command` 直接创建进程）。
func runJavaVersion(absPath string) (*javaVersionProbe, []string, *Error) {
	ctx, cancel := context.WithTimeout(context.Background(), javaVersionTimeout)
	defer cancel()

	// 唯一的进程创建点：路径作为独立参数传入，不经过任何 shell 解析。
	command := exec.CommandContext(ctx, absPath, javaVersionArgument)
	// 隐藏控制台窗口：否则 Windows 会为 `java.exe -version` 闪出一个黑框。
	platform.HideWindow(command)

	// 两个缓冲分别收集两路输出，随后按「stdout 在前、stderr 在后」合并。
	var stdoutBuffer bytes.Buffer
	var stderrBuffer bytes.Buffer
	command.Stdout = &stdoutBuffer
	command.Stderr = &stderrBuffer
	// 显式清空环境变量以外的副作用：不设置 Stdin，避免进程等待输入。
	command.Stdin = nil

	runErr := command.Run()

	// 超时优先判定：context 超时后 Run 返回的信号错误不可靠，
	// 必须用 ctx.Err() 才能区分「超时」与「进程自身失败」。
	if ctx.Err() == context.DeadlineExceeded {
		return nil, nil, newSystemError(ErrorCodeJavaVersion, messageVersionTimeout)
	}

	combined := strings.TrimSpace(stdoutBuffer.String() + "\n" + stderrBuffer.String())
	combined = strings.TrimSpace(combined)

	if combined == "" {
		// 没有任何输出：可能是「不是 Java」，也可能是执行失败。
		// 若 Run 也报错，说明进程都没起来；否则说明程序静默退出。
		if runErr != nil {
			return nil, nil, newSystemError(ErrorCodeJavaVersion, messageVersionRunFailed)
		}
		return nil, nil, newExternalError(ErrorCodeJavaVersion, messageVersionOutputEmpty)
	}

	probe := &javaVersionProbe{output: combined}
	if lines := strings.Split(combined, "\n"); len(lines) > 0 {
		probe.firstLine = strings.TrimSpace(lines[0])
	}

	// 退出码非 0 但输出可解析：按「可解析即通过」处理，只加一条警告。
	var warnings []string
	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			warnings = append(warnings, "该 Java 执行版本检查时返回了非零退出码，但版本输出可解析，已按可用处理。")
		} else {
			// 非 ExitError 说明进程根本没起来（例如文件不是可执行格式）。
			return nil, nil, newSystemError(ErrorCodeJavaVersion, messageVersionRunFailed)
		}
	}

	return probe, warnings, nil
}

// parseJavaMajorVersion 从版本输出中解析主版本号。
//
// 覆盖的 5 种格式（卡面第 3 条）：
//  1. `java version "1.8.0_401"` → 8（旧式 `1.x` 前缀取第二段）；
//  2. `openjdk version "17.0.9" 2023-10-17` → 17；
//  3. `openjdk version "21" 2023-09-19` → 21；
//  4. `java version "21.0.1" 2023-10-17 LTS` → 21；
//  5. `OpenJDK Runtime Environment (build 21.0.1+12-LTS-29)` → 21（补充来源）。
//
// 解析优先级：带引号的 `java|openjdk version "..."` 行 → `(build ...)` 行。
// 两者都缺失或数字段无法提取时返回 0 与 false，由调用方转成 `JAVA_VERSION_001`。
//
// 参数 output 为合并后的完整输出。
func parseJavaMajorVersion(output string) (int, bool) {
	// 优先按行处理：同一份输出可能同时含多个候选行（Runtime Environment 行、
	// VM 行），逐行扫描并按优先级取第一个能解析出数字的。
	lines := strings.Split(output, "\n")

	// 第一优先级：带引号的版本行。
	for _, line := range lines {
		if matches := javaVersionPattern.FindStringSubmatch(line); len(matches) >= 2 {
			if major, ok := majorFromVersionString(matches[1]); ok {
				return major, true
			}
		}
	}

	// 第二优先级：`(build ...)` 行（部分实现只给这一行）。
	for _, line := range lines {
		if matches := buildPattern.FindStringSubmatch(line); len(matches) >= 2 {
			if major, ok := majorFromVersionString(matches[1]); ok {
				return major, true
			}
		}
	}

	return 0, false
}

// majorFromVersionString 把版本串转换为主版本号。
//
// 参数 version 为版本串本体（例如 `1.8.0_401`、`17.0.9`、`21.0.1+12-LTS-29`）。
// 规则：
//   - 以 `1.` 开头且第二段为两位数（`1.8`、`1.7`）→ 取第二段（旧式 JDK 1.x 语义）；
//   - 其余取开头第一段数字（`17.0.9` → 17、`21` → 21）。
//
// 无法提取时返回 0 与 false。
func majorFromVersionString(version string) (int, bool) {
	trimmed := strings.TrimSpace(version)
	if trimmed == "" {
		return 0, false
	}

	// 旧式 `1.8.0_401`：主版本是第二段 8，而不是第一段 1。
	if matches := legacyVersionPrefixPattern.FindStringSubmatch(trimmed); len(matches) >= 2 {
		major, err := strconv.Atoi(matches[1])
		if err != nil {
			return 0, false
		}
		// 只有 `1.x` 且 x >= 2 才按旧式语义解释：
		// `1.0` / `1.1` 等真正的主版本 1 不在此列（现代 JDK 不会出现，但保持严谨）。
		if major >= 2 {
			return major, true
		}
	}

	// 现代 `17.0.9` / `21` / `21.0.1+12-LTS-29`：取开头数字段。
	if matches := leadingVersionPattern.FindStringSubmatch(trimmed); len(matches) >= 2 {
		major, err := strconv.Atoi(matches[1])
		if err != nil {
			return 0, false
		}
		return major, true
	}

	return 0, false
}

// buildJavaDisplay 构造 Java 可执行文件的脱敏展示形态。
//
// 口径以决策记录 D-8 的**示例**为准（示例优先于措辞）：
// `Java 安装目录\…\jdk-21\bin\java.exe`。
// 原路径 `C:\Program Files\Java\jdk-21\bin\java.exe` 的段为
// `C:` / `Program Files` / `Java` / `jdk-21` / `bin` / `java.exe`，
// 示例保留的尾部是 `jdk-21` / `bin` / `java.exe` —— 即**末三段**。
//
// 为什么是三段而不是两段：`java.exe` 的父目录 `bin` 是识别「这是 JDK 的 bin 目录」
// 的关键信息，只保留 `bin\java.exe` 会让所有 JDK 看起来一模一样，失去确认价值。
// D-8 原文「末两级」指「目录层级 + 文件名」的语义单元，
// 在可执行文件路径上对应的正是 `<版本目录>\bin\<文件名>` 三段。
//
// 示例输出：
//   - `C:\Program Files\Java\jdk-21\bin\java.exe` → `Java 安装目录\…\jdk-21\bin\java.exe`
//   - `/usr/lib/jvm/java-17-openjdk/bin/java`     → `Java 安装目录/…/java-17-openjdk/bin/java`
//   - 段数不足三段时直接返回全部（不加类别前缀与省略标记）。
//
// 重要：返回值**不含完整绝对路径**，可安全用于界面展示。
//
// 参数 absPath 为归一化后的绝对路径。
func buildJavaDisplay(absPath string) string {
	if absPath == "" {
		return ""
	}

	// 统一按当前平台分隔符切分：Windows 用 `\`，POSIX 用 `/`。
	separator := displaySeparatorUnix
	if runtime.GOOS == "windows" {
		separator = `\`
	}

	segments := strings.Split(filepath.ToSlash(absPath), "/")
	// 过滤空段（UNC 路径开头与尾部分隔符会产生空串）。
	clean := make([]string, 0, len(segments))
	for _, segment := range segments {
		if segment != "" {
			clean = append(clean, segment)
		}
	}

	if len(clean) == 0 {
		return ""
	}

	// 取末三段：`<版本目录>\bin\java.exe`。
	// 段数不足三段时取全部，此时没有可省略的前缀。
	tailCount := 3
	if len(clean) < tailCount {
		tailCount = len(clean)
	}
	tail := clean[len(clean)-tailCount:]

	// 用平台分隔符重新拼接尾部。
	joinedTail := strings.Join(tail, separator)

	// 当且仅当确实存在被省略的前缀时，才加类别前缀与省略标记。
	// 拼接形态随平台：Windows 为 `Java 安装目录\…\尾部`，POSIX 为 `Java 安装目录/…/尾部`。
	if len(clean) > tailCount {
		return displayCategoryJava + separator + displayEllipsis + separator + joinedTail
	}
	return joinedTail
}

// sanitizeVersionLine 对版本输出首行做脱敏。
//
// 处理内容：
//   - 移除类绝对路径片段（Windows 盘符 / UNC）；
//   - 折叠连续空白；
//   - 截断到 maxSummaryRunes 个字符。
//
// 为什么必须在 DTО 里脱敏：异常输出可能把安装路径或启动命令带进首行，
// 而首行会展示给用户并可能被复制到反馈中。
func sanitizeVersionLine(line string) string {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return ""
	}

	sanitized := pathLikePattern.ReplaceAllString(trimmed, "（已隐藏的路径）")
	sanitized = strings.Join(strings.Fields(sanitized), " ")

	runes := []rune(sanitized)
	if len(runes) > maxSummaryRunes {
		return string(runes[:maxSummaryRunes]) + "…"
	}
	return sanitized
}

// validateJavaPath 执行完整的三层校验，返回 DTO。
//
// 顺序固定、失败即停：归一化 → 存在性 → 可执行性 → 版本输出。
// 归一化属于「路径入口」的前置步骤，失败同样返回 `PATH_INVALID_001`。
//
// 参数 rawPath 为用户提供的路径。
// 返回值始终非 nil：失败时 `Valid=false` 且填充 `ErrorCode`/`ErrorMessage`，
// 让前端只需处理一种返回形态。
func validateJavaPath(rawPath string) JavaValidationResult {
	result := JavaValidationResult{
		// Warnings 初始化为空切片而不是 nil：JSON 序列化后是 `[]` 而不是 `null`，
		// 前端不必额外处理 null。
		Warnings: []string{},
	}

	// 第 0 步：路径归一化（空值 / 相对路径 / 目录都在这里被拒）。
	absPath, pathErr := normalizeJavaPath(rawPath)
	if pathErr != nil {
		result.ErrorCode = pathErr.Code
		result.ErrorMessage = pathErr.Message
		return result
	}

	// 归一化成功即可给出脱敏展示形态，即使后续校验失败也能让用户确认选的是哪个文件。
	display := buildJavaDisplay(absPath)
	result.JavaDisplay = display
	result.ExecutableName = filepath.Base(absPath)

	// 第一步：存在性。
	if existsErr := checkJavaExists(absPath); existsErr != nil {
		result.ErrorCode = existsErr.Code
		result.ErrorMessage = existsErr.Message
		return result
	}

	// 第二步：可执行性。警告不影响继续校验。
	execWarnings, execErr := checkJavaExecutable(absPath)
	if execErr != nil {
		result.Warnings = append(result.Warnings, execWarnings...)
		result.ErrorCode = execErr.Code
		result.ErrorMessage = execErr.Message
		return result
	}
	result.Warnings = append(result.Warnings, execWarnings...)

	// 第三步：`java -version`。
	probe, probeWarnings, probeErr := runJavaVersion(absPath)
	if probeErr != nil {
		result.ErrorCode = probeErr.Code
		result.ErrorMessage = probeErr.Message
		return result
	}
	result.Warnings = append(result.Warnings, probeWarnings...)

	// 保留脱敏后的首行：即使版本解析失败，用户也能看到程序到底输出了什么。
	result.VersionOutputSummary = sanitizeVersionLine(probe.firstLine)

	// 第四步：解析主版本号。
	major, ok := parseJavaMajorVersion(probe.output)
	if !ok {
		result.ErrorCode = ErrorCodeJavaVersion
		result.ErrorMessage = messageVersionUnparsable
		return result
	}

	result.MajorVersion = major
	result.Valid = true
	return result
}
