package platform

import (
	"strings"
	"testing"
)

// TestJavaProxyOptionsAlwaysEnablesSystemProxies 验证始终带上系统代理开关。
//
// 该参数在无代理时是无副作用的空操作，但能让「用户在安装后才配代理」
// 的场景也生效，因此无条件携带。
func TestJavaProxyOptionsAlwaysEnablesSystemProxies(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "")
	t.Setenv("HTTP_PROXY", "")

	options := JavaProxyOptions()
	if !containsOption(options, "-Djava.net.useSystemProxies=true") {
		t.Fatalf("缺少系统代理开关：%v", options)
	}
}

// TestJavaProxyOptionsConvertsEnvironmentProxy 验证环境变量转成显式属性。
//
// JVM 不读 `HTTPS_PROXY`，必须翻译成 `-Dhttps.proxyHost` 与 `-Dhttps.proxyPort`，
// 否则用户的显式配置在安装器里完全失效。
func TestJavaProxyOptionsConvertsEnvironmentProxy(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:7898")

	options := JavaProxyOptions()
	if !containsOption(options, "-Dhttps.proxyHost=127.0.0.1") {
		t.Fatalf("缺少 https 代理主机：%v", options)
	}
	if !containsOption(options, "-Dhttps.proxyPort=7898") {
		t.Fatalf("缺少 https 代理端口：%v", options)
	}
	// http 与 https 各有一套属性，JVM 不会互推。
	if !containsOption(options, "-Dhttp.proxyHost=127.0.0.1") {
		t.Fatalf("缺少 http 代理主机：%v", options)
	}
}

// TestJavaProxyOptionsHandlesSocks 验证 socks 代理使用正确的属性名。
func TestJavaProxyOptionsHandlesSocks(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "socks5://127.0.0.1:1080")

	options := JavaProxyOptions()
	if !containsOption(options, "-DsocksProxyHost=127.0.0.1") {
		t.Fatalf("缺少 socks 代理主机：%v", options)
	}
	if !containsOption(options, "-DsocksProxyPort=1080") {
		t.Fatalf("缺少 socks 代理端口：%v", options)
	}
	for _, option := range options {
		if strings.HasPrefix(option, "-Dhttps.proxyHost=") {
			t.Fatalf("socks 代理不应生成 http 代理属性：%v", options)
		}
	}
}

// TestJavaProxyOptionsOmitsPortWhenAbsent 验证缺少端口时不生成端口属性。
func TestJavaProxyOptionsOmitsPortWhenAbsent(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://proxy.local")

	options := JavaProxyOptions()
	for _, option := range options {
		if strings.HasPrefix(option, "-Dhttps.proxyPort=") {
			t.Fatalf("无端口时不应生成端口属性：%v", options)
		}
	}
}

// containsOption 判断选项列表是否包含指定项。
func containsOption(options []string, want string) bool {
	for _, option := range options {
		if option == want {
			return true
		}
	}
	return false
}
