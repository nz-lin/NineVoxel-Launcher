// 本文件定义 version 包的结构化错误与稳定错误码。
//
// 错误码来源：《P3 接口冻结清单》§4 与 Leader 在本卡（P3-01）的批准追加：
//   - VERSION_MANIFEST_001 / VERSION_MANIFEST_002 为冻结清单已列出的 P3 新增码；
//   - VERSION_URL_001 / VERSION_LAYOUT_001 为本卡新增，需回填接口冻结清单 §4.2。
//
// 不得改义复用：VERSION_URL_001 只表达「地址本身不合法」，
// 不得用它表达「远程返回了 404」；后者属于 VERSION_MANIFEST_001。
package version

import "fmt"

// 本包使用的四个稳定错误码。
const (
	// ErrorCodeManifestFetch 表示清单获取失败：网络错误、非 2xx 状态、超时。
	ErrorCodeManifestFetch = "VERSION_MANIFEST_001"

	// ErrorCodeManifestInvalid 表示清单结构非法：缺字段、latest 悬空、
	// 条数或大小超限、条目 URL 非法。
	ErrorCodeManifestInvalid = "VERSION_MANIFEST_002"

	// ErrorCodeURLInvalid 表示清单地址非法：非 https、host 不在白名单、
	// 重定向跳数或目标越界。
	ErrorCodeURLInvalid = "VERSION_URL_001"

	// ErrorCodeDataRootUnavailable 表示数据根不可用：DataRootProvider
	// 未注入或返回错误，导致缓存无法读写。
	ErrorCodeDataRootUnavailable = "VERSION_LAYOUT_001"

	// ErrorCodeVersionJSON 表示版本 JSON 不可用：读取失败、结构非法、
	// 继承链成环或层级超限。该码在接口冻结清单 §4.2 已列出。
	ErrorCodeVersionJSON = "VERSION_JSON_001"

	// ErrorCodeArtifactPlan 表示安装计划无法构建：解析器不支持或关键字段缺失。
	// 该码在接口冻结清单 §4.2 已列出。
	ErrorCodeArtifactPlan = "ARTIFACT_PLAN_001"
)

// ErrorCategory 表示错误的归类，用于区分用户输入问题、系统问题与外部依赖问题。
//
// 分类依据 AGENTS.md 8.4：三者的处置方式不同——用户输入需要提示修正，
// 系统错误需要记录排查，外部依赖错误需要提示重试。
type ErrorCategory int

const (
	// CategoryInput 表示调用方传入的参数或地址不合法。
	CategoryInput ErrorCategory = iota

	// CategorySystem 表示本地环境问题（数据根不可用、缓存写入失败）。
	CategorySystem

	// CategoryExternal 表示外部依赖问题（网络、远端状态码、远端内容）。
	CategoryExternal
)

// String 返回错误分类的中文说明，供日志与排查使用。
func (c ErrorCategory) String() string {
	switch c {
	case CategoryInput:
		return "输入错误"
	case CategorySystem:
		return "系统错误"
	case CategoryExternal:
		return "外部依赖错误"
	default:
		return "未知错误"
	}
}

// AppError 是 version 包的结构化错误。
//
// 字段与前端 `toAppError` 的解析口径一致：
//   - Code 为稳定错误码，前端按它分支；
//   - Message 面向用户，已脱敏，不含完整 URL、绝对路径、响应体；
//   - Retryable 由后端给出，前端不得自行推断；
//   - Category 只进日志，不进前端。
//
// 实现 error 接口，`Error()` 输出 `<CODE>: <message>`，
// 与 P1/P2 的既有约定一致，前端可按前缀提取错误码。
type AppError struct {
	// Code 为稳定错误码。
	Code string

	// Message 为面向用户的中文说明（不得包含路径、URL 查询串或响应体）。
	Message string

	// Retryable 表示同一操作重试是否可能成功。
	Retryable bool

	// Category 为错误归类。
	Category ErrorCategory
}

// Error 实现 error 接口，输出 `CODE: message` 形式。
func (e *AppError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// IsRetryable 报告该错误是否可重试；nil 安全。
func (e *AppError) IsRetryable() bool {
	return e != nil && e.Retryable
}

// 面向用户的中文错误文案。
//
// 集中定义以避免同一语义在不同分支写出不同措辞；
// 这些文本都不含路径、URL、响应体或堆栈。
const (
	// messageManifestNetworkFailed 表示网络层失败（连接、超时、DNS）。
	messageManifestNetworkFailed = "版本清单获取失败，请检查网络连接后重试。"

	// messageManifestStatusFailed 表示远端返回了非 2xx 状态。
	messageManifestStatusFailed = "版本清单服务暂时不可用，请稍后重试。"

	// messageManifestTooLarge 表示响应体或单条版本超出大小上限。
	messageManifestTooLarge = "版本清单内容超出允许的大小，已被拒绝。"

	// messageManifestMalformed 表示 JSON 结构或字段不合法。
	messageManifestMalformed = "版本清单内容不合法，已拒绝使用。"

	// messageManifestLatestDangling 表示 latest 指向的版本不在 versions 中。
	messageManifestLatestDangling = "版本清单的 latest 指向不存在的版本，已拒绝使用。"

	// messageManifestTooManyVersions 表示版本条数超出上限。
	messageManifestTooManyVersions = "版本清单条目数量超出上限，已拒绝使用。"

	// messageManifestEntryInvalid 表示单条版本条目缺字段或取值非法。
	// 参数为条目的 id（已做长度截断），便于排查但不泄露路径。
	messageManifestEntryInvalid = "版本清单条目「%s」不合法，已拒绝使用。"

	// messageURLNotHTTPS 表示地址不是 https。
	messageURLNotHTTPS = "版本清单地址必须使用 HTTPS。"

	// messageURLHostNotAllowed 表示地址 host 不在白名单。
	messageURLHostNotAllowed = "版本清单地址的来源不在允许列表中。"

	// messageURLTooManyRedirects 表示重定向跳数超限。
	messageURLTooManyRedirects = "版本清单地址的重定向次数超出限制。"

	// messageDataRootUnavailable 表示数据根未注入或不可用。
	messageDataRootUnavailable = "数据根目录不可用，无法读写版本清单缓存。"

	// messageCacheWriteFailed 表示缓存写入失败（保留旧缓存继续使用）。
	messageCacheWriteFailed = "版本清单缓存写入失败，本次仅使用内存中的数据。"

	// messageVersionIDRequired 表示版本 id 为空。
	messageVersionIDRequired = "版本 id 不能为空。"

	// messageVersionIDInvalid 表示版本 id 含路径分隔符、冒号或 .. 段。
	messageVersionIDInvalid = "版本 id 非法：不能包含路径分隔符、冒号或 .. 段。"

	// messageVersionJSONReadFailed 表示版本 JSON 读取失败（未下载或文件缺失）。
	// 参数为版本 id（已截断），便于排查但不泄露路径。
	messageVersionJSONReadFailed = "读取版本「%s」的元数据失败，请确认该版本文件已就位。"

	// messageVersionJSONTooLarge 表示版本 JSON 超出大小上限。
	messageVersionJSONTooLarge = "版本元数据超出允许的大小，已拒绝解析。"

	// messageVersionJSONMalformed 表示版本 JSON 结构非法。
	// 参数为版本 id（已截断）。
	messageVersionJSONMalformed = "版本「%s」的元数据不合法，已拒绝使用。"

	// messageVersionInheritSelf 表示 inheritsFrom 指向自身。
	// 参数为版本 id（已截断）。
	messageVersionInheritSelf = "版本「%s」的 inheritsFrom 指向自身，已拒绝使用。"

	// messageVersionInheritCycle 表示继承链成环。
	// 参数为重复出现的版本 id（已截断）。
	messageVersionInheritCycle = "版本元数据的继承链存在循环引用（%s），已拒绝使用。"

	// messageInheritParentMissing 表示继承链中的父版本缺失。
	// 参数依次为子版本 id 与缺失的父版本 id（均已截断）。
	messageInheritParentMissing = "版本「%s」的父版本「%s」缺失，元数据不完整。"

	// messageInheritTooDeep 表示继承层级超过上限。
	// 参数依次为版本 id（已截断）与深度上限。
	messageInheritTooDeep = "版本「%s」的继承层级超过 %d 层，元数据不完整。"

	// messageArtifactPlanUnsupported 表示计划无法构建。
	messageArtifactPlanUnsupported = "该版本无法生成安装计划：关键字段缺失或暂不支持。"
)

// WarningManifestStale 是离线降级时返回的告警码。
//
// 它不表示失败：列表仍可展示，只是数据来自过期缓存。
// 前端据此在版本列表上提示「当前为离线缓存」。
const WarningManifestStale = "MANIFEST_STALE"

// newInputError 构造一个「输入/地址非法」错误（不可重试）。
func newInputError(code string, message string) *AppError {
	return &AppError{Code: code, Message: message, Retryable: false, Category: CategoryInput}
}

// newSystemError 构造一个「本地环境」错误。
//
// 参数 retryable 表示修正环境后重试是否可能成功。
func newSystemError(code string, message string, retryable bool) *AppError {
	return &AppError{Code: code, Message: message, Retryable: retryable, Category: CategorySystem}
}

// newExternalError 构造一个「外部依赖」错误。
//
// 参数 retryable 表示重试是否可能成功；网络与临时状态码通常为 true。
func newExternalError(code string, message string, retryable bool) *AppError {
	return &AppError{Code: code, Message: message, Retryable: retryable, Category: CategoryExternal}
}

// messageCompatVersionIDRequired 表示兼容性查询缺少版本标识。
const messageCompatVersionIDRequired = "请先选择一个版本后再查询兼容性。"

// messageEnsureRootRequired 表示「确保元数据就位」缺少可用的根目录。
const messageEnsureRootRequired = "缺少游戏目录，无法读取或获取版本元数据。"

// messageManifestCacheUnavailable 表示本地没有可用的清单缓存。
const messageManifestCacheUnavailable = "本地还没有版本清单缓存，请先刷新版本清单后再打开版本详情。"

// messageManifestEntryNotFound 表示清单缓存中查不到指定版本。
const messageManifestEntryNotFound = "版本清单缓存中没有版本「%s」，请先刷新版本清单。"

// messageVersionJSONNetworkFailed 表示下载版本元数据时的网络层失败。
const messageVersionJSONNetworkFailed = "获取版本元数据失败，请检查网络连接后重试。"

// messageVersionJSONNotFound 表示官方源上不存在该版本的元数据文件。
const messageVersionJSONNotFound = "官方源上没有版本「%s」的元数据文件，该版本可能已被下架。"

// messageVersionJSONStatusFailed 表示官方源返回了异常状态。
const messageVersionJSONStatusFailed = "获取版本「%s」的元数据失败，服务端返回了异常状态。"

// messageVersionJSONChecksumMismatch 表示下载内容的 SHA-1 与清单不一致。
const messageVersionJSONChecksumMismatch = "版本「%s」的元数据哈希与清单不一致，已拒绝写入。"

// messageVersionJSONWriteFailed 表示版本元数据写盘失败。
const messageVersionJSONWriteFailed = "版本「%s」的元数据写入失败，请检查磁盘空间与目录权限。"
