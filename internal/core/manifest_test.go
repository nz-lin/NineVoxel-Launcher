package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeCoreFixture 在核心根下写入一个测试文件并返回其相对路径。
func writeCoreFixture(t *testing.T, root, rel, content string) string {
	t.Helper()
	absolute := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(absolute), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(absolute, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return rel
}

// ===== 清单结构校验 =====

// TestManifestValidateRejectsUnsafePath 验证清单路径越界被拒绝。
//
// 清单可能被人工编辑或磁盘损坏。若据其删除/覆盖文件而不校验路径，
// 就可能误伤核心根之外的文件。
func TestManifestValidateRejectsUnsafePath(t *testing.T) {
	cases := map[string]string{
		"父目录逃逸": "../app.db",
		"深层父目录": "libraries/../../app.db",
		"绝对路径":  "/etc/passwd",
		"空路径":   "",
		"反斜杠逃逸": `libraries\..\..\app.db`,
	}

	for name, path := range cases {
		t.Run(name, func(t *testing.T) {
			manifest := Manifest{
				SchemaVersion: manifestSchemaVersion,
				Identity:      "forge:26.3:66.1.0",
				VersionID:     "26.3-forge-66.1.0",
				Files: []ManifestFile{{
					Path:   path,
					Size:   1,
					SHA256: strings.Repeat("a", 64),
					Group:  GroupLibrary,
				}},
			}
			if err := manifest.Validate(); err == nil {
				t.Fatalf("路径 %q 期望被拒绝", path)
			}
		})
	}
}

// TestManifestValidateRejectsBadHash 验证哈希格式非法被拒绝。
func TestManifestValidateRejectsBadHash(t *testing.T) {
	for _, hash := range []string{"", "abc", strings.Repeat("z", 64)} {
		manifest := Manifest{
			SchemaVersion: manifestSchemaVersion,
			Identity:      "vanilla:26.3",
			VersionID:     "26.3",
			Files: []ManifestFile{{
				Path:   "libraries/a.jar",
				Size:   1,
				SHA256: hash,
				Group:  GroupLibrary,
			}},
		}
		if err := manifest.Validate(); err == nil {
			t.Fatalf("哈希 %q 期望被拒绝", hash)
		}
	}
}

// TestManifestValidateRejectsUnsupportedSchema 验证不支持的清单版本被拒绝。
func TestManifestValidateRejectsUnsupportedSchema(t *testing.T) {
	manifest := Manifest{SchemaVersion: manifestSchemaVersion + 1, Identity: "vanilla:26.3", VersionID: "26.3"}
	if err := manifest.Validate(); err == nil {
		t.Fatal("不支持的清单版本期望被拒绝")
	}
}

// TestManifestRequiredFilesIncludesAssets 验证资源索引与对象计入必需文件。
//
// 只校验 libraries 会漏掉被删除的资源对象，导致「安装器认为已装、游戏起不来」。
func TestManifestRequiredFilesIncludesAssets(t *testing.T) {
	manifest := Manifest{
		Files: []ManifestFile{{Path: "libraries/a.jar"}},
		Assets: &AssetManifest{
			IndexID: "34",
			Index:   ManifestFile{Path: "assets/indexes/34.json"},
			Objects: []ManifestFile{{Path: "assets/objects/ab/abcdef"}},
		},
	}
	files := manifest.RequiredFiles()
	if len(files) != 3 {
		t.Fatalf("必需文件期望 3 个，实际 %d", len(files))
	}
}

// TestManifestFilesOfGroup 验证按分组筛选。
func TestManifestFilesOfGroup(t *testing.T) {
	manifest := Manifest{Files: []ManifestFile{
		{Path: "libraries/a.jar", Group: GroupLibrary},
		{Path: "versions/v/natives/x.dll", Group: GroupNative},
		{Path: "libraries/net/minecraftforge/forge/client.jar", Group: GroupProcessor},
	}}
	if got := len(manifest.FilesOfGroup(GroupNative)); got != 1 {
		t.Fatalf("native 分组期望 1 个文件，实际 %d", got)
	}
	if got := len(manifest.FilesOfGroup(GroupProcessor)); got != 1 {
		t.Fatalf("processor 分组期望 1 个文件，实际 %d", got)
	}
}

// ===== 两级校验 =====

// TestVerifyFastDetectsMissingAndSizeMismatch 验证 L1 能发现缺失与大小不符。
func TestVerifyFastDetectsMissingAndSizeMismatch(t *testing.T) {
	root := t.TempDir()
	okRel := writeCoreFixture(t, root, "libraries/ok.jar", "ok")
	badRel := writeCoreFixture(t, root, "libraries/bad.jar", "changed")
	missingRel := "libraries/missing.jar"

	okFile, err := buildManifestFile(root, okRel, GroupLibrary)
	if err != nil {
		t.Fatalf("构造清单条目失败: %v", err)
	}
	badFile, err := buildManifestFile(root, badRel, GroupLibrary)
	if err != nil {
		t.Fatalf("构造清单条目失败: %v", err)
	}
	// 记录与实际不同的期望大小，模拟文件被替换。
	badFile.Size = 9999

	manifest := Manifest{
		SchemaVersion: manifestSchemaVersion,
		Identity:      "vanilla:26.3",
		VersionID:     "26.3",
		Files: []ManifestFile{
			okFile,
			badFile,
			{Path: missingRel, Size: 1, SHA256: strings.Repeat("a", 64), Group: GroupLibrary},
		},
	}
	if err := manifest.Validate(); err != nil {
		t.Fatalf("清单应合法: %v", err)
	}

	report, verifyErr := verifyManifest(root, manifest, VerifyFast)
	if verifyErr != nil {
		t.Fatalf("校验失败: %v", verifyErr)
	}
	if len(report.Missing) != 1 {
		t.Fatalf("缺失文件期望 1 个，实际 %d（%v）", len(report.Missing), report.Missing)
	}
	if len(report.SizeMismatch) != 1 {
		t.Fatalf("大小不符期望 1 个，实际 %d（%v）", len(report.SizeMismatch), report.SizeMismatch)
	}
	if report.Complete() {
		t.Fatal("存在问题文件时不应判定为完整")
	}
}

// TestVerifyFastSkipsHashing 验证 L1 不读取文件内容。
//
// 构造一个大小一致但内容不同的文件：L1 应通过，L2 应发现哈希不符。
// 这证明「快速路径只 stat」与「完整校验比对内容」确实不同。
func TestVerifyFastSkipsHashing(t *testing.T) {
	root := t.TempDir()
	rel := writeCoreFixture(t, root, "libraries/tampered.jar", "AAAA")

	// 用内容 "BBBB" 计算清单，再把磁盘上的内容改成 "AAAA"（长度相同）。
	original := filepath.Join(root, "libraries", "source.jar")
	if err := os.WriteFile(original, []byte("BBBB"), 0o644); err != nil {
		t.Fatal(err)
	}
	expected, err := buildManifestFile(root, "libraries/source.jar", GroupLibrary)
	if err != nil {
		t.Fatalf("构造清单条目失败: %v", err)
	}
	expected.Path = rel

	manifest := Manifest{
		SchemaVersion: manifestSchemaVersion,
		Identity:      "vanilla:26.3",
		VersionID:     "26.3",
		Files:         []ManifestFile{expected},
	}

	fast, fastErr := verifyManifest(root, manifest, VerifyFast)
	if fastErr != nil {
		t.Fatalf("快速校验失败: %v", fastErr)
	}
	if !fast.Complete() {
		t.Fatalf("L1 只比对大小，应通过：%+v", fast)
	}

	full, fullErr := verifyManifest(root, manifest, VerifyFull)
	if fullErr != nil {
		t.Fatalf("完整校验失败: %v", fullErr)
	}
	if len(full.HashMismatch) != 1 {
		t.Fatalf("L2 期望发现 1 个哈希不符，实际 %+v", full)
	}
}

// TestBuildManifestFileRejectsMissing 验证清单条目只记录确实存在的文件。
func TestBuildManifestFileRejectsMissing(t *testing.T) {
	root := t.TempDir()
	if _, err := buildManifestFile(root, "libraries/none.jar", GroupLibrary); err == nil {
		t.Fatal("不存在的文件期望返回错误")
	}
}

// TestBuildManifestFileRejectsTraversal 验证路径越界被拒绝。
func TestBuildManifestFileRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	if _, err := buildManifestFile(root, "../outside.jar", GroupLibrary); err == nil {
		t.Fatal("越界路径期望返回错误")
	}
}
