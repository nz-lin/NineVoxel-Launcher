// 本文件是账号子系统的 Wails 服务壳：把 Service 的能力暴露成可绑定方法。
//
// 与 internal/download、internal/runtime 的做法保持一致：
//   - 服务结构体自身无状态，存储/注册表/发射器放在包级单例里，
//     这样 Wails 多次扫描绑定不会创建出多套数据库连接与登录会话表；
//   - 初始化失败不 panic：把稳定错误码保留下来，每个绑定方法如实返回，
//     界面据此提示「账号功能不可用」，应用本身照常启动。
//
// 安全边界：本文件是前端唯一入口，**所有 DTO 都不得包含凭证字段**；
// 转换函数逐字段赋值，禁止直接序列化内部结构体（避免将来新增凭证字段被
// 意外透出）。
package account

import (
	"context"
	"path/filepath"
	"strings"
	"sync"

	"example.com/ninevoxel/launcher/internal/artifact"
	"example.com/ninevoxel/launcher/internal/database"
	"example.com/ninevoxel/launcher/internal/events"
	"example.com/ninevoxel/launcher/internal/logging"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// AccountService 是账号子系统的 Wails 服务。
//
// 导出方法集合即前端绑定面；未导出的辅助函数不会被生成到 bindings。
type AccountService struct{}

// NewWailsService 创建账号服务实例（供 application.Options 装配）。
func NewWailsService() application.Service {
	return application.NewService(&AccountService{})
}

// 包级单例：服务、初始化错误、事件发射器与日志器。
var (
	accountServiceOnce   sync.Once
	accountServiceHolder *Service
	accountServiceErr    *Error

	accountEmitterOnce sync.Once
	accountEmitterInst *events.Emitter

	accountLoggerOnce sync.Once
	accountLoggerInst *logging.Logger
)

// accountLog 返回带组件标签的日志器。
func accountLog() *logging.Logger {
	accountLoggerOnce.Do(func() {
		accountLoggerInst = logging.Default.WithComponent("account")
	})
	return accountLoggerInst
}

// accountEmitter 返回事件发射器，并惰性绑定当前 Wails 应用。
//
// 与下载模块一致：惰性绑定避免要求装配层再加一行；发射失败（应用未就绪）
// 不影响业务，前端可用 ListAccounts 恢复状态。
func accountEmitter() *events.Emitter {
	accountEmitterOnce.Do(func() {
		accountEmitterInst = events.NewEmitter()
		if app := application.Get(); app != nil {
			accountEmitterInst.BindApp(app)
		}
	})
	return accountEmitterInst
}

// accountKeyPath 返回凭证加密密钥文件路径。
//
// 密钥固定放在 `<数据根>/secrets/account.key`，**不跟随 app.db 所在目录**：
// 数据库搬迁到 db/ 后，密钥位置必须保持稳定，否则旧账号会全部解密失败。
func accountKeyPath() (string, *Error) {
	root, rootErr := artifact.DataRoot()
	if rootErr != nil || strings.TrimSpace(root) == "" {
		return "", newSystemError(ErrorCodeStorage, "数据目录不可用，无法定位账号密钥")
	}
	return filepath.Join(root, "secrets", "account.key"), nil
}

// sharedAccountService 返回包级账号服务单例，必要时完成初始化。
func sharedAccountService() (*Service, *Error) {
	accountServiceOnce.Do(func() {
		keyPath, pathErr := accountKeyPath()
		if pathErr != nil {
			accountServiceErr = pathErr
			return
		}
		cipher, cipherErr := OpenCipher(keyPath)
		if cipherErr != nil {
			accountServiceErr = cipherErr
			return
		}

		// 连接来自进程级共享池：账号模块不再自行打开/迁移 app.db。
		db, dbErr := database.Default(context.Background())
		if dbErr != nil {
			accountServiceErr = newSystemError(ErrorCodeStorage, "账号数据库不可用，请检查数据目录权限")
			return
		}
		store, storeErr := OpenSQLiteStoreWithDB(db, cipher)
		if storeErr != nil {
			accountServiceErr = storeErr
			return
		}
		accountServiceHolder = NewService(ServiceOptions{
			Store:    store,
			Registry: NewRegistry(DefaultDeps()),
			Emitter:  accountEmitter(),
			Now:      nil,
		})
		accountLog().Info("账号服务已就绪", logging.Fields{})
	})
	if accountServiceErr != nil {
		return nil, accountServiceErr
	}
	return accountServiceHolder, nil
}

// ===== 面向前端的 DTO =====

// StartLoginRequest 是开始登录的入参。
//
// 不同来源按需填字段：离线只填 username；Yggdrasil 填 apiUrl+username+password；
// 微软不需要任何字段。密码只在请求栈内存在，不落盘、不进日志。
type StartLoginRequest struct {
	// Kind 为账号来源类型（offline / microsoft / yggdrasil / ninevoxel）。
	Kind string `json:"kind"`

	// Username 为用户名/邮箱/玩家名。
	Username string `json:"username"`

	// Password 为密码（仅 Yggdrasil 使用）。
	Password string `json:"password"`

	// APIURL 为自定 Yggdrasil 服务地址。
	APIURL string `json:"apiUrl"`

	// LoginMode 为登录方式偏好（仅微软账号使用）：
	// 空/"pkce" = 系统浏览器 + 本地回环（默认）；"device_code" = 设备码回退。
	LoginMode string `json:"loginMode,omitempty"`
}

// LoginSessionRequest 是按会话 id 操作的入参。
type LoginSessionRequest struct {
	// SessionID 为登录会话标识。
	SessionID string `json:"sessionId"`
}

// SelectProfileRequest 是选择档案的入参。
type SelectProfileRequest struct {
	// SessionID 为登录会话标识。
	SessionID string `json:"sessionId"`

	// ProfileID 为要选择的档案 UUID（无连字符或带连字符均可）。
	ProfileID string `json:"profileId"`
}

// AccountIDRequest 是按账号 id 操作的入参。
type AccountIDRequest struct {
	// AccountID 为本地账号标识。
	AccountID string `json:"accountId"`
}

// APIURLRequest 是地址解析入参。
type APIURLRequest struct {
	// APIURL 为用户输入的地址（可省略协议，由来源补全）。
	APIURL string `json:"apiUrl"`
}

// ProfileView 是玩家档案的对外视图。
type ProfileView struct {
	// ID 为无连字符小写 UUID。
	ID string `json:"id"`

	// Name 为玩家名。
	Name string `json:"name"`

	// SkinURL 为皮肤地址。
	SkinURL string `json:"skinUrl,omitempty"`

	// SkinModel 为皮肤模型（classic / slim）。
	SkinModel string `json:"skinModel,omitempty"`

	// CapeURL 为披风地址。
	CapeURL string `json:"capeUrl,omitempty"`
}

// AccountView 是账号摘要的对外视图（**不含任何凭证**）。
type AccountView struct {
	// ID 为本地账号标识。
	ID string `json:"id"`

	// Kind 为来源类型。
	Kind string `json:"kind"`

	// ProviderID 为来源实例标识（Yggdrasil 为 API 根）。
	ProviderID string `json:"providerId"`

	// DisplayName 为展示名。
	DisplayName string `json:"displayName"`

	// Profile 为玩家档案。
	Profile ProfileView `json:"profile"`

	// Selected 表示是否为当前账号。
	Selected bool `json:"selected"`

	// CreatedAtMs / UpdatedAtMs / LastUsedAtMs 为 UTC Unix 毫秒时间戳。
	CreatedAtMs  int64 `json:"createdAtMs"`
	UpdatedAtMs  int64 `json:"updatedAtMs"`
	LastUsedAtMs int64 `json:"lastUsedAtMs"`
}

// AccountList 是账号列表快照。
type AccountList struct {
	// Accounts 为全部账号（当前选中账号排在最前）。
	Accounts []AccountView `json:"accounts"`

	// SelectedAccountID 为当前账号 id；没有选中账号时为空。
	SelectedAccountID string `json:"selectedAccountId"`
}

// ChallengeView 是登录交互信息的对外视图。
type ChallengeView struct {
	// UserCode 为设备码。
	UserCode string `json:"userCode"`

	// VerificationURI 为用户需要访问的地址。
	VerificationURI string `json:"verificationUri"`

	// Message 为服务端提示文案。
	Message string `json:"message"`

	// ExpiresAtMs 为用户码过期时间。
	ExpiresAtMs int64 `json:"expiresAtMs"`

	// IntervalSeconds 为建议轮询间隔。
	IntervalSeconds int `json:"intervalSeconds"`
}

// LoginSessionView 是登录流程的对外快照。
type LoginSessionView struct {
	// SessionID 为会话标识。
	SessionID string `json:"sessionId"`

	// Kind 为来源类型。
	Kind string `json:"kind"`

	// State 为流程状态。
	State string `json:"state"`

	// Challenge 为交互信息。
	Challenge ChallengeView `json:"challenge"`

	// Profiles 为待选择档案。
	Profiles []ProfileView `json:"profiles,omitempty"`

	// Account 为成功后的账号摘要。
	Account *AccountView `json:"account,omitempty"`

	// ErrorCode / ErrorMessage 为失败信息。
	ErrorCode    string `json:"errorCode,omitempty"`
	ErrorMessage string `json:"errorMessage,omitempty"`
}

// ProviderView 是账号来源的对外描述。
type ProviderView struct {
	// Kind 为来源类型。
	Kind string `json:"kind"`

	// DisplayName 为中文名称。
	DisplayName string `json:"displayName"`

	// Implemented 表示是否已实现（Ninevoxel 首期为 false）。
	Implemented bool `json:"implemented"`

	// Description 为说明文案。
	Description string `json:"description"`

	// Capabilities 为能力声明。
	Capabilities Capabilities `json:"capabilities"`
}

// ProviderList 是账号来源列表。
type ProviderList struct {
	// Providers 为按展示顺序排列的来源。
	Providers []ProviderView `json:"providers"`
}

// SiteInfoView 是自定义 Yggdrasil 站点信息。
type SiteInfoView struct {
	// APIURL 为规范化后的 API 根。
	APIURL string `json:"apiUrl"`

	// ServerName 为服务端名称。
	ServerName string `json:"serverName"`

	// ImplementationName 为实现名称。
	ImplementationName string `json:"implementationName"`

	// NonEmailLogin 表示支持非邮箱登录。
	NonEmailLogin bool `json:"nonEmailLogin"`

	// SkinDomains 为皮肤域名白名单。
	SkinDomains []string `json:"skinDomains,omitempty"`
}

// ===== 绑定方法 =====

// ListProviders 返回全部账号来源（界面据此渲染「添加账号」入口）。
func (s *AccountService) ListProviders() (ProviderList, error) {
	service, initErr := sharedAccountService()
	if initErr != nil {
		return ProviderList{}, initErr
	}
	infos := service.ListProviders()
	views := make([]ProviderView, 0, len(infos))
	for _, info := range infos {
		views = append(views, ProviderView{
			Kind:         string(info.Kind),
			DisplayName:  info.DisplayName,
			Implemented:  info.Implemented,
			Description:  info.Description,
			Capabilities: info.Capabilities,
		})
	}
	return ProviderList{Providers: views}, nil
}

// ListAccounts 返回账号列表与当前选中账号。
func (s *AccountService) ListAccounts() (AccountList, error) {
	service, initErr := sharedAccountService()
	if initErr != nil {
		return AccountList{}, initErr
	}
	accounts, listErr := service.ListAccounts(context.Background())
	if listErr != nil {
		return AccountList{}, listErr
	}
	return toAccountList(accounts), nil
}

// StartLogin 开始一次登录并返回会话快照。
func (s *AccountService) StartLogin(req StartLoginRequest) (LoginSessionView, error) {
	service, initErr := sharedAccountService()
	if initErr != nil {
		return LoginSessionView{}, initErr
	}
	session, loginErr := service.StartLogin(context.Background(), Kind(req.Kind), LoginRequest{
		Username:  req.Username,
		Password:  req.Password,
		APIURL:    req.APIURL,
		LoginMode: req.LoginMode,
	})
	if loginErr != nil {
		return LoginSessionView{}, loginErr
	}
	return toLoginSessionView(session), nil
}

// PollLogin 推进登录流程（前端按 Challenge.IntervalSeconds 轮询）。
func (s *AccountService) PollLogin(req LoginSessionRequest) (LoginSessionView, error) {
	service, initErr := sharedAccountService()
	if initErr != nil {
		return LoginSessionView{}, initErr
	}
	session, pollErr := service.PollLogin(context.Background(), req.SessionID)
	if pollErr != nil {
		return LoginSessionView{}, pollErr
	}
	return toLoginSessionView(session), nil
}

// SelectProfile 为多档案账号选择档案。
func (s *AccountService) SelectProfile(req SelectProfileRequest) (LoginSessionView, error) {
	service, initErr := sharedAccountService()
	if initErr != nil {
		return LoginSessionView{}, initErr
	}
	session, selectErr := service.SelectProfile(context.Background(), req.SessionID, req.ProfileID)
	if selectErr != nil {
		return LoginSessionView{}, selectErr
	}
	return toLoginSessionView(session), nil
}

// CancelLogin 取消登录会话。
func (s *AccountService) CancelLogin(req LoginSessionRequest) (LoginSessionView, error) {
	service, initErr := sharedAccountService()
	if initErr != nil {
		return LoginSessionView{}, initErr
	}
	session, cancelErr := service.CancelLogin(req.SessionID)
	if cancelErr != nil {
		return LoginSessionView{}, cancelErr
	}
	return toLoginSessionView(session), nil
}

// SelectAccount 把指定账号设为当前账号。
func (s *AccountService) SelectAccount(req AccountIDRequest) (AccountList, error) {
	service, initErr := sharedAccountService()
	if initErr != nil {
		return AccountList{}, initErr
	}
	accounts, selectErr := service.SelectAccount(context.Background(), req.AccountID)
	if selectErr != nil {
		return AccountList{}, selectErr
	}
	return toAccountList(accounts), nil
}

// DeleteAccount 删除账号并返回剩余列表。
func (s *AccountService) DeleteAccount(req AccountIDRequest) (AccountList, error) {
	service, initErr := sharedAccountService()
	if initErr != nil {
		return AccountList{}, initErr
	}
	accounts, deleteErr := service.DeleteAccount(context.Background(), req.AccountID)
	if deleteErr != nil {
		return AccountList{}, deleteErr
	}
	return toAccountList(accounts), nil
}

// RefreshAccount 刷新指定账号的凭证。
func (s *AccountService) RefreshAccount(req AccountIDRequest) (AccountView, error) {
	service, initErr := sharedAccountService()
	if initErr != nil {
		return AccountView{}, initErr
	}
	account, refreshErr := service.RefreshAccount(context.Background(), req.AccountID)
	if refreshErr != nil {
		return AccountView{}, refreshErr
	}
	return toAccountView(account), nil
}

// ResolveYggdrasilSite 解析自定义 Yggdrasil 地址（提交前校验 + 回显服务名）。
func (s *AccountService) ResolveYggdrasilSite(req APIURLRequest) (SiteInfoView, error) {
	service, initErr := sharedAccountService()
	if initErr != nil {
		return SiteInfoView{}, initErr
	}
	site, siteErr := service.ResolveYggdrasilSite(context.Background(), req.APIURL)
	if siteErr != nil {
		return SiteInfoView{}, siteErr
	}
	return SiteInfoView{
		APIURL:             site.APIURL,
		ServerName:         site.ServerName,
		ImplementationName: site.ImplementationName,
		NonEmailLogin:      site.NonEmailLogin,
		SkinDomains:        site.SkinDomains,
	}, nil
}

// ===== DTO 转换 =====

// toProfileView 转换档案。
func toProfileView(profile Profile) ProfileView {
	return ProfileView{
		ID:        profile.ID,
		Name:      profile.Name,
		SkinURL:   profile.SkinURL,
		SkinModel: profile.SkinModel,
		CapeURL:   profile.CapeURL,
	}
}

// toAccountView 转换账号摘要。
func toAccountView(account Account) AccountView {
	return AccountView{
		ID:           account.ID,
		Kind:         string(account.Kind),
		ProviderID:   account.ProviderID,
		DisplayName:  account.DisplayName,
		Profile:      toProfileView(account.Profile),
		Selected:     account.Selected,
		CreatedAtMs:  account.CreatedAtMs,
		UpdatedAtMs:  account.UpdatedAtMs,
		LastUsedAtMs: account.LastUsedAtMs,
	}
}

// toAccountList 转换账号列表并计算当前选中账号。
func toAccountList(accounts []Account) AccountList {
	views := make([]AccountView, 0, len(accounts))
	selectedID := ""
	for _, account := range accounts {
		view := toAccountView(account)
		if account.Selected {
			selectedID = account.ID
		}
		views = append(views, view)
	}
	return AccountList{Accounts: views, SelectedAccountID: selectedID}
}

// toLoginSessionView 转换登录会话快照。
func toLoginSessionView(session LoginSession) LoginSessionView {
	view := LoginSessionView{
		SessionID:    session.SessionID,
		Kind:         string(session.Kind),
		State:        string(session.State),
		Challenge:    toChallengeView(session.Challenge),
		ErrorCode:    session.ErrorCode,
		ErrorMessage: session.ErrorMessage,
	}
	if len(session.Profiles) > 0 {
		profiles := make([]ProfileView, 0, len(session.Profiles))
		for _, profile := range session.Profiles {
			profiles = append(profiles, toProfileView(profile))
		}
		view.Profiles = profiles
	}
	if session.Account != nil {
		account := toAccountView(*session.Account)
		view.Account = &account
	}
	return view
}

// toChallengeView 转换登录交互信息。
func toChallengeView(challenge LoginChallenge) ChallengeView {
	return ChallengeView{
		UserCode:        challenge.UserCode,
		VerificationURI: challenge.VerificationURI,
		Message:         challenge.Message,
		ExpiresAtMs:     challenge.ExpiresAtMs,
		IntervalSeconds: challenge.IntervalSeconds,
	}
}
