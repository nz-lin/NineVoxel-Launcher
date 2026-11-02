// 本文件是版本清单获取与缓存的单元测试。
//
// 测试约束（P3-01 卡面）：
//   - **绝不发起真实网络请求**：所有 HTTP 交互都通过注入的假 Transport 完成，
//     URL 仍是官方地址，但 Transport 不建立任何连接；
//   - 数据根用 t.TempDir() 注入，测试之间互不影响；
//   - 时钟由 nowFunc 注入，便于稳定构造「缓存过期」场景。
package version

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// roundTripFunc 把函数适配为 http.RoundTripper。
type roundTripFunc func(*http.Request) (*http.Response, error)

// RoundTrip 实现 http.RoundTripper。
func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

// setupTestDataRoot 注入临时数据根、重置包级状态，并在测试结束后还原。
func setupTestDataRoot(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	SetDataRootProvider(func() (string, error) { return dir, nil })

	stateMu.Lock()
	currentSnapshot = nil
	lastManifestErrorCode = ""
	stateMu.Unlock()

	t.Cleanup(func() {
		stateMu.Lock()
		currentSnapshot = nil
		lastManifestErrorCode = ""
		stateMu.Unlock()
		SetDataRootProvider(nil)
	})

	return dir
}

// stubTransport 替换清单 HTTP 客户端为使用指定 Transport 的客户端。
func stubTransport(t *testing.T, transport http.RoundTripper) {
	t.Helper()

	previous := newManifestHTTPClient
	newManifestHTTPClient = func() *http.Client {
		return &http.Client{
			Timeout:       manifestHTTPTimeout,
			CheckRedirect: checkManifestRedirect,
			Transport:     transport,
		}
	}
	t.Cleanup(func() {
		newManifestHTTPClient = previous
	})
}

// stubManifestResponse 让客户端始终返回同一段清单内容。
func stubManifestResponse(t *testing.T, body []byte) {
	t.Helper()

	stubTransport(t, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return jsonResponse(request, body), nil
	}))
}

// jsonResponse 构造一个 200 JSON 响应。
func jsonResponse(request *http.Request, body []byte) *http.Response {
	return &http.Response{
		Status:     "200 OK",
		StatusCode: http.StatusOK,
		Proto:      "HTTP/1.1",
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader(body)),
		Request:    request,
	}
}

// errorResponse 构造一个指定状态码的空响应。
func errorResponse(request *http.Request, statusCode int) *http.Response {
	return &http.Response{
		StatusCode: statusCode,
		Proto:      "HTTP/1.1",
		Header:     http.Header{},
		Body:       http.NoBody,
		Request:    request,
	}
}

// redirectResponse 构造一个重定向响应。
func redirectResponse(request *http.Request, target string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusFound,
		Proto:      "HTTP/1.1",
		Header:     http.Header{"Location": []string{target}},
		Body:       http.NoBody,
		Request:    request,
	}
}

// freezeClock 用可变时间替换 nowFunc，返回设置当前时间的闭包。
func freezeClock(t *testing.T, start time.Time) func(time.Time) {
	t.Helper()

	current := start
	previous := nowFunc
	nowFunc = func() time.Time { return current }
	t.Cleanup(func() {
		nowFunc = previous
	})

	return func(next time.Time) {
		current = next
	}
}

// entry 构造一条合法的清单条目。
func entry(id string, versionType string, releaseTime string) map[string]any {
	return map[string]any{
		"id":          id,
		"type":        versionType,
		"url":         "https://piston-meta.mojang.com/v1/packages/abc/" + id + ".json",
		"releaseTime": releaseTime,
		"sha1":        strings.Repeat("a", 40),
		"size":        1234,
	}
}

// buildManifestJSON 把条目序列化为清单 JSON。
func buildManifestJSON(t *testing.T, latestRelease string, latestSnapshot string, versions []map[string]any) []byte {
	t.Helper()

	document := map[string]any{
		"latest": map[string]any{
			"release":  latestRelease,
			"snapshot": latestSnapshot,
		},
		"versions": versions,
	}
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("构造清单 JSON 失败：%v", err)
	}
	return raw
}

// defaultManifestJSON 返回一份包含四种类型、发布时间各不相同的合法清单。
func defaultManifestJSON(t *testing.T) []byte {
	t.Helper()

	return buildManifestJSON(t, "1.21.4", "25w02a", []map[string]any{
		entry("1.21.4", "release", "2024-12-03T10:12:34+00:00"),
		entry("1.20.1", "release", "2023-06-12T10:00:00+00:00"),
		entry("25w02a", "snapshot", "2025-01-08T10:00:00+00:00"),
		entry("a1.2.6", "old_alpha", "2010-06-30T10:00:00+00:00"),
	})
}

// requireCode 断言错误码符合预期。
func requireCode(t *testing.T, err *AppError, expected string) {
	t.Helper()

	if err == nil {
		t.Fatalf("期望错误码 %s，实际没有错误", expected)
	}
	if err.Code != expected {
		t.Fatalf("期望错误码 %s，实际 %s（%s）", expected, err.Code, err.Message)
	}
}

// TestParseManifestAcceptsValidDocument 验证合法清单可解析、latest 可读且按发布时间降序。
func TestParseManifestAcceptsValidDocument(t *testing.T) {
	snapshot, appErr := parseManifest(defaultManifestJSON(t))
	if appErr != nil {
		t.Fatalf("合法清单不应报错：%v", appErr)
	}

	if len(snapshot.versions) != 4 {
		t.Fatalf("期望 4 条版本，实际 %d", len(snapshot.versions))
	}
	if snapshot.latestRelease != "1.21.4" || snapshot.latestSnapshot != "25w02a" {
		t.Fatalf("latest 解析错误：%+v", snapshot)
	}
	if snapshot.verified {
		t.Fatal("官方清单没有自校验哈希，verified 必须为 false")
	}
	if snapshot.versions[0].id != "25w02a" {
		t.Fatalf("期望按发布时间降序排列，首条为 25w02a，实际 %s", snapshot.versions[0].id)
	}
	if snapshot.versions[0].releaseTimeUnixMs == 0 {
		t.Fatal("发布时间未被解析为时间戳，排序与展示都会失真")
	}
	if snapshot.versions[0].url == "" {
		t.Fatal("条目 URL 必须保留在后端模型中（供后续卡使用）")
	}
}

// TestParseManifestAcceptsOfficialShapeWithoutSize 验证官方清单的真实形态被接受。
// 官方 version_manifest_v2 的版本条目只有 id / type / url / time / releaseTime / sha1 / complianceLevel，
// **不含 size**（size 只出现在版本 JSON 内部的单个下载条目里）。若把 size 缺失判为非法，
// 整个版本列表都会不可用——2026-11-01 的真机验收中曾因此报出「版本清单条目「26.4-snapshot-6」不合法」。
func TestParseManifestAcceptsOfficialShapeWithoutSize(t *testing.T) {
	raw := buildManifestJSON(t, "26.3", "26.4-snapshot-6", []map[string]any{
		{
			"id":              "26.4-snapshot-6",
			"type":            "snapshot",
			"url":             "https://piston-meta.mojang.com/v1/packages/04be0f389fdac9f1c8b9b551c1e717104a08899d/26.4-snapshot-6.json",
			"time":            "2026-10-27T13:49:25+00:00",
			"releaseTime":     "2026-10-27T13:40:29+00:00",
			"sha1":            "04be0f389fdac9f1c8b9b551c1e717104a08899d",
			"complianceLevel": 1,
		},
		{
			"id":              "26.3",
			"type":            "release",
			"url":             "https://piston-meta.mojang.com/v1/packages/5df1795e16eea19d75b923d66ecca9ce183353c5/26.3.json",
			"time":            "2026-10-27T06:57:57+00:00",
			"releaseTime":     "2026-09-15T11:23:02+00:00",
			"sha1":            "5df1795e16eea19d75b923d66ecca9ce183353c5",
			"complianceLevel": 1,
		},
	})

	snapshot, appErr := parseManifest(raw)
	if appErr != nil {
		t.Fatalf("官方清单形态不应报错：%v", appErr)
	}
	if len(snapshot.versions) != 2 {
		t.Fatalf("期望 2 条版本，实际 %d", len(snapshot.versions))
	}
	for _, version := range snapshot.versions {
		if version.size != 0 {
			t.Fatalf("清单未提供 size 时应保持 0（表示未知），实际 %d", version.size)
		}
	}
	if snapshot.latestSnapshot != "26.4-snapshot-6" {
		t.Fatalf("latest.snapshot 解析错误：%s", snapshot.latestSnapshot)
	}
}

// TestParseManifestRejectsMalformedDocuments 验证各类结构非法都返回 VERSION_MANIFEST_002。
func TestParseManifestRejectsMalformedDocuments(t *testing.T) {
	tooMany := make([]map[string]any, 0, manifestMaxVersions+1)
	for index := 0; index < manifestMaxVersions+1; index++ {
		tooMany = append(tooMany, entry(fmt.Sprintf("v%d", index), "release", "2024-01-01T00:00:00+00:00"))
	}

	cases := []struct {
		name string
		raw  []byte
	}{
		{name: "非法 JSON", raw: []byte("{not json")},
		{name: "缺少 versions", raw: buildManifestJSON(t, "1.0", "1.0", nil)},
		{name: "latest 为空", raw: buildManifestJSON(t, "", "", []map[string]any{entry("1.0", "release", "2024-01-01T00:00:00+00:00")})},
		{name: "latest 悬空", raw: buildManifestJSON(t, "9.9", "9.9", []map[string]any{entry("1.0", "release", "2024-01-01T00:00:00+00:00")})},
		{name: "缺少 id", raw: buildManifestJSON(t, "1.0", "1.0", []map[string]any{{"type": "release", "url": "https://piston-meta.mojang.com/x.json", "releaseTime": "2024-01-01T00:00:00+00:00", "sha1": strings.Repeat("a", 40), "size": 10}})},
		{name: "未知类型", raw: buildManifestJSON(t, "1.0", "1.0", []map[string]any{entry("1.0", "beta", "2024-01-01T00:00:00+00:00")})},
		{name: "大小为负", raw: buildManifestJSON(t, "1.0", "1.0", []map[string]any{{"id": "1.0", "type": "release", "url": "https://piston-meta.mojang.com/x.json", "releaseTime": "2024-01-01T00:00:00+00:00", "sha1": strings.Repeat("a", 40), "size": -1}})},
		{name: "大小超限", raw: buildManifestJSON(t, "1.0", "1.0", []map[string]any{{"id": "1.0", "type": "release", "url": "https://piston-meta.mojang.com/x.json", "releaseTime": "2024-01-01T00:00:00+00:00", "sha1": strings.Repeat("a", 40), "size": manifestMaxSingleVersionSize + 1}})},
		{name: "sha1 非法", raw: buildManifestJSON(t, "1.0", "1.0", []map[string]any{{"id": "1.0", "type": "release", "url": "https://piston-meta.mojang.com/x.json", "releaseTime": "2024-01-01T00:00:00+00:00", "sha1": "zz", "size": 10}})},
		{name: "发布时间非法", raw: buildManifestJSON(t, "1.0", "1.0", []map[string]any{{"id": "1.0", "type": "release", "url": "https://piston-meta.mojang.com/x.json", "releaseTime": "昨天", "sha1": strings.Repeat("a", 40), "size": 10}})},
		{name: "重复 id", raw: buildManifestJSON(t, "1.0", "1.0", []map[string]any{entry("1.0", "release", "2024-01-01T00:00:00+00:00"), entry("1.0", "release", "2024-01-02T00:00:00+00:00")})},
		{name: "条数超限", raw: buildManifestJSON(t, "1.0", "1.0", tooMany)},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, appErr := parseManifest(testCase.raw)
			requireCode(t, appErr, ErrorCodeManifestInvalid)
			if appErr.Retryable {
				t.Fatal("结构非法的错误不可重试：重试不会改变内容")
			}
		})
	}
}

// TestParseManifestRejectsInvalidEntryURL 验证条目 URL 必须 https 且 host 在白名单内。
func TestParseManifestRejectsInvalidEntryURL(t *testing.T) {
	httpURL := entry("1.0", "release", "2024-01-01T00:00:00+00:00")
	httpURL["url"] = "http://piston-meta.mojang.com/x.json"

	foreignHost := entry("1.0", "release", "2024-01-01T00:00:00+00:00")
	foreignHost["url"] = "https://evil.example.com/x.json"

	cases := []struct {
		name    string
		raw     []byte
		message string
	}{
		{name: "非 https", raw: buildManifestJSON(t, "1.0", "1.0", []map[string]any{httpURL}), message: messageURLNotHTTPS},
		{name: "非白名单 host", raw: buildManifestJSON(t, "1.0", "1.0", []map[string]any{foreignHost}), message: messageURLHostNotAllowed},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, appErr := parseManifest(testCase.raw)
			requireCode(t, appErr, ErrorCodeURLInvalid)
			if appErr.Message != testCase.message {
				t.Fatalf("期望文案 %q，实际 %q", testCase.message, appErr.Message)
			}
		})
	}
}

// TestValidateManifestURL 验证地址校验的边界取值。
func TestValidateManifestURL(t *testing.T) {
	cases := []struct {
		name  string
		raw   string
		valid bool
	}{
		{name: "官方清单地址", raw: manifestURL, valid: true},
		{name: "空白包裹", raw: "  https://piston-meta.mojang.com/a.json  ", valid: true},
		{name: "缺少协议", raw: "piston-meta.mojang.com/a.json", valid: false},
		{name: "http", raw: "http://piston-meta.mojang.com/a.json", valid: false},
		{name: "非白名单 host", raw: "https://example.com/a.json", valid: false},
		{name: "空字符串", raw: "", valid: false},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			appErr := validateManifestURL(testCase.raw)
			if testCase.valid && appErr != nil {
				t.Fatalf("期望通过校验，实际 %v", appErr)
			}
			if !testCase.valid {
				requireCode(t, appErr, ErrorCodeURLInvalid)
			}
		})
	}
}

// TestRefreshManifestWritesCache 验证成功获取后同时写出清单与元数据缓存。
func TestRefreshManifestWritesCache(t *testing.T) {
	root := setupTestDataRoot(t)
	now := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	freezeClock(t, now)

	body := defaultManifestJSON(t)
	stubManifestResponse(t, body)

	status, appErr := RefreshManifest()
	if appErr != nil {
		t.Fatalf("刷新不应失败：%v", appErr)
	}
	if !status.Available || status.Count != 4 || status.Stale {
		t.Fatalf("状态不正确：%+v", status)
	}
	if status.SourceHost != manifestSourceHost {
		t.Fatalf("来源 host 不正确：%s", status.SourceHost)
	}
	if status.Verified {
		t.Fatal("清单没有自校验哈希，verified 必须为 false")
	}

	cached, err := os.ReadFile(cachePath(root))
	if err != nil {
		t.Fatalf("清单缓存未写入：%v", err)
	}
	if !bytes.Equal(cached, body) {
		t.Fatal("缓存内容必须与官方原始字节一致，便于后续卡按官方格式复用")
	}

	meta, err := readCacheMeta(root)
	if err != nil {
		t.Fatalf("元数据缓存未写入：%v", err)
	}
	if meta.FetchedAtMs != now.UnixMilli() {
		t.Fatalf("获取时间不正确：%d", meta.FetchedAtMs)
	}
	if meta.Size != int64(len(body)) {
		t.Fatalf("元数据大小不正确：%d", meta.Size)
	}
	if len(meta.SHA256) != 64 {
		t.Fatalf("元数据应记录 SHA-256 摘要，实际 %q", meta.SHA256)
	}
	if meta.SourceHost != manifestSourceHost {
		t.Fatalf("元数据来源 host 不正确：%s", meta.SourceHost)
	}
}

// TestRefreshManifestRejectsOversizedBody 验证响应体超过 8 MiB 时返回 VERSION_MANIFEST_002。
func TestRefreshManifestRejectsOversizedBody(t *testing.T) {
	setupTestDataRoot(t)
	stubManifestResponse(t, bytes.Repeat([]byte("x"), manifestMaxBodyBytes+1))

	_, appErr := RefreshManifest()
	requireCode(t, appErr, ErrorCodeManifestInvalid)
	if appErr.Message != messageManifestTooLarge {
		t.Fatalf("期望文案 %q，实际 %q", messageManifestTooLarge, appErr.Message)
	}
}

// TestRefreshManifestReportsFetchFailures 验证网络错误与非 2xx 状态都返回 VERSION_MANIFEST_001。
func TestRefreshManifestReportsFetchFailures(t *testing.T) {
	cases := []struct {
		name      string
		transport http.RoundTripper
	}{
		{
			name: "网络错误",
			transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				return nil, errors.New("connection refused")
			}),
		},
		{
			name: "服务端错误",
			transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				return errorResponse(request, http.StatusInternalServerError), nil
			}),
		},
		{
			name: "未找到",
			transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				return errorResponse(request, http.StatusNotFound), nil
			}),
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			setupTestDataRoot(t)
			stubTransport(t, testCase.transport)

			_, appErr := RefreshManifest()
			requireCode(t, appErr, ErrorCodeManifestFetch)
			if !appErr.Retryable {
				t.Fatal("网络与临时状态码错误应标记为可重试")
			}
		})
	}
}

// TestRefreshManifestRejectsInvalidRedirect 验证每一跳重定向都必须 https 且 host 在白名单内。
func TestRefreshManifestRejectsInvalidRedirect(t *testing.T) {
	cases := []struct {
		name    string
		target  string
		message string
	}{
		{name: "重定向到 http", target: "http://piston-meta.mojang.com/other.json", message: messageURLNotHTTPS},
		{name: "重定向到非白名单 host", target: "https://evil.example.com/manifest.json", message: messageURLHostNotAllowed},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			setupTestDataRoot(t)
			stubTransport(t, roundTripFunc(func(request *http.Request) (*http.Response, error) {
				return redirectResponse(request, testCase.target), nil
			}))

			_, appErr := RefreshManifest()
			requireCode(t, appErr, ErrorCodeURLInvalid)
			if appErr.Message != testCase.message {
				t.Fatalf("期望文案 %q，实际 %q", testCase.message, appErr.Message)
			}
		})
	}
}

// TestRefreshManifestRejectsTooManyRedirects 验证重定向跳数超过 3 跳即拒绝。
func TestRefreshManifestRejectsTooManyRedirects(t *testing.T) {
	setupTestDataRoot(t)

	hops := 0
	stubTransport(t, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		hops++
		return redirectResponse(request, "https://piston-meta.mojang.com/redirect/"+request.URL.Path), nil
	}))

	_, appErr := RefreshManifest()
	requireCode(t, appErr, ErrorCodeURLInvalid)
	if appErr.Message != messageURLTooManyRedirects {
		t.Fatalf("期望文案 %q，实际 %q", messageURLTooManyRedirects, appErr.Message)
	}
	if hops > manifestMaxRedirects+1 {
		t.Fatalf("重定向跳数未被及时限制，实际发起 %d 次请求", hops)
	}
}

// TestRefreshManifestUsesConditionalRequest 验证 304 时沿用旧内容并只刷新获取时间。
func TestRefreshManifestUsesConditionalRequest(t *testing.T) {
	setupTestDataRoot(t)
	now := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	setNow := freezeClock(t, now)

	body := defaultManifestJSON(t)
	stubTransport(t, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Header.Get("If-None-Match") != "" {
			response := errorResponse(request, http.StatusNotModified)
			response.Header.Set("ETag", "rev-2")
			return response, nil
		}
		response := jsonResponse(request, body)
		response.Header.Set("ETag", "rev-1")
		return response, nil
	}))

	if _, appErr := RefreshManifest(); appErr != nil {
		t.Fatalf("首次刷新不应失败：%v", appErr)
	}

	setNow(now.Add(2 * time.Hour))
	status, appErr := RefreshManifest()
	if appErr != nil {
		t.Fatalf("条件刷新不应失败：%v", appErr)
	}
	if status.FetchedAtMs != now.Add(2*time.Hour).UnixMilli() {
		t.Fatalf("304 后应更新获取时间，实际 %d", status.FetchedAtMs)
	}
	if status.Count != 4 {
		t.Fatalf("304 后应沿用旧内容，实际条数 %d", status.Count)
	}
	if status.Stale {
		t.Fatal("304 表示远端未变化，缓存应视为新鲜")
	}
}

// TestCacheWriteFailureKeepsOldCache 验证写入失败时旧缓存仍可读。
func TestCacheWriteFailureKeepsOldCache(t *testing.T) {
	root := setupTestDataRoot(t)
	freezeClock(t, time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC))

	firstBody := defaultManifestJSON(t)
	stubManifestResponse(t, firstBody)
	if _, appErr := RefreshManifest(); appErr != nil {
		t.Fatalf("首次刷新不应失败：%v", appErr)
	}

	secondBody := buildManifestJSON(t, "1.21.4", "25w02a", []map[string]any{
		entry("1.21.4", "release", "2024-12-03T10:12:34+00:00"),
		entry("1.20.1", "release", "2023-06-12T10:00:00+00:00"),
		entry("25w02a", "snapshot", "2025-01-08T10:00:00+00:00"),
		entry("a1.2.6", "old_alpha", "2010-06-30T10:00:00+00:00"),
		entry("1.19.4", "release", "2023-03-14T10:00:00+00:00"),
	})
	stubManifestResponse(t, secondBody)

	previousRename := renameFile
	renameFile = func(oldPath string, newPath string) error {
		return errors.New("disk full")
	}
	t.Cleanup(func() { renameFile = previousRename })

	status, appErr := RefreshManifest()
	if appErr != nil {
		t.Fatalf("写入失败不应中断本次展示：%v", appErr)
	}
	if status.Count != 5 {
		t.Fatalf("内存状态应使用新内容，实际条数 %d", status.Count)
	}

	cached, err := os.ReadFile(cachePath(root))
	if err != nil {
		t.Fatalf("旧缓存被破坏：%v", err)
	}
	if !bytes.Equal(cached, firstBody) {
		t.Fatal("写入失败后旧缓存必须保持原内容")
	}
}

// TestListVersionsOfflineFallbackUsesCache 验证过期缓存 + 网络失败时返回缓存并标记 stale。
func TestListVersionsOfflineFallbackUsesCache(t *testing.T) {
	setupTestDataRoot(t)
	fetchedAt := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	setNow := freezeClock(t, fetchedAt)

	stubManifestResponse(t, defaultManifestJSON(t))
	if _, appErr := RefreshManifest(); appErr != nil {
		t.Fatalf("预置缓存失败：%v", appErr)
	}

	// 越过 24 小时 TTL 后让网络失效。
	setNow(fetchedAt.Add(25 * time.Hour))
	stubTransport(t, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return nil, errors.New("offline")
	}))

	stateMu.Lock()
	currentSnapshot = nil
	stateMu.Unlock()

	result, appErr := ListVersions(VersionListRequest{})
	if appErr != nil {
		t.Fatalf("离线降级应返回缓存而不是错误：%v", appErr)
	}
	if !result.Stale {
		t.Fatal("来自过期缓存的结果必须标记 stale")
	}
	if result.WarningCode != WarningManifestStale {
		t.Fatalf("期望告警码 %s，实际 %q", WarningManifestStale, result.WarningCode)
	}
	if result.Total == 0 || len(result.Items) == 0 {
		t.Fatal("离线时仍应返回可展示的版本列表")
	}
	if result.FetchedAtMs != fetchedAt.UnixMilli() {
		t.Fatalf("应保留缓存获取时间，实际 %d", result.FetchedAtMs)
	}
}

// TestListVersionsWithoutCacheReturnsError 验证无缓存且网络失败时返回错误码而非空列表。
func TestListVersionsWithoutCacheReturnsError(t *testing.T) {
	setupTestDataRoot(t)
	stubTransport(t, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return nil, errors.New("offline")
	}))

	result, appErr := ListVersions(VersionListRequest{})
	requireCode(t, appErr, ErrorCodeManifestFetch)
	if len(result.Items) != 0 || result.Total != 0 {
		t.Fatal("失败时不得返回看起来成功的空列表")
	}
}

// TestListVersionsFiltersAndPaginates 验证类型过滤与分页边界。
func TestListVersionsFiltersAndPaginates(t *testing.T) {
	setupTestDataRoot(t)
	stubManifestResponse(t, defaultManifestJSON(t))

	if _, appErr := RefreshManifest(); appErr != nil {
		t.Fatalf("预置缓存失败：%v", appErr)
	}

	// 默认只返回 release + snapshot，不展示旧预览版。
	defaultResult, appErr := ListVersions(VersionListRequest{})
	if appErr != nil {
		t.Fatalf("默认查询不应失败：%v", appErr)
	}
	if defaultResult.Total != 3 {
		t.Fatalf("默认应返回 3 条（2 release + 1 snapshot），实际 %d", defaultResult.Total)
	}
	for _, item := range defaultResult.Items {
		if item.Type == versionTypeOldAlpha || item.Type == versionTypeOldBeta {
			t.Fatalf("默认列表不应包含旧预览版，实际出现 %s", item.Type)
		}
	}

	// 显式请求 old_alpha 才返回旧预览版。
	oldResult, appErr := ListVersions(VersionListRequest{Type: versionTypeOldAlpha})
	if appErr != nil {
		t.Fatalf("旧预览版查询不应失败：%v", appErr)
	}
	if oldResult.Total != 1 || oldResult.Items[0].ID != "a1.2.6" {
		t.Fatalf("old_alpha 过滤结果不正确：%+v", oldResult.Items)
	}

	// 分页：offset 超界时返回空页但 total 保持。
	paged, appErr := ListVersions(VersionListRequest{Offset: 10, Limit: 2})
	if appErr != nil {
		t.Fatalf("分页查询不应失败：%v", appErr)
	}
	if len(paged.Items) != 0 || paged.Total != 3 {
		t.Fatalf("超界分页应返回空页且 total 不变：%+v", paged)
	}

	// Limit 超上限时截断到 200，负 offset 纠正为 0。
	clamped, appErr := ListVersions(VersionListRequest{Offset: -5, Limit: 9999})
	if appErr != nil {
		t.Fatalf("边界参数不应失败：%v", appErr)
	}
	if clamped.Offset != 0 || clamped.Limit != versionListMaxLimit {
		t.Fatalf("分页参数未被纠正：offset=%d limit=%d", clamped.Offset, clamped.Limit)
	}

	// 多个类型用逗号分隔。
	multi, appErr := ListVersions(VersionListRequest{Type: "release,old_alpha"})
	if appErr != nil {
		t.Fatalf("多类型查询不应失败：%v", appErr)
	}
	if multi.Total != 3 {
		t.Fatalf("release + old_alpha 应为 3 条，实际 %d", multi.Total)
	}

	// 未知类型不匹配任何版本，但不报错。
	unknown, appErr := ListVersions(VersionListRequest{Type: "nonsense"})
	if appErr != nil {
		t.Fatalf("未知类型不应报错：%v", appErr)
	}
	if unknown.Total != 0 {
		t.Fatalf("未知类型应无匹配，实际 %d", unknown.Total)
	}
}

// TestVersionSummaryDoesNotExposeURL 验证面向前端的 DTO 不含条目 URL。
func TestVersionSummaryDoesNotExposeURL(t *testing.T) {
	setupTestDataRoot(t)
	stubManifestResponse(t, defaultManifestJSON(t))
	if _, appErr := RefreshManifest(); appErr != nil {
		t.Fatalf("预置缓存失败：%v", appErr)
	}

	result, appErr := ListVersions(VersionListRequest{})
	if appErr != nil {
		t.Fatalf("查询不应失败：%v", appErr)
	}

	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("结果无法序列化：%v", err)
	}
	if strings.Contains(string(encoded), "\"url\"") {
		t.Fatalf("DTO 不得包含 url 字段（安全约束 §2.1）：%s", string(encoded))
	}
	if !strings.Contains(string(encoded), "\"metadataSupported\"") {
		t.Fatalf("DTO 应包含兼容性四层字段：%s", string(encoded))
	}
	if !strings.Contains(string(encoded), "\"fetchedAtMs\"") {
		t.Fatalf("DTO 应包含 fetchedAtMs 字段：%s", string(encoded))
	}
}

// TestDataRootProviderMissingReturnsLayoutError 验证未注入数据根时三个入口都返回 VERSION_LAYOUT_001。
func TestDataRootProviderMissingReturnsLayoutError(t *testing.T) {
	stateMu.Lock()
	currentSnapshot = nil
	lastManifestErrorCode = ""
	stateMu.Unlock()
	SetDataRootProvider(nil)
	t.Cleanup(func() { SetDataRootProvider(nil) })

	_, listErr := ListVersions(VersionListRequest{})
	requireCode(t, listErr, ErrorCodeDataRootUnavailable)

	_, statusErr := GetManifestStatus()
	requireCode(t, statusErr, ErrorCodeDataRootUnavailable)

	_, refreshErr := RefreshManifest()
	requireCode(t, refreshErr, ErrorCodeDataRootUnavailable)
}

// TestDataRootProviderErrorReturnsLayoutError 验证 provider 返回错误时的降级行为。
func TestDataRootProviderErrorReturnsLayoutError(t *testing.T) {
	SetDataRootProvider(func() (string, error) { return "", errors.New("no data root") })
	t.Cleanup(func() { SetDataRootProvider(nil) })

	_, appErr := GetManifestStatus()
	requireCode(t, appErr, ErrorCodeDataRootUnavailable)
	if !appErr.Retryable {
		t.Fatal("数据根不可用属于环境问题，修正后应可重试")
	}
}

// TestGetManifestStatusWithoutCache 验证无缓存时返回「不可用」状态而非错误。
func TestGetManifestStatusWithoutCache(t *testing.T) {
	setupTestDataRoot(t)

	status, appErr := GetManifestStatus()
	if appErr != nil {
		t.Fatalf("无缓存时状态查询不应报错：%v", appErr)
	}
	if status.Available || status.Count != 0 {
		t.Fatalf("无缓存时应报告不可用：%+v", status)
	}
}

// TestLoadCacheWithoutMetaFallsBackToMtime 验证元数据缺失时用文件时间兜底并标记过期。
func TestLoadCacheWithoutMetaFallsBackToMtime(t *testing.T) {
	root := setupTestDataRoot(t)
	freezeClock(t, time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC))

	if err := writeFileAtomic(cachePath(root), defaultManifestJSON(t)); err != nil {
		t.Fatalf("预置缓存失败：%v", err)
	}

	snapshot, appErr := loadCache(root)
	if appErr != nil {
		t.Fatalf("加载缓存不应失败：%v", appErr)
	}
	if snapshot == nil {
		t.Fatal("缓存文件存在时应能加载")
	}
	if !snapshot.stale || snapshot.warningCode != WarningManifestStale {
		t.Fatal("缺少元数据时无法确认新鲜度，必须按过期处理")
	}
	if snapshot.fetchedAtMs == 0 {
		t.Fatal("应以文件修改时间兜底填充获取时间")
	}
}

// TestLoadCacheReportsExpiredState 验证缓存超过 TTL 后标记过期。
func TestLoadCacheReportsExpiredState(t *testing.T) {
	root := setupTestDataRoot(t)
	fetchedAt := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	setNow := freezeClock(t, fetchedAt)

	stubManifestResponse(t, defaultManifestJSON(t))
	if _, appErr := RefreshManifest(); appErr != nil {
		t.Fatalf("预置缓存失败：%v", appErr)
	}

	fresh, appErr := loadCache(root)
	if appErr != nil || fresh == nil {
		t.Fatalf("应能加载缓存：%v", appErr)
	}
	if fresh.stale {
		t.Fatal("刚写入的缓存不应过期")
	}

	setNow(fetchedAt.Add(manifestCacheTTL))
	stale, appErr := loadCache(root)
	if appErr != nil || stale == nil {
		t.Fatalf("应能加载缓存：%v", appErr)
	}
	if !stale.stale {
		t.Fatal("超过 24 小时后缓存必须标记过期")
	}
}

// TestVersionServiceDelegates 验证 Wails 服务包装把调用转发到包级实现。
func TestVersionServiceDelegates(t *testing.T) {
	setupTestDataRoot(t)
	stubManifestResponse(t, defaultManifestJSON(t))

	service := &VersionService{}

	status, err := service.RefreshManifest()
	if err != nil {
		t.Fatalf("刷新不应失败：%v", err)
	}
	if status.Count != 4 {
		t.Fatalf("刷新结果不正确：%+v", status)
	}

	result, err := service.ListVersions(VersionListRequest{})
	if err != nil {
		t.Fatalf("列表查询不应失败：%v", err)
	}
	if result.Total != 3 {
		t.Fatalf("默认列表应为 3 条，实际 %d", result.Total)
	}

	queried, err := service.GetManifestStatus()
	if err != nil {
		t.Fatalf("状态查询不应失败：%v", err)
	}
	if !queried.Available {
		t.Fatal("刷新成功后状态应为可用")
	}
}
