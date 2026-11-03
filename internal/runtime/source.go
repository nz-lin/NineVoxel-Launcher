package runtime

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	"example.com/ninevoxel/launcher/internal/logging"
)

// sourceSpec 描述一个第三方 Java 发行版来源的装配信息。
//
// 安装流程只依赖这组函数，不再直接引用 Adoptium 的具体实现；
// 新增发行版时在 runtimeSources 里登记一条即可。
type sourceSpec struct {
	// Name 是落库的 source_name，也是 runtimeId 的来源段。
	Name string
	// ComponentKey 是 runtimeId 里代替官方组件名的段。
	ComponentKey string
	// Vendor 是落库的厂商标签。
	Vendor string
	// ClientFactory 返回带白名单与超时的 HTTP 客户端。
	ClientFactory func() *http.Client
	// Fetch 查询该来源在指定大版本上的最新构建元数据。
	Fetch func(ctx context.Context, client *http.Client, major int, goos, goarch string) (*AdoptiumRelease, *AppError)
	// Candidates 返回归档的候选下载地址（镜像可选）。
	Candidates func(release *AdoptiumRelease, mirrorEnabled bool) []string
	// ValidateDownloadURL 校验归档地址是否属于该来源的白名单。
	ValidateDownloadURL func(rawURL string) *AppError
	// LTSVersions 查询该来源当前提供的 LTS 大版本。
	LTSVersions func(ctx context.Context, client *http.Client, goos, goarch string) (RuntimeLtsVersions, *AppError)
}

const (
	// sourceNameZulu 是 Azul Zulu 的来标识。
	sourceNameZulu = "zulu"
	// zuluComponentKey 是 runtimeId 里代替官方组件名的段。
	zuluComponentKey = "zulu"
)

// runtimeSources 是受支持的第三方来源注册表。
var runtimeSources = map[string]sourceSpec{
	sourceNameAdoptium: {
		Name:         sourceNameAdoptium,
		ComponentKey: adoptiumComponentKey,
		Vendor:       VendorOpenJDK,
		// 用闭包读取变量，测试替换 adoptiumHTTPClientFactory 后仍然生效。
		ClientFactory:       func() *http.Client { return adoptiumHTTPClientFactory() },
		Fetch:               fetchAdoptiumRelease,
		Candidates:          adoptiumDownloadCandidates,
		ValidateDownloadURL: validateAdoptiumDownloadURL,
		LTSVersions:         fetchAdoptiumLtsVersions,
	},
	sourceNameZulu: {
		Name:                sourceNameZulu,
		ComponentKey:        zuluComponentKey,
		Vendor:              VendorZulu,
		ClientFactory:       func() *http.Client { return zuluHTTPClientFactory() },
		Fetch:               fetchZuluRelease,
		Candidates:          zuluDownloadCandidates,
		ValidateDownloadURL: validateZuluDownloadURL,
		LTSVersions:         fetchZuluLtsVersions,
	},
}

// resolveRuntimeSource 把发行版标识解析成来源装配信息。
//
// 空串按 Adoptium 处理，兼容未传 vendor 的旧调用方。
func resolveRuntimeSource(vendor string) (sourceSpec, *AppError) {
	normalized := strings.ToLower(strings.TrimSpace(vendor))
	if normalized == "" {
		normalized = sourceNameAdoptium
	}
	spec, ok := runtimeSources[normalized]
	if !ok {
		return sourceSpec{}, newInputError(ErrorCodeRuntimeSource, messageRuntimeSourceUnsupported)
	}
	return spec, nil
}

// validateHTTPSHost 校验地址是 HTTPS 且 host 与允许值完全一致。
func validateHTTPSHost(rawURL, allowedHost string) *AppError {
	parsed, parseErr := url.Parse(strings.TrimSpace(rawURL))
	if parseErr != nil || parsed == nil {
		return newInputError(ErrorCodeRuntimeSource, messageRuntimeSourceRequestFailed)
	}
	if !strings.EqualFold(parsed.Scheme, "https") {
		return newInputError(ErrorCodeRuntimeSource, messageRuntimeSourceNotHTTPS)
	}
	if !strings.EqualFold(parsed.Hostname(), allowedHost) {
		return newInputError(ErrorCodeRuntimeSource, messageRuntimeSourceHostNotAllowed)
	}
	return nil
}

// fetchSourceDocument 发起一次受限的 JSON GET 请求并返回响应体。
//
// 限制：https + 单一 host、响应体上限、非 200 明确失败、404 不重试。
func fetchSourceDocument(
	ctx context.Context,
	client *http.Client,
	rawURL, allowedHost, logMessage string,
	maxBytes int64,
) ([]byte, *AppError) {
	if appErr := validateHTTPSHost(rawURL, allowedHost); appErr != nil {
		return nil, appErr
	}
	request, buildErr := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if buildErr != nil {
		return nil, newInputError(ErrorCodeRuntimeSource, messageRuntimeSourceRequestFailed)
	}
	request.Header.Set("Accept", "application/json")
	response, doErr := client.Do(request)
	if doErr != nil {
		if ctx.Err() != nil {
			return nil, newExternalError(ErrorCodeRuntimeSource, messageRuntimeSourceTimeout, true)
		}
		var redirectErr *sourceRedirectError
		if errors.As(doErr, &redirectErr) {
			return nil, newExternalError(ErrorCodeRuntimeSource, messageRuntimeSourceRedirectRejected, false)
		}
		runtimeLog().Warn(logMessage, logging.Fields{
			Event:     "runtime.source.fetch_failed",
			ErrorCode: ErrorCodeRuntimeSource,
		})
		return nil, newExternalError(ErrorCodeRuntimeSource, messageRuntimeSourceUnreachable, true)
	}
	defer func() {
		_ = response.Body.Close()
	}()

	if response.StatusCode == http.StatusNotFound {
		return nil, newExternalError(ErrorCodeRuntimeSource, messageRuntimeSourceNotFound, false)
	}
	if response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500 {
		return nil, newExternalError(ErrorCodeRuntimeSource, messageRuntimeSourceUnreachable, true)
	}
	if response.StatusCode != http.StatusOK {
		return nil, newExternalError(ErrorCodeRuntimeSource, messageRuntimeSourceStatusFailed, false)
	}

	body, readErr := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	if readErr != nil {
		return nil, newExternalError(ErrorCodeRuntimeSource, messageRuntimeSourceUnreachable, true)
	}
	if int64(len(body)) > maxBytes {
		return nil, newExternalError(ErrorCodeRuntimeSource, messageRuntimeSourceTooLarge, false)
	}
	return body, nil
}
