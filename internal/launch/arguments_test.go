package launch

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// ===== 占位符替换 =====

// TestReplacePlaceholdersResolvesKnown 验证已知占位符被替换为解析值。
func TestReplacePlaceholdersResolvesKnown(t *testing.T) {
	table := map[string]placeholderValue{
		"version_name":   {value: "1.21"},
		"game_directory": {value: `C:\game`, isPath: true},
	}

	cases := []struct {
		name   string
		input  string
		want   string
		failed int
	}{
		{"单一占位符", "${version_name}", "1.21", 0},
		{"嵌入文本", "--version=${version_name}", "--version=1.21", 0},
		{"多个占位符", "${version_name}@${game_directory}", `1.21@C:\game`, 0},
		{"无占位符", "--demo", "--demo", 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, failed := replacePlaceholders(tc.input, table)
			if got != tc.want {
				t.Fatalf("替换结果期望 %q，实际 %q", tc.want, got)
			}
			if len(failed) != tc.failed {
				t.Fatalf("未解析数期望 %d，实际 %d（%v）", tc.failed, len(failed), failed)
			}
		})
	}
}

// TestReplacePlaceholdersReportsUnknown 验证未知占位符被报告而非静默留空。
//
// 关键断言：未解析时不返回空串，而是把原占位符文本留在结果里并报告，
// 由调用方跳过整个参数。
func TestReplacePlaceholdersReportsUnknown(t *testing.T) {
	table := map[string]placeholderValue{
		"version_name": {value: "1.21"},
	}

	input := "--width=${resolution_width}"
	got, failed := replacePlaceholders(input, table)

	if len(failed) != 1 {
		t.Fatalf("未解析数期望 1，实际 %d", len(failed))
	}
	if failed[0] != "${resolution_width}" {
		t.Fatalf("未解析占位符期望 ${resolution_width}，实际 %q", failed[0])
	}
	// 不得替换为空串：结果里必须仍保留原占位符（避免产生残缺参数）。
	if !strings.Contains(got, "${resolution_width}") {
		t.Fatalf("未解析占位符不应被清空，实际 %q", got)
	}
}

// TestReplacePlaceholdersDedupesFailures 验证同一占位符重复出现时只报告一次。
func TestReplacePlaceholdersDedupesFailures(t *testing.T) {
	table := map[string]placeholderValue{}

	_, failed := replacePlaceholders("${unknown}-${unknown}", table)
	if len(failed) != 1 {
		t.Fatalf("重复占位符应只报告 1 次，实际 %d 次", len(failed))
	}
}

// TestReplacePlaceholdersHandlesUnclosed 验证未闭合的 `${` 被原样保留，不 panic。
func TestReplacePlaceholdersHandlesUnclosed(t *testing.T) {
	table := map[string]placeholderValue{}

	got, failed := replacePlaceholders("--arg=${unclosed", table)
	if !strings.Contains(got, "${unclosed") {
		t.Fatalf("未闭合内容应原样保留，实际 %q", got)
	}
	if len(failed) != 0 {
		t.Fatalf("未闭合不算占位符，期望 0 个失败，实际 %d", len(failed))
	}
}

// ===== 旧格式切分 =====

// TestSplitLegacyArguments 验证旧格式按空白切分。
func TestSplitLegacyArguments(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  []string
	}{
		{
			name:  "空格分隔",
			input: "--username ${auth_player_name} --version ${version_name}",
			want:  []string{"--username", "${auth_player_name}", "--version", "${version_name}"},
		},
		{
			name:  "连续空白折叠",
			input: "--demo   --fullscreen",
			want:  []string{"--demo", "--fullscreen"},
		},
		{
			name:  "制表符与换行",
			input: "--demo\t--fullscreen\n--width",
			want:  []string{"--demo", "--fullscreen", "--width"},
		},
		{
			name:  "空字符串",
			input: "   ",
			want:  nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := splitLegacyArguments(tc.input)
			if len(got) != len(tc.want) {
				t.Fatalf("切分数量期望 %d，实际 %d（%v）", len(tc.want), len(got), got)
			}
			for index := range tc.want {
				if got[index] != tc.want[index] {
					t.Fatalf("第 %d 项期望 %q，实际 %q", index, tc.want[index], got[index])
				}
			}
		})
	}
}

// TestSplitLegacyArgumentsDoesNotImplementShellLexing 验证旧格式不使用 shell 词法。
//
// 关键断言：引号与转义不被解释，`|`/`>`/`$` 都是普通字符。
// 这是「不得实现 shell 词法」的代码级证据。
func TestSplitLegacyArgumentsDoesNotImplementShellLexing(t *testing.T) {
	input := `--arg="a b" --pipe=a|b --redir=a>b`

	got := splitLegacyArguments(input)

	// 引号不被解释：`--arg="a` 与 `b"` 会被空格切成两段（这是已知局限）。
	wantCount := 4
	if len(got) != wantCount {
		t.Fatalf("期望切分为 %d 段（引号不解释），实际 %d（%v）", wantCount, len(got), got)
	}
	// 引号作为普通字符保留。
	if !strings.Contains(got[0], `"a`) {
		t.Fatalf("引号应作为普通字符保留，实际 %q", got[1])
	}
	// 管道与重定向不被解释为操作符。
	if !strings.Contains(got[2], "|") {
		t.Fatalf("管道符应保留为普通字符，实际 %q", got[2])
	}
	if !strings.Contains(got[3], ">") {
		t.Fatalf("重定向符应保留为普通字符，实际 %q", got[4])
	}
}

// ===== 附加参数校验 =====

// TestValidateAdditionalRejectsControlChars 验证含换行 / NUL 的附加参数被拒绝。
func TestValidateAdditionalRejectsControlChars(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"换行", "--demo\n--other"},
		{"回车", "--demo\r--other"},
		{"NUL", "--demo\x00"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateAdditionalArguments([]string{tc.input}, categoryGame)
			if err == nil {
				t.Fatalf("含控制字符的附加参数 %q 期望被拒绝", tc.input)
			}
			if err.Code != ErrorCodeLaunchArgs {
				t.Fatalf("错误码期望 %q，实际 %q", ErrorCodeLaunchArgs, err.Code)
			}
		})
	}
}

// TestValidateAdditionalRejectsShellMeta 验证含 shell 元字符的附加参数被拒绝。
func TestValidateAdditionalRejectsShellMeta(t *testing.T) {
	cases := []string{
		"--demo|--other",
		"a&b",
		"a>b",
		"a<b",
		"a`b",
		"$(whoami)",
	}

	for _, input := range cases {
		t.Run(input, func(t *testing.T) {
			if err := validateAdditionalArguments([]string{input}, categoryGame); err == nil {
				t.Fatalf("含 shell 元字符的 %q 期望被拒绝", input)
			}
		})
	}
}

// TestValidateAdditionalRejectsEmpty 验证空附加参数被拒绝。
func TestValidateAdditionalRejectsEmpty(t *testing.T) {
	for _, input := range []string{"", "   "} {
		if err := validateAdditionalArguments([]string{input}, categoryGame); err == nil {
			t.Fatalf("空附加参数 %q 期望被拒绝", input)
		}
	}
}

// TestValidateAdditionalProtectsIdentityArgs 验证附加游戏参数不得覆盖离线身份三项。
//
// D-5 冻结要求：`--username` / `--uuid` / `--accessToken` 不得被覆盖。
func TestValidateAdditionalProtectsIdentityArgs(t *testing.T) {
	cases := []string{
		"--username=Hacker",
		"--username Hacker",
		"--username",
		"--uuid=11111111-1111-1111-1111-111111111111",
		"--accessToken=real-token",
		"--userType=msa",
		"--version=1.12.2",
		"--gameDir=C:\\other",
	}

	for _, input := range cases {
		t.Run(input, func(t *testing.T) {
			err := validateAdditionalArguments([]string{input}, categoryGame)
			if err == nil {
				t.Fatalf("覆盖受保护参数的 %q 期望被拒绝", input)
			}
			if err.Code != ErrorCodeLaunchArgs {
				t.Fatalf("错误码期望 %q，实际 %q", ErrorCodeLaunchArgs, err.Code)
			}
		})
	}
}

// TestValidateAdditionalAllowsHarmlessArgs 验证无害附加参数被接受。
func TestValidateAdditionalAllowsHarmlessArgs(t *testing.T) {
	harmless := []string{"--demo", "--fullscreen", "--width=1280"}

	if err := validateAdditionalArguments(harmless, categoryGame); err != nil {
		t.Fatalf("无害附加参数期望通过，实际错误: %v", err)
	}
}

// TestValidateAdditionalProtectsJVMClasspath 验证附加 JVM 参数不得覆盖 classpath 与库路径。
func TestValidateAdditionalProtectsJVMClasspath(t *testing.T) {
	cases := []string{
		"-Djava.library.path=C:\\x",
		"-Djava.class.path=C:\\x",
		"-cp C:\\x",
		"-classpath C:\\x",
	}

	for _, input := range cases {
		t.Run(input, func(t *testing.T) {
			err := validateAdditionalArguments([]string{input}, categoryJVM)
			if err == nil {
				t.Fatalf("覆盖 JVM 关键参数的 %q 期望被拒绝", input)
			}
		})
	}
}

// TestFindProtectedGameArgKey 验证受保护键识别的三种写法。
func TestFindProtectedGameArgKey(t *testing.T) {
	cases := []struct {
		input     string
		wantKey   string
		wantFound bool
	}{
		{"--username=Player2", "--username", true},
		{"--username Player2", "--username", true},
		{"--username", "--username", true},
		{"--demo", "", false},
		{"--width=1280", "", false},
	}

	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			key, found := findProtectedGameArgKey(tc.input)
			if found != tc.wantFound {
				t.Fatalf("命中期望 %v，实际 %v", tc.wantFound, found)
			}
			if found && key != tc.wantKey {
				t.Fatalf("命中键期望 %q，实际 %q", tc.wantKey, key)
			}
		})
	}
}

// ===== 参数元素解码 =====

// TestDecodeArgumentElementsStringAndObject 验证字符串元素与对象元素都能解码。
func TestDecodeArgumentElementsStringAndObject(t *testing.T) {
	raw := []json.RawMessage{
		json.RawMessage(`"--demo"`),
		json.RawMessage(`{"value":"--fullscreen"}`),
		json.RawMessage(`{"rules":[{"action":"allow"}],"value":"-Dflag"}`),
	}

	elements := decodeArgumentElements(raw)
	if len(elements) != 3 {
		t.Fatalf("期望 3 个元素，实际 %d", len(elements))
	}

	// 纯字符串。
	if !elements[0].IsString || elements[0].Value != "--demo" {
		t.Fatalf("第一个元素期望字符串 --demo，实际 %+v", elements[0])
	}
	// 对象带 value、无 rules。
	if elements[1].Value != "--fullscreen" || elements[1].HasRules {
		t.Fatalf("第二个元素期望 --fullscreen 且无 rules，实际 %+v", elements[1])
	}
	// 对象带 rules：标记 HasRules，由调用方转警告。
	if !elements[2].HasRules || elements[2].Value != "-Dflag" {
		t.Fatalf("第三个元素期望带 rules 的 -Dflag，实际 %+v", elements[2])
	}
}

// TestDecodeArgumentElementsArrayValue 验证对象元素的多值 value 被展开为独立参数。
func TestDecodeArgumentElementsArrayValue(t *testing.T) {
	raw := []json.RawMessage{
		json.RawMessage(`{"value":["-A","-B"]}`),
	}

	elements := decodeArgumentElements(raw)
	if len(elements) != 2 {
		t.Fatalf("多值 value 期望展开为 2 个元素，实际 %d", len(elements))
	}
	if elements[0].Value != "-A" || elements[1].Value != "-B" {
		t.Fatalf("展开值不符：%+v", elements)
	}
}

// TestExpandArgumentsExcludesElementWithoutAllowRule 验证无 allow 规则的 rules 元素被排除。
//
// 该用例原为「规则一律跳过 + warn」（P2-04 初版行为）。D-12 补强后改为最小规则判断：
// **没有 allow 规则时按官方语义默认排除**，且这是正常结果、不产生警告。
func TestExpandArgumentsExcludesElementWithoutAllowRule(t *testing.T) {
	elements := []argumentElement{
		{Value: "-Dok", IsString: true},
		// 带 rules 但没有 allow：按官方语义排除。
		{Value: "-Druled", HasRules: true, IsString: false,
			Rules: []json.RawMessage{json.RawMessage(`{"action":"disallow"}`)}},
	}

	parts, warnings, unresolved := expandArguments(elements, argumentSourceArgumentsJVM,
		map[string]placeholderValue{}, nil, nil)

	if len(parts) != 1 {
		t.Fatalf("期望只保留 1 个参数（无 allow 的被排除），实际 %d", len(parts))
	}
	if len(warnings) != 0 {
		t.Fatalf("正常规则排除不应产生警告，实际 %d 条（%+v）", len(warnings), warnings)
	}
	if len(unresolved) != 0 {
		t.Fatalf("rules 元素不算未解析占位符，期望 0，实际 %d", len(unresolved))
	}
}

// TestExpandArgumentsBlocksOnUnresolvedPlaceholder 验证含未解析占位符的参数被跳过并记 blocking。
func TestExpandArgumentsBlocksOnUnresolvedPlaceholder(t *testing.T) {
	elements := []argumentElement{{Value: "--width=${resolution_width}", IsString: true}}

	parts, warnings, unresolved := expandArguments(elements, argumentSourceArgumentsGame,
		map[string]placeholderValue{}, nil, nil)

	if len(parts) != 0 {
		t.Fatalf("含未解析占位符的参数不应写入，实际 %d 项", len(parts))
	}
	if len(unresolved) != 1 {
		t.Fatalf("期望 1 个未解析占位符，实际 %d", len(unresolved))
	}
	if len(warnings) != 1 {
		t.Fatalf("期望 1 条警告，实际 %d", len(warnings))
	}
	// 必须为 blocking：否则会把残缺参数交给进程。
	if warnings[0].Severity != warningSeverityBlocking {
		t.Fatalf("期望 blocking 警告，实际 %q", warnings[0].Severity)
	}
}

// ===== classpath 顺序 =====

// TestBuildLaunchPlanClassPathOrder 验证 classpath 顺序：客户端 JAR 在前，库在后。
//
// 卡面第 4 条明确要求该顺序；理由见 buildLaunchPlan 内注释。
func TestBuildLaunchPlanClassPathOrder(t *testing.T) {
	root := t.TempDir()
	id := "1.21"

	// 客户端 jar + 两个库（其中一个缺失）。
	writeVersionJSON(t, root, id, `{
		"id":"1.21","type":"release","mainClass":"net.minecraft.client.main.Main",
		"libraries":[
			{"name":"lib.a:alpha:1.0"},
			{"name":"lib.b:beta:1.0"}
		]
	}`)
	writeClientJar(t, root, id, id)
	writeLibraryJar(t, root, "libraries/lib/a/alpha/1.0/alpha-1.0.jar")
	// beta 缺失。

	merged := &mergedVersion{
		ID:          id,
		MainClass:   "net.minecraft.client.main.Main",
		ClientJarID: id,
		Libraries: []libraryJSON{
			{Name: "lib.a:alpha:1.0"},
			{Name: "lib.b:beta:1.0"},
		},
	}

	plan, planErr := buildLaunchPlan(root, root, id, merged, nil, nil)
	if planErr != nil {
		t.Fatalf("构建计划失败: %v", planErr)
	}

	if len(plan.ClassPathEntries) != 2 {
		t.Fatalf("classpath 期望 2 项（客户端 jar + 1 个存在的库），实际 %d（%v）",
			len(plan.ClassPathEntries), plan.ClassPathEntries)
	}

	// 第一项必须是客户端 jar（文件名 <id>.jar）。
	first := filepath.Base(plan.ClassPathEntries[0])
	if first != id+".jar" {
		t.Fatalf("classpath 首项期望客户端 jar %q，实际 %q", id+".jar", first)
	}
	// 第二项是存在的库。
	second := filepath.Base(plan.ClassPathEntries[1])
	if second != "alpha-1.0.jar" {
		t.Fatalf("classpath 第二项期望 alpha-1.0.jar，实际 %q", second)
	}
}

// TestBuildLaunchPlanWarnsMissingLibraries 验证缺失库产生 blocking 警告且 safeToLaunch=false。
func TestBuildLaunchPlanWarnsMissingLibraries(t *testing.T) {
	root := t.TempDir()
	id := "v"

	writeVersionJSON(t, root, id, `{"id":"v","type":"release","mainClass":"Main","libraries":[{"name":"lib.b:beta:1.0"}]}`)
	writeClientJar(t, root, id, id)

	merged := &mergedVersion{
		ID:          id,
		MainClass:   "Main",
		ClientJarID: id,
		Libraries:   []libraryJSON{{Name: "lib.b:beta:1.0"}},
	}

	plan, planErr := buildLaunchPlan(root, root, id, merged, nil, nil)
	if planErr != nil {
		t.Fatalf("构建计划失败: %v", planErr)
	}

	if plan.SafeToLaunch {
		t.Fatal("缺库时期望 safeToLaunch=false")
	}

	found := false
	for _, warning := range plan.Warnings {
		if warning.Severity == warningSeverityBlocking && strings.Contains(warning.Message, "依赖库缺失") {
			found = true
		}
	}
	if !found {
		t.Fatalf("期望缺库的 blocking 警告，实际 %+v", plan.Warnings)
	}

	// 警告文案不得含绝对路径。
	for _, warning := range plan.Warnings {
		if filepath.IsAbs(warning.Message) {
			t.Fatalf("警告文案不得是绝对路径: %q", warning.Message)
		}
	}
}

// TestBuildLaunchPlanEmptyClassPathIsError 验证 classpath 为空时返回 LAUNCH_ARGS_001。
func TestBuildLaunchPlanEmptyClassPathIsError(t *testing.T) {
	root := t.TempDir()
	id := "no-jar"

	writeVersionJSON(t, root, id, `{"id":"no-jar","type":"release","mainClass":"Main"}`)
	// 刻意不创建客户端 jar。

	merged := &mergedVersion{ID: id, MainClass: "Main", ClientJarID: id}

	_, planErr := buildLaunchPlan(root, root, id, merged, nil, nil)
	if planErr == nil {
		t.Fatal("classpath 为空期望返回错误")
	}
	if planErr.Code != ErrorCodeLaunchArgs {
		t.Fatalf("错误码期望 %q，实际 %q", ErrorCodeLaunchArgs, planErr.Code)
	}
}

// TestBuildLaunchPlanMissingMainClassIsError 验证缺 mainClass 返回 LAUNCH_ARGS_001。
func TestBuildLaunchPlanMissingMainClassIsError(t *testing.T) {
	root := t.TempDir()
	id := "no-main"

	merged := &mergedVersion{ID: id, ClientJarID: id}

	_, planErr := buildLaunchPlan(root, root, id, merged, nil, nil)
	if planErr == nil {
		t.Fatal("缺 mainClass 期望返回错误")
	}
	if planErr.Code != ErrorCodeLaunchArgs {
		t.Fatalf("错误码期望 %q，实际 %q", ErrorCodeLaunchArgs, planErr.Code)
	}
}

// TestBuildLaunchPlanNativesMissingBlocks 验证 natives 缺失给 blocking 警告且不创建目录（D-10）。
func TestBuildLaunchPlanNativesMissingBlocks(t *testing.T) {
	root := t.TempDir()
	id := "with-natives"

	writeVersionJSON(t, root, id, `{
		"id":"with-natives","type":"release","mainClass":"Main",
		"libraries":[{"name":"org.lwjgl:lwjgl-platform:2.9.4","natives":{"windows":"natives-windows"}}]
	}`)
	writeClientJar(t, root, id, id)
	writeLibraryJar(t, root, "libraries/org/lwjgl/lwjgl-platform/2.9.4/lwjgl-platform-2.9.4.jar")

	merged := &mergedVersion{
		ID:          id,
		MainClass:   "Main",
		ClientJarID: id,
		Libraries: []libraryJSON{{
			Name:    "org.lwjgl:lwjgl-platform:2.9.4",
			Natives: map[string]string{"windows": "natives-windows"},
		}},
	}

	plan, planErr := buildLaunchPlan(root, root, id, merged, nil, nil)
	if planErr != nil {
		t.Fatalf("构建计划失败: %v", planErr)
	}

	// natives 缺失时，`${natives_directory}` 仍然必须是一个**非空的常规路径**：
	// 版本 JSON 普遍写成 `-Djava.library.path=${natives_directory}`，
	// 空串会拼出 `/jna`、`/lwjgl` 这类指向盘符根目录的路径。
	// 因此这里期望「游戏目录下的 natives」，而不是空字符串。
	wantNatives := joinGamePath(root, dirNatives)
	if plan.NativesDirectory != wantNatives {
		t.Fatalf("natives 缺失时期望默认目录 %q，实际 %q", wantNatives, plan.NativesDirectory)
	}
	if _, statErr := os.Stat(plan.NativesDirectory); !os.IsNotExist(statErr) {
		t.Fatalf("计划构建不应创建 natives 目录，实际 stat 结果: %v", statErr)
	}
	if plan.SafeToLaunch {
		t.Fatal("natives 缺失时期望 safeToLaunch=false")
	}

	found := false
	for _, warning := range plan.Warnings {
		if warning.Severity == warningSeverityBlocking && strings.Contains(warning.Message, "natives") {
			found = true
		}
	}
	if !found {
		t.Fatalf("期望 natives 的 blocking 警告，实际 %+v", plan.Warnings)
	}

	// 关键：不得创建 natives 目录（D-10）。
	if _, statErr := os.Stat(filepath.Join(root, dirVersions, id, "natives")); !os.IsNotExist(statErr) {
		t.Fatal("构建计划不应创建 natives 目录")
	}
}

// ===== 参数长度上限 =====

// TestBuildLaunchPlanModernVersionKeepsNativesDirectoryNonEmpty 验证无 natives 声明的现代版本
// 仍然拿到一个**非空**的 natives 目录。
//
// 版本 JSON 的 JVM 参数普遍写成 `-Djava.library.path=${natives_directory}`、
// `-Djna.tmpdir=${natives_directory}` 等。现代版本（1.19+）不再声明 natives，
// 若此时把 ${natives_directory} 置空，就会拼出 `/jna`、`/lwjgl` 这种指向盘符根目录的路径
// （P2 验收缺陷 3）。因此这里断言它回落到 `<游戏目录>/natives`。
func TestBuildLaunchPlanModernVersionKeepsNativesDirectoryNonEmpty(t *testing.T) {
	root := t.TempDir()
	id := "modern"

	writeVersionJSON(t, root, id, `{
		"id":"modern","type":"release","mainClass":"Main","libraries":[]
	}`)
	writeClientJar(t, root, id, id)

	merged := &mergedVersion{
		ID:          id,
		MainClass:   "Main",
		ClientJarID: id,
	}

	plan, planErr := buildLaunchPlan(root, root, id, merged, nil, nil)
	if planErr != nil {
		t.Fatalf("构建计划失败: %v", planErr)
	}
	if !plan.SafeToLaunch {
		t.Fatalf("无 natives 声明的版本应可启动，warnings=%+v", plan.Warnings)
	}

	wantNatives := joinGamePath(root, dirNatives)
	if plan.NativesDirectory != wantNatives {
		t.Fatalf("无 natives 声明时期望 %q，实际 %q", wantNatives, plan.NativesDirectory)
	}
}

// TestBuildLaunchPlanResolvesClientIDPlaceholders 验证 1.20.5+ 的 clientid / auth_xuid 占位符有取值。
//
// 这两个占位符用于实时服务与遥测，离线模式下没有真实值也不影响启动。
// 若不给取值，参数会被判为「无法解析」并升级成 blocking，让本可启动的版本卡在预览阶段
// （P2 验收实测：`26.3` 版本因此无法启动，提示 `${auth_xuid}`、`${clientid}` 未解析）。
func TestBuildLaunchPlanResolvesClientIDPlaceholders(t *testing.T) {
	root := t.TempDir()
	id := "modern-with-clientid"

	writeVersionJSON(t, root, id, `{
		"id":"modern-with-clientid","type":"release","mainClass":"Main",
		"libraries":[],
		"arguments":{"game":["--clientId","${clientid}","--auth_xuid","${auth_xuid}"]}
	}`)
	writeClientJar(t, root, id, id)

	merged := &mergedVersion{
		ID:          id,
		MainClass:   "Main",
		ClientJarID: id,
		GameArguments: []json.RawMessage{
			json.RawMessage(`"--clientId"`),
			json.RawMessage(`"${clientid}"`),
			json.RawMessage(`"--auth_xuid"`),
			json.RawMessage(`"${auth_xuid}"`),
		},
	}

	plan, planErr := buildLaunchPlan(root, root, id, merged, nil, nil)
	if planErr != nil {
		t.Fatalf("构建计划失败: %v", planErr)
	}
	if len(plan.UnresolvedPlaceholders) != 0 {
		t.Fatalf("clientid 与 auth_xuid 应可解析，实际仍未解析: %v", plan.UnresolvedPlaceholders)
	}
	if !plan.SafeToLaunch {
		t.Fatalf("占位符已解析时应可启动，warnings=%+v", plan.Warnings)
	}

	// 占位值必须真的出现在游戏参数里，而不是参数被整条丢弃。
	values := make([]string, 0, len(plan.GameArguments))
	for _, part := range plan.GameArguments {
		values = append(values, part.Value)
	}
	joined := strings.Join(values, " ")
	if !strings.Contains(joined, placeholderClientID) {
		t.Fatalf("游戏参数应包含占位值 %q，实际 %q", placeholderClientID, joined)
	}
}

// TestBuildLaunchPlanAllowsLongArguments 验证构建计划时不再按命令行长度拒绝。
//
// 32767 字符是「交给系统时」的限制，且 Java 9 及以上可以用 `@argfile` 绕过；
// 在计划层拒绝会让装加载器的实例连预览都打不开。实际的长度校验与兜底
// 在进程层（见 argfile.go）。
func TestBuildLaunchPlanAllowsLongArguments(t *testing.T) {
	root := t.TempDir()
	id := "v"

	writeVersionJSON(t, root, id, `{"id":"v","type":"release","mainClass":"Main"}`)
	writeClientJar(t, root, id, id)

	merged := &mergedVersion{ID: id, MainClass: "Main", ClientJarID: id}

	// 构造一个远超上限的附加参数（不含 shell 元字符以便通过逐项校验）。
	huge := "-Dpad=" + strings.Repeat("x", maxTotalArgumentBytes+1024)

	plan, planErr := buildLaunchPlan(root, root, id, merged, []string{huge}, nil)
	if planErr != nil {
		t.Fatalf("超长参数不应在计划层被拒绝，实际返回 %q: %s", planErr.Code, planErr.Message)
	}

	// 参数必须原样保留，交给进程层决定用命令行还是参数文件。
	found := false
	for _, part := range plan.JVMMajorArgs {
		if part.Value == huge {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("超长附加参数未出现在启动计划中")
	}
}

// TestMaxTotalArgumentBytesFrozen 验证长度上限常量为 Windows 传统命令行上限。
func TestMaxTotalArgumentBytesFrozen(t *testing.T) {
	if maxTotalArgumentBytes != 32767 {
		t.Fatalf("上限期望 32767，实际 %d", maxTotalArgumentBytes)
	}
}

// ===== 预览脱敏 =====

// TestPreviewRedactsAbsolutePaths 验证预览 DTO 不含未脱敏绝对路径。
//
// 这是本卡最重要的安全断言（D-8 + 决策记录第六节）。
func TestPreviewRedactsAbsolutePaths(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("绝对路径脱敏断言针对 Windows 盘符路径")
	}

	root := t.TempDir()
	id := "1.21"

	writeVersionJSON(t, root, id, `{
		"id":"1.21","type":"release","mainClass":"net.minecraft.client.main.Main",
		"assets":"legacy","assetIndex":{"id":"legacy"},
		"arguments":{"jvm":["-Djava.library.path=${natives_directory}","-cp","${classpath}"],
		             "game":["--version","${version_name}","--gameDir","${game_directory}"]}
	}`)
	writeClientJar(t, root, id, id)

	preview := previewLaunchPlan(root, id, nil, nil)

	// 遍历所有字符串字段，断言不含完整绝对路径前缀。
	for _, entry := range preview.ClassPathEntries {
		if strings.Contains(entry, root) {
			t.Fatalf("classpath 预览项含完整路径: %q", entry)
		}
	}
	for _, arg := range preview.JVMMajorArgs {
		if strings.Contains(arg.Value, root) {
			t.Fatalf("JVM 参数预览含完整路径: %q", arg.Value)
		}
	}
	for _, arg := range preview.GameArguments {
		if strings.Contains(arg.Value, root) {
			t.Fatalf("游戏参数预览含完整路径: %q", arg.Value)
		}
	}
	for _, field := range []string{
		preview.NativesDirectoryDisplay,
		preview.AssetsDirectoryDisplay,
		preview.WorkingDirectoryDisplay,
	} {
		if field != "" && strings.Contains(field, root) {
			t.Fatalf("目录字段含完整路径: %q", field)
		}
	}
}

// TestPreviewIncludesOfflineIdentityNotice 验证预览带离线身份提示（D-5）。
func TestPreviewIncludesOfflineIdentityNotice(t *testing.T) {
	root := t.TempDir()
	id := "v"

	writeVersionJSON(t, root, id, `{"id":"v","type":"release","mainClass":"Main"}`)
	writeClientJar(t, root, id, id)

	preview := previewLaunchPlan(root, id, nil, nil)

	if preview.OfflineIdentityNotice == "" {
		t.Fatal("预览必须包含离线身份提示（D-5 要求 UI 明示非登录）")
	}
	if !strings.Contains(preview.OfflineIdentityNotice, "离线") {
		t.Fatalf("离线提示应明确写出「离线」，实际 %q", preview.OfflineIdentityNotice)
	}
}

// TestPreviewListsUnresolvedPlaceholders 验证未解析占位符集中列出。
//
// 注意：D-13 补强后 `${resolution_width}` / `${resolution_height}` **已解析**
// （默认 854×480），因此本用例改用真正无法解析的未知占位符。
func TestPreviewListsUnresolvedPlaceholders(t *testing.T) {
	root := t.TempDir()
	id := "v"

	// 引用 P2 无法解析的占位符（未知占位符必然无法解析）。
	writeVersionJSON(t, root, id, `{
		"id":"v","type":"release","mainClass":"Main",
		"arguments":{"game":["--unknown-arg","${some_unknown_placeholder}"]}
	}`)
	writeClientJar(t, root, id, id)

	preview := previewLaunchPlan(root, id, nil, nil)

	if len(preview.UnresolvedPlaceholders) != 1 {
		t.Fatalf("期望 1 个未解析占位符，实际 %d（%v）",
			len(preview.UnresolvedPlaceholders), preview.UnresolvedPlaceholders)
	}
	if preview.UnresolvedPlaceholders[0] != "${some_unknown_placeholder}" {
		t.Fatalf("未解析占位符期望 ${some_unknown_placeholder}，实际 %q", preview.UnresolvedPlaceholders[0])
	}
	if preview.SafeToLaunch {
		t.Fatal("存在未解析占位符时期望 safeToLaunch=false")
	}
}

// TestPreviewResolvesResolutionPlaceholders 验证分辨率占位符在预览中已解析（D-13）。
func TestPreviewResolvesResolutionPlaceholders(t *testing.T) {
	root := t.TempDir()
	id := "v"

	writeVersionJSON(t, root, id, `{
		"id":"v","type":"release","mainClass":"Main",
		"arguments":{"game":["--width","${resolution_width}","--height","${resolution_height}"]}
	}`)
	writeClientJar(t, root, id, id)

	preview := previewLaunchPlan(root, id, nil, nil)

	// 不得有任何未解析占位符。
	if len(preview.UnresolvedPlaceholders) != 0 {
		t.Fatalf("分辨率占位符应已解析，实际未解析 %v", preview.UnresolvedPlaceholders)
	}

	// 参数中应出现默认分辨率的实际数值。
	foundWidth := false
	foundHeight := false
	for _, arg := range preview.GameArguments {
		if arg.Value == strconv.Itoa(defaultResolutionWidth) {
			foundWidth = true
		}
		if arg.Value == strconv.Itoa(defaultResolutionHeight) {
			foundHeight = true
		}
	}
	if !foundWidth || !foundHeight {
		t.Fatalf("期望参数含默认分辨率 %d/%d，实际 %+v",
			defaultResolutionWidth, defaultResolutionHeight, preview.GameArguments)
	}
}

// TestPreviewAdditionalArgsRejectionSurfacesAsWarning 验证附加参数非法时预览给 blocking 警告。
func TestPreviewAdditionalArgsRejectionSurfacesAsWarning(t *testing.T) {
	root := t.TempDir()
	id := "v"

	writeVersionJSON(t, root, id, `{"id":"v","type":"release","mainClass":"Main"}`)
	writeClientJar(t, root, id, id)

	preview := previewLaunchPlan(root, id, nil, []string{"--username=Hacker"})

	if preview.SafeToLaunch {
		t.Fatal("覆盖身份参数期望 safeToLaunch=false")
	}
	found := false
	for _, warning := range preview.Warnings {
		if warning.Code == ErrorCodeLaunchArgs && warning.Severity == warningSeverityBlocking {
			found = true
		}
	}
	if !found {
		t.Fatalf("期望 LAUNCH_ARGS_001 的 blocking 警告，实际 %+v", preview.Warnings)
	}
}

// ===== 脱敏函数 =====

// TestRedactPathInArgument 验证参数值中的绝对路径被脱敏。
func TestRedactPathInArgument(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("针对 Windows 盘符路径")
	}

	cases := []struct {
		name       string
		input      string
		wantPrefix string
		wantRedact bool
	}{
		{
			name:       "带前缀的路径参数",
			input:      `-Djava.library.path=C:\game\versions\1.21\natives`,
			wantPrefix: "-Djava.library.path=",
			wantRedact: true,
		},
		{
			name:       "非路径参数不脱敏",
			input:      "-Xmx2G",
			wantPrefix: "-Xmx2G",
			wantRedact: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, changed := RedactPathInArgument(tc.input)
			if changed != tc.wantRedact {
				t.Fatalf("脱敏标志期望 %v，实际 %v", tc.wantRedact, changed)
			}
			if !strings.HasPrefix(got, tc.wantPrefix) {
				t.Fatalf("结果期望保留前缀 %q，实际 %q", tc.wantPrefix, got)
			}
			if changed && strings.Contains(got, `C:\`) {
				t.Fatalf("脱敏后仍含盘符路径: %q", got)
			}
		})
	}
}

// ===== 离线身份常量 =====

// TestOfflineIdentityConstants 验证离线占位身份的三项固定值（D-5）。
func TestOfflineIdentityConstants(t *testing.T) {
	if placeholderUsername != "Player" {
		t.Fatalf("username 占位期望 Player，实际 %q", placeholderUsername)
	}
	if placeholderAccessToken == "" {
		t.Fatal("accessToken 占位不得为空")
	}
	// 占位 token 不得长得像真实 token（长度与字符集都应是明显的假值）。
	if len(placeholderAccessToken) > 8 {
		t.Fatalf("accessToken 占位过长，可能被误认为真实 token: %q", placeholderAccessToken)
	}
	if placeholderUUID != "00000000-0000-0000-0000-000000000000" {
		t.Fatalf("UUID 占位期望全零 UUID，实际 %q", placeholderUUID)
	}
}

// TestProtectedGameArgKeys 验证受保护键集合覆盖身份与安全必需参数。
func TestProtectedGameArgKeys(t *testing.T) {
	required := []string{"--username", "--uuid", "--accessToken", "--userType", "--version"}
	for _, key := range required {
		if !protectedGameArgKeys[key] {
			t.Fatalf("受保护键集合缺少 %q（D-5 要求不可被附加参数覆盖）", key)
		}
	}
}

// ===== 零写入 =====

// TestPreviewLaunchPlanDoesNotWrite 验证预览全流程对游戏目录零写入。
func TestPreviewLaunchPlanDoesNotWrite(t *testing.T) {
	root := t.TempDir()
	id := "v"

	writeVersionJSON(t, root, id, `{
		"id":"v","type":"release","mainClass":"Main",
		"arguments":{"jvm":["-cp","${classpath}"],"game":["--version","${version_name}"]}
	}`)
	writeClientJar(t, root, id, id)

	before := listTree(t, root)
	_ = previewLaunchPlan(root, id, []string{"-Xmx2G"}, []string{"--demo"})
	after := listTree(t, root)

	if len(before) != len(after) {
		t.Fatalf("预览过程创建了文件：前 %d 项，后 %d 项", len(before), len(after))
	}
	for index := range before {
		if before[index] != after[index] {
			t.Fatalf("文件树发生变化：\n前 %v\n后 %v", before, after)
		}
	}
}

// ===== 错误码 =====

// TestArgumentErrorCodesFrozen 验证两个参数错误码与冻结表一致。
func TestArgumentErrorCodesFrozen(t *testing.T) {
	if ErrorCodeLaunchArgs != "LAUNCH_ARGS_001" {
		t.Fatalf("常量值错误: %q", ErrorCodeLaunchArgs)
	}
	if ErrorCodeLaunchArgsTooLong != "LAUNCH_ARGS_002" {
		t.Fatalf("常量值错误: %q", ErrorCodeLaunchArgsTooLong)
	}
}

// TestClassPathStringUsesPathListSeparator 验证 classpath 字符串用平台分隔符拼接。
func TestClassPathStringUsesPathListSeparator(t *testing.T) {
	entries := []string{"a.jar", "b.jar", "c.jar"}

	got := buildClassPathString(entries)
	separator := string(os.PathListSeparator)

	expected := "a.jar" + separator + "b.jar" + separator + "c.jar"
	if got != expected {
		t.Fatalf("classpath 拼接期望 %q，实际 %q", expected, got)
	}
}

// TestErrorMessageDefaults 验证错误文案不含绝对路径。
//
// 兜底断言：防止将来修改文案时不经意加入路径。
func TestErrorMessageDefaults(t *testing.T) {
	messages := []string{
		messageClassPathEmpty,
		messageAdditionalArgEmpty,
		messageAdditionalArgControlChar,
		messageAdditionalArgShellMeta,
		messageAdditionalArgOverridesJVM,
		messageNativesMissing,
		messageOfflineIdentity,
	}

	for _, message := range messages {
		if strings.Contains(message, `C:\`) || strings.Contains(message, `\\`) {
			t.Fatalf("文案不应含绝对路径: %q", message)
		}
	}
}

// 编译期断言：确认 errors 包被使用（errorCode 辅助函数由此导入）。
var _ = errors.As

// ===== D-12：arguments.rules 最小语义 =====

// ruleJSON 构造一条规则 JSON。
//
// 参数 body 为规则对象内部片段（不含花括号）。
func ruleJSON(body string) json.RawMessage {
	return json.RawMessage("{" + body + "}")
}

// TestCurrentRuntimeOSMapping 验证 OS 名称映射符合官方启动器约定。
func TestCurrentRuntimeOSMapping(t *testing.T) {
	got := currentRuntimeOS()

	// Go 的 `windows` 保持；`darwin` 映射为 `osx`。
	switch runtime.GOOS {
	case "windows":
		if got != "windows" {
			t.Fatalf("windows 映射期望 windows，实际 %q", got)
		}
	case "darwin":
		if got != "osx" {
			t.Fatalf("darwin 应映射为 osx，实际 %q", got)
		}
	default:
		if got != runtime.GOOS {
			t.Fatalf("其余平台应原样返回，期望 %q，实际 %q", runtime.GOOS, got)
		}
	}
}

// TestCurrentRuntimeArchMapping 验证架构名称映射。
func TestCurrentRuntimeArchMapping(t *testing.T) {
	cases := map[string]string{
		"amd64": "x86_64",
		"386":   "x86",
		"arm64": "arm64",
	}
	// 只断言当前运行平台的映射，避免依赖交叉编译环境。
	expected, ok := cases[runtime.GOARCH]
	if !ok {
		t.Skipf("当前架构 %q 无固定映射断言", runtime.GOARCH)
	}
	if got := currentRuntimeArch(); got != expected {
		t.Fatalf("架构映射期望 %q，实际 %q", expected, got)
	}
}

// TestFeatureEnabled 验证 feature 取值策略（D-12 + D-13）。
func TestFeatureEnabled(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{featureHasCustomResolution, true}, // D-13：注入默认分辨率，恒 true
		{featureIsDemoUser, false},         // P2 不提供 demo 模式
		{"unknown_feature", false},         // 未知特性一律 false
		{"", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := featureEnabled(tc.name); got != tc.want {
				t.Fatalf("featureEnabled(%q) = %v，期望 %v", tc.name, got, tc.want)
			}
		})
	}
}

// TestEvaluateRulesAllowOnly 验证只有 allow 规则时的判定。
func TestEvaluateRulesAllowOnly(t *testing.T) {
	rules := []json.RawMessage{
		ruleJSON(`"action":"allow","os":{"name":"` + currentRuntimeOS() + `"}`),
	}

	evaluation := evaluateElementRules(rules, argumentSourceArgumentsJVM)
	if !evaluation.keep {
		t.Fatal("allow 规则匹配本机时期望保留参数")
	}
}

// TestEvaluateRulesAllowNotMatching 验证 allow 规则不匹配时排除。
func TestEvaluateRulesAllowNotMatching(t *testing.T) {
	// 构造一个必然不匹配的 OS 名。
	otherOS := "windows"
	if currentRuntimeOS() == "windows" {
		otherOS = "linux"
	}

	rules := []json.RawMessage{
		ruleJSON(`"action":"allow","os":{"name":"` + otherOS + `"}`),
	}

	evaluation := evaluateElementRules(rules, argumentSourceArgumentsJVM)
	if evaluation.keep {
		t.Fatalf("allow 规则不匹配时期望排除参数（其他 OS=%q）", otherOS)
	}
}

// TestEvaluateRulesDisallowExcludes 验证 **disallow 匹配时排除参数**。
//
// 这是 D-12 的关键要求：初版只处理 allow，遗漏 disallow 会让本应排除的参数被加入。
func TestEvaluateRulesDisallowExcludes(t *testing.T) {
	// 同时给一条匹配的 allow 与一条匹配的 disallow：
	// 按官方语义，disallow 命中即排除，即使 allow 也命中。
	rules := []json.RawMessage{
		ruleJSON(`"action":"allow"`),
		ruleJSON(`"action":"disallow","os":{"name":"` + currentRuntimeOS() + `"}`),
	}

	evaluation := evaluateElementRules(rules, argumentSourceArgumentsJVM)
	if evaluation.keep {
		t.Fatal("disallow 匹配时期望排除参数（即使 allow 也命中）")
	}
}

// TestEvaluateRulesDisallowNotMatchingKeeps 验证 disallow 不匹配时 allow 决定结果。
func TestEvaluateRulesDisallowNotMatchingKeeps(t *testing.T) {
	otherOS := "windows"
	if currentRuntimeOS() == "windows" {
		otherOS = "linux"
	}

	rules := []json.RawMessage{
		ruleJSON(`"action":"allow"`),
		ruleJSON(`"action":"disallow","os":{"name":"` + otherOS + `"}`),
	}

	evaluation := evaluateElementRules(rules, argumentSourceArgumentsJVM)
	if !evaluation.keep {
		t.Fatal("disallow 不匹配且 allow 匹配时期望保留参数")
	}
}

// TestEvaluateRulesNoAllowRule 验证只有 disallow、没有 allow 时默认排除。
func TestEvaluateRulesNoAllowRule(t *testing.T) {
	rules := []json.RawMessage{
		ruleJSON(`"action":"disallow","os":{"name":"` + currentRuntimeOS() + `"}`),
	}

	evaluation := evaluateElementRules(rules, argumentSourceArgumentsJVM)
	if evaluation.keep {
		t.Fatal("没有 allow 规则时期望排除（官方语义：无 allow 即不生效）")
	}
}

// TestEvaluateRulesOSArch 验证 os.arch 条件判定。
func TestEvaluateRulesOSArch(t *testing.T) {
	t.Run("匹配本机架构", func(t *testing.T) {
		rules := []json.RawMessage{
			ruleJSON(`"action":"allow","os":{"arch":"` + currentRuntimeArch() + `"}`),
		}
		if evaluation := evaluateElementRules(rules, argumentSourceArgumentsJVM); !evaluation.keep {
			t.Fatal("arch 匹配本机时期望保留")
		}
	})

	t.Run("不匹配本机架构", func(t *testing.T) {
		otherArch := "x86_64"
		if currentRuntimeArch() == "x86_64" {
			otherArch = "arm64"
		}
		rules := []json.RawMessage{
			ruleJSON(`"action":"allow","os":{"arch":"` + otherArch + `"}`),
		}
		if evaluation := evaluateElementRules(rules, argumentSourceArgumentsJVM); evaluation.keep {
			t.Fatalf("arch 不匹配时期望排除（其他 arch=%q）", otherArch)
		}
	})
}

// TestEvaluateRulesFeatures 验证 features 条件判定。
//
// `has_custom_resolution` 恒 true、`is_demo_user` 恒 false（D-12 + D-13）。
func TestEvaluateRulesFeatures(t *testing.T) {
	cases := []struct {
		name     string
		ruleBody string
		wantKeep bool
	}{
		{
			name:     "has_custom_resolution 为 true 时匹配",
			ruleBody: `"action":"allow","features":{"has_custom_resolution":true}`,
			wantKeep: true,
		},
		{
			name:     "has_custom_resolution 为 false 时不匹配",
			ruleBody: `"action":"allow","features":{"has_custom_resolution":false}`,
			wantKeep: false,
		},
		{
			name:     "is_demo_user 为 true 时不匹配（P2 恒 false）",
			ruleBody: `"action":"allow","features":{"is_demo_user":true}`,
			wantKeep: false,
		},
		{
			name:     "is_demo_user 为 false 时匹配",
			ruleBody: `"action":"allow","features":{"is_demo_user":false}`,
			wantKeep: true,
		},
		{
			name:     "未知 feature 为 true 时不匹配",
			ruleBody: `"action":"allow","features":{"foo":true}`,
			wantKeep: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rules := []json.RawMessage{ruleJSON(tc.ruleBody)}
			evaluation := evaluateElementRules(rules, argumentSourceArgumentsJVM)
			if evaluation.keep != tc.wantKeep {
				t.Fatalf("判定期望 keep=%v，实际 %v", tc.wantKeep, evaluation.keep)
			}
		})
	}
}

// TestEvaluateRulesOSVersionRecordsInfoWarning 验证 os.version 不支持时记 info 警告。
//
// 关键要求（D-12）：无法判定的条件视为不匹配，且**不得静默丢弃不留痕**。
func TestEvaluateRulesOSVersionRecordsInfoWarning(t *testing.T) {
	rules := []json.RawMessage{
		ruleJSON(`"action":"allow","os":{"name":"` + currentRuntimeOS() + `","version":"^10\\."}`),
	}

	evaluation := evaluateElementRules(rules, argumentSourceArgumentsJVM)

	// os.version 无法判定 → 不匹配 → 排除。
	if evaluation.keep {
		t.Fatal("含 os.version 的规则期望排除（P2 不支持该条件）")
	}

	// 必须留下 info 级警告。
	if len(evaluation.warnings) != 1 {
		t.Fatalf("期望 1 条 info 警告，实际 %d 条", len(evaluation.warnings))
	}
	if evaluation.warnings[0].Severity != warningSeverityInfo {
		t.Fatalf("警告级别期望 info，实际 %q", evaluation.warnings[0].Severity)
	}
	if !strings.Contains(evaluation.warnings[0].Message, "os.version") {
		t.Fatalf("警告应提到 os.version，实际 %q", evaluation.warnings[0].Message)
	}
}

// TestEvaluateRulesUnparsableRecordsInfo 验证规则无法解析时记 info 警告。
func TestEvaluateRulesUnparsableRecordsInfo(t *testing.T) {
	rules := []json.RawMessage{json.RawMessage(`"not-an-object"`)}

	evaluation := evaluateElementRules(rules, argumentSourceArgumentsJVM)

	// 没有可识别的 allow → 排除。
	if evaluation.keep {
		t.Fatal("无法解析的规则期望排除")
	}
	if len(evaluation.warnings) != 1 {
		t.Fatalf("期望 1 条 info 警告，实际 %d 条", len(evaluation.warnings))
	}
	if evaluation.warnings[0].Severity != warningSeverityInfo {
		t.Fatalf("警告级别期望 info，实际 %q", evaluation.warnings[0].Severity)
	}
}

// TestEvaluateRulesUnsupportedAction 验证未知 action 记 info 并按不匹配处理。
func TestEvaluateRulesUnsupportedAction(t *testing.T) {
	rules := []json.RawMessage{ruleJSON(`"action":"maybe"`)}

	evaluation := evaluateElementRules(rules, argumentSourceArgumentsJVM)

	if evaluation.keep {
		t.Fatal("未知 action 期望排除")
	}
	if len(evaluation.warnings) != 1 || evaluation.warnings[0].Severity != warningSeverityInfo {
		t.Fatalf("期望 1 条 info 警告，实际 %+v", evaluation.warnings)
	}
}

// TestExpandArgumentsKeepsRuleMatchedElement 验证带 rules 且匹配的元素被保留。
//
// 这是 D-12 的核心行为变化：初版是「一律跳过」，现在应保留匹配项。
func TestExpandArgumentsKeepsRuleMatchedElement(t *testing.T) {
	elements := []argumentElement{{
		Value:    "-Dosflag",
		HasRules: true,
		Rules: []json.RawMessage{
			ruleJSON(`"action":"allow","os":{"name":"` + currentRuntimeOS() + `"}`),
		},
	}}

	parts, _, _ := expandArguments(elements, argumentSourceArgumentsJVM,
		map[string]placeholderValue{}, nil, nil)

	if len(parts) != 1 {
		t.Fatalf("规则匹配的元素期望保留，实际 %d 项", len(parts))
	}
	if parts[0].Value != "-Dosflag" {
		t.Fatalf("保留的参数值期望 -Dosflag，实际 %q", parts[0].Value)
	}
}

// TestExpandArgumentsExcludesRuleUnmatchedElement 验证带 rules 且不匹配的元素被排除。
func TestExpandArgumentsExcludesRuleUnmatchedElement(t *testing.T) {
	otherOS := "windows"
	if currentRuntimeOS() == "windows" {
		otherOS = "linux"
	}

	elements := []argumentElement{{
		Value:    "-Dotherflag",
		HasRules: true,
		Rules: []json.RawMessage{
			ruleJSON(`"action":"allow","os":{"name":"` + otherOS + `"}`),
		},
	}}

	parts, warnings, _ := expandArguments(elements, argumentSourceArgumentsJVM,
		map[string]placeholderValue{}, nil, nil)

	if len(parts) != 0 {
		t.Fatalf("规则不匹配的元素期望排除，实际 %d 项", len(parts))
	}
	// 正常排除**不应**产生警告（这不是异常）。
	if len(warnings) != 0 {
		t.Fatalf("正常规则排除不应产生警告，实际 %+v", warnings)
	}
}

// ===== D-13：默认分辨率 =====

// TestDefaultResolutionConstants 验证默认分辨率常量。
func TestDefaultResolutionConstants(t *testing.T) {
	if defaultResolutionWidth != 854 {
		t.Fatalf("默认宽度期望 854，实际 %d", defaultResolutionWidth)
	}
	if defaultResolutionHeight != 480 {
		t.Fatalf("默认高度期望 480，实际 %d", defaultResolutionHeight)
	}
	if maxResolutionValue != 16384 {
		t.Fatalf("覆盖上限期望 16384，实际 %d", maxResolutionValue)
	}
}

// TestResolutionPlaceholdersResolved 验证分辨率占位符已解析、不再进入未解析清单。
//
// 这是 D-13 的核心行为变化。
func TestResolutionPlaceholdersResolved(t *testing.T) {
	table := buildPlaceholderTable(&buildContext{})

	got, failed := replacePlaceholders("--width=${resolution_width}", table)
	if len(failed) != 0 {
		t.Fatalf("分辨率占位符应已解析，实际仍有 %d 个未解析（%v）", len(failed), failed)
	}
	if got != "--width=854" {
		t.Fatalf("替换结果期望 --width=854，实际 %q", got)
	}

	gotHeight, failedHeight := replacePlaceholders("${resolution_height}", table)
	if len(failedHeight) != 0 {
		t.Fatalf("高度占位符应已解析，实际仍有 %d 个未解析", len(failedHeight))
	}
	if gotHeight != "480" {
		t.Fatalf("替换结果期望 480，实际 %q", gotHeight)
	}
}

// TestValidateResolutionOverrides 验证 --width / --height 覆盖校验（D-13）。
//
// 按 D-13：允许覆盖，不列入保护参数。
func TestValidateResolutionOverrides(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{"内联合法宽度", []string{"--width=1280"}, false},
		{"内联合法高度", []string{"--height=720"}, false},
		{"空格分隔合法宽度", []string{"--width", "1280"}, false},
		{"边界值上限", []string{"--width=16384"}, false},
		{"边界值 1", []string{"--width=1"}, false},
		{"零值非法", []string{"--width=0"}, true},
		{"负值非法", []string{"--width=-100"}, true},
		{"超过上限", []string{"--height=16385"}, true},
		{"非数字", []string{"--width=abc"}, true},
		{"缺少取值", []string{"--width"}, true},
		{"无关参数不校验", []string{"--demo"}, false},
		{"多参数混合", []string{"--demo", "--width=1024", "--height=768"}, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateResolutionOverrides(tc.args)
			if tc.wantErr && err == nil {
				t.Fatalf("参数 %v 期望校验失败，实际通过", tc.args)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("参数 %v 期望校验通过，实际错误: %v", tc.args, err)
			}
			if err != nil && err.Code != ErrorCodeLaunchArgs {
				t.Fatalf("错误码期望 %q，实际 %q", ErrorCodeLaunchArgs, err.Code)
			}
		})
	}
}

// TestWidthHeightNotProtected 验证 --width / --height 不在受保护参数集合中（D-13）。
func TestWidthHeightNotProtected(t *testing.T) {
	for _, key := range []string{"--width", "--height"} {
		if protectedGameArgKeys[key] {
			t.Fatalf("%q 不应在受保护集合中（D-13 允许覆盖）", key)
		}
	}
}

// TestBuildPlanIncludesDefaultResolutionWarning 验证计划中有默认分辨率的 info 提示。
func TestBuildPlanIncludesDefaultResolutionWarning(t *testing.T) {
	root := t.TempDir()
	id := "v"

	writeVersionJSON(t, root, id, `{"id":"v","type":"release","mainClass":"Main"}`)
	writeClientJar(t, root, id, id)

	merged := &mergedVersion{ID: id, MainClass: "Main", ClientJarID: id}

	plan, planErr := buildLaunchPlan(root, root, id, merged, nil, nil)
	if planErr != nil {
		t.Fatalf("构建计划失败: %v", planErr)
	}

	found := false
	for _, warning := range plan.Warnings {
		if warning.Severity == warningSeverityInfo && strings.Contains(warning.Message, "默认分辨率") {
			found = true
		}
	}
	if !found {
		t.Fatalf("期望默认分辨率的 info 提示，实际 %+v", plan.Warnings)
	}
}

// TestBuildPlanRejectsInvalidResolutionOverride 验证非法分辨率覆盖返回 LAUNCH_ARGS_001。
func TestBuildPlanRejectsInvalidResolutionOverride(t *testing.T) {
	root := t.TempDir()
	id := "v"

	writeVersionJSON(t, root, id, `{"id":"v","type":"release","mainClass":"Main"}`)
	writeClientJar(t, root, id, id)

	merged := &mergedVersion{ID: id, MainClass: "Main", ClientJarID: id}

	_, planErr := buildLaunchPlan(root, root, id, merged, nil, []string{"--width=99999"})
	if planErr == nil {
		t.Fatal("超上限的分辨率期望返回错误")
	}
	if planErr.Code != ErrorCodeLaunchArgs {
		t.Fatalf("错误码期望 %q，实际 %q", ErrorCodeLaunchArgs, planErr.Code)
	}
}

// TestBuildPlanAllowsValidResolutionOverride 验证合法覆盖被接受且计划仍可启动。
func TestBuildPlanAllowsValidResolutionOverride(t *testing.T) {
	root := t.TempDir()
	id := "v"

	writeVersionJSON(t, root, id, `{"id":"v","type":"release","mainClass":"Main"}`)
	writeClientJar(t, root, id, id)

	merged := &mergedVersion{ID: id, MainClass: "Main", ClientJarID: id}

	// 该用例聚焦分辨率覆盖，因此不声明 natives（避免 natives 缺失的 blocking 警告干扰判断）。
	plan, planErr := buildLaunchPlan(root, root, id, merged, nil, []string{"--width=1280", "--height=720"})
	if planErr != nil {
		t.Fatalf("合法分辨率覆盖期望通过，实际错误: %v", planErr)
	}
	if !plan.SafeToLaunch {
		t.Fatalf("合法覆盖时期望 safeToLaunch=true，实际 warnings=%+v", plan.Warnings)
	}

	// 覆盖参数应作为独立数组元素追加。
	foundWidth := false
	for _, part := range plan.GameArguments {
		if part.Value == "--width=1280" && part.Source == argumentSourceLauncher {
			foundWidth = true
		}
	}
	if !foundWidth {
		t.Fatalf("覆盖参数应作为独立元素追加，实际 %+v", plan.GameArguments)
	}
}
