package instance

import (
	"crypto/sha1"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

// ===== 空间回收（硬链接去重） =====

// TestDedupStorageLinksDuplicateAssets 验证多个实例中相同的资产对象被合并为硬链接。
//
// 这是用户实际遇到的场景：5 个实例只有加载器不同，`assets` 却各存一份 463MB。
func TestDedupStorageLinksDuplicateAssets(t *testing.T) {
	s := testService(t)

	asset := "sound fixture"
	hash := sha1Hex(asset)
	// 共享缓存中的对象。
	writeFixture(t, filepath.Join(s.root, "artifacts", "objects", hash[:2], hash), asset)

	// 两个实例各自保存了一份完整副本（模拟改造前安装的实例）。
	first := filepath.Join(s.root, "instances", "inst-"+repeatHex("a"), ".minecraft", "assets", "objects", hash[:2], hash)
	second := filepath.Join(s.root, "instances", "inst-"+repeatHex("b"), ".minecraft", "assets", "objects", hash[:2], hash)
	writeFixture(t, first, asset)
	writeFixture(t, second, asset)

	result, err := s.DedupInstanceStorage()
	if err != nil {
		t.Fatalf("去重失败: %v", err)
	}
	if result.LinkedFiles != 2 {
		t.Fatalf("期望合并 2 个文件，实际 %d（%+v）", result.LinkedFiles, result)
	}
	if result.SavedBytes != int64(len(asset))*2 {
		t.Fatalf("回收字节数期望 %d，实际 %d", len(asset)*2, result.SavedBytes)
	}

	// 两个实例都必须与共享缓存是同一 inode。
	cachePath := filepath.Join(s.root, "artifacts", "objects", hash[:2], hash)
	for _, path := range []string{first, second} {
		same, sameErr := sameFileContent(cachePath, path)
		if sameErr != nil || !same {
			t.Fatalf("%s 期望为硬链接: %v", path, sameErr)
		}
		content, _ := os.ReadFile(path)
		if string(content) != asset {
			t.Fatalf("去重后内容损坏: %q", content)
		}
	}
}

// TestDedupStorageIsIdempotent 验证重复调用不会重复计数或破坏文件。
func TestDedupStorageIsIdempotent(t *testing.T) {
	s := testService(t)

	asset := "shared asset"
	hash := sha1Hex(asset)
	writeFixture(t, filepath.Join(s.root, "artifacts", "objects", hash[:2], hash), asset)
	instancePath := filepath.Join(s.root, "instances", "inst-"+repeatHex("c"), ".minecraft", "assets", "objects", hash[:2], hash)
	writeFixture(t, instancePath, asset)

	first, firstErr := s.DedupInstanceStorage()
	if firstErr != nil {
		t.Fatalf("首次去重失败: %v", firstErr)
	}
	if first.LinkedFiles != 1 {
		t.Fatalf("首次期望合并 1 个文件，实际 %+v", first)
	}

	second, secondErr := s.DedupInstanceStorage()
	if secondErr != nil {
		t.Fatalf("二次去重失败: %v", secondErr)
	}
	if second.LinkedFiles != 0 || second.SavedBytes != 0 {
		t.Fatalf("二次调用不应再回收空间，实际 %+v", second)
	}
}

// TestDedupStorageSkipsFilesMissingFromCache 验证缓存中没有对应对象时保持原样。
//
// 这类文件可能是用户自己放进来的，绝不能因为「找不到同内容对象」就删除或改写。
func TestDedupStorageSkipsFilesMissingFromCache(t *testing.T) {
	s := testService(t)

	library := filepath.Join(s.root, "instances", "inst-"+repeatHex("d"), ".minecraft", "libraries", "org", "example", "lib-1.jar")
	writeFixture(t, library, "library only in instance")

	result, err := s.DedupInstanceStorage()
	if err != nil {
		t.Fatalf("去重失败: %v", err)
	}
	if result.LinkedFiles != 0 || result.SkippedFiles == 0 {
		t.Fatalf("缓存缺失时期望跳过，实际 %+v", result)
	}
	content, readErr := os.ReadFile(library)
	if readErr != nil || string(content) != "library only in instance" {
		t.Fatalf("原文件被破坏: %q %v", content, readErr)
	}
}

// TestDedupStorageIgnoresUserContent 验证 mods/saves 等用户数据不参与去重。
func TestDedupStorageIgnoresUserContent(t *testing.T) {
	s := testService(t)

	mod := "mod jar bytes"
	hash := sha1Hex(mod)
	// 即便缓存里恰好有同内容对象，mods 目录也不该被链接。
	writeFixture(t, filepath.Join(s.root, "artifacts", "objects", hash[:2], hash), mod)
	modPath := filepath.Join(s.root, "instances", "inst-"+repeatHex("e"), ".minecraft", "mods", "example.jar")
	writeFixture(t, modPath, mod)

	if _, err := s.DedupInstanceStorage(); err != nil {
		t.Fatalf("去重失败: %v", err)
	}

	same, sameErr := sameFileContent(filepath.Join(s.root, "artifacts", "objects", hash[:2], hash), modPath)
	if sameErr == nil && same {
		t.Fatal("mods 目录不应被硬链接到共享缓存")
	}
}

// TestDedupStorageLinksLibrariesByContent 验证 libraries 按内容哈希匹配后合并。
//
// libraries 的路径是 maven 坐标，文件名不含哈希，必须实际计算 SHA-1。
func TestDedupStorageLinksLibrariesByContent(t *testing.T) {
	s := testService(t)

	library := "library jar content"
	hash := sha1Hex(library)
	cachePath := filepath.Join(s.root, "artifacts", "objects", hash[:2], hash)
	writeFixture(t, cachePath, library)

	instanceLibrary := filepath.Join(s.root, "instances", "inst-"+repeatHex("f"), ".minecraft", "libraries", "org", "example", "lib", "1", "lib-1.jar")
	writeFixture(t, instanceLibrary, library)

	result, err := s.DedupInstanceStorage()
	if err != nil {
		t.Fatalf("去重失败: %v", err)
	}
	if result.LinkedFiles != 1 {
		t.Fatalf("libraries 期望合并 1 个文件，实际 %+v", result)
	}
	if same, _ := sameFileContent(cachePath, instanceLibrary); !same {
		t.Fatal("libraries 文件期望成为硬链接")
	}
}

// sha1Hex 返回字符串内容的 SHA-1 十六进制串。
func sha1Hex(content string) string {
	digest := sha1.Sum([]byte(content))
	return hex.EncodeToString(digest[:])
}

// repeatHex 生成 32 位十六进制实例标识后缀。
func repeatHex(char string) string {
	suffix := ""
	for len(suffix) < 32 {
		suffix += char
	}
	return suffix[:32]
}
