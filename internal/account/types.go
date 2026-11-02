// 本文件定义账号子系统的**纯数据类型**：不依赖 Wails、不依赖数据库、不做 IO。
//
// 这些类型同时是 SPI 的契约：Provider 只认识这里的类型，服务层与 Wails 壳
// 只做转换与编排。任何字段变更都会影响前端契约（见 wails.go 的 DTO 注释）。

package account

// Kind 是账号来源类型。
//
// 取值是稳定字符串：会写进数据库、出现在前端契约里，**不得随意改名**。
type Kind string

const (
	// KindOffline 表示离线账号（Offline Mode）。
	KindOffline Kind = "offline"

	// KindMicrosoft 表示微软账号（Microsoft / Xbox Live 登录）。
	KindMicrosoft Kind = "microsoft"

	// KindYggdrasil 表示自定 Yggdrasil API（authlib-injector 兼容服务）。
	KindYggdrasil Kind = "yggdrasil"

	// KindNinevoxel 表示 Ninevoxel 站账号（首期占位）。
	KindNinevoxel Kind = "ninevoxel"
)

// allKinds 是全部合法账号来源，顺序即界面展示顺序。
var allKinds = []Kind{KindMicrosoft, KindOffline, KindYggdrasil, KindNinevoxel}

// Valid 判断 Kind 是否为已知来源。
func (k Kind) Valid() bool {
	for _, item := range allKinds {
		if k == item {
			return true
		}
	}
	return false
}

// DisplayName 返回面向用户的中文名称。
func (k Kind) DisplayName() string {
	switch k {
	case KindOffline:
		return "离线账号"
	case KindMicrosoft:
		return "微软账号"
	case KindYggdrasil:
		return "Yggdrasil 账号"
	case KindNinevoxel:
		return "Ninevoxel 账号"
	default:
		return "未知账号类型"
	}
}

// Capabilities 描述一种账号来源支持的能力，供界面决定展示哪些操作。
//
// 能力必须如实：不支持的能力一律 false，界面据此隐藏按钮，避免「点了没反应」。
type Capabilities struct {
	// Interactive 表示登录需要用户在浏览器/外部设备完成交互（微软设备码）。
	Interactive bool `json:"interactive"`

	// Refresh 表示支持用 Refresh Token / Access Token 续期。
	Refresh bool `json:"refresh"`

	// Validate 表示支持主动校验凭证是否仍然有效。
	Validate bool `json:"validate"`

	// Logout 表示支持在服务端注销凭证（本地删除不算）。
	Logout bool `json:"logout"`

	// SkinUpload 表示支持更换皮肤/披风（首期恒 false）。
	SkinUpload bool `json:"skinUpload"`

	// MultipleProfiles 表示一个用户可能有多个档案，需要二次选择。
	MultipleProfiles bool `json:"multipleProfiles"`
}

// Profile 是 Minecraft 侧的玩家档案（一个账号对应一个 Profile）。
//
// ID 统一使用**无连字符小写 UUID**（Minecraft 启动参数与 Yggdrasil 协议都要求
// 该形式）；展示需要连字符时用 FormatHyphenated 自行格式化，不额外存两份。
type Profile struct {
	// ID 为无连字符小写 UUID。
	ID string `json:"id"`

	// Name 为玩家名（游戏内昵称）。
	Name string `json:"name"`

	// SkinURL 为皮肤地址；没有皮肤时为空字符串。
	SkinURL string `json:"skinUrl,omitempty"`

	// SkinModel 为皮肤模型：classic / slim；未知时为空字符串。
	SkinModel string `json:"skinModel,omitempty"`

	// CapeURL 为披风地址；没有披风时为空字符串。
	CapeURL string `json:"capeUrl,omitempty"`
}

// Credentials 是一次登录产生的凭证。
//
// **本类型只在后端流转，绝不进入任何返回给前端的结构体。**
type Credentials struct {
	// AccessToken 为访问令牌（Minecraft / Yggdrasil）。
	AccessToken string

	// ClientToken 为客户端令牌（Yggdrasil 协议使用；微软账号为空）。
	ClientToken string

	// RefreshToken 为续期令牌（微软与部分 Yggdrasil 服务使用；离线账号为空）。
	RefreshToken string

	// ExpiresAtMs 为 AccessToken 过期时间（UTC Unix 毫秒）；0 表示不过期/未知。
	ExpiresAtMs int64

	// UserType 为启动参数 `${user_type}` 的取值：msa / mojang / legacy。
	UserType string

	// XUID 为 Xbox User ID（微软账号），用于 `${auth_xuid}`；其他来源为空。
	XUID string
}

// 启动参数 `${user_type}` 的稳定取值。
const (
	// UserTypeMSA 表示微软账号。
	UserTypeMSA = "msa"

	// UserTypeMojang 表示 Yggdrasil / Mojang 账号。
	UserTypeMojang = "mojang"

	// UserTypeLegacy 表示离线账号。
	UserTypeLegacy = "legacy"
)

// Account 是本地持久化的账号摘要（不含任何凭证）。
//
// 这是前端可见的账号模型：ID 是本地生成的 UUID，用于选择/删除/刷新账号；
// Kind + ProviderID + Profile.ID 三者共同构成「同一账号」的唯一身份。
type Account struct {
	// ID 为本地账号标识（UUID v4），由服务层生成。
	ID string `json:"id"`

	// Kind 为账号来源类型。
	Kind Kind `json:"kind"`

	// ProviderID 为来源实例标识：微软为 "microsoft"，Yggdrasil 为 API 根，
	// Ninevoxel 为服务根，离线为空字符串。
	ProviderID string `json:"providerId"`

	// DisplayName 为界面展示名（通常等于 Profile.Name）。
	DisplayName string `json:"displayName"`

	// Profile 为 Minecraft 档案。
	Profile Profile `json:"profile"`

	// CreatedAtMs 为账号创建时间（UTC Unix 毫秒）。
	CreatedAtMs int64 `json:"createdAtMs"`

	// UpdatedAtMs 为账号最近一次资料/凭证更新时间（UTC Unix 毫秒）。
	UpdatedAtMs int64 `json:"updatedAtMs"`

	// LastUsedAtMs 为账号最近一次被选为当前账号的时间（UTC Unix 毫秒）。
	LastUsedAtMs int64 `json:"lastUsedAtMs"`

	// Selected 表示是否为当前选中的账号（全局唯一）。
	Selected bool `json:"selected"`
}

// Identity 返回账号的唯一身份键（用于登录成功后的去重）。
//
// 规则来自 authlib-injector 规范：认证服务器 + 档案 UUID 才能唯一确定一个账号，
// 玩家名是可变的，不能参与身份判定。
func (a Account) Identity() AccountIdentity {
	return AccountIdentity{Kind: a.Kind, ProviderID: a.ProviderID, ProfileID: a.Profile.ID}
}

// AccountIdentity 是账号去重键。
type AccountIdentity struct {
	Kind       Kind
	ProviderID string
	ProfileID  string
}

// LoginState 是登录流程的状态机取值。
type LoginState string

const (
	// LoginStatePending 表示流程已创建但尚未拿到用户交互信息（瞬时态）。
	LoginStatePending LoginState = "pending"

	// LoginStateAwaitingUser 表示等待用户在浏览器/外部设备完成授权。
	LoginStateAwaitingUser LoginState = "awaiting_user"

	// LoginStateAwaitingProfile 表示凭证已拿到，但用户有多个档案需要选择。
	LoginStateAwaitingProfile LoginState = "awaiting_profile"

	// LoginStateSucceeded 表示登录成功（此时 Outcome 带 Account 与 Credentials）。
	LoginStateSucceeded LoginState = "succeeded"

	// LoginStateFailed 表示登录失败（带稳定错误码与可读文案）。
	LoginStateFailed LoginState = "failed"

	// LoginStateCancelled 表示用户或调用方取消了登录。
	LoginStateCancelled LoginState = "cancelled"
)

// Terminal 判断状态是否为终态。
func (s LoginState) Terminal() bool {
	return s == LoginStateSucceeded || s == LoginStateFailed || s == LoginStateCancelled
}

// LoginRequest 是开始一次登录所需的**全部**输入。
//
// 不同来源按需取字段：离线只用 Username；Yggdrasil 用 APIURL+Username+Password；
// 微软不使用任何字段（走设备码）。来源不认识的字段一律忽略，不做隐式转换。
type LoginRequest struct {
	// Username 为用户名/邮箱（离线账号为玩家名）。
	Username string

	// Password 为密码（Yggdrasil 使用；**不落盘、不进日志**）。
	Password string

	// APIURL 为自定 Yggdrasil 服务地址（用户可输入简写地址，由来源负责 ALI 发现）。
	APIURL string

	// LoginMode 为交互式来源的登录方式偏好（目前仅微软账号使用）：
	//   - "pkce"（默认）：系统浏览器 + 本地回环回调，登录后自动返回启动器；
	//   - "device_code"：设备码，作为 PKCE 不可用时的回退项。
	// 其他来源忽略该字段；未知取值按默认方式处理。
	LoginMode string
}

// LoginChallenge 是登录过程中展示给用户的交互信息。
type LoginChallenge struct {
	// UserCode 为设备码流程中的用户码（如 ABCD-EFGH）。
	UserCode string `json:"userCode,omitempty"`

	// VerificationURI 为用户需要访问的验证地址。
	VerificationURI string `json:"verificationUri,omitempty"`

	// Message 为服务端返回的完整提示文案（微软会返回本地化文案）。
	Message string `json:"message,omitempty"`

	// ExpiresAtMs 为用户码过期时间（UTC Unix 毫秒）；0 表示未知。
	ExpiresAtMs int64 `json:"expiresAtMs"`

	// IntervalSeconds 为建议的轮询间隔（秒）；0 表示按服务层默认值。
	IntervalSeconds int `json:"intervalSeconds,omitempty"`
}

// LoginOutcome 是 Poll / SelectProfile 的返回值，也是流程的权威快照。
//
// 调用方必须按 State 分支：只有 Succeeded 时 Account/Credentials 才有效。
type LoginOutcome struct {
	// State 为当前状态。
	State LoginState

	// Challenge 为需要展示给用户的交互信息（AwaitingUser 时有效）。
	Challenge LoginChallenge

	// Profiles 为可选择的档案列表（AwaitingProfile 时非空）。
	Profiles []Profile

	// Account 为成功时的账号摘要（ID/时间戳由服务层补齐）。
	Account *Account

	// Credentials 为成功时的凭证（仅后端使用）。
	Credentials *Credentials

	// ErrorCode 为失败时的稳定错误码。
	ErrorCode string

	// ErrorMessage 为失败时的中文可读文案（已脱敏）。
	ErrorMessage string
}

// SiteInfo 是 Yggdrasil 服务地址解析后的站点信息（供「添加自定义 API」界面展示）。
type SiteInfo struct {
	// APIURL 为规范化后的 API 根（以 / 结尾）。
	APIURL string

	// ServerName 为服务端展示名（来自元数据 meta.serverName）。
	ServerName string

	// ImplementationName 为实现名称（来自元数据 meta.implementationName）。
	ImplementationName string

	// NonEmailLogin 表示服务端允许用非邮箱标识登录。
	NonEmailLogin bool

	// SkinDomains 为服务端声明的皮肤域名白名单。
	SkinDomains []string
}

// ProviderInfo 是账号来源的静态描述（供前端渲染「添加账号」入口）。
type ProviderInfo struct {
	// Kind 为来源类型。
	Kind Kind `json:"kind"`

	// DisplayName 为中文名称。
	DisplayName string `json:"displayName"`

	// Implemented 表示该来源是否已经真正实现（Ninevoxel 首期为 false）。
	Implemented bool `json:"implemented"`

	// Description 为面向用户的一句话说明。
	Description string `json:"description"`

	// Capabilities 为能力声明。
	Capabilities Capabilities `json:"capabilities"`
}
