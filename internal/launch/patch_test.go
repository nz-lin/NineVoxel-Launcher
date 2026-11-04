package launch

import (
	"os"
	"path/filepath"
	"testing"
)

// 本文件验证实例级版本声明覆盖（patches/<versionId>.json）的语义。
//
// 约束：补丁只改变**声明解析**，不改变文件定位根。jar、libraries、assets
// 仍然从共享核心读取。

// writePatch 写入实例补丁文件。
func writePatch(t *testing.T, patchDir, versionID, content string) {
	t.Helper()
	if err := os.MkdirAll(patchDir, 0o755); err != nil {
		t.Fatalf("创建补丁目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(patchDir, versionID+".json"), []byte(content), 0o600); err != nil {
		t.Fatalf("写入补丁失败: %v", err)
	}
}

// TestPatchOverridesMainClass 验证补丁覆盖主类。
func TestPatchOverridesMainClass(t *testing.T) {
	root := t.TempDir()
	patchDir := t.TempDir()
	id := "1.21.4"
	writeVersionJSON(t, root, id, `{"id":"1.21.4","mainClass":"net.minecraft.client.main.Main"}`)
	writePatch(t, patchDir, id, `{"id":"1.21.4","mainClass":"example.CustomMain"}`)

	outcome := loadMergedVersionWithPatches(root, patchDir, id)
	if outcome.merged == nil {
		t.Fatalf("补丁合并失败: %s", outcome.reason)
	}
	if outcome.merged.MainClass != "example.CustomMain" {
		t.Fatalf("主类 = %q，期望补丁覆盖后的值", outcome.merged.MainClass)
	}

	// 无补丁时保持核心声明。
	baseline := loadMergedVersionWithPatches(root, "", id)
	if baseline.merged == nil || baseline.merged.MainClass != "net.minecraft.client.main.Main" {
		t.Fatalf("无补丁时应使用核心声明，实际 %+v", baseline.merged)
	}
}

// TestPatchAddsLibraries 验证补丁追加依赖库。
func TestPatchAddsLibraries(t *testing.T) {
	root := t.TempDir()
	patchDir := t.TempDir()
	id := "1.21.4"
	writeVersionJSON(t, root, id, `{"id":"1.21.4","mainClass":"Main","libraries":[{"name":"org.example:base:1.0"}]}`)
	writePatch(t, patchDir, id, `{"id":"1.21.4","libraries":[{"name":"org.example:extra:2.0"}]}`)

	outcome := loadMergedVersionWithPatches(root, patchDir, id)
	if outcome.merged == nil {
		t.Fatalf("补丁合并失败: %s", outcome.reason)
	}
	names := make(map[string]bool, len(outcome.merged.Libraries))
	for _, library := range outcome.merged.Libraries {
		names[library.Name] = true
	}
	if !names["org.example:base:1.0"] || !names["org.example:extra:2.0"] {
		t.Fatalf("补丁应追加库而不是替换，实际 %v", names)
	}
}

// TestPatchParticipatesInInheritance 验证补丁版本仍能继承核心里的父版本。
func TestPatchParticipatesInInheritance(t *testing.T) {
	root := t.TempDir()
	patchDir := t.TempDir()
	writeVersionJSON(t, root, "1.21.4", `{"id":"1.21.4","mainClass":"ParentMain","assets":"17"}`)
	// 加载器版本本身只在实例补丁里存在：模拟「手动覆盖一个未安装的版本」。
	writePatch(t, patchDir, "fabric-loader-1.0-1.21.4",
		`{"id":"fabric-loader-1.0-1.21.4","inheritsFrom":"1.21.4","mainClass":"KnotClient"}`)

	outcome := loadMergedVersionWithPatches(root, patchDir, "fabric-loader-1.0-1.21.4")
	if outcome.merged == nil {
		t.Fatalf("补丁合并失败: %s", outcome.reason)
	}
	if outcome.merged.MainClass != "KnotClient" {
		t.Fatalf("主类 = %q", outcome.merged.MainClass)
	}
	if outcome.merged.Assets != "17" {
		t.Fatalf("父版本声明未合并，assets = %q", outcome.merged.Assets)
	}
}

// TestPatchCycleIsReported 验证补丁自引用被判为损坏。
func TestPatchCycleIsReported(t *testing.T) {
	root := t.TempDir()
	patchDir := t.TempDir()
	id := "looping"
	writePatch(t, patchDir, id, `{"id":"looping","inheritsFrom":"looping","mainClass":"Main"}`)

	outcome := loadMergedVersionWithPatches(root, patchDir, id)
	if outcome.merged != nil {
		t.Fatal("自引用补丁不应合并成功")
	}
	if outcome.status != versionStatusBroken {
		t.Fatalf("状态 = %q，期望 broken", outcome.status)
	}
}

// TestBrokenPatchDoesNotFallBack 验证损坏的补丁不会被静默忽略。
//
// 静默回退会让用户以为修改生效，实际启动用的还是核心声明——这种「看不见的
// 忽略」比直接报错难排查得多。
func TestBrokenPatchDoesNotFallBack(t *testing.T) {
	root := t.TempDir()
	patchDir := t.TempDir()
	id := "1.21.4"
	writeVersionJSON(t, root, id, `{"id":"1.21.4","mainClass":"Main"}`)
	writePatch(t, patchDir, id, `{not json`)

	outcome := loadMergedVersionWithPatches(root, patchDir, id)
	if outcome.merged != nil {
		t.Fatal("损坏的补丁不应回退到核心声明")
	}
}

// TestValidateVersionPatchDocument 验证保存前的补丁校验。
func TestValidateVersionPatchDocument(t *testing.T) {
	root := t.TempDir()
	id := "1.21.4"
	writeVersionJSON(t, root, id, `{"id":"1.21.4","mainClass":"Main"}`)

	valid := []byte(`{"id":"1.21.4","mainClass":"Custom"}`)
	if err := ValidateVersionPatchDocument(root, id, valid); err != nil {
		t.Fatalf("合法补丁被拒绝: %v", err)
	}

	cases := []struct {
		name    string
		content string
	}{
		{name: "非对象", content: `[]`},
		{name: "非法 JSON", content: `{`},
		{name: "id 不一致", content: `{"id":"1.20.1"}`},
		{name: "自引用", content: `{"id":"1.21.4","inheritsFrom":"1.21.4"}`},
	}
	for _, testCase := range cases {
		if err := ValidateVersionPatchDocument(root, id, []byte(testCase.content)); err == nil {
			t.Fatalf("%s：应拒绝该补丁", testCase.name)
		}
	}

	// 核心声明本身缺主类时，补丁也没补上：应拒绝而不是给出一个不可启动的实例。
	brokenRoot := t.TempDir()
	writeVersionJSON(t, brokenRoot, "no-main", `{"id":"no-main"}`)
	if err := ValidateVersionPatchDocument(brokenRoot, "no-main", []byte(`{"id":"no-main"}`)); err == nil {
		t.Fatal("补丁未提供主类时应拒绝")
	}
}

// TestReadVersionPatchFile 验证补丁文件读取的边界。
func TestReadVersionPatchFile(t *testing.T) {
	patchDir := t.TempDir()
	if _, exists, err := ReadVersionPatchFile(patchDir, "1.21.4"); err != nil || exists {
		t.Fatalf("不存在的补丁应返回 exists=false: %v %v", exists, err)
	}
	writePatch(t, patchDir, "1.21.4", `{"id":"1.21.4"}`)
	content, exists, err := ReadVersionPatchFile(patchDir, "1.21.4")
	if err != nil || !exists || len(content) == 0 {
		t.Fatalf("补丁读取失败: exists=%v err=%v", exists, err)
	}
}
