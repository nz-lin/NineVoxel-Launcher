package runtime

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	// zuluAPIBaseURL 是 Azul Metadata API 的根地址。
	zuluAPIBaseURL = "https://api.azul.com"
	// zuluAPIHost 是元数据请求唯一允许的 host。
	zuluAPIHost = "api.azul.com"
	// zuluCDNHost 是归档下载允许的 host。
	zuluCDNHost = "cdn.azul.com"
	// zuluAPIMaxBytes 是 API 响应体的读取上限（4 MiB）。
	zuluAPIMaxBytes = 4 << 20
	// zuluPageSize 是包列表查询的单页数量。
	zuluPageSize = 20
)

// zuluPlatform 描述目标平台到 Azul 查询参数的映射。
type zuluPlatform struct {
	// APIOS 是查询参数 `os` 的取值。
	APIOS string
	// ArchiveType 是查询参数 `archive_type` 的取值。
	ArchiveType string
	// ExecutableRel 是解压后必须存在的 java 可执行文件相对路径。
	ExecutableRel string
}

var zuluPlatforms = map[string]zuluPlatform{
	"windows": {APIOS: "windows", ArchiveType: "zip", ExecutableRel: "bin/java.exe"},
	"linux":   {APIOS: "linux", ArchiveType: "tar.gz", ExecutableRel: "bin/java"},
	"darwin":  {APIOS: "macos", ArchiveType: "tar.gz", ExecutableRel: "bin/java"},
}

var zuluArchitectures = map[string]string{
	"amd64": "x64",
	"arm64": "aarch64",
}

// zuluDownloadHosts 是归档下载与重定向允许的 host 集合。
var zuluDownloadHosts = map[string]bool{
	zuluAPIHost: true,
	zuluCDNHost: true,
}

// zuluHTTPClientFactory 返回访问 Azul 的 HTTP 客户端；测试替换为假实现。
var zuluHTTPClientFactory = newZuluHTTPClient

// newZuluHTTPClient 构造带白名单与超时的 HTTP 客户端。
func newZuluHTTPClient() *http.Client {
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
		CheckRedirect: checkZuluRedirect,
	}
}

// checkZuluRedirect 校验每一次重定向的目标。
func checkZuluRedirect(request *http.Request, via []*http.Request) error {
	if request == nil {
		return &sourceRedirectError{message: "重定向请求为空"}
	}
	if len(via) >= adoptiumMaxRedirects {
		return &sourceRedirectError{message: "重定向次数超过上限"}
	}
	if !strings.EqualFold(request.URL.Scheme, "https") {
		return &sourceRedirectError{message: "重定向目标不是 HTTPS 地址"}
	}
	if !zuluDownloadHosts[strings.ToLower(request.URL.Hostname())] {
		return &sourceRedirectError{message: "重定向目标不在允许的来源列表中"}
	}
	return nil
}

// zuluPackageSummary 是包列表接口返回的一项。
type zuluPackageSummary struct {
	PackageUUID string `json:"package_uuid"`
	Name        string `json:"name"`
	Product     string `json:"product"`
	DownloadURL string `json:"download_url"`
	JavaVersion []int  `json:"java_version"`
}

// zuluPackageDetail 是包详情接口返回的字段子集。
type zuluPackageDetail struct {
	PackageUUID string `json:"package_uuid"`
	Name        string `json:"name"`
	DownloadURL string `json:"download_url"`
	SHA256Hash  string `json:"sha256_hash"`
	Size        int64  `json:"size"`
	JavaVersion []int  `json:"java_version"`
}

// zuluPlatformInfo 返回目标平台的 Azul 映射。
func zuluPlatformInfo(goos, goarch string) (zuluPlatform, string, bool) {
	platform, ok := zuluPlatforms[strings.ToLower(strings.TrimSpace(goos))]
	if !ok {
		return zuluPlatform{}, "", false
	}
	arch, ok := zuluArchitectures[strings.ToLower(strings.TrimSpace(goarch))]
	if !ok {
		return zuluPlatform{}, "", false
	}
	return platform, arch, true
}

// fetchZuluRelease 查询某个 Java 大版本在 Azul Zulu 上的最新构建。
func fetchZuluRelease(ctx context.Context, client *http.Client, major int, goos, goarch string) (*AdoptiumRelease, *AppError) {
	if major <= 0 {
		return nil, newInputError(ErrorCodeRuntimeRequirement, messageRuntimeMajorRequired)
	}
	platform, arch, supported := zuluPlatformInfo(goos, goarch)
	if !supported {
		return nil, newInputError(ErrorCodeRuntimeRequirement, messageRuntimeSourcePlatformUnsupported)
	}
	if client == nil {
		client = zuluHTTPClientFactory()
	}

	listURL := buildZuluPackagesURL(major, platform, arch)
	body, fetchErr := fetchZuluDocument(ctx, client, listURL)
	if fetchErr != nil {
		return nil, fetchErr
	}
	var packages []zuluPackageSummary
	if unmarshalErr := json.Unmarshal(body, &packages); unmarshalErr != nil {
		return nil, newExternalError(ErrorCodeRuntimeSource, messageRuntimeSourceMalformed, false)
	}
	summary := selectZuluPackage(packages)
	if summary == nil {
		return nil, newExternalError(ErrorCodeRuntimeSource, messageRuntimeSourceEmpty, false)
	}
	if !isZuluPackageUUID(summary.PackageUUID) {
		return nil, newExternalError(ErrorCodeRuntimeSource, messageRuntimeSourcePackageMissing, false)
	}

	detailURL := zuluAPIBaseURL + "/metadata/v1/zulu/packages/" + url.PathEscape(summary.PackageUUID)
	detailBody, detailErr := fetchZuluDocument(ctx, client, detailURL)
	if detailErr != nil {
		return nil, detailErr
	}
	var detail zuluPackageDetail
	if unmarshalErr := json.Unmarshal(detailBody, &detail); unmarshalErr != nil {
		return nil, newExternalError(ErrorCodeRuntimeSource, messageRuntimeSourceMalformed, false)
	}

	checksum := strings.ToLower(strings.TrimSpace(detail.SHA256Hash))
	if !isSHA256Hex(checksum) {
		return nil, newExternalError(ErrorCodeRuntimeSource, messageRuntimeSourceChecksumInvalid, false)
	}
	officialURL := strings.TrimSpace(detail.DownloadURL)
	if officialURL == "" {
		officialURL = strings.TrimSpace(summary.DownloadURL)
	}
	if officialURL == "" {
		return nil, newExternalError(ErrorCodeRuntimeSource, messageRuntimeSourcePackageMissing, false)
	}
	fileName, nameErr := adoptiumArchiveFileName(officialURL)
	if nameErr != nil {
		return nil, nameErr
	}
	size := detail.Size
	if size <= 0 || size > runtimeMaxTotalBytes {
		return nil, newExternalError(ErrorCodeRuntimeSource, messageRuntimeSourceSizeInvalid, false)
	}
	// Azul 元数据里的 size 是取整值（实测会少几百字节），直接用于精确校验会让
	// 下载在「写入字节数超出声明值」处被误判。这里以归档的真实 Content-Length
	// 为准；HEAD 不支持或拿不到长度时退回元数据值并继续。
	if headSize, headErr := fetchZuluArchiveSize(ctx, client, officialURL); headErr == nil {
		size = headSize
	}

	javaVersion := detail.JavaVersion
	if len(javaVersion) == 0 {
		javaVersion = summary.JavaVersion
	}
	fullVersion := zuluVersionString(javaVersion)
	if fullVersion == "" {
		return nil, newExternalError(ErrorCodeRuntimeSource, messageRuntimeSourceVersionMissing, false)
	}

	return &AdoptiumRelease{
		Major:         major,
		Semver:        fullVersion,
		FullVersion:   fullVersion,
		OS:            platform.APIOS,
		Arch:          arch,
		OfficialURL:   officialURL,
		FileName:      fileName,
		SizeBytes:     size,
		SHA256:        checksum,
		ExecutableRel: platform.ExecutableRel,
	}, nil
}

// buildZuluPackagesURL 拼接包列表查询地址。
func buildZuluPackagesURL(major int, platform zuluPlatform, arch string) string {
	query := baseZuluQuery(platform, arch)
	query.Set("java_version", strconv.Itoa(major))
	query.Set("latest", "true")
	query.Set("page_size", strconv.Itoa(zuluPageSize))
	return zuluAPIBaseURL + "/metadata/v1/zulu/packages/?" + query.Encode()
}

// buildZuluLtsPackagesURL 拼接 LTS 大版本查询地址。
//
// 用 `support_term=lts` 过滤，而不是在前端或代码里维护 LTS 常量；
// 接口返回的 java_version 去重后就是当前可用的 LTS 列表。
func buildZuluLtsPackagesURL(platform zuluPlatform, arch string) string {
	query := baseZuluQuery(platform, arch)
	query.Set("support_term", "lts")
	query.Set("latest", "true")
	query.Set("page_size", "1000")
	return zuluAPIBaseURL + "/metadata/v1/zulu/packages/?" + query.Encode()
}

// baseZuluQuery 构造平台与包类型相关的公共查询参数。
func baseZuluQuery(platform zuluPlatform, arch string) url.Values {
	query := url.Values{}
	query.Set("os", platform.APIOS)
	query.Set("arch", arch)
	query.Set("archive_type", platform.ArchiveType)
	query.Set("java_package_type", "jdk")
	query.Set("release_status", "ga")
	query.Set("availability_types", "CA")
	query.Set("page", "1")
	return query
}

// fetchZuluLtsVersions 从 Azul Metadata API 读取 LTS 大版本列表。
func fetchZuluLtsVersions(ctx context.Context, client *http.Client, goos, goarch string) (RuntimeLtsVersions, *AppError) {
	platform, arch, supported := zuluPlatformInfo(goos, goarch)
	if !supported {
		return RuntimeLtsVersions{}, newInputError(ErrorCodeRuntimeRequirement, messageRuntimeSourcePlatformUnsupported)
	}
	if client == nil {
		client = zuluHTTPClientFactory()
	}
	body, fetchErr := fetchZuluDocument(ctx, client, buildZuluLtsPackagesURL(platform, arch))
	if fetchErr != nil {
		return RuntimeLtsVersions{}, fetchErr
	}
	var packages []zuluPackageSummary
	if unmarshalErr := json.Unmarshal(body, &packages); unmarshalErr != nil {
		return RuntimeLtsVersions{}, newExternalError(ErrorCodeRuntimeSource, messageRuntimeSourceMalformed, false)
	}
	majors := make([]int, 0, len(packages))
	for index := range packages {
		pkg := &packages[index]
		if !isPlainZuluJDK(pkg) || len(pkg.JavaVersion) == 0 {
			continue
		}
		majors = append(majors, pkg.JavaVersion[0])
	}
	versions, normalizeErr := normalizeLtsVersions(majors)
	if normalizeErr != nil {
		return RuntimeLtsVersions{}, normalizeErr
	}
	return RuntimeLtsVersions{Versions: versions, MostRecentLts: versions[0]}, nil
}

// fetchZuluDocument 发起一次受限的 GET 请求并返回响应体。
func fetchZuluDocument(ctx context.Context, client *http.Client, rawURL string) ([]byte, *AppError) {
	return fetchSourceDocument(ctx, client, rawURL, zuluAPIHost, "请求 Azul 元数据接口失败", zuluAPIMaxBytes)
}

// selectZuluPackage 从 latest 列表里选出普通 JDK 包（排除 FX / CRaC 变体）。
//
// Azul 会给同一个 java_version 返回多个 `latest=true` 的变体，顺序不保证；
// 这里按 java_version 取最高者，避免拿到旧构建或带 JavaFX 的包。
func selectZuluPackage(packages []zuluPackageSummary) *zuluPackageSummary {
	var selected *zuluPackageSummary
	for index := range packages {
		pkg := &packages[index]
		if !isPlainZuluJDK(pkg) {
			continue
		}
		if selected == nil || compareZuluVersions(pkg.JavaVersion, selected.JavaVersion) > 0 {
			selected = pkg
		}
	}
	return selected
}

// isPlainZuluJDK 判断包名是否是不带 JavaFX / CRaC 的普通 JDK。
func isPlainZuluJDK(pkg *zuluPackageSummary) bool {
	if pkg == nil {
		return false
	}
	if pkg.Product != "" && !strings.EqualFold(pkg.Product, "zulu") {
		return false
	}
	name := strings.ToLower(strings.TrimSpace(pkg.Name))
	return strings.Contains(name, "-ca-jdk")
}

// compareZuluVersions 逐段比较版本号数组。
func compareZuluVersions(left, right []int) int {
	limit := len(left)
	if len(right) > limit {
		limit = len(right)
	}
	for index := 0; index < limit; index++ {
		leftValue := 0
		if index < len(left) {
			leftValue = left[index]
		}
		rightValue := 0
		if index < len(right) {
			rightValue = right[index]
		}
		if leftValue != rightValue {
			return leftValue - rightValue
		}
	}
	return 0
}

// zuluVersionString 把版本数组拼成 `21.0.13.0.3` 形式。
func zuluVersionString(parts []int) string {
	if len(parts) == 0 {
		return ""
	}
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		if part < 0 {
			return ""
		}
		values = append(values, strconv.Itoa(part))
	}
	return strings.Join(values, ".")
}

// isZuluPackageUUID 校验 package_uuid 只含十六进制与连字符。
func isZuluPackageUUID(value string) bool {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return false
	}
	for _, char := range trimmed {
		switch {
		case char >= '0' && char <= '9':
		case char >= 'a' && char <= 'f':
		case char >= 'A' && char <= 'F':
		case char == '-':
		default:
			return false
		}
	}
	return true
}

// fetchZuluArchiveSize 通过 HEAD 请求读取归档的真实字节数。
//
// Azul 元数据里的 size 与真实 Content-Length 存在少量偏差，只有后者能用于
// 严格的写入校验；HEAD 失败时由调用方决定是否退回元数据值。
func fetchZuluArchiveSize(ctx context.Context, client *http.Client, rawURL string) (int64, *AppError) {
	if appErr := validateZuluDownloadURL(rawURL); appErr != nil {
		return 0, appErr
	}
	if client == nil {
		client = zuluHTTPClientFactory()
	}
	request, buildErr := http.NewRequestWithContext(ctx, http.MethodHead, rawURL, nil)
	if buildErr != nil {
		return 0, newInputError(ErrorCodeRuntimeSource, messageRuntimeSourceRequestFailed)
	}
	response, doErr := client.Do(request)
	if doErr != nil {
		if ctx.Err() != nil {
			return 0, newExternalError(ErrorCodeRuntimeSource, messageRuntimeSourceTimeout, true)
		}
		return 0, newExternalError(ErrorCodeRuntimeSource, messageRuntimeSourceUnreachable, true)
	}
	defer func() {
		_ = response.Body.Close()
	}()
	if response.StatusCode != http.StatusOK {
		return 0, newExternalError(ErrorCodeRuntimeSource, messageRuntimeSourceSizeInvalid, false)
	}
	size := response.ContentLength
	if size <= 0 || size > runtimeMaxTotalBytes {
		return 0, newExternalError(ErrorCodeRuntimeSource, messageRuntimeSourceSizeInvalid, false)
	}
	return size, nil
}

// zuluDownloadCandidates 返回归档的候选下载地址；Zulu 暂无镜像，只走官方。
func zuluDownloadCandidates(release *AdoptiumRelease, _ bool) []string {
	if release == nil {
		return nil
	}
	official := strings.TrimSpace(release.OfficialURL)
	if official == "" {
		return nil
	}
	return []string{official}
}

// validateZuluDownloadURL 校验归档地址是否允许请求。
func validateZuluDownloadURL(rawURL string) *AppError {
	parsed, parseErr := url.Parse(strings.TrimSpace(rawURL))
	if parseErr != nil || parsed == nil {
		return newInputError(ErrorCodeRuntimeSource, messageRuntimeSourceRequestFailed)
	}
	if !strings.EqualFold(parsed.Scheme, "https") {
		return newInputError(ErrorCodeRuntimeSource, messageRuntimeSourceNotHTTPS)
	}
	if !zuluDownloadHosts[strings.ToLower(parsed.Hostname())] {
		return newInputError(ErrorCodeRuntimeSource, messageRuntimeSourceHostNotAllowed)
	}
	return nil
}
