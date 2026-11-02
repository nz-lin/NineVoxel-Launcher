package version

import (
	"context"
	"example.com/ninevoxel/launcher/internal/download"
	"strings"
	"testing"
)

func TestPrepareInstallationMapsStandardMinecraftLayout(t *testing.T) {
	setupPlanFixture(t)
	prepared, err := PrepareInstallation("1.20.1")
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.Plan.Items) == 0 || prepared.Plan.PlanHash == "" || prepared.AssetIndexID == "" {
		t.Fatalf("incomplete plan: %+v", prepared)
	}
	found := map[string]bool{}
	for _, file := range prepared.Files {
		if strings.Contains(file.Target, "..") || strings.HasPrefix(file.Target, "/") {
			t.Fatalf("unsafe target %q", file.Target)
		}
		if strings.HasPrefix(file.Target, "libraries/") {
			found["library"] = true
		}
		if strings.HasPrefix(file.Target, "assets/indexes/") {
			found["index"] = true
		}
		if strings.HasSuffix(file.Target, "1.20.1.jar") {
			found["client"] = true
		}
		if file.Extract && strings.HasSuffix(file.Target, "/natives") {
			found["native"] = true
		}
	}
	for _, kind := range []string{"client", "library", "index", "native"} {
		if !found[kind] {
			t.Errorf("missing %s", kind)
		}
	}
	bridge, appErr := DownloadPlan(context.Background(), download.StartDownloadPlanRequest{VersionID: "1.20.1"})
	if appErr != nil || bridge.PlanHash != prepared.Plan.PlanHash {
		t.Fatalf("bridge mismatch: %+v %v", bridge, appErr)
	}
}

func TestPrepareInstallationRejectsIncompleteAndUnsafeLibraries(t *testing.T) {
	root := setupTestDataRoot(t)
	doc := minimalVersionDocument("incomplete")
	delete(doc, "downloads")
	writeVersionMetadata(t, root, "incomplete", doc)
	if _, err := PrepareInstallation("incomplete"); err == nil {
		t.Fatal("accepted incomplete version")
	}
	doc = planFixtureDocument("unsafe")
	libraries := doc["libraries"].([]any)
	lib := libraries[0].(map[string]any)
	lib["downloads"].(map[string]any)["artifact"].(map[string]any)["path"] = "../../outside.jar"
	writeVersionMetadata(t, root, "unsafe", doc)
	if _, err := PrepareInstallation("unsafe"); err == nil {
		t.Fatal("accepted traversing library path")
	}
}

func TestPrepareInstallationPistonDataClient(t *testing.T) {
	const clientPath = "/v1/objects/e877b6a07acd633fb3bb475002175cec036e7b87/client.jar"
	cases := []struct {
		name    string
		base    string
		allowed bool
	}{
		{name: "official", base: "https://piston-data.mojang.com", allowed: true},
		{name: "http", base: "http://piston-data.mojang.com"},
		{name: "lookalike", base: "https://piston-data.mojang.com.evil.example"},
		{name: "subdomain", base: "https://evil.piston-data.mojang.com"},
		{name: "userinfo_host_spoof", base: "https://piston-data.mojang.com@evil.example"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := setupTestDataRoot(t)
			doc := planFixtureDocument("26.3")
			client := doc["downloads"].(map[string]any)["client"].(map[string]any)
			client["url"] = tc.base + clientPath
			client["sha1"] = "e877b6a07acd633fb3bb475002175cec036e7b87"
			client["size"] = 41483720
			writeVersionMetadata(t, root, "26.3", doc)
			prepared, err := PrepareInstallation("26.3")
			if !tc.allowed {
				if err == nil || !strings.Contains(err.Error(), "下载地址不在允许的来源列表中") {
					t.Fatalf("expected source rejection, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, item := range prepared.Plan.Items {
				if item.TaskType != download.TaskTypeClientJar {
					continue
				}
				if item.SourceURI != tc.base+clientPath || item.ExpectedSHA1 != client["sha1"] || item.ExpectedSize != 41483720 {
					t.Fatalf("client metadata changed: %+v", item)
				}
				if appErr := download.NewSourceRegistry(false).ValidateURL(item.SourceURI); appErr != nil {
					t.Fatalf("planned client rejected by downloader: %v", appErr)
				}
				return
			}
			t.Fatal("client missing from installation plan")
		})
	}
}
