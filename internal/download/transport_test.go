// 本文件测试真实 HTTP 传输层（P3-05 第 1~8、12、13 条）。
//
// 铁律：本文件**不发起任何真实网络请求**。所有用例都注入假的 http.RoundTripper，
// 甚至连 DNS 解析都不会发生；一旦有人回退到 http.DefaultClient，这些用例会立刻
// 因「真实 host 无法访问」而失败。
package download

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"example.com/ninevoxel/launcher/internal/logging"
)

// stubRoundTripper 是记录请求并返回预置响应的假传输层。
type stubRoundTripper struct {
	mutex sync.Mutex
	calls []*http.Request
	fn    func(request *http.Request) (*http.Response, error)
}

// RoundTrip 实现 http.RoundTripper。
func (s *stubRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	s.mutex.Lock()
	s.calls = append(s.calls, request)
	s.mutex.Unlock()
	return s.fn(request)
}

// count 返回已收到的请求数。
func (s *stubRoundTripper) count() int {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	return len(s.calls)
}

// request 返回第 index 个请求（越界返回 nil）。
func (s *stubRoundTripper) request(index int) *http.Request {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	if index < 0 || index >= len(s.calls) {
		return nil
	}
	return s.calls[index]
}

// fakeStagingSink 记录 staging 打开方式并接收写入的字节。
type fakeStagingSink struct {
	openCalls int
	offset    int64
	truncate  bool
	body      strings.Builder
	flushes   int
	closed    bool
}

// Open 记录本次打开参数。
func (s *fakeStagingSink) Open(_ context.Context, _ Task, offset int64, truncate bool) (StagingWriter, error) {
	s.openCalls++
	s.offset = offset
	s.truncate = truncate
	s.body.Reset()
	return s, nil
}

// Write 收集写入内容。
func (s *fakeStagingSink) Write(chunk []byte) (int, error) {
	return s.body.Write(chunk)
}

// Flush 记录 flush 次数。
func (s *fakeStagingSink) Flush() error {
	s.flushes++
	return nil
}

// Close 记录关闭。
func (s *fakeStagingSink) Close() error {
	s.closed = true
	return nil
}

// len 返回已写入字节数。
func (s *fakeStagingSink) len() int {
	return s.body.Len()
}

// recordingLogger 记录日志字段。
type recordingLogger struct {
	infos []logging.Fields
	warns []logging.Fields
}

// Info 记录 info 日志。
func (l *recordingLogger) Info(_ string, fields logging.Fields) {
	l.infos = append(l.infos, fields)
}

// Warn 记录 warn 日志。
func (l *recordingLogger) Warn(_ string, fields logging.Fields) {
	l.warns = append(l.warns, fields)
}

// stubResponse 构造一个假响应。
func stubResponse(status int, body string, contentLength int64, headers map[string]string) *http.Response {
	response := &http.Response{
		StatusCode:    status,
		Body:          io.NopCloser(strings.NewReader(body)),
		Header:        make(http.Header),
		ContentLength: contentLength,
	}
	for key, value := range headers {
		response.Header.Set(key, value)
	}
	return response
}

// newTestTransport 构造一个不联网的传输层。
func newTestTransport(rt *stubRoundTripper, sink *fakeStagingSink, registry *SourceRegistry) *HTTPTransport {
	if registry == nil {
		registry = NewSourceRegistry(false)
	}
	return NewHTTPTransport(HTTPTransportOptions{
		RoundTripper: rt,
		Registry:     registry,
		Sink:         sink,
		AppVersion:   "0.3.0-test",
		Now:          func() time.Time { return time.Unix(0, 0) },
	})
}

const officialLibraryURL = "https://libraries.minecraft.net/com/example/lib-1.0.jar"

// TestTransferRejectsUnsafeSourceBeforeAnyRequest 验证地址校验发生在发请求之前。
func TestTransferRejectsUnsafeSourceBeforeAnyRequest(t *testing.T) {
	cases := []struct {
		name   string
		source string
	}{
		{name: "http 协议", source: "http://libraries.minecraft.net/com/x.jar"},
		{name: "非白名单 host", source: "https://evil.example.com/com/x.jar"},
		{name: "空地址", source: ""},
		{name: "file 协议", source: "file:///C:/x.jar"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			roundTripper := &stubRoundTripper{fn: func(*http.Request) (*http.Response, error) {
				t.Fatalf("不安全地址不应发起请求")
				return nil, nil
			}}
			sink := &fakeStagingSink{}
			transport := newTestTransport(roundTripper, sink, nil)

			result := transport.Transfer(context.Background(), Task{SourceURI: testCase.source, ExpectedSize: 10})
			if result.Err == nil {
				t.Fatalf("期望失败，实际成功")
			}
			if result.ErrorCode != ErrorCodeSource {
				t.Fatalf("错误码 = %q，期望 %q", result.ErrorCode, ErrorCodeSource)
			}
			if result.Retryable {
				t.Fatalf("不安全地址不应可重试")
			}
			if roundTripper.count() != 0 {
				t.Fatalf("请求数 = %d，期望 0", roundTripper.count())
			}
		})
	}
}

// TestTransferRejectsInvalidExpectedSize 验证大小前置条件（第 8 条与遗留第 6 条）。
func TestTransferRejectsInvalidExpectedSize(t *testing.T) {
	cases := []struct {
		name         string
		expectedSize int64
	}{
		{name: "零", expectedSize: 0},
		{name: "负数", expectedSize: -1},
		{name: "超过 1GiB 上限", expectedSize: maxTransferSize + 1},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			roundTripper := &stubRoundTripper{fn: func(*http.Request) (*http.Response, error) {
				t.Fatalf("非法大小不应发起请求")
				return nil, nil
			}}
			transport := newTestTransport(roundTripper, &fakeStagingSink{}, nil)
			result := transport.Transfer(context.Background(), Task{
				SourceURI:    officialLibraryURL,
				ExpectedSize: testCase.expectedSize,
			})
			if result.ErrorCode != ErrorCodeSize {
				t.Fatalf("错误码 = %q，期望 %q", result.ErrorCode, ErrorCodeSize)
			}
			if roundTripper.count() != 0 {
				t.Fatalf("请求数 = %d，期望 0", roundTripper.count())
			}
		})
	}
}

// TestTransferFailsExplicitlyWithoutStagingSink 验证暂存接缝缺失时不伪造成功。
func TestTransferFailsExplicitlyWithoutStagingSink(t *testing.T) {
	roundTripper := &stubRoundTripper{fn: func(*http.Request) (*http.Response, error) {
		t.Fatalf("暂存缺失时不应发起请求")
		return nil, nil
	}}
	transport := NewHTTPTransport(HTTPTransportOptions{
		RoundTripper: roundTripper,
		Registry:     NewSourceRegistry(false),
	})

	result := transport.Transfer(context.Background(), Task{SourceURI: officialLibraryURL, ExpectedSize: 10})
	if result.Err == nil {
		t.Fatalf("期望失败，实际成功")
	}
	if result.Retryable {
		t.Fatalf("暂存缺失不应可重试")
	}
	if roundTripper.count() != 0 {
		t.Fatalf("请求数 = %d，期望 0", roundTripper.count())
	}
}

// TestTransferWritesFullBodyOn200 验证全新下载（200）从头写入。
func TestTransferWritesFullBodyOn200(t *testing.T) {
	payload := strings.Repeat("x", 100)
	roundTripper := &stubRoundTripper{fn: func(*http.Request) (*http.Response, error) {
		return stubResponse(200, payload, 100, nil), nil
	}}
	sink := &fakeStagingSink{}
	transport := newTestTransport(roundTripper, sink, nil)

	result := transport.Transfer(context.Background(), Task{SourceURI: officialLibraryURL, ExpectedSize: 100})
	if result.Err != nil {
		t.Fatalf("期望成功，实际错误：%v", result.Err)
	}
	if result.Downloaded != 100 {
		t.Fatalf("Downloaded = %d，期望 100", result.Downloaded)
	}
	if result.TotalSize != 100 {
		t.Fatalf("TotalSize = %d，期望 100", result.TotalSize)
	}
	if sink.openCalls != 1 {
		t.Fatalf("Open 调用次数 = %d，期望 1", sink.openCalls)
	}
	if sink.offset != 0 || !sink.truncate {
		t.Fatalf("Open(offset=%d, truncate=%v)，期望 (0, true)", sink.offset, sink.truncate)
	}
	if sink.len() != 100 {
		t.Fatalf("写入字节 = %d，期望 100", sink.len())
	}
	if !sink.closed {
		t.Fatalf("写入句柄未关闭")
	}
}

// TestTransferResumesOn206 验证续传：从本地偏移追加，不截断。
func TestTransferResumesOn206(t *testing.T) {
	payload := strings.Repeat("y", 50)
	roundTripper := &stubRoundTripper{fn: func(*http.Request) (*http.Response, error) {
		return stubResponse(206, payload, 50, map[string]string{
			"Content-Range": "bytes 50-99/100",
			"ETag":          `"v1"`,
		}), nil
	}}
	sink := &fakeStagingSink{}
	transport := newTestTransport(roundTripper, sink, nil)

	task := Task{
		SourceURI:      officialLibraryURL,
		ExpectedSize:   100,
		RangeStart:     50,
		DownloadedSize: 50,
		ETag:           `"v1"`,
	}
	result := transport.Transfer(context.Background(), task)
	if result.Err != nil {
		t.Fatalf("期望成功，实际错误：%v", result.Err)
	}
	if result.Downloaded != 50 {
		t.Fatalf("Downloaded = %d，期望 50", result.Downloaded)
	}
	if sink.offset != 50 || sink.truncate {
		t.Fatalf("Open(offset=%d, truncate=%v)，期望 (50, false)", sink.offset, sink.truncate)
	}
	if sink.len() != 50 {
		t.Fatalf("写入字节 = %d，期望 50", sink.len())
	}

	rangeHeader := roundTripper.request(0).Header.Get("Range")
	if rangeHeader != "bytes=50-" {
		t.Fatalf("Range = %q，期望 bytes=50-", rangeHeader)
	}
}

// TestTransferRestartsWhenServerIgnoresRange 是第 7 条的核心用例：
// 服务器忽略 Range 返回 200 时，必须丢弃已写字节从头写，绝不追加。
func TestTransferRestartsWhenServerIgnoresRange(t *testing.T) {
	payload := strings.Repeat("z", 100)
	roundTripper := &stubRoundTripper{fn: func(*http.Request) (*http.Response, error) {
		return stubResponse(200, payload, 100, nil), nil
	}}
	sink := &fakeStagingSink{}
	transport := newTestTransport(roundTripper, sink, nil)

	task := Task{
		SourceURI:      officialLibraryURL,
		ExpectedSize:   100,
		RangeStart:     50,
		DownloadedSize: 50,
		ETag:           `"v1"`,
	}
	result := transport.Transfer(context.Background(), task)
	if result.Err != nil {
		t.Fatalf("期望成功，实际错误：%v", result.Err)
	}
	if sink.offset != 0 || !sink.truncate {
		t.Fatalf("Open(offset=%d, truncate=%v)，期望 (0, true)：必须全量重写", sink.offset, sink.truncate)
	}
	if sink.len() != 100 {
		t.Fatalf("写入字节 = %d，期望 100", sink.len())
	}
	// 请求仍然带 Range（探测能力），是否遵守由服务器决定。
	if rangeHeader := roundTripper.request(0).Header.Get("Range"); rangeHeader != "bytes=50-" {
		t.Fatalf("Range = %q，期望 bytes=50-", rangeHeader)
	}
}

// TestTransferAbortsWhenBodyExceedsExpectedSize 验证第 8 条：越界立即中止。
func TestTransferAbortsWhenBodyExceedsExpectedSize(t *testing.T) {
	payload := strings.Repeat("w", 500)
	roundTripper := &stubRoundTripper{fn: func(*http.Request) (*http.Response, error) {
		return stubResponse(200, payload, 500, nil), nil
	}}
	sink := &fakeStagingSink{}
	transport := newTestTransport(roundTripper, sink, nil)

	result := transport.Transfer(context.Background(), Task{SourceURI: officialLibraryURL, ExpectedSize: 10})
	if result.Err == nil {
		t.Fatalf("期望失败，实际成功")
	}
	if result.ErrorCode != ErrorCodeSize {
		t.Fatalf("错误码 = %q，期望 %q", result.ErrorCode, ErrorCodeSize)
	}
	if sink.len() > 10 {
		t.Fatalf("写入字节 = %d，超过上限 10", sink.len())
	}
}

// TestTransferRejectsTooManyRedirects 验证第 2 条：最多 3 跳。
func TestTransferRejectsTooManyRedirects(t *testing.T) {
	roundTripper := &stubRoundTripper{fn: func(*http.Request) (*http.Response, error) {
		return stubResponse(302, "", 0, map[string]string{
			"Location": "https://libraries.minecraft.net/next.jar",
		}), nil
	}}
	sink := &fakeStagingSink{}
	transport := newTestTransport(roundTripper, sink, nil)

	result := transport.Transfer(context.Background(), Task{SourceURI: officialLibraryURL, ExpectedSize: 10})
	if result.Err == nil {
		t.Fatalf("期望因重定向过多失败")
	}
	if result.ErrorCode != ErrorCodeSource {
		t.Fatalf("错误码 = %q，期望 %q", result.ErrorCode, ErrorCodeSource)
	}
	// 初始请求 + 3 次允许的跳转 = 4 次；第 4 次跳转前被拒绝。
	if got := roundTripper.count(); got != 4 {
		t.Fatalf("请求数 = %d，期望 4", got)
	}
	if sink.openCalls != 0 {
		t.Fatalf("被拒绝的重定向不应打开暂存写入")
	}
}

// TestTransferRejectsRedirectToNonWhitelistedHost 验证每一跳都要过白名单。
func TestTransferRejectsRedirectToNonWhitelistedHost(t *testing.T) {
	roundTripper := &stubRoundTripper{fn: func(*http.Request) (*http.Response, error) {
		return stubResponse(302, "", 0, map[string]string{
			"Location": "https://evil.example.com/steal.jar",
		}), nil
	}}
	sink := &fakeStagingSink{}
	transport := newTestTransport(roundTripper, sink, nil)

	result := transport.Transfer(context.Background(), Task{SourceURI: officialLibraryURL, ExpectedSize: 10})
	if result.Err == nil || result.ErrorCode != ErrorCodeSource {
		t.Fatalf("期望白名单拒绝，实际：%v (%s)", result.Err, result.ErrorCode)
	}
	if roundTripper.count() != 1 {
		t.Fatalf("请求数 = %d，期望 1（第二跳不应发出）", roundTripper.count())
	}
}

// TestTransferFallsBackToMirror 验证镜像回退：官方 5xx 后改用镜像候选。
func TestTransferFallsBackToMirror(t *testing.T) {
	roundTripper := &stubRoundTripper{fn: func(request *http.Request) (*http.Response, error) {
		if request.URL.Hostname() == defaultMirrorHost {
			return stubResponse(200, strings.Repeat("m", 20), 20, nil), nil
		}
		return stubResponse(503, "", 0, nil), nil
	}}
	sink := &fakeStagingSink{}
	transport := newTestTransport(roundTripper, sink, NewSourceRegistry(true))

	result := transport.Transfer(context.Background(), Task{SourceURI: officialLibraryURL, ExpectedSize: 20})
	if result.Err != nil {
		t.Fatalf("镜像回退后应成功，实际错误：%v", result.Err)
	}
	if result.Downloaded != 20 {
		t.Fatalf("Downloaded = %d，期望 20", result.Downloaded)
	}
	if roundTripper.count() != 2 {
		t.Fatalf("请求数 = %d，期望 2（官方 + 镜像）", roundTripper.count())
	}
	if host := roundTripper.request(0).URL.Hostname(); host != "libraries.minecraft.net" {
		t.Fatalf("第 1 次请求 host = %q，期望官方 host", host)
	}
	if host := roundTripper.request(1).URL.Hostname(); host != defaultMirrorHost {
		t.Fatalf("第 2 次请求 host = %q，期望镜像 host", host)
	}
}

// TestTransferSkipsRequestWhenPartAlreadyComplete 验证本地已满时不重复下载。
func TestTransferSkipsRequestWhenPartAlreadyComplete(t *testing.T) {
	roundTripper := &stubRoundTripper{fn: func(*http.Request) (*http.Response, error) {
		t.Fatalf("本地已满时不应发起请求")
		return nil, nil
	}}
	sink := &fakeStagingSink{}
	transport := newTestTransport(roundTripper, sink, nil)

	task := Task{
		SourceURI:    officialLibraryURL,
		ExpectedSize: 100,
		RangeStart:   100,
		ETag:         `"v1"`,
	}
	result := transport.Transfer(context.Background(), task)
	if result.Err != nil {
		t.Fatalf("期望成功，实际错误：%v", result.Err)
	}
	if result.Downloaded != 0 {
		t.Fatalf("Downloaded = %d，期望 0", result.Downloaded)
	}
	if result.TotalSize != 100 {
		t.Fatalf("TotalSize = %d，期望 100", result.TotalSize)
	}
	if sink.openCalls != 0 {
		t.Fatalf("不应打开暂存写入")
	}
}

// TestTransferRequestHeadersAreMinimal 验证第 4、12 条：请求头固定且不含身份信息。
func TestTransferRequestHeadersAreMinimal(t *testing.T) {
	roundTripper := &stubRoundTripper{fn: func(*http.Request) (*http.Response, error) {
		return stubResponse(200, "abc", 3, nil), nil
	}}
	transport := newTestTransport(roundTripper, &fakeStagingSink{}, nil)

	result := transport.Transfer(context.Background(), Task{SourceURI: officialLibraryURL, ExpectedSize: 3})
	if result.Err != nil {
		t.Fatalf("期望成功，实际错误：%v", result.Err)
	}

	request := roundTripper.request(0)
	if request.Method != "GET" {
		t.Fatalf("Method = %q，期望 GET", request.Method)
	}
	if agent := request.Header.Get("User-Agent"); agent != "NineVoxelLauncher/0.3.0-test" {
		t.Fatalf("User-Agent = %q", agent)
	}
	if encoding := request.Header.Get("Accept-Encoding"); encoding != "identity" {
		t.Fatalf("Accept-Encoding = %q，期望 identity", encoding)
	}
	if cookie := request.Header.Get("Cookie"); cookie != "" {
		t.Fatalf("不应发送 Cookie，实际 %q", cookie)
	}
	if authorization := request.Header.Get("Authorization"); authorization != "" {
		t.Fatalf("不应发送认证头，实际 %q", authorization)
	}
	if rangeHeader := request.Header.Get("Range"); rangeHeader != "bytes=0-" {
		t.Fatalf("首次请求 Range = %q，期望 bytes=0-", rangeHeader)
	}
}

// TestTransferLogsStayRedacted 验证第 13 条：日志只含 host 与路径前缀。
func TestTransferLogsStayRedacted(t *testing.T) {
	source := officialLibraryURL + "?signature=super-secret-token"
	roundTripper := &stubRoundTripper{fn: func(*http.Request) (*http.Response, error) {
		return stubResponse(200, "abc", 3, nil), nil
	}}
	logger := &recordingLogger{}
	transport := NewHTTPTransport(HTTPTransportOptions{
		RoundTripper: roundTripper,
		Registry:     NewSourceRegistry(false),
		Sink:         &fakeStagingSink{},
		Logger:       logger,
		AppVersion:   "0.3.0-test",
	})

	// 带查询串的地址本身会被白名单放行，但日志里绝不能出现它。
	result := transport.Transfer(context.Background(), Task{SourceURI: source, ExpectedSize: 3})
	if result.Err != nil {
		t.Fatalf("期望成功，实际错误：%v", result.Err)
	}
	if len(logger.infos) != 1 {
		t.Fatalf("info 日志条数 = %d，期望 1", len(logger.infos))
	}
	attributes := logger.infos[0].Attributes
	if attributes["host"] != "libraries.minecraft.net" {
		t.Fatalf("host 字段 = %q", attributes["host"])
	}
	if attributes["pathPrefix"] != "com/example" {
		t.Fatalf("pathPrefix 字段 = %q", attributes["pathPrefix"])
	}
	for key, value := range attributes {
		if strings.Contains(value, "secret") {
			t.Fatalf("字段 %s 泄漏了查询串内容：%q", key, value)
		}
		if strings.Contains(value, "?") {
			t.Fatalf("字段 %s 含完整 URL 痕迹：%q", key, value)
		}
	}
}

// TestTransferHonoursContextCancel 验证第 3 条：响应 context 取消且不可重试。
func TestTransferHonoursContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	roundTripper := &stubRoundTripper{fn: func(request *http.Request) (*http.Response, error) {
		// 真实 client 在 ctx 已取消时会直接返回错误；这里模拟同样行为。
		return nil, request.Context().Err()
	}}
	transport := newTestTransport(roundTripper, &fakeStagingSink{}, nil)

	result := transport.Transfer(ctx, Task{SourceURI: officialLibraryURL, ExpectedSize: 10})
	if result.Err == nil {
		t.Fatalf("期望取消导致失败")
	}
	if result.Retryable {
		t.Fatalf("取消不应标记为可重试")
	}
}

// TestTransferTimeoutScalesWithSize 验证整体超时估算（第 3 条）。
func TestTransferTimeoutScalesWithSize(t *testing.T) {
	if got := transferTimeout(0); got != transferBaseTimeout {
		t.Fatalf("空文件超时 = %v，期望 %v", got, transferBaseTimeout)
	}
	if got := transferTimeout(256 * 1024); got != transferBaseTimeout+time.Second {
		t.Fatalf("256KiB 超时 = %v，期望 %v", got, transferBaseTimeout+time.Second)
	}
	if got := transferTimeout(maxTransferSize); got != transferMaxTimeout {
		t.Fatalf("1GiB 超时 = %v，期望上限 %v", got, transferMaxTimeout)
	}
}

// TestTransferPartSizePrefersRangeStart 验证本地偏移的来源优先级。
func TestTransferPartSizePrefersRangeStart(t *testing.T) {
	if got := partSize(Task{}); got != 0 {
		t.Fatalf("partSize(空) = %d，期望 0", got)
	}
	if got := partSize(Task{DownloadedSize: 30}); got != 30 {
		t.Fatalf("partSize(仅 DownloadedSize) = %d，期望 30", got)
	}
	if got := partSize(Task{DownloadedSize: 30, RangeStart: 70}); got != 70 {
		t.Fatalf("partSize(两者都有) = %d，期望以 range_start 为准", got)
	}
}

func TestTransferPistonDataClient(t *testing.T) {
	const clientURL = "https://piston-data.mojang.com/v1/objects/e877b6a07acd633fb3bb475002175cec036e7b87/client.jar"
	const payload = "client jar test payload"
	for _, redirect := range []bool{false, true} {
		name := "direct"
		if redirect {
			name = "redirect"
		}
		t.Run(name, func(t *testing.T) {
			sourceURL := clientURL
			if redirect {
				sourceURL = "https://launcher.mojang.com/client.jar"
			}
			roundTripper := &stubRoundTripper{fn: func(request *http.Request) (*http.Response, error) {
				if redirect && request.URL.String() == sourceURL {
					return stubResponse(http.StatusFound, "", 0, map[string]string{"Location": clientURL}), nil
				}
				if request.URL.String() != clientURL {
					t.Fatalf("unexpected request: %s", request.URL)
				}
				return stubResponse(http.StatusOK, payload, int64(len(payload)), nil), nil
			}}
			sink := &fakeStagingSink{}
			transport := newTestTransport(roundTripper, sink, nil)
			result := transport.Transfer(context.Background(), Task{SourceURI: sourceURL, ExpectedSize: int64(len(payload))})
			if result.Err != nil {
				t.Fatal(result.Err)
			}
			if result.Downloaded != int64(len(payload)) || sink.body.String() != payload || !sink.closed {
				t.Fatalf("incomplete transfer: %+v", result)
			}
			wantCalls := 1
			if redirect {
				wantCalls = 2
			}
			if roundTripper.count() != wantCalls {
				t.Fatalf("request count = %d, want %d", roundTripper.count(), wantCalls)
			}
		})
	}
}
