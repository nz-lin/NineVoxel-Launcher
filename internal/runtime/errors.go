// 本文件定义运行时模块的稳定错误类型与错误码。
//
// 错误码格式沿用 P1 的 `<DOMAIN>_<CATEGORY>_<NNN>`。本包只新增「探测失败」
// 四个码（`RUNTIME_PROBE_001`~`RUNTIME_PROBE_004`），其余场景复用既有冻结码：
//
//   - `RUNTIME_NOT_FOUND_001`：没有找到任何可用 Java（冻结清单 §4.2 已列出）；
//   - `VERSION_METADATA_001`：版本元数据不可读或结构非法（F0 既有码，不得重定义）。
//
// 为什么探测失败要拆成四个码而不是一个：
// 四类原因的**处置方式完全不同**——权限不足要用户改权限、不是 Java 要用户重选路径、
// 超时可以重试、格式不认识说明该发行版输出不符合 `java -version` 约定。
// 若合并成一个码，前端只能提示「探测失败」，用户无从判断下一步做什么。
package runtime

import "fmt"

const (
	// ErrorCodeProbePermission 表示目标存在但当前进程无权执行。
	//
	// 典型场景：POSIX 上文件缺少可执行位、Windows 上被 ACL 拒绝。
	// 用户可见文案会提示检查文件权限，且**不**建议重试（重试没有意义）。
	ErrorCodeProbePermission = "RUNTIME_PROBE_001"

	// ErrorCodeProbeNotJava 表示目标不是可用的 Java 程序。
	//
	// 典型场景：文件不存在、路径是目录、启动即失败、执行成功但没有任何输出。
	// 这一类不重试：换一个路径比重复执行更有效。
	ErrorCodeProbeNotJava = "RUNTIME_PROBE_002"

	// ErrorCodeProbeTimeout 表示 `java -version` 在 5 秒内没有结束。
	//
	// 这一类**可重试**：冷启动或磁盘繁忙时可能只是一次性超时。
	ErrorCodeProbeTimeout = "RUNTIME_PROBE_003"

	// ErrorCodeProbeFormat 表示程序能执行，但输出里没有可识别的版本信息。
	//
	// 与 `RUNTIME_PROBE_002` 的区别：程序确实是 Java（能跑），只是输出格式
	// 不在已知的 `java|openjdk version "...` 与 `(build ...)` 形态内。
	ErrorCodeProbeFormat = "RUNTIME_PROBE_004"

	// ErrorCodeRuntimeNotFound 表示没有任何可用的本机 Java。
	//
	// 本卡不把它当作 `DetectRuntimes` 的错误返回（空列表 + 警告更适合界面展示），
	// 仅保留给后续卡（P3-09 选择 Runtime 时使用），避免各卡各自发明同义码。
	ErrorCodeRuntimeNotFound = "RUNTIME_NOT_FOUND_001"

	// ErrorCodeVersionMetadata 复用 F0 的版本元数据错误码。
	//
	// 本包用它表示「需求映射读不到版本元数据」：版本 id 为空、元数据文件缺失、
	// JSON 非法。复用而不是新增 `RUNTIME_METADATA_001`，避免同一现象出现两个码。
	ErrorCodeVersionMetadata = "VERSION_METADATA_001"

	// ErrorCodeRuntimeSource 表示第三方来源（Adoptium）请求失败或响应非法（P3-14）。
	//
	// 与 `RUNTIME_INSTALL_001` 的分工：本码只描述「取不到可信的第三方元数据/归档」，
	// 安装流程自身的问题（写盘失败、提交失败）仍用 `RUNTIME_INSTALL_001`。
	// 该码不在《接口冻结清单》§4.2 的既有列表中，属 P3-14 新增，需 Leader 回填。
	ErrorCodeRuntimeSource = "RUNTIME_SOURCE_001"
)

// ErrorCategory 表示错误的来源类别，用于前端决定提示语气与是否建议重试。
type ErrorCategory int

const (
	// CategoryInput 表示输入或目标本身的问题（由用户操作引起）。
	CategoryInput ErrorCategory = iota
	// CategorySystem 表示本机环境或启动器自身的问题。
	CategorySystem
	// CategoryExternal 表示外部程序（这里是 java 可执行文件）的问题。
	CategoryExternal
)

// String 返回类别的中文描述，用于日志与错误摘要。
func (c ErrorCategory) String() string {
	switch c {
	case CategoryInput:
		return "输入错误"
	case CategorySystem:
		return "系统错误"
	case CategoryExternal:
		return "外部依赖错误"
	default:
		return "未知错误类别"
	}
}

// AppError 是本包对外暴露的错误类型。
//
// 字段与 P1/P2/P3 既有模块保持一致（Code / Message / Retryable / Category），
// 前端按 Code 分支、按 Message 直接展示给用户；Retryable 由后端判定，
// 前端不得自行推断（接口冻结清单 §4.2 约定）。
type AppError struct {
	// Code 为稳定错误码，形如 `RUNTIME_PROBE_003`。
	Code string
	// Message 为可直接展示给用户的简体中文文案。
	Message string
	// Retryable 表示同样的请求稍后重试是否有意义。
	Retryable bool
	// Category 为错误来源类别。
	Category ErrorCategory
}

// Error 实现 error 接口，输出 `CODE: message` 形态。
//
// 与 `internal/launch.Error`、`internal/version.AppError` 的格式一致，
// 前端 `toAppError` 依赖这个前缀解析稳定码。
func (e *AppError) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// IsRetryable 返回该错误是否建议重试。
func (e *AppError) IsRetryable() bool {
	if e == nil {
		return false
	}
	return e.Retryable
}

// newInputError 构造输入类错误（不可重试）。
func newInputError(code, message string) *AppError {
	return &AppError{Code: code, Message: message, Retryable: false, Category: CategoryInput}
}

// newSystemError 构造系统类错误。
//
// 参数 retryable 由调用方按场景给出：数据根暂时不可写之类可能稍后恢复，
// 而解析失败之类重试没有意义。
func newSystemError(code, message string, retryable bool) *AppError {
	return &AppError{Code: code, Message: message, Retryable: retryable, Category: CategorySystem}
}

// newExternalError 构造外部依赖类错误（这里是 java 可执行文件）。
func newExternalError(code, message string, retryable bool) *AppError {
	return &AppError{Code: code, Message: message, Retryable: retryable, Category: CategoryExternal}
}

// 面向用户的简体中文文案。
//
// 集中定义而不是散落在调用点：文案属于产品口径，改动应当集中可见。
const (
	// messageProbeNotFound 用于「路径不存在 / 是目录」。
	messageProbeNotFound = "该路径不是可执行的 Java 程序，请重新选择。"
	// messageProbePermission 用于「文件存在但无权执行」。
	messageProbePermission = "没有权限执行该文件，请检查文件权限或改用其他 Java。"
	// messageProbeWindowsExtension 用于 Windows 上扩展名不是 .exe 的情况。
	messageProbeWindowsExtension = "该文件不是 Java 可执行程序（Windows 下应为 java.exe）。"
	// messageProbeTimeout 用于执行超时。
	messageProbeTimeout = "检测 Java 版本超时，请稍后重试或检查该 Java 是否损坏。"
	// messageProbeLaunchFailed 用于进程无法启动（不是可执行格式）。
	messageProbeLaunchFailed = "无法启动该程序，它可能不是 Java 可执行文件。"
	// messageProbeOutputEmpty 用于执行成功但没有任何输出。
	messageProbeOutputEmpty = "该程序没有输出 Java 版本信息，可能不是 Java。"
	// messageProbeFormat 用于输出无法解析出版本号。
	messageProbeFormat = "无法识别该 Java 的版本信息（输出格式不受支持）。"
	// messageDataRootUnavailable 用于数据根不可用。
	messageDataRootUnavailable = "无法定位数据目录，请检查安装完整性。"
	// messageVersionIDRequired 用于需求映射缺少版本 id。
	messageVersionIDRequired = "请先选择要检查的 Minecraft 版本。"
	// messageMetadataUnreadable 用于版本元数据文件缺失或无法读取。
	messageMetadataUnreadable = "该版本的元数据缺失或无法读取，无法解析 Java 需求。"
	// messageMetadataMalformed 用于版本元数据 JSON 非法。
	messageMetadataMalformed = "该版本的元数据格式非法，无法解析 Java 需求。"
	// messageRequirementUnknown 用于元数据没有声明 Java 版本。
	messageRequirementUnknown = "该版本的元数据没有声明所需的 Java 版本，请手动选择 Java。"
)

// P3-09 新增的错误码。
//
// 全部来自接口冻结清单 §4.2 已列出的码表，不自造：
//   - `RUNTIME_INSTALL_001`：下载、校验或提交失败（视阶段可重试）；
//   - `RUNTIME_UNPACK_001`：解压或路径防护拒绝（不可重试）；
//   - `RUNTIME_REQUIREMENT_001`：所选 Runtime 不满足当前环境要求（不可重试）。
const (
	// ErrorCodeRuntimeInstall 表示安装过程中的下载/校验/提交失败。
	ErrorCodeRuntimeInstall = "RUNTIME_INSTALL_001"

	// ErrorCodeRuntimeUnpack 表示解压或路径防护拒绝了该 Runtime。
	ErrorCodeRuntimeUnpack = "RUNTIME_UNPACK_001"

	// ErrorCodeRuntimeRequirement 表示所选 Runtime 不满足当前要求。
	ErrorCodeRuntimeRequirement = "RUNTIME_REQUIREMENT_001"
)

// P3-09 的中文文案。
const (
	// messageRuntimeDatabaseUnavailable 用于数据库不可用。
	messageRuntimeDatabaseUnavailable = "无法打开运行时记录库，请检查数据目录是否可写。"
	// messageRuntimeWriteFailed 用于写入 Runtime 记录失败。
	messageRuntimeWriteFailed = "保存运行时记录失败，请稍后重试。"
	// messageRuntimeReadFailed 用于读取 Runtime 记录失败。
	messageRuntimeReadFailed = "读取运行时记录失败，请稍后重试。"
	// messageRuntimeNotFound 用于选择了一个不存在的 Runtime。
	messageRuntimeNotFound = "未找到该运行时，它可能已被删除，请重新检测。"
	// messageRuntimeSelectionRequired 用于选择时未给出 runtimeId。
	messageRuntimeSelectionRequired = "请先选择一个 Java 运行时。"
	// messageRuntimeUninstallIDRequired 用于卸载时未给出 runtimeId。
	messageRuntimeUninstallIDRequired = "请先选择要卸载的 Java 运行时。"
	// messageRuntimeUninstallLocalRejected 用于尝试删除本机探测到的 Java。
	messageRuntimeUninstallLocalRejected = "只能删除由启动器安装的运行时；本机 Java 请手动处理。"
	// messageRuntimeUninstallFailed 用于删除安装目录失败。
	messageRuntimeUninstallFailed = "删除运行时目录失败，可能仍被其他进程占用。"
	// messageRuntimeIncompatible 用于选择与当前系统不兼容的 Runtime。
	messageRuntimeIncompatible = "该 Java 与当前系统架构不匹配，如需强制使用请确认后再试。"
	// messageRuntimeLicenseRequired 用于未接受许可就发起安装。
	messageRuntimeLicenseRequired = "安装 Java 运行时前必须勾选并接受许可提示。"
	// messageRuntimeComponentRequired 用于未给出组件名。
	messageRuntimeComponentRequired = "请先选择要安装的 Java 组件。"
	// messageRuntimeComponentUnsupported 用于官方清单里没有该平台/组件。
	messageRuntimeComponentUnsupported = "官方清单中没有适应当前平台与架构的该组件。"
	// messageRuntimePlatformUnsupported 用于当前平台不在官方清单覆盖范围内。
	messageRuntimePlatformUnsupported = "当前平台或架构不受官方 Java 运行时清单支持。"
	// messageRuntimeSourceRejected 用于来源地址不在白名单内。
	messageRuntimeSourceRejected = "该下载地址不在允许的官方来源列表中。"
	// messageRuntimeManifestUnreachable 用于元数据请求失败。
	messageRuntimeManifestUnreachable = "获取官方 Java 运行时清单失败，请检查网络后重试。"
	// messageRuntimeManifestTooLarge 用于元数据超过读取上限。
	messageRuntimeManifestTooLarge = "官方清单体积超出允许的范围，已中止安装。"
	// messageRuntimeManifestMalformed 用于清单结构非法。
	messageRuntimeManifestMalformed = "官方清单结构非法，无法继续安装。"
	// messageRuntimeManifestChecksumMismatch 用于组件清单校验不通过。
	messageRuntimeManifestChecksumMismatch = "官方组件清单的大小或校验值与声明不一致，已中止安装。"
	// messageRuntimeManifestIncomplete 用于提交前文件清单不一致。
	messageRuntimeManifestIncomplete = "安装结果与官方清单不一致，已放弃本次安装。"
	// messageRuntimeTooManyFiles 用于文件数超限。
	messageRuntimeTooManyFiles = "该 Runtime 的文件数量超出上限，已拒绝安装。"
	// messageRuntimeTotalTooLarge 用于总字节超限。
	messageRuntimeTotalTooLarge = "该 Runtime 的总体积超出上限，已拒绝安装。"
	// messageRuntimeFileTooLarge 用于单文件超限。
	messageRuntimeFileTooLarge = "该 Runtime 存在超出单文件上限的文件，已拒绝安装。"
	// messageRuntimeFileDownloadFailed 用于单个文件下载失败。
	messageRuntimeFileDownloadFailed = "下载 Java 运行时文件失败，请重试。"
	// messageRuntimeFileVerifyFailed 用于单个文件校验失败。
	messageRuntimeFileVerifyFailed = "Java 运行时文件校验失败，已放弃本次安装。"
	// messageRuntimeStagingFailed 用于暂存目录创建失败。
	messageRuntimeStagingFailed = "无法创建安装暂存目录，请检查磁盘与数据目录权限。"
	// messageRuntimeCommitFailed 用于提交（整体改名）失败。
	messageRuntimeCommitFailed = "提交 Java 运行时安装失败，请重试。"
	// messageRuntimeQuarantineFailed 用于损坏目录隔离失败。
	messageRuntimeQuarantineFailed = "无法隔离损坏的运行时目录，请手动清理后重试。"
	// messageRuntimeIDInvalid 用于生成的 runtimeId 不合法。
	messageRuntimeIDInvalid = "无法生成合法的运行时标识（版本名包含不支持的字符）。"
	// messageRuntimeInstallFailed 用于下载包返回的未归类失败。
	messageRuntimeInstallFailed = "安装 Java 运行时失败，请重试。"
)

// P3-09 的解压与路径防护文案。
const (
	// messageUnpackTargetInvalid 用于解压目标目录非法。
	messageUnpackTargetInvalid = "解压目标目录非法（必须是绝对路径）。"
	// messageUnpackArchiveUnreadable 用于归档无法打开或读取。
	messageUnpackArchiveUnreadable = "无法读取该归档文件，它可能已损坏。"
	// messageUnpackTooManyFiles 用于文件数超限。
	messageUnpackTooManyFiles = "归档内文件数量超出上限，已拒绝解压。"
	// messageUnpackTooLarge 用于解压总字节超限。
	messageUnpackTooLarge = "归档解压后的总体积超出上限，已拒绝解压。"
	// messageUnpackWriteFailed 用于写盘失败。
	messageUnpackWriteFailed = "写入解压文件失败，请检查磁盘空间与权限。"
)

// messageUnpackSkippedLink 生成「符号链接条目被跳过」的提示文案。
func messageUnpackSkippedLink(relPath string) string {
	return fmt.Sprintf("归档中的符号链接条目 %s 已被跳过。", relPath)
}

// messageUnpackEntryInvalid 生成「条目路径非法」的文案。
func messageUnpackEntryInvalid(name string) string {
	return fmt.Sprintf("归档条目路径非法（可能是路径穿越）：%s", name)
}

// messageUnpackFileTooLarge 生成「单文件超限」的文案。
func messageUnpackFileTooLarge(name string) string {
	return fmt.Sprintf("归档条目 %s 超出单文件大小上限。", name)
}

// messageUnpackRatioExceeded 生成「压缩比超限」的文案。
func messageUnpackRatioExceeded(name string) string {
	return fmt.Sprintf("归档条目 %s 的压缩比超出上限（疑似资源炸弹）。", name)
}

// P3-14 第三方来源（Adoptium）相关文案。
//
// 这一组文案刻意把「元数据不可用」与「归档不可用」分开表述：前者用户重试或换网络
// 即可，后者往往意味着源端文件与校验值不一致，重试不会变好。
const (
	// messageRuntimeMajorRequired 用于未给出 Java 大版本。
	messageRuntimeMajorRequired = "请先选择要安装的 Java 大版本。"
	// messageRuntimeSourcePlatformUnsupported 用于当前平台/架构不受第三方来源支持。
	messageRuntimeSourcePlatformUnsupported = "当前平台或架构不在该第三方来源的支持列表中。"
	// messageRuntimeSourceUnsupported 用于不认识的发行版来源标识。
	messageRuntimeSourceUnsupported = "暂不支持该 Java 发行版。"
	// messageRuntimeSourceMalformed 用于 API 响应不是合法 JSON。
	messageRuntimeSourceMalformed = "第三方来源返回的元数据格式非法，无法继续。"
	// messageRuntimeSourceEmpty 用于 API 返回空数组（没有该大版本的构建）。
	messageRuntimeSourceEmpty = "第三方来源没有提供该 Java 大版本的可用构建。"
	// messageRuntimeSourcePackageMissing 用于响应缺少归档信息。
	messageRuntimeSourcePackageMissing = "第三方来源的元数据缺少归档地址，无法下载。"
	// messageRuntimeSourceChecksumInvalid 用于校验值不是 64 位小写十六进制。
	messageRuntimeSourceChecksumInvalid = "第三方来源给出的 SHA-256 校验值格式非法，已中止。"
	// messageRuntimeSourceSizeInvalid 用于归档大小缺失或超出上限。
	messageRuntimeSourceSizeInvalid = "第三方来源给出的归档大小不在允许范围内，已中止。"
	// messageRuntimeSourceVersionMissing 用于响应缺少版本号。
	messageRuntimeSourceVersionMissing = "第三方来源的元数据缺少版本号，无法比较更新。"
	// messageRuntimeSourceRequestFailed 用于请求构造失败。
	messageRuntimeSourceRequestFailed = "无法构造第三方来源的请求地址。"
	// messageRuntimeSourceTimeout 用于请求超时。
	messageRuntimeSourceTimeout = "请求第三方来源超时，请检查网络后重试。"
	// messageRuntimeSourceRedirectRejected 用于重定向被拒绝。
	messageRuntimeSourceRedirectRejected = "第三方来源的重定向目标不在允许列表中，已中止。"
	// messageRuntimeSourceUnreachable 用于连接失败或 5xx/429。
	messageRuntimeSourceUnreachable = "无法连接第三方来源，请检查网络后重试。"
	// messageRuntimeSourceNotFound 用于 404（镜像滞后或该版本已下架）。
	messageRuntimeSourceNotFound = "第三方来源上没有找到该构建文件。"
	// messageRuntimeSourceStatusFailed 用于其它非 200 状态。
	messageRuntimeSourceStatusFailed = "第三方来源返回了异常状态，已中止。"
	// messageRuntimeSourceTooLarge 用于 API 响应或归档超出上限。
	messageRuntimeSourceTooLarge = "第三方来源返回的内容超出允许的大小，已中止。"
	// messageRuntimeSourceNotHTTPS 用于非 HTTPS 地址。
	messageRuntimeSourceNotHTTPS = "第三方来源的地址必须是 HTTPS。"
	// messageRuntimeSourceHostNotAllowed 用于 host 不在白名单内。
	messageRuntimeSourceHostNotAllowed = "该地址不属于允许的第三方来源。"
	// messageRuntimeSourceMirrorInvalid 用于镜像根地址或路径段非法。
	messageRuntimeSourceMirrorInvalid = "镜像地址非法，已改用官方来源。"
	// messageRuntimeSourceSizeMismatch 用于归档实际字节数与声明不一致。
	messageRuntimeSourceSizeMismatch = "下载的归档大小与声明不一致，已重试下一来源。"
	// messageRuntimeArchiveChecksumMismatch 用于 SHA-256 校验不通过。
	messageRuntimeArchiveChecksumMismatch = "下载的 Java 归档与官方校验值不一致，已丢弃本次下载。"
	// messageRuntimeArchiveUnreadable 用于归档无法读取。
	messageRuntimeArchiveUnreadable = "无法读取下载的 Java 归档，它可能已损坏。"
	// messageRuntimeArchiveTopLevelInvalid 用于归档顶层结构不符合预期。
	messageRuntimeArchiveTopLevelInvalid = "Java 归档的顶层结构不符合预期（应只含一个目录）。"
	// messageRuntimeArchiveExecutableMissing 用于归档缺少 java 可执行文件。
	messageRuntimeArchiveExecutableMissing = "Java 归档中没有找到 java 可执行文件，已放弃安装。"
	// messageRuntimeUpdateInstalledParseFailed 用于已安装版本号无法解析。
	messageRuntimeUpdateInstalledParseFailed = "已安装运行时的版本号无法解析，无法比较更新。"
	// messageRuntimeUpdateLatestParseFailed 用于远端版本号无法解析。
	messageRuntimeUpdateLatestParseFailed = "第三方来源返回的版本号无法解析，无法比较更新。"
	// messageRuntimeUpdateNotChecked 用于尚未检查过更新。
	messageRuntimeUpdateNotChecked = "尚未检查更新。"
)
