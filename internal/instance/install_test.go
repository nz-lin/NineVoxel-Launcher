package instance

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"example.com/ninevoxel/launcher/internal/artifact"
	"example.com/ninevoxel/launcher/internal/download"
	"example.com/ninevoxel/launcher/internal/version"
)

func fixtureHash(body []byte) string { sum := sha1.Sum(body); return hex.EncodeToString(sum[:]) }
func fixtureWrite(t *testing.T, path string, body []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0644); err != nil {
		t.Fatal(err)
	}
}
func nativeArchive(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var data bytes.Buffer
	writer := zip.NewWriter(&data)
	for name, body := range entries {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func TestInstallAssemblesClientLibrariesAssetsAndNatives(t *testing.T) {
	s := testService(t)
	item, err := s.CreateInstance(testConfig("offline install"))
	if err != nil {
		t.Fatal(err)
	}
	asset := []byte("sound fixture")
	hash := fixtureHash(asset)
	index := []byte(fmt.Sprintf(`{"objects":{"sound/example.ogg":{"hash":%q,"size":%d}}}`, hash, len(asset)))
	jar := []byte("client jar")
	library := []byte("library jar")
	natives := nativeArchive(t, map[string]string{"native.dll": "native", "META-INF/signature": "excluded"})
	files := []struct {
		body    []byte
		target  string
		extract bool
	}{
		{jar, "versions/1.21.4/1.21.4.jar", false}, {library, "libraries/org/example/lib/1/lib-1.jar", false},
		{natives, "versions/1.21.4/natives", true}, {index, "assets/indexes/test-index.json", false},
	}
	prepared := version.Installation{Plan: download.TransferPlan{VersionID: item.MCVersion, PlanHash: "fixture"}, AssetIndexID: "test-index", JavaMajor: 21}
	for _, file := range files {
		rel, appErr := artifact.AssetObjectRelPath(fixtureHash(file.body))
		if appErr != nil {
			t.Fatal(appErr)
		}
		if bytes.Equal(file.body, index) {
			rel = "assets/indexes/test-index.json"
		}
		if file.extract {
			// natives 压缩包按 Maven 布局下载，再解压到版本目录。
			rel = "libraries/org/example/native/1.0/native-1.0-natives-windows.jar"
		}
		fixtureWrite(t, filepath.Join(s.root, filepath.FromSlash(rel)), file.body)
		// 只有 natives 需要安装步骤：下载的是压缩包，必须解压。
		// 其余制品下载后已在 classpath 引用的位置，不再复制到实例目录。
		if file.extract {
			prepared.Files = append(prepared.Files, version.InstallFile{Source: rel, Target: file.target, SHA1: fixtureHash(file.body), Size: int64(len(file.body)), Extract: true})
		}
	}
	metadata := []byte(`{"id":"1.21.4","mainClass":"net.minecraft.client.main.Main"}`)
	preparedFiles := []struct {
		body []byte
		rel  string
	}{
		{jar, "versions/1.21.4/1.21.4.jar"},
		{library, "libraries/org/example/lib/1/lib-1.jar"},
		{metadata, "versions/1.21.4/1.21.4.json"},
	}
	for _, file := range preparedFiles {
		fixtureWrite(t, filepath.Join(s.root, filepath.FromSlash(file.rel)), file.body)
	}
	phases := 0
	fakeRun := func(ctx context.Context, id string, plan download.TransferPlan, base, span float64) error {
		phases++
		for _, entry := range plan.Items {
			if entry.ExpectedSHA1 != hash {
				t.Fatalf("unexpected asset %+v", entry)
			}
			fixtureWrite(t, filepath.Join(s.root, filepath.FromSlash(entry.TargetRelPath)), asset)
		}
		return nil
	}
	s.installer = func(ctx context.Context, item Instance) error {
		_, installErr := s.installPrepared(ctx, item, prepared, fakeRun)
		return installErr
	}
	if _, err := s.InstallInstance(IDRequest{InstanceID: item.ID}); err != nil {
		t.Fatal(err)
	}
	s.workers.Wait()
	list, err := s.ListInstances()
	if err != nil {
		t.Fatal(err)
	}
	if list.Items[0].InstallationState != "installed" {
		t.Fatalf("install failed: %+v", list.Items[0])
	}
	if phases != 2 {
		t.Fatalf("expected artifacts and asset phases; got %d", phases)
	}
	// 资源全部落在共享核心，实例目录不再保存任何游戏资源。
	for relative, want := range map[string]string{
		"versions/1.21.4/1.21.4.jar": string(jar), "libraries/org/example/lib/1/lib-1.jar": string(library),
		"versions/1.21.4/natives/native.dll": "native", "assets/indexes/test-index.json": string(index),
		"assets/objects/" + hash[:2] + "/" + hash: string(asset), "versions/1.21.4/1.21.4.json": string(metadata),
	} {
		actual, err := os.ReadFile(filepath.Join(s.root, filepath.FromSlash(relative)))
		if err != nil || string(actual) != want {
			t.Fatalf("core layout %s: %q %v", relative, actual, err)
		}
	}
	if _, err := os.Stat(filepath.Join(s.root, "versions", "1.21.4", "natives", "META-INF", "signature")); !os.IsNotExist(err) {
		t.Fatal("native metadata was not excluded")
	}
	game, err := s.gameDir(item)
	if err != nil {
		t.Fatal(err)
	}
	for _, relative := range []string{"libraries", "assets", "versions"} {
		if _, statErr := os.Stat(filepath.Join(game, relative)); !os.IsNotExist(statErr) {
			t.Fatalf("实例目录不应包含 %s：%v", relative, statErr)
		}
	}
}

func TestNativeExtractionRejectsTraversalBeforeWriting(t *testing.T) {
	root := t.TempDir()
	archive := filepath.Join(root, "native.zip")
	fixtureWrite(t, archive, nativeArchive(t, map[string]string{"../escape.dll": "bad", "safe.dll": "must not be written"}))
	if err := extractNatives(context.Background(), archive, filepath.Join(root, "natives"), nil); err == nil {
		t.Fatal("accepted traversal")
	}
	if _, err := os.Stat(filepath.Join(root, "natives", "safe.dll")); !os.IsNotExist(err) {
		t.Fatal("wrote files before validating archive")
	}
}

func TestCopyCancellationPreservesExistingFile(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	target := filepath.Join(root, "target")
	fixtureWrite(t, source, []byte("replacement"))
	fixtureWrite(t, target, []byte("original"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := copyFile(ctx, source, target); err == nil {
		t.Fatal("ignored cancellation")
	}
	body, err := os.ReadFile(target)
	if err != nil || string(body) != "original" {
		t.Fatal("existing file was changed")
	}
}
