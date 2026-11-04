package instance

import (
	"os"
	"path/filepath"
	"testing"

	"example.com/ninevoxel/launcher/internal/artifact"
)

func TestResolveLaunchConfigReadsManifestAndGameDirectory(t *testing.T) {
	restore := launchDataRoot
	root := t.TempDir()
	launchDataRoot = func() (string, *artifact.Error) { return root, nil }
	t.Cleanup(func() { launchDataRoot = restore })

	id := "inst-0123456789abcdef0123456789abcdef"
	directory := filepath.Join(root, "instances", id)
	gameDirectory := filepath.Join(directory, ".minecraft")
	// 版本资源位于共享核心，不在实例目录。
	versionDirectory := filepath.Join(root, "versions", "1.21.4")
	if err := os.MkdirAll(versionDirectory, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(versionDirectory, "1.21.4.json"), []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}
	config := Config{
		Name:                "测试实例",
		MCVersion:           "1.21.4",
		Loader:              "vanilla",
		MemoryMB:            6144,
		JVMArgs:             "-XX:+UseG1GC",
		CustomJavaRuntimeID: "runtime-test",
	}
	if _, err := validConfig(config); err != nil {
		t.Fatal(err)
	}
	if err := writeInstanceManifest(directory, instanceManifest{
		ID:                id,
		Config:            config,
		InstallationState: "installed",
		CreatedAtMS:       1,
	}); err != nil {
		t.Fatal(err)
	}

	resolved, err := ResolveLaunchConfig(id)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.GameDirectory != gameDirectory || resolved.MCVersion != "1.21.4" {
		t.Fatalf("解析结果 = %#v", resolved)
	}
	if !resolved.Installed || resolved.MemoryMB != 6144 || resolved.CustomJavaRuntimeID != "runtime-test" {
		t.Fatalf("实例启动配置 = %#v", resolved)
	}
}

func TestResolveLaunchConfigDoesNotUseSQLiteIndexForMissingFolder(t *testing.T) {
	restore := launchDataRoot
	root := t.TempDir()
	launchDataRoot = func() (string, *artifact.Error) { return root, nil }
	t.Cleanup(func() { launchDataRoot = restore })

	_, err := ResolveLaunchConfig("inst-0123456789abcdef0123456789abcdef")
	if err == nil {
		t.Fatal("实例目录不存在时必须拒绝启动")
	}
}
