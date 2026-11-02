// 本文件提供「用系统默认浏览器打开登录页」的跨平台实现。
//
// 为什么放在后端而不是让前端调用 Browser.OpenURL：
//   - PKCE 流程由后端统一编排（回环监听 + state/PKCE 校验 + 换 token），
//     打开浏览器是流程的第一步，放后端可以保证「先监听、后打开」的顺序；
//   - 浏览器打开失败时后端仍会返回带 authorize URL 的登录流程，
//     界面可以用「重新打开登录页」按钮兜底，因此这里失败不阻断登录。
//
// 安全：只允许 http/https；命令参数以数组传递、不经过 shell，避免命令注入。
package account

import (
	"errors"
	"net/url"
	"os/exec"
	"runtime"
)

// openURLWithOS 用系统默认浏览器打开 URL。
//
// 参数 rawURL 必须是 http/https 链接（登录页），否则直接拒绝。
// 返回错误时调用方应提示用户手动打开，而不是中断登录流程。
func openURLWithOS(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme == "" {
		return errors.New("account: 无法解析待打开的链接")
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return errors.New("account: 只允许打开 HTTP(S) 链接")
	}

	var command *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		command = exec.Command("open", rawURL)
	case "windows":
		// rundll32 是 Windows 上无需 cmd.exe 即可调用默认处理程序的方式。
		command = exec.Command("rundll32", "url.dll,FileProtocolHandler", rawURL)
	default:
		command = exec.Command("xdg-open", rawURL)
	}
	if err := command.Start(); err != nil {
		return errors.New("account: 无法启动系统浏览器")
	}
	// 只负责拉起浏览器，不等待其退出；子进程由系统接管。
	go func() { _ = command.Wait() }()
	return nil
}
