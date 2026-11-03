package loader

import "testing"

func TestVersionIDAndInputValidation(t *testing.T) {
	tests := []struct {
		kind     Kind
		mc       string
		version  string
		expected string
	}{
		{KindVanilla, "1.21.4", "", "1.21.4"},
		{KindFabric, "1.21.4", "0.16.14", "fabric-loader-0.16.14-1.21.4"},
		{KindQuilt, "1.21.4", "0.27.0", "quilt-loader-0.27.0-1.21.4"},
		{KindForge, "1.20.1", "47.3.12", "1.20.1-forge-47.3.12"},
		{KindNeoForge, "1.20.1", "47.1.106", "1.20.1-forge-47.1.106"},
		{KindNeoForge, "1.21.1", "21.1.80", "neoforge-21.1.80"},
		{KindNeoForge, "26.3", "26.3.0.101", "neoforge-26.3.0.101"},
	}
	for _, test := range tests {
		got, err := VersionID(test.kind, test.mc, test.version)
		if err != nil || got != test.expected {
			t.Errorf("VersionID(%q, %q, %q) = %q, %v; want %q", test.kind, test.mc, test.version, got, err, test.expected)
		}
	}
	for _, version := range []string{"../1.21", "1.21/evil", "1.21\\evil", "", "1.21:evil"} {
		if _, err := VersionID(KindFabric, version, "0.16.14"); err == nil {
			t.Errorf("accepted invalid Minecraft version %q", version)
		}
	}
	for _, version := range []string{"0.16.14/evil", "0.16.14\\evil", "0.16.14:evil", ""} {
		if _, err := VersionID(KindFabric, "1.21.4", version); err == nil {
			t.Errorf("accepted invalid loader version %q", version)
		}
	}
}

func TestProfileLoaderVersionParsingSupportsNestedAndFlatResponses(t *testing.T) {
	stable := true
	preview := false
	nested := []profileLoaderEntry{
		{Loader: &struct {
			Version string `json:"version"`
			Stable  *bool  `json:"stable"`
		}{Version: "0.16.14", Stable: &stable}},
		{Loader: &struct {
			Version string `json:"version"`
			Stable  *bool  `json:"stable"`
		}{Version: "0.17.0-beta.1", Stable: &preview}},
	}
	versions := parseProfileLoaderVersions(nested)
	if len(versions) != 2 || versions[0].Version != "0.16.14" || !versions[0].Stable || !versions[0].Recommended || versions[1].Version != "0.17.0-beta.1" || versions[1].Stable {
		t.Fatalf("nested versions = %+v", versions)
	}

	flat := []profileLoaderEntry{
		{Version: "0.16.14", Stable: &stable},
		{Version: "0.16.14", Stable: &preview},
		{Version: "../bad"},
	}
	versions = parseProfileLoaderVersions(flat)
	if len(versions) != 1 || versions[0].Version != "0.16.14" || !versions[0].Stable {
		t.Fatalf("flat versions = %+v", versions)
	}
}

func TestProfileLoaderVersionsKeepHistoricalReleasesStable(t *testing.T) {
	stable := true
	historical := false
	entries := []profileLoaderEntry{
		{Loader: &struct {
			Version string `json:"version"`
			Stable  *bool  `json:"stable"`
		}{Version: "0.20.1", Stable: &stable}},
		{Loader: &struct {
			Version string `json:"version"`
			Stable  *bool  `json:"stable"`
		}{Version: "0.20.0", Stable: &historical}},
		{Loader: &struct {
			Version string `json:"version"`
			Stable  *bool  `json:"stable"`
		}{Version: "0.21.0-beta.1", Stable: &historical}},
	}

	versions := parseProfileLoaderVersions(entries)
	if len(versions) != 3 {
		t.Fatalf("versions = %+v", versions)
	}
	if !versions[0].Stable || !versions[0].Recommended || !versions[1].Stable || versions[2].Stable {
		t.Fatalf("stable flags = %+v", versions)
	}
}

func TestLoaderVersionsSortStableBeforePrerelease(t *testing.T) {
	versions := []Version{
		{Version: "0.31.0-beta.5"},
		{Version: "0.30.1", Stable: true},
		{Version: "0.31.0-beta.4"},
		{Version: "0.30.2", Stable: true},
	}
	sortVersions(versions)

	want := []string{"0.30.2", "0.30.1", "0.31.0-beta.5", "0.31.0-beta.4"}
	for index, version := range versions {
		if version.Version != want[index] {
			t.Fatalf("sorted versions = %+v", versions)
		}
	}
}

func TestNeoForgeCatalogMapping(t *testing.T) {
	tests := []struct {
		mc        string
		group     string
		prefix    string
		display   string
		supported bool
	}{
		{"1.20.1", "forge", "1.20.1-47.1.", "1.20.1-", true},
		{"1.20.2", "neoforge", "20.2.", "", true},
		{"1.21", "neoforge", "21.0.", "", true},
		{"1.21.1", "neoforge", "21.1.", "", true},
		{"26.3", "neoforge", "26.3.", "", true},
		{"1.19.4", "", "", "", false},
		{"1.20", "", "", "", false},
		{"26.4-snapshot-6", "", "", "", false},
	}
	for _, test := range tests {
		group, prefix, display, supported := neoForgeCatalog(test.mc)
		if group != test.group || prefix != test.prefix || display != test.display || supported != test.supported {
			t.Errorf("neoForgeCatalog(%q) = %q, %q, %q, %v", test.mc, group, prefix, display, supported)
		}
	}
}
