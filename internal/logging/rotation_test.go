package logging

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// testRecord 构造一条用于轮转测试的最小合法记录。
func testRecord(message string) *Record {
	return &Record{
		Timestamp: "2026-11-02T00:00:00.000Z",
		Level:     string(LevelInfo),
		Component: "rotation-test",
		Message:   message,
	}
}

// newRotationTestWriter 构造指向临时目录、单文件上限很小的写入器。
//
// 把上限注入为字段而不是常量，测试才能在毫秒级内触发多次轮转。
// 注册清理：测试结束前必须关闭句柄，否则 Windows 上 TempDir 的 RemoveAll
// 会因「文件正被另一进程使用」失败（Linux 的 unlink 允许打开中的文件）。
func newRotationTestWriter(t *testing.T, maxFileSize, maxDirBytes int64) *writer {
	t.Helper()
	w := newWriter(t.TempDir(), LevelInfo)
	w.maxFileSize = maxFileSize
	w.maxDirectoryBytes = maxDirBytes
	t.Cleanup(func() {
		if err := w.Close(); err != nil {
			t.Errorf("关闭写入器失败: %v", err)
		}
	})
	return w
}

// TestWriterCloseIsIdempotentAndStopsWrites 验证显式 Close 释放句柄且幂等。
//
// 回归目标：writer 原先没有任何关闭出口，测试或调用方无法释放日志文件句柄；
// Windows 上文件被占用便无法删除。Close 还必须阻止后续 write 重新打开文件，
// 否则「关闭」只是暂时的。
func TestWriterCloseIsIdempotentAndStopsWrites(t *testing.T) {
	w := newWriter(t.TempDir(), LevelInfo)

	w.write(testRecord("close-前"))
	if w.file == nil {
		t.Fatal("写入后应持有已打开的文件句柄")
	}

	if err := w.Close(); err != nil {
		t.Fatalf("首次关闭失败: %v", err)
	}
	if w.file != nil {
		t.Fatal("关闭后不应仍持有文件句柄")
	}

	// 幂等：重复关闭不得 panic 或报错。
	if err := w.Close(); err != nil {
		t.Fatalf("重复关闭失败: %v", err)
	}

	// 关闭后的写入必须被丢弃，不得重新打开文件。
	w.write(testRecord("close-后"))
	if w.file != nil {
		t.Fatal("关闭后的写入重新打开了文件句柄")
	}

	// 关键断言：句柄已释放，目录可以被完整删除（Windows 上会因占用失败）。
	if err := os.RemoveAll(w.dir); err != nil {
		t.Fatalf("关闭后仍无法删除日志目录，说明句柄未释放: %v", err)
	}
}

// TestLogFileNameForAndParse 验证两种文件名形态的生成与解析。
func TestLogFileNameForAndParse(t *testing.T) {
	cases := []struct {
		date string
		seq  int
		name string
	}{
		{"20261102", 0, "app-20261102.log"},
		{"20261102", 1, "app-20261102-1.log"},
		{"20261102", 42, "app-20261102-42.log"},
	}

	for _, tc := range cases {
		got := logFileNameFor(tc.date, tc.seq)
		if got != tc.name {
			t.Fatalf("logFileNameFor(%q, %d) = %q，期望 %q", tc.date, tc.seq, got, tc.name)
		}

		date, seq, ok := parseLogFileSeq(got)
		if !ok || date != tc.date || seq != tc.seq {
			t.Fatalf("parseLogFileSeq(%q) = (%q, %d, %v)，期望 (%q, %d, true)",
				got, date, seq, ok, tc.date, tc.seq)
		}
		if !isLogFileName(got) {
			t.Fatalf("isLogFileName(%q) 期望 true", got)
		}
	}

	invalid := []string{
		"app-2026110.log",      // 日期位数不足
		"app-20261102-0.log",   // 序号必须 ≥ 1
		"app-20261102-.log",    // 序号为空
		"app-20261102-x.log",   // 序号非数字
		"app-20261102.log.tmp", // 后缀不符
		"launcher.log",         // P0 启动日志不参与轮转
		"game-abcd-20261102.log",
		"app-20261332.log", // 非法日期
	}
	for _, name := range invalid {
		if isLogFileName(name) {
			t.Fatalf("isLogFileName(%q) 期望 false", name)
		}
	}
}

// TestWriterRotatesWithinSameDay 验证同一天超过单文件上限后会改用序号文件。
//
// 回归目标：旧实现在超限后关闭句柄、再用同一个日期文件名重开，
// 导致 5 MiB 上限完全失效（同一文件无限增长）。
func TestWriterRotatesWithinSameDay(t *testing.T) {
	const maxFileSize = 256
	w := newRotationTestWriter(t, maxFileSize, 1<<20)

	for index := 0; index < 40; index++ {
		w.write(testRecord(strings.Repeat("x", 64)))
	}

	date := w.fileDate
	if date == "" {
		t.Fatal("写入后应有当前文件日期")
	}

	first := filepath.Join(w.dir, logFileNameFor(date, 0))
	if _, err := os.Stat(first); err != nil {
		t.Fatalf("首个文件应存在: %v", err)
	}

	second := filepath.Join(w.dir, logFileNameFor(date, 1))
	if _, err := os.Stat(second); err != nil {
		t.Fatalf("超限后应生成第二个文件: %v", err)
	}

	// 当前写入的文件必须是最后一个序号文件，且大小受上限约束（单条记录可能略微超出）。
	if w.fileName != logFileNameFor(date, 1) && !strings.HasPrefix(w.fileName, logFileNameFor(date, 1)) {
		// 记录足够多时可能已经轮转到 -2；只要求不是首个文件即可。
		if w.fileName == logFileNameFor(date, 0) {
			t.Fatalf("当前文件不应仍是首个文件：%q", w.fileName)
		}
	}
	if w.fileSize > maxFileSize*2 {
		t.Fatalf("当前文件大小 %d 远超单文件上限 %d，轮转未生效", w.fileSize, maxFileSize)
	}
}

// TestMaintainKeepsCurrentFile 验证轮转清理永远不会删除当前正在写入的文件。
//
// 回归目标：旧实现超限后把 fileDate 清空，maintainLocked 便推不出当前文件名，
// 过期文件清理可能删掉本次运行刚写的日志。
func TestMaintainKeepsCurrentFile(t *testing.T) {
	w := newRotationTestWriter(t, 256, 1<<20)

	for index := 0; index < 20; index++ {
		w.write(testRecord(strings.Repeat("y", 64)))
	}
	if w.fileName == "" {
		t.Fatal("写入后应有当前文件名")
	}
	currentName := w.fileName
	currentPath := filepath.Join(w.dir, currentName)

	// 把目录内所有文件（含当前文件）的修改时间退到 8 天前，触发保留天数清理。
	files, err := listLogFiles(w.dir)
	if err != nil {
		t.Fatalf("列举日志文件失败: %v", err)
	}
	old := time.Now().Add(-8 * 24 * time.Hour)
	for _, file := range files {
		if chtimesErr := os.Chtimes(filepath.Join(w.dir, file.Name()), old, old); chtimesErr != nil {
			t.Fatalf("修改文件时间失败: %v", chtimesErr)
		}
	}

	w.maintainLocked(time.Now())

	if _, statErr := os.Stat(currentPath); statErr != nil {
		t.Fatalf("当前写入的文件不应被清理: %v", statErr)
	}
}

// TestMaintainEnforcesDirectoryLimit 验证目录总量超限时按最旧优先删除。
func TestMaintainEnforcesDirectoryLimit(t *testing.T) {
	const maxFileSize = 256
	const maxDirBytes = 600

	w := newRotationTestWriter(t, maxFileSize, maxDirBytes)

	for index := 0; index < 40; index++ {
		w.write(testRecord(strings.Repeat("z", 64)))
	}

	files, err := listLogFiles(w.dir)
	if err != nil {
		t.Fatalf("列举日志文件失败: %v", err)
	}
	if len(files) < 3 {
		t.Fatalf("测试前提不满足：期望至少 3 个文件，实际 %d", len(files))
	}

	// 给出确定的修改时间顺序：越靠前的越旧。
	base := time.Now().Add(-2 * time.Hour)
	for index, file := range files {
		stamp := base.Add(time.Duration(index) * time.Minute)
		if chtimesErr := os.Chtimes(filepath.Join(w.dir, file.Name()), stamp, stamp); chtimesErr != nil {
			t.Fatalf("修改文件时间失败: %v", chtimesErr)
		}
	}

	w.maintainLocked(time.Now())

	remaining, err := listLogFiles(w.dir)
	if err != nil {
		t.Fatalf("列举日志文件失败: %v", err)
	}

	var total int64
	for _, file := range remaining {
		total += file.Size()
	}
	// 当前文件受保护，可能仍略高于上限；其余文件应已被删到上限以内。
	if total > maxDirBytes+maxFileSize {
		t.Fatalf("清理后目录总量 %d 仍显著超过上限 %d", total, maxDirBytes)
	}
	if len(remaining) >= len(files) {
		t.Fatalf("容量清理未删除任何文件：清理前 %d，清理后 %d", len(files), len(remaining))
	}
}
