// 本文件覆盖微软账号的授权码 + PKCE + 本地回环登录流程（默认方式）。
//
// 测试完全离线：
//   - OpenURL 注入成记录函数，不会真的打开浏览器；
//   - 浏览器回跳由测试直接向 redirect_uri 发起 HTTP 请求模拟；
//   - 令牌 / XBL / XSTS / Minecraft 端点全部指向 microsoftTestServer。
package account

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// microsoftPKCETestDeps 构造指向假服务端、且不真的打开浏览器的依赖。
func microsoftPKCETestDeps(t *testing.T, fake *microsoftTestServer, opened *string) Deps {
	t.Helper()
	deps := fake.deps()
	deps.OpenURL = func(rawURL string) error {
		*opened = rawURL
		return nil
	}
	return deps
}

// microsoftPKCEAuthorizeParams 解析授权地址里的关键参数。
func microsoftPKCEAuthorizeParams(t *testing.T, challenge LoginChallenge) (redirectURI, state, codeChallenge, method string) {
	t.Helper()
	parsed, err := url.Parse(challenge.VerificationURI)
	if err != nil {
		t.Fatalf("授权地址无法解析: %v", err)
	}
	query := parsed.Query()
	if query.Get("response_type") != "code" {
		t.Fatalf("response_type 期望 code，实际 %q", query.Get("response_type"))
	}
	if query.Get("client_id") == "" {
		t.Fatal("授权地址缺少 client_id")
	}
	if query.Get("scope") != microsoftScope {
		t.Fatalf("scope 不匹配: %q", query.Get("scope"))
	}
	return query.Get("redirect_uri"), query.Get("state"), query.Get("code_challenge"), query.Get("code_challenge_method")
}

// microsoftPKCESimulateBrowser 模拟浏览器回跳（GET redirect_uri?code&state）。
func microsoftPKCESimulateBrowser(t *testing.T, redirectURI string, values url.Values) int {
	t.Helper()
	response, err := http.Get(redirectURI + "?" + values.Encode())
	if err != nil {
		t.Fatalf("模拟浏览器回跳失败: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	return response.StatusCode
}

// TestMicrosoftPKCESuccess 覆盖完整成功链路：授权地址 → 回跳 → code 换 token → XBL/XSTS/MC/Profile。
func TestMicrosoftPKCESuccess(t *testing.T) {
	fake := newMicrosoftTestServer(t)
	var openedURL string
	provider := NewMicrosoftProvider(microsoftPKCETestDeps(t, fake, &openedURL))

	flow, startErr := provider.StartLogin(context.Background(), LoginRequest{})
	if startErr != nil {
		t.Fatalf("PKCE StartLogin 失败: %v", startErr)
	}
	if state := flow.State(); state != LoginStateAwaitingUser {
		t.Fatalf("期望等待授权，实际 %s", state)
	}
	challenge := flow.Challenge()
	if challenge.UserCode != "" {
		t.Fatalf("PKCE 流程不应有设备码，实际 %q", challenge.UserCode)
	}
	redirectURI, stateParam, codeChallenge, method := microsoftPKCEAuthorizeParams(t, challenge)
	if method != microsoftPKCEChallengeMethod {
		t.Fatalf("code_challenge_method 期望 S256，实际 %q", method)
	}
	if codeChallenge == "" {
		t.Fatal("授权地址缺少 code_challenge")
	}
	if openedURL != challenge.VerificationURI {
		t.Fatal("自动打开浏览器的地址应与 challenge.VerificationURI 一致")
	}

	// 默认方式不应调用设备码端点。
	if deviceCode, _, _, _, _, _ := fake.counts(); deviceCode != 0 {
		t.Fatalf("PKCE 默认方式不应请求设备码端点，实际 %d 次", deviceCode)
	}

	if status := microsoftPKCESimulateBrowser(t, redirectURI, url.Values{
		"code":  {"test-auth-code"},
		"state": {stateParam},
	}); status != http.StatusOK {
		t.Fatalf("回跳响应状态期望 200，实际 %d", status)
	}

	outcome, pollErr := flow.Poll(context.Background())
	if pollErr != nil {
		t.Fatalf("Poll 失败: %v", pollErr)
	}
	if outcome.State != LoginStateSucceeded {
		t.Fatalf("期望登录成功，实际 %s（%s / %s）", outcome.State, outcome.ErrorCode, outcome.ErrorMessage)
	}
	if outcome.Account == nil || outcome.Credentials == nil {
		t.Fatal("成功结果必须带账号与凭证")
	}
	if outcome.Account.Kind != KindMicrosoft || outcome.Account.ProviderID != microsoftProviderID {
		t.Fatalf("账号来源字段不正确: %+v", outcome.Account)
	}
	if outcome.Account.Profile.Name == "" || outcome.Account.Profile.ID == "" {
		t.Fatal("账号档案缺少名字或 UUID")
	}
	if outcome.Credentials.AccessToken == "" || outcome.Credentials.RefreshToken == "" {
		t.Fatal("凭证缺少 access/refresh token")
	}

	// 校验换码请求：grant_type / code / redirect_uri / code_verifier 与 PKCE challenge 匹配。
	form := fake.tokenForm()
	if form.Get("grant_type") != "authorization_code" {
		t.Fatalf("换码请求 grant_type 不正确: %q", form.Get("grant_type"))
	}
	if form.Get("code") != "test-auth-code" {
		t.Fatalf("换码请求 code 不正确: %q", form.Get("code"))
	}
	if form.Get("redirect_uri") != redirectURI {
		t.Fatalf("换码请求 redirect_uri 与授权地址不一致: %q vs %q", form.Get("redirect_uri"), redirectURI)
	}
	if form.Get("client_id") != microsoftTestClientID {
		t.Fatalf("换码请求 client_id 不正确: %q", form.Get("client_id"))
	}
	verifier := form.Get("code_verifier")
	if verifier == "" {
		t.Fatal("换码请求缺少 code_verifier")
	}
	if microsoftPKCEChallenge(verifier) != codeChallenge {
		t.Fatal("code_verifier 与授权地址中的 code_challenge 不匹配")
	}
}

// TestMicrosoftPKCEDenied 验证用户在浏览器拒绝授权。
func TestMicrosoftPKCEDenied(t *testing.T) {
	fake := newMicrosoftTestServer(t)
	var openedURL string
	provider := NewMicrosoftProvider(microsoftPKCETestDeps(t, fake, &openedURL))

	flow, startErr := provider.StartLogin(context.Background(), LoginRequest{})
	if startErr != nil {
		t.Fatalf("PKCE StartLogin 失败: %v", startErr)
	}
	redirectURI, stateParam, _, _ := microsoftPKCEAuthorizeParams(t, flow.Challenge())

	microsoftPKCESimulateBrowser(t, redirectURI, url.Values{
		"error": {"access_denied"},
		"state": {stateParam},
	})

	outcome, pollErr := flow.Poll(context.Background())
	if pollErr != nil {
		t.Fatalf("Poll 不应返回流程性错误: %v", pollErr)
	}
	if outcome.State != LoginStateFailed {
		t.Fatalf("期望失败终态，实际 %s", outcome.State)
	}
	if outcome.ErrorCode != ErrorCodeLoginDeclined {
		t.Fatalf("期望错误码 %s，实际 %s", ErrorCodeLoginDeclined, outcome.ErrorCode)
	}
	if _, _, xbl, _, _, _ := fake.counts(); xbl != 0 {
		t.Fatal("用户拒绝后不应继续 XBL 链路")
	}

	// 终态重复 Poll 幂等。
	again, againErr := flow.Poll(context.Background())
	if againErr != nil || again.State != LoginStateFailed || again.ErrorCode != ErrorCodeLoginDeclined {
		t.Fatalf("终态 Poll 应幂等，实际 %+v / %v", again, againErr)
	}
}

// TestMicrosoftPKCEStateMismatchRejected 验证 state 不匹配的回跳被拒绝且不影响流程。
func TestMicrosoftPKCEStateMismatchRejected(t *testing.T) {
	fake := newMicrosoftTestServer(t)
	var openedURL string
	provider := NewMicrosoftProvider(microsoftPKCETestDeps(t, fake, &openedURL))

	flow, startErr := provider.StartLogin(context.Background(), LoginRequest{})
	if startErr != nil {
		t.Fatalf("PKCE StartLogin 失败: %v", startErr)
	}
	redirectURI, stateParam, _, _ := microsoftPKCEAuthorizeParams(t, flow.Challenge())

	status := microsoftPKCESimulateBrowser(t, redirectURI, url.Values{
		"code":  {"attacker-code"},
		"state": {"wrong-state"},
	})
	if status != http.StatusBadRequest {
		t.Fatalf("state 不匹配期望 400，实际 %d", status)
	}

	outcome, pollErr := flow.Poll(context.Background())
	if pollErr != nil {
		t.Fatalf("Poll 失败: %v", pollErr)
	}
	if outcome.State != LoginStateAwaitingUser {
		t.Fatalf("state 不匹配不应推进流程，实际 %s", outcome.State)
	}
	if _, _, token, _, _, _ := fake.counts(); token != 0 {
		t.Fatal("state 不匹配的 code 不应触发换码")
	}

	// 正确的回跳仍然可用，证明错误请求没有破坏流程。
	microsoftPKCESimulateBrowser(t, redirectURI, url.Values{
		"code":  {"good-code"},
		"state": {stateParam},
	})
	done, doneErr := flow.Poll(context.Background())
	if doneErr != nil || done.State != LoginStateSucceeded {
		t.Fatalf("正确回跳应成功，实际 %s / %v", done.State, doneErr)
	}
}

// TestMicrosoftPKCEExpired 验证等待超时后返回「登录已超时」。
func TestMicrosoftPKCEExpired(t *testing.T) {
	fake := newMicrosoftTestServer(t)
	var openedURL string
	clock := newMutableClock(microsoftTestNow())
	deps := microsoftPKCETestDeps(t, fake, &openedURL)
	deps.Now = clock.Now
	provider := NewMicrosoftProvider(deps)

	flow, startErr := provider.StartLogin(context.Background(), LoginRequest{})
	if startErr != nil {
		t.Fatalf("PKCE StartLogin 失败: %v", startErr)
	}
	if flow.State() != LoginStateAwaitingUser {
		t.Fatalf("期望等待授权，实际 %s", flow.State())
	}

	clock.Advance(microsoftPKCEExpiry + time.Second)
	outcome, pollErr := flow.Poll(context.Background())
	if pollErr != nil {
		t.Fatalf("Poll 不应返回流程性错误: %v", pollErr)
	}
	if outcome.State != LoginStateFailed || outcome.ErrorCode != ErrorCodeLoginExpired {
		t.Fatalf("期望超时失败，实际 %+v", outcome)
	}
}

// TestMicrosoftPKCEInvalidGrant 验证换码时 invalid_grant 给出重新登录指引。
func TestMicrosoftPKCEInvalidGrant(t *testing.T) {
	fake := newMicrosoftTestServer(t)
	var openedURL string
	fake.onToken = func(w http.ResponseWriter, _ *http.Request, _ int) bool {
		fake.writeJSON(w, http.StatusBadRequest, map[string]any{
			"error":             "invalid_grant",
			"error_description": "AADSTS70008: The provided authorization code has expired.",
			"error_codes":       []int{70008},
		})
		return true
	}
	provider := NewMicrosoftProvider(microsoftPKCETestDeps(t, fake, &openedURL))

	flow, startErr := provider.StartLogin(context.Background(), LoginRequest{})
	if startErr != nil {
		t.Fatalf("PKCE StartLogin 失败: %v", startErr)
	}
	redirectURI, stateParam, _, _ := microsoftPKCEAuthorizeParams(t, flow.Challenge())
	microsoftPKCESimulateBrowser(t, redirectURI, url.Values{
		"code":  {"expired-code"},
		"state": {stateParam},
	})

	outcome, pollErr := flow.Poll(context.Background())
	if pollErr != nil {
		t.Fatalf("Poll 不应返回流程性错误: %v", pollErr)
	}
	if outcome.State != LoginStateFailed {
		t.Fatalf("期望失败终态，实际 %s", outcome.State)
	}
	if outcome.ErrorCode != ErrorCodeAuth {
		t.Fatalf("invalid_grant 期望错误码 %s，实际 %s", ErrorCodeAuth, outcome.ErrorCode)
	}
	if !strings.Contains(outcome.ErrorMessage, "重新登录") {
		t.Fatalf("提示应引导重新登录，实际 %q", outcome.ErrorMessage)
	}
}

// TestMicrosoftPKCECancel 验证取消后状态为 cancelled 且重复 Poll/取消幂等。
func TestMicrosoftPKCECancel(t *testing.T) {
	fake := newMicrosoftTestServer(t)
	var openedURL string
	provider := NewMicrosoftProvider(microsoftPKCETestDeps(t, fake, &openedURL))

	flow, startErr := provider.StartLogin(context.Background(), LoginRequest{})
	if startErr != nil {
		t.Fatalf("PKCE StartLogin 失败: %v", startErr)
	}
	flow.Cancel()
	if state := flow.State(); state != LoginStateCancelled {
		t.Fatalf("取消后期望 cancelled，实际 %s", state)
	}
	flow.Cancel() // 幂等

	outcome, pollErr := flow.Poll(context.Background())
	if pollErr != nil {
		t.Fatalf("取消后 Poll 不应返回错误: %v", pollErr)
	}
	if outcome.State != LoginStateCancelled {
		t.Fatalf("取消后 Poll 期望 cancelled，实际 %s", outcome.State)
	}
}

// TestMicrosoftLoginModeDeviceCodeFallback 验证显式指定设备码时走设备码端点。
func TestMicrosoftLoginModeDeviceCodeFallback(t *testing.T) {
	fake := newMicrosoftTestServer(t)
	var openedURL string
	provider := NewMicrosoftProvider(microsoftPKCETestDeps(t, fake, &openedURL))

	flow, startErr := provider.StartLogin(context.Background(), LoginRequest{LoginMode: microsoftLoginModeDeviceCode})
	if startErr != nil {
		t.Fatalf("设备码回退 StartLogin 失败: %v", startErr)
	}
	challenge := flow.Challenge()
	if challenge.UserCode == "" {
		t.Fatal("设备码回退应返回 user_code")
	}
	if openedURL != "" {
		t.Fatal("设备码回退不应打开浏览器")
	}
	if deviceCode, _, _, _, _, _ := fake.counts(); deviceCode != 1 {
		t.Fatalf("设备码端点期望调用 1 次，实际 %d", deviceCode)
	}
	if flow.State() != LoginStateAwaitingUser {
		t.Fatalf("设备码流程期望等待授权，实际 %s", flow.State())
	}
}

// mutableClock 是可推进的测试时钟。
type mutableClock struct {
	mu  sync.Mutex
	now time.Time
}

// newMutableClock 构造固定起点的测试时钟。
func newMutableClock(start time.Time) *mutableClock {
	return &mutableClock{now: start}
}

// Now 返回当前测试时间。
func (c *mutableClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Advance 推进测试时间。
func (c *mutableClock) Advance(delta time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(delta)
}

// microsoftPKCESimulateBrowserBody 模拟浏览器回跳并返回状态码与页面内容。
func microsoftPKCESimulateBrowserBody(t *testing.T, redirectURI string, values url.Values) (int, string) {
	t.Helper()
	response, err := http.Get(redirectURI + "?" + values.Encode())
	if err != nil {
		t.Fatalf("模拟浏览器回跳失败: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	body, readErr := io.ReadAll(response.Body)
	if readErr != nil {
		t.Fatalf("读取回调页失败: %v", readErr)
	}
	return response.StatusCode, string(body)
}

// TestMicrosoftPKCECallbackPageBranding 验证本地回调页的品牌化内容。
//
// 回调页是用户登录完成后唯一会看到的「我们自己的页面」，必须：
//   - 明确标注 NineVoxel（不是仿冒的微软页面）；
//   - 成功/未完成/校验失败三种状态给出对应文案与状态码；
//   - 不依赖任何外部资源（离线可显示）。
func TestMicrosoftPKCECallbackPageBranding(t *testing.T) {
	fake := newMicrosoftTestServer(t)
	var openedURL string
	provider := NewMicrosoftProvider(microsoftPKCETestDeps(t, fake, &openedURL))

	// 1) 成功页
	flow, startErr := provider.StartLogin(context.Background(), LoginRequest{})
	if startErr != nil {
		t.Fatalf("PKCE StartLogin 失败: %v", startErr)
	}
	redirectURI, stateParam, _, _ := microsoftPKCEAuthorizeParams(t, flow.Challenge())
	status, body := microsoftPKCESimulateBrowserBody(t, redirectURI, url.Values{
		"code":  {"test-code"},
		"state": {stateParam},
	})
	flow.Cancel()
	if status != http.StatusOK {
		t.Fatalf("成功页状态期望 200，实际 %d", status)
	}
	for _, want := range []string{"NineVoxel", "Launcher", "登录成功", "Microsoft 授权", "可以安全关闭"} {
		if !strings.Contains(body, want) {
			t.Fatalf("成功页缺少 %q", want)
		}
	}
	if strings.Contains(body, "http://") || strings.Contains(body, "https://") {
		t.Fatal("回调页不应引用任何外部资源")
	}

	// 2) 用户拒绝页
	flowDenied, deniedErr := provider.StartLogin(context.Background(), LoginRequest{})
	if deniedErr != nil {
		t.Fatalf("PKCE StartLogin 失败: %v", deniedErr)
	}
	redirectURI, stateParam, _, _ = microsoftPKCEAuthorizeParams(t, flowDenied.Challenge())
	status, body = microsoftPKCESimulateBrowserBody(t, redirectURI, url.Values{
		"error": {"access_denied"},
		"state": {stateParam},
	})
	flowDenied.Cancel()
	if status != http.StatusOK {
		t.Fatalf("拒绝页状态期望 200，实际 %d", status)
	}
	for _, want := range []string{"NineVoxel", "登录未完成", "取消"} {
		if !strings.Contains(body, want) {
			t.Fatalf("拒绝页缺少 %q", want)
		}
	}

	// 3) state 校验失败页
	flowBadState, badStateErr := provider.StartLogin(context.Background(), LoginRequest{})
	if badStateErr != nil {
		t.Fatalf("PKCE StartLogin 失败: %v", badStateErr)
	}
	redirectURI, _, _, _ = microsoftPKCEAuthorizeParams(t, flowBadState.Challenge())
	status, body = microsoftPKCESimulateBrowserBody(t, redirectURI, url.Values{
		"code":  {"attacker-code"},
		"state": {"wrong-state"},
	})
	flowBadState.Cancel()
	if status != http.StatusBadRequest {
		t.Fatalf("state 校验失败页状态期望 400，实际 %d", status)
	}
	for _, want := range []string{"NineVoxel", "登录状态校验失败"} {
		if !strings.Contains(body, want) {
			t.Fatalf("校验失败页缺少 %q", want)
		}
	}
}
