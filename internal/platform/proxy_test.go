package platform

import (
	"testing"
)

// ===== 系统代理探测 =====

// TestNormalizeProxyServerSingle 验证单端点写法的解析。
//
// Windows 的 ProxyServer 最常见的就是 `host:port`，此时所有协议共用同一端点。
func TestNormalizeProxyServerSingle(t *testing.T) {
	cases := map[string]string{
		"127.0.0.1:7898":   "http://127.0.0.1:7898",
		" 127.0.0.1:7898 ": "http://127.0.0.1:7898",
		"proxy.local:8080": "http://proxy.local:8080",
	}
	for input, want := range cases {
		if got := normalizeWindowsProxy(input); got != want {
			t.Fatalf("%q 解析期望 %q，实际 %q", input, want, got)
		}
	}
}

// TestNormalizeProxyServerPerScheme 验证按协议分别指定的写法。
//
// 优先取 https：Go 的 Transport 只用同一个代理地址，取任一协议即可，
// 但取 https 更贴近「用户最可能需要代理的流量」。
func TestNormalizeProxyServerPerScheme(t *testing.T) {
	cases := map[string]string{
		"http=127.0.0.1:7898;https=127.0.0.1:7898": "https://127.0.0.1:7898",
		"http=127.0.0.1:1111;https=127.0.0.1:2222": "https://127.0.0.1:2222",
		"http=127.0.0.1:1111":                      "http://127.0.0.1:1111",
		"socks=127.0.0.1:1080":                     "socks://127.0.0.1:1080",
	}
	for input, want := range cases {
		if got := normalizeWindowsProxy(input); got != want {
			t.Fatalf("%q 解析期望 %q，实际 %q", input, want, got)
		}
	}
}

// TestNormalizeProxyServerRejectsEmpty 验证空值返回空串。
func TestNormalizeProxyServerRejectsEmpty(t *testing.T) {
	for _, input := range []string{"", "   ", ";;", "http="} {
		if got := normalizeWindowsProxy(input); got != "" {
			t.Fatalf("%q 期望解析为空，实际 %q", input, got)
		}
	}
}

// TestSystemProxyUsable 验证当前平台实现不 panic 且返回值自洽。
//
// 不假设开发者机器一定配了代理：只断言「返回 true 时地址非空」。
func TestSystemProxyUsable(t *testing.T) {
	address, ok := Current.Proxy.SystemProxy()
	if ok && address == "" {
		t.Fatal("返回可用时必须给出地址")
	}
	if !ok && address != "" {
		t.Fatal("返回不可用时地址应为空")
	}
}

// TestParseScutilProxy 验证 macOS 代理输出解析。
func TestParseScutilProxy(t *testing.T) {
	output := `<dictionary> {
  ExceptionsList : <array> {
    0 : *.local
  }
  HTTPSEnable : 1
  HTTPSPort : 7898
  HTTPSProxy : 127.0.0.1
  HTTPEnable : 1
  HTTPPort : 7898
  HTTPProxy : 127.0.0.1
}`
	address, ok := parseScutilProxy(output)
	if !ok || address != "https://127.0.0.1:7898" {
		t.Fatalf("解析结果 = %q %v", address, ok)
	}
}

// TestParseScutilProxyDisabled 验证未启用时返回空。
func TestParseScutilProxyDisabled(t *testing.T) {
	output := `<dictionary> {
  HTTPEnable : 0
  HTTPProxy : 127.0.0.1
  HTTPPort : 7898
}`
	if address, ok := parseScutilProxy(output); ok || address != "" {
		t.Fatalf("未启用时代理解析结果 = %q %v", address, ok)
	}
}
