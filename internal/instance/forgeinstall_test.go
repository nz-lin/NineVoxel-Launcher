package instance

import (
	"os"
	"path/filepath"
	"testing"
)

// 本文件覆盖加载器安装的两个易错点：
//   - processor 产出必须进入清单的 processor 组（否则重建会把它当残留删掉）
//   - 原版客户端 jar 缺失时必须在加载器安装前就报错

// TestMergeProcessorOutputs 验证显式记录与扫描结果合并后不重复。
func TestMergeProcessorOutputs(t *testing.T) {
	explicit := []string{"libraries/net/minecraftforge/forge/1.0/forge-1.0-client.jar"}
	scanned := []string{
		"libraries/net/minecraftforge/forge/1.0/forge-1.0-client.jar", // 与显式记录重叠
		"versions/1.0-forge/1.0-forge.jar",
	}
	merged := mergeProcessorOutputs(explicit, scanned)
	if len(merged) != 2 {
		t.Fatalf("合并结果 = %v，期望 2 项", merged)
	}
	if merged[0] != explicit[0] {
		t.Fatalf("显式记录应排在前面：%v", merged)
	}

	// 只有扫描结果时原样返回。
	if got := mergeProcessorOutputs(nil, scanned); len(got) != len(scanned) {
		t.Fatalf("仅扫描结果 = %v", got)
	}
	// 空路径必须被过滤，否则清单会写入空条目并在校验时报错。
	if got := mergeProcessorOutputs([]string{"", "a.jar"}, nil); len(got) != 1 || got[0] != "a.jar" {
		t.Fatalf("空路径未被过滤：%v", got)
	}
}

// TestBuildCoreManifestIncludesProcessorOutput 验证 processor 产出被单独分组。
//
// 分组决定修复策略：processor 产物没有独立下载源，损坏时只能重装。
// 若被误记为普通依赖库，修复流程会去下载一个不存在的地址。
func TestBuildCoreManifestIncludesProcessorOutput(t *testing.T) {
	root := t.TempDir()
	versionDir := filepath.Join(root, "versions", "1.0-forge")
	if err := os.MkdirAll(versionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile := func(relative, body string) {
		target := filepath.Join(root, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeFile("versions/1.0-forge/1.0-forge.json", `{"id":"1.0-forge","inheritsFrom":"1.0"}`)
	writeFile("libraries/lib.jar", "library")
	writeFile("libraries/forge-1.0-client.jar", "processor output")

	manifest, err := buildCoreManifest(root, manifestInput{
		VersionID:         "1.0-forge",
		Identity:          "forge:1.0:47.0.0",
		LoaderKind:        "forge",
		LoaderVersion:     "47.0.0",
		MCVersion:         "1.0",
		RecipeFingerprint: "fp",
		Downloaded:        []string{"libraries/lib.jar"},
		VersionJSON:       []string{"versions/1.0-forge/1.0-forge.json"},
		ProcessorOutputs:  []string{"libraries/forge-1.0-client.jar"},
	})
	if err != nil {
		t.Fatalf("构建清单失败：%v", err)
	}

	groups := map[string]string{}
	for _, file := range manifest.Files {
		groups[file.Path] = file.Group
	}
	if groups["libraries/forge-1.0-client.jar"] != "processor" {
		t.Fatalf("客户端 jar 分组 = %q，期望 processor", groups["libraries/forge-1.0-client.jar"])
	}
	if groups["libraries/lib.jar"] != "library" {
		t.Fatalf("普通依赖分组 = %q", groups["libraries/lib.jar"])
	}
	if groups["versions/1.0-forge/1.0-forge.json"] != "versionJson" {
		t.Fatalf("版本描述分组 = %q", groups["versions/1.0-forge/1.0-forge.json"])
	}
}

// TestVanillaClientJarPath 验证缺少原版 jar 时给出可操作错误。
func TestVanillaClientJarPath(t *testing.T) {
	root := t.TempDir()
	if _, err := vanillaClientJarPath(root, "1.20.1"); err == nil {
		t.Fatal("缺失原版 jar 时应报错")
	}

	target := filepath.Join(root, "versions", "1.20.1", "1.20.1.jar")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("jar"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := vanillaClientJarPath(root, "1.20.1")
	if err != nil {
		t.Fatalf("存在时应成功：%v", err)
	}
	if got != target {
		t.Fatalf("路径 = %q，期望 %q", got, target)
	}

	// 空文件视为缺失：processor 拿到空输入只会以难懂的错误失败。
	if err := os.WriteFile(target, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := vanillaClientJarPath(root, "1.20.1"); err == nil {
		t.Fatal("空文件应视为缺失")
	}
}
