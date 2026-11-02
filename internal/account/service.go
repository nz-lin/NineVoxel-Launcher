// 本文件实现账号服务层：把「账号来源 SPI」编排成可被界面与启动链路消费的能力。
//
// 职责边界：
//   - 登录会话管理（创建/轮询/选档案/取消，含过期清理）；
//   - 账号持久化编排（登录成功后落库、去重、自动选中第一个账号）；
//   - 账号操作（列表/选中/删除/刷新/取凭证）；
//   - 事件发射（account.changed.v1；先落库再发事件）。
//
// 本文件不依赖 Wails：Wails 绑定壳在 wails.go，二者必须保持「服务层可单测」。
package account

import (
	"context"
	"sort"
	"sync"
	"time"

	"example.com/ninevoxel/launcher/internal/events"
)

// DefaultSessionTTL 是登录会话的存活时间。
//
// 微软设备码默认 15 分钟过期；会话再留一点余量没有意义，过期后重新开始即可。
const DefaultSessionTTL = 15 * time.Minute

// ServiceOptions 是构造账号服务的依赖。
type ServiceOptions struct {
	// Store 为账号存储；必填，nil 时服务不可用（所有方法返回 STORAGE 错误）。
	Store Store

	// Registry 为账号来源注册表；nil 时使用 NewRegistry(DefaultDeps())。
	Registry *Registry

	// Emitter 为事件发射器；nil 时只做落库不发事件（测试与命令行场景）。
	Emitter *events.Emitter

	// Now 为时钟；nil 时使用 time.Now。
	Now func() time.Time

	// SessionTTL 为登录会话存活时间；0 时使用 DefaultSessionTTL。
	SessionTTL time.Duration
}

// Service 是账号服务本体。
//
// 并发模型：
//   - sessions 由 mu 保护；
//   - 每个会话有独立的 pollMu，保证同一个流程不会被并发轮询两次；
//   - Store 实现自行保证数据库并发安全。
type Service struct {
	store    Store
	registry *Registry
	emitter  *events.Emitter
	now      func() time.Time
	ttl      time.Duration

	mu       sync.Mutex
	sessions map[string]*loginSession
}

// loginSession 是一次登录在服务层的记录。
type loginSession struct {
	// pollMu 串行化 Poll/SelectProfile，避免同一流程并发发起两次网络请求。
	pollMu sync.Mutex

	id      string
	kind    Kind
	flow    LoginFlow
	created time.Time

	// outline 缓存最近一次 Poll 的结果，供终态幂等返回。
	outline LoginOutcome

	// accountID 为落库后的本地账号 id（成功且已落库时非空）。
	accountID string
}

// LoginSession 是登录会话的对外快照（可安全序列化给前端）。
//
// 与 LoginOutcome 的区别：这里带会话 id 与账号摘要，且**永远不含凭证**。
type LoginSession struct {
	// SessionID 为登录会话标识（后续 Poll/SelectProfile/Cancel 都要带上）。
	SessionID string `json:"sessionId"`

	// Kind 为账号来源类型。
	Kind Kind `json:"kind"`

	// State 为当前状态。
	State LoginState `json:"state"`

	// Challenge 为需要展示给用户的交互信息（设备码等）。
	Challenge LoginChallenge `json:"challenge"`

	// Profiles 为待选择的档案（AwaitingProfile 时非空）。
	Profiles []Profile `json:"profiles,omitempty"`

	// Account 为成功后的账号摘要。
	Account *Account `json:"account,omitempty"`

	// ErrorCode 为失败时的稳定错误码。
	ErrorCode string `json:"errorCode,omitempty"`

	// ErrorMessage 为失败时的中文可读文案。
	ErrorMessage string `json:"errorMessage,omitempty"`
}

// NewService 构造账号服务。
func NewService(options ServiceOptions) *Service {
	now := options.Now
	if now == nil {
		now = time.Now
	}
	ttl := options.SessionTTL
	if ttl <= 0 {
		ttl = DefaultSessionTTL
	}
	registry := options.Registry
	if registry == nil {
		registry = NewRegistry(DefaultDeps())
	}
	return &Service{
		store:    options.Store,
		registry: registry,
		emitter:  options.Emitter,
		now:      now,
		ttl:      ttl,
		sessions: make(map[string]*loginSession, 4),
	}
}

// ListProviders 返回全部账号来源的静态描述。
func (s *Service) ListProviders() []ProviderInfo {
	if s == nil || s.registry == nil {
		return nil
	}
	return s.registry.List()
}

// ListAccounts 返回全部已保存账号（当前选中账号排在最前）。
func (s *Service) ListAccounts(ctx context.Context) ([]Account, *Error) {
	if s.store == nil {
		return nil, newSystemError(ErrorCodeStorage, "账号存储不可用")
	}
	stored, listErr := s.store.List(ctx)
	if listErr != nil {
		return nil, toAccountError(listErr, "读取账号列表失败")
	}
	accounts := make([]Account, 0, len(stored))
	for _, item := range stored {
		accounts = append(accounts, item.Account)
	}
	sortAccounts(accounts)
	return accounts, nil
}

// GetAccount 按 id 返回账号摘要。
func (s *Service) GetAccount(ctx context.Context, accountID string) (Account, *Error) {
	stored, getErr := s.loadStored(ctx, accountID)
	if getErr != nil {
		return Account{}, getErr
	}
	return stored.Account, nil
}

// StartLogin 开始一次登录。
//
// 成功后立即返回会话快照：离线账号会直接是 succeeded（服务层已落库）；
// 微软账号返回 awaiting_user，调用方需要展示设备码并轮询 PollLogin。
func (s *Service) StartLogin(ctx context.Context, kind Kind, req LoginRequest) (LoginSession, *Error) {
	provider, providerErr := s.providerOf(kind)
	if providerErr != nil {
		return LoginSession{}, providerErr
	}

	if s.store == nil {
		return LoginSession{}, newSystemError(ErrorCodeStorage, "账号存储不可用")
	}

	flow, startErr := provider.StartLogin(ctx, req)
	if startErr != nil {
		return LoginSession{}, startErr
	}
	if flow == nil {
		return LoginSession{}, newSystemError(ErrorCodeLogin, "账号来源未返回登录流程")
	}

	session := &loginSession{
		id:      flow.ID(),
		kind:    kind,
		flow:    flow,
		created: s.now(),
		outline: LoginOutcome{
			State:     flow.State(),
			Challenge: flow.Challenge(),
			Profiles:  flow.Profiles(),
		},
	}
	if session.id == "" {
		return LoginSession{}, newSystemError(ErrorCodeState, "账号来源返回了无效的登录会话")
	}

	s.mu.Lock()
	s.cleanupExpiredLocked()
	s.sessions[session.id] = session
	s.mu.Unlock()

	// 同步完成的来源（离线）直接落库并返回成功。
	if session.flow.State() == LoginStateSucceeded {
		return s.finishSession(ctx, session)
	}
	return s.viewOf(session), nil
}

// PollLogin 推进登录流程并返回最新快照。
//
// 网络类错误会原样返回（可重试），会话保持原状；终态重复调用是幂等的。
func (s *Service) PollLogin(ctx context.Context, sessionID string) (LoginSession, *Error) {
	session, findErr := s.sessionByID(sessionID)
	if findErr != nil {
		return LoginSession{}, findErr
	}

	session.pollMu.Lock()
	defer session.pollMu.Unlock()

	// 终态幂等：成功但尚未落库（上一次落库失败）时补一次落库。
	if session.flow.State() == LoginStateSucceeded {
		if session.accountID == "" {
			return s.finishSession(ctx, session)
		}
		return s.viewOf(session), nil
	}
	if session.flow.State() == LoginStateCancelled || session.flow.State() == LoginStateFailed {
		return s.viewOf(session), nil
	}

	outcome, pollErr := session.flow.Poll(ctx)
	if pollErr != nil {
		// 终止性失败（用户拒绝/设备码过期/XSTS 拒绝等）统一归一化成「失败快照」，
		// 方便界面按 ErrorCode 渲染；只有可重试的流程性错误才以 error 形式抛出。
		// 两种来源风格都兼容：有的来源用 error 表达失败，有的用 LoginOutcome。
		if session.flow.State() == LoginStateFailed {
			session.outline = LoginOutcome{
				State:        LoginStateFailed,
				ErrorCode:    pollErr.Code,
				ErrorMessage: pollErr.Message,
			}
			return s.viewOf(session), nil
		}
		// 保留会话，让界面可以重试；错误文案已由来源脱敏。
		return LoginSession{}, pollErr
	}
	session.outline = outcome

	if outcome.State == LoginStateSucceeded {
		return s.finishSession(ctx, session)
	}
	return s.viewOf(session), nil
}

// SelectProfile 为「需要选择档案」的登录流程指定档案并继续。
func (s *Service) SelectProfile(ctx context.Context, sessionID, profileID string) (LoginSession, *Error) {
	session, findErr := s.sessionByID(sessionID)
	if findErr != nil {
		return LoginSession{}, findErr
	}

	session.pollMu.Lock()
	defer session.pollMu.Unlock()

	selector, ok := session.flow.(ProfileSelector)
	if !ok {
		return LoginSession{}, newInputError(ErrorCodeState, "当前登录流程不需要选择档案")
	}
	if session.flow.State() != LoginStateAwaitingProfile {
		return LoginSession{}, newInputError(ErrorCodeState, "当前登录流程不在选择档案阶段")
	}

	outcome, selectErr := selector.SelectProfile(ctx, profileID)
	if selectErr != nil {
		return LoginSession{}, selectErr
	}
	session.outline = outcome
	if outcome.State == LoginStateSucceeded {
		return s.finishSession(ctx, session)
	}
	return s.viewOf(session), nil
}

// CancelLogin 取消会话并从会话表移除（幂等）。
func (s *Service) CancelLogin(sessionID string) (LoginSession, *Error) {
	s.mu.Lock()
	session, ok := s.sessions[sessionID]
	if ok {
		delete(s.sessions, sessionID)
	}
	s.mu.Unlock()
	if !ok {
		return LoginSession{}, newInputError(ErrorCodeState, "登录会话不存在或已结束")
	}
	session.flow.Cancel()
	view := s.viewOf(session)
	view.State = LoginStateCancelled
	return view, nil
}

// SelectAccount 把指定账号设为当前账号。
func (s *Service) SelectAccount(ctx context.Context, accountID string) ([]Account, *Error) {
	if _, getErr := s.loadStored(ctx, accountID); getErr != nil {
		return nil, getErr
	}
	nowMs := s.nowUTC().UnixMilli()
	if err := s.store.SetSelected(ctx, accountID, nowMs); err != nil {
		return nil, toAccountError(err, "保存当前账号失败")
	}
	s.emitChanged(ctx, events.AccountActionSelected, accountID)
	return s.ListAccounts(ctx)
}

// DeleteAccount 删除账号并返回剩余列表。
//
// 服务端注销是「尽力而为」：即使 Logout 失败也要删掉本地账号，否则用户会被
// 一个无法使用的账号卡住。
func (s *Service) DeleteAccount(ctx context.Context, accountID string) ([]Account, *Error) {
	stored, getErr := s.loadStored(ctx, accountID)
	if getErr != nil {
		return nil, getErr
	}

	if provider, ok := s.registry.Get(stored.Kind); ok {
		// 优先使用「带站点根」的扩展接口：Yggdrasil 的注销端点依赖账号所属站点，
		// 跨进程重启后仅靠凭证无法反推站点，必须显式传 Account.ProviderID。
		if siteLogouter, ok := provider.(YggdrasilSiteLogouter); ok && stored.ProviderID != "" {
			// 忽略注销失败：本地删除优先。
			_ = siteLogouter.LogoutAtSite(ctx, stored.ProviderID, stored.Credentials)
		} else if logoutger, ok := provider.(Logoutger); ok {
			// 忽略注销失败：本地删除优先。
			_ = logoutger.Logout(ctx, stored.Credentials)
		}
	}

	if err := s.store.Delete(ctx, accountID); err != nil {
		return nil, toAccountError(err, "删除账号失败")
	}

	// 删除的是当前账号时，自动选中最近使用的一个，避免界面出现「无当前账号」。
	accounts, listErr := s.ListAccounts(ctx)
	if listErr != nil {
		return nil, listErr
	}
	hasSelected := false
	for _, item := range accounts {
		if item.Selected {
			hasSelected = true
			break
		}
	}
	if !hasSelected && len(accounts) > 0 {
		nowMs := s.nowUTC().UnixMilli()
		if err := s.store.SetSelected(ctx, accounts[0].ID, nowMs); err != nil {
			return nil, toAccountError(err, "更新当前账号失败")
		}
		if accounts, listErr = s.ListAccounts(ctx); listErr != nil {
			return nil, listErr
		}
	}

	s.emitChanged(ctx, events.AccountActionDeleted, accountID)
	return accounts, nil
}

// RefreshAccount 刷新账号凭证（仅对实现了 Refresher 的来源有效）。
func (s *Service) RefreshAccount(ctx context.Context, accountID string) (Account, *Error) {
	stored, getErr := s.loadStored(ctx, accountID)
	if getErr != nil {
		return Account{}, getErr
	}

	credentials, refreshErr := s.refreshCredentials(ctx, stored)
	if refreshErr != nil {
		return Account{}, refreshErr
	}

	stored.Credentials = credentials
	stored.UpdatedAtMs = s.nowUTC().UnixMilli()
	if err := s.store.Upsert(ctx, stored); err != nil {
		return Account{}, toAccountError(err, "保存刷新后的凭证失败")
	}
	s.emitChanged(ctx, events.AccountActionUpdated, stored.ID)
	return stored.Account, nil
}

// ResolveCredentials 返回启动游戏所需的档案与凭证。
//
// 这是启动链路（internal/launch）与账号子系统的唯一衔接点：
//   - 凭证即将过期且来源支持刷新时，先刷新再返回；
//   - 刷新失败原样返回错误（界面应提示重新登录）；
//   - 本方法**不得**作为 Wails 绑定暴露（凭证只在后端流转）。
func (s *Service) ResolveCredentials(ctx context.Context, accountID string) (Profile, Credentials, *Error) {
	stored, getErr := s.loadStored(ctx, accountID)
	if getErr != nil {
		return Profile{}, Credentials{}, getErr
	}

	nowMs := s.nowUTC().UnixMilli()
	const refreshAheadMs = 60 * 1000
	if stored.Credentials.ExpiresAtMs > 0 && nowMs+refreshAheadMs >= stored.Credentials.ExpiresAtMs {
		refreshed, refreshErr := s.refreshCredentials(ctx, stored)
		if refreshErr != nil {
			return Profile{}, Credentials{}, refreshErr
		}
		stored.Credentials = refreshed
		stored.UpdatedAtMs = nowMs
		if err := s.store.Upsert(ctx, stored); err != nil {
			return Profile{}, Credentials{}, toAccountError(err, "保存刷新后的凭证失败")
		}
		s.emitChanged(ctx, events.AccountActionUpdated, stored.ID)
	}
	return stored.Profile, stored.Credentials, nil
}

// ResolveYggdrasilSite 解析用户输入的自定义 Yggdrasil 地址。
func (s *Service) ResolveYggdrasilSite(ctx context.Context, rawURL string) (SiteInfo, *Error) {
	provider, providerErr := s.providerOf(KindYggdrasil)
	if providerErr != nil {
		return SiteInfo{}, providerErr
	}
	resolver, ok := provider.(SiteResolver)
	if !ok {
		return SiteInfo{}, newSystemError(ErrorCodeState, "Yggdrasil 来源不支持地址解析")
	}
	return resolver.ResolveSite(ctx, rawURL)
}

// ===== 内部实现 =====

// refreshCredentials 按来源支持的能力刷新凭证。
//
// 优先使用「带站点根」的扩展接口：Yggdrasil 的刷新端点依赖账号所属站点，
// 而 SPI 的 Refresher 只收到凭证与档案；跨进程重启后必须靠 Account.ProviderID。
// 其他来源（微软）继续走标准 Refresher。
func (s *Service) refreshCredentials(ctx context.Context, stored StoredAccount) (Credentials, *Error) {
	provider, providerErr := s.providerOf(stored.Kind)
	if providerErr != nil {
		return Credentials{}, providerErr
	}
	if siteRefresher, ok := provider.(YggdrasilSiteRefresher); ok && stored.ProviderID != "" {
		return siteRefresher.RefreshAtSite(ctx, stored.ProviderID, stored.Credentials, stored.Profile)
	}
	if refresher, ok := provider.(Refresher); ok {
		return refresher.Refresh(ctx, stored.Credentials, stored.Profile)
	}
	return Credentials{}, newInputError(ErrorCodeState, "该账号类型不支持刷新")
}

// providerOf 按类型取已注册来源。
func (s *Service) providerOf(kind Kind) (Provider, *Error) {
	if s == nil || s.registry == nil {
		return nil, newSystemError(ErrorCodeProviderUnknown, "账号来源注册表不可用")
	}
	if !kind.Valid() {
		return nil, newInputError(ErrorCodeProviderUnknown, "未知的账号类型")
	}
	provider, ok := s.registry.Get(kind)
	if !ok {
		return nil, newInputError(ErrorCodeProviderUnknown, "该账号类型尚未注册")
	}
	return provider, nil
}

// loadStored 读取账号记录（不存在时返回 ACCOUNT_NOT_FOUND_001）。
func (s *Service) loadStored(ctx context.Context, accountID string) (StoredAccount, *Error) {
	if s.store == nil {
		return StoredAccount{}, newSystemError(ErrorCodeStorage, "账号存储不可用")
	}
	if accountID == "" {
		return StoredAccount{}, newInputError(ErrorCodeInput, "缺少账号标识")
	}
	stored, err := s.store.Get(ctx, accountID)
	if err != nil {
		return StoredAccount{}, toAccountError(err, "读取账号失败")
	}
	if stored == nil {
		return StoredAccount{}, newInputError(ErrorCodeNotFound, "账号不存在或已被删除")
	}
	return *stored, nil
}

// sessionByID 取登录会话；未知或已过期时返回稳定错误。
func (s *Service) sessionByID(sessionID string) (*loginSession, *Error) {
	if sessionID == "" {
		return nil, newInputError(ErrorCodeInput, "缺少登录会话标识")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupExpiredLocked()
	session, ok := s.sessions[sessionID]
	if !ok {
		return nil, newInputError(ErrorCodeState, "登录会话不存在或已过期")
	}
	return session, nil
}

// cleanupExpiredLocked 清理过期会话（调用方必须持有 s.mu）。
func (s *Service) cleanupExpiredLocked() {
	if len(s.sessions) == 0 {
		return
	}
	deadline := s.now().Add(-s.ttl)
	for id, session := range s.sessions {
		if session.created.Before(deadline) {
			session.flow.Cancel()
			delete(s.sessions, id)
		}
	}
}

// finishSession 在登录成功后落库并返回成功快照。
func (s *Service) finishSession(ctx context.Context, session *loginSession) (LoginSession, *Error) {
	// 成功可能来自两种路径：
	//   - flow 在 StartLogin 返回时已成功（离线），outline 只有 State，需要补一次 Poll；
	//   - flow 在 Poll/SelectProfile 中成功，outline 已带账号与凭证。
	// 判据用「是否已有账号与凭证」而不是 State：State 为 succeeded 但字段为空
	// 正是离线路径的形态。
	if session.outline.Account == nil || session.outline.Credentials == nil {
		outcome, pollErr := session.flow.Poll(ctx)
		if pollErr != nil {
			return LoginSession{}, pollErr
		}
		session.outline = outcome
	}
	if session.outline.Account == nil || session.outline.Credentials == nil {
		return LoginSession{}, newSystemError(ErrorCodeLogin, "登录结果缺少账号信息")
	}

	account, persistErr := s.persistAccount(ctx, *session.outline.Account, *session.outline.Credentials)
	if persistErr != nil {
		return LoginSession{}, persistErr
	}
	session.accountID = account.ID
	session.outline.Account = &account
	return s.viewOf(session), nil
}

// persistAccount 把登录结果写入存储：同身份账号更新、否则新建并按需自动选中。
//
// 身份由 (Kind, ProviderID, Profile.ID) 决定：玩家名可改，不能参与去重。
func (s *Service) persistAccount(ctx context.Context, account Account, credentials Credentials) (Account, *Error) {
	nowMs := s.nowUTC().UnixMilli()

	existing, listErr := s.store.List(ctx)
	if listErr != nil {
		return Account{}, toAccountError(listErr, "读取账号列表失败")
	}

	identity := account.Identity()
	var matched *Account
	for index := range existing {
		candidate := existing[index]
		if candidate.Identity() == identity {
			matchedAccount := candidate.Account
			matched = &matchedAccount
			break
		}
	}

	item := StoredAccount{Account: account, Credentials: credentials}
	if matched != nil {
		item.ID = matched.ID
		item.CreatedAtMs = matched.CreatedAtMs
		item.LastUsedAtMs = matched.LastUsedAtMs
		item.Selected = matched.Selected
	} else {
		id, idErr := NewUUIDv4()
		if idErr != nil {
			return Account{}, idErr
		}
		item.ID = id
		item.CreatedAtMs = nowMs
	}
	item.UpdatedAtMs = nowMs
	if item.DisplayName == "" {
		item.DisplayName = item.Profile.Name
	}

	if err := s.store.Upsert(ctx, item); err != nil {
		return Account{}, toAccountError(err, "保存账号失败")
	}

	action := events.AccountActionUpdated
	if matched == nil {
		action = events.AccountActionCreated
	}

	// 第一个账号自动成为当前账号；否则保持用户已有的选择。
	if matched == nil && !hasSelectedAccount(existing) {
		if err := s.store.SetSelected(ctx, item.ID, nowMs); err != nil {
			return Account{}, toAccountError(err, "保存当前账号失败")
		}
		item.Selected = true
		item.LastUsedAtMs = nowMs
	} else if matched != nil {
		item.Selected = matched.Selected
	}

	s.emitChanged(ctx, action, item.ID)
	return item.Account, nil
}

// hasSelectedAccount 判断列表中是否已有选中账号。
func hasSelectedAccount(accounts []StoredAccount) bool {
	for _, item := range accounts {
		if item.Selected {
			return true
		}
	}
	return false
}

// viewOf 把内部会话转换为对外快照。
func (s *Service) viewOf(session *loginSession) LoginSession {
	view := LoginSession{
		SessionID:    session.id,
		Kind:         session.kind,
		State:        session.flow.State(),
		Challenge:    session.flow.Challenge(),
		Profiles:     session.flow.Profiles(),
		ErrorCode:    session.outline.ErrorCode,
		ErrorMessage: session.outline.ErrorMessage,
	}
	// 终态以缓存的 outcome 为准（flow.State 可能已被 Cancel 覆盖）。
	if session.outline.State != "" {
		view.State = session.outline.State
	}
	if len(session.outline.Profiles) > 0 {
		view.Profiles = session.outline.Profiles
	}
	if session.outline.Account != nil {
		account := *session.outline.Account
		view.Account = &account
	}
	return view
}

// emitChanged 发射账号变更事件（先落库后发事件由调用顺序保证）。
//
// 事件发射失败不影响业务：前端仍可通过 ListAccounts 恢复状态。
func (s *Service) emitChanged(ctx context.Context, action, accountID string) {
	if s.emitter == nil || s.store == nil {
		return
	}
	stored, err := s.store.List(ctx)
	if err != nil {
		return
	}
	selectedID := ""
	for _, item := range stored {
		if item.Selected {
			selectedID = item.ID
			break
		}
	}
	_, _ = events.Emit(s.emitter, events.EventAccountChanged, events.AccountChangedPayload{
		AccountID:         accountID,
		Action:            action,
		AccountCount:      len(stored),
		SelectedAccountID: selectedID,
	})
}

// nowUTC 返回 UTC 时间（账号时间戳统一用 UTC 毫秒）。
func (s *Service) nowUTC() time.Time {
	if s.now == nil {
		return time.Now().UTC()
	}
	return s.now().UTC()
}

// toAccountError 把底层错误统一成账号错误。
//
// Store 实现返回的 *Error 直接透传（保留错误码）；其他错误包装成存储错误。
func toAccountError(err error, message string) *Error {
	if err == nil {
		return nil
	}
	if accountErr, ok := err.(*Error); ok {
		return accountErr
	}
	return newSystemError(ErrorCodeStorage, message)
}

// sortAccounts 排序：选中账号最前，其余按最近使用时间倒序，再按创建时间倒序。
func sortAccounts(accounts []Account) {
	sort.SliceStable(accounts, func(i, j int) bool {
		left, right := accounts[i], accounts[j]
		if left.Selected != right.Selected {
			return left.Selected
		}
		if left.LastUsedAtMs != right.LastUsedAtMs {
			return left.LastUsedAtMs > right.LastUsedAtMs
		}
		return left.CreatedAtMs > right.CreatedAtMs
	})
}
