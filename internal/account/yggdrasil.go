// 本文件实现自定 Yggdrasil 账号来源（authlib-injector 兼容）。
//
// 协议链路：
//
//	用户输入地址 --(GET + X-Authlib-Injector-API-Location)--> API 根
//	API 根 --(GET 元数据)--> 站点信息（服务名 / 非邮箱登录 / 皮肤域名）
//	API 根/authserver/authenticate --> accessToken/clientToken + 档案列表
//	API 根/sessionserver/session/minecraft/profile/{uuid}?unsigned=false
//	  --> 皮肤/披风（尽力而为，失败不影响登录）
//
// 关键设计：
//   - Provider 不持有单次登录的可变状态：一次登录的全部状态在 yggdrasilFlow
//     里，用互斥锁保护；Poll 对终态与「待选择档案」幂等，不再发起网络请求；
//   - 站点解析结果缓存在 flow 内，重试时不会重复做 ALI 发现；
//   - Refresher/Validator/Logoutger 的 SPI 只接收凭证，而 Yggdrasil 端点依赖
//     站点 API 根，因此 Provider 额外维护一个进程内 accessToken→API 根索引
//     （登录/续期成功时登记），并提供显式的 *AtSite 扩展接口，供服务层在持有
//     Account.ProviderID 时直接传入（跨进程重启后必须走扩展接口，见接口注释）；
//   - 所有对外错误文案都不包含 accessToken/clientToken/密码/完整 URL 查询串。
package account

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

const (
	// authlibInjectorAPILocationHeader 是 authlib-injector 规范定义的 API 根发现响应头。
	authlibInjectorAPILocationHeader = "X-Authlib-Injector-API-Location"

	// yggdrasilAgentName / yggdrasilAgentVersion 是认证请求里声明的客户端类型。
	yggdrasilAgentName    = "Minecraft"
	yggdrasilAgentVersion = 1

	// yggdrasilServerMessageLimit 是透传服务端 errorMessage 时的最大字符数。
	//
	// 服务端文案可能很长，甚至夹带换行；截断并去换行后再展示，避免撑爆界面。
	yggdrasilServerMessageLimit = 200

	// yggdrasilSiteIndexLimit 限制进程内 accessToken→API 根索引的条目数。
	//
	// 该索引只是 SPI 缺省参数（站点根）的兜底，不是业务数据；超过上限时
	// 丢弃一个旧条目即可，不需要精确 LRU。
	yggdrasilSiteIndexLimit = 512
)

// yggdrasilProvider 是自定 Yggdrasil 来源。
type yggdrasilProvider struct {
	deps Deps

	// sites 是进程内的 accessToken→API 根索引。
	//
	// Refresher/Validator/Logoutger 的契约只给凭证不给站点根，而 Yggdrasil
	// 每个请求都必须打到具体站点；登录成功时登记，续期时跟随新 token 迁移。
	// 服务层持有账号记录时应优先使用 *AtSite 扩展接口（见 YggdrasilSiteRefresher
	// 等），本索引只覆盖「同一进程内刚登录过」的场景。
	sitesMu sync.Mutex
	sites   map[string]string
}

// NewYggdrasilProvider 构造自定 Yggdrasil 来源。
func NewYggdrasilProvider(deps Deps) Provider {
	return &yggdrasilProvider{
		deps:  deps.normalize(),
		sites: make(map[string]string, 8),
	}
}

// Info 返回来源静态描述。
func (p *yggdrasilProvider) Info() ProviderInfo {
	return ProviderInfo{
		Kind:        KindYggdrasil,
		DisplayName: KindYggdrasil.DisplayName(),
		Implemented: true,
		Description: "使用自定 Yggdrasil API（authlib-injector 兼容服务）登录",
		Capabilities: Capabilities{
			Interactive:      false,
			Refresh:          true,
			Validate:         true,
			Logout:           true,
			SkinUpload:       false,
			MultipleProfiles: true,
		},
	}
}

// StartLogin 开始一次 Yggdrasil 登录。
//
// 这里只做输入校验并构造流程；站点解析与认证在第一次 Poll 时执行，这样
// 调用方可以用自己的 ctx 控制网络阶段，StartLogin 本身不会被网络阻塞。
func (p *yggdrasilProvider) StartLogin(_ context.Context, req LoginRequest) (LoginFlow, *Error) {
	root, nerr := normalizeYggdrasilAPIURL(req.APIURL)
	if nerr != nil {
		return nil, nerr
	}
	username := strings.TrimSpace(req.Username)
	if username == "" {
		return nil, newInputError(ErrorCodeInput, "请输入用户名或邮箱")
	}
	if req.Password == "" {
		return nil, newInputError(ErrorCodeInput, "请输入密码")
	}
	flowID, uerr := NewUUIDv4()
	if uerr != nil {
		return nil, uerr
	}
	flow := &yggdrasilFlow{
		id:       flowID,
		provider: p,
		root:     root,
		username: username,
		password: req.Password,
		state:    LoginStatePending,
	}
	flow.cond = sync.NewCond(&flow.mu)
	return flow, nil
}

// ResolveSite 实现 SiteResolver：ALI 发现 + 元数据读取。
func (p *yggdrasilProvider) ResolveSite(ctx context.Context, rawURL string) (SiteInfo, *Error) {
	return resolveYggdrasilSite(ctx, p.dependencies(), rawURL)
}

// Refresh 实现 Refresher（凭证续期）。
//
// 站点根取自进程内 token→站点根索引；索引里没有时返回不可重试的状态错误，
// 调用方应改用 YggdrasilSiteRefresher.RefreshAtSite 传入 Account.ProviderID。
func (p *yggdrasilProvider) Refresh(ctx context.Context, creds Credentials, _ Profile) (Credentials, *Error) {
	apiRoot, ok := p.siteForToken(creds.AccessToken)
	if !ok {
		return Credentials{}, newExternalError(ErrorCodeState, "无法确定该账号的 Yggdrasil 服务地址，请重新登录", false)
	}
	return p.refreshCredentials(ctx, apiRoot, creds)
}

// Validate 实现 Validator（凭证校验）。
func (p *yggdrasilProvider) Validate(ctx context.Context, creds Credentials) (bool, *Error) {
	apiRoot, ok := p.siteForToken(creds.AccessToken)
	if !ok {
		return false, newExternalError(ErrorCodeState, "无法确定该账号的 Yggdrasil 服务地址，请重新登录", false)
	}
	return p.validateAtSite(ctx, apiRoot, creds)
}

// Logout 实现 Logoutger（服务端注销凭证）。
//
// 索引里没有站点根时视为「服务端凭证已不可定位」，直接返回成功：本地删除
// 不依赖该能力，且重复注销必须幂等。
func (p *yggdrasilProvider) Logout(ctx context.Context, creds Credentials) *Error {
	apiRoot, ok := p.siteForToken(creds.AccessToken)
	if !ok {
		return nil
	}
	return p.logoutAtSite(ctx, apiRoot, creds)
}

// YggdrasilSiteRefresher 是 Yggdrasil 来源的扩展能力：显式传入站点 API 根。
//
// 背景：Provider SPI 的 Refresher 只接收 Credentials + Profile，而 Yggdrasil
// 的 authserver 端点挂在具体站点根下，凭证本身不携带站点信息。服务层持有
// Account（ProviderID 即规范化后的 API 根），因此刷新账号时应优先：
//
//	if siteRefresher, ok := provider.(account.YggdrasilSiteRefresher); ok {
//	    creds, aerr = siteRefresher.RefreshAtSite(ctx, acc.ProviderID, creds, acc.Profile)
//	}
//
// 未使用扩展接口时，Provider 会回退到进程内 token→站点根索引；该索引不跨
// 进程重启，重启后需要服务层传 ProviderID。
type YggdrasilSiteRefresher interface {
	RefreshAtSite(ctx context.Context, siteURL string, creds Credentials, profile Profile) (Credentials, *Error)
}

// YggdrasilSiteValidator 是 Validator 的显式站点根版本，语义同 Validator。
type YggdrasilSiteValidator interface {
	ValidateAtSite(ctx context.Context, siteURL string, creds Credentials) (bool, *Error)
}

// YggdrasilSiteLogouter 是 Logoutger 的显式站点根版本，语义同 Logoutger。
type YggdrasilSiteLogouter interface {
	LogoutAtSite(ctx context.Context, siteURL string, creds Credentials) *Error
}

// RefreshAtSite 实现 YggdrasilSiteRefresher。
func (p *yggdrasilProvider) RefreshAtSite(ctx context.Context, siteURL string, creds Credentials, _ Profile) (Credentials, *Error) {
	apiRoot, nerr := normalizeYggdrasilAPIURL(siteURL)
	if nerr != nil {
		return Credentials{}, newInputError(ErrorCodeInput, "Yggdrasil 服务地址不合法")
	}
	return p.refreshCredentials(ctx, apiRoot, creds)
}

// ValidateAtSite 实现 YggdrasilSiteValidator。
func (p *yggdrasilProvider) ValidateAtSite(ctx context.Context, siteURL string, creds Credentials) (bool, *Error) {
	apiRoot, nerr := normalizeYggdrasilAPIURL(siteURL)
	if nerr != nil {
		return false, newInputError(ErrorCodeInput, "Yggdrasil 服务地址不合法")
	}
	return p.validateAtSite(ctx, apiRoot, creds)
}

// LogoutAtSite 实现 YggdrasilSiteLogouter。
func (p *yggdrasilProvider) LogoutAtSite(ctx context.Context, siteURL string, creds Credentials) *Error {
	apiRoot, nerr := normalizeYggdrasilAPIURL(siteURL)
	if nerr != nil {
		return newInputError(ErrorCodeInput, "Yggdrasil 服务地址不合法")
	}
	return p.logoutAtSite(ctx, apiRoot, creds)
}

// dependencies 返回补齐零值后的依赖。
//
// 构造函数已规范化，这里再兜一次底，保证直接构造 provider 字面量的测试
// 也不会因为 HTTP 为 nil 而 panic。
func (p *yggdrasilProvider) dependencies() Deps {
	return p.deps.normalize()
}

// rememberSite 登记 accessToken → API 根（用于只给凭证的 SPI）。
func (p *yggdrasilProvider) rememberSite(accessToken, apiRoot string) {
	if accessToken == "" || apiRoot == "" {
		return
	}
	p.sitesMu.Lock()
	defer p.sitesMu.Unlock()
	if p.sites == nil {
		p.sites = make(map[string]string, 8)
	}
	if len(p.sites) >= yggdrasilSiteIndexLimit {
		for key := range p.sites {
			delete(p.sites, key)
			break
		}
	}
	p.sites[accessToken] = apiRoot
}

// forgetSite 删除 token 索引（注销或续期换 token 时）。
func (p *yggdrasilProvider) forgetSite(accessToken string) {
	if accessToken == "" {
		return
	}
	p.sitesMu.Lock()
	delete(p.sites, accessToken)
	p.sitesMu.Unlock()
}

// siteForToken 查询 token 对应的 API 根。
func (p *yggdrasilProvider) siteForToken(accessToken string) (string, bool) {
	if accessToken == "" {
		return "", false
	}
	p.sitesMu.Lock()
	defer p.sitesMu.Unlock()
	apiRoot, ok := p.sites[accessToken]
	return apiRoot, ok
}

// ---------------------------------------------------------------------------
// 登录流程
// ---------------------------------------------------------------------------

// yggdrasilFlow 是一次进行中的 Yggdrasil 登录。
//
// 除 ID/Kind 外，所有字段都由 mu 保护；网络请求一律在锁外执行，只在
// 开始/收敛时短暂持锁，保证 Poll、SelectProfile、Cancel、State 可并发调用。
type yggdrasilFlow struct {
	mu   sync.Mutex
	cond *sync.Cond

	id       string
	provider *yggdrasilProvider

	// root 为用户输入规范化后的地址（ALI 发现前）。
	root string

	username string
	// password 只在内存中存活：认证成功、失败、取消或进入待选档案后立即清空。
	password string

	// site 为解析后的站点信息缓存（重试时复用，避免重复 ALI 发现）。
	site *SiteInfo

	state      LoginState
	challenge  LoginChallenge
	profiles   []Profile
	account    *Account
	creds      *Credentials
	errCode    string
	errMessage string

	running   bool
	selecting bool
	canceled  bool

	// accessToken/clientToken 是进入 AwaitingProfile 后执行档案选择所需凭证。
	accessToken string
	clientToken string
}

// 流程实现 LoginFlow 与 ProfileSelector。
var (
	_ LoginFlow       = (*yggdrasilFlow)(nil)
	_ ProfileSelector = (*yggdrasilFlow)(nil)
)

// 来源实现可选的 SPI 与扩展接口。
var (
	_ SiteResolver           = (*yggdrasilProvider)(nil)
	_ Refresher              = (*yggdrasilProvider)(nil)
	_ Validator              = (*yggdrasilProvider)(nil)
	_ Logoutger              = (*yggdrasilProvider)(nil)
	_ YggdrasilSiteRefresher = (*yggdrasilProvider)(nil)
	_ YggdrasilSiteValidator = (*yggdrasilProvider)(nil)
	_ YggdrasilSiteLogouter  = (*yggdrasilProvider)(nil)
)

// ID 返回流程标识。
func (f *yggdrasilFlow) ID() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.id
}

// Kind 返回来源类型。
func (f *yggdrasilFlow) Kind() Kind { return KindYggdrasil }

// State 返回当前状态（无 IO）。
func (f *yggdrasilFlow) State() LoginState {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state
}

// Challenge 返回交互信息（Yggdrasil 不需要用户交互，恒为零值）。
func (f *yggdrasilFlow) Challenge() LoginChallenge {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.challenge
}

// Profiles 返回待选择的档案列表（返回副本，调用方修改不影响流程）。
func (f *yggdrasilFlow) Profiles() []Profile {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.profiles) == 0 {
		return nil
	}
	out := make([]Profile, len(f.profiles))
	copy(out, f.profiles)
	return out
}

// Poll 推进流程并返回最新快照。
//
// 并发语义：同一时刻只有一个 goroutine 真正执行网络阶段，其余 Poll 会等待
// 该阶段结束再返回（因此不会出现两次认证请求）；对终态与 AwaitingProfile
// 直接返回同一快照，不产生任何 IO。
//
// 错误语义：终态失败（凭证错误、无档案、协议错误）通过 LoginOutcome.Failed +
// ErrorCode/ErrorMessage 表达，第二个返回值为 nil；只有「可重试且流程仍然
// 可用」的网络错误会同时返回非 nil *Error，此时状态保持 Pending，调用方可以
// 再次 Poll 重试。
func (f *yggdrasilFlow) Poll(ctx context.Context) (LoginOutcome, *Error) {
	f.mu.Lock()
	f.ensureCondLocked()
	for f.running {
		f.waitLocked()
	}
	if f.state.Terminal() || f.state == LoginStateAwaitingProfile {
		out := f.snapshotLocked()
		f.mu.Unlock()
		return out, nil
	}
	f.running = true
	root := f.root
	username := f.username
	password := f.password
	site := f.site
	f.mu.Unlock()

	resolved := SiteInfo{}
	if site != nil {
		resolved = *site
	}
	var failure *Error
	if site == nil {
		resolved, failure = resolveYggdrasilSite(ctx, f.provider.dependencies(), root)
		if failure == nil {
			// 站点解析成功即缓存：后续认证失败重试时不必重做 ALI 发现。
			f.mu.Lock()
			if !f.canceled && !f.state.Terminal() {
				siteCopy := resolved
				f.site = &siteCopy
			}
			f.mu.Unlock()
		}
	}
	var auth *yggdrasilAuthResult
	if failure == nil {
		auth, failure = f.provider.authenticate(ctx, resolved.APIURL, username, password)
	}
	if failure != nil {
		return f.finishPollFailure(failure)
	}

	var textures yggdrasilTexturesInfo
	if auth.selected != nil {
		textures = f.provider.fetchProfileTextures(ctx, resolved.APIURL, auth.selected.ID)
	}
	return f.finishPollSuccess(resolved, auth, textures)
}

// SelectProfile 实现 ProfileSelector：选定档案后执行一次 refresh 完成登录。
func (f *yggdrasilFlow) SelectProfile(ctx context.Context, profileID string) (LoginOutcome, *Error) {
	normalizedID, nerr := NormalizeMinecraftID(profileID)
	if nerr != nil {
		return f.snapshot(), newInputError(ErrorCodeInput, "所选档案标识格式不正确")
	}
	f.mu.Lock()
	f.ensureCondLocked()
	for f.running || f.selecting {
		f.waitLocked()
	}
	if f.state != LoginStateAwaitingProfile {
		out := f.snapshotLocked()
		f.mu.Unlock()
		return out, newExternalError(ErrorCodeState, "当前登录流程不在选择档案阶段", false)
	}
	chosen, found := Profile{}, false
	for _, candidate := range f.profiles {
		if candidate.ID == normalizedID {
			chosen, found = candidate, true
			break
		}
	}
	if !found {
		out := f.snapshotLocked()
		f.mu.Unlock()
		return out, newInputError(ErrorCodeInput, "所选档案不属于该账号")
	}
	apiRoot := ""
	if f.site != nil {
		apiRoot = f.site.APIURL
	}
	accessToken, clientToken := f.accessToken, f.clientToken
	f.selecting = true
	f.mu.Unlock()

	result, failure := f.provider.refresh(ctx, apiRoot, yggdrasilRefreshWithProfileRequest{
		AccessToken:     accessToken,
		ClientToken:     clientToken,
		SelectedProfile: yggdrasilProfile{ID: chosen.ID, Name: chosen.Name},
		RequestUser:     true,
	})
	profile := chosen
	if failure == nil && result.selected != nil {
		profile = *result.selected
	}
	var textures yggdrasilTexturesInfo
	if failure == nil {
		textures = f.provider.fetchProfileTextures(ctx, apiRoot, profile.ID)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	f.selecting = false
	f.wakeLocked()
	if f.canceled || f.state != LoginStateAwaitingProfile {
		return f.snapshotLocked(), nil
	}
	if failure != nil {
		// 网络类错误保留 AwaitingProfile，让用户可以直接重试选择；
		// 其余错误（凭证失效等）按终态失败处理。
		if failure.Retryable {
			return f.snapshotLocked(), failure
		}
		f.setFailureLocked(failure)
		return f.snapshotLocked(), nil
	}
	applyYggdrasilTextures(&profile, textures)
	creds := &Credentials{
		AccessToken: result.accessToken,
		ClientToken: result.clientToken,
		UserType:    UserTypeMojang,
	}
	f.creds = creds
	f.account = &Account{
		Kind:        KindYggdrasil,
		ProviderID:  apiRoot,
		DisplayName: profile.Name,
		Profile:     profile,
	}
	f.accessToken, f.clientToken = result.accessToken, result.clientToken
	f.profiles = nil
	f.password = ""
	f.errCode, f.errMessage = "", ""
	f.state = LoginStateSucceeded
	f.provider.rememberSite(creds.AccessToken, apiRoot)
	return f.snapshotLocked(), nil
}

// Cancel 取消流程（幂等）；取消后状态固定为 Cancelled。
func (f *yggdrasilFlow) Cancel() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensureCondLocked()
	if f.state.Terminal() {
		return
	}
	f.canceled = true
	f.state = LoginStateCancelled
	f.password = ""
	f.profiles = nil
	f.errCode, f.errMessage = "", ""
	f.wakeLocked()
}

// finishPollFailure 收敛 Poll 的失败结果。
func (f *yggdrasilFlow) finishPollFailure(failure *Error) (LoginOutcome, *Error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.running = false
	f.wakeLocked()
	if f.canceled || f.state.Terminal() {
		return f.snapshotLocked(), nil
	}
	if failure.Retryable {
		return f.snapshotLocked(), failure
	}
	f.setFailureLocked(failure)
	return f.snapshotLocked(), nil
}

// finishPollSuccess 收敛 Poll 的成功结果（单档案成功 / 待选档案 / 无档案）。
func (f *yggdrasilFlow) finishPollSuccess(site SiteInfo, auth *yggdrasilAuthResult, textures yggdrasilTexturesInfo) (LoginOutcome, *Error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.running = false
	f.wakeLocked()
	if f.canceled || f.state.Terminal() {
		return f.snapshotLocked(), nil
	}
	siteCopy := site
	f.site = &siteCopy
	f.accessToken = auth.accessToken
	f.clientToken = auth.clientToken
	f.password = ""
	switch {
	case auth.selected != nil:
		profile := *auth.selected
		applyYggdrasilTextures(&profile, textures)
		creds := &Credentials{
			AccessToken: auth.accessToken,
			ClientToken: auth.clientToken,
			UserType:    UserTypeMojang,
		}
		f.creds = creds
		f.account = &Account{
			Kind:        KindYggdrasil,
			ProviderID:  site.APIURL,
			DisplayName: profile.Name,
			Profile:     profile,
		}
		f.profiles = nil
		f.errCode, f.errMessage = "", ""
		f.state = LoginStateSucceeded
		f.provider.rememberSite(creds.AccessToken, site.APIURL)
	case len(auth.profiles) > 0:
		f.profiles = auth.profiles
		f.state = LoginStateAwaitingProfile
	default:
		f.setFailureLocked(newExternalError(ErrorCodeOwnership, "该账号没有可用的 Minecraft 档案", false))
	}
	return f.snapshotLocked(), nil
}

// setFailureLocked 把流程置为终态失败。
func (f *yggdrasilFlow) setFailureLocked(failure *Error) {
	f.state = LoginStateFailed
	f.errCode = failure.Code
	f.errMessage = failure.Message
	f.password = ""
}

// ensureCondLocked 惰性初始化条件变量（仅兼容直接构造 flow 的测试）。
func (f *yggdrasilFlow) ensureCondLocked() {
	if f.cond == nil {
		f.cond = sync.NewCond(&f.mu)
	}
}

// waitLocked 在条件变量上等待；调用方必须持有 f.mu。
func (f *yggdrasilFlow) waitLocked() {
	if f.cond != nil {
		f.cond.Wait()
	}
}

// wakeLocked 唤醒等待中的 Poll/SelectProfile；调用方必须持有 f.mu。
func (f *yggdrasilFlow) wakeLocked() {
	if f.cond != nil {
		f.cond.Broadcast()
	}
}

// snapshot 返回当前快照（自行加锁）。
func (f *yggdrasilFlow) snapshot() LoginOutcome {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.snapshotLocked()
}

// snapshotLocked 返回当前快照；调用方必须持有 f.mu。
func (f *yggdrasilFlow) snapshotLocked() LoginOutcome {
	out := LoginOutcome{
		State:        f.state,
		Challenge:    f.challenge,
		ErrorCode:    f.errCode,
		ErrorMessage: f.errMessage,
	}
	if len(f.profiles) > 0 {
		out.Profiles = append([]Profile(nil), f.profiles...)
	}
	if f.account != nil {
		account := *f.account
		out.Account = &account
	}
	if f.creds != nil {
		creds := *f.creds
		out.Credentials = &creds
	}
	return out
}

// ---------------------------------------------------------------------------
// 站点解析（ALI + 元数据）
// ---------------------------------------------------------------------------

// resolveYggdrasilSite 执行 ALI 发现并读取站点元数据。
//
// 流程：
//  1. 规范化用户输入地址并 GET 它；
//  2. 若响应头带 X-Authlib-Injector-API-Location，则按「相对/绝对」解析出
//     API 根并再 GET 一次元数据；否则把（重定向后的）输入地址当作 API 根，
//     第一次 GET 的响应体就是元数据（无 ALI 回退）；
//  3. 非 2xx 或 JSON 不合法 → ErrorCodeSite（不可重试）；网络失败保持
//     ErrorCodeNetwork（可重试）。
func resolveYggdrasilSite(ctx context.Context, deps Deps, rawURL string) (SiteInfo, *Error) {
	deps = deps.normalize()
	root, nerr := normalizeYggdrasilAPIURL(rawURL)
	if nerr != nil {
		return SiteInfo{}, nerr
	}
	discovery, finalURL, derr := yggdrasilGet(ctx, deps, root)
	if derr != nil {
		return SiteInfo{}, derr
	}

	apiRoot := root
	base := root
	if finalURL != nil {
		base = finalURL.String()
		if normalized, nerr := normalizeYggdrasilAPIURL(base); nerr == nil {
			apiRoot = normalized
		}
	}

	metadataResponse := discovery
	if location := strings.TrimSpace(discovery.Header.Get(authlibInjectorAPILocationHeader)); location != "" {
		apiRoot, derr = resolveYggdrasilAPILocation(location, base)
		if derr != nil {
			return SiteInfo{}, derr
		}
		var metadataFinalURL *url.URL
		metadataResponse, metadataFinalURL, derr = yggdrasilGet(ctx, deps, apiRoot)
		if derr != nil {
			return SiteInfo{}, derr
		}
		if metadataFinalURL != nil {
			if normalized, nerr := normalizeYggdrasilAPIURL(metadataFinalURL.String()); nerr == nil {
				apiRoot = normalized
			}
		}
	}

	if !isSuccess(metadataResponse.Status) {
		return SiteInfo{}, newExternalError(ErrorCodeSite,
			fmt.Sprintf("Yggdrasil 服务返回异常状态，无法读取站点信息（HTTP %d）", metadataResponse.Status), false)
	}
	if len(bytes.TrimSpace(metadataResponse.Body)) == 0 {
		return SiteInfo{}, newExternalError(ErrorCodeSite, "Yggdrasil 服务没有返回站点元数据", false)
	}
	var metadata yggdrasilMetadata
	if derr := DecodeJSON(metadataResponse, &metadata); derr != nil {
		return SiteInfo{}, newExternalError(ErrorCodeSite, "Yggdrasil 服务元数据无法解析", false)
	}
	return SiteInfo{
		APIURL:             apiRoot,
		ServerName:         metadata.Meta.ServerName,
		ImplementationName: metadata.Meta.ImplementationName,
		NonEmailLogin:      metadata.Meta.Feature.NonEmailLogin,
		SkinDomains:        metadata.SkinDomains,
	}, nil
}

// resolveYggdrasilAPILocation 解析 ALI 响应头（支持相对地址与绝对地址）。
func resolveYggdrasilAPILocation(location, baseRaw string) (string, *Error) {
	trimmed := strings.TrimSpace(location)
	if trimmed == "" {
		return "", newExternalError(ErrorCodeSite, "Yggdrasil 服务没有返回可用的 API 地址", false)
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", newExternalError(ErrorCodeSite, "Yggdrasil 服务返回了无效的 API 地址", false)
	}
	if !parsed.IsAbs() {
		base, berr := url.Parse(baseRaw)
		if berr != nil {
			return "", newExternalError(ErrorCodeSite, "Yggdrasil 服务返回了无效的 API 地址", false)
		}
		parsed = base.ResolveReference(parsed)
	}
	normalized, nerr := normalizeYggdrasilAPIURL(parsed.String())
	if nerr != nil {
		return "", newExternalError(ErrorCodeSite, "Yggdrasil 服务返回的 API 地址不合法", false)
	}
	return normalized, nil
}

// normalizeYggdrasilAPIURL 规范化用户输入的 API 地址。
//
// 规则：去首尾空格；无 scheme 补 https://（"//host" 补 https:）；host 小写；
// 丢弃查询串与片段；强制以 / 结尾；只接受 http/https；拒绝带用户信息的 URL
// （避免把密码写进 ProviderID 与错误文案）。
func normalizeYggdrasilAPIURL(rawURL string) (string, *Error) {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return "", newInputError(ErrorCodeInput, "请输入 Yggdrasil 服务地址")
	}
	if strings.HasPrefix(trimmed, "//") {
		trimmed = "https:" + trimmed
	}
	if !strings.Contains(trimmed, "://") {
		trimmed = "https://" + trimmed
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Host == "" {
		return "", newInputError(ErrorCodeInput, "Yggdrasil 服务地址格式不正确")
	}
	if parsed.User != nil {
		return "", newInputError(ErrorCodeInput, "Yggdrasil 服务地址不能包含用户名或密码")
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", newInputError(ErrorCodeInput, "Yggdrasil 服务地址只支持 http 或 https")
	}
	parsed.Scheme = scheme
	parsed.Host = strings.ToLower(parsed.Host)
	parsed.Fragment = ""
	parsed.RawFragment = ""
	parsed.RawQuery = ""
	if parsed.Path == "" {
		parsed.Path = "/"
	} else if !strings.HasSuffix(parsed.Path, "/") {
		parsed.Path += "/"
	}
	return parsed.String(), nil
}

// yggdrasilGet 发送 GET 请求，并返回（经过重定向后的）最终 URL。
//
// 之所以不直接用 DoGet：ALI 相对地址必须以「重定向之后的最终 URL」为基准
// 解析，而 DoGet 的返回结构不含最终请求地址。这里通过一个记录型 HTTPDoer
// 复用 httpjson.do 的响应体上限与网络错误映射。
func yggdrasilGet(ctx context.Context, deps Deps, rawURL string) (*HTTPResponse, *url.URL, *Error) {
	deps = deps.normalize()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, nil, newExternalError(ErrorCodeSite, "Yggdrasil 服务地址不合法", false)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", deps.UserAgent)
	recorder := &yggdrasilFinalURLRecorder{target: deps.HTTP}
	response, derr := do(recorder, request)
	if derr != nil {
		return nil, nil, derr
	}
	finalURL := recorder.final
	if finalURL == nil {
		finalURL = request.URL
	}
	return response, finalURL, nil
}

// yggdrasilFinalURLRecorder 记录底层客户端返回的最终响应 URL。
type yggdrasilFinalURLRecorder struct {
	target HTTPDoer
	final  *url.URL
}

// Do 实现 HTTPDoer。
func (r *yggdrasilFinalURLRecorder) Do(request *http.Request) (*http.Response, error) {
	response, err := r.target.Do(request)
	if response != nil && response.Request != nil && response.Request.URL != nil {
		r.final = response.Request.URL
	}
	return response, err
}

// ---------------------------------------------------------------------------
// 认证 / 续期 / 校验 / 注销
// ---------------------------------------------------------------------------

// authenticate 调用 /authserver/authenticate。
func (p *yggdrasilProvider) authenticate(ctx context.Context, apiRoot, username, password string) (*yggdrasilAuthResult, *Error) {
	deps := p.dependencies()
	clientToken, cerr := newYggdrasilClientToken()
	if cerr != nil {
		return nil, cerr
	}
	payload := yggdrasilAuthenticateRequest{
		Username:    username,
		Password:    password,
		ClientToken: clientToken,
		RequestUser: true,
		Agent:       yggdrasilAgent{Name: yggdrasilAgentName, Version: yggdrasilAgentVersion},
	}
	response, derr := DoJSON(ctx, deps.HTTP, http.MethodPost, apiRoot+"authserver/authenticate", payload, deps.UserAgent)
	if derr != nil {
		return nil, derr
	}
	if !isSuccess(response.Status) {
		body := decodeYggdrasilErrorBody(response)
		if response.Status == http.StatusForbidden || body.Error == "ForbiddenOperationException" {
			return nil, newAuthError(ErrorCodeAuth, "账号或密码错误")
		}
		return nil, newExternalError(ErrorCodeLogin,
			body.displayMessage(fmt.Sprintf("Yggdrasil 认证失败（HTTP %d）", response.Status)), false)
	}
	var parsed yggdrasilAuthenticateResponse
	if derr := DecodeJSON(response, &parsed); derr != nil {
		return nil, newExternalError(ErrorCodeLogin, "Yggdrasil 服务返回了无法解析的认证结果", false)
	}
	result := &yggdrasilAuthResult{
		accessToken: strings.TrimSpace(parsed.AccessToken),
		clientToken: strings.TrimSpace(parsed.ClientToken),
	}
	if result.accessToken == "" {
		return nil, newExternalError(ErrorCodeLogin, "Yggdrasil 认证响应缺少访问令牌", false)
	}
	if result.clientToken == "" {
		// 个别服务端不回显 clientToken，沿用请求里的值。
		result.clientToken = clientToken
	}
	if parsed.SelectedProfile != nil && strings.TrimSpace(parsed.SelectedProfile.ID) != "" {
		profile, perr := normalizeYggdrasilProfile(*parsed.SelectedProfile)
		if perr != nil {
			return nil, newExternalError(ErrorCodeLogin, "Yggdrasil 服务返回的档案标识不合法", false)
		}
		result.selected = &profile
		return result, nil
	}
	for _, item := range parsed.AvailableProfiles {
		if strings.TrimSpace(item.ID) == "" {
			continue
		}
		profile, perr := normalizeYggdrasilProfile(item)
		if perr != nil {
			continue
		}
		result.profiles = append(result.profiles, profile)
	}
	if len(result.profiles) == 0 && len(parsed.AvailableProfiles) > 0 {
		return nil, newExternalError(ErrorCodeLogin, "Yggdrasil 服务返回的档案标识不合法", false)
	}
	return result, nil
}

// refreshCredentials 用 refresh 端点换一组新凭证。
func (p *yggdrasilProvider) refreshCredentials(ctx context.Context, apiRoot string, creds Credentials) (Credentials, *Error) {
	result, rerr := p.refresh(ctx, apiRoot, yggdrasilRefreshRequest{
		AccessToken: creds.AccessToken,
		ClientToken: creds.ClientToken,
		RequestUser: true,
	})
	if rerr != nil {
		return Credentials{}, rerr
	}
	accessToken := result.accessToken
	if accessToken == "" {
		accessToken = creds.AccessToken
	}
	clientToken := result.clientToken
	if clientToken == "" {
		clientToken = creds.ClientToken
	}
	if accessToken == "" {
		return Credentials{}, newExternalError(ErrorCodeLogin, "Yggdrasil 服务返回了无效的续期结果", false)
	}
	p.forgetSite(creds.AccessToken)
	p.rememberSite(accessToken, apiRoot)
	return Credentials{
		AccessToken:  accessToken,
		ClientToken:  clientToken,
		RefreshToken: "",
		ExpiresAtMs:  0,
		UserType:     UserTypeMojang,
	}, nil
}

// refresh 调用 /authserver/refresh，payload 由调用方决定是否带 selectedProfile。
func (p *yggdrasilProvider) refresh(ctx context.Context, apiRoot string, payload any) (*yggdrasilRefreshResult, *Error) {
	deps := p.dependencies()
	response, derr := DoJSON(ctx, deps.HTTP, http.MethodPost, apiRoot+"authserver/refresh", payload, deps.UserAgent)
	if derr != nil {
		return nil, derr
	}
	if !isSuccess(response.Status) {
		body := decodeYggdrasilErrorBody(response)
		if response.Status == http.StatusUnauthorized || response.Status == http.StatusForbidden ||
			body.Error == "ForbiddenOperationException" ||
			strings.Contains(strings.ToLower(body.ErrorMessage), "invalid token") {
			return nil, newAuthError(ErrorCodeAuth, "登录状态已失效，请重新登录")
		}
		return nil, newExternalError(ErrorCodeHTTP,
			fmt.Sprintf("Yggdrasil 服务刷新凭证失败（HTTP %d）", response.Status), false)
	}
	var parsed yggdrasilRefreshResponse
	if derr := DecodeJSON(response, &parsed); derr != nil {
		return nil, newExternalError(ErrorCodeHTTP, "Yggdrasil 服务返回了无法解析的续期结果", false)
	}
	result := &yggdrasilRefreshResult{
		accessToken: strings.TrimSpace(parsed.AccessToken),
		clientToken: strings.TrimSpace(parsed.ClientToken),
	}
	if parsed.SelectedProfile != nil && strings.TrimSpace(parsed.SelectedProfile.ID) != "" {
		profile, perr := normalizeYggdrasilProfile(*parsed.SelectedProfile)
		if perr != nil {
			return nil, newExternalError(ErrorCodeLogin, "Yggdrasil 服务返回的档案标识不合法", false)
		}
		result.selected = &profile
	}
	return result, nil
}

// validateAtSite 调用 /authserver/validate。
func (p *yggdrasilProvider) validateAtSite(ctx context.Context, apiRoot string, creds Credentials) (bool, *Error) {
	deps := p.dependencies()
	response, derr := DoJSON(ctx, deps.HTTP, http.MethodPost, apiRoot+"authserver/validate",
		yggdrasilValidateRequest{AccessToken: creds.AccessToken, ClientToken: creds.ClientToken}, deps.UserAgent)
	if derr != nil {
		return false, derr
	}
	switch {
	case isSuccess(response.Status):
		return true, nil
	case response.Status == http.StatusUnauthorized || response.Status == http.StatusForbidden:
		return false, nil
	default:
		return false, statusError(response, "Yggdrasil 服务校验凭证失败")
	}
}

// logoutAtSite 调用 /authserver/invalidate；204/403 都视为成功（幂等）。
func (p *yggdrasilProvider) logoutAtSite(ctx context.Context, apiRoot string, creds Credentials) *Error {
	deps := p.dependencies()
	response, derr := DoJSON(ctx, deps.HTTP, http.MethodPost, apiRoot+"authserver/invalidate",
		yggdrasilInvalidateRequest{AccessToken: creds.AccessToken, ClientToken: creds.ClientToken}, deps.UserAgent)
	if derr != nil {
		return derr
	}
	if isSuccess(response.Status) || response.Status == http.StatusUnauthorized || response.Status == http.StatusForbidden {
		p.forgetSite(creds.AccessToken)
		return nil
	}
	return statusError(response, "Yggdrasil 服务注销凭证失败")
}

// newYggdrasilClientToken 生成无连字符小写 UUID 作为 clientToken。
func newYggdrasilClientToken() (string, *Error) {
	id, uerr := NewUUIDv4()
	if uerr != nil {
		return "", uerr
	}
	return strings.ReplaceAll(id, "-", ""), nil
}

// normalizeYggdrasilProfile 把协议档案规范化为内部 Profile（UUID 去连字符小写）。
func normalizeYggdrasilProfile(raw yggdrasilProfile) (Profile, *Error) {
	id, ierr := NormalizeMinecraftID(raw.ID)
	if ierr != nil {
		return Profile{}, ierr
	}
	return Profile{ID: id, Name: strings.TrimSpace(raw.Name)}, nil
}

// ---------------------------------------------------------------------------
// 皮肤 / 披风（sessionserver profile，尽力而为）
// ---------------------------------------------------------------------------

// yggdrasilTexturesInfo 是解码后的纹理信息。
type yggdrasilTexturesInfo struct {
	skinURL   string
	skinModel string
	capeURL   string
}

// fetchProfileTextures 从 sessionserver 读取纹理；任何失败都静默忽略。
func (p *yggdrasilProvider) fetchProfileTextures(ctx context.Context, apiRoot, profileID string) yggdrasilTexturesInfo {
	if apiRoot == "" || profileID == "" {
		return yggdrasilTexturesInfo{}
	}
	deps := p.dependencies()
	endpoint := apiRoot + "sessionserver/session/minecraft/profile/" + url.PathEscape(profileID) + "?unsigned=false"
	response, derr := DoGet(ctx, deps.HTTP, endpoint, nil, deps.UserAgent)
	if derr != nil {
		return yggdrasilTexturesInfo{}
	}
	if !isSuccess(response.Status) {
		return yggdrasilTexturesInfo{}
	}
	var profile yggdrasilSessionProfile
	if derr := DecodeJSON(response, &profile); derr != nil {
		return yggdrasilTexturesInfo{}
	}
	for _, property := range profile.Properties {
		if !strings.EqualFold(property.Name, "textures") {
			continue
		}
		decoded, ok := decodeYggdrasilTexturesProperty(property.Value)
		if !ok {
			return yggdrasilTexturesInfo{}
		}
		return decoded
	}
	return yggdrasilTexturesInfo{}
}

// decodeYggdrasilTexturesProperty 解码 base64 的 textures 属性。
func decodeYggdrasilTexturesProperty(value string) (yggdrasilTexturesInfo, bool) {
	trimmed := strings.TrimSpace(value)
	raw, err := base64.StdEncoding.DecodeString(trimmed)
	if err != nil {
		// 部分服务端省略了 base64 填充，再尝试一次无填充编码。
		raw, err = base64.RawStdEncoding.DecodeString(trimmed)
		if err != nil {
			return yggdrasilTexturesInfo{}, false
		}
	}
	var payload yggdrasilTexturePayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return yggdrasilTexturesInfo{}, false
	}
	info := yggdrasilTexturesInfo{}
	if payload.Textures.SKIN != nil {
		info.skinURL = strings.TrimSpace(payload.Textures.SKIN.URL)
		if info.skinURL != "" {
			info.skinModel = "classic"
			if payload.Textures.SKIN.Metadata != nil &&
				strings.EqualFold(strings.TrimSpace(payload.Textures.SKIN.Metadata.Model), "slim") {
				info.skinModel = "slim"
			}
		}
	}
	if payload.Textures.CAPE != nil {
		info.capeURL = strings.TrimSpace(payload.Textures.CAPE.URL)
	}
	return info, true
}

// applyYggdrasilTextures 把纹理信息写到 Profile 上。
func applyYggdrasilTextures(profile *Profile, textures yggdrasilTexturesInfo) {
	if profile == nil {
		return
	}
	profile.SkinURL = NormalizeTextureURL(textures.skinURL)
	profile.SkinModel = textures.skinModel
	profile.CapeURL = NormalizeTextureURL(textures.capeURL)
}

// ---------------------------------------------------------------------------
// 协议数据结构
// ---------------------------------------------------------------------------

// yggdrasilMetadata 是 API 根返回的元数据。
type yggdrasilMetadata struct {
	Meta struct {
		ServerName            string `json:"serverName"`
		ImplementationName    string `json:"implementationName"`
		ImplementationVersion string `json:"implementationVersion"`
		Feature               struct {
			NonEmailLogin bool `json:"non_email_login"`
		} `json:"feature"`
	} `json:"meta"`
	SkinDomains []string `json:"skinDomains"`
}

// yggdrasilAgent 是认证请求里的 agent 字段。
type yggdrasilAgent struct {
	Name    string `json:"name"`
	Version int    `json:"version"`
}

// yggdrasilAuthenticateRequest 是 /authserver/authenticate 请求体。
type yggdrasilAuthenticateRequest struct {
	Username    string         `json:"username"`
	Password    string         `json:"password"`
	ClientToken string         `json:"clientToken"`
	RequestUser bool           `json:"requestUser"`
	Agent       yggdrasilAgent `json:"agent"`
}

// yggdrasilProfile 是协议里的档案（id/name）。
type yggdrasilProfile struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// yggdrasilAuthenticateResponse 是认证成功响应。
type yggdrasilAuthenticateResponse struct {
	AccessToken       string             `json:"accessToken"`
	ClientToken       string             `json:"clientToken"`
	AvailableProfiles []yggdrasilProfile `json:"availableProfiles"`
	SelectedProfile   *yggdrasilProfile  `json:"selectedProfile"`
}

// yggdrasilAuthResult 是认证成功后的内部结果。
type yggdrasilAuthResult struct {
	accessToken string
	clientToken string
	profiles    []Profile
	selected    *Profile
}

// yggdrasilRefreshRequest 是普通续期请求体。
type yggdrasilRefreshRequest struct {
	AccessToken string `json:"accessToken"`
	ClientToken string `json:"clientToken"`
	RequestUser bool   `json:"requestUser"`
}

// yggdrasilRefreshWithProfileRequest 是「选择档案」续期请求体。
type yggdrasilRefreshWithProfileRequest struct {
	AccessToken     string           `json:"accessToken"`
	ClientToken     string           `json:"clientToken"`
	SelectedProfile yggdrasilProfile `json:"selectedProfile"`
	RequestUser     bool             `json:"requestUser"`
}

// yggdrasilRefreshResponse 是续期响应。
type yggdrasilRefreshResponse struct {
	AccessToken       string             `json:"accessToken"`
	ClientToken       string             `json:"clientToken"`
	SelectedProfile   *yggdrasilProfile  `json:"selectedProfile"`
	AvailableProfiles []yggdrasilProfile `json:"availableProfiles"`
}

// yggdrasilRefreshResult 是续期成功后的内部结果。
type yggdrasilRefreshResult struct {
	accessToken string
	clientToken string
	selected    *Profile
}

// yggdrasilValidateRequest 是 /authserver/validate 请求体。
type yggdrasilValidateRequest struct {
	AccessToken string `json:"accessToken"`
	ClientToken string `json:"clientToken"`
}

// yggdrasilInvalidateRequest 是 /authserver/invalidate 请求体。
type yggdrasilInvalidateRequest struct {
	AccessToken string `json:"accessToken"`
	ClientToken string `json:"clientToken"`
}

// yggdrasilErrorBody 是 Yggdrasil 标准错误体。
type yggdrasilErrorBody struct {
	Error        string `json:"error"`
	ErrorMessage string `json:"errorMessage"`
	Message      string `json:"message"`
}

// decodeYggdrasilErrorBody 解析错误体；解析失败时按空错误体处理（由状态码兜底）。
func decodeYggdrasilErrorBody(response *HTTPResponse) yggdrasilErrorBody {
	var body yggdrasilErrorBody
	if response == nil {
		return body
	}
	_ = json.Unmarshal(response.Body, &body)
	return body
}

// displayMessage 返回脱敏后的服务端文案，并截断到 yggdrasilServerMessageLimit 个字符。
func (b yggdrasilErrorBody) displayMessage(fallback string) string {
	message := strings.TrimSpace(b.ErrorMessage)
	if message == "" {
		message = strings.TrimSpace(b.Message)
	}
	if message == "" {
		return fallback
	}
	replacer := strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ")
	cleaned := strings.TrimSpace(replacer.Replace(message))
	if runes := []rune(cleaned); len(runes) > yggdrasilServerMessageLimit {
		cleaned = string(runes[:yggdrasilServerMessageLimit])
	}
	return cleaned
}

// yggdrasilSessionProfile 是 sessionserver 档案响应。
type yggdrasilSessionProfile struct {
	ID         string                     `json:"id"`
	Name       string                     `json:"name"`
	Properties []yggdrasilProfileProperty `json:"properties"`
}

// yggdrasilProfileProperty 是档案属性（textures 的 value 为 base64 JSON）。
type yggdrasilProfileProperty struct {
	Name      string `json:"name"`
	Value     string `json:"value"`
	Signature string `json:"signature"`
}

// yggdrasilTexturePayload 是 base64 解码后的纹理 JSON。
type yggdrasilTexturePayload struct {
	Textures struct {
		SKIN *struct {
			URL      string `json:"url"`
			Metadata *struct {
				Model string `json:"model"`
			} `json:"metadata"`
		} `json:"SKIN"`
		CAPE *struct {
			URL string `json:"url"`
		} `json:"CAPE"`
	} `json:"textures"`
}
