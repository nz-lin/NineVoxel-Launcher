package launch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 本文件的测试**只覆盖错误分支**（任务约束）：
// 严禁在本卡执行期内启动真实 Minecraft 进程。
// 因此所有用例都在「进程创建之前」就返回错误，或直接测纯函数。

// ===== natives 目录创建 =====

// TestEnsureNativesDirectoryCreatesMissingDirectory 验证 natives 目录缺失时会被创建。
//
// 这是 P2 验收实测缺陷的回归用例：`${natives_directory}\jna` 不存在会让 JNA
// 抛 `UnsatisfiedLinkError`，游戏启动后立即崩溃（Minecraft 26.3 退出码 -2）。
func TestEnsureNativesDirectoryCreatesMissingDirectory(t *testing.T) {
	root := t.TempDir()
	natives := filepath.Join(root, "natives")

	if _, statErr := os.Stat(natives); !os.IsNotExist(statErr) {
		t.Fatalf("前置条件失败：%q 不应存在", natives)
	}

	err := ensureNativesDirectory(natives)
	if err != nil {
		t.Fatalf("创建 natives 目录期望成功，实际返回错误 %q", err.Code)
	}

	info, statErr := os.Stat(natives)
	if statErr != nil {
		t.Fatalf("natives 目录期望已创建，实际 stat 失败: %v", statErr)
	}
	if !info.IsDir() {
		t.Fatalf("natives 路径期望是目录，实际不是")
	}
}

// TestEnsureNativesDirectoryKeepsExistingDirectory 验证目录已存在时重复调用幂等。
func TestEnsureNativesDirectoryKeepsExistingDirectory(t *testing.T) {
	natives := filepath.Join(t.TempDir(), "natives")
	if mkErr := os.MkdirAll(natives, 0o755); mkErr != nil {
		t.Fatalf("准备目录失败: %v", mkErr)
	}

	if err := ensureNativesDirectory(natives); err != nil {
		t.Fatalf("已存在的 natives 目录期望成功，实际返回错误 %q", err.Code)
	}
}

// TestEnsureNativesDirectoryRejectsFile 验证 natives 路径被同名文件占用时返回 LAUNCH_PROCESS_001。
func TestEnsureNativesDirectoryRejectsFile(t *testing.T) {
	root := t.TempDir()
	natives := filepath.Join(root, "natives")
	if writeErr := os.WriteFile(natives, []byte("occupied"), 0o644); writeErr != nil {
		t.Fatalf("准备同名文件失败: %v", writeErr)
	}

	err := ensureNativesDirectory(natives)
	if err == nil {
		t.Fatal("natives 路径为文件时期望返回错误，实际通过")
	}
	if err.Code != ErrorCodeLaunchProcess {
		t.Fatalf("错误码期望 %q，实际 %q", ErrorCodeLaunchProcess, err.Code)
	}
}

// TestEnsureNativesDirectorySkipsEmptyPath 验证路径为空时跳过且不创建任何目录。
//
// 空值来自「版本未声明 natives 且启动计划未提供默认值」的兼容路径，
// 此时不应凭空创建工作目录之外的目录。
func TestEnsureNativesDirectorySkipsEmptyPath(t *testing.T) {
	if err := ensureNativesDirectory(""); err != nil {
		t.Fatalf("空路径期望直接跳过，实际返回错误 %q", err.Code)
	}
	if err := ensureNativesDirectory("   "); err != nil {
		t.Fatalf("空白路径期望直接跳过，实际返回错误 %q", err.Code)
	}
}

// ===== 工作目录校验 =====

// TestValidateProcessWorkDirRejectsMissing 验证工作目录不存在时返回 PATH_INVALID_001。
//
// 关键约束（卡面第 2 条）：**不得回退到进程当前目录**。
// 该用例的存在就是为了守住「宁可失败也不走错目录」。
func TestValidateProcessWorkDirRejectsMissing(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "not-exist-game-root")

	err := validateProcessWorkDir(missing)
	if err == nil {
		t.Fatal("不存在的工作目录期望返回错误，实际通过")
	}
	if err.Code != ErrorCodePathInvalid {
		t.Fatalf("错误码期望 %q，实际 %q", ErrorCodePathInvalid, err.Code)
	}
}

// TestValidateProcessWorkDirRejectsEmpty 验证空工作目录被拒绝。
func TestValidateProcessWorkDirRejectsEmpty(t *testing.T) {
	for _, dir := range []string{"", "   "} {
		if err := validateProcessWorkDir(dir); err == nil {
			t.Fatalf("空工作目录 %q 期望被拒绝", dir)
		}
	}
}

// TestValidateProcessWorkDirRejectsFile 验证指向文件时被拒绝。
func TestValidateProcessWorkDirRejectsFile(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "a-file.txt")
	if writeErr := os.WriteFile(filePath, []byte("x"), 0o600); writeErr != nil {
		t.Fatalf("准备测试文件失败: %v", writeErr)
	}

	err := validateProcessWorkDir(filePath)
	if err == nil {
		t.Fatal("指向文件期望被拒绝")
	}
	if err.Code != ErrorCodePathInvalid {
		t.Fatalf("错误码期望 %q，实际 %q", ErrorCodePathInvalid, err.Code)
	}
}

// TestValidateProcessWorkDirAcceptsRealDirectory 验证真实目录被接受。
func TestValidateProcessWorkDirAcceptsRealDirectory(t *testing.T) {
	if err := validateProcessWorkDir(t.TempDir()); err != nil {
		t.Fatalf("真实目录期望通过，实际错误: %v", err)
	}
}

// ===== 参数组装 =====

// minimalPlan 构造一个可组装的最小计划。
//
// 参数 dir 为工作目录；classPath 为 classpath 条目。
func minimalPlan(dir string, classPath []string) *LaunchPlan {
	return &LaunchPlan{
		WorkingDirectory: dir,
		JVMMajorArgs: []ArgumentPart{
			{Value: "-Xmx2G", Source: argumentSourceLauncher},
		},
		ClassPathEntries: classPath,
		MainClass:        "net.minecraft.client.main.Main",
		GameArguments: []ArgumentPart{
			{Value: "--version", Source: argumentSourceArgumentsGame},
			{Value: "1.21", Source: argumentSourceArgumentsGame},
		},
	}
}

// TestBuildProcessArgumentsOrder 验证参数组装顺序：JVM 参数 → -cp → 主类 → 游戏参数。
//
// 顺序是 JVM 命令行语义的一部分：`-cp` 必须在主类之前，游戏参数在主类之后。
func TestBuildProcessArgumentsOrder(t *testing.T) {
	plan := minimalPlan(t.TempDir(), []string{"client.jar", "lib1.jar"})

	args, err := buildProcessArguments(plan)
	if err != nil {
		t.Fatalf("组装参数失败: %v", err)
	}

	// 断言关键位置：`-cp` 需要出现在主类之前。
	indexOfCp := -1
	indexOfMainClass := -1
	for index, arg := range args {
		if arg == "-cp" {
			indexOfCp = index
		}
		if arg == "net.minecraft.client.main.Main" {
			indexOfMainClass = index
		}
	}

	if indexOfCp < 0 {
		t.Fatalf("参数中缺少 -cp: %v", args)
	}
	if indexOfMainClass < 0 {
		t.Fatalf("参数中缺少主类: %v", args)
	}
	if indexOfCp >= indexOfMainClass {
		t.Fatalf("-cp（索引 %d）必须位于主类（索引 %d）之前", indexOfCp, indexOfMainClass)
	}

	// 第一项应是 JVM 参数。
	if args[0] != "-Xmx2G" {
		t.Fatalf("首项期望 JVM 参数 -Xmx2G，实际 %q", args[0])
	}

	// `-cp` 之后紧跟 classpath 值，且用平台分隔符。
	classPathValue := args[indexOfCp+1]
	separator := string(os.PathListSeparator)
	if !strings.Contains(classPathValue, separator) {
		t.Fatalf("classpath 应含平台分隔符 %q，实际 %q", separator, classPathValue)
	}

	// 主类之后是游戏参数。
	afterMain := args[indexOfMainClass+1:]
	if len(afterMain) == 0 || afterMain[0] != "--version" {
		t.Fatalf("主类之后应是游戏参数，实际 %v", afterMain)
	}
}

// TestBuildProcessArgumentsEmptyClassPath 验证空 classpath 被拒绝。
func TestBuildProcessArgumentsEmptyClassPath(t *testing.T) {
	plan := minimalPlan(t.TempDir(), nil)

	_, err := buildProcessArguments(plan)
	if err == nil {
		t.Fatal("空 classpath 期望返回错误")
	}
	if err.Code != ErrorCodeLaunchArgs {
		t.Fatalf("错误码期望 %q，实际 %q", ErrorCodeLaunchArgs, err.Code)
	}
}

// TestBuildProcessArgumentsMissingMainClass 验证缺主类被拒绝。
func TestBuildProcessArgumentsMissingMainClass(t *testing.T) {
	plan := minimalPlan(t.TempDir(), []string{"client.jar"})
	plan.MainClass = "   "

	_, err := buildProcessArguments(plan)
	if err == nil {
		t.Fatal("缺主类期望返回错误")
	}
	if err.Code != ErrorCodeLaunchArgs {
		t.Fatalf("错误码期望 %q，实际 %q", ErrorCodeLaunchArgs, err.Code)
	}
}

// TestBuildProcessArgumentsSkipsBlankParts 验证空白参数项被跳过。
func TestBuildProcessArgumentsSkipsBlankParts(t *testing.T) {
	plan := minimalPlan(t.TempDir(), []string{"client.jar"})
	plan.JVMMajorArgs = append(plan.JVMMajorArgs,
		ArgumentPart{Value: "", Source: argumentSourceLauncher},
		ArgumentPart{Value: "   ", Source: argumentSourceLauncher})

	args, err := buildProcessArguments(plan)
	if err != nil {
		t.Fatalf("组装参数失败: %v", err)
	}

	for _, arg := range args {
		if strings.TrimSpace(arg) == "" {
			t.Fatalf("空白参数不应进入结果: %v", args)
		}
	}
}

// ===== 超长命令行的错误分支 =====

// TestStartGameProcessRejectsTooLongArguments 验证参数总长超限时返回 LAUNCH_ARGS_002。
//
// 该用例在 `exec.Command` 之前就返回（参数总长检查在启动前），
// 因此**不会真正创建进程**——符合「严禁启动真实游戏进程」的约束。
func TestStartGameProcessRejectsTooLongArguments(t *testing.T) {
	// 构造超长 classpath：两个超长条目之和超过上限。
	longEntry := strings.Repeat("x", maxTotalArgumentBytes/2+1000)
	plan := minimalPlan(t.TempDir(), []string{longEntry, longEntry})

	_, err := StartGameProcess(plan, "C:\\fake\\java.exe")
	if err == nil {
		t.Fatal("超长参数期望返回错误")
	}
	if err.Code != ErrorCodeLaunchArgsTooLong {
		t.Fatalf("错误码期望 %q，实际 %q", ErrorCodeLaunchArgsTooLong, err.Code)
	}
	// 提示必须可读：应说明「参数过长」而不是系统原始报错。
	if !strings.Contains(err.Message, "参数过长") {
		t.Fatalf("提示应说明参数过长，实际 %q", err.Message)
	}
}

// TestStartGameProcessRejectsMissingWorkDir 验证工作目录缺失时返回 PATH_INVALID_001。
//
// 同样在进程创建之前返回，不会启动任何进程。
func TestStartGameProcessRejectsMissingWorkDir(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "gone")
	plan := minimalPlan(missing, []string{"client.jar"})

	_, err := StartGameProcess(plan, "C:\\fake\\java.exe")
	if err == nil {
		t.Fatal("工作目录缺失期望返回错误")
	}
	if err.Code != ErrorCodePathInvalid {
		t.Fatalf("错误码期望 %q，实际 %q", ErrorCodePathInvalid, err.Code)
	}
}

// TestStartGameProcessRejectsNilPlan 验证 nil 计划被拒绝（不 panic）。
func TestStartGameProcessRejectsNilPlan(t *testing.T) {
	_, err := StartGameProcess(nil, "C:\\fake\\java.exe")
	if err == nil {
		t.Fatal("nil 计划期望返回错误")
	}
	if err.Code != ErrorCodeLaunchArgs {
		t.Fatalf("错误码期望 %q，实际 %q", ErrorCodeLaunchArgs, err.Code)
	}
}

// ===== 环境变量白名单 =====

// TestProcessEnvWhitelistContainsRequiredKeys 验证白名单覆盖卡面要求的全部键。
func TestProcessEnvWhitelistContainsRequiredKeys(t *testing.T) {
	required := []string{
		"SystemRoot", "PATH", "TEMP", "TMP",
		"USERPROFILE", "APPDATA", "LOCALAPPDATA", "PROCESSOR_ARCHITECTURE",
	}

	present := make(map[string]bool, len(processEnvWhitelist))
	for _, key := range processEnvWhitelist {
		present[key] = true
	}

	for _, key := range required {
		if !present[key] {
			t.Fatalf("白名单缺少必需键 %q", key)
		}
	}
}

// TestProcessEnvWhitelistExcludesJavaHome 验证白名单**不包含** JAVA_HOME。
//
// 卡面明确要求不注入 JAVA_HOME：用户环境里的值可能指向另一个 JDK，
// 与已校验的 javaPath 矛盾。
func TestProcessEnvWhitelistExcludesJavaHome(t *testing.T) {
	for _, key := range processEnvWhitelist {
		if strings.EqualFold(key, "JAVA_HOME") {
			t.Fatal("白名单不得包含 JAVA_HOME")
		}
	}
}

// TestProcessEnvWhitelistExcludesCredentials 验证白名单不含凭据类与代理变量。
func TestProcessEnvWhitelistExcludesCredentials(t *testing.T) {
	forbidden := []string{"http_proxy", "https_proxy", "TOKEN", "PASSWORD", "SECRET"}

	for _, key := range processEnvWhitelist {
		for _, bad := range forbidden {
			if strings.EqualFold(key, bad) {
				t.Fatalf("白名单不得包含 %q", bad)
			}
		}
	}
}

// TestBuildProcessEnvDoesNotInheritAll 验证构造出的环境不含未白名单的变量。
//
// 做法：设置一个测试专用环境变量，断言它**不**出现在构造结果里。
func TestBuildProcessEnvDoesNotInheritAll(t *testing.T) {
	const probeKey = "NV_PROCESS_TEST_PROBE"
	t.Setenv(probeKey, "probe-value")

	env := buildProcessEnv()

	for _, entry := range env {
		if strings.HasPrefix(entry, probeKey+"=") {
			t.Fatalf("未白名单的环境变量 %q 不应被继承", probeKey)
		}
	}
}

// TestProcessEnvKeysForDiagnosticsNoValues 验证诊断函数只返回键名、不含值。
func TestProcessEnvKeysForDiagnosticsNoValues(t *testing.T) {
	const probeKey = "NV_ENV_KEYS_PROBE"
	t.Setenv(probeKey, "secret-value-must-not-leak")
	// 该键不在白名单里，因此不应出现。
	keys := processEnvKeysForDiagnostics()

	for _, key := range keys {
		if strings.Contains(key, "=") {
			t.Fatalf("诊断返回值应为纯键名，实际含等号: %q", key)
		}
		if strings.Contains(key, "secret-value-must-not-leak") {
			t.Fatalf("诊断返回值泄漏了值: %q", key)
		}
		if key == probeKey {
			t.Fatal("未白名单的键不应出现在诊断列表")
		}
	}
}

// ===== 超长行截断 =====

// TestTruncateProcessLine 验证超长行被截断并追加标记。
func TestTruncateProcessLine(t *testing.T) {
	long := strings.Repeat("x", processMaxLineBytes+100)

	got := truncateProcessLine(long)

	if !strings.HasSuffix(got, processTruncatedMarker) {
		t.Fatalf("截断后应带标记 %q，实际结尾为 %q", processTruncatedMarker, got[len(got)-20:])
	}
	if len(got) > processMaxLineBytes {
		t.Fatalf("截断后长度应不超过 %d，实际 %d", processMaxLineBytes, len(got))
	}
}

// TestTruncateProcessLineDoesNotSplitUTF8 验证截断不会产生半个多字节字符。
//
// 用中文（每字符 3 字节）构造，断言结果仍是合法 UTF-8。
func TestTruncateProcessLineDoesNotSplitUTF8(t *testing.T) {
	// 构造恰好会在多字节字符中间被切断的内容。
	repeatCount := processMaxLineBytes/3 + 10
	long := strings.Repeat("中", repeatCount)

	got := truncateProcessLine(long)

	// 判断是否为合法 UTF-8：非法时 strings.ToValidUTF8 会替换字符。
	cleaned := strings.ToValidUTF8(got, "")
	if cleaned != got {
		t.Fatalf("截断结果不是合法 UTF-8（含半个字符）")
	}
}

// TestTruncateProcessLineShortInput 验证短输入只追加标记不截断。
func TestTruncateProcessLineShortInput(t *testing.T) {
	got := truncateProcessLine("short line")

	if got != "short line"+processTruncatedMarker {
		t.Fatalf("短输入期望原样加标记，实际 %q", got)
	}
}

// ===== 错误码 =====

// TestLaunchProcessErrorCodeFrozen 验证 LAUNCH_PROCESS_001 码值来自冻结表。
func TestLaunchProcessErrorCodeFrozen(t *testing.T) {
	if ErrorCodeLaunchProcess != "LAUNCH_PROCESS_001" {
		t.Fatalf("常量值错误: %q", ErrorCodeLaunchProcess)
	}
}

// TestProcessErrorCodesMatchFrozenTable 验证三类错误各自使用正确的码。
//
// 卡面要求语义不混用：进程创建失败 / 工作目录无效 / 参数过长是三件事。
func TestProcessErrorCodesMatchFrozenTable(t *testing.T) {
	cases := []struct {
		name string
		code string
		want string
	}{
		{"进程创建失败", ErrorCodeLaunchProcess, "LAUNCH_PROCESS_001"},
		{"工作目录无效", ErrorCodePathInvalid, "PATH_INVALID_001"},
		{"参数过长", ErrorCodeLaunchArgsTooLong, "LAUNCH_ARGS_002"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.code != tc.want {
				t.Fatalf("错误码期望 %q，实际 %q", tc.want, tc.code)
			}
		})
	}
}

// ===== 常量 =====

// TestProcessConstantsFrozen 验证关键容量常量。
func TestProcessConstantsFrozen(t *testing.T) {
	if processMaxLineBytes != 64*1024 {
		t.Fatalf("最大行长期望 64 KiB，实际 %d", processMaxLineBytes)
	}
	if processTruncatedMarker != " <truncated>" {
		t.Fatalf("截断标记不符: %q", processTruncatedMarker)
	}
}

// TestExecBaseName 验证可执行文件名提取。
func TestExecBaseName(t *testing.T) {
	cases := map[string]string{
		`C:\Program Files\Java\jdk-21\bin\java.exe`: "java.exe",
		"/usr/lib/jvm/bin/java":                     "java",
		"java.exe":                                  "java.exe",
		"":                                          "",
	}

	for input, want := range cases {
		t.Run(input, func(t *testing.T) {
			if got := execBaseName(input); got != want {
				t.Fatalf("期望 %q，实际 %q", want, got)
			}
		})
	}
}

// TestGameProcessNilSafety 验证 GameProcess 的辅助方法对 nil 安全。
//
// P2-06 可能在收尾时持有一个未完全初始化的结构，这些方法不得 panic。
func TestGameProcessNilSafety(t *testing.T) {
	var process *GameProcess

	// 这些调用都不得 panic。
	process.WaitReads()
	if got := process.StartedAtMillis(); got != 0 {
		t.Fatalf("nil 的 StartedAtMillis 期望 0，实际 %d", got)
	}
	if got := process.StdoutPipeCount(); got != 0 {
		t.Fatalf("nil 的 StdoutPipeCount 期望 0，实际 %d", got)
	}
}

// TestWaitReadsIdempotent 验证 WaitReads 可重复调用不 panic。
func TestWaitReadsIdempotent(t *testing.T) {
	closed := make(chan struct{})
	close(closed)

	process := &GameProcess{readsDone: closed}

	// 连续调用两次：第二次应被 sync.Once 短路。
	process.WaitReads()
	process.WaitReads()
}
