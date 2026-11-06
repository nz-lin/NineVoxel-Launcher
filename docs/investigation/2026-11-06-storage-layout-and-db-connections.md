# 调查报告：制品落盘现状、数据目录布局与数据库连接治理

> 调查日期：2026-11-06 · 本文记录现状与整改方案。
> 范围：① 制品/资产三条写入链的落盘定位；② `data/` 数据根布局问题与整改建议；③ 各服务打开 `app.db` 的方式差异与治理建议；附录 E 给出 SQLite 引擎替换评估。
> 代码引用均为当前提交的实测行号（grep 验证），随时可能失效，阅读方式以"调用链 + 结论"为主。
>
> **实施状态（2026-11-06 更新）**：三项治理已落地。
> - ① 路径与描述收敛：`artifact.DatabasePath` 成为唯一位置规则；`artifact`/`download`
>   的落盘注释已改为运行时实际行为；`instance` 不再自行拼 `app.db` 路径。
> - ② 数据库归拢：`app.db` 迁到 `data/db/`，启动时自动搬迁旧 `data/app.db`、WAL/SHM
>   与备份；`data/secrets/account.key` 位置保持不变。
> - ③ 连接治理：新增 `database.Default` 进程级共享池，main 在装配前预热；
>   settings/account/launch/runtime/instance/download 全部改用它，不再各自 Open/Migrate。
> - 未实施（保留为后续）：A.5-2 死代码删除、A.5-3 安装器缓存归位、B3-2 `core-index/` 改名、
>   D-1 的「构造参数注入」形态（本次用共享池 + 包级 provider，测试仍可隔离）、
>   D-3 中 download 降级为内存库的策略调整。

---

## A. 制品与资产的落盘现状

### A.1 运行时真实写入路径（逐一核实）

| 制品类型 | 实际目标路径 | 生成代码 | 证据 |
|---|---|---|---|
| 客户端 jar | `versions/<id>/<id>.jar` | `internal/version/plan.go:265` | 走 `ResolveTarget(KindClient, …)` → `DirVersions` |
| 资产索引 | `assets/indexes/<indexId>.json` | `internal/version/plan.go:308`、`internal/download/assets.go:354/387` | 走 `AssetIndexRelPath` → `DirAssetIndexes` |
| 资产对象 | `assets/objects/<前2位>/<sha1>` | `internal/version/plan.go:489`、`internal/download/assets.go:419/492`、`internal/instance/install.go:110` | 走 `AssetObjectRelPath` → `DirAssetObjects` |
| 日志配置 | `assets/log_configs/<id>` | `internal/version/plan.go:422` | 走 `LogConfigRelPath` |
| 依赖库 jar | `libraries/<maven path>`（直达最终位置，不进缓存） | `internal/version/plan.go:448`（`mavenPlanArtifact`）、`internal/loader/forge_install.go:386` | 走 `LibraryRelPath` → `DirLibraries` |
| 加载器安装器 | `artifacts/objects/<前2位>/<sha1>`（**唯一还写 `artifacts/` 的链路**） | `internal/loader/installer.go:66` | 走 `ObjectRelPath(SHA-1, …)` → `DirArtifactObjects` |
| Forge 处理器 jar | `cache/processors/<maven path>` | `internal/loader/forge_install.go:451` | 手工拼 `DirCache + "/processors/"` |
| 虚拟布局组装 | `versions/<id>/virtual/<indexId>/<逻辑名>`（硬链接→软链→复制，源是 `assets/objects`） | `internal/download/assets.go:547` | 注释与代码均为 `DirAssetObjects` |

### A.2 结论一：**不存在资产对象"双写"**

- 资产对象、资产索引在运行时只写 `assets/` 一处；所有读取（虚拟布局组装、修复校验）也从 `assets/objects` 读。
- `artifacts/objects` 里**不是**资产对象，而是加载器安装器 jar（Forge/NeoForge/Fabric 官方 installer，按 SHA-1 内容寻址）。它的语义已经从早期设计的"共享内容寻址缓存"漂移成了"安装器缓存"。

真正的乱点在于**语义错位**，而非磁盘上有两份同内容文件集中在两处路径的混乱：

### A.3 冻结文档已过时 / 死代码（列表）

以下内容与代码已有出入，`grep` 全仓验证（排除 tests）：

| 项 | 现状 | 与之矛盾的代码/注释 |
|---|---|---|
| `ResolveTarget` 对 `library`/`native`/`asset` 的映射（`internal/artifact/layout.go:314-315` 指向 `artifacts/objects`） | **死路径**：运行时没有任何制品经由它规划目标（唯一调用点只解析 `KindClient`），实际库/资产走 `LibraryRelPath` / `AssetObjectRelPath` | `layout.go:288` 注释 "library / native / asset → artifacts/objects" |
| `insternal/download/doc.go`（"现代共享对象写 `artifacts/objects/<hash[0:2]>/<hash>`"） | 与实际写入位置的注释不符 | `assets.go:547` 读的是 `assets/objects` |
| `artifact/dir.go` 的 `artifacts/indexes` / `artifacts/metadata` | **零运行时使用**（metadata 函数无调用点；index 被资产版 `AssetIndexRelPath` 取代） | 保留在 doc.go 的路径表 |
| `artifact.InstallerCacheRelPath`（`cache/installers/`） | **死代码**：安装器实际走 `ObjectRelPath` | `installer.go:66` 从未调用 |
| AGENTS.md 中"libraries 是最终位置不走缓存" | 该说法与 `version/plan.go` 一致；但 `artifact.ResolveTarget` 的映射还保留旧方案 | 两套描述并存，审查者容易得出错误结论 |

### A.4 附带风险（调查过程中发现，记录在案）

1. **`artifacts/objects` 永不清理**：安装器 jar 是一次性缓存(`cache` 语义)，但没有 GC 或 prune 逻辑；核心清单（`.nv-core-manifest.json`）不记录它，`CleanupUnusedCore` 也不扫码。破坏后走"重新下载"的修复路径即可，风险低，但破烂长期堆积。
2. **`artifacts/object` 命名误导审查**：名字里带 object 会让人联想到资产对象，实际是"按 SHA-1 寻址的安装器存放点"。归位到 `cache/installers/<sha1>.jar`（现在已有现成死函数'InstallerCacheRelPath'可复活）是最小改动方案。

### A.5 建议（落地成本从低到高）

> 状态：A.5-1（更新文档/注释）已完成；A.5-2、A.5-3 尚未实施。

| # | 方案 | 成本 | 收益 |
|---|---|---|---|
| 1 | **不做任何代码改动，只更新注释/doc.go/AGENTS.md** ——删除 `artifacts/objects` 作为"共享内容寻址缓存"的说法、删掉 `ResolveTarget` 对 library/native/asset 的 stale 映射描述 | 极低 | 消除认知矛盾，后续审查不再误判 |
| 2 | 删除死代码：`DirArtifactMetadata`/`MetadataRelPath`/`DirArtifactIndexes`/`IndexRelPath`/`InstallerCacheRelPath` 中无引用者 | 低 | 缩小 API 面（前提：确认没有 P5+ 任务计划使用） |
| 3 | 安装器缓存从 `artifacts/objects/<xx>/<sha1>` 迁移到 `cache/installers/<sha1>.jar`（复活现成 helper），或原地保留路径但补充 prune | 中（涉及安装器 plan 哈希与恢复） | 命名回归字面，cache 语义一致 |

> 优先级：A.5-1 应当先做（零风险）；-2/-3 应等一个独立的修复卡。

---

## B. `data/` 数据根目录布局（整改方案）

### B.1 现状全景（按运行时实际生成物）

```
data/                               ← artifact.DataRoot()
├── app.db                          ← 6 个服务各自打开 / 共享
├── app.db / app.db-wal / app.db-shm
├── app.db.backup-<version>-<ts>    ← 迁移备份散在数据根顶层(保留 3 份)
├── secrets/                        ← 账号凭证密钥(account, 与 app.db 同级)
├── logs/
├── downloads/staging | quarantine
├── artifacts/objects/              ← 实际是加载器安装器缓存(见 A)
├── libraries/                      ← Maven 布局, classpath 直指
├── assets/objects/:indexes/:log_configs/
├── versions/<id>/(+ .nv-core-manifest.json)
├── loaders/<id>/(+ .nv-core-manifest.json)
├── core-index/                     ← 重建租约 leases.json 等
├── runtimes/<jreId>/
├── cache/installers/ （空, 见 A.3）/processors/
└── instances/<id>/(mods, saves, config, logs, natives, instance.json, patches/)
```

### B.2 问题清单

| # | 问题 | 严重度 | 说明 |
|---|---|---|---|
| B1 | `app.db.backup-*` 散落在数据根顶层，与 `secrets/`、`versions/` 平级 | 中 | 由 `database/migrate.go:488` 命名规则决定；多一个目录体面的聚会点放 db 相关文件更整洁 |
| B2 | `core-index/` 命令含糊 | 中 | 里面装的是重建事务租约、版本清单索引等"元状态"，用户看了不知所云；重命名成本高（涉及 core 包与已有测试 fixture） |
| B3 | `artifacts/` 语义漂移（见 A.2） | 中 | 目录名对"共享制品库"的暗示早已失效 |
| B4 | 数据根顶层文件/目录数量达 15+，且 db、secrets、日志、缓存、版本、实例全部平铺 | 观感问题 | 可以整理但不急于动手 |

> 仓库根（非 data 目录）里 `bin/`、`.task/`、`.pnpm-store/`、`webview/`、`logs/`、`frontend/dist` 已在 `.gitignore`，`main.go` / `appservice.go` / `logservice.go` 是 Wails 项目惯例。repo 顶层结构本身没毛病。

### B.3 整改方案（B 组，不动核心写入链；每项单独可退出）

| # | 方案 | 影响面 | 风险 |
|---|---|---|---|
| B3-1 | 把 `app.db` 及备份挪进 `data/db/`：`app.db` 移到 `data/db/app.db`，备份命名 `data/db/backup-<ver>-<ts>.db` | `settings.ResolveDatabasePath`、`migrate.go` 的路径拼接与 prune、`instance/service.go`（现在自己拼 DataRoot+\app.db，见 D.3）、测试 fixture | 低-中；注意 `database.migrate` 的 hunt `app.db.backup-` 前缀逻辑与 `PRAGMA database_list` 都要跟路径走。不需要数据迁移——SQLite 文件直接挪就是合规动作；但必须做一次"老 app.db 在数据根顶层"的检测+搬迁(用户已存在的数据) |
| B3-2 | `core-index/` → `data/db/state/` 争议大，更审慎的选法仅是**改名 `core-index/` → `core-state/`** 且不动 leas 文件 | core 包、instance 测试 fixture | 中；与 core 重建事务耦合，需要单独评估 |
| B3-3 | 接受现状，只补 `doc.go` | 无代码改动 | 无 |

> 建议：B3-1 做成一个"数据库搬家"任务，与 D 组(连接治理)一并验收；B3-2 另立任务。

---

## C. （本文核心结论摘要，可只读本节）

1. **资产没有双写**。资产对象/索引仅落 `assets/`，`artifacts/` 里装的是加载器安装器，与资产无关。AGENTS.md 的"共享内容寻址缓存"表述、`artifact.ResolveTarget` 的路径映射、`download/doc.go` 的落盘说明都停留在旧设计，**全部应该更新为现状**。
2. **真正想要"变整洁"的动作**（低成本）只有三件：更新文档、清理死代码、把安装器缓存归位到 `cache/installers/`（或让它可清理）。
3. `data/` 目录里值得动手的一项：db 相关文件归拢到 `data/db/`（含备份），并写一个"老 app.db 存在则搬迁"的启动逻辑。`core-index/` 改名值得另立任务。

---

## D. 数据库连接治理

### D.1 打开 `app.db` 的全部位置（治理前状态：7 处 × 3 种 lazy 模式）

| # | 包 | 初始化方式 | 说明 |
|---|---|---|---|
| 1 | `internal/settings/service.go:171` (`openStore`) | 服务初始化时 `OpenSQLiteStore(ctx)`：解析路径→`Open`→`Migrate` | settings 服务持有长期连接 |
| 2 | `internal/settings/initial_theme.go:22` (`LoadThemePreference`) | **窗口创建前**自己 `OpenSQLiteStore` 读完即 `Close`，重复同一套 Open+Migrate | 与 #1 是两次独立的 Open+Migrate，仅为抢在窗口渲染前读到主题 |
| 3 | `internal/account/wails.go:88`（经 `sharedAccountService` 的 `sync.Once`） | lazy 一次性，包级可变状态 holder | 与 launch/runtime 同类的 Once 模式 |
| 4 | `internal/launch/profile.go:92-101`（`profileDatabase`） | **包级可变状态** + mutex + `profileOpened` 旗标，lazy 只试一次 | 全局单例、测试得靠 `resetProfileDatabaseForTest` |
| 5 | `internal/runtime/registry.go:100` | `sync.Once` + 包级可变状态 | 同上 |
| 6 | `internal/instance/service.go:167` | 服务自身 `openLocked()` 里 lazy（`db` 字段，持自身 `mu`） | 每服务一个连接 |
| 7 | `internal/download/store.go:272` | `taskStoreFactory` 包级变量，队列**启动**时新开（`queue.go:274`），重启恢复（`recover_hook.go:51`）也各开一次 | 打开失败静默降级为内存 store，继续运行 |

**一致性问题：**
- 路径解析双轨：`instance/service.go:167` 直接 `database.Open(DataRoot()+"/app.db")`，而其他都经 `settings.ResolveDatabasePath()`（其内部有 `PathKind` 判定）——一旦将来 db 搬家(B3-1)，instance 会被遗漏。
- 迁移的安全网是**全局包级 `database.migrationMu`**（`migrate.go:108`）：7 个池同打同一个库，靠 WAL + busy_timeout + immediate tx 保证正确性；先进入者完成迁移，其余调用在锁内空跑。能用，但正确性依赖一个包级互斥量的隐式约定。
- 各家对"Migrate 失败"的降级处理不一致（settings 返回错误；runtime/launch 报各自错误码但会转译 `DB_MIGRATION_001`；instance 报「实例数据库迁移失败」无稳定码；download 静默降级为内存 store，任务不落盘），体验存在歧义。

### D.2 多个连接池对同一 SQLite 文件的稳固点

- `busy_timeout(5000)` 在 DSN 里，锁冲突前等 5 秒；
- `_txlock=immediate` 让事务即拿写锁，避免升级失败；
- `journal_mode=WAL` 允许读写并发；
- 频率只是"启动时最多 1 次"的 Migrate 与少量读写，没有真实痛点。

这意味着**现状在功能上是正确的，没有 bug**；问题在于形态重复（6 个连接池、2 套路径解析、3 种 lazy 模式）与可维护性。

### D.3 建议（D 组，不涉及引擎替换）

| # | 方案 | 收益 | 影响面 |
|---|---|---|---|
| D-1 | bootstrap 阶段**开一个** `db` 池并完成 `Migrate`（进程级单例，而不是包级可变状态），以依赖注入提供给 6 个服务（每个领域包只接受 `*sql.DB` 参数） | 消除 6 处重复 Open/Migrate；路径解析单轨；懒加载/Once/mutex 全数删除；失败模式统一（启动即失败，或统一"降级"策略） | `settings/account/launch/runtime/instance/download` 6 个包的 Service 构造签名要暴露 db 参数；`internal/artifact` 的启动顺序（DataRoot 探测）需要先于 bootstrap 打开 db；测试基建要跟 |
| D-2 | 统一 `settings.ResolveDatabasePath()` 为**唯一** db 路径来源，instance 改走它（无须搬迁目录即可先做，独立可合并） | 解决 D.1 双轨问题，B3-1 的前手 | instance/service.go 一处，测试 fixture 跟 |
| D-3 | 统一"Migrate 失败"语义：要么全部"硬失败"，要么全部"软降级（停写+UI 提示）"；目前 download 的"变内存库(进程内存活)"是最危险的静默，至少要像 runtime/launch 一样报出 `DB_MIGRATION_001` 码 | 用户体验统一 | download/store.go 的 factory 降级分支 |

> D-2 是独立小步骤，建议最先做；D-3 跟随；D-1 是一次较大的 refactor，要在 CI 里跑 `go test ./internal/...` 全量。

### D.4 分阶段路线图（不动引擎，仅形态治理）

```
阶段 0（立即）                  —— 无风险
  A.5-1 更新 doc/注释消除矛盾表述
  D-2 instance 服走 ResolveDatabasePath（单轨）

阶段 1（连接治理）             —— 一次 PR
  D-3 Migrate 失败语义统一
  D-1 bootstrap 单池开门 + 注入

阶段 2（目录归拢）             —— 一次 PR
  B3-1 app.db → data/db/（含备份搬迁与旧文件检测）

阶段 3（可选，逐项立卡）
  A.5-2 死代码清理
  A.5-3 安装器缓存归位 cache/installers/ 或补 prune
  B3-2 core-index/ 改名
```

---

## 附录 E：SQLite 引擎替换评估

**前提事实：** 7 张业务表 + 1 张迁移表、9 个迁移、约 77 条语句；SQL 只用：单表 CRUD、`UPSERT`(ON CONFLICT)、`ORDER BY`/索引、无 JOIN；驱动 `modernc.org/sqlite`（纯 Go 无 CGO）；数据量小额（配置、任务记录、账号加密串、实例元信息）。

| 引擎 | 可行性 | 主要迁移点 | 判断 |
|---|---|---|---|
| bbolt / embed KV | 可行 | UPSERT/排序语义搬进 Go；迁移框架重写；SQL 检查器超出包边界 | 不推荐：无收益，迁移机制/事务语义重做 |
| JSON 文件 | 仅 `app_settings` 可行；`download_tasks`（状态机/断点）/`instances`（并发安装覆盖）不可 | 每文件/锁 | 部分可行，收益≈0 |
| 仍用 SQLite，但换驱动（如 mattn/go-sqlite3） | 可行却反而要吃 CGO | 连接 DSN、busy_timeout 语法重构 | 反向收益（丢纯 Go 编译目标） |
| 嵌入式 PostgreSQL 等 | 不可行 | 安装包、副本、备份体系全重做 | 否 |

**结论：不换**。最值得做的不是"换引擎"，而是三件治理工作：(a) A.5-1/D.2——收敛路径与描述；(b) D-1——单连接池 + 注入；(c) B3-1——db 目录归拢。这三件落地后，如果未来真要轻轻地推进引擎替换，只动 8 个文件、9 个迁移就够。
