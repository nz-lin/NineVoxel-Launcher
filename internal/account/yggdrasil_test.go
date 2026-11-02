// 本文件是 Yggdrasil 来源的完全离线协议测试。
//
// 用 httptest 起一个假站点（metadata + authserver + sessionserver），通过
// Deps.HTTP 注入该站点的客户端，覆盖：ALI 发现（相对/绝对/回退/重定向）、
// 单档案登录与纹理、多档案选择、密码错误、refresh/validate/invalidate、
// API 根规范化、错误文案脱敏与并发 Poll。测试不联网、不依赖真实服务。
package account

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

const (
	yggdrasilTestAPIPath   = "/api/yggdrasil/"
	yggdrasilTestUsername  = "alice"
	yggdrasilTestPassword  = "secret-password"
	yggdrasilTestAccessTok = "access-token-1"

	yggdrasilTestProfileID    = "069a79f444e94726a5befca90e38aaf5"
	yggdrasilTestProfileHyph  = "069a79f4-44e9-4726-a5be-fca90e38aaf5"
	yggdrasilTestSecondID     = "7b3f2a1c4d5e6f708192a3b4c5d6e7f8"
	yggdrasilTestSecondIDHyph = "7b3f2a1c-4d5e-6f70-8192-a3b4c5d6e7f8"
	yggdrasilTestSkinURL      = "https://textures.example.com/skin/alice.png"
	yggdrasilTestCapeURL      = "https://textures.example.com/cape/alice.png"
)

// ---------------------------------------------------------------------------
// 假 Yggdrasil 站点
// ---------------------------------------------------------------------------

// yggdrasilTestSite 是一个可配置的假 authlib-injector 站点。
type yggdrasilTestSite struct {
	t      *testing.T
	server *httptest.Server

	mu     sync.Mutex
	counts map[string]int

	lastAuth       yggdrasilAuthenticateRequest
	lastRefresh    yggdrasilTestRefreshPayload
	lastValidate   yggdrasilTestTokenPayload
	lastInvalidate yggdrasilTestTokenPayload

	// 站点发现与元数据。
	apiPath        string
	aliLocation    string // 默认相对地址（相对输入 URL 解析）
	absoluteALI    bool   // 为 true 时 ALI 头返回绝对地址
	noALI          bool   // 为 true 时不返回 ALI 头（走「输入地址即 API 根」回退）
	rootRedirect   string // 非空时根路径 302 到该地址
	metadataStatus int
	metadataBody   string

	// 认证。
	username              string
	password              string
	authenticateStatus    int
	authenticateErrorBody string
	selectedProfile       *yggdrasilProfile
	availableProfiles     []yggdrasilProfile

	// 续期 / 校验 / 注销。
	refreshAccessToken     string
	refreshClientToken     string
	refreshProfileOverride *yggdrasilProfile
	refreshForbidden       map[string]bool
	refreshStatus          int
	validateStatus         int
	invalidateStatus       int

	// 纹理。
	profileName     string
	texturesValue   string
	texturesInvalid bool
}

// yggdrasilTestRefreshPayload 对应 refresh 请求体（测试侧宽松解析）。
type yggdrasilTestRefreshPayload struct {
	AccessToken     string            `json:"accessToken"`
	ClientToken     string            `json:"clientToken"`
	RequestUser     bool              `json:"requestUser"`
	SelectedProfile *yggdrasilProfile `json:"selectedProfile"`
}

// yggdrasilTestTokenPayload 对应 validate/invalidate 请求体。
type yggdrasilTestTokenPayload struct {
	AccessToken string `json:"accessToken"`
	ClientToken string `json:"clientToken"`
}

func newYggdrasilTestSite(t *testing.T) *yggdrasilTestSite {
	t.Helper()
	return newYggdrasilTestSiteWithTLS(t, false)
}

func newYggdrasilTestTLSSite(t *testing.T) *yggdrasilTestSite {
	t.Helper()
	return newYggdrasilTestSiteWithTLS(t, true)
}

func newYggdrasilTestSiteWithTLS(t *testing.T, useTLS bool) *yggdrasilTestSite {
	t.Helper()
	site := &yggdrasilTestSite{
		t:                      t,
		counts:                 make(map[string]int),
		apiPath:                yggdrasilTestAPIPath,
		aliLocation:            "api/yggdrasil/",
		username:               yggdrasilTestUsername,
		password:               yggdrasilTestPassword,
		refreshAccessToken:     "access-token-refreshed",
		refreshClientToken:     "client-token-refreshed",
		refreshForbidden:       make(map[string]bool),
		profileName:            "Alice",
		availableProfiles:      nil,
		authenticateStatus:     0,
		selectedProfile:        nil,
		metadataStatus:         0,
		metadataBody:           "",
		refreshProfileOverride: nil,
	}
	handler := http.HandlerFunc(site.handle)
	if useTLS {
		site.server = httptest.NewTLSServer(handler)
	} else {
		site.server = httptest.NewServer(handler)
	}
	t.Cleanup(site.server.Close)
	return site
}

// newProvider 构造指向本假站点的来源（复用 httptest 的客户端）。
func (s *yggdrasilTestSite) newProvider() Provider {
	return NewYggdrasilProvider(Deps{HTTP: s.server.Client(), UserAgent: "NineVoxelTest/1.0"})
}

// apiURL 是站点的规范化 API 根（以 / 结尾）。
func (s *yggdrasilTestSite) apiURL() string { return s.server.URL + s.apiPath }

func (s *yggdrasilTestSite) incr(name string) {
	s.mu.Lock()
	s.counts[name]++
	s.mu.Unlock()
}

func (s *yggdrasilTestSite) count(name string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.counts[name]
}

func (s *yggdrasilTestSite) authRequest() yggdrasilAuthenticateRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastAuth
}

func (s *yggdrasilTestSite) refreshRequest() yggdrasilTestRefreshPayload {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastRefresh
}

func (s *yggdrasilTestSite) validateRequest() yggdrasilTestTokenPayload {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastValidate
}

func (s *yggdrasilTestSite) invalidateRequest() yggdrasilTestTokenPayload {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastInvalidate
}

// setTextures 设置会话服务器的皮肤/披风纹理。
func (s *yggdrasilTestSite) setTextures(skinURL, model, capeURL string) {
	s.texturesValue = yggdrasilTestTexturesValue(skinURL, model, capeURL)
}

// handle 路由假站点请求。
func (s *yggdrasilTestSite) handle(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	switch {
	case path == "/":
		s.incr("root")
		if !s.noALI {
			if s.absoluteALI {
				w.Header().Set(authlibInjectorAPILocationHeader, s.apiURL())
			} else {
				w.Header().Set(authlibInjectorAPILocationHeader, s.aliLocation)
			}
			writeYggdrasilTestJSON(w, http.StatusOK, map[string]any{
				"meta": map[string]any{"serverName": "discovery"},
			})
			return
		}
		if s.rootRedirect != "" {
			http.Redirect(w, r, s.rootRedirect, http.StatusFound)
			return
		}
		s.writeMetadata(w)
	case path == s.apiPath:
		s.incr("metadata")
		s.writeMetadata(w)
	case path == s.apiPath+"authserver/authenticate":
		s.handleAuthenticate(w, r)
	case path == s.apiPath+"authserver/refresh":
		s.handleRefresh(w, r)
	case path == s.apiPath+"authserver/validate":
		s.handleValidate(w, r)
	case path == s.apiPath+"authserver/invalidate":
		s.handleInvalidate(w, r)
	case strings.HasPrefix(path, s.apiPath+"sessionserver/session/minecraft/profile/"):
		s.handleSessionProfile(w, path)
	default:
		http.NotFound(w, r)
	}
}

func (s *yggdrasilTestSite) writeMetadata(w http.ResponseWriter) {
	if s.metadataStatus != 0 {
		w.WriteHeader(s.metadataStatus)
		_, _ = w.Write([]byte(`{"error":"metadata unavailable"}`))
		return
	}
	if s.metadataBody != "" {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(s.metadataBody))
		return
	}
	writeYggdrasilTestJSON(w, http.StatusOK, map[string]any{
		"meta": map[string]any{
			"serverName":            "测试服",
			"implementationName":    "authlib-injector",
			"implementationVersion": "1.2.0",
			"feature":               map[string]any{"non_email_login": true},
		},
		"skinDomains": []string{"textures.example.com", "*.example.com"},
	})
}

func (s *yggdrasilTestSite) handleAuthenticate(w http.ResponseWriter, r *http.Request) {
	s.incr("authenticate")
	var payload yggdrasilAuthenticateRequest
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeYggdrasilTestJSON(w, http.StatusBadRequest, map[string]string{
			"error":        "IllegalArgumentException",
			"errorMessage": "bad request",
		})
		return
	}
	s.mu.Lock()
	s.lastAuth = payload
	s.mu.Unlock()
	if s.authenticateStatus != 0 {
		if s.authenticateErrorBody != "" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(s.authenticateStatus)
			_, _ = w.Write([]byte(s.authenticateErrorBody))
			return
		}
		writeYggdrasilTestJSON(w, s.authenticateStatus, map[string]string{
			"error":        "ForbiddenOperationException",
			"errorMessage": "Invalid credentials. Invalid username or password.",
		})
		return
	}
	if payload.Username != s.username || payload.Password != s.password {
		writeYggdrasilTestJSON(w, http.StatusForbidden, map[string]string{
			"error":        "ForbiddenOperationException",
			"errorMessage": "Invalid credentials. Invalid username or password.",
		})
		return
	}
	profiles := s.availableProfiles
	if profiles == nil {
		profiles = []yggdrasilProfile{}
	}
	response := map[string]any{
		"accessToken":       yggdrasilTestAccessTok,
		"clientToken":       payload.ClientToken,
		"availableProfiles": profiles,
	}
	if s.selectedProfile != nil {
		response["selectedProfile"] = s.selectedProfile
	}
	writeYggdrasilTestJSON(w, http.StatusOK, response)
}

func (s *yggdrasilTestSite) handleRefresh(w http.ResponseWriter, r *http.Request) {
	s.incr("refresh")
	var payload yggdrasilTestRefreshPayload
	_ = json.NewDecoder(r.Body).Decode(&payload)
	s.mu.Lock()
	s.lastRefresh = payload
	s.mu.Unlock()
	if s.refreshStatus != 0 {
		w.WriteHeader(s.refreshStatus)
		return
	}
	if s.refreshForbidden[payload.AccessToken] {
		writeYggdrasilTestJSON(w, http.StatusForbidden, map[string]string{
			"error":        "ForbiddenOperationException",
			"errorMessage": "Invalid token.",
		})
		return
	}
	response := map[string]any{
		"accessToken": s.refreshAccessToken,
		"clientToken": s.refreshClientToken,
	}
	if payload.SelectedProfile != nil {
		profile := *payload.SelectedProfile
		if s.refreshProfileOverride != nil {
			profile = *s.refreshProfileOverride
		}
		response["selectedProfile"] = profile
	}
	writeYggdrasilTestJSON(w, http.StatusOK, response)
}

func (s *yggdrasilTestSite) handleValidate(w http.ResponseWriter, r *http.Request) {
	s.incr("validate")
	var payload yggdrasilTestTokenPayload
	_ = json.NewDecoder(r.Body).Decode(&payload)
	s.mu.Lock()
	s.lastValidate = payload
	s.mu.Unlock()
	status := s.validateStatus
	if status == 0 {
		status = http.StatusNoContent
	}
	w.WriteHeader(status)
}

func (s *yggdrasilTestSite) handleInvalidate(w http.ResponseWriter, r *http.Request) {
	s.incr("invalidate")
	var payload yggdrasilTestTokenPayload
	_ = json.NewDecoder(r.Body).Decode(&payload)
	s.mu.Lock()
	s.lastInvalidate = payload
	s.mu.Unlock()
	status := s.invalidateStatus
	if status == 0 {
		status = http.StatusNoContent
	}
	w.WriteHeader(status)
}

func (s *yggdrasilTestSite) handleSessionProfile(w http.ResponseWriter, path string) {
	s.incr("textures")
	prefix := s.apiPath + "sessionserver/session/minecraft/profile/"
	id := strings.TrimPrefix(path, prefix)
	properties := make([]map[string]string, 0, 1)
	if s.texturesInvalid {
		properties = append(properties, map[string]string{"name": "textures", "value": "!!!not-base64!!!"})
	} else if s.texturesValue != "" {
		properties = append(properties, map[string]string{"name": "textures", "value": s.texturesValue})
	}
	writeYggdrasilTestJSON(w, http.StatusOK, map[string]any{
		"id":         id,
		"name":       s.profileName,
		"properties": properties,
	})
}

func writeYggdrasilTestJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

// yggdrasilTestTexturesValue 构造 base64 编码的 textures 属性值。
func yggdrasilTestTexturesValue(skinURL, model, capeURL string) string {
	textures := make(map[string]any)
	if skinURL != "" {
		skin := map[string]any{"url": skinURL}
		if model != "" {
			skin["metadata"] = map[string]string{"model": model}
		}
		textures["SKIN"] = skin
	}
	if capeURL != "" {
		textures["CAPE"] = map[string]any{"url": capeURL}
	}
	payload := map[string]any{
		"timestamp":   int64(1),
		"profileId":   yggdrasilTestProfileID,
		"profileName": "Alice",
		"textures":    textures,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	return base64.StdEncoding.EncodeToString(raw)
}

// ---------------------------------------------------------------------------
// 测试辅助
// ---------------------------------------------------------------------------

func startYggdrasilTestLogin(t *testing.T, provider Provider, apiURL, username, password string) LoginFlow {
	t.Helper()
	flow, aerr := provider.StartLogin(context.Background(), LoginRequest{
		APIURL:   apiURL,
		Username: username,
		Password: password,
	})
	if aerr != nil {
		t.Fatalf("StartLogin 失败: %v", aerr)
	}
	if flow == nil {
		t.Fatal("StartLogin 返回了 nil flow")
	}
	return flow
}

func pollYggdrasilTest(t *testing.T, flow LoginFlow) (LoginOutcome, *Error) {
	t.Helper()
	out, aerr := flow.Poll(context.Background())
	return out, aerr
}

func requireYggdrasilErrorCode(t *testing.T, aerr *Error, wantCode string) {
	t.Helper()
	if aerr == nil {
		t.Fatalf("期望错误码 %s，实际返回 nil", wantCode)
	}
	if aerr.Code != wantCode {
		t.Fatalf("期望错误码 %s，实际 %s（%s）", wantCode, aerr.Code, aerr.Message)
	}
}

func requireYggdrasilOutcome(t *testing.T, out LoginOutcome, wantState LoginState, wantCode string) {
	t.Helper()
	if out.State != wantState {
		t.Fatalf("期望状态 %s，实际 %s（code=%s message=%s）", wantState, out.State, out.ErrorCode, out.ErrorMessage)
	}
	if wantCode != "" && out.ErrorCode != wantCode {
		t.Fatalf("期望错误码 %s，实际 %s（%s）", wantCode, out.ErrorCode, out.ErrorMessage)
	}
}

// yggdrasilRefresher / yggdrasilValidator / yggdrasilLogoutger 用类型断言取可选能力，
// 断言失败时直接失败测试而不是 panic。
func yggdrasilRefresher(t *testing.T, provider Provider) Refresher {
	t.Helper()
	refresher, ok := provider.(Refresher)
	if !ok {
		t.Fatal("Provider 未实现 Refresher")
	}
	return refresher
}

func yggdrasilValidator(t *testing.T, provider Provider) Validator {
	t.Helper()
	validator, ok := provider.(Validator)
	if !ok {
		t.Fatal("Provider 未实现 Validator")
	}
	return validator
}

func yggdrasilLogoutger(t *testing.T, provider Provider) Logoutger {
	t.Helper()
	logoutger, ok := provider.(Logoutger)
	if !ok {
		t.Fatal("Provider 未实现 Logoutger")
	}
	return logoutger
}

// ---------------------------------------------------------------------------
// API 根规范化
// ---------------------------------------------------------------------------

func TestYggdrasilNormalizeAPIURL(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		want     string
		wantCode string
	}{
		{name: "去空格并补 https", input: " example.com ", want: "https://example.com/"},
		{name: "无 scheme 带路径", input: "example.com/api/yggdrasil", want: "https://example.com/api/yggdrasil/"},
		{name: "保留 http 与端口", input: "http://127.0.0.1:8080/api", want: "http://127.0.0.1:8080/api/"},
		{name: "已带结尾斜杠", input: "https://example.com:8443/", want: "https://example.com:8443/"},
		{name: "host 小写化", input: "HTTPS://Example.COM/Api", want: "https://example.com/Api/"},
		{name: "scheme 相对地址", input: "//example.com/api", want: "https://example.com/api/"},
		{name: "丢弃查询串与片段", input: "https://example.com/foo?x=1#frag", want: "https://example.com/foo/"},
		{name: "空地址", input: "   ", wantCode: ErrorCodeInput},
		{name: "非 http scheme", input: "ftp://example.com", wantCode: ErrorCodeInput},
		{name: "带用户名密码", input: "https://user:pass@example.com", wantCode: ErrorCodeInput},
		{name: "缺少 host", input: "https://", wantCode: ErrorCodeInput},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, aerr := normalizeYggdrasilAPIURL(tc.input)
			if tc.wantCode != "" {
				requireYggdrasilErrorCode(t, aerr, tc.wantCode)
				if got != "" {
					t.Fatalf("期望空地址，实际 %q", got)
				}
				return
			}
			if aerr != nil {
				t.Fatalf("期望成功，实际错误: %v", aerr)
			}
			if got != tc.want {
				t.Fatalf("规范化结果不匹配: 期望 %q，实际 %q", tc.want, got)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Provider 静态信息与输入校验
// ---------------------------------------------------------------------------

func TestYggdrasilProviderInfoAndInputValidation(t *testing.T) {
	provider := NewYggdrasilProvider(Deps{HTTP: http.DefaultClient})
	info := provider.Info()
	if info.Kind != KindYggdrasil {
		t.Fatalf("Kind 不匹配: %s", info.Kind)
	}
	if !info.Implemented {
		t.Fatal("Yggdrasil 必须是已实现来源")
	}
	if info.DisplayName != KindYggdrasil.DisplayName() {
		t.Fatalf("DisplayName 不匹配: %s", info.DisplayName)
	}
	if info.Description == "" {
		t.Fatal("Description 不能为空")
	}
	capabilities := info.Capabilities
	if capabilities.Interactive {
		t.Fatal("Yggdrasil 不需要交互式登录，Interactive 必须为 false")
	}
	if !capabilities.Refresh || !capabilities.Validate || !capabilities.Logout {
		t.Fatalf("Refresh/Validate/Logout 能力必须为 true: %+v", capabilities)
	}
	if !capabilities.MultipleProfiles {
		t.Fatal("Yggdrasil 支持多档案，MultipleProfiles 必须为 true")
	}
	if capabilities.SkinUpload {
		t.Fatal("首期不支持皮肤上传，SkinUpload 必须为 false")
	}
	if _, ok := provider.(SiteResolver); !ok {
		t.Fatal("Yggdrasil 必须实现 SiteResolver")
	}
	if _, ok := provider.(Refresher); !ok {
		t.Fatal("Yggdrasil 必须实现 Refresher")
	}
	if _, ok := provider.(Validator); !ok {
		t.Fatal("Yggdrasil 必须实现 Validator")
	}
	if _, ok := provider.(Logoutger); !ok {
		t.Fatal("Yggdrasil 必须实现 Logoutger")
	}
	if _, ok := provider.(YggdrasilSiteRefresher); !ok {
		t.Fatal("Yggdrasil 必须实现 YggdrasilSiteRefresher 扩展接口")
	}

	ctx := context.Background()
	validationCases := []struct {
		name string
		req  LoginRequest
	}{
		{name: "缺少 API 地址", req: LoginRequest{Username: "alice", Password: "pw"}},
		{name: "API 地址不合法", req: LoginRequest{APIURL: "ftp://example.com", Username: "alice", Password: "pw"}},
		{name: "缺少用户名", req: LoginRequest{APIURL: "example.com", Password: "pw"}},
		{name: "缺少密码", req: LoginRequest{APIURL: "example.com", Username: "alice"}},
	}
	for _, tc := range validationCases {
		t.Run(tc.name, func(t *testing.T) {
			flow, aerr := provider.StartLogin(ctx, tc.req)
			if flow != nil {
				t.Fatal("输入不合法时不应返回 flow")
			}
			requireYggdrasilErrorCode(t, aerr, ErrorCodeInput)
		})
	}

	t.Run("取消流程幂等", func(t *testing.T) {
		flow := startYggdrasilTestLogin(t, provider, "example.com", "alice", "pw")
		if flow.Kind() != KindYggdrasil {
			t.Fatalf("Kind 不匹配: %s", flow.Kind())
		}
		if flow.ID() == "" {
			t.Fatal("流程 ID 不能为空")
		}
		if flow.State() != LoginStatePending {
			t.Fatalf("初始状态应为 Pending，实际 %s", flow.State())
		}
		flow.Cancel()
		flow.Cancel()
		if flow.State() != LoginStateCancelled {
			t.Fatalf("取消后状态应为 Cancelled，实际 %s", flow.State())
		}
		out, aerr := pollYggdrasilTest(t, flow)
		if aerr != nil {
			t.Fatalf("已取消流程的 Poll 不应返回错误: %v", aerr)
		}
		requireYggdrasilOutcome(t, out, LoginStateCancelled, "")
		if out.Account != nil || out.Credentials != nil {
			t.Fatal("已取消流程不应带账号或凭证")
		}
	})
}

// ---------------------------------------------------------------------------
// 单档案登录
// ---------------------------------------------------------------------------

func TestYggdrasilLoginSingleProfile(t *testing.T) {
	site := newYggdrasilTestSite(t)
	site.selectedProfile = &yggdrasilProfile{ID: yggdrasilTestProfileHyph, Name: "Alice"}
	site.setTextures(yggdrasilTestSkinURL, "slim", yggdrasilTestCapeURL)
	provider := site.newProvider()

	// 用户输入不带结尾斜杠：由规范化补上；ALI 相对地址再定位到 API 根。
	flow := startYggdrasilTestLogin(t, provider, site.server.URL, yggdrasilTestUsername, yggdrasilTestPassword)
	if flow.State() != LoginStatePending {
		t.Fatalf("第一次 Poll 前状态应为 Pending，实际 %s", flow.State())
	}
	if flow.Challenge() != (LoginChallenge{}) {
		t.Fatal("Yggdrasil 不应产生登录挑战")
	}

	out, aerr := pollYggdrasilTest(t, flow)
	if aerr != nil {
		t.Fatalf("Poll 不应返回错误: %v", aerr)
	}
	requireYggdrasilOutcome(t, out, LoginStateSucceeded, "")
	if out.Account == nil || out.Credentials == nil {
		t.Fatal("成功时 Account/Credentials 必须非 nil")
	}

	account := out.Account
	if account.Kind != KindYggdrasil {
		t.Fatalf("Account.Kind 不匹配: %s", account.Kind)
	}
	if account.ProviderID != site.apiURL() {
		t.Fatalf("ProviderID 应为规范化后的 API 根 %q，实际 %q", site.apiURL(), account.ProviderID)
	}
	if account.DisplayName != "Alice" {
		t.Fatalf("DisplayName 不匹配: %s", account.DisplayName)
	}
	profile := account.Profile
	if profile.ID != yggdrasilTestProfileID {
		t.Fatalf("档案 ID 应被规范化为无连字符小写，实际 %q", profile.ID)
	}
	if profile.Name != "Alice" {
		t.Fatalf("档案名不匹配: %s", profile.Name)
	}
	if profile.SkinURL != yggdrasilTestSkinURL {
		t.Fatalf("SkinURL 不匹配: %s", profile.SkinURL)
	}
	if profile.SkinModel != "slim" {
		t.Fatalf("SkinModel 应为 slim，实际 %q", profile.SkinModel)
	}
	if profile.CapeURL != yggdrasilTestCapeURL {
		t.Fatalf("CapeURL 不匹配: %s", profile.CapeURL)
	}

	creds := out.Credentials
	if creds.AccessToken != yggdrasilTestAccessTok {
		t.Fatalf("AccessToken 不匹配: %s", creds.AccessToken)
	}
	if creds.RefreshToken != "" {
		t.Fatalf("Yggdrasil 不使用 RefreshToken，期望空，实际 %q", creds.RefreshToken)
	}
	if creds.ExpiresAtMs != 0 {
		t.Fatalf("Yggdrasil ExpiresAtMs 应为 0，实际 %d", creds.ExpiresAtMs)
	}
	if creds.UserType != UserTypeMojang {
		t.Fatalf("UserType 应为 mojang，实际 %q", creds.UserType)
	}

	// 认证请求：agent/requestUser/clientToken 符合规范，clientToken 无连字符 UUID。
	authRequest := site.authRequest()
	if authRequest.Username != yggdrasilTestUsername || authRequest.Password != yggdrasilTestPassword {
		t.Fatalf("认证请求用户名/密码不匹配: %+v", authRequest)
	}
	if !authRequest.RequestUser {
		t.Fatal("认证请求必须携带 requestUser=true")
	}
	if authRequest.Agent.Name != "Minecraft" || authRequest.Agent.Version != 1 {
		t.Fatalf("agent 不匹配: %+v", authRequest.Agent)
	}
	if len(authRequest.ClientToken) != 32 || strings.Contains(authRequest.ClientToken, "-") {
		t.Fatalf("clientToken 应为 32 位无连字符 UUID，实际 %q", authRequest.ClientToken)
	}
	// 服务端回显 clientToken，登录凭证应沿用回显值。
	if creds.ClientToken != authRequest.ClientToken {
		t.Fatalf("ClientToken 应回传服务端回显值 %q，实际 %q", authRequest.ClientToken, creds.ClientToken)
	}
	if site.count("authenticate") != 1 {
		t.Fatalf("认证请求应只发一次，实际 %d", site.count("authenticate"))
	}
	if site.count("textures") != 1 {
		t.Fatalf("纹理请求应只发一次，实际 %d", site.count("textures"))
	}

	// 终态幂等：重复 Poll 不重复请求网络。
	second, aerr := pollYggdrasilTest(t, flow)
	if aerr != nil {
		t.Fatalf("重复 Poll 不应返回错误: %v", aerr)
	}
	requireYggdrasilOutcome(t, second, LoginStateSucceeded, "")
	if second.Account == nil || second.Account.Profile.ID != yggdrasilTestProfileID {
		t.Fatal("重复 Poll 应返回同一终态账号")
	}
	if site.count("authenticate") != 1 || site.count("textures") != 1 {
		t.Fatalf("重复 Poll 不应重复请求网络: authenticate=%d textures=%d",
			site.count("authenticate"), site.count("textures"))
	}
	if flow.Profiles() != nil {
		t.Fatal("单档案成功后不应有可选择的档案列表")
	}
}

// TestYggdrasilLoginWithoutScheme 验证「用户输入不带 scheme」时自动补 https。
func TestYggdrasilLoginWithoutScheme(t *testing.T) {
	site := newYggdrasilTestTLSSite(t)
	site.selectedProfile = &yggdrasilProfile{ID: yggdrasilTestProfileID, Name: "Alice"}
	site.setTextures(yggdrasilTestSkinURL, "classic", yggdrasilTestCapeURL)
	provider := site.newProvider()

	rawInput := strings.TrimPrefix(site.server.URL, "https://") // 127.0.0.1:port
	if strings.Contains(rawInput, "://") {
		t.Fatalf("测试输入不应包含 scheme: %q", rawInput)
	}
	flow := startYggdrasilTestLogin(t, provider, rawInput, yggdrasilTestUsername, yggdrasilTestPassword)
	out, aerr := pollYggdrasilTest(t, flow)
	if aerr != nil {
		t.Fatalf("Poll 不应返回错误: %v", aerr)
	}
	requireYggdrasilOutcome(t, out, LoginStateSucceeded, "")
	if out.Account == nil {
		t.Fatal("成功时 Account 必须非 nil")
	}
	if out.Account.ProviderID != site.apiURL() {
		t.Fatalf("补 https 后 ProviderID 应为 %q，实际 %q", site.apiURL(), out.Account.ProviderID)
	}
}

// ---------------------------------------------------------------------------
// 多档案选择
// ---------------------------------------------------------------------------

func TestYggdrasilLoginMultipleProfiles(t *testing.T) {
	site := newYggdrasilTestSite(t)
	site.availableProfiles = []yggdrasilProfile{
		{ID: yggdrasilTestProfileHyph, Name: "Alice"},
		{ID: strings.ToUpper(yggdrasilTestSecondIDHyph), Name: "Bob"},
	}
	site.refreshProfileOverride = &yggdrasilProfile{ID: yggdrasilTestSecondID, Name: "Bob"}
	site.setTextures(yggdrasilTestSkinURL, "slim", yggdrasilTestCapeURL)
	provider := site.newProvider()

	flow := startYggdrasilTestLogin(t, provider, site.server.URL, yggdrasilTestUsername, yggdrasilTestPassword)
	out, aerr := pollYggdrasilTest(t, flow)
	if aerr != nil {
		t.Fatalf("Poll 不应返回错误: %v", aerr)
	}
	requireYggdrasilOutcome(t, out, LoginStateAwaitingProfile, "")
	if len(out.Profiles) != 2 {
		t.Fatalf("应有 2 个待选档案，实际 %d", len(out.Profiles))
	}
	if out.Profiles[0].ID != yggdrasilTestProfileID || out.Profiles[0].Name != "Alice" {
		t.Fatalf("第一个档案不匹配: %+v", out.Profiles[0])
	}
	if out.Profiles[1].ID != yggdrasilTestSecondID || out.Profiles[1].Name != "Bob" {
		t.Fatalf("第二个档案应被规范化为无连字符小写: %+v", out.Profiles[1])
	}
	if site.count("textures") != 0 {
		t.Fatal("待选档案阶段不应请求纹理")
	}
	if site.count("authenticate") != 1 {
		t.Fatalf("认证请求应只发一次，实际 %d", site.count("authenticate"))
	}

	// AwaitingProfile 幂等：重复 Poll 不推进状态、不重复认证。
	again, aerr := pollYggdrasilTest(t, flow)
	if aerr != nil {
		t.Fatalf("Poll 不应返回错误: %v", aerr)
	}
	requireYggdrasilOutcome(t, again, LoginStateAwaitingProfile, "")
	if site.count("authenticate") != 1 {
		t.Fatal("AwaitingProfile 状态下重复 Poll 不应重复认证")
	}

	selector, ok := flow.(ProfileSelector)
	if !ok {
		t.Fatal("Yggdrasil flow 必须实现 ProfileSelector")
	}

	// 非法档案标识与不属于该账号的档案都返回输入错误，并保持待选状态。
	for _, invalidID := range []string{"not-a-uuid", "ffffffffffffffffffffffffffffffff"} {
		selectOut, selectErr := selector.SelectProfile(context.Background(), invalidID)
		requireYggdrasilErrorCode(t, selectErr, ErrorCodeInput)
		requireYggdrasilOutcome(t, selectOut, LoginStateAwaitingProfile, "")
	}

	// 选择第二个档案（输入连字符大写形式，内部规范化后匹配）。
	selectOut, selectErr := selector.SelectProfile(context.Background(), strings.ToUpper(yggdrasilTestSecondIDHyph))
	if selectErr != nil {
		t.Fatalf("SelectProfile 不应返回错误: %v", selectErr)
	}
	requireYggdrasilOutcome(t, selectOut, LoginStateSucceeded, "")
	if selectOut.Account == nil || selectOut.Credentials == nil {
		t.Fatal("选择档案成功后 Account/Credentials 必须非 nil")
	}
	selected := selectOut.Account
	if selected.Profile.ID != yggdrasilTestSecondID || selected.Profile.Name != "Bob" {
		t.Fatalf("选中的档案不匹配: %+v", selected.Profile)
	}
	if selected.ProviderID != site.apiURL() {
		t.Fatalf("ProviderID 不匹配: %s", selected.ProviderID)
	}
	if selected.Profile.SkinURL != yggdrasilTestSkinURL || selected.Profile.SkinModel != "slim" ||
		selected.Profile.CapeURL != yggdrasilTestCapeURL {
		t.Fatalf("选择档案后应补齐纹理: %+v", selected.Profile)
	}
	if selectOut.Credentials.AccessToken != site.refreshAccessToken ||
		selectOut.Credentials.ClientToken != site.refreshClientToken {
		t.Fatalf("选择档案后应使用 refresh 返回的新凭证: %+v", *selectOut.Credentials)
	}
	refreshRequest := site.refreshRequest()
	if refreshRequest.SelectedProfile == nil || refreshRequest.SelectedProfile.ID != yggdrasilTestSecondID {
		t.Fatalf("refresh 请求应带选中的 selectedProfile: %+v", refreshRequest)
	}
	if !refreshRequest.RequestUser {
		t.Fatal("refresh 请求必须携带 requestUser=true")
	}
	if refreshRequest.AccessToken != yggdrasilTestAccessTok {
		t.Fatalf("refresh 请求应使用认证得到的 accessToken，实际 %q", refreshRequest.AccessToken)
	}
	if selectOut.Profiles != nil {
		t.Fatal("登录成功后不应再有待选档案")
	}

	// 已成功后再选择档案属于状态错误。
	lateOut, lateErr := selector.SelectProfile(context.Background(), yggdrasilTestProfileID)
	requireYggdrasilErrorCode(t, lateErr, ErrorCodeState)
	requireYggdrasilOutcome(t, lateOut, LoginStateSucceeded, "")
}

// TestYggdrasilSelectProfileInvalidToken 验证选择档案时 refresh 返回 403 的分支。
func TestYggdrasilSelectProfileInvalidToken(t *testing.T) {
	site := newYggdrasilTestSite(t)
	site.availableProfiles = []yggdrasilProfile{
		{ID: yggdrasilTestProfileID, Name: "Alice"},
		{ID: yggdrasilTestSecondID, Name: "Bob"},
	}
	site.refreshForbidden[yggdrasilTestAccessTok] = true
	provider := site.newProvider()

	flow := startYggdrasilTestLogin(t, provider, site.server.URL, yggdrasilTestUsername, yggdrasilTestPassword)
	out, aerr := pollYggdrasilTest(t, flow)
	if aerr != nil {
		t.Fatalf("Poll 不应返回错误: %v", aerr)
	}
	requireYggdrasilOutcome(t, out, LoginStateAwaitingProfile, "")

	selectOut, selectErr := flow.(ProfileSelector).SelectProfile(context.Background(), yggdrasilTestSecondID)
	if selectErr != nil {
		t.Fatalf("凭证失效属于终态失败，不应返回 transport 错误: %v", selectErr)
	}
	requireYggdrasilOutcome(t, selectOut, LoginStateFailed, ErrorCodeAuth)
	if selectOut.ErrorMessage != "登录状态已失效，请重新登录" {
		t.Fatalf("失败文案不匹配: %q", selectOut.ErrorMessage)
	}
	if flow.State() != LoginStateFailed {
		t.Fatalf("选择档案失败后应为终态 Failed，实际 %s", flow.State())
	}
}

// ---------------------------------------------------------------------------
// 失败分支
// ---------------------------------------------------------------------------

func TestYggdrasilLoginInvalidPassword(t *testing.T) {
	site := newYggdrasilTestSite(t)
	provider := site.newProvider()
	flow := startYggdrasilTestLogin(t, provider, site.server.URL, yggdrasilTestUsername, "wrong-password")

	out, aerr := pollYggdrasilTest(t, flow)
	if aerr != nil {
		t.Fatalf("凭证错误属于终态失败，不应返回 transport 错误: %v", aerr)
	}
	requireYggdrasilOutcome(t, out, LoginStateFailed, ErrorCodeAuth)
	if out.ErrorMessage != "账号或密码错误" {
		t.Fatalf("失败文案不匹配: %q", out.ErrorMessage)
	}
	if out.Account != nil || out.Credentials != nil {
		t.Fatal("失败时不应带账号或凭证")
	}
	if strings.Contains(out.ErrorMessage, "wrong-password") || strings.Contains(out.ErrorMessage, "access-token") {
		t.Fatalf("错误文案泄露了敏感信息: %q", out.ErrorMessage)
	}
	if site.count("authenticate") != 1 {
		t.Fatalf("认证请求应只发一次，实际 %d", site.count("authenticate"))
	}

	// 终态幂等：重复 Poll 返回同一失败，不重复请求。
	again, aerr := pollYggdrasilTest(t, flow)
	if aerr != nil {
		t.Fatalf("重复 Poll 不应返回错误: %v", aerr)
	}
	requireYggdrasilOutcome(t, again, LoginStateFailed, ErrorCodeAuth)
	if site.count("authenticate") != 1 {
		t.Fatalf("重复 Poll 不应重复请求认证，实际 %d", site.count("authenticate"))
	}
}

// TestYggdrasilLoginServerErrorTruncated 验证服务端错误文案的截断与去换行。
func TestYggdrasilLoginServerErrorTruncated(t *testing.T) {
	site := newYggdrasilTestSite(t)
	longMessage := strings.Repeat("x", 260) + "\n第二行不该出现"
	body, err := json.Marshal(map[string]string{
		"error":        "IllegalArgumentException",
		"errorMessage": longMessage,
	})
	if err != nil {
		t.Fatalf("构造错误体失败: %v", err)
	}
	site.authenticateStatus = http.StatusBadRequest
	site.authenticateErrorBody = string(body)
	provider := site.newProvider()
	flow := startYggdrasilTestLogin(t, provider, site.server.URL, yggdrasilTestUsername, yggdrasilTestPassword)

	out, aerr := pollYggdrasilTest(t, flow)
	if aerr != nil {
		t.Fatalf("Poll 不应返回错误: %v", aerr)
	}
	requireYggdrasilOutcome(t, out, LoginStateFailed, ErrorCodeLogin)
	message := out.ErrorMessage
	if strings.ContainsAny(message, "\r\n") {
		t.Fatalf("错误文案不应包含换行: %q", message)
	}
	if len([]rune(message)) > yggdrasilServerMessageLimit {
		t.Fatalf("错误文案应截断到 %d 字符，实际 %d", yggdrasilServerMessageLimit, len([]rune(message)))
	}
	if strings.Contains(message, yggdrasilTestPassword) {
		t.Fatalf("错误文案泄露了密码: %q", message)
	}
	if strings.Contains(message, "第二行") {
		t.Fatalf("错误文案截断位置不正确: %q", message)
	}
}

func TestYggdrasilLoginNoProfiles(t *testing.T) {
	site := newYggdrasilTestSite(t)
	// selectedProfile 与 availableProfiles 都为空。
	provider := site.newProvider()
	flow := startYggdrasilTestLogin(t, provider, site.server.URL, yggdrasilTestUsername, yggdrasilTestPassword)

	out, aerr := pollYggdrasilTest(t, flow)
	if aerr != nil {
		t.Fatalf("Poll 不应返回错误: %v", aerr)
	}
	requireYggdrasilOutcome(t, out, LoginStateFailed, ErrorCodeOwnership)
	if out.ErrorMessage != "该账号没有可用的 Minecraft 档案" {
		t.Fatalf("失败文案不匹配: %q", out.ErrorMessage)
	}
}

// TestYggdrasilTexturesBestEffort 验证纹理解析失败不影响登录。
func TestYggdrasilTexturesBestEffort(t *testing.T) {
	t.Run("纹理 base64 无法解码", func(t *testing.T) {
		site := newYggdrasilTestSite(t)
		site.selectedProfile = &yggdrasilProfile{ID: yggdrasilTestProfileID, Name: "Alice"}
		site.texturesInvalid = true
		provider := site.newProvider()
		flow := startYggdrasilTestLogin(t, provider, site.server.URL, yggdrasilTestUsername, yggdrasilTestPassword)
		out, aerr := pollYggdrasilTest(t, flow)
		if aerr != nil {
			t.Fatalf("纹理解析失败不应导致 Poll 返回错误: %v", aerr)
		}
		requireYggdrasilOutcome(t, out, LoginStateSucceeded, "")
		if out.Account.Profile.SkinURL != "" || out.Account.Profile.SkinModel != "" || out.Account.Profile.CapeURL != "" {
			t.Fatalf("纹理不可解析时应留空: %+v", out.Account.Profile)
		}
	})

	t.Run("服务端未提供纹理属性", func(t *testing.T) {
		site := newYggdrasilTestSite(t)
		site.selectedProfile = &yggdrasilProfile{ID: yggdrasilTestProfileID, Name: "Alice"}
		provider := site.newProvider()
		flow := startYggdrasilTestLogin(t, provider, site.server.URL, yggdrasilTestUsername, yggdrasilTestPassword)
		out, aerr := pollYggdrasilTest(t, flow)
		if aerr != nil {
			t.Fatalf("Poll 不应返回错误: %v", aerr)
		}
		requireYggdrasilOutcome(t, out, LoginStateSucceeded, "")
		if out.Account.Profile.SkinURL != "" || out.Account.Profile.CapeURL != "" {
			t.Fatalf("无纹理属性时应留空: %+v", out.Account.Profile)
		}
	})

	t.Run("无 metadata 时 model 回退 classic", func(t *testing.T) {
		site := newYggdrasilTestSite(t)
		site.selectedProfile = &yggdrasilProfile{ID: yggdrasilTestProfileID, Name: "Alice"}
		site.setTextures(yggdrasilTestSkinURL, "", "")
		provider := site.newProvider()
		flow := startYggdrasilTestLogin(t, provider, site.server.URL, yggdrasilTestUsername, yggdrasilTestPassword)
		out, aerr := pollYggdrasilTest(t, flow)
		if aerr != nil {
			t.Fatalf("Poll 不应返回错误: %v", aerr)
		}
		requireYggdrasilOutcome(t, out, LoginStateSucceeded, "")
		if out.Account.Profile.SkinModel != "classic" {
			t.Fatalf("缺少 metadata.model 时应为 classic，实际 %q", out.Account.Profile.SkinModel)
		}
	})
}

// ---------------------------------------------------------------------------
// 站点解析（ALI / 元数据）
// ---------------------------------------------------------------------------

func TestYggdrasilResolveSite(t *testing.T) {
	t.Run("ALI 相对地址", func(t *testing.T) {
		site := newYggdrasilTestSite(t)
		provider := site.newProvider()
		info, aerr := provider.(SiteResolver).ResolveSite(context.Background(), site.server.URL)
		if aerr != nil {
			t.Fatalf("ResolveSite 不应返回错误: %v", aerr)
		}
		if info.APIURL != site.apiURL() {
			t.Fatalf("APIURL 应为 %q，实际 %q", site.apiURL(), info.APIURL)
		}
		if info.ServerName != "测试服" {
			t.Fatalf("ServerName 不匹配: %q", info.ServerName)
		}
		if info.ImplementationName != "authlib-injector" {
			t.Fatalf("ImplementationName 不匹配: %q", info.ImplementationName)
		}
		if !info.NonEmailLogin {
			t.Fatal("NonEmailLogin 应为 true")
		}
		if len(info.SkinDomains) != 2 || info.SkinDomains[0] != "textures.example.com" {
			t.Fatalf("SkinDomains 不匹配: %+v", info.SkinDomains)
		}
		if !strings.HasSuffix(info.APIURL, "/") {
			t.Fatalf("APIURL 必须以 / 结尾: %q", info.APIURL)
		}
		if site.count("root") != 1 || site.count("metadata") != 1 {
			t.Fatalf("ALI 发现请求次数不匹配: root=%d metadata=%d", site.count("root"), site.count("metadata"))
		}
	})

	t.Run("ALI 绝对地址", func(t *testing.T) {
		site := newYggdrasilTestSite(t)
		site.absoluteALI = true
		provider := site.newProvider()
		info, aerr := provider.(SiteResolver).ResolveSite(context.Background(), site.server.URL)
		if aerr != nil {
			t.Fatalf("ResolveSite 不应返回错误: %v", aerr)
		}
		if info.APIURL != site.apiURL() {
			t.Fatalf("APIURL 应为 %q，实际 %q", site.apiURL(), info.APIURL)
		}
		if info.ServerName != "测试服" {
			t.Fatalf("ServerName 不匹配: %q", info.ServerName)
		}
	})

	t.Run("无 ALI 回退到输入地址", func(t *testing.T) {
		site := newYggdrasilTestSite(t)
		site.noALI = true
		provider := site.newProvider()
		info, aerr := provider.(SiteResolver).ResolveSite(context.Background(), site.server.URL)
		if aerr != nil {
			t.Fatalf("ResolveSite 不应返回错误: %v", aerr)
		}
		if info.APIURL != site.server.URL+"/" {
			t.Fatalf("无 ALI 时 APIURL 应回退为输入地址 %q，实际 %q", site.server.URL+"/", info.APIURL)
		}
		if info.ServerName != "测试服" {
			t.Fatalf("回退时应解析输入地址返回的元数据，实际 ServerName=%q", info.ServerName)
		}
		if site.count("metadata") != 0 {
			t.Fatalf("无 ALI 时不应再请求一次元数据，实际 %d", site.count("metadata"))
		}
	})

	t.Run("ALI 发现跟随重定向", func(t *testing.T) {
		site := newYggdrasilTestSite(t)
		site.noALI = true
		site.rootRedirect = site.apiPath
		provider := site.newProvider()
		info, aerr := provider.(SiteResolver).ResolveSite(context.Background(), site.server.URL)
		if aerr != nil {
			t.Fatalf("ResolveSite 不应返回错误: %v", aerr)
		}
		if info.APIURL != site.apiURL() {
			t.Fatalf("重定向后 APIURL 应取最终地址 %q，实际 %q", site.apiURL(), info.APIURL)
		}
		if info.ServerName != "测试服" {
			t.Fatalf("ServerName 不匹配: %q", info.ServerName)
		}
	})

	t.Run("元数据非 2xx", func(t *testing.T) {
		site := newYggdrasilTestSite(t)
		site.metadataStatus = http.StatusInternalServerError
		provider := site.newProvider()
		_, aerr := provider.(SiteResolver).ResolveSite(context.Background(), site.server.URL)
		requireYggdrasilErrorCode(t, aerr, ErrorCodeSite)
		if aerr.Retryable {
			t.Fatal("元数据非 2xx 属于站点错误，不应标记为可重试")
		}
	})

	t.Run("元数据非法 JSON", func(t *testing.T) {
		site := newYggdrasilTestSite(t)
		site.metadataBody = "<html>not json</html>"
		provider := site.newProvider()
		_, aerr := provider.(SiteResolver).ResolveSite(context.Background(), site.server.URL)
		requireYggdrasilErrorCode(t, aerr, ErrorCodeSite)
	})

	t.Run("元数据为空", func(t *testing.T) {
		site := newYggdrasilTestSite(t)
		site.metadataStatus = http.StatusNoContent
		provider := site.newProvider()
		_, aerr := provider.(SiteResolver).ResolveSite(context.Background(), site.server.URL)
		requireYggdrasilErrorCode(t, aerr, ErrorCodeSite)
	})

	t.Run("无法连接", func(t *testing.T) {
		site := newYggdrasilTestSite(t)
		site.server.Close()
		provider := site.newProvider()
		_, aerr := provider.(SiteResolver).ResolveSite(context.Background(), site.apiURL())
		requireYggdrasilErrorCode(t, aerr, ErrorCodeNetwork)
		if !aerr.Retryable {
			t.Fatal("网络错误必须标记为可重试")
		}
	})

	t.Run("输入地址非法", func(t *testing.T) {
		site := newYggdrasilTestSite(t)
		provider := site.newProvider()
		_, aerr := provider.(SiteResolver).ResolveSite(context.Background(), "ftp://example.com")
		requireYggdrasilErrorCode(t, aerr, ErrorCodeInput)
	})
}

// ---------------------------------------------------------------------------
// Validate
// ---------------------------------------------------------------------------

func TestYggdrasilValidate(t *testing.T) {
	site := newYggdrasilTestSite(t)
	site.selectedProfile = &yggdrasilProfile{ID: yggdrasilTestProfileID, Name: "Alice"}
	provider := site.newProvider()
	flow := startYggdrasilTestLogin(t, provider, site.server.URL, yggdrasilTestUsername, yggdrasilTestPassword)
	out, aerr := pollYggdrasilTest(t, flow)
	if aerr != nil {
		t.Fatalf("Poll 不应返回错误: %v", aerr)
	}
	requireYggdrasilOutcome(t, out, LoginStateSucceeded, "")
	creds := *out.Credentials

	valid, verr := yggdrasilValidator(t, provider).Validate(context.Background(), creds)
	if verr != nil {
		t.Fatalf("Validate 不应返回错误: %v", verr)
	}
	if !valid {
		t.Fatal("204 应判定为凭证有效")
	}
	validateRequest := site.validateRequest()
	if validateRequest.AccessToken != creds.AccessToken || validateRequest.ClientToken != creds.ClientToken {
		t.Fatalf("validate 请求凭证不匹配: %+v", validateRequest)
	}

	site.validateStatus = http.StatusForbidden
	valid, verr = yggdrasilValidator(t, provider).Validate(context.Background(), creds)
	if verr != nil {
		t.Fatalf("403 应返回 (false, nil)，实际错误: %v", verr)
	}
	if valid {
		t.Fatal("403 应判定为凭证失效")
	}

	site.validateStatus = http.StatusInternalServerError
	_, verr = yggdrasilValidator(t, provider).Validate(context.Background(), creds)
	requireYggdrasilErrorCode(t, verr, ErrorCodeHTTP)

	// 未登记的 token 无法定位站点，返回状态错误而不是伪造有效性。
	_, verr = yggdrasilValidator(t, provider).Validate(context.Background(), Credentials{AccessToken: "unknown-token"})
	requireYggdrasilErrorCode(t, verr, ErrorCodeState)
}

// TestYggdrasilValidateNetworkError 验证网络失败映射为可重试错误。
func TestYggdrasilValidateNetworkError(t *testing.T) {
	site := newYggdrasilTestSite(t)
	site.selectedProfile = &yggdrasilProfile{ID: yggdrasilTestProfileID, Name: "Alice"}
	provider := site.newProvider()
	flow := startYggdrasilTestLogin(t, provider, site.server.URL, yggdrasilTestUsername, yggdrasilTestPassword)
	out, aerr := pollYggdrasilTest(t, flow)
	if aerr != nil {
		t.Fatalf("Poll 不应返回错误: %v", aerr)
	}
	requireYggdrasilOutcome(t, out, LoginStateSucceeded, "")
	creds := *out.Credentials

	site.server.Close()
	_, verr := yggdrasilValidator(t, provider).Validate(context.Background(), creds)
	requireYggdrasilErrorCode(t, verr, ErrorCodeNetwork)
	if !verr.Retryable {
		t.Fatal("网络错误必须标记为可重试")
	}
}

// ---------------------------------------------------------------------------
// Refresh
// ---------------------------------------------------------------------------

func TestYggdrasilRefresh(t *testing.T) {
	site := newYggdrasilTestSite(t)
	site.selectedProfile = &yggdrasilProfile{ID: yggdrasilTestProfileID, Name: "Alice"}
	provider := site.newProvider()
	flow := startYggdrasilTestLogin(t, provider, site.server.URL, yggdrasilTestUsername, yggdrasilTestPassword)
	out, aerr := pollYggdrasilTest(t, flow)
	if aerr != nil {
		t.Fatalf("Poll 不应返回错误: %v", aerr)
	}
	requireYggdrasilOutcome(t, out, LoginStateSucceeded, "")
	original := *out.Credentials
	profile := out.Account.Profile

	refreshed, rerr := yggdrasilRefresher(t, provider).Refresh(context.Background(), original, profile)
	if rerr != nil {
		t.Fatalf("Refresh 不应返回错误: %v", rerr)
	}
	if refreshed.AccessToken != site.refreshAccessToken {
		t.Fatalf("刷新后 AccessToken 不匹配: %q", refreshed.AccessToken)
	}
	if refreshed.ClientToken != site.refreshClientToken {
		t.Fatalf("刷新后 ClientToken 不匹配: %q", refreshed.ClientToken)
	}
	if refreshed.RefreshToken != "" {
		t.Fatalf("Yggdrasil 不使用 RefreshToken，期望空，实际 %q", refreshed.RefreshToken)
	}
	if refreshed.UserType != UserTypeMojang {
		t.Fatalf("UserType 应为 mojang，实际 %q", refreshed.UserType)
	}
	if refreshed.ExpiresAtMs != 0 {
		t.Fatalf("ExpiresAtMs 应为 0，实际 %d", refreshed.ExpiresAtMs)
	}
	refreshRequest := site.refreshRequest()
	if refreshRequest.AccessToken != original.AccessToken || refreshRequest.ClientToken != original.ClientToken {
		t.Fatalf("refresh 请求凭证不匹配: %+v", refreshRequest)
	}
	if !refreshRequest.RequestUser {
		t.Fatal("refresh 请求必须携带 requestUser=true")
	}
	if refreshRequest.SelectedProfile != nil {
		t.Fatalf("普通续期不应携带 selectedProfile: %+v", refreshRequest.SelectedProfile)
	}

	// 新 token 应替换索引：Validate 使用新凭证仍可定位站点。
	valid, verr := yggdrasilValidator(t, provider).Validate(context.Background(), refreshed)
	if verr != nil || !valid {
		t.Fatalf("刷新后的凭证应可校验: valid=%v err=%v", valid, verr)
	}

	// 服务端明确拒绝（403 / Invalid token）→ 凭证失效终态。
	site.refreshForbidden[refreshed.AccessToken] = true
	_, rerr = yggdrasilRefresher(t, provider).Refresh(context.Background(), refreshed, profile)
	requireYggdrasilErrorCode(t, rerr, ErrorCodeAuth)
	if rerr.Message != "登录状态已失效，请重新登录" {
		t.Fatalf("失败文案不匹配: %q", rerr.Message)
	}

	// 非 2xx 的其他状态 → ErrorCodeHTTP。
	site.refreshStatus = http.StatusInternalServerError
	_, rerr = yggdrasilRefresher(t, provider).Refresh(context.Background(), refreshed, profile)
	requireYggdrasilErrorCode(t, rerr, ErrorCodeHTTP)

	// 网络不可达 → 可重试 ErrorCodeNetwork。
	site.server.Close()
	_, rerr = yggdrasilRefresher(t, provider).Refresh(context.Background(), refreshed, profile)
	requireYggdrasilErrorCode(t, rerr, ErrorCodeNetwork)
	if !rerr.Retryable {
		t.Fatal("续期的网络错误必须标记为可重试")
	}
}

// TestYggdrasilRefreshAtSite 验证跨进程重启场景：索引为空时由服务层传站点根。
func TestYggdrasilRefreshAtSite(t *testing.T) {
	site := newYggdrasilTestSite(t)
	site.selectedProfile = &yggdrasilProfile{ID: yggdrasilTestProfileID, Name: "Alice"}
	loginProvider := site.newProvider()
	flow := startYggdrasilTestLogin(t, loginProvider, site.server.URL, yggdrasilTestUsername, yggdrasilTestPassword)
	out, aerr := pollYggdrasilTest(t, flow)
	if aerr != nil {
		t.Fatalf("Poll 不应返回错误: %v", aerr)
	}
	requireYggdrasilOutcome(t, out, LoginStateSucceeded, "")
	original := *out.Credentials

	// 新进程：Provider 的 token→站点根索引为空。
	restartedProvider := site.newProvider()
	_, rerr := yggdrasilRefresher(t, restartedProvider).Refresh(context.Background(), original, out.Account.Profile)
	requireYggdrasilErrorCode(t, rerr, ErrorCodeState)

	siteRefresher, ok := restartedProvider.(YggdrasilSiteRefresher)
	if !ok {
		t.Fatal("重载的 Provider 也必须实现 YggdrasilSiteRefresher")
	}
	refreshed, rerr := siteRefresher.RefreshAtSite(context.Background(), site.apiURL(), original, out.Account.Profile)
	if rerr != nil {
		t.Fatalf("RefreshAtSite 不应返回错误: %v", rerr)
	}
	if refreshed.AccessToken != site.refreshAccessToken {
		t.Fatalf("RefreshAtSite 后 AccessToken 不匹配: %q", refreshed.AccessToken)
	}
	siteValidator, ok := restartedProvider.(YggdrasilSiteValidator)
	if !ok {
		t.Fatal("重载的 Provider 也必须实现 YggdrasilSiteValidator")
	}
	valid, verr := siteValidator.ValidateAtSite(context.Background(), site.apiURL(), refreshed)
	if verr != nil || !valid {
		t.Fatalf("ValidateAtSite 应判定有效: valid=%v err=%v", valid, verr)
	}
}

// ---------------------------------------------------------------------------
// Logout
// ---------------------------------------------------------------------------

func TestYggdrasilLogout(t *testing.T) {
	site := newYggdrasilTestSite(t)
	site.selectedProfile = &yggdrasilProfile{ID: yggdrasilTestProfileID, Name: "Alice"}
	provider := site.newProvider()
	flow := startYggdrasilTestLogin(t, provider, site.server.URL, yggdrasilTestUsername, yggdrasilTestPassword)
	out, aerr := pollYggdrasilTest(t, flow)
	if aerr != nil {
		t.Fatalf("Poll 不应返回错误: %v", aerr)
	}
	requireYggdrasilOutcome(t, out, LoginStateSucceeded, "")
	creds := *out.Credentials

	if lerr := yggdrasilLogoutger(t, provider).Logout(context.Background(), creds); lerr != nil {
		t.Fatalf("Logout 不应返回错误: %v", lerr)
	}
	if site.count("invalidate") != 1 {
		t.Fatalf("注销请求应发一次，实际 %d", site.count("invalidate"))
	}
	invalidateRequest := site.invalidateRequest()
	if invalidateRequest.AccessToken != creds.AccessToken || invalidateRequest.ClientToken != creds.ClientToken {
		t.Fatalf("invalidate 请求凭证不匹配: %+v", invalidateRequest)
	}
	// 幂等：再次注销应直接成功且不再发请求（索引已删除）。
	if lerr := yggdrasilLogoutger(t, provider).Logout(context.Background(), creds); lerr != nil {
		t.Fatalf("重复 Logout 不应返回错误: %v", lerr)
	}
	if site.count("invalidate") != 1 {
		t.Fatalf("重复 Logout 不应重复请求，实际 %d", site.count("invalidate"))
	}

	// 403 视为已在服务端注销（幂等成功）。
	site.invalidateStatus = http.StatusForbidden
	siteLogouter := provider.(YggdrasilSiteLogouter)
	if lerr := siteLogouter.LogoutAtSite(context.Background(), site.apiURL(), creds); lerr != nil {
		t.Fatalf("403 应视为注销成功，实际错误: %v", lerr)
	}

	// 其他非 2xx → ErrorCodeHTTP。
	site.invalidateStatus = http.StatusInternalServerError
	lerr := siteLogouter.LogoutAtSite(context.Background(), site.apiURL(), creds)
	requireYggdrasilErrorCode(t, lerr, ErrorCodeHTTP)

	// 网络失败 → 可重试 ErrorCodeNetwork。
	closedSite := newYggdrasilTestSite(t)
	closedSite.server.Close()
	lerr = siteLogouter.LogoutAtSite(context.Background(), closedSite.apiURL(), creds)
	requireYggdrasilErrorCode(t, lerr, ErrorCodeNetwork)
	if !lerr.Retryable {
		t.Fatal("注销的网络错误必须标记为可重试")
	}
}

// ---------------------------------------------------------------------------
// 并发
// ---------------------------------------------------------------------------

// TestYggdrasilConcurrentPoll 验证并发 Poll 只触发一次认证，且所有调用方
// 看到同一个终态快照。
func TestYggdrasilConcurrentPoll(t *testing.T) {
	site := newYggdrasilTestSite(t)
	site.selectedProfile = &yggdrasilProfile{ID: yggdrasilTestProfileID, Name: "Alice"}
	site.setTextures(yggdrasilTestSkinURL, "slim", yggdrasilTestCapeURL)
	provider := site.newProvider()
	flow := startYggdrasilTestLogin(t, provider, site.server.URL, yggdrasilTestUsername, yggdrasilTestPassword)

	const pollers = 4
	outcomes := make([]LoginOutcome, pollers)
	errs := make([]*Error, pollers)
	var wg sync.WaitGroup
	wg.Add(pollers)
	for i := 0; i < pollers; i++ {
		go func(index int) {
			defer wg.Done()
			outcomes[index], errs[index] = flow.Poll(context.Background())
		}(i)
	}
	wg.Wait()

	for i := 0; i < pollers; i++ {
		if errs[i] != nil {
			t.Fatalf("第 %d 个并发 Poll 返回错误: %v", i, errs[i])
		}
		requireYggdrasilOutcome(t, outcomes[i], LoginStateSucceeded, "")
		if outcomes[i].Account == nil || outcomes[i].Account.Profile.ID != yggdrasilTestProfileID {
			t.Fatalf("第 %d 个并发 Poll 返回的账号不完整: %+v", i, outcomes[i].Account)
		}
	}
	if site.count("authenticate") != 1 {
		t.Fatalf("并发 Poll 只应触发一次认证请求，实际 %d", site.count("authenticate"))
	}
	if site.count("textures") != 1 {
		t.Fatalf("并发 Poll 只应触发一次纹理请求，实际 %d", site.count("textures"))
	}
}
