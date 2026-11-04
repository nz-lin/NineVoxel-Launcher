//go:build windows

package platform

import (
	"golang.org/x/sys/windows/registry"
)

// systemProxy 读取 Windows 的 Internet 选项代理配置。
//
// 为什么不能只依赖 `http.ProxyFromEnvironment`：它只读 `HTTP_PROXY` /
// `HTTPS_PROXY` 环境变量，而 Windows 用户习惯在「Internet 选项」里配置系统
// 代理（写在注册表里）。只读环境变量会让这类用户的所有下载静默走直连。
//
// PAC（AutoConfigURL）在这里不展开：解析 PAC 需要执行 JavaScript，超出本
// 启动器的职责；检测到 PAC 时视为无可用代理，由调用方回退环境变量。
func systemProxy() (string, bool) {
	key, err := registry.OpenKey(registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Internet Settings`, registry.QUERY_VALUE)
	if err != nil {
		return "", false
	}
	defer key.Close()

	enabled, _, enableErr := key.GetIntegerValue("ProxyEnable")
	if enableErr != nil || enabled == 0 {
		return "", false
	}

	if script, _, scriptErr := key.GetStringValue("AutoConfigURL"); scriptErr == nil && script != "" {
		return "", false
	}

	server, _, serverErr := key.GetStringValue("ProxyServer")
	if serverErr != nil || server == "" {
		return "", false
	}
	normalized := normalizeWindowsProxy(server)
	return normalized, normalized != ""
}
