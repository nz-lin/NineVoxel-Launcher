package loader

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type metadataRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn metadataRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func metadataTestClient(t *testing.T, responses map[string]string) {
	t.Helper()
	original := metadataHTTPClientFactory
	metadataHTTPClientFactory = func() *http.Client {
		return &http.Client{
			Transport: metadataRoundTripFunc(func(request *http.Request) (*http.Response, error) {
				body, ok := responses[request.URL.String()]
				status := http.StatusOK
				if !ok {
					status = http.StatusNotFound
				}
				return &http.Response{
					StatusCode: status,
					Body:       io.NopCloser(strings.NewReader(body)),
					Header:     make(http.Header),
					Request:    request,
				}, nil
			}),
		}
	}
	t.Cleanup(func() {
		metadataHTTPClientFactory = original
	})
}

func TestFetchMetadataDocumentFetchesWhenCachePathCannotBeResolved(t *testing.T) {
	const endpoint = "https://meta.fabricmc.net/v2/versions/loader/1.21.1"
	const responseBody = `[{"version":"0.16.14","stable":true}]`
	metadataTestClient(t, map[string]string{endpoint: responseBody})

	body, stale, err := fetchMetadataDocument(
		context.Background(),
		"relative-data-root",
		KindFabric,
		"1.21.1",
		"catalog",
		endpoint,
		metadataMaxListBytes,
	)
	if err != nil {
		t.Fatalf("fetchMetadataDocument returned cache path error: %v", err)
	}
	if stale || string(body) != responseBody {
		t.Fatalf("fetchMetadataDocument = %q, stale=%v", body, stale)
	}
}

func TestFetchMetadataDocumentIgnoresCacheWriteFailure(t *testing.T) {
	const endpoint = "https://meta.fabricmc.net/v2/versions/loader/1.21.1"
	const responseBody = `[{"version":"0.16.14","stable":true}]`
	metadataTestClient(t, map[string]string{endpoint: responseBody})

	root := t.TempDir()
	cacheRel, relErr := metadataCacheRel(KindFabric, "1.21.1", endpoint)
	if relErr != nil {
		t.Fatal(relErr)
	}
	// A directory at the cache file path makes the final rename fail while the
	// path itself is still valid and safely resolvable inside the data root.
	cachePath := filepath.Join(root, filepath.FromSlash(cacheRel))
	if mkdirErr := os.MkdirAll(cachePath, 0755); mkdirErr != nil {
		t.Fatal(mkdirErr)
	}

	body, stale, err := fetchMetadataDocument(
		context.Background(),
		root,
		KindFabric,
		"1.21.1",
		"catalog",
		endpoint,
		metadataMaxListBytes,
	)
	if err != nil {
		t.Fatalf("fetchMetadataDocument returned cache write error: %v", err)
	}
	if stale || string(body) != responseBody {
		t.Fatalf("fetchMetadataDocument = %q, stale=%v", body, stale)
	}
}

func TestParseForgePromotionsSupportsNestedAndLegacyDocuments(t *testing.T) {
	nested, err := parseForgePromotions([]byte(`{"homepage":"https://example.invalid","promos":{"26.3-recommended":"66.1.0","26.3-latest":"66.1.5"}}`))
	if err != nil || nested["26.3-recommended"] != "66.1.0" || nested["26.3-latest"] != "66.1.5" {
		t.Fatalf("nested promotions = %#v, %v", nested, err)
	}

	legacy, err := parseForgePromotions([]byte(`{"26.3-recommended":"66.1.0"}`))
	if err != nil || legacy["26.3-recommended"] != "66.1.0" {
		t.Fatalf("legacy promotions = %#v, %v", legacy, err)
	}

	if _, err := parseForgePromotions([]byte(`{"promos":"invalid"}`)); err == nil {
		t.Fatal("accepted invalid promotions document")
	}
}

func TestListForgeVersionsUsesNestedPromotions(t *testing.T) {
	const promotionsURL = "https://files.minecraftforge.net/net/minecraftforge/forge/promotions_slim.json"
	const metadataURL = "https://maven.minecraftforge.net/net/minecraftforge/forge/maven-metadata.xml"
	metadataTestClient(t, map[string]string{
		promotionsURL: `{"homepage":"https://example.invalid","promos":{"26.3-recommended":"66.1.0","26.3-latest":"66.1.5"}}`,
		metadataURL: `<metadata><versioning><versions>` +
			`<version>26.3-66.0.0</version>` +
			`<version>26.3-66.1.5</version>` +
			`<version>26.3-66.1.0</version>` +
			`<version>1.21.1-52.0.0</version>` +
			`</versions></versioning></metadata>`,
	})

	versions, stale, err := listForgeVersions(context.Background(), t.TempDir(), "26.3")
	if err != nil {
		t.Fatal(err)
	}
	if stale || len(versions) != 3 {
		t.Fatalf("versions = %+v, stale=%v", versions, stale)
	}
	if versions[0].Version != "66.1.5" || versions[1].Version != "66.1.0" || versions[2].Version != "66.0.0" {
		t.Fatalf("unexpected order: %+v", versions)
	}
	if !versions[1].Recommended || versions[0].Recommended {
		t.Fatalf("recommended version = %+v", versions)
	}
}

func TestListNeoForgeVersionsKeepsModernInstallerCoordinate(t *testing.T) {
	const metadataURL = "https://maven.neoforged.net/releases/net/neoforged/neoforge/maven-metadata.xml"
	metadataTestClient(t, map[string]string{
		metadataURL: `<metadata><versioning><versions>` +
			`<version>26.3.0.99-beta</version>` +
			`<version>26.3.0.100</version>` +
			`<version>26.3.0.101</version>` +
			`<version>26.1.2.126</version>` +
			`</versions></versioning></metadata>`,
	})

	versions, stale, err := listNeoForgeVersions(context.Background(), t.TempDir(), "26.3")
	if err != nil {
		t.Fatal(err)
	}
	if stale || len(versions) != 3 {
		t.Fatalf("versions = %+v, stale=%v", versions, stale)
	}
	if versions[0].Version != "26.3.0.101" || versions[1].Version != "26.3.0.100" || versions[2].Version != "26.3.0.99-beta" {
		t.Fatalf("unexpected versions: %+v", versions)
	}
	if !versions[0].Recommended {
		t.Fatalf("recommended version = %+v", versions)
	}
}
