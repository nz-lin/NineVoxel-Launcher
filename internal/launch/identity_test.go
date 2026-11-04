// 本文件验证 P3 账号接入：启动链路用所选账号的真实身份替换离线占位身份。
//
// 覆盖两段：
//  1. `resolveAccountIdentity`（identity.go）：空 accountID 回落、字段透传、
//     解析器缺失 / 解析失败 / 身份字段缺失时的脱敏错误；
//  2. `buildLaunchPlanWithIdentity`（arguments.go）：真实身份真的进了游戏参数，
//     且旧入口 `buildLaunchPlan` 仍产出 D-5 的离线占位值（兼容性回归）。
package launch

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// fakeAccountIdentityResolver 是 AccountIdentityResolver 的测试替身。
//
// 记录调用次数与最近一次 accountID，便于断言「空 accountID 时不应调用解析器」。
type fakeAccountIdentityResolver struct {
	identity AccountIdentity
	err      error

	calls  int
	lastID string
}

// ResolveIdentity 实现 AccountIdentityResolver。
func (f *fakeAccountIdentityResolver) ResolveIdentity(_ context.Context, accountID string) (AccountIdentity, error) {
	f.calls++
	f.lastID = accountID
	if f.err != nil {
		return AccountIdentity{}, f.err
	}
	return f.identity, nil
}

// setIdentityResolverForTest 注入测试解析器，并在用例结束时恢复原值。
//
// 解析器是进程级单例（装配层启动时注入一次），测试必须自己收尾，
// 否则会污染同包其他用例。
func setIdentityResolverForTest(t *testing.T, resolver AccountIdentityResolver) {
	t.Helper()

	previous := currentAccountIdentityResolver()
	SetAccountIdentityResolver(resolver)
	t.Cleanup(func() {
		SetAccountIdentityResolver(previous)
	})
}

// newIdentityTestRoot 创建已解析符号链接的临时游戏根。
//
// 为什么不能直接用 `t.TempDir()`：macOS 上 `/var` 是指向 `/private/var` 的符号链接，
// 而 `resolveInside` 对已存在文件会 `EvalSymlinks` 后比对真实路径，
// 未解析的 root 会被误判成「符号链接逃逸」（本仓库既有一批此类环境相关失败）。
// 本卡不改无关代码，但**新用例**先解析符号链接，避免复现同一个环境问题。
func newIdentityTestRoot(t *testing.T) string {
	t.Helper()

	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("解析临时目录符号链接失败: %v", err)
	}
	return root
}

// identityGameArguments 是含全部身份类占位符的游戏参数（真实版本 JSON 的常见形态）。
func identityGameArguments() []json.RawMessage {
	return []json.RawMessage{
		json.RawMessage(`"--username"`), json.RawMessage(`"${auth_player_name}"`),
		json.RawMessage(`"--uuid"`), json.RawMessage(`"${auth_uuid}"`),
		json.RawMessage(`"--accessToken"`), json.RawMessage(`"${auth_access_token}"`),
		json.RawMessage(`"--userType"`), json.RawMessage(`"${user_type}"`),
		json.RawMessage(`"--clientId"`), json.RawMessage(`"${clientid}"`),
		json.RawMessage(`"--auth_xuid"`), json.RawMessage(`"${auth_xuid}"`),
		json.RawMessage(`"--userProperties"`), json.RawMessage(`"${user_properties}"`),
	}
}

// gameArgValueAfter 返回游戏参数中紧跟 key 之后的值。
//
// 参数 plan 为已构建计划；key 为参数名（如 `--username`）。
// 找不到时直接判定用例失败——参数被整条丢弃时必须暴露，而不是静默跳过断言。
func gameArgValueAfter(t *testing.T, plan *LaunchPlan, key string) string {
	t.Helper()

	for index := 0; index+1 < len(plan.GameArguments); index++ {
		if plan.GameArguments[index].Value == key {
			return plan.GameArguments[index+1].Value
		}
	}
	t.Fatalf("游戏参数中找不到 %q，实际 %+v", key, plan.GameArguments)
	return ""
}

// ===== resolveAccountIdentity =====

// TestIdentityResolveEmptyIDKeepsOfflinePlaceholder 验证未传 accountID 时不解析、不报错。
//
// 这是兼容旧调用的关键：空 accountID 必须返回零值身份，调用方据此回落到离线占位。
func TestIdentityResolveEmptyIDKeepsOfflinePlaceholder(t *testing.T) {
	resolver := &fakeAccountIdentityResolver{err: errors.New("不应被调用")}
	setIdentityResolverForTest(t, resolver)

	identity, resolveErr := resolveAccountIdentity(context.Background(), "")
	if resolveErr != nil {
		t.Fatalf("空 accountID 不应报错，实际 %v", resolveErr)
	}
	if identity != (AccountIdentity{}) {
		t.Fatalf("空 accountID 应返回零值身份，实际 %+v", identity)
	}
	if resolver.calls != 0 {
		t.Fatalf("空 accountID 不应调用解析器，实际调用 %d 次", resolver.calls)
	}

	// 纯空白同样视为「未选择账号」。
	identity, resolveErr = resolveAccountIdentity(context.Background(), "   ")
	if resolveErr != nil || identity != (AccountIdentity{}) {
		t.Fatalf("空白 accountID 应返回零值且无错误，实际 identity=%+v err=%v", identity, resolveErr)
	}

	// 未注入解析器时，空 accountID 依然不报错（装配缺失只影响选了账号的调用）。
	SetAccountIdentityResolver(nil)
	if _, resolveErr := resolveAccountIdentity(context.Background(), ""); resolveErr != nil {
		t.Fatalf("未注入解析器时空 accountID 不应报错，实际 %v", resolveErr)
	}
}

// TestIdentityResolvePassesThroughFields 验证有 accountID 时字段原样透传。
func TestIdentityResolvePassesThroughFields(t *testing.T) {
	want := AccountIdentity{
		Username:    "Steve",
		UUID:        "069a79f444e94726a5befca90e38aaf5",
		AccessToken: "mc-access-token",
		UserType:    "msa",
		XUID:        "2535412345678901",
	}
	resolver := &fakeAccountIdentityResolver{identity: want}
	setIdentityResolverForTest(t, resolver)

	identity, resolveErr := resolveAccountIdentity(context.Background(), "account-1")
	if resolveErr != nil {
		t.Fatalf("解析身份不应报错，实际 %v", resolveErr)
	}
	if identity != want {
		t.Fatalf("身份字段应原样透传，期望 %+v，实际 %+v", want, identity)
	}
	if resolver.lastID != "account-1" {
		t.Fatalf("应把 accountID 传给解析器，实际 %q", resolver.lastID)
	}
}

// TestIdentityResolveWithoutResolver 验证缺少解析器时返回 LAUNCH_ACCOUNT_001。
func TestIdentityResolveWithoutResolver(t *testing.T) {
	setIdentityResolverForTest(t, nil)

	identity, resolveErr := resolveAccountIdentity(context.Background(), "account-1")
	if resolveErr == nil {
		t.Fatal("缺少解析器时应返回错误")
	}
	if resolveErr.Code != ErrorCodeLaunchAccount {
		t.Fatalf("错误码期望 %q，实际 %q", ErrorCodeLaunchAccount, resolveErr.Code)
	}
	if resolveErr.Message == "" {
		t.Fatal("错误文案不能为空")
	}
	if identity != (AccountIdentity{}) {
		t.Fatalf("失败时应返回零值身份，实际 %+v", identity)
	}
}

// TestIdentityResolveErrorIsSanitized 验证解析器错误被脱敏为 LAUNCH_ACCOUNT_001。
//
// 底层错误可能含 token 片段或账号 id，绝不能透传到用户可见文案里。
func TestIdentityResolveErrorIsSanitized(t *testing.T) {
	const secret = "secret-token-abc123"
	resolver := &fakeAccountIdentityResolver{err: errors.New("refresh failed: " + secret)}
	setIdentityResolverForTest(t, resolver)

	identity, resolveErr := resolveAccountIdentity(context.Background(), "account-secret-id")
	if resolveErr == nil {
		t.Fatal("解析器返回错误时应返回错误")
	}
	if resolveErr.Code != ErrorCodeLaunchAccount {
		t.Fatalf("错误码期望 %q，实际 %q", ErrorCodeLaunchAccount, resolveErr.Code)
	}
	if identity != (AccountIdentity{}) {
		t.Fatalf("失败时应返回零值身份，实际 %+v", identity)
	}
	if strings.Contains(resolveErr.Message, secret) {
		t.Fatalf("错误文案泄漏了底层错误内容: %q", resolveErr.Message)
	}
	if strings.Contains(resolveErr.Message, "account-secret-id") {
		t.Fatalf("错误文案泄漏了账号 id: %q", resolveErr.Message)
	}
}

// TestIdentityResolveRejectsIncompleteIdentity 验证身份缺 Username 或 UUID 时拒绝启动。
//
// 没有玩家名 / UUID 就无法构造可启动的身份参数：宁可拒绝并提示重新登录，
// 也不要带着空值启动（游戏侧会以难懂的错误失败）。
func TestIdentityResolveRejectsIncompleteIdentity(t *testing.T) {
	cases := []struct {
		name     string
		identity AccountIdentity
	}{
		{name: "缺用户名", identity: AccountIdentity{UUID: "069a79f444e94726a5befca90e38aaf5"}},
		{name: "缺 UUID", identity: AccountIdentity{Username: "Steve"}},
		{name: "用户名为空白", identity: AccountIdentity{Username: "   ", UUID: "069a79f444e94726a5befca90e38aaf5"}},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			resolver := &fakeAccountIdentityResolver{identity: testCase.identity}
			setIdentityResolverForTest(t, resolver)

			identity, resolveErr := resolveAccountIdentity(context.Background(), "account-1")
			if resolveErr == nil {
				t.Fatal("身份字段不完整时应返回错误")
			}
			if resolveErr.Code != ErrorCodeLaunchAccount {
				t.Fatalf("错误码期望 %q，实际 %q", ErrorCodeLaunchAccount, resolveErr.Code)
			}
			if identity != (AccountIdentity{}) {
				t.Fatalf("失败时应返回零值身份，实际 %+v", identity)
			}
		})
	}
}

// ===== buildLaunchPlanWithIdentity =====

// TestIdentityPlanReplacesPlaceholders 验证真实身份替换全部身份类占位符。
func TestIdentityPlanReplacesPlaceholders(t *testing.T) {
	root := newIdentityTestRoot(t)
	id := "with-real-identity"
	writeVersionJSON(t, root, id, `{"id":"with-real-identity","type":"release","mainClass":"Main"}`)
	writeClientJar(t, root, id, id)

	merged := &mergedVersion{
		ID:            id,
		MainClass:     "Main",
		ClientJarID:   id,
		GameArguments: identityGameArguments(),
	}
	identity := AccountIdentity{
		Username:    "Steve",
		UUID:        "069a79f444e94726a5befca90e38aaf5",
		AccessToken: "mc-access-token",
		UserType:    "msa",
		XUID:        "2535412345678901",
	}

	plan, planErr := buildLaunchPlanWithIdentity(root, root, id, merged, nil, nil, identity)
	if planErr != nil {
		t.Fatalf("构建计划失败: %v", planErr)
	}
	if len(plan.UnresolvedPlaceholders) != 0 {
		t.Fatalf("身份占位符应全部解析，实际未解析: %v", plan.UnresolvedPlaceholders)
	}
	if !plan.SafeToLaunch {
		t.Fatalf("占位符已解析时可启动，warnings=%+v", plan.Warnings)
	}

	// 参数值必须与账号身份逐项一致（不是「出现子串」就算通过）。
	expectedArgs := map[string]string{
		"--username":    identity.Username,
		"--uuid":        identity.UUID,
		"--accessToken": identity.AccessToken,
		"--userType":    identity.UserType,
		"--clientId":    identity.XUID,
		"--auth_xuid":   identity.XUID,
		// 首期不做 user properties：即使有真实身份也保持空 JSON 对象。
		"--userProperties": "{}",
	}
	for key, want := range expectedArgs {
		if got := gameArgValueAfter(t, plan, key); got != want {
			t.Fatalf("%s 期望 %q，实际 %q", key, want, got)
		}
	}

	// 明确断言不再出现离线占位值（Player / 全零 UUID）。
	values := make([]string, 0, len(plan.GameArguments))
	for _, part := range plan.GameArguments {
		values = append(values, part.Value)
	}
	joined := strings.Join(values, "\n")
	if strings.Contains(joined, placeholderUsername) {
		t.Fatalf("有真实身份时不应再出现离线占位用户名 %q: %q", placeholderUsername, joined)
	}
	if strings.Contains(joined, placeholderUUID) {
		t.Fatalf("有真实身份时不应再出现离线占位 UUID %q", placeholderUUID)
	}
}

// TestIdentityPlanFallsBackPerField 验证真实身份的空字段逐项回落占位值。
//
// 离线账号的典型形态：有 Username/UUID，没有 AccessToken/UserType/XUID。
// 此时玩家名与 UUID 必须用真实的，其余回落占位常量（而不是整体退回 Player）。
func TestIdentityPlanFallsBackPerField(t *testing.T) {
	root := newIdentityTestRoot(t)
	id := "offline-account-identity"
	writeVersionJSON(t, root, id, `{"id":"offline-account-identity","type":"release","mainClass":"Main"}`)
	writeClientJar(t, root, id, id)

	merged := &mergedVersion{
		ID:            id,
		MainClass:     "Main",
		ClientJarID:   id,
		GameArguments: identityGameArguments(),
	}
	identity := AccountIdentity{
		Username: "OfflinePlayer",
		UUID:     "0123456789abcdef0123456789abcdef",
		// AccessToken / UserType / XUID 均为空（离线账号没有这些值）。
	}

	plan, planErr := buildLaunchPlanWithIdentity(root, root, id, merged, nil, nil, identity)
	if planErr != nil {
		t.Fatalf("构建计划失败: %v", planErr)
	}

	expectedArgs := map[string]string{
		"--username":    identity.Username,
		"--uuid":        identity.UUID,
		"--accessToken": placeholderAccessToken,
		"--userType":    placeholderUserType,
		"--clientId":    placeholderClientID,
		"--auth_xuid":   placeholderClientID,
	}
	for key, want := range expectedArgs {
		if got := gameArgValueAfter(t, plan, key); got != want {
			t.Fatalf("%s 期望 %q，实际 %q", key, want, got)
		}
	}
}

// TestIdentityPlanKeepsOfflinePlaceholders 验证旧入口行为完全不变。
//
// 兼容性回归：`buildLaunchPlan` 是既有测试与预览路径的入口，必须仍然产出
// D-5 的离线占位值，不得因为账号接入而改变签名或行为。
func TestIdentityPlanKeepsOfflinePlaceholders(t *testing.T) {
	root := newIdentityTestRoot(t)
	id := "offline-placeholder"
	writeVersionJSON(t, root, id, `{"id":"offline-placeholder","type":"release","mainClass":"Main"}`)
	writeClientJar(t, root, id, id)

	merged := &mergedVersion{
		ID:            id,
		MainClass:     "Main",
		ClientJarID:   id,
		GameArguments: identityGameArguments(),
	}

	plan, planErr := buildLaunchPlan(root, root, id, merged, nil, nil)
	if planErr != nil {
		t.Fatalf("构建计划失败: %v", planErr)
	}

	expectedArgs := map[string]string{
		"--username":       placeholderUsername,
		"--uuid":           placeholderUUID,
		"--accessToken":    placeholderAccessToken,
		"--userType":       placeholderUserType,
		"--clientId":       placeholderClientID,
		"--auth_xuid":      placeholderClientID,
		"--userProperties": "{}",
	}
	for key, want := range expectedArgs {
		if got := gameArgValueAfter(t, plan, key); got != want {
			t.Fatalf("%s 期望占位值 %q，实际 %q", key, want, got)
		}
	}

	// 离线提示仍必须存在（D-5：UI 明示非登录）。
	found := false
	for _, warning := range plan.Warnings {
		if warning.Message == messageOfflineIdentity {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("离线占位身份应带离线提示，warnings=%+v", plan.Warnings)
	}
}
