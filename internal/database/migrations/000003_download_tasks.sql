-- 下载任务表：保存下载队列中每个制品任务的调度状态、断点信息与校验结果。
--
-- 本迁移由 P3-04（下载队列与调度）引入。表结构依据《数据库设计与迁移规范》§3.10
-- 与决策记录 D-25：download_tasks 在 P3 首次创建，因此一次定义完整字段集，
-- 不需要像旧表那样补列；后续任何字段变更都必须走新迁移，不得回改本文件
-- （迁移文件受 checksum 绑定，改动会直接触发 DB_MIGRATION_001）。
--
-- 字段归属说明（哪些列本卡写入、哪些列留给后续卡）：
--   - P3-04 写入：id、parent_task_id、artifact_id、version_id、task_type、state、
--     source_uri、expected_size、downloaded_size、expected_sha1、expected_sha256、
--     retry_count、error_code、error_message_safe、created_at_ms、
--     updated_at_ms、completed_at_ms、plan_hash、target_kind、
--     staging_rel_path、target_rel_path；
--   - P3-05 写入：etag、last_modified（续传一致性依据，D-34）；
--   - P3-06 写入：range_start、verified_at_ms（提交后校验时间，D-35 的失效语义承载列）。
--
-- 安全约束：source_uri 只在后端流转，绝不进入面向前端的 DTO；
-- error_message_safe 必须是已脱敏的安全文案，禁止写入完整 URL 查询串或响应体。

CREATE TABLE IF NOT EXISTS download_tasks (
    -- 任务唯一标识（由后端生成的 UUID 形式字符串）。
    id TEXT PRIMARY KEY,

    -- 父任务标识：资产子任务挂到计划任务上，父任务聚合进度（P3-07）。
    parent_task_id TEXT NOT NULL DEFAULT '',

    -- 关联的制品标识（artifacts.id），计划级任务为空。
    artifact_id TEXT NOT NULL DEFAULT '',

    -- 所属 Minecraft 版本 id：版本级任务（plan / version_json / client_jar）必填，
    -- 共享内容级任务（library / asset）可空。
    -- 为什么必须落库：P3-07 的重启恢复要按版本筛出未完成任务，
    -- UI 也要按版本聚合进度；只留在内存里会导致重启后任务「无主」。
    version_id TEXT NOT NULL DEFAULT '',

    -- 任务类型：plan / version_json / client_jar / library / asset_index / asset。
    task_type TEXT NOT NULL,

    -- 调度状态，取值见 P3 状态机（queued/resolving/downloading/paused/
    -- verifying/committing/completed/failed/cancelled）。
    state TEXT NOT NULL,

    -- 下载源地址（仅后端使用，不含查询串以外的凭证；不得返回前端）。
    source_uri TEXT NOT NULL,

    -- staging 相对路径（相对数据根），由 P3-06 在传输前写入。
    staging_rel_path TEXT NOT NULL DEFAULT '',

    -- 最终目标相对路径（相对数据根），由 P3-03 的 ResolveTarget 解析。
    target_rel_path TEXT NOT NULL DEFAULT '',

    -- 目标路径根类别：shared / version / runtime。
    target_kind TEXT NOT NULL DEFAULT 'shared',

    -- 期望字节数，0 表示未知。
    expected_size INTEGER NOT NULL DEFAULT 0,

    -- 已下载字节数，续传时作为起点。
    downloaded_size INTEGER NOT NULL DEFAULT 0,

    -- 已有 .part 文件的字节偏移（重启恢复用，P3-06 写入）。
    range_start INTEGER NOT NULL DEFAULT 0,

    -- 期望 SHA-1（资产以 SHA-1 为寻址键，D-27），空串表示无。
    expected_sha1 TEXT NOT NULL DEFAULT '',

    -- 期望 SHA-256，空串表示无（部分制品只有 SHA-1 或 SHA-512）。
    expected_sha256 TEXT NOT NULL DEFAULT '',

    -- 服务端 ETag，续传一致性校验用（D-34，P3-05 写入）。
    etag TEXT NOT NULL DEFAULT '',

    -- 服务端 Last-Modified，ETag 缺失时的回退依据（D-34）。
    last_modified TEXT NOT NULL DEFAULT '',

    -- 计划哈希（D-26）：sha256(versionId + resolverVersion + 规范化计划 JSON)，
    -- 既是幂等键也是「计划漂移」的判定依据。
    plan_hash TEXT NOT NULL DEFAULT '',

    -- 已重试次数，重试上限由调度器常量决定（D-31）。
    retry_count INTEGER NOT NULL DEFAULT 0,

    -- 稳定错误码（如 DOWNLOAD_NETWORK_001），成功路径为空串。
    error_code TEXT NOT NULL DEFAULT '',

    -- 已脱敏的错误文案，只用于界面展示，禁止包含路径与凭证。
    error_message_safe TEXT NOT NULL DEFAULT '',

    -- 提交后校验通过的时间，UTC Unix 毫秒；0 表示尚未校验（D-35）。
    verified_at_ms INTEGER NOT NULL DEFAULT 0,

    -- 创建时间与最近一次状态更新时间，UTC Unix 毫秒。
    created_at_ms INTEGER NOT NULL,
    updated_at_ms INTEGER NOT NULL,

    -- 进入终态的时间，UTC Unix 毫秒；未结束为 0。
    completed_at_ms INTEGER NOT NULL DEFAULT 0
);

-- 调度循环按状态筛选待执行与运行中任务。
CREATE INDEX IF NOT EXISTS idx_download_tasks_state ON download_tasks (state);

-- 父任务聚合进度时按 parent_task_id 查询子任务。
CREATE INDEX IF NOT EXISTS idx_download_tasks_parent ON download_tasks (parent_task_id);

-- 计划幂等判定与「同一计划重复入队」查询。
CREATE INDEX IF NOT EXISTS idx_download_tasks_plan ON download_tasks (plan_hash);

-- 同一制品的历史任务查询（去重与失效判定）。
CREATE INDEX IF NOT EXISTS idx_download_tasks_artifact ON download_tasks (artifact_id);

-- 按版本筛选任务：重启恢复与 UI 按版本聚合进度都要用它。
CREATE INDEX IF NOT EXISTS idx_download_tasks_version ON download_tasks (version_id);
