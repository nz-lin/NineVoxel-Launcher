// 本文件实现 Ninevoxel 站账号来源的**占位**实现。
//
// 产品规划中 Ninevoxel 站会提供自有账号体系，但服务端协议尚未冻结。
// 占位实现刻意返回稳定的「尚未上线」错误，而不是伪造登录成功：
// 界面可以照常展示入口与说明，用户不会拿到一个假账号。
package account

import "context"

// ninevoxelProvider 是 Ninevoxel 站账号来源（占位）。
type ninevoxelProvider struct {
	deps Deps
}

// NewNinevoxelProvider 构造 Ninevoxel 账号来源。
func NewNinevoxelProvider(deps Deps) Provider {
	return &ninevoxelProvider{deps: deps}
}

// Info 返回来源静态描述。
//
// Implemented 为 false：前端据此把入口标记为「即将上线」，并禁用登录按钮。
func (p *ninevoxelProvider) Info() ProviderInfo {
	return ProviderInfo{
		Kind:        KindNinevoxel,
		DisplayName: KindNinevoxel.DisplayName(),
		Implemented: false,
		Description: "Ninevoxel 站账号服务尚未上线（占位）。",
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

// StartLogin 始终返回「尚未上线」。
//
// 用 ErrorCodeProviderUnavailable 而不是 ErrorCodeLogin：前者是产品能力缺失，
// 前端应展示「敬请期待」，后者会让用户误以为是自己账号/网络的问题。
func (p *ninevoxelProvider) StartLogin(_ context.Context, _ LoginRequest) (LoginFlow, *Error) {
	return nil, newSystemError(ErrorCodeProviderUnavailable, "Ninevoxel 账号服务尚未上线，敬请期待")
}
