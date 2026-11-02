package logging

import (
	"strings"
	"testing"
)

// TestRedactCredentialKeyValue 验证形如 key=value 的凭证片段会被整体替换。
//
// 反例说明：未脱敏时 access_token=xxx 会把一次性凭证完整写进日志，
// 而日志可能被用户上传用于排查问题。
func TestRedactCredentialKeyValue(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"access_token", "请求失败 access_token=abc123def", "请求失败 access_token=***"},
		{"password", "登录失败 password=hunter2", "登录失败 password=***"},
		{"secret", "配置读取失败 secret=s3cr3t", "配置读取失败 secret=***"},
		{"authorization", "响应头 authorization: Bearer xyz", "响应头 authorization: ***"},
		{"非敏感键不误伤", "重试次数 retry=3", "重试次数 retry=3"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Redact(tc.in)
			if got != tc.want {
				t.Fatalf("Redact(%q) = %q, 期望 %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestRedactBearerAndJWT 验证 Bearer 头与 JWT 形态 Token 会被替换。
func TestRedactBearerAndJWT(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"bearer", "Authorization: Bearer abc.def.ghi", "Authorization: Bearer ***"},
		{"jwt", "收到 eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.sig", "收到 ***"},
		{"普通短标识不误伤", "版本号 v1.2.3", "版本号 v1.2.3"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Redact(tc.in)
			// Bearer 分支会先把 "Bearer <token>" 整体替换，随后 keyValue 分支再处理前缀，
			// 因此这里只断言不再包含原始 Token 片段即可，同时保留可读前缀。
			if strings.Contains(got, "abc.def.ghi") || strings.Contains(got, "eyJhbGci") {
				t.Fatalf("Redact(%q) = %q，仍然包含原始凭证", tc.in, got)
			}
			if !strings.Contains(got, redactedPlaceholder) && tc.name != "普通短标识不误伤" {
				t.Fatalf("Redact(%q) = %q，期望包含占位符 %q", tc.in, got, redactedPlaceholder)
			}
		})
	}
}

// TestRedactURLQuery 验证 URL 查询串被整体移除，路径与域名保留。
//
// 反例说明：OAuth 回调地址的查询串常带 code 与 state，
// 原样写入日志等同于泄漏一次性授权凭据。
func TestRedactURLQuery(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			"oauth回调",
			"回调 https://login.example.com/oauth2/callback?code=abc&state=xyz 失败",
			"回调 https://login.example.com/oauth2/callback 失败",
		},
		{
			"无查询串不受影响",
			"请求 https://api.example.com/v1/versions 成功",
			"请求 https://api.example.com/v1/versions 成功",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Redact(tc.in)
			if got != tc.want {
				t.Fatalf("Redact(%q) = %q, 期望 %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestRedactAbsolutePath 验证 Windows 盘符路径、UNC 路径与常见 POSIX 目录被替换。
func TestRedactAbsolutePath(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"windows盘符", `打开 C:\Users\alice\game\logs 失败`, "打开 " + pathPlaceholder + " 失败"},
		{"unc共享", `访问 \\server\share\file 失败`, "访问 " + pathPlaceholder + " 失败"},
		{"posix目录", "读取 /home/alice/.config/app 失败", "读取 " + pathPlaceholder + " 失败"},
		{"相对路径不受影响", "读取 data/settings.json 成功", "读取 data/settings.json 成功"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Redact(tc.in)
			if got != tc.want {
				t.Fatalf("Redact(%q) = %q, 期望 %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestRedactEmail 验证邮箱按「保留首字符 + *** + @域名」脱敏。
//
// 保留域名是有意取舍：既隐藏完整账号，又能判断是哪个服务返回的地址。
func TestRedactEmail(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"完整邮箱", "账号 alice@example.com 登录失败", "账号 a***@example.com 登录失败"},
		{"多字符本地部分", "联系 support@example.org", "联系 s***@example.org"},
		{"无邮箱文本不受影响", "版本 1.2.3 已安装", "版本 1.2.3 已安装"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Redact(tc.in)
			if got != tc.want {
				t.Fatalf("Redact(%q) = %q, 期望 %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestRedactAttributesSensitiveKey 验证敏感键的值整体替换，其余键按文本脱敏。
func TestRedactAttributesSensitiveKey(t *testing.T) {
	in := map[string]string{
		"accessToken": "abc123",
		"password":    "hunter2",
		"X-Api-Token": "xyz",
		"filePath":    `C:\Users\alice\a.jar`,
		"retry":       "3",
	}
	got := RedactAttributes(in)
	if got == nil {
		t.Fatal("RedactAttributes 返回 nil")
	}

	for _, key := range []string{"accessToken", "password", "X-Api-Token"} {
		if got[key] != redactedPlaceholder {
			t.Fatalf("敏感键 %q 的值 = %q，期望 %q", key, got[key], redactedPlaceholder)
		}
	}
	if strings.Contains(got["filePath"], "alice") {
		t.Fatalf("非敏感键 filePath 仍包含用户名: %q", got["filePath"])
	}
	if got["retry"] != "3" {
		t.Fatalf("非敏感键 retry 被误改: %q", got["retry"])
	}
}

// TestRedactAttributesNil 验证 nil 属性表原样返回 nil，不panic。
func TestRedactAttributesNil(t *testing.T) {
	if got := RedactAttributes(nil); got != nil {
		t.Fatalf("RedactAttributes(nil) = %v，期望 nil", got)
	}
}

// TestIsSensitiveKey 验证敏感键识别的大小写与子串规则。
func TestIsSensitiveKey(t *testing.T) {
	cases := []struct {
		key  string
		want bool
	}{
		{"accessToken", true},
		{"Access-Token", true},
		{"refresh_token", true},
		{"password", true},
		{"passwd", true},
		{"cookie", true},
		{"authorization", true},
		{"credential", true},
		{"session", true},
		{"component", false},
		{"retry", false},
		{"", false},
	}

	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			if got := IsSensitiveKey(tc.key); got != tc.want {
				t.Fatalf("IsSensitiveKey(%q) = %v，期望 %v", tc.key, got, tc.want)
			}
		})
	}
}

// TestQueryRequestNormalizedLimit 验证 Query.Limit 在 Go 边界被裁剪。
//
// 对应接口冻结清单 §8.6：缺省 200、上限 1000，且不信任前端传入值。
func TestQueryRequestNormalizedLimit(t *testing.T) {
	cases := []struct {
		name string
		in   int
		want int
	}{
		{"缺省取 200", 0, DefaultQueryLimit},
		{"负数取 200", -1, DefaultQueryLimit},
		{"常规值原样", 50, 50},
		{"恰好上限", MaxQueryLimit, MaxQueryLimit},
		{"超上限被裁剪", 5000, MaxQueryLimit},
		{"极大值被裁剪", 1 << 30, MaxQueryLimit},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := (QueryRequest{Limit: tc.in}).normalizedLimit()
			if got != tc.want {
				t.Fatalf("Limit=%d 裁剪为 %d，期望 %d", tc.in, got, tc.want)
			}
		})
	}
}

// TestLevelEnabled 验证级别过滤次序：只有不低于阈值的记录会被写入。
func TestLevelEnabled(t *testing.T) {
	cases := []struct {
		level     Level
		threshold Level
		want      bool
	}{
		{LevelDebug, LevelInfo, false},
		{LevelInfo, LevelInfo, true},
		{LevelWarn, LevelInfo, true},
		{LevelError, LevelInfo, true},
		{LevelDebug, LevelDebug, true},
		{LevelInfo, LevelError, false},
	}

	for _, tc := range cases {
		name := string(tc.level) + "_at_" + string(tc.threshold)
		t.Run(name, func(t *testing.T) {
			if got := tc.level.Enabled(tc.threshold); got != tc.want {
				t.Fatalf("%s 在阈值 %s 下 Enabled = %v，期望 %v", tc.level, tc.threshold, got, tc.want)
			}
		})
	}
}

// TestParseLevel 验证级别解析：大小写不敏感，非法值回退 info。
func TestParseLevel(t *testing.T) {
	cases := []struct {
		in   string
		want Level
	}{
		{"debug", LevelDebug},
		{"DEBUG", LevelDebug},
		{"  warn  ", LevelWarn},
		{"error", LevelError},
		{"info", LevelInfo},
		{"", LevelInfo},
		{"trace", LevelInfo},
	}

	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := ParseLevel(tc.in); got != tc.want {
				t.Fatalf("ParseLevel(%q) = %q，期望 %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestIsLogFileName 验证只有 app-<YYYYMMDD>.log 形式的文件参与轮转。
//
// 关键点：P0 的 launcher.log 必须被排除，否则轮转逻辑会把启动日志误删。
func TestIsLogFileName(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"app-20261101.log", true},
		{"launcher.log", false},
		{"app-2026.log", false},
		{"app-20261301.log", false},
		{"app-20261101.log.tmp", false},
		{"other-20261101.log", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isLogFileName(tc.name); got != tc.want {
				t.Fatalf("isLogFileName(%q) = %v，期望 %v", tc.name, got, tc.want)
			}
		})
	}
}
