package version

// 本文件覆盖 P3-13 的「版本元数据按需获取」与「列表徽标不再误导」。
//
// 测试约束：
//   - 全程不访问网络：元数据下载与清单请求都由假 http.RoundTripper 承接，
//     清单客户端被替换为「一旦被调用就记录并报错」的传输层；
//   - 全程使用 t.TempDir()，不触碰用户目录；
//   - 不启动任何外部进程。

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"example.com/ninevoxel/launcher/internal/artifact"
)

// fetchTestVersionID 是测试使用的版本标识。
const fetchTestVersionID = "1.20.4"

// fetchTestDocument 返回一份结构完整、可被安装视图解析的版本 JSON。
func fetchTestDocument(versionID string) []byte {
	return []byte(`{"id":"` + versionID + `","type":"release",` +
		`"mainClass":"net.minecraft.client.main.Main","assets":"1.20",` +
		`"javaVersion":{"majorVersion":17},` +
		`"minecraftArguments":"--username ${auth_player_name} --gameDir ${game_directory}"}`)
}

// fetchTestSHA1 返回字节内容的 SHA-1 十六进制摘要。
func fetchTestSHA1(raw []byte) string {
	sum := sha1.Sum(raw)
	return hex.EncodeToString(sum[:])
}

// fetchTestEnv 是一次测试的运行环境。
type fetchTestEnv struct {
	// root 为注入的临时数据根（也是兼容性测试里的游戏目录）。
	root string

	// calls 统计元数据下载请求次数。
	calls int32

	// manifestCalls 统计清单请求次数（正常情况下必须为 0）。
	manifestCalls int32
}

// setupFetchEnv 注入数据根、重置包级状态，并替换两个 HTTP 客户端工厂。
func setupFetchEnv(t *testing.T, respond func(*http.Request) (*http.Response, error)) *fetchTestEnv {
	t.Helper()

	root := t.TempDir()
	env := &fetchTestEnv{root: root}

	SetDataRootProvider(func() (string, error) { return root, nil })
	stateMu.Lock()
	currentSnapshot = nil
	lastManifestErrorCode = ""
	stateMu.Unlock()
	clearEnsureCallsForTest()

	previousEnsure := ensureHTTPClientFactory
	previousManifest := newManifestHTTPClient

	ensureHTTPClientFactory = func() *http.Client {
		return &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			atomic.AddInt32(&env.calls, 1)
			return respond(request)
		})}
	}
	// 清单客户端在「按需获取元数据」路径里绝不应被使用：一旦命中就报错，
	// 避免测试悄悄走真实网络。
	newManifestHTTPClient = func() *http.Client {
		return &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			atomic.AddInt32(&env.manifestCalls, 1)
			return nil, fmt.Errorf("测试中不应发起清单请求")
		})}
	}

	t.Cleanup(func() {
		ensureHTTPClientFactory = previousEnsure
		newManifestHTTPClient = previousManifest
		SetDataRootProvider(nil)
		stateMu.Lock()
		currentSnapshot = nil
		lastManifestErrorCode = ""
		stateMu.Unlock()
		clearEnsureCallsForTest()
	})
	return env
}

// clearEnsureCallsForTest 清空并发去重表，避免测试互相影响。
func clearEnsureCallsForTest() {
	ensureCalls.Range(func(key, _ any) bool {
		ensureCalls.Delete(key)
		return true
	})
}

// manifestEntryForFetch 构造一条指定哈希与地址的清单条目。
func manifestEntryForFetch(versionID, sha1Value, rawURL string) map[string]any {
	return map[string]any{
		"id":          versionID,
		"type":        "release",
		"url":         rawURL,
		"releaseTime": "2024-01-01T00:00:00+00:00",
		"sha1":        sha1Value,
		"size":        0,
	}
}

// installManifestSnapshot 把条目写入内存清单（不写盘、不联网）。
func installManifestSnapshot(t *testing.T, entries []map[string]any) {
	t.Helper()
	if len(entries) == 0 {
		return
	}

	firstID, _ := entries[0]["id"].(string)
	raw := buildManifestJSON(t, firstID, firstID, entries)
	snapshot, appErr := parseManifest(raw)
	if appErr != nil {
		t.Fatalf("构造清单失败：%v", appErr.Message)
	}
	snapshot.fetchedAtMs = nowFunc().UnixMilli()

	stateMu.Lock()
	currentSnapshot = snapshot
	stateMu.Unlock()
}

// localVersionJSONPath 返回版本 JSON 在数据根下的绝对路径。
func localVersionJSONPath(t *testing.T, root, versionID string) string {
	t.Helper()

	relPath, relErr := artifact.VersionRelPath(versionID, versionID+".json")
	if relErr != nil {
		t.Fatalf("生成版本 JSON 相对路径失败：%v", relErr.Message)
	}
	return filepath.Join(root, filepath.FromSlash(relPath))
}

// writeLocalVersionJSON 在数据根下写入版本 JSON。
func writeLocalVersionJSON(t *testing.T, root, versionID string, raw []byte) string {
	t.Helper()

	absolute := localVersionJSONPath(t, root, versionID)
	if err := os.MkdirAll(filepath.Dir(absolute), 0o755); err != nil {
		t.Fatalf("创建版本目录失败：%v", err)
	}
	if err := os.WriteFile(absolute, raw, 0o644); err != nil {
		t.Fatalf("写入版本 JSON 失败：%v", err)
	}
	return absolute
}

// TestEnsureVersionMetadataSkipsNetworkWhenLocalFileExists 验证本地文件已就位时零网络请求。
func TestEnsureVersionMetadataSkipsNetworkWhenLocalFileExists(t *testing.T) {
	env := setupFetchEnv(t, func(*http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("本地已就位时不应发起任何请求")
	})
	writeLocalVersionJSON(t, env.root, fetchTestVersionID, fetchTestDocument(fetchTestVersionID))

	if err := ensureVersionMetadata(env.root, fetchTestVersionID); err != nil {
		t.Fatalf("本地已就位时不应报错：%v", err.Message)
	}
	if got := atomic.LoadInt32(&env.calls); got != 0 {
		t.Fatalf("本地已就位时请求次数应为 0，实际 %d", got)
	}
	if got := atomic.LoadInt32(&env.manifestCalls); got != 0 {
		t.Fatalf("不应请求清单，实际 %d", got)
	}
}

// TestEnsureVersionMetadataRejectsMissingManifest 验证清单缓存不可用时直接失败且可重试。
func TestEnsureVersionMetadataRejectsMissingManifest(t *testing.T) {
	env := setupFetchEnv(t, func(*http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("没有清单时不应发起元数据请求")
	})

	err := ensureVersionMetadata(env.root, fetchTestVersionID)
	requireCode(t, err, ErrorCodeManifestFetch)
	if !err.Retryable {
		t.Fatalf("清单缺失应标记为可重试")
	}
	if !strings.Contains(err.Message, "刷新版本清单") {
		t.Fatalf("提示语应引导刷新清单，实际 %q", err.Message)
	}
	if got := atomic.LoadInt32(&env.calls); got != 0 {
		t.Fatalf("不应发起元数据请求，实际 %d", got)
	}
	if got := atomic.LoadInt32(&env.manifestCalls); got != 0 {
		t.Fatalf("不应发起清单请求，实际 %d", got)
	}
}

// TestEnsureVersionMetadataRejectsUnknownVersion 验证清单里查不到该版本时不联网。
func TestEnsureVersionMetadataRejectsUnknownVersion(t *testing.T) {
	env := setupFetchEnv(t, func(*http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("未知版本不应发起请求")
	})
	installManifestSnapshot(t, []map[string]any{
		manifestEntryForFetch("1.21.4", strings.Repeat("a", 40), "https://piston-meta.mojang.com/v1/packages/a/1.21.4.json"),
	})

	err := ensureVersionMetadata(env.root, fetchTestVersionID)
	requireCode(t, err, ErrorCodeManifestFetch)
	if !err.Retryable {
		t.Fatalf("清单缺条目应标记为可重试")
	}
	if got := atomic.LoadInt32(&env.calls); got != 0 {
		t.Fatalf("不应发起元数据请求，实际 %d", got)
	}
}

// TestEnsureVersionMetadataDownloadsAndWrites 验证正常路径：下载、校验、原子落盘、再读回。
func TestEnsureVersionMetadataDownloadsAndWrites(t *testing.T) {
	body := fetchTestDocument(fetchTestVersionID)
	env := setupFetchEnv(t, func(request *http.Request) (*http.Response, error) {
		return jsonResponse(request, body), nil
	})
	installManifestSnapshot(t, []map[string]any{
		manifestEntryForFetch(
			fetchTestVersionID,
			fetchTestSHA1(body),
			"https://piston-meta.mojang.com/v1/packages/"+fetchTestVersionID+"/"+fetchTestVersionID+".json",
		),
	})

	if err := ensureVersionMetadata(env.root, fetchTestVersionID); err != nil {
		t.Fatalf("按需获取元数据失败：%v", err.Message)
	}
	if got := atomic.LoadInt32(&env.calls); got != 1 {
		t.Fatalf("应恰好发起 1 次请求，实际 %d", got)
	}

	absolute := localVersionJSONPath(t, env.root, fetchTestVersionID)
	stored, readErr := os.ReadFile(absolute)
	if readErr != nil {
		t.Fatalf("版本 JSON 未落盘：%v", readErr)
	}
	if !bytes.Equal(stored, body) {
		t.Fatalf("落盘内容与响应体不一致")
	}

	// 再次调用：本地已就位，不再联网。
	if err := ensureVersionMetadata(env.root, fetchTestVersionID); err != nil {
		t.Fatalf("第二次调用不应报错：%v", err.Message)
	}
	if got := atomic.LoadInt32(&env.calls); got != 1 {
		t.Fatalf("本地已就位后请求次数不应增加，实际 %d", got)
	}
}

// TestEnsureVersionMetadataRejectsChecksumMismatch 验证哈希不一致时拒绝写盘。
func TestEnsureVersionMetadataRejectsChecksumMismatch(t *testing.T) {
	body := fetchTestDocument(fetchTestVersionID)
	env := setupFetchEnv(t, func(request *http.Request) (*http.Response, error) {
		// 返回的内容与清单声明的哈希不一致（构造一份不同内容）。
		return jsonResponse(request, append([]byte(" "), body...)), nil
	})
	installManifestSnapshot(t, []map[string]any{
		manifestEntryForFetch(
			fetchTestVersionID,
			fetchTestSHA1(body),
			"https://piston-meta.mojang.com/v1/packages/"+fetchTestVersionID+".json",
		),
	})

	err := ensureVersionMetadata(env.root, fetchTestVersionID)
	requireCode(t, err, ErrorCodeVersionJSON)
	if err.Retryable {
		t.Fatalf("哈希不一致不应标记为可重试")
	}
	absolute := localVersionJSONPath(t, env.root, fetchTestVersionID)
	if _, statErr := os.Stat(absolute); !os.IsNotExist(statErr) {
		t.Fatalf("哈希不一致时不得写盘，实际文件存在")
	}
	if got := atomic.LoadInt32(&env.calls); got != 1 {
		t.Fatalf("应发起 1 次请求，实际 %d", got)
	}
}

// TestEnsureVersionMetadataRejectsOversizedResponse 验证响应体超过上限时拒绝。
func TestEnsureVersionMetadataRejectsOversizedResponse(t *testing.T) {
	huge := bytes.Repeat([]byte("x"), installMaxVersionJSONBytes+16)
	env := setupFetchEnv(t, func(request *http.Request) (*http.Response, error) {
		return jsonResponse(request, huge), nil
	})
	installManifestSnapshot(t, []map[string]any{
		manifestEntryForFetch(
			fetchTestVersionID,
			fetchTestSHA1(huge),
			"https://piston-meta.mojang.com/v1/packages/"+fetchTestVersionID+".json",
		),
	})

	err := ensureVersionMetadata(env.root, fetchTestVersionID)
	requireCode(t, err, ErrorCodeVersionJSON)
	if !strings.Contains(err.Message, "超出允许的大小") {
		t.Fatalf("提示语应说明超出大小上限，实际 %q", err.Message)
	}
	absolute := localVersionJSONPath(t, env.root, fetchTestVersionID)
	if _, statErr := os.Stat(absolute); !os.IsNotExist(statErr) {
		t.Fatalf("超限响应不得写盘")
	}
}

// TestEnsureVersionMetadataRejectsNotFound 验证 404 的提示与可重试标记。
func TestEnsureVersionMetadataRejectsNotFound(t *testing.T) {
	env := setupFetchEnv(t, func(request *http.Request) (*http.Response, error) {
		return errorResponse(request, http.StatusNotFound), nil
	})
	installManifestSnapshot(t, []map[string]any{
		manifestEntryForFetch(
			fetchTestVersionID,
			strings.Repeat("a", 40),
			"https://piston-meta.mojang.com/v1/packages/"+fetchTestVersionID+".json",
		),
	})

	err := ensureVersionMetadata(env.root, fetchTestVersionID)
	requireCode(t, err, ErrorCodeVersionJSON)
	if err.Retryable {
		t.Fatalf("404 不应标记为可重试")
	}
}

// TestDownloadVersionJSONRejectsURLOutsideWhitelist 验证白名单外的地址在请求前被拒绝。
//
// 说明：清单解析阶段（parseManifest → normalizeEntry）已对条目 URL 做同样的校验，
// 因此这条测试直接打下载层，覆盖「清单缓存被绕过」时的第二道防线。
func TestDownloadVersionJSONRejectsURLOutsideWhitelist(t *testing.T) {
	env := setupFetchEnv(t, func(*http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("非法地址不应发起请求")
	})

	_, err := downloadVersionJSON(context.Background(), manifestVersion{
		id:   fetchTestVersionID,
		url:  "https://evil.example.com/v1/packages/" + fetchTestVersionID + ".json",
		sha1: strings.Repeat("a", 40),
	})
	requireCode(t, err, ErrorCodeURLInvalid)
	if got := atomic.LoadInt32(&env.calls); got != 0 {
		t.Fatalf("非法地址不应发起请求，实际 %d", got)
	}
}

// TestEnsureVersionMetadataRejectsNetworkFailure 验证网络失败可重试。
func TestEnsureVersionMetadataRejectsNetworkFailure(t *testing.T) {
	env := setupFetchEnv(t, func(*http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("dial tcp: connection refused")
	})
	installManifestSnapshot(t, []map[string]any{
		manifestEntryForFetch(
			fetchTestVersionID,
			strings.Repeat("a", 40),
			"https://piston-meta.mojang.com/v1/packages/"+fetchTestVersionID+".json",
		),
	})

	err := ensureVersionMetadata(env.root, fetchTestVersionID)
	requireCode(t, err, ErrorCodeVersionJSON)
	if !err.Retryable {
		t.Fatalf("网络失败应标记为可重试")
	}
}

// TestEnsureVersionMetadataRejectsEmptyRoot 验证缺少根目录时立即失败。
func TestEnsureVersionMetadataRejectsEmptyRoot(t *testing.T) {
	setupFetchEnv(t, func(*http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("缺少根目录时不应发起请求")
	})

	err := ensureVersionMetadata("   ", fetchTestVersionID)
	requireCode(t, err, ErrorCodeVersionJSON)
}

// TestEnsureVersionMetadataDeduplicatesConcurrentCalls 验证同一版本的并发调用只下载一次。
func TestEnsureVersionMetadataDeduplicatesConcurrentCalls(t *testing.T) {
	body := fetchTestDocument(fetchTestVersionID)
	var started sync.WaitGroup
	started.Add(1)
	env := setupFetchEnv(t, func(request *http.Request) (*http.Response, error) {
		started.Done()
		// 让并发调用者有足够时间进入等待分支。
		time.Sleep(80 * time.Millisecond)
		return jsonResponse(request, body), nil
	})
	installManifestSnapshot(t, []map[string]any{
		manifestEntryForFetch(
			fetchTestVersionID,
			fetchTestSHA1(body),
			"https://piston-meta.mojang.com/v1/packages/"+fetchTestVersionID+".json",
		),
	})

	const callers = 8
	errs := make([]*AppError, callers)
	var wait sync.WaitGroup
	wait.Add(callers)
	for index := 0; index < callers; index++ {
		go func(slot int) {
			defer wait.Done()
			errs[slot] = ensureVersionMetadata(env.root, fetchTestVersionID)
		}(index)
	}

	started.Wait()
	wait.Wait()

	for slot, err := range errs {
		if err != nil {
			t.Fatalf("第 %d 个并发调用失败：%v", slot, err.Message)
		}
	}
	if got := atomic.LoadInt32(&env.calls); got != 1 {
		t.Fatalf("并发调用应合并为 1 次请求，实际 %d", got)
	}
}

// TestListVersionsDoesNotFetchVersionMetadata 锁定「列表页不拉版本 JSON」。
func TestListVersionsDoesNotFetchVersionMetadata(t *testing.T) {
	env := setupFetchEnv(t, func(request *http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("列表页不应发起元数据请求")
	})
	installManifestSnapshot(t, []map[string]any{
		manifestEntryForFetch("1.21.4", strings.Repeat("a", 40), "https://piston-meta.mojang.com/v1/packages/a.json"),
		manifestEntryForFetch("1.20.4", strings.Repeat("b", 40), "https://piston-meta.mojang.com/v1/packages/b.json"),
	})

	result, appErr := ListVersions(VersionListRequest{})
	if appErr != nil {
		t.Fatalf("列表查询失败：%v", appErr.Message)
	}
	if len(result.Items) != 2 {
		t.Fatalf("应返回 2 条版本，实际 %d", len(result.Items))
	}
	if got := atomic.LoadInt32(&env.calls); got != 0 {
		t.Fatalf("列表页不得拉取版本 JSON，实际 %d 次", got)
	}
	if got := atomic.LoadInt32(&env.manifestCalls); got != 0 {
		t.Fatalf("清单新鲜时不得重新拉清单，实际 %d 次", got)
	}
	if _, statErr := os.Stat(filepath.Join(env.root, artifact.DirVersions)); !os.IsNotExist(statErr) {
		t.Fatalf("列表页不得创建 versions 目录")
	}

	// 列表徽标：未评估，四层一律 false，不得再假装「元数据支持」。
	for _, item := range result.Items {
		if item.Compat.Evaluated {
			t.Fatalf("列表页兼容性应为未评估")
		}
		layer := item.Compat
		if layer.MetadataSupported || layer.InstallSupported || layer.LaunchSupported || layer.LoaderSupported {
			t.Fatalf("未评估时四层必须全为 false，实际 %+v", layer)
		}
	}
}

// TestBuildCompatReportMarksEvaluated 验证兼容性报告在元数据就位后标记为已评估。
func TestBuildCompatReportMarksEvaluated(t *testing.T) {
	body := fetchTestDocument(fetchTestVersionID)
	env := setupFetchEnv(t, func(request *http.Request) (*http.Response, error) {
		return jsonResponse(request, body), nil
	})
	installManifestSnapshot(t, []map[string]any{
		manifestEntryForFetch(
			fetchTestVersionID,
			fetchTestSHA1(body),
			"https://piston-meta.mojang.com/v1/packages/"+fetchTestVersionID+".json",
		),
	})

	view, appErr := buildCompatReport(CompatReportRequest{
		VersionID:     fetchTestVersionID,
		GameDirectory: env.root,
	})
	if appErr != nil {
		t.Fatalf("兼容性报告失败：%v", appErr.Message)
	}
	if !view.Evaluated {
		t.Fatalf("报告应标记为已评估")
	}
	if !view.Layers.MetadataSupported {
		t.Fatalf("元数据可读时 metadataSupported 应为 true")
	}
	if got := atomic.LoadInt32(&env.calls); got != 1 {
		t.Fatalf("应恰好下载 1 次元数据，实际 %d", got)
	}
}

// TestGetVersionDetailEnsuresMetadata 验证详情查询会先把元数据拉取到位。
func TestGetVersionDetailEnsuresMetadata(t *testing.T) {
	body := fetchTestDocument(fetchTestVersionID)
	env := setupFetchEnv(t, func(request *http.Request) (*http.Response, error) {
		return jsonResponse(request, body), nil
	})
	installManifestSnapshot(t, []map[string]any{
		manifestEntryForFetch(
			fetchTestVersionID,
			fetchTestSHA1(body),
			"https://piston-meta.mojang.com/v1/packages/"+fetchTestVersionID+".json",
		),
	})

	detail, appErr := getVersionDetail(VersionDetailRequest{VersionID: fetchTestVersionID})
	if appErr != nil {
		t.Fatalf("详情查询失败：%v", appErr.Message)
	}
	if detail.ID != fetchTestVersionID {
		t.Fatalf("详情 ID 不符：%q", detail.ID)
	}
	if got := atomic.LoadInt32(&env.calls); got != 1 {
		t.Fatalf("应恰好下载 1 次元数据，实际 %d", got)
	}
}
