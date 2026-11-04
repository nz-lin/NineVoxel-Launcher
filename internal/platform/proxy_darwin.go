//go:build darwin

package platform

import "os/exec"

// systemProxy 读取 macOS 的系统代理配置。
//
// 与 Windows 不同，macOS 的代理既可写在系统偏好设置里，也常被 shell 配置以
// 环境变量形式提供。这里只读系统偏好设置（`scutil --proxy`）；环境变量由
// `http.ProxyFromEnvironment` 处理，两者由调用方合并。
func systemProxy() (string, bool) {
	output, err := exec.Command("scutil", "--proxy").Output()
	if err != nil {
		return "", false
	}
	return parseScutilProxy(string(output))
}
