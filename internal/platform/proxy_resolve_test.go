package platform

import (
	"net/http"
	"net/url"
	"os"
	"testing"
)

// TestProxyFuncPrefersEnvironment 验证环境变量优先于系统设置。
func TestProxyFuncPrefersEnvironment(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1111")

	proxyFunc := ProxyFunc()
	if proxyFunc == nil {
		t.Fatal("设置了 HTTPS_PROXY 时应返回代理函数")
	}
	request := &http.Request{URL: mustParseURL(t, "https://libraries.minecraft.net/x.jar")}
	target, err := proxyFunc(request)
	if err != nil {
		t.Fatalf("解析代理失败: %v", err)
	}
	if target == nil || target.String() != "http://127.0.0.1:1111" {
		t.Fatalf("代理期望 http://127.0.0.1:1111，实际 %v", target)
	}
}

// TestProxyFuncAcceptsBareHostPort 验证只写 host:port 时补全协议。
func TestProxyFuncAcceptsBareHostPort(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "127.0.0.1:7898")

	proxyFunc := ProxyFunc()
	if proxyFunc == nil {
		t.Fatal("应返回代理函数")
	}
	target, err := proxyFunc(&http.Request{URL: mustParseURL(t, "https://example.com")})
	if err != nil {
		t.Fatalf("解析代理失败: %v", err)
	}
	if target == nil || target.String() != "http://127.0.0.1:7898" {
		t.Fatalf("代理期望 http://127.0.0.1:7898，实际 %v", target)
	}
}

// TestProxyFuncFallsBackToSystem 验证无环境变量时读取系统代理。
//
// 该用例只在系统确实启用了代理时断言具体地址；否则仅断言「不 panic 且
// 返回值自洽」，以免在未配代理的 CI 机器上误报。
func TestProxyFuncFallsBackToSystem(t *testing.T) {
	os.Unsetenv("HTTPS_PROXY")
	os.Unsetenv("https_proxy")
	os.Unsetenv("HTTP_PROXY")
	os.Unsetenv("http_proxy")

	address, enabled := Current.Proxy.SystemProxy()
	proxyFunc := ProxyFunc()
	if !enabled {
		if proxyFunc != nil {
			t.Fatal("系统未启用代理时不应返回代理函数")
		}
		return
	}
	if proxyFunc == nil {
		t.Fatal("系统启用了代理时应返回代理函数")
	}
	target, err := proxyFunc(&http.Request{URL: mustParseURL(t, "https://example.com")})
	if err != nil || target == nil {
		t.Fatalf("系统代理解析失败: %v %v", target, err)
	}
	if target.String() != address {
		t.Fatalf("代理期望 %s，实际 %s", address, target)
	}
}

// mustParseURL 解析测试用 URL。
func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}
