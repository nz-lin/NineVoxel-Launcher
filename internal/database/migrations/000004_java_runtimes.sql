-- Java 运行时表：记录「本机探测到的」与「启动器安装的」Java 运行时，以及当前选中项。
--
-- 本迁移由 P3-09（Runtime 下载、解压事务提交与选择）引入，表结构依据
-- 《接口冻结清单》§5.3 与决策记录 D-28：
--   - 选中结果落列而不新增设置 key（D-28 已冻结：P3 不新增设置 key）；
--   - 不建 instance_id 列：P3 不引入实例概念，实例级绑定留给 P5；
--   - 本表只服务「选择」与「展示」，不承担权限或安全判定。
--
-- 与 artifacts 表的关系：D-47 决定 artifacts 表推迟，因此本表不引用外键，
-- 也不承担「对象已验证」的判据；已安装 Runtime 的完整性以磁盘实际文件 +
-- 流式哈希校验为准（重复校验的开销是可接受的取舍）。
--
-- 安全约束：
--   - source_uri 只在后端流转，必须剔除查询串后才允许落库与展示；
--   - local_rel_path 必须是相对数据根的 slash 路径，绝不存绝对路径（避免泄露用户名）；
--   - sha1 仅用于校验下载归档，不是访问凭证。
--
-- selected 列语义：
--   - 取值 0/1，1 表示当前选中；同一时刻至多一行为 1（由应用层在事务内先清后置，
--     并用下方唯一部分索引兜底）；
--   - 选中的 Runtime 是后续启动流程读取 Java 可执行文件位置的唯一来源；
--   - 本列不表达「兼容性」：与当前系统架构不匹配的记录也可以被选中，
--     但需要用户显式确认（force=true），兼容性结论每次都重新计算。

CREATE TABLE IF NOT EXISTS java_runtimes (
    -- 运行时唯一标识（`<component|local>-<platform>-<arch>-<version>`，字符集 [a-z0-9._-]）。
    id TEXT PRIMARY KEY,

    -- 来源类别：`local`（本机探测）或 `downloaded`（启动器安装）。
    kind TEXT NOT NULL,

    -- 官方组件名，例如 `java-runtime-gamma`；本机探测的 Java 为空。
    component TEXT NOT NULL DEFAULT '',

    -- Java 主版本号（1.8 → 8）；元数据或版本名无法解析时为 0（表示未知）。
    major_version INTEGER NOT NULL DEFAULT 0,

    -- 完整版本串，例如 `21.0.5+11-LTS`，用于展示与诊断。
    full_version TEXT NOT NULL DEFAULT '',

    -- 平台标识：windows / mac-os / linux（与官方清单一致）。
    platform TEXT NOT NULL DEFAULT '',

    -- 架构标识：x64 / x86 / aarch64（与 P3-08 探测结论一致）。
    arch TEXT NOT NULL DEFAULT '',

    -- 位数：32 / 64，未知为 0。
    bitness INTEGER NOT NULL DEFAULT 0,

    -- 厂商标签：Adoptium / Microsoft / Zulu / Oracle / OpenJDK / Other / Unknown。
    vendor TEXT NOT NULL DEFAULT '',

    -- 下载来源地址：已剔除查询串，仅供诊断，不进入前端 DTO。
    source_uri TEXT NOT NULL DEFAULT '',

    -- 归档 SHA-1（官方清单声明值），用于重装前比对与诊断。
    sha1 TEXT NOT NULL DEFAULT '',

    -- 安装目录相对数据根的路径，例如 `runtimes/java-runtime-gamma-windows-x64-21.0.5`。
    local_rel_path TEXT NOT NULL DEFAULT '',

    -- 最近一次校验通过的时间（毫秒），0 表示尚未校验。
    verified_at_ms INTEGER NOT NULL DEFAULT 0,

    -- 是否当前选中：1 = 选中，0 = 未选中；同一时刻至多一行为 1。
    selected INTEGER NOT NULL DEFAULT 0,

    -- 记录创建时间（毫秒），用于列表排序与诊断。
    created_at_ms INTEGER NOT NULL DEFAULT 0
);

-- 唯一部分索引：从数据库层面保证「至多一行 selected = 1」。
-- 应用层在事务内先清后置，本索引用于兜底并发或异常路径。
CREATE UNIQUE INDEX IF NOT EXISTS ux_java_runtimes_selected
    ON java_runtimes (selected)
    WHERE selected = 1;
