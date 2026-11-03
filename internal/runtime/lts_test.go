package runtime

import (
	"fmt"
	"net/http"
	"sync"
	"testing"
)

// TestGetAvailableLtsVersionsParsesAndSorts 验证 LTS 列表来自接口并降序去重。
func TestGetAvailableLtsVersionsParsesAndSorts(t *testing.T) {
	resetLtsCacheForTest()
	t.Cleanup(resetLtsCacheForTest)

	oldClient := adoptiumHTTPClientFactory
	t.Cleanup(func() { adoptiumHTTPClientFactory = oldClient })
	adoptiumHTTPClientFactory = func() *http.Client {
		return &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.Host != adoptiumAPIHost || req.URL.Path != "/v3/info/available_releases" {
				return adoptiumTestResponse(req, http.StatusNotFound, nil), nil
			}
			body := []byte(`{"available_lts_releases":[17,21,8,21],"most_recent_lts":21}`)
			return adoptiumTestResponse(req, http.StatusOK, body), nil
		})}
	}

	versions, appErr := GetAvailableLtsVersions("adoptium")
	if appErr != nil {
		t.Fatalf("获取 LTS 列表失败：%v", appErr)
	}
	want := []int{21, 17, 8}
	if fmt.Sprint(versions.Versions) != fmt.Sprint(want) {
		t.Fatalf("LTS 列表 = %v，期望 %v", versions.Versions, want)
	}
	if versions.MostRecentLts != 21 {
		t.Fatalf("最新 LTS = %d，期望 21", versions.MostRecentLts)
	}
}

// TestGetAvailableLtsVersionsUsesCache 验证成功结果会被缓存，避免重复请求。
func TestGetAvailableLtsVersionsUsesCache(t *testing.T) {
	resetLtsCacheForTest()
	t.Cleanup(resetLtsCacheForTest)

	oldClient := adoptiumHTTPClientFactory
	t.Cleanup(func() { adoptiumHTTPClientFactory = oldClient })
	var calls int32
	var mu sync.Mutex
	adoptiumHTTPClientFactory = func() *http.Client {
		return &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			mu.Lock()
			calls++
			mu.Unlock()
			return adoptiumTestResponse(req, http.StatusOK, []byte(`{"available_lts_releases":[21],"most_recent_lts":21}`)), nil
		})}
	}

	if _, appErr := GetAvailableLtsVersions("adoptium"); appErr != nil {
		t.Fatalf("首次获取失败：%v", appErr)
	}
	if _, appErr := GetAvailableLtsVersions("adoptium"); appErr != nil {
		t.Fatalf("二次获取失败：%v", appErr)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("请求次数 = %d，期望 1（应命中缓存）", calls)
	}
}

// TestGetAvailableLtsVersionsRejectsEmpty 验证空列表按来源错误处理。
func TestGetAvailableLtsVersionsRejectsEmpty(t *testing.T) {
	resetLtsCacheForTest()
	t.Cleanup(resetLtsCacheForTest)

	oldClient := adoptiumHTTPClientFactory
	t.Cleanup(func() { adoptiumHTTPClientFactory = oldClient })
	adoptiumHTTPClientFactory = func() *http.Client {
		return &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return adoptiumTestResponse(req, http.StatusOK, []byte(`{"available_lts_releases":[]}`)), nil
		})}
	}

	_, appErr := GetAvailableLtsVersions("adoptium")
	requireCode(t, appErr, ErrorCodeRuntimeSource)
}

// roundTripFunc 让普通函数实现 http.RoundTripper。
type roundTripFunc func(*http.Request) (*http.Response, error)

// RoundTrip 实现 http.RoundTripper。
func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
