// 本文件实现 P3-14：第三方 Java Runtime 来源（Adoptium / Eclipse Temurin）与更新检查。
//
// 设计背景（D-50 修订）：
//   - 元数据与校验值**始终**来自 Adoptium API（api.adoptium.net），它提供 SHA-256；
//   - 归档的下载地址**可以**换成清华 TUNA 镜像加速，但镜像只是候选，且可能滞后，
//     因此必须保留官方地址作为回落；
//   - Mojang 官方 Runtime（P3-09 的 `java-runtime-*`）仍走原来的 Mojang 清单，
//     本文件不参与，也不改变它的镜像开关行为（官方源继续不读镜像设置）。
//
// 为什么本文件的下载没有直接用 `download.HTTPTransport`：
// P3-05 的 `download.SourceRegistry` 把「官方 host 白名单」硬编码为 5 个 Mojang 域名，
// 任何其它 host（包括 api.adoptium.net 与 github.com）都会被 `ValidateURL` 拒绝；
// 而本卡的允许文件列表明确禁止修改 `internal/download/**`。因此这里**复用** P3-05 的
// 写盘接缝（`download.StagingSink`）与 P3-06 的校验器（`download.VerifyFile`），
// 只把 HTTP 请求这一层换成带 Adoptium 白名单的实现。这一点已在交付报告中列为
// 需 Leader 裁决事项：若后续在 `internal/download` 提供可注入官方 host 的注册表，
// 这里应改用 `download.HTTPTransport` 以获得续传与限速能力。
package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	goruntime "runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"example.com/ninevoxel/launcher/internal/artifact"
	"example.com/ninevoxel/launcher/internal/download"
	"example.com/ninevoxel/launcher/internal/logging"
)

const (
	// adoptiumAPIBaseURL 是 Adoptium 资产 API 的根地址。
	adoptiumAPIBaseURL = "https://api.adoptium.net"

	// adoptiumAPIHost 是 API 请求唯一允许的 host。
	adoptiumAPIHost = "api.adoptium.net"

	// adoptiumAPIMaxBytes 是 API 响应体的读取上限（4 MiB）。
	//
	// 单个 major 的资产描述通常只有几 KB，4 MiB 已经非常宽松；
	// 设上限是为了防止异常响应把进程内存打满（与 P3-01 的清单校验同一思路）。
	adoptiumAPIMaxBytes = 4 << 20

	// adoptiumDialTimeout 是建立连接的超时。
	adoptiumDialTimeout = 10 * time.Second

	// adoptiumOverallTimeout 是单次 API 请求的整体超时。
	adoptiumOverallTimeout = 30 * time.Second

	// adoptiumMaxRedirects 是允许的最大重定向跳数。
	adoptiumMaxRedirects = 3

	// adoptiumMirrorBase 是内置的清华 TUNA 镜像根地址。
	adoptiumMirrorBase = "https://mirrors.tuna.tsinghua.edu.cn/Adoptium"

	// adoptiumMirrorHost 是镜像的 host，用于白名单与展示。
	adoptiumMirrorHost = "mirrors.tuna.tsinghua.edu.cn"

	// adoptiumOfficialHost 是 Adoptium 官方归档的 host（GitHub Release）。
	adoptiumOfficialHost = "github.com"

	// adoptiumRedirectHost 是 GitHub Release 下载时的旧重定向目标 host。
	//
	// GitHub 在 2024 年之后逐步改用 release-assets.githubusercontent.com，
	// 但部分存量链接仍可能跳到旧域名，因此两个都保留在允许列表里。
	adoptiumRedirectHost = "objects.githubusercontent.com"

	// adoptiumReleaseAssetsHost 是 GitHub Release 资产当前的重定向目标 host。
	//
	// 实测（2026-11）：github.com → 302 → release-assets.githubusercontent.com → 200。
	// 只放旧域名会让每一次归档下载都在重定向处被白名单拒绝（RUNTIME_SOURCE_001）。
	adoptiumReleaseAssetsHost = "release-assets.githubusercontent.com"

	// sourceNameAdoptium 是 `java_runtimes.source_name` 里记录的来源标识。
	sourceNameAdoptium = "adoptium"

	// adoptiumComponentKey 是 runtimeId 里代替官方组件名的段。
	adoptiumComponentKey = "adoptium"

	// adoptiumInstallTimeout 是本文件安装流程的整体超时。
	//
	// 归档约 200 MiB，慢速网络下需要数分钟；30 分钟与 P3-09 的官方安装保持同一量级。
	adoptiumInstallTimeout = 30 * time.Minute

	// adoptiumCopyBufferSize 是下载归档时的拷贝缓冲区大小。
	adoptiumCopyBufferSize = 64 * 1024
)

// adoptiumPlatform 描述一个目标平台的映射关系（表驱动，不做猜测）。
//
// 三列分别对应：Adoptium API 的 `os` 查询参数、清华镜像路径里的 os 段、
// 解压后应当存在的可执行文件相对路径。
type adoptiumPlatform struct {
	// APIOs 是 API 查询参数 `os` 的取值。
	APIOs string
	// MirrorOS 是镜像 URL 路径里的 os 段。
	MirrorOS string
	// ExecutableRel 是解压后必须存在的 java 可执行文件相对路径。
	ExecutableRel string
}

// adoptiumPlatforms 是 GOOS 到 Adoptium 平台信息的映射。
//
// 只列出有把握的组合：windows / linux / darwin。其它平台（例如 plan9）直接拒绝，
// 而不是猜一个最接近的映射——猜错会让用户下载到根本无法运行的运行时。
var adoptiumPlatforms = map[string]adoptiumPlatform{
	"windows": {APIOs: "windows", MirrorOS: "windows", ExecutableRel: "bin/java.exe"},
	"linux":   {APIOs: "linux", MirrorOS: "linux", ExecutableRel: "bin/java"},
	"darwin":  {APIOs: "mac", MirrorOS: "mac", ExecutableRel: "bin/java"},
}

// adoptiumArchitectures 是 GOARCH 到 Adoptium 架构标识的映射。
var adoptiumArchitectures = map[string]string{
	"amd64": "x64",
	"arm64": "aarch64",
}

// adoptiumDownloadHosts 是归档下载允许的 host 集合。
//
// 包含四类：官方 GitHub Release、GitHub 的两代静态对象存储（302 之后）、清华镜像。
// 任何其它 host 都拒绝，避免「元数据来自 A、归档却来自 B」的供应链风险。
var adoptiumDownloadHosts = map[string]bool{
	adoptiumOfficialHost:      true,
	adoptiumRedirectHost:      true,
	adoptiumReleaseAssetsHost: true,
	adoptiumMirrorHost:        true,
}

// adoptiumAPIResponse 是资产 API 返回数组里的一个元素。
//
// 只声明本文件真正需要的字段：多余字段会被 encoding/json 忽略，
// 这样 Adoptium 增加字段时不需要改这里。
type adoptiumAPIResponse struct {
	// Binary 描述构建产物。
	Binary struct {
		// Package 为归档包信息；缺失表示该条目不可用。
		Package *struct {
			// Link 是归档下载地址（GitHub Release）。
			Link string `json:"link"`
			// Checksum 是 SHA-256（64 位小写十六进制）。
			Checksum string `json:"checksum"`
			// Size 是归档字节数。
			Size int64 `json:"size"`
		} `json:"package"`
	} `json:"binary"`
	// ReleaseName 是发布名（例如 `jdk-21.0.13+9`）。
	ReleaseName string `json:"release_name"`
	// Version 描述版本信息。
	Version struct {
		// Semver 是语义化版本（例如 `21.0.13+9.0.LTS`）。
		Semver string `json:"semver"`
		// OpenJDKVersion 是 OpenJDK 版本串（例如 `21.0.13+9-LTS`）。
		OpenJDKVersion string `json:"openjdk_version"`
		// Major 是主版本号。
		Major int `json:"major"`
	} `json:"version"`
}

// AdoptiumRelease 描述某个 Java 大版本可从 Adoptium 获取的最新构建。
type AdoptiumRelease struct {
	// Major 是主版本号。
	Major int
	// Semver 是语义化版本（用于展示）。
	Semver string
	// FullVersion 是 OpenJDK 版本串（落库的 `full_version`）。
	FullVersion string
	// OS 是镜像路径里的 os 段（windows / mac / linux）。
	OS string
	// Arch 是镜像路径里的架构段（x64 / aarch64）。
	Arch string
	// OfficialURL 是官方归档地址。
	OfficialURL string
	// FileName 是归档文件名（与镜像上的文件名一致）。
	FileName string
	// SizeBytes 是归档字节数。
	SizeBytes int64
	// SHA256 是归档的 SHA-256（小写十六进制）。
	SHA256 string
	// ExecutableRel 是解压后必须存在的 java 可执行文件相对路径。
	ExecutableRel string
}

// javaVersionTuple 是版本号四元组，用于「是否有更新」的比较。
type javaVersionTuple struct {
	// Major 是主版本。
	Major int
	// Minor 是次版本。
	Minor int
	// Security 是安全更新号。
	Security int
	// Build 是构建号。
	Build int
}

// adoptiumHTTPClientFactory 返回访问 Adoptium 的 HTTP 客户端；测试替换为假实现。
//
// 之所以是工厂而不是共享单例：测试需要在每个用例里注入不同的 RoundTripper，
// 共享单例会互相污染。
var adoptiumHTTPClientFactory = newAdoptiumHTTPClient

// adoptiumMirrorEnabled 返回镜像开关状态；测试替换为固定值。
//
// 只影响第三方来源（Adoptium），Mojang 官方 Runtime 继续走 D-50 的旧规则。
var adoptiumMirrorEnabled = readMirrorEnabledSetting

// newAdoptiumHTTPClient 构造带白名单与超时的 HTTP 客户端。
//
// 不使用 http.DefaultClient：默认客户端没有超时、且可能被其它包改写。
func newAdoptiumHTTPClient() *http.Client {
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   adoptiumDialTimeout,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout:   adoptiumDialTimeout,
		ResponseHeaderTimeout: adoptiumOverallTimeout,
		MaxIdleConnsPerHost:   2,
	}
	return &http.Client{
		Transport:     transport,
		CheckRedirect: checkAdoptiumRedirect,
	}
}

// checkAdoptiumRedirect 校验每一次重定向的目标。
//
// GitHub Release 会 302 到 release-assets.githubusercontent.com（存量链接仍可能
// 跳 objects.githubusercontent.com），因此不能要求 host 不变；但必须仍然在
// https + 白名单内，且跳数受限（与 P3-01 的清单重定向同一策略）。
func checkAdoptiumRedirect(request *http.Request, via []*http.Request) error {
	if request == nil {
		return errors.New("重定向请求为空")
	}
	if len(via) >= adoptiumMaxRedirects {
		return &adoptiumRedirectError{message: fmt.Sprintf("重定向次数超过 %d 跳", adoptiumMaxRedirects)}
	}
	if !strings.EqualFold(request.URL.Scheme, "https") {
		return &adoptiumRedirectError{message: "重定向目标不是 HTTPS 地址"}
	}
	host := strings.ToLower(request.URL.Hostname())
	if !adoptiumDownloadHosts[host] {
		return &adoptiumRedirectError{message: "重定向目标不在允许的来源列表中"}
	}
	return nil
}

// adoptiumRedirectError 表示一次被拒绝的重定向。
type adoptiumRedirectError struct {
	// message 是中文原因。
	message string
}

// Error 实现 error 接口。
func (e *adoptiumRedirectError) Error() string {
	return e.message
}

// platformInfo 返回目标平台的 Adoptium 映射。
//
// 不在映射表里的组合返回 false，由调用方给出 RUNTIME_REQUIREMENT_001。
func platformInfo(goos, goarch string) (adoptiumPlatform, string, bool) {
	platform, ok := adoptiumPlatforms[strings.ToLower(strings.TrimSpace(goos))]
	if !ok {
		return adoptiumPlatform{}, "", false
	}
	arch, ok := adoptiumArchitectures[strings.ToLower(strings.TrimSpace(goarch))]
	if !ok {
		return adoptiumPlatform{}, "", false
	}
	return platform, arch, true
}

// FetchAdoptiumRelease 查询某个 Java 大版本在 Adoptium 上的最新构建。
//
// 参数 major 为主版本号（必须 > 0）；goos / goarch 为目标平台与架构。
// 返回的 *AppError：
//   - RUNTIME_REQUIREMENT_001：主版本非法，或平台组合不受支持（不猜）；
//   - RUNTIME_SOURCE_001：网络失败 / 非 200 / 响应非法（Retryable 表达是否值得重试）。
//
// 注意：本函数只取元数据，不下载归档；解压与安装见 InstallRuntimeFromSource。
func FetchAdoptiumRelease(major int, goos, goarch string) (*AdoptiumRelease, *AppError) {
	ctx, cancel := context.WithTimeout(context.Background(), adoptiumOverallTimeout)
	defer cancel()
	return fetchAdoptiumRelease(ctx, adoptiumHTTPClientFactory(), major, goos, goarch)
}

// fetchAdoptiumRelease 是 FetchAdoptiumRelease 的可注入实现。
func fetchAdoptiumRelease(ctx context.Context, client *http.Client, major int, goos, goarch string) (*AdoptiumRelease, *AppError) {
	if major <= 0 {
		return nil, newInputError(ErrorCodeRuntimeRequirement, messageRuntimeMajorRequired)
	}
	platform, arch, supported := platformInfo(goos, goarch)
	if !supported {
		return nil, newInputError(ErrorCodeRuntimeRequirement, messageRuntimeSourcePlatformUnsupported)
	}
	rawURL := buildAdoptiumAssetsURL(major, platform.APIOs, arch)

	body, fetchErr := fetchAdoptiumDocument(ctx, client, rawURL)
	if fetchErr != nil {
		return nil, fetchErr
	}
	var assets []adoptiumAPIResponse
	if unmarshalErr := json.Unmarshal(body, &assets); unmarshalErr != nil {
		return nil, newExternalError(ErrorCodeRuntimeSource, messageRuntimeSourceMalformed, false)
	}
	if len(assets) == 0 {
		return nil, newExternalError(ErrorCodeRuntimeSource, messageRuntimeSourceEmpty, false)
	}
	asset := assets[0]
	if asset.Binary.Package == nil {
		return nil, newExternalError(ErrorCodeRuntimeSource, messageRuntimeSourcePackageMissing, false)
	}
	checksum := strings.TrimSpace(asset.Binary.Package.Checksum)
	if !isSHA256Hex(checksum) {
		return nil, newExternalError(ErrorCodeRuntimeSource, messageRuntimeSourceChecksumInvalid, false)
	}
	officialURL := strings.TrimSpace(asset.Binary.Package.Link)
	if officialURL == "" {
		return nil, newExternalError(ErrorCodeRuntimeSource, messageRuntimeSourcePackageMissing, false)
	}
	fileName, nameErr := adoptiumArchiveFileName(officialURL)
	if nameErr != nil {
		return nil, nameErr
	}
	size := asset.Binary.Package.Size
	if size <= 0 || size > runtimeMaxTotalBytes {
		return nil, newExternalError(ErrorCodeRuntimeSource, messageRuntimeSourceSizeInvalid, false)
	}

	fullVersion := strings.TrimSpace(asset.Version.OpenJDKVersion)
	if fullVersion == "" {
		fullVersion = strings.TrimSpace(asset.Version.Semver)
	}
	if fullVersion == "" {
		return nil, newExternalError(ErrorCodeRuntimeSource, messageRuntimeSourceVersionMissing, false)
	}

	return &AdoptiumRelease{
		Major:         major,
		Semver:        strings.TrimSpace(asset.Version.Semver),
		FullVersion:   fullVersion,
		OS:            platform.MirrorOS,
		Arch:          arch,
		OfficialURL:   officialURL,
		FileName:      fileName,
		SizeBytes:     size,
		SHA256:        checksum,
		ExecutableRel: platform.ExecutableRel,
	}, nil
}

// buildAdoptiumAssetsURL 拼接资产 API 的请求地址。
func buildAdoptiumAssetsURL(major int, apiOS, arch string) string {
	query := url.Values{}
	query.Set("os", apiOS)
	query.Set("architecture", arch)
	query.Set("image_type", "jdk")
	return fmt.Sprintf("%s/v3/assets/latest/%d/hotspot?%s", adoptiumAPIBaseURL, major, query.Encode())
}

// fetchAdoptiumDocument 发起一次受限的 GET 请求并返回响应体。
//
// 限制：https + 单一 host、响应体上限、非 200 明确失败、404 不重试。
func fetchAdoptiumDocument(ctx context.Context, client *http.Client, rawURL string) ([]byte, *AppError) {
	if appErr := validateAdoptiumAPIURL(rawURL); appErr != nil {
		return nil, appErr
	}
	if client == nil {
		client = adoptiumHTTPClientFactory()
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
		var redirectErr *adoptiumRedirectError
		if errors.As(doErr, &redirectErr) {
			return nil, newExternalError(ErrorCodeRuntimeSource, messageRuntimeSourceRedirectRejected, false)
		}
		runtimeLog().Warn("请求 Adoptium 资产接口失败", logging.Fields{
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

	body, readErr := io.ReadAll(io.LimitReader(response.Body, adoptiumAPIMaxBytes+1))
	if readErr != nil {
		return nil, newExternalError(ErrorCodeRuntimeSource, messageRuntimeSourceUnreachable, true)
	}
	if len(body) > adoptiumAPIMaxBytes {
		return nil, newExternalError(ErrorCodeRuntimeSource, messageRuntimeSourceTooLarge, false)
	}
	return body, nil
}

// validateAdoptiumAPIURL 校验 API 地址是否允许请求。
func validateAdoptiumAPIURL(rawURL string) *AppError {
	parsed, parseErr := url.Parse(strings.TrimSpace(rawURL))
	if parseErr != nil || parsed == nil {
		return newInputError(ErrorCodeRuntimeSource, messageRuntimeSourceRequestFailed)
	}
	if !strings.EqualFold(parsed.Scheme, "https") {
		return newInputError(ErrorCodeRuntimeSource, messageRuntimeSourceNotHTTPS)
	}
	if !strings.EqualFold(parsed.Hostname(), adoptiumAPIHost) {
		return newInputError(ErrorCodeRuntimeSource, messageRuntimeSourceHostNotAllowed)
	}
	return nil
}

// adoptiumArchiveFileName 从归档地址里取出文件名。
//
// GitHub Release 的地址形如
// https://github.com/.../download/jdk-21.0.13%2B9/OpenJDK21U-jdk_x64_windows_hotspot_21.0.13_9.zip，
// 最后一段是文件名；这里要求它不含路径分隔符与 `..`，避免把地址里的内容当成路径用。
func adoptiumArchiveFileName(officialURL string) (string, *AppError) {
	parsed, parseErr := url.Parse(strings.TrimSpace(officialURL))
	if parseErr != nil || parsed == nil {
		return "", newInputError(ErrorCodeRuntimeSource, messageRuntimeSourceRequestFailed)
	}
	escaped := parsed.EscapedPath()
	index := strings.LastIndex(escaped, "/")
	if index < 0 || index == len(escaped)-1 {
		return "", newExternalError(ErrorCodeRuntimeSource, messageRuntimeSourcePackageMissing, false)
	}
	name, unescapeErr := url.PathUnescape(escaped[index+1:])
	if unescapeErr != nil {
		return "", newExternalError(ErrorCodeRuntimeSource, messageRuntimeSourcePackageMissing, false)
	}
	name = strings.TrimSpace(name)
	if name == "" || strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") {
		return "", newExternalError(ErrorCodeRuntimeSource, messageRuntimeSourcePackageMissing, false)
	}
	return name, nil
}

// BuildMirrorURL 按清华 TUNA 的目录约定拼接镜像下载地址。
//
// 规则：`<base>/<major>/jdk/<arch>/<os>/<filename>`，其中 filename 与 GitHub Release
// 上的文件名**逐字节相同**（镜像只是换了 host 与前缀）。
//
// 参数 mirrorBase 为空时使用内置的清华镜像根地址；传入自定义地址时必须仍是 https。
// 所有路径段都做注入校验：含 `/`、`\`、`..` 或为空的段直接拒绝，
// 否则一个恶意文件名就能把下载目标导到镜像站的其它路径。
func BuildMirrorURL(mirrorBase string, major int, arch, osName, filename string) (string, *AppError) {
	if major <= 0 {
		return "", newInputError(ErrorCodeRuntimeRequirement, messageRuntimeMajorRequired)
	}
	base := strings.TrimSpace(mirrorBase)
	if base == "" {
		base = adoptiumMirrorBase
	}
	base = strings.TrimRight(base, "/")
	parsed, parseErr := url.Parse(base)
	if parseErr != nil || parsed == nil || !strings.EqualFold(parsed.Scheme, "https") || parsed.Hostname() == "" {
		return "", newInputError(ErrorCodeRuntimeSource, messageRuntimeSourceMirrorInvalid)
	}
	for _, segment := range []string{arch, osName, filename} {
		if !isSafePathSegment(segment) {
			return "", newInputError(ErrorCodeRuntimeSource, messageRuntimeSourceMirrorInvalid)
		}
	}
	return fmt.Sprintf("%s/%d/jdk/%s/%s/%s", base, major, arch, osName, filename), nil
}

// isSafePathSegment 判断一个值是否可以安全地作为单个路径段。
func isSafePathSegment(value string) bool {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return false
	}
	if strings.ContainsAny(trimmed, `/\`) || strings.Contains(trimmed, "..") {
		return false
	}
	return trimmed == value
}

// adoptiumDownloadCandidates 返回归档的候选下载地址。
//
// 镜像打开时镜像在前、官方在后：镜像通常更快，但可能滞后（实测 21 的 windows 目录
// 停在 21.0.12.1 而 API 已给 21.0.13），所以官方必须是回落而不能被替代。
func adoptiumDownloadCandidates(release *AdoptiumRelease, mirrorEnabled bool) []string {
	if release == nil {
		return nil
	}
	candidates := make([]string, 0, 2)
	official := strings.TrimSpace(release.OfficialURL)
	if mirrorEnabled {
		if mirror, mirrorErr := BuildMirrorURL(adoptiumMirrorBase, release.Major, release.Arch, release.OS, release.FileName); mirrorErr == nil {
			candidates = append(candidates, mirror)
		}
	}
	if official != "" {
		candidates = append(candidates, official)
	}
	return candidates
}

// validateAdoptiumDownloadURL 校验归档地址是否允许请求。
func validateAdoptiumDownloadURL(rawURL string) *AppError {
	parsed, parseErr := url.Parse(strings.TrimSpace(rawURL))
	if parseErr != nil || parsed == nil {
		return newInputError(ErrorCodeRuntimeSource, messageRuntimeSourceRequestFailed)
	}
	if !strings.EqualFold(parsed.Scheme, "https") {
		return newInputError(ErrorCodeRuntimeSource, messageRuntimeSourceNotHTTPS)
	}
	if !adoptiumDownloadHosts[strings.ToLower(parsed.Hostname())] {
		return newInputError(ErrorCodeRuntimeSource, messageRuntimeSourceHostNotAllowed)
	}
	return nil
}

// sourceVersionToken 把完整版本串归一化成 runtimeId 里可用的版本段。
//
// 例：`21.0.13+9-LTS` → `21.0.13`。规则：先丢掉 `+` 之后的构建号，
// 再去掉尾部的 `-LTS` 标记，最后交给 sanitizeIDPart 收紧字符集。
func sourceVersionToken(fullVersion string) string {
	trimmed := strings.TrimSpace(fullVersion)
	if index := strings.Index(trimmed, "+"); index >= 0 {
		trimmed = trimmed[:index]
	}
	lower := strings.ToLower(trimmed)
	lower = strings.TrimSuffix(lower, "-lts")
	return sanitizeIDPart(lower)
}

// parseJavaVersionTuple 把版本串解析成 (major, minor, security, build) 四元组。
//
// 规则（与 Adoptium 的两种格式兼容）：
//  1. `+` 之后的首个数字是构建号（`21.0.13+9-LTS` 的构建号是 9）；
//  2. `+` 之前的部分按 `.` / `_` / `-` 拆成数字序列；
//  3. 若首段是 1 且至少三段（`1.8.0_401`），去掉首段，主版本取 8；
//  4. 段数 ≥ 4 时取前四段（`21.0.12.1` → 21.0.12.1），否则缺位补 0、
//     第四位取构建号（`21.0.13+9` → 21.0.13.9）。
//
// 之所以要在段数 ≥ 4 时忽略构建号：Adoptium 的 semver 可以出现四段
// （`21.0.12.1`），此时第四段本身就是比构建号更强的次序信息；
// 若同时保留两者，`21.0.12.1+1` 与 `21.0.12.1+2` 会被误判为不同版本。
func parseJavaVersionTuple(raw string) (javaVersionTuple, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return javaVersionTuple{}, false
	}
	versionPart := trimmed
	build := 0
	if index := strings.Index(trimmed, "+"); index >= 0 {
		versionPart = trimmed[:index]
		if rest := strings.TrimSpace(trimmed[index+1:]); rest != "" {
			build = leadingNumber(rest)
		}
	}
	parts, ok := splitVersionNumbers(versionPart)
	if !ok || len(parts) == 0 {
		return javaVersionTuple{}, false
	}
	if parts[0] == 1 && len(parts) >= 3 {
		parts = parts[1:]
	}
	tuple := javaVersionTuple{Major: parts[0]}
	if len(parts) > 1 {
		tuple.Minor = parts[1]
	}
	if len(parts) > 2 {
		tuple.Security = parts[2]
	}
	if len(parts) >= 4 {
		tuple.Build = parts[3]
	} else {
		tuple.Build = build
	}
	return tuple, true
}

// splitVersionNumbers 把版本串拆成数字序列；任何一段不是数字都返回 false。
func splitVersionNumbers(value string) ([]int, bool) {
	segments := strings.FieldsFunc(value, func(r rune) bool {
		return r == '.' || r == '_' || r == '-'
	})
	parts := make([]int, 0, len(segments))
	for _, segment := range segments {
		if segment == "" {
			continue
		}
		number, convErr := strconv.Atoi(segment)
		if convErr != nil {
			return nil, false
		}
		parts = append(parts, number)
	}
	if len(parts) == 0 {
		return nil, false
	}
	return parts, true
}

// leadingNumber 返回字符串开头连续数字组成的值；没有数字时返回 0。
func leadingNumber(value string) int {
	trimmed := strings.TrimSpace(value)
	end := 0
	for end < len(trimmed) && trimmed[end] >= '0' && trimmed[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0
	}
	number, convErr := strconv.Atoi(trimmed[:end])
	if convErr != nil {
		return 0
	}
	return number
}

// compareJavaVersionTuple 比较两个四元组：a > b 返回正数，a < b 返回负数。
func compareJavaVersionTuple(a, b javaVersionTuple) int {
	switch {
	case a.Major != b.Major:
		return a.Major - b.Major
	case a.Minor != b.Minor:
		return a.Minor - b.Minor
	case a.Security != b.Security:
		return a.Security - b.Security
	default:
		return a.Build - b.Build
	}
}

// isSHA256Hex 判断字符串是否为 64 位小写十六进制。
func isSHA256Hex(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, char := range value {
		switch {
		case char >= '0' && char <= '9':
		case char >= 'a' && char <= 'f':
		default:
			return false
		}
	}
	return true
}

// flattenSingleTopDirectory 剥掉归档解压后唯一的顶层目录。
//
// Adoptium 的归档固定带一层 `jdk-21.0.13+9/`，安装目录里不应再套这一层。
// 顶层不是「恰好一个目录」时直接失败：多条目可能是打包方式变了，
// 猜一个「看起来像根」的目录会让用户拿到结构不可预期的运行时。
func flattenSingleTopDirectory(directory string) (string, *AppError) {
	entries, readErr := os.ReadDir(directory)
	if readErr != nil {
		return "", newSystemError(ErrorCodeRuntimeUnpack, messageRuntimeArchiveUnreadable, false)
	}
	if len(entries) != 1 || !entries[0].IsDir() {
		return "", newInputError(ErrorCodeRuntimeUnpack, messageRuntimeArchiveTopLevelInvalid)
	}
	return filepath.Join(directory, entries[0].Name()), nil
}

// InstallRuntimeFromSource 从第三方来源安装一个 Java Runtime。
//
// 流程：许可 → 取元数据 → 校验平台 → 下载归档（镜像优先，官方回落）→
// SHA-256 校验 → 解压 → 剥顶层目录 → 校验可执行文件 → 原子改名 → 落库。
// 任一步失败都会删除 staging 目录，绝不留下半装的运行时。
func InstallRuntimeFromSource(req InstallRuntimeSourceRequest) (download.TaskAccepted, *AppError) {
	ctx, cancel := context.WithTimeout(context.Background(), adoptiumInstallTimeout)
	defer cancel()
	return installRuntimeFromSource(ctx, req)
}

// installRuntimeFromSource 是 InstallRuntimeFromSource 的可注入实现。
func installRuntimeFromSource(ctx context.Context, req InstallRuntimeSourceRequest) (download.TaskAccepted, *AppError) {
	if !req.AcceptLicense {
		return download.TaskAccepted{}, newInputError(ErrorCodeRuntimeInstall, messageRuntimeLicenseRequired)
	}
	release, fetchErr := FetchAdoptiumRelease(req.MajorVersion, goruntime.GOOS, goruntime.GOARCH)
	if fetchErr != nil {
		return download.TaskAccepted{}, fetchErr
	}
	root, rootErr := runtimeDataRoot()
	if rootErr != nil {
		return download.TaskAccepted{}, rootErr
	}

	runtimeID, idErr := sourceRuntimeID(release)
	if idErr != nil {
		return download.TaskAccepted{}, idErr
	}
	taskID := runtimeEventTaskPrefix + runtimeID
	finalDir := filepath.Join(root, artifact.DirRuntimes, runtimeID)
	if appErr := ensureSourceRuntimeIntact(finalDir, release); appErr == nil {
		// 目录已存在且可执行文件就位：不重复下载，直接把记录补上并返回 Duplicate。
		if saveErr := saveSourceRuntimeRow(ctx, release, runtimeID); saveErr != nil {
			return download.TaskAccepted{}, saveErr
		}
		runtimeProgressSink(taskID, runtimeEventStateCompleted, 1, "")
		return download.TaskAccepted{
			TaskID:      taskID,
			State:       runtimeEventStateCompleted,
			Duplicate:   true,
			CreatedAtMs: runtimeSelectionNowMs(),
		}, nil
	}

	stagingRoot := filepath.Join(root, artifact.DirRuntimes, runtimeStagingPrefix+runtimeID)
	archivePath := filepath.Join(stagingRoot, "archive.zip")
	extractDir := filepath.Join(stagingRoot, "extract")

	if _, spaceErr := download.CheckDiskSpace(root, release.SizeBytes); spaceErr != nil {
		return download.TaskAccepted{}, downloadErrToAppError(spaceErr)
	}
	progress := newInstallProgress(taskID, release.SizeBytes, 1)
	progress.report(runtimeEventStateDownloading, "")

	if appErr := os.RemoveAll(stagingRoot); appErr != nil {
		return download.TaskAccepted{}, newSystemError(ErrorCodeRuntimeInstall, messageRuntimeStagingFailed, false)
	}
	if mkdirErr := os.MkdirAll(extractDir, 0o755); mkdirErr != nil {
		return download.TaskAccepted{}, newSystemError(ErrorCodeRuntimeInstall, messageRuntimeStagingFailed, false)
	}

	candidates := adoptiumDownloadCandidates(release, adoptiumMirrorEnabled())
	if downloadErr := downloadAdoptiumArchive(ctx, candidates, archivePath, release.SizeBytes, progress); downloadErr != nil {
		cleanupRuntimeStaging(stagingRoot)
		runtimeProgressSink(taskID, runtimeEventStateFailed, 0, downloadErr.Code)
		return download.TaskAccepted{}, downloadErr
	}
	// 下载完成：再上报一次，确保进度条停在 100% 而不是被节流吞掉的中间值。
	progress.report(runtimeEventStateDownloading, "")

	// SHA-256 必须匹配：Adoptium 的校验值是 SHA-256，绝不能塞进 sha1 字段。
	// 校验不通过属于「源端给了错文件」，重试也只会再拿到同一份坏文件，故不可重试。
	if _, verifyErr := download.VerifyFile(archivePath, download.VerifyExpectation{
		ExpectedSize:   release.SizeBytes,
		ExpectedSHA256: release.SHA256,
	}); verifyErr != nil {
		cleanupRuntimeStaging(stagingRoot)
		runtimeLog().Warn("Adoptium 归档校验失败，已清理 staging", logging.Fields{
			Event:      "runtime.source.verify_failed",
			ErrorCode:  ErrorCodeRuntimeInstall,
			Attributes: map[string]string{"runtimeId": runtimeID},
		})
		runtimeProgressSink(taskID, runtimeEventStateFailed, 0, ErrorCodeRuntimeInstall)
		return download.TaskAccepted{}, newInputError(ErrorCodeRuntimeInstall, messageRuntimeArchiveChecksumMismatch)
	}

	if _, unpackErr := UnpackZip(archivePath, extractDir, UnpackOptions{
		Executable: func(relPath string) bool {
			return strings.HasSuffix(relPath, "/bin/java") || relPath == "bin/java"
		},
	}); unpackErr != nil {
		cleanupRuntimeStaging(stagingRoot)
		runtimeProgressSink(taskID, runtimeEventStateFailed, 0, unpackErr.Code)
		return download.TaskAccepted{}, unpackErr
	}

	innerRoot, flattenErr := flattenSingleTopDirectory(extractDir)
	if flattenErr != nil {
		cleanupRuntimeStaging(stagingRoot)
		runtimeProgressSink(taskID, runtimeEventStateFailed, 0, flattenErr.Code)
		return download.TaskAccepted{}, flattenErr
	}
	executablePath := filepath.Join(innerRoot, filepath.FromSlash(release.ExecutableRel))
	if !isRegularFile(executablePath) {
		cleanupRuntimeStaging(stagingRoot)
		runtimeProgressSink(taskID, runtimeEventStateFailed, 0, ErrorCodeRuntimeUnpack)
		return download.TaskAccepted{}, newInputError(ErrorCodeRuntimeUnpack, messageRuntimeArchiveExecutableMissing)
	}

	progress.report(runtimeEventStateCommitting, "")
	if removeErr := os.RemoveAll(finalDir); removeErr != nil {
		cleanupRuntimeStaging(stagingRoot)
		return download.TaskAccepted{}, newSystemError(ErrorCodeRuntimeInstall, messageRuntimeCommitFailed, false)
	}
	if mkdirErr := os.MkdirAll(filepath.Dir(finalDir), 0o755); mkdirErr != nil {
		cleanupRuntimeStaging(stagingRoot)
		return download.TaskAccepted{}, newSystemError(ErrorCodeRuntimeInstall, messageRuntimeCommitFailed, false)
	}
	if renameErr := os.Rename(innerRoot, finalDir); renameErr != nil {
		cleanupRuntimeStaging(stagingRoot)
		return download.TaskAccepted{}, newSystemError(ErrorCodeRuntimeInstall, messageRuntimeCommitFailed, false)
	}
	// 归档与解压残留都在 staging 里，安装完成后统一清掉。
	cleanupRuntimeStaging(stagingRoot)

	if saveErr := saveSourceRuntimeRow(ctx, release, runtimeID); saveErr != nil {
		runtimeProgressSink(taskID, runtimeEventStateFailed, 0, saveErr.Code)
		return download.TaskAccepted{}, saveErr
	}
	runtimeProgressSink(taskID, runtimeEventStateCompleted, 1, "")
	runtimeLog().Info("已安装第三方 Java Runtime", logging.Fields{
		Event:      "runtime.source.installed",
		Attributes: map[string]string{"runtimeId": runtimeID, "source": sourceNameAdoptium},
	})
	return download.TaskAccepted{
		TaskID:      taskID,
		State:       runtimeEventStateCompleted,
		Duplicate:   false,
		CreatedAtMs: runtimeSelectionNowMs(),
	}, nil
}

// sourceRuntimeID 生成第三方来源的 runtimeId。
//
// 形式沿用 P3-09：`<来源>-<platform>-<arch>-<version>`，例如
// `adoptium-windows-x64-21.0.13`。借用 buildRuntimeID 是为了让两套来源的
// 标识规则完全一致，避免下游按 runtimeId 解析平台时出现两种格式。
func sourceRuntimeID(release *AdoptiumRelease) (string, *AppError) {
	if release == nil {
		return "", newInputError(ErrorCodeRuntimeInstall, messageRuntimeIDInvalid)
	}
	token := sourceVersionToken(release.FullVersion)
	if token == "" {
		return "", newInputError(ErrorCodeRuntimeInstall, messageRuntimeIDInvalid)
	}
	return buildRuntimeID(adoptiumComponentKey, "", token)
}

// ensureSourceRuntimeIntact 判断已安装目录是否可用于跳过重复安装。
//
// 只校验「可执行文件存在且非空」：这是最低限度的可用性判定，真正的完整性校验
// 需要清单（Adoptium 不提供逐文件清单），因此这里不假装做了全量校验。
func ensureSourceRuntimeIntact(finalDir string, release *AdoptiumRelease) *AppError {
	if release == nil {
		return newInputError(ErrorCodeRuntimeInstall, messageRuntimeIDInvalid)
	}
	executablePath := filepath.Join(finalDir, filepath.FromSlash(release.ExecutableRel))
	if isRegularFile(executablePath) {
		return nil
	}
	return newInputError(ErrorCodeRuntimeNotFound, messageRuntimeNotFound)
}

// isRegularFile 判断路径是否存在且为非空普通文件。
func isRegularFile(path string) bool {
	info, statErr := os.Stat(path)
	if statErr != nil {
		return false
	}
	return info.Mode().IsRegular() && info.Size() > 0
}

// saveSourceRuntimeRow 把第三方 Runtime 写入 `java_runtimes`。
func saveSourceRuntimeRow(ctx context.Context, release *AdoptiumRelease, runtimeID string) *AppError {
	nowMs := runtimeSelectionNowMs()
	row := runtimeRow{
		ID:                runtimeID,
		Kind:              SourceDownloaded,
		Component:         "",
		MajorVersion:      release.Major,
		FullVersion:       release.FullVersion,
		Platform:          currentPlatformName(),
		Arch:              currentArch(),
		Bitness:           bitnessValue(currentBitness()),
		Vendor:            VendorOpenJDK,
		SourceURI:         stripQuery(release.OfficialURL),
		SHA256:            release.SHA256,
		SourceName:        sourceNameAdoptium,
		LocalRelPath:      artifact.DirRuntimes + "/" + runtimeID,
		VerifiedAtMs:      nowMs,
		CreatedAtMs:       nowMs,
		LatestCheckedAtMs: nowMs,
		LatestFullVersion: release.FullVersion,
	}
	return saveRuntime(ctx, row)
}

// cleanupRuntimeStaging 删除 staging 目录（失败只忽略，不覆盖原始错误）。
func cleanupRuntimeStaging(stagingRoot string) {
	_ = os.RemoveAll(stagingRoot)
}

// downloadAdoptiumArchive 按候选顺序下载归档到 archivePath。
//
// 写盘走 P3-05 的 `download.StagingSink` 接缝（与官方 Runtime 安装共用同一套
// 「staging 内写入」语义），因此这里的 HTTP 代码只负责把字节搬进 writer。
func downloadAdoptiumArchive(ctx context.Context, candidates []string, archivePath string, expectedSize int64, progress *installProgress) *AppError {
	if len(candidates) == 0 {
		return newExternalError(ErrorCodeRuntimeSource, messageRuntimeSourceUnreachable, true)
	}
	client := adoptiumHTTPClientFactory()
	stagingRoot := filepath.Dir(archivePath)
	sink := &runtimeStagingSink{stagingRoot: stagingRoot}
	task := download.Task{TargetRelPath: filepath.Base(archivePath)}

	var lastErr *AppError
	for _, candidate := range candidates {
		if appErr := validateAdoptiumDownloadURL(candidate); appErr != nil {
			lastErr = appErr
			continue
		}
		writer, openErr := sink.Open(ctx, task, 0, true)
		if openErr != nil {
			return newSystemError(ErrorCodeRuntimeInstall, messageRuntimeStagingFailed, false)
		}
		appErr := fetchAdoptiumArchiveInto(ctx, client, candidate, writer, expectedSize, progress)
		if appErr == nil {
			return nil
		}
		lastErr = appErr
		// 半写文件必须清掉：下一次候选会用 O_TRUNC 重写，但留着脏文件会让
		// 失败路径下的 staging 更难诊断。
		_ = os.Remove(archivePath)
	}
	if lastErr == nil {
		lastErr = newExternalError(ErrorCodeRuntimeSource, messageRuntimeSourceUnreachable, true)
	}
	return lastErr
}

// progressReader 在每次 Read 后把字节数累加给 installProgress。
//
// 为什么包在读取侧而不是写入侧：io.CopyBuffer 会优先使用 writer 的 ReadFrom
// 方法，包装 writer 可能在优化路径下被绕过；包装 reader 则一定会被调用。
type progressReader struct {
	reader   io.Reader
	progress *installProgress
}

// Read 实现 io.Reader，并把本次读到的字节数计入进度。
func (r *progressReader) Read(buffer []byte) (int, error) {
	read, err := r.reader.Read(buffer)
	if read > 0 {
		r.progress.addBytes(int64(read))
	}
	return read, err
}

// fetchAdoptiumArchiveInto 把单个候选地址的归档写入 writer。
func fetchAdoptiumArchiveInto(
	ctx context.Context,
	client *http.Client,
	rawURL string,
	writer download.StagingWriter,
	expectedSize int64,
	progress *installProgress,
) *AppError {
	closeWriter := func() {
		_ = writer.Close()
	}
	request, buildErr := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if buildErr != nil {
		closeWriter()
		return newInputError(ErrorCodeRuntimeSource, messageRuntimeSourceRequestFailed)
	}
	response, doErr := client.Do(request)
	if doErr != nil {
		closeWriter()
		if ctx.Err() != nil {
			return newExternalError(ErrorCodeRuntimeSource, messageRuntimeSourceTimeout, true)
		}
		var redirectErr *adoptiumRedirectError
		if errors.As(doErr, &redirectErr) {
			return newExternalError(ErrorCodeRuntimeSource, messageRuntimeSourceRedirectRejected, false)
		}
		return newExternalError(ErrorCodeRuntimeSource, messageRuntimeSourceUnreachable, true)
	}
	defer func() {
		_ = response.Body.Close()
	}()

	if response.StatusCode == http.StatusNotFound {
		closeWriter()
		// 镜像滞后会造成 404：下一次候选（官方地址）还可能成功，因此标记可重试，
		// 由 downloadAdoptiumArchive 决定是否换下一个候选。
		return newExternalError(ErrorCodeRuntimeSource, messageRuntimeSourceNotFound, true)
	}
	if response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500 {
		closeWriter()
		return newExternalError(ErrorCodeRuntimeSource, messageRuntimeSourceUnreachable, true)
	}
	if response.StatusCode != http.StatusOK {
		closeWriter()
		return newExternalError(ErrorCodeRuntimeSource, messageRuntimeSourceStatusFailed, false)
	}

	body := io.LimitReader(response.Body, expectedSize+1)
	// 读取侧计数：见 progressReader 的说明。
	var source io.Reader = body
	if progress != nil {
		source = &progressReader{reader: body, progress: progress}
	}
	written, copyErr := io.CopyBuffer(writer, source, make([]byte, adoptiumCopyBufferSize))
	if copyErr != nil {
		closeWriter()
		return newExternalError(ErrorCodeRuntimeSource, messageRuntimeSourceUnreachable, true)
	}
	if written > expectedSize {
		closeWriter()
		return newExternalError(ErrorCodeRuntimeSource, messageRuntimeSourceTooLarge, false)
	}
	if written != expectedSize {
		closeWriter()
		return newExternalError(ErrorCodeRuntimeSource, messageRuntimeSourceSizeMismatch, true)
	}
	if flushErr := writer.Flush(); flushErr != nil {
		closeWriter()
		return newSystemError(ErrorCodeRuntimeInstall, messageRuntimeStagingFailed, false)
	}
	if closeErr := writer.Close(); closeErr != nil {
		return newSystemError(ErrorCodeRuntimeInstall, messageRuntimeStagingFailed, false)
	}
	return nil
}

// GetRuntimeSourceInfo 返回某个 Java 大版本在第三方来源上的最新构建信息。
//
// 只查询 Adoptium 的元数据（不下载归档），用于「Java 运行时」页展示候选来源。
// DownloadHosts 只放 host，不放完整地址——DTO 里不得出现可被误用的完整下载链接。
func GetRuntimeSourceInfo(major int) (RuntimeSourceInfo, *AppError) {
	release, fetchErr := FetchAdoptiumRelease(major, goruntime.GOOS, goruntime.GOARCH)
	if fetchErr != nil {
		return RuntimeSourceInfo{}, fetchErr
	}
	return RuntimeSourceInfo{
		Source:        sourceNameAdoptium,
		MajorVersion:  release.Major,
		Semver:        release.Semver,
		FullVersion:   release.FullVersion,
		OS:            release.OS,
		Arch:          release.Arch,
		SizeBytes:     release.SizeBytes,
		SHA256:        release.SHA256,
		DownloadHosts: buildDownloadHosts(adoptiumMirrorEnabled()),
	}, nil
}

// buildDownloadHosts 返回候选来源的 host 列表（不放完整地址）。
func buildDownloadHosts(mirrorEnabled bool) []string {
	hosts := []string{adoptiumOfficialHost}
	if mirrorEnabled {
		hosts = append(hosts, adoptiumMirrorHost)
	}
	return hosts
}

// CheckRuntimeUpdates 检查已安装的第三方 Runtime 是否有小版本更新。
//
// 只检查 `kind='downloaded'` 且 `source_name='adoptium'` 的行；
// 同一个 major 只请求一次 API（用 map 缓存），任一 major 失败不影响其它项。
func CheckRuntimeUpdates() ([]RuntimeUpdateInfo, *AppError) {
	ctx, cancel := context.WithTimeout(context.Background(), adoptiumOverallTimeout*2)
	defer cancel()
	return checkRuntimeUpdates(ctx)
}

// checkRuntimeUpdates 是 CheckRuntimeUpdates 的可注入实现。
func checkRuntimeUpdates(ctx context.Context) ([]RuntimeUpdateInfo, *AppError) {
	rows, listErr := installedRuntimeRows(ctx)
	if listErr != nil {
		return nil, listErr
	}
	nowMs := runtimeSelectionNowMs()
	type fetchOutcome struct {
		release *AdoptiumRelease
		err     *AppError
	}
	cache := make(map[int]fetchOutcome, 4)
	result := make([]RuntimeUpdateInfo, 0, len(rows))

	for index := range rows {
		row := rows[index]
		if !isAdoptiumRuntimeRow(row) {
			continue
		}
		info := RuntimeUpdateInfo{
			RuntimeID:            row.ID,
			MajorVersion:         row.MajorVersion,
			InstalledFullVersion: row.FullVersion,
			CheckedAtMs:          nowMs,
		}
		outcome, cached := cache[row.MajorVersion]
		if !cached {
			release, fetchErr := FetchAdoptiumRelease(row.MajorVersion, goruntime.GOOS, goruntime.GOARCH)
			outcome = fetchOutcome{release: release, err: fetchErr}
			cache[row.MajorVersion] = outcome
		}
		if outcome.err != nil {
			info.CheckError = outcome.err.Message
			result = append(result, info)
			continue
		}
		info.LatestFullVersion = outcome.release.FullVersion
		info.LatestSizeBytes = outcome.release.SizeBytes
		installedTuple, installedOK := parseJavaVersionTuple(row.FullVersion)
		latestTuple, latestOK := parseJavaVersionTuple(outcome.release.FullVersion)
		switch {
		case !installedOK:
			info.CheckError = messageRuntimeUpdateInstalledParseFailed
		case !latestOK:
			info.CheckError = messageRuntimeUpdateLatestParseFailed
		default:
			info.UpdateAvailable = compareJavaVersionTuple(latestTuple, installedTuple) > 0
		}
		if saveErr := updateRuntimeLatest(ctx, row.ID, nowMs, info.LatestFullVersion); saveErr != nil {
			// 写库失败只影响「下次打开页面能否离线看到结果」，不该让整次检查失败。
			runtimeLog().Warn("写回 Runtime 更新检查结果失败", logging.Fields{
				Event:      "runtime.update.save_failed",
				ErrorCode:  saveErr.Code,
				Attributes: map[string]string{"runtimeId": row.ID},
			})
		}
		result = append(result, info)
	}
	sortRuntimeUpdates(result)
	return result, nil
}

// ListRuntimeUpdates 返回上次检查的缓存结果，**不触发任何网络请求**。
//
// 与 CheckRuntimeUpdates 分开是冻结契约的要求：页面打开时可以先渲染上次结果，
// 由用户显式点击「检查更新」再走网络。
func ListRuntimeUpdates() ([]RuntimeUpdateInfo, *AppError) {
	rows, listErr := installedRuntimeRows(context.Background())
	if listErr != nil {
		return nil, listErr
	}
	result := make([]RuntimeUpdateInfo, 0, len(rows))
	for index := range rows {
		row := rows[index]
		if !isAdoptiumRuntimeRow(row) {
			continue
		}
		info := RuntimeUpdateInfo{
			RuntimeID:            row.ID,
			MajorVersion:         row.MajorVersion,
			InstalledFullVersion: row.FullVersion,
			LatestFullVersion:    row.LatestFullVersion,
			CheckedAtMs:          row.LatestCheckedAtMs,
		}
		if strings.TrimSpace(row.LatestFullVersion) == "" {
			info.CheckError = messageRuntimeUpdateNotChecked
			result = append(result, info)
			continue
		}
		installedTuple, installedOK := parseJavaVersionTuple(row.FullVersion)
		latestTuple, latestOK := parseJavaVersionTuple(row.LatestFullVersion)
		switch {
		case !installedOK:
			info.CheckError = messageRuntimeUpdateInstalledParseFailed
		case !latestOK:
			info.CheckError = messageRuntimeUpdateLatestParseFailed
		default:
			info.UpdateAvailable = compareJavaVersionTuple(latestTuple, installedTuple) > 0
		}
		result = append(result, info)
	}
	sortRuntimeUpdates(result)
	return result, nil
}

// isAdoptiumRuntimeRow 判断一行记录是否属于本文件管理的第三方 Runtime。
func isAdoptiumRuntimeRow(row runtimeRow) bool {
	return row.Kind == SourceDownloaded && strings.EqualFold(row.SourceName, sourceNameAdoptium)
}

// sortRuntimeUpdates 按主版本降序、runtimeId 升序排序，保证输出稳定可断言。
func sortRuntimeUpdates(items []RuntimeUpdateInfo) {
	sort.SliceStable(items, func(left, right int) bool {
		if items[left].MajorVersion != items[right].MajorVersion {
			return items[left].MajorVersion > items[right].MajorVersion
		}
		return items[left].RuntimeID < items[right].RuntimeID
	})
}

// readMirrorEnabledSetting 读取镜像开关（D-57）。
//
// 唯一读法是下载模块的 Provider：装配层把设置服务里的
// `download.mirrorEnabled` 包成函数注入 internal/download，
// 本包（以及下载包）都从那里读，避免「自己再开一次数据库读同一个 key」的第二套实现。
// 未注入 Provider 时返回 false——镜像默认关闭是安全默认。
func readMirrorEnabledSetting() bool {
	return download.MirrorEnabledFromProvider()
}
