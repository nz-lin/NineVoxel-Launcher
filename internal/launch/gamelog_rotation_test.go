package launch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"example.com/ninevoxel/launcher/internal/events"
)

// TestGameLogFileNameSeq 验证游戏日志的两种文件名形态与解析。
func TestGameLogFileNameSeq(t *testing.T) {
	cases := []struct {
		launchID string
		date     string
		seq      int
		want     string
	}{
		{"abcdef1234567890", "20261102", 0, "game-abcdef12-20261102.log"},
		{"abcdef1234567890", "20261102", 1, "game-abcdef12-20261102-1.log"},
		{"short", "20261102", 3, "game-short-20261102-3.log"},
		{"", "20261102", 2, "game-unknown-20261102-2.log"},
	}

	for _, tc := range cases {
		got := gameLogFileNameFor(tc.launchID, tc.date, tc.seq)
		if got != tc.want {
			t.Fatalf("gameLogFileNameFor(%q, %q, %d) = %q，期望 %q",
				tc.launchID, tc.date, tc.seq, got, tc.want)
		}

		launchPart, date, seq, ok := parseGameLogFileName(got)
		if !ok {
			t.Fatalf("parseGameLogFileName(%q) 期望合法", got)
		}
		if date != tc.date || seq != tc.seq {
			t.Fatalf("解析 (%q, %d)，期望 (%q, %d)", date, seq, tc.date, tc.seq)
		}
		if launchPart != gameLogLaunchPart(tc.launchID) {
			t.Fatalf("解析 launch 片段 %q，期望 %q", launchPart, gameLogLaunchPart(tc.launchID))
		}
	}
}

// TestIsGameLogFileNameSeq 验证带序号的文件名仍被识别，且不与 app 日志重叠。
func TestIsGameLogFileNameSeq(t *testing.T) {
	valid := []string{
		"game-abcdef12-20261102.log",
		"game-abcdef12-20261102-1.log",
		"game-abcd-ef-20261102.log", // launchId 清洗后含连字符
		"game-abcd-ef-20261102-12.log",
	}
	for _, name := range valid {
		if !isGameLogFileName(name) {
			t.Fatalf("isGameLogFileName(%q) 期望 true", name)
		}
	}

	invalid := []string{
		"app-20261102.log",
		"app-20261102-1.log",
		"game-20261102.log",            // 缺 launchId
		"game-abcdef12.log",            // 缺日期
		"game-abcdef12-20261102-0.log", // 序号必须 ≥ 1
		"game-abcdef12-20261102-.log",
		"game-abcdef12-20261102-x.log",
		"game-abcdef12-20261332.log",
	}
	for _, name := range invalid {
		if isGameLogFileName(name) {
			t.Fatalf("isGameLogFileName(%q) 期望 false", name)
		}
	}
}

// TestGameLogRotatesWithinSameDay 验证同一天超过单文件上限后会改用序号文件。
//
// 回归目标：旧实现在超限后关闭句柄、下次写入又用同一日期文件名重开，
// 10 MiB 上限形同虚设。
func TestGameLogRotatesWithinSameDay(t *testing.T) {
	const maxFileSize = 256

	dir := t.TempDir()
	channel := &gameLogChannel{launchID: "rotation-test", dir: dir, maxFileSize: maxFileSize}
	// 测试直接构造通道（不启动 writer goroutine），因此必须显式关闭句柄：
	// 否则 Windows 上 TempDir 的 RemoveAll 会因文件被占用而失败。
	t.Cleanup(func() {
		if err := channel.closeFile(); err != nil {
			t.Errorf("关闭游戏日志文件失败: %v", err)
		}
	})
	line := events.GameLogLine{
		TimestampMs: time.Now().UnixMilli(),
		Stream:      streamStdout,
		Text:        strings.Repeat("x", 64),
	}

	for index := 0; index < 40; index++ {
		channel.writeLine(line)
	}

	date := channel.currentDate
	if date == "" {
		t.Fatal("写入后应有当前文件日期")
	}

	first := filepath.Join(dir, gameLogFileNameFor("rotation-test", date, 0))
	if _, err := os.Stat(first); err != nil {
		t.Fatalf("首个游戏日志文件应存在: %v", err)
	}

	second := filepath.Join(dir, gameLogFileNameFor("rotation-test", date, 1))
	if _, err := os.Stat(second); err != nil {
		t.Fatalf("超限后应生成第二个游戏日志文件: %v", err)
	}

	if channel.fileSize > maxFileSize*2 {
		t.Fatalf("当前游戏日志文件大小 %d 远超上限 %d，轮转未生效", channel.fileSize, maxFileSize)
	}
}

// TestGameLogCloseFileIsIdempotentAndStopsWrites 验证显式关闭释放句柄且幂等。
//
// 回归目标：文件句柄原先只能在 stop()（依赖已启动的 writer goroutine）
// 或 drainAndClose() 内关闭；直接构造通道的测试与「仅用内存缓冲」的场景
// 没有任何关闭出口，Windows 上目录便无法删除。关闭还必须阻止后续 writeLine
// 重新打开文件，否则句柄会再次泄漏。
func TestGameLogCloseFileIsIdempotentAndStopsWrites(t *testing.T) {
	dir := t.TempDir()
	channel := &gameLogChannel{launchID: "close-test", dir: dir, maxFileSize: 1 << 20}

	line := events.GameLogLine{
		TimestampMs: time.Now().UnixMilli(),
		Stream:      streamStdout,
		Text:        "close-test",
	}
	channel.writeLine(line)
	if channel.file == nil {
		t.Fatal("写入后应持有已打开的文件句柄")
	}

	if err := channel.closeFile(); err != nil {
		t.Fatalf("首次关闭失败: %v", err)
	}
	if channel.file != nil {
		t.Fatal("关闭后不应仍持有文件句柄")
	}

	// 幂等：重复关闭不得 panic 或报错。
	if err := channel.closeFile(); err != nil {
		t.Fatalf("重复关闭失败: %v", err)
	}

	// 关闭后的写入必须被丢弃，不得重新打开文件。
	channel.writeLine(line)
	if channel.file != nil {
		t.Fatal("关闭后的写入重新打开了文件句柄")
	}

	// 关键断言：句柄已释放，目录可以被完整删除（Windows 上会因占用失败）。
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("关闭后仍无法删除日志目录，说明句柄未释放: %v", err)
	}
}
