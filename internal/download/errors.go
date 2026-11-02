// 本文件定义下载模块的稳定错误码与错误类型。
//
// 错误码来源是《接口冻结清单》§4.2（P3 新增）与 §4.1（可复用码）：
// 本包**不得**自造冻结清单之外的码，否则前端按码分支的处理逻辑会漏判。
// 每个码在 errors.go 里只出现一次，避免同一个码在不同调用点被赋予不同含义。
package download

import (
	"fmt"
	"strings"
)

// 本包使用的稳定错误码。
const (
	// ErrorCodeNetwork 表示传输网络错误（连接失败、超时、5xx、429）。
	//
	// 可重试：调度器按 D-31 的退避策略重试，最多 3 次。
	// 本卡的默认 TransferFunc（未接入实现）也返回该码，语义是「传输层不可用」。
	ErrorCodeNetwork = "DOWNLOAD_NETWORK_001"

	// ErrorCodeResume 表示续传信息不一致（ETag / Last-Modified 变化、Range 不支持）。
	//
	// 该码不直接展示给用户：P3-05 收到后应改为全量重启，因此对用户不可见。
	ErrorCodeResume = "DOWNLOAD_RESUME_001"

	// ErrorCodeSize 表示实际字节与期望大小不符或超出上限。
	//
	// 不可重试：大小不符说明源端数据与元数据不一致，重试只会重复失败。
	ErrorCodeSize = "DOWNLOAD_SIZE_001"

	// ErrorCodeState 表示非法状态转移（例如对已完成任务再次取消）。
	//
	// 不可重试：这是调用方的时序错误，只能由界面纠正操作顺序。
	ErrorCodeState = "DOWNLOAD_STATE_001"

	// ErrorCodeCancel 表示取消失败（任务已进入提交阶段，无法回滚）。
	//
	// 不可重试。与 ErrorCodeState 的区别：本码专指「状态合法但语义上不允许
	// 中断」的提交阶段，界面文案更明确（「任务已进入提交，无法取消」）。
	ErrorCodeCancel = "DOWNLOAD_CANCEL_001"

	// ErrorCodePlan 表示安装计划不可用（解析器未接入、版本元数据缺失或计划漂移）。
	//
	// 复用冻结清单 §4.2 中 ARTIFACT_PLAN_001 的语义（计划构建失败）。
	ErrorCodePlan = "ARTIFACT_PLAN_001"

	// ErrorCodeDatabaseOpen 复用 P1 的数据库打开失败码。
	//
	// 任务表所在连接打不开时使用，不与 DB_MIGRATION_001 混用：
	// 前者是「连不上」，后者是「表结构升级失败」，排查手段不同。
	ErrorCodeDatabaseOpen = "DB_OPEN_001"

	// ErrorCodeDatabaseMigration 复用 P1 的数据库迁移失败码。
	//
	// 000003_download_tasks.sql 应用失败时使用：此时任务表不可用，
	// 调度器降级为「仅内存队列」，界面据此提示「本次运行无法保存下载任务」。
	ErrorCodeDatabaseMigration = "DB_MIGRATION_001"
)

// 错误分类，用于区分「用户输入问题 / 系统问题 / 外部依赖问题」。
type ErrorCategory string

const (
	// CategoryInput 表示调用方传入的参数或时序不合法。
	CategoryInput ErrorCategory = "input"

	// CategorySystem 表示本机系统层面失败（磁盘、权限、数据库）。
	CategorySystem ErrorCategory = "system"

	// CategoryExternal 表示外部依赖失败（网络、服务端）。
	CategoryExternal ErrorCategory = "external"
)

// Error 是下载模块的稳定错误类型。
//
// 字段与前端 `DownloadTaskError` 对齐：code / message / retryable。
// 前端**不得**自行推断 retryable（冻结清单 §4.2 约定），必须由后端给出。
type Error struct {
	// Code 为稳定错误码，取值见上面的 ErrorCode* 常量。
	Code string

	// Message 为面向用户的中文文案，不得包含路径、URL 查询串或凭证。
	Message string

	// Retryable 表示调度器是否应该自动重试该错误。
	Retryable bool

	// Category 为错误分类。
	Category ErrorCategory
}

// Error 实现 error 接口，输出形如 `DOWNLOAD_STATE_001: 当前状态不支持该操作`。
//
// 之所以把错误码拼进字符串：日志里只出现文案时无法按码检索，
// 而下载失败排障几乎都要按错误码统计。
func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Is 支持 errors.Is 按错误码匹配。
//
// 只比较 Code：同一个码在不同调用点可能带不同文案，
// 但语义相同，按码匹配才符合「稳定错误码」的契约。
func (e *Error) Is(target error) bool {
	other, ok := target.(*Error)
	if !ok || other == nil || e == nil {
		return false
	}
	return e.Code == other.Code
}

// newInputError 构造「调用方问题」类错误（不可重试）。
func newInputError(code string, message string) *Error {
	return &Error{Code: code, Message: message, Retryable: false, Category: CategoryInput}
}

// newSystemError 构造「系统问题」类错误；retryable 表示重试是否可能有帮助。
func newSystemError(code string, message string, retryable bool) *Error {
	return &Error{Code: code, Message: message, Retryable: retryable, Category: CategorySystem}
}

// newExternalError 构造「外部依赖问题」类错误；retryable 表示重试是否可能有帮助。
func newExternalError(code string, message string, retryable bool) *Error {
	return &Error{Code: code, Message: message, Retryable: retryable, Category: CategoryExternal}
}

// 面向用户的中文文案常量。
//
// 全部集中在此处，便于统一审查「是否泄漏路径 / 凭证 / 完整 URL」：
// 这些字符串会被写进数据库列 error_message_safe 并直接显示在界面上。
const (
	// messageStateNotAllowed 用于非法状态转移。
	messageStateNotAllowed = "当前状态不支持该操作。"

	// messageCancelDuringCommit 用于提交阶段的取消失败。
	messageCancelDuringCommit = "任务已进入提交，无法取消。"

	// messageTaskNotFound 用于查询不存在的任务。
	messageTaskNotFound = "下载任务不存在或已被清理。"

	// messageTaskIDEmpty 用于传入空任务 ID。
	messageTaskIDEmpty = "下载任务 ID 不能为空。"

	// messageTransferNotImplemented 用于尚未注入传输实现时的明确拒绝。
	//
	// 措辞刻意点明「未接入」而不是「下载失败」：本卡（P3-04）只有队列与状态机，
	// 真实传输属 P3-05，把未知实现说成网络失败会误导排查方向。
	messageTransferNotImplemented = "下载传输尚未接入，本次只完成了队列调度。"

	// messagePlanSourceMissing 用于计划来源未注入。
	messagePlanSourceMissing = "安装计划解析尚未接入，无法创建下载任务。"

	// messagePlanHashMismatch 用于计划漂移（前端传入的 planHash 与后端重新解析的不一致）。
	messagePlanHashMismatch = "安装计划已变化，请刷新后重试。"

	// messagePlanEmpty 用于计划里没有任何可下载制品。
	messagePlanEmpty = "该版本没有需要下载的文件。"

	// messageVersionIDEmpty 用于传入空版本 ID。
	messageVersionIDEmpty = "版本 ID 不能为空。"

	// messageDatabaseUnavailable 用于任务表不可用时的降级提示。
	messageDatabaseUnavailable = "下载任务存储不可用，本次运行无法保存任务记录。"

	// messageStorageFailed 用于单次写入失败（写入失败不中断调度）。
	messageStorageFailed = "下载任务记录写入失败，本次仅保存在内存中。"

	// messageTaskHostInvalid 用于无法从源地址解析出主机名。
	messageTaskHostInvalid = "下载地址无法解析出主机名，已拒绝入队。"

	// messageQueueClosed 用于队列已停止后仍尝试入队的场景。
	messageQueueClosed = "下载队列已停止，无法接受新任务。"

	// messageStateUnknown 用于未知状态值。
	messageStateUnknown = "未知的下载状态：%s。"
)

// isTerminalState 的判定在 task.go，此处只提供错误构造的辅助函数。
//
// 这样拆分是为了让 errors.go 完全不依赖状态机：
// 错误码是跨端契约，状态机是内部逻辑，两者的变更频率不同。

// describeState 把状态值包装成用户可读的文案片段。
//
// 只在错误信息里使用，避免把英文状态值直接丢给用户。
func describeState(state State) string {
	return strings.TrimSpace(string(state))
}

// ===== P3-05 HTTP 传输（本卡追加，不修改上方 P3-04 内容）=====

// ErrorCodeSource 表示下载源地址不可用或不被信任。
//
// 触发条件（全部不发起连接或立即失败、**不可重试**）：
//   - 地址为空 / 无法解析；
//   - scheme 不是 https；
//   - host 既不在官方白名单、也不是已启用的镜像；
//   - 重定向超出 3 跳或重定向目标不满足上述约束；
//   - 源端返回 403 / 404 / 410 等不可重试的客户端错误。
//
// 与 DOWNLOAD_NETWORK_001 的区别：本码是「这个地址本身有问题」，
// 重试只会重复失败；网络码是「这次连接失败」，值得退避后重试。
//
// 注意：该码是 P3-05 按卡面「追加本卡新增错误码常量」授权新增的，
// 《接口冻结清单》§4.2 尚未收录，需由 Leader 决定是否同步到冻结清单
// 并补前端分支；在此之前前端按未知码回退到 message 展示。
const ErrorCodeSource = "DOWNLOAD_SOURCE_001"

// P3-05 追加的面向用户文案常量。
//
// 与上方常量一样集中管理：这些字符串会写进 error_message_safe 并直接展示，
// 因此**不得**包含完整 URL、路径、查询串或凭证。
const (
	// messageSourceEmpty 用于空下载地址。
	messageSourceEmpty = "下载地址为空。"

	// messageSourceInvalid 用于无法解析的下载地址。
	messageSourceInvalid = "下载地址无法解析。"

	// messageSourceNotHTTPS 用于非 https 的下载地址。
	messageSourceNotHTTPS = "只允许 https 下载地址。"

	// messageSourceHostNotAllowed 用于白名单外的下载主机。
	messageSourceHostNotAllowed = "下载地址不是受信任的官方主机或已启用的镜像。"

	// messageSourceRejected 用于源端拒绝访问或文件不存在。
	messageSourceRejected = "下载源拒绝访问或该文件不存在。"

	// messageNetworkInterrupted 用于可重试的网络中断。
	messageNetworkInterrupted = "下载中断，可重试。"

	// messageSizeExceeded 用于写入字节超出预期大小。
	messageSizeExceeded = "实际下载字节数超出预期大小，已中止。"

	// messageExpectedSizeInvalid 用于缺少预期大小的请求。
	messageExpectedSizeInvalid = "缺少预期的文件大小，无法安全下载。"

	// messageStagingNotConfigured 用于 staging 写入接缝未注入。
	messageStagingNotConfigured = "下载内容暂存尚未接入，无法保存文件。"

	// messageRequestBuildFailed 用于构造 HTTP 请求失败。
	messageRequestBuildFailed = "无法构造下载请求。"

	// messageCancelled 用于调用方主动取消。
	messageCancelled = "下载已取消。"
)

// ===== P3-06 暂存、校验、提交、隔离与恢复（本卡追加，不修改上方内容）=====

// ErrorCodeVerify 表示下载内容校验不通过或无法校验。
//
// 触发条件（**不可重试**：同样的大小与哈希只会得到同样的结论）：
//   - 暂存文件读取失败（权限、被占用、磁盘错误）；
//   - 实测大小与预期大小不一致；
//   - 预期提供 SHA-1 / SHA-256 时实测哈希不一致；
//   - 目标位置已有文件、但该文件本身校验不通过（拒绝覆盖）。
//
// 该码是 P3-06 按卡面「追加本卡新增错误码」授权新增的，
// 《接口冻结清单》§4.2 尚未收录，需由 Leader 决定是否同步并补前端分支。
const ErrorCodeVerify = "DOWNLOAD_VERIFY_001"

// ErrorCodeCommit 表示提交阶段的文件系统操作失败。
//
// 触发条件（**不可重试**：重试会在同一块磁盘上重复失败）：
//   - 数据根未配置或不可用；
//   - 暂存文件在提交时已不存在；
//   - fsync、rename 或跨卷复制失败（含空间耗尽、权限不足、目标被占用）。
const ErrorCodeCommit = "DOWNLOAD_COMMIT_001"

// ErrorCodeSpace 表示目标卷剩余空间不足以安全完成本次下载。
//
// 触发条件（**不可重试**：先释放空间才可能成功）：
//   - 可用空间小于 预期大小 × 1.1（预留 10% 余量）；
//   - 无法读取目标卷的可用空间（探测失败）。
//
// 该码由 Leader 明确指定，用于卡面要求「空间不足必须拒绝创建任务」。
const ErrorCodeSpace = "DOWNLOAD_SPACE_001"

// P3-06 追加的面向用户文案常量。
//
// 同样集中管理：这些字符串会写进 error_message_safe 并直接展示，
// 因此**不得**包含完整路径、文件名或哈希原文。
const (
	// messageVerifyFileUnreadable 用于暂存或目标文件无法读取。
	messageVerifyFileUnreadable = "无法读取待校验的文件。"

	// messageVerifySizeUnknown 用于调用方未提供预期大小。
	messageVerifySizeUnknown = "缺少预期的文件大小，无法校验。"

	// messageVerifySizeMismatch 用于实测大小与预期不一致。
	messageVerifySizeMismatch = "文件大小与预期不一致，已放弃本次提交。"

	// messageVerifyHashMismatch 用于实测哈希与预期不一致。
	messageVerifyHashMismatch = "文件哈希与预期不一致，已放弃本次提交。"

	// messageTargetPathInvalid 用于目标相对路径非法或越界。
	messageTargetPathInvalid = "下载目标路径非法，已拒绝提交。"

	// messageStagingPathInvalid 用于暂存相对路径非法。
	messageStagingPathInvalid = "暂存路径非法，已拒绝提交。"

	// messageStagingMissing 用于提交时暂存文件已不存在。
	messageStagingMissing = "暂存文件已不存在，无法提交。"

	// messageCommitFailed 用于 fsync、rename 或跨卷复制失败。
	messageCommitFailed = "提交下载文件失败，请检查磁盘空间与权限。"

	// messageCommitTargetOccupied 用于目标已存在但校验不通过。
	messageCommitTargetOccupied = "目标位置已有内容不同的文件，已拒绝覆盖。"

	// messageQuarantineFailed 用于隔离文件或写入原因失败。
	messageQuarantineFailed = "隔离损坏文件失败，已保留在原处。"

	// messageDiskSpaceInsufficient 用于目标卷空间不足。
	messageDiskSpaceInsufficient = "磁盘剩余空间不足，已拒绝创建下载任务。"

	// messageDiskSpaceProbeFailed 用于无法读取目标卷可用空间。
	messageDiskSpaceProbeFailed = "无法读取磁盘剩余空间，已拒绝创建下载任务。"

	// messageRecoveryStoreMissing 用于恢复时缺少任务存储。
	messageRecoveryStoreMissing = "下载任务存储不可用，无法恢复上次的下载进度。"

	// messageRecoveryLoadFailed 用于恢复时读取任务列表失败。
	messageRecoveryLoadFailed = "读取下载任务列表失败，无法恢复上次的下载进度。"

	// messageRecoverySaveFailed 用于恢复时写回任务状态失败。
	messageRecoverySaveFailed = "写回下载任务状态失败，无法完成本次恢复。"
)

// P3-07（Assets 管理）追加的错误码。
//
// 说明：`ASSET_INDEX_001` 已在《接口冻结清单》§4.2 列出；
// `ASSET_LAYOUT_001` 是本卡为「虚拟布局组装失败」新增的码，
// §4.2 尚未收录，需由 Leader 决定是否同步（同 P3-06 的 DOWNLOAD_VERIFY_001）。

// ErrorCodeAssetIndex 表示资产索引不可用。
//
// 触发条件（**不可重试**：同样的输入只会得到同样的结论）：
//   - 索引文件不存在、无法读取，或超过 64 MiB 上限；
//   - 内容不是合法 JSON，或 `objects` 为空；
//   - 条目数超过 20 万、逻辑名越界、hash 不是 40 位小写十六进制、
//     size <= 0 或超过 512 MiB。
const ErrorCodeAssetIndex = "ASSET_INDEX_001"

// ErrorCodeAssetLayout 表示虚拟布局（virtual）组装失败。
//
// 触发条件（**不可重试**：磁盘或权限问题会在同一路径重复出现）：
//   - 目标目录参数为空或非法；
//   - 创建目录失败、删除旧文件失败、复制对象失败。
//
// 注意：硬链接与软链接失败**不属于**该码——按卡面第 5 条，
// 它们会逐级回退到复制，只有复制也失败才报错。
const ErrorCodeAssetLayout = "ASSET_LAYOUT_001"

// P3-07 追加的面向用户文案常量。
//
// 同样集中管理：这些字符串会进入事件载荷与界面提示，
// 因此不得包含完整路径、文件名以外的敏感信息或哈希原文以外的内容。
const (
	// messageAssetIndexEmpty 用于索引内容为空。
	messageAssetIndexEmpty = "资产索引内容为空，无法使用。"

	// messageAssetIndexTooLarge 用于索引超过大小上限。
	messageAssetIndexTooLarge = "资产索引超过允许的大小上限（64 MiB）。"

	// messageAssetIndexMalformed 用于索引不是合法 JSON。
	messageAssetIndexMalformed = "资产索引不是合法的 JSON 结构。"

	// messageAssetIndexNoObjects 用于索引没有声明任何对象。
	messageAssetIndexNoObjects = "资产索引没有声明任何资源对象。"

	// messageAssetTooManyObjects 用于条目数超过上限。
	messageAssetTooManyObjects = "资产索引声明的对象数量超过上限，已拒绝。"

	// messageAssetLogicalNameEmpty 用于逻辑名为空。
	messageAssetLogicalNameEmpty = "资产索引中存在空的资源逻辑名。"

	// messageAssetLogicalNameInvalid 用于逻辑名越界或非法。
	messageAssetLogicalNameInvalid = "资产索引中的资源逻辑名非法或越界，已拒绝。"

	// messageAssetHashInvalid 用于哈希不是合法 SHA-1。
	messageAssetHashInvalid = "资产索引中的资源哈希不是合法的 SHA-1。"

	// messageAssetObjectSizeInvalid 用于对象大小非法。
	messageAssetObjectSizeInvalid = "资产索引中的资源对象大小非法。"

	// messageAssetObjectTooLarge 用于单个对象超过上限。
	messageAssetObjectTooLarge = "资产索引中单个资源对象超过大小上限，已拒绝。"

	// messageAssetTotalSizeMismatch 用于声明总大小与实际不一致。
	messageAssetTotalSizeMismatch = "索引声明的总大小与实际条目累加值不一致，已忽略该声明。"

	// messageAssetIndexPathInvalid 用于索引标识无法生成路径。
	messageAssetIndexPathInvalid = "资产索引标识非法，无法生成缓存路径。"

	// messageAssetIndexMissing 用于本地没有索引缓存。
	messageAssetIndexMissing = "本地没有该资源索引的缓存文件。"

	// messageAssetIndexUnreadable 用于索引无法读取。
	messageAssetIndexUnreadable = "无法读取资源索引文件。"

	// messageAssetObjectUnreadable 用于共享缓存对象无法读取。
	messageAssetObjectUnreadable = "无法读取共享缓存中的资源对象。"

	// messageAssetLayoutTargetInvalid 用于虚拟布局目标目录非法。
	messageAssetLayoutTargetInvalid = "虚拟资源目录参数非法，已拒绝组装。"

	// messageAssetLayoutWriteFailed 用于写入虚拟资源失败。
	messageAssetLayoutWriteFailed = "写入虚拟资源文件失败，请检查磁盘空间与权限。"

	// messageAssetVirtualSourceMissing 用于虚拟组装时对象缺失。
	messageAssetVirtualSourceMissing = "部分资源对象尚未下载，虚拟目录只组装了已有的对象。"

	// messageAssetMapToResourcesIgnored 用于 map_to_resources 被忽略。
	messageAssetMapToResourcesIgnored = "该索引要求 map_to_resources 布局，当前阶段不启用，已忽略。"
)
