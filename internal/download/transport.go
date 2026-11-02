// 本文件实现真实 HTTP 传输层（P3-05）：单一 GET 流式写入、Range 续传、
// 镜像回退与白名单/重定向约束。
//
// 边界（与相邻卡的分工）：
//   - 本卡不调度（P3-04）、不校验哈希、不重命名、不写 artifacts/objects（P3-06）；
//   - 本卡不实现限速与代理（D-30 已冻结为延后）；
//   - 本包**不含任何文件系统写入调用**：staging 落盘由注入的 StagingSink 承担，
//     因此「新增写盘点」为 0，字节流与偏移决策留在本文件。
//
// 唯一的新增网络点就是 HTTPTransport：所有请求都经 SourceRegistry 校验，
// 只允许 https + 白名单 host（含重定向的每一跳）。
package download

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"example.com/ninevoxel/launcher/internal/logging"
)

const (
	// transferConnectTimeout 是 TCP 连接超时（卡面第 3 条）。
	transferConnectTimeout = 10 * time.Second

	// transferBaseTimeout 是整体超时的固定部分。
	transferBaseTimeout = 30 * time.Second

	// transferSizeAllowancePerSecond 是整体超时按大小追加的余量：每秒 256 KiB。
	transferSizeAllowancePerSecond = int64(256 * 1024)

	// transferMaxTimeout 是单个来源尝试的超时上限：30 分钟。
	transferMaxTimeout = 30 * time.Minute

	// transferMaxRedirects 是允许的最大重定向跳数（卡面第 2 条）。
	transferMaxRedirects = 3

	// transferFlushThreshold 是周期 flush 的字节阈值：每 4 MiB 落一次。
	//
	// 太小会把磁盘 IO 打成随机写，太大则取消/崩溃时丢掉更多已下载字节。
	transferFlushThreshold = int64(4 << 20)

	// transferCopyBufferSize 是单次读写的缓冲区大小。
	transferCopyBufferSize = 32 * 1024

	// maxTransferSize 是单文件大小上限：1 GiB（卡面遗留第 6 条）。
	maxTransferSize = int64(1 << 30)

	// defaultAppVersion 是未注入版本号时使用的占位。
	defaultAppVersion = "dev"

	// userAgentPrefix 是固定 User-Agent 前缀（卡面第 4 条）。
	userAgentPrefix = "NineVoxelLauncher/"

	// sourceLabelOfficial / sourceLabelMirror 用于日志区分来源类别。
	sourceLabelOfficial = "official"
	sourceLabelMirror   = "mirror"
)

// errSizeExceeded 表示写入字节超过预期大小，用于在拷贝循环内部快速中止。
var errSizeExceeded = errors.New("download: size limit exceeded")

// StagingWriter 是 staging 写入句柄。
//
// 之所以要求 Flush：下载中途取消时，已 flush 的字节才是真正落盘的字节，
// 断点续传的 range_start 必须与实际落盘量一致。
type StagingWriter interface {
	// Write 写入一段字节。
	io.Writer

	// Flush 把缓冲内容落盘。
	Flush() error

	// Close 关闭句柄（应同时 flush）。
	Close() error
}

// StagingSink 是 staging 写入接缝，由 P3-06 提供落盘实现。
//
// 本卡只决定「从哪个偏移写、是否截断」，不决定文件路径、
// 不创建目录、不重命名：这些都属于提交与落盘语义。
type StagingSink interface {
	// Open 打开写入句柄。
	//
	// offset 为追加起点；truncate 为 true 表示丢弃已有内容、从 0 开始写。
	// 两者组合由传输决策给出（见 DecideResponse）。
	Open(ctx context.Context, task Task, offset int64, truncate bool) (StagingWriter, error)
}

// TransportLogger 是日志接缝。
//
// *logging.Logger 天然满足该接口；测试可注入记录器，断言日志里
// 不含完整 URL、查询串与路径。
type TransportLogger interface {
	// Info 写入一条 info 日志。
	Info(message string, fields logging.Fields)

	// Warn 写入一条 warn 日志。
	Warn(message string, fields logging.Fields)
}

// HTTPTransportOptions 是传输层构造参数。
//
// Client 与 RoundTripper 二选一：都注入时 RoundTripper 优先（更底层、便于测试）。
type HTTPTransportOptions struct {
	// Client 为可替换的 HTTP 客户端（禁止使用 http.DefaultClient 全局单例）。
	Client *http.Client

	// RoundTripper 为可替换的传输层，通常用于测试注入假实现（不联网）。
	RoundTripper http.RoundTripper

	// Registry 为来源注册表；nil 时按「镜像关闭」构造。
	Registry *SourceRegistry

	// Sink 为 staging 写入接缝；nil 时传输直接返回「暂存尚未接入」的明确失败。
	Sink StagingSink

	// Limiter 为限速接缝；nil 等价于不限速（D-30）。
	Limiter RateLimiter

	// Logger 为日志接缝；nil 时不记录。
	Logger TransportLogger

	// Now 为时钟注入点，测试可替换。
	Now func() time.Time

	// AppVersion 用于拼 User-Agent。
	AppVersion string
}

// HTTPTransport 是 TransferFunc 的真实实现。
type HTTPTransport struct {
	// client 为底层 HTTP 客户端。
	client *http.Client

	// registry 为来源注册表。
	registry *SourceRegistry

	// sink 为 staging 写入接缝。
	sink StagingSink

	// limiter 为限速接缝（默认空实现）。
	limiter RateLimiter

	// logger 为日志接缝。
	logger TransportLogger

	// now 为时钟注入点。
	now func() time.Time

	// appVersion 用于拼 User-Agent。
	appVersion string
}

// NewHTTPTransport 构造传输层实现。
func NewHTTPTransport(opts HTTPTransportOptions) *HTTPTransport {
	registry := opts.Registry
	if registry == nil {
		// 兜底也要读 Provider（D-57）：写死 false 会让镜像开关在未显式注入
		// Registry 的调用点永久失效。
		registry = NewSourceRegistryFromProvider()
	}
	limiter := opts.Limiter
	if limiter == nil {
		limiter = NewNoopRateLimiter()
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	appVersion := strings.TrimSpace(opts.AppVersion)
	if appVersion == "" {
		appVersion = defaultAppVersion
	}

	client := opts.Client
	if client == nil {
		client = &http.Client{
			// 不设 Proxy：D-30 已冻结「不复用系统代理、不支持手配代理」。
			Transport: &http.Transport{
				DialContext: (&net.Dialer{Timeout: transferConnectTimeout}).DialContext,
				// 不猜协议、不启用压缩：Accept-Encoding 显式设为 identity。
				ForceAttemptHTTP2: true,
			},
		}
	}
	if opts.RoundTripper != nil {
		// 复制一份 client 以免污染调用方对象。
		base := *client
		base.Transport = opts.RoundTripper
		client = &base
	}

	return &HTTPTransport{
		client:     client,
		registry:   registry,
		sink:       opts.Sink,
		limiter:    limiter,
		logger:     opts.Logger,
		now:        now,
		appVersion: appVersion,
	}
}

// Transfer 实现 TransferFunc：把任务描述的文件下载到 staging。
//
// 流程：校验地址与大小 → 本地续传决策 → 逐候选（官方→镜像）尝试 →
// 按响应决策追加/重写 → 限额流式写入 → 返回结果。
func (t *HTTPTransport) Transfer(ctx context.Context, task Task) TransferResult {
	startedAt := t.now()

	source := strings.TrimSpace(task.SourceURI)
	if source == "" {
		return failResult(newInputError(ErrorCodeSource, messageSourceEmpty))
	}
	if appErr := t.registry.ValidateURL(source); appErr != nil {
		return failResult(appErr)
	}
	if task.ExpectedSize <= 0 {
		return failResult(newInputError(ErrorCodeSize, messageExpectedSizeInvalid))
	}
	if task.ExpectedSize > maxTransferSize {
		// 单文件上限是常量而不是配置：超过 1 GiB 的单个文件在 P3 不是合法制品。
		return failResult(newInputError(ErrorCodeSize, messageSizeExceeded))
	}
	if t.sink == nil {
		// 接缝未接入属于开发期状态：明确失败，不伪造成功。
		return failResult(newSystemError(ErrorCodeNetwork, messageStagingNotConfigured, false))
	}

	part := PartState{
		Size:         partSize(task),
		ETag:         task.ETag,
		LastModified: task.LastModified,
	}
	plan := PlanResume(part, task.ExpectedSize)
	if plan.Action == ResumeActionComplete {
		// 本地字节已满：本轮不再下载，是否可用由 P3-06 的哈希校验判定。
		t.logTransferSuccess(startedAt, source, sourceLabelOfficial, 0, part.Size, plan.Reason)
		return TransferResult{
			Downloaded:   0,
			TotalSize:    task.ExpectedSize,
			ETag:         part.ETag,
			LastModified: part.LastModified,
		}
	}

	candidates, candidateErr := t.registry.Candidates(source)
	if candidateErr != nil {
		return failResult(candidateErr)
	}

	var lastFailure *Error
	for index, candidate := range candidates {
		label := sourceLabelOfficial
		if index > 0 {
			label = sourceLabelMirror
		}
		result, retryable := t.attempt(ctx, task, plan, part, candidate, label, startedAt)
		if result.Err == nil {
			return result
		}
		lastFailure = toDownloadError(result)
		// 还有候选就回退到下一个来源；否则返回本轮最终失败。
		// 回退只在「本次尝试失败」时发生，不在写入中途发生：
		// 中途失败意味着 body 已写入部分字节，换源续传要重新决策，
		// 交给调度器的下一次重试更安全。
		if index == len(candidates)-1 || retryable == retryableNeverFallback {
			return result
		}
		if retryable == retryableStop {
			return result
		}
	}
	if lastFailure == nil {
		lastFailure = newInputError(ErrorCodeSource, messageSourceRejected)
	}
	return failResult(lastFailure)
}

// fallbackPolicy 表示一次失败是否值得换源重试。
type fallbackPolicy int

const (
	// retryableFallback 表示可以换下一个来源（连接错误、5xx、429）。
	retryableFallback fallbackPolicy = iota

	// retryableStop 表示停止换源但保留调度器重试（不可用来源已确认不可达）。
	retryableStop

	// retryableNeverFallback 表示立即返回，不换源也不再由调度器重试。
	retryableNeverFallback
)

// attempt 对单个候选来源发起一次完整尝试。
func (t *HTTPTransport) attempt(
	ctx context.Context,
	task Task,
	plan ResumePlan,
	part PartState,
	candidate string,
	label string,
	startedAt time.Time,
) (TransferResult, fallbackPolicy) {
	attemptCtx, cancel := context.WithTimeout(ctx, transferTimeout(task.ExpectedSize))
	defer cancel()

	request, buildErr := t.newRequest(attemptCtx, candidate, plan)
	if buildErr != nil {
		return failResult(newInputError(ErrorCodeSource, messageRequestBuildFailed)), retryableNeverFallback
	}

	client := t.clientForAttempt()
	response, doErr := client.Do(request)
	if doErr != nil {
		// 连接失败 / 超时 / 重定向被拒 / context 取消。
		if ctx.Err() != nil {
			return failResult(newExternalError(ErrorCodeNetwork, messageCancelled, false)), retryableNeverFallback
		}
		var denied *redirectDeniedError
		if errors.As(doErr, &denied) {
			return failResult(denied.appErr), retryableNeverFallback
		}
		t.warnTransfer(startedAt, candidate, label, 0, 0, "连接失败")
		return failResult(newExternalError(ErrorCodeNetwork, messageNetworkInterrupted, true)), retryableFallback
	}
	defer func() { _ = response.Body.Close() }()

	resp := RangeResponse{
		StatusCode:    response.StatusCode,
		ContentRange:  response.Header.Get("Content-Range"),
		ETag:          response.Header.Get("ETag"),
		LastModified:  response.Header.Get("Last-Modified"),
		ContentLength: response.ContentLength,
	}
	decision := DecideResponse(plan, part, resp, task.ExpectedSize)

	switch decision.Outcome {
	case OutcomeFail:
		t.warnTransfer(startedAt, candidate, label, response.StatusCode, 0, decision.Reason)
		failure := decision.Failure
		if failure == nil {
			failure = newExternalError(ErrorCodeNetwork, messageNetworkInterrupted, true)
		}
		policy := retryableFallback
		if !failure.Retryable {
			// 不可重试的 4xx：换源仍可能成功（镜像与官方内容一致但可用性不同），
			// 所以这里仍然是回退，只是不再交给调度器重试。
			policy = retryableStop
		}
		return failResult(failure), policy

	case OutcomeComplete:
		t.logTransferSuccess(startedAt, candidate, label, response.StatusCode, 0, decision.Reason)
		return TransferResult{
			Downloaded:   0,
			TotalSize:    decision.TotalSize,
			ETag:         decision.ETag,
			LastModified: decision.LastModified,
		}, retryableNeverFallback

	default:
		return t.writeBody(attemptCtx, task, decision, response, candidate, label, startedAt)
	}
}

// writeBody 按决策动作把响应体写入 staging。
func (t *HTTPTransport) writeBody(
	ctx context.Context,
	task Task,
	decision ResponseDecision,
	response *http.Response,
	candidate string,
	label string,
	startedAt time.Time,
) (TransferResult, fallbackPolicy) {
	truncate := decision.Outcome == OutcomeRewrite
	offset := decision.Offset
	if truncate {
		offset = 0
	}

	writer, openErr := t.sink.Open(ctx, task, offset, truncate)
	if openErr != nil {
		return failResult(newSystemError(ErrorCodeNetwork, messageStagingNotConfigured, false)), retryableStop
	}

	written, copyErr := t.copyBody(ctx, writer, response.Body, task.ExpectedSize, offset)
	flushErr := writer.Flush()
	closeErr := writer.Close()

	if copyErr != nil {
		switch {
		case errors.Is(copyErr, errSizeExceeded):
			t.warnTransfer(startedAt, candidate, label, response.StatusCode, written, "超出预期大小")
			return failResult(newInputError(ErrorCodeSize, messageSizeExceeded)), retryableNeverFallback
		case ctx.Err() != nil:
			t.warnTransfer(startedAt, candidate, label, response.StatusCode, written, "已取消")
			return failResult(newExternalError(ErrorCodeNetwork, messageCancelled, false)), retryableNeverFallback
		default:
			t.warnTransfer(startedAt, candidate, label, response.StatusCode, written, "读取中断")
			// 已写入的字节保留（不回滚）：下一次重试依据 .part 记录续传。
			return failResult(newExternalError(ErrorCodeNetwork, messageNetworkInterrupted, true)), retryableFallback
		}
	}
	if flushErr != nil || closeErr != nil {
		t.warnTransfer(startedAt, candidate, label, response.StatusCode, written, "写入暂存失败")
		return failResult(newSystemError(ErrorCodeNetwork, messageStagingNotConfigured, false)), retryableStop
	}

	t.logTransferSuccess(startedAt, candidate, label, response.StatusCode, written, decision.Reason)

	return TransferResult{
		Downloaded:   written,
		TotalSize:    decision.TotalSize,
		ETag:         decision.ETag,
		LastModified: decision.LastModified,
	}, retryableNeverFallback
}

// copyBody 把响应体写入 writer，并强制不超过剩余预期大小。
//
// 上限语义（卡面第 8 条）：实际写入总字节（本地已有 + 本轮）不得超过 ExpectedSize。
// 一旦越界立即中止，防止恶意源把磁盘写满。
func (t *HTTPTransport) copyBody(
	ctx context.Context,
	writer StagingWriter,
	body io.Reader,
	expectedSize int64,
	offset int64,
) (int64, error) {
	remaining := expectedSize - offset
	if remaining <= 0 {
		return 0, nil
	}

	buffer := make([]byte, transferCopyBufferSize)
	var written int64
	var sinceFlush int64

	for {
		if err := ctx.Err(); err != nil {
			return written, err
		}
		readSize, readErr := body.Read(buffer)
		if readSize > 0 {
			chunk := buffer[:readSize]
			// 超额检查放在写之前：先算出这一块会不会越界，越界就不再动 writer。
			if written+int64(len(chunk)) > remaining {
				allowed := remaining - written
				if allowed > 0 {
					if _, writeErr := writer.Write(chunk[:allowed]); writeErr != nil {
						return written + allowed, writeErr
					}
					written += allowed
				}
				return written, errSizeExceeded
			}
			if waitErr := t.limiter.WaitN(ctx, len(chunk)); waitErr != nil {
				return written, waitErr
			}
			if _, writeErr := writer.Write(chunk); writeErr != nil {
				return written, writeErr
			}
			written += int64(len(chunk))
			sinceFlush += int64(len(chunk))
			if sinceFlush >= transferFlushThreshold {
				if flushErr := writer.Flush(); flushErr != nil {
					return written, flushErr
				}
				sinceFlush = 0
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return written, nil
			}
			return written, readErr
		}
	}
}

// newRequest 构造 GET 请求并按决策附加 Range 头。
func (t *HTTPTransport) newRequest(ctx context.Context, target string, plan ResumePlan) (*http.Request, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", userAgentPrefix+t.appVersion)
	// identity：避免压缩导致的字节数与 ExpectedSize 不一致。
	request.Header.Set("Accept-Encoding", "identity")
	if plan.RangeHeader != "" {
		request.Header.Set("Range", plan.RangeHeader)
	}
	// 明确不发送 Cookie 与任何认证头：下载源不需要身份，带上去只会扩大泄漏面。
	return request, nil
}

// clientForAttempt 返回带强制重定向校验的客户端副本。
//
// 即使调用方注入了自定义 client，也覆盖其 CheckRedirect：
// 否则一个被注入的宽松 client 就能绕过「每跳都必须 https + 白名单」的约束。
func (t *HTTPTransport) clientForAttempt() *http.Client {
	clone := *t.client
	clone.CheckRedirect = t.checkRedirect
	return &clone
}

// redirectDeniedError 承载重定向被拒绝时的结构化错误。
type redirectDeniedError struct {
	// appErr 为要返回给调用方的错误。
	appErr *Error
}

// Error 实现 error 接口。
func (e *redirectDeniedError) Error() string {
	if e == nil || e.appErr == nil {
		return ""
	}
	return e.appErr.Error()
}

// checkRedirect 对每一跳重定向做 https + 白名单校验，并限制总跳数。
func (t *HTTPTransport) checkRedirect(request *http.Request, via []*http.Request) error {
	if request == nil || request.URL == nil {
		return &redirectDeniedError{appErr: newInputError(ErrorCodeSource, messageSourceInvalid)}
	}
	if len(via) > transferMaxRedirects {
		return &redirectDeniedError{appErr: newInputError(ErrorCodeSource, messageSourceHostNotAllowed)}
	}
	if appErr := t.registry.ValidateURL(request.URL.String()); appErr != nil {
		return &redirectDeniedError{appErr: appErr}
	}
	return nil
}

// transferTimeout 按文件大小估算单次尝试的整体超时。
//
// 公式：30s + 大小/256KiB 秒，上限 30 分钟（卡面第 3 条）。
func transferTimeout(expectedSize int64) time.Duration {
	size := expectedSize
	if size < 0 {
		size = 0
	}
	seconds := size / transferSizeAllowancePerSecond
	timeout := transferBaseTimeout + time.Duration(seconds)*time.Second
	if timeout > transferMaxTimeout {
		timeout = transferMaxTimeout
	}
	return timeout
}

// partSize 返回本地已有字节数：优先取 P3-06 落库的 range_start。
func partSize(task Task) int64 {
	if task.RangeStart > 0 {
		return task.RangeStart
	}
	if task.DownloadedSize > 0 {
		return task.DownloadedSize
	}
	return 0
}

// failResult 把结构化错误包成传输结果。
func failResult(cause *Error) TransferResult {
	if cause == nil {
		cause = newExternalError(ErrorCodeNetwork, messageNetworkInterrupted, true)
	}
	return TransferResult{
		Err:       cause,
		ErrorCode: cause.Code,
		Retryable: cause.Retryable,
	}
}

// toDownloadError 把传输结果里的错误还原成 *Error（用于跨候选汇总）。
func toDownloadError(result TransferResult) *Error {
	if result.Err == nil {
		return nil
	}
	var appErr *Error
	if errors.As(result.Err, &appErr) {
		return appErr
	}
	return newExternalError(ErrorCodeNetwork, messageNetworkInterrupted, result.Retryable)
}

// logTransferSuccess 记录一次成功尝试（只含 host、路径前缀、状态码、字节数与耗时）。
func (t *HTTPTransport) logTransferSuccess(
	startedAt time.Time,
	source string,
	label string,
	status int,
	bytes int64,
	reason string,
) {
	t.logTransport("info", "下载尝试成功", startedAt, source, label, status, bytes, reason)
}

// warnTransfer 记录一次失败尝试。
func (t *HTTPTransport) warnTransfer(
	startedAt time.Time,
	source string,
	label string,
	status int,
	bytes int64,
	reason string,
) {
	t.logTransport("warn", "下载尝试失败", startedAt, source, label, status, bytes, reason)
}

// logTransport 组装并输出一条脱敏日志。
//
// 只放 host 与路径前缀：完整 URL 会带出查询串（某些源把签名放查询串里），
// 因此**永远不记录** request.URL，也不用它拼 message。
func (t *HTTPTransport) logTransport(
	level string,
	message string,
	startedAt time.Time,
	source string,
	label string,
	status int,
	bytes int64,
	reason string,
) {
	if t.logger == nil {
		return
	}
	attributes := map[string]string{
		"host":       HostOf(source),
		"pathPrefix": PathPrefixOf(source),
		"source":     label,
		"status":     strconv.Itoa(status),
		"bytes":      strconv.FormatInt(bytes, 10),
		"elapsedMs":  strconv.FormatInt(t.now().Sub(startedAt).Milliseconds(), 10),
	}
	if reason != "" {
		attributes["reason"] = reason
	}
	fields := logging.Fields{
		Event:      "download.transfer.attempt",
		Attributes: attributes,
	}
	switch level {
	case "warn":
		t.logger.Warn(message, fields)
	default:
		t.logger.Info(message, fields)
	}
}
