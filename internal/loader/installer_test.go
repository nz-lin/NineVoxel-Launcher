package loader

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallerCoordinates(t *testing.T) {
	tests := []struct {
		kind      Kind
		mc        string
		version   string
		group     string
		coord     string
		fileName  string
		versionID string
	}{
		{KindForge, "1.20.1", "47.3.12", "", "1.20.1-47.3.12", "forge-1.20.1-47.3.12-installer.jar", "1.20.1-forge-47.3.12"},
		{KindNeoForge, "1.20.1", "47.1.106", "forge", "1.20.1-47.1.106", "forge-1.20.1-47.1.106-installer.jar", "1.20.1-forge-47.1.106"},
		{KindNeoForge, "1.21.1", "21.1.80", "neoforge", "21.1.80", "neoforge-21.1.80-installer.jar", "neoforge-21.1.80"},
		{KindNeoForge, "26.3", "26.3.0.101", "neoforge", "26.3.0.101", "neoforge-26.3.0.101-installer.jar", "neoforge-26.3.0.101"},
	}
	for _, test := range tests {
		group, coord, fileName, versionID, err := installerCoordinates(test.kind, test.mc, test.version)
		if err != nil || group != test.group || coord != test.coord || fileName != test.fileName || versionID != test.versionID {
			t.Errorf("installerCoordinates(%q, %q, %q) = %q, %q, %q, %q, %v", test.kind, test.mc, test.version, group, coord, fileName, versionID, err)
		}
	}
	if _, _, _, _, err := installerCoordinates(KindFabric, "1.21.4", "0.16.14"); err == nil {
		t.Fatal("Fabric must not resolve an installer")
	}
}

func TestParseSHA1(t *testing.T) {
	want := strings.Repeat("a", 40)
	for _, body := range []string{want, "  " + strings.ToUpper(want) + "  archive.jar\n", "sha1=" + want} {
		got, err := parseSHA1([]byte(body))
		if err != nil || got != want {
			t.Errorf("parseSHA1(%q) = %q, %v", body, got, err)
		}
	}
	for _, body := range []string{"", "not a checksum", strings.Repeat("g", 40)} {
		if _, err := parseSHA1([]byte(body)); err == nil {
			t.Errorf("accepted invalid checksum %q", body)
		}
	}
}

func TestEnsureLauncherProfiles(t *testing.T) {
	game := t.TempDir()
	if err := ensureLauncherProfiles(game); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(game, "launcher_profiles.json"))
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Profiles map[string]any `json:"profiles"`
		Settings map[string]any `json:"settings"`
		Version  int            `json:"version"`
	}
	if err := json.Unmarshal(body, &document); err != nil || document.Version != 3 || document.Profiles == nil || document.Settings == nil {
		t.Fatalf("minimal launcher profile = %s, %v", body, err)
	}

	existing := []byte(`{"profiles":{"minecraft":{"name":"keep"}},"settings":{},"version":3}`)
	if err := os.WriteFile(filepath.Join(game, "launcher_profiles.json"), existing, 0600); err != nil {
		t.Fatal(err)
	}
	if err := ensureLauncherProfiles(game); err != nil {
		t.Fatal(err)
	}
	if body, err := os.ReadFile(filepath.Join(game, "launcher_profiles.json")); err != nil || string(body) != string(existing) {
		t.Fatalf("existing launcher profile was modified: %s, %v", body, err)
	}

	broken := t.TempDir()
	if err := os.WriteFile(filepath.Join(broken, "launcher_profiles.json"), []byte("{broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := ensureLauncherProfiles(broken); err == nil {
		t.Fatal("accepted malformed launcher profile")
	}
}

func TestDetectInstalledVersionID(t *testing.T) {
	game := t.TempDir()
	expected := "1.20.1-forge-47.3.12"
	writeVersionFixture(t, game, expected, "1.20.1")
	got, err := DetectInstalledVersionID(game, KindForge, "1.20.1", "47.3.12", map[string]bool{})
	if err != nil || got != expected {
		t.Fatalf("detect expected = %q, %v", got, err)
	}

	alternateGame := t.TempDir()
	alternate := "forge-47.3.12-custom"
	writeVersionFixture(t, alternateGame, alternate, "1.20.1")
	got, err = DetectInstalledVersionID(game, KindForge, "1.20.1", "47.3.12", map[string]bool{"1.20.1": true, expected: true})
	if err != nil || got != expected {
		t.Fatalf("detect preferred = %q, %v", got, err)
	}
	got, err = DetectInstalledVersionID(alternateGame, KindForge, "1.20.1", "47.3.12", map[string]bool{"1.20.1": true})
	if err != nil || got != alternate {
		t.Fatalf("detect alternate = %q, %v", got, err)
	}

	if _, err := DetectInstalledVersionID(alternateGame, KindForge, "1.20.1", "47.3.12", map[string]bool{"1.20.1": true, alternate: true}); err == nil {
		t.Fatal("expected missing version directory error")
	}
}

func writeVersionFixture(t *testing.T, game, versionID, parent string) {
	t.Helper()
	directory := filepath.Join(game, "versions", versionID)
	if err := os.MkdirAll(directory, 0755); err != nil {
		t.Fatal(err)
	}
	body := `{"id":"` + versionID + `","inheritsFrom":"` + parent + `","mainClass":"net.minecraft.client.main.Main"}`
	if err := os.WriteFile(filepath.Join(directory, versionID+".json"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}
