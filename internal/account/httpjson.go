// 本文件提供账号子系统共用的 HTTP/JSON 辅助。
//
// 统一在这里做三件事，避免每个来源各写一套：
//   - 超时与 UA；
//   - 响应体大小上限（防止恶意/异常服务端撑爆内存）；
//   - 网络错误到稳定错误码的映射（可重试 vs 不可重试）。
//
// 注意：本文件**不**解析业务错误体（例如 Yggdrasil 的 ForbiddenOperationException），
// 那是各来源自己的职责；这里只保证「非 2xx 也能拿到状态码与原始 body」。

package account

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// HTTPDoer 是 HTTP 客户端的最小接口（*http.Client 天然满足）。
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// DefaultHTTPTimeout 是账号相关 HTTP 请求的超时。
//
// 设备码轮询与档案查询都是小请求；20 秒足够覆盖慢网络，又不会让界面
// 长时间悬在一个不可见的请求上。
const DefaultHTTPTimeout = 20 * time.Second

// maxResponseBytes 是响应体读取上限（1 MiB）。
//
// Yggdrasil 元数据/档案与微软 token 响应都远小于该值；超限直接判协议错误，
// 防止异常服务端返回超大 body。
const maxResponseBytes = 1 << 20

// DefaultHTTPClient 返回账号子系统使用的默认客户端。
func DefaultHTTPClient() *http.Client {
	return &http.Client{Timeout: DefaultHTTPTimeout}
}

// HTTPResponse 是统一后的响应结构。
type HTTPResponse struct {
	// Status 为 HTTP 状态码。
	Status int

	// Header 为响应头。
	Header http.Header

	// Body 为响应体原始字节（已限制大小）。
	Body []byte
}

// DoJSON 发送 JSON 请求并读取完整响应。
//
// 参数 payload 为 nil 时不带请求体；userAgent 为空时使用 DefaultUserAgent。
// 返回的错误码：网络问题 ACCOUNT_NETWORK_001（可重试），响应异常 ACCOUNT_HTTP_001。
func DoJSON(ctx context.Context, client HTTPDoer, method, rawURL string, payload any, userAgent string) (*HTTPResponse, *Error) {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, newSystemError(ErrorCodeHTTP, "无法编码请求内容")
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return nil, newInputError(ErrorCodeHTTP, "请求地址不合法")
	}
	request.Header.Set("Accept", "application/json")
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if userAgent == "" {
		userAgent = DefaultUserAgent
	}
	request.Header.Set("User-Agent", userAgent)
	return do(client, request)
}

// DoForm 发送 application/x-www-form-urlencoded 请求。
//
// 微软 OAuth 端点只接受表单编码，因此单独提供该辅助。
func DoForm(ctx context.Context, client HTTPDoer, rawURL string, form url.Values, userAgent string) (*HTTPResponse, *Error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, bytes.NewBufferString(form.Encode()))
	if err != nil {
		return nil, newInputError(ErrorCodeHTTP, "请求地址不合法")
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	if userAgent == "" {
		userAgent = DefaultUserAgent
	}
	request.Header.Set("User-Agent", userAgent)
	return do(client, request)
}

// DoGet 发送 GET 请求（可带额外请求头）。
func DoGet(ctx context.Context, client HTTPDoer, rawURL string, headers map[string]string, userAgent string) (*HTTPResponse, *Error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, newInputError(ErrorCodeHTTP, "请求地址不合法")
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	if userAgent == "" {
		userAgent = DefaultUserAgent
	}
	request.Header.Set("User-Agent", userAgent)
	return do(client, request)
}

// do 执行请求并收敛响应。
func do(client HTTPDoer, request *http.Request) (*HTTPResponse, *Error) {
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(request)
	if err != nil {
		// 网络层错误不暴露底层细节（可能包含 URL/地址），只给稳定的可重试文案。
		return nil, newExternalError(ErrorCodeNetwork, "无法连接账号服务，请检查网络后重试", true)
	}
	defer func() { _ = response.Body.Close() }()

	limited := io.LimitReader(response.Body, maxResponseBytes+1)
	body, readErr := io.ReadAll(limited)
	if readErr != nil {
		return nil, newExternalError(ErrorCodeNetwork, "读取账号服务响应失败，请重试", true)
	}
	if len(body) > maxResponseBytes {
		return nil, newExternalError(ErrorCodeHTTP, "账号服务返回的数据超过预期大小", false)
	}
	return &HTTPResponse{Status: response.StatusCode, Header: response.Header, Body: body}, nil
}

// DecodeJSON 把响应体解析到 out。
//
// 空响应体不算错误（Yggdrasil 的 validate/invalidate 返回 204 无 body），
// 由调用方按状态码判断语义。
func DecodeJSON(response *HTTPResponse, out any) *Error {
	if response == nil || len(bytes.TrimSpace(response.Body)) == 0 {
		return nil
	}
	if err := json.Unmarshal(response.Body, out); err != nil {
		return newExternalError(ErrorCodeHTTP, "账号服务返回了无法解析的数据", false)
	}
	return nil
}

// statusError 把非预期状态码转换为错误（文案可定制，不透传服务端原文）。
func statusError(response *HTTPResponse, message string) *Error {
	if response == nil {
		return newExternalError(ErrorCodeHTTP, message, false)
	}
	return newExternalError(ErrorCodeHTTP, fmt.Sprintf("%s（HTTP %d）", message, response.Status), false)
}

// isSuccess 判断状态码是否 2xx。
func isSuccess(status int) bool {
	return status >= 200 && status < 300
}
