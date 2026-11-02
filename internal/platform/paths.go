// Package platform 提供与桌面运行环境相关的目录解析能力。
//
// 本包只负责把运行环境映射成「可写目录」：不读取或写入业务配置、
// 不创建数据库、不实现日志模块。P1 后续任务卡（P1-04 设置、P1-05 日志）
// 可以复用本包的目录探测能力，但不得改变这里的目录优先级（ADR-011）。
package platform

import (
	"os"
	"path/filepath"
	"runtime"
)

// 目录与探测相关的常量：集中声明，避免同一字符串在多处硬编码。
const (
	// appDirName 是应用在用户目录（配置或缓存）下使用的数据目录名。
	appDirName = "NineVoxel-Launcher"

	// webviewDirName 是 WebView2 用户数据目录的子目录名。
	webviewDirName = "webview"

	// logDirName 是启动日志目录的子目录名。
	logDirName = "logs"

	// writeProbePattern 是可写探测使用的临时文件名模板。
	writeProbePattern = ".write-probe-*"

	// directoryPermission 是创建目录时使用的权限位。
	directoryPermission = 0o755
)

// 日志目录来源类别：与接口冻结清单 §8.5 的 directoryKind 取值逐字一致。
//
// 定义在本包而不是 internal/logging，是因为「目录从哪来」由本包决定：
// 只有在这里才能同时知道各候选基目录与最终选择结果，避免日志模块
// 自己再拼一遍路径做字符串比较（两套规则迟早漂移）。
const (
	// LogDirectoryKindUserConfig 表示日志落在用户配置目录。
	//
	// macOS 为 ~/Library/Application Support：日志不是可再生成数据，
	// 放 Caches 会被清理工具/系统在磁盘压力下删除，恰恰在最需要排查时丢失。
	LogDirectoryKindUserConfig = "userConfig"

	// LogDirectoryKindUserCache 表示日志落在用户缓存目录。
	//
	// Windows 为 %LocalAppData%：日志不应写入会随漫游配置同步的 %AppData%。
	LogDirectoryKindUserCache = "userCache"

	// LogDirectoryKindExeDir 表示日志回退到可执行文件同级目录。
	LogDirectoryKindExeDir = "exeDir"
)

// ProbeWritable 判断指定目录是否真的可以写入。
//
// 实现方式：先尝试创建目录，再在目录内创建并立即删除一个临时文件；
// 两步都成功才返回 true。只检查目录是否存在无法发现只读安装目录或受限
// 运行环境，而 WebView2 与启动日志都依赖真实的写入能力。
// 参数 dir 为待检测目录；返回 false 表示该目录不可用作数据目录。
func ProbeWritable(dir string) bool {
	if err := os.MkdirAll(dir, directoryPermission); err != nil {
		return false
	}

	probe, err := os.CreateTemp(dir, writeProbePattern)
	if err != nil {
		return false
	}

	probeName := probe.Name()
	// 先关闭再删除：Windows 不允许删除仍处于打开状态的文件。
	if err := probe.Close(); err != nil {
		os.Remove(probeName)
		return false
	}
	os.Remove(probeName)
	return true
}

// WebviewDataPath 解析 WebView2 用户数据目录。
//
// 该目录**只对 Windows 有意义**：Wails v3 的 WebviewUserDataPath 属于
// WindowsOptions，macOS/Linux 的后端不会读取它。非 Windows 平台直接返回空，
// 避免 ProbeWritable 在 macOS 上创建无用目录——打进 .app 时那会是
// Contents/MacOS/webview/，既无用又有破坏代码签名的风险。
//
// Windows 下的目录优先级（受 ADR-011 约束，不得调整顺序）：
//  1. 优先使用可执行文件所在目录下的 webview 子目录，随程序目录一起安装和卸载；
//  2. 应用目录只读时（例如安装到 Program Files），回退到用户缓存目录下的
//     NineVoxel-Launcher/webview；
//  3. 两处都不可写时返回空字符串，交给 WebView2 使用默认目录。
//
// 显式指定该目录可以避免 WebView2 默认把用户数据放到 %AppData%\<exe 名>：
// 该目录在受限环境下创建失败时，WebView2 控制器初始化会直接失败，
// 进程随即退出，用户只能看到启动器一闪而过。
func WebviewDataPath() string {
	if runtime.GOOS != "windows" {
		return ""
	}

	// 第一步：优先使用可执行文件同级目录，便于便携版随目录整体清理。
	if exePath, err := os.Executable(); err == nil {
		appDir := filepath.Join(filepath.Dir(exePath), webviewDirName)
		if ProbeWritable(appDir) {
			return appDir
		}
	}

	// 第二步：应用目录不可写时回退到用户缓存目录。
	if cacheDir, err := os.UserCacheDir(); err == nil {
		userDir := filepath.Join(cacheDir, appDirName, webviewDirName)
		if ProbeWritable(userDir) {
			return userDir
		}
	}

	// 第三步：两处都不可写时交给 WebView2 使用默认目录。
	return ""
}

// userLogBaseDir 返回当前平台的用户级日志基目录及其来源类别。
//
// 平台差异是有意为之，不能统一用 os.UserCacheDir：
//   - macOS：使用 ~/Library/Application Support。Caches 语义是「可再生成数据」，
//     会被系统与清理工具删除，不适合承载需要事后排查的日志；Application Support
//     与设置库同父目录（<base>/NineVoxel-Launcher/{data,logs}），便于一并收集。
//   - Windows：保持 %LocalAppData%（os.UserCacheDir）。%AppData% 是漫游配置目录，
//     日志写进去会随漫游配置同步，既污染服务器也拖慢登录。
//   - 其他平台：沿用缓存目录，保持既有行为。
//
// 返回 false 表示拿不到该平台的标准用户目录，调用方应继续走 exe 同级回退。
func userLogBaseDir() (string, string, bool) {
	if runtime.GOOS == "darwin" {
		if configDir, err := os.UserConfigDir(); err == nil && configDir != "" {
			return configDir, LogDirectoryKindUserConfig, true
		}
		return "", "", false
	}

	if cacheDir, err := os.UserCacheDir(); err == nil && cacheDir != "" {
		return cacheDir, LogDirectoryKindUserCache, true
	}
	return "", "", false
}

// LogDirectory 返回启动日志目录。
//
// 目录优先级：
//  1. 用户级日志基目录下的 NineVoxel-Launcher/logs（macOS 为 Application Support，
//     Windows 为 %LocalAppData%，见 userLogBaseDir），用户目录通常可写；
//  2. 不可写时回退到可执行文件所在目录下的 logs；
//  3. 两处都不可写时返回空字符串，调用方应跳过文件日志并继续启动。
//
// 日志优先落在用户目录：安装到只读目录时仍要保证启动阶段的错误有可查记录。
func LogDirectory() string {
	// 第一步：优先使用用户级目录。
	if baseDir, _, ok := userLogBaseDir(); ok {
		dir := filepath.Join(baseDir, appDirName, logDirName)
		if ProbeWritable(dir) {
			return dir
		}
	}

	// 第二步：回退到可执行文件同级目录。
	if exePath, err := os.Executable(); err == nil {
		dir := filepath.Join(filepath.Dir(exePath), logDirName)
		if ProbeWritable(dir) {
			return dir
		}
	}

	// 第三步：两处都不可写时返回空字符串。
	return ""
}

// LogDirectoryKind 返回给定日志目录的来源类别，供状态展示使用。
//
// 返回值为 LogDirectoryKind* 常量；无法识别时返回空字符串。
// 该函数只做路径比较，不重新探测可写性：实际可写性已由 LogDirectory 保证。
func LogDirectoryKind(dir string) string {
	if dir == "" {
		return ""
	}

	if baseDir, kind, ok := userLogBaseDir(); ok {
		expected := filepath.Join(baseDir, appDirName, logDirName)
		if filepath.Clean(dir) == filepath.Clean(expected) {
			return kind
		}
	}

	if exePath, err := os.Executable(); err == nil {
		expected := filepath.Join(filepath.Dir(exePath), logDirName)
		if filepath.Clean(dir) == filepath.Clean(expected) {
			return LogDirectoryKindExeDir
		}
	}

	return ""
}
