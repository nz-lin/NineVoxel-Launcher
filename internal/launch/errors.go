// 本文件定义 launch 包的错误分类与稳定错误码。
//
// 错误码来源（不得自造、不得改义复用）：
//   - `launcher/docs/tasks/P2/决策记录.md` 第五节「P2 新增错误码冻结（2026-11-01）」；
//   - `PATH_INVALID_001` / `JAVA_NOT_FOUND_001` 为 P1 已冻结、P2 复用的既有码；
//   - `JAVA_VERSION_001` 由 P2 新增冻结，首个使用卡即 P2-01；
//   - `LAUNCH_PROFILE_001`（P2-13）与 `LAUNCH_ACCOUNT_001`（P3 账号接入）为后续新增码，
//     同样不得改义复用。
//
// 改义复用被明令禁止：不得用 `JAVA_NOT_FOUND_001` 表达「Java 版本过低」，
// 也不得用 `PATH_INVALID_001` 表达「目录结构缺失」（后者属 P2-02 的 `GAME_DIR_001`）。
package launch

import "fmt"

// 本卡使用的三个稳定错误码。
const (
	// ErrorCodePathInvalid 表示路径非法或越界（空值、相对路径、指向目录、归一化失败）。
	//
	// 来源：P1 冻结清单既有码，P2 复用（决策记录第五节「复用既有码」）。
	ErrorCodePathInvalid = "PATH_INVALID_001"

	// ErrorCodeJavaNotFound 表示文件不存在、不是普通文件，或不可执行。
	//
	// 来源：P1 冻结清单既有码，P2 复用（决策记录第五节「复用既有码」）。
	// 注意：**不得**用它表达「Java 版本过低」。
	ErrorCodeJavaNotFound = "JAVA_NOT_FOUND_001"

	// ErrorCodeJavaVersion 表示 Java 文件存在且可执行，但 `-version` 输出无法解析。
	//
	// 来源：决策记录第五节新增冻结码表（首个使用卡 P2-01）。
	// 注意：仅覆盖「可执行但版本解析失败」，不得用于「文件不存在」。
	ErrorCodeJavaVersion = "JAVA_VERSION_001"
)

// P3 账号接入新增的稳定错误码。
//
// 不复用既有码：账号读取失败既不是「路径非法」（`PATH_INVALID_001`），
// 也不是「版本元数据损坏」（`VERSION_METADATA_001`）——用独立码让前端能按
// 「账号失效 → 引导去账号管理重新登录」处理，而不是当成普通启动参数错误。
const (
	// ErrorCodeLaunchAccount 表示启动时无法读取所选账号的身份或凭证。
	//
	// 触发条件（见 identity.go 的 `resolveAccountIdentity`）：
	//   - 传了 accountId 但装配层没有注入身份解析器（程序装配问题）；
	//   - 解析器返回错误（账号已删除 / 凭证刷新失败）；
	//   - 解析出的身份缺少玩家名或 UUID（账号数据异常）。
	//
	// 文案必须脱敏：不含账号 id、token 片段与路径。
	ErrorCodeLaunchAccount = "LAUNCH_ACCOUNT_001"
)

// ErrorCategory 表示错误的归类，用于区分「用户输入错误 / 系统错误 / 外部依赖错误」。
//
// 分类依据 AGENTS.md 8.4：必须区分用户错误、系统错误和外部依赖错误，
// 因为三者的处置方式不同（前者提示用户修正，后者需要记录与升级排查）。
type ErrorCategory int

const (
	// CategoryInput 表示用户输入问题（选错文件、路径不存在、指向目录）。
	//
	// 这类错误用户看文案自己就能修正，因此界面应就地提示而不是弹系统错误。
	CategoryInput ErrorCategory = iota

	// CategorySystem 表示系统层面问题（无权限、进程创建失败、超时）。
	//
	// 这类错误通常需要用户检查权限或环境，可能需要重试。
	CategorySystem

	// CategoryExternal 表示外部依赖问题（版本输出格式不可识别）。
	//
	// 这类错误说明外部工具的形态超出预期，需要记录以便后续适配。
	CategoryExternal
)

// String 返回错误分类的中文说明，供日志与排查使用。
func (c ErrorCategory) String() string {
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

// Error 是 launch 包的结构化错误。
//
// 携带稳定错误码、归类与已脱敏的中文用户文案：
//   - `Code` 供前端按错误码分支处理与展示；
//   - `Category` 供日志与排查区分问题性质；
//   - `Message` 面向用户，**不得包含堆栈、完整路径或版本输出全文**。
//
// 实现 error 接口，`Error()` 的输出格式为 `<CODE>: <message>`，
// 与 P1 各包的既有约定一致（前端 `toAppError` 能按前缀提取错误码）。
type Error struct {
	// Code 为稳定错误码，取值必须来自本文件定义的常量。
	Code string

	// Category 为错误归类。
	Category ErrorCategory

	// Message 为面向用户的中文说明，已脱敏。
	Message string
}

// Error 实现 error 接口，输出 `CODE: message` 形式。
//
// 该格式让前端 `toAppError` 能直接按前缀识别错误码，无需额外的解析逻辑。
func (e *Error) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Unwrap 支持 errors.Is/As 链式判断。
//
// 当前没有包装底层错误（底层细节不进入用户可见错误），
// 保留该方法是为了后续需要挂载 `%w` 时不破坏调用方。
func (e *Error) Unwrap() error {
	return nil
}

// newInputError 构造一个「用户输入错误」。
//
// 参数 code 为稳定错误码；message 为面向用户的中文说明（调用方负责脱敏）。
func newInputError(code string, message string) *Error {
	return &Error{Code: code, Category: CategoryInput, Message: message}
}

// newSystemError 构造一个「系统错误」。
//
// 参数 code 为稳定错误码；message 为面向用户的中文说明（调用方负责脱敏）。
func newSystemError(code string, message string) *Error {
	return &Error{Code: code, Category: CategorySystem, Message: message}
}

// newExternalError 构造一个「外部依赖错误」。
//
// 参数 code 为稳定错误码；message 为面向用户的中文说明（调用方负责脱敏）。
func newExternalError(code string, message string) *Error {
	return &Error{Code: code, Category: CategoryExternal, Message: message}
}

// 面向用户的中文错误文案。
//
// 集中定义避免同一语义在不同分支写出不同措辞；这些文案都不含路径与堆栈。
const (
	// messagePathEmpty 表示用户没有输入路径。
	messagePathEmpty = "请填写 Java 可执行文件的路径。"

	// messagePathRelative 表示用户输入的是相对路径。
	messagePathRelative = "请填写 Java 可执行文件的完整路径（不支持相对路径）。"

	// messagePathIsDirectory 表示用户选中的是一个目录而不是文件。
	messagePathIsDirectory = "所选路径是一个目录，请选择 Java 可执行文件。"

	// messagePathResolveFailed 表示路径归一化失败。
	messagePathResolveFailed = "无法解析该路径，请检查路径是否正确。"

	// messageJavaNotExist 表示文件不存在。
	messageJavaNotExist = "未在该路径找到 Java 可执行文件，请确认路径是否正确。"

	// messageJavaNotRegular 表示不是普通文件（例如符号链接指向的目录、设备文件）。
	messageJavaNotRegular = "所选路径不是普通文件，请选择 Java 可执行文件。"

	// messageJavaNoPermission 表示读取文件信息时权限不足。
	messageJavaNoPermission = "没有权限读取该文件，请检查文件权限。"

	// messageJavaNotExecutable 表示 Windows 下扩展名不是 .exe。
	messageJavaNotExecutable = "该文件不是 Windows 可执行程序（应为 .exe），请重新选择。"

	// messageVersionRunFailed 表示无法启动 java -version 进程。
	messageVersionRunFailed = "无法执行该 Java 程序，请确认它是可用的 Java 可执行文件。"

	// messageVersionTimeout 表示 java -version 超时。
	messageVersionTimeout = "执行 Java 版本检查超时，该文件可能不是有效的 Java 程序。"

	// messageVersionUnparsable 表示版本输出无法解析。
	messageVersionUnparsable = "已执行该程序，但无法解析其版本输出，可能不是受支持的 Java 实现。"

	// messageVersionOutputEmpty 表示 java -version 没有任何输出。
	messageVersionOutputEmpty = "该程序没有输出任何版本信息，可能不是有效的 Java 程序或执行失败。"

	// messageLaunchAccountUnreadable 表示启动时无法读取所选账号的身份（脱敏文案）。
	//
	// 刻意不区分「解析器未注入 / 账号已删除 / 凭证刷新失败 / 身份字段缺失」：
	// 这些细节对用户没有操作意义，且可能泄漏内部状态；用户能做的动作都是
	// 「去账号管理页重新选择或登录」。
	messageLaunchAccountUnreadable = "无法读取所选账号，请在账号管理中重新登录。"
)

// P2-13 新增：启动配置持久化相关的稳定错误码与中文文案。
//
// 触发条件（`LAUNCH_PROFILE_001`，P2-13 第 56 条新增冻结码）：
//   - 启动配置库打不开或迁移失败（降级：本次运行无法保存配置，但应用照常启动）；
//   - 保存/读取/删除语句执行失败或结果解析失败；
//   - 读取到的最近一条配置指向的 Java 文件或游戏目录已不存在（「上次配置不可用」）。
//
// 不得改义复用既有码：`PATH_INVALID_001` 只表达用户输入路径非法，
// 不表达「历史配置失效」；`DB_MIGRATION_001` 只在 database 包内部使用。
const (
	// ErrorCodeLaunchProfile 表示启动配置读写失败。
	ErrorCodeLaunchProfile = "LAUNCH_PROFILE_001"

	// messageProfileStorageUnavailable 表示启动配置存储不可用（数据库打开或迁移失败）。
	//
	// 该文案刻意说明「本次运行」，因为存储失败是降级而不是崩溃：
	// 用户仍可正常启动游戏，只是保存不了配置。
	messageProfileStorageUnavailable = "启动配置存储不可用（数据库不可用或迁移失败），本次运行无法保存启动配置。"

	// messageProfileJavaPathEmpty 表示保存时没有提供 Java 路径。
	messageProfileJavaPathEmpty = "保存启动配置前需要先选择 Java 可执行文件。"

	// messageProfileGameDirEmpty 表示保存时没有提供游戏目录。
	messageProfileGameDirEmpty = "保存启动配置前需要先选择游戏目录。"

	// messageProfileVersionEmpty 表示保存时没有提供版本 id。
	messageProfileVersionEmpty = "保存启动配置前需要先选择游戏版本。"

	// messageProfilePathUnusable 表示配置中的路径已不可用（Java 文件被卸载、目录被移动）。
	messageProfilePathUnusable = "上次保存的 Java 或游戏目录已不可用，请重新选择。"

	// messageProfileMigrationFailed 表示启动配置库迁移失败。
	//
	// 该文案与 database.MigrationErrorCode（DB_MIGRATION_001）一起返回，
	// 让前端能区分「数据库迁移失败」与普通的配置读写失败；
	// 迁移失败不影响应用启动，只是本次运行无法保存启动配置。
	messageProfileMigrationFailed = "启动配置存储不可用（数据库迁移失败），本次运行无法保存启动配置。"

	// messageProfileSaveFailed 表示写入启动配置失败。
	messageProfileSaveFailed = "保存启动配置失败，请稍后重试。"

	// messageProfileListFailed 表示读取启动配置列表失败。
	messageProfileListFailed = "读取启动配置失败，请稍后重试。"

	// messageProfileDeleteFailed 表示删除启动配置失败。
	messageProfileDeleteFailed = "删除启动配置失败，请稍后重试。"

	// messageProfileIDEmpty 表示删除时没有提供配置标识。
	messageProfileIDEmpty = "删除启动配置需要指定配置标识。"

	// messageProfilePathNotAbsolute 表示保存时路径不是绝对路径。
	//
	// 启动配置要跨重启复用，相对路径会随工作目录变化而失效，因此直接拒绝。
	messageProfilePathNotAbsolute = "启动配置只能保存完整的绝对路径，请重新选择。"
)
