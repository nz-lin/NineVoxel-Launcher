//go:build !windows && !darwin

package platform

// systemProxy 在 Linux 与其他平台上不读取系统级代理。
//
// 这些平台的「系统代理」本身就是环境变量约定（由桌面环境写入
// `http_proxy` / `https_proxy`），调用方用 `http.ProxyFromEnvironment`
// 即可覆盖，无需另行探测。
func systemProxy() (string, bool) {
	return "", false
}
