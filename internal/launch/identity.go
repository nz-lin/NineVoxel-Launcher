// 本文件定义启动链路与账号子系统的**唯一衔接点**（P3 账号接入）。
//
// 背景：P2 的启动参数里身份类占位符（`${auth_player_name}` / `${auth_uuid}` /
// `${auth_access_token}` / `${user_type}` / `${clientid}` / `${auth_xuid}`）全部
// 写死为离线占位值（见 arguments.go 的 `placeholder*` 常量，D-5 冻结）。
// P3 起启动时若用户选了账号，应由账号子系统提供真实的档案与凭证。
//
// 边界（务必保持）：
//   - 本文件**不依赖** `internal/account`：只声明一个接口，由装配层（`internal/app`）
//     用 account 包实现并注入。这样 launch 包不会反向依赖账号子系统，
//     也不会把账号/凭证类型泄漏到启动链路的其他文件。
//   - 本类型**不含密码与 refresh token**：启动只需要 access token，
//     长期凭证留在账号子系统内，不进入启动参数构建路径。
//   - 依赖注入只有一次（应用启动时），之后只读；`resolveAccountIdentity`
//     只做「取身份 + 校验最小字段」，不做 IO、不刷新凭证。
package launch

import (
	"context"
	"strings"
	"sync"
)

// AccountIdentity 是启动所需的最小身份集合（不含密码/refresh token）。
//
// 字段来源（装配层的 resolver 负责映射）：
//   - Username    ← 账号 Profile.Name；
//   - UUID        ← 账号 Profile.ID（**无连字符小写**，与 Minecraft 启动参数一致）；
//   - AccessToken ← 账号凭证的 AccessToken（离线账号为空）；
//   - UserType    ← 凭证的 UserType（msa / mojang / legacy）；
//   - XUID        ← 微软账号的 Xbox User ID；其他来源为空。
//
// 空值语义：任一字段为空表示「该来源没有这个值」，占位符替换表会**逐项回落**
// 到 D-5 的离线占位常量（见 `buildPlaceholderTable`）。
type AccountIdentity struct {
	// Username 为玩家名（游戏内昵称，对应 `${auth_player_name}`）。
	Username string

	// UUID 为无连字符小写 UUID（对应 `${auth_uuid}`）。
	UUID string

	// AccessToken 为启动用访问令牌（对应 `${auth_access_token}`）；离线账号为空。
	AccessToken string

	// UserType 为身份类型：msa / mojang / legacy（对应 `${user_type}`）。
	UserType string

	// XUID 为 Xbox User ID（对应 `${auth_xuid}` / `${clientid}`）；其他来源为空。
	XUID string
}

// AccountIdentityResolver 由装配层注入（`internal/app/bootstrap.go` 里用 account 包实现）。
//
// 实现方约定：
//   - accountID 为账号子系统内的本地账号 id（非空）；
//   - 返回的身份必须包含 Username 与 UUID，其余字段可为空；
//   - 凭证失效/刷新失败时返回 error，由本包统一转成 `LAUNCH_ACCOUNT_001`，
//     **不得**把底层错误文本透给前端（可能含 token 片段）。
type AccountIdentityResolver interface {
	// ResolveIdentity 读取指定账号的启动身份。
	ResolveIdentity(ctx context.Context, accountID string) (AccountIdentity, error)
}

// identityResolver 是进程内唯一的注入点。
//
// 用读写锁而不是裸变量：装配发生在启动期，但启动请求可能并发进来，
// 之后只读；锁只保护「注入」与「读取指针」这一次内存访问，不参与任何 IO。
var (
	identityResolverMu sync.RWMutex
	identityResolver   AccountIdentityResolver
)

// SetAccountIdentityResolver 注入账号身份解析器（应用启动时调用一次，之后只读）。
//
// 参数 resolver 为装配层实现；传 nil 表示不启用账号接入（所有启动沿用离线占位身份）。
// 允许重复调用：测试与热重载场景下后一次覆盖前一次。
func SetAccountIdentityResolver(resolver AccountIdentityResolver) {
	identityResolverMu.Lock()
	defer identityResolverMu.Unlock()

	identityResolver = resolver
}

// currentAccountIdentityResolver 返回当前注入的解析器（可能为 nil）。
func currentAccountIdentityResolver() AccountIdentityResolver {
	identityResolverMu.RLock()
	defer identityResolverMu.RUnlock()

	return identityResolver
}

// resolveAccountIdentity 解析启动所需的账号身份。
//
// 参数 ctx 为调用上下文；accountID 为所选账号的本地 id。
//
// 行为（与 DTO 的 `accountId` 语义一致）：
//   - accountID 为空（含纯空白）：返回**零值** AccountIdentity 与 nil，
//     调用方据此回落到离线占位身份——这样旧调用方（不传账号）行为完全不变；
//   - accountID 非空但 resolver 未注入：返回 `LAUNCH_ACCOUNT_001`；
//   - resolver 返回错误：返回 `LAUNCH_ACCOUNT_001`（**不**透传底层错误文本）；
//   - 返回的身份缺 Username 或 UUID：返回 `LAUNCH_ACCOUNT_001`。
//
// 返回的 *Error 文案已脱敏（不含账号 id、token、路径）。
func resolveAccountIdentity(ctx context.Context, accountID string) (AccountIdentity, *Error) {
	trimmedID := strings.TrimSpace(accountID)
	if trimmedID == "" {
		// 未选择账号：保持 P2 的离线占位行为（兼容旧调用）。
		return AccountIdentity{}, nil
	}

	resolver := currentAccountIdentityResolver()
	if resolver == nil {
		// 装配缺失属于程序问题（不是用户能修的），归为系统错误。
		return AccountIdentity{}, newSystemError(ErrorCodeLaunchAccount, messageLaunchAccountUnreadable)
	}

	identity, resolveErr := resolver.ResolveIdentity(ctx, trimmedID)
	if resolveErr != nil {
		// 典型原因：凭证过期且刷新失败、账号已被删除。用户可去账号管理重新登录。
		// 底层错误原样丢弃（可能含 token 片段），只保留脱敏文案。
		return AccountIdentity{}, newInputError(ErrorCodeLaunchAccount, messageLaunchAccountUnreadable)
	}

	// 最小字段校验：没有玩家名或 UUID 的身份无法构造可启动的参数，
	// 此时宁可拒绝启动并提示用户，也不要带着空 `${auth_player_name}` 起游戏。
	identity.Username = strings.TrimSpace(identity.Username)
	identity.UUID = strings.TrimSpace(identity.UUID)
	if identity.Username == "" || identity.UUID == "" {
		return AccountIdentity{}, newExternalError(ErrorCodeLaunchAccount, messageLaunchAccountUnreadable)
	}

	return identity, nil
}
