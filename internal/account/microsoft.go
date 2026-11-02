// 本文件实现微软账号来源：OAuth 2.0 Device Code Flow → Xbox Live → XSTS →
// Minecraft Services。
//
// 安全边界（与 doc.go 一致）：
//   - 所有错误都是 *Error，文案不包含 token、device_code、密码或服务端完整响应体；
//   - 本文件不打印任何日志，token 只在内存中流转；
//   - 所有端点都可注入（Deps.Microsoft.Endpoints），零值字段回落到官方默认地址，
//     因此测试可以完全离线。
package account

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// 微软登录链路的稳定常量。
const (
	// microsoftProviderID 是微软来源在 Account.ProviderID 中的稳定取值。
	microsoftProviderID = "microsoft"

	// microsoftScope 是设备码与刷新令牌共用的 OAuth scope。
	// XboxLive.signin 用于换取 Xbox Live 令牌，offline_access 用于拿到 refresh_token。
	microsoftScope = "XboxLive.signin offline_access"

	// microsoftDeviceCodeGrant 是设备码轮询的 grant_type。
	microsoftDeviceCodeGrant = "urn:ietf:params:oauth:grant-type:device_code"

	// microsoftDefaultInterval 是设备码端点未返回 interval 时的默认轮询间隔。
	microsoftDefaultInterval = 5 * time.Second

	// microsoftSlowDownStep 是服务端返回 slow_down 时增加的轮询间隔。
	microsoftSlowDownStep = 5 * time.Second
)

// defaultMicrosoftEndpoints 返回微软登录链路的官方默认端点。
//
// 集中在一处便于审计：所有地址都是公开的 OAuth/Xbox/Minecraft 服务地址，
// 不含任何密钥。租户为空时回落到 consumers（消费者账号必须使用该租户）。
func defaultMicrosoftEndpoints(tenant string) MicrosoftEndpoints {
	if tenant == "" {
		tenant = DefaultMicrosoftTenant
	}
	oauthBase := "https://login.microsoftonline.com/" + tenant + "/oauth2/v2.0"
	return MicrosoftEndpoints{
		AuthorizeURL:        oauthBase + "/authorize",
		DeviceCodeURL:       oauthBase + "/devicecode",
		TokenURL:            oauthBase + "/token",
		XBLURL:              "https://user.auth.xboxlive.com/user/authenticate",
		XSTSURL:             "https://xsts.auth.xboxlive.com/xsts/authorize",
		MinecraftLoginURL:   "https://api.minecraftservices.com/authentication/login_with_xbox",
		MinecraftProfileURL: "https://api.minecraftservices.com/minecraft/profile",
		EntitlementsURL:     "https://api.minecraftservices.com/entitlements/mcstore",
	}
}

// microsoftEndpointsOrDefault 用默认值补齐配置中的零值端点。
//
// 已显式配置的端点保持原样，便于测试注入 httptest 地址。
func microsoftEndpointsOrDefault(config MicrosoftConfig) MicrosoftEndpoints {
	endpoints := config.Endpoints
	defaults := defaultMicrosoftEndpoints(config.Tenant)
	if endpoints.AuthorizeURL == "" {
		endpoints.AuthorizeURL = defaults.AuthorizeURL
	}
	if endpoints.DeviceCodeURL == "" {
		endpoints.DeviceCodeURL = defaults.DeviceCodeURL
	}
	if endpoints.TokenURL == "" {
		endpoints.TokenURL = defaults.TokenURL
	}
	if endpoints.XBLURL == "" {
		endpoints.XBLURL = defaults.XBLURL
	}
	if endpoints.XSTSURL == "" {
		endpoints.XSTSURL = defaults.XSTSURL
	}
	if endpoints.MinecraftLoginURL == "" {
		endpoints.MinecraftLoginURL = defaults.MinecraftLoginURL
	}
	if endpoints.MinecraftProfileURL == "" {
		endpoints.MinecraftProfileURL = defaults.MinecraftProfileURL
	}
	if endpoints.EntitlementsURL == "" {
		endpoints.EntitlementsURL = defaults.EntitlementsURL
	}
	return endpoints
}

// microsoftProvider 是微软账号来源。
//
// 实现无状态：一次登录的全部可变状态放在 microsoftLoginFlow 里，因此同一进程
// 可以并行多个登录流程。
type microsoftProvider struct {
	deps      Deps
	endpoints MicrosoftEndpoints
}

// newMicrosoftProvider 构造微软账号来源并补齐依赖与端点。
func newMicrosoftProvider(deps Deps) *microsoftProvider {
	normalized := deps.normalize()
	return &microsoftProvider{
		deps:      normalized,
		endpoints: microsoftEndpointsOrDefault(normalized.Microsoft),
	}
}

// NewMicrosoftProvider 构造微软账号来源（Provider SPI 入口）。
func NewMicrosoftProvider(deps Deps) Provider { return newMicrosoftProvider(deps) }

// Info 返回来源静态描述。
func (p *microsoftProvider) Info() ProviderInfo {
	return ProviderInfo{
		Kind:        KindMicrosoft,
		DisplayName: KindMicrosoft.DisplayName(),
		Implemented: true,
		Description: "使用微软账号登录：默认在系统浏览器完成授权并自动返回；浏览器无法回跳时可改用设备码。",
		Capabilities: Capabilities{
			Interactive:      true,
			Refresh:          true,
			Validate:         true,
			Logout:           false,
			SkinUpload:       false,
			MultipleProfiles: false,
		},
	}
}

// StartLogin 开始一次微软登录。
//
// 默认使用「授权码 + PKCE + 本地回环」：打开系统浏览器，登录完成后浏览器回跳
// 到 127.0.0.1 的临时端口，体验最顺。调用方可通过 LoginRequest.LoginMode
// 指定 "device_code" 回退到设备码流程（例如浏览器无法回跳、端口被安全软件拦截）。
//
// 本步骤只负责建立流程；令牌交换与后续 XBL/XSTS/Minecraft 链路由 Poll 推进。
func (p *microsoftProvider) StartLogin(ctx context.Context, req LoginRequest) (LoginFlow, *Error) {
	switch strings.TrimSpace(req.LoginMode) {
	case microsoftLoginModeDeviceCode:
		return p.startDeviceCodeLogin(ctx)
	default:
		return p.startPKCELogin(ctx)
	}
}

// startDeviceCodeLogin 建立设备码流程（回退路径）。
func (p *microsoftProvider) startDeviceCodeLogin(ctx context.Context) (LoginFlow, *Error) {
	flowID, idErr := NewUUIDv4()
	if idErr != nil {
		return nil, idErr
	}
	challenge, deviceCode, err := p.requestDeviceCode(ctx)
	if err != nil {
		return nil, err
	}
	interval := time.Duration(challenge.IntervalSeconds) * time.Second
	if interval <= 0 {
		interval = microsoftDefaultInterval
	}
	flow := &microsoftLoginFlow{
		id:         flowID,
		provider:   p,
		state:      LoginStateAwaitingUser,
		challenge:  challenge,
		deviceCode: deviceCode,
		interval:   interval,
		outcome:    LoginOutcome{State: LoginStateAwaitingUser, Challenge: challenge},
	}
	return flow, nil
}

// microsoftDeviceCodeResponse 是设备码申请端点的响应。
type microsoftDeviceCodeResponse struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int64  `json:"expires_in"`
	Interval        int64  `json:"interval"`
	Message         string `json:"message"`
	Error           string `json:"error"`
}

// microsoftOAuthError 是微软 OAuth 错误响应的公共字段。
//
// 只取 error / error_codes / error_description 中的 AADSTS 码用于诊断；
// 完整 error_description（含 Trace ID、时间戳）**不**进入用户文案。
type microsoftOAuthError struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
	ErrorCodes       []int  `json:"error_codes"`
}

// microsoftAADSTSPattern 用于从 error_description 兜底提取 AADSTS 数字码。
var microsoftAADSTSPattern = regexp.MustCompile(`AADSTS\d+`)

// microsoftOAuthErrorHint 把微软 OAuth 错误转换成可操作的中文提示。
//
// 安全：只输出稳定的 OAuth 错误名与 AADSTS 码（两者都不是凭证），
// 不透传 error_description 原文，避免把 trace/correlation ID 与响应细节带进界面。
// 参数 response 为原始响应（可为 nil）；fallback 为无法识别时的兜底文案。
func microsoftOAuthErrorHint(response *HTTPResponse, fallback string) string {
	var decoded microsoftOAuthError
	if response != nil {
		_ = DecodeJSON(response, &decoded)
	}

	aadsts := ""
	if len(decoded.ErrorCodes) > 0 {
		aadsts = fmt.Sprintf("AADSTS%d", decoded.ErrorCodes[0])
	} else if match := microsoftAADSTSPattern.FindString(decoded.ErrorDescription); match != "" {
		aadsts = match
	}

	hint := ""
	switch aadsts {
	case "AADSTS70002", "AADSTS7000218":
		hint = "Entra 应用未启用公共客户端：请到「身份验证 → 高级设置 → 允许公共客户端流」选择「是」后重试"
	case "AADSTS50194":
		hint = "该应用是单租户应用：请把「受支持的账户类型」改为包含「个人 Microsoft 账户」"
	case "AADSTS9002325", "AADSTS9002331", "AADSTS90002":
		hint = "该应用未配置支持个人 Microsoft 账户（consumers 端点），请检查受支持的账户类型"
	case "AADSTS700016":
		hint = "Entra 客户端 ID 不存在或不属于该租户，请检查客户端 ID"
	}
	if hint == "" {
		switch decoded.Error {
		case "invalid_client":
			hint = "微软拒绝该客户端（invalid_client）：通常是应用未启用公共客户端或客户端 ID 不正确"
		case "unauthorized_client":
			hint = "该应用不允许这种登录方式（unauthorized_client）"
		}
	}
	if hint != "" && aadsts != "" {
		return fmt.Sprintf("%s（%s）", hint, aadsts)
	}
	if hint != "" {
		return hint
	}
	return fallback
}

// requestDeviceCode 申请设备码并转换为 LoginChallenge。
func (p *microsoftProvider) requestDeviceCode(ctx context.Context) (LoginChallenge, string, *Error) {
	form := url.Values{}
	form.Set("client_id", p.deps.Microsoft.ClientID)
	form.Set("scope", microsoftScope)
	response, err := DoForm(ctx, p.deps.HTTP, p.endpoints.DeviceCodeURL, form, p.deps.UserAgent)
	if err != nil {
		return LoginChallenge{}, "", err
	}
	if !isSuccess(response.Status) {
		return LoginChallenge{}, "", newExternalError(
			ErrorCodeLogin,
			microsoftOAuthErrorHint(response, fmt.Sprintf("无法开始微软登录（HTTP %d）", response.Status)),
			response.Status >= 500,
		)
	}
	var decoded microsoftDeviceCodeResponse
	if decodeErr := DecodeJSON(response, &decoded); decodeErr != nil {
		return LoginChallenge{}, "", decodeErr
	}
	if decoded.Error != "" {
		return LoginChallenge{}, "", newExternalError(ErrorCodeLogin, "无法开始微软登录，请稍后重试", true)
	}
	if decoded.DeviceCode == "" || decoded.UserCode == "" {
		return LoginChallenge{}, "", newExternalError(ErrorCodeLogin, "微软登录服务返回了不完整的数据", true)
	}
	intervalSeconds := decoded.Interval
	if intervalSeconds <= 0 {
		intervalSeconds = int64(microsoftDefaultInterval / time.Second)
	}
	var expiresAtMs int64
	if decoded.ExpiresIn > 0 {
		expiresAtMs = p.deps.Now().Add(time.Duration(decoded.ExpiresIn) * time.Second).UnixMilli()
	}
	return LoginChallenge{
		UserCode:        decoded.UserCode,
		VerificationURI: decoded.VerificationURI,
		Message:         decoded.Message,
		ExpiresAtMs:     expiresAtMs,
		IntervalSeconds: int(intervalSeconds),
	}, decoded.DeviceCode, nil
}

// microsoftLoginFlow 是一次进行中的微软设备码登录。
//
// 并发约束：Poll 与 Cancel 可能来自不同 goroutine，全部状态由 mu 保护；
// Poll 在终态上幂等，且并发 Poll 只会有一个真正发起网络请求。
type microsoftLoginFlow struct {
	id       string
	provider *microsoftProvider

	mu          sync.Mutex
	state       LoginState
	challenge   LoginChallenge
	outcome     LoginOutcome
	terminalErr *Error
	deviceCode  string
	interval    time.Duration
	polling     bool
}

// ID 返回流程标识。
func (f *microsoftLoginFlow) ID() string { return f.id }

// Kind 返回来源类型。
func (f *microsoftLoginFlow) Kind() Kind { return KindMicrosoft }

// State 返回当前状态（不产生 IO）。
func (f *microsoftLoginFlow) State() LoginState {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state
}

// Challenge 返回需要展示给用户的交互信息（不产生 IO）。
func (f *microsoftLoginFlow) Challenge() LoginChallenge {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.challenge
}

// Profiles 返回待选择的档案列表（微软账号只有一个档案，恒为空）。
func (f *microsoftLoginFlow) Profiles() []Profile {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.outcome.Profiles) == 0 {
		return nil
	}
	profiles := make([]Profile, len(f.outcome.Profiles))
	copy(profiles, f.outcome.Profiles)
	return profiles
}

// Poll 推进流程：设备码轮询 → XBL → XSTS → Minecraft → Profile。
//
// 终态幂等：流程结束后重复调用直接返回同一快照，不再发起网络请求。
func (f *microsoftLoginFlow) Poll(ctx context.Context) (LoginOutcome, *Error) {
	f.mu.Lock()
	if f.state.Terminal() {
		outcome, terminalErr := f.outcome, f.terminalErr
		f.mu.Unlock()
		return outcome, terminalErr
	}
	if f.polling {
		// 已有一次 Poll 在推进流程，本次只返回当前快照，避免重复请求网络。
		outcome := f.outcome
		f.mu.Unlock()
		return outcome, nil
	}
	f.polling = true
	deviceCode := f.deviceCode
	interval := f.interval
	f.mu.Unlock()

	result, pollErr := f.advance(ctx, deviceCode, interval)

	f.mu.Lock()
	defer f.mu.Unlock()
	f.polling = false
	if f.state.Terminal() {
		// 轮询期间被 Cancel：丢弃本次结果，保持取消终态。
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
	if result.pending {
		f.interval = result.interval
		f.challenge.IntervalSeconds = int(result.interval / time.Second)
		f.outcome = LoginOutcome{State: LoginStateAwaitingUser, Challenge: f.challenge}
		return f.outcome, nil
	}
	f.state = LoginStateSucceeded
	f.outcome = LoginOutcome{
		State:       LoginStateSucceeded,
		Account:     result.account,
		Credentials: result.credentials,
	}
	return f.outcome, nil
}

// Cancel 取消流程（幂等）；已经结束的流程保持原终态。
func (f *microsoftLoginFlow) Cancel() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.state.Terminal() {
		return
	}
	f.state = LoginStateCancelled
	f.terminalErr = nil
	f.outcome = LoginOutcome{State: LoginStateCancelled}
}

// microsoftPollResult 是一次 Poll 推进的结果（内部使用）。
type microsoftPollResult struct {
	pending     bool
	interval    time.Duration
	account     *Account
	credentials *Credentials
}

// advance 执行一次完整推进：先轮询设备码令牌，拿到 MS token 后走完后续链路。
func (f *microsoftLoginFlow) advance(ctx context.Context, deviceCode string, interval time.Duration) (microsoftPollResult, *Error) {
	token, pollErr := f.provider.pollDeviceCodeToken(ctx, deviceCode, interval)
	if pollErr != nil {
		return microsoftPollResult{}, pollErr
	}
	if token.pending {
		return microsoftPollResult{pending: true, interval: token.interval}, nil
	}
	account, credentials, err := f.provider.completeLogin(ctx, token.accessToken, token.refreshToken)
	if err != nil {
		return microsoftPollResult{}, err
	}
	return microsoftPollResult{account: account, credentials: credentials}, nil
}

// microsoftTokenResponse 是 OAuth token 端点（设备码轮询与 refresh 共用）的响应。
type microsoftTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	Error        string `json:"error"`
}

// microsoftTokenPollResult 是设备码轮询结果。
type microsoftTokenPollResult struct {
	pending      bool
	interval     time.Duration
	accessToken  string
	refreshToken string
	expiresIn    int64
}

// pollDeviceCodeToken 向 token 端点发起一次设备码轮询。
func (p *microsoftProvider) pollDeviceCodeToken(ctx context.Context, deviceCode string, interval time.Duration) (microsoftTokenPollResult, *Error) {
	form := url.Values{}
	form.Set("grant_type", microsoftDeviceCodeGrant)
	form.Set("client_id", p.deps.Microsoft.ClientID)
	form.Set("device_code", deviceCode)
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
		case "authorization_pending":
			return microsoftTokenPollResult{pending: true, interval: interval}, nil
		case "slow_down":
			return microsoftTokenPollResult{pending: true, interval: interval + microsoftSlowDownStep}, nil
		case "authorization_declined":
			return microsoftTokenPollResult{}, newAuthError(ErrorCodeLoginDeclined, "用户取消了授权")
		case "expired_token":
			return microsoftTokenPollResult{}, newAuthError(ErrorCodeLoginExpired, "登录已超时，请重新开始")
		case "bad_verification_code":
			return microsoftTokenPollResult{}, newAuthError(ErrorCodeLogin, "登录会话已失效，请重新开始")
		default:
			// 不透传服务端 error_description，只给出稳定的错误名/AADSTS 码。
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

// microsoftXBLResponse 是 XBL / XSTS 端点的响应（两者结构一致）。
type microsoftXBLResponse struct {
	Token         string `json:"Token"`
	DisplayClaims struct {
		XUI []struct {
			UHS string `json:"uhs"`
		} `json:"xui"`
	} `json:"DisplayClaims"`
}

// microsoftXBLResult 是 XBL / XSTS 链路结果。
type microsoftXBLResult struct {
	token string
	uhs   string
}

// authenticateXbox 用 MS access token 换取 Xbox Live 用户令牌。
func (p *microsoftProvider) authenticateXbox(ctx context.Context, msAccessToken string) (microsoftXBLResult, *Error) {
	payload := map[string]any{
		"Properties": map[string]string{
			"AuthMethod": "RPS",
			"SiteName":   "user.auth.xboxlive.com",
			"RpsTicket":  "d=" + msAccessToken,
		},
		"RelyingParty": "http://auth.xboxlive.com",
		"TokenType":    "JWT",
	}
	response, err := DoJSON(ctx, p.deps.HTTP, http.MethodPost, p.endpoints.XBLURL, payload, p.deps.UserAgent)
	if err != nil {
		return microsoftXBLResult{}, err
	}
	if !isSuccess(response.Status) {
		return microsoftXBLResult{}, newAuthError(
			ErrorCodeLogin,
			fmt.Sprintf("Xbox Live 授权失败（HTTP %d）", response.Status),
		)
	}
	var decoded microsoftXBLResponse
	if decodeErr := DecodeJSON(response, &decoded); decodeErr != nil {
		return microsoftXBLResult{}, decodeErr
	}
	if decoded.Token == "" || len(decoded.DisplayClaims.XUI) == 0 || decoded.DisplayClaims.XUI[0].UHS == "" {
		return microsoftXBLResult{}, newAuthError(ErrorCodeLogin, "Xbox Live 返回了不完整的数据")
	}
	return microsoftXBLResult{token: decoded.Token, uhs: decoded.DisplayClaims.XUI[0].UHS}, nil
}

// microsoftXSTSErrorResponse 是 XSTS 401 响应里的错误信息。
type microsoftXSTSErrorResponse struct {
	XErr    int64  `json:"XErr"`
	Message string `json:"Message"`
}

// authorizeXSTS 用 XBL 用户令牌换取 XSTS 授权令牌。
func (p *microsoftProvider) authorizeXSTS(ctx context.Context, xblToken string) (microsoftXBLResult, *Error) {
	payload := map[string]any{
		"Properties": map[string]any{
			"SandboxId":  "RETAIL",
			"UserTokens": []string{xblToken},
		},
		"RelyingParty": "rp://api.minecraftservices.com/",
		"TokenType":    "JWT",
	}
	response, err := DoJSON(ctx, p.deps.HTTP, http.MethodPost, p.endpoints.XSTSURL, payload, p.deps.UserAgent)
	if err != nil {
		return microsoftXBLResult{}, err
	}
	if response.Status == http.StatusUnauthorized {
		return microsoftXBLResult{}, microsoftXSTSError(response)
	}
	if !isSuccess(response.Status) {
		return microsoftXBLResult{}, newAuthError(
			ErrorCodeLogin,
			fmt.Sprintf("Xbox 授权失败（HTTP %d）", response.Status),
		)
	}
	var decoded microsoftXBLResponse
	if decodeErr := DecodeJSON(response, &decoded); decodeErr != nil {
		return microsoftXBLResult{}, decodeErr
	}
	if decoded.Token == "" || len(decoded.DisplayClaims.XUI) == 0 || decoded.DisplayClaims.XUI[0].UHS == "" {
		return microsoftXBLResult{}, newAuthError(ErrorCodeLogin, "Xbox 授权失败，请重新登录")
	}
	return microsoftXBLResult{token: decoded.Token, uhs: decoded.DisplayClaims.XUI[0].UHS}, nil
}

// microsoftXSTSError 把 XSTS 401 响应映射为稳定的中文错误。
func microsoftXSTSError(response *HTTPResponse) *Error {
	var decoded microsoftXSTSErrorResponse
	if response != nil && len(response.Body) > 0 {
		// 解析失败不影响错误归类：仍然返回不可重试的登录错误。
		_ = json.Unmarshal(response.Body, &decoded)
	}
	if decoded.XErr != 0 {
		return newAuthError(ErrorCodeLogin, microsoftXErrMessage(decoded.XErr))
	}
	return newAuthError(ErrorCodeLogin, "Xbox 授权失败，请重新登录")
}

// microsoftXErrMessage 返回 Xbox 错误码对应的中文文案。
func microsoftXErrMessage(code int64) string {
	switch code {
	case 2148916227:
		return "该账号已被 Xbox 封禁"
	case 2148916233:
		return "该账号还没有 Xbox 档案"
	case 2148916235:
		return "所在地区不支持 Xbox Live"
	case 2148916236, 2148916237:
		return "该账号需要在 Xbox 页面完成成人验证"
	case 2148916238:
		return "该账号为儿童账号，需要先加入家庭组"
	default:
		return fmt.Sprintf("Xbox 授权失败（XErr %d）", code)
	}
}

// microsoftMinecraftToken 是 Minecraft 登录端点返回的令牌信息。
type microsoftMinecraftToken struct {
	accessToken string
	expiresIn   int64
}

// loginMinecraft 用 XSTS 令牌换取 Minecraft access token。
func (p *microsoftProvider) loginMinecraft(ctx context.Context, uhs, xstsToken string) (microsoftMinecraftToken, *Error) {
	payload := map[string]string{
		"identityToken": "XBL3.0 x=" + uhs + ";" + xstsToken,
	}
	response, err := DoJSON(ctx, p.deps.HTTP, http.MethodPost, p.endpoints.MinecraftLoginURL, payload, p.deps.UserAgent)
	if err != nil {
		return microsoftMinecraftToken{}, err
	}
	if !isSuccess(response.Status) {
		return microsoftMinecraftToken{}, newAuthError(
			ErrorCodeLogin,
			fmt.Sprintf("Minecraft 登录失败（HTTP %d）", response.Status),
		)
	}
	var decoded struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if decodeErr := DecodeJSON(response, &decoded); decodeErr != nil {
		return microsoftMinecraftToken{}, decodeErr
	}
	if decoded.AccessToken == "" {
		return microsoftMinecraftToken{}, newAuthError(ErrorCodeLogin, "Minecraft 登录服务返回了不完整的数据")
	}
	return microsoftMinecraftToken{accessToken: decoded.AccessToken, expiresIn: decoded.ExpiresIn}, nil
}

// microsoftProfileResponse 是 Minecraft 档案端点响应。
type microsoftProfileResponse struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Error string `json:"error"`
	Skins []struct {
		State   string `json:"state"`
		URL     string `json:"url"`
		Variant string `json:"variant"`
	} `json:"skins"`
	Capes []struct {
		State string `json:"state"`
		URL   string `json:"url"`
	} `json:"capes"`
}

// fetchProfile 读取 Minecraft 档案（UUID / 玩家名 / 皮肤 / 披风）。
func (p *microsoftProvider) fetchProfile(ctx context.Context, mcToken string) (Profile, *Error) {
	headers := map[string]string{"Authorization": "Bearer " + mcToken}
	response, err := DoGet(ctx, p.deps.HTTP, p.endpoints.MinecraftProfileURL, headers, p.deps.UserAgent)
	if err != nil {
		return Profile{}, err
	}
	switch {
	case response.Status == http.StatusNotFound:
		return Profile{}, microsoftOwnershipError()
	case response.Status == http.StatusUnauthorized || response.Status == http.StatusForbidden:
		return Profile{}, newAuthError(ErrorCodeAuth, "Minecraft 登录凭证无效，请重新登录")
	case !isSuccess(response.Status):
		return Profile{}, newExternalError(
			ErrorCodeLogin,
			fmt.Sprintf("读取 Minecraft 档案失败（HTTP %d）", response.Status),
			true,
		)
	}
	var decoded microsoftProfileResponse
	if decodeErr := DecodeJSON(response, &decoded); decodeErr != nil {
		return Profile{}, decodeErr
	}
	if decoded.Error == "NOT_FOUND" {
		return Profile{}, microsoftOwnershipError()
	}
	if decoded.ID == "" || decoded.Name == "" {
		return Profile{}, newExternalError(ErrorCodeHTTP, "Minecraft 档案数据不完整", false)
	}
	normalizedID, idErr := NormalizeMinecraftID(decoded.ID)
	if idErr != nil {
		return Profile{}, newExternalError(ErrorCodeHTTP, "Minecraft 档案数据不完整", false)
	}
	profile := Profile{ID: normalizedID, Name: decoded.Name}
	for _, skin := range decoded.Skins {
		if strings.EqualFold(skin.State, "ACTIVE") {
			profile.SkinURL = skin.URL
			profile.SkinModel = microsoftSkinModel(skin.Variant)
			break
		}
	}
	for _, cape := range decoded.Capes {
		if strings.EqualFold(cape.State, "ACTIVE") {
			profile.CapeURL = cape.URL
			break
		}
	}
	// 官方纹理地址统一升级到 https（否则 WebView 混合内容策略会拦掉头像）。
	return normalizeProfileTextures(profile), nil
}

// microsoftOwnershipError 构造「没有可用 Minecraft 档案」的错误。
func microsoftOwnershipError() *Error {
	return newAuthError(ErrorCodeOwnership, "该账号没有可用的 Minecraft 档案（未购买或未创建档案）")
}

// microsoftSkinModel 把 Minecraft 的皮肤变体映射为内部稳定取值。
func microsoftSkinModel(variant string) string {
	switch strings.ToUpper(strings.TrimSpace(variant)) {
	case "SLIM":
		return "slim"
	case "CLASSIC":
		return "classic"
	default:
		return ""
	}
}

// microsoftExpiresAtMs 把 expires_in 秒转换为 UTC Unix 毫秒；未知时返回 0。
func microsoftExpiresAtMs(now time.Time, expiresIn int64) int64 {
	if expiresIn <= 0 {
		return 0
	}
	return now.Add(time.Duration(expiresIn) * time.Second).UnixMilli()
}

// microsoftParseXUID 尽力从 XBL JWT 的 payload 中解析 "xid"（XUID）。
//
// 只做 base64url 解码与 JSON 解析，不校验签名；任何失败都返回空字符串，
// 不影响登录成功（XUID 仅用于启动参数 ${auth_xuid}）。
func microsoftParseXUID(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims struct {
		XID string `json:"xid"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return ""
	}
	return claims.XID
}

// completeLogin 走完 XBL → XSTS → Minecraft → Profile 并组装账号与凭证。
func (p *microsoftProvider) completeLogin(ctx context.Context, msAccessToken, msRefreshToken string) (*Account, *Credentials, *Error) {
	xbl, xblErr := p.authenticateXbox(ctx, msAccessToken)
	if xblErr != nil {
		return nil, nil, xblErr
	}
	xsts, xstsErr := p.authorizeXSTS(ctx, xbl.token)
	if xstsErr != nil {
		return nil, nil, xstsErr
	}
	minecraft, minecraftErr := p.loginMinecraft(ctx, xsts.uhs, xsts.token)
	if minecraftErr != nil {
		return nil, nil, minecraftErr
	}
	profile, profileErr := p.fetchProfile(ctx, minecraft.accessToken)
	if profileErr != nil {
		return nil, nil, profileErr
	}
	account := &Account{
		Kind:        KindMicrosoft,
		ProviderID:  microsoftProviderID,
		DisplayName: profile.Name,
		Profile:     profile,
	}
	credentials := &Credentials{
		AccessToken:  minecraft.accessToken,
		RefreshToken: msRefreshToken,
		ExpiresAtMs:  microsoftExpiresAtMs(p.deps.Now(), minecraft.expiresIn),
		UserType:     UserTypeMSA,
		XUID:         microsoftParseXUID(xbl.token),
	}
	return account, credentials, nil
}

// Refresh 实现 Refresher：用 refresh_token 换新 MS token，再重跑 XBL → XSTS → Minecraft。
func (p *microsoftProvider) Refresh(ctx context.Context, creds Credentials, _ Profile) (Credentials, *Error) {
	if strings.TrimSpace(creds.RefreshToken) == "" {
		return Credentials{}, newAuthError(ErrorCodeAuth, "登录状态已失效，请重新登录")
	}
	token, tokenErr := p.refreshMicrosoftToken(ctx, creds.RefreshToken)
	if tokenErr != nil {
		return Credentials{}, tokenErr
	}
	refreshToken := token.refreshToken
	if refreshToken == "" {
		// 服务端没有轮换 refresh_token 时沿用旧值，避免把有效凭证丢掉。
		refreshToken = creds.RefreshToken
	}
	xbl, xblErr := p.authenticateXbox(ctx, token.accessToken)
	if xblErr != nil {
		return Credentials{}, xblErr
	}
	xsts, xstsErr := p.authorizeXSTS(ctx, xbl.token)
	if xstsErr != nil {
		return Credentials{}, xstsErr
	}
	minecraft, minecraftErr := p.loginMinecraft(ctx, xsts.uhs, xsts.token)
	if minecraftErr != nil {
		return Credentials{}, minecraftErr
	}
	return Credentials{
		AccessToken:  minecraft.accessToken,
		RefreshToken: refreshToken,
		ExpiresAtMs:  microsoftExpiresAtMs(p.deps.Now(), minecraft.expiresIn),
		UserType:     UserTypeMSA,
		XUID:         microsoftParseXUID(xbl.token),
	}, nil
}

// refreshMicrosoftToken 用 refresh_token 换取新的微软 access token。
func (p *microsoftProvider) refreshMicrosoftToken(ctx context.Context, refreshToken string) (microsoftTokenPollResult, *Error) {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("client_id", p.deps.Microsoft.ClientID)
	form.Set("refresh_token", refreshToken)
	form.Set("scope", microsoftScope)
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
				fmt.Sprintf("刷新登录状态失败（HTTP %d）", response.Status),
				true,
			)
		}
		return microsoftTokenPollResult{}, decodeErr
	}
	if decoded.Error != "" {
		switch decoded.Error {
		case "invalid_grant", "invalid_request", "invalid_client", "unauthorized_client":
			return microsoftTokenPollResult{}, newAuthError(ErrorCodeAuth, "登录状态已失效，请重新登录")
		default:
			return microsoftTokenPollResult{}, newExternalError(ErrorCodeLogin, "刷新登录状态失败，请稍后重试", true)
		}
	}
	if !isSuccess(response.Status) {
		if response.Status == http.StatusBadRequest || response.Status == http.StatusUnauthorized {
			return microsoftTokenPollResult{}, newAuthError(ErrorCodeAuth, "登录状态已失效，请重新登录")
		}
		return microsoftTokenPollResult{}, newExternalError(
			ErrorCodeLogin,
			fmt.Sprintf("刷新登录状态失败（HTTP %d）", response.Status),
			true,
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

// Validate 实现 Validator：用 Minecraft access token 读取 Profile。
//
// 200 视为有效；401/403 视为明确失效；其他状态或网络错误返回 error（无法确认）。
func (p *microsoftProvider) Validate(ctx context.Context, creds Credentials) (bool, *Error) {
	if strings.TrimSpace(creds.AccessToken) == "" {
		return false, nil
	}
	headers := map[string]string{"Authorization": "Bearer " + creds.AccessToken}
	response, err := DoGet(ctx, p.deps.HTTP, p.endpoints.MinecraftProfileURL, headers, p.deps.UserAgent)
	if err != nil {
		return false, err
	}
	switch {
	case isSuccess(response.Status):
		return true, nil
	case response.Status == http.StatusUnauthorized || response.Status == http.StatusForbidden:
		return false, nil
	default:
		return false, newExternalError(
			ErrorCodeHTTP,
			fmt.Sprintf("校验登录状态失败（HTTP %d）", response.Status),
			true,
		)
	}
}

// Logout 实现 Logoutger。
//
// 微软公共客户端没有服务端注销端点（refresh_token 无法远程吊销），本地删除
// 账号即可，因此这里直接返回 nil，保证服务层删除流程不被阻塞。
func (p *microsoftProvider) Logout(context.Context, Credentials) *Error { return nil }

// 编译期断言：微软来源必须实现这些可选能力。
var (
	_ Refresher = (*microsoftProvider)(nil)
	_ Validator = (*microsoftProvider)(nil)
	_ Logoutger = (*microsoftProvider)(nil)
)
