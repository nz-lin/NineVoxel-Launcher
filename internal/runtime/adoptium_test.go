package runtime

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"

	"example.com/ninevoxel/launcher/internal/download"
)

// 本文件是 P3-14 第三方 Runtime 来源（Adoptium/Temurin）的单元测试。
//
// 测试约束（与 P3-09 的 install_test.go 一致）：
//   - 不访问真实网络：所有 HTTP 请求都交给注入的假 RoundTripper；
//   - 不启动真实 Java 进程：本机探测入口被替换为固定空列表；
//   - 不写用户目录：数据根与数据库都放在 t.TempDir() 里；
//   - 压缩包用 archive/zip 在临时目录里现场生成。

// requireCode 断言错误码，失败时打印可读原因。
func requireCode(t *testing.T, appErr *AppError, want string) {
	t.Helper()

	if appErr == nil {
		t.Fatalf("期望错误码 %s，但返回了 nil 错误", want)
	}
	if appErr.Code != want {
		t.Fatalf("错误码 = %s，期望 %s（提示：%s）", appErr.Code, want, appErr.Message)
	}
}

// adoptiumTestSHA256 计算测试数据的规范小写 SHA-256。
func adoptiumTestSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// adoptiumTestAssetsBody 构造 Adoptium API 的响应体。
//
// 只保留被测代码实际读取的字段：version.semver / version.openjdk_version /
// version.major / binary.package.{link,checksum,size} / release_name。
func adoptiumTestAssetsBody(t *testing.T, link, checksum string, size int64) []byte {
	t.Helper()

	payload := []map[string]any{
		{
			"release_name": "jdk-21.0.13+9",
			"version": map[string]any{
				"semver":          "21.0.13+9.0.LTS",
				"openjdk_version": "21.0.13+9-LTS",
				"major":           21,
			},
			"binary": map[string]any{
				"package": map[string]any{
					"link":     link,
					"checksum": checksum,
					"size":     size,
				},
			},
		},
	}

	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("构造测试用 API 响应失败: %v", err)
	}
	return encoded
}

// adoptiumTestZip 生成一个内存中的 zip，按 entries 的相对路径写入文本内容。
func adoptiumTestZip(t *testing.T, entries map[string]string) []byte {
	t.Helper()

	buffer := &bytes.Buffer{}
	writer := zip.NewWriter(buffer)

	// 按 key 排序只为让产物稳定，便于断言错误信息。
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		file, err := writer.Create(name)
		if err != nil {
			t.Fatalf("写入测试 zip 条目 %s 失败: %v", name, err)
		}
		if _, err := file.Write([]byte(entries[name])); err != nil {
			t.Fatalf("写入测试 zip 内容 %s 失败: %v", name, err)
		}
	}

	if err := writer.Close(); err != nil {
		t.Fatalf("关闭测试 zip 失败: %v", err)
	}
	return buffer.Bytes()
}

// adoptiumRoundTripper 把函数适配成 http.RoundTripper，便于注入假响应。
type adoptiumRoundTripper func(req *http.Request) (*http.Response, error)

// RoundTrip 实现 http.RoundTripper。
func (f adoptiumRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

// adoptiumTestResponse 构造一个带响应体的假响应。
func adoptiumTestResponse(req *http.Request, status int, body []byte) *http.Response {
	return &http.Response{
		StatusCode:    status,
		Status:        fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
		Header:        make(http.Header),
		Request:       req,
	}
}

// adoptiumTestServer 记录请求次数并按 host 分派假响应。
type adoptiumTestServer struct {
	mu           sync.Mutex
	apiCalls     int32
	archiveCalls int32
	apiBodies    map[int][]byte
	archiveBody  []byte
	archiveError error
	apiStatus    int
	handler      adoptiumRoundTripper
}

// roundTrip 实现假网络分派：API 请求返回 apiBodies，归档请求返回 archiveBody。
//
// handler 非空时优先使用，用于需要构造重定向等特殊响应的用例。
func (s *adoptiumTestServer) roundTrip(req *http.Request) (*http.Response, error) {
	if s.handler != nil {
		return s.handler(req)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	switch req.URL.Host {
	case adoptiumAPIHost:
		s.apiCalls++
		if s.apiStatus != 0 && s.apiStatus != http.StatusOK {
			return adoptiumTestResponse(req, s.apiStatus, nil), nil
		}
		for major, body := range s.apiBodies {
			if strings.Contains(req.URL.Path, fmt.Sprintf("/latest/%d/", major)) {
				return adoptiumTestResponse(req, http.StatusOK, body), nil
			}
		}
		return adoptiumTestResponse(req, http.StatusOK, nil), nil
	case adoptiumOfficialHost, adoptiumMirrorHost, adoptiumRedirectHost:
		s.archiveCalls++
		if s.archiveError != nil {
			return nil, s.archiveError
		}
		return adoptiumTestResponse(req, http.StatusOK, s.archiveBody), nil
	default:
		return adoptiumTestResponse(req, http.StatusNotFound, nil), nil
	}
}

// adoptiumTestFixture 是一次测试用的完整替身环境。
type adoptiumTestFixture struct {
	t             *testing.T
	server        *adoptiumTestServer
	dataRoot      string
	events        []recordedRuntimeEvent
	mirrorEnabled bool
	oldDataRoot   func() (string, *AppError)
	oldDBPath     func() string
	oldProbeList  func() (RuntimeList, *AppError)
	oldProgress   func(taskID, state string, progress float64, errorCode string)
	oldClient     func() *http.Client
	oldMirror     func() bool
}

// newAdoptiumTestFixture 建立临时数据根、临时数据库与假网络，并在清理时全部还原。
func newAdoptiumTestFixture(t *testing.T, server *adoptiumTestServer) *adoptiumTestFixture {
	t.Helper()

	root := t.TempDir()
	dataRoot := filepath.Join(root, "data")
	if err := os.MkdirAll(dataRoot, 0o755); err != nil {
		t.Fatalf("创建临时数据根失败: %v", err)
	}

	fixture := &adoptiumTestFixture{
		t:             t,
		server:        server,
		dataRoot:      dataRoot,
		mirrorEnabled: false,
		oldDataRoot:   runtimeDataRoot,
		oldDBPath:     runtimeDatabasePathResolver,
		oldProbeList:  runtimeLocalProbeList,
		oldProgress:   runtimeProgressSink,
		oldClient:     adoptiumHTTPClientFactory,
		oldMirror:     adoptiumMirrorEnabled,
	}

	resetRuntimeDatabaseForTest(t)

	runtimeDataRoot = func() (string, *AppError) { return dataRoot, nil }
	runtimeDatabasePathResolver = func() string { return filepath.Join(root, "app.db") }
	runtimeLocalProbeList = func() (RuntimeList, *AppError) { return RuntimeList{}, nil }
	runtimeProgressSink = func(taskID, state string, progress float64, errorCode string) {
		fixture.events = append(fixture.events, recordedRuntimeEvent{
			TaskID: taskID, State: state, Progress: progress, ErrorCode: errorCode,
		})
	}
	adoptiumHTTPClientFactory = func() *http.Client {
		return &http.Client{Transport: adoptiumRoundTripper(server.roundTrip), CheckRedirect: checkAdoptiumRedirect}
	}
	adoptiumMirrorEnabled = func() bool { return fixture.mirrorEnabled }

	t.Cleanup(func() {
		runtimeDataRoot = fixture.oldDataRoot
		runtimeDatabasePathResolver = fixture.oldDBPath
		runtimeLocalProbeList = fixture.oldProbeList
		runtimeProgressSink = fixture.oldProgress
		adoptiumHTTPClientFactory = fixture.oldClient
		adoptiumMirrorEnabled = fixture.oldMirror
		resetRuntimeDatabaseForTest(t)
	})

	return fixture
}

// runtimeDir 返回数据根下的 runtimes 目录。
func (f *adoptiumTestFixture) runtimeDir() string {
	return filepath.Join(f.dataRoot, "runtimes")
}

// requirePlatform 在平台不受支持时跳过测试，避免在非 Windows/macOS/Linux 上误报失败。
func requireAdoptiumPlatform(t *testing.T) {
	t.Helper()

	if _, _, ok := platformInfo(runtime.GOOS, runtime.GOARCH); !ok {
		t.Skipf("当前平台 %s/%s 不受 Adoptium 来源支持，跳过", runtime.GOOS, runtime.GOARCH)
	}
}

// TestFetchAdoptiumReleaseSucceeds 验证成功解析 API 响应。
func TestFetchAdoptiumReleaseSucceeds(t *testing.T) {
	requireAdoptiumPlatform(t)

	zipBody := adoptiumTestZip(t, map[string]string{"jdk-21.0.13+9/bin/java.exe": "binary"})
	link := "https://github.com/adoptium/temurin21-binaries/releases/download/jdk-21.0.13%2B9/OpenJDK21U-jdk_x64_windows_hotspot_21.0.13_9.zip"
	server := &adoptiumTestServer{
		apiBodies:   map[int][]byte{21: adoptiumTestAssetsBody(t, link, adoptiumTestSHA256(zipBody), int64(len(zipBody)))},
		archiveBody: zipBody,
	}
	newAdoptiumTestFixture(t, server)

	release, appErr := FetchAdoptiumRelease(21, runtime.GOOS, runtime.GOARCH)
	if appErr != nil {
		t.Fatalf("获取 Adoptium 版本失败: %v", appErr)
	}

	if release.Major != 21 {
		t.Errorf("主版本 = %d，期望 21", release.Major)
	}
	if release.Semver != "21.0.13+9.0.LTS" {
		t.Errorf("semver = %q，期望 21.0.13+9.0.LTS", release.Semver)
	}
	if release.FullVersion != "21.0.13+9-LTS" {
		t.Errorf("openjdk 版本 = %q，期望 21.0.13+9-LTS", release.FullVersion)
	}
	if release.FileName != "OpenJDK21U-jdk_x64_windows_hotspot_21.0.13_9.zip" {
		t.Errorf("归档文件名 = %q，未从 link 中还原", release.FileName)
	}
	if release.SHA256 != adoptiumTestSHA256(zipBody) {
		t.Errorf("sha256 = %q，与期望不一致", release.SHA256)
	}
	if release.SizeBytes != int64(len(zipBody)) {
		t.Errorf("归档大小 = %d，期望 %d", release.SizeBytes, len(zipBody))
	}
	if release.OfficialURL != link {
		t.Errorf("官方地址 = %q，期望 %q", release.OfficialURL, link)
	}
	if release.ExecutableRel == "" {
		t.Error("可执行文件相对路径为空，平台映射未生效")
	}
	if server.apiCalls != 1 {
		t.Errorf("API 请求次数 = %d，期望 1", server.apiCalls)
	}
}

// TestFetchAdoptiumReleaseRejectsInvalidMajor 验证大版本非正数时拒绝。
func TestFetchAdoptiumReleaseRejectsInvalidMajor(t *testing.T) {
	newAdoptiumTestFixture(t, &adoptiumTestServer{})

	_, appErr := FetchAdoptiumRelease(0, runtime.GOOS, runtime.GOARCH)
	requireCode(t, appErr, ErrorCodeRuntimeRequirement)
	if appErr.Retryable {
		t.Error("大版本非法不应标记为可重试")
	}
}

// TestFetchAdoptiumReleaseRejectsUnsupportedPlatform 验证不支持的平台组合被拒绝。
func TestFetchAdoptiumReleaseRejectsUnsupportedPlatform(t *testing.T) {
	newAdoptiumTestFixture(t, &adoptiumTestServer{})

	_, appErr := FetchAdoptiumRelease(21, "plan9", "mips64")
	requireCode(t, appErr, ErrorCodeRuntimeRequirement)
}

// TestFetchAdoptiumReleaseRejectsMalformedPayloads 验证响应非法时统一返回来源错误码。
func TestFetchAdoptiumReleaseRejectsMalformedPayloads(t *testing.T) {
	requireAdoptiumPlatform(t)

	cases := []struct {
		name    string
		body    []byte
		retried bool
	}{
		{name: "非法 JSON", body: []byte(`{`), retried: false},
		{name: "空数组", body: []byte(`[]`), retried: false},
		{
			name:    "缺少 binary.package",
			body:    []byte(`[{"release_name":"jdk-21","version":{"semver":"21","openjdk_version":"21"}}]`),
			retried: false,
		},
		{
			name:    "checksum 不是 sha256",
			body:    []byte(`[{"version":{"semver":"21","openjdk_version":"21"},"binary":{"package":{"link":"https://github.com/a/b.zip","checksum":"abc","size":10}}}]`),
			retried: false,
		},
		{
			name:    "size 非正",
			body:    []byte(`[{"version":{"semver":"21","openjdk_version":"21"},"binary":{"package":{"link":"https://github.com/a/b.zip","checksum":"` + strings.Repeat("a", 64) + `","size":0}}}]`),
			retried: false,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			server := &adoptiumTestServer{apiBodies: map[int][]byte{21: testCase.body}}
			newAdoptiumTestFixture(t, server)

			_, appErr := FetchAdoptiumRelease(21, runtime.GOOS, runtime.GOARCH)
			requireCode(t, appErr, ErrorCodeRuntimeSource)
			if appErr.Retryable != testCase.retried {
				t.Errorf("可重试 = %v，期望 %v", appErr.Retryable, testCase.retried)
			}
		})
	}
}

// TestFetchAdoptiumReleaseMapsHTTPStatus 验证状态码到错误码与可重试性的映射。
func TestFetchAdoptiumReleaseMapsHTTPStatus(t *testing.T) {
	requireAdoptiumPlatform(t)

	cases := []struct {
		status  int
		retried bool
	}{
		{status: http.StatusNotFound, retried: false},
		{status: http.StatusTooManyRequests, retried: true},
		{status: http.StatusInternalServerError, retried: true},
		{status: http.StatusBadRequest, retried: false},
	}

	for _, testCase := range cases {
		t.Run(fmt.Sprintf("状态码 %d", testCase.status), func(t *testing.T) {
			server := &adoptiumTestServer{apiStatus: testCase.status}
			newAdoptiumTestFixture(t, server)

			_, appErr := FetchAdoptiumRelease(21, runtime.GOOS, runtime.GOARCH)
			requireCode(t, appErr, ErrorCodeRuntimeSource)
			if appErr.Retryable != testCase.retried {
				t.Errorf("可重试 = %v，期望 %v", appErr.Retryable, testCase.retried)
			}
		})
	}
}

// TestFetchAdoptiumReleaseRejectsRedirectOutsideWhitelist 验证重定向到白名单外 host 被拒绝。
func TestFetchAdoptiumReleaseRejectsRedirectOutsideWhitelist(t *testing.T) {
	requireAdoptiumPlatform(t)

	server := &adoptiumTestServer{}
	server.handler = func(req *http.Request) (*http.Response, error) {
		response := adoptiumTestResponse(req, http.StatusFound, nil)
		response.Header.Set("Location", "http://example.com/evil.zip")
		return response, nil
	}
	newAdoptiumTestFixture(t, server)

	_, appErr := FetchAdoptiumRelease(21, runtime.GOOS, runtime.GOARCH)
	requireCode(t, appErr, ErrorCodeRuntimeSource)
	if appErr.Retryable {
		t.Error("白名单外重定向不应标记为可重试")
	}
}

// TestBuildMirrorURL 验证镜像地址拼接与非法入参拒绝。
func TestBuildMirrorURL(t *testing.T) {
	url, appErr := BuildMirrorURL("", 21, "x64", "windows", "OpenJDK21U-jdk_x64_windows_hotspot_21.0.13_9.zip")
	if appErr != nil {
		t.Fatalf("构造镜像地址失败: %v", appErr)
	}
	want := "https://mirrors.tuna.tsinghua.edu.cn/Adoptium/21/jdk/x64/windows/OpenJDK21U-jdk_x64_windows_hotspot_21.0.13_9.zip"
	if url != want {
		t.Errorf("镜像地址 = %q，期望 %q", url, want)
	}

	cases := []struct {
		name     string
		base     string
		major    int
		arch     string
		osName   string
		filename string
	}{
		{name: "大版本非法", base: "", major: 0, arch: "x64", osName: "windows", filename: "a.zip"},
		{name: "base 非 https", base: "http://mirrors.tuna.tsinghua.edu.cn", major: 21, arch: "x64", osName: "windows", filename: "a.zip"},
		{name: "架构含斜杠", base: "", major: 21, arch: "x/64", osName: "windows", filename: "a.zip"},
		{name: "文件名含上跳", base: "", major: 21, arch: "x64", osName: "windows", filename: "../a.zip"},
		{name: "文件名为空", base: "", major: 21, arch: "x64", osName: "windows", filename: ""},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, appErr := BuildMirrorURL(testCase.base, testCase.major, testCase.arch, testCase.osName, testCase.filename)
			if appErr == nil {
				t.Fatal("期望拒绝非法镜像地址，但返回了 nil")
			}
		})
	}
}

// TestAdoptiumDownloadCandidates 验证镜像开关对候选顺序的影响。
func TestAdoptiumDownloadCandidates(t *testing.T) {
	release := &AdoptiumRelease{
		Major:       21,
		OS:          "windows",
		Arch:        "x64",
		FileName:    "OpenJDK21U-jdk_x64_windows_hotspot_21.0.13_9.zip",
		OfficialURL: "https://github.com/adoptium/temurin21-binaries/releases/download/jdk-21.0.13%2B9/OpenJDK21U-jdk_x64_windows_hotspot_21.0.13_9.zip",
	}

	withoutMirror := adoptiumDownloadCandidates(release, false)
	if len(withoutMirror) != 1 || withoutMirror[0] != release.OfficialURL {
		t.Errorf("镜像关闭时候选 = %v，期望只含官方地址", withoutMirror)
	}

	withMirror := adoptiumDownloadCandidates(release, true)
	if len(withMirror) != 2 {
		t.Fatalf("镜像开启时候选数量 = %d，期望 2", len(withMirror))
	}
	if withMirror[0] != "https://mirrors.tuna.tsinghua.edu.cn/Adoptium/21/jdk/x64/windows/OpenJDK21U-jdk_x64_windows_hotspot_21.0.13_9.zip" {
		t.Errorf("镜像开启时首个候选 = %q", withMirror[0])
	}
	if withMirror[1] != release.OfficialURL {
		t.Errorf("镜像开启时次个候选 = %q，期望官方地址", withMirror[1])
	}
}

// TestParseJavaVersionTuple 验证版本串到四元组的解析。
func TestParseJavaVersionTuple(t *testing.T) {
	cases := []struct {
		raw  string
		want javaVersionTuple
		ok   bool
	}{
		{raw: "21.0.13+9-LTS", want: javaVersionTuple{Major: 21, Minor: 0, Security: 13, Build: 9}, ok: true},
		{raw: "21.0.12.1", want: javaVersionTuple{Major: 21, Minor: 0, Security: 12, Build: 1}, ok: true},
		{raw: "21.0.13", want: javaVersionTuple{Major: 21, Minor: 0, Security: 13, Build: 0}, ok: true},
		{raw: "17.0.9+9", want: javaVersionTuple{Major: 17, Minor: 0, Security: 9, Build: 9}, ok: true},
		{raw: "1.8.0_401", want: javaVersionTuple{Major: 8, Minor: 0, Security: 401, Build: 0}, ok: true},
		{raw: "", ok: false},
		{raw: "LTS", ok: false},
	}

	for _, testCase := range cases {
		t.Run(testCase.raw, func(t *testing.T) {
			got, ok := parseJavaVersionTuple(testCase.raw)
			if ok != testCase.ok {
				t.Fatalf("可解析 = %v，期望 %v", ok, testCase.ok)
			}
			if !testCase.ok {
				return
			}
			if got != testCase.want {
				t.Errorf("四元组 = %+v，期望 %+v", got, testCase.want)
			}
		})
	}
}

// TestCompareJavaVersionTuple 验证四元组的比较方向与边界。
func TestCompareJavaVersionTuple(t *testing.T) {
	newer, ok := parseJavaVersionTuple("21.0.13+9-LTS")
	if !ok {
		t.Fatal("解析 21.0.13+9-LTS 失败")
	}
	older, ok := parseJavaVersionTuple("21.0.12.1")
	if !ok {
		t.Fatal("解析 21.0.12.1 失败")
	}
	same, ok := parseJavaVersionTuple("21.0.13")
	if !ok {
		t.Fatal("解析 21.0.13 失败")
	}

	if compareJavaVersionTuple(newer, older) <= 0 {
		t.Error("21.0.13+9-LTS 应严格大于 21.0.12.1")
	}
	if compareJavaVersionTuple(older, newer) >= 0 {
		t.Error("21.0.12.1 应小于 21.0.13+9-LTS")
	}
	// 关键边界：security 位更高时，build 位较小也算更新。
	if compareJavaVersionTuple(same, older) <= 0 {
		t.Error("21.0.13 应严格大于 21.0.12.1（security 位优先于 build）")
	}
	if compareJavaVersionTuple(newer, same) <= 0 {
		t.Error("21.0.13+9-LTS 应严格大于 21.0.13（build 位更大）")
	}
	if compareJavaVersionTuple(newer, newer) != 0 {
		t.Error("相同版本比较应为 0")
	}
}

// TestSourceVersionToken 验证 runtimeId 版本段提取规则。
func TestSourceVersionToken(t *testing.T) {
	cases := map[string]string{
		"21.0.13+9-LTS": "21.0.13",
		"21.0.13+9":     "21.0.13",
		"21.0.13":       "21.0.13",
		"17.0.9+9":      "17.0.9",
	}

	for input, want := range cases {
		if got := sourceVersionToken(input); got != want {
			t.Errorf("sourceVersionToken(%q) = %q，期望 %q", input, got, want)
		}
	}

	if got := sourceVersionToken(""); got != "" {
		t.Errorf("空版本串应返回空 token，实际 %q", got)
	}
}

// TestFlattenSingleTopDirectory 验证剥离唯一顶层目录的行为。
func TestFlattenSingleTopDirectory(t *testing.T) {
	t.Run("唯一目录", func(t *testing.T) {
		root := t.TempDir()
		inner := filepath.Join(root, "jdk-21.0.13+9")
		if err := os.MkdirAll(filepath.Join(inner, "bin"), 0o755); err != nil {
			t.Fatalf("创建目录失败: %v", err)
		}

		got, appErr := flattenSingleTopDirectory(root)
		if appErr != nil {
			t.Fatalf("剥离顶层目录失败: %v", appErr)
		}
		if got != inner {
			t.Errorf("返回路径 = %q，期望 %q", got, inner)
		}
	})

	t.Run("多个顶层条目", func(t *testing.T) {
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, "a"), 0o755); err != nil {
			t.Fatalf("创建目录失败: %v", err)
		}
		if err := os.WriteFile(filepath.Join(root, "b.txt"), []byte("x"), 0o644); err != nil {
			t.Fatalf("写入文件失败: %v", err)
		}

		_, appErr := flattenSingleTopDirectory(root)
		requireCode(t, appErr, ErrorCodeRuntimeUnpack)
	})

	t.Run("唯一文件", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "one.txt"), []byte("x"), 0o644); err != nil {
			t.Fatalf("写入文件失败: %v", err)
		}

		_, appErr := flattenSingleTopDirectory(root)
		requireCode(t, appErr, ErrorCodeRuntimeUnpack)
	})

	t.Run("空目录", func(t *testing.T) {
		root := t.TempDir()
		_, appErr := flattenSingleTopDirectory(root)
		requireCode(t, appErr, ErrorCodeRuntimeUnpack)
	})
}

// TestIsSHA256Hex 验证 SHA-256 文本格式判定。
func TestIsSHA256Hex(t *testing.T) {
	valid := strings.Repeat("a", 64)
	if !isSHA256Hex(valid) {
		t.Error("64 位小写十六进制应被判为合法 SHA-256")
	}

	invalid := []string{
		"",
		strings.Repeat("a", 63),
		strings.Repeat("A", 64),
		strings.Repeat("z", 64),
		strings.Repeat("a", 64) + "b",
	}
	for _, value := range invalid {
		if isSHA256Hex(value) {
			t.Errorf("%q 不应被判为合法 SHA-256", value)
		}
	}
}

// TestInstallRuntimeFromSourceSucceeds 验证从第三方来源安装的完整流程与幂等性。
func TestInstallRuntimeFromSourceSucceeds(t *testing.T) {
	requireAdoptiumPlatform(t)

	zipBody := adoptiumTestZip(t, map[string]string{
		"jdk-21.0.13+9/bin/java.exe": "java",
		"jdk-21.0.13+9/lib/modules":  "modules",
		"jdk-21.0.13+9/release":      "release notes",
	})
	link := "https://github.com/adoptium/temurin21-binaries/releases/download/jdk-21.0.13%2B9/OpenJDK21U-jdk_x64_windows_hotspot_21.0.13_9.zip"
	server := &adoptiumTestServer{
		apiBodies:   map[int][]byte{21: adoptiumTestAssetsBody(t, link, adoptiumTestSHA256(zipBody), int64(len(zipBody)))},
		archiveBody: zipBody,
	}
	fixture := newAdoptiumTestFixture(t, server)

	accepted, appErr := InstallRuntimeFromSource(InstallRuntimeSourceRequest{MajorVersion: 21, AcceptLicense: true})
	if appErr != nil {
		t.Fatalf("安装第三方 Runtime 失败: %v", appErr)
	}

	wantRuntimeID := "adoptium-" + currentPlatformName() + "-" + currentArch() + "-21.0.13"
	if accepted.State != runtimeEventStateCompleted {
		t.Errorf("状态 = %q，期望 %q", accepted.State, runtimeEventStateCompleted)
	}
	if accepted.TaskID != runtimeEventTaskPrefix+wantRuntimeID {
		t.Errorf("任务 id = %q，期望 %q", accepted.TaskID, runtimeEventTaskPrefix+wantRuntimeID)
	}
	if accepted.Duplicate {
		t.Error("首次安装不应标记为重复")
	}

	finalDir := filepath.Join(fixture.runtimeDir(), wantRuntimeID)
	executable := filepath.Join(finalDir, "bin", "java.exe")
	if _, err := os.Stat(executable); err != nil {
		t.Fatalf("安装后找不到可执行文件 %s: %v", executable, err)
	}

	// staging 必须清理干净，避免留下半成品。
	if entries, err := os.ReadDir(fixture.runtimeDir()); err == nil {
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), ".") {
				t.Errorf("staging 目录未清理: %s", entry.Name())
			}
		}
	}

	rows, appErr := installedRuntimeRows(context.Background())
	if appErr != nil {
		t.Fatalf("读取已安装 Runtime 失败: %v", appErr)
	}
	if len(rows) != 1 {
		t.Fatalf("已安装行数 = %d，期望 1", len(rows))
	}
	row := rows[0]
	if row.SourceName != sourceNameAdoptium {
		t.Errorf("来源名 = %q，期望 %q", row.SourceName, sourceNameAdoptium)
	}
	if row.SHA256 != adoptiumTestSHA256(zipBody) {
		t.Errorf("sha256 = %q，与归档校验值不一致", row.SHA256)
	}
	if row.SHA1 != "" {
		t.Errorf("sha1 = %q，第三方来源不得把 sha256 塞进 sha1", row.SHA1)
	}
	if row.FullVersion != "21.0.13+9-LTS" {
		t.Errorf("完整版本 = %q，期望 openjdk_version", row.FullVersion)
	}
	if row.Component != "" {
		t.Errorf("component = %q，第三方来源应为空", row.Component)
	}
	if row.Vendor != VendorOpenJDK {
		t.Errorf("vendor = %q，期望 %q", row.Vendor, VendorOpenJDK)
	}

	// 第一次安装应产生「下载中 → 提交中 → 完成」三类事件，末尾必须是 completed。
	if len(fixture.events) == 0 {
		t.Fatal("未收到任何进度事件")
	}
	if last := fixture.events[len(fixture.events)-1]; last.State != runtimeEventStateCompleted {
		t.Errorf("末次事件状态 = %q，期望 %q", last.State, runtimeEventStateCompleted)
	}

	archiveCallsBefore := server.archiveCalls
	again, appErr := InstallRuntimeFromSource(InstallRuntimeSourceRequest{MajorVersion: 21, AcceptLicense: true})
	if appErr != nil {
		t.Fatalf("重复安装应直接返回成功: %v", appErr)
	}
	if !again.Duplicate {
		t.Error("重复安装应标记 Duplicate=true")
	}
	if server.archiveCalls != archiveCallsBefore {
		t.Errorf("重复安装不应重新下载归档（归档请求 %d → %d）", archiveCallsBefore, server.archiveCalls)
	}
}

// TestInstallRuntimeFromSourceRejectsChecksumMismatch 验证 sha256 不匹配时拒绝落盘并清理 staging。
func TestInstallRuntimeFromSourceRejectsChecksumMismatch(t *testing.T) {
	requireAdoptiumPlatform(t)

	zipBody := adoptiumTestZip(t, map[string]string{"jdk-21.0.13+9/bin/java.exe": "java"})
	link := "https://github.com/adoptium/temurin21-binaries/releases/download/jdk-21.0.13%2B9/a.zip"
	server := &adoptiumTestServer{
		apiBodies:   map[int][]byte{21: adoptiumTestAssetsBody(t, link, strings.Repeat("b", 64), int64(len(zipBody)))},
		archiveBody: zipBody,
	}
	fixture := newAdoptiumTestFixture(t, server)

	_, appErr := InstallRuntimeFromSource(InstallRuntimeSourceRequest{MajorVersion: 21, AcceptLicense: true})
	requireCode(t, appErr, ErrorCodeRuntimeInstall)
	if appErr.Retryable {
		t.Error("校验失败不应标记为可重试")
	}

	entries, err := os.ReadDir(fixture.runtimeDir())
	if err == nil {
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), runtimeStagingPrefix) {
				t.Errorf("staging 目录未清理: %s", entry.Name())
			}
		}
	}

	rows, appErr := installedRuntimeRows(context.Background())
	if appErr != nil {
		t.Fatalf("读取已安装 Runtime 失败: %v", appErr)
	}
	if len(rows) != 0 {
		t.Errorf("校验失败后不应写入数据库，实际 %d 行", len(rows))
	}
}

// TestInstallRuntimeFromSourceRejectsMultipleTopLevelEntries 验证归档顶层结构非法时失败。
func TestInstallRuntimeFromSourceRejectsMultipleTopLevelEntries(t *testing.T) {
	requireAdoptiumPlatform(t)

	zipBody := adoptiumTestZip(t, map[string]string{
		"jdk-21.0.13+9/bin/java.exe": "java",
		"extra/readme.txt":           "hello",
	})
	link := "https://github.com/adoptium/temurin21-binaries/releases/download/jdk-21.0.13%2B9/a.zip"
	server := &adoptiumTestServer{
		apiBodies:   map[int][]byte{21: adoptiumTestAssetsBody(t, link, adoptiumTestSHA256(zipBody), int64(len(zipBody)))},
		archiveBody: zipBody,
	}
	newAdoptiumTestFixture(t, server)

	_, appErr := InstallRuntimeFromSource(InstallRuntimeSourceRequest{MajorVersion: 21, AcceptLicense: true})
	requireCode(t, appErr, ErrorCodeRuntimeUnpack)
}

// TestInstallRuntimeFromSourceRejectsMissingExecutable 验证归档缺少 java 可执行文件时失败。
func TestInstallRuntimeFromSourceRejectsMissingExecutable(t *testing.T) {
	requireAdoptiumPlatform(t)

	zipBody := adoptiumTestZip(t, map[string]string{"jdk-21.0.13+9/lib/modules": "modules"})
	link := "https://github.com/adoptium/temurin21-binaries/releases/download/jdk-21.0.13%2B9/a.zip"
	server := &adoptiumTestServer{
		apiBodies:   map[int][]byte{21: adoptiumTestAssetsBody(t, link, adoptiumTestSHA256(zipBody), int64(len(zipBody)))},
		archiveBody: zipBody,
	}
	newAdoptiumTestFixture(t, server)

	_, appErr := InstallRuntimeFromSource(InstallRuntimeSourceRequest{MajorVersion: 21, AcceptLicense: true})
	requireCode(t, appErr, ErrorCodeRuntimeUnpack)
}

// TestInstallRuntimeFromSourceRejectsLicense 验证未接受许可时直接拒绝且不发起请求。
func TestInstallRuntimeFromSourceRejectsLicense(t *testing.T) {
	server := &adoptiumTestServer{}
	newAdoptiumTestFixture(t, server)

	_, appErr := InstallRuntimeFromSource(InstallRuntimeSourceRequest{MajorVersion: 21, AcceptLicense: false})
	// 许可校验发生在任何网络请求之前，错误码沿用 P3-09 的安装错误码（不是「需求不支持」）。
	requireCode(t, appErr, ErrorCodeRuntimeInstall)
	if appErr.Message != messageRuntimeLicenseRequired {
		t.Errorf("提示文案 = %q，期望 %q", appErr.Message, messageRuntimeLicenseRequired)
	}
	if server.apiCalls != 0 {
		t.Errorf("未接受许可时不应发起 API 请求，实际 %d 次", server.apiCalls)
	}
}

// TestCheckRuntimeUpdates 验证更新检查的请求去重、判定与失败隔离。
func TestCheckRuntimeUpdates(t *testing.T) {
	requireAdoptiumPlatform(t)

	server := &adoptiumTestServer{
		apiBodies: map[int][]byte{
			21: adoptiumTestAssetsBody(t, "https://github.com/adoptium/temurin21-binaries/releases/download/jdk-21.0.13%2B9/a.zip", strings.Repeat("c", 64), 100),
			17: []byte(`[]`),
		},
	}
	newAdoptiumTestFixture(t, server)

	ctx := context.Background()
	// 两行同大版本（21），用于验证「同一 major 只请求一次」。
	for _, id := range []string{"adoptium-windows-x64-21.0.12.1", "adoptium-windows-x64-21.0.10"} {
		row := runtimeRow{
			ID: id, Kind: SourceDownloaded, Component: "",
			MajorVersion: 21, FullVersion: "21.0.12.1", Platform: currentPlatformName(),
			Arch: currentArch(), Bitness: bitnessValue(currentBitness()), Vendor: VendorOpenJDK,
			SHA256: strings.Repeat("c", 64), SourceName: sourceNameAdoptium,
			LocalRelPath: filepath.Join("runtimes", id), CreatedAtMs: 1,
		}
		if appErr := saveRuntime(ctx, row); appErr != nil {
			t.Fatalf("写入测试 Runtime 行失败: %v", appErr)
		}
	}
	// 一行 17，API 返回空数组 → 该项应记录检查失败而其它项不受影响。
	row17 := runtimeRow{
		ID: "adoptium-windows-x64-17.0.9", Kind: SourceDownloaded, MajorVersion: 17,
		FullVersion: "17.0.9", Platform: currentPlatformName(), Arch: currentArch(),
		Bitness: bitnessValue(currentBitness()), Vendor: VendorOpenJDK,
		SHA256: strings.Repeat("d", 64), SourceName: sourceNameAdoptium,
		LocalRelPath: filepath.Join("runtimes", "adoptium-windows-x64-17.0.9"), CreatedAtMs: 2,
	}
	if appErr := saveRuntime(ctx, row17); appErr != nil {
		t.Fatalf("写入测试 Runtime 行失败: %v", appErr)
	}
	// 一行非 Adoptium 来源，应被忽略。
	if appErr := saveRuntime(ctx, runtimeRow{
		ID: "java-runtime-gamma-windows-x64-21.0.5", Kind: SourceDownloaded,
		Component: "java-runtime-gamma", MajorVersion: 21, FullVersion: "21.0.5",
		Platform: currentPlatformName(), Arch: currentArch(), SourceName: "",
		LocalRelPath: filepath.Join("runtimes", "gamma"), CreatedAtMs: 3,
	}); appErr != nil {
		t.Fatalf("写入测试 Runtime 行失败: %v", appErr)
	}

	updates, appErr := CheckRuntimeUpdates()
	if appErr != nil {
		t.Fatalf("检查更新失败: %v", appErr)
	}
	if server.apiCalls != 2 {
		t.Errorf("API 请求次数 = %d，期望 2（21 与 17 各一次）", server.apiCalls)
	}
	if len(updates) != 3 {
		t.Fatalf("返回条目数 = %d，期望 3（忽略非 Adoptium 行）", len(updates))
	}

	byID := map[string]RuntimeUpdateInfo{}
	for _, item := range updates {
		byID[item.RuntimeID] = item
	}

	newer := byID["adoptium-windows-x64-21.0.12.1"]
	if !newer.UpdateAvailable {
		t.Error("21.0.12.1 → 21.0.13+9-LTS 应判定为有更新")
	}
	if newer.LatestFullVersion != "21.0.13+9-LTS" {
		t.Errorf("最新版本 = %q，期望 21.0.13+9-LTS", newer.LatestFullVersion)
	}
	if newer.CheckError != "" {
		t.Errorf("成功项不应有错误，实际 %q", newer.CheckError)
	}
	if newer.CheckedAtMs == 0 {
		t.Error("应记录检查时间")
	}

	failed := byID["adoptium-windows-x64-17.0.9"]
	if failed.CheckError == "" {
		t.Error("API 返回空数组时应记录检查失败原因")
	}
	if failed.UpdateAvailable {
		t.Error("检查失败时不得判定为有更新")
	}

	// 结果应写回数据库，供不联网的 ListRuntimeUpdates 读取。
	listed, listErr := ListRuntimeUpdates()
	if listErr != nil {
		t.Fatalf("读取更新缓存失败: %v", listErr)
	}
	if len(listed) != 3 {
		t.Fatalf("缓存条目数 = %d，期望 3", len(listed))
	}
}

// TestListRuntimeUpdatesDoesNotUseNetwork 验证只读接口零网络。
func TestListRuntimeUpdatesDoesNotUseNetwork(t *testing.T) {
	server := &adoptiumTestServer{}
	fixture := newAdoptiumTestFixture(t, server)

	ctx := context.Background()
	if appErr := saveRuntime(ctx, runtimeRow{
		ID: "adoptium-windows-x64-21.0.13", Kind: SourceDownloaded, MajorVersion: 21,
		FullVersion: "21.0.13+9-LTS", Platform: currentPlatformName(), Arch: currentArch(),
		SourceName: sourceNameAdoptium, SHA256: strings.Repeat("e", 64),
		LocalRelPath: filepath.Join("runtimes", "x"), CreatedAtMs: 1,
		LatestCheckedAtMs: 1700000000000, LatestFullVersion: "21.0.13+9-LTS",
	}); appErr != nil {
		t.Fatalf("写入测试 Runtime 行失败: %v", appErr)
	}

	updates, appErr := ListRuntimeUpdates()
	if appErr != nil {
		t.Fatalf("读取更新缓存失败: %v", appErr)
	}
	if len(updates) != 1 {
		t.Fatalf("条目数 = %d，期望 1", len(updates))
	}
	if updates[0].LatestFullVersion != "21.0.13+9-LTS" {
		t.Errorf("最新版本 = %q，期望来自缓存列", updates[0].LatestFullVersion)
	}
	if updates[0].CheckedAtMs != 1700000000000 {
		t.Errorf("检查时间 = %d，期望来自缓存列", updates[0].CheckedAtMs)
	}
	if server.apiCalls != 0 || server.archiveCalls != 0 {
		t.Errorf("ListRuntimeUpdates 不得联网，实际 API %d 次 / 归档 %d 次", server.apiCalls, server.archiveCalls)
	}
	_ = fixture
}

// TestGetRuntimeSourceInfo 验证来源信息聚合与镜像开关。
func TestGetRuntimeSourceInfo(t *testing.T) {
	requireAdoptiumPlatform(t)

	zipBody := []byte("zip")
	link := "https://github.com/adoptium/temurin21-binaries/releases/download/jdk-21.0.13%2B9/a.zip"
	server := &adoptiumTestServer{
		apiBodies:   map[int][]byte{21: adoptiumTestAssetsBody(t, link, adoptiumTestSHA256(zipBody), int64(len(zipBody)))},
		archiveBody: zipBody,
	}
	fixture := newAdoptiumTestFixture(t, server)

	info, appErr := GetRuntimeSourceInfo(21)
	if appErr != nil {
		t.Fatalf("获取来源信息失败: %v", appErr)
	}
	if info.Source != sourceNameAdoptium {
		t.Errorf("来源 = %q，期望 %q", info.Source, sourceNameAdoptium)
	}
	if info.Semver != "21.0.13+9.0.LTS" || info.FullVersion != "21.0.13+9-LTS" {
		t.Errorf("版本字段异常: semver=%q fullVersion=%q", info.Semver, info.FullVersion)
	}
	if len(info.DownloadHosts) != 1 || info.DownloadHosts[0] != adoptiumOfficialHost {
		t.Errorf("镜像关闭时来源 host = %v，期望只含 %s", info.DownloadHosts, adoptiumOfficialHost)
	}
	for _, host := range info.DownloadHosts {
		if strings.Contains(host, "/") {
			t.Errorf("DownloadHosts 只应包含 host，实际含路径: %s", host)
		}
	}

	fixture.mirrorEnabled = true
	mirrored, appErr := GetRuntimeSourceInfo(21)
	if appErr != nil {
		t.Fatalf("镜像开启时获取来源信息失败: %v", appErr)
	}
	if len(mirrored.DownloadHosts) != 2 {
		t.Errorf("镜像开启时来源 host 数量 = %d，期望 2", len(mirrored.DownloadHosts))
	}

	_, appErr = GetRuntimeSourceInfo(0)
	requireCode(t, appErr, ErrorCodeRuntimeRequirement)
}

// TestReadMirrorEnabledSetting 验证镜像开关的唯一读法来自下载模块的 Provider。
//
// P3-15（D-57）之后本包不再自己打开设置存储读 key：装配层把设置服务里的
// `download.mirrorEnabled` 包成函数注入 internal/download，本包与下载包共用
// 同一个读法。这里验证三件事：未注入时默认关闭（安全默认）、注入后跟随、
// 运行期切换能立刻生效（不需要重建任何对象）。
func TestReadMirrorEnabledSetting(t *testing.T) {
	// 把间接变量指回真实读法：fixture 会把它替换成固定值，会掩盖真实行为。
	original := adoptiumMirrorEnabled
	adoptiumMirrorEnabled = download.MirrorEnabledFromProvider
	t.Cleanup(func() {
		adoptiumMirrorEnabled = original
		download.SetMirrorEnabledProvider(nil)
	})

	// 未注入 Provider：必须关闭。镜像只是加速手段，缺失配置时不该启用。
	download.SetMirrorEnabledProvider(nil)
	if got := readMirrorEnabledSetting(); got {
		t.Error("未注入 Provider 时应关闭镜像")
	}
	if got := adoptiumMirrorEnabled(); got {
		t.Error("调用方使用的间接变量也应看到关闭状态")
	}

	download.SetMirrorEnabledProvider(func() bool { return true })
	if got := readMirrorEnabledSetting(); !got {
		t.Error("Provider 返回 true 时应开启镜像")
	}

	// 运行期切换：用户改设置后不重启也应生效，说明没有缓存住首次结果。
	download.SetMirrorEnabledProvider(func() bool { return false })
	if got := readMirrorEnabledSetting(); got {
		t.Error("Provider 切回 false 后应关闭镜像")
	}
}
