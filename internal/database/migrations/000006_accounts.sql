-- 账号表：持久化本地账号摘要与该账号的加密凭证。
--
-- 本迁移由账号子系统（task-4）引入，是 000006 全新增表；表结构依据
-- 《接口冻结清单》账号子系统的 StoredAccount 契约与 internal/account 包注释。
--
-- 兼容性：
--   - 全新表，旧版本数据库升级时直接创建，不需要数据回填；
--   - 建表建索引全部使用 IF NOT EXISTS，重复执行安全（迁移执行器本身也保证
--     每个版本只应用一次，这里只是多一层防御）；
--   - 后续字段变更必须走新迁移，不得回改本文件（迁移文件受 checksum 绑定，
--     任何改动都会直接触发 DB_MIGRATION_001）。
--
-- 安全约束（硬性）：
--   - access_token_enc / client_token_enc / refresh_token_enc 三列只允许写入
--     AES-256-GCM 密文（见 internal/account/cipher.go），数据库中绝不出现
--     明文 token；三列允许为 NULL，表示该凭证不存在（例如离线账号没有 token）；
--   - 本迁移不建任何覆盖凭证列的索引，避免密文进入索引页后更难清理；
--   - 凭证是后端专用数据，不经过任何面向前端的 DTO。
--
-- 身份与去重：
--   - id 是本地账号标识（UUID v4），由服务层生成，写入后不再变化；
--   - (kind, provider_id, profile_id) 才是一个账号的稳定身份：相同身份重复登录
--     只会更新同一行（Upsert），不会产生重复账号；
--   - profile_name 是可变昵称，不参与身份判定。
--
-- 选中态：
--   - is_selected 取值 0/1，全局至多一行为 1；
--   - 由应用层在事务内「先清空再置 1」维护（Store.SetSelected），并用下方
--     部分唯一索引兜底；本列不参与任何安全判定。

CREATE TABLE IF NOT EXISTS accounts (
    -- 本地账号标识（UUID v4 字符串），供选择/删除/刷新账号使用。
    id TEXT PRIMARY KEY,

    -- 账号来源类型：microsoft / offline / yggdrasil / ninevoxel。
    kind TEXT NOT NULL,

    -- 来源实例标识：微软固定为 microsoft，Yggdrasil 为 API 根，离线为空字符串。
    provider_id TEXT NOT NULL DEFAULT '',

    -- 界面展示名（通常等于 profile_name）。
    display_name TEXT NOT NULL,

    -- Minecraft 档案 UUID：32 位无连字符小写十六进制，是账号身份的一部分。
    profile_id TEXT NOT NULL,

    -- 档案玩家名（游戏内昵称）；昵称可变，不参与身份判定。
    profile_name TEXT NOT NULL,

    -- 皮肤模型：classic / slim；未知时为空字符串。
    skin_model TEXT NOT NULL DEFAULT '',

    -- 皮肤地址；没有皮肤时为空字符串。
    skin_url TEXT NOT NULL DEFAULT '',

    -- 披风地址；没有披风时为空字符串。
    cape_url TEXT NOT NULL DEFAULT '',

    -- Access Token 的 AES-256-GCM 密文；NULL 表示没有该凭证。
    access_token_enc BLOB,

    -- Client Token 的 AES-256-GCM 密文；NULL 表示没有该凭证（微软账号为空）。
    client_token_enc BLOB,

    -- Refresh Token 的 AES-256-GCM 密文；NULL 表示没有该凭证（离线账号为空）。
    refresh_token_enc BLOB,

    -- Access Token 过期时间（UTC Unix 毫秒）；0 表示不过期或未知。
    token_expires_at_ms INTEGER NOT NULL DEFAULT 0,

    -- 启动参数 user_type 的取值：msa / mojang / legacy；未知时为空字符串。
    user_type TEXT NOT NULL DEFAULT '',

    -- Xbox User ID（微软账号），用于启动参数 auth_xuid；其他来源为空字符串。
    xuid TEXT NOT NULL DEFAULT '',

    -- 是否为当前选中账号：0/1，全局至多一行为 1。
    is_selected INTEGER NOT NULL DEFAULT 0 CHECK (is_selected IN (0, 1)),

    -- 账号首次创建时间（UTC Unix 毫秒）；Upsert 冲突时保留原值。
    created_at_ms INTEGER NOT NULL,

    -- 账号最近一次资料或凭证更新时间（UTC Unix 毫秒）。
    updated_at_ms INTEGER NOT NULL,

    -- 账号最近一次被选为当前账号的时间（UTC Unix 毫秒）；0 表示从未被选中。
    last_used_at_ms INTEGER NOT NULL DEFAULT 0
);

-- 账号身份唯一索引：同一 (kind, provider_id, profile_id) 只保留一行，
-- 登录成功后重复 Upsert 走 ON CONFLICT 更新而不是新增。
CREATE UNIQUE INDEX IF NOT EXISTS idx_accounts_identity
    ON accounts (kind, provider_id, profile_id);

-- 全局唯一选中索引：部分唯一索引保证 is_selected = 1 至多一行。
-- 这是「全局只选中一个账号」的数据库级兜底，正常写入顺序由 SetSelected 事务保证。
CREATE UNIQUE INDEX IF NOT EXISTS idx_accounts_selected
    ON accounts (is_selected) WHERE is_selected = 1;

-- 列表排序索引：支撑「选中优先、最近使用优先、创建时间」的稳定排序。
CREATE INDEX IF NOT EXISTS idx_accounts_list_order
    ON accounts (is_selected DESC, last_used_at_ms DESC, created_at_ms ASC, id ASC);
