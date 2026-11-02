// 本文件定义 launch 包对外暴露的 DTO。
//
// 契约约束（决策记录 D-8 / 第六节，以及 P2-01 卡面第 5 条）：
//   - DTO 的**展示字段**一律是脱敏形态（`javaDisplay`），不含完整绝对路径；
//   - 完整路径只在 `PickJavaExecutable` 的返回值与校验方法的入参之间传递一次，
//     禁止渲染、禁止写 localStorage、禁止进日志、禁止进 `AppError.details`。
//
// 字段命名使用 lowerCamelCase，与前端 `types/launch.ts` 逐字段对应，
// 便于后续 P2-02 / P2-09 直接复用同一套结构。
package launch

// ValidateJavaRequest 是 `ValidateJavaPath` 的入参。
//
// 字段只有一个：用户提供的 Java 可执行文件路径（可能是原生对话框返回的完整路径，
// 也可能是手动输入的文本）。路径的合法性由后端归一化 + 三层校验判定。
type ValidateJavaRequest struct {
	// JavaPath 为用户提供的 Java 可执行文件路径。
	//
	// 该字段**只用于后端校验**：校验通过后前端只应使用返回的 `javaDisplay`，
	// 不得把它渲染到界面。
	JavaPath string `json:"javaPath"`
}

// JavaValidationResult 是 `ValidateJavaPath` 的返回值。
//
// 无论校验成功还是失败都返回本结构（失败时 `Valid=false` 且带 `ErrorCode`/`ErrorMessage`），
// 这样前端只需处理一种形态，不需要「成功走返回值、失败走异常」两套分支。
type JavaValidationResult struct {
	// Valid 表示三层校验是否全部通过。
	Valid bool `json:"valid"`

	// JavaDisplay 为 Java 可执行文件的**脱敏展示形态**。
	//
	// 口径（决策记录 D-8）：目录类别 + `…\` + 末两级，
	// 例如 `Java 安装目录\…\jdk-21\bin\java.exe`。
	// 校验失败时若仍能确定末两级（例如路径存在但版本不可解析）也会填充，
	// 无法确定时为空字符串。
	JavaDisplay string `json:"javaDisplay"`

	// ExecutableName 为可执行文件名（例如 `java.exe`）。
	//
	// 该字段是非敏感信息，可安全用于日志 attributes 与界面展示。
	ExecutableName string `json:"executableName"`

	// MajorVersion 为解析出的 Java 主版本号（整数）。
	//
	// 规则：`1.8.0_401` → 8；`17.0.9` → 17；`21` → 21；`21.0.1` → 21。
	// 校验未走到版本解析阶段或解析失败时为 0。
	MajorVersion int `json:"majorVersion"`

	// VersionOutputSummary 为 `java -version` 输出的**脱敏后首行**。
	//
	// 用于排查「版本解析失败」但又不泄漏完整路径：首行通常形如
	// `openjdk version "17.0.9" 2023-10-17`，本身不含路径；
	// 若首行含路径会被脱敏函数替换。解析成功时也保留，便于用户核对。
	VersionOutputSummary string `json:"versionOutputSummary"`

	// ErrorCode 为失败时的稳定错误码；成功时为空字符串。
	//
	// 取值见 `errors.go` 的三个常量，语义不得混用。
	ErrorCode string `json:"errorCode"`

	// ErrorMessage 为失败时的中文用户文案；成功时为空字符串。
	//
	// 文案不含堆栈、完整路径或输出全文。
	ErrorMessage string `json:"errorMessage"`

	// Warnings 为不影响通过结论的提示（例如「所选文件没有扩展名」）。
	//
	// 前端可原样展示；每条都是已脱敏的中文短句。
	Warnings []string `json:"warnings"`
}

// PickJavaResult 是 `PickJavaExecutable` 的返回值。
//
// 用户取消对话框时：`Canceled=true`、`JavaPath` 与 `JavaDisplay` 为空串，
// 前端应静默保持原值（决策记录第四节的硬性降级要求），不得弹错误提示。
type PickJavaResult struct {
	// Canceled 表示用户取消或关闭了对话框。
	Canceled bool `json:"canceled"`

	// JavaPath 为用户选中的**完整路径**。
	//
	// 该字段按决策记录第六节的边界允许返回给前端一次，供前端立即回传给
	// `ValidateJavaPath`；**禁止**渲染到界面、禁止写入 localStorage、
	// 禁止写进日志、禁止放进 `AppError.details`。
	JavaPath string `json:"javaPath"`

	// JavaDisplay 为同一路径的脱敏展示形态，供界面直接展示。
	JavaDisplay string `json:"javaDisplay"`

	// ErrorCode 为对话框不可用或被系统拒绝时的稳定错误码；正常取消时为空字符串。
	ErrorCode string `json:"errorCode"`

	// ErrorMessage 为对话框不可用时的中文说明；正常取消时为空字符串。
	ErrorMessage string `json:"errorMessage"`
}

// 选择对话框相关的提示文案。
const (
	// dialogTitleJavaExecutable 是 Java 文件选择对话框的标题。
	dialogTitleJavaExecutable = "选择 Java 可执行文件（java.exe）"

	// filterNameJavaExecutable 是文件过滤器在对话框中的显示名。
	filterNameJavaExecutable = "Java 可执行文件 (*.exe)"

	// filterPatternJavaExecutable 是文件过滤器匹配的扩展名模式。
	//
	// Windows 的 Common File Dialog 使用分号分隔多个模式。
	filterPatternJavaExecutable = "*.exe"

	// messageDialogUnavailable 表示当前没有可用的应用实例（装配缺失或未就绪）。
	messageDialogUnavailable = "文件选择对话框当前不可用，请手动填写 Java 路径。"

	// messageDialogFailed 表示对话框本身返回了错误。
	messageDialogFailed = "无法打开文件选择对话框，请手动填写 Java 路径。"
)

// ===== P2-02 游戏目录检查（本卡追加，不修改上方 P2-01 结构）=====

// GameDirectoryRequest 是游戏目录结构检查的入参。
//
// 对应接口冻结清单第 4.2 节的 `GameDirectoryRequest`：
// 只承载一个目录路径，合法性由后端归一化 + 结构检查判定。
type GameDirectoryRequest struct {
	// GameDirectory 为用户提供的游戏根目录路径（通常是 .minecraft）。
	//
	// 该字段**只用于后端检查**：返回结果中只出现 `gameRootDisplay`（脱敏形态），
	// 前端不得把本字段渲染到界面、写 localStorage 或放进 AppError.details。
	GameDirectory string `json:"gameDirectory"`
}

// GameDirectoryCheckResult 是游戏目录结构检查的返回值。
//
// 字段与接口冻结清单第 4.4 节的 `GameDirectoryCheck` 逐字段一致，
// 便于 P2-03 / P2-09 直接复用同一套结构。
type GameDirectoryCheckResult struct {
	// Valid 表示该目录是否足以支撑后续版本识别与启动。
	//
	// 判定规则：`versions/` 是目录且至少有一个直接子目录。
	// **结构缺失不算错误**：`Valid=false` 时通过 `LaunchableHint` 告诉用户缺什么，
	// 而不是返回 error 让界面弹「失败」。
	Valid bool `json:"valid"`

	// GameRootDisplay 为游戏目录的**脱敏展示形态**。
	//
	// 口径（决策记录 C-11）：`游戏目录\…\<父目录>\<目录名>`，
	// 例如 `游戏目录\…\Roaming\.minecraft`。
	// 归一化失败时为空字符串。
	GameRootDisplay string `json:"gameRootDisplay"`

	// Checks 为逐项结构检查结果（六项，顺序固定）。
	//
	// 每项三态，不得用布尔值掩盖「存在但是文件」。
	Checks []GameDirectoryCheckItem `json:"checks"`

	// VersionsCount 为 `versions/` 的**直接**子目录数量（不递归、不读 JSON）。
	//
	// `versions/` 不可用或不可列举时为 0。
	VersionsCount int `json:"versionsCount"`

	// LaunchableHint 为「为什么不能启动」的中文提示；结构可用时为空字符串。
	LaunchableHint string `json:"launchableHint"`

	// ErrorCode 为归一化/访问失败时的稳定错误码；成功时为空字符串。
	//
	// 注意：结构缺失**不**设置该字段（见 `Valid` 的说明）。
	ErrorCode string `json:"errorCode"`

	// ErrorMessage 为失败时的中文用户文案；成功时为空字符串。
	ErrorMessage string `json:"errorMessage"`

	// Warnings 为不影响结论的提示（例如符号链接解析失败、versions 无法列举）。
	Warnings []string `json:"warnings"`
}

// GameDirectoryCheckItem 是单个结构检查项。
//
// 字段与接口冻结清单第 4.4 节的 `checks[]` 元素一致；
// `name` 使用稳定标识（`versions` / `libraries` / `assets` / `assets/indexes` /
// `assets/objects` / `launcher_profiles.json`），前端应按 name 映射文案，
// 不得依赖 `detail` 的措辞做逻辑判断。
type GameDirectoryCheckItem struct {
	// Name 为检查项稳定标识。
	Name string `json:"name"`

	// Status 为三态之一：`present` / `missing` / `not_a_directory`。
	Status string `json:"status"`

	// Detail 为该项的中文说明，面向用户展示。
	Detail string `json:"detail"`
}

// PickGameDirectoryResult 是 `PickGameDirectory` 的返回值。
//
// 与 `PickJavaResult` 结构一致：用户取消时 `Canceled=true` 且路径为空，
// 前端应静默保持原值，不弹错误提示（决策记录第四节的硬性降级要求）。
type PickGameDirectoryResult struct {
	// Canceled 表示用户取消或关闭了对话框。
	Canceled bool `json:"canceled"`

	// GameDirectory 为用户选中的**完整路径**。
	//
	// 按决策记录第六节的边界允许返回给前端一次，供其立即回传给
	// `CheckGameDirectory`；**禁止**渲染、禁止持久化、禁止记日志。
	GameDirectory string `json:"gameDirectory"`

	// GameRootDisplay 为同一路径的脱敏展示形态，供界面直接展示。
	GameRootDisplay string `json:"gameRootDisplay"`

	// ErrorCode 为对话框不可用时的稳定错误码；正常取消时为空字符串。
	ErrorCode string `json:"errorCode"`

	// ErrorMessage 为对话框不可用时的中文说明；正常取消时为空字符串。
	ErrorMessage string `json:"errorMessage"`
}

// 游戏目录选择对话框相关的提示文案。
const (
	// dialogTitleGameDirectory 是游戏目录选择对话框的标题。
	dialogTitleGameDirectory = "选择 Minecraft 游戏目录（通常为 .minecraft）"

	// messageGameDirDialogUnavailable 表示没有可用的应用实例。
	messageGameDirDialogUnavailable = "目录选择对话框当前不可用，请手动填写游戏目录路径。"

	// messageGameDirDialogFailed 表示对话框本身返回了错误。
	messageGameDirDialogFailed = "无法打开目录选择对话框，请手动填写游戏目录路径。"
)

// ===== P2-03 版本识别（本卡追加，不修改上方 P2-01 / P2-02 结构）=====

// VersionListRequest 是「列出版本」的入参。
//
// 只承载游戏根目录；版本 id 不参与列举（列举本身就是为了拿到 id）。
type VersionListRequest struct {
	// GameDirectory 为用户提供的游戏根目录路径（通常是 .minecraft）。
	//
	// 该字段只用于后端检查：返回结果不含任何绝对路径。
	GameDirectory string `json:"gameDirectory"`
}

// VersionSummary 是单个版本的摘要（列表项）。
//
// 字段与接口冻结清单第 4.4 节的 `VersionSummary` 一致：
// `id` / `type?` / `status` / `reason?`。
type VersionSummary struct {
	// ID 为版本标识（目录名）。
	ID string `json:"id"`

	// Type 为版本类型（release / snapshot / old_beta / old_alpha 或自定义）；
	// 无法识别时为空字符串。
	Type string `json:"type"`

	// Status 为三态判定：`launch_supported` / `metadata_only` /
	// `unsupported` / `broken`。
	Status string `json:"status"`

	// Reason 为不能启动或无法识别的原因；可启动时为空字符串。
	Reason string `json:"reason"`
}

// VersionListResult 是「列出版本」的完整返回。
//
// 除版本列表外还带上目录级错误与被跳过的非法目录名，
// 让前端能一次说清「为什么列表是空的」。
type VersionListResult struct {
	// Versions 为识别到的版本列表（按 id 升序）。
	Versions []VersionSummary `json:"versions"`

	// GameRootDisplay 为游戏目录的脱敏展示形态（口径同 P2-02 的 C-11）。
	GameRootDisplay string `json:"gameRootDisplay"`

	// SkippedIDs 为被跳过的非法版本目录名（含不合法字符或超长）。
	//
	// 只列目录名，不含路径。
	SkippedIDs []string `json:"skippedIds"`

	// ErrorCode 为目录级失败时的稳定错误码；成功时为空字符串。
	//
	// 注意：单个版本损坏**不会**设置该字段，而是体现在对应 `VersionSummary.Status`。
	ErrorCode string `json:"errorCode"`

	// ErrorMessage 为目录级失败时的中文文案；成功时为空字符串。
	ErrorMessage string `json:"errorMessage"`

	// Warnings 为不影响结论的提示。
	Warnings []string `json:"warnings"`
}

// VersionCheckRequest 是「检查单个版本」的入参。
type VersionCheckRequest struct {
	// GameDirectory 为用户提供的游戏根目录路径。
	GameDirectory string `json:"gameDirectory"`

	// VersionID 为要检查的版本 id（即 `versions/` 下的目录名）。
	VersionID string `json:"versionId"`
}

// VersionCheckResult 是单个版本的详细检查结果。
//
// 字段与接口冻结清单第 4.4 节的 `VersionCheck` 一致（在 `VersionSummary`
// 基础上增加启动所需信息）。
type VersionCheckResult struct {
	// ID 为版本标识（目录名）。
	ID string `json:"id"`

	// Type 为版本类型；无法识别时为空字符串。
	Type string `json:"type"`

	// Status 为三态判定：`launch_supported` / `metadata_only` /
	// `unsupported` / `broken`。
	Status string `json:"status"`

	// Reason 为不能启动的原因；可启动时为空字符串。
	//
	// 文案覆盖：JSON 损坏 / 缺 jar / 缺 libraries / 缺 natives /
	// 继承不支持 / 缺 mainClass。
	Reason string `json:"reason"`

	// MainClass 为游戏主类；未解析出时为空字符串。
	MainClass string `json:"mainClass"`

	// ClientJarPresent 表示客户端 JAR 是否存在且非空。
	ClientJarPresent bool `json:"clientJarPresent"`

	// LibrariesRequired 为需要检查的依赖库数量。
	LibrariesRequired int `json:"librariesRequired"`

	// LibrariesPresent 为已存在的依赖库数量。
	LibrariesPresent int `json:"librariesPresent"`

	// LibrariesMissing 为缺失的依赖库清单。
	//
	// **只允许相对路径**（例如 `libraries/net/minecraft/.../x.jar`），
	// 绝不包含绝对路径（接口冻结清单与 D-8 的硬性要求）。
	LibrariesMissing []string `json:"librariesMissing"`

	// NativesPresent 表示 natives 目录是否存在且非空。
	//
	// 用指针是为了区分「未检查」（nil）与「检查结果为 false」：
	// 当版本没有任何 natives 声明时，该字段为 nil。
	NativesPresent *bool `json:"nativesPresent"`

	// JavaMajorVersionRequired 为版本 JSON 声明的 Java 主版本；未声明时为 0。
	JavaMajorVersionRequired int `json:"javaMajorVersionRequired"`

	// Warnings 为不影响结论的提示（例如 id 不一致、坐标无法解析）。
	Warnings []string `json:"warnings"`

	// Slow 表示本次检查耗时超过阈值（3 秒）。
	//
	// 仅作提示，不中断检查（卡面第 9 条）。
	Slow bool `json:"slow"`

	// ErrorCode 为版本级失败时的稳定错误码（`VERSION_METADATA_001`）；
	// 结构缺失（缺 jar/库/natives）**不**设置该字段。
	ErrorCode string `json:"errorCode"`

	// ErrorMessage 为版本级失败时的中文文案；正常时为空字符串。
	ErrorMessage string `json:"errorMessage"`
}

// ===== P2-04 启动参数结构化建模（本卡追加，不修改上方 P2-01/02/03 结构）=====

// ArgumentPart 是单个参数元素。
//
// **参数自始至终以本结构组成的数组表示**：不存在「整行命令字符串」的中间形态，
// 交给进程层的唯一形态是 `[]string`（由 P2-05 从这些元素提取 Value 得到）。
type ArgumentPart struct {
	// Value 为参数解析后的最终文本（占位符已替换）。
	//
	// 路径类参数在此处是绝对路径，**仅供进程层内部使用**；
	// 面向用户的预览 DTO 必须另外做脱敏，不得直接展示该字段。
	Value string `json:"value"`

	// Source 为来源标注：`arguments.jvm` / `arguments.game` /
	// `minecraftArguments` / `launcher`（启动器附加）。
	//
	// 该字段用于 P2-09 的参数摘要与排错：能看出某个参数是版本 JSON 声明的
	// 还是启动器自己加的。
	Source string `json:"source"`

	// Placeholder 为原始占位符文本（例如 `${version_name}`）；已解析时留空。
	//
	// 保留原始占位符便于对照「这个值是从哪个占位符来的」。
	Placeholder string `json:"placeholder"`
}

// ArgumentWarning 是参数构建过程中的一条警告。
type ArgumentWarning struct {
	// Code 为稳定错误码（`LAUNCH_ARGS_001` 等）。
	Code string `json:"code"`

	// Message 为面向用户的中文说明，已脱敏（不含绝对路径）。
	Message string `json:"message"`

	// Severity 为严重级别：`blocking`（阻止启动）/ `warn`（提示风险）/ `info`（说明）。
	Severity string `json:"severity"`
}

// UnresolvedPlaceholder 是一个未能解析的占位符。
//
// 集中列出的原因（卡面第 3 条）：未解析占位符不得静默保留原样或替换为空串，
// 必须显式暴露，让用户与后续卡知道「这里缺上下文」。
type UnresolvedPlaceholder struct {
	// Placeholder 为完整占位符文本，例如 `${resolution_width}`。
	Placeholder string `json:"placeholder"`

	// Source 为该占位符出现的位置（来源标注）。
	Source string `json:"source"`

	// Reason 为无法解析的原因说明。
	Reason string `json:"reason"`
}

// LaunchPlan 是启动计划的完整结构。
//
// **四路分离**（卡面第 1 条）：classpath / mainClass / JVM 参数 / 游戏参数
// 各有独立字段，消费方（P2-05 启动、P2-09 预览）不得把它们拼成一行命令。
//
// 该结构**仅供进程层内部使用**：含未脱敏绝对路径（`ClassPathEntries`、
// `NativesDirectory`、`AssetsDirectory`、`WorkingDirectory`），
// **不得**直接返回给前端。前端预览使用 `LaunchPlanPreview`。
type LaunchPlan struct {
	// LaunchIDPlaceholder 为 launchId 占位；P2-06 尚未分配时留空（本卡不涉及进程）。
	LaunchIDPlaceholder string `json:"launchIdPlaceholder"`

	// WorkingDirectory 为进程工作目录：固定为游戏根目录（绝对路径）。
	WorkingDirectory string `json:"workingDirectory"`

	// JVMMajorArgs 为 JVM 参数数组（每项独立，含 `-Dxxx=yyy` 这类单项参数）。
	JVMMajorArgs []ArgumentPart `json:"jvmArgs"`

	// GameArguments 为游戏参数数组（每项独立）。
	GameArguments []ArgumentPart `json:"gameArgs"`

	// ClassPathEntries 为 classpath 条目的绝对路径数组。
	//
	// **顺序**：客户端 JAR 在前，其后按版本 JSON 中 libraries 的声明顺序追加已存在的库。
	// 拼接为单个 `-cp` 值时使用 `os.PathListSeparator`（由 P2-05 负责）。
	ClassPathEntries []string `json:"classPath"`

	// MainClass 为游戏主类。
	MainClass string `json:"mainClass"`

	// NativesDirectory 为 natives 目录绝对路径；目录不存在时为空字符串。
	NativesDirectory string `json:"nativesDirectory"`

	// AssetsDirectory 为资源目录绝对路径（`<gameRoot>/assets`）。
	AssetsDirectory string `json:"assetsDirectory"`

	// AssetIndexID 为资源索引 id（来自版本 JSON）。
	AssetIndexID string `json:"assetIndexID"`

	// Warnings 为构建过程的警告列表。
	Warnings []ArgumentWarning `json:"warnings"`

	// UnresolvedPlaceholders 为未解析占位符清单。
	UnresolvedPlaceholders []UnresolvedPlaceholder `json:"unresolvedPlaceholders"`

	// SafeToLaunch 表示该计划是否可安全交给进程层。
	//
	// false 的情形：存在未解析占位符，或存在 blocking 级警告（缺库 / 缺 natives）。
	SafeToLaunch bool `json:"safeToLaunch"`

	// JavaExecutablePath 为启动使用的 Java 可执行文件绝对路径（P2-06 追加）。
	//
	// 该字段由启动请求传入、由 P2-05 的 `StartGameProcess` 消费；
	// **仅供进程层内部使用**，绝不进入任何前端 DTO
	// （预览走 `LaunchPlanPreview`，启动走 `LaunchAcceptedResult` / `LaunchStatusResult`）。
	JavaExecutablePath string `json:"-"`
}

// LaunchPlanPreviewRequest 是「预览启动计划」的入参。
//
// 附加参数为可选：P2-08 是否提供输入框由 UI 决策决定，
// 本卡只提供后端校验能力（卡面遗留问题 5）。
type LaunchPlanPreviewRequest struct {
	// GameDirectory 为游戏根目录路径。
	GameDirectory string `json:"gameDirectory"`

	// VersionID 为要预览的版本 id。
	VersionID string `json:"versionId"`

	// AdditionalJvmArgs 为附加 JVM 参数（逐项校验后追加）。
	AdditionalJvmArgs []string `json:"additionalJvmArgs"`

	// AdditionalGameArgs 为附加游戏参数（逐项校验，且不得覆盖受保护参数）。
	AdditionalGameArgs []string `json:"additionalGameArgs"`
}

// LaunchPlanPreview 是**面向用户**的启动计划预览。
//
// 与 `LaunchPlan` 的关键区别（安全边界，D-8 与决策记录第六节）：
//   - **不含任何未脱敏绝对路径**：classpath 只给出条目数量与脱敏后的末级文件名，
//     natives / assets / workingDirectory 只给出脱敏展示形态；
//   - 参数数组中**已解析的路径值**也必须脱敏：含绝对路径的参数只展示脱敏形态。
type LaunchPlanPreview struct {
	// VersionID 为预览的版本 id。
	VersionID string `json:"versionId"`

	// MainClass 为游戏主类（非敏感）。
	MainClass string `json:"mainClass"`

	// ClassPathEntryCount 为 classpath 条目数量。
	ClassPathEntryCount int `json:"classPathEntryCount"`

	// ClassPathEntries 为 classpath 条目的**脱敏展示形态**列表。
	//
	// 每项是 `<类别>\…\<末级文件名>` 形式，不含完整路径。
	ClassPathEntries []string `json:"classPathEntries"`

	// JVMMajorArgs 为 JVM 参数的**脱敏后**数组（每项独立）。
	JVMMajorArgs []PreviewArgumentPart `json:"jvmArgs"`

	// GameArguments 为游戏参数的**脱敏后**数组（每项独立）。
	GameArguments []PreviewArgumentPart `json:"gameArgs"`

	// NativesDirectoryDisplay 为 natives 目录的脱敏展示形态；不存在时为空。
	NativesDirectoryDisplay string `json:"nativesDirectoryDisplay"`

	// AssetsDirectoryDisplay 为资源目录的脱敏展示形态。
	AssetsDirectoryDisplay string `json:"assetsDirectoryDisplay"`

	// WorkingDirectoryDisplay 为工作目录的脱敏展示形态。
	WorkingDirectoryDisplay string `json:"workingDirectoryDisplay"`

	// AssetIndexID 为资源索引 id（非敏感）。
	AssetIndexID string `json:"assetIndexID"`

	// Warnings 为警告列表（message 已脱敏）。
	Warnings []ArgumentWarning `json:"warnings"`

	// UnresolvedPlaceholders 为未解析占位符清单（集中列出）。
	UnresolvedPlaceholders []string `json:"unresolvedPlaceholders"`

	// SafeToLaunch 表示是否可安全启动。
	SafeToLaunch bool `json:"safeToLaunch"`

	// OfflineIdentityNotice 为离线身份提示（D-5 要求 UI 必须明示非登录）。
	OfflineIdentityNotice string `json:"offlineIdentityNotice"`

	// ===== 以下字段由 P2-09 追加（决策 D-14~D-18），不改变上方既有字段语义 =====

	// VersionType 为版本类型（release / snapshot / old_beta 等，取自版本 JSON，非敏感）。
	VersionType string `json:"versionType"`

	// JavaDisplay 为 Java 的脱敏展示名（P2-01 口径，仅「目录类别 + 末两段」）。
	//
	// 该字段**不含盘符与用户名段**：完整路径会暴露用户身份与目录结构，
	// 而预览的目的只是让用户确认「用的是哪一个 Java」。
	JavaDisplay string `json:"javaDisplay"`

	// JavaMajorVersion 为 Java 大版本号；0 表示「未知」（缓存缺失或未校验过）。
	//
	// D-16：预览**不得**执行 `java -version` 探针，只能复用 P2-01 最近一次校验结果，
	// 因此该值为 0 时界面应提示「以最近一次校验为准」而不是报错。
	JavaMajorVersion int `json:"javaMajorVersion"`

	// JavaCheckStale 表示 Java 版本信息来自历史校验缓存（不是本次实时探测）。
	//
	// 恒为 true 时界面需要提示用户：显示的是最近一次校验结果。
	JavaCheckStale bool `json:"javaCheckStale"`

	// ClassPathMissingCount 为启动计划中缺失的依赖库数量（来自 P2-04 的 libraries 校验）。
	ClassPathMissingCount int `json:"classPathMissingCount"`

	// BlockingWarnings 为 blocking 级警告子集，供 UI 直接渲染阻断原因（D-18）。
	BlockingWarnings []ArgumentWarning `json:"blockingWarnings"`

	// BuildFailed 表示启动计划构建失败（此时多数字段为空，原因见 BuildErrorCode）。
	//
	// 卡面要求「错误处理为结构化失败原因」：预览失败不返回通用 error，
	// 而是把稳定错误码与脱敏原因放进预览结构，前端按字段渲染即可。
	BuildFailed bool `json:"buildFailed"`

	// BuildErrorCode 为构建失败时的稳定错误码（P2-04 既有错误码，不新增）。
	BuildErrorCode string `json:"buildErrorCode"`

	// BuildErrorMessage 为构建失败的**脱敏后**原因说明。
	BuildErrorMessage string `json:"buildErrorMessage"`

	// DiagnosticsText 为可复制的诊断信息文本（D-17）。
	//
	// 只含脱敏参数摘要、warnings、版本 id / mainClass / classpath 计数、
	// Java 展示名与大版本；**不含**版本 JSON 原文、libraries 全量清单、
	// 未脱敏路径与凭证。文本在后端生成时对同一份已脱敏数据**再走一次**
	// 脱敏函数（二次脱敏），前端复制按钮只复制该字段，不自行拼装。
	DiagnosticsText string `json:"diagnosticsText"`
}

// PreviewArgumentPart 是预览用的参数元素（值已脱敏）。
type PreviewArgumentPart struct {
	// Value 为**脱敏后**的参数文本。
	Value string `json:"value"`

	// Source 为来源标注（与 `ArgumentPart.Source` 一致，非敏感）。
	Source string `json:"source"`

	// Redacted 表示该参数原本含绝对路径，已被脱敏。
	Redacted bool `json:"redacted"`

	// Note 为该参数的补充标注（P2-09 追加，已本地化，可直接展示）。
	//
	// 目前用于离线占位身份：`--username` 的值展示为占位名，
	// 需要提示用户「这不是真实登录身份」（D-15）。
	Note string `json:"note"`
}

// ===== P2-07 游戏日志通道（本卡追加，不修改上方既有结构）=====

// GameLogLineResult 是返回给前端的单行游戏日志。
//
// 与 events 包的 `GameLogLine` 字段一致，但**在这里重新声明**：
// internal/launch 的对外 DTO 不该直接暴露 internal/events 的内部类型，
// 否则 events 包的结构调整会直接改动冻结的前端契约。
type GameLogLineResult struct {
	// TimestampMs 为该行被采集到的时间，UTC Unix 毫秒。
	TimestampMs int64 `json:"timestampMs"`

	// Stream 为输出来源：`stdout` 或 `stderr`。
	Stream string `json:"stream"`

	// Text 为该行**已脱敏**的文本。
	Text string `json:"text"`
}

// GameLogQueryRequest 是快照读取游戏日志的入参。
//
// 选择「独立 `GetGameLog`」而不是「`GetLaunchStatus` 携带最近 N 行」的理由：
//   - 控制台需要翻页（`beforeMs` 游标）与较大 `limit`，塞进状态结构会让
//     每次状态轮询都携带大量日志，且状态与日志的刷新节奏完全不同；
//   - 状态结构（P2-06）已冻结，追加日志数组会破坏其轻量语义。
//
// 因此 P2-08 的控制台用「状态轮询 + 日志快照分页 + 增量事件」三路组合。
type GameLogQueryRequest struct {
	// LaunchID 为要查询的启动标识；为空表示当前活动启动。
	LaunchID string `json:"launchId"`

	// BeforeMs 为时间游标（UTC Unix 毫秒），只返回早于该时间的行；
	// 0 表示从最新开始。
	BeforeMs int64 `json:"beforeMs"`

	// Limit 为最大行数；<=0 用默认值（200），超过上限（1000）被裁剪。
	Limit int `json:"limit"`
}

// GameLogQueryResult 是快照读取的结果。
type GameLogQueryResult struct {
	// Lines 为本页行，按时间**倒序**（最新在前）。
	Lines []GameLogLineResult `json:"lines"`

	// NextBeforeMs 为继续往更早翻的游标；0 表示没有更早的行。
	NextBeforeMs int64 `json:"nextBeforeMs"`

	// DroppedLines 为**累计**被丢弃的行数（内存缓冲溢出 + 落盘队列溢出）。
	//
	// 前端只需展示这一个数值即可知道「日志不完整」。
	DroppedLines int `json:"droppedLines"`

	// HasMore 表示缓冲中是否还有更早的行。
	HasMore bool `json:"hasMore"`

	// ErrorCode 为读取失败时的稳定错误码（`LAUNCH_LOG_001`）；成功时为空。
	ErrorCode string `json:"errorCode"`

	// ErrorMessage 为失败时的中文文案；成功时为空。
	ErrorMessage string `json:"errorMessage"`
}

// ===== P2-06 启动生命周期（本卡追加，不修改上方既有结构）=====

// StartLaunchRequest 是启动请求（**只接受结构化参数，不接受任意命令行**）。
//
// 字段依据接口冻结清单第 4.4 节。与原提案的差异：
//   - `instanceId` 改为不使用：P2 无实例语义（C-1，实例属 P5），
//     标识由本卡分配的 `launchId` 承担；
//   - `dryRun` 不在此结构内：预览走 P2-04 的 `PreviewLaunchPlan`，职责更清晰。
type StartLaunchRequest struct {
	// JavaPath 为用户已校验的 Java 可执行文件路径。
	//
	// 该字段只用于后端启动，**不进入任何返回值**。
	JavaPath string `json:"javaPath"`

	// GameDirectory 为游戏根目录路径。
	GameDirectory string `json:"gameDirectory"`

	// VersionID 为要启动的版本 id。
	VersionID string `json:"versionId"`

	// AccountID 为所选账号的本地 id；为空表示沿用离线占位身份（兼容旧调用）。
	//
	// 该字段只是账号子系统内的本地标识，**不是**凭证：后端用它向账号子系统
	// 换取启动所需的身份（见 identity.go 的 AccountIdentityResolver），
	// access token 等凭证只在后端内存中流转，不进入任何返回值。
	AccountID string `json:"accountId,omitempty"`

	// AdditionalJvmArgs 为附加 JVM 参数（逐项校验后追加）。
	AdditionalJvmArgs []string `json:"additionalJvmArgs"`

	// AdditionalGameArgs 为附加游戏参数（逐项校验，且不得覆盖受保护参数）。
	AdditionalGameArgs []string `json:"additionalGameArgs"`
}

// LaunchAcceptedResult 是启动受理结果。
//
// 字段依据接口冻结清单第 4.4 节 `LaunchAccepted`。
// **不含命令行原文与任何凭证**。
type LaunchAcceptedResult struct {
	// LaunchID 为本次启动标识（UUID v4）；只在内存中存在。
	LaunchID string `json:"launchId"`

	// AcceptedAtMs 为受理时间（状态首次迁移时间），UTC Unix 毫秒。
	AcceptedAtMs int64 `json:"acceptedAtMs"`

	// State 为受理后的当前状态。
	State string `json:"state"`
}

// StopLaunchRequest 是停止请求。
type StopLaunchRequest struct {
	// LaunchID 为要停止的启动标识；空字符串表示当前活动启动。
	LaunchID string `json:"launchId"`

	// Force 表示直接强制终止（跳过优雅等待）。
	//
	// **UI 必须在二次确认后才传 true**：Windows 上 `Kill()` 不执行 JVM 关闭钩子，
	// Minecraft 可能来不及保存世界数据（D-3）。
	Force bool `json:"force"`
}

// LaunchStatusRequest 是状态查询请求。
type LaunchStatusRequest struct {
	// LaunchID 为要查询的启动标识；空字符串表示当前活动启动（或最近的终态记录）。
	LaunchID string `json:"launchId"`
}

// LaunchStatusResult 是启动状态快照。
//
// **本结构是 `GetLaunchStatus` 的唯一真相源**：事件可能丢失，
// 前端必须能在完全丢失事件后靠它恢复界面。
//
// 字段依据接口冻结清单第 4.4 节 `LaunchStatus`，**不含任何绝对路径**。
// `recentLines` / `droppedLines` 不内嵌：控制台数据由 P2-07 的 `GetGameLog` 分页读取
// （理由见 P2-07 的 `GameLogQueryRequest` 注释）。
type LaunchStatusResult struct {
	// LaunchID 为本次启动标识。
	LaunchID string `json:"launchId"`

	// State 为当前状态；取值见 State* 常量。
	State string `json:"state"`

	// VersionID 为本次启动的版本 id。
	VersionID string `json:"versionId"`

	// StartedAtMs 为进程创建成功时间；尚未创建时 0。
	StartedAtMs int64 `json:"startedAtMs"`

	// EndedAtMs 为进程退出时间；尚未退出时 0。
	EndedAtMs int64 `json:"endedAtMs"`

	// ExitCode 为进程退出码；尚无时 0。
	ExitCode int `json:"exitCode"`

	// HasExitCode 区分「退出码 0」与「尚无退出码」。
	HasExitCode bool `json:"hasExitCode"`

	// StopReason 为停止原因：`user_requested` / `app_exiting` / `startup_failed`。
	StopReason string `json:"stopReason"`

	// StopResult 为停止结果：`graceful` / `killed` / `timeout`。
	StopResult string `json:"stopResult"`

	// Outcome 为退出结局：`exited_normally` / `stopped_by_user` / `crashed`。
	Outcome string `json:"outcome"`

	// StateChangedAtMs 为最近一次状态迁移时间，UTC Unix 毫秒。
	StateChangedAtMs int64 `json:"stateChangedAtMs"`

	// ErrorCode 为查询失败时的稳定错误码；成功时为空字符串。
	ErrorCode string `json:"errorCode"`

	// ErrorMessage 为失败时的中文文案；成功时为空字符串。
	ErrorMessage string `json:"errorMessage"`
}

// ===== P2-13 启动配置持久化（本卡追加，不修改上方既有结构）=====

// SaveLaunchProfileRequest 是保存启动配置的请求。
//
// 字段说明：
//   - `JavaPath` 与 `GameDirectory` 是**绝对路径**，只入库存档，永不回传前端；
//   - `VersionID` 是可直接展示的版本 id；
//   - `Name` 预留配置名称，P2 阶段前端不传（由后端按版本 id 生成默认名称）。
type SaveLaunchProfileRequest struct {
	// JavaPath 为用户已校验通过的 Java 可执行文件绝对路径。
	JavaPath string `json:"javaPath"`

	// GameDirectory 为游戏根目录绝对路径。
	GameDirectory string `json:"gameDirectory"`

	// VersionID 为本次启动使用的版本 id。
	VersionID string `json:"versionId"`

	// Name 为配置显示名称；空字符串时由后端按版本 id 生成。
	Name string `json:"name"`
}

// LaunchProfileSummary 是启动配置的对外摘要。
//
// **安全硬约束（P2-13 第 55 条）**：本结构**不得包含任何绝对路径**，
// 只提供经过脱敏的展示串，理由与 P2-09 的 D-14 一致：
// 用户配置目录里可能含用户名，完整路径既无展示价值又会泄露本机信息。
//
// 字段与绝对路径的对应关系：
//   - `JavaDisplay`  ← java_path，经「Java 安装目录 + … + 末三段」脱敏；
//   - `GameRootDisplay` ← game_directory，经「游戏目录 + … + 末两段」脱敏。
type LaunchProfileSummary struct {
	// ID 为配置唯一标识。
	ID string `json:"id"`

	// Name 为配置显示名称。
	Name string `json:"name"`

	// JavaDisplay 为 Java 可执行文件的脱敏展示串（不含盘符与用户名段）。
	JavaDisplay string `json:"javaDisplay"`

	// GameRootDisplay 为游戏目录的脱敏展示串（不含盘符与用户名段）。
	GameRootDisplay string `json:"gameRootDisplay"`

	// VersionID 为配置保存的版本 id。
	VersionID string `json:"versionId"`

	// LastUsedAtMs 为该配置最近一次用于启动的时间，UTC Unix 毫秒。
	LastUsedAtMs int64 `json:"lastUsedAtMs"`
}

// DeleteLaunchProfileRequest 是删除启动配置的请求。
type DeleteLaunchProfileRequest struct {
	// ID 为要删除的配置标识。
	ID string `json:"id"`
}

// LaunchProfileDeletedResult 是删除启动配置的结果。
//
// 用 `Deleted` 显式表达结果而不是只靠 error：
// 配置本就不存在时删除也算成功（幂等），前端仍能据此刷新列表。
type LaunchProfileDeletedResult struct {
	// Deleted 表示配置是否已不存在（删除成功与「本来就不存在」都返回 true）。
	Deleted bool `json:"deleted"`
}
