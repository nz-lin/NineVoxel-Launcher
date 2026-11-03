package loader

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeProfileLibrariesAddsMavenDownloads(t *testing.T) {
	const loaderURL = "https://maven.fabricmc.net/net/fabricmc/fabric-loader/0.20.1/fabric-loader-0.20.1.jar"
	const loaderSHA1 = "5566f8e9f27b303d5d08decb5f4258a9f03296e9"
	original := metadataHTTPClientFactory
	metadataHTTPClientFactory = func() *http.Client {
		return &http.Client{Transport: metadataRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			header := make(http.Header)
			response := &http.Response{StatusCode: http.StatusOK, Header: header, Request: request, Body: http.NoBody}
			switch {
			case request.Method == http.MethodHead && request.URL.String() == loaderURL:
				response.ContentLength = 1988880
			case request.Method == http.MethodGet && request.URL.String() == loaderURL+".sha1":
				response.Body = io.NopCloser(strings.NewReader(loaderSHA1 + "  fabric-loader.jar\n"))
			default:
				response.StatusCode = http.StatusNotFound
			}
			return response, nil
		})}
	}
	t.Cleanup(func() { metadataHTTPClientFactory = original })

	body := []byte(`{
  "id": "fabric-loader-0.20.1-26.3",
  "inheritsFrom": "26.3",
  "mainClass": "net.fabricmc.loader.impl.launch.knot.KnotClient",
  "arguments": {"game": [], "jvm": ["-DFabricMcEmu= net.minecraft.client.main.Main "]},
  "libraries": [
    {"name":"org.ow2.asm:asm:9.10.1","url":"https://maven.fabricmc.net/","md5":"keep-me","sha1":"ada2141c0cc52ee8f5c48cd5fa4ce0e794f22236","sha256":"ed825d10ab1399c8c0cb669e688cf0c8c82629b4c8399b58352b68e92ca10fcb","size":126151},
    {"name":"net.fabricmc:fabric-loader:0.20.1","url":"https://maven.fabricmc.net/"}
  ]
}`)
	normalized, err := normalizeProfileLibraries(context.Background(), "", KindFabric, "26.3", body)
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		ID        string `json:"id"`
		Arguments struct {
			JVM []string `json:"jvm"`
		} `json:"arguments"`
		Libraries []struct {
			Name      string `json:"name"`
			MD5       string `json:"md5"`
			Downloads *struct {
				Artifact *profileArtifactDocument `json:"artifact"`
			} `json:"downloads"`
		} `json:"libraries"`
	}
	if err := json.Unmarshal(normalized, &document); err != nil {
		t.Fatal(err)
	}
	if document.ID != "fabric-loader-0.20.1-26.3" || len(document.Arguments.JVM) != 1 {
		t.Fatalf("profile fields were not preserved: %s", normalized)
	}
	if len(document.Libraries) != 2 || document.Libraries[0].MD5 != "keep-me" {
		t.Fatalf("library fields were not preserved: %s", normalized)
	}
	loaderArtifact := document.Libraries[1].Downloads.Artifact
	if loaderArtifact == nil {
		t.Fatalf("loader artifact missing: %s", normalized)
	}
	if loaderArtifact.Path != "net/fabricmc/fabric-loader/0.20.1/fabric-loader-0.20.1.jar" ||
		loaderArtifact.URL != loaderURL || loaderArtifact.SHA1 != loaderSHA1 || loaderArtifact.Size != 1988880 {
		t.Fatalf("unexpected loader artifact: %+v", loaderArtifact)
	}
}

func TestProfileFileValidRejectsLegacyLibraries(t *testing.T) {
	const versionID = "fabric-loader-0.20.1-26.3"
	legacy := []byte(`{"id":"` + versionID + `","inheritsFrom":"26.3","mainClass":"Main","libraries":[{"name":"net.fabricmc:fabric-loader:0.20.1","url":"https://maven.fabricmc.net/"}]}`)
	path := filepath.Join(t.TempDir(), versionID+".json")
	if err := os.WriteFile(path, legacy, 0600); err != nil {
		t.Fatal(err)
	}
	if profileFileValid(path, versionID, "26.3") {
		t.Fatal("legacy profile without downloads.artifact must be refreshed")
	}
}

func TestMavenArtifactPath(t *testing.T) {
	tests := map[string]string{
		"net.fabricmc:fabric-loader:0.20.1":    "net/fabricmc/fabric-loader/0.20.1/fabric-loader-0.20.1.jar",
		"org.example:demo:1.0.0:linux":         "org/example/demo/1.0.0/demo-1.0.0-linux.jar",
		"org.example:native:1.0.0:windows@zip": "org/example/native/1.0.0/native-1.0.0-windows.zip",
	}
	for coordinate, want := range tests {
		got, err := mavenArtifactPath(coordinate)
		if err != nil || got != want {
			t.Errorf("mavenArtifactPath(%q) = %q, %v; want %q", coordinate, got, err, want)
		}
	}
	for _, invalid := range []string{"", "one:two", "a/b:c:1", "a:b:../escape"} {
		if _, err := mavenArtifactPath(invalid); err == nil {
			t.Errorf("accepted invalid Maven coordinate %q", invalid)
		}
	}
}
