// 本文件实现微软账号的**授权码 + PKCE + 本地回环**登录流程（默认方式）。
//
// 为什么默认用它（而不是设备码）：
//   - 用户点一次「微软登录」→ 系统浏览器打开 → 登录/同意 → 浏览器回跳
//     到 127.0.0.1 的临时端口，启动器自动完成后续链路，全程不需要抄验证码；
//   - 桌面应用属于公共客户端，PKCE 让授权码即使被本地进程截获也无法兑换
//     （缺少 code_verifier），比内嵌 WebView 或 client secret 更安全；
//   - 设备码作为回退路径保留（见 microsoft.go），浏览器无法回跳、端口被安全
//     软件拦截、企业策略禁止本地回环时由用户显式切换。
//
// 协议要点（Microsoft identity platform v2.0）：
//
//	GET  {authorize}?client_id&response_type=code&redirect_uri=http://localhost:PORT
//	     &scope=XboxLive.signin offline_access&state&code_challenge&code_challenge_method=S256
//	POST {token} grant_type=authorization_code&code&redirect_uri&client_id&code_verifier
//
// 安全边界：
//   - code_verifier 只存在于本进程内存，不落盘、不进日志、不进错误文案；
//   - state 校验失败的回调一律拒绝（防 CSRF / 串码）；
//   - 回环服务只监听 127.0.0.1，生命周期与登录流程一致，结束/取消/超时即关闭；
//   - 错误文案只引用稳定的 OAuth 错误名与 AADSTS 码，不透传服务端响应原文。
package account

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// 微软登录方式与 PKCE 流程常量。
const (
	// microsoftLoginModePKCE 是默认登录方式：系统浏览器 + 本地回环回调。
	microsoftLoginModePKCE = "pkce"

	// microsoftLoginModeDeviceCode 是回退登录方式：设备码轮询。
	microsoftLoginModeDeviceCode = "device_code"

	// microsoftPKCERedirectHost 是回环回调使用的 host。
	//
	// 固定写成 localhost 而不是 127.0.0.1：Entra 对 native 应用注册的
	// `http://localhost` 会忽略端口，写 localhost 才能命中已注册的重定向 URI。
	microsoftPKCERedirectHost = "localhost"

	// microsoftPKCEExpiry 是等待用户在浏览器完成登录的最长时间。
	microsoftPKCEExpiry = 5 * time.Minute

	// microsoftPKCEPollInterval 是前端轮询回调状态的建议间隔。
	microsoftPKCEPollInterval = 2 * time.Second

	// microsoftPKCEVerifierBytes 是 code_verifier 的随机字节数（base64url 后 43 字符）。
	microsoftPKCEVerifierBytes = 32

	// microsoftPKCEStateBytes 是 state 的随机字节数。
	microsoftPKCEStateBytes = 16

	// microsoftPKCEChallengeMethod 是 PKCE 的挑战算法（S256）。
	microsoftPKCEChallengeMethod = "S256"

	// microsoftPKCEResponseMode 让授权结果以 query 参数回跳（便于读取）。
	microsoftPKCEResponseMode = "query"
)

// microsoftPKCECallback 是回环服务收到的一次授权结果。
type microsoftPKCECallback struct {
	// code 为授权码；error 非空时为空。
	code string

	// errCode 为 OAuth 错误名（如 access_denied）。
	errCode string
}

// microsoftPKCELoginFlow 是一次进行中的 PKCE 登录。
//
// 并发约束与设备码流程一致：Poll / Cancel 可能来自不同 goroutine，
// 全部状态由 mu 保护；Poll 在终态幂等，并发 Poll 只有一次会真正推进。
type microsoftPKCELoginFlow struct {
	id       string
	provider *microsoftProvider

	mu          sync.Mutex
	state       LoginState
	challenge   LoginChallenge
	outcome     LoginOutcome
	terminalErr *Error
	polling     bool

	// codeVerifier 是 PKCE 的 code_verifier（仅内存，绝不外泄）。
	codeVerifier string

	// stateParam 是 OAuth state，用于校验回跳请求确实由本次登录发起。
	stateParam string

	// redirectURI 是本次登录注册的回环地址（http://localhost:PORT）。
	redirectURI string

	// expiresAt 是等待回调的截止时间。
	expiresAt time.Time

	// callback 缓冲 1：回环服务收到结果后写入，Poll 读取。
	callback chan microsoftPKCECallback

	// listener / server 为本地回环服务，closeOnce 保证只关闭一次。
	listener  net.Listener
	server    *http.Server
	closeOnce sync.Once
}

// startPKCELogin 建立 PKCE 登录流程：监听回环 → 打开浏览器 → 等待回跳。
func (p *microsoftProvider) startPKCELogin(_ context.Context) (LoginFlow, *Error) {
	flowID, idErr := NewUUIDv4()
	if idErr != nil {
		return nil, idErr
	}
	verifier, verifierErr := microsoftRandomURLSafe(microsoftPKCEVerifierBytes)
	if verifierErr != nil {
		return nil, verifierErr
	}
	stateParam, stateErr := microsoftRandomURLSafe(microsoftPKCEStateBytes)
	if stateErr != nil {
		return nil, stateErr
	}

	// 先监听再打开浏览器：浏览器回跳非常快，晚监听会丢回调。
	listener, listenErr := net.Listen("tcp", "127.0.0.1:0")
	if listenErr != nil {
		return nil, newSystemError(
			ErrorCodeLogin,
			"无法启动本地登录回调服务（端口被占用或被安全软件拦截），请改用设备码登录",
		)
	}
	tcpAddress, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		_ = listener.Close()
		return nil, newSystemError(ErrorCodeLogin, "无法确定本地登录回调端口，请改用设备码登录")
	}

	now := p.deps.Now()
	flow := &microsoftPKCELoginFlow{
		id:           flowID,
		provider:     p,
		state:        LoginStateAwaitingUser,
		codeVerifier: verifier,
		stateParam:   stateParam,
		redirectURI:  fmt.Sprintf("http://%s:%d", microsoftPKCERedirectHost, tcpAddress.Port),
		expiresAt:    now.Add(microsoftPKCEExpiry),
		callback:     make(chan microsoftPKCECallback, 1),
		listener:     listener,
	}
	flow.server = &http.Server{
		Handler:           http.HandlerFunc(flow.handleCallback),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		// Serve 返回错误（包括正常关闭）都不影响流程状态，状态由 Poll 决定。
		_ = flow.server.Serve(listener)
	}()

	authorizeURL := p.buildAuthorizeURL(flow.redirectURI, microsoftPKCEChallenge(verifier), stateParam)
	challenge := LoginChallenge{
		VerificationURI: authorizeURL,
		Message:         "已在系统浏览器中打开 Microsoft 登录页；完成登录后会自动返回启动器。",
		ExpiresAtMs:     flow.expiresAt.UnixMilli(),
		IntervalSeconds: int(microsoftPKCEPollInterval / time.Second),
	}
	// 浏览器打开失败不中断流程：界面仍有 verificationUri，可让用户手动打开。
	if openErr := p.deps.OpenURL(authorizeURL); openErr != nil {
		challenge.Message = "无法自动打开浏览器：请点击「重新打开登录页」，或改用设备码登录。"
	}

	flow.challenge = challenge
	flow.outcome = LoginOutcome{State: LoginStateAwaitingUser, Challenge: challenge}
	return flow, nil
}

// buildAuthorizeURL 构造 OAuth 授权地址（含 PKCE challenge 与 state）。
func (p *microsoftProvider) buildAuthorizeURL(redirectURI, codeChallenge, stateParam string) string {
	authorizeURL := p.endpoints.AuthorizeURL
	parsed, err := url.Parse(authorizeURL)
	if err != nil {
		return authorizeURL
	}
	query := parsed.Query()
	query.Set("client_id", p.deps.Microsoft.ClientID)
	query.Set("response_type", "code")
	query.Set("redirect_uri", redirectURI)
	query.Set("response_mode", microsoftPKCEResponseMode)
	query.Set("scope", microsoftScope)
	query.Set("state", stateParam)
	query.Set("code_challenge", codeChallenge)
	query.Set("code_challenge_method", microsoftPKCEChallengeMethod)
	// 每次登录都让用户选择账号，避免多个 Microsoft 账号时默默复用上次会话。
	query.Set("prompt", "select_account")
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

// handleCallback 处理浏览器回跳。
//
// 只接受 state 匹配的请求；成功与失败都会返回一段静态 HTML 提示用户回启动器。
func (f *microsoftPKCELoginFlow) handleCallback(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	if query.Get("state") != f.stateParam {
		f.writeCallbackPage(w, http.StatusBadRequest, callbackToneError, "登录状态校验失败", "请回到 NineVoxel 启动器重新发起登录。")
		return
	}

	callback := microsoftPKCECallback{code: query.Get("code"), errCode: query.Get("error")}
	if callback.code == "" && callback.errCode == "" {
		f.writeCallbackPage(w, http.StatusBadRequest, callbackToneError, "登录回调不完整", "请回到 NineVoxel 启动器重新发起登录。")
		return
	}
	select {
	case f.callback <- callback:
	default:
		// 已有待处理结果：重复回调直接忽略（浏览器刷新/重放）。
	}

	if callback.errCode == "" {
		f.writeCallbackPage(w, http.StatusOK, callbackToneSuccess, "登录成功", "已完成 Microsoft 授权，请返回 NineVoxel 启动器继续。")
	} else {
		f.writeCallbackPage(w, http.StatusOK, callbackToneWarning, "登录未完成", "授权未完成或已被取消，请返回 NineVoxel 启动器重试。")
	}
	// 响应写出后再关闭服务，避免把当前连接一起中断。
	go f.closeServer()
}

// callbackTone 决定回调页的配色与图标。
type callbackTone int

const (
	// callbackToneSuccess 表示登录成功（品牌绿）。
	callbackToneSuccess callbackTone = iota

	// callbackToneWarning 表示授权未完成（琥珀色）。
	callbackToneWarning

	// callbackToneError 表示回调校验失败（红色）。
	callbackToneError
)

// callbackPageTemplate 是本地回调页的 HTML 模板。
//
// 约束：
//   - 不引用任何外部资源（无字体、图片、脚本），离线与内网环境都能正常显示；
//   - 深色卡片与启动器视觉一致，让用户明确「这是 NineVoxel 的页面」；
//   - 用占位符替换而不是 fmt.Sprintf，避免 CSS 中的百分号需要转义。
const callbackPageTemplate = `<!doctype html>
<html lang="zh-Hans">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{TITLE}} · NineVoxel Launcher</title>
<style>
  :root { color-scheme: dark; }
  * { box-sizing: border-box; }
  body {
    margin: 0;
    min-height: 100vh;
    display: flex;
    align-items: center;
    justify-content: center;
    padding: 24px;
    background:
      radial-gradient(900px 520px at 50% -12%, rgba(16, 185, 129, 0.16), transparent 60%),
      radial-gradient(760px 420px at 100% 100%, rgba(59, 130, 246, 0.10), transparent 60%),
      #0b1220;
    color: #e2e8f0;
    font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", "PingFang SC", "Microsoft YaHei", sans-serif;
    -webkit-font-smoothing: antialiased;
  }
  .card {
    width: 100%;
    max-width: 440px;
    padding: 36px 32px 28px;
    border: 1px solid #1e293b;
    border-radius: 22px;
    background: linear-gradient(180deg, #111a2e, #0d1526);
    box-shadow: 0 30px 80px -24px rgba(0, 0, 0, 0.75);
    text-align: center;
  }
  .brand {
    display: flex;
    align-items: center;
    justify-content: center;
    gap: 10px;
    margin-bottom: 28px;
  }
  .logo {
    width: 30px;
    height: 30px;
    padding: 5px;
    border-radius: 9px;
    background: linear-gradient(135deg, #10b981, #0d9488);
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 2px;
  }
  .logo span { border-radius: 1.5px; background: #ffffff; opacity: 0.95; }
  .logo span:nth-child(2), .logo span:nth-child(3) { opacity: 0.45; }
  .brand-name { font-size: 14px; font-weight: 700; letter-spacing: 0.2px; }
  .brand-sub { font-size: 11px; color: #64748b; margin-left: 2px; }
  .status-icon {
    width: 64px;
    height: 64px;
    margin: 0 auto 18px;
    border: 1px solid rgba(255, 255, 255, 0.06);
    border-radius: 50%;
    background: rgba(255, 255, 255, 0.03);
    display: flex;
    align-items: center;
    justify-content: center;
    color: {{ACCENT}};
  }
  .title { margin: 0 0 10px; font-size: 19px; font-weight: 700; }
  .message { margin: 0; font-size: 13px; line-height: 1.7; color: #94a3b8; }
  .hint {
    margin-top: 22px;
    padding-top: 16px;
    border-top: 1px solid #1e293b;
    font-size: 11px;
    color: #64748b;
  }
</style>
</head>
<body>
  <main class="card">
    <div class="brand">
      <div class="logo" aria-hidden="true"><span></span><span></span><span></span><span></span></div>
      <span class="brand-name">NineVoxel</span>
      <span class="brand-sub">Launcher</span>
    </div>
    <div class="status-icon" aria-hidden="true">{{ICON}}</div>
    <h1 class="title">{{TITLE}}</h1>
    <p class="message">{{MESSAGE}}</p>
    <div class="hint">此页面由 NineVoxel 启动器打开，可以安全关闭。</div>
  </main>
</body>
</html>`

// callbackPageIcons 是三种状态的内联图标（不依赖外部资源）。
var callbackPageIcons = map[callbackTone]string{
	callbackToneSuccess: `<svg width="28" height="28" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round"><path d="M20 6 9 17l-5-5"/></svg>`,
	callbackToneWarning: `<svg width="28" height="28" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M10.29 3.86 1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0Z"/><path d="M12 9v4"/><path d="M12 17h.01"/></svg>`,
	callbackToneError:   `<svg width="28" height="28" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="9"/><path d="m15 9-6 6"/><path d="m9 9 6 6"/></svg>`,
}

// writeCallbackPage 输出品牌化的本地回调页（不引用任何外部资源）。
//
// 参数 status 为 HTTP 状态码；tone 决定配色与图标；title/message 会做 HTML 转义。
func (f *microsoftPKCELoginFlow) writeCallbackPage(w http.ResponseWriter, status int, tone callbackTone, title, message string) {
	accent := "#34d399"
	switch tone {
	case callbackToneWarning:
		accent = "#fbbf24"
	case callbackToneError:
		accent = "#f87171"
	}
	icon, ok := callbackPageIcons[tone]
	if !ok {
		icon = callbackPageIcons[callbackToneError]
	}

	page := strings.NewReplacer(
		"{{TITLE}}", html.EscapeString(title),
		"{{MESSAGE}}", html.EscapeString(message),
		"{{ACCENT}}", accent,
		"{{ICON}}", icon,
	).Replace(callbackPageTemplate)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, page)
}

// ID 返回流程标识。
func (f *microsoftPKCELoginFlow) ID() string { return f.id }

// Kind 返回来源类型。
func (f *microsoftPKCELoginFlow) Kind() Kind { return KindMicrosoft }

// State 返回当前状态（不产生 IO）。
func (f *microsoftPKCELoginFlow) State() LoginState {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state
}

// Challenge 返回需要展示给用户的交互信息（不产生 IO）。
func (f *microsoftPKCELoginFlow) Challenge() LoginChallenge {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.challenge
}

// Profiles 返回待选档案（微软账号只有一个档案，恒为空）。
func (f *microsoftPKCELoginFlow) Profiles() []Profile { return nil }

// Poll 推进流程：检查回跳结果 → 换 token → XBL → XSTS → Minecraft → Profile。
//
// 终态幂等：流程结束后重复调用直接返回同一快照，不再发起网络请求。
func (f *microsoftPKCELoginFlow) Poll(ctx context.Context) (LoginOutcome, *Error) {
	f.mu.Lock()
	if f.state.Terminal() {
		outcome, terminalErr := f.outcome, f.terminalErr
		f.mu.Unlock()
		return outcome, terminalErr
	}
	if f.polling {
		// 已有一次 Poll 在推进流程，本次只返回当前快照，避免重复交换授权码。
		outcome := f.outcome
		f.mu.Unlock()
		return outcome, nil
	}
	f.polling = true
	f.mu.Unlock()

	outcome, pollErr := f.advance(ctx)

	f.mu.Lock()
	defer f.mu.Unlock()
	f.polling = false
	if f.state.Terminal() {
		// 推进期间被 Cancel：丢弃本次结果，保持取消终态。
		return f.outcome, f.terminalErr
	}
	if pollErr != nil {
		f.state = LoginStateFailed
		f.terminalErr = pollErr
		f.outcome = LoginOutcome{
			State:        LoginStateFailed,
			ErrorCode:    pollErr.Code,
			ErrorMessage: pollErr.Message,
		}
		return f.outcome, pollErr
	}
	f.outcome = outcome
	if outcome.State == LoginStateSucceeded || outcome.State == LoginStateFailed {
		f.state = outcome.State
	}
	return outcome, nil
}

// advance 执行一次流程推进。
//
// 返回 (outcome, nil) 表示状态推进成功（可能仍是等待中）；
// 返回非 nil error 仅用于「可重试的流程性错误」，终态失败通过 outcome.State 表达。
func (f *microsoftPKCELoginFlow) advance(ctx context.Context) (LoginOutcome, *Error) {
	if f.provider.deps.Now().After(f.expiresAt) {
		f.closeServer()
		return LoginOutcome{
			State:        LoginStateFailed,
			ErrorCode:    ErrorCodeLoginExpired,
			ErrorMessage: "登录已超时，请重新开始",
		}, nil
	}

	var callback microsoftPKCECallback
	select {
	case callback = <-f.callback:
	default:
		// 用户还没完成浏览器授权。
		return LoginOutcome{State: LoginStateAwaitingUser, Challenge: f.challenge}, nil
	}
	f.closeServer()

	if callback.errCode != "" {
		code, message := microsoftPKCECallbackError(callback.errCode)
		return LoginOutcome{State: LoginStateFailed, ErrorCode: code, ErrorMessage: message}, nil
	}

	token, tokenErr := f.provider.exchangeAuthorizationCode(ctx, callback.code, f.redirectURI, f.codeVerifier)
	if tokenErr != nil {
		return LoginOutcome{State: LoginStateFailed, ErrorCode: tokenErr.Code, ErrorMessage: tokenErr.Message}, nil
	}
	account, credentials, loginErr := f.provider.completeLogin(ctx, token.accessToken, token.refreshToken)
	if loginErr != nil {
		return LoginOutcome{State: LoginStateFailed, ErrorCode: loginErr.Code, ErrorMessage: loginErr.Message}, nil
	}
	return LoginOutcome{
		State:       LoginStateSucceeded,
		Account:     account,
		Credentials: credentials,
	}, nil
}

// Cancel 取消流程（幂等）：关闭回环服务，终态保持取消。
func (f *microsoftPKCELoginFlow) Cancel() {
	f.mu.Lock()
	if !f.state.Terminal() {
		f.state = LoginStateCancelled
		f.outcome = LoginOutcome{State: LoginStateCancelled}
	}
	f.mu.Unlock()
	f.closeServer()
}

// closeServer 关闭回环服务（幂等）。
func (f *microsoftPKCELoginFlow) closeServer() {
	f.closeOnce.Do(func() {
		if f.server != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = f.server.Shutdown(ctx)
		}
		if f.listener != nil {
			_ = f.listener.Close()
		}
	})
}

// exchangeAuthorizationCode 用授权码 + code_verifier 换取微软令牌。
func (p *microsoftProvider) exchangeAuthorizationCode(ctx context.Context, code, redirectURI, verifier string) (microsoftTokenPollResult, *Error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("client_id", p.deps.Microsoft.ClientID)
	form.Set("code", code)
	form.Set("redirect_uri", redirectURI)
	form.Set("code_verifier", verifier)
	response, err := DoForm(ctx, p.deps.HTTP, p.endpoints.TokenURL, form, p.deps.UserAgent)
	if err != nil {
		return microsoftTokenPollResult{}, err
	}

	var decoded microsoftTokenResponse
	decodeErr := DecodeJSON(response, &decoded)
	if decodeErr != nil {
		if !isSuccess(response.Status) {
			return microsoftTokenPollResult{}, newExternalError(
				ErrorCodeLogin,
				microsoftOAuthErrorHint(response, fmt.Sprintf("微软登录失败（HTTP %d）", response.Status)),
				response.Status >= 500,
			)
		}
		return microsoftTokenPollResult{}, decodeErr
	}
	if decoded.Error != "" {
		switch decoded.Error {
		case "invalid_grant":
			return microsoftTokenPollResult{}, newAuthError(ErrorCodeAuth, "登录已过期或授权已被使用，请重新登录")
		case "invalid_client":
			return microsoftTokenPollResult{}, newAuthError(
				ErrorCodeLogin,
				microsoftOAuthErrorHint(response, "微软拒绝该客户端（invalid_client）"),
			)
		default:
			return microsoftTokenPollResult{}, newAuthError(
				ErrorCodeLogin,
				microsoftOAuthErrorHint(response, "微软登录失败，请重新开始"),
			)
		}
	}
	if !isSuccess(response.Status) {
		return microsoftTokenPollResult{}, newExternalError(
			ErrorCodeLogin,
			microsoftOAuthErrorHint(response, fmt.Sprintf("微软登录失败（HTTP %d）", response.Status)),
			response.Status >= 500,
		)
	}
	if decoded.AccessToken == "" {
		return microsoftTokenPollResult{}, newExternalError(ErrorCodeLogin, "微软登录服务返回了不完整的数据", true)
	}
	return microsoftTokenPollResult{
		accessToken:  decoded.AccessToken,
		refreshToken: decoded.RefreshToken,
		expiresIn:    decoded.ExpiresIn,
	}, nil
}

// microsoftPKCECallbackError 把回跳错误映射成稳定错误码与中文文案。
func microsoftPKCECallbackError(errCode string) (string, string) {
	switch strings.ToLower(strings.TrimSpace(errCode)) {
	case "access_denied":
		return ErrorCodeLoginDeclined, "用户取消了授权"
	case "temporarily_unavailable", "server_error":
		return ErrorCodeNetwork, "微软登录服务暂时不可用，请稍后重试"
	default:
		return ErrorCodeLogin, "微软登录未完成，请重新开始"
	}
}

// microsoftRandomURLSafe 生成指定字节数的 base64url 随机串。
func microsoftRandomURLSafe(size int) (string, *Error) {
	buffer := make([]byte, size)
	if _, err := rand.Read(buffer); err != nil {
		return "", newSystemError(ErrorCodeCrypto, "无法生成登录参数，请重试")
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}

// microsoftPKCEChallenge 计算 S256 code_challenge。
func microsoftPKCEChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
