// 本文件实现版本清单的获取、结构校验、文件缓存与离线降级。
//
// 处理链路：
//
//	ListVersions / GetManifestStatus / RefreshManifest
//	  → resolveDataRoot（数据根由 DataRootProvider 注入，本包不依赖 artifact 包）
//	  → ensureSnapshotLocked（内存 → 磁盘缓存 → 网络刷新）
//	  → 缓存过期时尝试刷新；刷新失败则保留旧缓存并标 stale
//
// 缓存布局（接口冻结清单 §6）：
//
//	<DataRoot>/artifacts/metadata/version_manifest_v2.json        官方原始字节
//	<DataRoot>/artifacts/metadata/version_manifest_v2.json.meta.json  获取时间/ETag/SHA-256
//
// 写入采用「临时文件 + rename」的原子替换：中途失败时旧缓存保持可读。
package version

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"example.com/ninevoxel/launcher/internal/logging"
)

// 清单相关的常量。
const (
	// manifestURL 是官方版本清单地址（固定，不接受请求参数覆盖）。
	manifestURL = "https://piston-meta.mojang.com/mc/game/version_manifest_v2.json"

	// manifestSourceHost 是清单的官方来源 host。
	manifestSourceHost = "piston-meta.mojang.com"

	// manifestCacheFileName 是缓存文件的名称。
	manifestCacheFileName = "version_manifest_v2.json"

	// manifestMetaFileName 是缓存元数据的名称。
	manifestMetaFileName = "version_manifest_v2.json.meta.json"

	// artifactMetadataDirName 是数据根下的元数据目录（相对路径，使用 POSIX 分隔符）。
	artifactMetadataDirName = "artifacts/metadata"

	// manifestCacheTTL 是缓存软过期时长：超过后仍可展示，但会尝试刷新。
	manifestCacheTTL = 24 * time.Hour

	// manifestHTTPTimeout 是单次清单请求的总超时（含重定向）。
	manifestHTTPTimeout = 15 * time.Second

	// manifestMaxBodyBytes 是响应体读取上限（8 MiB）。
	manifestMaxBodyBytes = 8 << 20

	// manifestMaxVersions 是清单条目数上限。
	manifestMaxVersions = 5000

	// manifestMaxSingleVersionSize 是单条版本 JSON 的大小上限（1 MiB）。
	manifestMaxSingleVersionSize = 1 << 20

	// manifestMaxRedirects 是允许的最大重定向跳数。
	manifestMaxRedirects = 3

	// manifestMaxVersionIDLen 是版本标识的最大长度。
	manifestMaxVersionIDLen = 64

	// versionListDefaultLimit 是版本列表的默认单页条数。
	versionListDefaultLimit = 50

	// versionListMaxLimit 是版本列表的单页条数上限。
	versionListMaxLimit = 200

	// sha1HexLength 是 SHA-1 十六进制字符串的长度。
	sha1HexLength = 40
)

// 版本类型取值（与清单中的 type 字段一致）。
const (
	// versionTypeRelease 是正式版。
	versionTypeRelease = "release"

	// versionTypeSnapshot 是快照版。
	versionTypeSnapshot = "snapshot"

	// versionTypeOldBeta 是旧测试版。
	versionTypeOldBeta = "old_beta"

	// versionTypeOldAlpha 是旧 Alpha 版。
	versionTypeOldAlpha = "old_alpha"
)

// manifestAllowedHosts 是允许的清单来源 host 白名单。
//
// 写死在包内、不接受请求参数覆盖；清单只从官方源获取，不经过镜像。
// 完整白名单同时包含后续下载域，但本卡只使用 piston-meta。
var manifestAllowedHosts = map[string]bool{
	"piston-meta.mojang.com":           true,
	"launcher.mojang.com":              true,
	"launchermeta.mojang.com":          true,
	"resources.download.minecraft.net": true,
	"libraries.minecraft.net":          true,
}

// DataRootProvider 由外部注入数据根解析函数，避免本包在编译期依赖 artifact 包。
//
// 返回数据根的绝对路径；失败时返回错误（此时缓存读写不可用）。
type DataRootProvider func() (string, error)

// 包级可注入点。
//
// 这些变量只在包内可见：生产代码不可替换，测试可临时改写以注入假时钟、
// 假 HTTP 传输与假 rename，从而在不联网、不触碰真实文件的前提下覆盖失败分支。
var (
	// nowFunc 返回当前时间，测试可替换以模拟 TTL 过期。
	nowFunc = time.Now

	// newManifestHTTPClient 构造清单请求客户端，测试可替换以注入假 Transport。
	newManifestHTTPClient = func() *http.Client {
		return &http.Client{
			Timeout:       manifestHTTPTimeout,
			CheckRedirect: checkManifestRedirect,
		}
	}

	// renameFile 是原子替换使用的 rename，测试可替换以模拟写入失败。
	renameFile = os.Rename
)

// 包级状态。
var (
	// stateMu 保护 dataRootProvider、currentSnapshot 与 lastManifestErrorCode。
	// 它同时充当刷新互斥：同一时刻只允许一个刷新流程。
	stateMu sync.Mutex

	// dataRootProvider 为数据根解析函数；未注入时为 nil。
	dataRootProvider DataRootProvider

	// currentSnapshot 为当前使用的清单状态（内存副本）；nil 表示尚未加载。
	currentSnapshot *manifestSnapshot

	// lastManifestErrorCode 记录最近一次获取失败的稳定错误码。
	lastManifestErrorCode string
)

// logger 返回版本模块的日志器。
//
// 日志只记录条数、耗时、stale 与错误码，不记录 URL 查询串、响应体或用户目录。
func logger() *logging.Logger {
	return logging.Default.WithComponent("version")
}

// SetDataRootProvider 设置数据根提供者。
//
// 由装配层在 P3-03 完成后注入（例如指向 artifact.DataRoot）；
// 未注入时任何缓存读写都返回 VERSION_LAYOUT_001，而不是静默使用当前目录。
func SetDataRootProvider(provider DataRootProvider) {
	stateMu.Lock()
	defer stateMu.Unlock()

	dataRootProvider = provider
}

// resolveDataRoot 解析数据根目录。
//
// 返回绝对路径；未注入或 provider 返回空路径/错误时返回 VERSION_LAYOUT_001。
func resolveDataRoot() (string, *AppError) {
	stateMu.Lock()
	provider := dataRootProvider
	stateMu.Unlock()

	if provider == nil {
		return "", newSystemError(ErrorCodeDataRootUnavailable, messageDataRootUnavailable, false)
	}

	root, err := provider()
	root = strings.TrimSpace(root)
	if err != nil || root == "" {
		return "", newSystemError(ErrorCodeDataRootUnavailable, messageDataRootUnavailable, true)
	}
	return root, nil
}

// ListVersions 返回按类型过滤并分页后的版本列表。
//
// 行为：
//   - 优先使用内存中的清单；内存为空时读磁盘缓存；
//   - 缓存过期（超过 24 小时）时尝试刷新；刷新失败则返回旧缓存并标 stale；
//   - 完全没有缓存且刷新失败时返回错误码，**不返回空列表假装成功**。
//
// Type 为空时只返回 release + snapshot；显式传 old_beta / old_alpha 才返回旧预览版。
func ListVersions(req VersionListRequest) (VersionListResult, *AppError) {
	root, appErr := resolveDataRoot()
	if appErr != nil {
		return VersionListResult{}, appErr
	}

	stateMu.Lock()
	defer stateMu.Unlock()

	snapshot, appErr := ensureSnapshotLocked(root)
	if appErr != nil {
		return VersionListResult{}, appErr
	}

	types := parseTypeFilter(req.Type)
	filtered := make([]manifestVersion, 0, len(snapshot.versions))
	for _, item := range snapshot.versions {
		if types[item.versionType] {
			filtered = append(filtered, item)
		}
	}

	offset := req.Offset
	if offset < 0 {
		offset = 0
	}
	limit := req.Limit
	if limit <= 0 {
		limit = versionListDefaultLimit
	}
	if limit > versionListMaxLimit {
		limit = versionListMaxLimit
	}

	total := len(filtered)
	start := offset
	if start > total {
		start = total
	}
	end := start + limit
	if end > total {
		end = total
	}

	items := make([]VersionSummary, 0, end-start)
	for _, item := range filtered[start:end] {
		items = append(items, toVersionSummary(item, snapshot.verified))
	}

	return VersionListResult{
		Items:       items,
		Total:       total,
		Offset:      start,
		Limit:       limit,
		Stale:       snapshot.stale,
		FetchedAtMs: snapshot.fetchedAtMs,
		Verified:    snapshot.verified,
		WarningCode: snapshot.warningCode,
	}, nil
}

// GetManifestStatus 返回当前清单的可用性状态。
//
// 该函数**不发起网络请求**：只读内存与磁盘缓存，因此 UI 可以在离线时安全轮询。
// 数据根不可用时返回 VERSION_LAYOUT_001。
func GetManifestStatus() (ManifestStatus, *AppError) {
	root, appErr := resolveDataRoot()
	if appErr != nil {
		return ManifestStatus{}, appErr
	}

	stateMu.Lock()
	defer stateMu.Unlock()

	if currentSnapshot == nil {
		if cached, _ := loadCache(root); cached != nil {
			currentSnapshot = cached
		}
	}

	if currentSnapshot == nil {
		// 无缓存时仍返回「不可用」状态而不是错误：状态查询本身是诊断入口。
		return ManifestStatus{
			Available:     false,
			LastErrorCode: lastManifestErrorCode,
		}, nil
	}

	return statusOf(currentSnapshot), nil
}

// RefreshManifest 强制从官方源刷新清单并返回最新状态。
//
// 与 ListVersions 的降级策略不同：刷新失败时**返回错误**，让 UI 明确提示
// 「刷新失败，当前使用缓存」；成功时清空上次错误码。
func RefreshManifest() (ManifestStatus, *AppError) {
	root, appErr := resolveDataRoot()
	if appErr != nil {
		return ManifestStatus{}, appErr
	}

	stateMu.Lock()
	defer stateMu.Unlock()

	startedAt := nowFunc()
	fresh, fetchErr := fetchManifest(context.Background(), root)
	if fetchErr != nil {
		lastManifestErrorCode = fetchErr.Code
		logger().Warn("版本清单刷新失败", logging.Fields{
			ErrorCode: fetchErr.Code,
			Attributes: map[string]string{
				"durationMs": fmt.Sprintf("%d", nowFunc().Sub(startedAt).Milliseconds()),
			},
		})
		if currentSnapshot != nil {
			markStaleLocked(currentSnapshot, fetchErr.Code)
		}
		return statusOfOrEmpty(currentSnapshot), fetchErr
	}

	currentSnapshot = fresh
	lastManifestErrorCode = ""
	logger().Info("版本清单已刷新", logging.Fields{
		Attributes: map[string]string{
			"count":      fmt.Sprintf("%d", len(fresh.versions)),
			"stale":      "false",
			"durationMs": fmt.Sprintf("%d", nowFunc().Sub(startedAt).Milliseconds()),
		},
	})
	return statusOf(fresh), nil
}

// ensureSnapshotLocked 返回可用清单，必要时先刷新。
//
// 调用前提：持有 stateMu。返回的 snapshot 可能带 stale 标记，但不会是 nil。
func ensureSnapshotLocked(root string) (*manifestSnapshot, *AppError) {
	now := nowFunc()

	if currentSnapshot == nil {
		if cached, _ := loadCache(root); cached != nil {
			currentSnapshot = cached
		}
	}

	if currentSnapshot != nil && !expiredAt(currentSnapshot, now) {
		return currentSnapshot, nil
	}

	startedAt := now
	fresh, fetchErr := fetchManifest(context.Background(), root)
	if fetchErr == nil {
		currentSnapshot = fresh
		lastManifestErrorCode = ""
		return fresh, nil
	}

	lastManifestErrorCode = fetchErr.Code
	logger().Warn("版本清单获取失败", logging.Fields{
		ErrorCode: fetchErr.Code,
		Attributes: map[string]string{
			"hasCache":   fmt.Sprintf("%t", currentSnapshot != nil),
			"durationMs": fmt.Sprintf("%d", nowFunc().Sub(startedAt).Milliseconds()),
		},
	})

	if currentSnapshot != nil {
		// 离线降级：返回旧缓存并标记过期，列表仍可展示。
		markStaleLocked(currentSnapshot, fetchErr.Code)
		return currentSnapshot, nil
	}

	return nil, fetchErr
}

// markStaleLocked 把清单标记为过期并记录告警与错误码。
//
// 调用前提：持有 stateMu。
func markStaleLocked(snapshot *manifestSnapshot, errorCode string) {
	snapshot.stale = true
	snapshot.warningCode = WarningManifestStale
	if errorCode != "" {
		snapshot.lastErrorCode = errorCode
	}
}

// expiredAt 报告清单在给定时刻是否已软过期。
func expiredAt(snapshot *manifestSnapshot, now time.Time) bool {
	if snapshot == nil || snapshot.fetchedAtMs <= 0 {
		return true
	}
	return now.Sub(time.UnixMilli(snapshot.fetchedAtMs)) >= manifestCacheTTL
}

// statusOf 把清单状态转换为对外 DTO。
func statusOf(snapshot *manifestSnapshot) ManifestStatus {
	return ManifestStatus{
		Available:     true,
		FetchedAtMs:   snapshot.fetchedAtMs,
		Stale:         snapshot.stale,
		Count:         len(snapshot.versions),
		SourceHost:    snapshot.sourceHost,
		LastErrorCode: snapshot.lastErrorCode,
		Verified:      snapshot.verified,
	}
}

// statusOfOrEmpty 在 snapshot 为 nil 时返回不可用状态。
func statusOfOrEmpty(snapshot *manifestSnapshot) ManifestStatus {
	if snapshot == nil {
		return ManifestStatus{Available: false, LastErrorCode: lastManifestErrorCode}
	}
	return statusOf(snapshot)
}

// toVersionSummary 把内部条目转换为对外 DTO。
//
// 列表页**不解析**版本 JSON（逐个拉取会打爆上游），因此 Compat.Evaluated 为 false、
// 四层一律 false：前端看到 evaluated=false 时不渲染「支持 / 不支持」徽标，
// 而是提示「兼容性待评估（打开详情后计算）」。真实判定见 GetCompatReport。
func toVersionSummary(item manifestVersion, verified bool) VersionSummary {
	return VersionSummary{
		ID:          item.id,
		Type:        item.versionType,
		ReleaseTime: item.releaseTime,
		SHA1:        item.sha1,
		Size:        item.size,
		Installed:   false,
		Compat: CompatLayer{
			Evaluated:         false,
			MetadataSupported: false,
			InstallSupported:  false,
			LaunchSupported:   false,
			LoaderSupported:   false,
		},
		Verified: verified,
	}
}

// parseTypeFilter 解析类型过滤参数。
//
// 空值按「默认不展示旧预览版」处理，只返回 release + snapshot；
// 未知取值不匹配任何版本（不报错，也不自造错误码）。
func parseTypeFilter(raw string) map[string]bool {
	types := map[string]bool{}
	for _, part := range strings.Split(raw, ",") {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			types[trimmed] = true
		}
	}
	if len(types) == 0 {
		types[versionTypeRelease] = true
		types[versionTypeSnapshot] = true
	}
	return types
}

// fetchManifest 从官方源获取清单，校验后写入缓存并返回内存状态。
//
// 调用前提：持有 stateMu（保证同一时刻只有一个刷新流程）。
// 网络失败、状态码异常与非 304 的响应都按 VERSION_MANIFEST_001 返回；
// 结构非法返回 VERSION_MANIFEST_002；地址非法返回 VERSION_URL_001。
func fetchManifest(ctx context.Context, root string) (*manifestSnapshot, *AppError) {
	if appErr := validateManifestURL(manifestURL); appErr != nil {
		return nil, appErr
	}

	client := newManifestHTTPClient()

	var etag string
	if currentSnapshot != nil {
		etag = currentSnapshot.etag
	} else if cached, err := readCacheMeta(root); err == nil {
		etag = cached.ETag
	}

	reqCtx, cancel := context.WithTimeout(ctx, manifestHTTPTimeout)
	defer cancel()

	request, err := http.NewRequestWithContext(reqCtx, http.MethodGet, manifestURL, nil)
	if err != nil {
		return nil, newExternalError(ErrorCodeManifestFetch, messageManifestNetworkFailed, true)
	}
	request.Header.Set("Accept", "application/json")
	if etag != "" {
		request.Header.Set("If-None-Match", etag)
	}

	response, err := client.Do(request)
	if err != nil {
		var rejected *redirectRejectedError
		if errors.As(err, &rejected) {
			return nil, rejected.appErr
		}
		return nil, newExternalError(ErrorCodeManifestFetch, messageManifestNetworkFailed, true)
	}
	defer func() {
		_ = response.Body.Close()
	}()

	if response.StatusCode == http.StatusNotModified {
		return useNotModifiedSnapshot(root, etag)
	}

	if response.StatusCode != http.StatusOK {
		return nil, newExternalError(ErrorCodeManifestFetch, messageManifestStatusFailed, true)
	}

	raw, readErr := io.ReadAll(io.LimitReader(response.Body, manifestMaxBodyBytes+1))
	if readErr != nil {
		return nil, newExternalError(ErrorCodeManifestFetch, messageManifestNetworkFailed, true)
	}
	if len(raw) > manifestMaxBodyBytes {
		return nil, newExternalError(ErrorCodeManifestInvalid, messageManifestTooLarge, false)
	}

	snapshot, appErr := parseManifest(raw)
	if appErr != nil {
		return nil, appErr
	}

	meta := manifestMeta{
		FetchedAtMs: nowFunc().UnixMilli(),
		ETag:        response.Header.Get("ETag"),
		SHA256:      hashSHA256(raw),
		Size:        int64(len(raw)),
		SourceHost:  manifestSourceHost,
	}

	// 缓存写入失败不影响本次展示：内存状态仍然有效，只记录告警。
	if writeErr := writeCache(root, raw, meta); writeErr != nil {
		logger().Warn("版本清单缓存写入失败", logging.Fields{
			Attributes: map[string]string{"reason": cacheWriteReason(writeErr)},
		})
	}

	snapshot.fetchedAtMs = meta.FetchedAtMs
	snapshot.etag = meta.ETag
	snapshot.sourceHost = meta.SourceHost
	snapshot.stale = false
	snapshot.warningCode = ""
	return snapshot, nil
}

// useNotModifiedSnapshot 处理 304 响应：沿用旧清单内容，只刷新获取时间。
//
// 只有在已有缓存时才会发送 If-None-Match，因此这里旧内容必然存在；
// 若磁盘缓存已被外部删除，则退化为一次错误（提示稍后重试）。
func useNotModifiedSnapshot(root string, etag string) (*manifestSnapshot, *AppError) {
	previous := currentSnapshot
	if previous == nil {
		cached, loadErr := loadCache(root)
		if loadErr != nil || cached == nil {
			return nil, newExternalError(ErrorCodeManifestFetch, messageManifestNetworkFailed, true)
		}
		previous = cached
	}

	fetchedAt := nowFunc().UnixMilli()
	refreshed := *previous
	refreshed.fetchedAtMs = fetchedAt
	refreshed.stale = false
	refreshed.warningCode = ""
	refreshed.etag = etag

	// 304 没有响应体，用缓存文件内容重算摘要；读不到就保留旧元数据。
	if raw, readErr := os.ReadFile(cachePath(root)); readErr == nil {
		meta := manifestMeta{
			FetchedAtMs: fetchedAt,
			ETag:        etag,
			SHA256:      hashSHA256(raw),
			Size:        int64(len(raw)),
			SourceHost:  refreshed.sourceHost,
		}
		if writeErr := writeMetaCache(root, meta); writeErr != nil {
			logger().Warn("版本清单元数据写入失败", logging.Fields{
				Attributes: map[string]string{"reason": cacheWriteReason(writeErr)},
			})
		}
	}

	return &refreshed, nil
}

// parseManifest 解析并校验清单原始字节。
//
// 校验范围：JSON 合法性、条目数与单条大小上限、必填字段、类型取值、
// 条目 URL 的 https + 白名单、SHA-1 形态、发布时间可解析、
// id 唯一性以及 latest 是否指向真实存在的版本。
func parseManifest(raw []byte) (*manifestSnapshot, *AppError) {
	var document manifestDocument
	if err := json.Unmarshal(raw, &document); err != nil {
		return nil, newExternalError(ErrorCodeManifestInvalid, messageManifestMalformed, false)
	}

	if len(document.Versions) == 0 {
		return nil, newExternalError(ErrorCodeManifestInvalid, messageManifestMalformed, false)
	}
	if len(document.Versions) > manifestMaxVersions {
		return nil, newExternalError(ErrorCodeManifestInvalid, messageManifestTooManyVersions, false)
	}

	latestRelease := strings.TrimSpace(document.Latest.Release)
	latestSnapshot := strings.TrimSpace(document.Latest.Snapshot)
	if latestRelease == "" || latestSnapshot == "" {
		return nil, newExternalError(ErrorCodeManifestInvalid, messageManifestMalformed, false)
	}

	seen := make(map[string]bool, len(document.Versions))
	versions := make([]manifestVersion, 0, len(document.Versions))

	for _, entry := range document.Versions {
		id := strings.TrimSpace(entry.ID)
		item, appErr := normalizeEntry(id, entry)
		if appErr != nil {
			return nil, appErr
		}
		if seen[id] {
			return nil, newExternalError(
				ErrorCodeManifestInvalid,
				fmt.Sprintf(messageManifestEntryInvalid, truncateForMessage(id)),
				false,
			)
		}
		seen[id] = true
		versions = append(versions, item)
	}

	if !seen[latestRelease] || !seen[latestSnapshot] {
		return nil, newExternalError(ErrorCodeManifestInvalid, messageManifestLatestDangling, false)
	}

	// 按发布时间降序排列，最新版本在前；同一时间按 id 升序保证结果稳定。
	sort.SliceStable(versions, func(i, j int) bool {
		if versions[i].releaseTimeUnixMs == versions[j].releaseTimeUnixMs {
			return versions[i].id < versions[j].id
		}
		return versions[i].releaseTimeUnixMs > versions[j].releaseTimeUnixMs
	})

	return &manifestSnapshot{
		versions:       versions,
		latestRelease:  latestRelease,
		latestSnapshot: latestSnapshot,
		verified:       false,
		sourceHost:     manifestSourceHost,
	}, nil
}

// normalizeEntry 校验并规范化单条版本记录。
func normalizeEntry(id string, entry manifestEntry) (manifestVersion, *AppError) {
	invalid := func() (manifestVersion, *AppError) {
		return manifestVersion{}, newExternalError(
			ErrorCodeManifestInvalid,
			fmt.Sprintf(messageManifestEntryInvalid, truncateForMessage(id)),
			false,
		)
	}

	if id == "" || len(id) > manifestMaxVersionIDLen {
		return invalid()
	}
	if !isKnownVersionType(entry.Type) {
		return invalid()
	}
	if appErr := validateManifestURL(entry.URL); appErr != nil {
		return manifestVersion{}, appErr
	}
	// 官方 version_manifest_v2 的版本条目只有 id / type / url / time / releaseTime / sha1 / complianceLevel，
	// **从不包含 size**（size 只出现在版本 JSON 内部的单个下载条目里）。
	// 因此这里只在「提供了 size 且越界」时判非法：0 表示未提供，完整性由 sha1 校验保证。
	if entry.Size < 0 || entry.Size > manifestMaxSingleVersionSize {
		return invalid()
	}
	if !isSHA1Hex(entry.SHA1) {
		return invalid()
	}

	releaseTime := strings.TrimSpace(entry.ReleaseTime)
	parsed, err := time.Parse(time.RFC3339, releaseTime)
	if err != nil {
		return invalid()
	}

	return manifestVersion{
		id:                id,
		versionType:       entry.Type,
		url:               entry.URL,
		releaseTime:       releaseTime,
		releaseTimeUnixMs: parsed.UnixMilli(),
		sha1:              strings.ToLower(entry.SHA1),
		size:              entry.Size,
	}, nil
}

// isKnownVersionType 报告版本类型是否为冻结的四种取值之一。
func isKnownVersionType(value string) bool {
	switch value {
	case versionTypeRelease, versionTypeSnapshot, versionTypeOldBeta, versionTypeOldAlpha:
		return true
	default:
		return false
	}
}

// isSHA1Hex 报告字符串是否形如 40 位十六进制 SHA-1。
func isSHA1Hex(value string) bool {
	if len(value) != sha1HexLength {
		return false
	}
	if _, err := hex.DecodeString(value); err != nil {
		return false
	}
	return true
}

// truncateForMessage 把版本 id 截断到安全的展示长度。
func truncateForMessage(id string) string {
	const maxLength = 64
	if len(id) <= maxLength {
		return id
	}
	return id[:maxLength] + "…"
}

// validateManifestURL 校验清单相关地址：必须 https 且 host 在白名单内。
func validateManifestURL(raw string) *AppError {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed == nil {
		return newInputError(ErrorCodeURLInvalid, messageURLNotHTTPS)
	}
	if !strings.EqualFold(parsed.Scheme, "https") {
		return newInputError(ErrorCodeURLInvalid, messageURLNotHTTPS)
	}
	if !manifestAllowedHosts[strings.ToLower(parsed.Hostname())] {
		return newInputError(ErrorCodeURLInvalid, messageURLHostNotAllowed)
	}
	return nil
}

// redirectRejectedError 表示重定向校验失败，承载具体错误码。
type redirectRejectedError struct {
	// appErr 为要返回给调用方的结构化错误。
	appErr *AppError
}

// Error 实现 error 接口。
func (e *redirectRejectedError) Error() string {
	if e == nil || e.appErr == nil {
		return ""
	}
	return e.appErr.Error()
}

// checkManifestRedirect 对每一跳重定向做 https + 白名单校验，并限制总跳数。
func checkManifestRedirect(req *http.Request, via []*http.Request) error {
	if len(via) > manifestMaxRedirects {
		return &redirectRejectedError{
			appErr: newInputError(ErrorCodeURLInvalid, messageURLTooManyRedirects),
		}
	}
	if appErr := validateManifestURL(req.URL.String()); appErr != nil {
		return &redirectRejectedError{appErr: appErr}
	}
	return nil
}

// cacheDir 返回缓存目录的绝对路径。
func cacheDir(root string) string {
	return filepath.Join(root, filepath.FromSlash(artifactMetadataDirName))
}

// cachePath 返回清单缓存文件路径。
func cachePath(root string) string {
	return filepath.Join(cacheDir(root), manifestCacheFileName)
}

// metaPath 返回清单元数据缓存文件路径。
func metaPath(root string) string {
	return filepath.Join(cacheDir(root), manifestMetaFileName)
}

// writeCache 原子写入清单文件与元数据。
//
// 顺序：先清单后元数据。若清单替换失败，元数据保持不变，旧缓存整体可读；
// 若元数据写入失败，清单已是新内容，但获取时间仍是旧的，下次会自动再刷新。
func writeCache(root string, raw []byte, meta manifestMeta) error {
	if err := writeFileAtomic(cachePath(root), raw); err != nil {
		return err
	}
	return writeMetaCache(root, meta)
}

// writeMetaCache 原子写入元数据文件。
func writeMetaCache(root string, meta manifestMeta) error {
	encoded, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(metaPath(root), encoded)
}

// writeFileAtomic 以「临时文件 + rename」方式原子写入文件。
//
// 失败时删除临时文件，目标文件保持原内容。
func writeFileAtomic(target string, data []byte) error {
	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	tempFile, err := os.CreateTemp(dir, filepath.Base(target)+".tmp-*")
	if err != nil {
		return err
	}
	tempPath := tempFile.Name()

	if _, err := tempFile.Write(data); err != nil {
		_ = tempFile.Close()
		_ = os.Remove(tempPath)
		return err
	}
	if err := tempFile.Sync(); err != nil {
		_ = tempFile.Close()
		_ = os.Remove(tempPath)
		return err
	}
	if err := tempFile.Close(); err != nil {
		_ = os.Remove(tempPath)
		return err
	}
	if err := renameFile(tempPath, target); err != nil {
		_ = os.Remove(tempPath)
		return err
	}
	return nil
}

// loadCache 读取磁盘缓存并构建清单状态。
//
// 无缓存、内容损坏或元数据缺失时返回 nil（调用方按「无缓存」处理）；
// 元数据缺失但清单存在时，用文件修改时间兜底并标记过期，避免误判为新鲜。
func loadCache(root string) (*manifestSnapshot, *AppError) {
	raw, err := os.ReadFile(cachePath(root))
	if err != nil {
		return nil, nil
	}

	snapshot, appErr := parseManifest(raw)
	if appErr != nil {
		// 缓存损坏：按无缓存处理，让上层重新获取；错误码留给状态查询展示。
		lastManifestErrorCode = appErr.Code
		logger().Warn("版本清单缓存无法解析", logging.Fields{ErrorCode: appErr.Code})
		return nil, nil
	}

	meta, metaErr := readCacheMeta(root)
	if metaErr != nil {
		info, statErr := os.Stat(cachePath(root))
		if statErr != nil {
			return nil, nil
		}
		snapshot.fetchedAtMs = info.ModTime().UnixMilli()
		snapshot.stale = true
		snapshot.warningCode = WarningManifestStale
		return snapshot, nil
	}

	snapshot.fetchedAtMs = meta.FetchedAtMs
	snapshot.etag = meta.ETag
	if meta.SourceHost != "" {
		snapshot.sourceHost = meta.SourceHost
	}
	snapshot.stale = expiredAt(snapshot, nowFunc())
	if snapshot.stale {
		snapshot.warningCode = WarningManifestStale
	}
	return snapshot, nil
}

// readCacheMeta 读取并解析元数据缓存文件。
func readCacheMeta(root string) (manifestMeta, error) {
	raw, err := os.ReadFile(metaPath(root))
	if err != nil {
		return manifestMeta{}, err
	}
	var meta manifestMeta
	if err := json.Unmarshal(raw, &meta); err != nil {
		return manifestMeta{}, err
	}
	return meta, nil
}

// hashSHA256 返回字节内容的 SHA-256 十六进制摘要。
func hashSHA256(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// cacheWriteReason 把缓存写入错误归类为可安全记录的简短原因。
//
// 只记录错误类别，不记录路径：日志与诊断字段都不得包含用户目录。
func cacheWriteReason(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, os.ErrPermission):
		return "permission"
	case errors.Is(err, os.ErrNotExist):
		return "not_found"
	default:
		return "io"
	}
}
