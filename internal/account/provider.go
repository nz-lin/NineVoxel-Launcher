// 本文件定义账号来源 SPI 的契约：Provider / LoginFlow / Registry 与共用依赖。
//
// 新增一种账号来源只需要三步：
//  1. 实现 Provider（通常再实现一个 LoginFlow）；
//  2. 在 NewRegistry 中注册构造函数；
//  3. 在 types.go 增加 Kind 常量与展示名。
//
// SPI 刻意不依赖 Wails：服务层与绑定层都是它的消费者，而不是它的宿主。

package account

import (
	"context"
	"net/http"
	"time"
)

// Provider 是账号来源的 SPI 入口。
//
// 实现必须无状态（或只持有不可变配置）：一次登录的所有可变状态放在
// StartLogin 返回的 LoginFlow 里，这样服务层可以在同一进程内并行多个登录流程。
type Provider interface {
	// Info 返回来源的静态描述（类型、能力、是否已实现）。
	Info() ProviderInfo

	// StartLogin 开始一次登录流程。
	//
	// 返回值永远非 nil（除非出现系统级错误）：离线/Yggdrasil 可能同步完成，
	// 微软则返回等待用户授权的流程，由调用方按 LoginFlow.Challenge 提示用户，
	// 再周期性调用 LoginFlow.Poll 推进。
	StartLogin(ctx context.Context, req LoginRequest) (LoginFlow, *Error)
}

// LoginFlow 是一次进行中的登录流程。
//
// 并发约束：Poll 与 Cancel 可能被不同 goroutine 调用，实现必须自行加锁；
// Poll 对已结束的流程必须幂等（重复调用返回同一终态，不重复请求网络）。
type LoginFlow interface {
	// ID 为流程标识（由实现生成，进程内唯一即可）。
	ID() string

	// Kind 返回来源类型。
	Kind() Kind

	// State 返回当前状态（不推进流程、不产生 IO）。
	State() LoginState

	// Challenge 返回需要展示给用户的交互信息（不推进流程）。
	Challenge() LoginChallenge

	// Profiles 返回待选择的档案列表（AwaitingProfile 时非空）。
	Profiles() []Profile

	// Poll 推进流程并返回最新快照。
	//
	// 对 AwaitingUser 的流程会发起一次轮询请求；对终态流程直接返回当前快照。
	Poll(ctx context.Context) (LoginOutcome, *Error)

	// Cancel 取消流程（幂等）；取消后 State 变为 LoginStateCancelled。
	Cancel()
}

// Refresher 是可选能力：支持用已有凭证续期。
//
// 服务层在「刷新账号」时通过类型断言调用；离线账号等不实现该接口。
// 实现不得修改传入的 profile（只读），返回新的凭证；失败返回稳定错误码。
type Refresher interface {
	Refresh(ctx context.Context, creds Credentials, profile Profile) (Credentials, *Error)
}

// Validator 是可选能力：主动校验凭证是否仍有效。
//
// 返回 (false, nil) 表示「明确失效」；返回 error 表示「无法确认」（网络问题等），
// 服务层对两者的处置不同：前者提示重新登录，后者保留账号并提示稍后重试。
type Validator interface {
	Validate(ctx context.Context, creds Credentials) (bool, *Error)
}

// Logoutger 是可选能力：在服务端注销凭证。
//
// 本地删除账号不依赖该能力；调用失败不应阻止本地删除。
type Logoutger interface {
	Logout(ctx context.Context, creds Credentials) *Error
}

// ProfileSelector 是可选能力：需要用户从多个档案中选择一个的来源实现它。
//
// 目前只有 Yggdrasil 会用到；服务层通过类型断言判断，未实现的来源不暴露入口。
type ProfileSelector interface {
	// SelectProfile 选择档案并继续登录（Yggdrasil 内部会执行一次 refresh）。
	SelectProfile(ctx context.Context, profileID string) (LoginOutcome, *Error)
}

// SiteResolver 是可选能力：把用户输入的地址解析成可用的服务信息。
//
// 目前只有 Yggdrasil 实现（ALI 发现 + 元数据读取），供「添加自定义 API」界面
// 在用户提交前校验地址并回显服务名。
type SiteResolver interface {
	ResolveSite(ctx context.Context, rawURL string) (SiteInfo, *Error)
}

// MicrosoftConfig 是微软账号来源的配置。
type MicrosoftConfig struct {
	// ClientID 为 Microsoft Entra 应用（公共客户端）ID。
	//
	// 默认值由产品方提供；测试可通过 Deps 注入任意值，避免依赖真实应用。
	ClientID string

	// Tenant 为 OAuth 租户。消费者账号必须使用 "consumers"，
	// 不能用 "common"/"organizations"，否则 XboxLive.signin 会失败。
	Tenant string

	// Endpoints 为各协议端点；零值字段由 microsoftEndpointsOrDefault 补齐。
	Endpoints MicrosoftEndpoints
}

// MicrosoftEndpoints 汇总微软登录链路的 HTTP 端点（可注入，便于测试）。
type MicrosoftEndpoints struct {
	// AuthorizeURL 为 OAuth 授权端点（授权码 + PKCE 流程使用）。
	AuthorizeURL string

	// DeviceCodeURL 为设备码申请端点（回退流程使用）。
	DeviceCodeURL string

	// TokenURL 为 OAuth 令牌端点（设备码轮询与 refresh_token 共用）。
	TokenURL string

	// XBLURL 为 Xbox Live 用户认证端点。
	XBLURL string

	// XSTSURL 为 XSTS 授权端点。
	XSTSURL string

	// MinecraftLoginURL 为用 XSTS 换取 Minecraft Access Token 的端点。
	MinecraftLoginURL string

	// MinecraftProfileURL 为读取 Minecraft 档案（UUID/皮肤/披风）的端点。
	MinecraftProfileURL string

	// EntitlementsURL 为游戏所有权检查端点（首期只保留，不阻塞登录）。
	EntitlementsURL string
}

// DefaultMicrosoftClientID 是产品默认的 Microsoft Entra 公共客户端 ID。
//
// 公共客户端 ID 不是密钥，可以随客户端分发；仍允许通过 Deps 覆盖，
// 便于将来轮换应用或测试隔离。
const DefaultMicrosoftClientID = "6a668ea6-6551-450e-ba38-aeb4cdfb130f"

// DefaultMicrosoftTenant 是消费者账号必须使用的租户。
const DefaultMicrosoftTenant = "consumers"

// DefaultUserAgent 是账号子系统访问外部服务的 User-Agent。
//
// 带上产品名与版本便于服务端排查；不得包含机器名、用户名等可识别信息。
const DefaultUserAgent = "NineVoxelLauncher/0.1 (+https://ninevoxel.example)"

// Deps 汇总所有账号来源共用的依赖（HTTP 客户端、时钟、配置）。
//
// 服务层在装配时构造一次并传给 NewRegistry；测试通过替换 HTTP 与 Endpoints
// 指向 httptest.Server，实现完全离线的协议测试。
type Deps struct {
	// HTTP 为 HTTP 客户端；nil 时使用 DefaultHTTPClient()。
	HTTP HTTPDoer

	// Now 为时钟；nil 时使用 time.Now。
	Now func() time.Time

	// UserAgent 为请求 UA；空字符串时使用 DefaultUserAgent。
	UserAgent string

	// OpenURL 用于在系统默认浏览器中打开登录页（授权码 + PKCE 流程）。
	//
	// nil 时使用跨平台默认实现（macOS `open` / Windows `rundll32` / Linux `xdg-open`）；
	// 测试注入一个记录函数即可完全离线，不会真的弹浏览器。
	OpenURL func(rawURL string) error

	// Microsoft 为微软来源配置。
	Microsoft MicrosoftConfig
}

// DefaultDeps 返回生产默认依赖。
func DefaultDeps() Deps {
	return Deps{
		HTTP:      DefaultHTTPClient(),
		Now:       time.Now,
		UserAgent: DefaultUserAgent,
		Microsoft: MicrosoftConfig{
			ClientID: DefaultMicrosoftClientID,
			Tenant:   DefaultMicrosoftTenant,
		},
	}
}

// normalize 补齐零值，保证各来源实现不需要重复判空。
func (d Deps) normalize() Deps {
	if d.HTTP == nil {
		d.HTTP = DefaultHTTPClient()
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.UserAgent == "" {
		d.UserAgent = DefaultUserAgent
	}
	if d.Microsoft.ClientID == "" {
		d.Microsoft.ClientID = DefaultMicrosoftClientID
	}
	if d.Microsoft.Tenant == "" {
		d.Microsoft.Tenant = DefaultMicrosoftTenant
	}
	if d.OpenURL == nil {
		d.OpenURL = openURLWithOS
	}
	return d
}

// Registry 是账号来源注册表。
//
// 首期一次性注册四个来源；顺序即界面展示顺序（微软 → 离线 → Yggdrasil →
// Ninevoxel）。注册表在进程生命周期内只读，并发安全。
type Registry struct {
	providers map[Kind]Provider
	order     []Kind
}

// NewRegistry 用给定依赖构造并注册全部内置来源。
//
// 依赖会先规范化（补 HTTP/Now/UA/ClientID），因此调用方可以只覆盖关心的字段。
func NewRegistry(deps Deps) *Registry {
	normalized := deps.normalize()
	return NewRegistryFromProviders(
		NewMicrosoftProvider(normalized),
		NewOfflineProvider(normalized),
		NewYggdrasilProvider(normalized),
		NewNinevoxelProvider(normalized),
	)
}

// NewRegistryFromProviders 用给定来源列表构造注册表。
//
// 供单元测试注入假来源，也为将来「插件式来源」留出扩展点；
// 同 Kind 的来源后者覆盖前者（保持首次注册顺序）。
func NewRegistryFromProviders(providers ...Provider) *Registry {
	registry := &Registry{
		providers: make(map[Kind]Provider, len(providers)),
		order:     make([]Kind, 0, len(providers)),
	}
	for _, provider := range providers {
		if provider == nil {
			continue
		}
		info := provider.Info()
		if _, exists := registry.providers[info.Kind]; !exists {
			registry.order = append(registry.order, info.Kind)
		}
		registry.providers[info.Kind] = provider
	}
	return registry
}

// Get 按类型取来源。
func (r *Registry) Get(kind Kind) (Provider, bool) {
	if r == nil {
		return nil, false
	}
	provider, ok := r.providers[kind]
	return provider, ok
}

// List 返回全部来源的静态描述（按注册顺序）。
func (r *Registry) List() []ProviderInfo {
	if r == nil {
		return nil
	}
	infos := make([]ProviderInfo, 0, len(r.order))
	for _, kind := range r.order {
		if provider, ok := r.providers[kind]; ok {
			infos = append(infos, provider.Info())
		}
	}
	return infos
}

// Compile-time 断言：内置来源必须实现 Provider。
var (
	_ Provider = (*offlineProvider)(nil)
	_ Provider = (*microsoftProvider)(nil)
	_ Provider = (*yggdrasilProvider)(nil)
	_ Provider = (*ninevoxelProvider)(nil)
)

// EnsureHTTPClient 是一个编译期辅助，确保 http 包被引用（避免各文件
// import 抖动导致实现文件漏 import 时出现难懂的编译错误）。
var _ HTTPDoer = (http.DefaultClient)
