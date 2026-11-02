// 本文件是 runtime 包的纯解析测试。
//
// 强约束（卡面第 2 条与「硬约束」）：**不执行任何真实 java**。
// 这里只把 `java -version` 的输出当作字符串喂给解析函数，
// 涉及目录的用例只做只读枚举（不启动子进程），可用性判定全部走纯函数。
package runtime

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestParseJavaVersionString 覆盖主版本号解析的旧式与现代语义。
func TestParseJavaVersionString(t *testing.T) {
	cases := []struct {
		name   string
		input  string
		want   int
		wantOK bool
	}{
		{name: "旧式 1.8", input: "1.8.0_401", want: 8, wantOK: true},
		{name: "旧式 1.7", input: "1.7.0_80", want: 7, wantOK: true},
		{name: "真正的 1.x", input: "1.1", want: 1, wantOK: true},
		{name: "现代 17", input: "17.0.9", want: 17, wantOK: true},
		{name: "纯数字 21", input: "21", want: 21, wantOK: true},
		{name: "带构建号", input: "21.0.1+12-LTS-29", want: 21, wantOK: true},
		{name: "带空白", input: "  25.0.1  ", want: 25, wantOK: true},
		{name: "空串", input: "", want: 0, wantOK: false},
		{name: "无数字", input: "unknown", want: 0, wantOK: false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got, ok := ParseJavaVersionString(testCase.input)
			if ok != testCase.wantOK {
				t.Fatalf("ParseJavaVersionString(%q) ok = %v, want %v", testCase.input, ok, testCase.wantOK)
			}
			if ok && got != testCase.want {
				t.Fatalf("ParseJavaVersionString(%q) = %d, want %d", testCase.input, got, testCase.want)
			}
		})
	}
}

// TestParseJavaVersionOutputOracleLegacy 覆盖 Oracle 8 的经典三行输出。
func TestParseJavaVersionOutputOracleLegacy(t *testing.T) {
	output := strings.Join([]string{
		`java version "1.8.0_401"`,
		`Java(TM) SE Runtime Environment (build 1.8.0_401-b10)`,
		`Java HotSpot(TM) 64-Bit Server VM (build 25.401-b10, mixed mode)`,
	}, "\n")

	info, ok := ParseJavaVersionOutput(output)
	if !ok {
		t.Fatal("Oracle 8 输出应当可以解析")
	}
	if info.MajorVersion != 8 {
		t.Fatalf("MajorVersion = %d, want 8", info.MajorVersion)
	}
	if info.FullVersion != "1.8.0_401" {
		t.Fatalf("FullVersion = %q, want 1.8.0_401", info.FullVersion)
	}
	if info.Vendor != VendorOracle {
		t.Fatalf("Vendor = %q, want %q", info.Vendor, VendorOracle)
	}
	if info.Arch != ArchX64 || info.Bitness != Bitness64 {
		t.Fatalf("Arch/Bitness = %q/%q, want x64/64", info.Arch, info.Bitness)
	}
}

// TestParseJavaVersionOutputOpenJDK21 覆盖 Temurin 21 的三行输出。
func TestParseJavaVersionOutputOpenJDK21(t *testing.T) {
	output := strings.Join([]string{
		`openjdk version "21.0.1" 2023-10-17 LTS`,
		`OpenJDK Runtime Environment Temurin-21.0.1+12 (build 21.0.1+12-LTS)`,
		`OpenJDK 64-Bit Server VM Temurin-21.0.1+12 (build 21.0.1+12-LTS, mixed mode, sharing)`,
	}, "\n")

	info, ok := ParseJavaVersionOutput(output)
	if !ok {
		t.Fatal("OpenJDK 21 输出应当可以解析")
	}
	if info.MajorVersion != 21 {
		t.Fatalf("MajorVersion = %d, want 21", info.MajorVersion)
	}
	if info.Vendor != VendorAdoptium {
		t.Fatalf("Vendor = %q, want %q", info.Vendor, VendorAdoptium)
	}
	if info.Arch != ArchX64 || info.Bitness != Bitness64 {
		t.Fatalf("Arch/Bitness = %q/%q, want x64/64", info.Arch, info.Bitness)
	}
}

// TestParseJavaVersionOutputBuildOnly 覆盖只有 build 行的输出（第二优先级）。
func TestParseJavaVersionOutputBuildOnly(t *testing.T) {
	output := "OpenJDK Runtime Environment (build 17.0.9+9)\nOpenJDK 64-Bit Server VM (build 17.0.9+9, mixed mode)"

	info, ok := ParseJavaVersionOutput(output)
	if !ok {
		t.Fatal("只有 build 行时也应当解析出版本")
	}
	if info.MajorVersion != 17 {
		t.Fatalf("MajorVersion = %d, want 17", info.MajorVersion)
	}
	if info.FullVersion != "17.0.9+9" {
		t.Fatalf("FullVersion = %q, want 17.0.9+9", info.FullVersion)
	}
}

// TestParseJavaVersionOutputAarch64 覆盖 ARM64 输出与位数判定。
func TestParseJavaVersionOutputAarch64(t *testing.T) {
	output := strings.Join([]string{
		`openjdk version "21.0.2" 2024-01-16`,
		`OpenJDK Runtime Environment (build 21.0.2+13-58)`,
		`OpenJDK 64-Bit Server VM (build 21.0.2+13-58, mixed mode, sharing)  aarch64`,
	}, "\n")

	info, ok := ParseJavaVersionOutput(output)
	if !ok {
		t.Fatal("ARM64 输出应当可以解析")
	}
	if info.Arch != ArchAarch64 || info.Bitness != Bitness64 {
		t.Fatalf("Arch/Bitness = %q/%q, want aarch64/64", info.Arch, info.Bitness)
	}
}

// TestParseJavaVersionOutputWithoutVersion 覆盖「能执行但没有版本号」的输入。
func TestParseJavaVersionOutputWithoutVersion(t *testing.T) {
	outputs := []string{
		"",
		"Error: could not open `C:\\Program Files\\Java\\lib\\amd64\\jvm.cfg'",
		"Usage: python [option] ... [-c cmd | -m mod | file | -]",
		"hello world",
	}
	for _, output := range outputs {
		if _, ok := ParseJavaVersionOutput(output); ok {
			t.Fatalf("输出 %q 不应解析出版本", output)
		}
	}
}

// TestParseJavaVersionOutputKeepsFirstLineSafe 覆盖首行摘要的脱敏与截断。
func TestParseJavaVersionOutputKeepsFirstLineSafe(t *testing.T) {
	longTail := strings.Repeat("x", 300)
	output := `openjdk version "21.0.1" https://example.com/download?token=secret ` + longTail

	info, ok := ParseJavaVersionOutput(output)
	if !ok {
		t.Fatal("带 URL 的版本行仍应解析出版本")
	}
	if strings.Contains(info.FirstLine, "http") {
		t.Fatalf("首行摘要不应保留完整 URL：%q", info.FirstLine)
	}
	if runes := []rune(info.FirstLine); len(runes) > 200 {
		t.Fatalf("首行摘要长度 = %d，应被截断到 200", len(runes))
	}
}

// TestDetectVendor 覆盖厂商判定的优先级与未知分支。
func TestDetectVendor(t *testing.T) {
	cases := []struct {
		name   string
		output string
		want   string
	}{
		{name: "Adoptium 优先于 OpenJDK", output: "OpenJDK Runtime Environment Temurin-21.0.1", want: VendorAdoptium},
		{name: "Microsoft", output: "OpenJDK Runtime Environment Microsoft-12345", want: VendorMicrosoft},
		{name: "Azul Zulu", output: "OpenJDK Runtime Environment Zulu21.30+15-CA", want: VendorZulu},
		{name: "Oracle", output: "Java(TM) SE Runtime Environment", want: VendorOracle},
		{name: "普通 OpenJDK", output: "OpenJDK Runtime Environment (build 17.0.9+9)", want: VendorOpenJDK},
		{name: "未知", output: "some java build", want: VendorUnknown},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := detectVendor(testCase.output); got != testCase.want {
				t.Fatalf("detectVendor = %q, want %q", got, testCase.want)
			}
		})
	}
}

// TestDetectKind 覆盖 JDK/JRE/未知三态。
func TestDetectKind(t *testing.T) {
	cases := []struct {
		name   string
		output string
		want   string
	}{
		{name: "JDK", output: "java version \"21\" 2023-09-19 LTS\nJava(TM) SE JDK", want: KindJDK},
		{name: "JRE", output: "java version \"17.0.9\"\nJRE 17", want: KindJRE},
		{name: "未知", output: "openjdk version \"21.0.1\"", want: KindUnknown},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := detectKind(testCase.output); got != testCase.want {
				t.Fatalf("detectKind = %q, want %q", got, testCase.want)
			}
		})
	}
}

// TestDetectArch 覆盖架构与位数的组合判定。
func TestDetectArch(t *testing.T) {
	cases := []struct {
		name        string
		output      string
		wantArch    string
		wantBitness string
	}{
		{name: "64 位无关键词按 x64", output: "OpenJDK 64-Bit Server VM", wantArch: ArchX64, wantBitness: Bitness64},
		{name: "32 位", output: "Java HotSpot(TM) 32-Bit Server VM", wantArch: ArchX86, wantBitness: Bitness32},
		{name: "aarch64", output: "OpenJDK 64-Bit Server VM aarch64", wantArch: ArchAarch64, wantBitness: Bitness64},
		{name: "arm64", output: "OpenJDK 64-Bit Server VM arm64", wantArch: ArchAarch64, wantBitness: Bitness64},
		{name: "x86_64", output: "OpenJDK 64-Bit Server VM (build 21, mixed mode) x86_64", wantArch: ArchX64, wantBitness: Bitness64},
		{name: "i686", output: "OpenJDK Server VM (build 17) i686", wantArch: ArchX86, wantBitness: Bitness32},
		{name: "无信息", output: "OpenJDK Runtime Environment", wantArch: ArchUnknown, wantBitness: BitnessUnknown},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			arch, bitness := detectArch(testCase.output)
			if arch != testCase.wantArch || bitness != testCase.wantBitness {
				t.Fatalf("detectArch = %q/%q, want %q/%q", arch, bitness, testCase.wantArch, testCase.wantBitness)
			}
		})
	}
}

// TestSanitizeVersionLine 覆盖 URL 剔除与空白折叠。
func TestSanitizeVersionLine(t *testing.T) {
	line := "  openjdk version \"21\"   https://example.com/x?y=1  2023-10-17  "
	got := sanitizeVersionLine(line)
	if strings.Contains(got, "http") {
		t.Fatalf("sanitizeVersionLine 仍保留 URL：%q", got)
	}
	if strings.Contains(got, "  ") {
		t.Fatalf("sanitizeVersionLine 未折叠多余空白：%q", got)
	}
}

// TestParseJavaVersionMetadata 覆盖版本元数据 javaVersion 段的解析。
func TestParseJavaVersionMetadata(t *testing.T) {
	t.Run("完整声明", func(t *testing.T) {
		meta, appErr := ParseJavaVersionMetadata([]byte(`{"id":"1.21.4","javaVersion":{"majorVersion":21,"component":"java-runtime-gamma"}}`))
		if appErr != nil {
			t.Fatalf("不应返回错误：%v", appErr)
		}
		if meta.MajorVersion != 21 || meta.Component != "java-runtime-gamma" {
			t.Fatalf("meta = %+v, want 21/java-runtime-gamma", meta)
		}
	})

	t.Run("缺少 javaVersion", func(t *testing.T) {
		meta, appErr := ParseJavaVersionMetadata([]byte(`{"id":"1.7.10"}`))
		if appErr != nil {
			t.Fatalf("缺少 javaVersion 不应报错：%v", appErr)
		}
		if meta.MajorVersion != 0 || meta.Component != "" {
			t.Fatalf("meta = %+v, want 零值", meta)
		}
	})

	t.Run("缺少 majorVersion", func(t *testing.T) {
		meta, appErr := ParseJavaVersionMetadata([]byte(`{"javaVersion":{"component":"java-runtime-alpha"}}`))
		if appErr != nil {
			t.Fatalf("缺少 majorVersion 不应报错：%v", appErr)
		}
		if meta.MajorVersion != 0 || meta.Component != "java-runtime-alpha" {
			t.Fatalf("meta = %+v, want 0/java-runtime-alpha", meta)
		}
	})

	t.Run("非法 JSON", func(t *testing.T) {
		_, appErr := ParseJavaVersionMetadata([]byte(`{not json`))
		if appErr == nil || appErr.Code != ErrorCodeVersionMetadata {
			t.Fatalf("appErr = %v, want %s", appErr, ErrorCodeVersionMetadata)
		}
	})
}

// TestBuildRuntimeRequirementMetadata 覆盖元数据明确声明时的映射。
func TestBuildRuntimeRequirementMetadata(t *testing.T) {
	requirement := BuildRuntimeRequirement(JavaVersionMetadata{MajorVersion: 21, Component: "java-runtime-gamma"})
	if requirement.Source != RequirementSourceMetadata {
		t.Fatalf("Source = %q, want %q", requirement.Source, RequirementSourceMetadata)
	}
	if requirement.Major != 21 || requirement.Component != "java-runtime-gamma" {
		t.Fatalf("requirement = %+v, want 21/java-runtime-gamma", requirement)
	}
	if requirement.Platform == "" || requirement.Arch == "" || requirement.Bitness == "" {
		t.Fatalf("平台字段不应为空：%+v", requirement)
	}
	if len(requirement.Warnings) != 0 {
		t.Fatalf("声明明确时不应有告警：%+v", requirement.Warnings)
	}
}

// TestBuildRuntimeRequirementUnknown 覆盖元数据缺失时「只标未知、不做猜测」。
func TestBuildRuntimeRequirementUnknown(t *testing.T) {
	for _, major := range []int{0, -1} {
		requirement := BuildRuntimeRequirement(JavaVersionMetadata{MajorVersion: major})
		if requirement.Source != RequirementSourceUnknown {
			t.Fatalf("major=%d 时 Source = %q, want %q", major, requirement.Source, RequirementSourceUnknown)
		}
		if requirement.Major != major {
			t.Fatalf("major=%d 时 requirement.Major = %d（不得猜一个值）", major, requirement.Major)
		}
		if len(requirement.Warnings) != 1 || requirement.Warnings[0].Code != WarningJavaVersionUnknown {
			t.Fatalf("major=%d 时应带 JAVA_VERSION_UNKNOWN 告警：%+v", major, requirement.Warnings)
		}
		if requirement.Warnings[0].Message == "" {
			t.Fatal("告警必须带用户可见文案")
		}
	}
}

// TestResolveRuntimeRequirementRejectsEmptyVersionID 覆盖空版本 id。
func TestResolveRuntimeRequirementRejectsEmptyVersionID(t *testing.T) {
	_, appErr := ResolveRuntimeRequirement(RuntimeRequirementRequest{VersionID: "   "})
	if appErr == nil || appErr.Code != ErrorCodeVersionMetadata {
		t.Fatalf("appErr = %v, want %s", appErr, ErrorCodeVersionMetadata)
	}
}

// TestResolveRuntimeRequirementReadsMetadata 覆盖从临时数据根读取元数据。
//
// 只读写本地文件，不启动任何子进程。
func TestResolveRuntimeRequirementReadsMetadata(t *testing.T) {
	root := t.TempDir()
	versionID := "1.21.4"
	targetDir := filepath.Join(root, "versions", versionID)
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatalf("创建临时目录失败：%v", err)
	}
	document := `{"id":"` + versionID + `","javaVersion":{"majorVersion":21,"component":"java-runtime-gamma"}}`
	if err := os.WriteFile(filepath.Join(targetDir, versionID+".json"), []byte(document), 0o644); err != nil {
		t.Fatalf("写入临时元数据失败：%v", err)
	}

	restore := runtimeDataRoot
	runtimeDataRoot = func() (string, *AppError) { return root, nil }
	t.Cleanup(func() { runtimeDataRoot = restore })

	requirement, appErr := ResolveRuntimeRequirement(RuntimeRequirementRequest{VersionID: versionID})
	if appErr != nil {
		t.Fatalf("不应返回错误：%v", appErr)
	}
	if requirement.Source != RequirementSourceMetadata || requirement.Major != 21 {
		t.Fatalf("requirement = %+v, want metadata/21", requirement)
	}
}

// TestResolveRuntimeRequirementMissingMetadata 覆盖元数据文件不存在。
func TestResolveRuntimeRequirementMissingMetadata(t *testing.T) {
	restore := runtimeDataRoot
	runtimeDataRoot = func() (string, *AppError) { return t.TempDir(), nil }
	t.Cleanup(func() { runtimeDataRoot = restore })

	_, appErr := ResolveRuntimeRequirement(RuntimeRequirementRequest{VersionID: "1.21.4"})
	if appErr == nil || appErr.Code != ErrorCodeVersionMetadata {
		t.Fatalf("appErr = %v, want %s", appErr, ErrorCodeVersionMetadata)
	}
}

// TestEvaluateCompatibility 覆盖架构/位数兼容判定。
func TestEvaluateCompatibility(t *testing.T) {
	t.Run("架构未知", func(t *testing.T) {
		compatible, reason := evaluateCompatibility(JavaVersionInfo{Arch: ArchUnknown})
		if compatible || reason == "" {
			t.Fatalf("架构未知时应不兼容并给出原因：%v/%q", compatible, reason)
		}
	})

	t.Run("32 位 Java", func(t *testing.T) {
		compatible, reason := evaluateCompatibility(JavaVersionInfo{Arch: ArchX86, Bitness: Bitness32})
		if compatible || !strings.Contains(reason, "32 位") {
			t.Fatalf("32 位 Java 应不兼容并提示位数：%v/%q", compatible, reason)
		}
	})

	t.Run("架构不一致", func(t *testing.T) {
		other := ArchX64
		if currentArch() == ArchX64 {
			other = ArchAarch64
		}
		compatible, reason := evaluateCompatibility(JavaVersionInfo{Arch: other, Bitness: Bitness64})
		if compatible || reason == "" {
			t.Fatalf("架构不一致时应不兼容并给出原因：%v/%q", compatible, reason)
		}
	})

	t.Run("当前架构 64 位", func(t *testing.T) {
		compatible, reason := evaluateCompatibility(JavaVersionInfo{Arch: currentArch(), Bitness: currentBitness()})
		if !compatible || reason != "" {
			t.Fatalf("当前架构应兼容且无原因：%v/%q", compatible, reason)
		}
	})
}

// TestDisplayNameFor 覆盖脱敏展示名。
func TestDisplayNameFor(t *testing.T) {
	cases := []struct {
		name string
		info JavaVersionInfo
		want string
	}{
		{name: "Adoptium 21", info: JavaVersionInfo{Vendor: VendorAdoptium, MajorVersion: 21, Arch: ArchX64}, want: "Adoptium 21 (x64)"},
		{name: "未知厂商", info: JavaVersionInfo{Vendor: VendorUnknown, MajorVersion: 17, Arch: ArchX64}, want: "Java 17 (x64)"},
		{name: "版本未知", info: JavaVersionInfo{Vendor: VendorAdoptium, MajorVersion: 0, Arch: ArchX64}, want: "Adoptium 未知版本 (x64)"},
		{name: "架构未知", info: JavaVersionInfo{Vendor: VendorOracle, MajorVersion: 8, Arch: ArchUnknown}, want: "Oracle 8 (未知架构)"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := displayNameFor(testCase.info); got != testCase.want {
				t.Fatalf("displayNameFor = %q, want %q", got, testCase.want)
			}
		})
	}
}

// TestRuntimeIDFor 覆盖 runtimeId 的稳定性与不泄露路径。
func TestRuntimeIDFor(t *testing.T) {
	info := JavaVersionInfo{FullVersion: "21.0.1", Arch: ArchX64}
	first := runtimeIDFor(`C:\Program Files\Java\jdk-21\bin\java.exe`, info)
	second := runtimeIDFor(`C:\Program Files\Java\jdk-21\bin\java.exe`, info)
	if first != second {
		t.Fatalf("同一路径同一版本应得到相同 id：%q vs %q", first, second)
	}
	other := runtimeIDFor(`C:\Program Files\Java\jdk-17\bin\java.exe`, info)
	if first == other {
		t.Fatal("不同路径不应得到相同 id")
	}
	if strings.Contains(strings.ToLower(first), "program") || strings.Contains(first, ":") {
		t.Fatalf("id 不应包含路径片段：%q", first)
	}
	if !strings.HasPrefix(first, "local-") {
		t.Fatalf("id 应以 local- 开头：%q", first)
	}
}

// TestClassifyExecFailure 覆盖执行失败的分类（不启动任何进程）。
func TestClassifyExecFailure(t *testing.T) {
	t.Run("成功", func(t *testing.T) {
		if appErr := classifyExecFailure(nil, ""); appErr != nil {
			t.Fatalf("runErr 为 nil 时应返回 nil，实际 %v", appErr)
		}
	})

	t.Run("权限拒绝", func(t *testing.T) {
		permissionErr := &fs.PathError{Op: "fork", Path: "java", Err: fs.ErrPermission}
		appErr := classifyExecFailure(permissionErr, "")
		if appErr == nil || appErr.Code != ErrorCodeProbePermission {
			t.Fatalf("appErr = %v, want %s", appErr, ErrorCodeProbePermission)
		}
		if appErr.Retryable {
			t.Fatal("权限拒绝不应标记为可重试")
		}
	})

	t.Run("文件不存在", func(t *testing.T) {
		appErr := classifyExecFailure(&fs.PathError{Op: "fork", Path: "java", Err: fs.ErrNotExist}, "")
		if appErr == nil || appErr.Code != ErrorCodeProbeNotJava {
			t.Fatalf("appErr = %v, want %s", appErr, ErrorCodeProbeNotJava)
		}
	})

	t.Run("启动失败", func(t *testing.T) {
		appErr := classifyExecFailure(errors.New("exec format error"), "")
		if appErr == nil || appErr.Code != ErrorCodeProbeNotJava {
			t.Fatalf("appErr = %v, want %s", appErr, ErrorCodeProbeNotJava)
		}
	})
}

// TestCappedBufferLimits 覆盖输出上限的截断行为。
func TestCappedBufferLimits(t *testing.T) {
	buffer := &cappedBuffer{limit: 8}
	written, err := buffer.Write([]byte("12345"))
	if err != nil || written != 5 {
		t.Fatalf("首次写入 = %d/%v, want 5/nil", written, err)
	}
	written, err = buffer.Write([]byte("67890"))
	if err != nil || written != 5 {
		t.Fatalf("超限写入应报告完整长度，实际 %d/%v", written, err)
	}
	if buffer.String() != "12345678" {
		t.Fatalf("截断后内容 = %q, want 12345678", buffer.String())
	}
	if !buffer.truncated {
		t.Fatal("超限写入应标记 truncated")
	}
}

// TestEnumerateJavaCandidatesOnlyUsesExplicitPath 覆盖候选来源只有显式配置。
//
// 回归目标：探测曾按「显式配置 → JAVA_HOME → PATH → 常见安装根」枚举，
// 会让界面列出用户从未选择过的外部 JDK，甚至让启动参数悄悄指向它。
// 现在只允许显式配置，即使环境变量与常见安装根都指向真实可执行文件也必须忽略。
func TestEnumerateJavaCandidatesOnlyUsesExplicitPath(t *testing.T) {
	root := t.TempDir()
	javaPath := filepath.Join(root, "bin", javaExecutableName())
	if err := os.MkdirAll(filepath.Join(root, "bin"), 0o755); err != nil {
		t.Fatalf("创建目录失败：%v", err)
	}
	if err := os.WriteFile(javaPath, []byte("stub"), 0o755); err != nil {
		t.Fatalf("创建占位文件失败：%v", err)
	}

	previous := preferredJavaPathProvider
	t.Cleanup(func() { SetPreferredJavaPathProvider(previous) })

	// 三个自动来源都指向含 java 的目录：若仍被读取，候选数必然大于 1。
	t.Setenv("JAVA_HOME", root)
	t.Setenv("PATH", filepath.Join(root, "bin"))
	t.Setenv("ProgramFiles", root)

	SetPreferredJavaPathProvider(func() string { return javaPath })

	candidates := enumerateJavaCandidates()
	if len(candidates) != 1 {
		t.Fatalf("候选数 = %d，期望只有显式配置的 1 个：%v", len(candidates), candidates)
	}
	if candidates[0].origin != "preferred" {
		t.Fatalf("候选来源 = %q，期望 preferred", candidates[0].origin)
	}
	if !filepath.IsAbs(candidates[0].path) {
		t.Fatalf("候选必须是绝对路径：%s", candidates[0].path)
	}

	// 未注入 provider 时不应产生任何候选：没有可用的 Java 是正常状态。
	SetPreferredJavaPathProvider(nil)
	if got := enumerateJavaCandidates(); len(got) != 0 {
		t.Fatalf("未指定路径时候选数 = %d，期望 0：%v", len(got), got)
	}
}
