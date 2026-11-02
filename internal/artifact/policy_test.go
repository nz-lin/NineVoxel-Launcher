package artifact

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// testAbs 构造一个当前平台合法的绝对路径，用于数据根解析测试。
//
// 不调用 t.TempDir：本卡授权只覆盖纯函数，测试不得产生目录创建与文件写入。
func testAbs(segments ...string) string {
	joined := filepath.Join(segments...)
	if runtime.GOOS == "windows" {
		return filepath.Join(`C:\`, joined)
	}
	return filepath.Join(`/`, joined)
}

// requireCode 断言错误码；err 为 nil 时直接失败。
func requireCode(t *testing.T, err *Error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("期望错误码 %s，实际返回 nil", want)
	}
	if err.Code != want {
		t.Fatalf("期望错误码 %s，实际 %s（%s）", want, err.Code, err.Message)
	}
}

// TestResolveTargetMapsKinds 校验 kind → 目标根 → 相对路径形态的映射表。
func TestResolveTargetMapsKinds(t *testing.T) {
	cases := []struct {
		name       string
		kind       ArtifactKind
		input      string
		wantPrefix string
		want       string
	}{
		{
			name:       "library 落共享对象缓存",
			kind:       KindLibrary,
			input:      "com/mojang/brigadier/1.0.18/brigadier-1.0.18.jar",
			wantPrefix: DirArtifactObjects,
			want:       "artifacts/objects/com/mojang/brigadier/1.0.18/brigadier-1.0.18.jar",
		},
		{
			name:       "native 落共享对象缓存",
			kind:       KindNative,
			input:      "natives/lwjgl-3.3.1-natives-windows.jar",
			wantPrefix: DirArtifactObjects,
			want:       "artifacts/objects/natives/lwjgl-3.3.1-natives-windows.jar",
		},
		{
			name:       "asset 落共享对象缓存",
			kind:       KindAsset,
			input:      "ab/abcdef0123456789abcdef0123456789abcdef01",
			wantPrefix: DirArtifactObjects,
			want:       "artifacts/objects/ab/abcdef0123456789abcdef0123456789abcdef01",
		},
		{
			name:       "client 落版本私有目录",
			kind:       KindClient,
			input:      "1.21/1.21.jar",
			wantPrefix: DirVersions,
			want:       "versions/1.21/1.21.jar",
		},
		{
			name:       "runtime 落 runtimes 目录",
			kind:       KindRuntime,
			input:      "jre-legacy/bin/java.exe",
			wantPrefix: DirRuntimes,
			want:       "runtimes/jre-legacy/bin/java.exe",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := ResolveTarget(testCase.kind, testCase.input)
			if err != nil {
				t.Fatalf("ResolveTarget 返回错误：%s", err)
			}
			if got != testCase.want {
				t.Fatalf("期望 %q，实际 %q", testCase.want, got)
			}
			if !strings.HasPrefix(got, testCase.wantPrefix+"/") {
				t.Fatalf("目标路径 %q 未落在期望根 %q 下", got, testCase.wantPrefix)
			}
			if filepath.IsAbs(got) {
				t.Fatalf("目标路径必须是相对路径，实际 %q", got)
			}
		})
	}
}

// TestResolveTargetRejectsInstanceKinds 校验 P3 不写实例目录：实例级内容一律拒绝。
func TestResolveTargetRejectsInstanceKinds(t *testing.T) {
	for _, kind := range []ArtifactKind{KindMod, KindResourcePack, KindShader, KindPlugin, "", "unknown_kind"} {
		got, err := ResolveTarget(kind, "some/path.jar")
		if got != "" {
			t.Fatalf("kind=%q 期望空结果，实际 %q", kind, got)
		}
		requireCode(t, err, ErrorCodeArtifactPath)
	}
}

// TestResolveTargetRejectsUnsafePaths 校验越界与非法路径全部被拒绝。
func TestResolveTargetRejectsUnsafePaths(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{name: "空相对路径", input: ""},
		{name: "只有空白", input: "   "},
		{name: "绝对路径斜杠", input: "/etc/passwd"},
		{name: "绝对路径反斜杠", input: `\Windows\System32`},
		{name: "盘符绝对", input: `C:\Windows\System32\drivers\etc\hosts`},
		{name: "盘符相对", input: `C:foo`},
		{name: "UNC 共享", input: `\\server\share\evil.jar`},
		{name: "NUL 截断", input: "legit.jar\x00../../evil.jar"},
		{name: "开头父目录段", input: "../evil.jar"},
		{name: "中间父目录段", input: "a/../../evil.jar"},
		{name: "反斜杠父目录段", input: `a\..\evil.jar`},
		{name: "斜杠归一化后越界", input: "a/b/../../../evil.jar"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := ResolveTarget(KindLibrary, testCase.input)
			if got != "" {
				t.Fatalf("期望拒绝，实际返回 %q", got)
			}
			requireCode(t, err, ErrorCodeArtifactPath)
		})
	}
}

// TestValidateRelativePathNormalizes 校验分隔符统一与 `.` / `//` 归一化。
func TestValidateRelativePathNormalizes(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{input: "a/b.jar", want: "a/b.jar"},
		{input: "a//b.jar", want: "a/b.jar"},
		{input: "a/./b.jar", want: "a/b.jar"},
		{input: `a\b.jar`, want: "a/b.jar"},
		{input: `a\b/c.jar`, want: "a/b/c.jar"},
		{input: "  spaced/path.jar  ", want: "spaced/path.jar"},
	}

	for _, testCase := range cases {
		t.Run(testCase.input, func(t *testing.T) {
			got, err := ValidateRelativePath(testCase.input)
			if err != nil {
				t.Fatalf("ValidateRelativePath 返回错误：%s", err)
			}
			if got != testCase.want {
				t.Fatalf("期望 %q，实际 %q", testCase.want, got)
			}
		})
	}
}

// TestObjectRelPathContentAddressing 校验内容寻址路径与算法约束（决策 D-27）。
func TestObjectRelPathContentAddressing(t *testing.T) {
	sha1 := strings.Repeat("ab", 20)
	sha256 := strings.Repeat("cd", 32)

	got, err := ObjectRelPath(HashAlgorithmSHA256, sha256)
	if err != nil {
		t.Fatalf("ObjectRelPath 返回错误：%s", err)
	}
	want := "artifacts/objects/cd/" + sha256
	if got != want {
		t.Fatalf("期望 %q，实际 %q", want, got)
	}

	got, err = ObjectRelPath(HashAlgorithmSHA1, sha1)
	if err != nil {
		t.Fatalf("ObjectRelPath(SHA-1) 返回错误：%s", err)
	}
	want = "artifacts/objects/ab/" + sha1
	if got != want {
		t.Fatalf("期望 %q，实际 %q", want, got)
	}

	// SHA-512 不得作为寻址键；长度不匹配与大小写不符的哈希一律拒绝。
	rejects := []struct {
		name      string
		algorithm HashAlgorithm
		hash      string
	}{
		{name: "SHA-512 不作为寻址键", algorithm: HashAlgorithmSHA512, hash: strings.Repeat("ef", 64)},
		{name: "空算法", algorithm: "", hash: sha256},
		{name: "SHA-256 长度不符", algorithm: HashAlgorithmSHA256, hash: sha1},
		{name: "大写十六进制", algorithm: HashAlgorithmSHA256, hash: strings.ToUpper(sha256)},
		{name: "非十六进制字符", algorithm: HashAlgorithmSHA1, hash: strings.Repeat("zz", 20)},
		{name: "空哈希", algorithm: HashAlgorithmSHA256, hash: ""},
	}
	for _, testCase := range rejects {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := ObjectRelPath(testCase.algorithm, testCase.hash)
			if got != "" {
				t.Fatalf("期望拒绝，实际返回 %q", got)
			}
			requireCode(t, err, ErrorCodeArtifactPath)
		})
	}
}

// TestLayoutHelpers 校验索引 / 版本 / Runtime / staging / quarantine 的路径形态。
func TestLayoutHelpers(t *testing.T) {
	indexPath, err := IndexRelPath("17")
	if err != nil || indexPath != "artifacts/indexes/17.json" {
		t.Fatalf("IndexRelPath 结果异常：%q, %v", indexPath, err)
	}

	metadataPath, err := MetadataRelPath("version_manifest_v2.json")
	if err != nil || metadataPath != "artifacts/metadata/version_manifest_v2.json" {
		t.Fatalf("MetadataRelPath 结果异常：%q, %v", metadataPath, err)
	}

	versionPath, err := VersionRelPath("1.21", "1.21.json")
	if err != nil || versionPath != "versions/1.21/1.21.json" {
		t.Fatalf("VersionRelPath 结果异常：%q, %v", versionPath, err)
	}

	runtimePath, err := RuntimeRelPath("jre-legacy", "bin/java.exe")
	if err != nil || runtimePath != "runtimes/jre-legacy/bin/java.exe" {
		t.Fatalf("RuntimeRelPath 结果异常：%q, %v", runtimePath, err)
	}

	stagingPath, err := StagingRelPath("task-1")
	if err != nil || stagingPath != "downloads/staging/task-1.part" {
		t.Fatalf("StagingRelPath 结果异常：%q, %v", stagingPath, err)
	}

	quarantinePath, err := QuarantineRelPath("task-1")
	if err != nil || quarantinePath != "downloads/quarantine/ta/task-1.bad" {
		t.Fatalf("QuarantineRelPath 结果异常：%q, %v", quarantinePath, err)
	}

	// 标识符不得携带路径语义。
	badNames := []string{"", "  ", "../x", "a/b", `a\b`, `C:evil`, ".", ".."}
	for _, name := range badNames {
		if _, err := VersionRelPath(name, "1.21.jar"); err == nil {
			t.Fatalf("VersionRelPath 应拒绝非法版本 id %q", name)
		}
		if _, err := IndexRelPath(name); err == nil {
			t.Fatalf("IndexRelPath 应拒绝非法索引 id %q", name)
		}
		if _, err := StagingRelPath(name); err == nil {
			t.Fatalf("StagingRelPath 应拒绝非法任务 id %q", name)
		}
	}
}

// TestEnsureWithinRootRejectsSiblingPrefix 校验经典前缀误判：C:\game 不得把 C:\game2 判为内部。
func TestEnsureWithinRootRejectsSiblingPrefix(t *testing.T) {
	root := testAbs("game")
	sibling := testAbs("game2")
	siblingFile := filepath.Join(sibling, "evil.jar")
	insideFile := filepath.Join(root, "sub", "ok.jar")

	if err := EnsureWithinRoot(root, siblingFile); err == nil {
		t.Fatalf("同前缀的兄弟目录 %q 不应被判为 %q 的内部路径", siblingFile, root)
	} else {
		requireCode(t, err, ErrorCodeArtifactPath)
	}

	if err := EnsureWithinRoot(root, insideFile); err != nil {
		t.Fatalf("根内路径 %q 被误判为越界：%s", insideFile, err)
	}

	// 根目录自身视为在根内。
	if err := EnsureWithinRoot(root, root); err != nil {
		t.Fatalf("根目录自身应判定为内部：%s", err)
	}

	// 跨卷（Windows 上有效；其他平台由 Rel 返回相对路径，仍然越界）。
	otherVolume := `D:\elsewhere\evil.jar`
	if runtime.GOOS == "windows" {
		if err := EnsureWithinRoot(root, otherVolume); err == nil {
			t.Fatalf("跨卷路径 %q 应被拒绝", otherVolume)
		}
	}
}

// TestEnsureWithinRootCaseInsensitiveOnWindows 校验 Windows 大小写不敏感。
func TestEnsureWithinRootCaseInsensitiveOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("大小写语义仅在 Windows 生效")
	}
	root := `C:\GameData`
	if err := EnsureWithinRoot(root, `c:\gamedata\versions\1.21`); err != nil {
		t.Fatalf("Windows 大小写不同不应判为越界：%s", err)
	}
}

// TestResolveInsideRootRejectsEscapes 校验绝对路径入口的越界防护。
func TestResolveInsideRootRejectsEscapes(t *testing.T) {
	root := testAbs("data")

	for _, rel := range []string{"../evil", "a/../../evil", `C:\Windows`, "/etc/passwd", "bad\x00name"} {
		if _, err := ResolveInsideRoot(root, rel); err == nil {
			t.Fatalf("相对路径 %q 应被拒绝", rel)
		}
	}

	// 空相对路径对齐 resolveInside 语义：返回根目录本身。
	got, err := ResolveInsideRoot(root, "")
	if err != nil || got != root {
		t.Fatalf("空相对路径应返回根目录 %q，实际 %q, %v", root, got, err)
	}

	// 根内路径（不存在）应返回拼接结果且不报错。
	want := filepath.Join(root, "versions", "1.21", "1.21.json")
	got, err = ResolveInsideRoot(root, "versions/1.21/1.21.json")
	if err != nil {
		t.Fatalf("根内路径被拒绝：%s", err)
	}
	if got != want {
		t.Fatalf("期望 %q，实际 %q", want, got)
	}

	// 数据根非法时拒绝。
	if _, err := ResolveInsideRoot("relative/root", "a.jar"); err == nil {
		t.Fatal("相对数据根应被拒绝")
	}
	if _, err := ResolveInsideRoot("", "a.jar"); err == nil {
		t.Fatal("空数据根应被拒绝")
	}
}

// TestResolveDataRootPolicy 校验数据根优先级与三态原因。
func TestResolveDataRootPolicy(t *testing.T) {
	userDir := testAbs("home", "user", ".config", "NineVoxel-Launcher", "data")
	portableDir := testAbs("opt", "launcher", "data")

	// 不能用 filepath.Join 构造带 `..` 的路径：Join 会直接做 Clean 把 `..` 消掉，
	// 测试就失去了「未归一化的数据根必须被拒绝」这个断言点。
	parentSegmentDir := userDir + string(filepath.Separator) + ".." + string(filepath.Separator) + "evil"

	cases := []struct {
		name     string
		input    DataRootInput
		wantRoot string
		wantCode string
		wantReas string
	}{
		{
			name:     "用户目录可写时优先（安装版）",
			input:    DataRootInput{UserDataDir: userDir, UserDataWritable: true, PortableDataDir: portableDir},
			wantRoot: userDir,
		},
		{
			name: "便携版 data 已存在时使用便携目录",
			input: DataRootInput{
				UserDataDir:          userDir,
				UserDataWritable:     true,
				PortableDataDir:      portableDir,
				PortableDataExists:   true,
				PortableDataWritable: true,
			},
			wantRoot: portableDir,
		},
		{
			name: "便携版不可写时报错且不回退用户目录",
			input: DataRootInput{
				UserDataDir:          userDir,
				UserDataWritable:     true,
				PortableDataDir:      portableDir,
				PortableDataExists:   true,
				PortableDataWritable: false,
			},
			wantCode: ErrorCodeArtifactLayout,
			wantReas: DataRootReasonNotWritable,
		},
		{
			name:     "用户目录不可写时回退 exe 同级 data",
			input:    DataRootInput{UserDataDir: userDir, UserDataWritable: false, PortableDataDir: portableDir, PortableDataWritable: true},
			wantRoot: portableDir,
		},
		{
			name:     "两处都拿不到时报告未配置",
			input:    DataRootInput{},
			wantCode: ErrorCodeArtifactLayout,
			wantReas: DataRootReasonNotConfigured,
		},
		{
			name:     "两处都不可写时报告不可写",
			input:    DataRootInput{UserDataDir: userDir, PortableDataDir: portableDir},
			wantCode: ErrorCodeArtifactLayout,
			wantReas: DataRootReasonNotWritable,
		},
		{
			name:     "相对数据根视为路径非法",
			input:    DataRootInput{UserDataDir: "relative/data", UserDataWritable: true},
			wantCode: ErrorCodeArtifactLayout,
			wantReas: DataRootReasonInvalidPath,
		},
		{
			name:     "含父目录段的数据根视为路径非法",
			input:    DataRootInput{UserDataDir: parentSegmentDir, UserDataWritable: true},
			wantCode: ErrorCodeArtifactLayout,
			wantReas: DataRootReasonInvalidPath,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			root, err := ResolveDataRoot(testCase.input)
			if testCase.wantCode == "" {
				if err != nil {
					t.Fatalf("期望成功，实际错误：%s", err)
				}
				if root != testCase.wantRoot {
					t.Fatalf("期望 %q，实际 %q", testCase.wantRoot, root)
				}
				return
			}

			if root != "" {
				t.Fatalf("期望失败，实际返回 %q", root)
			}
			requireCode(t, err, testCase.wantCode)
			if err.Reason != testCase.wantReas {
				t.Fatalf("期望原因 %q，实际 %q", testCase.wantReas, err.Reason)
			}
			if err.Category != ErrorCategorySystem {
				t.Fatalf("数据根错误应归为 system，实际 %q", err.Category)
			}
		})
	}
}

// TestCanShareRequiresImmutableAndVerified 校验共享判定的双条件与反例。
func TestCanShareRequiresImmutableAndVerified(t *testing.T) {
	verifiedLibrary := Artifact{
		ID:           "lib-1",
		Kind:         KindLibrary,
		Immutable:    true,
		VerifiedAtMs: 1,
		Hashes:       Hashes{SHA1: strings.Repeat("a", 40)},
	}
	if !CanShare(verifiedLibrary) {
		t.Fatal("immutable + verified + 有哈希的 library 应可共享")
	}

	rejects := []struct {
		name     string
		artifact Artifact
	}{
		{
			name:     "未校验不得共享",
			artifact: Artifact{ID: "lib-2", Kind: KindLibrary, Immutable: true, Hashes: Hashes{SHA1: strings.Repeat("a", 40)}},
		},
		{
			name:     "可变内容不得共享",
			artifact: Artifact{ID: "mod-1", Kind: KindMod, Immutable: true, VerifiedAtMs: 1, Hashes: Hashes{SHA1: strings.Repeat("a", 40)}},
		},
		{
			name:     "无哈希不得共享",
			artifact: Artifact{ID: "lib-3", Kind: KindLibrary, Immutable: true, VerifiedAtMs: 1},
		},
		{
			name:     "非共享类别（plugin）不得共享",
			artifact: Artifact{ID: "plugin-1", Kind: KindPlugin, Immutable: true, VerifiedAtMs: 1, Hashes: Hashes{SHA1: strings.Repeat("a", 40)}},
		},
		{
			name:     "客户端属版本私有",
			artifact: Artifact{ID: "client-1", Kind: KindClient, Immutable: true, VerifiedAtMs: 1, Hashes: Hashes{SHA1: strings.Repeat("a", 40)}},
		},
	}

	for _, testCase := range rejects {
		t.Run(testCase.name, func(t *testing.T) {
			if CanShare(testCase.artifact) {
				t.Fatalf("制品 %q 不应可共享", testCase.artifact.ID)
			}
			if !RequiresPrivateTarget(testCase.artifact) {
				t.Fatalf("制品 %q 应要求版本私有目标", testCase.artifact.ID)
			}
		})
	}
}

// TestTrustLevelAndVerifiable 校验可校验性与信任等级。
func TestTrustLevelAndVerifiable(t *testing.T) {
	noHash := Artifact{ID: "a", Kind: KindLibrary}
	if IsVerifiable(noHash) {
		t.Fatal("无哈希制品不应可校验")
	}
	if TrustLevelOf(noHash) != TrustLevelLow {
		t.Fatalf("无哈希制品应为低信任，实际 %q", TrustLevelOf(noHash))
	}

	sha1Only := Artifact{ID: "b", Kind: KindAsset, Hashes: Hashes{SHA1: strings.Repeat("b", 40)}}
	if !IsVerifiable(sha1Only) {
		t.Fatal("只有 SHA-1 的资产应可校验")
	}
	if TrustLevelOf(sha1Only) != TrustLevelUnverified {
		t.Fatalf("未校验制品应为 unverified，实际 %q", TrustLevelOf(sha1Only))
	}

	verified := Artifact{ID: "c", Kind: KindAsset, Hashes: Hashes{SHA256: strings.Repeat("c", 64)}, VerifiedAtMs: 42}
	if TrustLevelOf(verified) != TrustLevelVerified {
		t.Fatalf("已校验制品应为 verified，实际 %q", TrustLevelOf(verified))
	}
}

// TestDefaultImmutableByKind 校验默认不可变性：可变内容默认 false。
func TestDefaultImmutableByKind(t *testing.T) {
	mutable := []ArtifactKind{KindMod, KindResourcePack, KindShader, KindPlugin, "unknown_kind"}
	for _, kind := range mutable {
		if DefaultImmutable(kind) {
			t.Fatalf("kind=%q 应默认可变", kind)
		}
	}

	fixed := []ArtifactKind{KindClient, KindLibrary, KindAsset, KindNative, KindRuntime}
	for _, kind := range fixed {
		if !DefaultImmutable(kind) {
			t.Fatalf("kind=%q 应默认不可变", kind)
		}
	}
}

// TestIsP3TargetKind 校验 P3 目标解析能力边界。
func TestIsP3TargetKind(t *testing.T) {
	for _, kind := range []ArtifactKind{KindClient, KindLibrary, KindAsset, KindNative, KindRuntime} {
		if !IsP3TargetKind(kind) {
			t.Fatalf("kind=%q 应属于 P3 目标类型", kind)
		}
	}
	for _, kind := range []ArtifactKind{KindMod, KindResourcePack, KindShader, KindPlugin, ""} {
		if IsP3TargetKind(kind) {
			t.Fatalf("kind=%q 不应属于 P3 目标类型", kind)
		}
	}
}

// TestSortArtifactsDeterministic 校验排序与输入顺序无关且可复现。
func TestSortArtifactsDeterministic(t *testing.T) {
	build := func() []Artifact {
		return []Artifact{
			{ID: "3", Kind: KindAsset, LogicalKey: "objects/b", Version: "1"},
			{ID: "1", Kind: KindLibrary, LogicalKey: "com/a", Version: "2"},
			{ID: "2", Kind: KindLibrary, LogicalKey: "com/a", Version: "1"},
			{ID: "4", Kind: KindClient, LogicalKey: "client", Version: ""},
			{ID: "5", Kind: KindRuntime, LogicalKey: "jre-legacy", Version: "8"},
			{ID: "6", Kind: KindNative, LogicalKey: "natives/a", Version: ""},
		}
	}

	wantIDs := []string{"4", "2", "1", "3", "6", "5"}

	first := build()
	SortArtifacts(first)
	for index, id := range wantIDs {
		if first[index].ID != id {
			t.Fatalf("排序结果第 %d 位期望 %q，实际 %q", index, id, first[index].ID)
		}
	}

	// 打乱输入顺序后结果必须完全一致（确定性，不依赖 map 迭代顺序）。
	second := build()
	for left, right := 0, len(second)-1; left < right; left, right = left+1, right-1 {
		second[left], second[right] = second[right], second[left]
	}
	SortArtifacts(second)
	for index := range first {
		if first[index].ID != second[index].ID {
			t.Fatalf("两次排序结果不一致：第 %d 位 %q vs %q", index, first[index].ID, second[index].ID)
		}
	}
}

// TestCountReferencesSorted 校验引用计数统计与稳定排序。
func TestCountReferencesSorted(t *testing.T) {
	references := []ArtifactReference{
		{ArtifactID: "b"},
		{ArtifactID: "a"},
		{ArtifactID: "b"},
		{ArtifactID: ""},
		{ArtifactID: "c"},
		{ArtifactID: "b"},
	}

	counts := CountReferences(references)
	if len(counts) != 3 {
		t.Fatalf("期望 3 个不同制品，实际 %d", len(counts))
	}

	wantOrder := []string{"a", "b", "c"}
	wantCount := []int{1, 3, 1}
	for index := range counts {
		if counts[index].ArtifactID != wantOrder[index] {
			t.Fatalf("第 %d 位期望 %q，实际 %q", index, wantOrder[index], counts[index].ArtifactID)
		}
		if counts[index].Count != wantCount[index] {
			t.Fatalf("制品 %q 期望计数 %d，实际 %d", wantOrder[index], wantCount[index], counts[index].Count)
		}
	}
}

// TestSortSourcesDeterministic 校验来源排序：优先级升序，同级按 ID。
func TestSortSourcesDeterministic(t *testing.T) {
	sources := []ArtifactSource{
		{ID: "mirror-b", Priority: 1},
		{ID: "official", Priority: 0},
		{ID: "mirror-a", Priority: 1},
	}
	SortSources(sources)

	want := []string{"official", "mirror-a", "mirror-b"}
	for index, id := range want {
		if sources[index].ID != id {
			t.Fatalf("第 %d 位期望 %q，实际 %q", index, id, sources[index].ID)
		}
	}
}

// TestErrorIsMatchesByCode 校验 errors.Is 可按错误码比较。
func TestErrorIsMatchesByCode(t *testing.T) {
	_, err := ResolveTarget(KindLibrary, "../evil")
	if err == nil {
		t.Fatal("期望返回错误")
	}
	target := &Error{Code: ErrorCodeArtifactPath}
	if !errors.Is(err, target) {
		t.Fatal("errors.Is 应按错误码匹配")
	}
	if errors.Is(err, &Error{Code: ErrorCodeArtifactLayout}) {
		t.Fatal("不同错误码不应匹配")
	}
}

func TestResolveInsideRootRejectsMissingLeafBelowEscapingSymlink(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := ResolveInsideRoot(root, "escape/missing/file"); err == nil {
		t.Fatal("accepted escaping ancestor")
	}
}

func TestResolveInsideRootAllowsMissingLeafBelowInternalSymlink(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "inside")
	if err := os.Mkdir(inside, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(inside, filepath.Join(root, "link")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	resolved, err := ResolveInsideRoot(root, "link/missing/file")
	if err != nil || resolved != filepath.Join(inside, "missing", "file") {
		t.Fatalf("internal link: %q %v", resolved, err)
	}
}
