// 本文件实现「启动引导器（launcher part）」的 Go 侧：内嵌字节码、落地、生成启动脚本。
//
// 设计目标（D-8 的延伸）：
//   - **命令行只留 JVM 参数 + classpath + 引导器类名**，游戏参数（含 `--accessToken`、
//     `--userProperties`、加载器的 `--tweakClass`）改由 stdin 传递。这样既不受
//     Windows 的 32767 字符命令行上限约束，令牌也不再出现在任务管理器 / `ps` 里。
//   - **不启动第二个 JVM**：引导器在自身进程内反射调用游戏主类，进程句柄、stdio
//     管道与退出码都直接对应游戏本身，supervisor 无需感知引导层的存在。
//   - **stdin 在 `launch` 之后保持打开**：D-3 的优雅停止依赖「关闭 stdin」触发，
//     因此协议里不能以 EOF 作为启动信号。
//
// 明确不做：
//   - 不做 `abort` / profiler 挂载等交互式握手：本启动器没有「确认后才放行」的需求，
//     多一个往返就多一个卡死窗口。
//   - 不把 classpath 放进 stdin：JVM 启动前就需要它，放进去没有任何效果。
package launch

import (
	"embed"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// launcherPartFS 内嵌引导器的编译产物。
//
// class 文件按 Java 8（class 版本 52.0）编译：1.12.2 及更早的实例仍在 Java 8 上
// 运行，高版本字节码会直接抛 `UnsupportedClassVersionError`。
// 重新编译的命令见 launcherpart/README.md。
//
//go:embed all:launcherpart/classes
var launcherPartFS embed.FS

const (
	// launcherPartMainClass 是引导器的入口类（与 Java 源码一致）。
	//
	// 用反向域名包名：这是 Java 生态的通用约定，也让任务管理器 / `sun.java.command`
	// 里显示的类名能一眼看出归属。
	launcherPartMainClass = "com.ninezhou.ninevoxel.EntryPoint"

	// launcherPartClassDir 是内嵌的 class 根目录（相对包目录）。
	launcherPartClassDir = "launcherpart/classes"

	// launcherPartTempDirPattern 是引导器落地目录名的前缀。
	launcherPartTempDirPattern = "ninevoxel-launch-part-"

	// launcherPartFileMode 是落地文件的权限：只读，避免被同机其他进程改写。
	launcherPartFileMode = 0o600

	// launcherPartDirMode 是落地目录的权限。
	launcherPartDirMode = 0o700

	// launcherPartKeyMainClass / Param / Launch 是协议键，必须与 Java 侧常量一致。
	launcherPartKeyMainClass = "mainClass"
	launcherPartKeyParam     = "param"
	launcherPartKeyLaunch    = "launch"
)

// prepareLauncherPart 把内嵌的引导器字节码写到临时目录，返回 classpath 根与清理函数。
//
// 每次启动写一份而不是复用固定路径：固定路径在多实例并发启动时会互相覆盖，
// 且可能被残留的旧版本字节码污染。目录很小（单个类文件），用完即删。
//
// 参数无；返回 classpath 根目录、清理函数与错误（`LAUNCH_PROCESS_001`）。
func prepareLauncherPart() (string, func(), *Error) {
	tempDir, mkErr := os.MkdirTemp("", launcherPartTempDirPattern)
	if mkErr != nil {
		return "", nil, newSystemError(ErrorCodeLaunchProcess, messageLauncherPartUnavailable)
	}
	cleanup := func() {
		_ = os.RemoveAll(tempDir)
	}

	walkErr := fs.WalkDir(launcherPartFS, launcherPartClassDir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		// 内嵌路径形如 `launcherpart/classes/com/ninezhou/ninevoxel/EntryPoint.class`；
		// 落地路径去掉前缀、保留 `com/ninezhou/ninevoxel/EntryPoint.class` 这层包结构——
		// JVM 按包路径定位类，结构错一层就会 ClassNotFound。
		relative, relErr := filepath.Rel(launcherPartClassDir, path)
		if relErr != nil {
			return relErr
		}
		if relative == "." {
			return nil
		}
		target := filepath.Join(tempDir, relative)

		if entry.IsDir() {
			return os.MkdirAll(target, launcherPartDirMode)
		}

		content, readErr := launcherPartFS.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		return os.WriteFile(target, content, launcherPartFileMode)
	})
	if walkErr != nil {
		cleanup()
		return "", nil, newSystemError(ErrorCodeLaunchProcess, messageLauncherPartUnavailable)
	}

	return tempDir, cleanup, nil
}

// buildLaunchScript 生成写入引导器 stdin 的启动脚本。
//
// 协议（UTF-8，逐行）：
//
//	mainClass <游戏主类>
//	param <游戏参数>
//	...
//	launch
//
// 参数 mainClass 为游戏主类；parts 为游戏参数数组。
//
// 为什么行内用「键 + 空格 + 值」而不是 JSON：引导器必须按 Java 8 编译且不引入
// 任何第三方依赖；行协议既不需要解析库，也能直接读懂。代价是值里不能含换行——
// 这类参数只可能来自被篡改的版本 JSON 或附加参数，因此直接给出可读错误，
// 而不是让引导器读到半行就开跑。
func buildLaunchScript(mainClass string, parts []ArgumentPart) (string, *Error) {
	trimmedMain := strings.TrimSpace(mainClass)
	if trimmedMain == "" {
		return "", newInputError(ErrorCodeLaunchArgs, messageVersionMainClassMissing)
	}
	if strings.ContainsAny(trimmedMain, " \t\r\n") {
		return "", newInputError(ErrorCodeLaunchArgs, messageLauncherPartScriptInvalid)
	}

	var builder strings.Builder
	// 预估长度：主类行 + 每个参数的键前缀与换行。
	builder.Grow(len(trimmedMain) + 24 + len(parts)*12)
	builder.WriteString(launcherPartKeyMainClass)
	builder.WriteByte(' ')
	builder.WriteString(trimmedMain)
	builder.WriteByte('\n')

	for _, part := range parts {
		if strings.TrimSpace(part.Value) == "" {
			continue
		}
		if strings.ContainsAny(part.Value, "\r\n") {
			return "", newInputError(ErrorCodeLaunchArgs, messageLauncherPartScriptInvalid)
		}
		builder.WriteString(launcherPartKeyParam)
		builder.WriteByte(' ')
		builder.WriteString(part.Value)
		builder.WriteByte('\n')
	}

	builder.WriteString(launcherPartKeyLaunch)
	builder.WriteByte('\n')
	return builder.String(), nil
}
