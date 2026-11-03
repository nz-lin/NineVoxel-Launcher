// Package launch 提供 Minecraft 本地启动闭环的后端能力。
//
// 职责边界（本包在 P2 阶段逐步扩展，后续任务卡**只追加**、不重排既有结构）：
//   - P2-01（本卡）：Java 可执行文件路径的三层校验与 `java -version` 版本解析；
//   - P2-02 起：游戏目录校验、版本 JSON 解析、启动参数构建、进程启动与停止、
//     游戏输出独立通道（D-2）、启动状态机（D-6）。
//
// 明确的边界（本包不做什么）：
//   - **不做**「Minecraft 版本 → Java 大版本」映射表（决策记录 D-1）：映射会随版本
//     与启动器策略变化，属 P3 元数据能力，内置一张会过期的表比不内置更糟。
//   - **不做** Java 探测矩阵 / Runtime 下载与管理（属 P3 的 RuntimeService）。
//   - **不写**用户本地 Java 安装目录与游戏目录（P2 全阶段只读）。
//   - **不改** PATH / JAVA_HOME / 注册表 / 用户环境变量，不创建临时目录、
//     不复制文件、不创建包装脚本。
//
// 路径处理约定（决策记录 D-8 / 第六节）：
//   - 用户选中的**完整路径**允许在 `Pick*` 方法返给前端、再回传给校验方法之间经过一次；
//   - 但任何面向用户的展示一律使用 `javaDisplay` 这类**脱敏形态**
//     （「目录类别 + …\ + 末两级」），不得渲染完整路径；
//   - 完整路径禁止写入日志、禁止进 `AppError.details`、禁止写 localStorage。
//
// 日志约定：成功与失败各记一条 `component=launch` 的 info/warn，
// `attributes` 只放 `javaExecutableName`、`javaMajorVersion` 等非敏感键，
// **不放完整路径、不放版本输出全文**。
//
// # 启动参数如何交给 JVM
//
// 命令行只放「JVM 启动前就必须知道」的内容：JVM 参数、classpath、引导器入口类。
// **游戏主类与全部游戏参数经 stdin 传给引导器**（launcher part，见 launcherpart.go），
// 由引导器在同一个 JVM 内反射调用游戏主类。这样：
//   - 不受 Windows 32767 字符命令行上限约束（装加载器的实例依赖库极多）；
//   - `--accessToken` 等凭据不出现在进程命令行里，任务管理器与 `ps` 都看不到。
//
// classpath 与 JVM 参数本身仍可能超长（例如 NeoForge 的 `-p` 模块路径），
// 此时改用 `@argfile`（Java 9+，见 argfile.go）；Java 8 不支持该语法，
// 只能返回 `LAUNCH_ARGS_002` 并提示缩短游戏目录路径。
package launch
