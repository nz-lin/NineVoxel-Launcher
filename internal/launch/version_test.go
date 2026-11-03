package launch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// 测试用的最小版本 JSON 构造辅助。
//
// 所有测试文件都写在 `t.TempDir()` 内，**不触碰任何真实游戏目录**。

// writeVersionJSON 在 root 下写入版本 JSON，返回版本目录路径。
//
// 参数 t 为测试实例；root 为临时游戏根；id 为版本 id；content 为 JSON 文本。
func writeVersionJSON(t *testing.T, root, id, content string) {
	t.Helper()

	dir := filepath.Join(root, dirVersions, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("创建版本目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, id+".json"), []byte(content), 0o600); err != nil {
		t.Fatalf("写入版本 JSON 失败: %v", err)
	}
}

// writeClientJar 在版本目录内写入客户端 jar 占位文件。
//
// 参数 t 为测试实例；root 为临时游戏根；id 为版本目录名；jarName 为 jar 文件名（不含扩展名）。
func writeClientJar(t *testing.T, root, id, jarName string) {
	t.Helper()

	path := filepath.Join(root, dirVersions, id, jarName+".jar")
	if err := os.WriteFile(path, []byte("jar-placeholder"), 0o600); err != nil {
		t.Fatalf("写入客户端 jar 失败: %v", err)
	}
}

// writeLibraryJar 在 libraries/ 下写入库占位文件（按相对路径）。
//
// 参数 t 为测试实例；root 为临时游戏根；relative 为相对路径（正斜杠）。
func writeLibraryJar(t *testing.T, root, relative string) {
	t.Helper()

	normalized := filepath.FromSlash(relative)
	path := filepath.Join(root, normalized)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("创建库目录失败: %v", err)
	}
	if err := os.WriteFile(path, []byte("lib-placeholder"), 0o600); err != nil {
		t.Fatalf("写入库文件失败: %v", err)
	}
}

func TestInheritedClientJarFallbackForForgeStyleVersion(t *testing.T) {
	root := t.TempDir()
	writeVersionJSON(t, root, "1.20.1", `{"id":"1.20.1","mainClass":"net.minecraft.client.main.Main"}`)
	writeClientJar(t, root, "1.20.1", "1.20.1")
	writeVersionJSON(t, root, "1.20.1-forge-47.3.12", `{"id":"1.20.1-forge-47.3.12","inheritsFrom":"1.20.1","mainClass":"cpw.mods.bootstraplauncher.BootstrapLauncher"}`)

	outcome := loadMergedVersion(root, "1.20.1-forge-47.3.12")
	if outcome.merged == nil {
		t.Fatalf("load merged version failed: %+v", outcome)
	}
	path := resolveClientJarPath(root, outcome.merged)
	expected := filepath.Join(root, dirVersions, "1.20.1", "1.20.1.jar")
	if path != expected {
		t.Fatalf("client jar = %q, want %q", path, expected)
	}
	status, reason, present, _, _, _, _, _, _ := judgeVersionLaunchability(root, outcome.merged)
	if !present || status != versionStatusLaunchSupported {
		t.Fatalf("launchability = %q, %q, present=%v", status, reason, present)
	}
}

// minimalVersionJSON 生成一个最小可解析的版本 JSON。
//
// 参数 id 为版本 id；extra 为附加 JSON 片段（不含首尾花括号）。
func minimalVersionJSON(id, extra string) string {
	body := `"id":"` + id + `","type":"release","mainClass":"net.minecraft.client.main.Main"`
	if extra != "" {
		body += "," + extra
	}
	return "{" + body + "}"
}

// TestIsVersionIDValid 验证版本 id 合法性规则（卡面第 1 条）。
func TestIsVersionIDValid(t *testing.T) {
	valid := []string{"1.21", "1.7.10-forge-1614", "1.16.5", "custom_pack.v2", "A1"}
	for _, id := range valid {
		if !isVersionIDValid(id) {
			t.Fatalf("合法 id %q 被判为非法", id)
		}
	}

	invalid := []string{"", ".", "..", "a/b", `a\b`, "a b", "版本", strings.Repeat("a", 129), "1.21\n"}
	for _, id := range invalid {
		if isVersionIDValid(id) {
			t.Fatalf("非法 id %q 被判为合法", id)
		}
	}
}

// TestListVersionDirectoriesFiltersInvalid 验证枚举会跳过非法目录名且不递归。
func TestListVersionDirectoriesFiltersInvalid(t *testing.T) {
	root := t.TempDir()

	// 两个合法版本 + 一个非法目录名 + 一个多余层级（验证不递归）。
	writeVersionJSON(t, root, "1.21", minimalVersionJSON("1.21", ""))
	writeVersionJSON(t, root, "1.20", minimalVersionJSON("1.20", ""))

	badDir := filepath.Join(root, dirVersions, "bad name")
	if err := os.MkdirAll(badDir, 0o755); err != nil {
		t.Fatalf("创建非法目录失败: %v", err)
	}

	nested := filepath.Join(root, dirVersions, "1.21", "nested-version")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("创建嵌套目录失败: %v", err)
	}

	validIDs, skipped, err := listVersionDirectories(root)
	if err != nil {
		t.Fatalf("枚举版本目录失败: %v", err)
	}

	if len(validIDs) != 2 {
		t.Fatalf("合法版本数期望 2，实际 %d（%v）", len(validIDs), validIDs)
	}
	// 嵌套目录不应出现在顶层列表中。
	for _, id := range validIDs {
		if id == "nested-version" {
			t.Fatal("枚举不应递归到子目录")
		}
	}
	if len(skipped) != 1 || skipped[0] != "bad name" {
		t.Fatalf("被跳过的非法目录期望 [bad name]，实际 %v", skipped)
	}
}

// TestListVersionDirectoriesMissingVersionsDir 验证 versions 缺失时报 GAME_DIR_001。
func TestListVersionDirectoriesMissingVersionsDir(t *testing.T) {
	root := t.TempDir()

	_, _, err := listVersionDirectories(root)
	if err == nil {
		t.Fatal("versions 缺失期望返回错误")
	}
	if err.Code != ErrorCodeGameDir {
		t.Fatalf("错误码期望 %q，实际 %q", ErrorCodeGameDir, err.Code)
	}
}

// TestReadVersionJSONRejectsOversized 验证超过大小上限的 JSON 被拒绝。
//
// 构造一个超过 8 MiB 的 JSON，断言判为 broken 且带 VERSION_METADATA_001。
func TestReadVersionJSONRejectsOversized(t *testing.T) {
	root := t.TempDir()
	id := "huge"

	// 构造远超上限的内容（合法 JSON 结构但体积极大）。
	padding := strings.Repeat("x", maxVersionJSONBytes+1024)
	content := `{"id":"huge","type":"release","mainClass":"Main","padding":"` + padding + `"}`

	writeVersionJSON(t, root, id, content)

	_, code, readErr := readVersionJSON(root, id)
	if readErr == nil {
		t.Fatal("超大 JSON 期望被拒绝")
	}
	if code != ErrorCodeVersionMetadata {
		t.Fatalf("错误码期望 %q，实际 %q", ErrorCodeVersionMetadata, code)
	}
}

// TestReadVersionJSONRejectsInvalidSyntax 验证 JSON 语法错误不 panic、判 broken。
func TestReadVersionJSONRejectsInvalidSyntax(t *testing.T) {
	root := t.TempDir()
	id := "broken-json"

	writeVersionJSON(t, root, id, `{"id": "broken-json", "type": `)

	_, code, readErr := readVersionJSON(root, id)
	if readErr == nil {
		t.Fatal("语法错误期望被拒绝")
	}
	if code != ErrorCodeVersionMetadata {
		t.Fatalf("错误码期望 %q，实际 %q", ErrorCodeVersionMetadata, code)
	}
}

// TestReadVersionJSONMissingFile 验证目录内缺少同名 JSON 时判 broken。
func TestReadVersionJSONMissingFile(t *testing.T) {
	root := t.TempDir()
	id := "no-json"

	dir := filepath.Join(root, dirVersions, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("创建目录失败: %v", err)
	}

	_, code, readErr := readVersionJSON(root, id)
	if readErr == nil {
		t.Fatal("缺少 JSON 期望返回错误")
	}
	if code != ErrorCodeVersionMetadata {
		t.Fatalf("错误码期望 %q，实际 %q", ErrorCodeVersionMetadata, code)
	}
}

// TestLoadMergedVersionDetectsCycle 验证继承环被判 broken。
//
// 构造 A→B→A 的双向环，断言 status=broken 且 errorCode=VERSION_METADATA_001。
func TestLoadMergedVersionDetectsCycle(t *testing.T) {
	root := t.TempDir()

	writeVersionJSON(t, root, "cycle-a", `{"id":"cycle-a","type":"release","mainClass":"Main","inheritsFrom":"cycle-b"}`)
	writeVersionJSON(t, root, "cycle-b", `{"id":"cycle-b","type":"release","inheritsFrom":"cycle-a"}`)

	outcome := loadMergedVersion(root, "cycle-a")
	if outcome.status != versionStatusBroken {
		t.Fatalf("环期望判为 broken，实际 %q（reason=%q）", outcome.status, outcome.reason)
	}
	if outcome.errorCode != ErrorCodeVersionMetadata {
		t.Fatalf("错误码期望 %q，实际 %q", ErrorCodeVersionMetadata, outcome.errorCode)
	}
	if outcome.reason != messageVersionInheritCycle {
		t.Fatalf("原因期望环检测文案，实际 %q", outcome.reason)
	}
}

// TestLoadMergedVersionDetectsSelfReference 验证自引用被判 broken。
func TestLoadMergedVersionDetectsSelfReference(t *testing.T) {
	root := t.TempDir()
	id := "self-ref"

	writeVersionJSON(t, root, id, `{"id":"self-ref","type":"release","mainClass":"Main","inheritsFrom":"self-ref"}`)

	outcome := loadMergedVersion(root, id)
	if outcome.status != versionStatusBroken {
		t.Fatalf("自引用期望判为 broken，实际 %q", outcome.status)
	}
	if outcome.reason != messageVersionInheritSelf {
		t.Fatalf("原因期望自引用文案，实际 %q", outcome.reason)
	}
}

// TestLoadMergedVersionDepthLimit 验证超过 5 层的继承链被判 unsupported。
//
// 构造一条 7 层链，断言不 panic、判 unsupported、原因是「层数超限」。
func TestLoadMergedVersionDepthLimit(t *testing.T) {
	root := t.TempDir()

	// 构造 v0 → v1 → ... → v6（共 7 个节点，超过 1+5 上限）。
	for index := 0; index < 7; index++ {
		id := "deep-" + itoaForTest(index)
		extra := ""
		if index < 6 {
			extra = `"inheritsFrom":"deep-` + itoaForTest(index+1) + `"`
		}
		mainClass := ""
		if index == 6 {
			// 最底层提供 mainClass，避免它在深度判定前就被「缺 mainClass」拦下。
			mainClass = `"mainClass":"net.minecraft.client.main.Main"`
		} else {
			mainClass = `"mainClass":"net.minecraft.client.main.Main"`
		}

		body := `"id":"` + id + `","type":"release",` + mainClass
		if extra != "" {
			body += "," + extra
		}
		writeVersionJSON(t, root, id, "{"+body+"}")
	}

	outcome := loadMergedVersion(root, "deep-0")
	if outcome.status != versionStatusUnsupported {
		t.Fatalf("超深链期望判为 unsupported，实际 %q（reason=%q）", outcome.status, outcome.reason)
	}
	if outcome.reason != messageVersionInheritTooDeep {
		t.Fatalf("原因期望层数超限文案，实际 %q", outcome.reason)
	}
}

// TestLoadMergedVersionDepthWithinLimit 验证 5 层以内的继承链能正常合并。
//
// 构造 v0 → v1 → v2（共 3 个节点），断言合并成功且 mainClass 来自子版本。
func TestLoadMergedVersionDepthWithinLimit(t *testing.T) {
	root := t.TempDir()

	writeVersionJSON(t, root, "child", `{"id":"child","type":"release","mainClass":"ChildMain","inheritsFrom":"parent"}`)
	writeVersionJSON(t, root, "parent", `{"id":"parent","type":"release","mainClass":"ParentMain","inheritsFrom":"grand"}`)
	writeVersionJSON(t, root, "grand", `{"id":"grand","type":"old_beta","mainClass":"GrandMain"}`)

	outcome := loadMergedVersion(root, "child")
	if outcome.merged == nil {
		t.Fatalf("继承链应可合并，实际失败：status=%q reason=%q", outcome.status, outcome.reason)
	}

	merged := outcome.merged
	// 标量：子版本优先。
	if merged.MainClass != "ChildMain" {
		t.Fatalf("mainClass 期望取子版本 ChildMain，实际 %q", merged.MainClass)
	}
	// 继承链应记录三个节点（子在前）。
	if len(merged.InheritChain) != 3 {
		t.Fatalf("继承链长度期望 3，实际 %d（%v）", len(merged.InheritChain), merged.InheritChain)
	}
	if merged.InheritChain[0] != "child" {
		t.Fatalf("继承链首项期望 child，实际 %q", merged.InheritChain[0])
	}
}

// TestMergeLibrariesChildOverridesParent 验证 libraries 按 name 去重、子覆盖父。
func TestMergeLibrariesChildOverridesParent(t *testing.T) {
	root := t.TempDir()

	// 子与父都声明了 net.minecraft:launchwrapper:1.12，但下载路径不同。
	writeVersionJSON(t, root, "child", `{
		"id":"child","type":"release","mainClass":"ChildMain","inheritsFrom":"parent",
		"libraries":[
			{"name":"net.minecraft:launchwrapper:1.12","downloads":{"artifact":{"path":"libraries/child-version/launchwrapper.jar"}}}
		]
	}`)
	writeVersionJSON(t, root, "parent", `{
		"id":"parent","type":"release",
		"libraries":[
			{"name":"net.minecraft:launchwrapper:1.12","downloads":{"artifact":{"path":"libraries/parent-version/launchwrapper.jar"}}},
			{"name":"org.ow2.asm:asm:9.5","downloads":{"artifact":{"path":"libraries/asm.jar"}}}
		]
	}`)

	outcome := loadMergedVersion(root, "child")
	if outcome.merged == nil {
		t.Fatalf("合并失败：%q", outcome.reason)
	}

	libraries := outcome.merged.Libraries
	if len(libraries) != 2 {
		t.Fatalf("合并后期望 2 个库（去重后），实际 %d", len(libraries))
	}

	// 同名库必须保留子版本的路径。
	for _, library := range libraries {
		if library.Name == "net.minecraft:launchwrapper:1.12" {
			if library.Downloads.Artifact.Path != "libraries/child-version/launchwrapper.jar" {
				t.Fatalf("同名库应保留子版本路径，实际 %q", library.Downloads.Artifact.Path)
			}
		}
	}
}

// TestMergeArgumentsParentFirst 验证 arguments 合并顺序为「父在前、子在后」。
func TestMergeArgumentsParentFirst(t *testing.T) {
	root := t.TempDir()

	writeVersionJSON(t, root, "child", `{
		"id":"child","type":"release","mainClass":"ChildMain","inheritsFrom":"parent",
		"arguments":{"game":["--child-arg"],"jvm":["-Dchild=1"]}
	}`)
	writeVersionJSON(t, root, "parent", `{
		"id":"parent","type":"release",
		"arguments":{"game":["--parent-arg"],"jvm":["-Dparent=1"]}
	}`)

	outcome := loadMergedVersion(root, "child")
	if outcome.merged == nil {
		t.Fatalf("合并失败：%q", outcome.reason)
	}

	gameArgs := outcome.merged.GameArguments
	if len(gameArgs) != 2 {
		t.Fatalf("合并后 game 参数期望 2 项，实际 %d", len(gameArgs))
	}

	// 第一项必须是父版本参数（父在前）。
	firstText := string(gameArgs[0])
	if !strings.Contains(firstText, "--parent-arg") {
		t.Fatalf("第一项期望父版本参数 --parent-arg，实际 %q", firstText)
	}
	secondText := string(gameArgs[1])
	if !strings.Contains(secondText, "--child-arg") {
		t.Fatalf("第二项期望子版本参数 --child-arg，实际 %q", secondText)
	}

	jvmArgs := outcome.merged.JVMMajorArguments
	if len(jvmArgs) != 2 {
		t.Fatalf("合并后 jvm 参数期望 2 项，实际 %d", len(jvmArgs))
	}
	if !strings.Contains(string(jvmArgs[0]), "-Dparent=1") {
		t.Fatalf("jvm 第一项期望父版本参数，实际 %q", string(jvmArgs[0]))
	}
}

// TestClientJarIdentifierFallback 验证客户端 jar 定位规则：`jar` 优先、缺失回退 `id`。
func TestClientJarIdentifierFallback(t *testing.T) {
	cases := []struct {
		name    string
		parsed  versionJSON
		id      string
		wantJar string
	}{
		{
			name:    "jar 与 id 不同时用 jar",
			parsed:  versionJSON{Jar: "1.7.10"},
			id:      "1.7.10-forge-1614",
			wantJar: "1.7.10",
		},
		{
			name:    "jar 缺失时回退 id",
			parsed:  versionJSON{},
			id:      "1.21",
			wantJar: "1.21",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := clientJarIdentifier(&tc.parsed, tc.id); got != tc.wantJar {
				t.Fatalf("客户端 jar 标识期望 %q，实际 %q", tc.wantJar, got)
			}
		})
	}
}

// TestMavenCoordinateToPath 验证 Maven 坐标到相对路径的推导（C-2 口径 B）。
func TestMavenCoordinateToPath(t *testing.T) {
	cases := []struct {
		coordinate string
		want       string
	}{
		{
			coordinate: "net.minecraft:launchwrapper:1.12",
			want:       "libraries/net/minecraft/launchwrapper/1.12/launchwrapper-1.12.jar",
		},
		{
			coordinate: "org.ow2.asm:asm:9.5",
			want:       "libraries/org/ow2/asm/asm/9.5/asm-9.5.jar",
		},
		{
			// 带 classifier：文件名追加 classifier。
			coordinate: "org.lwjgl:lwjgl:3.3.1:natives-windows",
			want:       "libraries/org/lwjgl/lwjgl/3.3.1/lwjgl-3.3.1-natives-windows.jar",
		},
		{
			// 段数不足：无法推导。
			coordinate: "badcoordinate",
			want:       "",
		},
		{
			// 坐标含路径分隔符：拒绝（防穿越）。
			coordinate: "net.minecraft:../evil:1.0",
			want:       "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.coordinate, func(t *testing.T) {
			if got := mavenCoordinateToPath(tc.coordinate); got != tc.want {
				t.Fatalf("坐标 %q 推导期望 %q，实际 %q", tc.coordinate, tc.want, got)
			}
		})
	}
}

// TestLibraryRelativePathPrefersArtifactPath 验证优先使用 downloads.artifact.path。
func TestLibraryRelativePathPrefersArtifactPath(t *testing.T) {
	library := libraryJSON{
		Name: "net.minecraft:foo:1.0",
		Downloads: &libraryDownloadsJSON{
			Artifact: &downloadArtifactJSON{Path: "libraries/special/foo.jar"},
		},
	}

	got := libraryRelativePath(library)
	if got != "libraries/special/foo.jar" {
		t.Fatalf("期望优先用 artifact.path，实际 %q", got)
	}
}

// TestLibraryRelativePathAddsLibrariesPrefix 验证 artifact.path 不带 `libraries/` 前缀时会被补上。
//
// 这是真实版本 JSON 的常见形态（现代版本的每个库几乎都带 `downloads.artifact.path`），
// 路径相对 **libraries 目录** 而不是游戏根目录。不补前缀会解析到 `<游戏根>/at/...`，
// 导致所有库被误报缺失（P2 验收实测 114 个库全部缺失的根因）。
func TestLibraryRelativePathAddsLibrariesPrefix(t *testing.T) {
	library := libraryJSON{
		Name: "at.yawk.lz4:lz4-java:1.10.1",
		Downloads: &libraryDownloadsJSON{
			Artifact: &downloadArtifactJSON{Path: "at/yawk/lz4/lz4-java/1.10.1/lz4-java-1.10.1.jar"},
		},
	}

	got := libraryRelativePath(library)
	want := "libraries/at/yawk/lz4/lz4-java/1.10.1/lz4-java-1.10.1.jar"
	if got != want {
		t.Fatalf("期望补上 libraries 前缀得到 %q，实际 %q", want, got)
	}
}

// TestLibraryRelativePathNormalizesArtifactPath 验证反斜杠与前导分隔符会被规范化。
func TestLibraryRelativePathNormalizesArtifactPath(t *testing.T) {
	cases := []struct {
		name string
		path string
		want string
	}{
		{name: "反斜杠", path: `com\google\code\gson\gson\2.14.0\gson-2.14.0.jar`, want: "libraries/com/google/code/gson/gson/2.14.0/gson-2.14.0.jar"},
		{name: "前导分隔符", path: "/com/google/code/gson/gson/2.14.0/gson-2.14.0.jar", want: "libraries/com/google/code/gson/gson/2.14.0/gson-2.14.0.jar"},
		{name: "已带前缀", path: "libraries/com/google/code/gson/gson/2.14.0/gson-2.14.0.jar", want: "libraries/com/google/code/gson/gson/2.14.0/gson-2.14.0.jar"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			library := libraryJSON{
				Name:      "com.google.code.gson:gson:2.14.0",
				Downloads: &libraryDownloadsJSON{Artifact: &downloadArtifactJSON{Path: testCase.path}},
			}
			if got := libraryRelativePath(library); got != testCase.want {
				t.Fatalf("期望 %q，实际 %q", testCase.want, got)
			}
		})
	}
}

// TestLibraryAppliesToCurrentPlatformFiltersOtherPlatforms 验证跨平台库会被 rules 过滤。
//
// 版本 JSON 会把所有平台的 native 变体都列进 libraries，用 rules 限定平台。
// 不过滤会让 Windows 把 40 个 macOS/Linux 专用文件算成「缺失」（P2 验收实测）。
func TestLibraryAppliesToCurrentPlatformFiltersOtherPlatforms(t *testing.T) {
	// 无 rules 字段：适用所有平台。
	if !libraryAppliesToCurrentPlatform(libraryJSON{Name: "com.google.code.gson:gson:2.14.0"}) {
		t.Fatal("无 rules 的库应适用所有平台")
	}

	// 空 rules 数组：同样适用（官方规范缺省即 allow）。
	if !libraryAppliesToCurrentPlatform(libraryJSON{Rules: json.RawMessage(`[]`)}) {
		t.Fatal("空 rules 数组应适用所有平台")
	}

	allowOsx := libraryJSON{Rules: json.RawMessage(`[{"action":"allow","os":{"name":"osx"}}]`)}
	allowWindows := libraryJSON{Rules: json.RawMessage(`[{"action":"allow","os":{"name":"windows"}}]`)}

	switch runtime.GOOS {
	case "darwin":
		if !libraryAppliesToCurrentPlatform(allowOsx) {
			t.Fatal("macOS 上 allow osx 的库应适用")
		}
		if libraryAppliesToCurrentPlatform(allowWindows) {
			t.Fatal("macOS 上 allow windows 的库不应适用")
		}
	case "windows":
		if libraryAppliesToCurrentPlatform(allowOsx) {
			t.Fatal("Windows 上 allow osx 的库不应适用")
		}
		if !libraryAppliesToCurrentPlatform(allowWindows) {
			t.Fatal("Windows 上 allow windows 的库应适用")
		}
	}
}

// TestCheckLibrariesCountsAndMissing 验证库检查的计数与缺失清单。
//
// 关键断言：缺失清单**只含相对路径**，不含绝对路径。
func TestCheckLibrariesCountsAndMissing(t *testing.T) {
	root := t.TempDir()

	// 准备两个库：一个存在、一个缺失。
	present := "libraries/net/minecraft/launchwrapper/1.12/launchwrapper-1.12.jar"
	writeLibraryJar(t, root, present)
	missing := "libraries/org/ow2/asm/asm/9.5/asm-9.5.jar"

	libraries := []libraryJSON{
		{Name: "net.minecraft:launchwrapper:1.12"},
		{Name: "org.ow2.asm:asm:9.5"},
	}

	required, presentCount, missingList, warnings := checkLibraries(root, libraries)

	if required != 2 {
		t.Fatalf("required 期望 2，实际 %d", required)
	}
	if presentCount != 1 {
		t.Fatalf("present 期望 1，实际 %d", presentCount)
	}
	if len(missingList) != 1 {
		t.Fatalf("missing 期望 1 项，实际 %d（%v）", len(missingList), missingList)
	}
	if missingList[0] != missing {
		t.Fatalf("缺失项期望 %q，实际 %q", missing, missingList[0])
	}
	// 缺失清单不得含绝对路径。
	if filepath.IsAbs(missingList[0]) {
		t.Fatalf("缺失清单必须是相对路径，实际 %q", missingList[0])
	}
	if len(warnings) != 0 {
		t.Fatalf("期望无警告，实际 %v", warnings)
	}
}

// TestCheckLibrariesSkipsInvalidCoordinate 验证坐标无法解析时不计入 required 并给警告。
func TestCheckLibrariesSkipsInvalidCoordinate(t *testing.T) {
	root := t.TempDir()

	libraries := []libraryJSON{{Name: "invalid"}}

	required, presentCount, missingList, warnings := checkLibraries(root, libraries)

	if required != 0 {
		t.Fatalf("非法坐标不应计入 required，实际 %d", required)
	}
	if presentCount != 0 {
		t.Fatalf("非法坐标不应计入 present，实际 %d", presentCount)
	}
	if len(missingList) != 0 {
		t.Fatalf("非法坐标不应进入缺失清单，实际 %v", missingList)
	}
	if len(warnings) != 1 {
		t.Fatalf("期望 1 条警告，实际 %d 条", len(warnings))
	}
}

// TestJudgeLaunchabilityMatrix 验证三态判定矩阵。
func TestJudgeLaunchabilityMatrix(t *testing.T) {
	cases := []struct {
		name          string
		withJar       bool
		withLibraries bool
		wantStatus    string
		wantReason    string
	}{
		{
			name:          "jar 与库齐全",
			withJar:       true,
			withLibraries: true,
			wantStatus:    versionStatusLaunchSupported,
			wantReason:    "",
		},
		{
			name:          "缺客户端 jar",
			withJar:       false,
			withLibraries: true,
			wantStatus:    versionStatusMetadataOnly,
			wantReason:    reasonClientJarMissing,
		},
		{
			name:          "缺依赖库",
			withJar:       true,
			withLibraries: false,
			wantStatus:    versionStatusMetadataOnly,
			wantReason:    reasonLibrariesMissing,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			id := "v1"

			// 版本 JSON：一个库，可选是否准备好。
			libraryName := "net.minecraft:launchwrapper:1.12"
			versionContent := `{"id":"` + id + `","type":"release","mainClass":"Main","libraries":[{"name":"` + libraryName + `"}]}`
			writeVersionJSON(t, root, id, versionContent)

			if tc.withJar {
				writeClientJar(t, root, id, id)
			}
			if tc.withLibraries {
				writeLibraryJar(t, root, "libraries/net/minecraft/launchwrapper/1.12/launchwrapper-1.12.jar")
			}

			merged := &mergedVersion{
				ID:          id,
				MainClass:   "Main",
				ClientJarID: id,
				Libraries:   []libraryJSON{{Name: libraryName}},
			}

			status, reason, jarPresent, required, presentCount, missingList, needsNatives, _, _ :=
				judgeVersionLaunchability(root, merged)

			if status != tc.wantStatus {
				t.Fatalf("状态期望 %q，实际 %q", tc.wantStatus, status)
			}
			if reason != tc.wantReason {
				t.Fatalf("原因期望 %q，实际 %q", tc.wantReason, reason)
			}
			if jarPresent != tc.withJar {
				t.Fatalf("clientJarPresent 期望 %v，实际 %v", tc.withJar, jarPresent)
			}
			if required != 1 {
				t.Fatalf("required 期望 1，实际 %d", required)
			}
			if tc.withLibraries && presentCount != 1 {
				t.Fatalf("present 期望 1，实际 %d", presentCount)
			}
			if needsNatives {
				t.Fatal("无 natives 声明的版本不应要求 natives")
			}
			_ = missingList
		})
	}
}

// TestNativesMissingYieldsMetadataOnly 验证缺 natives 时判 metadata_only（D-10）。
func TestNativesMissingYieldsMetadataOnly(t *testing.T) {
	root := t.TempDir()
	id := "with-natives"

	// 库声明了 natives 映射（旧式 natives 字段）。
	versionContent := `{
		"id":"with-natives","type":"release","mainClass":"Main",
		"libraries":[{"name":"org.lwjgl:lwjgl-platform:2.9.4","natives":{"windows":"natives-windows"}}]
	}`
	writeVersionJSON(t, root, id, versionContent)
	writeClientJar(t, root, id, id)
	// 准备库文件，确保库检查通过。
	writeLibraryJar(t, root, "libraries/org/lwjgl/lwjgl-platform/2.9.4/lwjgl-platform-2.9.4.jar")
	// 刻意**不**创建 natives 目录。

	merged := &mergedVersion{
		ID:          id,
		MainClass:   "Main",
		ClientJarID: id,
		Libraries:   []libraryJSON{{Name: "org.lwjgl:lwjgl-platform:2.9.4", Natives: map[string]string{"windows": "natives-windows"}}},
	}

	status, reason, _, _, _, _, _, nativesOK, _ := judgeVersionLaunchability(root, merged)

	if status != versionStatusMetadataOnly {
		t.Fatalf("缺 natives 期望 metadata_only，实际 %q", status)
	}
	if reason != reasonNativesMissing {
		t.Fatalf("原因期望缺 natives 文案，实际 %q", reason)
	}
	if nativesOK {
		t.Fatal("natives 目录不存在时应为 false")
	}
}

// TestNativesPresentWhenDirectoryNonEmpty 验证 natives 目录非空时视为就绪。
func TestNativesPresentWhenDirectoryNonEmpty(t *testing.T) {
	root := t.TempDir()
	id := "natives-ready"

	// 创建非空 natives 目录。
	nativesDir := filepath.Join(root, dirVersions, id, "natives")
	if err := os.MkdirAll(nativesDir, 0o755); err != nil {
		t.Fatalf("创建 natives 目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nativesDir, "lwjgl.dll"), []byte("dll"), 0o600); err != nil {
		t.Fatalf("写入 natives 文件失败: %v", err)
	}

	if !nativesPresent(root, id) {
		t.Fatal("非空 natives 目录应视为就绪")
	}
}

// TestCheckVersionEndToEnd 验证单版本检查的完整链路（成功可启动）。
func TestCheckVersionEndToEnd(t *testing.T) {
	root := t.TempDir()
	id := "1.21"

	writeVersionJSON(t, root, id, `{
		"id":"1.21","type":"release","mainClass":"net.minecraft.client.main.Main",
		"javaVersion":{"majorVersion":21},
		"libraries":[{"name":"net.minecraft:launchwrapper:1.12"}]
	}`)
	writeClientJar(t, root, id, id)
	writeLibraryJar(t, root, "libraries/net/minecraft/launchwrapper/1.12/launchwrapper-1.12.jar")

	result := checkVersion(root, id)

	if result.Status != versionStatusLaunchSupported {
		t.Fatalf("状态期望 launch_supported，实际 %q（reason=%q）", result.Status, result.Reason)
	}
	if result.ErrorCode != "" {
		t.Fatalf("可启动时不应有错误码，实际 %q", result.ErrorCode)
	}
	if result.MainClass != "net.minecraft.client.main.Main" {
		t.Fatalf("mainClass 不符：%q", result.MainClass)
	}
	if result.JavaMajorVersionRequired != 21 {
		t.Fatalf("javaMajorVersionRequired 期望 21，实际 %d", result.JavaMajorVersionRequired)
	}
	if result.LibrariesRequired != 1 || result.LibrariesPresent != 1 {
		t.Fatalf("库计数期望 1/1，实际 %d/%d", result.LibrariesRequired, result.LibrariesPresent)
	}
	if len(result.LibrariesMissing) != 0 {
		t.Fatalf("缺失清单应为空，实际 %v", result.LibrariesMissing)
	}
	// 无 natives 声明时 NativesPresent 应为 nil（区分「未检查」与「不满足」）。
	if result.NativesPresent != nil {
		t.Fatalf("无 natives 声明时 NativesPresent 期望 nil，实际 %v", *result.NativesPresent)
	}
}

// TestCheckVersionMissingJarIsNotError 验证缺 jar 是业务结论而非错误。
func TestCheckVersionMissingJarIsNotError(t *testing.T) {
	root := t.TempDir()
	id := "no-jar"

	writeVersionJSON(t, root, id, minimalVersionJSON(id, ""))

	result := checkVersion(root, id)

	if result.Status != versionStatusMetadataOnly {
		t.Fatalf("缺 jar 期望 metadata_only，实际 %q", result.Status)
	}
	// 关键：缺 jar 不设置错误码。
	if result.ErrorCode != "" {
		t.Fatalf("缺 jar 不应有错误码，实际 %q", result.ErrorCode)
	}
	if result.Reason != reasonClientJarMissing {
		t.Fatalf("原因期望缺 jar 文案，实际 %q", result.Reason)
	}
}

// TestCheckVersionMissingMainClassIsUnsupported 验证缺 mainClass 判 unsupported。
func TestCheckVersionMissingMainClassIsUnsupported(t *testing.T) {
	root := t.TempDir()
	id := "no-mainclass"

	writeVersionJSON(t, root, id, `{"id":"no-mainclass","type":"release"}`)

	result := checkVersion(root, id)

	if result.Status != versionStatusUnsupported {
		t.Fatalf("缺 mainClass 期望 unsupported，实际 %q", result.Status)
	}
	if result.ErrorCode != ErrorCodeVersionMetadata {
		t.Fatalf("错误码期望 %q，实际 %q", ErrorCodeVersionMetadata, result.ErrorCode)
	}
}

// TestCheckVersionInvalidID 验证非法版本 id 直接判 unsupported（不访问文件系统）。
func TestCheckVersionInvalidID(t *testing.T) {
	root := t.TempDir()

	result := checkVersion(root, "../escape")

	if result.Status != versionStatusUnsupported {
		t.Fatalf("非法 id 期望 unsupported，实际 %q", result.Status)
	}
	if result.ErrorCode != ErrorCodeVersionMetadata {
		t.Fatalf("错误码期望 %q，实际 %q", ErrorCodeVersionMetadata, result.ErrorCode)
	}
	if result.Reason != messageVersionIDInvalid {
		t.Fatalf("原因期望非法 id 文案，实际 %q", result.Reason)
	}
}

// TestVersionChecksDoNotWriteAnything 验证版本识别对游戏目录零写入。
//
// 断言方式：检查前后目录树的完整文件清单完全一致。
func TestVersionChecksDoNotWriteAnything(t *testing.T) {
	root := t.TempDir()
	id := "readonly-check"

	writeVersionJSON(t, root, id, minimalVersionJSON(id, `"libraries":[{"name":"net.minecraft:launchwrapper:1.12"}]`))
	writeClientJar(t, root, id, id)

	before := listTree(t, root)

	// 执行列举与检查两条只读路径。
	_ = listLocalVersions(root)
	_ = checkVersion(root, id)

	after := listTree(t, root)

	if len(before) != len(after) {
		t.Fatalf("版本识别创建了文件：前 %d 项，后 %d 项\n前 %v\n后 %v", len(before), len(after), before, after)
	}
	for index := range before {
		if before[index] != after[index] {
			t.Fatalf("文件树发生变化：\n前 %v\n后 %v", before, after)
		}
	}
}

// TestListLocalVersionsReportsStatuses 验证列举结果包含三态与原因。
func TestListLocalVersionsReportsStatuses(t *testing.T) {
	root := t.TempDir()

	// 可启动版本。
	goodID := "good"
	writeVersionJSON(t, root, goodID, minimalVersionJSON(goodID, ""))
	writeClientJar(t, root, goodID, goodID)

	// 缺 jar 版本。
	missingJarID := "missing-jar"
	writeVersionJSON(t, root, missingJarID, minimalVersionJSON(missingJarID, ""))

	// 损坏 JSON 版本。
	brokenID := "broken"
	writeVersionJSON(t, root, brokenID, `{"id":`)

	result := listLocalVersions(root)

	if result.ErrorCode != "" {
		t.Fatalf("目录级不应有错误，实际 %q（%q）", result.ErrorCode, result.ErrorMessage)
	}
	if len(result.Versions) != 3 {
		t.Fatalf("期望 3 个版本，实际 %d", len(result.Versions))
	}

	byID := make(map[string]VersionSummary, len(result.Versions))
	for _, item := range result.Versions {
		byID[item.ID] = item
	}

	if byID[goodID].Status != versionStatusLaunchSupported {
		t.Fatalf("good 版本期望 launch_supported，实际 %q", byID[goodID].Status)
	}
	if byID[missingJarID].Status != versionStatusMetadataOnly {
		t.Fatalf("missing-jar 期望 metadata_only，实际 %q", byID[missingJarID].Status)
	}
	if byID[missingJarID].Reason != reasonClientJarMissing {
		t.Fatalf("missing-jar 原因不符：%q", byID[missingJarID].Reason)
	}
	if byID[brokenID].Status != versionStatusBroken {
		t.Fatalf("broken 期望 broken，实际 %q", byID[brokenID].Status)
	}
	// 展示形态不得含绝对路径。
	if filepath.IsAbs(result.GameRootDisplay) {
		t.Fatalf("GameRootDisplay 不得是绝对路径，实际 %q", result.GameRootDisplay)
	}
}

// TestVersionErrorCodeFrozen 验证 VERSION_METADATA_001 的码值与冻结表一致。
func TestVersionErrorCodeFrozen(t *testing.T) {
	if ErrorCodeVersionMetadata != "VERSION_METADATA_001" {
		t.Fatalf("常量值错误: %q", ErrorCodeVersionMetadata)
	}
}

// TestVersionStatusValuesFrozen 验证三态取值与接口冻结清单一致。
func TestVersionStatusValuesFrozen(t *testing.T) {
	cases := map[string]string{
		versionStatusLaunchSupported: "launch_supported",
		versionStatusMetadataOnly:    "metadata_only",
		versionStatusUnsupported:     "unsupported",
		versionStatusBroken:          "broken",
	}
	for constant, want := range cases {
		if constant != want {
			t.Fatalf("状态常量 %q 与冻结值 %q 不一致", constant, want)
		}
	}
}

// listTree 返回目录下所有文件的相对路径清单（排序后），用于零写入断言。
//
// 只用于测试临时目录。
func listTree(t *testing.T, root string) []string {
	t.Helper()

	names := make([]string, 0)
	walkErr := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		names = append(names, rel)
		return nil
	})
	if walkErr != nil {
		t.Fatalf("遍历临时目录失败: %v", walkErr)
	}
	return names
}

// itoaForTest 是测试用的非负整数转字符串。
func itoaForTest(value int) string {
	if value == 0 {
		return "0"
	}
	digits := make([]byte, 0, 8)
	for value > 0 {
		digits = append(digits, byte('0'+value%10))
		value /= 10
	}
	for left, right := 0, len(digits)-1; left < right; left, right = left+1, right-1 {
		digits[left], digits[right] = digits[right], digits[left]
	}
	return string(digits)
}

// 编译期断言：测试文件确实使用了 encoding/json（部分用例通过字符串构造 JSON）。
var _ = json.Valid
