// 本文件用「内存存储 + 假来源」验证账号服务层的编排逻辑，
// 不依赖 Wails、不联网、不碰真实数据库。
package account

import (
	"context"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// ===== 测试替身：内存存储 =====

// memoryStore 是 Store 的内存实现，语义与 SQLite 实现保持一致：
// 身份唯一（Kind+ProviderID+ProfileID）、选中唯一、Upsert 保留创建时间。
type memoryStore struct {
	mu    sync.Mutex
	items map[string]StoredAccount
}

func newMemoryStore() *memoryStore {
	return &memoryStore{items: make(map[string]StoredAccount)}
}

func (m *memoryStore) List(_ context.Context) ([]StoredAccount, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	list := make([]StoredAccount, 0, len(m.items))
	for _, item := range m.items {
		list = append(list, item)
	}
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].Selected != list[j].Selected {
			return list[i].Selected
		}
		return list[i].LastUsedAtMs > list[j].LastUsedAtMs
	})
	return list, nil
}

func (m *memoryStore) Get(_ context.Context, id string) (*StoredAccount, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	item, ok := m.items[id]
	if !ok {
		return nil, nil
	}
	return &item, nil
}

func (m *memoryStore) Upsert(_ context.Context, item StoredAccount) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	// 身份冲突时更新已有行（模拟唯一索引 ON CONFLICT）。
	for id, existing := range m.items {
		if existing.Identity() == item.Identity() && id != item.ID {
			item.CreatedAtMs = existing.CreatedAtMs
			item.Selected = existing.Selected
			delete(m.items, id)
			m.items[item.ID] = item
			return nil
		}
	}
	if existing, ok := m.items[item.ID]; ok {
		item.CreatedAtMs = existing.CreatedAtMs
		item.Selected = existing.Selected
	}
	m.items[item.ID] = item
	return nil
}

func (m *memoryStore) Delete(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.items, id)
	return nil
}

func (m *memoryStore) SetSelected(_ context.Context, id string, atMs int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.items[id]; !ok {
		return newInputError(ErrorCodeNotFound, "账号不存在")
	}
	for key, item := range m.items {
		if key == id {
			item.Selected = true
			item.LastUsedAtMs = atMs
		} else {
			item.Selected = false
		}
		m.items[key] = item
	}
	return nil
}

func (m *memoryStore) Touch(_ context.Context, id string, atMs int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	item, ok := m.items[id]
	if !ok {
		return newInputError(ErrorCodeNotFound, "账号不存在")
	}
	item.LastUsedAtMs = atMs
	m.items[id] = item
	return nil
}

func (m *memoryStore) Close() error { return nil }

// seed 直接写入账号（供刷新/凭证相关测试构造前置状态）。
func (m *memoryStore) seed(item StoredAccount) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.items[item.ID] = item
}

// ===== 测试替身：可编排的账号来源 =====

// scriptedFlow 是按步骤返回结果的登录流程。
type scriptedFlow struct {
	mu       sync.Mutex
	id       string
	kind     Kind
	state    LoginState
	chall    LoginChallenge
	profiles []Profile
	steps    []func(context.Context) (LoginOutcome, *Error)
	last     LoginOutcome
	selector func(context.Context, string) (LoginOutcome, *Error)
}

func (f *scriptedFlow) ID() string                { return f.id }
func (f *scriptedFlow) Kind() Kind                { return f.kind }
func (f *scriptedFlow) State() LoginState         { f.mu.Lock(); defer f.mu.Unlock(); return f.state }
func (f *scriptedFlow) Challenge() LoginChallenge { f.mu.Lock(); defer f.mu.Unlock(); return f.chall }
func (f *scriptedFlow) Profiles() []Profile       { f.mu.Lock(); defer f.mu.Unlock(); return f.profiles }

func (f *scriptedFlow) Poll(ctx context.Context) (LoginOutcome, *Error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.state.Terminal() && f.state != LoginStatePending {
		return f.last, nil
	}
	if f.state == LoginStateAwaitingProfile {
		return f.last, nil
	}
	if len(f.steps) == 0 {
		return f.last, nil
	}
	step := f.steps[0]
	f.steps = f.steps[1:]
	outcome, err := step(ctx)
	if err != nil {
		return LoginOutcome{}, err
	}
	f.last = outcome
	f.state = outcome.State
	return outcome, nil
}

func (f *scriptedFlow) Cancel() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.state = LoginStateCancelled
}

func (f *scriptedFlow) SelectProfile(ctx context.Context, profileID string) (LoginOutcome, *Error) {
	f.mu.Lock()
	selector := f.selector
	f.mu.Unlock()
	if selector == nil {
		return LoginOutcome{}, newInputError(ErrorCodeState, "不支持选择档案")
	}
	outcome, err := selector(ctx, profileID)
	if err != nil {
		return LoginOutcome{}, err
	}
	f.mu.Lock()
	f.last = outcome
	f.state = outcome.State
	f.mu.Unlock()
	return outcome, nil
}

// fakeProvider 是可控来源。
type fakeProvider struct {
	info  ProviderInfo
	start func(context.Context, LoginRequest) (LoginFlow, *Error)

	refresh  func(context.Context, Credentials, Profile) (Credentials, *Error)
	validate func(context.Context, Credentials) (bool, *Error)
}

func (p *fakeProvider) Info() ProviderInfo { return p.info }

func (p *fakeProvider) StartLogin(ctx context.Context, req LoginRequest) (LoginFlow, *Error) {
	if p.start == nil {
		return nil, newSystemError(ErrorCodeProviderUnavailable, "未配置登录行为")
	}
	return p.start(ctx, req)
}

func (p *fakeProvider) Refresh(ctx context.Context, creds Credentials, profile Profile) (Credentials, *Error) {
	if p.refresh == nil {
		return Credentials{}, newSystemError(ErrorCodeState, "未配置刷新行为")
	}
	return p.refresh(ctx, creds, profile)
}

func (p *fakeProvider) Validate(ctx context.Context, creds Credentials) (bool, *Error) {
	if p.validate == nil {
		return true, nil
	}
	return p.validate(ctx, creds)
}

// ===== 测试辅助 =====

// testClock 是可推进的时钟。
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func newTestClock() *testClock {
	return &testClock{now: time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)}
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// newTestService 构造带内存存储与给定来源的服务。
func newTestService(t *testing.T, clock *testClock, providers ...Provider) (*Service, *memoryStore) {
	t.Helper()
	store := newMemoryStore()
	service := NewService(ServiceOptions{
		Store:      store,
		Registry:   NewRegistryFromProviders(providers...),
		Now:        clock.Now,
		SessionTTL: 10 * time.Minute,
	})
	return service, store
}

// offlineProfileFor 构造一个离线账号结果。
func offlineProfileFor(name string) (Account, Credentials) {
	profile := Profile{ID: OfflinePlayerUUID(name), Name: name}
	return Account{Kind: KindOffline, DisplayName: name, Profile: profile}, Credentials{UserType: UserTypeLegacy}
}

// ===== 用例 =====

// TestServiceOfflineLoginPersists 验证离线登录直接落库并自动选中。
func TestServiceOfflineLoginPersists(t *testing.T) {
	clock := newTestClock()
	service, store := newTestService(t, clock, NewOfflineProvider(DefaultDeps()))

	session, err := service.StartLogin(context.Background(), KindOffline, LoginRequest{Username: "Steve"})
	if err != nil {
		t.Fatalf("离线登录失败：%v", err)
	}
	if session.State != LoginStateSucceeded {
		t.Fatalf("期望直接成功，实际 %s", session.State)
	}
	if session.Account == nil || session.Account.ID == "" {
		t.Fatal("成功会话必须带已落库的账号")
	}
	if !session.Account.Selected {
		t.Fatal("第一个账号应自动成为当前账号")
	}

	accounts, listErr := service.ListAccounts(context.Background())
	if listErr != nil {
		t.Fatalf("列出账号失败：%v", listErr)
	}
	if len(accounts) != 1 {
		t.Fatalf("期望 1 个账号，实际 %d", len(accounts))
	}
	items, _ := store.List(context.Background())
	if len(items) != 1 || items[0].Credentials.UserType != UserTypeLegacy {
		t.Fatal("账号凭证未正确落库")
	}
}

// TestServiceDuplicateLoginUpdates 验证同身份账号不会重复创建。
func TestServiceDuplicateLoginUpdates(t *testing.T) {
	clock := newTestClock()
	service, store := newTestService(t, clock, NewOfflineProvider(DefaultDeps()))

	first, err := service.StartLogin(context.Background(), KindOffline, LoginRequest{Username: "Alex"})
	if err != nil {
		t.Fatalf("首次登录失败：%v", err)
	}
	clock.Advance(time.Minute)
	second, err := service.StartLogin(context.Background(), KindOffline, LoginRequest{Username: "Alex"})
	if err != nil {
		t.Fatalf("二次登录失败：%v", err)
	}
	if first.Account.ID != second.Account.ID {
		t.Fatalf("同身份账号应复用 id：%s vs %s", first.Account.ID, second.Account.ID)
	}
	items, _ := store.List(context.Background())
	if len(items) != 1 {
		t.Fatalf("期望仅 1 条记录，实际 %d", len(items))
	}
}

// TestServiceSelectAndDelete 验证选中账号与删除后的自动接管。
func TestServiceSelectAndDelete(t *testing.T) {
	clock := newTestClock()
	service, _ := newTestService(t, clock, NewOfflineProvider(DefaultDeps()))

	first, err := service.StartLogin(context.Background(), KindOffline, LoginRequest{Username: "Steve"})
	if err != nil {
		t.Fatalf("登录 Steve 失败：%v", err)
	}
	clock.Advance(time.Second)
	second, err := service.StartLogin(context.Background(), KindOffline, LoginRequest{Username: "Alex"})
	if err != nil {
		t.Fatalf("登录 Alex 失败：%v", err)
	}

	accounts, selectErr := service.SelectAccount(context.Background(), first.Account.ID)
	if selectErr != nil {
		t.Fatalf("切换账号失败：%v", selectErr)
	}
	selectedID := ""
	for _, item := range accounts {
		if item.Selected {
			selectedID = item.ID
		}
	}
	if selectedID != first.Account.ID {
		t.Fatalf("期望选中 %s，实际 %s", first.Account.ID, selectedID)
	}

	remaining, deleteErr := service.DeleteAccount(context.Background(), first.Account.ID)
	if deleteErr != nil {
		t.Fatalf("删除账号失败：%v", deleteErr)
	}
	if len(remaining) != 1 {
		t.Fatalf("期望剩余 1 个账号，实际 %d", len(remaining))
	}
	if remaining[0].ID != second.Account.ID || !remaining[0].Selected {
		t.Fatal("删除当前账号后应自动选中剩余账号")
	}
}

// TestServiceRefreshAccount 验证刷新凭证并落库。
func TestServiceRefreshAccount(t *testing.T) {
	clock := newTestClock()
	refreshed := 0
	provider := &fakeProvider{
		info: ProviderInfo{Kind: KindMicrosoft, DisplayName: "微软", Implemented: true},
		refresh: func(_ context.Context, creds Credentials, _ Profile) (Credentials, *Error) {
			refreshed++
			creds.AccessToken = "new-token"
			creds.ExpiresAtMs = clock.Now().Add(time.Hour).UnixMilli()
			return creds, nil
		},
	}
	service, store := newTestService(t, clock, provider)

	profile := Profile{ID: "0123456789abcdef0123456789abcdef", Name: "Voxel"}
	store.seed(StoredAccount{
		Account: Account{
			ID: "acc-1", Kind: KindMicrosoft, ProviderID: "microsoft",
			DisplayName: "Voxel", Profile: profile, CreatedAtMs: 1, UpdatedAtMs: 1,
		},
		Credentials: Credentials{AccessToken: "old-token", RefreshToken: "refresh", UserType: UserTypeMSA},
	})

	account, refreshErr := service.RefreshAccount(context.Background(), "acc-1")
	if refreshErr != nil {
		t.Fatalf("刷新失败：%v", refreshErr)
	}
	if account.ID != "acc-1" || account.DisplayName != "Voxel" {
		t.Fatalf("刷新后账号摘要异常：%+v", account)
	}
	if refreshed != 1 {
		t.Fatalf("期望调用 1 次刷新，实际 %d", refreshed)
	}
	stored, _ := store.Get(context.Background(), "acc-1")
	if stored.Credentials.AccessToken != "new-token" {
		t.Fatalf("刷新后的凭证未落库：%s", stored.Credentials.AccessToken)
	}
}

// TestServiceSessionExpiry 验证登录会话超过 TTL 后拒绝继续轮询。
func TestServiceSessionExpiry(t *testing.T) {
	clock := newTestClock()
	provider := &fakeProvider{
		info: ProviderInfo{Kind: KindMicrosoft, DisplayName: "微软", Implemented: true},
		start: func(_ context.Context, _ LoginRequest) (LoginFlow, *Error) {
			return &scriptedFlow{
				id: "sess-1", kind: KindMicrosoft, state: LoginStateAwaitingUser,
				chall: LoginChallenge{UserCode: "ABCD-EFGH"},
				steps: []func(context.Context) (LoginOutcome, *Error){
					func(context.Context) (LoginOutcome, *Error) {
						return LoginOutcome{State: LoginStateAwaitingUser}, nil
					},
				},
			}, nil
		},
	}
	service, _ := newTestService(t, clock, provider)

	session, err := service.StartLogin(context.Background(), KindMicrosoft, LoginRequest{})
	if err != nil {
		t.Fatalf("开始登录失败：%v", err)
	}
	if session.Challenge.UserCode != "ABCD-EFGH" {
		t.Fatalf("设备码未回传：%+v", session.Challenge)
	}

	clock.Advance(11 * time.Minute)
	if _, pollErr := service.PollLogin(context.Background(), session.SessionID); pollErr == nil {
		t.Fatal("过期会话应返回错误")
	} else if pollErr.Code != ErrorCodeState {
		t.Fatalf("过期会话期望错误码 %s，实际 %s", ErrorCodeState, pollErr.Code)
	}
}

// TestServicePollFailureThenSuccess 验证可重试错误不破坏会话。
func TestServicePollFailureThenSuccess(t *testing.T) {
	clock := newTestClock()
	account, credentials := offlineProfileFor("RetryUser")
	account.Kind = KindMicrosoft
	provider := &fakeProvider{
		info: ProviderInfo{Kind: KindMicrosoft, DisplayName: "微软", Implemented: true},
		start: func(_ context.Context, _ LoginRequest) (LoginFlow, *Error) {
			return &scriptedFlow{
				id: "sess-retry", kind: KindMicrosoft, state: LoginStateAwaitingUser,
				chall: LoginChallenge{UserCode: "CODE"},
				steps: []func(context.Context) (LoginOutcome, *Error){
					func(context.Context) (LoginOutcome, *Error) {
						return LoginOutcome{}, newExternalError(ErrorCodeNetwork, "网络失败", true)
					},
					func(context.Context) (LoginOutcome, *Error) {
						a, c := account, credentials
						return LoginOutcome{State: LoginStateSucceeded, Account: &a, Credentials: &c}, nil
					},
				},
			}, nil
		},
	}
	service, _ := newTestService(t, clock, provider)

	session, err := service.StartLogin(context.Background(), KindMicrosoft, LoginRequest{})
	if err != nil {
		t.Fatalf("开始登录失败：%v", err)
	}
	if _, pollErr := service.PollLogin(context.Background(), session.SessionID); pollErr == nil {
		t.Fatal("首次轮询应返回网络错误")
	}
	done, pollErr := service.PollLogin(context.Background(), session.SessionID)
	if pollErr != nil {
		t.Fatalf("第二次轮询应成功：%v", pollErr)
	}
	if done.State != LoginStateSucceeded || done.Account == nil {
		t.Fatalf("第二次轮询结果异常：%+v", done)
	}
}

// TestServiceSelectProfile 验证多档案流程。
func TestServiceSelectProfile(t *testing.T) {
	clock := newTestClock()
	profileA := Profile{ID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Name: "Alpha"}
	profileB := Profile{ID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Name: "Beta"}
	provider := &fakeProvider{
		info: ProviderInfo{Kind: KindMicrosoft, DisplayName: "微软", Implemented: true},
		start: func(_ context.Context, _ LoginRequest) (LoginFlow, *Error) {
			flow := &scriptedFlow{
				id: "sess-profile", kind: KindMicrosoft, state: LoginStateAwaitingProfile,
				profiles: []Profile{profileA, profileB},
				steps: []func(context.Context) (LoginOutcome, *Error){
					func(context.Context) (LoginOutcome, *Error) {
						return LoginOutcome{State: LoginStateAwaitingProfile, Profiles: []Profile{profileA, profileB}}, nil
					},
				},
			}
			flow.selector = func(_ context.Context, profileID string) (LoginOutcome, *Error) {
				var picked Profile
				for _, candidate := range []Profile{profileA, profileB} {
					if candidate.ID == profileID {
						picked = candidate
					}
				}
				if picked.ID == "" {
					return LoginOutcome{}, newInputError(ErrorCodeInput, "档案不存在")
				}
				return LoginOutcome{
					State:       LoginStateSucceeded,
					Account:     &Account{Kind: KindMicrosoft, DisplayName: picked.Name, Profile: picked},
					Credentials: &Credentials{AccessToken: "mc-token", UserType: UserTypeMSA},
				}, nil
			}
			return flow, nil
		},
	}
	service, _ := newTestService(t, clock, provider)

	session, err := service.StartLogin(context.Background(), KindMicrosoft, LoginRequest{Username: "user@example.com", Password: "secret"})
	if err != nil {
		t.Fatalf("开始登录失败：%v", err)
	}
	if session.State != LoginStateAwaitingProfile || len(session.Profiles) != 2 {
		t.Fatalf("期望进入选档案阶段，实际 %+v", session)
	}
	polled, pollErr := service.PollLogin(context.Background(), session.SessionID)
	if pollErr != nil {
		t.Fatalf("轮询失败：%v", pollErr)
	}
	if polled.State != LoginStateAwaitingProfile {
		t.Fatalf("轮询后仍应等待选择档案，实际 %s", polled.State)
	}
	done, selectErr := service.SelectProfile(context.Background(), session.SessionID, profileB.ID)
	if selectErr != nil {
		t.Fatalf("选择档案失败：%v", selectErr)
	}
	if done.State != LoginStateSucceeded || done.Account == nil || done.Account.Profile.Name != "Beta" {
		t.Fatalf("选择档案结果异常：%+v", done)
	}
}

// TestServiceResolveCredentialsRefreshesExpired 验证启动取凭证前会先续期。
func TestServiceResolveCredentialsRefreshesExpired(t *testing.T) {
	clock := newTestClock()
	refreshed := 0
	provider := &fakeProvider{
		info: ProviderInfo{Kind: KindMicrosoft, DisplayName: "微软", Implemented: true},
		refresh: func(_ context.Context, creds Credentials, _ Profile) (Credentials, *Error) {
			refreshed++
			creds.AccessToken = "fresh-token"
			creds.ExpiresAtMs = clock.Now().Add(24 * time.Hour).UnixMilli()
			return creds, nil
		},
	}
	service, store := newTestService(t, clock, provider)
	store.seed(StoredAccount{
		Account: Account{
			ID: "acc-expired", Kind: KindMicrosoft, ProviderID: "microsoft",
			Profile: Profile{ID: "0123456789abcdef0123456789abcdef", Name: "Voxel"},
		},
		Credentials: Credentials{AccessToken: "stale-token", RefreshToken: "refresh", ExpiresAtMs: clock.Now().Add(-time.Minute).UnixMilli()},
	})

	profile, credentials, err := service.ResolveCredentials(context.Background(), "acc-expired")
	if err != nil {
		t.Fatalf("取凭证失败：%v", err)
	}
	if profile.Name != "Voxel" || credentials.AccessToken != "fresh-token" {
		t.Fatalf("凭证未续期：%+v", credentials)
	}
	if refreshed != 1 {
		t.Fatalf("期望续期 1 次，实际 %d", refreshed)
	}
}

// TestServiceLoginFailurePropagates 验证来源错误原样透传。
func TestServiceLoginFailurePropagates(t *testing.T) {
	clock := newTestClock()
	provider := &fakeProvider{
		info: ProviderInfo{Kind: KindMicrosoft, DisplayName: "微软", Implemented: true},
		start: func(_ context.Context, _ LoginRequest) (LoginFlow, *Error) {
			return nil, newAuthError(ErrorCodeAuth, "账号或密码错误")
		},
	}
	service, _ := newTestService(t, clock, provider)
	if _, err := service.StartLogin(context.Background(), KindMicrosoft, LoginRequest{}); err == nil {
		t.Fatal("来源错误必须透传")
	} else if err.Code != ErrorCodeAuth {
		t.Fatalf("期望错误码 %s，实际 %s", ErrorCodeAuth, err.Code)
	}
}

// TestServiceUsesRealRegistryKinds 验证默认注册表包含四类来源。
func TestServiceUsesRealRegistryKinds(t *testing.T) {
	registry := NewRegistry(DefaultDeps())
	infos := registry.List()
	if len(infos) != 4 {
		t.Fatalf("期望 4 个来源，实际 %d", len(infos))
	}
	seen := make(map[Kind]bool, len(infos))
	for _, info := range infos {
		seen[info.Kind] = true
	}
	for _, kind := range []Kind{KindMicrosoft, KindOffline, KindYggdrasil, KindNinevoxel} {
		if !seen[kind] {
			t.Fatalf("缺少来源 %s", kind)
		}
	}
	if !registry.List()[0].Implemented {
		t.Fatal("微软来源应为已实现")
	}
	if registry.List()[3].Implemented {
		t.Fatal("Ninevoxel 首期应为未实现")
	}
}

// fakeSiteProvider 同时实现标准 Refresher 的替身能力与「带站点根」扩展接口。
type fakeSiteProvider struct {
	fakeProvider
	gotSite string
	calls   int
}

func (p *fakeSiteProvider) RefreshAtSite(_ context.Context, siteURL string, creds Credentials, _ Profile) (Credentials, *Error) {
	p.calls++
	p.gotSite = siteURL
	creds.AccessToken = "site-token"
	return creds, nil
}

// TestServiceRefreshUsesSiteAwareExtension 验证 Yggdrasil 刷新优先走带站点根的扩展接口。
//
// 跨进程重启后只能靠 Account.ProviderID 反推站点，标准 Refresher 不带站点根，
// 因此服务层必须先尝试 YggdrasilSiteRefresher，否则 Yggdrasil 账号无法续期。
func TestServiceRefreshUsesSiteAwareExtension(t *testing.T) {
	clock := newTestClock()
	provider := &fakeSiteProvider{
		fakeProvider: fakeProvider{
			info: ProviderInfo{Kind: KindYggdrasil, DisplayName: "Yggdrasil", Implemented: true},
		},
	}
	service, store := newTestService(t, clock, provider)

	siteURL := "https://example.test/api/yggdrasil/"
	store.seed(StoredAccount{
		Account: Account{
			ID: "acc-ygg", Kind: KindYggdrasil, ProviderID: siteURL,
			DisplayName: "Player", Profile: Profile{ID: "0123456789abcdef0123456789abcdef", Name: "Player"},
		},
		Credentials: Credentials{AccessToken: "old", ClientToken: "client", UserType: UserTypeMojang},
	})

	if _, err := service.RefreshAccount(context.Background(), "acc-ygg"); err != nil {
		t.Fatalf("Yggdrasil 刷新失败：%v", err)
	}
	if provider.calls != 1 || provider.gotSite != siteURL {
		t.Fatalf("未走带站点根的刷新接口：calls=%d site=%q", provider.calls, provider.gotSite)
	}
	stored, _ := store.Get(context.Background(), "acc-ygg")
	if stored.Credentials.AccessToken != "site-token" {
		t.Fatalf("刷新后的凭证未落库：%s", stored.Credentials.AccessToken)
	}
}

// TestAccountViewCarriesNoSecrets 验证前端 DTO 不含凭证字段（安全回归）。
func TestAccountViewCarriesNoSecrets(t *testing.T) {
	forbidden := []string{"token", "password", "secret", "credential"}
	types := []reflect.Type{
		reflect.TypeOf(AccountView{}),
		reflect.TypeOf(LoginSessionView{}),
		reflect.TypeOf(AccountList{}),
		reflect.TypeOf(ProviderView{}),
	}
	for _, typ := range types {
		for index := 0; index < typ.NumField(); index++ {
			name := strings.ToLower(typ.Field(index).Name)
			tag := strings.ToLower(typ.Field(index).Tag.Get("json"))
			for _, word := range forbidden {
				if strings.Contains(name, word) || strings.Contains(tag, word) {
					t.Fatalf("%s 字段 %s 疑似携带凭证", typ.Name(), name)
				}
			}
		}
	}
}
