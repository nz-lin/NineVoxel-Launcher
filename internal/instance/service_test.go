package instance

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"example.com/ninevoxel/launcher/internal/launch"
)

func testService(t *testing.T) *InstanceService {
	t.Helper()
	s := &InstanceService{root: t.TempDir()}
	t.Cleanup(func() {
		if err := s.ServiceShutdown(); err != nil {
			t.Error(err)
		}
	})
	return s
}
// testConfig 构造一个测试用实例配置。
//
// 内存与 Java 显式使用「自定义」模式：新模型的默认是「跟随全局」，
// 后者会把 MemoryMB 归一化为 0，测试若依赖具体数值就会失效。
func testConfig(name string) Config {
	return Config{
		Name:       name,
		MCVersion:  "1.21.4",
		Loader:     "vanilla",
		MemoryMode: launch.MemoryCustom,
		MemoryMB:   4096,
	}
}

func TestInstancePersistenceAndSelection(t *testing.T) {
	s := testService(t)
	first, err := s.CreateInstance(testConfig("第一个实例"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.CreateInstance(testConfig("第二个实例"))
	if err != nil {
		t.Fatal(err)
	}
	if first.FolderPath == second.FolderPath {
		t.Fatal("instance directories must be isolated")
	}
	if _, err := os.Stat(filepath.Join(s.root, filepath.FromSlash(first.FolderPath))); err != nil {
		t.Fatal(err)
	}
	first.Name = "已修改"
	first.IsFavorite = true
	first.MemoryMB = 8192
	if _, err := s.UpdateInstance(UpdateRequest{Config: first.Config, InstanceID: first.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SelectInstance(IDRequest{InstanceID: second.ID}); err != nil {
		t.Fatal(err)
	}
	reopened := &InstanceService{root: s.root}
	defer reopened.ServiceShutdown()
	list, err := reopened.ListInstances()
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 2 || list.SelectedInstanceID != second.ID {
		t.Fatalf("persistence: %+v", list)
	}
	found := false
	for _, item := range list.Items {
		if item.ID == first.ID {
			found = item.Name == "已修改" && item.IsFavorite && item.MemoryMB == 8192
		}
	}
	if !found {
		t.Fatal("updated settings were lost")
	}
}

func TestOpenInstanceFolderUsesPlatformDirectoryOpener(t *testing.T) {
	s := testService(t)
	item, err := s.CreateInstance(testConfig("打开目录"))
	if err != nil {
		t.Fatal(err)
	}
	original := openInstanceDirectory
	opened := ""
	openInstanceDirectory = func(dir string) error {
		opened = dir
		return nil
	}
	t.Cleanup(func() { openInstanceDirectory = original })

	if err := s.OpenInstanceFolder(IDRequest{InstanceID: item.ID}); err != nil {
		t.Fatal(err)
	}
	expected, err := s.gameDir(item)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Clean(opened) != filepath.Clean(expected) {
		t.Fatalf("opened directory = %q, want %q", opened, expected)
	}
}

func TestOpenInstanceFolderRejectsMissingDirectory(t *testing.T) {
	s := testService(t)
	item, err := s.CreateInstance(testConfig("目录缺失"))
	if err != nil {
		t.Fatal(err)
	}
	game, err := s.gameDir(item)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(game); err != nil {
		t.Fatal(err)
	}
	called := false
	original := openInstanceDirectory
	openInstanceDirectory = func(string) error {
		called = true
		return nil
	}
	t.Cleanup(func() { openInstanceDirectory = original })

	if err := s.OpenInstanceFolder(IDRequest{InstanceID: item.ID}); err == nil {
		t.Fatal("expected missing instance directory error")
	}
	if called {
		t.Fatal("platform opener must not run for a missing directory")
	}
}

func TestDeleteArchivesFilesAndAllowsEmptyList(t *testing.T) {
	s := testService(t)
	item, err := s.CreateInstance(testConfig("存档测试"))
	if err != nil {
		t.Fatal(err)
	}
	game, err := s.gameDir(item)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(game, "world.txt"), []byte("keep my world"), 0600); err != nil {
		t.Fatal(err)
	}
	list, err := s.DeleteInstance(IDRequest{InstanceID: item.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 0 || list.SelectedInstanceID != "" {
		t.Fatalf("expected empty list: %+v", list)
	}
	archived, err := filepath.Glob(filepath.Join(s.root, "instances", ".trash", item.ID+"-*", ".minecraft", "world.txt"))
	if err != nil || len(archived) != 1 {
		t.Fatalf("archive missing: %v %v", archived, err)
	}
	body, err := os.ReadFile(archived[0])
	if err != nil || string(body) != "keep my world" {
		t.Fatal("user world was not preserved")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(archived[0]), "..", manifestFileName)); err != nil {
		t.Fatalf("归档实例缺少清单：%v", err)
	}
}

func TestInstanceManifestSurvivesDatabaseLoss(t *testing.T) {
	s := testService(t)
	first, err := s.CreateInstance(testConfig("文件权威实例"))
	if err != nil {
		t.Fatal(err)
	}
	first.Name = "数据库丢失后仍保留"
	first.MemoryMB = 6144
	if _, err := s.UpdateInstance(UpdateRequest{Config: first.Config, InstanceID: first.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateInstance(testConfig("第二个实例")); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(s.root, filepath.FromSlash(activeInstanceRel(first.ID)), manifestFileName)
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"schemaVersion": 1`) || !strings.Contains(string(raw), first.Name) {
		t.Fatalf("实例清单内容不完整：%s", raw)
	}
	if err := s.ServiceShutdown(); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		_ = os.Remove(filepath.Join(s.root, "app.db"+suffix))
	}

	reopened := &InstanceService{root: s.root}
	defer reopened.ServiceShutdown()
	list, err := reopened.ListInstances()
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 2 || list.SelectedInstanceID == "" {
		t.Fatalf("从实例清单恢复失败：%+v", list)
	}
	found := false
	for _, item := range list.Items {
		if item.ID == first.ID {
			found = item.Name == first.Name && item.MemoryMB == 6144
		}
	}
	if !found {
		t.Fatal("数据库重建后实例配置丢失")
	}
}

func TestDeletedInstanceFolderRemovesGhostIndex(t *testing.T) {
	s := testService(t)
	item, err := s.CreateInstance(testConfig("手动删除目录"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(s.root, filepath.FromSlash(activeInstanceRel(item.ID)))); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListInstances()
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 0 || list.SelectedInstanceID != "" {
		t.Fatalf("已删除目录仍留下幽灵记录：%+v", list)
	}
}

func TestDeleteMissingInstanceFolderStillSucceeds(t *testing.T) {
	s := testService(t)
	item, err := s.CreateInstance(testConfig("删除失效实例"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(s.root, filepath.FromSlash(activeInstanceRel(item.ID)))); err != nil {
		t.Fatal(err)
	}
	list, err := s.DeleteInstance(IDRequest{InstanceID: item.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 0 {
		t.Fatalf("失效实例记录未清理：%+v", list)
	}
}

func TestInstanceTrashListAndRestore(t *testing.T) {
	s := testService(t)
	item, err := s.CreateInstance(testConfig("回收站实例"))
	if err != nil {
		t.Fatal(err)
	}
	game, err := s.gameDir(item)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(game, "world.txt"), []byte("restore me"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeleteInstance(IDRequest{InstanceID: item.ID}); err != nil {
		t.Fatal(err)
	}
	trash, err := s.ListInstanceTrash()
	if err != nil {
		t.Fatal(err)
	}
	if len(trash.Items) != 1 {
		t.Fatalf("回收站条目数 = %d，期望 1", len(trash.Items))
	}
	entry := trash.Items[0]
	if entry.InstanceID != item.ID || entry.Name != item.Name || entry.MCVersion != item.MCVersion || entry.Loader != "vanilla" || entry.DeletedAtMS <= 0 || entry.SizeBytes <= 0 || !entry.Restorable {
		t.Fatalf("回收站条目信息不完整：%+v", entry)
	}
	list, err := s.RestoreInstanceFromTrash(TrashIDRequest{TrashID: entry.TrashID})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 || list.Items[0].ID != item.ID || list.Items[0].Name != item.Name {
		t.Fatalf("还原后的实例列表：%+v", list)
	}
	body, err := os.ReadFile(filepath.Join(game, "world.txt"))
	if err != nil || string(body) != "restore me" {
		t.Fatalf("还原后世界文件丢失：%q %v", body, err)
	}
	trash, err = s.ListInstanceTrash()
	if err != nil {
		t.Fatal(err)
	}
	if len(trash.Items) != 0 {
		t.Fatalf("还原后回收站应为空：%+v", trash.Items)
	}
}

func TestInstanceTrashRestoreConflictAndPermanentDelete(t *testing.T) {
	s := testService(t)
	item, err := s.CreateInstance(testConfig("冲突实例"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeleteInstance(IDRequest{InstanceID: item.ID}); err != nil {
		t.Fatal(err)
	}
	trash, err := s.ListInstanceTrash()
	if err != nil {
		t.Fatal(err)
	}
	if len(trash.Items) != 1 {
		t.Fatalf("回收站条目数 = %d", len(trash.Items))
	}
	trashID := trash.Items[0].TrashID
	directory := filepath.Join(s.root, filepath.FromSlash(activeInstanceRel(item.ID)))
	if err := os.MkdirAll(directory, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RestoreInstanceFromTrash(TrashIDRequest{TrashID: trashID}); err == nil {
		t.Fatal("原目录存在时不应覆盖还原")
	}
	if err := os.RemoveAll(directory); err != nil {
		t.Fatal(err)
	}
	trash, err = s.DeleteInstancePermanently(TrashIDRequest{TrashID: trashID})
	if err != nil {
		t.Fatal(err)
	}
	if len(trash.Items) != 0 {
		t.Fatalf("永久删除后回收站应为空：%+v", trash.Items)
	}
	if _, err := os.Stat(filepath.Join(s.root, filepath.FromSlash(trashInstanceRel(trashID)))); !os.IsNotExist(err) {
		t.Fatal("永久删除后回收站目录仍存在")
	}
}

func TestEmptyInstanceTrash(t *testing.T) {
	s := testService(t)
	first, err := s.CreateInstance(testConfig("回收站一"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.CreateInstance(testConfig("回收站二"))
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []Instance{first, second} {
		if _, err := s.DeleteInstance(IDRequest{InstanceID: item.ID}); err != nil {
			t.Fatal(err)
		}
	}
	trash, err := s.ListInstanceTrash()
	if err != nil {
		t.Fatal(err)
	}
	if len(trash.Items) != 2 {
		t.Fatalf("回收站条目数 = %d，期望 2", len(trash.Items))
	}
	trash, err = s.EmptyInstanceTrash()
	if err != nil {
		t.Fatal(err)
	}
	if len(trash.Items) != 0 {
		t.Fatalf("清空后回收站仍有条目：%+v", trash.Items)
	}
	entries, err := os.ReadDir(filepath.Join(s.root, filepath.FromSlash(trashRootRel)))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("清空后磁盘上仍有回收站目录：%v", entries)
	}
}

// TestInstanceTrashRejectsArchiveWithoutManifest 验证清单丢失的归档不可恢复。
//
// 存储改为共享核心后，实例目录不再包含 versions / libraries / assets，
// 因此无法再从目录推断它用的是哪个游戏版本：同一版本可能被多个实例共用，
// 猜一个版本会把实例指向错误的核心资源。此时应明确报「不可恢复」，
// 而不是恢复出一个版本未知的空壳实例。
func TestInstanceTrashRejectsArchiveWithoutManifest(t *testing.T) {
	s := testService(t)
	item, err := s.CreateInstance(testConfig("旧归档实例"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeleteInstance(IDRequest{InstanceID: item.ID}); err != nil {
		t.Fatal(err)
	}
	manifestPaths, err := filepath.Glob(filepath.Join(s.root, filepath.FromSlash(trashRootRel), item.ID+"-*", manifestFileName))
	if err != nil || len(manifestPaths) != 1 {
		t.Fatalf("归档清单路径：%v %v", manifestPaths, err)
	}
	if err := os.Remove(manifestPaths[0]); err != nil {
		t.Fatal(err)
	}
	trash, err := s.ListInstanceTrash()
	if err != nil {
		t.Fatal(err)
	}
	if len(trash.Items) != 1 {
		t.Fatalf("回收站条目数 = %d", len(trash.Items))
	}
	if trash.Items[0].Restorable {
		t.Fatalf("清单丢失的归档不应标记为可恢复：%+v", trash.Items[0])
	}
	if trash.Items[0].ErrorMessage == "" {
		t.Fatalf("不可恢复时应给出原因：%+v", trash.Items[0])
	}
	if _, err := s.RestoreInstanceFromTrash(TrashIDRequest{TrashID: trash.Items[0].TrashID}); err == nil {
		t.Fatal("清单丢失的归档不应恢复成功")
	}
}

func TestInstanceRejectsInvalidInputAndPaths(t *testing.T) {
	s := testService(t)
	for _, version := range []string{"../1.21", "C:\\Windows", "a/b", "a\\b", ""} {
		config := testConfig("test")
		config.MCVersion = version
		if _, err := s.CreateInstance(config); err == nil {
			t.Errorf("accepted version %q", version)
		}
	}
	config := testConfig("test")
	config.Loader = "optifine"
	if _, err := s.CreateInstance(config); err == nil {
		t.Fatal("accepted unsupported loader")
	}
	config = testConfig("fabric")
	config.Loader = "fabric"
	config.LoaderVersion = "0.16.14"
	if _, err := s.CreateInstance(config); err != nil {
		t.Fatalf("rejected supported loader: %v", err)
	}
	config = testConfig("test")
	config.MemoryMode = launch.MemoryCustom
	config.MemoryMB = 128
	if _, err := s.CreateInstance(config); err == nil {
		t.Fatal("accepted invalid memory")
	}
	item, err := s.CreateInstance(testConfig("../name is not a path"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(item.FolderPath, "name") {
		t.Fatal("display name leaked into filesystem path")
	}
	item.MCVersion = "1.20.1"
	if _, err := s.UpdateInstance(UpdateRequest{Config: item.Config, InstanceID: item.ID}); err == nil {
		t.Fatal("allowed incompatible in-place version change")
	}
	item.FolderPath = "../outside"
	if _, err := s.gameDir(item); err == nil {
		t.Fatal("accepted corrupted directory")
	}
}

func TestInstallationFailureRetryAndIdempotence(t *testing.T) {
	s := testService(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	s.installer = func(ctx context.Context, item Instance) error {
		close(entered)
		select {
		case <-release:
			return errors.New("network fixture failed")
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	item, err := s.CreateInstance(testConfig("retry"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.InstallInstance(IDRequest{InstanceID: item.ID}); err != nil {
		t.Fatal(err)
	}
	<-entered
	if repeated, err := s.InstallInstance(IDRequest{InstanceID: item.ID}); err != nil || repeated.InstallationState != "installing" {
		t.Fatalf("duplicate install: %+v %v", repeated, err)
	}
	close(release)
	s.workers.Wait()
	list, err := s.ListInstances()
	if err != nil {
		t.Fatal(err)
	}
	if list.Items[0].InstallationState != "failed" || !strings.Contains(list.Items[0].InstallationError, "fixture") {
		t.Fatalf("failure lost: %+v", list)
	}
	s.installer = func(context.Context, Instance) error {
		// 真实安装必然在核心中产出对应版本目录；这里如实模拟，
		// 否则「已安装」状态会被核心校验判为不成立。
		fixtureWrite(t, filepath.Join(s.root, "versions", "1.21.4", "1.21.4.json"), []byte(`{"id":"1.21.4"}`))
		return nil
	}
	if _, err := s.InstallInstance(IDRequest{InstanceID: item.ID}); err != nil {
		t.Fatal(err)
	}
	s.workers.Wait()
	list, err = s.ListInstances()
	if err != nil {
		t.Fatal(err)
	}
	if list.Items[0].InstallationState != "installed" || list.Items[0].InstallationProgress != 1 || list.Items[0].InstallationError != "" {
		t.Fatalf("retry failed: %+v", list)
	}
}

func TestDeleteInstallingInstanceCancelsInstall(t *testing.T) {
	s := testService(t)
	entered := make(chan struct{})
	exited := make(chan struct{})
	s.installer = func(ctx context.Context, _ Instance) error {
		close(entered)
		<-ctx.Done()
		close(exited)
		return ctx.Err()
	}
	item, err := s.CreateInstance(testConfig("delete-installing"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.InstallInstance(IDRequest{InstanceID: item.ID}); err != nil {
		t.Fatal(err)
	}
	<-entered
	list, err := s.DeleteInstance(IDRequest{InstanceID: item.ID})
	if err != nil {
		t.Fatalf("删除安装中的实例失败：%v", err)
	}
	if len(list.Items) != 0 {
		t.Fatalf("实例未被删除：%+v", list.Items)
	}
	select {
	case <-exited:
	case <-time.After(3 * time.Second):
		t.Fatal("安装协程未在取消后退出")
	}
}

func TestInterruptedInstallSurvivesRestart(t *testing.T) {
	s := testService(t)
	item, err := s.CreateInstance(testConfig("interrupted"))
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	item.InstallationState = "installing"
	err = s.saveLocked(item)
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	reopened := &InstanceService{root: s.root}
	defer reopened.ServiceShutdown()
	list, err := reopened.ListInstances()
	if err != nil {
		t.Fatal(err)
	}
	if list.Items[0].InstallationState != "failed" || !strings.Contains(list.Items[0].InstallationError, "中断") {
		t.Fatalf("interruption lost: %+v", list)
	}
}

func TestShutdownCancelsInstaller(t *testing.T) {
	s := &InstanceService{root: t.TempDir()}
	s.installer = func(ctx context.Context, _ Instance) error { <-ctx.Done(); return ctx.Err() }
	item, err := s.CreateInstance(testConfig("shutdown"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.InstallInstance(IDRequest{InstanceID: item.ID}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.ServiceShutdown() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown did not stop installer")
	}
}

func TestManagedPathRejectsLinkedAncestor(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := managedPath(root, "linked/missing/file"); err == nil {
		t.Fatal("accepted a missing file below linked ancestor")
	}
}
