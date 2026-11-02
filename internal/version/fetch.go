package version

// 本文件实现「按需获取版本元数据」（P3-13）。
//
// 背景：P3-02 的详情与计划构建只读本地 `versions/<id>/<id>.json`，
// 而安装流程真正写这个文件的时机在下载之后；用户从未安装过任何版本时，
// 详情页、兼容性报告与安装计划全部不可用。本文件补上「确保元数据就位」这一步：
//
//  1. 本地文件已存在且非空 → 直接返回，不联网；
//  2. 否则从**已缓存的清单**取该版本的 url 与 sha1（不重新拉清单）；
//  3. 走与清单相同的白名单 / https / 重定向复检约束下载，校验 sha1；
//  4. 原子写盘后再读一遍，保证与「本地已存在」分支结果一致。
//
// 本文件不会修改清单缓存，也不会为列表页逐个拉取版本 JSON（那会打爆上游）。

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"example.com/ninevoxel/launcher/internal/artifact"
	"example.com/ninevoxel/launcher/internal/logging"
)

// 版本元数据下载相关的常量。
const (
	// fetchDialTimeout 是建立连接（含 TLS 握手）的超时。
	fetchDialTimeout = 10 * time.Second

	// fetchOverallTimeout 是单次元数据请求的总超时（含重定向与响应体读取）。
	fetchOverallTimeout = 30 * time.Second

	// fetchMaxRedirects 是允许的最大重定向跳数（与清单一致）。
	fetchMaxRedirects = manifestMaxRedirects
)

// newFetchHTTPClient 构造元数据下载客户端，测试可替换以注入假 Transport。
//
// 与清单客户端共用同一套约束：https + 白名单 host、重定向每跳复检、跳数上限。
func newFetchHTTPClient() *http.Client {
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   fetchDialTimeout,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout:   fetchDialTimeout,
		ResponseHeaderTimeout: fetchDialTimeout,
		MaxIdleConnsPerHost:   2,
	}
	return &http.Client{
		Timeout:       fetchOverallTimeout,
		Transport:     transport,
		CheckRedirect: checkManifestRedirect,
	}
}

// 包级可注入点（仅包内可见）。
var (
	// ensureHTTPClientFactory 构造元数据下载客户端，测试可替换。
	ensureHTTPClientFactory = newFetchHTTPClient

	// ensureCalls 记录正在进行的按版本去重调用，key 为「数据根 + 版本标识」。
	ensureCalls sync.Map

	// ensureLog 返回本文件的日志器（与版本模块其余部分同组件）。
	ensureLogger = func() *logging.Logger {
		return logger()
	}
)

// ensureCall 是一次「确保元数据就位」的执行记录，供并发调用方等待同一结果。
type ensureCall struct {
	// done 在结果就绪时关闭。
	done chan struct{}

	// err 为本次执行的结果；并发等待者在 done 关闭后读取。
	err *AppError
}

// ensureVersionMetadata 确保 `<root>/versions/<id>/<id>.json` 可读。
//
// 本地文件存在且非空时立即返回（不联网）；否则按清单条目下载、校验并原子落盘。
// 同一数据根下同一版本的并发调用会合并为一次下载，其余调用等待同一结果。
// 返回的 *AppError 中 VERSION_MANIFEST_001 表示「清单缓存不可用，请先刷新清单」，
// VERSION_JSON_001 表示「版本 JSON 获取、校验或写入失败」。
func ensureVersionMetadata(root, versionID string) *AppError {
	if strings.TrimSpace(root) == "" {
		return newInputError(ErrorCodeVersionJSON, messageEnsureRootRequired)
	}
	if strings.TrimSpace(versionID) == "" {
		return newInputError(ErrorCodeVersionJSON, messageVersionIDRequired)
	}

	// 本地已就位时直接返回，避免任何网络请求。
	if installedVersionMetadataPresent(root, versionID) {
		return nil
	}

	key := root + "\x00" + versionID
	call := &ensureCall{done: make(chan struct{})}
	actual, loaded := ensureCalls.LoadOrStore(key, call)
	if loaded {
		// 并发调用：等待首个执行者完成，复用其结果。
		pending, _ := actual.(*ensureCall)
		if pending != nil {
			<-pending.done
			return pending.err
		}
		// 理论不可达（类型断言失败）；退化为单次执行，保证行为正确。
		return ensureVersionMetadataOnce(root, versionID)
	}

	call.err = ensureVersionMetadataOnce(root, versionID)
	close(call.done)
	// 结果已写入；后续调用会重新判断本地文件，已就位时不会再联网。
	ensureCalls.Delete(key)
	return call.err
}

// installedVersionMetadataPresent 报告版本 JSON 是否已存在且非空。
//
// 只做 Stat，不读内容：内容合法性由调用方的读取流程负责。
func installedVersionMetadataPresent(root, versionID string) bool {
	relPath, relErr := artifact.VersionRelPath(versionID, versionID+".json")
	if relErr != nil {
		return false
	}
	absolute := filepath.Join(root, filepath.FromSlash(relPath))
	info, err := os.Stat(absolute)
	if err != nil || info == nil || !info.Mode().IsRegular() {
		return false
	}
	return info.Size() > 0
}

// ensureVersionMetadataOnce 执行一次完整的「检查 → 下载 → 校验 → 落盘 → 读回」。
func ensureVersionMetadataOnce(root, versionID string) *AppError {
	relPath, relErr := artifact.VersionRelPath(versionID, versionID+".json")
	if relErr != nil {
		return newInputError(ErrorCodeVersionJSON, messageVersionIDInvalid)
	}
	absolute := filepath.Join(root, filepath.FromSlash(relPath))

	entry, lookupErr := lookupManifestEntry(root, versionID)
	if lookupErr != nil {
		return lookupErr
	}

	startedAt := nowFunc()
	raw, fetchErr := downloadVersionJSON(context.Background(), entry)
	if fetchErr != nil {
		ensureLogger().Warn("版本元数据获取失败", logging.Fields{
			ErrorCode: fetchErr.Code,
			Attributes: map[string]string{
				"durationMs": fmt.Sprintf("%d", nowFunc().Sub(startedAt).Milliseconds()),
			},
		})
		return fetchErr
	}

	if writeErr := writeVersionJSONAtomically(absolute, versionID, raw); writeErr != nil {
		return writeErr
	}

	// 写盘后再读一遍：保证与「本地已存在」分支走完全相同的解析路径，
	// 也能立刻发现磁盘损坏（例如被并发进程截断）。
	if _, _, readErr := readInstallVersionJSON(root, versionID); readErr != nil {
		ensureLogger().Warn("版本元数据写入后无法解析", logging.Fields{
			ErrorCode: readErr.Code,
		})
		return readErr
	}

	ensureLogger().Info("版本元数据已就位", logging.Fields{
		Attributes: map[string]string{
			"sizeBytes":  fmt.Sprintf("%d", len(raw)),
			"durationMs": fmt.Sprintf("%d", nowFunc().Sub(startedAt).Milliseconds()),
		},
	})
	return nil
}

// lookupManifestEntry 从已缓存的清单中查找版本条目。
//
// 只读内存或磁盘缓存，**不会发起清单请求**：清单不可用时返回可重试的
// VERSION_MANIFEST_001，提示用户先刷新清单。
func lookupManifestEntry(root, versionID string) (manifestVersion, *AppError) {
	snapshot, snapshotErr := cachedManifest(root)
	if snapshotErr != nil {
		return manifestVersion{}, snapshotErr
	}
	for _, item := range snapshot.versions {
		if item.id == versionID {
			return item, nil
		}
	}
	return manifestVersion{}, newExternalError(
		ErrorCodeManifestFetch,
		fmt.Sprintf(messageManifestEntryNotFound, truncateForMessage(versionID)),
		true,
	)
}

// cachedManifest 返回当前可用清单（内存优先，其次磁盘缓存），不触发刷新。
func cachedManifest(root string) (*manifestSnapshot, *AppError) {
	stateMu.Lock()
	defer stateMu.Unlock()

	if currentSnapshot != nil {
		return currentSnapshot, nil
	}
	if cached, _ := loadCache(root); cached != nil {
		currentSnapshot = cached
		return cached, nil
	}
	return nil, newExternalError(ErrorCodeManifestFetch, messageManifestCacheUnavailable, true)
}

// downloadVersionJSON 下载并校验单个版本的 JSON 字节。
//
// 校验顺序：URL 合规（https + 白名单）→ 状态码 → 大小上限 → SHA-1 与清单一致。
// 网络错误与 5xx 标记为可重试；404 与哈希不一致标记为不可重试。
func downloadVersionJSON(ctx context.Context, entry manifestVersion) ([]byte, *AppError) {
	if appErr := validateManifestURL(entry.url); appErr != nil {
		return nil, appErr
	}

	reqCtx, cancel := context.WithTimeout(ctx, fetchOverallTimeout)
	defer cancel()

	request, err := http.NewRequestWithContext(reqCtx, http.MethodGet, entry.url, nil)
	if err != nil {
		return nil, newExternalError(ErrorCodeVersionJSON, messageVersionJSONNetworkFailed, true)
	}
	request.Header.Set("Accept", "application/json")

	client := ensureHTTPClientFactory()
	response, err := client.Do(request)
	if err != nil {
		var rejected *redirectRejectedError
		if errors.As(err, &rejected) {
			return nil, rejected.appErr
		}
		return nil, newExternalError(ErrorCodeVersionJSON, messageVersionJSONNetworkFailed, true)
	}
	defer func() {
		_ = response.Body.Close()
	}()

	if response.StatusCode == http.StatusNotFound {
		return nil, newExternalError(
			ErrorCodeVersionJSON,
			fmt.Sprintf(messageVersionJSONNotFound, truncateForMessage(entry.id)),
			false,
		)
	}
	if response.StatusCode != http.StatusOK {
		return nil, newExternalError(
			ErrorCodeVersionJSON,
			fmt.Sprintf(messageVersionJSONStatusFailed, truncateForMessage(entry.id)),
			true,
		)
	}

	raw, readErr := io.ReadAll(io.LimitReader(response.Body, installMaxVersionJSONBytes+1))
	if readErr != nil {
		return nil, newExternalError(ErrorCodeVersionJSON, messageVersionJSONNetworkFailed, true)
	}
	if len(raw) > installMaxVersionJSONBytes {
		return nil, newExternalError(ErrorCodeVersionJSON, messageVersionJSONTooLarge, false)
	}
	if len(raw) == 0 {
		return nil, newExternalError(
			ErrorCodeVersionJSON,
			fmt.Sprintf(messageVersionJSONStatusFailed, truncateForMessage(entry.id)),
			true,
		)
	}

	if !strings.EqualFold(hashSHA1Hex(raw), entry.sha1) {
		return nil, newExternalError(
			ErrorCodeVersionJSON,
			fmt.Sprintf(messageVersionJSONChecksumMismatch, truncateForMessage(entry.id)),
			false,
		)
	}

	return raw, nil
}

// writeVersionJSONAtomically 以「临时文件 + Sync + rename」方式写入版本 JSON。
//
// 复用清单缓存的原子写实现：失败时删除临时文件，目标文件保持原内容。
// 目录由 writeFileAtomic 负责创建（MkdirAll）。
func writeVersionJSONAtomically(absolute, versionID string, raw []byte) *AppError {
	if err := writeFileAtomic(absolute, raw); err != nil {
		ensureLogger().Warn("版本元数据写入失败", logging.Fields{
			Attributes: map[string]string{"reason": cacheWriteReason(err)},
		})
		return newSystemError(
			ErrorCodeVersionJSON,
			fmt.Sprintf(messageVersionJSONWriteFailed, truncateForMessage(versionID)),
			true,
		)
	}
	return nil
}

// hashSHA1Hex 返回字节内容的 SHA-1 十六进制摘要（小写）。
func hashSHA1Hex(raw []byte) string {
	sum := sha1.Sum(raw)
	return hex.EncodeToString(sum[:])
}
