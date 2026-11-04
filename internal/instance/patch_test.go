package instance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setupPatchFixture 准备一个「已安装」的实例与它的核心版本声明。
func setupPatchFixture(t *testing.T) (*InstanceService, string) {
	t.Helper()
	s := testService(t)
	item, err := s.CreateInstance(testConfig("版本覆盖"))
	if err != nil {
		t.Fatal(err)
	}
	// 核心声明：实例补丁要叠加在它之上。
	fixtureWrite(t, filepath.Join(s.root, "versions", item.MCVersion, item.MCVersion+".json"),
		[]byte(`{"id":"`+item.MCVersion+`","mainClass":"net.minecraft.client.main.Main","libraries":[{"name":"org.example:base:1.0"}]}`))
	updated := item
	updated.VersionID = item.MCVersion
	updated.InstallationState = "installed"
	if err := s.saveLocked(updated); err != nil {
		t.Fatal(err)
	}
	return s, item.ID
}

// TestVersionPatchLifecycle 验证补丁的保存、读取与删除。
func TestVersionPatchLifecycle(t *testing.T) {
	s, instanceID := setupPatchFixture(t)

	read, err := s.ReadVersionPatch(IDRequest{InstanceID: instanceID})
	if err != nil {
		t.Fatal(err)
	}
	if read.Exists || read.VersionID != "1.21.4" {
		t.Fatalf("初始补丁状态异常: %+v", read)
	}

	saved, err := s.SaveVersionPatch(SaveVersionPatchRequest{
		InstanceID: instanceID,
		Content:    `{"id":"1.21.4","mainClass":"example.CustomMain"}`,
	})
	if err != nil {
		t.Fatalf("保存补丁失败: %v", err)
	}
	if !saved.Exists {
		t.Fatal("保存后应标记为存在")
	}
	// 补丁必须落在实例目录，而不是共享核心。
	patchPath := filepath.Join(s.root, "instances", instanceID, "patches", "1.21.4.json")
	if _, statErr := os.Stat(patchPath); statErr != nil {
		t.Fatalf("补丁未写入实例目录: %v", statErr)
	}

	read, err = s.ReadVersionPatch(IDRequest{InstanceID: instanceID})
	if err != nil {
		t.Fatal(err)
	}
	if !read.Exists || !strings.Contains(read.Content, "example.CustomMain") {
		t.Fatalf("读取补丁结果异常: %+v", read)
	}

	deleted, err := s.DeleteVersionPatch(IDRequest{InstanceID: instanceID})
	if err != nil {
		t.Fatal(err)
	}
	if deleted.Exists {
		t.Fatal("删除后不应再标记为存在")
	}
	if _, statErr := os.Stat(patchPath); !os.IsNotExist(statErr) {
		t.Fatalf("补丁文件应已删除，实际 stat: %v", statErr)
	}
}

// TestSaveVersionPatchRejectsInvalidAndKeepsOld 验证非法补丁被拒绝且不破坏旧文件。
func TestSaveVersionPatchRejectsInvalidAndKeepsOld(t *testing.T) {
	s, instanceID := setupPatchFixture(t)
	if _, err := s.SaveVersionPatch(SaveVersionPatchRequest{
		InstanceID: instanceID,
		Content:    `{"id":"1.21.4","mainClass":"example.CustomMain"}`,
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := s.SaveVersionPatch(SaveVersionPatchRequest{
		InstanceID: instanceID,
		Content:    `{"id":"1.21.4","mainClass":`,
	}); err == nil {
		t.Fatal("非法 JSON 应被拒绝")
	}

	read, err := s.ReadVersionPatch(IDRequest{InstanceID: instanceID})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(read.Content, "example.CustomMain") {
		t.Fatalf("失败保存不应破坏旧补丁: %+v", read)
	}
}

// TestSaveVersionPatchRejectsWrongID 验证补丁 id 必须是实例当前版本。
func TestSaveVersionPatchRejectsWrongID(t *testing.T) {
	s, instanceID := setupPatchFixture(t)
	if _, err := s.SaveVersionPatch(SaveVersionPatchRequest{
		InstanceID: instanceID,
		Content:    `{"id":"1.20.1","mainClass":"example.CustomMain"}`,
	}); err == nil {
		t.Fatal("id 与实例版本不一致时应拒绝")
	}
}
