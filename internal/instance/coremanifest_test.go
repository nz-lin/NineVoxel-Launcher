package instance

import (
	"os"
	"path/filepath"
	"testing"

	"example.com/ninevoxel/launcher/internal/core"
	"example.com/ninevoxel/launcher/internal/loader"
)

// writeVersionJSONAt 在核心中写入版本 JSON。
func writeVersionJSONAt(t *testing.T, root, versionID string, body string) string {
	t.Helper()
	rel := "versions/" + versionID + "/" + versionID + ".json"
	fixtureWrite(t, filepath.Join(root, filepath.FromSlash(rel)), []byte(body))
	return rel
}

// ===== 配方指纹 =====

// TestRecipeFingerprintStablePerLoader 验证同参数得到同指纹。
func TestRecipeFingerprintStablePerLoader(t *testing.T) {
	s := testService(t)
	first := s.recipeFingerprint(loader.KindForge, "26.3", "66.1.0")
	second := s.recipeFingerprint(loader.KindForge, "26.3", "66.1.0")
	if first == "" || first != second {
		t.Fatalf("同参数指纹不一致：%q vs %q", first, second)
	}
}

// TestRecipeFingerprintDiffersByLoader 验证不同加载器产生不同指纹。
//
// 同一 MC 版本下 Forge 与 Fabric 的产物完全不同，指纹必须能区分。
func TestRecipeFingerprintDiffersByLoader(t *testing.T) {
	s := testService(t)
	forge := s.recipeFingerprint(loader.KindForge, "26.3", "66.1.0")
	fabric := s.recipeFingerprint(loader.KindFabric, "26.3", "66.1.0")
	if forge == fabric {
		t.Fatal("不同加载器应产生不同指纹")
	}
}

// ===== 清单写入与读取 =====

// TestWriteAndReadManifest 验证清单写入后可正确读回并通过校验。
func TestWriteAndReadManifest(t *testing.T) {
	s := testService(t)
	jsonRel := writeVersionJSONAt(t, s.root, "26.3", `{"id":"26.3"}`)
	libRel := writeLibrary(t, s.root, "com/example/lib/1.0/lib-1.0.jar", "library bytes")

	artifacts := installArtifacts{Downloaded: []string{libRel}}
	if err := s.writeCoreManifest(loader.KindVanilla, "26.3", "", "26.3", artifacts); err != nil {
		t.Fatalf("写入清单失败: %v", err)
	}
	_ = jsonRel

	manifest, found, err := core.ReadManifest(s.root, "26.3")
	if err != nil || !found {
		t.Fatalf("读取清单失败: %v %v", found, err)
	}
	if manifest.Identity != "vanilla:26.3" {
		t.Fatalf("身份 = %q", manifest.Identity)
	}
	if len(manifest.Files) != 2 {
		t.Fatalf("文件条目期望 2（版本 JSON + 库），实际 %d", len(manifest.Files))
	}

	report, verifyErr := core.VerifyManifest(s.root, manifest, core.VerifyFull)
	if verifyErr != nil {
		t.Fatalf("校验失败: %v", verifyErr)
	}
	if !report.Complete() {
		t.Fatalf("刚写完的清单应完整：%+v", report)
	}
}

// TestWriteCoreManifestFailsWithoutVersionJSON 验证缺版本 JSON 时拒绝写清单。
//
// 写不出清单就等于「安装未完成」，下次启动会重装；若放行一份不完整清单，
// 下次启动会跳过安装并在合并阶段才失败。
func TestWriteCoreManifestFailsWithoutVersionJSON(t *testing.T) {
	s := testService(t)
	err := s.writeCoreManifest(loader.KindVanilla, "26.3", "", "26.3", installArtifacts{})
	if err == nil {
		t.Fatal("缺版本 JSON 时应拒绝写清单")
	}
}

// TestWriteCoreManifestIncludesInheritChain 验证清单记录整条继承链。
//
// 只记录子版本时，父版本 JSON 被删除后快速校验发现不了，
// 启动会在合并阶段才失败。
func TestWriteCoreManifestIncludesInheritChain(t *testing.T) {
	s := testService(t)
	writeVersionJSONAt(t, s.root, "26.3", `{"id":"26.3"}`)
	writeVersionJSONAt(t, s.root, "26.3-forge-66.1.0", `{"id":"26.3-forge-66.1.0","inheritsFrom":"26.3"}`)

	if err := s.writeCoreManifest(loader.KindForge, "26.3", "66.1.0", "26.3-forge-66.1.0", installArtifacts{}); err != nil {
		t.Fatalf("写入清单失败: %v", err)
	}
	manifest, found, err := core.ReadManifest(s.root, "26.3-forge-66.1.0")
	if err != nil || !found {
		t.Fatalf("读取清单失败: %v %v", found, err)
	}
	versionJSONs := manifest.FilesOfGroup(core.GroupVersionJSON)
	if len(versionJSONs) != 2 {
		t.Fatalf("继承链期望 2 个版本 JSON，实际 %d（%+v）", len(versionJSONs), versionJSONs)
	}
}

// ===== 已安装判定 =====

// TestInstalledVersionIDReusesCompleteInstall 验证完整安装被识别。
func TestInstalledVersionIDReusesCompleteInstall(t *testing.T) {
	s := testService(t)
	writeVersionJSONAt(t, s.root, "26.3", `{"id":"26.3"}`)
	if err := s.writeCoreManifest(loader.KindVanilla, "26.3", "", "26.3", installArtifacts{}); err != nil {
		t.Fatal(err)
	}

	versionID, installed := s.installedVersionID(loader.KindVanilla, "26.3", "")
	if !installed || versionID != "26.3" {
		t.Fatalf("应识别为已安装，实际 %q %v", versionID, installed)
	}
}

// TestInstalledVersionIDRejectsMissingFile 验证文件缺失时不再视为已安装。
//
// 这是「清单存在但内容被破坏」的核心用例：只判断清单文件存在会让用户
// 看到「已安装」却启动失败。
func TestInstalledVersionIDRejectsMissingFile(t *testing.T) {
	s := testService(t)
	writeVersionJSONAt(t, s.root, "26.3", `{"id":"26.3"}`)
	libRel := writeLibrary(t, s.root, "com/example/lib/1.0/lib-1.0.jar", "library bytes")
	if err := s.writeCoreManifest(loader.KindVanilla, "26.3", "", "26.3", installArtifacts{Downloaded: []string{libRel}}); err != nil {
		t.Fatal(err)
	}

	// 删除清单记录的一个文件，模拟「装完后又丢文件」。
	if err := os.Remove(filepath.Join(s.root, filepath.FromSlash(libRel))); err != nil {
		t.Fatal(err)
	}

	if _, installed := s.installedVersionID(loader.KindVanilla, "26.3", ""); installed {
		t.Fatal("文件缺失时不应视为已安装")
	}
}

// TestInstalledVersionIDRejectsMissingManifest 验证没有清单时视为未安装。
func TestInstalledVersionIDRejectsMissingManifest(t *testing.T) {
	s := testService(t)
	writeVersionJSONAt(t, s.root, "26.3", `{"id":"26.3"}`)

	if _, installed := s.installedVersionID(loader.KindVanilla, "26.3", ""); installed {
		t.Fatal("没有清单时不应视为已安装")
	}
}

// TestInstalledVersionIDRejectsRecipeChange 验证配方变化后不再复用。
//
// 安装器或平台发生变化时，旧产物不能当作当前配方的结果。
func TestInstalledVersionIDRejectsRecipeChange(t *testing.T) {
	s := testService(t)
	writeVersionJSONAt(t, s.root, "26.3", `{"id":"26.3"}`)
	if err := s.writeCoreManifest(loader.KindVanilla, "26.3", "", "26.3", installArtifacts{}); err != nil {
		t.Fatal(err)
	}

	// 篡改清单的配方指纹，模拟「用另一套配方装过」。
	manifest, found, err := core.ReadManifest(s.root, "26.3")
	if err != nil || !found {
		t.Fatal(err)
	}
	manifest.RecipeFingerprint = "stale-recipe"
	if err := core.WriteManifest(s.root, manifest); err != nil {
		t.Fatal(err)
	}

	if _, installed := s.installedVersionID(loader.KindVanilla, "26.3", ""); installed {
		t.Fatal("配方指纹不一致时不应复用")
	}
}

// writeLibrary 在核心 libraries 下写入一个文件并返回相对路径。
func writeLibrary(t *testing.T, root, rel, content string) string {
	t.Helper()
	full := "libraries/" + rel
	fixtureWrite(t, filepath.Join(root, filepath.FromSlash(full)), []byte(content))
	return full
}
