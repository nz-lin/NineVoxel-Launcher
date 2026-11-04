package core

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// ===== 版本索引 =====

// TestReadIndexMissingReturnsEmpty 验证索引文件不存在时返回空索引。
//
// 首次启动、尚未安装任何版本时，「没有索引」与「索引为空」必须等价，
// 否则实例列表会因为索引缺失而整体失败。
func TestReadIndexMissingReturnsEmpty(t *testing.T) {
	root := t.TempDir()
	index, err := readIndex(root)
	if err != nil {
		t.Fatalf("缺失索引不应返回错误: %v", err)
	}
	if len(index.Identities) != 0 {
		t.Fatalf("期望空索引，实际 %d 条", len(index.Identities))
	}
}

// TestIndexRoundTrip 验证索引写入后可正确读回。
func TestIndexRoundTrip(t *testing.T) {
	root := t.TempDir()
	index := newIndex()
	index.Identities["forge:26.3:66.1.0"] = IndexEntry{
		VersionID:         "26.3-forge-66.1.0",
		RecipeFingerprint: strings.Repeat("a", 64),
		ManifestPath:      "versions/26.3-forge-66.1.0/.nv-core-manifest.json",
		InstalledAtMs:     1793772823963,
	}
	if err := writeIndexAtomic(root, index); err != nil {
		t.Fatalf("写入索引失败: %v", err)
	}

	readBack, err := readIndex(root)
	if err != nil {
		t.Fatalf("读取索引失败: %v", err)
	}
	entry, ok := readBack.Lookup("forge:26.3:66.1.0")
	if !ok {
		t.Fatal("索引条目丢失")
	}
	if entry.VersionID != "26.3-forge-66.1.0" {
		t.Fatalf("版本标识期望 26.3-forge-66.1.0，实际 %q", entry.VersionID)
	}
	if len(readBack.AllIdentities()) != 1 {
		t.Fatalf("身份数量期望 1，实际 %d", len(readBack.AllIdentities()))
	}
}

// TestReadIndexCorruptedReturnsError 验证损坏的索引返回错误而不是空索引。
//
// 返回空索引会让调用方误以为「没装过」，从而重复安装；明确报错才能触发
// 上层「扫描清单重建索引」的恢复路径。
func TestReadIndexCorruptedReturnsError(t *testing.T) {
	root := t.TempDir()
	absolute := filepath.Join(root, filepath.FromSlash(identityIndexPath()))
	if err := os.MkdirAll(filepath.Dir(absolute), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(absolute, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readIndex(root); err == nil {
		t.Fatal("损坏的索引期望返回错误")
	}
}

// TestReadIndexRejectsUnsupportedSchema 验证不支持的索引版本被拒绝。
func TestReadIndexRejectsUnsupportedSchema(t *testing.T) {
	root := t.TempDir()
	absolute := filepath.Join(root, filepath.FromSlash(identityIndexPath()))
	if err := os.MkdirAll(filepath.Dir(absolute), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"schemaVersion":99,"identities":{}}`
	if err := os.WriteFile(absolute, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readIndex(root); err == nil {
		t.Fatal("不支持的索引版本期望返回错误")
	}
}

// TestWriteIndexIsAtomic 验证写入不会残留临时文件。
func TestWriteIndexIsAtomic(t *testing.T) {
	root := t.TempDir()
	if err := writeIndexAtomic(root, newIndex()); err != nil {
		t.Fatalf("写入索引失败: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(root, indexDirName))
	if err != nil {
		t.Fatalf("读取索引目录失败: %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".versions-") {
			t.Fatalf("残留临时文件: %s", entry.Name())
		}
	}
}

// ===== 锁分层 =====

// timeAfter 返回一个短超时的通道，用于断言「锁未被取得」。
//
// 用固定短超时而不是 sleep：断言的目标是「在合理时间内没有发生」，
// 超时本身就是期望结果。
func timeAfter() <-chan time.Time {
	return time.After(100 * time.Millisecond)
}

// TestLocksGlobalReadAllowsConcurrency 验证全局读锁可被多方同时持有。
//
// 这是「不同身份的安装可以并行」的前提：若安装都持全局写锁，
// Identity 层就失去意义。
func TestLocksGlobalReadAllowsConcurrency(t *testing.T) {
	locks := NewLocks()
	releaseFirst := locks.GlobalRead()
	releaseSecond := locks.GlobalRead()
	releaseSecond()
	releaseFirst()
}

// TestLocksGlobalWriteExcludesReaders 验证全局写锁与读锁互斥。
func TestLocksGlobalWriteExcludesReaders(t *testing.T) {
	locks := NewLocks()

	// 先持读锁，再在另一个 goroutine 里尝试写：写入必须等待读释放。
	releaseRead := locks.GlobalRead()
	writeStarted := make(chan struct{})
	writeAcquired := make(chan struct{})

	go func() {
		close(writeStarted)
		releaseWrite := locks.GlobalWrite()
		close(writeAcquired)
		releaseWrite()
	}()

	<-writeStarted
	select {
	case <-writeAcquired:
		t.Fatal("读锁未释放时不应取得写锁")
	default:
	}
	releaseRead()
	<-writeAcquired
}

// TestLocksKeyedIndependent 验证不同键的锁互不阻塞。
func TestLocksKeyedIndependent(t *testing.T) {
	locks := NewLocks()
	releaseA := locks.Identity("forge:26.3:66.1.0")

	acquired := make(chan struct{})
	go func() {
		releaseB := locks.Identity("fabric:26.3:0.16.14")
		close(acquired)
		releaseB()
	}()

	select {
	case <-acquired:
	case <-timeAfter():
		t.Fatal("不同身份的锁不应互相阻塞")
	}
	releaseA()
}

// TestLocksSameKeySerialized 验证同键的锁串行化。
func TestLocksSameKeySerialized(t *testing.T) {
	locks := NewLocks()
	release := locks.Identity("forge:26.3:66.1.0")

	acquired := make(chan struct{})
	go func() {
		releaseSecond := locks.Identity("forge:26.3:66.1.0")
		close(acquired)
		releaseSecond()
	}()

	select {
	case <-acquired:
		t.Fatal("同键的锁不应并发取得")
	case <-timeAfter():
	}
	release()
	<-acquired
}

// TestLocksConcurrentSameKey 验证并发争用时只有一个持有者。
func TestLocksConcurrentSameKey(t *testing.T) {
	locks := NewLocks()
	var (
		mu      sync.Mutex
		current int
		peak    int
		wg      sync.WaitGroup
	)

	for index := 0; index < 8; index++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release := locks.Version("26.3")
			defer release()

			mu.Lock()
			current++
			if current > peak {
				peak = current
			}
			mu.Unlock()

			mu.Lock()
			current--
			mu.Unlock()
		}()
	}
	wg.Wait()

	if peak > 1 {
		t.Fatalf("同键锁峰值并发期望 1，实际 %d", peak)
	}
}
