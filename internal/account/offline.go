// 本文件实现离线账号来源（Offline Mode）。
//
// 离线账号不联网、不保存任何凭证：玩家名经 MD5 派生与 Minecraft 一致的
// UUID，启动时直接以离线身份进入游戏。它存在的价值是让单机/局域网玩家
// 在没有任何账号服务的情况下也能启动游戏。
package account

import (
	"context"
	"strings"
)

// offlineProvider 是离线账号来源。
type offlineProvider struct {
	deps Deps
}

// NewOfflineProvider 构造离线账号来源。
func NewOfflineProvider(deps Deps) Provider {
	return &offlineProvider{deps: deps}
}

// Info 返回来源静态描述。
func (p *offlineProvider) Info() ProviderInfo {
	return ProviderInfo{
		Kind:        KindOffline,
		DisplayName: KindOffline.DisplayName(),
		Implemented: true,
		Description: "仅使用本地玩家名，不联网验证，适合单机与局域网。",
		Capabilities: Capabilities{
			Interactive:      false,
			Refresh:          false,
			Validate:         false,
			Logout:           false,
			SkinUpload:       false,
			MultipleProfiles: false,
		},
	}
}

// StartLogin 立即完成一次离线登录。
//
// 离线登录没有网络交互，因此返回的 flow 已经处于成功状态；
// 服务层会直接把它转成账号并落库。
func (p *offlineProvider) StartLogin(_ context.Context, req LoginRequest) (LoginFlow, *Error) {
	name := strings.TrimSpace(req.Username)
	if name == "" {
		return nil, newInputError(ErrorCodeInput, "请输入离线玩家名")
	}
	if !validOfflineUsername(name) {
		return nil, newInputError(ErrorCodeInput, "离线玩家名需为 3-16 位字母、数字或下划线")
	}

	sessionID, idErr := NewUUIDv4()
	if idErr != nil {
		return nil, idErr
	}

	profile := Profile{
		ID:        OfflinePlayerUUID(name),
		Name:      name,
		SkinURL:   "",
		SkinModel: "",
		CapeURL:   "",
	}
	account := Account{
		Kind:        KindOffline,
		ProviderID:  "",
		DisplayName: name,
		Profile:     profile,
	}
	credentials := Credentials{
		// 离线模式没有真实令牌：留空，启动参数由启动链路使用占位值。
		AccessToken: "",
		UserType:    UserTypeLegacy,
		ExpiresAtMs: 0,
	}
	return &offlineFlow{
		id:          sessionID,
		state:       LoginStateSucceeded,
		account:     account,
		credentials: credentials,
	}, nil
}

// offlineFlow 是离线登录流程：从创建起即处于终态。
type offlineFlow struct {
	id          string
	state       LoginState
	account     Account
	credentials Credentials
}

// ID 返回流程标识。
func (f *offlineFlow) ID() string { return f.id }

// Kind 返回来源类型。
func (f *offlineFlow) Kind() Kind { return KindOffline }

// State 返回当前状态。
func (f *offlineFlow) State() LoginState { return f.state }

// Challenge 返回交互信息（离线流程没有交互）。
func (f *offlineFlow) Challenge() LoginChallenge { return LoginChallenge{} }

// Profiles 返回待选档案（离线流程没有选择步骤）。
func (f *offlineFlow) Profiles() []Profile { return nil }

// Poll 返回当前快照（幂等）。
func (f *offlineFlow) Poll(_ context.Context) (LoginOutcome, *Error) {
	if f.state != LoginStateSucceeded {
		return LoginOutcome{State: f.state}, nil
	}
	account := f.account
	credentials := f.credentials
	return LoginOutcome{
		State:       LoginStateSucceeded,
		Account:     &account,
		Credentials: &credentials,
	}, nil
}

// Cancel 取消流程；已成功的流程保持成功（幂等，避免竞态下把成功改写成取消）。
func (f *offlineFlow) Cancel() {
	if f.state == LoginStateSucceeded {
		return
	}
	f.state = LoginStateCancelled
}
