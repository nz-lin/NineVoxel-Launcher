-- 首个数据库迁移：建立设置持久化所需的全部表结构。
--
-- 依据 launcher/docs/tasks/P1/接口冻结清单.md §6.1、§6.2 与
-- docs/NineVoxel-Launcher-开发方案/docs/architecture/数据库设计与迁移规范.md。
-- 本迁移只建表，不插入任何业务数据：默认值由 Go 侧（internal/settings/schema.go）
-- 统一提供，避免「默认值同时存在于 SQL 与 Go」造成的双份真相。

-- 迁移记录表：记录每个已应用迁移的版本、时间与内容校验值。
-- 校验值用于启动时比对迁移文件内容，一旦不一致说明文件被改动过，必须报错而非静默修复。
CREATE TABLE IF NOT EXISTS schema_migrations (
    -- 迁移版本号，取迁移文件名的数字前缀（例如 000001 对应 1）。
    version INTEGER PRIMARY KEY,

    -- 迁移应用时间，UTC Unix 毫秒。
    applied_at_ms INTEGER NOT NULL,

    -- 迁移文件内容的 SHA-256 校验值（十六进制小写）。
    checksum TEXT NOT NULL,

    -- 迁移说明，取自迁移文件首行注释，便于人工核对。
    description TEXT NOT NULL
);

-- 应用设置表：以 key-value 形式保存非秘密设置项。
-- 键名必须在 Go 侧白名单内（internal/settings/schema.go），
-- 值统一以 JSON 编码存入 value_json，因此布尔与数字都能无损往返。
CREATE TABLE IF NOT EXISTS app_settings (
    -- 设置项标识，例如 appearance.theme、window.width。
    -- 作为主键保证同一个 key 只有一行，写入天然幂等。
    key TEXT PRIMARY KEY,

    -- JSON 编码后的设置值，例如 "dark"、1120、true。
    value_json TEXT NOT NULL,

    -- 最近一次写入时间，UTC Unix 毫秒；用于排查「设置何时被改过」。
    updated_at_ms INTEGER NOT NULL
);
