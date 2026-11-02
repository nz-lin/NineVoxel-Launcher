// 本文件实现游戏根目录的归一化、结构检查与**路径边界函数**。
//
// 本文件最重要的产出是 `resolveInside`：它是 P2-03 ~ P2-06 **唯一**的
// 文件访问入口。后续任务卡必须调用它，不得各自再写一套边界校验。
//
// 全程只读：本文件不创建任何目录、不写任何文件、不读取 `versions/` 下的 JSON、
// 不递归遍历（`versions/` 只统计一层直接子目录）、不统计目录体积。
// 用户游戏目录在本卡与整个 P2 阶段都是只读的。
package launch

import (
	"errors"
	"example.com/ninevoxel/launcher/internal/artifact"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// 结构检查与边界校验相关的常量。
const (
	// dirVersions 是版本目录名。
	dirVersions = "versions"

	// dirLibraries 是库目录名。
	dirLibraries = "libraries"

	// dirNatives 是旧版本 natives 目录名（位于 `versions/<id>/` 下）。
	dirNatives = "natives"

	// dirAssets 是资源目录名。
	dirAssets = "assets"

	// dirAssetsIndexes 是资源索引子目录名（位于 assets 下）。
	dirAssetsIndexes = "indexes"

	// dirAssetsObjects 是资源对象子目录名（位于 assets 下）。
	dirAssetsObjects = "objects"

	// fileLauncherProfiles 是官方启动器配置文件名。
	//
	// 本包**只检查存在性**，绝不打开或解析它：
	// 该文件可能包含第三方启动器的账号令牌与用户配置，读取它违反最小够用原则。
	fileLauncherProfiles = "launcher_profiles.json"

	// displayCategoryGame 是游戏目录的展示类别名（决策记录 C-11）。
	displayCategoryGame = "游戏目录"

	// gameDisplayTailSegments 是游戏目录脱敏展示保留的末段数量。
	//
	// 决策记录 C-11 规定游戏目录「保留末两段」，示例为 `游戏目录\…\.minecraft`。
	// 该示例的原路径 `C:\Users\xx\AppData\Roaming\.minecraft` 的末段是 `.minecraft`；
	// 保留两段在更深路径下形如 `…\<父目录>\.minecraft`，与示例语义一致。
	gameDisplayTailSegments = 2

	// 三态状态取值，与接口冻结清单第 4.4 节的 TS 联合类型逐字一致。
	checkStatusPresent       = "present"
	checkStatusMissing       = "missing"
	checkStatusNotADirectory = "not_a_directory"
)

// 结构检查项名称：作为 `checks[].name` 的稳定标识，
// 前端可据此做映射，不得依赖展示文案做判断。
const (
	// checkNameVersions 对应 `versions/` 目录。
	checkNameVersions = "versions"

	// checkNameLibraries 对应 `libraries/` 目录。
	checkNameLibraries = "libraries"

	// checkNameAssets 对应 `assets/` 目录。
	checkNameAssets = "assets"

	// checkNameAssetsIndexes 对应 `assets/indexes/` 目录。
	checkNameAssetsIndexes = "assets/indexes"

	// checkNameAssetsObjects 对应 `assets/objects/` 目录。
	checkNameAssetsObjects = "assets/objects"

	// checkNameLauncherProfiles 对应根目录下的 `launcher_profiles.json`。
	checkNameLauncherProfiles = "launcher_profiles.json"
)

// 游戏目录相关的错误码与文案。
const (
	// ErrorCodeGameDir 表示目录存在但结构缺失（无法支撑启动）。
	//
	// 来源：决策记录第五节新增冻结码表，首个使用卡 P2-02。
	// 注意：该码**不用于**「目录不存在」（那是 PATH_INVALID_001），
	// 也不用于「目录结构正常」的情形。
	ErrorCodeGameDir = "GAME_DIR_001"

	// messageGameDirEmpty 表示用户没有填写目录。
	messageGameDirEmpty = "请填写 Minecraft 游戏目录的路径。"

	// messageGameDirRelative 表示输入的是相对路径。
	messageGameDirRelative = "请填写游戏目录的完整路径（不支持相对路径）。"

	// messageGameDirHasParentSegment 表示路径中含 `..` 段。
	messageGameDirHasParentSegment = "路径中不允许包含 “..” 相对于上一级的写法，请填写完整路径。"

	// messageGameDirHasNUL 表示路径含 NUL 字符。
	messageGameDirHasNUL = "路径中包含非法字符，请重新填写。"

	// messageGameDirNotAbsolute 表示路径归一化失败。
	messageGameDirNotAbsolute = "无法解析该路径，请检查路径是否正确。"

	// messageGameDirDriveRoot 表示用户选了盘符根目录（例如 `C:\`）。
	messageGameDirDriveRoot = "请不要选择磁盘根目录，请选择 .minecraft 这类游戏目录。"

	// messageGameDirEmptyUNC 表示 UNC 路径缺少共享名（例如 `\\`）。
	messageGameDirEmptyUNC = "网络路径不完整，请填写共享目录的完整路径。"

	// messageGameDirNotExist 表示目录不存在。
	messageGameDirNotExist = "未在该路径找到游戏目录，请确认路径是否正确。"

	// messageGameDirNotDirectory 表示路径指向的是普通文件。
	messageGameDirNotDirectory = "所选路径是一个文件，请选择游戏目录（通常是 .minecraft）。"

	// messageGameDirNoPermission 表示无权限访问该目录。
	messageGameDirNoPermission = "没有权限访问该游戏目录，请检查目录权限。"

	// warningGameDirSymlinkUnresolved 表示符号链接解析失败时的提示。
	warningGameDirSymlinkUnresolved = "无法解析该路径的符号链接，已按给定路径继续检查。"

	// hintGameDirMissingVersions 表示缺少 versions 目录时的启动提示。
	hintGameDirMissingVersions = "未找到 versions 目录，无法启动游戏。"

	// hintGameDirMissingLibraries 表示缺少 libraries 目录时的启动提示。
	hintGameDirMissingLibraries = "未找到 libraries 目录，缺失依赖库时将无法启动。"

	// hintGameDirVersionsNotDir 表示 versions 存在但不是目录。
	hintGameDirVersionsNotDir = "versions 存在但不是目录，目录结构异常，无法启动游戏。"

	// hintGameDirNoVersions 表示 versions 目录为空（没有可直接启动的版本）。
	hintGameDirNoVersions = "versions 目录下没有任何版本，请先安装游戏版本。"

	// warningVersionsUnreadable 表示 versions 目录无法列举（通常是权限问题）。
	warningVersionsUnreadable = "无法列举 versions 目录，版本数量按 0 处理。"

	// detailAssetsIncomplete 表示 assets 目录存在但缺少必要子目录。
	detailAssetsIncomplete = "assets 目录存在，但缺少 indexes 或 objects 子目录。"
)

// 结构检查项的说明文案。
const (
	// detailVersionsPresent 表示 versions 目录可用。
	detailVersionsPresent = "已找到 versions 目录。"

	// detailVersionsMissing 表示 versions 目录缺失。
	detailVersionsMissing = "未找到 versions 目录。"

	// detailVersionsNotDir 表示 versions 存在但为文件。
	detailVersionsNotDir = "versions 存在但不是目录。"

	// detailLibrariesPresent 表示 libraries 目录存在且含至少一层分组目录。
	detailLibrariesPresent = "已找到 libraries 目录及其分组子目录。"

	// detailLibrariesMissing 表示 libraries 目录缺失。
	detailLibrariesMissing = "未找到 libraries 目录。"

	// detailLibrariesNotDir 表示 libraries 存在但为文件。
	detailLibrariesNotDir = "libraries 存在但不是目录。"

	// detailLibrariesNoGroup 表示 libraries 存在但没有分组子目录。
	detailLibrariesNoGroup = "libraries 目录存在，但没有找到任何分组子目录。"

	// detailAssetsPresent 表示 assets 目录可用。
	detailAssetsPresent = "已找到 assets 目录及其 indexes、objects 子目录。"

	// detailAssetsMissing 表示 assets 目录缺失。
	detailAssetsMissing = "未找到 assets 目录。"

	// detailAssetsNotDir 表示 assets 存在但为文件。
	detailAssetsNotDir = "assets 存在但不是目录。"

	// detailAssetsIndexesPresent 表示 indexes 子目录可用。
	detailAssetsIndexesPresent = "已找到 assets/indexes 目录。"

	// detailAssetsIndexesMissing 表示 indexes 子目录缺失。
	detailAssetsIndexesMissing = "未找到 assets/indexes 目录。"

	// detailAssetsIndexesNotDir 表示 indexes 存在但为文件。
	detailAssetsIndexesNotDir = "assets/indexes 存在但不是目录。"

	// detailAssetsObjectsPresent 表示 objects 子目录可用。
	detailAssetsObjectsPresent = "已找到 assets/objects 目录。"

	// detailAssetsObjectsMissing 表示 objects 子目录缺失。
	detailAssetsObjectsMissing = "未找到 assets/objects 目录。"

	// detailAssetsObjectsNotDir 表示 objects 存在但为文件。
	detailAssetsObjectsNotDir = "assets/objects 存在但不是目录。"

	// detailLauncherProfilesPresent 表示该文件存在（只记存在性）。
	detailLauncherProfilesPresent = "存在 launcher_profiles.json（未读取其内容）。"

	// detailLauncherProfilesMissing 表示该文件不存在。
	detailLauncherProfilesMissing = "未找到 launcher_profiles.json（不影响启动）。"
)

// duplicateSegment 用于判断路径中是否出现 `..` 段。
//
// 单独定义而不是字面量 `..`，避免在代码里散落魔法字符串。
const duplicateSegment = ".."

// nulCharacter 是不允许出现在路径中的 NUL 字符。
const nulCharacter = "\x00"

// resolveInside 把相对路径安全地解析到游戏根目录内部。
//
// 这是 P2-03 ~ P2-06 **唯一**的文件访问入口。后续任务卡必须调用本函数，
// 不得各自实现边界校验（重复实现会产生不一致的防御强度，是路径穿越漏洞的常见来源）。
//
// 签名与调用约定（后续卡请照抄）：
//
//		func resolveInside(root, relative string) (string, error)
//
//	  - `root` 必须是**已归一化**的游戏根目录（由 `normalizeGameRoot` 产出）；
//	  - `relative` 是相对于 root 的路径，例如 `versions/1.21/1.21.json`；
//	  - 返回值为可直接 `os.Open` 的绝对路径；
//	  - 返回 `*Error`（`PATH_INVALID_001`），调用方应把错误码透传给前端。
//
// 防御的攻击面：
//  1. **路径穿越（`..`）**：拒绝任何 `..` 段，无论出现在开头还是中间；
//  2. **绝对路径注入**：`relative` 含盘符（`C:`）或 POSIX 根（`/x`）一律拒绝；
//  3. **盘符切换**：Windows 下 `relative` 若自带 `D:` 会改变解析根，单独检测；
//  4. **NUL 字符截断**：`\x00` 在部分系统调用中会被截断，导致绕过校验；
//  5. **符号链接逃逸**：目标已存在时用 `filepath.EvalSymlinks` 解析真实路径，
//     再校验真实路径仍在 root 之内（否则 `root/link -> C:\Windows` 可绕过）；
//  6. **前缀边界**：比较时用 `filepath.Rel` 而不是字符串前缀，
//     避免 `C:\game` 误判 `C:\game2` 为内部路径；
//  7. **空相对路径**：`relative == ""` 返回 root 本身（读取根目录合法）。
//
// 特别说明「未创建的文件」：若目标尚不存在（例如 P2 卡要判断某文件是否缺失），
// `EvalSymlinks` 会失败；此时只要**父目录链**在 root 内即视为安全，
// 因为后续写入（若有）也只能落在 root 内部。
func resolveInside(root, relative string) (string, error) {
	if root == "" {
		return "", newInputError(ErrorCodePathInvalid, messageGameDirNotExist)
	}

	// 空相对路径：语义是「root 本身」，合法。
	if relative == "" {
		return root, nil
	}

	// NUL 字符：必须先于任何路径操作拒绝，否则底层可能截断后绕过校验。
	if strings.Contains(relative, nulCharacter) {
		return "", newInputError(ErrorCodePathInvalid, messageGameDirHasNUL)
	}

	// 绝对路径注入：Windows 盘符与 POSIX 根都要拒绝。
	// 注意 `filepath.IsAbs` 在 Windows 上对 `/x` 返回 false（会被当成「当前盘相对」），
	// 因此额外检查前导分隔符。
	if filepath.IsAbs(relative) || hasVolumePrefix(relative) || startsWithSeparator(relative) {
		return "", newInputError(ErrorCodePathInvalid, messageGameDirRelative)
	}

	// `..` 段：逐段检查，覆盖 `a/../b` 与 `../x` 两种形态。
	if hasParentSegment(relative) {
		return "", newInputError(ErrorCodePathInvalid, messageGameDirHasParentSegment)
	}

	// 拼接并归一化。此时 `..` 已被拒绝，Clean 不会产生越界结果，
	// 但为防御未预见的分隔符花样（如 `//`、`\` 混用）仍需再校验一次。
	joined := filepath.Clean(filepath.Join(root, relative))

	if err := ensureWithinRoot(root, joined); err != nil {
		return "", err
	}

	resolved, resolveErr := artifact.ResolveInsideRoot(root, relative)
	if resolveErr != nil {
		return "", newInputError(ErrorCodePathInvalid, messageGameDirRelative)
	}
	return resolved, nil
}

// ensureWithinRoot 校验绝对路径 candidate 是否确实位于 root 之内。
//
// 使用 `filepath.Rel` 而不是字符串前缀比较：
// 前缀比较会把 `C:\game2` 误判为 `C:\game` 的子路径（经典边界漏洞）。
// `Rel` 返回以 `..` 开头或本身就是 `..` 时，说明 candidate 在 root 之外。
//
// 参数 root 为已归一化的游戏根；candidate 为待校验的绝对路径。
func ensureWithinRoot(root, candidate string) *Error {
	rootClean := filepath.Clean(root)
	candidateClean := filepath.Clean(candidate)

	// 先做大小写归一：Windows 文件系统不区分大小写，
	// `C:\Game` 与 `c:\game` 是同一目录，直接比较会误判为越界。
	if runtime.GOOS == "windows" {
		rootClean = strings.ToLower(rootClean)
		candidateClean = strings.ToLower(candidateClean)
	}

	relativePath, err := filepath.Rel(rootClean, candidateClean)
	if err != nil {
		// Rel 失败说明两路径不在同一卷（例如 root 在 C:、candidate 在 D:）。
		return newInputError(ErrorCodePathInvalid, messageGameDirRelative)
	}

	// Rel 结果是 `.`（同一路径）或普通相对路径时都在 root 内。
	if relativePath == "." {
		return nil
	}

	// 以 `..` 开头或等于 `..` 表示越界。
	if relativePath == duplicateSegment || strings.HasPrefix(relativePath, duplicateSegment+string(filepath.Separator)) {
		return newInputError(ErrorCodePathInvalid, messageGameDirRelative)
	}

	return nil
}

// hasVolumePrefix 判断路径是否带盘符或 UNC 前缀。
//
// Windows 下 `C:foo` 这类「盘符相对路径」既不是绝对路径也不在 root 内，
// 必须单独拒绝。UNC 以 `\\` 开头，同样属于宿主路径而非 root 内部路径。
//
// 参数 value 为待判断的路径片段。
func hasVolumePrefix(value string) bool {
	if len(value) < 2 {
		return false
	}
	// 形态 `X:` 其中 X 为字母。
	if value[1] == ':' {
		char := value[0]
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') {
			return true
		}
	}
	return false
}

// startsWithSeparator 判断路径是否以路径分隔符开头。
//
// 覆盖 POSIX 的 `/x` 与 Windows 的 `\x`：两者在拼接语义上都表示
// 「回到根」而不是「相对于 root」，因此必须拒绝。
//
// 参数 value 为待判断的路径片段。
func startsWithSeparator(value string) bool {
	if value == "" {
		return false
	}
	return value[0] == '/' || value[0] == '\\'
}

// hasParentSegment 判断路径是否包含 `..` 段。
//
// 逐段检查而不是用字符串包含判断：后者会把正常文件名
// `..bashrc` 或 `foo..bar` 误判成穿越。
//
// 参数 value 为待判断的相对路径。
func hasParentSegment(value string) bool {
	// 统一按两种分隔符切分：Windows 允许 `\` 与 `/` 混用。
	segments := strings.FieldsFunc(value, func(char rune) bool {
		return char == '/' || char == '\\'
	})
	for _, segment := range segments {
		if segment == duplicateSegment {
			return true
		}
	}
	return false
}

// normalizeGameRoot 归一化用户提供的游戏根目录路径。
//
// 归一化流程（P2-02 卡面第 1 条）：
//  1. 去首尾空白；空值 → `PATH_INVALID_001`；
//  2. 拒绝 NUL 字符；
//  3. 拒绝 `..` 段（用户不该用「上一级」写法指代目录）；
//  4. 拒绝相对路径（含盘符相对路径 `C:foo`）；
//  5. 拒绝盘符根（`C:\`）与空 UNC（`\\`）——它们不是可用的游戏目录；
//  6. `filepath.Clean` + `filepath.Abs`；
//  7. `filepath.EvalSymlinks`：成功则用真实路径；失败时**保留归一化结果并给出警告**
//     （目录可能尚未创建或权限受限，但这不影响后续结构检查的结论）；
//  8. 校验目标存在且为目录：不存在 → `PATH_INVALID_001`；是文件 → `PATH_INVALID_001`。
//
// 参数 rawPath 为用户输入或对话框返回的路径。
// 返回归一化后的绝对路径、警告列表与错误。
func normalizeGameRoot(rawPath string) (string, []string, *Error) {
	trimmed := strings.TrimSpace(rawPath)
	if trimmed == "" {
		return "", nil, newInputError(ErrorCodePathInvalid, messageGameDirEmpty)
	}

	if strings.Contains(trimmed, nulCharacter) {
		return "", nil, newInputError(ErrorCodePathInvalid, messageGameDirHasNUL)
	}

	// `..` 段在游戏根目录里没有正当用法：用户要的是「某个具体目录」。
	if hasParentSegment(trimmed) {
		return "", nil, newInputError(ErrorCodePathInvalid, messageGameDirHasParentSegment)
	}

	// 相对路径与盘符相对路径一律拒绝：路径不能依赖进程 cwd。
	if !filepath.IsAbs(trimmed) || hasVolumePrefix(trimmed) && filepath.VolumeName(trimmed) == "" {
		return "", nil, newInputError(ErrorCodePathInvalid, messageGameDirRelative)
	}

	cleaned := filepath.Clean(trimmed)

	// 盘符根（`C:\`）不是游戏目录：它的直接子目录是整盘内容，结构检查毫无意义。
	if isVolumeRoot(cleaned) {
		return "", nil, newInputError(ErrorCodePathInvalid, messageGameDirDriveRoot)
	}

	// 空 UNC（`\\` 或 `\\\`）缺少共享名，无法访问。
	if runtime.GOOS == "windows" && strings.HasPrefix(cleaned, `\\`) && filepath.VolumeName(cleaned) == "" {
		return "", nil, newInputError(ErrorCodePathInvalid, messageGameDirEmptyUNC)
	}

	absPath, err := filepath.Abs(cleaned)
	if err != nil {
		return "", nil, newInputError(ErrorCodePathInvalid, messageGameDirNotAbsolute)
	}

	warnings := make([]string, 0, 1)

	// 符号链接解析：成功时使用真实路径（后续边界比较基于真实路径才可靠）。
	if resolved, evalErr := filepath.EvalSymlinks(absPath); evalErr == nil {
		absPath = resolved
	} else {
		// 解析失败不阻断：目录可能不存在或权限受限。
		// 保留归一化结果并给警告，让用户在结果里看到「这是降级判断」。
		warnings = append(warnings, warningGameDirSymlinkUnresolved)
	}

	// 存在性与类型检查：必须区分「不存在」「是文件」「是目录」三种结论，
	// 因为它们对应用户能做的不同动作（改路径 / 选目录）。
	info, statErr := os.Stat(absPath)
	if statErr != nil {
		if errors.Is(statErr, os.ErrNotExist) {
			return "", warnings, newInputError(ErrorCodePathInvalid, messageGameDirNotExist)
		}
		if errors.Is(statErr, os.ErrPermission) {
			return "", warnings, newSystemError(ErrorCodePathInvalid, messageGameDirNoPermission)
		}
		return "", warnings, newInputError(ErrorCodePathInvalid, messageGameDirNotExist)
	}

	if info.IsDir() {
		return absPath, warnings, nil
	}

	// 普通文件：用户可能选错成了 jar 或 exe。
	return "", warnings, newInputError(ErrorCodePathInvalid, messageGameDirNotDirectory)
}

// isVolumeRoot 判断路径是否为盘符根（例如 `C:\`）。
//
// 判定方式：`filepath.Dir(root)` 等于 root 自身时即为根
// （`filepath.Dir("C:\\") == "C:\\"`，而 `filepath.Dir("C:\\game") == "C:\\"`）。
// POSIX 上 `/` 同理。
//
// 参数 path 为已 Clean 的路径。
func isVolumeRoot(path string) bool {
	if path == "" {
		return false
	}
	parent := filepath.Dir(path)
	return parent == path
}

// describeDirectory 检查一个路径的三态：present（目录）/ missing / not_a_directory。
//
// 参数 path 为待检查的绝对路径。
// 返回状态与说明文案；无权限等系统错误由调用方在更高层处理
// （本函数把读不到的目录也归为 missing，避免把「读不了」误报成「不存在」以外的语义，
// 详细原因由 `checkGameDirectory` 在顶层捕获）。
func describeDirectory(path string) (string, string) {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return checkStatusMissing, ""
		}
		// 权限不足等情况：无法确认是目录，按缺失处理；
		// 顶层检查会用 os.ReadDir 的权限错误给出系统级结论。
		return checkStatusMissing, ""
	}
	if info.IsDir() {
		return checkStatusPresent, ""
	}
	// 存在但不是目录：这一态必须与 missing 区分开，
	// 否则用户会以为「删掉重装」能解决，实际是路径被同名文件占住了。
	return checkStatusNotADirectory, ""
}

// countDirectSubdirectories 统计目录的直接子目录数量（一层，不递归）。
//
// 参数 path 为目录绝对路径。
// 返回子目录数量与错误；目录不可读时返回 0 与错误（由调用方决定是否降级为警告）。
//
// 性能：`os.ReadDir` 只读一层目录项，不打开文件、不递归，
// 因此即使目录含上万文件也只是一次目录列举，属常数级操作。
func countDirectSubdirectories(path string) (int, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return 0, err
	}

	count := 0
	for _, entry := range entries {
		if entry.IsDir() {
			count++
		}
	}
	return count, nil
}

// hasAnyGroupingSubdirectory 判断目录下是否存在至少一层分组子目录。
//
// 用于 `libraries/` 的检查：Maven 布局下 `libraries/net/minecraft/...`，
// 因此「至少有一个子目录」即说明它不是空目录或只有文件。
//
// 参数 path 为目录绝对路径。
func hasAnyGroupingSubdirectory(path string) bool {
	entries, err := os.ReadDir(path)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if entry.IsDir() {
			return true
		}
	}
	return false
}

// checkGameDirectory 是游戏目录结构检查的编排函数。
//
// 流程：
//  1. 归一化用户路径（失败即停，返回 `PATH_INVALID_001`）；
//  2. 一次性只读检查六个结构项（`versions` / `libraries` / `assets` /
//     `assets/indexes` / `assets/objects` / `launcher_profiles.json`）；
//  3. 统计 `versions/` 的直接子目录数量（一层，不递归）；
//  4. 综合出 `valid` 与 `launchableHint`。
//
// 关键语义（卡面第 6 条）：**目录存在但结构缺失不算错误**——
// 用户可能选错了层级（例如选了 `.minecraft/versions`），
// 这时应返回 `valid=false` + 具体缺失项 + 提示，让用户自己换个路径重试，
// 而不是弹一个「错误」把人劝退。
//
// 参数 rawPath 为用户提供的目录路径。
// 返回值始终非 nil，失败时通过 `ErrorCode`/`ErrorMessage` 表达。
//
// 全程只读：不创建目录、不写文件、不读任何版本 JSON。
func checkGameDirectory(rawPath string) GameDirectoryCheckResult {
	result := GameDirectoryCheckResult{
		// 初始化为空切片而不是 nil：JSON 序列化为 `[]` 而不是 `null`。
		Checks:   []GameDirectoryCheckItem{},
		Warnings: []string{},
	}

	absRoot, warnings, pathErr := normalizeGameRoot(rawPath)
	result.Warnings = append(result.Warnings, warnings...)

	if pathErr != nil {
		result.ErrorCode = pathErr.Code
		result.ErrorMessage = pathErr.Message
		return result
	}

	// 归一化成功即可给出脱敏展示形态：即使结构检查失败，用户也能确认选的是哪个目录。
	result.GameRootDisplay = buildGameRootDisplay(absRoot)

	// 逐项检查：每一项都产出三态，不是布尔值。
	versionsPath := filepath.Join(absRoot, dirVersions)
	librariesPath := filepath.Join(absRoot, dirLibraries)
	assetsPath := filepath.Join(absRoot, dirAssets)
	indexesPath := filepath.Join(assetsPath, dirAssetsIndexes)
	objectsPath := filepath.Join(assetsPath, dirAssetsObjects)
	profilesPath := filepath.Join(absRoot, fileLauncherProfiles)

	versionsStatus, _ := describeDirectory(versionsPath)
	librariesStatus, _ := describeDirectory(librariesPath)
	assetsStatus, _ := describeDirectory(assetsPath)
	indexesStatus, _ := describeDirectory(indexesPath)
	objectsStatus, _ := describeDirectory(objectsPath)

	// `versions/` 的直接子目录计数：只在这一层，不递归、不读 JSON。
	// 读失败时按 0 处理并记警告：这可能是权限问题，但不该让整个检查失败。
	if versionsStatus == checkStatusPresent {
		count, countErr := countDirectSubdirectories(versionsPath)
		if countErr != nil {
			result.Warnings = append(result.Warnings, warningVersionsUnreadable)
		}
		result.VersionsCount = count
	}

	// `libraries/` 除目录存在外，还要求至少一层分组子目录。
	librariesDetail := detailLibrariesPresent
	if librariesStatus == checkStatusPresent && !hasAnyGroupingSubdirectory(librariesPath) {
		// 存在但没有任何分组目录：结构不完整，但仍属 present（目录本身可用）。
		librariesDetail = detailLibrariesNoGroup
	}

	// `assets/` 的通过条件包含两个必需的子目录。
	assetsDetail := detailAssetsPresent
	if assetsStatus == checkStatusPresent && (indexesStatus != checkStatusPresent || objectsStatus != checkStatusPresent) {
		assetsDetail = detailAssetsIncomplete
	}

	// `launcher_profiles.json` 只检查存在性，**绝不读取内容**。
	profilesStatus := checkStatusMissing
	if fileExists(profilesPath) {
		profilesStatus = checkStatusPresent
	}

	result.Checks = []GameDirectoryCheckItem{
		{Name: checkNameVersions, Status: versionsStatus, Detail: describeVersionsDetail(versionsStatus)},
		{Name: checkNameLibraries, Status: librariesStatus, Detail: librariesDetail},
		{Name: checkNameAssets, Status: assetsStatus, Detail: assetsDetail},
		{Name: checkNameAssetsIndexes, Status: indexesStatus, Detail: describeIndexesDetail(indexesStatus)},
		{Name: checkNameAssetsObjects, Status: objectsStatus, Detail: describeObjectsDetail(objectsStatus)},
		{Name: checkNameLauncherProfiles, Status: profilesStatus, Detail: describeProfilesDetail(profilesStatus)},
	}

	// 综合判定：可启动的最低条件是 `versions/` 是目录且至少有一个版本。
	// `libraries/` 缺失会影响后续启动（缺库无法运行），但 P2 阶段只作提示，
	// 因为是否为「必须」取决于版本 JSON 的 libraries 列表（属 P2-03 的判断）。
	result.Valid = versionsStatus == checkStatusPresent && result.VersionsCount > 0
	result.LaunchableHint = buildLaunchableHint(versionsStatus, result.VersionsCount, librariesStatus)

	return result
}

// buildLaunchableHint 依据检查结果生成「能否启动」的中文提示。
//
// 参数 versionsStatus 为 versions 目录的三态；
// versionsCount 为直接子目录数量；librariesStatus 为 libraries 目录的三态。
// 返回空字符串表示无额外提示（结构可用）。
func buildLaunchableHint(versionsStatus string, versionsCount int, librariesStatus string) string {
	switch versionsStatus {
	case checkStatusNotADirectory:
		// versions 被同名文件占位：这是结构性错误，必须显式提示。
		return hintGameDirVersionsNotDir
	case checkStatusMissing:
		return hintGameDirMissingVersions
	}

	if versionsCount == 0 {
		return hintGameDirNoVersions
	}

	// versions 可用但 libraries 缺失：给出提示，但不判为「无法启动」——
	// 少数极简整合包可能不依赖 libraries 目录（判断权归 P2-03）。
	if librariesStatus == checkStatusMissing || librariesStatus == checkStatusNotADirectory {
		return hintGameDirMissingLibraries
	}

	return ""
}

// buildGameRootDisplay 构造游戏目录的脱敏展示形态。
//
// 口径（决策记录 C-11）：**游戏目录 + `…` + 末两段**，
// 示例为 `游戏目录\…\.minecraft`。
// 与 Java 那条（三段）不同：目录没有「bin 这类有识别价值的父目录」，
// 保留末两段（父目录 + 目录名）已足够区分。
//
// 参数 absRoot 为归一化后的绝对路径。
// 返回值**不含完整绝对路径**。
func buildGameRootDisplay(absRoot string) string {
	if absRoot == "" {
		return ""
	}

	separator := "/"
	if runtime.GOOS == "windows" {
		separator = `\`
	}

	segments := strings.Split(filepath.ToSlash(absRoot), "/")
	clean := make([]string, 0, len(segments))
	hasVolumeSegment := false
	for _, segment := range segments {
		if segment == "" {
			continue
		}
		// 盘符段（例如 `C:`）不是可展示的目录名：它属于「宿主位置」信息，
		// 保留它等于泄漏路径前缀——`C:\minecraft` 会原样显示成 `C:\minecraft`。
		if isVolumeSegment(segment) {
			hasVolumeSegment = true
			continue
		}
		clean = append(clean, segment)
	}

	if len(clean) == 0 {
		return ""
	}

	// 保留末两段：例如 `Roaming\.minecraft`。
	tailCount := gameDisplayTailSegments
	if len(clean) < tailCount {
		tailCount = len(clean)
	}
	tail := clean[len(clean)-tailCount:]
	joinedTail := strings.Join(tail, separator)

	// 是否有被省略的段落：清洗后仍多于保留段数，或原本含盘符段（盘符已被跳过）。
	// 后者保证 `C:\minecraft` 会显示成 `游戏目录\…\minecraft` 而不是 `C:\minecraft`。
	omitted := len(clean) > tailCount || hasVolumeSegment

	if omitted {
		return displayCategoryGame + separator + displayEllipsis + separator + joinedTail
	}
	return joinedTail
}

// isVolumeSegment 判断单个路径段是否为盘符（例如 `C:`）。
//
// 参数 segment 为路径切分后的单段文本。
func isVolumeSegment(segment string) bool {
	if len(segment) != 2 || segment[1] != ':' {
		return false
	}
	char := segment[0]
	return (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z')
}

// describeVersionsDetail 返回 versions 检查项的说明文案。
func describeVersionsDetail(status string) string {
	switch status {
	case checkStatusPresent:
		return detailVersionsPresent
	case checkStatusNotADirectory:
		return detailVersionsNotDir
	default:
		return detailVersionsMissing
	}
}

// describeLibrariesDetail 返回 libraries 检查项的说明文案。
func describeLibrariesDetail(status string) string {
	switch status {
	case checkStatusPresent:
		return detailLibrariesPresent
	case checkStatusNotADirectory:
		return detailLibrariesNotDir
	default:
		return detailLibrariesMissing
	}
}

// describeAssetsDetail 返回 assets 检查项的说明文案。
func describeAssetsDetail(status string) string {
	switch status {
	case checkStatusPresent:
		return detailAssetsPresent
	case checkStatusNotADirectory:
		return detailAssetsNotDir
	default:
		return detailAssetsMissing
	}
}

// describeIndexesDetail 返回 assets/indexes 检查项的说明文案。
func describeIndexesDetail(status string) string {
	switch status {
	case checkStatusPresent:
		return detailAssetsIndexesPresent
	case checkStatusNotADirectory:
		return detailAssetsIndexesNotDir
	default:
		return detailAssetsIndexesMissing
	}
}

// describeObjectsDetail 返回 assets/objects 检查项的说明文案。
func describeObjectsDetail(status string) string {
	switch status {
	case checkStatusPresent:
		return detailAssetsObjectsPresent
	case checkStatusNotADirectory:
		return detailAssetsObjectsNotDir
	default:
		return detailAssetsObjectsMissing
	}
}

// describeProfilesDetail 返回 launcher_profiles.json 检查项的说明文案。
func describeProfilesDetail(status string) string {
	if status == checkStatusPresent {
		return detailLauncherProfilesPresent
	}
	return detailLauncherProfilesMissing
}

// fileExists 只判断文件是否存在（不打开、不读取内容）。

// 用于 `launcher_profiles.json` 的存在性检查：
// 该文件可能含第三方启动器的账号数据，本包**绝不打开**它。
//
// 参数 path 为文件的绝对路径。
// 返回 true 表示该路径存在且不是目录。
func fileExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	// 同名目录占位不算「配置文件存在」：调用方需要区分这一点，
	// 所以这里要求必须是普通文件。
	return !info.IsDir()
}
