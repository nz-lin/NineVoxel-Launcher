// 本文件测试来源注册表与镜像重写规则（P3-05 第 1、2、14 条）。
//
// 全部是纯函数测试：不联网、不读环境、不写文件。
package download

import (
	"strings"
	"testing"
)

// TestRewriteMirrorURLKeepsPathByteIdentical 验证镜像重写只换 host、path 逐字节不变。
func TestRewriteMirrorURLKeepsPathByteIdentical(t *testing.T) {
	official := "https://libraries.minecraft.net/com/example/lib/1.0/lib-1.0.jar"

	mirrors, ok := RewriteMirrorURL(official)
	if !ok {
		t.Fatalf("RewriteMirrorURL(%q) 未生成镜像地址", official)
	}
	if len(mirrors) != 1 {
		t.Fatalf("镜像数量 = %d，期望 1", len(mirrors))
	}
	expected := "https://bmclapi2.bangbang93.com/libraries.minecraft.net/com/example/lib/1.0/lib-1.0.jar"
	if mirrors[0] != expected {
		t.Fatalf("镜像地址 = %q，期望 %q", mirrors[0], expected)
	}
}

// TestRewriteMirrorURLRejectsUnsupportedInput 覆盖所有不该被重写的输入。
func TestRewriteMirrorURLRejectsUnsupportedInput(t *testing.T) {
	cases := []struct {
		name     string
		official string
	}{
		{name: "空串", official: ""},
		{name: "http 协议", official: "http://libraries.minecraft.net/com/x.jar"},
		{name: "非白名单 host", official: "https://example.com/com/x.jar"},
		{name: "带查询串", official: "https://libraries.minecraft.net/com/x.jar?signature=abc"},
		{name: "带片段", official: "https://libraries.minecraft.net/com/x.jar#frag"},
		{name: "无法解析", official: "://bad"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			mirrors, ok := RewriteMirrorURL(testCase.official)
			if ok || len(mirrors) != 0 {
				t.Fatalf("RewriteMirrorURL(%q) = (%v, %v)，期望 (nil, false)", testCase.official, mirrors, ok)
			}
		})
	}
}

// TestRewriteMirrorURLIsPureFunction 验证纯函数对同一输入给出相同结果（无状态、无环境依赖）。
func TestRewriteMirrorURLIsPureFunction(t *testing.T) {
	official := "https://piston-meta.mojang.com/v1/packages/abc/manifest.json"

	first, firstOK := RewriteMirrorURL(official)
	second, secondOK := RewriteMirrorURL(official)
	if firstOK != secondOK || len(first) != len(second) {
		t.Fatalf("两次调用结果不一致")
	}
	for index := range first {
		if first[index] != second[index] {
			t.Fatalf("第 %d 项不一致：%q vs %q", index, first[index], second[index])
		}
	}
}

// TestSourceRegistryMirrorDisabledByDefault 验证镜像开关关闭时不产生镜像候选。
func TestSourceRegistryMirrorDisabledByDefault(t *testing.T) {
	registry := NewSourceRegistry(false)
	official := "https://libraries.minecraft.net/com/x.jar"

	if mirrors, ok := registry.RewriteMirrorURL(official); ok || len(mirrors) != 0 {
		t.Fatalf("镜像关闭时仍返回镜像：%v", mirrors)
	}
	candidates, appErr := registry.Candidates(official)
	if appErr != nil {
		t.Fatalf("Candidates 返回错误：%v", appErr)
	}
	if len(candidates) != 1 || candidates[0] != official {
		t.Fatalf("候选 = %v，期望只有官方地址", candidates)
	}
}

// TestSourceRegistryMirrorEnabledPrependsMirrorAfterOfficial 验证启用后官方仍优先。
func TestSourceRegistryMirrorEnabledPrependsMirrorAfterOfficial(t *testing.T) {
	registry := NewSourceRegistry(true)
	official := "https://resources.download.minecraft.net/ab/abcdef"

	candidates, appErr := registry.Candidates(official)
	if appErr != nil {
		t.Fatalf("Candidates 返回错误：%v", appErr)
	}
	if len(candidates) != 2 {
		t.Fatalf("候选数量 = %d，期望 2", len(candidates))
	}
	if candidates[0] != official {
		t.Fatalf("第 1 个候选 = %q，期望官方地址优先", candidates[0])
	}
	if !strings.HasPrefix(candidates[1], "https://bmclapi2.bangbang93.com/resources.download.minecraft.net/") {
		t.Fatalf("第 2 个候选 = %q，不是镜像地址", candidates[1])
	}
}

// TestValidateURLRejectsNonHTTPSAndUnknownHost 覆盖白名单与协议约束。
func TestValidateURLRejectsNonHTTPSAndUnknownHost(t *testing.T) {
	registry := NewSourceRegistry(false)
	cases := []struct {
		name string
		raw  string
		code string
	}{
		{name: "空串", raw: "", code: ErrorCodeSource},
		{name: "http", raw: "http://libraries.minecraft.net/com/x.jar", code: ErrorCodeSource},
		{name: "file 协议", raw: "file:///C:/x.jar", code: ErrorCodeSource},
		{name: "非白名单 host", raw: "https://evil.example.com/x.jar", code: ErrorCodeSource},
		{name: "相似但不同的 host", raw: "https://libraries.minecraft.net.evil.com/x.jar", code: ErrorCodeSource},
		{name: "piston data http", raw: "http://piston-data.mojang.com/client.jar", code: ErrorCodeSource},
		{name: "piston data lookalike", raw: "https://piston-data.mojang.com.evil.example/client.jar", code: ErrorCodeSource},
		{name: "piston data subdomain", raw: "https://evil.piston-data.mojang.com/client.jar", code: ErrorCodeSource},
		{name: "piston data userinfo spoof", raw: "https://piston-data.mojang.com@evil.example/client.jar", code: ErrorCodeSource},
		{name: "无法解析", raw: "://bad", code: ErrorCodeSource},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			appErr := registry.ValidateURL(testCase.raw)
			if appErr == nil {
				t.Fatalf("ValidateURL(%q) 未报错", testCase.raw)
			}
			if appErr.Code != testCase.code {
				t.Fatalf("错误码 = %q，期望 %q", appErr.Code, testCase.code)
			}
			if appErr.Retryable {
				t.Fatalf("地址错误不应标记为可重试")
			}
		})
	}
}

// TestValidateURLAcceptsOfficialHosts 验证所有官方 host 全部放行。
func TestValidateURLAcceptsOfficialHosts(t *testing.T) {
	registry := NewSourceRegistry(false)
	hosts := []string{
		"piston-meta.mojang.com",
		"piston-data.mojang.com",
		"launcher.mojang.com",
		"launchermeta.mojang.com",
		"resources.download.minecraft.net",
		"libraries.minecraft.net",
	}
	for _, host := range hosts {
		raw := "https://" + host + "/path/file.bin"
		if appErr := registry.ValidateURL(raw); appErr != nil {
			t.Fatalf("ValidateURL(%q) 报错：%v", raw, appErr)
		}
	}
}

// TestValidateURLOnlyAcceptsMirrorHostWhenEnabled 验证镜像 host 受开关约束。
func TestValidateURLOnlyAcceptsMirrorHostWhenEnabled(t *testing.T) {
	mirror := "https://bmclapi2.bangbang93.com/libraries.minecraft.net/com/x.jar"

	disabled := NewSourceRegistry(false)
	if appErr := disabled.ValidateURL(mirror); appErr == nil {
		t.Fatalf("镜像关闭时镜像地址不应通过校验")
	}

	enabled := NewSourceRegistry(true)
	if appErr := enabled.ValidateURL(mirror); appErr != nil {
		t.Fatalf("镜像开启时镜像地址应通过校验，实际：%v", appErr)
	}
}

// TestHostOfAndPathPrefixOfKeepLogsShort 验证日志字段只保留 host 与前两段路径。
func TestHostOfAndPathPrefixOfKeepLogsShort(t *testing.T) {
	raw := "https://libraries.minecraft.net/com/example/lib/1.0/lib-1.0.jar?signature=secret"

	if host := HostOf(raw); host != "libraries.minecraft.net" {
		t.Fatalf("HostOf = %q", host)
	}
	if prefix := PathPrefixOf(raw); prefix != "com/example" {
		t.Fatalf("PathPrefixOf = %q，期望 com/example", prefix)
	}
	if strings.Contains(PathPrefixOf(raw), "jar") {
		t.Fatalf("路径前缀不应包含文件名")
	}
	if strings.Contains(PathPrefixOf(raw), "secret") {
		t.Fatalf("路径前缀不应包含查询串内容")
	}
	if prefix := PathPrefixOf("not a url"); prefix != "" {
		t.Fatalf("无法解析时 PathPrefixOf = %q，期望空串", prefix)
	}
}

// TestMirrorEnabledProviderDefaultOff 验证未注入 Provider 时镜像保持关闭。
//
// 安全默认：镜像只在用户显式打开设置后才参与候选，缺失配置一律按关闭处理。
func TestMirrorEnabledProviderDefaultOff(t *testing.T) {
	SetMirrorEnabledProvider(nil)
	t.Cleanup(func() { SetMirrorEnabledProvider(nil) })

	if MirrorEnabledFromProvider() {
		t.Fatal("未注入 Provider 时镜像开关必须为关闭")
	}

	official := "https://libraries.minecraft.net/com/example/lib/1.0/lib-1.0.jar"
	candidates, appErr := NewSourceRegistryFromProvider().Candidates(official)
	if appErr != nil {
		t.Fatalf("生成候选地址失败: %v", appErr)
	}
	if len(candidates) != 1 || candidates[0] != official {
		t.Fatalf("镜像关闭时候选 = %v，期望只有官方地址", candidates)
	}
}

// TestMirrorEnabledProviderDrivesRegistry 验证 Provider 返回 true 时镜像进入候选。
func TestMirrorEnabledProviderDrivesRegistry(t *testing.T) {
	SetMirrorEnabledProvider(func() bool { return true })
	t.Cleanup(func() { SetMirrorEnabledProvider(nil) })

	if !MirrorEnabledFromProvider() {
		t.Fatal("Provider 返回 true 时镜像开关应为开启")
	}

	official := "https://libraries.minecraft.net/com/example/lib/1.0/lib-1.0.jar"
	candidates, appErr := NewSourceRegistryFromProvider().Candidates(official)
	if appErr != nil {
		t.Fatalf("生成候选地址失败: %v", appErr)
	}
	if len(candidates) != 2 {
		t.Fatalf("镜像开启时应有官方 + 镜像两条候选，实际 %d 条: %v", len(candidates), candidates)
	}
	if candidates[0] != official {
		t.Fatalf("第一条候选必须是官方地址，实际 %q", candidates[0])
	}

	want := "https://" + defaultMirrorHost + "/libraries.minecraft.net/com/example/lib/1.0/lib-1.0.jar"
	if candidates[1] != want {
		t.Fatalf("镜像候选 = %q，期望 %q", candidates[1], want)
	}
}

// TestMirrorEnabledProviderFollowsChanges 验证开关切换后新建的注册表立即跟随。
//
// 这正是 P3-15 要修的问题：包级 var 只求值一次，用户改设置后不重启不生效。
func TestMirrorEnabledProviderFollowsChanges(t *testing.T) {
	enabled := false
	SetMirrorEnabledProvider(func() bool { return enabled })
	t.Cleanup(func() { SetMirrorEnabledProvider(nil) })

	before := NewSourceRegistryFromProvider()
	if before.MirrorEnabled() {
		t.Fatal("初始状态下镜像应为关闭")
	}

	enabled = true
	after := NewSourceRegistryFromProvider()
	if !after.MirrorEnabled() {
		t.Fatal("开关切换后新建的注册表应读到新值")
	}
	if before.MirrorEnabled() {
		t.Fatal("已建好的注册表保留创建时的取值，不应被后续改动影响")
	}
}

// TestSetMirrorEnabledProviderNilRestoresDefault 验证注入 nil 能恢复默认关闭。
func TestSetMirrorEnabledProviderNilRestoresDefault(t *testing.T) {
	SetMirrorEnabledProvider(func() bool { return true })
	t.Cleanup(func() { SetMirrorEnabledProvider(nil) })

	if !MirrorEnabledFromProvider() {
		t.Fatal("注入 true 后应开启")
	}
	SetMirrorEnabledProvider(nil)
	if MirrorEnabledFromProvider() {
		t.Fatal("注入 nil 后应恢复默认关闭")
	}
}

func TestPistonDataDownloadCandidates(t *testing.T) {
	const official = "https://piston-data.mojang.com/v1/objects/e877b6a07acd633fb3bb475002175cec036e7b87/client.jar"
	for _, enabled := range []bool{false, true} {
		registry := NewSourceRegistry(enabled)
		candidates, appErr := registry.Candidates(official)
		if appErr != nil {
			t.Fatalf("mirror enabled=%v: %v", enabled, appErr)
		}
		wantCount := 1
		if enabled {
			wantCount = 2
		}
		if len(candidates) != wantCount || candidates[0] != official {
			t.Fatalf("mirror enabled=%v: unexpected candidates %v", enabled, candidates)
		}
		if enabled && candidates[1] != "https://bmclapi2.bangbang93.com/piston-data.mojang.com/v1/objects/e877b6a07acd633fb3bb475002175cec036e7b87/client.jar" {
			t.Fatalf("unexpected mirror URL: %s", candidates[1])
		}
		for _, candidate := range candidates {
			if appErr := registry.ValidateURL(candidate); appErr != nil {
				t.Fatalf("candidate rejected: %v", appErr)
			}
		}
	}
}
