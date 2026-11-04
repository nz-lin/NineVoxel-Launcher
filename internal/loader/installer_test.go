package loader

import (
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
