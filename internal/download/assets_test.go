package download

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"example.com/ninevoxel/launcher/internal/artifact"
)

// 本文件覆盖 P3-07（Assets 管理）的纯逻辑与受限文件系统行为。
//
// 约束：全部用例只在 t.TempDir() 内读写，不发起任何网络请求，
// 也不启动子进程；虚拟布局的回退链通过替换 assetLinkOperations 验证，
// 不依赖运行环境是否允许创建符号链接。

// assetTestSHA1 返回内容对应的 SHA-1（小写十六进制）。
func assetTestSHA1(content []byte) string {
	digest := sha1.Sum(content)
	return hex.EncodeToString(digest[:])
}

// assetTestWriteObject 把内容写入共享缓存的正确位置，返回哈希与大小。
func assetTestWriteObject(t *testing.T, dataRoot string, content []byte) (string, int64) {
	t.Helper()
	hash := assetTestSHA1(content)
	relPath, pathErr := artifact.AssetObjectRelPath(hash)
	if pathErr != nil {
		t.Fatalf("生成对象路径失败: %v", pathErr)
	}
	absolute := filepath.Join(dataRoot, filepath.FromSlash(relPath))
	if mkdirErr := os.MkdirAll(filepath.Dir(absolute), 0o755); mkdirErr != nil {
		t.Fatalf("创建对象目录失败: %v", mkdirErr)
	}
	if writeErr := os.WriteFile(absolute, content, 0o644); writeErr != nil {
		t.Fatalf("写入对象失败: %v", writeErr)
	}
	return hash, int64(len(content))
}

// assetBuildIndexJSON 构造索引 JSON 文本。
func assetBuildIndexJSON(t *testing.T, objects map[string]assetIndexObjectEntry, virtual bool, mapToResources bool) []byte {
	t.Helper()
	document := map[string]any{"objects": objects}
	if virtual {
		document["virtual"] = true
	}
	if mapToResources {
		document["map_to_resources"] = true
	}
	raw, marshalErr := json.Marshal(document)
	if marshalErr != nil {
		t.Fatalf("序列化索引失败: %v", marshalErr)
	}
	return raw
}

// assetEntry 构造一个索引条目。
func assetEntry(hash string, size int64) assetIndexObjectEntry {
	return assetIndexObjectEntry{Hash: hash, Size: size}
}

// requireAssetError 断言错误码符合预期且错误非空。
func requireAssetError(t *testing.T, err *Error, code string) {
	t.Helper()
	if err == nil {
		t.Fatalf("预期错误码 %s，实际没有错误", code)
	}
	if err.Code != code {
		t.Fatalf("预期错误码 %s，实际 %s（%s）", code, err.Code, err.Message)
	}
	if err.Message == "" {
		t.Fatalf("错误 %s 缺少面向用户的文案", code)
	}
}

// assetTestIndex 基于内容构造一个最小的合法索引（供展开/组装用例复用）。
func assetTestIndex(t *testing.T, dataRoot string, contents [][]byte) (AssetIndex, []string) {
	t.Helper()
	objects := make(map[string]assetIndexObjectEntry, len(contents))
	hashes := make([]string, 0, len(contents))
	for position, content := range contents {
		hash := assetTestSHA1(content)
		hashes = append(hashes, hash)
		objects[fmt.Sprintf("minecraft/lang/file_%d.lang", position)] = assetEntry(hash, int64(len(content)))
	}
	index, parseErr := ParseAssetIndex(assetBuildIndexJSON(t, objects, false, false), AssetIndexExpectation{IndexID: "1.21"})
	if parseErr != nil {
		t.Fatalf("构造测试索引失败: %v", parseErr)
	}
	return index, hashes
}

// TestParseAssetIndexAcceptsValidDocument 校验正常索引的解析结果。
func TestParseAssetIndexAcceptsValidDocument(t *testing.T) {
	hashA := strings.Repeat("a", 40)
	hashB := strings.Repeat("b", 40)
	raw := assetBuildIndexJSON(t, map[string]assetIndexObjectEntry{
		"minecraft/lang/en_US.lang": assetEntry(hashA, 100),
		"minecraft/sounds/a.ogg":    assetEntry(hashB, 200),
	}, true, false)

	index, err := ParseAssetIndex(raw, AssetIndexExpectation{IndexID: "1.21"})
	if err != nil {
		t.Fatalf("解析合法索引失败: %v", err)
	}
	if index.ID != "1.21" {
		t.Fatalf("索引标识不符: %s", index.ID)
	}
	if len(index.Objects) != 2 {
		t.Fatalf("条目数不符: %d", len(index.Objects))
	}
	if index.TotalBytes != 300 {
		t.Fatalf("总字节数不符: %d", index.TotalBytes)
	}
	if index.UniqueHashes != 2 {
		t.Fatalf("去重哈希数不符: %d", index.UniqueHashes)
	}
	if !index.Virtual {
		t.Fatal("virtual 标志未被读取")
	}
	if index.RawSize != int64(len(raw)) {
		t.Fatalf("索引字节数不符: %d != %d", index.RawSize, len(raw))
	}
	if len(index.Warnings) != 0 {
		t.Fatalf("合法索引不应有告警: %+v", index.Warnings)
	}
}

// TestParseAssetIndexSortsObjects 校验条目按逻辑名升序排列（确定性前提）。
func TestParseAssetIndexSortsObjects(t *testing.T) {
	hash := strings.Repeat("c", 40)
	raw := assetBuildIndexJSON(t, map[string]assetIndexObjectEntry{
		"minecraft/zeta.txt":   assetEntry(hash, 1),
		"minecraft/alpha.txt":  assetEntry(hash, 1),
		"minecraft/middle.txt": assetEntry(hash, 1),
	}, false, false)

	index, err := ParseAssetIndex(raw, AssetIndexExpectation{IndexID: "1.21"})
	if err != nil {
		t.Fatalf("解析索引失败: %v", err)
	}
	wanted := []string{"minecraft/alpha.txt", "minecraft/middle.txt", "minecraft/zeta.txt"}
	for position, name := range wanted {
		if index.Objects[position].LogicalName != name {
			t.Fatalf("第 %d 条逻辑名应为 %s，实际 %s", position, name, index.Objects[position].LogicalName)
		}
	}
}

// TestParseAssetIndexRejectsInvalidInput 覆盖结构校验的五类拒绝（卡面第 3 条）。
func TestParseAssetIndexRejectsInvalidInput(t *testing.T) {
	validHash := strings.Repeat("d", 40)
	cases := []struct {
		name string
		raw  []byte
	}{
		{name: "空内容", raw: []byte{}},
		{name: "非法JSON", raw: []byte(`{"objects":`)},
		{name: "不是对象", raw: []byte(`[]`)},
		{name: "缺少objects", raw: []byte(`{"virtual":true}`)},
		{name: "objects为空", raw: assetBuildIndexJSON(t, map[string]assetIndexObjectEntry{}, false, false)},
		{name: "空逻辑名", raw: assetBuildIndexJSON(t, map[string]assetIndexObjectEntry{"": assetEntry(validHash, 1)}, false, false)},
		{name: "越界逻辑名", raw: assetBuildIndexJSON(t, map[string]assetIndexObjectEntry{"../escape.txt": assetEntry(validHash, 1)}, false, false)},
		{name: "哈希为大写", raw: assetBuildIndexJSON(t, map[string]assetIndexObjectEntry{"a.txt": assetEntry(strings.ToUpper(validHash), 1)}, false, false)},
		{name: "哈希长度不足", raw: assetBuildIndexJSON(t, map[string]assetIndexObjectEntry{"a.txt": assetEntry("abc", 1)}, false, false)},
		{name: "哈希含非十六进制", raw: assetBuildIndexJSON(t, map[string]assetIndexObjectEntry{"a.txt": assetEntry(strings.Repeat("z", 40), 1)}, false, false)},
		{name: "大小为零", raw: assetBuildIndexJSON(t, map[string]assetIndexObjectEntry{"a.txt": assetEntry(validHash, 0)}, false, false)},
		{name: "大小为负", raw: assetBuildIndexJSON(t, map[string]assetIndexObjectEntry{"a.txt": assetEntry(validHash, -5)}, false, false)},
		{name: "单对象超限", raw: assetBuildIndexJSON(t, map[string]assetIndexObjectEntry{"a.txt": assetEntry(validHash, assetMaxObjectBytes+1)}, false, false)},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := ParseAssetIndex(testCase.raw, AssetIndexExpectation{IndexID: "1.21"})
			requireAssetError(t, err, ErrorCodeAssetIndex)
		})
	}
}

// TestParseAssetIndexRejectsTooManyObjects 校验条目数上限。
func TestParseAssetIndexRejectsTooManyObjects(t *testing.T) {
	hash := strings.Repeat("e", 40)
	var builder strings.Builder
	builder.WriteString(`{"objects":{`)
	for position := 0; position < assetMaxObjects+1; position++ {
		if position > 0 {
			builder.WriteString(",")
		}
		fmt.Fprintf(&builder, `"minecraft/file_%d.txt":{"hash":"%s","size":1}`, position, hash)
	}
	builder.WriteString(`}}`)

	_, err := ParseAssetIndex([]byte(builder.String()), AssetIndexExpectation{IndexID: "1.21"})
	requireAssetError(t, err, ErrorCodeAssetIndex)
	if err.Message != messageAssetTooManyObjects {
		t.Fatalf("应命中条目数上限文案，实际: %s", err.Message)
	}
}

// TestParseAssetIndexWarnsTotalSizeMismatch 校验 totalSize 不一致只告警。
func TestParseAssetIndexWarnsTotalSizeMismatch(t *testing.T) {
	hash := strings.Repeat("f", 40)
	raw := assetBuildIndexJSON(t, map[string]assetIndexObjectEntry{
		"a.txt": assetEntry(hash, 10),
	}, false, false)

	index, err := ParseAssetIndex(raw, AssetIndexExpectation{IndexID: "1.21", DeclaredTotalSize: 99})
	if err != nil {
		t.Fatalf("totalSize 不一致不应导致失败: %v", err)
	}
	if len(index.Warnings) != 1 || index.Warnings[0].Code != WarningAssetTotalSizeMismatch {
		t.Fatalf("应产生 totalSize 不一致告警: %+v", index.Warnings)
	}
	if !indexSummaryOf(index).TotalSizeMismatch {
		t.Fatal("摘要中的 TotalSizeMismatch 应为 true")
	}

	// 声明值与累加值一致时不应告警。
	index, err = ParseAssetIndex(raw, AssetIndexExpectation{IndexID: "1.21", DeclaredTotalSize: 10})
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(index.Warnings) != 0 {
		t.Fatalf("一致的 totalSize 不应告警: %+v", index.Warnings)
	}
}

// TestParseAssetIndexWarnsMapToResources 校验 map_to_resources 只提示不启用。
func TestParseAssetIndexWarnsMapToResources(t *testing.T) {
	hash := strings.Repeat("1", 40)
	raw := assetBuildIndexJSON(t, map[string]assetIndexObjectEntry{
		"a.txt": assetEntry(hash, 1),
	}, false, true)

	index, err := ParseAssetIndex(raw, AssetIndexExpectation{IndexID: "1.21"})
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if !index.MapToResources {
		t.Fatal("map_to_resources 标志未被读取")
	}
	if len(index.Warnings) != 1 || index.Warnings[0].Code != WarningAssetMapToResourcesIgnored {
		t.Fatalf("应提示 map_to_resources 被忽略: %+v", index.Warnings)
	}
}

// TestParseAssetIndexCountsUniqueHashes 校验同一哈希被多个逻辑名共享时的去重。
func TestParseAssetIndexCountsUniqueHashes(t *testing.T) {
	shared := strings.Repeat("2", 40)
	other := strings.Repeat("3", 40)
	raw := assetBuildIndexJSON(t, map[string]assetIndexObjectEntry{
		"minecraft/lang/en_US.lang": assetEntry(shared, 5),
		"minecraft/lang/en_GB.lang": assetEntry(shared, 5),
		"minecraft/sounds/a.ogg":    assetEntry(other, 7),
	}, false, false)

	index, err := ParseAssetIndex(raw, AssetIndexExpectation{IndexID: "1.21"})
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(index.Objects) != 3 {
		t.Fatalf("条目数不符: %d", len(index.Objects))
	}
	if index.UniqueHashes != 2 {
		t.Fatalf("去重哈希数应为 2，实际 %d", index.UniqueHashes)
	}
	if index.TotalBytes != 17 {
		t.Fatalf("总字节数应为 17（含重复），实际 %d", index.TotalBytes)
	}
}

// TestLoadAssetIndexReadsFile 校验从数据根读取索引的路径规则。
func TestLoadAssetIndexReadsFile(t *testing.T) {
	dataRoot := t.TempDir()
	hash := strings.Repeat("4", 40)
	raw := assetBuildIndexJSON(t, map[string]assetIndexObjectEntry{"a.txt": assetEntry(hash, 3)}, false, false)

	relPath, pathErr := artifact.AssetIndexRelPath("1.21")
	if pathErr != nil {
		t.Fatalf("生成索引路径失败: %v", pathErr)
	}
	absolute := filepath.Join(dataRoot, filepath.FromSlash(relPath))
	if mkdirErr := os.MkdirAll(filepath.Dir(absolute), 0o755); mkdirErr != nil {
		t.Fatalf("创建索引目录失败: %v", mkdirErr)
	}
	if writeErr := os.WriteFile(absolute, raw, 0o644); writeErr != nil {
		t.Fatalf("写入索引失败: %v", writeErr)
	}

	index, err := LoadAssetIndex(dataRoot, "1.21")
	if err != nil {
		t.Fatalf("读取索引失败: %v", err)
	}
	if index.ID != "1.21" || len(index.Objects) != 1 {
		t.Fatalf("索引内容不符: %+v", index)
	}
}

// TestLoadAssetIndexRejectsMissingFile 校验索引不存在时的错误码。
func TestLoadAssetIndexRejectsMissingFile(t *testing.T) {
	_, err := LoadAssetIndex(t.TempDir(), "1.21")
	requireAssetError(t, err, ErrorCodeAssetIndex)
	if err.Message != messageAssetIndexMissing {
		t.Fatalf("应命中缺失文案，实际: %s", err.Message)
	}
}

// TestLoadAssetIndexRejectsOversizedFile 校验超过 64 MiB 的索引被拒绝。
func TestLoadAssetIndexRejectsOversizedFile(t *testing.T) {
	dataRoot := t.TempDir()
	relPath, pathErr := artifact.AssetIndexRelPath("1.21")
	if pathErr != nil {
		t.Fatalf("生成索引路径失败: %v", pathErr)
	}
	absolute := filepath.Join(dataRoot, filepath.FromSlash(relPath))
	if mkdirErr := os.MkdirAll(filepath.Dir(absolute), 0o755); mkdirErr != nil {
		t.Fatalf("创建索引目录失败: %v", mkdirErr)
	}
	file, createErr := os.Create(absolute)
	if createErr != nil {
		t.Fatalf("创建大文件失败: %v", createErr)
	}
	// 用 Truncate 制造稀疏文件：只为让 Stat 返回超限大小，不真的写 64 MiB。
	if truncateErr := file.Truncate(assetMaxIndexBytes + 1); truncateErr != nil {
		_ = file.Close()
		t.Fatalf("扩展文件失败: %v", truncateErr)
	}
	if closeErr := file.Close(); closeErr != nil {
		t.Fatalf("关闭文件失败: %v", closeErr)
	}

	_, err := LoadAssetIndex(dataRoot, "1.21")
	requireAssetError(t, err, ErrorCodeAssetIndex)
	if err.Message != messageAssetIndexTooLarge {
		t.Fatalf("应命中大小上限文案，实际: %s", err.Message)
	}
}

// TestInspectAssetObjectStates 覆盖对象状态的三种判定。
func TestInspectAssetObjectStates(t *testing.T) {
	dataRoot := t.TempDir()
	content := []byte("asset-object-content")
	hash, size := assetTestWriteObject(t, dataRoot, content)

	state, err := InspectAssetObject(dataRoot, hash, size)
	if err != nil || state != AssetObjectVerified {
		t.Fatalf("应判定为已验证，实际 %s（%v）", state, err)
	}

	// 大小不符。
	state, err = InspectAssetObject(dataRoot, hash, size+1)
	if err != nil || state != AssetObjectCorrupt {
		t.Fatalf("大小不符应判定为损坏，实际 %s（%v）", state, err)
	}

	// 同大小但内容不同：覆盖文件后哈希必然不符。
	corruptPath := filepath.Join(dataRoot, filepath.FromSlash(mustObjectRelPath(t, hash)))
	replacement := []byte("asset-object-CONTENT")
	if len(replacement) != len(content) {
		t.Fatalf("测试自身错误：替换内容长度必须一致")
	}
	if writeErr := os.WriteFile(corruptPath, replacement, 0o644); writeErr != nil {
		t.Fatalf("覆盖对象失败: %v", writeErr)
	}
	state, err = InspectAssetObject(dataRoot, hash, size)
	if err != nil || state != AssetObjectCorrupt {
		t.Fatalf("内容不符应判定为损坏，实际 %s（%v）", state, err)
	}

	// 不存在。
	missingHash := strings.Repeat("9", 40)
	state, err = InspectAssetObject(dataRoot, missingHash, 10)
	if err != nil || state != AssetObjectMissing {
		t.Fatalf("不存在应判定为缺失，实际 %s（%v）", state, err)
	}

	// 非法哈希。
	_, err = InspectAssetObject(dataRoot, "not-a-hash", 10)
	requireAssetError(t, err, ErrorCodeAssetIndex)
}

// mustObjectRelPath 返回对象在共享缓存中的相对路径（测试辅助）。
func mustObjectRelPath(t *testing.T, hash string) string {
	t.Helper()
	relPath, pathErr := artifact.AssetObjectRelPath(hash)
	if pathErr != nil {
		t.Fatalf("生成对象路径失败: %v", pathErr)
	}
	return relPath
}

// TestExpandAssetObjectsDeduplicates 校验同哈希只生成一个计划项。
func TestExpandAssetObjectsDeduplicates(t *testing.T) {
	dataRoot := t.TempDir()
	shared := []byte("shared-bytes")
	hash := assetTestSHA1(shared)

	raw := assetBuildIndexJSON(t, map[string]assetIndexObjectEntry{
		"minecraft/lang/en_US.lang": assetEntry(hash, int64(len(shared))),
		"minecraft/lang/en_GB.lang": assetEntry(hash, int64(len(shared))),
		"minecraft/lang/de_DE.lang": assetEntry(hash, int64(len(shared))),
	}, false, false)
	index, parseErr := ParseAssetIndex(raw, AssetIndexExpectation{IndexID: "1.21"})
	if parseErr != nil {
		t.Fatalf("解析索引失败: %v", parseErr)
	}

	expansion, err := ExpandAssetObjects(dataRoot, index, AssetExpandOptions{VerifyExisting: true})
	if err != nil {
		t.Fatalf("展开失败: %v", err)
	}
	if expansion.UniqueObjects != 1 {
		t.Fatalf("唯一对象数应为 1，实际 %d", expansion.UniqueObjects)
	}
	if len(expansion.Items) != 1 {
		t.Fatalf("计划项数应为 1，实际 %d", len(expansion.Items))
	}
	item := expansion.Items[0]
	if item.TaskType != TaskTypeAsset {
		t.Fatalf("任务类型不符: %s", item.TaskType)
	}
	if item.TargetKind != TargetKindShared {
		t.Fatalf("目标类别不符: %s", item.TargetKind)
	}
	if item.ExpectedSHA1 != hash {
		t.Fatalf("期望哈希不符: %s", item.ExpectedSHA1)
	}
	if item.TargetRelPath != mustObjectRelPath(t, hash) {
		t.Fatalf("目标路径应为共享对象路径，实际 %s", item.TargetRelPath)
	}
	if item.ExpectedSize != int64(len(shared)) {
		t.Fatalf("期望大小不符: %d", item.ExpectedSize)
	}
	if !strings.HasPrefix(item.SourceURI, OfficialAssetObjectBaseURL) {
		t.Fatalf("默认应使用官方基址，实际 %s", item.SourceURI)
	}
}

// TestExpandAssetObjectsSkipsVerifiedObjects 校验已验证对象直接标记完成。
func TestExpandAssetObjectsSkipsVerifiedObjects(t *testing.T) {
	dataRoot := t.TempDir()
	present := []byte("already-present")
	hash, size := assetTestWriteObject(t, dataRoot, present)

	raw := assetBuildIndexJSON(t, map[string]assetIndexObjectEntry{
		"minecraft/lang/en_US.lang": assetEntry(hash, size),
	}, false, false)
	index, parseErr := ParseAssetIndex(raw, AssetIndexExpectation{IndexID: "1.21"})
	if parseErr != nil {
		t.Fatalf("解析索引失败: %v", parseErr)
	}

	expansion, err := ExpandAssetObjects(dataRoot, index, AssetExpandOptions{VerifyExisting: true})
	if err != nil {
		t.Fatalf("展开失败: %v", err)
	}
	if len(expansion.Items) != 0 {
		t.Fatalf("已验证对象不应生成计划项，实际 %d", len(expansion.Items))
	}
	if expansion.AlreadyPresent != 1 {
		t.Fatalf("已验证计数应为 1，实际 %d", expansion.AlreadyPresent)
	}
}

// TestExpandAssetObjectsReportsCorruptObjects 校验损坏对象仍会生成计划项。
func TestExpandAssetObjectsReportsCorruptObjects(t *testing.T) {
	dataRoot := t.TempDir()
	hash, size := assetTestWriteObject(t, dataRoot, []byte("good-content"))

	// 覆盖为同长度的不同内容。
	absolute := filepath.Join(dataRoot, filepath.FromSlash(mustObjectRelPath(t, hash)))
	if writeErr := os.WriteFile(absolute, []byte("bad--content"), 0o644); writeErr != nil {
		t.Fatalf("覆盖对象失败: %v", writeErr)
	}
	if int64(len("bad--content")) != size {
		t.Fatalf("测试自身错误：替换内容长度必须一致")
	}

	raw := assetBuildIndexJSON(t, map[string]assetIndexObjectEntry{
		"minecraft/sounds/a.ogg": assetEntry(hash, size),
	}, false, false)
	index, parseErr := ParseAssetIndex(raw, AssetIndexExpectation{IndexID: "1.21"})
	if parseErr != nil {
		t.Fatalf("解析索引失败: %v", parseErr)
	}

	expansion, err := ExpandAssetObjects(dataRoot, index, AssetExpandOptions{VerifyExisting: true})
	if err != nil {
		t.Fatalf("展开失败: %v", err)
	}
	if expansion.CorruptObjects != 1 {
		t.Fatalf("损坏计数应为 1，实际 %d", expansion.CorruptObjects)
	}
	if len(expansion.Items) != 1 {
		t.Fatalf("损坏对象应重新下载，实际计划项 %d", len(expansion.Items))
	}
}

// TestExpandAssetObjectsIsDeterministic 校验输出顺序与输入顺序无关。
func TestExpandAssetObjectsIsDeterministic(t *testing.T) {
	dataRoot := t.TempDir()
	contents := [][]byte{[]byte("one"), []byte("two"), []byte("three")}
	first, _ := assetTestIndex(t, dataRoot, contents)

	// 构造一个逻辑名与内容反向排列的索引，展开结果必须完全相同。
	reverseObjects := make(map[string]assetIndexObjectEntry, len(contents))
	names := []string{"minecraft/a.txt", "minecraft/b.txt", "minecraft/c.txt"}
	for position, content := range contents {
		reverseObjects[names[len(names)-1-position]] = assetEntry(assetTestSHA1(content), int64(len(content)))
	}
	second, parseErr := ParseAssetIndex(
		assetBuildIndexJSON(t, reverseObjects, false, false),
		AssetIndexExpectation{IndexID: "1.21"},
	)
	if parseErr != nil {
		t.Fatalf("解析索引失败: %v", parseErr)
	}

	firstExpansion, firstErr := ExpandAssetObjects(dataRoot, first, AssetExpandOptions{VerifyExisting: false})
	if firstErr != nil {
		t.Fatalf("展开失败: %v", firstErr)
	}
	secondExpansion, secondErr := ExpandAssetObjects(dataRoot, second, AssetExpandOptions{VerifyExisting: false})
	if secondErr != nil {
		t.Fatalf("展开失败: %v", secondErr)
	}
	if len(firstExpansion.Items) != len(secondExpansion.Items) {
		t.Fatalf("两次展开的计划项数不同: %d != %d", len(firstExpansion.Items), len(secondExpansion.Items))
	}
	for position := range firstExpansion.Items {
		if firstExpansion.Items[position].TargetRelPath != secondExpansion.Items[position].TargetRelPath {
			t.Fatalf("第 %d 项目标路径不同: %s != %s",
				position,
				firstExpansion.Items[position].TargetRelPath,
				secondExpansion.Items[position].TargetRelPath)
		}
	}
}

// TestAssetIndexPlanItem 校验索引自身计划项的字段。
func TestAssetIndexPlanItem(t *testing.T) {
	hash := strings.Repeat("5", 40)
	item, err := AssetIndexPlanItem("1.21", "https://piston-meta.mojang.com/mc/assets/1.21.json", 1234, hash)
	if err != nil {
		t.Fatalf("生成索引计划项失败: %v", err)
	}
	if item.TaskType != TaskTypeAssetIndex {
		t.Fatalf("任务类型不符: %s", item.TaskType)
	}
	if item.TargetKind != TargetKindShared {
		t.Fatalf("目标类别不符: %s", item.TargetKind)
	}
	if item.ExpectedSize != 1234 || item.ExpectedSHA1 != hash {
		t.Fatalf("期望值不符: %+v", item)
	}
	if item.TargetRelPath != "assets/indexes/1.21.json" {
		t.Fatalf("索引目标路径不符: %s", item.TargetRelPath)
	}

	if _, sizeErr := AssetIndexPlanItem("1.21", "https://example.invalid/x", 0, hash); sizeErr == nil {
		t.Fatal("大小非法时应返回错误")
	}
	if _, hashErr := AssetIndexPlanItem("1.21", "https://example.invalid/x", 10, "short"); hashErr == nil {
		t.Fatal("哈希非法时应返回错误")
	}
}

// withAssetLinkFaults 替换链接操作用于验证回退链，并在测试结束时恢复。
func withAssetLinkFaults(t *testing.T, hardLink func(string, string) error, symlink func(string, string) error) {
	t.Helper()
	originalHardLink := assetLinkOperations.hardLink
	originalSymlink := assetLinkOperations.symlink
	assetLinkOperations.hardLink = hardLink
	assetLinkOperations.symlink = symlink
	t.Cleanup(func() {
		assetLinkOperations.hardLink = originalHardLink
		assetLinkOperations.symlink = originalSymlink
	})
}

// assetFailLink 是恒失败的链接替身。
func assetFailLink(string, string) error {
	return fmt.Errorf("链接被测试禁用以触发回退")
}

// TestAssembleVirtualAssetsHardLinks 校验首选硬链接路径。
func TestAssembleVirtualAssetsHardLinks(t *testing.T) {
	objectsDir := t.TempDir()
	targetDir := filepath.Join(t.TempDir(), AssetVirtualSubdir, "1.21")
	content := []byte("virtual-hardlink-content")
	hash, size := assetTestWriteObject(t, objectsDir, content)

	index, _ := assetTestIndex(t, objectsDir, [][]byte{content})
	assembly, err := AssembleVirtualAssets(objectsDir, targetDir, index)
	if err != nil {
		t.Fatalf("组装失败: %v", err)
	}
	if assembly.Mode != LinkModeHardLink {
		t.Fatalf("应记录硬链接，实际 %q", assembly.Mode)
	}
	if assembly.HardLinked != 1 || assembly.Linked != 1 || assembly.Copied != 0 {
		t.Fatalf("计数不符: %+v", assembly)
	}

	target := filepath.Join(targetDir, "minecraft", "lang", "file_0.lang")
	stored, readErr := os.ReadFile(target)
	if readErr != nil {
		t.Fatalf("读取组装结果失败: %v", readErr)
	}
	if string(stored) != string(content) {
		t.Fatalf("组装内容不符: %q", string(stored))
	}
	if int64(len(stored)) != size {
		t.Fatalf("组装大小不符: %d", len(stored))
	}
	_ = hash
}

// TestAssembleVirtualAssetsFallsBackToSymlink 校验硬链接失败后回退软链接。
func TestAssembleVirtualAssetsFallsBackToSymlink(t *testing.T) {
	objectsDir := t.TempDir()
	targetDir := filepath.Join(t.TempDir(), AssetVirtualSubdir, "1.21")
	content := []byte("virtual-symlink-content")
	assetTestWriteObject(t, objectsDir, content)
	index, _ := assetTestIndex(t, objectsDir, [][]byte{content})

	// 用真实的 os.Link 冒充软链接：本用例只验证分支与计数，
	// 不验证符号链接语义（Windows 上创建符号链接需要额外权限，
	// 强行验证会让测试依赖运行环境）。
	withAssetLinkFaults(t, assetFailLink, os.Link)

	assembly, err := AssembleVirtualAssets(objectsDir, targetDir, index)
	if err != nil {
		t.Fatalf("组装失败: %v", err)
	}
	if assembly.Mode != LinkModeSymlink {
		t.Fatalf("应记录软链接，实际 %q", assembly.Mode)
	}
	if assembly.Symlinked != 1 || assembly.Linked != 1 || assembly.Copied != 0 {
		t.Fatalf("计数不符: %+v", assembly)
	}
	stored, readErr := os.ReadFile(filepath.Join(targetDir, "minecraft", "lang", "file_0.lang"))
	if readErr != nil || string(stored) != string(content) {
		t.Fatalf("组装内容不符: %q（%v）", string(stored), readErr)
	}
}

// TestAssembleVirtualAssetsFallsBackToCopy 校验两种链接都失败后回退复制。
func TestAssembleVirtualAssetsFallsBackToCopy(t *testing.T) {
	objectsDir := t.TempDir()
	targetDir := filepath.Join(t.TempDir(), AssetVirtualSubdir, "1.21")
	content := []byte("virtual-copy-content")
	assetTestWriteObject(t, objectsDir, content)
	index, _ := assetTestIndex(t, objectsDir, [][]byte{content})

	withAssetLinkFaults(t, assetFailLink, assetFailLink)

	assembly, err := AssembleVirtualAssets(objectsDir, targetDir, index)
	if err != nil {
		t.Fatalf("组装失败: %v", err)
	}
	if assembly.Mode != LinkModeCopy {
		t.Fatalf("应记录复制，实际 %q", assembly.Mode)
	}
	if assembly.Copied != 1 || assembly.Linked != 0 {
		t.Fatalf("计数不符: %+v", assembly)
	}
	stored, readErr := os.ReadFile(filepath.Join(targetDir, "minecraft", "lang", "file_0.lang"))
	if readErr != nil || string(stored) != string(content) {
		t.Fatalf("组装内容不符: %q（%v）", string(stored), readErr)
	}
}

// TestAssembleVirtualAssetsSkipsMissingSource 校验源对象缺失只跳过并告警。
func TestAssembleVirtualAssetsSkipsMissingSource(t *testing.T) {
	objectsDir := t.TempDir()
	targetDir := filepath.Join(t.TempDir(), AssetVirtualSubdir, "1.21")
	// 故意不写任何对象文件。
	index, _ := assetTestIndex(t, objectsDir, [][]byte{[]byte("missing-one"), []byte("missing-two")})

	assembly, err := AssembleVirtualAssets(objectsDir, targetDir, index)
	if err != nil {
		t.Fatalf("源缺失不应导致失败: %v", err)
	}
	if assembly.Skipped != 2 {
		t.Fatalf("跳过数应为 2，实际 %d", assembly.Skipped)
	}
	if assembly.Mode != "" {
		t.Fatalf("没有任何文件被组装时 Mode 应为空，实际 %q", assembly.Mode)
	}
	if len(assembly.Warnings) != 1 || assembly.Warnings[0].Code != WarningAssetVirtualSourceMissing {
		t.Fatalf("应产生一条源缺失告警: %+v", assembly.Warnings)
	}
}

// TestAssembleVirtualAssetsSkipsVerifiedTarget 校验重复组装是幂等的。
func TestAssembleVirtualAssetsSkipsVerifiedTarget(t *testing.T) {
	objectsDir := t.TempDir()
	targetDir := filepath.Join(t.TempDir(), AssetVirtualSubdir, "1.21")
	content := []byte("idempotent-content")
	assetTestWriteObject(t, objectsDir, content)
	index, _ := assetTestIndex(t, objectsDir, [][]byte{content})

	if _, firstErr := AssembleVirtualAssets(objectsDir, targetDir, index); firstErr != nil {
		t.Fatalf("首次组装失败: %v", firstErr)
	}
	second, err := AssembleVirtualAssets(objectsDir, targetDir, index)
	if err != nil {
		t.Fatalf("二次组装失败: %v", err)
	}
	if second.AlreadyPresent != 1 {
		t.Fatalf("已存在计数应为 1，实际 %d", second.AlreadyPresent)
	}
	if second.Linked != 0 || second.Copied != 0 {
		t.Fatalf("二次组装不应重新链接: %+v", second)
	}
	if second.Mode != "" {
		t.Fatalf("全部跳过时 Mode 应为空，实际 %q", second.Mode)
	}
}

// TestAssembleVirtualAssetsRebuildsCorruptTarget 校验目标内容不同时被重建。
func TestAssembleVirtualAssetsRebuildsCorruptTarget(t *testing.T) {
	objectsDir := t.TempDir()
	targetDir := filepath.Join(t.TempDir(), AssetVirtualSubdir, "1.21")
	content := []byte("rebuild-content")
	assetTestWriteObject(t, objectsDir, content)
	index, _ := assetTestIndex(t, objectsDir, [][]byte{content})

	targetPath := filepath.Join(targetDir, "minecraft", "lang", "file_0.lang")
	if mkdirErr := os.MkdirAll(filepath.Dir(targetPath), 0o755); mkdirErr != nil {
		t.Fatalf("创建目标目录失败: %v", mkdirErr)
	}
	if writeErr := os.WriteFile(targetPath, []byte("stale-content!!"), 0o644); writeErr != nil {
		t.Fatalf("写入旧内容失败: %v", writeErr)
	}

	assembly, err := AssembleVirtualAssets(objectsDir, targetDir, index)
	if err != nil {
		t.Fatalf("组装失败: %v", err)
	}
	if assembly.HardLinked != 1 {
		t.Fatalf("旧内容应被重建，实际计数 %+v", assembly)
	}
	stored, readErr := os.ReadFile(targetPath)
	if readErr != nil || string(stored) != string(content) {
		t.Fatalf("重建后内容不符: %q（%v）", string(stored), readErr)
	}
}

// TestAssembleVirtualAssetsRejectsInvalidLogicalName 校验越界逻辑名被拒绝。
func TestAssembleVirtualAssetsRejectsInvalidLogicalName(t *testing.T) {
	objectsDir := t.TempDir()
	targetDir := filepath.Join(t.TempDir(), AssetVirtualSubdir, "1.21")
	index := AssetIndex{
		ID: "1.21",
		Objects: []AssetObject{
			{LogicalName: "../escape.txt", Hash: strings.Repeat("6", 40), Size: 1},
		},
	}

	_, err := AssembleVirtualAssets(objectsDir, targetDir, index)
	requireAssetError(t, err, ErrorCodeAssetLayout)
}

// TestAssetVirtualTargetDir 校验虚拟布局目标目录的路径规则。
func TestAssetVirtualTargetDir(t *testing.T) {
	dataRoot := t.TempDir()
	targetDir, err := AssetVirtualTargetDir(dataRoot, "1.21", "5")
	if err != nil {
		t.Fatalf("生成虚拟目录失败: %v", err)
	}
	wanted := filepath.Join(dataRoot, "versions", "1.21", AssetVirtualSubdir, "5")
	if targetDir != wanted {
		t.Fatalf("虚拟目录不符: %s != %s", targetDir, wanted)
	}
	if !strings.Contains(targetDir, filepath.Join("versions", "1.21")) {
		t.Fatalf("虚拟目录必须位于版本私有目录：%s", targetDir)
	}

	if _, emptyErr := AssetVirtualTargetDir(dataRoot, "", "5"); emptyErr == nil {
		t.Fatal("版本标识为空时应返回错误")
	}
	if _, escapeErr := AssetVirtualTargetDir(dataRoot, "../escape", "5"); escapeErr == nil {
		t.Fatal("越界版本标识应被拒绝")
	}
}

// TestAssetProgressTrackerThrottles 校验父任务进度的事件节流。
func TestAssetProgressTrackerThrottles(t *testing.T) {
	dataRoot := t.TempDir()
	index, _ := assetTestIndex(t, dataRoot, [][]byte{[]byte("a"), []byte("bb"), []byte("ccc")})

	clock := int64(1000)
	tracker := NewAssetProgressTrackerWithClock("task-1", index, func() int64 { return clock })

	// 首条一定发送。
	progress, emit := tracker.RecordObject(assetTestSHA1([]byte("a")), 1, true)
	if !emit {
		t.Fatal("首条记录必须发送事件")
	}
	if progress.Progress <= 0 || progress.State != "downloading" {
		t.Fatalf("首条进度不符: %+v", progress)
	}

	// 500ms 内的第二条被节流。
	clock += 100
	if _, emit := tracker.RecordObject(assetTestSHA1([]byte("bb")), 2, true); emit {
		t.Fatal("间隔不足 500ms 时不应发送事件")
	}

	// 满 500ms 后恢复发送。
	clock += 400
	if _, emit := tracker.RecordObject(assetTestSHA1([]byte("ccc")), 3, true); !emit {
		t.Fatal("间隔达到 500ms 时应发送事件")
	}

	// 终态必须送达（即使距上次不足 500ms）。
	snapshot := tracker.Snapshot()
	if snapshot.State != "completed" {
		t.Fatalf("全部完成后状态应为 completed，实际 %s", snapshot.State)
	}
	if snapshot.CompletedObjects != 3 || snapshot.FailedObjects != 0 {
		t.Fatalf("计数不符: %+v", snapshot)
	}
	if snapshot.Progress != 1 {
		t.Fatalf("完成后进度应为 1，实际 %f", snapshot.Progress)
	}
	if snapshot.TotalBytes != 6 || snapshot.CompletedBytes != 6 {
		t.Fatalf("字节数不符: %+v", snapshot)
	}
}

// TestAssetProgressTrackerFailureThreshold 校验 5% 失败阈值。
func TestAssetProgressTrackerFailureThreshold(t *testing.T) {
	// 20 个对象，恰好 1 个失败 = 5%，不应超阈值；2 个失败 = 10%，应超阈值。
	objects := make(map[string]assetIndexObjectEntry, 20)
	for position := 0; position < 20; position++ {
		objects[fmt.Sprintf("minecraft/file_%d.txt", position)] = assetEntry(
			fmt.Sprintf("%040d", position), 10)
	}
	index, parseErr := ParseAssetIndex(
		assetBuildIndexJSON(t, objects, false, false),
		AssetIndexExpectation{IndexID: "1.21"},
	)
	if parseErr != nil {
		t.Fatalf("解析索引失败: %v", parseErr)
	}

	tracker := NewAssetProgressTrackerWithClock("task-2", index, func() int64 { return 0 })
	if _, emit := tracker.RecordObject(fmt.Sprintf("%040d", 0), 10, false); !emit {
		t.Fatal("首条记录必须发送事件")
	}
	exceeded, failed, samples := tracker.FailureThresholdExceeded()
	if exceeded {
		t.Fatalf("1/20 失败率恰好 5%%，不应超阈值（failed=%d, samples=%v）", failed, samples)
	}
	if failed != 1 || len(samples) != 1 {
		t.Fatalf("失败统计不符: failed=%d samples=%v", failed, samples)
	}

	tracker.RecordObject(fmt.Sprintf("%040d", 1), 10, false)
	exceeded, failed, _ = tracker.FailureThresholdExceeded()
	if !exceeded {
		t.Fatalf("2/20 失败率 10%% 应超阈值（failed=%d）", failed)
	}
	if snapshot := tracker.Snapshot(); snapshot.State != "failed" {
		t.Fatalf("超阈值后状态应为 failed，实际 %s", snapshot.State)
	}
	if snapshot := tracker.Snapshot(); snapshot.FailureRate != 0.1 {
		t.Fatalf("失败率应为 0.1，实际 %f", snapshot.FailureRate)
	}
}

// TestAssetProgressTrackerSamplesLimit 校验失败样例最多保留 5 条。
func TestAssetProgressTrackerSamplesLimit(t *testing.T) {
	objects := make(map[string]assetIndexObjectEntry, 10)
	for position := 0; position < 10; position++ {
		objects[fmt.Sprintf("minecraft/file_%d.txt", position)] = assetEntry(
			fmt.Sprintf("%040d", position), 1)
	}
	index, parseErr := ParseAssetIndex(
		assetBuildIndexJSON(t, objects, false, false),
		AssetIndexExpectation{IndexID: "1.21"},
	)
	if parseErr != nil {
		t.Fatalf("解析索引失败: %v", parseErr)
	}

	tracker := NewAssetProgressTrackerWithClock("task-3", index, func() int64 { return 0 })
	for position := 0; position < 10; position++ {
		tracker.RecordObject(fmt.Sprintf("%040d", position), 1, false)
	}
	_, failed, samples := tracker.FailureThresholdExceeded()
	if failed != 10 {
		t.Fatalf("失败数应为 10，实际 %d", failed)
	}
	if len(samples) != assetFailureSampleLimit {
		t.Fatalf("样例数应被限制为 %d，实际 %d", assetFailureSampleLimit, len(samples))
	}
	for _, sample := range samples {
		if len(sample) != 8 {
			t.Fatalf("样例应为 8 位哈希前缀，实际 %q", sample)
		}
	}
}

// TestIsAssetSHA1 覆盖哈希格式判定的边界。
func TestIsAssetSHA1(t *testing.T) {
	cases := []struct {
		value string
		want  bool
	}{
		{value: strings.Repeat("a", 40), want: true},
		{value: strings.Repeat("0", 40), want: true},
		{value: strings.Repeat("A", 40), want: false},
		{value: strings.Repeat("a", 39), want: false},
		{value: strings.Repeat("a", 41), want: false},
		{value: strings.Repeat("g", 40), want: false},
		{value: "", want: false},
	}
	for _, testCase := range cases {
		if got := isAssetSHA1(testCase.value); got != testCase.want {
			t.Fatalf("isAssetSHA1(%q) = %v，期望 %v", testCase.value, got, testCase.want)
		}
	}
}

// TestShortHash 校验哈希前缀截断。
func TestShortHash(t *testing.T) {
	if got := shortHash(strings.Repeat("a", 40)); got != "aaaaaaaa" {
		t.Fatalf("截断结果不符: %q", got)
	}
	if got := shortHash("abc"); got != "abc" {
		t.Fatalf("短哈希应原样返回，实际 %q", got)
	}
}

// TestCopyAssetFileFailure 校验复制失败返回 ASSET_LAYOUT_001。
func TestCopyAssetFileFailure(t *testing.T) {
	sourceDir := t.TempDir()
	source := filepath.Join(sourceDir, "source.bin")
	if writeErr := os.WriteFile(source, []byte("payload"), 0o644); writeErr != nil {
		t.Fatalf("写入源文件失败: %v", writeErr)
	}

	// 目标目录不存在：Create 必然失败。
	target := filepath.Join(sourceDir, "missing-dir", "target.bin")
	err := copyAssetFile(source, target)
	requireAssetError(t, err, ErrorCodeAssetLayout)

	// 源文件不存在同样失败。
	if err := copyAssetFile(filepath.Join(sourceDir, "nope.bin"), filepath.Join(sourceDir, "out.bin")); err == nil {
		t.Fatal("源文件不存在时应返回错误")
	}
}

// TestParseAssetIndexWarnsAreCopiedToSummary 校验告警被复制进摘要（避免切片别名）。
func TestParseAssetIndexWarnsAreCopiedToSummary(t *testing.T) {
	hash := strings.Repeat("7", 40)
	raw := assetBuildIndexJSON(t, map[string]assetIndexObjectEntry{"a.txt": assetEntry(hash, 1)}, false, true)
	index, parseErr := ParseAssetIndex(raw, AssetIndexExpectation{IndexID: "1.21"})
	if parseErr != nil {
		t.Fatalf("解析索引失败: %v", parseErr)
	}

	summary := indexSummaryOf(index)
	if len(summary.Warnings) != 1 {
		t.Fatalf("摘要应携带告警: %+v", summary.Warnings)
	}
	summary.Warnings[0].Message = "被外部修改"
	if index.Warnings[0].Message == "被外部修改" {
		t.Fatal("摘要必须复制告警切片，不能与索引共享底层数组")
	}
}
