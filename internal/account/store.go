// 本文件实现账号的 SQLite 持久化。
//
// 职责边界：
//   - 连接建立与迁移全部复用 internal/database（ADR-002 的唯一实现），
//     本文件不自己拼 DSN、不自己执行迁移；
//   - 数据库路径复用 internal/settings.ResolveDatabasePath（接口冻结清单 §8.9）；
//   - 凭证的加解密全部委托 Cipher（cipher.go），本文件只负责把密文 BLOB 读写,
//     不接触明文 token 的存储细节。
//
// 安全约束（硬性）：数据库里不出现明文 token；本文件不写任何日志，
// 错误文案也不包含 token / 密文 / 绝对路径。
//
// 并发约束：Store 的方法是并发安全的——*sql.DB 自身带连接池，Upsert/Delete/Touch
// 都是单条语句；只有 SetSelected 需要显式事务（先清空选中再置 1，避免触发
// 部分唯一索引）。Wails 可以从多个 goroutine 同时调用。
package account

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"example.com/ninevoxel/launcher/internal/database"
	"example.com/ninevoxel/launcher/internal/settings"
)

const (
	// tableNameAccounts 是账号表名，与迁移 000006_accounts.sql 保持一致。
	tableNameAccounts = "accounts"

	// accountColumns 是账号行的列顺序，与 storedRow.scan 严格对应。
	//
	// 凭证三列以 BLOB 形式取出，由 Cipher 解密后才进入 Credentials。
	accountColumns = `id, kind, provider_id, display_name,
		profile_id, profile_name, skin_model, skin_url, cape_url,
		access_token_enc, client_token_enc, refresh_token_enc,
		token_expires_at_ms, user_type, xuid,
		is_selected, created_at_ms, updated_at_ms, last_used_at_ms`

	// accountListOrder 是账号列表的稳定排序：
	// 选中账号置顶 → 最近使用时间倒序 → 创建时间正序 → id 正序（兜底保证确定性）。
	accountListOrder = `ORDER BY is_selected DESC, last_used_at_ms DESC, created_at_ms ASC, id ASC`
)

// StoredAccount 是「账号摘要 + 凭证」的持久化单元（仅在包内流转）。
type StoredAccount struct {
	Account
	Credentials Credentials
}

// Store 是账号存储抽象（便于测试替换与未来切换实现）。
type Store interface {
	List(ctx context.Context) ([]StoredAccount, error)
	Get(ctx context.Context, id string) (*StoredAccount, error)
	Upsert(ctx context.Context, item StoredAccount) error
	Delete(ctx context.Context, id string) error
	SetSelected(ctx context.Context, id string, atMs int64) error
	Touch(ctx context.Context, id string, atMs int64) error
	Close() error
}

// sqliteStore 是基于 SQLite 的 Store 实现。
type sqliteStore struct {
	// db 为按 ADR-002 打开的连接池，在应用生命周期内复用。
	db *sql.DB

	// cipher 负责凭证列的加解密；构造 Store 时必须提供。
	cipher *Cipher
}

// 编译期断言：sqliteStore 必须实现 Store。
var _ Store = (*sqliteStore)(nil)

// OpenSQLiteStore 打开账号数据库并执行迁移。
//
// 参数 path 为 app.db 的文件路径（通常来自 DefaultDatabasePath）；
// 参数 cipher 为凭证加解密器（通常来自 OpenCipher），必须非 nil。
//
// 行为与失败分类：
//   - cipher 为 nil → ErrorCodeCrypto（没有加密器时绝不能退化成明文落库）；
//   - path 为空 → ErrorCodeStorage；
//   - 打开失败 / 迁移失败 → ErrorCodeStorage（错误文案已脱敏，不含路径）。
//
// 迁移复用 database.Migrate：账号表随迁移 000006 建立，重复打开是幂等的。
func OpenSQLiteStore(path string, cipher *Cipher) (Store, *Error) {
	if cipher == nil {
		return nil, newSystemError(ErrorCodeCrypto, "凭证加密尚未初始化，无法打开账号存储")
	}
	if strings.TrimSpace(path) == "" {
		return nil, newSystemError(ErrorCodeStorage, "账号数据库路径为空，无法打开账号存储")
	}

	db, err := database.Open(path)
	if err != nil {
		return nil, newSystemError(ErrorCodeStorage, "打开账号数据库失败，请检查数据目录权限")
	}

	if _, err := database.Migrate(context.Background(), db); err != nil {
		// 迁移失败时数据库不可用，必须关闭连接池，避免泄漏。
		_ = db.Close()
		return nil, newSystemError(ErrorCodeStorage, "账号数据库迁移失败，请检查数据目录权限")
	}

	return &sqliteStore{db: db, cipher: cipher}, nil
}

// DefaultDatabasePath 返回 app.db 的绝对路径（复用 settings 的解析规则）。
//
// 两处候选目录（用户配置目录 / exe 同级目录）都不可用时返回 ErrorCodeStorage。
// 返回的路径包含本机绝对路径，**只允许在后端使用**，不得返回前端。
func DefaultDatabasePath() (string, *Error) {
	status := settings.ResolveDatabasePath()
	if status.Path == "" {
		return "", newSystemError(ErrorCodeStorage, "数据目录不可用，无法定位账号数据库")
	}
	return status.Path, nil
}

// List 返回全部账号（含解密后的凭证），按「选中 → 最近使用 → 创建时间」排序。
//
// 解密失败策略（本实现的选择及理由）：
//   - 单个账号的凭证密文损坏（例如数据库被外部篡改、密钥文件被替换）时，
//     **整个列表不失败**：仍返回该账号的摘要（用户能看见它、删除它或重新登录），
//     但三条凭证全部置空——不做「部分凭证可用」的半成品，避免启动链路拿到
//     不完整的凭证后产生难以解释的失败；
//   - 调用方（服务层）对非离线账号发现 AccessToken 为空时，即视为「凭证不可用，
//     需要重新登录」；离线账号本来就没有凭证，不受影响；
//   - Get 是「明确需要这份凭证」的路径（启动 / 刷新），因此解密失败时返回
//     ErrorCodeCrypto 的明确错误，而不是静默返回空凭证——把问题暴露在真正
//     使用凭证的地方，而不是一直拖到游戏启动失败。
func (s *sqliteStore) List(ctx context.Context) ([]StoredAccount, error) {
	if s == nil || s.db == nil {
		return nil, newSystemError(ErrorCodeStorage, "账号存储未打开")
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT `+accountColumns+` FROM `+tableNameAccounts+` `+accountListOrder)
	if err != nil {
		return nil, newSystemError(ErrorCodeStorage, "读取账号列表失败")
	}
	defer rows.Close()

	// 返回非 nil 的空切片：调用方（与前端契约）不需要区分 nil 与空列表。
	result := make([]StoredAccount, 0, 8)
	for rows.Next() {
		var raw storedRow
		if err := raw.scan(rows); err != nil {
			return nil, newSystemError(ErrorCodeStorage, "解析账号记录失败")
		}

		item := raw.toStoredAccount()
		credentials, decryptErr := s.decryptCredentials(raw)
		if decryptErr != nil {
			// 单行解密失败：保留摘要与非敏感元数据（过期时间/UserType/XUID），
			// 凭证三列已由 decryptCredentials 置空，继续处理下一行（策略见方法注释）。
			item.Credentials = credentials
			result = append(result, item)
			continue
		}
		item.Credentials = credentials
		result = append(result, item)
	}

	if err := rows.Err(); err != nil {
		return nil, newSystemError(ErrorCodeStorage, "遍历账号列表失败")
	}

	return result, nil
}

// Get 按 id 读取单个账号（含解密后的凭证）。
//
// 找不到 id 时返回 ErrorCodeNotFound；凭证解密失败时返回 ErrorCodeCrypto
// （这里是真正需要使用凭证的路径，不能静默返回空凭证，策略见 List 注释）。
func (s *sqliteStore) Get(ctx context.Context, id string) (*StoredAccount, error) {
	if s == nil || s.db == nil {
		return nil, newSystemError(ErrorCodeStorage, "账号存储未打开")
	}

	var raw storedRow
	err := s.db.QueryRowContext(ctx,
		`SELECT `+accountColumns+` FROM `+tableNameAccounts+` WHERE id = ?`, id).
		Scan(raw.scanTargets()...)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, newSystemError(ErrorCodeNotFound, "账号不存在")
	}
	if err != nil {
		return nil, newSystemError(ErrorCodeStorage, "读取账号失败")
	}

	credentials, decryptErr := s.decryptCredentials(raw)
	if decryptErr != nil {
		return nil, decryptErr
	}

	item := raw.toStoredAccount()
	item.Credentials = credentials
	return &item, nil
}

// Upsert 按身份 (kind, provider_id, profile_id) 插入或更新账号。
//
// 行为：
//   - 插入时 created_at_ms / updated_at_ms 取 item 的值（0 时补当前时间）；
//   - 冲突时更新资料与凭证，并**保留** created_at_ms、is_selected、last_used_at_ms；
//   - 选中态只由 SetSelected 维护：Upsert 插入时固定写 is_selected=0，
//     冲突时保留原值。理由：选中态是全局唯一状态，若允许 Upsert 携带选中位，
//     并发登录时很容易绕过部分唯一索引或产生「新登录顶掉当前账号」的意外行为；
//   - 凭证在这里加密后写 BLOB；空凭证写 NULL。
//
// 同一身份重复登录不会产生重复账号；若调用方传入了不同的本地 id，
// 冲突更新后行仍保留首次写入的 id（幂等身份优先于本地 id）。
func (s *sqliteStore) Upsert(ctx context.Context, item StoredAccount) error {
	if s == nil || s.db == nil {
		return newSystemError(ErrorCodeStorage, "账号存储未打开")
	}
	if s.cipher == nil {
		return newSystemError(ErrorCodeCrypto, "凭证加密尚未初始化")
	}
	if strings.TrimSpace(item.ID) == "" {
		return newInputError(ErrorCodeInput, "账号标识不能为空")
	}
	if !item.Kind.Valid() {
		return newInputError(ErrorCodeProviderUnknown, "账号类型不合法")
	}

	now := time.Now().UTC().UnixMilli()
	createdAtMs := item.CreatedAtMs
	if createdAtMs <= 0 {
		createdAtMs = now
	}
	updatedAtMs := item.UpdatedAtMs
	if updatedAtMs <= 0 {
		updatedAtMs = now
	}

	accessTokenEnc, cryptoErr := s.cipher.Encrypt(item.Credentials.AccessToken)
	if cryptoErr != nil {
		return cryptoErr
	}
	clientTokenEnc, cryptoErr := s.cipher.Encrypt(item.Credentials.ClientToken)
	if cryptoErr != nil {
		return cryptoErr
	}
	refreshTokenEnc, cryptoErr := s.cipher.Encrypt(item.Credentials.RefreshToken)
	if cryptoErr != nil {
		return cryptoErr
	}

	// 说明：
	//   - is_selected 插入固定 0（选中态由 SetSelected 维护，见方法注释）；
	//   - ON CONFLICT 只更新资料/凭证/更新时间，created_at_ms、is_selected、
	//     last_used_at_ms 与 id 都保持原值。
	const statement = `INSERT INTO ` + tableNameAccounts + ` (
		id, kind, provider_id, display_name,
		profile_id, profile_name, skin_model, skin_url, cape_url,
		access_token_enc, client_token_enc, refresh_token_enc,
		token_expires_at_ms, user_type, xuid,
		is_selected, created_at_ms, updated_at_ms, last_used_at_ms
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?)
	ON CONFLICT (kind, provider_id, profile_id) DO UPDATE SET
		display_name = excluded.display_name,
		profile_name = excluded.profile_name,
		skin_model = excluded.skin_model,
		skin_url = excluded.skin_url,
		cape_url = excluded.cape_url,
		access_token_enc = excluded.access_token_enc,
		client_token_enc = excluded.client_token_enc,
		refresh_token_enc = excluded.refresh_token_enc,
		token_expires_at_ms = excluded.token_expires_at_ms,
		user_type = excluded.user_type,
		xuid = excluded.xuid,
		updated_at_ms = excluded.updated_at_ms`

	if _, err := s.db.ExecContext(ctx, statement,
		item.ID, string(item.Kind), item.ProviderID, item.DisplayName,
		item.Profile.ID, item.Profile.Name, item.Profile.SkinModel,
		item.Profile.SkinURL, item.Profile.CapeURL,
		accessTokenEnc, clientTokenEnc, refreshTokenEnc,
		item.Credentials.ExpiresAtMs, item.Credentials.UserType, item.Credentials.XUID,
		createdAtMs, updatedAtMs, item.LastUsedAtMs,
	); err != nil {
		return newSystemError(ErrorCodeStorage, "保存账号失败")
	}

	return nil
}

// Delete 按 id 删除账号。
//
// 删除当前选中账号时，本方法只删行、不自动改选其他账号：行被删除后
// is_selected=1 的记录自然消失，不会留下「悬空选中」。由服务层在删除后
// 调用 SetSelected 选择剩余账号（没有剩余账号就什么都不做）——
// 存储层不做「自动选下一个」，避免在服务层不知情的情况下改变当前账号。
func (s *sqliteStore) Delete(ctx context.Context, id string) error {
	if s == nil || s.db == nil {
		return newSystemError(ErrorCodeStorage, "账号存储未打开")
	}

	result, err := s.db.ExecContext(ctx, `DELETE FROM `+tableNameAccounts+` WHERE id = ?`, id)
	if err != nil {
		return newSystemError(ErrorCodeStorage, "删除账号失败")
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return newSystemError(ErrorCodeStorage, "删除账号失败")
	}
	if affected == 0 {
		return newSystemError(ErrorCodeNotFound, "账号不存在")
	}
	return nil
}

// SetSelected 在事务内把指定账号设为当前账号（全局唯一）。
//
// 事务内顺序不能颠倒：表上有「WHERE is_selected = 1」的部分唯一索引，
// 必须先清空其他行的选中位，再把目标行置 1，否则中间状态会触发唯一约束。
//
// 参数 atMs 同时写入 last_used_at_ms：「选中账号」即「最近使用」，
// 这样列表排序与「恢复上次账号」都只需要一份时间戳。
// 目标 id 不存在时回滚事务并返回 ErrorCodeNotFound——回滚保证不会出现
// 「清空了旧选中但新选中没设上」的状态。
func (s *sqliteStore) SetSelected(ctx context.Context, id string, atMs int64) error {
	if s == nil || s.db == nil {
		return newSystemError(ErrorCodeStorage, "账号存储未打开")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return newSystemError(ErrorCodeStorage, "切换当前账号失败")
	}
	// 成功路径会显式 Commit，因此这里的 Rollback 只会是失败/panic 路径的空操作。
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx,
		`UPDATE `+tableNameAccounts+` SET is_selected = 0 WHERE is_selected = 1`); err != nil {
		return newSystemError(ErrorCodeStorage, "切换当前账号失败")
	}

	result, err := tx.ExecContext(ctx,
		`UPDATE `+tableNameAccounts+` SET is_selected = 1, last_used_at_ms = ? WHERE id = ?`,
		atMs, id)
	if err != nil {
		return newSystemError(ErrorCodeStorage, "切换当前账号失败")
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return newSystemError(ErrorCodeStorage, "切换当前账号失败")
	}
	if affected == 0 {
		// 事务回滚：旧选中保持不变。
		return newSystemError(ErrorCodeNotFound, "账号不存在")
	}

	if err := tx.Commit(); err != nil {
		return newSystemError(ErrorCodeStorage, "切换当前账号失败")
	}
	return nil
}

// Touch 更新账号的最近使用时间（不改选中态、不改资料）。
//
// 由「使用某账号启动但未切换当前账号」等场景调用；id 不存在返回 ErrorCodeNotFound。
func (s *sqliteStore) Touch(ctx context.Context, id string, atMs int64) error {
	if s == nil || s.db == nil {
		return newSystemError(ErrorCodeStorage, "账号存储未打开")
	}

	result, err := s.db.ExecContext(ctx,
		`UPDATE `+tableNameAccounts+` SET last_used_at_ms = ? WHERE id = ?`, atMs, id)
	if err != nil {
		return newSystemError(ErrorCodeStorage, "更新账号使用时间失败")
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return newSystemError(ErrorCodeStorage, "更新账号使用时间失败")
	}
	if affected == 0 {
		return newSystemError(ErrorCodeNotFound, "账号不存在")
	}
	return nil
}

// Close 释放数据库连接池。
//
// 重复调用安全（database/sql 的 Close 幂等）；nil 接收者也安全返回 nil。
func (s *sqliteStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	if err := s.db.Close(); err != nil {
		return newSystemError(ErrorCodeStorage, "关闭账号数据库失败")
	}
	return nil
}

// storedRow 是 accounts 表一行的原始形态（凭证仍是密文 BLOB）。
//
// 引入中间结构是为了让 List/Get 共用同一套 Scan 与转换逻辑，
// 也避免把「密文列」与「明文字段」混在同一个结构里传递。
type storedRow struct {
	id          string
	kind        string
	providerID  string
	displayName string

	profileID   string
	profileName string
	skinModel   string
	skinURL     string
	capeURL     string

	accessTokenEnc  []byte
	clientTokenEnc  []byte
	refreshTokenEnc []byte

	tokenExpiresAtMs int64
	userType         string
	xuid             string

	isSelected   int
	createdAtMs  int64
	updatedAtMs  int64
	lastUsedAtMs int64
}

// scanTargets 返回与 accountColumns 顺序一致的 Scan 目标列表。
func (r *storedRow) scanTargets() []any {
	return []any{
		&r.id, &r.kind, &r.providerID, &r.displayName,
		&r.profileID, &r.profileName, &r.skinModel, &r.skinURL, &r.capeURL,
		&r.accessTokenEnc, &r.clientTokenEnc, &r.refreshTokenEnc,
		&r.tokenExpiresAtMs, &r.userType, &r.xuid,
		&r.isSelected, &r.createdAtMs, &r.updatedAtMs, &r.lastUsedAtMs,
	}
}

// scan 从 rows 读取一行（与 Get 共用 scanTargets，避免两处列顺序漂移）。
func (r *storedRow) scan(rows *sql.Rows) error {
	return rows.Scan(r.scanTargets()...)
}

// toStoredAccount 把原始行转换为账号摘要（不含凭证；凭证由 decryptCredentials 填充）。
func (r *storedRow) toStoredAccount() StoredAccount {
	return StoredAccount{
		Account: Account{
			ID:          r.id,
			Kind:        Kind(r.kind),
			ProviderID:  r.providerID,
			DisplayName: r.displayName,
			// 纹理地址在读取时统一规范化（存量账号里的 http://textures.minecraft.net
			// 会被 WebView 的混合内容策略拦截，导致界面没有头像）。
			Profile: normalizeProfileTextures(Profile{
				ID:        r.profileID,
				Name:      r.profileName,
				SkinModel: r.skinModel,
				SkinURL:   r.skinURL,
				CapeURL:   r.capeURL,
			}),
			CreatedAtMs:  r.createdAtMs,
			UpdatedAtMs:  r.updatedAtMs,
			LastUsedAtMs: r.lastUsedAtMs,
			Selected:     r.isSelected == 1,
		},
	}
}

// decryptCredentials 解密三列凭证。
//
// 成功时返回完整 Credentials；失败时返回只带非敏感元数据（过期时间/UserType/XUID）
// 的空凭证与错误，由调用方按 List/Get 的不同策略处理。
// 错误来自 Cipher，必定是 ErrorCodeCrypto 且不含任何明文。
func (s *sqliteStore) decryptCredentials(raw storedRow) (Credentials, *Error) {
	credentials := Credentials{
		ExpiresAtMs: raw.tokenExpiresAtMs,
		UserType:    raw.userType,
		XUID:        raw.xuid,
	}
	if s.cipher == nil {
		// 构造 Store 时已强制要求 Cipher，这里只是防御性判空：绝不 panic。
		return credentials, newSystemError(ErrorCodeCrypto, "凭证加密尚未初始化")
	}

	accessToken, cryptoErr := s.cipher.Decrypt(raw.accessTokenEnc)
	if cryptoErr != nil {
		return credentials, cryptoErr
	}
	clientToken, cryptoErr := s.cipher.Decrypt(raw.clientTokenEnc)
	if cryptoErr != nil {
		return credentials, cryptoErr
	}
	refreshToken, cryptoErr := s.cipher.Decrypt(raw.refreshTokenEnc)
	if cryptoErr != nil {
		return credentials, cryptoErr
	}

	credentials.AccessToken = accessToken
	credentials.ClientToken = clientToken
	credentials.RefreshToken = refreshToken
	return credentials, nil
}
