package runtime

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestUninstallRuntimeRemovesManagedRuntimeAndSelection(t *testing.T) {
	f := newRuntimeInstallFixture(t, nil)
	createRuntimeInstallDir(t, f, "managed-test")
	row := runtimeRow{ID: "managed-test", Kind: SourceDownloaded, Selected: true, LocalRelPath: "runtimes/managed-test"}
	if err := saveRuntime(context.Background(), row); err != nil {
		t.Fatal(err)
	}
	list, err := UninstallRuntime(UninstallRuntimeRequest{RuntimeID: row.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 0 || list.SelectedRuntimeID != "" {
		t.Fatalf("stale selection: %+v", list)
	}
	if _, err := os.Stat(filepath.Join(f.dataRoot, "runtimes", row.ID)); !os.IsNotExist(err) {
		t.Fatalf("directory remains: %v", err)
	}
	if _, err := UninstallRuntime(UninstallRuntimeRequest{RuntimeID: row.ID}); err != nil {
		t.Fatal(err)
	}
}

func TestUninstallRuntimeRejectsExternalPaths(t *testing.T) {
	_ = newRuntimeInstallFixture(t, nil)
	for _, rel := range []string{"../outside", ".", "runtimes", "versions/test", "runtimes/other"} {
		row := runtimeRow{ID: "unsafe", Kind: SourceDownloaded, LocalRelPath: rel}
		if err := saveRuntime(context.Background(), row); err != nil {
			t.Fatal(err)
		}
		if _, err := UninstallRuntime(UninstallRuntimeRequest{RuntimeID: row.ID}); err == nil {
			t.Errorf("accepted %q", rel)
		}
	}
	local := runtimeRow{ID: "local-test", Kind: SourceLocal}
	if err := saveRuntime(context.Background(), local); err != nil {
		t.Fatal(err)
	}
	if _, err := UninstallRuntime(UninstallRuntimeRequest{RuntimeID: local.ID}); err == nil {
		t.Fatal("accepted local Java")
	}
}

func TestManagedRuntimeDirRejectsSymlink(t *testing.T) {
	f := newRuntimeInstallFixture(t, nil)
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(f.dataRoot, "runtimes"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(f.dataRoot, "runtimes", "linked")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := managedRuntimeDir(runtimeRow{ID: "linked", Kind: SourceDownloaded}); err == nil {
		t.Fatal("accepted linked runtime")
	}
}
