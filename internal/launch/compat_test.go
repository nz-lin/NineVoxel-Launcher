// 本文件是旧版本兼容性判定的纯函数测试。
//
// 约束（P3-10 卡面）：
//   - 不联网：所有版本 JSON 都是本文件构造的 fixture，写入 t.TempDir()；
//   - 不下载、不启动游戏、不创建下载任务；
//   - 不修改 internal/version 或 P2 既有测试文件。
package launch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// writeCompatVersion 把给定版本的 JSON 写入临时游戏目录。
//
// 参数 root 为游戏根；id 为版本目录名；document 为原始 JSON 文本。
func writeCompatVersion(t *testing.T, root, id, document string) {
	t.Helper()
	dir := filepath.Join(root, "versions", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("创建版本目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, id+".json"), []byte(document), 0o644); err != nil {
		t.Fatalf("写入版本 JSON 失败: %v", err)
	}
}

// newCompatRoot 创建一个临时游戏目录。
func newCompatRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "versions"), 0o755); err != nil {
		t.Fatalf("创建 versions 目录失败: %v", err)
	}
	return root
}

// legacy1710Document 是 1.7.10 风格的版本 JSON fixture。
const legacy1710Document = `{
  "id": "1.7.10",
  "type": "release",
  "mainClass": "net.minecraft.client.main.Main",
  "assets": "1.7.10",
  "minecraftArguments": "--username ${auth_player_name} --version ${version_name} --gameDir ${game_directory}",
  "libraries": [
    {
      "name": "org.lwjgl.lwjgl:lwjgl:2.9.1",
      "natives": {
        "linux": "natives-linux",
        "windows": "natives-windows",
        "osx": "natives-osx"
      },
      "extract": {"exclude": ["META-INF/"]}
    },
    {"name": "net.java.jinput:jinput:2.0.5"}
  ]
}`

// modern120Document 是 1.20.x 风格的版本 JSON fixture。
const modern120Document = `{
  "id": "1.20.6",
  "type": "release",
  "mainClass": "net.minecraft.client.main.Main",
  "assets": "17",
  "javaVersion": {"majorVersion": 21},
  "arguments": {
    "game": ["--username", "${auth_player_name}"],
    "jvm": ["-Xmx2G"]
  },
  "logging": {
    "client": {
      "argument": "-Dlog4j.configurationFile=${path}",
      "file": {"id": "client-1.12.xml", "sha1": "0000000000000000000000000000000000000000", "size": 1000, "url": "https://piston-data.mojang.com/v1/objects/client-1.12.xml"}
    }
  },
  "libraries": [
    {
      "name": "org.lwjgl:lwjgl:3.3.3",
      "downloads": {
        "artifact": {"path": "org/lwjgl/lwjgl/3.3.3/lwjgl-3.3.3.jar", "size": 1000},
        "classifiers": {"natives-windows": {"path": "natives-windows.jar", "size": 10}}
      }
    }
  ]
}`

// virtual160Document 是 1.6.x 风格（旧式虚拟资源索引 + map_to_resources）的 fixture。
const virtual160Document = `{
  "id": "1.6.4",
  "type": "old_beta",
  "mainClass": "net.minecraft.client.Minecraft",
  "assets": "legacy",
  "map_to_resources": true,
  "minecraftArguments": "--username ${auth_player_name}",
  "libraries": [{"name": "org.lwjgl.lwjgl:lwjgl:2.9.0"}]
}`

// parent120Document 是被继承的父版本 fixture。
const parent120Document = `{
  "id": "1.20.6",
  "type": "release",
  "mainClass": "net.minecraft.client.main.Main",
  "assets": "17",
  "javaVersion": {"majorVersion": 21},
  "arguments": {"game": ["--username", "${auth_player_name}"]},
  "libraries": [{"name": "com.mojang:brigadier:1.0.18"}]
}`

// childInheritDocument 是继承父版本的子版本 fixture。
const childInheritDocument = `{
  "id": "fabric-loader-1.20.6",
  "type": "release",
  "inheritsFrom": "1.20.6",
  "libraries": [{"name": "net.fabricmc:fabric-loader:0.15.0"}]
}`

// TestAssessVersionTraitsForLegacy1710 校验 1.7.10 风格版本的差异特征。
func TestCompatAssessVersionTraitsForLegacy1710(t *testing.T) {
	root := newCompatRoot(t)
	writeCompatVersion(t, root, "1.7.10", legacy1710Document)

	traits, err := AssessVersionTraits(root, "1.7.10")
	if err != nil {
		t.Fatalf("评估 1.7.10 失败: %v", err)
	}
	if !traits.MetadataSupported {
		t.Fatalf("1.7.10 元数据应可读，实际 status=%q", traits.MetadataStatus)
	}
	if traits.AssetLayout != CompatAssetLayoutObjects {
		t.Errorf("资源索引布局 = %q，期望 objects（assets=1.7.10）", traits.AssetLayout)
	}
	if traits.NativesConvention != CompatNativesLegacy {
		t.Errorf("natives 约定 = %q，期望 legacy", traits.NativesConvention)
	}
	if traits.ArgumentsFormat != CompatArgumentsLegacy {
		t.Errorf("参数格式 = %q，期望 minecraft_arguments", traits.ArgumentsFormat)
	}
	if traits.JavaSource != CompatJavaSourceUnknown || traits.JavaMajorVersion != 0 {
		t.Errorf("Java 需求应为 unknown，实际 source=%q major=%d", traits.JavaSource, traits.JavaMajorVersion)
	}
	if traits.LoggingConfigured {
		t.Errorf("1.7.10 不应带 logging.client")
	}
	if traits.LibrariesWithoutRules != 2 {
		t.Errorf("缺 rules 的库数量 = %d，期望 2", traits.LibrariesWithoutRules)
	}
	if !traits.InstallViable {
		t.Errorf("1.7.10 元数据应足以生成安装计划")
	}
	if !traits.LaunchViable {
		t.Errorf("1.7.10 应可启动，原因=%q", traits.LaunchReason)
	}
	requireCompatNote(t, traits.Notes, CompatNoteJavaUnknown)
	requireCompatNote(t, traits.Notes, CompatNoteLoggingAbsent)
	requireCompatNote(t, traits.Notes, CompatNoteLibrariesWithoutRules)
	requireCompatNote(t, traits.Notes, CompatNoteClientJarMissing)
}

// TestAssessVersionTraitsForModern120 校验 1.20.x 风格版本的差异特征。
func TestCompatAssessVersionTraitsForModern120(t *testing.T) {
	root := newCompatRoot(t)
	writeCompatVersion(t, root, "1.20.6", modern120Document)

	traits, err := AssessVersionTraits(root, "1.20.6")
	if err != nil {
		t.Fatalf("评估 1.20.6 失败: %v", err)
	}
	if !traits.MetadataSupported {
		t.Fatalf("1.20.6 元数据应可读，实际 status=%q", traits.MetadataStatus)
	}
	if traits.AssetLayout != CompatAssetLayoutObjects {
		t.Errorf("资源索引布局 = %q，期望 objects", traits.AssetLayout)
	}
	if traits.NativesConvention != CompatNativesModern {
		t.Errorf("natives 约定 = %q，期望 modern", traits.NativesConvention)
	}
	if traits.ArgumentsFormat != CompatArgumentsModern {
		t.Errorf("参数格式 = %q，期望 arguments", traits.ArgumentsFormat)
	}
	if traits.JavaMajorVersion != 21 || traits.JavaSource != CompatJavaSourceMetadata {
		t.Errorf("Java 需求应为 21/metadata，实际 %d/%q", traits.JavaMajorVersion, traits.JavaSource)
	}
	if !traits.LoggingConfigured {
		t.Errorf("1.20.6 应识别到 logging.client")
	}
	if !traits.InstallViable || !traits.LaunchViable {
		t.Errorf("1.20.6 应可安装且可启动: install=%v launch=%v", traits.InstallViable, traits.LaunchViable)
	}
	requireCompatNote(t, traits.Notes, CompatNoteJavaMetadata)
	requireCompatNote(t, traits.Notes, CompatNoteLoggingConfigured)
}

// TestAssessVersionTraitsVirtualAssets 校验旧式虚拟索引与 map_to_resources 判定。
func TestCompatAssessVersionTraitsVirtualAssets(t *testing.T) {
	root := newCompatRoot(t)
	writeCompatVersion(t, root, "1.6.4", virtual160Document)

	traits, err := AssessVersionTraits(root, "1.6.4")
	if err != nil {
		t.Fatalf("评估 1.6.4 失败: %v", err)
	}
	if traits.AssetLayout != CompatAssetLayoutVirtual {
		t.Errorf("资源索引布局 = %q，期望 virtual", traits.AssetLayout)
	}
	if !traits.MapToResources {
		t.Errorf("应识别 map_to_resources=true")
	}
	requireCompatNote(t, traits.Notes, CompatNoteMapToResources)
	requireCompatNote(t, traits.Notes, CompatNoteAssetsVirtual)
}

// TestAssessVersionTraitsInheritChain 校验继承链合并后的特征。
func TestCompatAssessVersionTraitsInheritChain(t *testing.T) {
	root := newCompatRoot(t)
	writeCompatVersion(t, root, "1.20.6", parent120Document)
	writeCompatVersion(t, root, "fabric-loader-1.20.6", childInheritDocument)

	traits, err := AssessVersionTraits(root, "fabric-loader-1.20.6")
	if err != nil {
		t.Fatalf("评估继承版本失败: %v", err)
	}
	if !traits.MetadataSupported {
		t.Fatalf("继承版本元数据应可读，实际 status=%q", traits.MetadataStatus)
	}
	if len(traits.InheritChain) != 2 {
		t.Fatalf("继承链长度 = %d，期望 2（%v）", len(traits.InheritChain), traits.InheritChain)
	}
	if traits.InheritChain[0] != "fabric-loader-1.20.6" || traits.InheritChain[1] != "1.20.6" {
		t.Errorf("继承链 = %v，期望子在前、父在后", traits.InheritChain)
	}
	// 主类、参数与 Java 需求都来自父版本。
	if !traits.LaunchViable {
		t.Errorf("继承版本应可启动，原因=%q", traits.LaunchReason)
	}
	if traits.JavaMajorVersion != 21 {
		t.Errorf("Java 需求应继承自父版本 21，实际 %d", traits.JavaMajorVersion)
	}
	requireCompatNote(t, traits.Notes, CompatNoteInheritChain)
}

// TestAssessVersionTraitsMissingVersion 校验版本不存在时给出明确原因而不 panic。
func TestCompatAssessVersionTraitsMissingVersion(t *testing.T) {
	root := newCompatRoot(t)

	traits, err := AssessVersionTraits(root, "9.9.9")
	if err != nil {
		t.Fatalf("缺失版本不应返回错误，实际 %v", err)
	}
	if traits.MetadataSupported {
		t.Errorf("缺失版本不应判定元数据可读")
	}
	if traits.MetadataReason == "" {
		t.Errorf("缺失版本必须给出原因")
	}
	if traits.LaunchViable {
		t.Errorf("缺失版本不应判定可启动")
	}
	requireCompatNote(t, traits.Notes, CompatNoteMetadataFailed)
}

// TestAssessVersionTraitsMalformedJSON 校验非法 JSON 的降级行为。
func TestCompatAssessVersionTraitsMalformedJSON(t *testing.T) {
	root := newCompatRoot(t)
	writeCompatVersion(t, root, "broken-json", "{not json")

	traits, err := AssessVersionTraits(root, "broken-json")
	if err != nil {
		t.Fatalf("非法 JSON 不应返回错误，实际 %v", err)
	}
	if traits.MetadataSupported {
		t.Errorf("非法 JSON 不应判定元数据可读")
	}
	if traits.MetadataStatus != versionStatusBroken {
		t.Errorf("非法 JSON 状态 = %q，期望 broken", traits.MetadataStatus)
	}
	requireCompatNote(t, traits.Notes, CompatNoteMetadataFailed)
}

// TestAssessVersionTraitsMissingMainClass 校验缺 mainClass 时不可启动。
func TestCompatAssessVersionTraitsMissingMainClass(t *testing.T) {
	root := newCompatRoot(t)
	writeCompatVersion(t, root, "no-main-class", `{"id":"no-main-class","type":"release","assets":"1.0"}`)

	traits, err := AssessVersionTraits(root, "no-main-class")
	if err != nil {
		t.Fatalf("缺 mainClass 不应返回错误，实际 %v", err)
	}
	if traits.MetadataSupported {
		t.Errorf("P2 要求 mainClass 才能合并，缺 mainClass 不应判定元数据可读")
	}
	if traits.LaunchViable {
		t.Errorf("缺 mainClass 不应判定可启动")
	}
}

// TestAssessVersionTraitsRejectsInvalidVersionID 校验非法版本 id 返回稳定错误码。
func TestCompatAssessVersionTraitsRejectsInvalidVersionID(t *testing.T) {
	root := newCompatRoot(t)
	cases := []string{"", "../escape", "a/b"}
	for _, versionID := range cases {
		t.Run(versionID, func(t *testing.T) {
			_, err := AssessVersionTraits(root, versionID)
			if err == nil {
				t.Fatalf("非法版本 id %q 应返回错误", versionID)
			}
			if err.Code != ErrorCodeVersionMetadata {
				t.Errorf("错误码 = %q，期望 %q", err.Code, ErrorCodeVersionMetadata)
			}
		})
	}
}

// TestAssessVersionTraitsEmptyRoot 校验空游戏目录返回路径错误。
func TestCompatAssessVersionTraitsEmptyRoot(t *testing.T) {
	_, err := AssessVersionTraits("", "1.20.6")
	if err == nil {
		t.Fatalf("空游戏目录应返回错误")
	}
	if err.Code != ErrorCodePathInvalid {
		t.Errorf("错误码 = %q，期望 %q", err.Code, ErrorCodePathInvalid)
	}
}

// TestAssessVersionTraitsNotesAreDeterministic 校验同一输入两次评估给出相同依据顺序。
func TestCompatAssessVersionTraitsNotesAreDeterministic(t *testing.T) {
	root := newCompatRoot(t)
	writeCompatVersion(t, root, "1.7.10", legacy1710Document)

	first, err := AssessVersionTraits(root, "1.7.10")
	if err != nil {
		t.Fatalf("第一次评估失败: %v", err)
	}
	second, err := AssessVersionTraits(root, "1.7.10")
	if err != nil {
		t.Fatalf("第二次评估失败: %v", err)
	}
	if len(first.Notes) != len(second.Notes) {
		t.Fatalf("两次依据数量不同: %d vs %d", len(first.Notes), len(second.Notes))
	}
	for index := range first.Notes {
		if first.Notes[index] != second.Notes[index] {
			t.Fatalf("第 %d 条依据不一致: %+v vs %+v", index, first.Notes[index], second.Notes[index])
		}
	}
	if len(first.Notes) == 0 {
		t.Fatalf("应至少产出一条依据")
	}
}

// TestDetectAssetLayout 校验资源索引布局判定。
func TestCompatDetectAssetLayout(t *testing.T) {
	cases := []struct {
		name     string
		merged   *mergedVersion
		probe    *versionCompatProbe
		expected string
	}{
		{"无标识", &mergedVersion{}, nil, CompatAssetLayoutUnknown},
		{"旧式 legacy", &mergedVersion{Assets: "legacy"}, nil, CompatAssetLayoutVirtual},
		{"旧式 pre-1.6", &mergedVersion{}, &versionCompatProbe{Assets: "pre-1.6"}, CompatAssetLayoutVirtual},
		{"数字索引", &mergedVersion{Assets: "1.7.10"}, nil, CompatAssetLayoutObjects},
		{"assetIndex 优先", &mergedVersion{AssetIndexID: "17", Assets: "17"}, nil, CompatAssetLayoutObjects},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			actual := detectAssetLayout(testCase.merged, testCase.probe)
			if actual != testCase.expected {
				t.Errorf("布局 = %q，期望 %q", actual, testCase.expected)
			}
		})
	}
}

// TestDetectNativesConvention 校验 natives 命名约定判定。
func TestCompatDetectNativesConvention(t *testing.T) {
	legacyLibrary := libraryJSON{
		Name:    "org.lwjgl.lwjgl:lwjgl:2.9.1",
		Natives: map[string]string{"windows": "natives-windows"},
	}
	modernLibrary := libraryJSON{
		Name: "org.lwjgl:lwjgl:3.3.3",
		Downloads: &libraryDownloadsJSON{
			Classifiers: map[string]*downloadArtifactJSON{
				"natives-windows": {Path: "natives-windows.jar"},
			},
		},
	}
	plainLibrary := libraryJSON{Name: "com.mojang:brigadier:1.0.18"}

	cases := []struct {
		name     string
		libs     []libraryJSON
		expected string
	}{
		{"无库", nil, CompatNativesNone},
		{"仅旧式", []libraryJSON{legacyLibrary, plainLibrary}, CompatNativesLegacy},
		{"仅现代", []libraryJSON{modernLibrary}, CompatNativesModern},
		{"两套并存", []libraryJSON{legacyLibrary, modernLibrary}, CompatNativesMixed},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			actual := detectNativesConvention(testCase.libs)
			if actual != testCase.expected {
				t.Errorf("约定 = %q，期望 %q", actual, testCase.expected)
			}
		})
	}
}

// TestDetectArgumentsFormat 校验启动参数格式判定。
func TestCompatDetectArgumentsFormat(t *testing.T) {
	cases := []struct {
		name     string
		merged   *mergedVersion
		expected string
	}{
		{"都没有", &mergedVersion{}, CompatArgumentsNone},
		{"仅旧式", &mergedVersion{MinecraftArguments: "--username x"}, CompatArgumentsLegacy},
		{"仅新式", &mergedVersion{GameArguments: []json.RawMessage{}}, CompatArgumentsNone},
		{"新式含元素", &mergedVersion{GameArguments: []json.RawMessage{[]byte(`"--x"`)}}, CompatArgumentsModern},
		{"两套并存", &mergedVersion{MinecraftArguments: "--x", JVMMajorArguments: []json.RawMessage{[]byte(`"-Xmx2G"`)}}, CompatArgumentsBoth},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			actual := detectArgumentsFormat(testCase.merged)
			if actual != testCase.expected {
				t.Errorf("格式 = %q，期望 %q", actual, testCase.expected)
			}
		})
	}
}

// TestCountLibrariesWithoutRules 校验缺 rules 的库计数。
func TestCompatCountLibrariesWithoutRules(t *testing.T) {
	libraries := []libraryJSON{
		{Name: "a"},
		{Name: "b", Rules: json.RawMessage(`[{"action":"allow"}]`)},
		{Name: "c", Rules: json.RawMessage(`[]`)},
		{Name: "d", Rules: json.RawMessage(`null`)},
	}
	if actual := countLibrariesWithoutRules(libraries); actual != 3 {
		t.Errorf("缺 rules 计数 = %d，期望 3", actual)
	}
}

// TestClientJarPresent 校验客户端 JAR 存在性判定（两种旧版布局）。
func TestCompatClientJarPresent(t *testing.T) {
	root := newCompatRoot(t)
	merged := &mergedVersion{ID: "1.7.10", ClientJarID: "1.7.10"}
	if clientJarPresent(root, merged) {
		t.Fatalf("未写 jar 时不应判定存在")
	}

	jarPath := filepath.Join(root, "versions", "1.7.10", "1.7.10.jar")
	if err := os.MkdirAll(filepath.Dir(jarPath), 0o755); err != nil {
		t.Fatalf("创建版本目录失败: %v", err)
	}
	if err := os.WriteFile(jarPath, []byte("jar-bytes"), 0o644); err != nil {
		t.Fatalf("写入 jar 失败: %v", err)
	}
	if !clientJarPresent(root, merged) {
		t.Fatalf("写入 jar 后应判定存在")
	}

	// jar 标识与 id 不同时，也应能识别 versions/<id>/<jar>.jar。
	renamed := &mergedVersion{ID: "1.7.10-forge", ClientJarID: "1.7.10"}
	altPath := filepath.Join(root, "versions", "1.7.10-forge", "1.7.10.jar")
	if err := os.MkdirAll(filepath.Dir(altPath), 0o755); err != nil {
		t.Fatalf("创建版本目录失败: %v", err)
	}
	if err := os.WriteFile(altPath, []byte("jar-bytes"), 0o644); err != nil {
		t.Fatalf("写入 jar 失败: %v", err)
	}
	if !clientJarPresent(root, renamed) {
		t.Fatalf("按 jar 标识命名的客户端应判定存在")
	}
}

// requireCompatNote 断言依据列表包含指定依据码。
func requireCompatNote(t *testing.T, notes []CompatNote, code string) {
	t.Helper()
	for _, note := range notes {
		if note.Code == code {
			if note.Message == "" {
				t.Fatalf("依据 %q 的说明为空", code)
			}
			return
		}
	}
	t.Fatalf("缺少依据 %q，实际依据: %v", code, compatNoteCodes(notes))
}

// compatNoteCodes 提取依据码列表，便于断言失败时查看。
func compatNoteCodes(notes []CompatNote) []string {
	codes := make([]string, 0, len(notes))
	for _, note := range notes {
		codes = append(codes, note.Code)
	}
	return codes
}

// legacy1122Document 是 1.12.2 风格的版本 JSON fixture（旧式参数 + 旧式 natives + 显式 Java 需求）。
const legacy1122Document = `{
  "id": "1.12.2",
  "type": "release",
  "mainClass": "net.minecraft.client.main.Main",
  "assets": "1.12",
  "javaVersion": {"majorVersion": 8},
  "minecraftArguments": "--username ${auth_player_name} --version ${version_name}",
  "libraries": [
    {
      "name": "org.lwjgl.lwjgl:lwjgl:2.9.4",
      "natives": {"linux": "natives-linux", "windows": "natives-windows", "osx": "natives-osx"},
      "extract": {"exclude": ["META-INF/"]}
    }
  ]
}`

// transition1165Document 是 1.16.5 风格的版本 JSON fixture（新式参数 + 现代 classifier）。
const transition1165Document = `{
  "id": "1.16.5",
  "type": "release",
  "mainClass": "net.minecraft.client.main.Main",
  "assets": "1.16",
  "javaVersion": {"majorVersion": 8},
  "arguments": {"game": ["--username", "${auth_player_name}"], "jvm": ["-Xmx2G"]},
  "logging": {"client": {"argument": "-Dlog4j.configurationFile=${path}", "file": {"id": "client-1.12.xml", "sha1": "0000000000000000000000000000000000000000", "size": 1000, "url": "https://piston-data.mojang.com/v1/objects/client-1.12.xml"}}},
  "libraries": [
    {
      "name": "org.lwjgl:lwjgl:3.2.2",
      "downloads": {
        "artifact": {"path": "org/lwjgl/lwjgl/3.2.2/lwjgl-3.2.2.jar", "size": 1000},
        "classifiers": {"natives-windows": {"path": "natives-windows.jar", "size": 10}}
      }
    }
  ]
}`

// TestCompatAssessLegacy1122 校验 1.12.2 风格版本的特征（旧式参数 + 旧式 natives + Java 8）。
func TestCompatAssessLegacy1122(t *testing.T) {
	root := newCompatRoot(t)
	writeCompatVersion(t, root, "1.12.2", legacy1122Document)

	traits, err := AssessVersionTraits(root, "1.12.2")
	if err != nil {
		t.Fatalf("评估 1.12.2 失败: %v", err)
	}
	if !traits.MetadataSupported {
		t.Fatalf("1.12.2 元数据应可读，实际 status=%q", traits.MetadataStatus)
	}
	if traits.ArgumentsFormat != CompatArgumentsLegacy {
		t.Errorf("参数格式 = %q，期望 minecraft_arguments", traits.ArgumentsFormat)
	}
	if traits.NativesConvention != CompatNativesLegacy {
		t.Errorf("natives 约定 = %q，期望 legacy", traits.NativesConvention)
	}
	if traits.JavaMajorVersion != 8 || traits.JavaSource != CompatJavaSourceMetadata {
		t.Errorf("Java 需求应为 8/metadata，实际 %d/%q", traits.JavaMajorVersion, traits.JavaSource)
	}
	if traits.LoggingConfigured {
		t.Errorf("1.12.2 fixture 未声明 logging.client，不应判定为已配置")
	}
	if !traits.InstallViable || !traits.LaunchViable {
		t.Errorf("1.12.2 应可安装且可启动: install=%v launch=%v", traits.InstallViable, traits.LaunchViable)
	}
}

// TestCompatAssessTransition1165 校验 1.16.5 风格版本的特征（新式参数 + 现代 classifier + Java 8）。
func TestCompatAssessTransition1165(t *testing.T) {
	root := newCompatRoot(t)
	writeCompatVersion(t, root, "1.16.5", transition1165Document)

	traits, err := AssessVersionTraits(root, "1.16.5")
	if err != nil {
		t.Fatalf("评估 1.16.5 失败: %v", err)
	}
	if !traits.MetadataSupported {
		t.Fatalf("1.16.5 元数据应可读，实际 status=%q", traits.MetadataStatus)
	}
	if traits.ArgumentsFormat != CompatArgumentsModern {
		t.Errorf("参数格式 = %q，期望 arguments", traits.ArgumentsFormat)
	}
	if traits.NativesConvention != CompatNativesModern {
		t.Errorf("natives 约定 = %q，期望 modern", traits.NativesConvention)
	}
	if traits.JavaMajorVersion != 8 {
		t.Errorf("Java 需求应为 8，实际 %d", traits.JavaMajorVersion)
	}
	if !traits.LoggingConfigured {
		t.Errorf("1.16.5 应识别到 logging.client")
	}
	if traits.LibrariesWithoutRules != 1 {
		t.Errorf("fixture 的库未声明 rules，计数应为 1，实际 %d", traits.LibrariesWithoutRules)
	}
}
