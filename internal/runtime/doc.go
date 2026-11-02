// Package runtime 探测本机已有的 Java 运行时，并按版本元数据解析 Java 需求。
//
// 本包是 P3-08 的实现，职责严格限定为**只读**：
//
//   - 探测：枚举候选路径、执行一次 `java -version`、解析版本/厂商/架构/位数；
//   - 需求映射：从版本 JSON 的 `javaVersion` 段读出主版本与组件名；
//   - 缓存：探测结果在内存里保留 5 分钟，不落库、不写盘、不创建目录。
//
// 本包不下载运行时会、不安装、不修改 `PATH` / `JAVA_HOME` / 注册表 / profile。
// Runtime 的下载与安装属于 P3-09（`java_runtimes` 表与 `InstallRuntime`）。
//
// # 为什么不做「Minecraft 版本 → Java 版本」硬编码表
//
// P2 的启动校验（`internal/launch/java.go`）已经确立了 D-1 的边界：
// 启动器只依据版本元数据判断 Java 需求，绝不按 Minecraft 版本号猜 Java 版本。
// 硬编码表（例如「1.20.5 需要 Java 21」）有三个不可接受的后果：
//
//  1. 版本范围会持续变化：快照版、第三方整合包、加载器补丁都可能改变需求，
//     硬编码表无法在离线状态下正确演进，最终会把「不认识的版本」判成错误结论；
//  2. 它会与官方元数据冲突：元数据缺失时应当明示「未知」，而不是用猜测替代；
//  3. 它无法表达组件名（`java-runtime-gamma` 等），而 Runtime 下载需要按组件名寻址。
//
// 因此本包的 `RuntimeRequirement.source` 只有两个取值：
// `metadata`（元数据明确声明）与 `unknown`（元数据未声明，界面必须提示用户手动选择）。
//
// # 与 P2 `ValidateJavaPath` 的关系
//
// P2 的校验器（`internal/launch/java.go`）与本包解析同一份 `java -version` 输出，
// 解析规则（正则、旧式 `1.8` 语义、超时判定）保持一致，但两者**不共享代码**：
// `internal/launch` 已验收且禁止修改，其解析函数为包私有，本包也不 import 它。
// 两者分工：P2 回答「用户手动选的这个 java 能不能用」，本包回答
// 「本机有哪些 java、各自什么版本/架构、能不能满足某个版本」。
//
// # 安全边界
//
//   - 全包只有一处创建子进程（`runJavaVersion`），参数用数组、不经过 shell；
//   - 不发起网络请求、不写文件、不改环境变量；
//   - 面向前端的 DTO 不包含绝对路径与用户名：只返回 `displayName`
//     与由「路径 + 版本 + 架构」派生的稳定 `runtimeId`。
package runtime
