package platform

import (
	"net/http"
	"net/url"
	"os"
	"strings"
)

// ProxyFunc 返回一个供 `http.Transport.Proxy` 使用的代理选择函数。
//
// 返回 nil 表示不使用代理（直连）。
func ProxyFunc() func(*http.Request) (*url.URL, error) {
	address, _ := ResolveProxy()
	if address == "" {
		return nil
	}
	parsed, err := url.Parse(address)
	if err != nil {
		return nil
	}
	return http.ProxyURL(parsed)
}

// ResolveProxy 返回代理地址与其来源。
//
// 解析顺序（先到先用）：
//  1. 环境变量 `HTTPS_PROXY` / `HTTP_PROXY`（含小写形式）——便于用户临时覆盖；
//  2. 操作系统代理设置（Windows 的 Internet 选项、macOS 的网络偏好设置）。
//
// 第二个返回值为 true 表示来源是环境变量。调用方据此决定如何转告外部程序：
// 环境变量的值需要显式转换，而系统设置可以直接让 JVM 自行读取。
//
// 为什么要显式读取系统设置：Go 的 `http.ProxyFromEnvironment` **只读环境
// 变量**。在 Windows 上通过「Internet 选项」配置的代理不会出现在环境变量里，
// 只依赖它会让这些用户的所有下载静默走直连。
func ResolveProxy() (string, bool) {
	if address := proxyFromEnvironment(); address != "" {
		return address, true
	}
	if address, ok := Current.Proxy.SystemProxy(); ok && address != "" {
		return address, false
	}
	return "", false
}

// proxyFromEnvironment 返回环境变量里配置的代理地址。
//
// 大小写两种写法都接受：Windows 上大小写不敏感，但 Go 读取时区分，
// 用户两种写法都可能出现。https 优先于 http。
func proxyFromEnvironment() string {
	for _, key := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy"} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			if !strings.Contains(value, "://") {
				value = "http://" + value
			}
			return value
		}
	}
	return ""
}

// JavaProxyOptions 返回让子 JVM 走同一代理所需的命令行参数。
//
// 为什么需要它：官方安装器在自己的 JVM 里下载依赖，完全不看启动器的
// HTTP 代理配置。若只给启动器设代理，加载器安装仍会走直连并在网络受限时
// 以 `Connection reset` 失败。
//
// 两种来源的处理方式不同：
//   - 系统设置：JVM 自己就能读（`-Djava.net.useSystemProxies=true`），
//     不需要我们解析地址；
//   - 环境变量：JVM **不读** `HTTPS_PROXY`，必须显式转成 `-Dhttps.proxyHost`
//     与 `-Dhttps.proxyPort`。
//
// 无论是否探测到代理，都返回 `-Djava.net.useSystemProxies=true`：它在没有
// 代理时是无副作用的空操作，能让「用户在安装后才配代理」的场景也生效。
func JavaProxyOptions() []string {
	options := []string{"-Djava.net.useSystemProxies=true"}
	address, fromEnvironment := ResolveProxy()
	if !fromEnvironment || address == "" {
		return options
	}
	parsed, err := url.Parse(address)
	if err != nil || parsed.Host == "" {
		return options
	}
	host := parsed.Hostname()
	port := parsed.Port()
	if host == "" {
		return options
	}

	switch strings.ToLower(parsed.Scheme) {
	case "socks", "socks5":
		options = append(options, "-DsocksProxyHost="+host)
		if port != "" {
			options = append(options, "-DsocksProxyPort="+port)
		}
	default:
		// http 与 https 各自一套属性：JVM 不会用一个推到另一个。
		for _, scheme := range []string{"http", "https"} {
			options = append(options, "-D"+scheme+".proxyHost="+host)
			if port != "" {
				options = append(options, "-D"+scheme+".proxyPort="+port)
			}
		}
	}
	return options
}
