// 本文件实现本地版本识别：版本枚举、版本 JSON 读取、inheritsFrom 继承合并、
// libraries 存在性检查与可启动性三态判定。
//
// 全程只读（P2-03 卡面第 8 条）：
//   - 所有文件访问都经 `resolveInside(root, relative)`（P2-02 产出的唯一边界函数），
//     本文件不自行拼接路径、不做边界校验；
//   - 不下载、不解压、不补全版本 JSON、不创建目录、不写数据库；
//   - libraries 只 `os.Stat` 存在性，不读 jar 内容、不校验 SHA-1（属 P3）。
//
// 继承（Q-2 已冻结，决策记录「Q-2 生效后的追加冻结」）：
//   - 递归解析父版本 JSON，**递归深度 ≤ 5**；
//   - **必须检测循环引用**，发现即报 `VERSION_METADATA_001`；
//   - `libraries` 按 `name` 去重、子覆盖父；
//   - `arguments` 父在前、子在后追加；
//   - 旧版 `jar` 字段可能与 `id` 不同：客户端 jar 按 `jar` 定位，缺失时回退 `id`。
//
// Natives（D-10 已冻结）：只检查存在性，**不解压、不创建目录**；
// 缺失时把版本判为 `metadata_only`。
package launch

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"time"
)

// 版本识别相关的常量。
const (
	// maxVersionJSONBytes 是单个版本 JSON 的读取上限。
	//
	// 取 8 MiB：真实版本 JSON 最大约 1–2 MiB（大型整合包带大量 libraries 时可能更大），
	// 8 MiB 给足余量；同时限制住「误选到超大文件」把内存与界面拖死的风险。
	// 超限时不解析，直接判 broken 并说明原因。
	maxVersionJSONBytes = 8 * 1024 * 1024

	// maxInheritDepth 是 inheritsFrom 继承链的最大解析层数。
	//
	// 取 5：真实版本的继承链最多 2–3 层（例如 Forge 版本继承原版、OptiFine 再继承 Forge）；
	// 5 层留足余量，又能及时拦住异常深链与环。
	maxInheritDepth = 5

	// maxVersionIDLength 是版本 id 的最大长度。
	//
	// 取 128：版本 id 通常是 `1.21`、`1.7.10-forge-1614` 这类短串，
	// 128 足够覆盖自定义整合包命名，又能拦住异常长名。
	maxVersionIDLength = 128

	// slowThreshold 是「检查耗时过长」的判定阈值。
	//
	// 取 3 秒（卡面第 9 条建议值）：单版本检查应远快于此；
	// 超过说明磁盘极慢或 libraries 条目异常多，结果里带 `slow=true` 提示用户，但不中断。
	slowThreshold = 3 * time.Second

	// fileSeparatorSlash 是 POSIX 分隔符，用于把相对路径统一成正斜杠。
	fileSeparatorSlash = "/"
)

// 版本可启动性三态取值，与接口冻结清单第 4.4 节的 TS 联合类型逐字一致。
const (
	// versionStatusLaunchSupported 表示结构完整、无缺失项，可尝试启动。
	versionStatusLaunchSupported = "launch_supported"

	// versionStatusMetadataOnly 表示元数据可读但缺 jar / 库 / natives，不可启动。
	versionStatusMetadataOnly = "metadata_only"

	// versionStatusUnsupported 表示能识别但格式不受支持（缺关键字段、继承过深等）。
	versionStatusUnsupported = "unsupported"

	// versionStatusBroken 表示 JSON 无法解析或继承环导致无法识别。
	versionStatusBroken = "broken"
)

// 版本相关的错误码与文案。
const (
	// ErrorCodeVersionMetadata 表示版本元数据无效（JSON 无法解析、必需字段缺失、继承环）。
	//
	// 来源：决策记录第五节「复用既有码」与接口冻结清单第 3.2 节：
	// `VERSION_METADATA_001`（版本 JSON 解析失败或继承环）。
	ErrorCodeVersionMetadata = "VERSION_METADATA_001"

	// messageVersionGameRootInvalid 表示游戏根目录本身不可用。
	messageVersionGameRootInvalid = "游戏目录不可用，请先选择有效的游戏目录。"

	// messageVersionDirMissing 表示 versions 目录不存在。
	messageVersionDirMissing = "未找到 versions 目录，无法识别本地版本。"

	// messageVersionDirNotDirectory 表示 versions 存在但不是目录。
	messageVersionDirNotDirectory = "versions 存在但不是目录，无法识别本地版本。"

	// messageVersionIDInvalid 表示版本目录名不合法。
	messageVersionIDInvalid = "版本目录名不合法，已跳过。"

	// messageVersionJSONMissing 表示版本目录内缺少同名 JSON。
	messageVersionJSONMissing = "版本目录内缺少同名 JSON 文件。"

	// messageVersionJSONTooLarge 表示版本 JSON 超过读取上限。
	messageVersionJSONTooLarge = "版本 JSON 文件过大，已拒绝解析。"

	// messageVersionJSONUnreadable 表示版本 JSON 无法读取（权限等）。
	messageVersionJSONUnreadable = "无法读取版本 JSON 文件。"

	// messageVersionJSONInvalid 表示 JSON 语法错误。
	messageVersionJSONInvalid = "版本 JSON 无法解析（语法错误）。"

	// messageVersionMainClassMissing 表示缺少 mainClass。
	messageVersionMainClassMissing = "版本 JSON 缺少 mainClass 字段，无法构造启动命令。"

	// messageVersionInheritTooDeep 表示继承链超过层数上限。
	messageVersionInheritTooDeep = "版本继承链超过 5 层，超出 P2 支持范围。"

	// messageVersionInheritMissingParent 表示父版本不存在。
	messageVersionInheritMissingParent = "继承的父版本不存在或无法读取。"

	// messageVersionInheritCycle 表示继承链出现循环引用。
	messageVersionInheritCycle = "版本继承链存在循环引用，无法解析。"

	// messageVersionInheritSelf 表示版本继承了自身。
	messageVersionInheritSelf = "版本继承了自身（inheritsFrom 指向自己）。"

	// messageVersionIDMismatch 表示 JSON 内的 id 与目录名不一致（仅警告）。
	messageVersionIDMismatch = "版本 JSON 中的 id 与目录名不一致，已按目录名识别。"

	// messageVersionPathEscaped 表示版本内相对路径越界（仅用于跳过单个库）。
	messageVersionPathEscaped = "该库的路径越界，已跳过存在性检查。"
)

// 判定原因文案（面向用户，`reason` 字段）。
const (
	// reasonClientJarMissing 表示缺客户端 JAR。
	reasonClientJarMissing = "缺少客户端 JAR（versions/<id>/<id>.jar）。"

	// reasonLibrariesMissing 表示缺部分依赖库。
	reasonLibrariesMissing = "缺少部分依赖库，无法构造完整 classpath。"

	// reasonNativesMissing 表示缺 natives 目录。
	reasonNativesMissing = "缺少 natives 目录（P2 不解压，需由 P3 下载并解压）。"
)

// 版本 JSON 的结构定义。
//
// 只声明 P2 需要的字段：Minecraft 的版本 JSON 含大量 P2 用不到的字段
// （downloads.server、logging、complianceLevel 等），未知字段由 encoding/json
// 自动忽略，无需声明。
type versionJSON struct {
	// ID 是版本标识。通常与目录名相同；旧版 modpack 可能不同，因此只用于警告比对。
	ID string `json:"id"`

	// Type 是版本类型：release / snapshot / old_beta / old_alpha 等。
	//
	// P2 只做展示与提示，不据此限制可启动性（加载器版本常见自定义 type）。
	Type string `json:"type"`

	// InheritsFrom 是父版本 id。存在时需递归合并父版本。
	InheritsFrom string `json:"inheritsFrom"`

	// Jar 是客户端 JAR 的版本标识。
	//
	// 旧版本（1.6 以前）常见 `jar` 与 `id` 不同（例如 id=1.7.10-forge，jar=1.7.10），
	// 客户端 jar 按 `jar` 定位，缺失时回退 `id`。
	Jar string `json:"jar"`

	// MainClass 是游戏主类，构造启动命令必需。
	MainClass string `json:"mainClass"`

	// Assets 是资源索引标识。
	//
	// P2 只读取用于展示/后续 P2-04 判断，不在本卡校验 assets 索引存在性（卡面遗留问题 3）。
	Assets string `json:"assets"`

	// AssetIndex 是资源索引对象，含 id 字段。
	AssetIndex *assetIndexJSON `json:"assetIndex"`

	// Libraries 是依赖库清单。
	Libraries []libraryJSON `json:"libraries"`

	// Arguments 是 1.13+ 的参数结构（游戏参数与 JVM 参数分开）。
	Arguments *argumentsJSON `json:"arguments"`

	// MinecraftArguments 是 1.12 及更早的单一参数字符串。
	MinecraftArguments string `json:"minecraftArguments"`

	// JavaVersion 声明该版本需要的 Java 大版本。
	JavaVersion *javaVersionJSON `json:"javaVersion"`

	// Downloads 含客户端 JAR 的下载信息。
	Downloads *downloadsJSON `json:"downloads"`
}

// assetIndexJSON 是 assetIndex 子对象。
type assetIndexJSON struct {
	// ID 是资源索引文件名（不含 .json）。
	ID string `json:"id"`
}

// javaVersionJSON 是 javaVersion 子对象。
type javaVersionJSON struct {
	// MajorVersion 是该版本需要的 Java 主版本号（例如 8、17、21）。
	MajorVersion int `json:"majorVersion"`
}

// downloadsJSON 是 downloads 子对象。
type downloadsJSON struct {
	// Client 是客户端 JAR 的下载信息。
	Client *downloadArtifactJSON `json:"client"`
}

// downloadArtifactJSON 是单个下载制品。
//
// P2 只读取 `path` 与 `size`：path 用于定位文件，size 用于判断是否为空文件；
// 不读取 sha1（哈希校验属 P3）。
type downloadArtifactJSON struct {
	// Path 是该制品在 libraries/ 或 versions/ 下的相对路径。
	Path string `json:"path"`

	// Size 是文件字节数；0 或缺省表示未知。
	Size int64 `json:"size"`
}

// libraryJSON 是单个依赖库。
type libraryJSON struct {
	// Name 是 Maven 坐标 `group:artifact:version`，也是去重键。
	Name string `json:"name"`

	// Downloads 含 artifact 与 natives classifiers。
	Downloads *libraryDownloadsJSON `json:"downloads"`

	// Natives 是旧式 natives 声明（按平台映射到 classifier）。
	Natives map[string]string `json:"natives"`

	// Rules 是平台规则（P2 不使用，保留字段以便 P3）。
	Rules json.RawMessage `json:"rules"`
}

// libraryDownloadsJSON 是库的下载信息容器。
type libraryDownloadsJSON struct {
	// Artifact 是主制品（classpath 用）。
	Artifact *downloadArtifactJSON `json:"artifact"`

	// Classifiers 是各类 classifier 制品（含 natives）。
	Classifiers map[string]*downloadArtifactJSON `json:"classifiers"`
}

// argumentsJSON 是 1.13+ 的参数结构。
type argumentsJSON struct {
	// Game 是游戏参数（可能是字符串或条件对象数组，因此用 RawMessage 保留原样）。
	Game []json.RawMessage `json:"game"`

	// JVM 是 JVM 参数（同上）。
	JVM []json.RawMessage `json:"jvm"`
}

// mergedVersion 是继承合并后的版本视图。
//
// 合并结果不落盘、不缓存（卡面遗留问题 5），只在一次检查内使用。
type mergedVersion struct {
	// ID 是最终版本的 id（取子版本的 id）。
	ID string

	// Type 是版本类型（取子版本）。
	Type string

	// MainClass 是主类（取子版本；子版本未声明时取父版本）。
	MainClass string

	// Assets 是资源索引标识（子版本优先）。
	Assets string

	// AssetIndexID 是资源索引 id（子版本优先）。
	AssetIndexID string

	// JavaMajorVersion 是需要的 Java 主版本（子版本优先）。
	JavaMajorVersion int

	// ClientJarID 是客户端 JAR 的定位标识（`jar` 优先，缺失回退 `id`）。
	//
	// 注意：这是「用哪个 jar 文件名」，不是被继承的父版本 id。
	ClientJarID string

	// Libraries 是合并后的依赖库（按 name 去重，子覆盖父）。
	Libraries []libraryJSON

	// GameArguments 是合并后的游戏参数（父在前、子在后）。
	GameArguments []json.RawMessage

	// JVMMajorArguments 是合并后的 JVM 参数（父在前、子在后）。
	JVMMajorArguments []json.RawMessage

	// MinecraftArguments 是旧式单一参数字符串（子优先）。
	MinecraftArguments string

	// InheritChain 为实际走过的继承链（子在前），用于报告与排查。
	InheritChain []string
}

// versionLoadOutcome 是加载单个版本的结果。
type versionLoadOutcome struct {
	// merged 是合并后的版本视图；加载失败时为 nil。
	merged *mergedVersion

	// status 是三态判定（加载阶段可能是 unsupported / broken）。
	status string

	// reason 是加载失败的原因文案；成功时为空。
	reason string

	// warnings 是加载过程中的非致命提示。
	warnings []string

	// errorCode 是加载失败的稳定错误码；成功时为空。
	errorCode string
}

// isVersionIDValid 判断版本目录名是否为合法版本 id。
//
// 规则（卡面第 1 条）：
//   - 字符集 `[A-Za-z0-9._-]`；
//   - 长度 ≤ maxVersionIDLength；
//   - 不得为 `.` / `..`；
//   - 不得含路径分隔符（字符集已保证，这里额外显式拦一次）。
//
// 参数 id 为目录名。
func isVersionIDValid(id string) bool {
	if id == "" || len(id) > maxVersionIDLength {
		return false
	}
	if id == "." || id == duplicateSegment {
		return false
	}
	if strings.ContainsAny(id, `/\`) {
		return false
	}
	for index := 0; index < len(id); index++ {
		char := id[index]
		switch {
		case char >= 'a' && char <= 'z':
		case char >= 'A' && char <= 'Z':
		case char >= '0' && char <= '9':
		case char == '.', char == '_', char == '-':
		default:
			return false
		}
	}
	return true
}

// listVersionDirectories 列举 `versions/` 的一层子目录。
//
// 参数 root 为已归一化的游戏根目录；返回合法 id 列表与被跳过的非法目录名。
//
// 只读一层，不递归、不跟随符号链接目录（`DirEntry.IsDir()` 对符号链接返回 false，
// 天然满足「不跟随」要求）。
func listVersionDirectories(root string) (validIDs []string, skipped []string, err *Error) {
	versionsPath, resolveErr := resolveInside(root, dirVersions)
	if resolveErr != nil {
		// 越界或非法：属于路径问题。
		var launchErr *Error
		if errors.As(resolveErr, &launchErr) {
			return nil, nil, launchErr
		}
		return nil, nil, newInputError(ErrorCodePathInvalid, messageVersionGameRootInvalid)
	}

	info, statErr := os.Stat(versionsPath)
	if statErr != nil {
		if errors.Is(statErr, os.ErrNotExist) {
			// versions 目录不存在：这是目录结构问题，不是版本元数据问题。
			return nil, nil, newInputError(ErrorCodeGameDir, messageVersionDirMissing)
		}
		return nil, nil, newSystemError(ErrorCodeGameDir, messageVersionDirMissing)
	}
	if !info.IsDir() {
		return nil, nil, newInputError(ErrorCodeGameDir, messageVersionDirNotDirectory)
	}

	entries, readErr := os.ReadDir(versionsPath)
	if readErr != nil {
		return nil, nil, newSystemError(ErrorCodeGameDir, messageVersionDirNotDirectory)
	}

	for _, entry := range entries {
		// 只认目录：符号链接与普通文件都跳过。
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !isVersionIDValid(name) {
			skipped = append(skipped, name)
			continue
		}
		validIDs = append(validIDs, name)
	}

	return validIDs, skipped, nil
}

// readVersionJSON 读取并解析单个版本 JSON。
//
// 参数 root 为游戏根；id 为版本 id（已校验合法）。
// 返回解析结果与错误码；错误码为空表示读取与语法解析都成功。
//
// 安全与稳健性：
//   - 路径经 `resolveInside(root, "versions/<id>/<id>.json")` 拼出，不自行拼接；
//   - 先 `Stat` 检查大小，超 `maxVersionJSONBytes` 直接拒绝（防超大文件拖垮）；
//   - 语法错误不 panic，返回 broken。
func readVersionJSON(root, id string) (*versionJSON, string, *Error) {
	relative := dirVersions + fileSeparatorSlash + id + fileSeparatorSlash + id + ".json"

	jsonPath, resolveErr := resolveInside(root, relative)
	if resolveErr != nil {
		var launchErr *Error
		if errors.As(resolveErr, &launchErr) {
			return nil, launchErr.Code, launchErr
		}
		return nil, ErrorCodePathInvalid, newInputError(ErrorCodePathInvalid, messageVersionGameRootInvalid)
	}

	info, statErr := os.Stat(jsonPath)
	if statErr != nil {
		if errors.Is(statErr, os.ErrNotExist) {
			// 目录在但同名 JSON 缺失：记为 broken（可识别目录但内容不完整）。
			return nil, ErrorCodeVersionMetadata, newExternalError(ErrorCodeVersionMetadata, messageVersionJSONMissing)
		}
		return nil, ErrorCodeVersionMetadata, newSystemError(ErrorCodeVersionMetadata, messageVersionJSONUnreadable)
	}

	if info.IsDir() {
		// 同名为目录：同样无法作为版本 JSON 使用。
		return nil, ErrorCodeVersionMetadata, newExternalError(ErrorCodeVersionMetadata, messageVersionJSONMissing)
	}

	if info.Size() > maxVersionJSONBytes {
		return nil, ErrorCodeVersionMetadata, newExternalError(ErrorCodeVersionMetadata, messageVersionJSONTooLarge)
	}

	content, readErr := os.ReadFile(jsonPath)
	if readErr != nil {
		return nil, ErrorCodeVersionMetadata, newSystemError(ErrorCodeVersionMetadata, messageVersionJSONUnreadable)
	}

	var parsed versionJSON
	if unmarshalErr := json.Unmarshal(content, &parsed); unmarshalErr != nil {
		// 语法错误：不返回半结构化对象，直接判 broken。
		return nil, ErrorCodeVersionMetadata, newExternalError(ErrorCodeVersionMetadata, messageVersionJSONInvalid)
	}

	return &parsed, "", nil
}

// loadMergedVersion 解析版本并沿 inheritsFrom 向上合并。
//
// 实现要点（Q-2 冻结规则）：
//   - **递归深度 ≤ maxInheritDepth（5）**：需要读取的版本节点数超过上限即判 unsupported；
//   - **环检测**：用 `visited` 记录已访问的 id，重复出现即判 broken；
//     `inheritsFrom` 指向自身也直接判 broken；
//   - 合并方向：从子版本向父版本逐层向上遍历；**子版本先就位、父版本后合并**，
//     合并时按「子覆盖父（标量）」「去重后追加（集合）」规则处理。
//
// 参数 root 为游戏根；id 为起始版本 id。
func loadMergedVersion(root, id string) versionLoadOutcome {
	outcome := versionLoadOutcome{
		warnings: make([]string, 0, 2),
	}

	// visited 记录继承链上出现过的 id：任何重复都说明有环。
	visited := make(map[string]bool, maxInheritDepth+1)

	// chain 记录实际读取过的 id（子在前），用于报告与排查。
	chain := make([]string, 0, maxInheritDepth+1)

	currentID := id
	var merged *mergedVersion

	// 循环上限 = 最大层数 + 1：允许「1 个子版本 + 5 层父版本」共 6 个节点。
	// 超过即说明链太长，判 unsupported。
	for nodeIndex := 0; nodeIndex <= maxInheritDepth; nodeIndex++ {
		// 环检测：当前 id 已在链上出现过。
		if visited[currentID] {
			outcome.status = versionStatusBroken
			outcome.reason = messageVersionInheritCycle
			outcome.errorCode = ErrorCodeVersionMetadata
			return outcome
		}
		visited[currentID] = true
		chain = append(chain, currentID)

		parsed, code, readErr := readVersionJSON(root, currentID)

		// 读取失败的处理：首层（节点 0）失败 → broken；父版本失败 → unsupported。
		if readErr != nil {
			outcome.errorCode = code
			if nodeIndex == 0 {
				outcome.status = versionStatusBroken
				outcome.reason = readErr.Message
				return outcome
			}
			outcome.status = versionStatusUnsupported
			// 对用户统一说「父版本不存在」，避免引导用户去改父版本的 JSON 内容。
			outcome.reason = messageVersionInheritMissingParent
			// 保留更具体的读取失败原因作为警告，便于排查。
			outcome.warnings = append(outcome.warnings, readErr.Message)
			return outcome
		}

		if nodeIndex == 0 {
			// 首层：建立合并视图；顺带校验 id 一致性（仅警告，不阻断）。
			if parsed.ID != "" && parsed.ID != currentID {
				outcome.warnings = append(outcome.warnings, messageVersionIDMismatch)
			}
			merged = newMergedFromVersion(parsed, currentID)
		} else {
			// 父版本（含更上层）：把父版本合并进已建立的视图。
			mergeParentIntoChild(merged, parsed)
		}

		// 该层没有父版本：继承链到此结束。
		if parsed.InheritsFrom == "" {
			break
		}

		// 自引用检测：inheritsFrom 指向自己。
		if parsed.InheritsFrom == currentID {
			outcome.status = versionStatusBroken
			outcome.reason = messageVersionInheritSelf
			outcome.errorCode = ErrorCodeVersionMetadata
			return outcome
		}

		// 进入下一层：把父版本 id 设为当前 id，下一轮读取它。
		currentID = parsed.InheritsFrom

		// 已经到达节点上限却还有父版本 → 链过长，判 unsupported。
		if nodeIndex == maxInheritDepth {
			outcome.status = versionStatusUnsupported
			outcome.reason = messageVersionInheritTooDeep
			outcome.errorCode = ErrorCodeVersionMetadata
			return outcome
		}
	}

	merged.InheritChain = chain

	// 缺 mainClass：无法构造启动命令，判 unsupported。
	if merged.MainClass == "" {
		outcome.status = versionStatusUnsupported
		outcome.reason = messageVersionMainClassMissing
		outcome.errorCode = ErrorCodeVersionMetadata
		return outcome
	}

	// 加载成功：状态由后续的可启动性判定覆盖。
	outcome.merged = merged
	outcome.status = versionStatusMetadataOnly
	return outcome
}

// newMergedFromVersion 用版本 JSON 建立初始合并视图。
//
// 参数 parsed 为该版本的 JSON；id 为版本 id。
func newMergedFromVersion(parsed *versionJSON, id string) *mergedVersion {
	merged := &mergedVersion{
		ID:        id,
		Type:      parsed.Type,
		MainClass: parsed.MainClass,
		// 客户端 jar 定位：`jar` 优先，缺失回退 `id`（Q-2 冻结规则）。
		ClientJarID:        clientJarIdentifier(parsed, id),
		Libraries:          copyLibraries(parsed.Libraries),
		MinecraftArguments: parsed.MinecraftArguments,
	}

	if parsed.Assets != "" {
		merged.Assets = parsed.Assets
	}
	if parsed.AssetIndex != nil && parsed.AssetIndex.ID != "" {
		merged.AssetIndexID = parsed.AssetIndex.ID
	}
	if parsed.JavaVersion != nil && parsed.JavaVersion.MajorVersion > 0 {
		merged.JavaMajorVersion = parsed.JavaVersion.MajorVersion
	}
	if parsed.Arguments != nil {
		merged.GameArguments = append([]json.RawMessage(nil), parsed.Arguments.Game...)
		merged.JVMMajorArguments = append([]json.RawMessage(nil), parsed.Arguments.JVM...)
	}

	return merged
}

// clientJarIdentifier 返回客户端 JAR 的定位标识。
//
// 规则（Q-2 冻结）：旧版本 `jar` 字段可能与 `id` 不同，优先用 `jar`；
// `jar` 缺失或为空时回退 `id`。
//
// 参数 parsed 为版本 JSON；id 为版本 id。
func clientJarIdentifier(parsed *versionJSON, id string) string {
	if parsed.Jar != "" {
		return parsed.Jar
	}
	return id
}

// copyLibraries 复制库清单，避免后续合并修改到解析结果。
//
// 参数 libraries 为解析出的库列表。
func copyLibraries(libraries []libraryJSON) []libraryJSON {
	if len(libraries) == 0 {
		return nil
	}
	result := make([]libraryJSON, len(libraries))
	copy(result, libraries)
	return result
}

// appendParentCollections 把父版本的 libraries 与 arguments 追加到合并视图。
//
// 顺序语义（Q-2 冻结）：
//   - `libraries` 按 `name` 去重、**子覆盖父**：子版本已有的 name 保留子版本条目，
//     父版本中同名条目被忽略；父版本独有的条目追加到末尾。
//   - `arguments` **父在前、子在后追加**：父版本参数必须排在子版本之前，
//     因为后者可能依赖前者的设置。
//     由于本实现是「子已就位、后追加父」，为保证「父在前」，这里把父参数
//     插到子参数之前（等价于最终顺序 = 父 + 子）。
func appendParentCollections(merged *mergedVersion, parent *versionJSON) {
	if merged == nil || parent == nil {
		return
	}

	// libraries：按 name 去重，子覆盖父。
	existing := make(map[string]int, len(merged.Libraries))
	for index, library := range merged.Libraries {
		if library.Name == "" {
			continue
		}
		existing[library.Name] = index
	}
	for _, parentLibrary := range parent.Libraries {
		if parentLibrary.Name == "" {
			// 无名条目不参与去重，直接追加（保持信息完整）。
			merged.Libraries = append(merged.Libraries, parentLibrary)
			continue
		}
		if _, ok := existing[parentLibrary.Name]; ok {
			// 子版本已有同 name：子覆盖父，跳过父版本条目。
			continue
		}
		merged.Libraries = append(merged.Libraries, parentLibrary)
	}

	// arguments：父在前、子在后。
	if parent.Arguments != nil {
		merged.GameArguments = appendBefore(merged.GameArguments, parent.Arguments.Game)
		merged.JVMMajorArguments = appendBefore(merged.JVMMajorArguments, parent.Arguments.JVM)
	}

	// 旧式 minecraftArguments：子优先，父版本只在子版本为空时补齐。
	if merged.MinecraftArguments == "" && parent.MinecraftArguments != "" {
		merged.MinecraftArguments = parent.MinecraftArguments
	}
}

// appendBefore 把 parent 追加到 current 之前。
//
// 用于实现「父在前、子在后」：本实现中子版本集合先就位，父版本后到，
// 因此需要把父版本插到前面。返回新切片，不修改入参。
func appendBefore(current, parent []json.RawMessage) []json.RawMessage {
	if len(parent) == 0 {
		return current
	}
	combined := make([]json.RawMessage, 0, len(parent)+len(current))
	combined = append(combined, parent...)
	combined = append(combined, current...)
	return combined
}

// fillMissingScalars 用父版本的标量字段补齐子版本的空缺。
//
// 规则：**子版本优先**——子版本已声明的值不覆盖；仅当子版本为空时取父版本。
// 覆盖字段：mainClass / assets / assetIndex.id / javaVersion.majorVersion。
//
// 参数 merged 为合并视图；parent 为父版本 JSON。
func fillMissingScalars(merged *mergedVersion, parent *versionJSON) {
	if merged == nil || parent == nil {
		return
	}

	if merged.MainClass == "" && parent.MainClass != "" {
		merged.MainClass = parent.MainClass
	}
	if merged.Assets == "" && parent.Assets != "" {
		merged.Assets = parent.Assets
	}
	if merged.AssetIndexID == "" && parent.AssetIndex != nil {
		merged.AssetIndexID = parent.AssetIndex.ID
	}
	if merged.JavaMajorVersion == 0 && parent.JavaVersion != nil {
		merged.JavaMajorVersion = parent.JavaVersion.MajorVersion
	}
}

// mergeParentIntoChild 把父版本合并进已建立的视图。
//
// 当前实现不走到该分支（遍历方向是子→父，父版本通过 appendParentCollections
// 与 fillMissingScalars 处理）。保留本函数是为了将来若改为「先父后子」的递归
// 结构时，合并语义有明确落点，不必重写。
func mergeParentIntoChild(merged *mergedVersion, parent *versionJSON) {
	appendParentCollections(merged, parent)
	fillMissingScalars(merged, parent)
}

// libraryRelativePath 推导库在 `libraries/` 下的相对路径。
//
// 优先级（卡面第 5 条）：
//  1. `downloads.artifact.path`（官方 JSON 自带，最可靠）；
//  2. 按 Maven 坐标 `group:artifact:version` 推导（C-2 口径 B 允许）：
//     `libraries/<group 以 / 分隔>/<artifact>/<version>/<artifact>-<version>.jar`。
//
// 返回空字符串表示无法推导（调用方跳过该库）。
// 返回值是**相对路径**（使用正斜杠），交给 `resolveInside` 拼接。
//
// 参数 library 为单个库定义。
func libraryRelativePath(library libraryJSON) string {
	if library.Downloads != nil && library.Downloads.Artifact != nil && library.Downloads.Artifact.Path != "" {
		// `downloads.artifact.path` 是**相对 libraries 目录**的路径
		// （例如 `at/yawk/lz4/lz4-java/1.10.1/lz4-java-1.10.1.jar`），
		// 而本函数的返回值统一是相对**游戏根目录**的路径，交给 `resolveInside` 拼接。
		// 这里必须补回 `libraries/` 前缀，否则会解析到 `<游戏根>/at/yawk/...` 这种不存在的路径。
		// P2 验收实测：现代版本 JSON 几乎每个库都带 downloads.artifact.path，
		// 少了这个前缀会让全部 114 个库被误报「缺失」。
		return joinLibraryPath(library.Downloads.Artifact.Path)
	}
	return mavenCoordinateToPath(library.Name)
}

// sourceLibraryRules 是库规则的来源标注（用于规则求值时的上下文文案）。
const sourceLibraryRules = "libraries"

// libraryAppliesToCurrentPlatform 判断库的 `rules` 是否允许在当前平台使用。
//
// 现代版本 JSON 会把**所有平台**的 native 库都列进 `libraries`，再用 `rules`
// 限定适用平台（例如 `{"action":"allow","os":{"name":"osx"}}`、netty 的
// `natives-linux` / `natives-macos` classifier 变体）。若不过滤，Windows 上会把
// 这些根本不该下载的跨平台文件算成「缺失」——P2 验收实测 40 个误报全部来自这里。
//
// 无 `rules` 字段的库一律视为适用（官方规范：缺省等价于 allow 全部）。
// `rules` 无法解析时同样保留，交给后续存在性检查暴露问题，避免因解析失败静默丢弃真实依赖。
//
// 参数 library 为单个库定义。
// 返回 true 表示该库适用于当前平台。
func libraryAppliesToCurrentPlatform(library libraryJSON) bool {
	if len(library.Rules) == 0 {
		return true
	}

	var rules []json.RawMessage
	if decodeErr := json.Unmarshal(library.Rules, &rules); decodeErr != nil {
		return true
	}
	if len(rules) == 0 {
		return true
	}

	// 复用 P2-04 的规则求值（allow/disallow 语义与 `arguments.rules` 完全一致）。
	// 这里丢弃它产生的 warning：库规则不匹配是**正常过滤**，不该变成用户可见警告。
	return evaluateElementRules(rules, sourceLibraryRules).keep
}

// joinLibraryPath 把「相对 libraries 目录」的路径规范成「相对游戏根目录」的路径。
//
// 容错三类常见写法：反斜杠分隔、前导分隔符、已经带 `libraries/` 前缀（部分整合包这样写）。
// 返回统一使用正斜杠的相对路径，交给 `resolveInside` 做越界校验。
func joinLibraryPath(path string) string {
	normalized := strings.ReplaceAll(strings.TrimSpace(path), `\`, fileSeparatorSlash)
	normalized = strings.TrimPrefix(normalized, fileSeparatorSlash)
	if normalized == "" {
		return ""
	}
	prefix := dirLibraries + fileSeparatorSlash
	if strings.HasPrefix(normalized, prefix) {
		return normalized
	}
	return prefix + normalized
}

// mavenCoordinateToPath 把 Maven 坐标转换为 `libraries/` 下相对路径。
//
// 参数 coordinate 形如 `net.minecraft:launchwrapper:1.12`。
// 坐标格式非法（段数不足 3）时返回空字符串。
// 附加 classifier（`group:artifact:version:classifier`）时把 classifier 拼进文件名。
func mavenCoordinateToPath(coordinate string) string {
	coordinate = strings.TrimSpace(coordinate)
	if coordinate == "" {
		return ""
	}

	parts := strings.Split(coordinate, ":")
	if len(parts) < 3 {
		return ""
	}

	group, artifact, version := parts[0], parts[1], parts[2]
	if group == "" || artifact == "" || version == "" {
		return ""
	}

	// 任一段含路径分隔符都视为非法（避免坐标本身构成穿越）。
	for _, part := range parts {
		if strings.ContainsAny(part, `/\`) {
			return ""
		}
	}

	groupPath := strings.ReplaceAll(group, ".", fileSeparatorSlash)

	fileName := artifact + "-" + version
	if len(parts) >= 4 && parts[3] != "" {
		// 带 classifier：文件名形如 `artifact-version-classifier.jar`。
		fileName = artifact + "-" + version + "-" + parts[3]
	}
	fileName += ".jar"

	return strings.Join([]string{dirLibraries, groupPath, artifact, version, fileName}, fileSeparatorSlash)
}

// libraryHasNatives 判断库是否声明了 natives（旧式 natives 字段或 natives classifier）。
//
// 参数 library 为单个库定义。
func libraryHasNatives(library libraryJSON) bool {
	if len(library.Natives) > 0 {
		return true
	}
	if library.Downloads == nil || library.Downloads.Classifiers == nil {
		return false
	}
	for key := range library.Downloads.Classifiers {
		if strings.Contains(key, "natives") {
			return true
		}
	}
	return false
}

// checkLibraries 检查全部库的存在性。
//
// 只做 `os.Stat` 存在性判断：**不读 jar 内容、不校验 SHA-1、不下载**。
// 单版本可能上千条库，因此是「一次 stat 循环」，不打开文件。
//
// 参数 root 为游戏根；libraries 为合并后的库列表。
// 返回必需数、已存在数、缺失清单（**相对路径**）与警告。
func checkLibraries(root string, libraries []libraryJSON) (required int, present int, missing []string, warnings []string) {
	missing = make([]string, 0)
	warnings = make([]string, 0)

	for _, library := range libraries {
		// 先按 rules 过滤掉不适用于当前平台的库，再检查存在性。
		// 不过滤会把其他平台的 native 变体算成「必需但缺失」，让健康版本被误判为不可启动。
		if !libraryAppliesToCurrentPlatform(library) {
			continue
		}
		relative := libraryRelativePath(library)
		if relative == "" {
			// 无法推导路径（坐标非法）：不计入 required，记一条警告。
			warnings = append(warnings, "有依赖库的坐标无法解析，已跳过存在性检查。")
			continue
		}

		required++

		absolute, resolveErr := resolveInside(root, relative)
		if resolveErr != nil {
			// 路径越界：不计入 present，并记警告（不把该库当成缺失，因为它本就不合法）。
			warnings = append(warnings, messageVersionPathEscaped)
			required--
			continue
		}

		if isRegularNonEmptyFile(absolute) {
			present++
			continue
		}
		// 缺失清单只放**相对路径**（接口要求，绝不列绝对路径）。
		missing = append(missing, relative)
	}

	return required, present, missing, warnings
}

// isRegularNonEmptyFile 判断路径是否为非空普通文件。
//
// 参数 path 为绝对路径。
func isRegularNonEmptyFile(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.Mode().IsRegular() && info.Size() > 0
}

// nativesPresent 判断版本的 natives 目录是否已存在且非空。
//
// 位置约定：`versions/<id>/natives/`（卡面第 6 条的保守口径）。
// **只检查存在性，不解压、不创建目录**（D-10 冻结）。
//
// 参数 root 为游戏根；id 为版本 id。
func nativesPresent(root, id string) bool {
	relative := dirVersions + fileSeparatorSlash + id + fileSeparatorSlash + "natives"

	absolute, resolveErr := resolveInside(root, relative)
	if resolveErr != nil {
		return false
	}

	info, err := os.Stat(absolute)
	if err != nil || !info.IsDir() {
		return false
	}

	// 目录存在但为空：视为「未就绪」，因为没有实际 natives 文件。
	entries, readErr := os.ReadDir(absolute)
	if readErr != nil {
		return false
	}
	return len(entries) > 0
}

// judgeVersionLaunchability 依合并视图判定可启动性三态。
//
// 判定矩阵（卡面第 7 条 + D-10）：
//   - 客户端 JAR 缺失 → metadata_only（reason=缺 jar）
//   - 必需库有缺失    → metadata_only（reason=缺库）
//   - 需要 natives 但目录缺失 → metadata_only（reason=缺 natives）
//   - 以上齐全        → launch_supported
//
// 参数 root 为游戏根；merged 为合并视图。
// 返回状态、原因、客户端 JAR 是否存在、库计数、缺失清单、
// natives 是否需要检查（needsNatives）与 natives 是否就绪（nativesOK）、警告。
//
// 为什么单独返回 needsNatives：DTO 的 `nativesPresent` 是**指针**，
// 需要区分「未检查」（版本没有任何 natives 声明 → nil）与「检查结果为 false」。
// 只返回 nativesOK 会让「无需 natives」的版本被误显示成「natives 已就绪」。
func judgeVersionLaunchability(root string, merged *mergedVersion) (
	status string,
	reason string,
	clientJarPresent bool,
	librariesRequired int,
	librariesPresent int,
	librariesMissing []string,
	needsNatives bool,
	nativesOK bool,
	warnings []string,
) {
	warnings = make([]string, 0)

	// 1. 客户端 JAR：按 `jar` 优先、`id` 回退的标识定位。
	jarRelative := dirVersions + fileSeparatorSlash + merged.ID + fileSeparatorSlash + merged.ClientJarID + ".jar"
	if jarAbsolute, resolveErr := resolveInside(root, jarRelative); resolveErr == nil {
		clientJarPresent = isRegularNonEmptyFile(jarAbsolute)
	}
	// 注意：若 `jar` 与 `id` 不同，实际客户端 jar 可能位于 `versions/<id>/<jar>.jar`。
	// 两种位置都检查是为了兼容旧版布局；任一存在即视为已就位。
	if !clientJarPresent && merged.ClientJarID != merged.ID {
		altRelative := dirVersions + fileSeparatorSlash + merged.ID + fileSeparatorSlash + merged.ID + ".jar"
		if altAbsolute, resolveErr := resolveInside(root, altRelative); resolveErr == nil {
			clientJarPresent = isRegularNonEmptyFile(altAbsolute)
		}
	}

	// 2. 依赖库存在性。
	librariesRequired, librariesPresent, librariesMissing, libraryWarnings := checkLibraries(root, merged.Libraries)
	warnings = append(warnings, libraryWarnings...)

	// 3. natives：仅当库声明了 natives 时才检查（D-10）。
	for _, library := range merged.Libraries {
		if libraryHasNatives(library) {
			needsNatives = true
			break
		}
	}
	nativesOK = true
	if needsNatives {
		nativesOK = nativesPresent(root, merged.ID)
	}

	// 4. 三态判定：按「最缺什么先说」的顺序给出原因。
	switch {
	case !clientJarPresent:
		return versionStatusMetadataOnly, reasonClientJarMissing,
			clientJarPresent, librariesRequired, librariesPresent, librariesMissing, needsNatives, nativesOK, warnings
	case len(librariesMissing) > 0:
		return versionStatusMetadataOnly, reasonLibrariesMissing,
			clientJarPresent, librariesRequired, librariesPresent, librariesMissing, needsNatives, nativesOK, warnings
	case needsNatives && !nativesOK:
		return versionStatusMetadataOnly, reasonNativesMissing,
			clientJarPresent, librariesRequired, librariesPresent, librariesMissing, needsNatives, nativesOK, warnings
	default:
		return versionStatusLaunchSupported, "",
			clientJarPresent, librariesRequired, librariesPresent, librariesMissing, needsNatives, nativesOK, warnings
	}
}

// checkVersion 是「检查单个版本」的编排函数。
//
// 流程：加载并合并 → 判定可启动性 → 组装 DTO。
// 参数 root 为已归一化的游戏根；id 为版本 id。
func checkVersion(root, id string) VersionCheckResult {
	startedAt := time.Now()

	result := VersionCheckResult{
		// 初始化为空切片而不是 nil：JSON 序列化为 `[]` 而不是 `null`。
		LibrariesMissing: []string{},
		Warnings:         []string{},
	}

	// 版本 id 合法性：非法 id 直接判 unsupported（不尝试访问文件系统）。
	if !isVersionIDValid(id) {
		result.Status = versionStatusUnsupported
		result.Reason = messageVersionIDInvalid
		result.ErrorCode = ErrorCodeVersionMetadata
		result.ErrorMessage = messageVersionIDInvalid
		return result
	}

	outcome := loadMergedVersion(root, id)
	result.Warnings = append(result.Warnings, outcome.warnings...)

	if outcome.merged == nil {
		// 加载失败：broken 或 unsupported，直接返回。
		result.Status = outcome.status
		result.Reason = outcome.reason
		result.ErrorCode = outcome.errorCode
		result.ErrorMessage = outcome.reason
		return result
	}

	merged := outcome.merged
	result.MainClass = merged.MainClass
	result.JavaMajorVersionRequired = merged.JavaMajorVersion
	result.Type = merged.Type

	status, reason, jarPresent, libRequired, libPresent, libMissing, needsNatives, nativesOK, judgeWarnings :=
		judgeVersionLaunchability(root, merged)

	result.Status = status
	result.Reason = reason
	result.ClientJarPresent = jarPresent
	result.LibrariesRequired = libRequired
	result.LibrariesPresent = libPresent
	result.LibrariesMissing = libMissing
	// 只有版本确实声明了 natives 时才填充指针：未声明时保持 nil，
	// 让前端能区分「未检查」与「检查结果为 false」。
	if needsNatives {
		natives := nativesOK
		result.NativesPresent = &natives
	}
	result.Warnings = append(result.Warnings, judgeWarnings...)

	// 耗时提示：超过阈值带 slow 标记但不中断（卡面第 9 条）。
	result.Slow = time.Since(startedAt) >= slowThreshold

	return result
}

// listLocalVersions 是「列出版本」的编排入口。
//
// 流程：归一化游戏根 → 枚举一层版本目录 → 逐个检查 → 组装 DTO。
// 参数 rawGameDirectory 为用户提供的游戏根目录。
// 返回结果始终非 nil：目录级失败通过 `ErrorCode`/`ErrorMessage` 表达。
func listLocalVersions(rawGameDirectory string) VersionListResult {
	result := VersionListResult{
		// 初始化为空切片而不是 nil：JSON 序列化为 `[]` 而不是 `null`。
		Versions:   []VersionSummary{},
		SkippedIDs: []string{},
		Warnings:   []string{},
	}

	// 复用 P2-02 的归一化：不自行拼接路径、不重复实现边界校验。
	root, warnings, pathErr := normalizeGameRoot(rawGameDirectory)
	result.Warnings = append(result.Warnings, warnings...)
	if pathErr != nil {
		result.ErrorCode = pathErr.Code
		result.ErrorMessage = pathErr.Message
		return result
	}

	result.GameRootDisplay = buildGameRootDisplay(root)

	versionIDs, skippedIDs, listErr := listVersionDirectories(root)
	if listErr != nil {
		result.ErrorCode = listErr.Code
		result.ErrorMessage = listErr.Message
		return result
	}
	result.SkippedIDs = append(result.SkippedIDs, skippedIDs...)

	// 逐个检查：每个版本都给出三态结论；单个版本损坏不影响其他版本。
	for _, id := range versionIDs {
		check := checkVersion(root, id)
		result.Versions = append(result.Versions, VersionSummary{
			ID:     id,
			Type:   check.Type,
			Status: check.Status,
			Reason: check.Reason,
		})
		// 版本级警告汇总到列表级，便于界面一次性看到异常版本提示。
		for _, warning := range check.Warnings {
			result.Warnings = append(result.Warnings, id+"："+warning)
		}
	}

	return result
}

// checkVersionWithRoot 是「检查单个版本」的编排入口。
//
// 流程：归一化游戏根 → 校验版本 id → 检查版本。
// 参数 rawGameDirectory 为用户提供的游戏根目录；versionID 为版本 id。
func checkVersionWithRoot(rawGameDirectory, versionID string) VersionCheckResult {
	// 复用 P2-02 的归一化。
	root, warnings, pathErr := normalizeGameRoot(rawGameDirectory)
	if pathErr != nil {
		return VersionCheckResult{
			ID:               versionID,
			Status:           versionStatusUnsupported,
			Reason:           pathErr.Message,
			ErrorCode:        pathErr.Code,
			ErrorMessage:     pathErr.Message,
			LibrariesMissing: []string{},
			Warnings:         []string{},
		}
	}

	result := checkVersion(root, versionID)
	// 归一化阶段的警告（例如符号链接解析失败）合并进结果。
	result.Warnings = append(warnings, result.Warnings...)
	return result
}
