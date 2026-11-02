package artifact

// 本包的错误码：取值来自 launcher/docs/tasks/P3/接口冻结清单.md，
// 由 Leader 为 P3-03 批准，**只有两个**，不得在实现中自行扩充。
const (
	// ErrorCodeArtifactPath 表示目标路径非法或越界。
	//
	// 触发条件：绝对路径、盘符切换、UNC 前缀、NUL 字符、`..` 段逃逸、
	// 符号链接逃逸、空相对路径、哈希/标识符格式非法、P3 不支持的制品类型。
	ErrorCodeArtifactPath = "ARTIFACT_PATH_001"

	// ErrorCodeArtifactLayout 表示数据根不可用。
	//
	// 触发条件：未配置（既无用户配置目录也拿不到可执行文件位置）、
	// 不可写（含便携版 data 目录不可写且按规则不得回退）、路径非法
	// （数据根不是绝对路径或含 NUL / `..`）。
	ErrorCodeArtifactLayout = "ARTIFACT_LAYOUT_001"
)

// 数据根失败原因：作为 Error.Reason 返回，用于调用方区分三态，
// 不新增错误码（Leader 只批准了 ARTIFACT_LAYOUT_001 一个码表达该域）。
const (
	// DataRootReasonNotConfigured 表示运行环境既没有用户配置目录，也拿不到可执行文件位置。
	DataRootReasonNotConfigured = "not_configured"

	// DataRootReasonNotWritable 表示数据根目录不可写（含便携版不可写且不允许回退）。
	DataRootReasonNotWritable = "not_writable"

	// DataRootReasonInvalidPath 表示解析出的数据根路径本身非法。
	DataRootReasonInvalidPath = "invalid_path"
)

// ErrorCategory 标识错误来源，用于调用方决定是否可重试或是否属于用户输入问题。
type ErrorCategory string

const (
	// ErrorCategoryInput 表示调用方输入不合法（路径、哈希、标识符等）。
	ErrorCategoryInput ErrorCategory = "input"

	// ErrorCategorySystem 表示运行环境不可用（数据根不可写等）。
	ErrorCategorySystem ErrorCategory = "system"
)

// Error 是本包的稳定错误形状。
//
// 形状与 internal/launch 的 Error 保持一致（code + category + message），
// 便于后续统一提取到公共错误包；本包不依赖 internal/launch，避免反向耦合。
// Reason 只用于数据根三态的细分，取值为 DataRootReason* 常量；其他错误留空。
type Error struct {
	// Code 是稳定错误码，前端按码分支。
	Code string

	// Category 区分输入错误与系统错误。
	Category ErrorCategory

	// Message 是面向用户的中文说明，不含任何绝对路径或凭证。
	Message string

	// Reason 是可选细分原因，不改变错误码。
	Reason string
}

// Error 实现 error 接口，输出「码: 说明」形式，便于日志与前端展示分离。
func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	return e.Code + ": " + e.Message
}

// Is 支持 errors.Is 按错误码比较，便于调用方在不解析文案的前提下判断错误类别。
func (e *Error) Is(target error) bool {
	other, ok := target.(*Error)
	if !ok || e == nil || other == nil {
		return false
	}
	return e.Code == other.Code
}

// newInputError 构造调用方输入错误。
func newInputError(code, message string) *Error {
	return &Error{Code: code, Category: ErrorCategoryInput, Message: message}
}

// newSystemError 构造运行环境错误。
func newSystemError(code, message string) *Error {
	return &Error{Code: code, Category: ErrorCategorySystem, Message: message}
}

// newLayoutError 构造数据根不可用错误，并带上细分原因。
func newLayoutError(reason, message string) *Error {
	err := newSystemError(ErrorCodeArtifactLayout, message)
	err.Reason = reason
	return err
}

// 面向用户的固定文案：集中声明，避免同一提示在多处硬编码（AGENTS.md 8.4）。
const (
	// messagePathEmpty 用于空相对路径。
	messagePathEmpty = "相对路径不能为空。"

	// messagePathAbsolute 用于绝对路径注入。
	messagePathAbsolute = "只接受相对路径：绝对路径会绕过数据根边界。"

	// messagePathVolume 用于盘符或 UNC 前缀。
	messagePathVolume = "路径中不能包含盘符或 UNC 前缀。"

	// messagePathNUL 用于 NUL 字符截断攻击。
	messagePathNUL = "路径中不能包含 NUL 字符。"

	// messagePathParentSegment 用于 `..` 段逃逸。
	messagePathParentSegment = "路径中不能包含 .. 段。"

	// messagePathEscape 用于 filepath.Rel 判定越界（含跨卷）。
	messagePathEscape = "解析后的路径越出数据根。"

	// messageRootEmpty 用于数据根为空。
	messageRootEmpty = "数据根为空，无法解析目标路径。"

	// messageRootRelative 用于数据根不是绝对路径。
	messageRootRelative = "数据根必须是绝对路径。"

	// messageDataRootNotConfigured 用于两处目录都拿不到。
	messageDataRootNotConfigured = "未配置数据根：既没有用户配置目录，也拿不到可执行文件位置。"

	// messageDataRootNotWritable 用于数据根不可写。
	messageDataRootNotWritable = "数据根目录不可写：请检查目录权限，或改用可写的位置。"

	// messageDataRootPortableNotWritable 用于便携版 data 目录不可写且不允许回退。
	messageDataRootPortableNotWritable = "便携版数据目录不可写：按目录策略不回退到用户目录，请修复该目录权限。"

	// messageDataRootInvalidPath 用于数据根路径非法。
	messageDataRootInvalidPath = "数据根路径非法：必须是绝对路径，且不能包含 .. 段或 NUL 字符。"

	// messageKindUnsupported 用于 P3 不落盘的制品类型（实例级内容归 P5）。
	messageKindUnsupported = "P3 阶段不支持该制品类型的目标路径，实例级内容由 P5 的实例目录承担。"

	// messageKindUnknown 用于未知 kind 取值。
	messageKindUnknown = "未知的制品类型。"

	// messageHashAlgorithm 用于内容寻址算法不受支持。
	messageHashAlgorithm = "内容寻址只接受 sha1 或 sha256 作为寻址算法。"

	// messageHashFormat 用于哈希格式非法。
	messageHashFormat = "哈希值格式非法：必须是小写十六进制，且长度与算法匹配。"

	// messageSimpleNameInvalid 用于标识符非法（版本 id、runtime id、任务 id 等）。
	messageSimpleNameInvalid = "标识符非法：不能为空，不能包含路径分隔符、冒号或 .. 段。"

	// messageFileNameInvalid 用于文件名非法。
	messageFileNameInvalid = "文件名非法：不能为空，不能包含路径分隔符或 .. 段。"
)
