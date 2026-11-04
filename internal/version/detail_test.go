// 本文件是「面向安装」版本元数据解析的单元测试。
//
// 测试约束（P3-02 卡面）：
//   - **绝不联网**：版本 JSON 一律由 os.WriteFile 写入 t.TempDir() 再读取，
//     不访问 piston-meta.mojang.com 或任何远端地址；
//   - 每个用例独立数据根，互不影响；
//   - fixture 采用真实形态（含 inheritsFrom 子父关系、rules 多平台库、
//     旧格式 minecraftArguments、natives + extract.exclude）。
package version

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 测试用 SHA-1（40 位十六进制，仅用于构造元数据，不代表真实文件）。
const (
	sha1Client     = "1111111111111111111111111111111111111111"
	sha1AssetIndex = "2222222222222222222222222222222222222222"
	sha1LibraryA   = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	sha1LibraryB   = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	sha1Native     = "cccccccccccccccccccccccccccccccccccccccc"
	sha1LogConfig  = "dddddddddddddddddddddddddddddddddddddddd"
)

// writeVersionMetadata 把版本元数据写入数据根的 `versions/<id>/<id>.json`。
func writeVersionMetadata(t *testing.T, root, id string, document map[string]any) {
	t.Helper()

	dir := filepath.Join(root, "versions", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("创建版本目录失败: %v", err)
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("序列化版本元数据失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, id+".json"), encoded, 0o644); err != nil {
		t.Fatalf("写入版本元数据失败: %v", err)
	}
}

// writeRawVersionMetadata 写入原始文本（用于构造非法 JSON 或超限内容）。
func writeRawVersionMetadata(t *testing.T, root, id, content string) {
	t.Helper()

	dir := filepath.Join(root, "versions", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("创建版本目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, id+".json"), []byte(content), 0o644); err != nil {
		t.Fatalf("写入版本元数据失败: %v", err)
	}
}

// officialArtifactURL 构造官方下载地址（host 必须在白名单内）。
func officialArtifactURL(host, sha1 string) string {
	return "https://" + host + "/v1/objects/" + sha1 + "/file"
}

// clientDownloadEntry 构造客户端 jar 的下载描述。
func clientDownloadEntry(sha1 string, size int64) map[string]any {
	return map[string]any{
		"url":  officialArtifactURL("piston-meta.mojang.com", sha1),
		"size": size,
		"sha1": sha1,
	}
}

// assetIndexDocument 构造资源索引描述。
func assetIndexDocument(id, sha1 string, size int64) map[string]any {
	return map[string]any{
		"id":        id,
		"sha1":      sha1,
		"size":      size,
		"totalSize": size * 2,
		"url":       officialArtifactURL("piston-meta.mojang.com", sha1),
	}
}

// libraryDocument 构造带完整下载信息的库条目。
func libraryDocument(name, sha1 string, size int64) map[string]any {
	return map[string]any{
		"name": name,
		"downloads": map[string]any{
			"artifact": map[string]any{
				"path": mavenPathFor(&installDownloadEntryJSON{}, name),
				"sha1": sha1,
				"size": size,
				"url":  officialArtifactURL("libraries.minecraft.net", sha1),
			},
		},
	}
}

// nativesLibraryDocument 构造旧格式本地库（natives + classifier + extract）。
func nativesLibraryDocument(name, classifier, sha1 string) map[string]any {
	return map[string]any{
		"name": name,
		"natives": map[string]any{
			"windows": classifier,
			"osx":     classifier,
			"linux":   classifier,
		},
		"extract": map[string]any{"exclude": []any{"META-INF/", "LICENSE.txt"}},
		"downloads": map[string]any{
			"classifiers": map[string]any{
				classifier: map[string]any{
					"sha1": sha1,
					"size": 512,
					"url":  officialArtifactURL("libraries.minecraft.net", sha1),
				},
			},
		},
	}
}

// minimalVersionDocument 构造一份可安装的最小版本元数据。
func minimalVersionDocument(id string) map[string]any {
	return map[string]any{
		"id":          id,
		"type":        "release",
		"releaseTime": "2024-01-01T00:00:00+00:00",
		"mainClass":   "net.minecraft.client.main.Main",
		"assets":      "5",
		"assetIndex":  assetIndexDocument("5", sha1AssetIndex, 1024),
		"downloads":   map[string]any{"client": clientDownloadEntry(sha1Client, 2048)},
		"javaVersion": map[string]any{"majorVersion": 17, "component": "java-runtime-gamma"},
		"libraries":   []any{},
	}
}

// requireWarningCode 断言告警列表中存在指定告警码。
func requireWarningCode(t *testing.T, warnings []planWarning, code string) {
	t.Helper()

	for _, warning := range warnings {
		if warning.Code == code {
			return
		}
	}
	t.Fatalf("期望告警码 %s，实际告警 %+v", code, warnings)
}

// TestLoadInstallVersionParsesBasicDocument 验证基本字段与下载描述被完整解析。
func TestLoadInstallVersionParsesBasicDocument(t *testing.T) {
	root := setupTestDataRoot(t)
	writeVersionMetadata(t, root, "1.21.4", minimalVersionDocument("1.21.4"))

	merged, status, warnings, appErr := loadInstallVersion(root, "1.21.4")
	if appErr != nil {
		t.Fatalf("解析失败: %v", appErr)
	}
	if status != installStatusMetadataOnly {
		t.Fatalf("无继承链时应为 metadata_only，实际 %s", status)
	}
	if len(warnings) != 0 {
		t.Fatalf("不应有告警，实际 %+v", warnings)
	}
	if merged.ID != "1.21.4" || merged.Type != "release" {
		t.Fatalf("版本标识或类型错误: %+v", merged)
	}
	if merged.MainClass != "net.minecraft.client.main.Main" {
		t.Fatalf("mainClass 解析错误: %q", merged.MainClass)
	}
	if merged.ClientJarID != "1.21.4" {
		t.Fatalf("jar 缺失时应回退版本 id，实际 %q", merged.ClientJarID)
	}
	if merged.ClientDownload == nil || merged.ClientDownload.SHA1 != sha1Client || merged.ClientDownload.Size != 2048 {
		t.Fatalf("客户端下载描述错误: %+v", merged.ClientDownload)
	}
	if merged.AssetIndex == nil || merged.AssetIndex.ID != "5" || merged.AssetIndex.TotalSize != 2048 {
		t.Fatalf("资源索引解析错误: %+v", merged.AssetIndex)
	}
	if merged.JavaMajorVersion != 17 || merged.JavaComponent != "java-runtime-gamma" {
		t.Fatalf("Java 要求解析错误: %+v", merged)
	}
	if merged.JSONSize <= 0 {
		t.Fatalf("应记录版本 JSON 大小，实际 %d", merged.JSONSize)
	}
	if len(merged.InheritChain) != 1 || merged.InheritChain[0] != "1.21.4" {
		t.Fatalf("继承链错误: %+v", merged.InheritChain)
	}
}

// TestLoadInstallVersionMergesInheritChain 验证继承链合并规则与 P2 一致。
//
// 断言的规则：
//   - 标量字段子版本优先、缺失回填父版本；
//   - libraries 子版本坐标优先，父版本独有条目追加到末尾；
//   - arguments 父版本在前、子版本在后；
//   - minecraftArguments 仅在子版本为空时取父版本。
func TestLoadInstallVersionMergesInheritChain(t *testing.T) {
	root := setupTestDataRoot(t)

	child := map[string]any{
		"id":           "1.20.1-forge",
		"type":         "release",
		"inheritsFrom": "1.20.1",
		"mainClass":    "net.minecraft.launchwrapper.Launch",
		"arguments": map[string]any{
			"game": []any{"--child"},
			"jvm":  []any{"-Dchild=true"},
		},
		"libraries": []any{
			libraryDocument("org.example:shared:1.0", sha1LibraryA, 100),
		},
	}
	parent := minimalVersionDocument("1.20.1")
	parent["arguments"] = map[string]any{
		"game": []any{"--parent"},
		"jvm":  []any{"-Dparent=true"},
	}
	parent["minecraftArguments"] = "--parent-template"
	parent["libraries"] = []any{
		libraryDocument("org.example:shared:1.0", sha1LibraryB, 200),
		libraryDocument("org.example:parent-only:2.0", sha1LibraryB, 300),
	}

	writeVersionMetadata(t, root, "1.20.1-forge", child)
	writeVersionMetadata(t, root, "1.20.1", parent)

	merged, status, warnings, appErr := loadInstallVersion(root, "1.20.1-forge")
	if appErr != nil {
		t.Fatalf("解析失败: %v", appErr)
	}
	if status != installStatusMetadataOnly {
		t.Fatalf("状态应为 metadata_only，实际 %s", status)
	}
	if len(warnings) != 0 {
		t.Fatalf("不应有告警，实际 %+v", warnings)
	}
	if len(merged.InheritChain) != 2 {
		t.Fatalf("继承链深度应为 2，实际 %+v", merged.InheritChain)
	}
	if merged.MainClass != "net.minecraft.launchwrapper.Launch" {
		t.Fatalf("mainClass 应子版本优先，实际 %q", merged.MainClass)
	}
	if merged.AssetIndex == nil {
		t.Fatalf("资源索引应从父版本回填")
	}
	if merged.ClientDownload == nil || merged.ClientDownload.SHA1 != sha1Client {
		t.Fatalf("客户端下载描述应从父版本回填: %+v", merged.ClientDownload)
	}
	if merged.JavaMajorVersion != 17 {
		t.Fatalf("Java 版本应从父版本回填，实际 %d", merged.JavaMajorVersion)
	}
	if merged.MinecraftArguments != "--parent-template" {
		t.Fatalf("子版本无旧格式参数时应取父版本，实际 %q", merged.MinecraftArguments)
	}

	// libraries：子版本共 1 条（子覆盖父的同坐标），父版本独有 1 条追加在后。
	if len(merged.Libraries) != 2 {
		t.Fatalf("库数量应为 2，实际 %d: %+v", len(merged.Libraries), merged.Libraries)
	}
	if merged.Libraries[0].Name != "org.example:shared:1.0" ||
		merged.Libraries[0].Downloads.Artifact.SHA1 != sha1LibraryA {
		t.Fatalf("同坐标库应保留子版本内容: %+v", merged.Libraries[0])
	}
	if merged.Libraries[1].Name != "org.example:parent-only:2.0" {
		t.Fatalf("父版本独有库应追加到末尾: %+v", merged.Libraries[1])
	}

	// arguments：父在前、子在后。
	if merged.Arguments == nil || len(merged.Arguments.Game) != 2 {
		t.Fatalf("合并后的参数数量错误: %+v", merged.Arguments)
	}
	if string(merged.Arguments.Game[0]) != `"--parent"` || string(merged.Arguments.Game[1]) != `"--child"` {
		t.Fatalf("arguments 顺序应为父在前子在后: %+v", merged.Arguments.Game)
	}
}

// TestLoadInstallVersionRejectsCycle 验证继承链成环被拒绝。
//
// 链路 a → b → c → b：父版本重新指向链中间节点（不是链首），
// 因此归为「循环引用」而不是「指向自身」。
func TestLoadInstallVersionRejectsCycle(t *testing.T) {
	root := setupTestDataRoot(t)

	a := minimalVersionDocument("a")
	a["inheritsFrom"] = "b"
	b := minimalVersionDocument("b")
	b["inheritsFrom"] = "c"
	c := minimalVersionDocument("c")
	c["inheritsFrom"] = "b"

	writeVersionMetadata(t, root, "a", a)
	writeVersionMetadata(t, root, "b", b)
	writeVersionMetadata(t, root, "c", c)

	_, status, _, appErr := loadInstallVersion(root, "a")
	requireCode(t, appErr, ErrorCodeVersionJSON)
	if status != installStatusBroken {
		t.Fatalf("成环应为 broken，实际 %s", status)
	}
	if !strings.Contains(appErr.Message, "循环引用") {
		t.Fatalf("错误文案应说明循环引用，实际 %q", appErr.Message)
	}
}

// TestLoadInstallVersionRejectsSelfReference 验证 inheritsFrom 指向自身被拒绝。
func TestLoadInstallVersionRejectsSelfReference(t *testing.T) {
	root := setupTestDataRoot(t)

	document := minimalVersionDocument("loop")
	document["inheritsFrom"] = "loop"
	writeVersionMetadata(t, root, "loop", document)

	_, status, _, appErr := loadInstallVersion(root, "loop")
	requireCode(t, appErr, ErrorCodeVersionJSON)
	if status != installStatusBroken {
		t.Fatalf("自引用应为 broken，实际 %s", status)
	}
	if !strings.Contains(appErr.Message, "指向自身") {
		t.Fatalf("错误文案应说明自引用，实际 %q", appErr.Message)
	}
}

// TestLoadInstallVersionStopsAtDepthLimit 验证继承层级超限按不支持处理。
func TestLoadInstallVersionStopsAtDepthLimit(t *testing.T) {
	root := setupTestDataRoot(t)

	// 构造 7 层链：v1 → v2 → ... → v7，超过「当前 + 5 层父」的上限。
	const depth = 7
	for index := 1; index <= depth; index++ {
		document := minimalVersionDocument(versionAt(index))
		if index < depth {
			document["inheritsFrom"] = versionAt(index + 1)
		}
		writeVersionMetadata(t, root, versionAt(index), document)
	}

	merged, status, warnings, appErr := loadInstallVersion(root, versionAt(1))
	if appErr != nil {
		t.Fatalf("层级超限不应返回致命错误: %v", appErr)
	}
	if status != installStatusUnsupported {
		t.Fatalf("层级超限应为 unsupported，实际 %s", status)
	}
	requireWarningCode(t, warnings, WarningInheritTooDeep)
	if len(merged.InheritChain) != installMaxInheritDepth+1 {
		t.Fatalf("继承链应停在 %d 个节点，实际 %d: %+v",
			installMaxInheritDepth+1, len(merged.InheritChain), merged.InheritChain)
	}
}

// versionAt 返回第 index 层版本标识（index 从 1 开始）。
func versionAt(index int) string {
	return "v" + string(rune('0'+index))
}

// TestLoadInstallVersionMissingParentIsUnsupported 验证父版本缺失按不支持处理。
func TestLoadInstallVersionMissingParentIsUnsupported(t *testing.T) {
	root := setupTestDataRoot(t)

	document := minimalVersionDocument("child")
	document["inheritsFrom"] = "missing-parent"
	writeVersionMetadata(t, root, "child", document)

	merged, status, warnings, appErr := loadInstallVersion(root, "child")
	if appErr != nil {
		t.Fatalf("父版本缺失不应返回致命错误: %v", appErr)
	}
	if status != installStatusUnsupported {
		t.Fatalf("父版本缺失应为 unsupported，实际 %s", status)
	}
	requireWarningCode(t, warnings, WarningInheritParentMissing)
	if merged.MainClass == "" {
		t.Fatalf("已合并的部分应保留子版本字段")
	}
}

// TestLoadInstallVersionRejectsMalformedJSON 验证非法 JSON 被拒绝。
func TestLoadInstallVersionRejectsMalformedJSON(t *testing.T) {
	root := setupTestDataRoot(t)
	writeRawVersionMetadata(t, root, "broken", `{"id": "broken",`)

	_, status, _, appErr := loadInstallVersion(root, "broken")
	requireCode(t, appErr, ErrorCodeVersionJSON)
	if status != installStatusBroken {
		t.Fatalf("非法 JSON 应为 broken，实际 %s", status)
	}
}

// TestLoadInstallVersionRejectsOversizedFile 验证超过 8 MiB 的元数据被拒绝。
func TestLoadInstallVersionRejectsOversizedFile(t *testing.T) {
	root := setupTestDataRoot(t)

	// 构造一个超过上限的文件：合法 JSON 前缀 + 足量空白填充。
	content := `{"id": "huge", "pad": "` + strings.Repeat("x", installMaxVersionJSONBytes) + `"}`
	writeRawVersionMetadata(t, root, "huge", content)

	_, _, _, appErr := loadInstallVersion(root, "huge")
	requireCode(t, appErr, ErrorCodeVersionJSON)
	if !strings.Contains(appErr.Message, "超出允许的大小") {
		t.Fatalf("错误文案应说明大小超限，实际 %q", appErr.Message)
	}
}

// TestLoadInstallVersionRejectsInvalidVersionID 验证版本 id 非法被拒绝。
func TestLoadInstallVersionRejectsInvalidVersionID(t *testing.T) {
	root := setupTestDataRoot(t)

	if _, _, _, appErr := loadInstallVersion(root, ""); appErr == nil {
		t.Fatalf("空版本 id 应被拒绝")
	} else {
		requireCode(t, appErr, ErrorCodeVersionJSON)
	}

	if _, _, _, appErr := loadInstallVersion(root, "../escape"); appErr == nil {
		t.Fatalf("含 .. 的版本 id 应被拒绝")
	} else {
		requireCode(t, appErr, ErrorCodeVersionJSON)
	}
}

// TestLoadInstallVersionFiltersLibrariesByRules 验证 rules 平台过滤生效。
func TestLoadInstallVersionFiltersLibrariesByRules(t *testing.T) {
	root := setupTestDataRoot(t)
	facts := currentPlatformFacts()

	// 选择与当前平台不同的系统名，构造「只允许其他平台」的库。
	otherOS := "linux"
	if facts.OSName == "linux" {
		otherOS = "windows"
	}

	document := minimalVersionDocument("rules")
	document["libraries"] = []any{
		libraryDocument("org.example:universal:1.0", sha1LibraryA, 100),
		map[string]any{
			"name":      "org.example:current-platform:1.0",
			"downloads": libraryDocument("org.example:current-platform:1.0", sha1LibraryB, 200)["downloads"],
			"rules":     []any{map[string]any{"action": "allow", "os": map[string]any{"name": facts.OSName}}},
		},
		map[string]any{
			"name":      "org.example:other-platform:1.0",
			"downloads": libraryDocument("org.example:other-platform:1.0", sha1LibraryB, 300)["downloads"],
			"rules":     []any{map[string]any{"action": "allow", "os": map[string]any{"name": otherOS}}},
		},
	}
	writeVersionMetadata(t, root, "rules", document)

	merged, _, _, appErr := loadInstallVersion(root, "rules")
	if appErr != nil {
		t.Fatalf("解析失败: %v", appErr)
	}
	if len(merged.Libraries) != 3 {
		t.Fatalf("解析阶段应保留全部库（过滤发生在计划阶段），实际 %d", len(merged.Libraries))
	}

	kept := map[string]bool{}
	for _, lib := range merged.Libraries {
		if libraryAppliesToPlatform(lib.Rules, facts) {
			kept[lib.Name] = true
		}
	}
	if !kept["org.example:universal:1.0"] {
		t.Fatalf("无规则的库应适用所有平台")
	}
	if !kept["org.example:current-platform:1.0"] {
		t.Fatalf("当前平台的库应适用")
	}
	if kept["org.example:other-platform:1.0"] {
		t.Fatalf("其他平台的库不应适用")
	}
}

// TestLibraryAppliesToPlatform 验证 allow/disallow 的求值语义。
func TestLibraryAppliesToPlatform(t *testing.T) {
	facts := installPlatformFacts{OSName: "windows", ArchNames: []string{"x86_64", "x64"}, ArchToken: "64"}

	allowAll := []installRuleJSON{{Action: "allow"}}
	if !libraryAppliesToPlatform(allowAll, facts) {
		t.Fatalf("allow 规则应适用")
	}

	allowLinux := []installRuleJSON{{Action: "allow", OS: &installRuleOSJSON{Name: "linux"}}}
	if libraryAppliesToPlatform(allowLinux, facts) {
		t.Fatalf("仅允许 linux 的规则不应适用于 windows")
	}

	// allow 之后被 disallow 覆盖：最终不适用。
	allowThenDisallow := []installRuleJSON{
		{Action: "allow", OS: &installRuleOSJSON{Name: "windows"}},
		{Action: "disallow", OS: &installRuleOSJSON{Name: "windows"}},
	}
	if libraryAppliesToPlatform(allowThenDisallow, facts) {
		t.Fatalf("后命中的 disallow 应覆盖 allow")
	}

	archAllow := []installRuleJSON{{Action: "allow", OS: &installRuleOSJSON{Arch: "x64"}}}
	if !libraryAppliesToPlatform(archAllow, facts) {
		t.Fatalf("架构别名 x64 应命中")
	}

	features := []installRuleJSON{{Action: "allow", Features: map[string]bool{"has_custom_resolution": true}}}
	if libraryAppliesToPlatform(features, facts) {
		t.Fatalf("无法满足的 features 条件应视为不命中")
	}
}

// TestSelectNativeClassifier 验证 natives classifier 选择与 ${arch} 替换。
func TestSelectNativeClassifier(t *testing.T) {
	facts := installPlatformFacts{OSName: "windows", ArchNames: []string{"x86_64"}, ArchToken: "64"}

	matched := installLibraryJSON{Natives: map[string]string{"windows": "natives-windows-${arch}"}}
	if got := selectNativeClassifier(matched, facts); got != "natives-windows-64" {
		t.Fatalf("${arch} 应替换为 64，实际 %q", got)
	}

	missing := installLibraryJSON{Natives: map[string]string{"linux": "natives-linux"}}
	if got := selectNativeClassifier(missing, facts); got != "" {
		t.Fatalf("无当前平台映射时应返回空串，实际 %q", got)
	}

	empty := installLibraryJSON{Natives: map[string]string{"windows": ""}}
	if got := selectNativeClassifier(empty, facts); got != "" {
		t.Fatalf("空 classifier 应返回空串，实际 %q", got)
	}
}

// TestLoadInstallVersionParsesOldMinecraftArguments 验证旧格式参数模板被解析。
func TestLoadInstallVersionParsesOldMinecraftArguments(t *testing.T) {
	root := setupTestDataRoot(t)

	document := minimalVersionDocument("1.7.10")
	document["minecraftArguments"] = "--username ${auth_player_name} --version ${version_name}"
	writeVersionMetadata(t, root, "1.7.10", document)

	merged, _, _, appErr := loadInstallVersion(root, "1.7.10")
	if appErr != nil {
		t.Fatalf("解析失败: %v", appErr)
	}
	if merged.MinecraftArguments != "--username ${auth_player_name} --version ${version_name}" {
		t.Fatalf("旧格式参数解析错误: %q", merged.MinecraftArguments)
	}
}

// TestLoadInstallVersionMissingMainClassKeepsMetadataOnly 验证缺 mainClass 不致命。
func TestLoadInstallVersionMissingMainClassKeepsMetadataOnly(t *testing.T) {
	root := setupTestDataRoot(t)

	document := minimalVersionDocument("no-main")
	delete(document, "mainClass")
	writeVersionMetadata(t, root, "no-main", document)

	merged, status, _, appErr := loadInstallVersion(root, "no-main")
	if appErr != nil {
		t.Fatalf("缺 mainClass 不应返回致命错误: %v", appErr)
	}
	if status != installStatusMetadataOnly {
		t.Fatalf("缺 mainClass 应为 metadata_only，实际 %s", status)
	}
	if merged.MainClass != "" {
		t.Fatalf("mainClass 应为空，实际 %q", merged.MainClass)
	}
}

// TestMavenLibraryPath 验证 Maven 坐标到相对路径的推导。
func TestMavenLibraryPath(t *testing.T) {
	got := mavenLibraryPath("org.lwjgl:lwjgl:3.3.1:natives-windows")
	expected := "libraries/org/lwjgl/lwjgl/3.3.1/lwjgl-3.3.1-natives-windows.jar"
	if got != expected {
		t.Fatalf("Maven 路径推导错误: %q", got)
	}

	if got := mavenLibraryPath("bad-coordinate"); got != "" {
		t.Fatalf("非法坐标应返回空串，实际 %q", got)
	}
}
