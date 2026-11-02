// Package download 实现 P3 的下载队列与调度骨架。
//
// 本包只负责「任务」层面的确定性行为：状态机、优先级队列、并发闸门、
// 暂停/恢复/取消、重试退避、状态事件推送与任务表持久化。
// 它**不传输字节**，也**不提交文件**——这两件事分别由后续卡接管：
//
//   - P3-05 提供 TransferFunc 的真实实现（HTTP、Range 续传、镜像回退）；
//   - P3-06 提供 CommitFunc 的真实实现（哈希校验、staging 原子提交、
//     恢复矩阵、verified_at_ms 落库）。
//
// 因此本包的传输边界被抽象成两个可注入的函数类型（见 queue.go）：
//
//	type TransferFunc func(ctx context.Context, task Task) TransferResult
//	type CommitFunc func(ctx context.Context, task Task) *Error
//
// 未注入时，前者返回 DOWNLOAD_NETWORK_001（「传输实现尚未接入」），
// 后者让任务停在 verifying（「校验与提交实现尚未接入」）。
// 这样既不会伪造「下载完成」，也让 P3-05/P3-06 只需注入实现、不改调度代码。
//
// # 状态机（与《接口冻结清单》§7 逐条一致）
//
//	queued     → resolving / downloading / paused / cancelled
//	resolving  → downloading / failed / cancelled
//	downloading→ paused / verifying / failed / cancelled
//	paused     → queued / downloading / cancelled
//	verifying  → committing / failed / cancelled
//	committing → completed / failed
//	completed / failed / cancelled → （终态，无出口）
//
// 非法转移一律返回 DOWNLOAD_STATE_001，绝不静默忽略：静默忽略会让界面与
// 磁盘状态悄悄分叉，而下载任务是「写盘」操作，分叉的代价是文件损坏。
//
// 取消与暂停的语义差异（卡面第 4 条）：
//
//   - 取消是**终态**（cancelled），之后不可恢复，临时文件由 P3-06 清理；
//   - 暂停是**可恢复**（paused），保留已下载字节，恢复时回到 queued 并
//     由 P3-06 重新校验已有分片（本卡只做状态推进）。
//
// # 并发与重试（D-31 已冻结）
//
// 全局并发 4、同一主机并发 2；失败最多重试 3 次，指数退避 1s → 2s → 4s，
// 上限 30s。这些是包内默认常量，测试通过 QueueOptions 注入更小的退避基准，
// 避免单元测试真的睡眠数秒。重试期间任务**保持 downloading 状态**，
// 只增加 retry_count——状态机里没有「retrying」态，硬造一个会与冻结清单冲突。
//
// 只有「网络错误 / 超时 / HTTP 5xx / 429」可重试（TransferResult.Retryable），
// 4xx（除 429）、哈希不匹配、路径非法一律不重试：重试一个确定性失败只是浪费带宽。
//
// # 事件
//
// 状态变化发 `download.task.changed.v1`，载荷含 taskId / state / progress /
// errorCode（见 events 包的 DownloadTaskChangedPayload）。发射顺序固定为
// **先落内存状态与任务表，再发事件**：P2-06 已验证，先发事件会让前端在
// 收到通知后立刻查询快照，却读到旧值。
//
// 进度事件做节流（≥250ms 或进度增量 ≥1%，先到者生效），避免每个 TCP 分片
// 都触发一次跨端事件。
//
// # 与 P3-05 / P3-06 的边界（重要）
//
// 本卡明确**不**实现：HTTP 请求、断点续传、mirror 重写、staging 目录写入、
// 哈希校验、原子提交、重启恢复矩阵、实例目录操作。任务表里 etag /
// last_modified / range_start / verified_at_ms 四列本卡只建表不写值。
//
// 计划来源同样是接缝：本卡的 StartDownloadPlan 通过 PlanSource 取得制品清单，
// 默认未注入时返回 ARTIFACT_PLAN_001。P3-02 的 InstallPlan 导出接口就绪后，
// 由装配层注入，调度代码不需要改动。
//
// # 资源（Assets）管理（P3-07 追加）
//
// assets.go 只做三件事：解析资产索引、把索引展开成对象下载计划项、按虚拟布局
// 把共享缓存里的对象组织成逻辑名目录树。它**不发起网络请求**，也不自建调度器：
// 索引与对象都作为普通 `PlanItem` 交给同一个队列，从而复用 P3-05 的传输、
// P3-06 的校验与提交，以及 D-31 的并发与退避策略。
//
// # 三种资产布局
//
//   - 现代共享（默认）：对象写 `artifacts/objects/<hash[0:2]>/<hash>`，
//     去重键是 SHA-1（D-27）。所有版本共用同一份内容。
//   - virtual：在 `versions/<versionId>/virtual/<indexId>/<逻辑名>` 下按索引
//     的逻辑名建目录树，优先硬链接、其次软链接、最后复制（卡面第 5 条）。
//     C-15/D-29 已冻结：必须写版本私有目录——逻辑名在不同版本间内容不同，
//     写进共享缓存会互相覆盖。组装报告里的 `Mode` 字段记录实际采用的方式。
//   - map_to_resources：D-29 已冻结 P3 **不启用**，本包只解析并告警，
//     不创建 `versions/<versionId>/resources/`，该能力留给 P3-10。
//
// # 事件风暴控制（D-45）
//
// 资产索引可能包含数千个对象。父任务（索引）完成后再展开对象子任务，
// 子任务**完全不发事件**；父任务进度只用 `download.task.changed.v1`
// 一个事件，载荷里的 progress 由 AssetProgressTracker 计算，
// 只在「首条 / 距上次 ≥500ms / 终态」时发送。失败率超过 5% 时
// 父任务标记为 failed，并携带失败数量与最多 5 个哈希样例。
//
// # 与 D-47 的关系
//
// `artifacts` 表暂未建立，因此「对象是否已验证」只能依赖磁盘实际文件：
// 先用 os.Stat 比对大小，再用 VerifyFile 流式计算 SHA-1。这会让每次安装
// 都重复校验已有对象（数 GB 资产上是分钟级开销），换来的是不依赖任何
// 数据库状态的简单性与可恢复性；待 artifacts 表落地后可在此处加缓存。
package download
