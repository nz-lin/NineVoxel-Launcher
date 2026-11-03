package instance

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
func testConfig(name string) Config {
	return Config{Name: name, MCVersion: "1.21.4", Loader: "vanilla", MemoryMB: 4096}
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
	config.Loader = "fabric"
	if _, err := s.CreateInstance(config); err == nil {
		t.Fatal("accepted unsupported loader")
	}
	config = testConfig("test")
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
	s.installer = func(context.Context, Instance) error { return nil }
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
