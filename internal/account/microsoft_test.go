// 本文件是微软账号来源的离线协议测试。
//
// 所有端点都由 httptest 提供，测试**不联网**；断言信息只描述字段名，不打印
// 任何 token / device_code，避免测试输出泄漏凭证（即便是测试用假值）。
package account

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// 测试用固定值：只用于离线协议断言。
const (
	microsoftTestClientID       = "test-client-id"
	microsoftTestDeviceCode     = "test-device-code"
	microsoftTestUserCode       = "ABCD-EFGH"
	microsoftTestVerification   = "https://microsoft.com/devicelogin"
	microsoftTestMSAccessToken  = "test-ms-access-token"
	microsoftTestMSRefreshToken = "test-ms-refresh-token"
	microsoftTestXBLUHS         = "test-xbl-uhs"
	microsoftTestXSTSuhs        = "test-xsts-uhs"
	microsoftTestXSTSToken      = "test-xsts-token"
	microsoftTestMCAccessToken  = "test-mc-access-token"
	microsoftTestXUID           = "2535412345678901"
	microsoftTestProfileID      = "069A79F4-44E9-4726-A5BE-FCA90E38AAF5"
	microsoftTestNormalizedID   = "069a79f444e94726a5befca90e38aaf5"
	microsoftTestProfileName    = "Notch"
	microsoftTestSkinURL        = "https://textures.minecraft.net/texture/skin"
	microsoftTestCapeURL        = "https://textures.minecraft.net/texture/cape"
	microsoftTestFixedUnix      = int64(1700000000)
)

// microsoftTestNow 返回固定时钟，保证过期时间断言可复现。
func microsoftTestNow() time.Time { return time.Unix(microsoftTestFixedUnix, 0) }

// microsoftTestJWT 构造一个未签名（测试用）JWT，payload 为给定 claims。
func microsoftTestJWT(claims map[string]any) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payload, err := json.Marshal(claims)
	if err != nil {
		panic(err)
	}
	return header + "." + base64.RawURLEncoding.EncodeToString(payload) + ".test-signature"
}

// microsoftTestHook 是某个端点的可替换处理逻辑；call 从 1 开始计数。
// 返回 true 表示已自行写入响应。
type microsoftTestHook func(w http.ResponseWriter, r *http.Request, call int) bool

// microsoftTestServer 是一个覆盖微软登录全链路的假服务端。
type microsoftTestServer struct {
	t      *testing.T
	server *httptest.Server

	mu                 sync.Mutex
	deviceCodeCalls    int
	tokenCalls         int
	xblCalls           int
	xstsCalls          int
	mcCalls            int
	profileCalls       int
	lastDeviceCodeForm url.Values
	lastTokenForm      url.Values
	lastXBLBody        map[string]any
	lastXSTSBody       map[string]any
	lastMCBody         map[string]any
	lastProfileAuth    string

	onDeviceCode microsoftTestHook
	onToken      microsoftTestHook
	onXBL        microsoftTestHook
	onXSTS       microsoftTestHook
	onMC         microsoftTestHook
	onProfile    microsoftTestHook
}

// newMicrosoftTestServer 启动假服务端并在测试结束时关闭。
func newMicrosoftTestServer(t *testing.T) *microsoftTestServer {
	t.Helper()
	fake := &microsoftTestServer{t: t}
	fake.server = httptest.NewServer(fake)
	t.Cleanup(fake.server.Close)
	return fake
}

// ServeHTTP 按路径分发到各协议端点。
func (s *microsoftTestServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/devicecode":
		_ = r.ParseForm()
		s.mu.Lock()
		s.deviceCodeCalls++
		call := s.deviceCodeCalls
		s.lastDeviceCodeForm = microsoftTestCloneForm(r.PostForm)
		hook := s.onDeviceCode
		s.mu.Unlock()
		if hook != nil && hook(w, r, call) {
			return
		}
		s.writeJSON(w, http.StatusOK, map[string]any{
			"device_code":      microsoftTestDeviceCode,
			"user_code":        microsoftTestUserCode,
			"verification_uri": microsoftTestVerification,
			"expires_in":       900,
			"interval":         2,
			"message":          "要登录，请在浏览器中打开验证地址并输入用户码。",
		})
	case "/token":
		_ = r.ParseForm()
		s.mu.Lock()
		s.tokenCalls++
		call := s.tokenCalls
		s.lastTokenForm = microsoftTestCloneForm(r.PostForm)
		hook := s.onToken
		s.mu.Unlock()
		if hook != nil && hook(w, r, call) {
			return
		}
		s.writeJSON(w, http.StatusOK, map[string]any{
			"token_type":    "Bearer",
			"expires_in":    3600,
			"access_token":  microsoftTestMSAccessToken,
			"refresh_token": microsoftTestMSRefreshToken,
		})
	case "/xbl":
		body := microsoftTestReadJSONBody(r)
		s.mu.Lock()
		s.xblCalls++
		call := s.xblCalls
		s.lastXBLBody = body
		hook := s.onXBL
		s.mu.Unlock()
		if hook != nil && hook(w, r, call) {
			return
		}
		s.writeJSON(w, http.StatusOK, map[string]any{
			"Token": microsoftTestJWT(map[string]any{
				"xid": microsoftTestXUID,
				"uhs": microsoftTestXBLUHS,
			}),
			"DisplayClaims": map[string]any{
				"xui": []any{map[string]any{"uhs": microsoftTestXBLUHS}},
			},
		})
	case "/xsts":
		body := microsoftTestReadJSONBody(r)
		s.mu.Lock()
		s.xstsCalls++
		call := s.xstsCalls
		s.lastXSTSBody = body
		hook := s.onXSTS
		s.mu.Unlock()
		if hook != nil && hook(w, r, call) {
			return
		}
		s.writeJSON(w, http.StatusOK, map[string]any{
			"Token": microsoftTestXSTSToken,
			"DisplayClaims": map[string]any{
				"xui": []any{map[string]any{"uhs": microsoftTestXSTSuhs}},
			},
		})
	case "/mc/login":
		body := microsoftTestReadJSONBody(r)
		s.mu.Lock()
		s.mcCalls++
		call := s.mcCalls
		s.lastMCBody = body
		hook := s.onMC
		s.mu.Unlock()
		if hook != nil && hook(w, r, call) {
			return
		}
		s.writeJSON(w, http.StatusOK, map[string]any{
			"username":     microsoftTestProfileName,
			"token_type":   "Bearer",
			"expires_in":   86400,
			"access_token": microsoftTestMCAccessToken,
		})
	case "/mc/profile":
		s.mu.Lock()
		s.profileCalls++
		call := s.profileCalls
		s.lastProfileAuth = r.Header.Get("Authorization")
		hook := s.onProfile
		s.mu.Unlock()
		if hook != nil && hook(w, r, call) {
			return
		}
		s.writeJSON(w, http.StatusOK, map[string]any{
			"id":   microsoftTestProfileID,
			"name": microsoftTestProfileName,
			"skins": []any{
				map[string]any{
					"id":      "skin-inactive",
					"state":   "INACTIVE",
					"url":     "https://textures.minecraft.net/texture/old-skin",
					"variant": "CLASSIC",
				},
				map[string]any{
					"id":      "skin-active",
					"state":   "ACTIVE",
					"url":     microsoftTestSkinURL,
					"variant": "SLIM",
				},
			},
			"capes": []any{
				map[string]any{
					"id":    "cape-inactive",
					"state": "INACTIVE",
					"url":   "https://textures.minecraft.net/texture/old-cape",
				},
				map[string]any{
					"id":    "cape-active",
					"state": "ACTIVE",
					"url":   microsoftTestCapeURL,
				},
			},
		})
	default:
		http.NotFound(w, r)
	}
}

// writeJSON 写出 JSON 响应。
func (s *microsoftTestServer) writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

// deps 构造指向假服务端的 Deps。
func (s *microsoftTestServer) deps() Deps {
	return Deps{
		HTTP:      s.server.Client(),
		Now:       microsoftTestNow,
		UserAgent: "NineVoxelTest/0.1",
		Microsoft: MicrosoftConfig{
			ClientID: microsoftTestClientID,
			Tenant:   DefaultMicrosoftTenant,
			Endpoints: MicrosoftEndpoints{
				DeviceCodeURL:       s.server.URL + "/devicecode",
				TokenURL:            s.server.URL + "/token",
				XBLURL:              s.server.URL + "/xbl",
				XSTSURL:             s.server.URL + "/xsts",
				MinecraftLoginURL:   s.server.URL + "/mc/login",
				MinecraftProfileURL: s.server.URL + "/mc/profile",
				EntitlementsURL:     s.server.URL + "/mc/entitlements",
			},
		},
	}
}

// counts 返回各端点被调用的次数。
func (s *microsoftTestServer) counts() (deviceCode, token, xbl, xsts, minecraft, profile int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.deviceCodeCalls, s.tokenCalls, s.xblCalls, s.xstsCalls, s.mcCalls, s.profileCalls
}

// totalCalls 返回所有端点调用次数之和。
func (s *microsoftTestServer) totalCalls() int {
	deviceCode, token, xbl, xsts, minecraft, profile := s.counts()
	return deviceCode + token + xbl + xsts + minecraft + profile
}

func (s *microsoftTestServer) deviceCodeForm() url.Values {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastDeviceCodeForm
}

func (s *microsoftTestServer) tokenForm() url.Values {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastTokenForm
}

func (s *microsoftTestServer) xblBody() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastXBLBody
}

func (s *microsoftTestServer) xstsBody() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastXSTSBody
}

func (s *microsoftTestServer) mcBody() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastMCBody
}

func (s *microsoftTestServer) profileAuth() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastProfileAuth
}

// microsoftTestReadJSONBody 读取并解析请求体（失败时返回空 map）。
func microsoftTestReadJSONBody(r *http.Request) map[string]any {
	decoded := map[string]any{}
	if r.Body == nil {
		return decoded
	}
	_ = json.NewDecoder(r.Body).Decode(&decoded)
	return decoded
}

// microsoftTestCloneForm 复制表单，避免后续请求覆盖已保存的值。
func microsoftTestCloneForm(form url.Values) url.Values {
	if form == nil {
		return nil
	}
	clone := make(url.Values, len(form))
	for key, values := range form {
		clone[key] = append([]string(nil), values...)
	}
	return clone
}

// microsoftTestStartLogin 开始一次登录流程（失败即终止测试）。
func microsoftTestStartLogin(t *testing.T, provider Provider) LoginFlow {
	t.Helper()
	// 旧用例全部针对设备码流程；PKCE 是默认方式，这里显式回退到设备码。
	// （PKCE 的测试在 microsoft_pkce_test.go，注入 OpenURL 后完全离线。）
	flow, flowErr := provider.StartLogin(context.Background(), LoginRequest{LoginMode: microsoftLoginModeDeviceCode})
	if flowErr != nil {
		t.Fatalf("StartLogin failed: %v", flowErr)
	}
	if flow == nil {
		t.Fatal("StartLogin returned nil flow")
	}
	return flow
}

// microsoftTestCompleteLogin 走完一次成功登录并返回流程与结果。
func microsoftTestCompleteLogin(t *testing.T, provider Provider) (LoginFlow, LoginOutcome) {
	t.Helper()
	flow := microsoftTestStartLogin(t, provider)
	outcome, pollErr := flow.Poll(context.Background())
	if pollErr != nil {
		t.Fatalf("Poll failed: %v", pollErr)
	}
	if outcome.State != LoginStateSucceeded {
		t.Fatalf("unexpected login state: %s", outcome.State)
	}
	return flow, outcome
}

// TestMicrosoftProviderInfo 校验来源静态描述与能力声明。
func TestMicrosoftProviderInfo(t *testing.T) {
	provider := NewMicrosoftProvider(Deps{})
	info := provider.Info()
	if info.Kind != KindMicrosoft {
		t.Fatalf("unexpected kind: %s", info.Kind)
	}
	if info.DisplayName != KindMicrosoft.DisplayName() {
		t.Errorf("unexpected display name: %s", info.DisplayName)
	}
	if !info.Implemented {
		t.Error("microsoft provider must be implemented")
	}
	if strings.TrimSpace(info.Description) == "" {
		t.Error("description must not be empty")
	}
	capabilities := info.Capabilities
	if !capabilities.Interactive {
		t.Error("microsoft login must be interactive")
	}
	if !capabilities.Refresh {
		t.Error("microsoft login must support refresh")
	}
	if !capabilities.Validate {
		t.Error("microsoft login must support validate")
	}
	if capabilities.SkinUpload {
		t.Error("skin upload is not supported in phase one")
	}
	if capabilities.MultipleProfiles {
		t.Error("microsoft account has exactly one profile")
	}
}

// TestMicrosoftDefaultEndpoints 校验端点零值回落与租户拼接。
func TestMicrosoftDefaultEndpoints(t *testing.T) {
	defaults := microsoftEndpointsOrDefault(MicrosoftConfig{})
	expected := map[string]string{
		"devicecode": "https://login.microsoftonline.com/consumers/oauth2/v2.0/devicecode",
		"token":      "https://login.microsoftonline.com/consumers/oauth2/v2.0/token",
		"xbl":        "https://user.auth.xboxlive.com/user/authenticate",
		"xsts":       "https://xsts.auth.xboxlive.com/xsts/authorize",
		"mclogin":    "https://api.minecraftservices.com/authentication/login_with_xbox",
		"mcprofile":  "https://api.minecraftservices.com/minecraft/profile",
		"entitle":    "https://api.minecraftservices.com/entitlements/mcstore",
	}
	actual := map[string]string{
		"devicecode": defaults.DeviceCodeURL,
		"token":      defaults.TokenURL,
		"xbl":        defaults.XBLURL,
		"xsts":       defaults.XSTSURL,
		"mclogin":    defaults.MinecraftLoginURL,
		"mcprofile":  defaults.MinecraftProfileURL,
		"entitle":    defaults.EntitlementsURL,
	}
	for name, want := range expected {
		if actual[name] != want {
			t.Errorf("unexpected default endpoint %s", name)
		}
	}

	tenant := microsoftEndpointsOrDefault(MicrosoftConfig{Tenant: "contoso"})
	if tenant.DeviceCodeURL != "https://login.microsoftonline.com/contoso/oauth2/v2.0/devicecode" {
		t.Error("device code URL must use configured tenant")
	}
	if tenant.TokenURL != "https://login.microsoftonline.com/contoso/oauth2/v2.0/token" {
		t.Error("token URL must use configured tenant")
	}

	custom := microsoftEndpointsOrDefault(MicrosoftConfig{
		Endpoints: MicrosoftEndpoints{XBLURL: "https://xbl.test/authenticate"},
	})
	if custom.XBLURL != "https://xbl.test/authenticate" {
		t.Error("injected endpoint must be preserved")
	}
	if custom.XSTSURL != defaults.XSTSURL {
		t.Error("missing endpoint must fall back to default")
	}
}

// TestMicrosoftLoginSuccess 覆盖完整成功链路与所有协议请求格式。
func TestMicrosoftLoginSuccess(t *testing.T) {
	server := newMicrosoftTestServer(t)
	provider := NewMicrosoftProvider(server.deps())
	flow := microsoftTestStartLogin(t, provider)

	// 1) 设备码 challenge。
	challenge := flow.Challenge()
	if challenge.UserCode != microsoftTestUserCode {
		t.Error("unexpected user code")
	}
	if challenge.VerificationURI != microsoftTestVerification {
		t.Error("unexpected verification URI")
	}
	if challenge.Message == "" {
		t.Error("challenge message must not be empty")
	}
	if challenge.IntervalSeconds != 2 {
		t.Errorf("unexpected interval seconds: %d", challenge.IntervalSeconds)
	}
	wantExpiresAt := microsoftTestNow().Add(900 * time.Second).UnixMilli()
	if challenge.ExpiresAtMs != wantExpiresAt {
		t.Error("unexpected challenge expiry")
	}
	if flow.State() != LoginStateAwaitingUser {
		t.Errorf("unexpected initial state: %s", flow.State())
	}
	if flow.ID() == "" {
		t.Error("flow id must not be empty")
	}
	if flow.Kind() != KindMicrosoft {
		t.Errorf("unexpected flow kind: %s", flow.Kind())
	}

	// 2) 设备码申请请求格式。
	deviceCodeForm := server.deviceCodeForm()
	if deviceCodeForm == nil {
		t.Fatal("device code request was not captured")
	}
	if deviceCodeForm.Get("client_id") != microsoftTestClientID {
		t.Error("unexpected device code client_id")
	}
	if deviceCodeForm.Get("scope") != microsoftScope {
		t.Error("unexpected device code scope")
	}

	// 3) 轮询成功并走完整条链路。
	outcome, pollErr := flow.Poll(context.Background())
	if pollErr != nil {
		t.Fatalf("Poll failed: %v", pollErr)
	}
	if outcome.State != LoginStateSucceeded {
		t.Fatalf("unexpected login state: %s", outcome.State)
	}
	if flow.State() != LoginStateSucceeded {
		t.Error("flow state must be succeeded after poll")
	}
	if len(flow.Profiles()) != 0 {
		t.Error("microsoft flow must not expose multiple profiles")
	}

	// 4) 令牌轮询请求格式。
	tokenForm := server.tokenForm()
	if tokenForm.Get("grant_type") != microsoftDeviceCodeGrant {
		t.Error("unexpected token grant_type")
	}
	if tokenForm.Get("client_id") != microsoftTestClientID {
		t.Error("unexpected token client_id")
	}
	if tokenForm.Get("device_code") != microsoftTestDeviceCode {
		t.Error("unexpected token device_code")
	}

	// 5) XBL 请求格式。
	xblBody := server.xblBody()
	properties, _ := xblBody["Properties"].(map[string]any)
	if properties == nil {
		t.Fatal("XBL request properties missing")
	}
	if properties["AuthMethod"] != "RPS" {
		t.Error("unexpected XBL AuthMethod")
	}
	if properties["SiteName"] != "user.auth.xboxlive.com" {
		t.Error("unexpected XBL SiteName")
	}
	if properties["RpsTicket"] != "d="+microsoftTestMSAccessToken {
		t.Error("unexpected XBL RpsTicket")
	}
	if xblBody["RelyingParty"] != "http://auth.xboxlive.com" {
		t.Error("unexpected XBL RelyingParty")
	}
	if xblBody["TokenType"] != "JWT" {
		t.Error("unexpected XBL TokenType")
	}

	// 6) XSTS 请求格式。
	xstsBody := server.xstsBody()
	xstsProperties, _ := xstsBody["Properties"].(map[string]any)
	if xstsProperties == nil {
		t.Fatal("XSTS request properties missing")
	}
	if xstsProperties["SandboxId"] != "RETAIL" {
		t.Error("unexpected XSTS SandboxId")
	}
	userTokens, _ := xstsProperties["UserTokens"].([]any)
	if len(userTokens) != 1 {
		t.Fatal("XSTS UserTokens must contain exactly one token")
	}
	expectedXBLToken := microsoftTestJWT(map[string]any{
		"xid": microsoftTestXUID,
		"uhs": microsoftTestXBLUHS,
	})
	if userTokens[0] != expectedXBLToken {
		t.Error("unexpected XSTS UserTokens value")
	}
	if xstsBody["RelyingParty"] != "rp://api.minecraftservices.com/" {
		t.Error("unexpected XSTS RelyingParty")
	}

	// 7) Minecraft 登录请求格式。
	mcBody := server.mcBody()
	if mcBody["identityToken"] != "XBL3.0 x="+microsoftTestXSTSuhs+";"+microsoftTestXSTSToken {
		t.Error("unexpected Minecraft identityToken")
	}

	// 8) 档案请求带 Bearer。
	if server.profileAuth() != "Bearer "+microsoftTestMCAccessToken {
		t.Error("unexpected profile Authorization header")
	}

	// 9) 账号摘要。
	account := outcome.Account
	if account == nil {
		t.Fatal("outcome account is nil")
	}
	if account.Kind != KindMicrosoft {
		t.Errorf("unexpected account kind: %s", account.Kind)
	}
	if account.ProviderID != "microsoft" {
		t.Errorf("unexpected provider id: %s", account.ProviderID)
	}
	if account.DisplayName != microsoftTestProfileName {
		t.Errorf("unexpected display name: %s", account.DisplayName)
	}
	if account.ID != "" {
		t.Error("account id must be filled by the service layer")
	}
	if account.Profile.ID != microsoftTestNormalizedID {
		t.Errorf("unexpected profile id: %s", account.Profile.ID)
	}
	if account.Profile.Name != microsoftTestProfileName {
		t.Errorf("unexpected profile name: %s", account.Profile.Name)
	}
	if account.Profile.SkinURL != microsoftTestSkinURL {
		t.Error("unexpected skin URL")
	}
	if account.Profile.SkinModel != "slim" {
		t.Errorf("unexpected skin model: %s", account.Profile.SkinModel)
	}
	if account.Profile.CapeURL != microsoftTestCapeURL {
		t.Error("unexpected cape URL")
	}

	// 10) 凭证。
	credentials := outcome.Credentials
	if credentials == nil {
		t.Fatal("outcome credentials is nil")
	}
	if credentials.AccessToken != microsoftTestMCAccessToken {
		t.Error("unexpected minecraft access token")
	}
	if credentials.RefreshToken != microsoftTestMSRefreshToken {
		t.Error("unexpected microsoft refresh token")
	}
	if credentials.UserType != UserTypeMSA {
		t.Errorf("unexpected user type: %s", credentials.UserType)
	}
	if credentials.XUID != microsoftTestXUID {
		t.Errorf("unexpected xuid: %s", credentials.XUID)
	}
	wantCredentialExpiry := microsoftTestNow().Add(86400 * time.Second).UnixMilli()
	if credentials.ExpiresAtMs != wantCredentialExpiry {
		t.Error("unexpected credential expiry")
	}

	// 11) 每个端点恰好调用一次。
	deviceCodeCalls, tokenCalls, xblCalls, xstsCalls, mcCalls, profileCalls := server.counts()
	if deviceCodeCalls != 1 || tokenCalls != 1 || xblCalls != 1 || xstsCalls != 1 || mcCalls != 1 || profileCalls != 1 {
		t.Errorf("unexpected call counts: devicecode=%d token=%d xbl=%d xsts=%d mc=%d profile=%d",
			deviceCodeCalls, tokenCalls, xblCalls, xstsCalls, mcCalls, profileCalls)
	}

	// 12) 终态 Poll 幂等：不再发起网络请求，Cancel 也不改变终态。
	before := server.totalCalls()
	again, againErr := flow.Poll(context.Background())
	if againErr != nil {
		t.Fatalf("terminal Poll returned error: %v", againErr)
	}
	if again.State != LoginStateSucceeded {
		t.Error("terminal Poll must keep succeeded state")
	}
	if server.totalCalls() != before {
		t.Error("terminal Poll must not issue network requests")
	}
	flow.Cancel()
	if flow.State() != LoginStateSucceeded {
		t.Error("Cancel must not change an already succeeded flow")
	}
}

// TestMicrosoftLoginWithoutXUID 校验 XBL JWT 无法解析 XUID 时登录仍然成功。
func TestMicrosoftLoginWithoutXUID(t *testing.T) {
	server := newMicrosoftTestServer(t)
	server.onXBL = func(w http.ResponseWriter, r *http.Request, _ int) bool {
		server.writeJSON(w, http.StatusOK, map[string]any{
			"Token": microsoftTestJWT(map[string]any{"uhs": microsoftTestXBLUHS}),
			"DisplayClaims": map[string]any{
				"xui": []any{map[string]any{"uhs": microsoftTestXBLUHS}},
			},
		})
		return true
	}
	provider := NewMicrosoftProvider(server.deps())
	_, outcome := microsoftTestCompleteLogin(t, provider)
	if outcome.Credentials == nil {
		t.Fatal("outcome credentials is nil")
	}
	if outcome.Credentials.XUID != "" {
		t.Errorf("xuid must be empty when the token has no xid claim, got %q", outcome.Credentials.XUID)
	}
}

// TestMicrosoftLoginPendingThenSuccess 覆盖 authorization_pending 后再次轮询成功。
func TestMicrosoftLoginPendingThenSuccess(t *testing.T) {
	server := newMicrosoftTestServer(t)
	server.onToken = func(w http.ResponseWriter, r *http.Request, call int) bool {
		if call == 1 {
			server.writeJSON(w, http.StatusBadRequest, map[string]any{
				"error":             "authorization_pending",
				"error_description": "The authorization request is still pending.",
			})
			return true
		}
		return false
	}
	provider := NewMicrosoftProvider(server.deps())
	flow := microsoftTestStartLogin(t, provider)

	first, firstErr := flow.Poll(context.Background())
	if firstErr != nil {
		t.Fatalf("first Poll returned error: %v", firstErr)
	}
	if first.State != LoginStateAwaitingUser {
		t.Fatalf("unexpected state after pending: %s", first.State)
	}
	if flow.State() != LoginStateAwaitingUser {
		t.Error("flow must stay awaiting_user while pending")
	}

	second, secondErr := flow.Poll(context.Background())
	if secondErr != nil {
		t.Fatalf("second Poll returned error: %v", secondErr)
	}
	if second.State != LoginStateSucceeded {
		t.Fatalf("unexpected state after success: %s", second.State)
	}
	if _, tokenCalls, _, _, _, _ := server.counts(); tokenCalls != 2 {
		t.Errorf("unexpected token poll count: %d", tokenCalls)
	}
}

// TestMicrosoftLoginSlowDown 覆盖 slow_down 把轮询间隔增加 5 秒。
func TestMicrosoftLoginSlowDown(t *testing.T) {
	server := newMicrosoftTestServer(t)
	server.onToken = func(w http.ResponseWriter, r *http.Request, call int) bool {
		if call == 1 {
			server.writeJSON(w, http.StatusBadRequest, map[string]any{"error": "slow_down"})
			return true
		}
		return false
	}
	provider := NewMicrosoftProvider(server.deps())
	flow := microsoftTestStartLogin(t, provider)
	if flow.Challenge().IntervalSeconds != 2 {
		t.Errorf("unexpected initial interval: %d", flow.Challenge().IntervalSeconds)
	}

	first, firstErr := flow.Poll(context.Background())
	if firstErr != nil {
		t.Fatalf("Poll returned error: %v", firstErr)
	}
	if first.State != LoginStateAwaitingUser {
		t.Fatalf("unexpected state after slow_down: %s", first.State)
	}
	if flow.Challenge().IntervalSeconds != 7 {
		t.Errorf("slow_down must add 5 seconds, got %d", flow.Challenge().IntervalSeconds)
	}
	if first.Challenge.IntervalSeconds != 7 {
		t.Errorf("outcome challenge must carry the new interval, got %d", first.Challenge.IntervalSeconds)
	}

	second, secondErr := flow.Poll(context.Background())
	if secondErr != nil {
		t.Fatalf("second Poll returned error: %v", secondErr)
	}
	if second.State != LoginStateSucceeded {
		t.Errorf("unexpected state after success: %s", second.State)
	}
}

// TestMicrosoftLoginDeclined 覆盖用户拒绝授权。
func TestMicrosoftLoginDeclined(t *testing.T) {
	server := newMicrosoftTestServer(t)
	server.onToken = func(w http.ResponseWriter, r *http.Request, _ int) bool {
		server.writeJSON(w, http.StatusBadRequest, map[string]any{
			"error":             "authorization_declined",
			"error_description": "The user declined the authorization request.",
		})
		return true
	}
	provider := NewMicrosoftProvider(server.deps())
	flow := microsoftTestStartLogin(t, provider)

	outcome, pollErr := flow.Poll(context.Background())
	if pollErr == nil {
		t.Fatal("expected declined error")
	}
	if pollErr.Code != ErrorCodeLoginDeclined {
		t.Errorf("unexpected error code: %s", pollErr.Code)
	}
	if pollErr.Message != "用户取消了授权" {
		t.Errorf("unexpected error message: %s", pollErr.Message)
	}
	if pollErr.Retryable {
		t.Error("declined login must not be retryable")
	}
	if outcome.State != LoginStateFailed {
		t.Errorf("unexpected outcome state: %s", outcome.State)
	}
	if outcome.ErrorCode != ErrorCodeLoginDeclined {
		t.Errorf("unexpected outcome error code: %s", outcome.ErrorCode)
	}
	if outcome.ErrorMessage != "用户取消了授权" {
		t.Errorf("unexpected outcome error message: %s", outcome.ErrorMessage)
	}
	if flow.State() != LoginStateFailed {
		t.Errorf("unexpected flow state: %s", flow.State())
	}

	// 脱敏：错误文案不得包含设备码或令牌。
	if strings.Contains(pollErr.Error(), microsoftTestDeviceCode) {
		t.Error("error message leaked device_code")
	}
	if strings.Contains(pollErr.Error(), microsoftTestMSAccessToken) {
		t.Error("error message leaked access token")
	}
	if strings.Contains(pollErr.Error(), "declined the authorization request") {
		t.Error("error message leaked server response body")
	}

	// 终态幂等：再次 Poll 返回同一错误且不再请求网络。
	_, againErr := flow.Poll(context.Background())
	if againErr == nil || againErr.Code != ErrorCodeLoginDeclined {
		t.Fatal("terminal Poll must return the same declined error")
	}
	if _, tokenCalls, _, _, _, _ := server.counts(); tokenCalls != 1 {
		t.Errorf("terminal Poll must not issue network requests, token calls: %d", tokenCalls)
	}
}

// TestMicrosoftLoginExpired 覆盖设备码过期。
func TestMicrosoftLoginExpired(t *testing.T) {
	server := newMicrosoftTestServer(t)
	server.onToken = func(w http.ResponseWriter, r *http.Request, _ int) bool {
		server.writeJSON(w, http.StatusBadRequest, map[string]any{"error": "expired_token"})
		return true
	}
	provider := NewMicrosoftProvider(server.deps())
	flow := microsoftTestStartLogin(t, provider)

	outcome, pollErr := flow.Poll(context.Background())
	if pollErr == nil {
		t.Fatal("expected expired error")
	}
	if pollErr.Code != ErrorCodeLoginExpired {
		t.Errorf("unexpected error code: %s", pollErr.Code)
	}
	if pollErr.Message != "登录已超时，请重新开始" {
		t.Errorf("unexpected error message: %s", pollErr.Message)
	}
	if outcome.State != LoginStateFailed {
		t.Errorf("unexpected outcome state: %s", outcome.State)
	}
	if outcome.ErrorCode != ErrorCodeLoginExpired {
		t.Errorf("unexpected outcome error code: %s", outcome.ErrorCode)
	}
}

// TestMicrosoftXSTSErrorMessages 覆盖 XSTS XErr 到中文文案的映射。
func TestMicrosoftXSTSErrorMessages(t *testing.T) {
	cases := []struct {
		name           string
		code           int64
		wantSubstrings []string
	}{
		{name: "banned", code: 2148916227, wantSubstrings: []string{"封禁"}},
		{name: "no-xbox-profile", code: 2148916233, wantSubstrings: []string{"Xbox 档案"}},
		{name: "region-unsupported", code: 2148916235, wantSubstrings: []string{"地区"}},
		{name: "adult-verification-236", code: 2148916236, wantSubstrings: []string{"成人验证"}},
		{name: "adult-verification-237", code: 2148916237, wantSubstrings: []string{"成人验证"}},
		{name: "child-account", code: 2148916238, wantSubstrings: []string{"儿童账号", "家庭组"}},
		{name: "unknown", code: 2148916299, wantSubstrings: []string{"XErr 2148916299"}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			server := newMicrosoftTestServer(t)
			server.onXSTS = func(w http.ResponseWriter, r *http.Request, _ int) bool {
				server.writeJSON(w, http.StatusUnauthorized, map[string]any{
					"Identity": "0",
					"XErr":     testCase.code,
					"Message":  "server detail must not leak",
					"Redirect": "https://xbox.com",
				})
				return true
			}
			provider := NewMicrosoftProvider(server.deps())
			flow := microsoftTestStartLogin(t, provider)
			outcome, pollErr := flow.Poll(context.Background())
			if pollErr == nil {
				t.Fatal("expected XSTS error")
			}
			if pollErr.Code != ErrorCodeLogin {
				t.Errorf("unexpected error code: %s", pollErr.Code)
			}
			if pollErr.Retryable {
				t.Error("XSTS authorization errors must not be retryable")
			}
			for _, want := range testCase.wantSubstrings {
				if !strings.Contains(pollErr.Message, want) {
					t.Errorf("error message %q does not contain %q", pollErr.Message, want)
				}
			}
			if strings.Contains(pollErr.Message, "server detail must not leak") {
				t.Error("error message leaked server response body")
			}
			if outcome.State != LoginStateFailed {
				t.Errorf("unexpected outcome state: %s", outcome.State)
			}
			if outcome.ErrorCode != ErrorCodeLogin {
				t.Errorf("unexpected outcome error code: %s", outcome.ErrorCode)
			}
		})
	}
}

// TestMicrosoftProfileMissing 覆盖 404 与 error=NOT_FOUND 两种「无档案」分支。
func TestMicrosoftProfileMissing(t *testing.T) {
	t.Run("http-404", func(t *testing.T) {
		server := newMicrosoftTestServer(t)
		server.onProfile = func(w http.ResponseWriter, r *http.Request, _ int) bool {
			server.writeJSON(w, http.StatusNotFound, map[string]any{
				"path":         "/minecraft/profile",
				"errorType":    "NOT_FOUND",
				"error":        "NOT_FOUND",
				"errorMessage": "The server has not found anything matching the request URI",
			})
			return true
		}
		provider := NewMicrosoftProvider(server.deps())
		flow := microsoftTestStartLogin(t, provider)
		outcome, pollErr := flow.Poll(context.Background())
		if pollErr == nil {
			t.Fatal("expected ownership error")
		}
		if pollErr.Code != ErrorCodeOwnership {
			t.Errorf("unexpected error code: %s", pollErr.Code)
		}
		if pollErr.Message != "该账号没有可用的 Minecraft 档案（未购买或未创建档案）" {
			t.Errorf("unexpected error message: %s", pollErr.Message)
		}
		if outcome.State != LoginStateFailed || outcome.ErrorCode != ErrorCodeOwnership {
			t.Errorf("unexpected outcome: state=%s code=%s", outcome.State, outcome.ErrorCode)
		}
	})

	t.Run("error-not-found", func(t *testing.T) {
		server := newMicrosoftTestServer(t)
		server.onProfile = func(w http.ResponseWriter, r *http.Request, _ int) bool {
			server.writeJSON(w, http.StatusOK, map[string]any{"error": "NOT_FOUND"})
			return true
		}
		provider := NewMicrosoftProvider(server.deps())
		flow := microsoftTestStartLogin(t, provider)
		_, pollErr := flow.Poll(context.Background())
		if pollErr == nil {
			t.Fatal("expected ownership error")
		}
		if pollErr.Code != ErrorCodeOwnership {
			t.Errorf("unexpected error code: %s", pollErr.Code)
		}
	})
}

// TestMicrosoftValidator 覆盖 Validator 的 200 / 401 / 403 / 5xx 分支。
func TestMicrosoftValidator(t *testing.T) {
	cases := []struct {
		name        string
		status      int
		wantValid   bool
		wantErrCode string
	}{
		{name: "ok", status: http.StatusOK, wantValid: true},
		{name: "unauthorized", status: http.StatusUnauthorized, wantValid: false},
		{name: "forbidden", status: http.StatusForbidden, wantValid: false},
		{name: "server-error", status: http.StatusInternalServerError, wantValid: false, wantErrCode: ErrorCodeHTTP},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			server := newMicrosoftTestServer(t)
			if testCase.status != http.StatusOK {
				status := testCase.status
				server.onProfile = func(w http.ResponseWriter, r *http.Request, _ int) bool {
					server.writeJSON(w, status, map[string]any{"error": "ERR"})
					return true
				}
			}
			provider := NewMicrosoftProvider(server.deps())
			validator, ok := provider.(Validator)
			if !ok {
				t.Fatal("microsoft provider must implement Validator")
			}
			valid, validateErr := validator.Validate(context.Background(), Credentials{
				AccessToken: microsoftTestMCAccessToken,
			})
			if testCase.wantErrCode == "" {
				if validateErr != nil {
					t.Fatalf("Validate returned unexpected error: %v", validateErr)
				}
				if valid != testCase.wantValid {
					t.Errorf("unexpected validate result: %t", valid)
				}
			} else {
				if validateErr == nil {
					t.Fatal("Validate must return an error for unexpected status")
				}
				if validateErr.Code != testCase.wantErrCode {
					t.Errorf("unexpected error code: %s", validateErr.Code)
				}
			}
			if server.profileAuth() != "Bearer "+microsoftTestMCAccessToken {
				t.Error("unexpected profile Authorization header")
			}
		})
	}

	t.Run("empty-token", func(t *testing.T) {
		server := newMicrosoftTestServer(t)
		provider := NewMicrosoftProvider(server.deps())
		validator, ok := provider.(Validator)
		if !ok {
			t.Fatal("microsoft provider must implement Validator")
		}
		valid, validateErr := validator.Validate(context.Background(), Credentials{})
		if validateErr != nil {
			t.Fatalf("Validate returned unexpected error: %v", validateErr)
		}
		if valid {
			t.Error("empty token must be invalid")
		}
		if server.totalCalls() != 0 {
			t.Error("empty token must not issue network requests")
		}
	})
}

// TestMicrosoftRefresher 覆盖 refresh_token 续期后重跑 XBL → XSTS → Minecraft。
func TestMicrosoftRefresher(t *testing.T) {
	server := newMicrosoftTestServer(t)
	rotatedRefreshToken := microsoftTestMSRefreshToken + "-rotated"
	refreshedMCToken := microsoftTestMCAccessToken + "-refreshed"
	server.onToken = func(w http.ResponseWriter, r *http.Request, _ int) bool {
		server.writeJSON(w, http.StatusOK, map[string]any{
			"token_type":    "Bearer",
			"expires_in":    3600,
			"access_token":  microsoftTestMSAccessToken,
			"refresh_token": rotatedRefreshToken,
		})
		return true
	}
	server.onMC = func(w http.ResponseWriter, r *http.Request, _ int) bool {
		server.writeJSON(w, http.StatusOK, map[string]any{
			"token_type":   "Bearer",
			"expires_in":   43200,
			"access_token": refreshedMCToken,
		})
		return true
	}
	provider := NewMicrosoftProvider(server.deps())
	refresher, ok := provider.(Refresher)
	if !ok {
		t.Fatal("microsoft provider must implement Refresher")
	}

	credentials, refreshErr := refresher.Refresh(context.Background(), Credentials{
		AccessToken:  "stale-minecraft-token",
		RefreshToken: microsoftTestMSRefreshToken,
	}, Profile{})
	if refreshErr != nil {
		t.Fatalf("Refresh failed: %v", refreshErr)
	}
	if credentials.AccessToken != refreshedMCToken {
		t.Error("unexpected refreshed minecraft access token")
	}
	if credentials.RefreshToken != rotatedRefreshToken {
		t.Error("refresh token rotation must be persisted")
	}
	if credentials.UserType != UserTypeMSA {
		t.Errorf("unexpected user type: %s", credentials.UserType)
	}
	if credentials.XUID != microsoftTestXUID {
		t.Errorf("unexpected xuid: %s", credentials.XUID)
	}
	wantExpiry := microsoftTestNow().Add(43200 * time.Second).UnixMilli()
	if credentials.ExpiresAtMs != wantExpiry {
		t.Error("unexpected refreshed credential expiry")
	}

	// 续期请求格式。
	form := server.tokenForm()
	if form.Get("grant_type") != "refresh_token" {
		t.Error("unexpected refresh grant_type")
	}
	if form.Get("client_id") != microsoftTestClientID {
		t.Error("unexpected refresh client_id")
	}
	if form.Get("refresh_token") != microsoftTestMSRefreshToken {
		t.Error("unexpected refresh_token value")
	}

	// 必须重跑 XBL → XSTS → Minecraft，且不重复读取档案。
	deviceCodeCalls, tokenCalls, xblCalls, xstsCalls, mcCalls, profileCalls := server.counts()
	if deviceCodeCalls != 0 || tokenCalls != 1 || xblCalls != 1 || xstsCalls != 1 || mcCalls != 1 || profileCalls != 0 {
		t.Errorf("unexpected call counts: devicecode=%d token=%d xbl=%d xsts=%d mc=%d profile=%d",
			deviceCodeCalls, tokenCalls, xblCalls, xstsCalls, mcCalls, profileCalls)
	}

	// XBL / XSTS / Minecraft 的请求格式与登录链路一致。
	xblProperties, _ := server.xblBody()["Properties"].(map[string]any)
	if xblProperties == nil || xblProperties["RpsTicket"] != "d="+microsoftTestMSAccessToken {
		t.Error("refresh must reuse the XBL authenticate step")
	}
	xstsProperties, _ := server.xstsBody()["Properties"].(map[string]any)
	if xstsProperties == nil || xstsProperties["SandboxId"] != "RETAIL" {
		t.Error("refresh must reuse the XSTS authorize step")
	}
	if server.mcBody()["identityToken"] != "XBL3.0 x="+microsoftTestXSTSuhs+";"+microsoftTestXSTSToken {
		t.Error("refresh must reuse the Minecraft login step")
	}
}

// TestMicrosoftRefresherInvalidGrant 覆盖 refresh token 明确失效的分支。
func TestMicrosoftRefresherInvalidGrant(t *testing.T) {
	server := newMicrosoftTestServer(t)
	server.onToken = func(w http.ResponseWriter, r *http.Request, _ int) bool {
		server.writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_grant"})
		return true
	}
	provider := NewMicrosoftProvider(server.deps())
	refresher, ok := provider.(Refresher)
	if !ok {
		t.Fatal("microsoft provider must implement Refresher")
	}
	_, refreshErr := refresher.Refresh(context.Background(), Credentials{
		RefreshToken: microsoftTestMSRefreshToken,
	}, Profile{})
	if refreshErr == nil {
		t.Fatal("expected auth error for invalid_grant")
	}
	if refreshErr.Code != ErrorCodeAuth {
		t.Errorf("unexpected error code: %s", refreshErr.Code)
	}
	if refreshErr.Message != "登录状态已失效，请重新登录" {
		t.Errorf("unexpected error message: %s", refreshErr.Message)
	}
	deviceCodeCalls, _, xblCalls, xstsCalls, mcCalls, profileCalls := server.counts()
	if deviceCodeCalls != 0 || xblCalls != 0 || xstsCalls != 0 || mcCalls != 0 || profileCalls != 0 {
		t.Error("invalid refresh token must not continue the xbox chain")
	}
}

// TestMicrosoftRefresherEmptyToken 覆盖没有 refresh token 时直接判失效。
func TestMicrosoftRefresherEmptyToken(t *testing.T) {
	server := newMicrosoftTestServer(t)
	provider := NewMicrosoftProvider(server.deps())
	refresher, ok := provider.(Refresher)
	if !ok {
		t.Fatal("microsoft provider must implement Refresher")
	}
	_, refreshErr := refresher.Refresh(context.Background(), Credentials{}, Profile{})
	if refreshErr == nil {
		t.Fatal("expected auth error for empty refresh token")
	}
	if refreshErr.Code != ErrorCodeAuth {
		t.Errorf("unexpected error code: %s", refreshErr.Code)
	}
	if server.totalCalls() != 0 {
		t.Error("empty refresh token must not issue network requests")
	}
}

// TestMicrosoftDeviceCodeFailure 覆盖设备码申请失败。
func TestMicrosoftDeviceCodeFailure(t *testing.T) {
	server := newMicrosoftTestServer(t)
	server.onDeviceCode = func(w http.ResponseWriter, r *http.Request, _ int) bool {
		server.writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "server_error"})
		return true
	}
	provider := NewMicrosoftProvider(server.deps())
	flow, flowErr := provider.StartLogin(context.Background(), LoginRequest{LoginMode: microsoftLoginModeDeviceCode})
	if flowErr == nil {
		t.Fatal("expected device code error")
	}
	if flow != nil {
		t.Error("flow must be nil when device code request fails")
	}
	if flowErr.Code != ErrorCodeLogin {
		t.Errorf("unexpected error code: %s", flowErr.Code)
	}
	if _, tokenCalls, _, _, _, _ := server.counts(); tokenCalls != 0 {
		t.Error("failed device code request must not poll the token endpoint")
	}
}

// TestMicrosoftPollCancelConcurrent 覆盖 Poll 与 Cancel 并发以及取消后的终态幂等。
func TestMicrosoftPollCancelConcurrent(t *testing.T) {
	server := newMicrosoftTestServer(t)
	server.onToken = func(w http.ResponseWriter, r *http.Request, _ int) bool {
		// 给并发 Cancel 留出与 Poll 重叠的窗口。
		time.Sleep(20 * time.Millisecond)
		server.writeJSON(w, http.StatusBadRequest, map[string]any{"error": "authorization_pending"})
		return true
	}
	provider := NewMicrosoftProvider(server.deps())
	flow := microsoftTestStartLogin(t, provider)

	var waitGroup sync.WaitGroup
	for i := 0; i < 8; i++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			_, _ = flow.Poll(context.Background())
		}()
	}
	flow.Cancel()
	flow.Cancel() // 幂等
	waitGroup.Wait()

	if flow.State() != LoginStateCancelled {
		t.Fatalf("unexpected flow state after cancel: %s", flow.State())
	}
	if _, tokenCalls, _, _, _, _ := server.counts(); tokenCalls > 1 {
		t.Errorf("concurrent Poll must issue at most one token request, got %d", tokenCalls)
	}

	// 取消后 Poll 幂等，且不再请求网络。
	before := server.totalCalls()
	outcome, pollErr := flow.Poll(context.Background())
	if pollErr != nil {
		t.Fatalf("cancelled flow Poll returned error: %v", pollErr)
	}
	if outcome.State != LoginStateCancelled {
		t.Errorf("unexpected cancelled outcome state: %s", outcome.State)
	}
	if server.totalCalls() != before {
		t.Error("cancelled flow Poll must not issue network requests")
	}
}

// TestMicrosoftLogout 校验 Logoutger 是本地无操作（微软公共客户端无注销端点）。
func TestMicrosoftLogout(t *testing.T) {
	server := newMicrosoftTestServer(t)
	provider := NewMicrosoftProvider(server.deps())
	logoutger, ok := provider.(Logoutger)
	if !ok {
		t.Fatal("microsoft provider must implement Logoutger")
	}
	if logoutErr := logoutger.Logout(context.Background(), Credentials{AccessToken: "token"}); logoutErr != nil {
		t.Fatalf("Logout must be a no-op, got error: %v", logoutErr)
	}
	if server.totalCalls() != 0 {
		t.Error("Logout must not issue network requests")
	}
}
