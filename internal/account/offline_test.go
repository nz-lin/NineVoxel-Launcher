// 本文件实现离线账号与 Ninevoxel 占位来源的单元测试。
package account

import (
	"context"
	"testing"
)

// TestOfflineUUIDVector 用已知向量锁定离线 UUID 派生规则。
//
// 期望值由标准算法独立计算：UUID.nameUUIDFromBytes("OfflinePlayer:Notch")。
// 该值同时是社区公认的 Notch 离线 UUID，可用于跨实现对照。
func TestOfflineUUIDVector(t *testing.T) {
	cases := map[string]string{
		"Notch": "b50ad385829d3141a2167e7d7539ba7f",
		"jeb_":  "",
	}
	for name, want := range cases {
		got := OfflinePlayerUUID(name)
		if want == "" {
			if len(got) != 32 {
				t.Fatalf("%s 的离线 UUID 长度期望 32，实际 %d", name, len(got))
			}
			continue
		}
		if got != want {
			t.Fatalf("%s 的离线 UUID 期望 %s，实际 %s", name, want, got)
		}
	}
}

// TestOfflineFormatHelpers 验证 UUID 规范化与展示格式。
func TestOfflineFormatHelpers(t *testing.T) {
	hyphenated := FormatHyphenated("b50ad385829d3141a2167e7d7539ba7f")
	if hyphenated != "b50ad385-829d-3141-a216-7e7d7539ba7f" {
		t.Fatalf("连字符格式不符：%s", hyphenated)
	}
	normalized, err := NormalizeMinecraftID("B50AD385-829D-3141-A216-7E7D7539BA7F")
	if err != nil {
		t.Fatalf("规范化大写带连字符 UUID 失败：%v", err)
	}
	if normalized != "b50ad385829d3141a2167e7d7539ba7f" {
		t.Fatalf("规范化结果不符：%s", normalized)
	}
	if _, err := NormalizeMinecraftID("not-a-uuid"); err == nil {
		t.Fatal("非法 UUID 期望报错")
	}
}

// TestOfflineStartLogin 验证离线登录直接成功并给出正确档案。
func TestOfflineStartLogin(t *testing.T) {
	provider := NewOfflineProvider(DefaultDeps())
	flow, loginErr := provider.StartLogin(context.Background(), LoginRequest{Username: "  Steve_Builder  "})
	if loginErr != nil {
		t.Fatalf("离线登录失败：%v", loginErr)
	}
	if flow.State() != LoginStateSucceeded {
		t.Fatalf("离线流程期望直接成功，实际 %s", flow.State())
	}

	outcome, err := flow.Poll(context.Background())
	if err != nil {
		t.Fatalf("离线 Poll 失败：%v", err)
	}
	if outcome.State != LoginStateSucceeded {
		t.Fatalf("离线 Poll 期望成功，实际 %s", outcome.State)
	}
	if outcome.Account == nil || outcome.Credentials == nil {
		t.Fatal("成功结果必须带账号与凭证")
	}
	if outcome.Account.Profile.Name != "Steve_Builder" {
		t.Fatalf("玩家名期望 Steve_Builder，实际 %s", outcome.Account.Profile.Name)
	}
	if outcome.Account.Profile.ID != OfflinePlayerUUID("Steve_Builder") {
		t.Fatalf("离线 UUID 不匹配：%s", outcome.Account.Profile.ID)
	}
	if outcome.Account.Kind != KindOffline {
		t.Fatalf("账号类型期望 offline，实际 %s", outcome.Account.Kind)
	}
	if outcome.Credentials.UserType != UserTypeLegacy {
		t.Fatalf("离线 userType 期望 legacy，实际 %s", outcome.Credentials.UserType)
	}
	if outcome.Credentials.AccessToken != "" {
		t.Fatal("离线账号不应带访问令牌")
	}
}

// TestOfflineRejectsInvalidUsername 验证离线玩家名边界。
func TestOfflineRejectsInvalidUsername(t *testing.T) {
	provider := NewOfflineProvider(DefaultDeps())
	cases := []string{"", "ab", "a b", "bad-name", "way_too_long_player_name"}
	for _, name := range cases {
		_, err := provider.StartLogin(context.Background(), LoginRequest{Username: name})
		if err == nil {
			t.Fatalf("期望拒绝非法玩家名 %q", name)
		}
		if err.Code != ErrorCodeInput {
			t.Fatalf("期望错误码 %s，实际 %s", ErrorCodeInput, err.Code)
		}
	}
}

// TestOfflineCapabilities 验证能力声明如实（不提供刷新/校验/注销）。
func TestOfflineCapabilities(t *testing.T) {
	info := NewOfflineProvider(DefaultDeps()).Info()
	if !info.Implemented {
		t.Fatal("离线账号应标记为已实现")
	}
	if info.Capabilities.Refresh || info.Capabilities.Validate || info.Capabilities.Logout {
		t.Fatal("离线账号不应声明刷新/校验/注销能力")
	}
	if info.Capabilities.Interactive {
		t.Fatal("离线账号不应声明需要交互")
	}
}

// TestNinevoxelPlaceholder 验证 Ninevoxel 占位来源如实返回「尚未上线」。
func TestNinevoxelPlaceholder(t *testing.T) {
	provider := NewNinevoxelProvider(DefaultDeps())
	info := provider.Info()
	if info.Implemented {
		t.Fatal("Ninevoxel 首期必须标记为未实现")
	}
	if info.Kind != KindNinevoxel {
		t.Fatalf("类型期望 ninevoxel，实际 %s", info.Kind)
	}

	flow, err := provider.StartLogin(context.Background(), LoginRequest{})
	if flow != nil {
		t.Fatal("占位来源不应返回登录流程")
	}
	if err == nil {
		t.Fatal("占位来源必须返回错误")
	}
	if err.Code != ErrorCodeProviderUnavailable {
		t.Fatalf("期望错误码 %s，实际 %s", ErrorCodeProviderUnavailable, err.Code)
	}
}

// TestKindDisplayAndValidity 验证来源类型枚举。
func TestKindDisplayAndValidity(t *testing.T) {
	for _, kind := range []Kind{KindOffline, KindMicrosoft, KindYggdrasil, KindNinevoxel} {
		if !kind.Valid() {
			t.Fatalf("%s 应为合法来源", kind)
		}
		if kind.DisplayName() == "" {
			t.Fatalf("%s 缺少展示名", kind)
		}
	}
	if Kind("unknown").Valid() {
		t.Fatal("未知来源不应通过校验")
	}
}
