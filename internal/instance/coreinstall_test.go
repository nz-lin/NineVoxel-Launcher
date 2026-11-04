package instance

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"example.com/ninevoxel/launcher/internal/loader"
)

// TestInstallSkipsWhenManifestComplete 验证清单完整时跳过实际安装。
//
// 这是「复制 5 个同版本实例只付一次安装代价」的核心保证：第二个实例安装时
// 应当直接复用核心产物，不下载、不跑安装器。
func TestInstallSkipsWhenManifestComplete(t *testing.T) {
	s := testService(t)
	item, err := s.CreateInstance(testConfig("跳过安装"))
	if err != nil {
		t.Fatal(err)
	}

	// 先构造一份完整的已安装状态：版本 JSON + 清单。
	writeVersionJSONAt(t, s.root, item.MCVersion, `{"id":"`+item.MCVersion+`"}`)
	if err := s.writeCoreManifest(loader.KindVanilla, item.MCVersion, "", item.MCVersion, installArtifacts{}); err != nil {
		t.Fatalf("写入清单失败: %v", err)
	}

	var installerCalls int32
	s.installer = func(context.Context, Instance) error {
		atomic.AddInt32(&installerCalls, 1)
		return nil
	}

	// 直接调用 install：注入的 installer 会整体替换实现，走不到快速路径。
	// 清单完整时这里必须在触碰版本元数据之前就返回。
	if err := s.install(context.Background(), item); err != nil {
		t.Fatalf("已完整安装时应直接返回，实际错误: %v", err)
	}
	if atomic.LoadInt32(&installerCalls) != 0 {
		t.Fatalf("清单完整时不应再次执行安装，实际调用 %d 次", installerCalls)
	}

	// 快速路径应把解析出的版本 ID 记到实例上；安装状态由安装线程写入，
	// 直接调用 install 不经过那条路径，因此这里只断言版本 ID。
	updated, err := s.getLocked(item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.VersionID != item.MCVersion {
		t.Fatalf("版本 ID 期望 %q，实际 %q", item.MCVersion, updated.VersionID)
	}
}

// TestInstalledVersionIDConcurrentSameIdentity 验证同一身份并发时只执行一次安装。
//
// 核心版本由所有实例共用：两个并发安装若不串行化，会同时写 versions/<id>/
// 并互相破坏。
func TestInstalledVersionIDConcurrentSameIdentity(t *testing.T) {
	s := testService(t)

	writeVersionJSONAt(t, s.root, "26.3", `{"id":"26.3"}`)

	var runs int32
	var wg sync.WaitGroup
	for index := 0; index < 4; index++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// 全部走 installWithManifest：先取锁，再二次检查。
			err := s.installWithManifest(context.Background(), loader.KindVanilla, "26.3", "", "26.3",
				func() (installArtifacts, error) {
					atomic.AddInt32(&runs, 1)
					return installArtifacts{}, nil
				})
			if err != nil {
				t.Errorf("installWithManifest: %v", err)
			}
		}()
	}
	wg.Wait()

	// 第一次会真正安装并写清单；其余三次在二次检查处命中并跳过。
	if runs != 1 {
		t.Fatalf("同身份并发安装期望执行 1 次，实际 %d 次", runs)
	}
	if _, installed := s.installedVersionID(loader.KindVanilla, "26.3", ""); !installed {
		t.Fatal("并发安装后应留下完整清单")
	}
}

// TestInstallWithManifestSkipsSecondRun 验证第二次调用不再执行安装。
func TestInstallWithManifestSkipsSecondRun(t *testing.T) {
	s := testService(t)
	writeVersionJSONAt(t, s.root, "26.3", `{"id":"26.3"}`)

	runs := 0
	run := func() (installArtifacts, error) {
		runs++
		return installArtifacts{}, nil
	}

	if err := s.installWithManifest(context.Background(), loader.KindVanilla, "26.3", "", "26.3", run); err != nil {
		t.Fatalf("首次安装失败: %v", err)
	}
	if err := s.installWithManifest(context.Background(), loader.KindVanilla, "26.3", "", "26.3", run); err != nil {
		t.Fatalf("二次安装失败: %v", err)
	}
	if runs != 1 {
		t.Fatalf("安装应只执行一次，实际 %d 次", runs)
	}
}
