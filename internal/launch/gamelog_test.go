package launch

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"example.com/ninevoxel/launcher/internal/events"
)

// ===== RedactForDisplay：文本级脱敏 =====

// TestRedactForDisplayCredentialPatterns 验证各类凭证文本被替换。
//
// 表驱动覆盖任务要求的全部敏感项；每项都是真实可能出现在游戏输出里的形态。
func TestRedactForDisplayCredentialPatterns(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		mustNot  string
		mustHave string
	}{
		{
			name:     "Authorization 头",
			input:    "Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.abc.def",
			mustNot:  "eyJhbGciOiJIUzI1NiJ9",
			mustHave: gameLogRedacted,
		},
		{
			name:     "Bearer 前缀",
			input:    "sent header Bearer abc123def456",
			mustNot:  "abc123def456",
			mustHave: "Bearer " + gameLogRedacted,
		},
		{
			name:     "accessToken 空格形式",
			input:    "args: --accessToken deadbeefcafe",
			mustNot:  "deadbeefcafe",
			mustHave: "--accessToken " + gameLogRedacted,
		},
		{
			name:     "accessToken 等号形式",
			input:    "arg --accessToken=deadbeefcafe",
			mustNot:  "deadbeefcafe",
			mustHave: "--accessToken=" + gameLogRedacted,
		},
		{
			name:     "clientId",
			input:    "--clientId 00000000402b5328",
			mustNot:  "00000000402b5328",
			mustHave: gameLogRedacted,
		},
		{
			name:     "xuid",
			input:    "--xuid 2535456789012345",
			mustNot:  "2535456789012345",
			mustHave: gameLogRedacted,
		},
		{
			name:     "auth_session",
			input:    "--auth_session sessionvalue123",
			mustNot:  "sessionvalue123",
			mustHave: gameLogRedacted,
		},
		{
			name:     "http 代理密码",
			input:    "-Dhttp.proxyPassword=secret123",
			mustNot:  "secret123",
			mustHave: "-Dhttp.proxyPassword=" + gameLogRedacted,
		},
		{
			name:     "https 代理密码",
			input:    "-Dhttps.proxyPassword=secret456",
			mustNot:  "secret456",
			mustHave: "-Dhttps.proxyPassword=" + gameLogRedacted,
		},
		{
			name:     "username 也替换（P4 后为真实账号名）",
			input:    "--username Player",
			mustNot:  "Player",
			mustHave: "--username " + gameLogRedacted,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := RedactForDisplay(tc.input)

			if strings.Contains(got, tc.mustNot) {
				t.Fatalf("脱敏后仍含敏感值 %q: %q", tc.mustNot, got)
			}
			if !strings.Contains(got, tc.mustHave) {
				t.Fatalf("脱敏后应含 %q，实际 %q", tc.mustHave, got)
			}
		})
	}
}

// TestRedactForDisplayKeepsNonSensitiveContent 验证非敏感内容不被改动。
//
// 关键：不做 IP 脱敏、不改写聊天与服务器地址（有意决策）。
func TestRedactForDisplayKeepsNonSensitiveContent(t *testing.T) {
	cases := []string{
		"[12:00:00] [main/INFO]: Loading Minecraft 1.21",
		"Connecting to 192.168.1.100:25565",
		"<Player> hello world",
		"-Xmx2G -Xms1G",
	}

	for _, input := range cases {
		t.Run(input, func(t *testing.T) {
			got := RedactForDisplay(input)
			if got != input {
				t.Fatalf("非敏感内容不应被改动：\n输入 %q\n输出 %q", input, got)
			}
		})
	}
}

// TestRedactForDisplayTruncatesLongLines 验证超长行被截断。
func TestRedactForDisplayTruncatesLongLines(t *testing.T) {
	long := strings.Repeat("x", gameLogMaxLineRunes+100)

	got := RedactForDisplay(long)
	if len([]rune(got)) != gameLogMaxLineRunes+1 {
		t.Fatalf("截断长度期望 %d，实际 %d", gameLogMaxLineRunes+1, len([]rune(got)))
	}
}

// TestRedactForDisplayEmpty 验证空输入返回空。
func TestRedactForDisplayEmpty(t *testing.T) {
	if got := RedactForDisplay(""); got != "" {
		t.Fatalf("空输入期望空输出，实际 %q", got)
	}
}

// ===== RedactCommandSummary：参数数组脱敏 =====

// TestRedactCommandSummarySpaceSeparated 验证「键 空格 值」形态。
func TestRedactCommandSummarySpaceSeparated(t *testing.T) {
	args := []string{
		"--username", "Player",
		"--uuid", "00000000-0000-0000-0000-000000000000",
		"--accessToken", "secret-token-value",
		"--version", "1.21",
	}

	got := RedactCommandSummary(args)

	if len(got) != len(args) {
		t.Fatalf("脱敏后长度应保持不变（%d），实际 %d", len(args), len(got))
	}

	// 敏感值被替换。
	for _, value := range got {
		if value == "secret-token-value" || value == "Player" {
			t.Fatalf("敏感值未被替换: %q", value)
		}
	}
	// 键本身保留。
	if got[0] != "--username" || got[2] != "--uuid" || got[4] != "--accessToken" {
		t.Fatalf("键名应保留，实际 %+v", got)
	}
	// 非敏感参数原样保留。
	if got[6] != "--version" || got[7] != "1.21" {
		t.Fatalf("非敏感参数应原样保留，实际 %+v", got)
	}
}

// TestRedactCommandSummaryInlineValue 验证「键=值」内嵌形态。
func TestRedactCommandSummaryInlineValue(t *testing.T) {
	args := []string{
		"--accessToken=secret-token",
		"-Dhttp.proxyPassword=proxy-secret",
		"-Dhttps.proxyPassword=proxy-secret2",
		"--clientId=12345",
		"--xuid=999",
		"--username=RealName",
		"-Xmx2G",
	}

	got := RedactCommandSummary(args)

	for _, item := range got {
		if strings.Contains(item, "secret") || strings.Contains(item, "12345") ||
			strings.Contains(item, "999") || strings.Contains(item, "RealName") {
			t.Fatalf("内嵌敏感值未被替换: %q", item)
		}
	}
	// `-Xmx2G` 原样保留。
	found := false
	for _, item := range got {
		if item == "-Xmx2G" {
			found = true
		}
	}
	if !found {
		t.Fatalf("非敏感参数 -Xmx2G 应保留，实际 %+v", got)
	}
}

// TestRedactCommandSummaryDoesNotMutateInput 验证不修改入参。
func TestRedactCommandSummaryDoesNotMutateInput(t *testing.T) {
	args := []string{"--accessToken", "secret"}
	original := append([]string(nil), args...)

	_ = RedactCommandSummary(args)

	for index := range args {
		if args[index] != original[index] {
			t.Fatalf("入参被修改：前 %+v，后 %+v", original, args)
		}
	}
}

// TestRedactCommandSummaryEmpty 验证空数组返回 nil。
func TestRedactCommandSummaryEmpty(t *testing.T) {
	if got := RedactCommandSummary(nil); got != nil {
		t.Fatalf("nil 输入期望 nil 输出，实际 %+v", got)
	}
	if got := RedactCommandSummary([]string{}); len(got) != 0 {
		t.Fatalf("空数组期望空输出，实际 %+v", got)
	}
}

// TestRedactCommandSummaryTrailingKeyNoValue 验证末尾孤立键不 panic。
func TestRedactCommandSummaryTrailingKeyNoValue(t *testing.T) {
	got := RedactCommandSummary([]string{"--accessToken"})

	if len(got) != 1 || got[0] != "--accessToken" {
		t.Fatalf("末尾孤立键应原样保留，实际 %+v", got)
	}
}

// ===== 环形缓冲与丢弃计数 =====

// TestRingBufferCapacityAndDropCount 验证缓冲容量与丢弃计数。
//
// 断言：写入超过容量的行后，缓冲只保留最新 capacity 行，且 droppedByBuffer 正确累加。
// 该用例不走真实落盘（构造通道但不启动 writer），聚焦缓冲逻辑本身。
func TestRingBufferCapacityAndDropCount(t *testing.T) {
	channel := &gameLogChannel{
		launchID:   "ring-test",
		lines:      make([]events.GameLogLine, 0, gameLogBufferCapacity),
		writeQueue: make(chan events.GameLogLine, gameLogQueueCapacity),
	}

	// 写入 capacity + 100 行：多出的 100 行应被丢弃。
	total := gameLogBufferCapacity + 100
	for index := 0; index < total; index++ {
		channel.appendLine(streamStdout, "line content")
	}

	channel.mu.Lock()
	bufferLen := len(channel.lines)
	dropped := channel.droppedByBuffer
	channel.mu.Unlock()

	if bufferLen != gameLogBufferCapacity {
		t.Fatalf("缓冲长度期望 %d，实际 %d", gameLogBufferCapacity, bufferLen)
	}
	if dropped == 0 {
		t.Fatalf("应有丢弃计数，实际 %d", dropped)
	}
	// 总写入 = 保留 + 丢弃（缓冲丢弃；队列丢弃另计，此处不排队列）。
	channel.mu.Lock()
	queueDropped := channel.droppedByQueue
	channel.mu.Unlock()
	if bufferLen+dropped+queueDropped != total {
		t.Fatalf("行数守恒被破坏：保留 %d + 缓冲丢弃 %d + 队列丢弃 %d ≠ 总写入 %d",
			bufferLen, dropped, queueDropped, total)
	}
}

// TestAppendLineAssignsMonotonicSequence 验证同一毫秒内的多行仍有稳定身份。
func TestAppendLineAssignsMonotonicSequence(t *testing.T) {
	channel := &gameLogChannel{
		launchID:   "sequence-test",
		lines:      make([]events.GameLogLine, 0, gameLogBufferCapacity),
		writeQueue: make(chan events.GameLogLine, gameLogQueueCapacity),
	}

	for index := 0; index < 3; index++ {
		channel.appendLine(streamStdout, "same line")
	}

	channel.mu.Lock()
	defer channel.mu.Unlock()
	for index, line := range channel.lines {
		want := int64(index + 1)
		if line.Sequence != want {
			t.Fatalf("第 %d 行序号期望 %d，实际 %d", index, want, line.Sequence)
		}
	}
}

// TestGameLogFileName 验证文件命名规则。
func TestGameLogFileName(t *testing.T) {
	cases := []struct {
		launchID string
		date     string
		want     string
	}{
		{"abcdef1234567890", "20261101", "game-abcdef12-20261101.log"},
		{"short", "20261101", "game-short-20261101.log"},
		{"", "20261101", "game-unknown-20261101.log"},
		// 含非法字符：被清洗。
		{"ab/cd-ef", "20261101", "game-abcd-ef-20261101.log"},
	}

	for _, tc := range cases {
		t.Run(tc.launchID, func(t *testing.T) {
			if got := gameLogFileName(tc.launchID, tc.date); got != tc.want {
				t.Fatalf("文件名期望 %q，实际 %q", tc.want, got)
			}
		})
	}
}

// TestIsGameLogFileName 验证文件名判定。
//
// 关键：必须与 internal/logging 的 `app-*.log` 判定互不重叠。
func TestIsGameLogFileName(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"game-abcdef12-20261101.log", true},
		{"game-short-20261101.log", true},
		// P1-05 的应用日志：绝不能被本通道认领。
		{"app-20261101.log", false},
		{"app-abcdef12-20261101.log", false},
		// 形态不符。
		{"game-abcdef12.log", false},
		{"game-abcdef12-20261301.log", false},
		{"game-abcdef12-20261101.log.tmp", false},
		{"other-abcdef12-20261101.log", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isGameLogFileName(tc.name); got != tc.want {
				t.Fatalf("isGameLogFileName(%q) = %v，期望 %v", tc.name, got, tc.want)
			}
		})
	}
}

// TestPruneOldFilesKeepsRecent 验证清理只保留最近 N 个文件。
func TestPruneOldFilesKeepsRecent(t *testing.T) {
	dir := t.TempDir()

	// 建 5 个 game 文件（用不同日期，模拟历史启动）。
	for _, date := range []string{"20261027", "20261028", "20261029", "20261030", "20261031"} {
		name := "game-testlaun-" + date + ".log"
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatalf("创建测试文件失败: %v", err)
		}
		// 明确设置修改时间，让「最近」的判定可预测。
		parsed, parseErr := time.Parse(gameLogDateLayout, date)
		if parseErr != nil {
			t.Fatalf("解析日期失败: %v", parseErr)
		}
		if err := os.Chtimes(path, parsed, parsed); err != nil {
			t.Fatalf("设置时间失败: %v", err)
		}
	}

	// 另建一个 P1-05 的应用日志：**不得被本通道清理**。
	appPath := filepath.Join(dir, "app-20261001.log")
	if err := os.WriteFile(appPath, []byte("app"), 0o600); err != nil {
		t.Fatalf("创建应用日志失败: %v", err)
	}

	channel := &gameLogChannel{launchID: "prune-test", dir: dir}
	channel.pruneOldFiles()

	files, err := listGameLogFiles(dir)
	if err != nil {
		t.Fatalf("列举文件失败: %v", err)
	}
	if len(files) != gameLogRetainFiles {
		t.Fatalf("清理后应保留 %d 个文件，实际 %d", gameLogRetainFiles, len(files))
	}

	// 应用日志必须仍在。
	if _, statErr := os.Stat(appPath); os.IsNotExist(statErr) {
		t.Fatal("P1-05 的 app-*.log 不应被游戏日志通道清理")
	}
}

// TestPruneOldFilesSkipsWhenUnderLimit 验证文件数未超限时不删除。
func TestPruneOldFilesSkipsWhenUnderLimit(t *testing.T) {
	dir := t.TempDir()

	for _, date := range []string{"20261029", "20261030"} {
		path := filepath.Join(dir, "game-testlaun-"+date+".log")
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatalf("创建测试文件失败: %v", err)
		}
	}

	channel := &gameLogChannel{launchID: "prune-test", dir: dir}
	channel.pruneOldFiles()

	files, _ := listGameLogFiles(dir)
	if len(files) != 2 {
		t.Fatalf("未超限时不应删除，实际保留 %d 个", len(files))
	}
}

// TestNormalizeStream 验证流标识收敛。
func TestNormalizeStream(t *testing.T) {
	cases := map[string]string{
		"stdout":  streamStdout,
		"STDOUT":  streamStdout,
		"stderr":  streamStderr,
		"STDERR":  streamStderr,
		" stderr": streamStderr,
		"other":   streamStdout,
		"":        streamStdout,
	}

	for input, want := range cases {
		t.Run(input, func(t *testing.T) {
			if got := normalizeStream(input); got != want {
				t.Fatalf("normalizeStream(%q) = %q，期望 %q", input, got, want)
			}
		})
	}
}

// TestQueryGameLogWithoutActiveChannel 验证无活动启动时返回 LAUNCH_LOG_001。
func TestQueryGameLogWithoutActiveChannel(t *testing.T) {
	StopGameLogChannel()

	_, queryErr := queryGameLog("", 0, 0)
	if queryErr == nil {
		t.Fatal("无活动启动时期望返回错误")
	}
	if queryErr.Code != ErrorCodeLaunchLog {
		t.Fatalf("错误码期望 %q，实际 %q", ErrorCodeLaunchLog, queryErr.Code)
	}
}

// TestQueryGameLogAfterChannelStoppedByLaunchID 验证退出后仍可回读最近会话。
func TestQueryGameLogAfterChannelStoppedByLaunchID(t *testing.T) {
	channel := StartGameLogChannel("finished-launch", nil)
	channel.appendLine(streamStdout, "last line")
	StopGameLogChannel()

	snapshot, queryErr := queryGameLog("finished-launch", 0, 0)
	if queryErr != nil {
		t.Fatalf("退出后按 launchId 查询失败: %v", queryErr)
	}
	if len(snapshot.lines) != 1 || snapshot.lines[0].Text != "last line" {
		t.Fatalf("退出后日志快照不符: %#v", snapshot.lines)
	}
}

// TestQueryGameLogLimitClamping 验证 limit 在 Go 边界被裁剪。
func TestQueryGameLogLimitClamping(t *testing.T) {
	if DefaultGameLogLimit != 200 {
		t.Fatalf("默认条数期望 200，实际 %d", DefaultGameLogLimit)
	}
	if MaxGameLogLimit != 1000 {
		t.Fatalf("上限期望 1000，实际 %d", MaxGameLogLimit)
	}
}

// TestGameLogConstantsFrozen 验证关键容量常量。
func TestGameLogConstantsFrozen(t *testing.T) {
	if gameLogBufferCapacity != 2000 {
		t.Fatalf("缓冲容量期望 2000，实际 %d", gameLogBufferCapacity)
	}
	if gameLogFileMaxBytes != 10*1024*1024 {
		t.Fatalf("单文件上限期望 10 MiB，实际 %d", gameLogFileMaxBytes)
	}
	if gameLogRetainFiles != 3 {
		t.Fatalf("保留文件数期望 3，实际 %d", gameLogRetainFiles)
	}
	if gameLogEventBatchLimit != 200 {
		t.Fatalf("事件批次上限期望 200，实际 %d", gameLogEventBatchLimit)
	}
}

// TestGameLogErrorCodeFrozen 验证 LAUNCH_LOG_001 码值来自冻结表。
func TestGameLogErrorCodeFrozen(t *testing.T) {
	if ErrorCodeLaunchLog != "LAUNCH_LOG_001" {
		t.Fatalf("常量值错误: %q", ErrorCodeLaunchLog)
	}
}

// TestFormatLocalMillis 验证时间格式化为本地时区毫秒精度。
func TestFormatLocalMillis(t *testing.T) {
	got := formatLocalMillis(time.Date(2026, 11, 1, 12, 0, 0, 123000000, time.UTC).UnixMilli())

	// 只断言格式（长度与结构），不断言具体数值（依赖本地时区）。
	if len(got) != len("2006-01-02 15:04:05.000") {
		t.Fatalf("时间格式长度不符: %q", got)
	}
	if !strings.Contains(got, ".") {
		t.Fatalf("时间应含毫秒分隔点: %q", got)
	}
}

// TestSanitizeFileNamePart 验证文件名片段清洗。
func TestSanitizeFileNamePart(t *testing.T) {
	cases := map[string]string{
		"abc123":   "abc123",
		"a-b_c":    "a-b_c",
		"a/b\\c:d": "abcd",
		"a b":      "ab",
		"标签":       "",
	}

	for input, want := range cases {
		t.Run(input, func(t *testing.T) {
			if got := sanitizeFileNamePart(input); got != want {
				t.Fatalf("清洗结果期望 %q，实际 %q", want, got)
			}
		})
	}
}

// TestGameLogChannelDoesNotWriteToGameDirectory 验证通道不写游戏目录。
//
// 该断言很重要：P2 全阶段游戏目录只读。通道固定使用 `platform.LogDirectory()`，
// 因此需要确认它**从不**使用游戏根目录。
func TestGameLogChannelDoesNotWriteToGameDirectory(t *testing.T) {
	if runtime.GOOS != "windows" && runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("仅在有真实日志目录的平台上验证")
	}

	gameRoot := t.TempDir()

	// 模拟一个游戏根：在它下面不应该出现任何 game-*.log。
	channel := StartGameLogChannel("dirtest-1234", nil)
	defer StopGameLogChannel()

	if channel.dir == "" {
		t.Skip("当前环境启动器日志目录不可用，跳过")
	}

	// 关键断言：通道目录不等于游戏根目录（也绝不在其下）。
	if channel.dir == gameRoot {
		t.Fatal("日志通道不应使用游戏根目录")
	}
	if strings.HasPrefix(channel.dir, gameRoot) {
		t.Fatalf("日志通道目录 %q 位于游戏根 %q 之下", channel.dir, gameRoot)
	}

	// 游戏根目录应保持为空（没有任何文件被创建）。
	entries, err := os.ReadDir(gameRoot)
	if err != nil {
		t.Fatalf("列举游戏根失败: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("游戏根目录不应有文件，实际 %d 个", len(entries))
	}
}

// TestAppendLineDoesNotBlock 验证采集入口不阻塞。
//
// 断言：连续写入远超队列容量的行，调用应立即返回（不阻塞采集 goroutine）。
// 用一个短超时守护：若实现误用了阻塞发送，本用例会超时失败。
func TestAppendLineDoesNotBlock(t *testing.T) {
	channel := StartGameLogChannel("blocktest-1", nil)
	defer StopGameLogChannel()

	lineCount := gameLogQueueCapacity * 2

	done := make(chan struct{})
	go func() {
		defer close(done)
		for index := 0; index < lineCount; index++ {
			channel.appendLine(streamStdout, "line content")
		}
	}()

	select {
	case <-done:
		// 正常：全部写入立即完成。
	case <-time.After(10 * time.Second):
		t.Fatal("appendLine 发生阻塞——违反「写入不阻塞游戏」要求")
	}
}

// TestAppendLineRedactsText 验证采集入口写入的是脱敏后文本。
func TestAppendLineRedactsText(t *testing.T) {
	channel := StartGameLogChannel("redacttest-1", nil)
	defer StopGameLogChannel()

	channel.appendLine(streamStdout, "--accessToken secretvalue123")

	snapshot, queryErr := queryGameLog("", 0, 0)
	if queryErr != nil {
		t.Fatalf("查询日志失败: %v", queryErr)
	}
	if len(snapshot.lines) == 0 {
		t.Fatal("应至少有一行日志")
	}

	// 最新一行在 index 0（倒序）。
	latest := snapshot.lines[0]
	if strings.Contains(latest.Text, "secretvalue123") {
		t.Fatalf("缓冲中的文本未脱敏: %q", latest.Text)
	}
	if !strings.Contains(latest.Text, gameLogRedacted) {
		t.Fatalf("缓冲中的文本应含占位符: %q", latest.Text)
	}
}
