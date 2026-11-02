// 本文件定义账号子系统的错误分类与稳定错误码。
//
// 约定与 internal/launch、internal/download 保持一致：
//   - 错误码是稳定字符串，前端按码分支，**不得改义复用**；
//   - 面向用户的 Message 必须已脱敏：不含 token、密码、设备码、完整 URL 查询串；
//   - 错误实现 error 接口，输出 `<CODE>: <message>`，前端按前缀提取错误码。
package account

import "fmt"

// 账号子系统的稳定错误码。
const (
	// ErrorCodeInput 表示用户输入不合法（用户名格式错误、缺少必填字段等）。
	ErrorCodeInput = "ACCOUNT_INPUT_001"

	// ErrorCodeProviderUnknown 表示账号来源类型未知或未注册。
	ErrorCodeProviderUnknown = "ACCOUNT_PROVIDER_001"

	// ErrorCodeProviderUnavailable 表示来源存在但尚未上线（Ninevoxel 占位）。
	ErrorCodeProviderUnavailable = "ACCOUNT_PROVIDER_UNAVAILABLE_001"

	// ErrorCodeLogin 表示登录流程失败（服务端拒绝、协议响应异常等）。
	ErrorCodeLogin = "ACCOUNT_LOGIN_001"

	// ErrorCodeLoginExpired 表示设备码/登录会话已过期。
	ErrorCodeLoginExpired = "ACCOUNT_LOGIN_002"

	// ErrorCodeLoginDeclined 表示用户在授权页面拒绝授权。
	ErrorCodeLoginDeclined = "ACCOUNT_LOGIN_003"

	// ErrorCodeProfileRequired 表示需要用户先选择档案。
	ErrorCodeProfileRequired = "ACCOUNT_LOGIN_004"

	// ErrorCodeAuth 表示凭证无效或已失效（401/403、Yggdrasil Invalid token）。
	ErrorCodeAuth = "ACCOUNT_AUTH_001"

	// ErrorCodeNetwork 表示网络不可达、超时或连接被中断（可重试）。
	ErrorCodeNetwork = "ACCOUNT_NETWORK_001"

	// ErrorCodeHTTP 表示服务端返回了非预期的 HTTP 状态或无法解析的响应。
	ErrorCodeHTTP = "ACCOUNT_HTTP_001"

	// ErrorCodeOwnership 表示账号没有可用的 Minecraft 档案（未购买/未创建档案）。
	ErrorCodeOwnership = "ACCOUNT_OWNERSHIP_001"

	// ErrorCodeStorage 表示账号存储不可用（数据库打开/迁移/读写失败）。
	ErrorCodeStorage = "ACCOUNT_STORAGE_001"

	// ErrorCodeCrypto 表示凭证加解密失败（密钥不可用或密文损坏）。
	ErrorCodeCrypto = "ACCOUNT_CRYPTO_001"

	// ErrorCodeNotFound 表示按 id 找不到账号。
	ErrorCodeNotFound = "ACCOUNT_NOT_FOUND_001"

	// ErrorCodeState 表示操作与账号/会话状态不匹配（会话不存在、已结束、状态不允许）。
	ErrorCodeState = "ACCOUNT_STATE_001"

	// ErrorCodeSite 表示 Yggdrasil 服务地址或元数据不合法。
	ErrorCodeSite = "ACCOUNT_SITE_001"
)

// Category 是错误归类，用于区分处置方式。
type Category int

const (
	// CategoryInput 表示用户输入问题：提示用户就地修正即可。
	CategoryInput Category = iota

	// CategorySystem 表示本机系统问题：权限、磁盘、加解密、数据库。
	CategorySystem

	// CategoryExternal 表示外部服务问题：网络、认证服务器、协议响应。
	CategoryExternal
)

// String 返回错误分类的中文说明。
func (c Category) String() string {
	switch c {
	case CategoryInput:
		return "用户输入错误"
	case CategorySystem:
		return "系统错误"
	case CategoryExternal:
		return "外部依赖错误"
	default:
		return "未知分类"
	}
}

// Error 是账号子系统的结构化错误。
//
// 与 internal/launch 的 Error 保持同构（Code/Category/Message + error 接口），
// 额外带 Retryable：网络类错误界面应给出「重试」，输入类错误不应出现重试按钮。
type Error struct {
	// Code 为稳定错误码。
	Code string

	// Category 为错误归类。
	Category Category

	// Retryable 表示重试是否可能成功。
	Retryable bool

	// Message 为面向用户的中文说明（已脱敏）。
	Message string
}

// Error 实现 error 接口，输出 `CODE: message`。
func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Unwrap 支持 errors.Is/As 链式判断（当前不包装底层错误）。
func (e *Error) Unwrap() error { return nil }

// newInputError 构造用户输入错误。
func newInputError(code, message string) *Error {
	return &Error{Code: code, Category: CategoryInput, Message: message}
}

// newSystemError 构造系统错误。
func newSystemError(code, message string) *Error {
	return &Error{Code: code, Category: CategorySystem, Message: message}
}

// newExternalError 构造外部依赖错误。
func newExternalError(code, message string, retryable bool) *Error {
	return &Error{Code: code, Category: CategoryExternal, Retryable: retryable, Message: message}
}

// newAuthError 构造外部依赖错误中的「凭证问题」（不可重试，需要重新登录）。
func newAuthError(code, message string) *Error {
	return &Error{Code: code, Category: CategoryExternal, Retryable: false, Message: message}
}
