// 本文件覆盖微软来源的**错误诊断**行为：把 AADSTS 码转换成可操作的中文提示。
//
// 背景：真实环境里最常见的失败不是网络问题，而是 Entra 应用配置问题
// （未启用公共客户端、账户类型不支持个人账户、客户端 ID 错误）。
// 只显示「HTTP 401」会让用户完全无法自助修复，因此需要稳定错误码 + 指引文案；
// 同时 error_description 里的 Trace/Correlation ID 不得进入界面。
package account

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// microsoftDiagnosticDeps 构造指向本地测试服务器的微软依赖。
func microsoftDiagnosticDeps(t *testing.T, server *httptest.Server) Deps {
	t.Helper()
	deps := DefaultDeps()
	deps.HTTP = server.Client()
	deps.Now = time.Now
	deps.UserAgent = "NineVoxelLauncher-Test"
	deps.Microsoft = MicrosoftConfig{
		ClientID: "test-client-id",
		Tenant:   "consumers",
		Endpoints: MicrosoftEndpoints{
			DeviceCodeURL:       server.URL + "/devicecode",
			TokenURL:            server.URL + "/token",
			XBLURL:              server.URL + "/xbl",
			XSTSURL:             server.URL + "/xsts",
			MinecraftLoginURL:   server.URL + "/minecraft-login",
			MinecraftProfileURL: server.URL + "/minecraft-profile",
			EntitlementsURL:     server.URL + "/entitlements",
		},
	}
	return deps
}

// newMicrosoftDiagnosticServer 构造只覆盖设备码/令牌两个端点的测试服务器。
func newMicrosoftDiagnosticServer(t *testing.T, deviceCodeStatus int, deviceCodeBody string, tokenStatus int, tokenBody string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/devicecode", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(deviceCodeStatus)
		_, _ = io.WriteString(w, deviceCodeBody)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(tokenStatus)
		_, _ = io.WriteString(w, tokenBody)
	})
	return httptest.NewServer(mux)
}

// TestMicrosoftDeviceCodeInvalidClientHint 验证 401 invalid_client + AADSTS70002
// 会被翻译成「未启用公共客户端」的可操作提示，且不泄漏 trace 信息。
func TestMicrosoftDeviceCodeInvalidClientHint(t *testing.T) {
	body := `{"error":"invalid_client","error_description":"AADSTS70002: The provided client is not supported for this feature. The client application must be marked as 'mobile.' Trace ID: 11111111-2222-3333-4444-555555555555 Correlation ID: 66666666-7777-8888-9999-000000000000","error_codes":[70002]}`
	server := newMicrosoftDiagnosticServer(t, http.StatusUnauthorized, body, http.StatusOK, `{}`)
	defer server.Close()

	provider := NewMicrosoftProvider(microsoftDiagnosticDeps(t, server))
	flow, err := provider.StartLogin(context.Background(), LoginRequest{LoginMode: microsoftLoginModeDeviceCode})
	if flow != nil {
		t.Fatal("设备码申请失败时不应返回登录流程")
	}
	if err == nil {
		t.Fatal("期望返回错误")
	}
	if err.Code != ErrorCodeLogin {
		t.Fatalf("期望错误码 %s，实际 %s", ErrorCodeLogin, err.Code)
	}
	if err.Retryable {
		t.Fatal("invalid_client 不应标记为可重试")
	}
	if !strings.Contains(err.Message, "公共客户端") {
		t.Fatalf("提示应包含公共客户端配置指引，实际 %q", err.Message)
	}
	if !strings.Contains(err.Message, "AADSTS70002") {
		t.Fatalf("提示应包含 AADSTS 码，实际 %q", err.Message)
	}
	for _, forbidden := range []string{"Trace ID", "Correlation ID", "11111111", "66666666"} {
		if strings.Contains(err.Message, forbidden) {
			t.Fatalf("错误文案泄漏了服务端诊断信息 %q：%q", forbidden, err.Message)
		}
	}
}

// TestMicrosoftDeviceCodeServerErrorIsRetryable 验证 5xx 被标记为可重试。
func TestMicrosoftDeviceCodeServerErrorIsRetryable(t *testing.T) {
	server := newMicrosoftDiagnosticServer(t, http.StatusServiceUnavailable, `{"error":"temporarily_unavailable"}`, http.StatusOK, `{}`)
	defer server.Close()

	provider := NewMicrosoftProvider(microsoftDiagnosticDeps(t, server))
	_, err := provider.StartLogin(context.Background(), LoginRequest{LoginMode: microsoftLoginModeDeviceCode})
	if err == nil {
		t.Fatal("期望返回错误")
	}
	if !err.Retryable {
		t.Fatal("5xx 应标记为可重试")
	}
	if !strings.Contains(err.Message, "HTTP 503") {
		t.Fatalf("兜底文案应包含状态码，实际 %q", err.Message)
	}
}

// TestMicrosoftTokenPollInvalidClientHint 验证轮询阶段的 invalid_client 同样有指引。
func TestMicrosoftTokenPollInvalidClientHint(t *testing.T) {
	deviceCodeBody := `{"device_code":"device-code","user_code":"ABCD-EFGH","verification_uri":"https://example.test/device","expires_in":900,"interval":5}`
	tokenBody := `{"error":"invalid_client","error_description":"AADSTS7000218: The request body must contain the following parameter: 'client_assertion' or 'client_secret'.","error_codes":[7000218]}`
	server := newMicrosoftDiagnosticServer(t, http.StatusOK, deviceCodeBody, http.StatusBadRequest, tokenBody)
	defer server.Close()

	provider := NewMicrosoftProvider(microsoftDiagnosticDeps(t, server))
	flow, startErr := provider.StartLogin(context.Background(), LoginRequest{LoginMode: microsoftLoginModeDeviceCode})
	if startErr != nil {
		t.Fatalf("设备码申请应成功：%v", startErr)
	}
	if flow == nil {
		t.Fatal("设备码成功后应返回登录流程")
	}
	if state := flow.State(); state != LoginStateAwaitingUser {
		t.Fatalf("期望等待用户授权，实际 %s", state)
	}

	if _, pollErr := flow.Poll(context.Background()); pollErr == nil {
		t.Fatal("轮询应返回错误")
	} else {
		if pollErr.Code != ErrorCodeLogin {
			t.Fatalf("期望错误码 %s，实际 %s", ErrorCodeLogin, pollErr.Code)
		}
		if !strings.Contains(pollErr.Message, "公共客户端") {
			t.Fatalf("轮询失败提示应包含配置指引，实际 %q", pollErr.Message)
		}
		if strings.Contains(pollErr.Message, "client_assertion") {
			t.Fatalf("不应透传服务端 error_description：%q", pollErr.Message)
		}
	}
}
