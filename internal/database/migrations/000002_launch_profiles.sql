-- 第二个数据库迁移：建立启动配置表 launch_profiles，用于持久化上一次成功启动的组合。
--
-- 依据 P2 决策记录 Q-1 / D-11（启动配置表 `launch_profiles` + `launchProfileId` 方案）
-- 与任务卡 launcher/docs/tasks/P2/P2-13.md 第 47 条。
-- 本迁移只建表与唯一索引，不插入任何业务数据：初始配置由用户在启动页选择后
-- 由 Go 侧（internal/launch/profile.go）写入，避免出现「默认配置」这种伪数据。

-- 启动配置表：保存「Java 可执行文件 + 游戏目录 + 版本 id」这组可直接复用的启动条件。
--
-- 说明：
--   - java_path 与 game_directory 存**绝对路径**，用于下次启动直接复用；
--     但 Go 侧对外只返回脱敏展示串（见 internal/launch/dto.go 的 LaunchProfileSummary），
--     绝对路径永不返回前端。
--   - 同一组 (java_path, game_directory, version_id) 只保留一行：重复保存走更新，
--     由下方唯一索引保证，避免用户反复启动后表无限膨胀。
CREATE TABLE IF NOT EXISTS launch_profiles (
    -- 配置唯一标识；由 Go 侧生成，写入后不再变化。
    id TEXT PRIMARY KEY,

    -- 配置显示名称；P2 阶段由 Go 侧按版本 id 自动生成，供后续配置列表界面使用。
    name TEXT NOT NULL,

    -- Java 可执行文件绝对路径；仅后端使用，不返回前端。
    java_path TEXT NOT NULL,

    -- 游戏根目录绝对路径；仅后端使用，不返回前端。
    game_directory TEXT NOT NULL,

    -- 游戏版本 id（例如 1.20.1）；可以展示给前端。
    version_id TEXT NOT NULL,

    -- 配置首次创建时间，UTC Unix 毫秒。
    created_at_ms INTEGER NOT NULL,

    -- 配置最近一次修改时间，UTC Unix 毫秒；重复保存时更新。
    updated_at_ms INTEGER NOT NULL,

    -- 配置最近一次被用于启动的时间，UTC Unix 毫秒；重复保存时更新，
    -- 「恢复上次配置」按该字段倒序取第一条。
    last_used_at_ms INTEGER NOT NULL
);

-- 启动条件唯一索引：保证同一组 Java、游戏目录、版本 id 不产生重复行。
CREATE UNIQUE INDEX IF NOT EXISTS idx_launch_profiles_identity
    ON launch_profiles (java_path, game_directory, version_id);

-- 最近使用时间索引：支撑「取最近一条配置」的排序查询。
CREATE INDEX IF NOT EXISTS idx_launch_profiles_last_used
    ON launch_profiles (last_used_at_ms DESC);
