# NineVoxel Launcher 剩余开发项

> 临时任务清单，后续可删除。

## 本次已完成

- [x] 实例配置改为文件权威：每个实例使用 `instances/<id>/instance.json`，SQLite 只保存可重建索引。
- [x] 启动与列表刷新时执行实例对账：可从 `instance.json` 重建数据库、修复旧数据库实例、清理已手动删除目录留下的幽灵记录。
- [x] 启动时若选中实例已不存在，会自动选择最新的剩余实例。
- [x] 删除实例先移入 `instances/.trash/<instanceId>-<timestamp>`，不再丢失存档。
- [x] 完成实例回收站：列表、还原、永久删除单个实例、清空回收站。
- [x] 回收站条目优先读取清单；旧归档缺少清单时尽量从 `.minecraft/versions` 恢复，无法恢复的条目仍可永久删除。
- [x] 还原时若原实例目录已存在，明确拒绝覆盖；清空与永久删除均要求前端二次确认。
- [x] 实例页新增“实例回收站”入口，删除文案改为“移入实例回收站”。
- [x] 新增后端测试覆盖数据库丢失恢复、幽灵记录清理、回收站生命周期、还原冲突、永久删除、清空与旧归档恢复。

## P0 - 立即处理

- [x] 修复下载队列重试回归：生产队列显式设置默认 `MaxRetries=3`，保留 `0` 禁用重试的语义，并修正依赖省略值的测试。
- [x] 接通真实游戏启动：主界面调用 `startLaunchProcess`，组合实例目录、版本、Java、账号、内存和 JVM 参数。
- [x] 接通启动状态与停止流程：订阅 `launch.state.changed.v1`，用 `GetLaunchStatus` 恢复状态，并接入停止/强制结束确认。
- [x] 接通游戏控制台：改用 `fetchGameLog` 与 `subscribeLaunchLogAppended`，替换当前仅展示启动器日志的实现。
- [x] 明确并接入实例级启动配置：实例专属 Java、内存上限、自定义 JVM 参数。
- [x] 游戏日志行增加通道内单调序号，快照与增量事件按序号去重，避免同一毫秒多行或刷新时重复展示。
- [x] 退出/崩溃提示只针对本次界面实际发起的启动，启动器重启后不再误报上一次会话的退出结果。
- [x] 保留最近一次游戏日志通道，游戏退出后控制台仍可按 `launchId` 刷新、翻页并查看崩溃前输出。

## P1 - 核心功能

- [x] 实现 Fabric、Forge、NeoForge、Quilt 的安装、更新与卸载，移除 Vanilla 限制。
- [x] 新增官方加载器版本目录与真实下载/安装流程；Forge/NeoForge 执行官方安装器，不再模拟成功。
- [x] 实例详情页启用加载器页签，接入官方版本选择、安装进度、失败重试、切换与卸载。
- [x] 修复 Forge/NeoForge 版本 JSON 无客户端 JAR 时的启动回退：按 `inheritsFrom` 使用原版父版本 JAR。
- [ ] 实现模组、光影和资源包管理，替换实例详情中的模拟数据。
- [ ] 接入 Modrinth / CurseForge：搜索、依赖解析、下载、校验、启用/停用、删除和兼容性提示。
- [ ] 实现账号皮肤/披风上传与切换。
- [ ] 实现 authlib-injector 下载及启动参数注入。
- [ ] 实现 Ninevoxel 站自有账号协议，替换当前占位登录。

## P2 - 产品化与平台

- [ ] 持久化并实际应用剩余设置：最小内存、自动下载 Java、实例目录、运行时目录、主题色、全屏/分辨率、JVM 预设、Discord RPC、启动后关闭启动器。
- [ ] 实现窗口尺寸/最大化恢复、界面语言切换、更新检查与下载、系统托盘。
- [ ] 增加启动配置列表、恢复与删除界面（后端接口已完成）。
- [ ] 实现下载隔离文件清理流程，避免 `.trash` / quarantine 文件长期堆积。
- [x] 补齐 macOS/Linux 物理内存探测与 macOS 深色模式探测。
- [x] Windows/macOS CI 增加全量 Go 测试。
- [x] 平台能力收敛为 `platform.Current` 接口 + 编译期实现（目录、内存、主题、参数文件、子进程、磁盘空间）。
- [ ] 补齐前端自动化测试和过期文档/注释。
- [ ] 评估前端分包，消除当前生产包超过 500 KB 的构建提示。

## 待确认 - 实例移除策略

- [x] 结论：保留“先移入回收站”的安全策略，不直接永久删除；用户可在回收站中主动永久删除或清空。
- [x] 删除流程：取消进行中的安装 -> 清单标记删除时间 -> 原子移动整个实例目录到 `.trash` -> 删除索引并补选实例。
- [x] 数据库失败时回滚目录移动；永久删除遇到 Windows 文件占用会返回可理解的错误。
- [x] 存储架构结论：本地 `instance.json` 为主，SQLite 为辅；用户直接删除实例文件夹不会再留下幽灵记录，`app.db` 丢失后也能从实例目录恢复。

## 后续可选增强

- [ ] 为损坏的 `instance.json` 提供图形化修复/导出入口；当前对账不会自动覆盖损坏清单。
- [ ] 为回收站增加搜索、按删除时间清理和容量统计。
- [ ] 将旧版已归档但缺少清单且无版本目录的实例，提供手动填写名称/版本的恢复向导。
- [ ] 若要求 `app.db` 可完全丢弃，还需把空分组和当前选中实例持久化到本地设置文件；目前实例本身可恢复，空分组与选中项属于数据库辅助状态。

## 最近验证

- [x] `go vet ./...` 通过。
- [x] `go test ./internal/instance -count=1` 通过。
- [x] 重新生成 Wails 绑定后，`pnpm --dir frontend run lint` 通过。
- [x] `go test ./internal/launch -count=1` 通过（含退出后日志回读）。
- [x] `pnpm --dir frontend run build` 通过（仍有 Vite `__dirname` 与包体超过 500 KB 提示）。
- [x] `go test ./... -count=1` 通过。
- [x] `go test ./internal/launch ./internal/events ./internal/instance ./internal/runtime ./internal/settings -count=1` 通过。
- [x] 重新生成 Wails 绑定后，`pnpm --dir frontend run lint` 通过。
- [x] 新增 `internal/loader` 测试，覆盖版本 ID/非法输入、Fabric/Quilt 嵌套目录、Forge/NeoForge 坐标、SHA-1 解析和安装器版本目录识别。
- [x] 新增实例加载器生命周期测试，覆盖更新、安装状态持久化、卸载回退原版、移除加载器目录和停用 `.jar` 模组。
- [x] 新增 Forge 风格继承版本测试，验证启动 classpath 可回退到父版本客户端 JAR。
- [x] 核对 Fabric、Quilt 官方目录与 profile 响应，确认嵌套 `loader.version` 解析和版本目录命名一致。
- [x] 实测 Forge 官方安装器要求 `launcher_profiles.json`；已补最小配置文件创建逻辑，临时目录实装成功并生成 `1.20.1-forge-47.3.12`。
- [x] 核对 NeoForge `neoforge` / 1.20.1 `forge` 官方安装器与 `.sha1` 地址均返回 200。
- [x] `pnpm --dir frontend run lint`、`pnpm --dir frontend run build`、`go test ./... -count=1`、`go vet ./...` 全部通过。

## 仍待手工验收

- [ ] 在真实桌面环境分别创建 Fabric、Quilt、Forge、NeoForge 实例，完成整包下载、启动、更新和卸载。
- [ ] 确认 Forge/NeoForge 官方安装器在当前 Java、代理与镜像设置下的实际执行结果。
