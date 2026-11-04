package instance

import (
	"testing"

	"example.com/ninevoxel/launcher/internal/launch"
)

// 本文件覆盖「实例 Java 配置的保存与回读」。
//
// 三个模式字段（内存 / JVM / Java）都有同一类历史问题：写进去的值在
// 另一端被当成「未设置」而清空。这里按真实调用链验证一次完整往返：
// CreateInstance → UpdateInstance → 重新读取 → ResolveLaunchConfig。

// TestJavaModeRoundTripsThroughUpdate 验证实例专属 Java 保存后能原样读回。
//
// 用户报告的现象是「选择实例专属 Java，保存再打开还是跟随全局」——
// 根因是界面从未提交该模式，但后端往返本身也必须被测试固定住，
// 否则同样的症状会以另一种方式复现。
func TestJavaModeRoundTripsThroughUpdate(t *testing.T) {
	s := testService(t)
	redirectLaunchDataRoot(t, s)

	item, err := s.CreateInstance(testConfig("Java 往返"))
	if err != nil {
		t.Fatal(err)
	}

	managed := item.Config
	managed.JavaMode = launch.JavaManaged
	managed.CustomJavaRuntimeID = "runtime-17"
	managed.CustomJavaPath = ""
	if _, err := s.UpdateInstance(UpdateRequest{InstanceID: item.ID, Config: managed}); err != nil {
		t.Fatalf("保存失败：%v", err)
	}

	// 通过列表接口读回：这是界面打开详情时走的路径。
	list, err := s.ListInstances()
	if err != nil {
		t.Fatal(err)
	}
	var reloaded Instance
	for _, candidate := range list.Items {
		if candidate.ID == item.ID {
			reloaded = candidate
		}
	}
	if reloaded.ID == "" {
		t.Fatal("实例未出现在列表中")
	}
	if reloaded.JavaMode != launch.JavaManaged {
		t.Fatalf("Java 模式 = %q，期望 managed", reloaded.JavaMode)
	}
	if reloaded.CustomJavaRuntimeID != "runtime-17" {
		t.Fatalf("运行时标识 = %q", reloaded.CustomJavaRuntimeID)
	}
}

// TestCustomJavaPathRoundTrips 验证指定路径模式同样能往返。
func TestCustomJavaPathRoundTrips(t *testing.T) {
	s := testService(t)
	redirectLaunchDataRoot(t, s)

	item, err := s.CreateInstance(testConfig("Java 路径往返"))
	if err != nil {
		t.Fatal(err)
	}

	custom := item.Config
	custom.JavaMode = launch.JavaCustom
	custom.CustomJavaPath = "/opt/java/bin/java"
	custom.CustomJavaRuntimeID = ""
	if _, err := s.UpdateInstance(UpdateRequest{InstanceID: item.ID, Config: custom}); err != nil {
		t.Fatalf("保存失败：%v", err)
	}

	config, err := ResolveLaunchConfig(item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if config.JavaMode != launch.JavaCustom || config.CustomJavaPath != "/opt/java/bin/java" {
		t.Fatalf("启动配置 = %#v", config)
	}
}

// TestFollowGlobalClearsJavaFields 验证切回跟随全局时清空专属字段。
//
// 不清空会让下次切换模式时冒出一个用户早已放弃的旧路径，
// 也让「跟随全局」名不副实。
func TestFollowGlobalClearsJavaFields(t *testing.T) {
	s := testService(t)
	redirectLaunchDataRoot(t, s)

	item, err := s.CreateInstance(testConfig("清除 Java 字段"))
	if err != nil {
		t.Fatal(err)
	}

	managed := item.Config
	managed.JavaMode = launch.JavaManaged
	managed.CustomJavaRuntimeID = "runtime-17"
	if _, err := s.UpdateInstance(UpdateRequest{InstanceID: item.ID, Config: managed}); err != nil {
		t.Fatal(err)
	}

	back := managed
	back.JavaMode = launch.JavaFollowGlobal
	updated, err := s.UpdateInstance(UpdateRequest{InstanceID: item.ID, Config: back})
	if err != nil {
		t.Fatalf("保存失败：%v", err)
	}
	if updated.CustomJavaRuntimeID != "" || updated.CustomJavaPath != "" {
		t.Fatalf("跟随全局应清空专属字段，实际 %#v", updated.Config)
	}
}

// TestMemoryFollowGlobalStoresZero 验证跟随全局时内存值归零。
//
// 归零是后端的约定：0 表示「用全局值」。若保留一个陈旧数字，
// 界面显示与实际分配就会不一致。
func TestMemoryFollowGlobalStoresZero(t *testing.T) {
	s := testService(t)
	redirectLaunchDataRoot(t, s)

	item, err := s.CreateInstance(testConfig("内存跟随全局"))
	if err != nil {
		t.Fatal(err)
	}

	config := item.Config
	config.MemoryMode = launch.MemoryFollowGlobal
	config.MemoryMB = 8192
	updated, err := s.UpdateInstance(UpdateRequest{InstanceID: item.ID, Config: config})
	if err != nil {
		t.Fatalf("保存失败：%v", err)
	}
	if updated.MemoryMB != 0 {
		t.Fatalf("跟随全局时内存应归零，实际 %d", updated.MemoryMB)
	}
	if updated.MemoryMode != launch.MemoryFollowGlobal {
		t.Fatalf("内存模式 = %q", updated.MemoryMode)
	}
}
