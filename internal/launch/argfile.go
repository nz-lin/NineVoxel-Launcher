// 本文件实现「命令行仍超长时改用 Java 参数文件（`@argfile`）」的兜底路径。
//
// 与引导器（launcherpart.go）的分工：
//   - 引导器把**游戏主类与游戏参数**搬出命令行，解决绝大多数超长场景；
//   - 但 classpath 与 JVM 参数（例如 NeoForge 的 `-p` 模块路径）必须在 JVM
//     启动前就绪，仍留在命令行里。它们本身超长时，把整串参数写入文本文件、
//     只向 Java 传 `@<文件路径>`，由 java 启动器自行展开。
//
// 两条硬约束：
//  1. `@argfile` 自 Java 9 引入，Java 8 不支持，只能以 `LAUNCH_ARGS_002` 拒绝；
//  2. 参数文件的编码必须与「JVM 读取它时使用的默认字符集」一致：Windows 上
//     是系统 ANSI 代码页（中文系统为 GBK），与 Java 主版本无关——已在
//     Java 17 / 21 / 25 上实测，UTF-8 编码会让含中文的路径变成乱码。
//     见 argfile_windows.go 的 `encodeArgFileContent`。

package launch

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"example.com/ninevoxel/launcher/internal/logging"
)

const (
	// argFileMinJavaMajorVersion 是支持 `@argfile` 的最低 Java 主版本。
	//
	// 参数文件语法由 Java 9 引入；Java 8 的命令行上限只能靠缩短参数解决。
	argFileMinJavaMajorVersion = 9

	// argFileTempDirPattern 是参数文件临时目录名的前缀。
	//
	// 用 `os.MkdirTemp` 生成唯一目录：同一台机器可能同时启动多个实例，
	// 固定文件名会让后启动的实例覆盖前一个实例尚未读取完的参数。
	argFileTempDirPattern = "ninevoxel-launch-args-"

	// argFileFileName 是参数文件名（位于临时目录内）。
	argFileFileName = "launch-args.txt"
)

// prepareProcessArguments 在参数总长超过命令行上限时改写为单个 `@<参数文件>` 参数。
//
// 参数 args 为已组装的完整参数数组；javaMajor 为所选 Java 的主版本号
// （0 表示未知，按「不支持参数文件」处理，宁可报错也不赌）。
//
// 返回：
//   - 未超限：原样返回 args，cleanup 为 nil；
//   - 超限且 Java >= 9：返回 `[]string{"@" + 路径}` 与清理函数；
//   - 超限且 Java 版本过低 / 未知：`LAUNCH_ARGS_002`；
//   - 参数文件创建或写入失败：`LAUNCH_PROCESS_001`（属系统错误）。
//
// 调用方在**进程退出之后**必须调用 cleanup：JVM 只在启动瞬间读取参数文件，
// 进程存活期间删除它可能让尚未打开文件的 JVM 读不到内容。
func prepareProcessArguments(args []string, javaMajor int) ([]string, func(), *Error) {
	totalBytes := totalArgumentsBytes(args)
	if totalBytes <= maxTotalArgumentBytes {
		return args, nil, nil
	}

	if javaMajor < argFileMinJavaMajorVersion {
		return nil, nil, newInputError(ErrorCodeLaunchArgsTooLong,
			fmt.Sprintf(messageProcessArgsTooLong, totalBytes, maxTotalArgumentBytes))
	}

	tempDir, mkErr := os.MkdirTemp("", argFileTempDirPattern)
	if mkErr != nil {
		return nil, nil, newSystemError(ErrorCodeLaunchProcess, messageArgFileCreateFailed)
	}
	cleanup := func() {
		_ = os.RemoveAll(tempDir)
	}

	content, encodeErr := encodeArgFileContent(buildArgFileText(args))
	if encodeErr != nil {
		cleanup()
		return nil, nil, newInputError(ErrorCodeLaunchArgsTooLong, messageArgFileUnencodable)
	}

	path := filepath.Join(tempDir, argFileFileName)
	if writeErr := os.WriteFile(path, content, 0o600); writeErr != nil {
		cleanup()
		return nil, nil, newSystemError(ErrorCodeLaunchProcess, messageArgFileCreateFailed)
	}

	// 只记字节数与参数个数，不记参数内容（可能含账号占位值之外的路径信息）。
	logger().Info("启动参数超出命令行上限，已改用参数文件传递", logging.Fields{
		Event: "launch.args.argfile",
		Attributes: map[string]string{
			"totalBytes":       countAttribute(totalBytes),
			"limitBytes":       countAttribute(maxTotalArgumentBytes),
			"argumentCount":    countAttribute(len(args)),
			"javaMajorVersion": countAttribute(javaMajor),
		},
	})

	return []string{"@" + path}, cleanup, nil
}

// totalArgumentsBytes 计算参数数组中所有参数的文本总字节数。
//
// 只累加参数本身，不计分隔符与引号：这是「明显过长」的防线，不追求逐字节
// 模拟系统的计数规则（系统会计入引号与结尾空字符）。
func totalArgumentsBytes(args []string) int {
	total := 0
	for _, arg := range args {
		total += len(arg)
	}
	return total
}

// buildArgFileText 把参数数组渲染为参数文件内容：每个参数一行。
//
// 参数文件以空白切分参数，因此含空格的路径（`C:\Program Files\...`）必须加引号。
func buildArgFileText(args []string) string {
	var builder strings.Builder
	// 预估长度：参数总长 + 每项的引号与换行。
	builder.Grow(totalArgumentsBytes(args) + len(args)*3)
	for _, arg := range args {
		builder.WriteString(quoteArgFileArgument(arg))
		builder.WriteByte('\n')
	}
	return builder.String()
}

// quoteArgFileArgument 按 Java 参数文件的引号内转义规则包裹单个参数。
//
// 一律加双引号：未加引号时反斜杠是字面量、空白会切分参数，规则组合起来
// 比「统一加引号」更容易出错。加引号后只需处理引号内的转义。
//
// 关键点：**引号内的反斜杠是转义字符**，`"C:\Users"` 会被解析成 `C:Users`
// （已用 Java 21 实测），因此 Windows 路径里的每个 `\` 都要写成 `\\`。
func quoteArgFileArgument(arg string) string {
	var builder strings.Builder
	builder.Grow(len(arg) + 2)
	builder.WriteByte('"')
	for _, r := range arg {
		switch r {
		case '\\':
			builder.WriteString(`\\`)
		case '"':
			builder.WriteString(`\"`)
		case '\n':
			builder.WriteString(`\n`)
		case '\r':
			builder.WriteString(`\r`)
		case '\t':
			builder.WriteString(`\t`)
		case '\f':
			builder.WriteString(`\f`)
		default:
			builder.WriteRune(r)
		}
	}
	builder.WriteByte('"')
	return builder.String()
}
