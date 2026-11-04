package instance

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpdateLoaderAndUninstallLoader(t *testing.T) {
	s := testService(t)
	item, err := s.CreateInstance(testConfig("loader lifecycle"))
	if err != nil {
		t.Fatal(err)
	}
	game, err := s.gameDir(item)
	if err != nil {
		t.Fatal(err)
	}
	baseJSON := filepath.Join(s.root, "versions", item.MCVersion, item.MCVersion+".json")
	fixtureWrite(t, baseJSON, []byte(`{"id":"`+item.MCVersion+`","mainClass":"net.minecraft.client.main.Main"}`))

	loaderVersion := "0.16.14"
	loaderID := "fabric-loader-" + loaderVersion + "-" + item.MCVersion
	s.installer = func(ctx context.Context, installed Instance) error {
		if installed.Loader != "fabric" || installed.LoaderVersion != loaderVersion {
			t.Fatalf("unexpected loader install request: %+v", installed.Config)
		}
		versionJSON := filepath.Join(s.root, "versions", loaderID, loaderID+".json")
		fixtureWrite(t, versionJSON, []byte(`{"id":"`+loaderID+`","inheritsFrom":"`+item.MCVersion+`","mainClass":"net.fabricmc.loader.impl.launch.knot.KnotClient"}`))
		return s.setResolvedVersionID(installed.ID, loaderID)
	}

	updated, err := s.UpdateLoader(ChangeLoaderRequest{InstanceID: item.ID, Loader: "fabric", LoaderVersion: loaderVersion})
	if err != nil {
		t.Fatal(err)
	}
	if updated.InstallationState != "installing" || updated.Loader != "fabric" || updated.LoaderVersion != loaderVersion {
		t.Fatalf("update did not start: %+v", updated)
	}
	s.workers.Wait()
	list, err := s.ListInstances()
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 || list.Items[0].InstallationState != "installed" || list.Items[0].VersionID != loaderID {
		t.Fatalf("loader install result: %+v", list.Items)
	}

	mods := filepath.Join(game, "mods")
	fixtureWrite(t, filepath.Join(mods, "example.jar"), []byte("jar"))
	fixtureWrite(t, filepath.Join(mods, "notes.txt"), []byte("keep"))
	uninstalled, err := s.UninstallLoader(IDRequest{InstanceID: item.ID})
	if err != nil {
		t.Fatal(err)
	}
	if uninstalled.Loader != "vanilla" || uninstalled.LoaderVersion != "" || uninstalled.VersionID != item.MCVersion || uninstalled.InstallationState != "installed" {
		t.Fatalf("uninstall result: %+v", uninstalled)
	}
	if _, err := os.Stat(filepath.Join(s.root, "versions", loaderID)); !os.IsNotExist(err) {
		t.Fatalf("loader version directory was not removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(mods, "example.jar.disabled")); err != nil {
		t.Fatalf("jar mod was not disabled: %v", err)
	}
	if _, err := os.Stat(filepath.Join(mods, "example.jar")); !os.IsNotExist(err) {
		t.Fatalf("active jar mod still exists: %v", err)
	}
	if body, err := os.ReadFile(filepath.Join(mods, "notes.txt")); err != nil || strings.TrimSpace(string(body)) != "keep" {
		t.Fatalf("non-jar mod file changed: %q, %v", body, err)
	}
}

func TestUpdateLoaderRejectsVanillaAndInvalidVersion(t *testing.T) {
	s := testService(t)
	item, err := s.CreateInstance(testConfig("loader validation"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateLoader(ChangeLoaderRequest{InstanceID: item.ID, Loader: "vanilla", LoaderVersion: ""}); err == nil {
		t.Fatal("accepted vanilla as a loader update")
	}
	if _, err := s.UpdateLoader(ChangeLoaderRequest{InstanceID: item.ID, Loader: "fabric", LoaderVersion: "../bad"}); err == nil {
		t.Fatal("accepted invalid loader version")
	}
	if _, err := s.UpdateLoader(ChangeLoaderRequest{InstanceID: item.ID, Loader: "unknown", LoaderVersion: "1.0.0"}); err == nil {
		t.Fatal("accepted unknown loader")
	}
}
