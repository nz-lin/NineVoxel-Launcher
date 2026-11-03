package runtime

import (
	"context"
	"encoding/json"
	"net/http"
	goruntime "runtime"
	"sort"
	"sync"
	"time"
)

// adoptiumAvailableReleasesURL 是 Adoptium 提供的版本总览接口。
//
// 该接口返回所有可用大版本与其中属于 LTS 的版本，是 Temurin 的 LTS 列表来源。
const adoptiumAvailableReleasesURL = adoptiumAPIBaseURL + "/v3/info/available_releases"

// ltsCacheTTL 是 LTS 列表的内存缓存时长。
//
// 版本发布是低频事件，1 小时足够；过期后下一次打开弹窗会重新拉取，
// 新出现的 LTS 最多延迟 1 小时展示。
const ltsCacheTTL = time.Hour

// availableReleasesResponse 是 Adoptium 版本总览接口的响应子集。
type availableReleasesResponse struct {
	AvailableLtsReleases []int `json:"available_lts_releases"`
	MostRecentLts        int   `json:"most_recent_lts"`
}

// ltsCacheEntry 是单个来源的 LTS 列表缓存。
type ltsCacheEntry struct {
	data     RuntimeLtsVersions
	cachedAt time.Time
}

var (
	ltsCacheMu sync.Mutex
	ltsCache   = make(map[string]ltsCacheEntry)
)

// GetAvailableLtsVersions 返回指定发行版当前提供的 LTS 大版本列表。
//
// 不同发行版的 LTS 列表由各自的来源接口决定；成功结果在内存中按来源缓存
// ltsCacheTTL，避免每次打开弹窗都请求网络。
func GetAvailableLtsVersions(vendor string) (RuntimeLtsVersions, *AppError) {
	spec, specErr := resolveRuntimeSource(vendor)
	if specErr != nil {
		return RuntimeLtsVersions{}, specErr
	}
	if cached, ok := readLtsCache(spec.Name); ok {
		return cached, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), adoptiumOverallTimeout)
	defer cancel()
	result, fetchErr := spec.LTSVersions(ctx, spec.ClientFactory(), goruntime.GOOS, goruntime.GOARCH)
	if fetchErr != nil {
		return RuntimeLtsVersions{}, fetchErr
	}
	writeLtsCache(spec.Name, result)
	return result, nil
}

// fetchAdoptiumLtsVersions 从 Adoptium 版本总览接口读取 LTS 列表。
func fetchAdoptiumLtsVersions(ctx context.Context, client *http.Client, _, _ string) (RuntimeLtsVersions, *AppError) {
	if client == nil {
		client = adoptiumHTTPClientFactory()
	}
	body, fetchErr := fetchAdoptiumDocument(ctx, client, adoptiumAvailableReleasesURL)
	if fetchErr != nil {
		return RuntimeLtsVersions{}, fetchErr
	}
	var response availableReleasesResponse
	if unmarshalErr := json.Unmarshal(body, &response); unmarshalErr != nil {
		return RuntimeLtsVersions{}, newExternalError(ErrorCodeRuntimeSource, messageRuntimeSourceMalformed, false)
	}
	versions, normalizeErr := normalizeLtsVersions(response.AvailableLtsReleases)
	if normalizeErr != nil {
		return RuntimeLtsVersions{}, normalizeErr
	}
	mostRecent := response.MostRecentLts
	if mostRecent <= 0 {
		mostRecent = versions[0]
	}
	return RuntimeLtsVersions{Versions: versions, MostRecentLts: mostRecent}, nil
}

// normalizeLtsVersions 去重、过滤非法值并按从新到旧排序。
func normalizeLtsVersions(raw []int) ([]int, *AppError) {
	seen := make(map[int]struct{}, len(raw))
	versions := make([]int, 0, len(raw))
	for _, version := range raw {
		if version <= 0 {
			continue
		}
		if _, exists := seen[version]; exists {
			continue
		}
		seen[version] = struct{}{}
		versions = append(versions, version)
	}
	if len(versions) == 0 {
		return nil, newExternalError(ErrorCodeRuntimeSource, messageRuntimeSourceEmpty, false)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(versions)))
	return versions, nil
}

// readLtsCache 返回指定来源仍在有效期内的缓存。
func readLtsCache(source string) (RuntimeLtsVersions, bool) {
	ltsCacheMu.Lock()
	defer ltsCacheMu.Unlock()
	entry, ok := ltsCache[source]
	if !ok || time.Since(entry.cachedAt) > ltsCacheTTL {
		return RuntimeLtsVersions{}, false
	}
	versions := append([]int(nil), entry.data.Versions...)
	return RuntimeLtsVersions{Versions: versions, MostRecentLts: entry.data.MostRecentLts}, true
}

// writeLtsCache 写入指定来源的缓存；切片先复制，避免调用方后续修改污染缓存。
func writeLtsCache(source string, value RuntimeLtsVersions) {
	ltsCacheMu.Lock()
	defer ltsCacheMu.Unlock()
	ltsCache[source] = ltsCacheEntry{
		data: RuntimeLtsVersions{
			Versions:      append([]int(nil), value.Versions...),
			MostRecentLts: value.MostRecentLts,
		},
		cachedAt: time.Now(),
	}
}

// resetLtsCacheForTest 清空缓存；只供测试使用。
func resetLtsCacheForTest() {
	ltsCacheMu.Lock()
	defer ltsCacheMu.Unlock()
	ltsCache = make(map[string]ltsCacheEntry)
}
