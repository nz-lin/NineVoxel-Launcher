package platform

import "strings"

// 本文件存放代理配置解析的**纯函数**，供各平台的 systemProxy 复用。
//
// 拆出来的理由：解析逻辑与平台无关，放在带构建约束的文件里会导致测试无法
// 在其他平台验证；把纯函数集中在这里，平台文件只负责「怎么读到原始值」。

// normalizeWindowsProxy 把注册表里的 ProxyServer 值转成可用的代理 URL。
//
// 该字段有两种写法：
//   - `127.0.0.1:7898`（所有协议共用）
//   - `http=127.0.0.1:7898;https=127.0.0.1:7898`（按协议分别指定）
func normalizeWindowsProxy(value string) string {
	segments := strings.Split(value, ";")
	byScheme := make(map[string]string, len(segments))
	for _, segment := range segments {
		trimmed := strings.TrimSpace(segment)
		if trimmed == "" {
			continue
		}
		scheme, endpoint, found := strings.Cut(trimmed, "=")
		if !found {
			// 无等号：所有协议共用同一个端点。
			byScheme["http"] = trimmed
			continue
		}
		scheme = strings.ToLower(strings.TrimSpace(scheme))
		endpoint = strings.TrimSpace(endpoint)
		if scheme != "" && endpoint != "" {
			byScheme[scheme] = endpoint
		}
	}
	// 优先 https，其次 http：Go 的 Transport 只用同一个代理地址，取任一即可，
	// 但取 https 更贴近「用户最可能需要代理的流量」。
	for _, scheme := range []string{"https", "http", "socks", "ftp"} {
		if endpoint, ok := byScheme[scheme]; ok {
			return scheme + "://" + endpoint
		}
	}
	for scheme, endpoint := range byScheme {
		return scheme + "://" + endpoint
	}
	return ""
}

// parseScutilProxy 解析 macOS `scutil --proxy` 的输出。//
// 输出形如：
//
//	<dictionary> {
//	  HTTPSEnable : 1
//	  HTTPSProxy : 127.0.0.1
//	  HTTPSPort : 7898
//	}
func parseScutilProxy(output string) (string, bool) {
	values := make(map[string]string, 8)
	for _, line := range strings.Split(output, "\n") {
		key, value, found := strings.Cut(strings.TrimSpace(line), " : ")
		if !found {
			continue
		}
		values[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	for _, scheme := range []string{"HTTPS", "HTTP", "SOCKS"} {
		if values[scheme+"Enable"] != "1" {
			continue
		}
		host := values[scheme+"Proxy"]
		port := values[scheme+"Port"]
		if host == "" || port == "" {
			continue
		}
		return strings.ToLower(scheme) + "://" + host + ":" + port, true
	}
	return "", false
}
