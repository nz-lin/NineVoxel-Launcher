package instance

import (
	"os"
	"path/filepath"
	"testing"

	"example.com/ninevoxel/launcher/internal/artifact"
	"example.com/ninevoxel/launcher/internal/launch"
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
		MemoryMode:          launch.MemoryCustom,
		MemoryMB:            6144,
		JVMMode:             launch.JVMCustom,
		JVMArgs:             "-XX:+UseG1GC",
		JavaMode:            launch.JavaManaged,
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
	// JavaMode 必须一并返回：只返回路径不返回模式时，启动侧无法区分
	// 「用户显式指定」与「未指定」，表现为实例专属 Java 保存后仍然跟随全局。
	if resolved.JavaMode != launch.JavaManaged {
		t.Fatalf("Java 模式 = %q，期望 %q", resolved.JavaMode, launch.JavaManaged)
	}
}

// TestResolveLaunchConfigNormalizesEmptyJavaMode 验证空模式归一化为跟随全局。
//
// 空字符串在 switch 里会落空，行为取决于零值——必须显式归一化，
// 否则「旧数据」与「前端部分提交」会走出不确定的启动路径。
func TestResolveLaunchConfigNormalizesEmptyJavaMode(t *testing.T) {
	restore := launchDataRoot
	root := t.TempDir()
	launchDataRoot = func() (string, *artifact.Error) { return root, nil }
	t.Cleanup(func() { launchDataRoot = restore })

	id := "inst-0123456789abcdef0123456789abcdef"
	directory := filepath.Join(root, "instances", id)
	versionDirectory := filepath.Join(root, "versions", "1.21.4")
	if err := os.MkdirAll(versionDirectory, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(versionDirectory, "1.21.4.json"), []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}
	// 直接写清单，绕过 validConfig 的归一化：模拟磁盘上的旧数据。
	if err := writeInstanceManifest(directory, instanceManifest{
		ID: id,
		Config: Config{
			Name:      "旧实例",
			MCVersion: "1.21.4",
			Loader:    "vanilla",
		},
		InstallationState: "installed",
		CreatedAtMS:       1,
	}); err != nil {
		t.Fatal(err)
	}

	resolved, err := ResolveLaunchConfig(id)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.JavaMode != launch.JavaFollowGlobal {
		t.Fatalf("空模式应归一化为跟随全局，实际 %q", resolved.JavaMode)
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
