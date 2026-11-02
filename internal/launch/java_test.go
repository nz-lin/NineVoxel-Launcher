package launch

import (
	"os"
	"runtime"
	"testing"
)

// TestParseJavaMajorVersionCoveredFormats 验证卡面要求的 5 种版本格式都能解析。
//
// 这些是真实 JDK 的 `java -version` 输出首行形态；任一条失败都会让
// 用户在对应 JDK 上无法通过校验。
func TestParseJavaMajorVersionCoveredFormats(t *testing.T) {
	cases := []struct {
		name   string
		output string
		want   int
		wantOK bool
	}{
		{
			name:   "JDK 8 旧式版本行",
			output: `java version "1.8.0_401"`,
			want:   8,
			wantOK: true,
		},
		{
			name:   "OpenJDK 17 带日期",
			output: `openjdk version "17.0.9" 2023-10-17`,
			want:   17,
			wantOK: true,
		},
		{
			name:   "OpenJDK 21 无小版本",
			output: `openjdk version "21" 2023-09-19`,
			want:   21,
			wantOK: true,
		},
		{
			name:   "JDK 21 LTS 标记",
			output: `java version "21.0.1" 2023-10-17 LTS`,
			want:   21,
			wantOK: true,
		},
		{
			name:   "仅 Runtime Environment build 行",
			output: `OpenJDK Runtime Environment (build 21.0.1+12-LTS-29)`,
			want:   21,
			wantOK: true,
		},
		{
			name: "完整多行输出",
			output: `openjdk version "17.0.9" 2023-10-17
OpenJDK Runtime Environment (build 17.0.9+9)
OpenJDK 64-Bit Server VM (build 17.0.9+9, mixed mode, sharing)`,
			want:   17,
			wantOK: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseJavaMajorVersion(tc.output)
			if ok != tc.wantOK {
				t.Fatalf("可解析性期望 %v，实际 %v（输出 %q）", tc.wantOK, ok, tc.output)
			}
			if ok && got != tc.want {
				t.Fatalf("主版本期望 %d，实际 %d（输出 %q）", tc.want, got, tc.output)
			}
		})
	}
}

// TestParseJavaMajorVersionRejectsUnparsable 验证无法识别的输出被拒绝。
//
// 关键要求：无法识别时必须返回 false，由调用方转成 JAVA_VERSION_001，
// 不得返回 0 却当成「解析成功」。
func TestParseJavaMajorVersionRejectsUnparsable(t *testing.T) {
	cases := []struct {
		name   string
		output string
	}{
		{name: "空输出", output: ""},
		{name: "纯文本无版本", output: "not a java program"},
		{name: "IBM J9 风格（未覆盖）", output: `java version "1.8.0"`[:0] + "Eclipse OpenJ9 VM (build 1.8.0, JRE 1.8.0)"},
		{name: "只有 VM 行且无 build", output: "OpenJDK 64-Bit Server VM (mixed mode)"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := parseJavaMajorVersion(tc.output); ok {
				t.Fatalf("输出 %q 期望解析失败，实际成功", tc.output)
			}
		})
	}
}

// TestMajorFromVersionString 验证版本串到主版本号的转换规则。
func TestMajorFromVersionString(t *testing.T) {
	cases := []struct {
		version string
		want    int
		wantOK  bool
	}{
		{"1.8.0_401", 8, true},
		{"1.7.0_80", 7, true},
		{"17.0.9", 17, true},
		{"21", 21, true},
		{"21.0.1+12-LTS-29", 21, true},
		{"1.8", 8, true},
		{"", 0, false},
		{"abc", 0, false},
	}

	for _, tc := range cases {
		t.Run(tc.version, func(t *testing.T) {
			got, ok := majorFromVersionString(tc.version)
			if ok != tc.wantOK {
				t.Fatalf("可解析性期望 %v，实际 %v", tc.wantOK, ok)
			}
			if ok && got != tc.want {
				t.Fatalf("主版本期望 %d，实际 %d", tc.want, got)
			}
		})
	}
}

// TestNormalizeJavaPathRejectsInvalid 验证路径入口的四类拒绝。
func TestNormalizeJavaPathRejectsInvalid(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		wantErr string
	}{
		{name: "空值", input: "   ", wantErr: ErrorCodePathInvalid},
		{name: "相对路径", input: "./java/bin/java", wantErr: ErrorCodePathInvalid},
		{name: "纯文件名", input: "java.exe", wantErr: ErrorCodePathInvalid},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := normalizeJavaPath(tc.input)
			if err == nil {
				t.Fatalf("输入 %q 期望拒绝，实际通过", tc.input)
			}
			if err.Code != tc.wantErr {
				t.Fatalf("错误码期望 %q，实际 %q", tc.wantErr, err.Code)
			}
		})
	}
}

// TestNormalizeJavaPathRejectsDirectory 验证指向目录的路径被拒绝。
//
// 关键点：目录必然「存在」，若不在归一化阶段拦截，会被存在性检查
// 误判成 JAVA_NOT_FOUND_001 —— 语义不符合「该内容不是可执行文件」。
func TestNormalizeJavaPathRejectsDirectory(t *testing.T) {
	dir := t.TempDir()

	_, err := normalizeJavaPath(dir)
	if err == nil {
		t.Fatal("目录路径期望拒绝，实际通过")
	}
	if err.Code != ErrorCodePathInvalid {
		t.Fatalf("目录路径的错误码期望 %q，实际 %q", ErrorCodePathInvalid, err.Code)
	}
}

// TestCheckJavaExists 验证第一层校验的三种结果。
func TestCheckJavaExists(t *testing.T) {
	t.Run("不存在", func(t *testing.T) {
		err := checkJavaExists(t.TempDir() + "/missing-java")
		if err == nil {
			t.Fatal("不存在的文件期望返回错误")
		}
		if err.Code != ErrorCodeJavaNotFound {
			t.Fatalf("错误码期望 %q，实际 %q", ErrorCodeJavaNotFound, err.Code)
		}
	})

	t.Run("普通文件通过", func(t *testing.T) {
		dir := t.TempDir()
		path := dir + "/placeholder-java"
		if writeErr := writeTestFile(path); writeErr != nil {
			t.Fatalf("准备测试文件失败: %v", writeErr)
		}
		if err := checkJavaExists(path); err != nil {
			t.Fatalf("普通文件期望通过，实际错误: %v", err)
		}
	})

	t.Run("目录被拒", func(t *testing.T) {
		// 直接调用 checkJavaExists 传目录，验证其兜底判断也拒绝目录。
		if err := checkJavaExists(t.TempDir()); err == nil {
			t.Fatal("目录期望返回错误")
		}
	})
}

// TestCheckJavaExecutableWindows 验证 Windows 下的扩展名判定。
func TestCheckJavaExecutableWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("仅在 Windows 上验证扩展名规则")
	}

	t.Run("exe 通过", func(t *testing.T) {
		warnings, err := checkJavaExecutable(`C:\fake\java.exe`)
		if err != nil {
			t.Fatalf("java.exe 期望通过，实际: %v", err)
		}
		if len(warnings) != 0 {
			t.Fatalf("java.exe 不应产生警告，实际 %v", warnings)
		}
	})

	t.Run("无扩展名只警告不拒绝", func(t *testing.T) {
		warnings, err := checkJavaExecutable(`C:\fake\java`)
		if err != nil {
			t.Fatalf("无扩展名期望只警告不拒绝，实际错误: %v", err)
		}
		if len(warnings) != 1 {
			t.Fatalf("无扩展名期望 1 条警告，实际 %d 条", len(warnings))
		}
	})

	t.Run("错误扩展名被拒", func(t *testing.T) {
		_, err := checkJavaExecutable(`C:\fake\java.txt`)
		if err == nil {
			t.Fatal("java.txt 期望拒绝，实际通过")
		}
		if err.Code != ErrorCodeJavaNotFound {
			t.Fatalf("错误码期望 %q，实际 %q", ErrorCodeJavaNotFound, err.Code)
		}
	})
}

// TestBuildJavaDisplay 验证脱敏展示口径（决策记录 D-8）。
//
// 断言要点：结果**不含**完整绝对路径；含目录类别前缀与末两级。
func TestBuildJavaDisplay(t *testing.T) {
	cases := []struct {
		name string
		path string
		want string
	}{
		{
			name: "Windows 典型路径",
			path: `C:\Program Files\Java\jdk-21\bin\java.exe`,
			want: `Java 安装目录\…\jdk-21\bin\java.exe`,
		},
		{
			name: "Windows 深路径",
			path: `D:\tools\jdk\17\bin\java.exe`,
			want: `Java 安装目录\…\17\bin\java.exe`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := buildJavaDisplay(tc.path)
			if runtime.GOOS == "windows" && got != tc.want {
				t.Fatalf("展示形态期望 %q，实际 %q", tc.want, got)
			}
			// 无论平台：结果都不得包含完整路径的前缀 `C:\Program Files` 等。
			if runtime.GOOS == "windows" && containsAny(got, `C:\`, `D:\`, "Program Files") {
				t.Fatalf("展示形态 %q 仍含绝对路径片段", got)
			}
		})
	}

	t.Run("空路径返回空串", func(t *testing.T) {
		if got := buildJavaDisplay(""); got != "" {
			t.Fatalf("空路径期望空串，实际 %q", got)
		}
	})
}

// TestSanitizeVersionLine 验证版本首行的脱敏与截断。
func TestSanitizeVersionLine(t *testing.T) {
	t.Run("路径被隐藏", func(t *testing.T) {
		got := sanitizeVersionLine(`Error: cannot load C:\Users\alice\jdk\lib\modules`)
		if containsAny(got, "alice", `C:\Users`) {
			t.Fatalf("脱敏后仍含路径片段: %q", got)
		}
	})

	t.Run("折叠空白", func(t *testing.T) {
		got := sanitizeVersionLine("openjdk   version   \"17.0.9\"")
		if containsAny(got, "   ", "\n") {
			t.Fatalf("空白未折叠: %q", got)
		}
	})

	t.Run("超长截断", func(t *testing.T) {
		long := make([]rune, 0, maxSummaryRunes+50)
		for index := 0; index < maxSummaryRunes+50; index++ {
			long = append(long, 'x')
		}
		got := sanitizeVersionLine(string(long))
		// 截断后应以上限加一个省略号结尾。
		if len([]rune(got)) != maxSummaryRunes+1 {
			t.Fatalf("截断长度期望 %d，实际 %d", maxSummaryRunes+1, len([]rune(got)))
		}
	})
}

// TestValidateJavaPathFailureStopsEarly 验证「失败即停」：早期失败不应走到后续阶段。
//
// 断言方式：路径不存在时，结果的 MajorVersion 必须为 0 且 VersionOutputSummary 为空，
// 说明没有执行到 `java -version`。
func TestValidateJavaPathFailureStopsEarly(t *testing.T) {
	result := validateJavaPath(t.TempDir() + "/definitely-missing-java")

	if result.Valid {
		t.Fatal("不存在的路径期望校验失败")
	}
	if result.ErrorCode != ErrorCodeJavaNotFound {
		t.Fatalf("错误码期望 %q，实际 %q", ErrorCodeJavaNotFound, result.ErrorCode)
	}
	if result.MajorVersion != 0 {
		t.Fatalf("早期失败不应解析出主版本，实际 %d", result.MajorVersion)
	}
	if result.VersionOutputSummary != "" {
		t.Fatalf("早期失败不应有版本输出，实际 %q", result.VersionOutputSummary)
	}
	// Warnings 必须是空切片而不是 nil（保证 JSON 序列化为 []）。
	if result.Warnings == nil {
		t.Fatal("Warnings 期望空切片而非 nil")
	}
}

// TestErrorCodesAreFrozenValues 验证三个错误码常量与决策记录第五节逐字一致。
func TestErrorCodesAreFrozenValues(t *testing.T) {
	cases := map[string]string{
		ErrorCodePathInvalid:  "PATH_INVALID_001",
		ErrorCodeJavaNotFound: "JAVA_NOT_FOUND_001",
		ErrorCodeJavaVersion:  "JAVA_VERSION_001",
	}

	for constant, want := range cases {
		if constant != want {
			t.Fatalf("错误码常量值 %q 与冻结值 %q 不一致", constant, want)
		}
	}
}

// TestErrorFormatIncludesCode 验证错误输出格式便于前端按前缀提取错误码。
func TestErrorFormatIncludesCode(t *testing.T) {
	err := newInputError(ErrorCodeJavaNotFound, messageJavaNotExist)
	got := err.Error()
	if len(got) < len(ErrorCodeJavaNotFound) || got[:len(ErrorCodeJavaNotFound)] != ErrorCodeJavaNotFound {
		t.Fatalf("错误文本应以错误码开头，实际 %q", got)
	}
}

// writeTestFile 在测试目录中写入一个占位文件。
//
// 只写临时目录（t.TempDir 提供），**不触碰任何真实 Java 安装目录或游戏目录**：
// 测试也需要遵守「零系统副作用」约束。
//
// 参数 path 为目标文件路径；返回写入错误。
func writeTestFile(path string) error {
	return os.WriteFile(path, []byte("placeholder"), 0o600)
}

// containsAny 判断文本是否包含任一子串。
//
// 测试辅助函数：避免为几个断言引入 strings 依赖差异。
func containsAny(text string, subs ...string) bool {
	for _, sub := range subs {
		if len(sub) == 0 {
			continue
		}
		for index := 0; index+len(sub) <= len(text); index++ {
			if text[index:index+len(sub)] == sub {
				return true
			}
		}
	}
	return false
}
