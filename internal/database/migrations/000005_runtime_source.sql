-- Java 运行时来源扩展：为第三方来源（Adoptium / Eclipse Temurin）补齐校验值、
-- 来源标识与更新检查缓存。
--
-- 本迁移由 P3-14 引入，是 000004 的纯增量变更（只加列、不改既有列语义）：
--   - SHA-256：Adoptium 只提供 SHA-256 校验值，不能复用 000004 的 sha1 列
--     （D-27 明确「严禁把 SHA-1 填进 SHA-256 字段」，反向同理）；
--   - source_name：区分「Mojang 官方清单安装」与「第三方发行版安装」，
--     更新检查只处理后者；Mojang 官方 Runtime 的该列保持空字符串；
--   - latest_checked_at_ms / latest_full_version：更新检查的缓存结果，
--     让「Java 运行时」页打开时能先展示上次结论，不必每次都联网
--     （ListRuntimeUpdates 只读这两列，不触发网络）。
--
-- 兼容性：四列都带 DEFAULT，旧记录自动取值 `''` 与 0，无需数据回填；
-- SQLite 的 ALTER TABLE ADD COLUMN 不允许非恒定默认值，这里全部使用字面量。
--
-- 安全约束：sha256 与 sha1 一样只是校验值，不是访问凭证；不写入日志。

-- 归档的 SHA-256（64 位小写十六进制）；Mojang 官方来源为空。
ALTER TABLE java_runtimes ADD COLUMN sha256 TEXT NOT NULL DEFAULT '';

-- 来源标识：`adoptium` 表示第三方发行版安装；空字符串表示 Mojang 官方清单或本机探测。
ALTER TABLE java_runtimes ADD COLUMN source_name TEXT NOT NULL DEFAULT '';

-- 最近一次更新检查的时间（毫秒），0 表示从未检查。
ALTER TABLE java_runtimes ADD COLUMN latest_checked_at_ms INTEGER NOT NULL DEFAULT 0;

-- 最近一次更新检查拿到的远端完整版本串（例如 `21.0.13+9-LTS`）；空字符串表示尚未成功检查。
ALTER TABLE java_runtimes ADD COLUMN latest_full_version TEXT NOT NULL DEFAULT '';
