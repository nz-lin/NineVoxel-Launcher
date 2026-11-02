// 本文件实现启动参数的结构化建模与占位符解析（P2-04）。
//
// 核心约束（卡面第 1–3 条，全文严格遵守）：
//   - **四路分离**：classpath / mainClass / JVM 参数 / 游戏参数各有独立字段，
//     任何 DTO 都不出现「一整行命令」的字符串；
//   - **只用数组**：参数自始至终是 `[]ArgumentPart`，本文件**不存在**把多个参数
//     拼成一个字符串再交给下游的代码路径；唯一交给进程层的形态是 `[]string`；
//   - **占位符显式化**：能解析的写入参数值并标注，不能解析的进入
//     `UnresolvedPlaceholders` 并产生 warning，**绝不**替换成空串后继续。
//
// 安全约束（AGENTS.md 第 9 节）：
//   - 不实现 shell 词法。旧格式 `minecraftArguments` 只按**空白字符**切分，
//     引号、转义、管道、重定向一律当普通字符（局限见 `splitLegacyArguments` 注释）；
//   - 附加参数逐项校验，拒绝换行 / NUL / shell 元字符；
//   - 附加参数不得覆盖离线身份三项与安全必需参数（D-5 冻结）。
//
// 只读：本文件不创建目录、不写文件、不解压、不下载。natives 目录仅**检查存在性**（D-10）。
package launch

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// 参数建模与安全校验相关的常量。
const (
	// ErrorCodeLaunchArgs 表示启动参数构建失败（占位符无法解析、classpath 为空等）。
	//
	// 来源：决策记录第五节冻结表：`LAUNCH_ARGS_001`（启动参数构建失败），首个使用卡 P2-04。
	ErrorCodeLaunchArgs = "LAUNCH_ARGS_001"

	// ErrorCodeLaunchArgsTooLong 表示参数总长度超限。
	//
	// 来源：决策记录第五节冻结表：`LAUNCH_ARGS_002`（参数总长度超限），首个使用卡 P2-04。
	ErrorCodeLaunchArgsTooLong = "LAUNCH_ARGS_002"

	// ===== D-12：arguments.rules 最小语义（2026-11-01 补强，决策记录第七节）=====

	// ruleActionAllow 是规则动作 `allow`：条件匹配时**保留**参数。
	ruleActionAllow = "allow"

	// ruleActionDisallow 是规则动作 `disallow`：条件匹配时**排除**参数。
	//
	// 该动作必须实现：遗漏会让本应排除的参数被加入。
	ruleActionDisallow = "disallow"

	// featureIsDemoUser 是 Minecraft 规则的 `is_demo_user` 特性名。
	//
	// P2 恒 false（不提供 demo 模式）。
	featureIsDemoUser = "is_demo_user"

	// featureHasCustomResolution 是 Minecraft 规则的 `has_custom_resolution` 特性名。
	//
	// P2 恒 true（按 D-13 注入默认分辨率）。
	featureHasCustomResolution = "has_custom_resolution"

	// ===== D-13：默认分辨率（2026-11-01 补强，决策记录第七节）=====

	// defaultResolutionWidth 是 P2 注入的默认分辨率宽度。
	//
	// 取 854×480（Minecraft 官方默认窗口尺寸）：让引用
	// `${resolution_width}` / `${resolution_height}` 的版本能启动。
	defaultResolutionWidth = 854

	// defaultResolutionHeight 是 P2 注入的默认分辨率高度。
	defaultResolutionHeight = 480

	// maxResolutionValue 是 `--width` / `--height` 覆盖值的上限。
	//
	// 取 16384：远超任何真实显示器分辨率（8K 为 7680），
	// 同时拦住把异常大数传进游戏导致的分配失败或崩溃。
	maxResolutionValue = 16384

	// maxTotalArgumentBytes 是「所有参数字符总长」的上限。
	//
	// 取 32767（Windows CreateProcess 的传统命令行长度上限）：虽然 Go 的 exec 在
	// Windows 上通过 CreateProcess 传递参数、实际分发给 Java 的是同一份命令行，
	// 超过该长度会被系统拒绝。**在软件层提前拦住**可以给出明确错误，
	// 而不是让进程创建以难以理解的方式失败。
	// 说明：该上限按「参数文本总字节数」计算，不追求逐字节模拟系统计数规则，
	// 只作为「明显过长」的防线。
	maxTotalArgumentBytes = 32767

	// argumentSourceArgumentsJVM 表示参数来自 `arguments.jvm`。
	argumentSourceArgumentsJVM = "arguments.jvm"

	// argumentSourceArgumentsGame 表示参数来自 `arguments.game`。
	argumentSourceArgumentsGame = "arguments.game"

	// argumentSourceMinecraftArguments 表示参数来自旧式 `minecraftArguments`。
	argumentSourceMinecraftArguments = "minecraftArguments"

	// argumentSourceLauncher 表示参数由启动器附加（附加参数或必需的身份参数）。
	argumentSourceLauncher = "launcher"

	// warningSeverityBlocking 表示该警告会阻止启动。
	warningSeverityBlocking = "blocking"

	// warningSeverityWarn 表示该警告只提示风险，不阻止启动。
	warningSeverityWarn = "warn"

	// warningSeverityInfo 表示普通说明。
	warningSeverityInfo = "info"
)

// 离线占位身份常量（D-5 冻结）。
//
// 必须在此集中定义并注明「占位」：P2 不做认证（认证属 P4），
// 这三项固定值只用于让游戏进程能起来，**不是真实账号**。
const (
	// placeholderUsername 是 `--username` 的固定占位值。
	//
	// 取值 `Player`：Minecraft 离线模式的默认显示名，便于用户一眼看出「不是真实账号」。
	placeholderUsername = "Player"

	// placeholderUUID 是 `--uuid` 的固定离线 UUID。
	//
	// 这是一个**离线占位 UUID**：格式合法（便于游戏侧解析），但不对应任何真实账号。
	// 值本身是固定的（不随机生成），以保证同一台机器每次启动参数一致，便于排查。
	// P2 不实现账号绑定，也不从任何地方读取真实 UUID。
	placeholderUUID = "00000000-0000-0000-0000-000000000000"

	// placeholderAccessToken 是 `--accessToken` 的固定占位值。
	//
	// **绝不是真实 token**：它只是一个固定字符串，用于满足游戏启动器对参数存在性的要求。
	// 离线模式下游戏不会用它访问任何在线服务；P2 也不发起任何网络请求。
	placeholderAccessToken = "0"

	// placeholderUserType 是 `--userType` 的固定占位值。
	//
	// `legacy` 是原版启动器对离线身份使用的类型标识。
	placeholderUserType = "legacy"

	// placeholderClientID 是 `${clientid}` 与 `${auth_xuid}` 的固定占位值（D-19）。
	//
	// 这两个占位符自 1.20.5 起出现在 `arguments.game`（`--clientId` 与 `--auth_xuid`），
	// 用于实时服务与遥测，**离线模式下没有真实值也不影响启动**。
	// 若不提供取值，整个参数会被判为「占位符无法解析」并升级成 blocking 警告，
	// 让本可启动的版本卡在预览阶段（P2 验收实测：`26.3` 版本因此无法启动）。
	// 取值 `0` 与 `--accessToken` 的占位保持一致；真实登录接入（P4）后由账号模块替换。
	placeholderClientID = "0"

	// launcherName 是 `${launcher_name}` 的解析值。
	launcherName = "NineVoxelLauncher"

	// launcherVersion 是 `${launcher_version}` 的解析值。
	//
	// 与 P1 阶段应用版本保持一致（`launcher/appservice.go` 的 `appVersion`）。
	// 此处独立声明而不是跨包引用：internal/launch 不应依赖 package main。
	launcherVersion = "0.1.0"
)

// 受保护的游戏参数键：这些由启动器自己写入，附加参数不得覆盖（D-5 冻结）。
//
// 覆盖尝试一律返回 `LAUNCH_ARGS_001`，而不是「后写覆盖前写」——
// 后者会让用户通过附加参数静默改掉身份或版本，是最危险的隐式行为。
var protectedGameArgKeys = map[string]bool{
	"--username":    true,
	"--uuid":        true,
	"--accessToken": true,
	"--userType":    true,
	"--version":     true,
	"--gameDir":     true,
	"--assetsDir":   true,
	"--assetIndex":  true,
}

// 受保护的 JVM 参数前缀：附加 JVM 参数不得覆盖 classpath 与 natives 路径。
var protectedJVMArgPrefixes = []string{
	"-cp ",
	"-classpath ",
	"-Djava.class.path=",
	"-Djava.library.path=",
}

// pathLikePlaceholderKeys 是需要按「路径脱敏」处理的占位符键。
//
// 用于区分「可安全解析」与「必须脱敏展示」：路径类解析值进入参数，
// 但在预览 DTO 中只以类别形态出现。
var pathLikePlaceholderKeys = map[string]bool{
	"natives_directory": true,
	"classpath":         true,
	"library_directory": true,
	"game_directory":    true,
	"assets_root":       true,
	"game_assets":       true,
}

// argumentCategory 表示参数所属类别，用于来源标注与展示。
type argumentCategory int

const (
	// categoryJVM 表示 JVM 参数。
	categoryJVM argumentCategory = iota

	// categoryGame 表示游戏参数。
	categoryGame
)

// buildContext 是构建 LaunchPlan 所需的上下文。
//
// 由 `buildLaunchPlan` 组装；集中在一处便于替换表各项注明来源。
type buildContext struct {
	// gameRoot 为已归一化的游戏根目录（绝对路径，内部使用）。
	gameRoot string

	// versionID 为版本 id。
	versionID string

	// versionType 为版本类型（release / snapshot 等），供 `${version_type}`。
	versionType string

	// mainClass 为游戏主类。
	mainClass string

	// assetsIndexID 为资源索引 id（供 `${assets_index_name}`）。
	assetsIndexID string

	// classPathEntries 为已解析出、且确认存在的 classpath 条目绝对路径。
	classPathEntries []string

	// nativesDirectory 为 natives 目录绝对路径；不存在时为空。
	nativesDirectory string

	// nativesPresent 表示 natives 目录是否存在。
	nativesPresent bool
}

// placeholderValue 是替换表中的一项。
type placeholderValue struct {
	// value 为替换后的字面值。
	value string

	// isPath 表示该值是否为路径（用于脱敏展示判断）。
	isPath bool

	// onlineOnly 表示该值仅在联网（真实登录）场景才有意义。
	onlineOnly bool
}

// buildPlaceholderTable 构造占位符替换表。
//
// 每个键的注释说明「来自哪个参数、缺失时的后果」（卡面第 8 条）。
// 该表是本包**唯一**的占位符取值来源：其他文件不得再写一套替换逻辑。
//
// 参数 ctx 为构建上下文。
func buildPlaceholderTable(ctx *buildContext) map[string]placeholderValue {
	table := make(map[string]placeholderValue, 24)

	// 路径类：来自启动器已解析的目录。
	// `${natives_directory}` 缺失后果：`-Djava.library.path` 指向空值，natives 加载失败。
	table["natives_directory"] = placeholderValue{value: ctx.nativesDirectory, isPath: true}
	// `${classpath}` 缺失后果：游戏启动后立即找不到主类。
	table["classpath"] = placeholderValue{value: buildClassPathString(ctx.classPathEntries), isPath: true}
	// `${library_directory}` 被少数旧版 JVM 参数使用；指向 libraries 目录（不校验存在性）。
	table["library_directory"] = placeholderValue{
		value:  joinGamePath(ctx.gameRoot, dirLibraries),
		isPath: true,
	}
	// `${game_directory}` 无论缺失后果：游戏找不到存档与配置，表现为「全新安装」。
	table["game_directory"] = placeholderValue{value: ctx.gameRoot, isPath: true}
	// `${assets_root}` 缺失后果：游戏无声音与语言文件。
	table["assets_root"] = placeholderValue{value: joinGamePath(ctx.gameRoot, dirAssets), isPath: true}
	// `${game_assets}` 是旧版（1.7 之前）写法，语义等同 assets_root。
	table["game_assets"] = placeholderValue{value: joinGamePath(ctx.gameRoot, dirAssets), isPath: true}

	// 分隔符类：由平台决定，不写死字面量。
	// `${classpath_separator}` 缺失后果：classpath 无法被正确切分。
	table["classpath_separator"] = placeholderValue{value: string(os.PathListSeparator)}

	// 版本与启动器元信息。
	// `${version_name}` 缺失后果：游戏无法确定自身版本，部分版本会直接崩溃。
	table["version_name"] = placeholderValue{value: ctx.versionID}
	// `${version_type}` 缺失后果：影响旧版启动器的资源与语言判定。
	table["version_type"] = placeholderValue{value: ctx.versionType}
	// `${assets_index_name}` 缺失后果：游戏找不到资源索引，无声音与语言。
	table["assets_index_name"] = placeholderValue{value: ctx.assetsIndexID}
	// `${launcher_name}` / `${launcher_version}` 仅用于客户端自报身份，缺失无功能后果。
	table["launcher_name"] = placeholderValue{value: launcherName}
	table["launcher_version"] = placeholderValue{value: launcherVersion}

	// 身份类（D-5 离线占位）：这三项**永远**是占位值，不是真实凭证。
	// `${auth_player_name}` 缺失后果：游戏无法确定玩家名。
	table["auth_player_name"] = placeholderValue{value: placeholderUsername}
	// `${auth_uuid}` 缺失后果：部分版本拒绝启动（无法生成玩家档案）。
	table["auth_uuid"] = placeholderValue{value: placeholderUUID}
	// `${auth_access_token}` 缺失后果：联网校验失败；离线占位下游戏不发起联网校验。
	table["auth_access_token"] = placeholderValue{value: placeholderAccessToken}
	// `${user_type}` 缺失后果：旧版会按在线身份处理并尝试联网校验。
	table["user_type"] = placeholderValue{value: placeholderUserType}
	// `${user_properties}` 是旧版常见占位（玩家皮肤等），P2 以空 JSON 对象表示「无属性」。
	table["user_properties"] = placeholderValue{value: "{}"}
	// `${clientid}` / `${auth_xuid}` 是 1.20.5+ 新增的身份参数（客户端 ID 与 Xbox 用户 ID）。
	// 离线占位身份下没有真实值，官方启动器在这两个参数缺省时同样能启动；
	// 给固定占位值是为了让整个参数得以保留，而不是被当成「无法解析」而阻塞启动（D-19）。
	table["clientid"] = placeholderValue{value: placeholderClientID, onlineOnly: true}
	table["auth_xuid"] = placeholderValue{value: placeholderClientID, onlineOnly: true}

	// 分辨率类（D-13 补强，2026-11-01）：P2 注入默认分辨率，使引用这两个占位符的
	// 版本可以启动，而不再落入 `unresolvedPlaceholders`。
	//
	// 取值 854×480 是 Minecraft 官方默认窗口尺寸；`features.has_custom_resolution`
	// 同步视为 `true`（见 `featureEnabled`），因为规则里引用分辨率的前提就是该特性成立。
	// 用户可通过 `additionalGameArgs` 的 `--width` / `--height` 覆盖（不列入保护参数）。
	table["resolution_width"] = placeholderValue{value: strconv.Itoa(defaultResolutionWidth)}
	table["resolution_height"] = placeholderValue{value: strconv.Itoa(defaultResolutionHeight)}

	return table
}

// unresolvedPlaceholderReasons 中记录「已知但本包无法解析」的占位符原因。
//
// 注意：分辨率两项**已不再**属于此列（D-13 补强后由默认值解析成功）。
// 保留本表是为了将来出现新的「已知但无上下文」占位符时不必改调用点。
var unresolvedPlaceholderReasons = map[string]string{}

// joinGamePath 把相对路径拼到游戏根下（仅用于构造展示或非校验路径）。
//
// 与 `resolveInside` 的区别：这里拼出的是**已知固定子目录名**（libraries、assets），
// 不涉及用户输入，因此不需要边界校验；仍需注意它不校验目录是否存在。
func joinGamePath(root, relative string) string {
	if root == "" {
		return ""
	}
	return filepath.Join(root, relative)
}

// buildClassPathString 把 classpath 条目拼接为单个字符串（供 `${classpath}` 替换）。
//
// 说明：这是**唯一**需要「把多个路径合并成一个字符串」的地方——
// classpath 在 JVM 语义上就是单参数、以 `os.PathListSeparator` 分隔，
// 它不会被交给 shell，也不构成「命令行拼接」。该结果只作为**一个**参数元素的值。
//
// 顺序：客户端 jar 在前，随后按 libraries 声明顺序追加已存在的库（卡面第 4 条）。
// 理由：一并说明在 `buildLaunchPlan` 的注释里。
func buildClassPathString(entries []string) string {
	if len(entries) == 0 {
		return ""
	}
	return strings.Join(entries, string(os.PathListSeparator))
}

// splitLegacyArguments 按空白字符切分旧式 `minecraftArguments`。
//
// **非 shell 词法**（卡面第 2 条要求，原因与局限必须写明）：
//   - 只按 Unicode 空白切分（空格、制表符等），连续空白视为一个分隔；
//   - **不处理引号**：`"` 与 `'` 被当作普通字符保留在参数里；
//   - **不处理转义**：`\"` 不会被识别为转义引号；
//   - **不处理管道/重定向/变量展开**：`|`、`>`、`$` 都是普通字符。
//
// 为什么这样设计：`minecraftArguments` 由官方启动器生成，其格式约定是
// 「空格分隔、不含引号」（含空格的取值会由占位符替换后再切分，且在真实版本中
// 需要空格的值（如 `--username` 的名字）不含空格）。实现 shell 词法会引入
// 一整套不必要的解析风险，且与「参数是数组」的设计目标冲突。
//
// 局限：若某个旧版本的 `minecraftArguments` 真的含带空格的引号包裹参数，
// 本函数会把它切成多段。这种情况在 P2 支持的版本范围内不出现，
// 若将来遇到，应在 `warnings` 中暴露而不是靠猜。
//
// 参数 raw 为原始字符串；返回切分后的参数数组（可能为空）。
func splitLegacyArguments(raw string) []string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}

	parts := make([]string, 0, 32)
	var current strings.Builder

	for _, char := range trimmed {
		if unicode.IsSpace(char) {
			if current.Len() > 0 {
				parts = append(parts, current.String())
				current.Reset()
			}
			continue
		}
		current.WriteRune(char)
	}
	if current.Len() > 0 {
		parts = append(parts, current.String())
	}

	return parts
}

// argumentElement 是 `arguments.jvm` / `arguments.game` 中的一个元素。
//
// 现代格式允许元素是字符串，或是 `{ "rules": [...], "value": "..." }` 对象。
// 带 rules 的对象元素需要按 D-12 做**最小规则判断**（见 `evaluateElementRules`）。
type argumentElement struct {
	// Value 为字符串元素的值；对象元素时为 `value` 字段解析结果。
	Value string

	// IsString 表示该元素是否为纯字符串元素。
	IsString bool

	// HasRules 表示对象元素是否带 rules（需要平台/特性判断）。
	HasRules bool

	// Rules 为原始 rules 数组（仅 HasRules=true 时有效）。
	//
	// 保留原始 JSON 而不是预先解析成结构：规则字段是开放集合
	// （`os.version` 等 P2 不支持的条件也要能被识别出来并记 warning），
	// 预先解析会丢掉未知条件。
	Rules []json.RawMessage

	// Raw 为对象元素的原始 JSON（仅在 IsString=false 时有效）。
	Raw string
}

// decodeArgumentElements 把 `[]json.RawMessage` 解析为结构化元素。
//
// 同时支持两种元素形态：
//   - 纯字符串 → 直接取值；
//   - 对象 → 取 `value`（可能是字符串或字符串数组）与 `rules`（保留原始 JSON）。
//
// 参数 raw 为版本 JSON 中的参数数组。
// 返回解析出的元素列表；无法识别的元素会被保留为「非字符串」并带原始 JSON。
func decodeArgumentElements(raw []json.RawMessage) []argumentElement {
	result := make([]argumentElement, 0, len(raw))

	for _, item := range raw {
		text := strings.TrimSpace(string(item))
		if text == "" {
			continue
		}

		// 先尝试纯字符串。
		var asString string
		if err := json.Unmarshal(item, &asString); err == nil {
			result = append(result, argumentElement{Value: asString, IsString: true})
			continue
		}

		// 再尝试对象形态。
		var asObject struct {
			Value json.RawMessage   `json:"value"`
			Rules []json.RawMessage `json:"rules"`
		}
		if err := json.Unmarshal(item, &asObject); err == nil {
			element := argumentElement{
				HasRules: len(asObject.Rules) > 0,
				Rules:    asObject.Rules,
				Raw:      text,
			}

			// value 可能是字符串或字符串数组，两种都支持。
			if len(asObject.Value) > 0 {
				var single string
				if err := json.Unmarshal(asObject.Value, &single); err == nil {
					element.Value = single
				} else {
					var many []string
					if err := json.Unmarshal(asObject.Value, &many); err == nil && len(many) > 0 {
						// 多值元素按数组展开：每个值是一个独立参数（JVM 语义如此）。
						// 每个展开项都继承同一组 rules：规则判定作用于整组值。
						for _, one := range many {
							result = append(result, argumentElement{
								Value:    one,
								IsString: !element.HasRules,
								HasRules: element.HasRules,
								Rules:    element.Rules,
								Raw:      text,
							})
						}
						continue
					}
				}
			}

			result = append(result, element)
			continue
		}

		// 完全无法识别：保留原始 JSON，由调用方转入未解析清单。
		result = append(result, argumentElement{Raw: text})
	}

	return result
}

// ===== D-12：arguments.rules 最小语义 =====

// ruleEvaluation 是一次规则判定的结果。
type ruleEvaluation struct {
	// keep 表示该参数是否应当保留。
	keep bool

	// warnings 为判定过程中产生的提示（仅 info 级，卡面要求「不得静默丢弃且不留痕」）。
	warnings []ArgumentWarning
}

// ruleCondition 是单条规则里可识别的条件集合。
//
// 字段缺失（nil）表示该条件未被声明；声明了但 P2 无法判定时记 warning。
type ruleCondition struct {
	// Action 为 `allow` 或 `disallow`。
	Action string

	// OSName 为 `os.name` 声明（如 `windows`）。
	OSName string

	// HasOSVersion 表示声明了 `os.version`（P2 不支持，仅用于记 warning）。
	HasOSVersion bool

	// OSArch 为 `os.arch` 声明（如 `x86_64`）。
	OSArch string

	// Features 为 `features` 声明（如 `is_demo_user`）。
	Features map[string]bool
}

// currentRuntimeOS 返回 Minecraft 规则使用的 OS 名称。
//
// 映射（官方启动器约定）：
//   - Go 的 `windows` → `windows`
//   - Go 的 `darwin`  → `osx`
//   - 其余（`linux` 等）→ 原样返回
func currentRuntimeOS() string {
	switch runtime.GOOS {
	case "windows":
		return "windows"
	case "darwin":
		return "osx"
	default:
		return runtime.GOOS
	}
}

// currentRuntimeArch 返回 Minecraft 规则使用的 CPU 架构名。
//
// 映射（官方启动器约定）：
//   - `amd64` → `x86_64`
//   - `386`   → `x86`
//   - `arm64` → `arm64`
//   - 其余原样返回
func currentRuntimeArch() string {
	switch runtime.GOARCH {
	case "amd64":
		return "x86_64"
	case "386":
		return "x86"
	case "arm64":
		return "arm64"
	default:
		return runtime.GOARCH
	}
}

// featureEnabled 判断某个 feature 在 P2 下是否生效。
//
// 依据 D-13 与 D-12：
//   - `is_demo_user` 恒 `false`（P2 不提供 demo 模式）；
//   - `has_custom_resolution` 恒 `true`（P2 注入默认分辨率 854×480）；
//   - 其余**未知 feature** 一律按 `false`（保守：不因未知特性开启参数）。
func featureEnabled(name string) bool {
	switch name {
	case featureHasCustomResolution:
		return true
	case featureIsDemoUser:
		return false
	default:
		return false
	}
}

// decodeRuleCondition 把单条原始 rule JSON 解析为 ruleCondition。
//
// 参数 raw 为单条 rule 的 JSON。
// 返回解析结果与是否解析成功；解析失败时调用方按「不匹配」处理并记 warning。
func decodeRuleCondition(raw json.RawMessage) (ruleCondition, bool) {
	text := strings.TrimSpace(string(raw))
	if text == "" {
		return ruleCondition{}, false
	}

	var parsed struct {
		Action string `json:"action"`
		OS     *struct {
			Name    string `json:"name"`
			Version string `json:"version"`
			Arch    string `json:"arch"`
		} `json:"os"`
		Features *map[string]bool `json:"features"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return ruleCondition{}, false
	}

	condition := ruleCondition{
		Action: strings.ToLower(strings.TrimSpace(parsed.Action)),
	}

	if parsed.OS != nil {
		condition.OSName = strings.ToLower(strings.TrimSpace(parsed.OS.Name))
		condition.OSArch = strings.ToLower(strings.TrimSpace(parsed.OS.Arch))
		condition.HasOSVersion = strings.TrimSpace(parsed.OS.Version) != ""
	}

	if parsed.Features != nil {
		condition.Features = make(map[string]bool, len(*parsed.Features))
		for key, wanted := range *parsed.Features {
			condition.Features[key] = wanted
		}
	}

	return condition, true
}

// conditionMatches 判断单条规则的条件是否与本机环境匹配。
//
// 匹配语义（D-12）：
//   - 未声明的条件视为「无约束」，视为匹配；
//   - `os.name` / `os.arch` / `features` 可判定，与本机不符即不匹配；
//   - `os.version` **P2 不支持**：视为不匹配，并由调用方记 info warning。
//
// 参数 condition 为解析后的规则；返回是否匹配与该规则是否含不支持的条件。
func conditionMatches(condition ruleCondition) (matched bool, unsupported bool) {
	// `os.version` 无法判定：视为不匹配（保守），并标记 unsupported 以便记 warning。
	if condition.HasOSVersion {
		return false, true
	}

	if condition.OSName != "" && condition.OSName != currentRuntimeOS() {
		return false, false
	}

	if condition.OSArch != "" && condition.OSArch != currentRuntimeArch() {
		return false, false
	}

	// features：每个声明的特性都必须与本机取值一致。
	// 例如 `{"features": {"has_custom_resolution": true}}` 要求该特性为 true。
	for name, wanted := range condition.Features {
		if featureEnabled(name) != wanted {
			return false, false
		}
	}

	return true, false
}

// evaluateElementRules 按 D-12 的最小语义判定某个带 rules 的元素是否保留。
//
// **判定规则（与官方启动器一致）**：
//  1. 任一 `allow` 规则匹配，且**没有** `disallow` 规则匹配 → 保留；
//  2. 否则排除。
//
// 关键点：**必须处理 `disallow`**。若只处理 allow，`disallow` 匹配的参数会被错误保留。
// 若整个 rules 数组里没有 allow 规则（只有 disallow），则按「无 allow 匹配」处理 → 排除；
// 这与官方行为一致（没有 allow 即默认排除）。
//
// 无法判定的条件（`os.version` 等）视为不匹配，并记 info 级 warning。
//
// 参数 rules 为原始 rules 数组；source 为来源标注（用于 warning 文案）。
// 返回判定结果（是否保留 + info 警告）。
func evaluateElementRules(rules []json.RawMessage, source string) ruleEvaluation {
	evaluation := ruleEvaluation{keep: false, warnings: make([]ArgumentWarning, 0, 2)}

	hasAllowMatch := false
	hasDisallowMatch := false
	hasAnyAllowRule := false
	unsupportedSeen := false

	for _, raw := range rules {
		condition, ok := decodeRuleCondition(raw)
		if !ok {
			// 规则本身无法解析：视为不匹配，并记 info（不静默丢弃）。
			evaluation.warnings = append(evaluation.warnings, ArgumentWarning{
				Code:     ErrorCodeLaunchArgs,
				Message:  "参数规则无法解析，已按「不匹配」处理（来源：" + source + "）。",
				Severity: warningSeverityInfo,
			})
			continue
		}

		action := condition.Action
		if action != ruleActionAllow && action != ruleActionDisallow {
			evaluation.warnings = append(evaluation.warnings, ArgumentWarning{
				Code:     ErrorCodeLaunchArgs,
				Message:  "参数规则的 action 不受支持（" + condition.Action + "），已按「不匹配」处理（来源：" + source + "）。",
				Severity: warningSeverityInfo,
			})
			continue
		}

		if action == ruleActionAllow {
			hasAnyAllowRule = true
		}

		matched, unsupported := conditionMatches(condition)
		if unsupported && !unsupportedSeen {
			// 只记一次，避免同一元素多条规则刷屏。
			unsupportedSeen = true
			evaluation.warnings = append(evaluation.warnings, ArgumentWarning{
				Code:     ErrorCodeLaunchArgs,
				Message:  "参数规则含 P2 不支持的条件（如 os.version），已按「不匹配」处理（来源：" + source + "）。",
				Severity: warningSeverityInfo,
			})
		}

		if !matched {
			continue
		}

		switch action {
		case ruleActionAllow:
			hasAllowMatch = true
		case ruleActionDisallow:
			hasDisallowMatch = true
		}
	}

	// 没有 allow 规则时默认排除：官方语义是「无 allow 即不生效」。
	if !hasAnyAllowRule {
		return evaluation
	}

	// disallow 优先：任一 disallow 匹配即排除。
	if hasDisallowMatch {
		evaluation.keep = false
		return evaluation
	}

	evaluation.keep = hasAllowMatch
	return evaluation
}

// expandArguments 把参数元素展开为带来源标注的 ArgumentPart 列表。
//
// 逐项处理顺序（顺序本身有正确性要求）：
//  1. **规则判定**（D-12）：带 rules 的元素先判断是否保留，排除项直接跳过；
//  2. **值可用性**：非字符串且无值的元素跳过并记 warning；
//  3. **占位符替换**：替换失败的项进入 `unresolved` 列表（不写入参数）。
//
// 参数 elements 为解析出的元素；source 为来源标注（`arguments.jvm` 等）；
// table 为占位符替换表；warnings 与 unresolved 由调用方持有并追加。
//
// 返回写入结果（已保留并解析的参数）与更新后的 warning/unresolved 列表。
func expandArguments(
	elements []argumentElement,
	source string,
	table map[string]placeholderValue,
	warnings []ArgumentWarning,
	unresolved []UnresolvedPlaceholder,
) ([]ArgumentPart, []ArgumentWarning, []UnresolvedPlaceholder) {
	parts := make([]ArgumentPart, 0, len(elements))

	for _, element := range elements {
		// 第 1 步：规则判定（D-12 最小语义，含 disallow）。
		// 被排除的元素**不产生 warning**：这是规则匹配的**正常**结果，
		// 例如「只适用于 macOS 的参数在 Windows 上被排除」，不是异常。
		if element.HasRules {
			evaluation := evaluateElementRules(element.Rules, source)
			warnings = append(warnings, evaluation.warnings...)
			if !evaluation.keep {
				continue
			}
		}

		// 第 2 步：值可用性。
		if !element.IsString && element.Value == "" {
			warnings = append(warnings, ArgumentWarning{
				Code:     ErrorCodeLaunchArgs,
				Message:  "参数元素无法解析为字符串，已跳过该元素（来源：" + source + "）。",
				Severity: warningSeverityWarn,
			})
			continue
		}

		// 第 3 步：占位符替换。
		expanded, failed := replacePlaceholders(element.Value, table)
		if len(failed) > 0 {
			// 未解析占位符：整项不写入参数（避免产生残缺值交给 JVM）。
			for _, placeholder := range failed {
				unresolved = append(unresolved, UnresolvedPlaceholder{
					Placeholder: placeholder,
					Source:      source,
					Reason:      unresolvedReasonFor(placeholder),
				})
				warnings = append(warnings, ArgumentWarning{
					Code:     ErrorCodeLaunchArgs,
					Message:  "参数中的占位符 " + placeholder + " 无法解析，已跳过该参数（来源：" + source + "）。",
					Severity: warningSeverityBlocking,
				})
			}
			continue
		}

		parts = append(parts, ArgumentPart{
			Value:  expanded,
			Source: source,
		})
	}

	return parts, warnings, unresolved
}

// unresolvedReasonFor 返回某个占位符无法解析的原因文案。
//
// 参数 placeholder 为完整占位符文本（含 `${}`）。
func unresolvedReasonFor(placeholder string) string {
	key := strings.TrimSuffix(strings.TrimPrefix(placeholder, "${"), "}")
	if reason, ok := unresolvedPlaceholderReasons[key]; ok {
		return reason
	}
	return "P2 未提供该占位符的取值上下文。"
}

// replacePlaceholders 在单个参数值内执行占位符替换。
//
// 返回替换后的值与未被解析的占位符列表（按出现顺序、去重）。
//
// 关键语义（卡面第 3 条）：无法解析的占位符**不替换为空串**，
// 而是原样报告给调用方，由调用方跳过整个参数并记 blocking 警告。
// 因为空值会让 `--width ` 这类参数变成残缺形式，JVM 侧表现异常且难排查。
func replacePlaceholders(value string, table map[string]placeholderValue) (string, []string) {
	if !strings.Contains(value, "${") {
		return value, nil
	}

	unresolved := make([]string, 0, 2)
	var result strings.Builder
	result.Grow(len(value))

	remaining := value
	for {
		start := strings.Index(remaining, "${")
		if start < 0 {
			result.WriteString(remaining)
			break
		}

		// 写入占位符之前的字面内容。
		result.WriteString(remaining[:start])

		end := strings.Index(remaining[start:], "}")
		if end < 0 {
			// 没有闭合 `}`：把剩余内容原样保留（不视为占位符）。
			result.WriteString(remaining[start:])
			break
		}

		placeholder := remaining[start : start+end+1]
		key := placeholder[2 : len(placeholder)-1]

		entry, ok := table[key]
		if !ok {
			// 未知占位符：记录并原样保留（调用方会因 unresolved 非空而跳过整个参数）。
			unresolved = appendUnique(unresolved, placeholder)
			result.WriteString(placeholder)
			remaining = remaining[start+end+1:]
			continue
		}

		// 已解析：写入解析值。
		result.WriteString(entry.value)
		remaining = remaining[start+end+1:]
	}

	// 去掉重复项。
	return result.String(), unresolved
}

// appendUnique 把值追加到切片（若尚未存在）。
//
// 参数 list 为现有列表；value 为待追加值。
func appendUnique(list []string, value string) []string {
	for _, existing := range list {
		if existing == value {
			return list
		}
	}
	return append(list, value)
}

// validateAdditionalArguments 校验附加参数（`additionalJvmArgs` / `additionalGameArgs`）。
//
// 逐项校验（卡面第 6 条）：
//   - 拒绝含换行、回车、NUL 的项（防止注入多条参数或截断）；
//   - 拒绝含明显 shell 元字符的项（`|`、`&`、`>`、`<`、“ ` “、`$(`）；
//   - 拒绝空项（空参数会被 JVM 当作独立参数，行为不可预期）。
//
// 附加游戏参数额外检查：**不得覆盖受保护的游戏参数键**（D-5 冻结）。
// 覆盖判定按「项是否以 `--键=值` 或 `--键` 开头」实现：命中即拒绝整批。
//
// 参数 additional 为附加参数数组；category 为所属类别。
// 返回错误（命中任一条即返回）；通过时返回 nil。
//
// 注意：返回错误而非警告——附加参数来自前端输入，属于用户输入错误，
// 应明确拒绝并让用户修正，而不是静默丢弃某一项。
func validateAdditionalArguments(additional []string, category argumentCategory) *Error {
	for _, item := range additional {
		if strings.TrimSpace(item) == "" {
			return newInputError(ErrorCodeLaunchArgs, messageAdditionalArgEmpty)
		}
		if strings.ContainsAny(item, "\n\r\x00") {
			return newInputError(ErrorCodeLaunchArgs, messageAdditionalArgControlChar)
		}
		if strings.ContainsAny(item, "|&<>`") {
			return newInputError(ErrorCodeLaunchArgs, messageAdditionalArgShellMeta)
		}
		if strings.Contains(item, "$(") {
			return newInputError(ErrorCodeLaunchArgs, messageAdditionalArgShellMeta)
		}

		if category == categoryGame {
			if key, blocked := findProtectedGameArgKey(item); blocked {
				return newInputError(ErrorCodeLaunchArgs,
					fmt.Sprintf(messageAdditionalArgOverridesProtected, key))
			}
		}
		if category == categoryJVM {
			for _, prefix := range protectedJVMArgPrefixes {
				if strings.HasPrefix(item, prefix) || item == strings.TrimSpace(prefix) {
					return newInputError(ErrorCodeLaunchArgs, messageAdditionalArgOverridesJVM)
				}
			}
		}
	}
	return nil
}

// findProtectedGameArgKey 判断附加游戏参数是否试图设置受保护的键。
//
// 判定覆盖三种写法：`--username=Player`、`--username Player`、`--username`（单独出现）。
// 返回命中的键与是否命中。
//
// 参数 item 为单个附加参数项。
func findProtectedGameArgKey(item string) (string, bool) {
	trimmed := strings.TrimSpace(item)

	// `--key=value` 形态。
	if index := strings.Index(trimmed, "="); index > 0 {
		candidate := trimmed[:index]
		if protectedGameArgKeys[candidate] {
			return candidate, true
		}
		// 也检查以空格分隔的 `--key value` 形态的前半段。
	}

	// `--key value` 形态：取第一个空白前的部分。
	fields := strings.Fields(trimmed)
	if len(fields) > 0 && protectedGameArgKeys[fields[0]] {
		return fields[0], true
	}

	return "", false
}

// 附加参数与身份相关的提示文案。
const (
	// messageAdditionalArgEmpty 表示附加参数为空项。
	messageAdditionalArgEmpty = "附加参数不能为空项。"

	// messageAdditionalArgControlChar 表示附加参数含控制字符。
	messageAdditionalArgControlChar = "附加参数不能包含换行或 NUL 字符。"

	// messageAdditionalArgShellMeta 表示附加参数含 shell 元字符。
	messageAdditionalArgShellMeta = "附加参数不能包含 shell 元字符（| & < > ` $( ）。"

	// messageAdditionalArgOverridesProtected 是覆盖受保护游戏参数的提示模板。
	messageAdditionalArgOverridesProtected = "附加参数不允许覆盖受保护的启动参数 %s（离线身份与版本参数由启动器负责）。"

	// messageAdditionalArgOverridesJVM 表示附加 JVM 参数试图覆盖 classpath 或 natives 路径。
	messageAdditionalArgOverridesJVM = "附加 JVM 参数不允许覆盖 classpath 或 java.library.path。"

	// messageClassPathEmpty 表示 classpath 为空（无法启动）。
	messageClassPathEmpty = "classpath 为空，无法启动游戏（客户端 JAR 与依赖库均未就绪）。"

	// messageArgsTooLong 是参数总长度超限的提示模板。
	messageArgsTooLong = "启动参数总长度 %d 字节，超过上限 %d 字节。"

	// messageNativesMissing 表示 natives 目录缺失（blocking）。
	messageNativesMissing = "未找到 natives 目录，游戏将无法加载本地库；P2 不解压，需由后续阶段补齐。"

	// messageLibrariesMissing 是缺失库数量的提示模板。
	messageLibrariesMissing = "有 %d 个依赖库缺失，未加入 classpath。"

	// messageOfflineIdentity 是离线占位身份的固定提示（D-5 要求 UI 明示）。
	messageOfflineIdentity = "当前使用离线占位身份（非真实登录），账号认证属后续阶段。"

	// messageDefaultResolution 是默认分辨率的固定提示（D-13 要求 info 级说明）。
	messageDefaultResolution = "使用默认分辨率 854×480（可通过附加参数 --width / --height 覆盖）。"

	// messageResolutionInvalid 是 --width / --height 取值非法的提示模板。
	messageResolutionInvalid = "附加参数 %s 的取值必须是正整数且不超过 %d。"

	// messageResolutionMissingValue 表示 --width / --height 缺少取值。
	messageResolutionMissingValue = "附加参数 %s 缺少取值。"
)

// buildLaunchPlan 构建 LaunchPlan。
//
// 参数 root 为已归一化的游戏根；merged 为 P2-03 的继承合并结果；
// additionalJvmArgs / additionalGameArgs 为可选附加参数（来自前端，需逐项校验）。
//
// 失败情形（返回 *Error）：
//   - classpath 为空 → `LAUNCH_ARGS_001`；
//   - 附加参数非法 → `LAUNCH_ARGS_001`；
//   - 参数总长度超限 → `LAUNCH_ARGS_002`。
//
// 成功情形也可能 `SafeToLaunch=false`：存在未解析占位符或 blocking 警告时，
// 计划本身可展示，但不应交给进程层执行。
//
// 只读：不创建目录、不写文件、不解压。
func buildLaunchPlan(root, versionID string, merged *mergedVersion, additionalJvmArgs, additionalGameArgs []string) (*LaunchPlan, *Error) {
	if merged == nil {
		return nil, newInputError(ErrorCodeLaunchArgs, messageVersionJSONMissing)
	}
	if strings.TrimSpace(merged.MainClass) == "" {
		return nil, newInputError(ErrorCodeLaunchArgs, messageVersionMainClassMissing)
	}

	warnings := make([]ArgumentWarning, 0, 6)
	unresolved := make([]UnresolvedPlaceholder, 0, 4)

	// 1. classpath：客户端 JAR 在前，其后按 libraries 声明顺序追加**已存在**的库。
	//
	// 顺序理由（卡面第 4 条）：Minecraft 的客户端 JAR 含主类与游戏核心代码，
	// 而库文件是它的依赖；把客户端 JAR 放在最前可以让 JVM 优先解析主类，
	// 同时与官方启动器行为一致（官方同样把 client jar 放在首位）。
	// 另一层原因：部分库与客户端 JAR 有同名类，客户端 JAR 优先可避免被旧库覆盖。
	classPathEntries := make([]string, 0, len(merged.Libraries)+2)
	missingLibraries := make([]string, 0, 8)

	// 客户端 JAR：按 `jar` 优先、`id` 回退的标识定位；两个位置都尝试。
	clientJarPath := ""
	jarRelative := dirVersions + fileSeparatorSlash + merged.ID + fileSeparatorSlash + merged.ClientJarID + ".jar"
	if absolute, resolveErr := resolveInside(root, jarRelative); resolveErr == nil {
		if isRegularNonEmptyFile(absolute) {
			clientJarPath = absolute
		}
	}
	if clientJarPath == "" && merged.ClientJarID != merged.ID {
		altRelative := dirVersions + fileSeparatorSlash + merged.ID + fileSeparatorSlash + merged.ID + ".jar"
		if absolute, resolveErr := resolveInside(root, altRelative); resolveErr == nil {
			if isRegularNonEmptyFile(absolute) {
				clientJarPath = absolute
			}
		}
	}
	if clientJarPath != "" {
		classPathEntries = append(classPathEntries, clientJarPath)
	}

	// 依赖库：只加入已存在的文件；缺失的记入 warning（相对路径摘要，不含绝对路径）。
	for _, library := range merged.Libraries {
		// 先按 rules 过滤掉不适用于当前平台的库（其他平台的 native 变体等）。
		// 不过滤会把它们算成「缺失」，在 Windows 上产生大量无关警告并阻塞启动。
		if !libraryAppliesToCurrentPlatform(library) {
			continue
		}
		relative := libraryRelativePath(library)
		if relative == "" {
			continue
		}
		absolute, resolveErr := resolveInside(root, relative)
		if resolveErr != nil {
			continue
		}
		if isRegularNonEmptyFile(absolute) {
			classPathEntries = append(classPathEntries, absolute)
			continue
		}
		missingLibraries = append(missingLibraries, relative)
	}

	// classpath 为空即无法启动（连客户端 JAR 都没有）。
	if len(classPathEntries) == 0 {
		return nil, newInputError(ErrorCodeLaunchArgs, messageClassPathEmpty)
	}

	if len(missingLibraries) > 0 {
		warnings = append(warnings, ArgumentWarning{
			Code:     ErrorCodeLaunchArgs,
			Message:  fmt.Sprintf(messageLibrariesMissing, len(missingLibraries)),
			Severity: warningSeverityBlocking,
		})
	}

	// 2. natives 目录：仅当版本**声明了** natives 时才检查存在性（D-10，不解压、不创建）。
	//
	// 关键点：现代版本（1.19+）不再使用 natives 目录，libraries 里没有 natives 声明。
	// 若不加这个前提就无条件检查 `versions/<id>/natives`，会把这类完全健康的版本
	// 误判为「缺 natives → 不可启动」。因此只有 `libraryHasNatives` 为真时才检查。
	//
	// `${natives_directory}` **必须始终非空**：版本 JSON 的 JVM 参数里普遍写成
	// `-Djava.library.path=${natives_directory}`、`-Djna.tmpdir=${natives_directory}` 等，
	// 值为空串会拼出 `/jna`、`/lwjgl` 这类指向盘符根目录的路径，比指向一个不存在的
	// 常规目录更糟。因此默认给 `<游戏目录>/natives`（不创建、不解压），
	// 声明了 natives 且目录存在时再改用版本目录下的绝对路径。
	nativesDir := joinGamePath(root, dirNatives)
	needsNatives := false
	for _, library := range merged.Libraries {
		// 同样先按 rules 过滤：其他平台的 natives 库不该要求本平台具备。
		if !libraryAppliesToCurrentPlatform(library) {
			continue
		}
		if libraryHasNatives(library) {
			needsNatives = true
			break
		}
	}
	if needsNatives {
		if present := nativesPresent(root, merged.ID); present {
			absolute, resolveErr := resolveInside(root, dirVersions+fileSeparatorSlash+merged.ID+fileSeparatorSlash+dirNatives)
			if resolveErr == nil {
				nativesDir = absolute
			}
		} else {
			warnings = append(warnings, ArgumentWarning{
				Code:     ErrorCodeLaunchArgs,
				Message:  messageNativesMissing,
				Severity: warningSeverityBlocking,
			})
		}
	}

	// 3. 构建上下文与占位符替换表。
	ctx := &buildContext{
		gameRoot:         root,
		versionID:        merged.ID,
		versionType:      merged.Type,
		mainClass:        merged.MainClass,
		assetsIndexID:    merged.AssetIndexID,
		classPathEntries: classPathEntries,
		nativesDirectory: nativesDir,
		nativesPresent:   nativesDir != "",
	}
	table := buildPlaceholderTable(ctx)

	// 4. JVM 参数：新格式 `arguments.jvm`；旧格式无 JVM 参数（JVM 参数由启动器自身提供）。
	jvmParts, warnings, unresolved := expandArguments(
		decodeArgumentElements(merged.JVMMajorArguments), argumentSourceArgumentsJVM, table, warnings, unresolved)

	// 5. 游戏参数：新格式优先；旧格式仅在没有任何新格式游戏参数时使用。
	//
	// 为什么旧格式作为回退而不是叠加：旧版 JSON 只有 `minecraftArguments`，
	// 新版只有 `arguments.game`；两者同时存在时新版才是权威（继承合并已处理父子追加）。
	var gameParts []ArgumentPart
	if len(merged.GameArguments) > 0 {
		gameParts, warnings, unresolved = expandArguments(
			decodeArgumentElements(merged.GameArguments), argumentSourceArgumentsGame, table, warnings, unresolved)
	} else if merged.MinecraftArguments != "" {
		legacy := splitLegacyArguments(merged.MinecraftArguments)
		legacyElements := make([]argumentElement, 0, len(legacy))
		for _, item := range legacy {
			legacyElements = append(legacyElements, argumentElement{Value: item, IsString: true})
		}
		gameParts, warnings, unresolved = expandArguments(
			legacyElements, argumentSourceMinecraftArguments, table, warnings, unresolved)
	}

	// 6. 附加参数：逐项校验后作为独立数组元素追加（不参与字符串拼接）。
	if err := validateAdditionalArguments(additionalJvmArgs, categoryJVM); err != nil {
		return nil, err
	}
	if err := validateAdditionalArguments(additionalGameArgs, categoryGame); err != nil {
		return nil, err
	}
	// D-13：`--width` / `--height` 允许覆盖默认分辨率，但取值必须合法。
	if err := validateResolutionOverrides(additionalGameArgs); err != nil {
		return nil, err
	}
	for _, item := range additionalJvmArgs {
		jvmParts = append(jvmParts, ArgumentPart{Value: item, Source: argumentSourceLauncher})
	}
	for _, item := range additionalGameArgs {
		gameParts = append(gameParts, ArgumentPart{Value: item, Source: argumentSourceLauncher})
	}

	// 7. 离线占位身份提示（D-5 要求 UI 明示非登录）。
	warnings = append(warnings, ArgumentWarning{
		Code:     ErrorCodeLaunchArgs,
		Message:  messageOfflineIdentity,
		Severity: warningSeverityInfo,
	})

	// 7b. 默认分辨率提示（D-13 要求 info 级说明）。
	warnings = append(warnings, ArgumentWarning{
		Code:     ErrorCodeLaunchArgs,
		Message:  messageDefaultResolution,
		Severity: warningSeverityInfo,
	})

	// 8. 参数总长度校验（`LAUNCH_ARGS_002`）。
	totalBytes := totalArgumentBytes(jvmParts) + totalArgumentBytes(gameParts)
	if totalBytes > maxTotalArgumentBytes {
		return nil, newInputError(ErrorCodeLaunchArgsTooLong,
			fmt.Sprintf(messageArgsTooLong, totalBytes, maxTotalArgumentBytes))
	}

	// 9. 组装 LaunchPlan。
	// 安全判定：有未解析占位符，或存在 blocking 级警告时不可启动。
	safeToLaunch := len(unresolved) == 0 && !hasBlockingWarning(warnings)

	plan := &LaunchPlan{
		// LaunchIDPlaceholder 留空：launchId 由 P2-06 分配，本卡不涉及进程。
		LaunchIDPlaceholder:    "",
		WorkingDirectory:       root,
		JVMMajorArgs:           jvmParts,
		GameArguments:          gameParts,
		ClassPathEntries:       classPathEntries,
		MainClass:              merged.MainClass,
		NativesDirectory:       nativesDir,
		AssetsDirectory:        joinGamePath(root, dirAssets),
		AssetIndexID:           merged.AssetIndexID,
		Warnings:               warnings,
		UnresolvedPlaceholders: unresolved,
		SafeToLaunch:           safeToLaunch,
	}

	return plan, nil
}

// totalArgumentBytes 计算参数数组中所有值文本的总字节数。
//
// 参数 parts 为参数数组；返回总字节数（不计算分隔符与引号，仅作长度防线）。
func totalArgumentBytes(parts []ArgumentPart) int {
	total := 0
	for _, part := range parts {
		total += len(part.Value)
	}
	return total
}

// hasBlockingWarning 判断警告列表中是否存在 blocking 级警告。
//
// 参数 warnings 为警告列表；返回 true 表示存在阻断项。
func hasBlockingWarning(warnings []ArgumentWarning) bool {
	for _, warning := range warnings {
		if warning.Severity == warningSeverityBlocking {
			return true
		}
	}
	return false
}

// sortedUnresolvedPlaceholders 返回去重并按字典序排序的未解析占位符文本。
//
// 供预览 DTO 展示：集中列出而不是散落在多条警告里，便于用户一次看清缺什么。
//
// 参数 unresolved 为未解析清单。
func sortedUnresolvedPlaceholders(unresolved []UnresolvedPlaceholder) []string {
	seen := make(map[string]bool, len(unresolved))
	names := make([]string, 0, len(unresolved))
	for _, item := range unresolved {
		if item.Placeholder == "" || seen[item.Placeholder] {
			continue
		}
		seen[item.Placeholder] = true
		names = append(names, item.Placeholder)
	}
	sort.Strings(names)
	return names
}

// buildLaunchPlanPreview 把内部的 LaunchPlan 转换为**面向用户**的预览 DTO。
//
// 安全边界（D-8 + 决策记录第六节）：预览 DTO **不得含未被脱敏的绝对路径**。
// 因此这里做两类脱敏：
//  1. 路径字段（classpath 条目、natives/assets/workingDirectory）→ 类别 + `…` + 末段；
//  2. 参数值中含绝对路径的 → 把路径片段替换为脱敏形态，并置 `Redacted=true`。
//
// 参数 plan 为内部计划（可为 nil，此时返回仅含错误信息的空预览由调用方处理）。
// 参数 versionID 为版本 id，用于展示。
func buildLaunchPlanPreview(plan *LaunchPlan, versionID string) LaunchPlanPreview {
	preview := LaunchPlanPreview{
		VersionID:              versionID,
		ClassPathEntryCount:    0,
		ClassPathEntries:       []string{},
		JVMMajorArgs:           []PreviewArgumentPart{},
		GameArguments:          []PreviewArgumentPart{},
		Warnings:               []ArgumentWarning{},
		UnresolvedPlaceholders: []string{},
		OfflineIdentityNotice:  messageOfflineIdentity,
	}

	if plan == nil {
		preview.SafeToLaunch = false
		return preview
	}

	preview.MainClass = plan.MainClass
	preview.AssetIndexID = plan.AssetIndexID
	preview.SafeToLaunch = plan.SafeToLaunch

	// classpath：只给出数量与脱敏形态，绝不展示完整路径。
	preview.ClassPathEntryCount = len(plan.ClassPathEntries)
	for _, entry := range plan.ClassPathEntries {
		preview.ClassPathEntries = append(preview.ClassPathEntries,
			redactPathForPreview(entry, displayCategoryGame))
	}

	// 目录类字段：脱敏展示。
	preview.NativesDirectoryDisplay = redactPathForPreview(plan.NativesDirectory, displayCategoryGame)
	preview.AssetsDirectoryDisplay = redactPathForPreview(plan.AssetsDirectory, displayCategoryGame)
	preview.WorkingDirectoryDisplay = redactPathForPreview(plan.WorkingDirectory, displayCategoryGame)

	// 参数数组：逐项脱敏。
	preview.JVMMajorArgs = redactArgumentParts(plan.JVMMajorArgs)
	preview.GameArguments = redactArgumentParts(plan.GameArguments)

	// 警告 message 已由生成处保证不含绝对路径（只写数量与相对路径摘要），
	// 这里再做一次文本脱敏兜底，防止将来新增警告时漏改。
	for _, warning := range plan.Warnings {
		preview.Warnings = append(preview.Warnings, ArgumentWarning{
			Code:     warning.Code,
			Message:  RedactPathText(warning.Message),
			Severity: warning.Severity,
		})
	}

	// 未解析占位符集中列出（去重排序）。
	preview.UnresolvedPlaceholders = sortedUnresolvedPlaceholders(plan.UnresolvedPlaceholders)

	return preview
}

// redactArgumentParts 对参数数组逐项做路径脱敏。
//
// 参数 parts 为内部参数数组；返回脱敏后的预览数组。
func redactArgumentParts(parts []ArgumentPart) []PreviewArgumentPart {
	result := make([]PreviewArgumentPart, 0, len(parts))
	for _, part := range parts {
		redactedText, changed := RedactPathInArgument(part.Value)
		result = append(result, PreviewArgumentPart{
			Value:    redactedText,
			Source:   part.Source,
			Redacted: changed,
		})
	}
	return result
}

// redactPathForPreview 把绝对路径转换为脱敏展示形态。
//
// 复用 P2-02 的 `buildGameRootDisplay`（游戏目录口径，末两段）与
// P2-01 的 `buildJavaDisplay`（Java 口径，末三段）。
// 这里统一按「目录类别 + `…` + 末两段」处理：预览里出现的都是游戏目录内的路径，
// 用游戏目录口径即可。
//
// 参数 path 为绝对路径；category 为展示类别名（保留参数以便将来区分 Java 路径）。
// 空路径返回空字符串。
func redactPathForPreview(path string, category string) string {
	if path == "" {
		return ""
	}
	// buildGameRootDisplay 已实现「类别 + 分隔符 + … + 分隔符 + 末两段」。
	_ = category
	return buildGameRootDisplay(path)
}

// RedactPathInArgument 对单个参数值中的绝对路径做脱敏。
//
// 处理的形态：
//   - Windows 盘符路径（`C:\...`、`-Dxxx=C:\...`）；
//   - UNC 路径（`\\server\...`）。
//
// 返回值与「是否发生过替换」标志：后者用于预览 DTO 的 `Redacted` 字段。
//
// 注意：该函数不做通用文本脱敏（那是 internal/logging 的职责），
// 只针对参数值中的路径片段，避免误伤 `-Xmx2G` 这类非路径值。
//
// 参数 value 为单个参数文本。
func RedactPathInArgument(value string) (string, bool) {
	if value == "" {
		return "", false
	}

	// 找到参数值里的第一个路径起点：盘符（`X:\` 或 `X:/`）或 UNC（`\\`）。
	start := -1
	for index := 0; index+2 < len(value); index++ {
		if isDriveLetter(value[index]) && value[index+1] == ':' && (value[index+2] == '\\' || value[index+2] == '/') {
			start = index
			break
		}
	}
	if start < 0 {
		// UNC：`\\` 连续两个反斜杠。
		if index := strings.Index(value, `\\`); index >= 0 {
			start = index
		}
	}
	if start < 0 {
		return value, false
	}

	// 保留参数前缀（例如 `-Djava.library.path=`），只脱敏其后的路径。
	prefix := value[:start]
	pathPart := value[start:]

	// 路径可能带引号包裹；先剥掉尾部引号再脱敏，保持结构对称。
	trailing := ""
	for len(pathPart) > 0 && (pathPart[len(pathPart)-1] == '"' || pathPart[len(pathPart)-1] == '\'') {
		trailing = string(pathPart[len(pathPart)-1]) + trailing
		pathPart = pathPart[:len(pathPart)-1]
	}

	redacted := buildGameRootDisplay(pathPart)
	if redacted == "" {
		redacted = "（已隐藏的路径）"
	}
	return prefix + redacted + trailing, true
}

// isDriveLetter 判断字节是否为盘符字母。
func isDriveLetter(char byte) bool {
	return (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z')
}

// RedactPathText 对任意文本中的绝对路径做脱敏兜底。
//
// 供警告文案与错误信息使用：即使上游已避免写路径，这里仍再兜一次。
//
// 参数 text 为原始文本；返回脱敏后文本。
func RedactPathText(text string) string {
	if text == "" {
		return ""
	}
	redacted, changed := RedactPathInArgument(text)
	if !changed {
		return text
	}
	return redacted
}

// previewLaunchPlan 是「预览启动计划」的编排入口。
//
// 流程：归一化游戏根 → 加载并合并版本 → 构建 LaunchPlan → 转脱敏预览。
//
// 参数 rawGameDirectory 为用户提供的游戏根目录；versionID 为版本 id；
// additionalJvmArgs / additionalGameArgs 为可选附加参数。
//
// 返回预览 DTO；构建失败时通过 ErrorCode/ErrorMessage 表达（预览仍可展示原因）。
func previewLaunchPlan(rawGameDirectory, versionID string, additionalJvmArgs, additionalGameArgs []string) LaunchPlanPreview {
	// 归一化失败：直接返回带错误码的空预览。
	root, warnings, pathErr := normalizeGameRoot(rawGameDirectory)
	if pathErr != nil {
		preview := buildLaunchPlanPreview(nil, versionID)
		preview.SafeToLaunch = false
		preview.Warnings = append(preview.Warnings, ArgumentWarning{
			Code:     pathErr.Code,
			Message:  pathErr.Message,
			Severity: warningSeverityBlocking,
		})
		return preview
	}

	// 版本 id 合法性。
	if !isVersionIDValid(versionID) {
		preview := buildLaunchPlanPreview(nil, versionID)
		preview.SafeToLaunch = false
		preview.Warnings = append(preview.Warnings, ArgumentWarning{
			Code:     ErrorCodeVersionMetadata,
			Message:  messageVersionIDInvalid,
			Severity: warningSeverityBlocking,
		})
		return preview
	}

	// 加载并合并版本（P2-03）。
	outcome := loadMergedVersion(root, versionID)
	if outcome.merged == nil {
		preview := buildLaunchPlanPreview(nil, versionID)
		preview.SafeToLaunch = false
		code := outcome.errorCode
		if code == "" {
			code = ErrorCodeVersionMetadata
		}
		preview.Warnings = append(preview.Warnings, ArgumentWarning{
			Code:     code,
			Message:  outcome.reason,
			Severity: warningSeverityBlocking,
		})
		// 归一化阶段的警告（例如符号链接解析失败）也带上。
		for _, warning := range append(warnings, outcome.warnings...) {
			preview.Warnings = append(preview.Warnings, ArgumentWarning{
				Code:     code,
				Message:  warning,
				Severity: warningSeverityWarn,
			})
		}
		return preview
	}

	// 构建 LaunchPlan。
	plan, planErr := buildLaunchPlan(root, versionID, outcome.merged, additionalJvmArgs, additionalGameArgs)
	if planErr != nil {
		preview := buildLaunchPlanPreview(nil, versionID)
		preview.SafeToLaunch = false
		preview.Warnings = append(preview.Warnings, ArgumentWarning{
			Code:     planErr.Code,
			Message:  planErr.Message,
			Severity: warningSeverityBlocking,
		})
		return preview
	}

	preview := buildLaunchPlanPreview(plan, versionID)
	// 归一化与版本加载阶段的警告并入（已脱敏）。
	for _, warning := range append(warnings, outcome.warnings...) {
		preview.Warnings = append(preview.Warnings, ArgumentWarning{
			Code:     ErrorCodeLaunchArgs,
			Message:  RedactPathText(warning),
			Severity: warningSeverityWarn,
		})
	}
	return preview
}

// ===== D-13：默认分辨率的覆盖校验 =====

// resolutionArgKeys 是允许覆盖默认分辨率的游戏参数键。
//
// 按 D-13 明确：`--width` / `--height` **不列入保护参数**，用户可以覆盖。
var resolutionArgKeys = map[string]bool{
	"--width":  true,
	"--height": true,
}

// validateResolutionOverrides 校验附加游戏参数中的 `--width` / `--height` 取值。
//
// 规则（D-13）：
//   - 取值必须是**正整数**；
//   - 取值必须 **≤ maxResolutionValue（16384）**；
//   - 越界或非整数值 → 返回 `LAUNCH_ARGS_001`。
//
// 支持两种写法：`--width=1280` 与 `--width 1280`（后者需要读下一项）。
// 缺少取值（如末尾只有 `--width`）同样返回错误。
//
// 为什么在这里校验而不是在 validateAdditionalArguments 里：
// 该规则只针对两个特定键，且需要读取「下一项」作为取值，
// 与「逐项独立校验」的通用逻辑形态不同，单独成函数更清晰。
//
// 参数 args 为附加游戏参数数组；返回错误（命中即返回）。
func validateResolutionOverrides(args []string) *Error {
	for index := 0; index < len(args); index++ {
		item := strings.TrimSpace(args[index])

		key, inlineValue, hasInline := splitKeyValue(item)
		if !resolutionArgKeys[key] {
			continue
		}

		if hasInline {
			if err := checkResolutionValue(key, inlineValue); err != nil {
				return err
			}
			continue
		}

		// `--width 1280` 形态：取值在下一项。
		if index+1 >= len(args) {
			return newInputError(ErrorCodeLaunchArgs, fmt.Sprintf(messageResolutionMissingValue, key))
		}
		if err := checkResolutionValue(key, strings.TrimSpace(args[index+1])); err != nil {
			return err
		}
		// 跳过已消费的取值项。
		index++
	}
	return nil
}

// checkResolutionValue 校验单个分辨率取值的合法性。
//
// 参数 key 为参数键（用于错误文案）；rawValue 为原始取值文本。
// 返回错误或 nil。
func checkResolutionValue(key string, rawValue string) *Error {
	value, err := strconv.Atoi(rawValue)
	if err != nil || value <= 0 || value > maxResolutionValue {
		return newInputError(ErrorCodeLaunchArgs,
			fmt.Sprintf(messageResolutionInvalid, key, maxResolutionValue))
	}
	return nil
}

// splitKeyValue 把 `--key=value` 拆成键与值。
//
// 参数 item 为单个参数文本。
// 返回值：键、内联值、是否存在内联值。无 `=` 时内联值为空且 hasInline 为 false。
func splitKeyValue(item string) (key string, value string, hasInline bool) {
	if index := strings.Index(item, "="); index > 0 {
		return strings.TrimSpace(item[:index]), strings.TrimSpace(item[index+1:]), true
	}
	return strings.TrimSpace(item), "", false
}
