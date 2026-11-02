// Package account 提供 NineVoxel Launcher 的账号子系统与账号来源 SPI。
//
// 设计目标：把「账号来源」与「账号生命周期/持久化/事件」彻底解耦，新增一种
// 账号类型只需要实现 Provider 接口并注册，不需要改动服务层与前端契约。
//
// # 账号来源（Provider）
//
// 首期支持四类来源，全部实现同一套 SPI：
//
//   - offline（离线账号）：用户名派生 UUID，不联网、不保存任何凭证；
//   - microsoft（微软账号）：默认 OAuth 2.0 授权码 + PKCE（系统浏览器 + 本地回环，
//     登录结束自动返回），浏览器无法回跳时可显式回退到 Device Code Flow；
//     两条路径最终都经 Xbox Live → XSTS → Minecraft Services 拿到令牌与 Profile；
//   - yggdrasil（自定 Yggdrasil API）：authlib-injector 兼容服务，
//     支持 API 根发现（ALI）、元数据、档案选择、validate/refresh/invalidate；
//   - ninevoxel（Ninevoxel 站）：**占位实现**，返回稳定的「尚未上线」错误，
//     保证界面可以先行展示入口而不会伪造登录成功。
//
// # 安全边界（硬性约束）
//
//  1. Access Token / Refresh Token / Client Token 是凭证，**永远不返回前端**：
//     前端只看到账号摘要（AccountView），凭证只在 Go 进程内使用。
//  2. 凭证落库前必须加密（见 cipher.go），数据库里不出现明文 token；
//     日志与错误文案不得包含 token、密码、设备码。
//  3. 密码只在登录调用栈内存在，不落盘、不缓存；登录成功后立即丢弃。
//  4. 事件载荷只带账号 id/动作/数量，不带任何凭证与绝对路径。
//
// # 与既有模块的关系
//
//   - 存储复用 internal/database 的 app.db 与迁移机制（新增 accounts 表）；
//   - 事件复用 internal/events 的 envelope 与 emitter；
//   - 启动（internal/launch）后续通过 Service 取「当前账号的 Profile + 凭证」，
//     本包不直接 import launch，避免循环依赖。
//
// # 明确不做的事（首期）
//
//   - 不做皮肤上传（Capabilities.SkinUpload 恒 false，后续单独任务）；
//   - 不做正版所有权/Entitlement 校验（保留端点与错误码，不阻塞登录）；
//   - 不做 authlib-injector 的下载与启动参数注入（属启动链路，另一个任务）；
//   - 不做 Ninevoxel 站的真实协议（占位）。
package account
