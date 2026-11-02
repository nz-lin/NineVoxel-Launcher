package platform

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestWebviewDataPathIsWindowsOnly 验证非 Windows 平台不再创建/返回 WebView2 数据目录。
//
// 回归目标：macOS 上调用 WebviewDataPath 会 ProbeWritable 并创建
// <exe 目录>/webview（打进 .app 时即 Contents/MacOS/webview），既无用又影响签名。
func TestWebviewDataPathIsWindowsOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("该断言只针对非 Windows 平台")
	}

	if got := WebviewDataPath(); got != "" {
		t.Fatalf("非 Windows 平台 WebviewDataPath() = %q，期望空字符串", got)
	}
}

// TestLogDirectoryKind 验证日志目录来源类别与平台策略一致。
func TestLogDirectoryKind(t *testing.T) {
	baseDir, expectedKind, ok := userLogBaseDir()
	if !ok {
		t.Skip("当前环境拿不到标准用户目录，跳过来源类别断言")
	}

	dir := filepath.Join(baseDir, appDirName, logDirName)
	if got := LogDirectoryKind(dir); got != expectedKind {
		t.Fatalf("LogDirectoryKind(%q) = %q，期望 %q", dir, got, expectedKind)
	}

	// macOS 必须使用 Application Support（userConfig）；其他平台保持缓存目录。
	if runtime.GOOS == "darwin" && expectedKind != LogDirectoryKindUserConfig {
		t.Fatalf("macOS 日志来源类别期望 %q，实际 %q", LogDirectoryKindUserConfig, expectedKind)
	}
	if runtime.GOOS != "darwin" && expectedKind != LogDirectoryKindUserCache {
		t.Fatalf("%s 日志来源类别期望 %q，实际 %q", runtime.GOOS, LogDirectoryKindUserCache, expectedKind)
	}

	if got := LogDirectoryKind(""); got != "" {
		t.Fatalf("空目录应返回空类别，实际 %q", got)
	}
}

// TestLogDirectoryKindExeDir 验证可执行文件同级日志目录被识别为 exeDir。
func TestLogDirectoryKindExeDir(t *testing.T) {
	exePath, err := os.Executable()
	if err != nil || exePath == "" {
		t.Skip("当前环境拿不到可执行文件路径")
	}

	exeLogDir := filepath.Join(filepath.Dir(exePath), logDirName)
	if got := LogDirectoryKind(exeLogDir); got != LogDirectoryKindExeDir {
		t.Fatalf("LogDirectoryKind(%q) = %q，期望 %q", exeLogDir, got, LogDirectoryKindExeDir)
	}
}
