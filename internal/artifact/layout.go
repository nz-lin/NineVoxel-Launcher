package artifact

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"example.com/ninevoxel/launcher/internal/platform"
)

// 目录树常量：全部是**相对数据根**的 slash 形式路径（P3-03 卡面第 3 条）。
//
// 本包只定义这些路径，**不创建目录**：创建由 P3-06 在下载事务内完成，
// 这样可以避免「预览 / 解析阶段就污染用户数据目录」。
const (
	// DirArtifacts 是制品缓存根目录。
	DirArtifacts = "artifacts"

	// DirArtifactObjects 是共享内容寻址缓存目录。
	DirArtifactObjects = "artifacts/objects"

	// DirArtifactIndexes 是资产索引缓存目录（artifacts/indexes/<indexId>.json）。
	DirArtifactIndexes = "artifacts/indexes"

	// DirArtifactMetadata 是元数据缓存目录（版本清单与版本 JSON）。
	DirArtifactMetadata = "artifacts/metadata"

	// DirDownloads 是下载临时区根目录。
	DirDownloads = "downloads"

	// DirStaging 是下载 staging 目录（未完成的 .part 文件）。
	DirStaging = "downloads/staging"

	// DirQuarantine 是下载隔离目录（校验失败的文件）。
	DirQuarantine = "downloads/quarantine"

	// DirRuntimes 是 Java Runtime 安装根目录。
	DirRuntimes = "runtimes"

	// DirVersions 是原版版本私有目录根（versions/<versionId>/）。
	//
	// 只存放官方 Minecraft 版本的声明与客户端 JAR：这样它与官方 `.minecraft`
	// 布局一致，外部目录预览、第三方工具与手动搬运都不需要额外解释。
	DirVersions = "versions"

	// DirLoaders 是加载器版本私有目录根（loaders/<versionId>/）。
	//
	// 加载器版本的声明与 processor 产物由启动器自己生成，没有独立下载源，
	// 因此与官方原版目录分开：`versions/` 保持「官方布局」语义，`loaders/`
	// 表达「启动器派生」语义。两者都由核心清单管理，切换顺序见 core.rebuild。
	DirLoaders = "loaders"

	// DirLibraries 是共享依赖库目录（Maven 布局）。
	//
	// 这是**最终位置**，不是缓存：下载产物直接落在 `libraries/<maven path>`，
	// 不再经过内容寻址的中间层。游戏的 classpath 直接指向这里。
	DirLibraries = "libraries"

	// DirAssets 是共享游戏资源根目录。
	DirAssets = "assets"

	// DirAssetObjects 是资源对象目录（`assets/objects/<前两位>/<hash>`）。
	DirAssetObjects = "assets/objects"

	// DirAssetIndexes 是资源索引目录（`assets/indexes/<indexId>.json`）。
	DirAssetIndexes = "assets/indexes"

	// DirAssetLogConfigs 是日志配置目录（`assets/log_configs/<name>`）。
	DirAssetLogConfigs = "assets/log_configs"

	// DirCache 是可安全删除的缓存根目录。
	//
	// 与核心资源严格区分：这里的文件删掉只会让下次安装变慢，不会让已安装的
	// 版本失效。官方安装器本体与元数据文档放在这里。
	DirCache = "cache"

	// DirCacheInstallers 是官方加载器安装器的缓存目录。
	DirCacheInstallers = "cache/installers"

	// DirLogs 是日志目录（P1 的应用日志与 P2 的游戏日志）。
	DirLogs = "logs"
)

// 数据根解析用到的常量。
const (
	// dataDirName 是数据根目录名（用户配置目录下的子目录，或便携版 exe 同级目录）。
	dataDirName = "data"

	// writeProbePattern 是可写探测的临时文件名模板。
	writeProbePattern = ".artifact-write-probe-*"

	// duplicateSegment 是父目录段标记。
	duplicateSegment = ".."

	// nulCharacter 是不允许出现在路径中的 NUL 字符。
	nulCharacter = "\x00"
)

// DataRootInput 是解析数据根所需的运行环境事实。
//
// 该结构把「读环境」与「做判断」分离：DataRootInput 的字段全部由调用方（DataRoot）
// 采集，ResolveDataRoot 只做纯逻辑判断，因此单元测试不需要访问真实文件系统。
type DataRootInput struct {
	// UserDataDir 是 `<用户配置目录>/NineVoxel-Launcher/data`；空表示拿不到用户配置目录。
	UserDataDir string

	// UserDataWritable 表示 UserDataDir 是否可写（由 DataRoot 探测）。
	UserDataWritable bool

	// PortableDataDir 是可执行文件同级的 `data/` 目录；空表示拿不到可执行文件位置。
	PortableDataDir string

	// PortableDataExists 表示便携版 data 目录是否已存在。
	//
	// 它是「这是便携版」的判定依据：一旦存在，数据根就是它，
	// 不可写时按卡面要求**报错而不回退**用户目录。
	PortableDataExists bool

	// PortableDataWritable 表示 PortableDataDir 是否可写（由 DataRoot 探测）。
	PortableDataWritable bool
}

// ResolveDataRoot 按 P1 冻结的目录策略决定数据根（纯函数，便于单元测试）。
//
// 优先级（与 internal/settings.ResolveDatabasePath 一致，并补充便携版硬判定）：
//  1. 便携版：exe 同级 `data/` 已存在 → 用它；不可写则直接报错，**不回退**；
//  2. 用户配置目录 `<用户配置目录>/NineVoxel-Launcher/data`（安装版首选）；
//  3. exe 同级 `data/`（用户目录不可写时的回退）；
//  4. 都不可用 → 按原因返回 ARTIFACT_LAYOUT_001（未配置 / 不可写 / 路径非法）。
//
// 返回的是**绝对路径**：数据根本身不是「目标相对路径」，调用方需要它来拼绝对路径。
func ResolveDataRoot(in DataRootInput) (string, *Error) {
	// 第一步：便携版硬判定。
	//
	// 便携版的特征是 exe 同级已存在 data 目录。此时数据根只能是它：
	// 否则同一份便携目录在不同机器上会一半写 exe 目录、一半写用户目录，
	// 用户会看到「换机器后配置消失」。不可写必须显式失败（卡面第 2 条）。
	if in.PortableDataDir != "" && in.PortableDataExists {
		if !isUsableDataRoot(in.PortableDataDir) {
			return "", newLayoutError(DataRootReasonInvalidPath, messageDataRootInvalidPath)
		}
		if !in.PortableDataWritable {
			return "", newLayoutError(DataRootReasonNotWritable, messageDataRootPortableNotWritable)
		}
		return in.PortableDataDir, nil
	}

	// 第二步：用户配置目录（安装版首选）。
	if in.UserDataDir != "" {
		if !isUsableDataRoot(in.UserDataDir) {
			return "", newLayoutError(DataRootReasonInvalidPath, messageDataRootInvalidPath)
		}
		if in.UserDataWritable {
			return in.UserDataDir, nil
		}
	}

	// 第三步：exe 同级 data 目录（回退）。
	if in.PortableDataDir != "" {
		if !isUsableDataRoot(in.PortableDataDir) {
			return "", newLayoutError(DataRootReasonInvalidPath, messageDataRootInvalidPath)
		}
		if in.PortableDataWritable {
			return in.PortableDataDir, nil
		}
	}

	// 第四步：区分「拿不到位置」与「位置不可写」。
	if in.UserDataDir == "" && in.PortableDataDir == "" {
		return "", newLayoutError(DataRootReasonNotConfigured, messageDataRootNotConfigured)
	}
	return "", newLayoutError(DataRootReasonNotWritable, messageDataRootNotWritable)
}

// DataRoot 解析并返回数据根的绝对路径。
//
// 该函数是包内唯一读取运行环境的地方：它采集用户配置目录、可执行文件位置，
// 并对候选目录做**不创建目录**的可写探测，然后交给纯函数 ResolveDataRoot 判定。
//
// 注意：本函数**不会**创建数据根目录，也不会创建任何子树。数据根及其子目录
// 由 P3-06 在下载事务内创建；本函数只回答「应该落在哪里」以及「那里是否可用」。
func DataRoot() (string, *Error) {
	return ResolveDataRoot(collectDataRootInput())
}

// collectDataRootInput 采集解析数据根所需的运行环境事实。
//
// 采集失败（例如拿不到可执行文件位置）不视为错误，对应字段留空，
// 由 ResolveDataRoot 统一按「未配置 / 不可写」给出结论。
func collectDataRootInput() DataRootInput {
	input := DataRootInput{}

	if appRoot := platform.AppRoot(); appRoot != "" {
		input.UserDataDir = filepath.Join(appRoot, dataDirName)
		_, input.UserDataWritable = directoryWritable(input.UserDataDir)
	}

	if exePath, err := os.Executable(); err == nil && exePath != "" {
		input.PortableDataDir = filepath.Join(filepath.Dir(exePath), dataDirName)
		input.PortableDataExists, input.PortableDataWritable = directoryWritable(input.PortableDataDir)
	}

	return input
}

// directoryWritable 判定候选数据目录当前是否可用。
//
// 与 platform.ProbeWritable 的差别：本函数**不会创建目录**。
//   - 目录已存在：创建并删除一个临时文件做真实写入探测（只读安装目录能因此被发现）；
//   - 目录不存在：向上找最近已存在的祖先目录做同样探测，表示「数据根尚待创建」。
//
// 返回 (是否存在, 是否可写)。两者都为 false 时表示整条祖先链都不可写。
func directoryWritable(dir string) (bool, bool) {
	cleaned := filepath.Clean(dir)
	exists := true

	for {
		info, err := os.Stat(cleaned)
		if err == nil {
			if !info.IsDir() {
				// 同名文件占位：既不是目录也不可写。
				return exists, false
			}
			return exists, probeWritable(cleaned)
		}

		exists = false

		parent := filepath.Dir(cleaned)
		if parent == cleaned {
			// 到达卷根仍不存在：没有任何可写祖先。
			return false, false
		}
		cleaned = parent
	}
}

// probeWritable 在已存在的目录内做一次写入探测。
//
// 只创建并删除临时文件，不创建目录。探测失败一律返回 false：
// 数据目录不可写属于「环境不可用」，调用方应据此报 ARTIFACT_LAYOUT_001。
func probeWritable(dir string) bool {
	probe, err := os.CreateTemp(dir, writeProbePattern)
	if err != nil {
		return false
	}

	name := probe.Name()
	// 先关闭再删除：Windows 不允许删除仍处于打开状态的文件。
	if err := probe.Close(); err != nil {
		_ = os.Remove(name)
		return false
	}
	_ = os.Remove(name)
	return true
}

// isUsableDataRoot 判断数据根路径本身是否合法。
//
// 合法要求：非空、绝对路径、不含 NUL、不含 `..` 段。
// 注意这里**不判断可写性**，可写性由调用方根据探测结果单独判断。
func isUsableDataRoot(dir string) bool {
	trimmed := strings.TrimSpace(dir)
	if trimmed == "" {
		return false
	}
	if strings.Contains(trimmed, nulCharacter) {
		return false
	}
	if !filepath.IsAbs(trimmed) {
		return false
	}
	return !hasParentSegment(trimmed)
}

// ResolveTarget 把逻辑目标解析成**相对数据根**的确定路径（P3-03 卡面第 5 条）。
//
// 参数 kind 为制品类别；planRelativePath 为计划给出的相对路径片段
// （例如 library 的 maven 相对路径 `com/mojang/.../foo.jar`、
// client 的 `1.21/1.21.jar`、runtime 的 `jre-legacy/bin/java.exe`）。
//
// 返回值为 slash 形式的相对路径，**不是绝对路径**：
// 绝对路径由调用方用 filepath.Join(DataRoot(), filepath.FromSlash(rel)) 拼接，
// 这样测试才能跨机器断言，也避免路径拼接分散到各处。
//
// 规则写死（不允许调用方覆盖）：
//   - library / native / asset → artifacts/objects/...（共享内容寻址）
//   - client → versions/...（版本私有）
//   - runtime → runtimes/...
//   - mod / resource_pack / shader / plugin → 拒绝：P3 不写实例目录（决策 D-29）
//
// 任何越界、绝对路径、盘符、UNC、NUL、`..`、空相对路径一律返回 ARTIFACT_PATH_001。
func ResolveTarget(kind ArtifactKind, planRelativePath string) (string, *Error) {
	root, err := targetRoot(kind)
	if err != nil {
		return "", err
	}

	cleaned, err := ValidateRelativePath(planRelativePath)
	if err != nil {
		return "", err
	}

	return root + "/" + cleaned, nil
}

// targetRoot 返回某类制品的相对目标根。
//
// 该表是「路径规则表」在代码里的唯一实现：新增 kind 必须同时更新 doc.go 的表格，
// 不允许在调用点各自拼接前缀。
func targetRoot(kind ArtifactKind) (string, *Error) {
	switch kind {
	case KindLibrary, KindNative, KindAsset:
		return DirArtifactObjects, nil
	case KindClient:
		return DirVersions, nil
	case KindRuntime:
		return DirRuntimes, nil
	case KindMod, KindResourcePack, KindShader, KindPlugin:
		return "", newInputError(ErrorCodeArtifactPath, messageKindUnsupported)
	case "":
		return "", newInputError(ErrorCodeArtifactPath, messageKindUnknown)
	default:
		return "", newInputError(ErrorCodeArtifactPath, messageKindUnknown)
	}
}

// IsP3TargetKind 判断某类制品在 P3 是否拥有确定的目标路径。
//
// mod / resource_pack / shader / plugin 属实例级内容（P5），P3 只建模不落盘。
func IsP3TargetKind(kind ArtifactKind) bool {
	switch kind {
	case KindClient, KindLibrary, KindAsset, KindNative, KindRuntime:
		return true
	default:
		return false
	}
}

// ObjectRelPath 生成共享内容寻址对象的相对路径。
//
// 形态固定为 `artifacts/objects/<hash 前 2 位>/<hash>`（卡面第 4 条）。
//
// 算法必须由调用方显式给出（决策 D-27）：资产用 SHA-1，其余优先 SHA-256；
// SHA-512 **不能**作为寻址键，传入会返回错误，避免「用 SHA-512 冒充 SHA-256」。
func ObjectRelPath(algorithm HashAlgorithm, hash string) (string, *Error) {
	normalized, err := normalizeHash(algorithm, hash)
	if err != nil {
		return "", err
	}
	return DirArtifactObjects + "/" + normalized[:2] + "/" + normalized, nil
}

// normalizeHash 校验并归一化哈希值。
//
// 只接受小写十六进制；大写会被拒绝而不是自动转换：哈希值来自元数据，
// 大小写不一致通常意味着上游数据异常，静默转换会掩盖问题（与 D-27 的
// 「严禁把 SHA-1 填进 SHA-256 字段」同一考虑）。
func normalizeHash(algorithm HashAlgorithm, hash string) (string, *Error) {
	var expectedLength int
	switch algorithm {
	case HashAlgorithmSHA1:
		expectedLength = 40
	case HashAlgorithmSHA256:
		expectedLength = 64
	default:
		return "", newInputError(ErrorCodeArtifactPath, messageHashAlgorithm)
	}

	trimmed := strings.TrimSpace(hash)
	if len(trimmed) != expectedLength {
		return "", newInputError(ErrorCodeArtifactPath, messageHashFormat)
	}
	for _, char := range trimmed {
		if !isLowerHexDigit(char) {
			return "", newInputError(ErrorCodeArtifactPath, messageHashFormat)
		}
	}
	return trimmed, nil
}

// isLowerHexDigit 判断字符是否为小写十六进制数字。
func isLowerHexDigit(char rune) bool {
	return (char >= '0' && char <= '9') || (char >= 'a' && char <= 'f')
}

// IndexRelPath 生成资产索引缓存的相对路径（`artifacts/indexes/<indexId>.json`）。
func IndexRelPath(indexID string) (string, *Error) {
	cleaned, err := validateSimpleName(indexID)
	if err != nil {
		return "", err
	}
	return DirArtifactIndexes + "/" + cleaned + ".json", nil
}

// MetadataRelPath 生成元数据缓存的相对路径（`artifacts/metadata/<fileName>`）。
func MetadataRelPath(fileName string) (string, *Error) {
	cleaned, err := validateSimpleName(fileName)
	if err != nil {
		return "", err
	}
	return DirArtifactMetadata + "/" + cleaned, nil
}

// VersionRelPath 生成原版版本私有文件的相对路径（`versions/<versionId>/<fileName>`）。
func VersionRelPath(versionID, fileName string) (string, *Error) {
	return versionRelPathIn(DirVersions, versionID, fileName)
}

// LoaderVersionRelPath 生成加载器版本私有文件的相对路径（`loaders/<versionId>/<fileName>`）。
//
// 加载器版本（Fabric / Quilt / Forge / NeoForge）的声明与处理器产物由启动器
// 生成，统一落在 `loaders/`，不污染原版 `versions/`。
func LoaderVersionRelPath(versionID, fileName string) (string, *Error) {
	return versionRelPathIn(DirLoaders, versionID, fileName)
}

// versionRelPathIn 生成指定版本根下的文件相对路径。
func versionRelPathIn(dirName, versionID, fileName string) (string, *Error) {
	id, err := validateSimpleName(versionID)
	if err != nil {
		return "", err
	}
	name, err := validateSimpleName(fileName)
	if err != nil {
		return "", err
	}
	return dirName + "/" + id + "/" + name, nil
}

// VersionDirRel 返回原版版本目录相对核心根的路径（`versions/<versionId>`）。
func VersionDirRel(versionID string) (string, *Error) {
	id, err := validateSimpleName(versionID)
	if err != nil {
		return "", err
	}
	return DirVersions + "/" + id, nil
}

// LoaderVersionDirRel 返回加载器版本目录相对核心根的路径（`loaders/<versionId>`）。
func LoaderVersionDirRel(versionID string) (string, *Error) {
	id, err := validateSimpleName(versionID)
	if err != nil {
		return "", err
	}
	return DirLoaders + "/" + id, nil
}

// ResolveVersionDir 解析某个版本实际位于哪个版本根，返回相对核心根的目录。
//
// 返回 (相对目录, 是否存在, 错误)。错误只在版本 ID 非法时产生：目录不存在是
// 正常状态（版本未安装），由调用方按「未安装」处理。
//
// 探测顺序固定为 `loaders/` → `versions/`：
//   - 加载器版本由启动器自己创建，只会出现在 `loaders/`；
//   - 原版版本只会出现在 `versions/`；
//   - 两者同名意味着数据异常，此时优先返回 `loaders/`（后者的声明指向原版父
//     版本，取错会得到更隐蔽的失败），并把冲突留给上层校验暴露。
//
// 刻意不按版本 ID 的命名推断加载器：`1.20.1-forge-47.2.0` 这类命名是
// 启动器自己的约定，不是可依赖的契约。
func ResolveVersionDir(root, versionID string) (string, bool, *Error) {
	loaderRel, err := LoaderVersionDirRel(versionID)
	if err != nil {
		return "", false, err
	}
	vanillaRel, err := VersionDirRel(versionID)
	if err != nil {
		return "", false, err
	}
	candidates := []string{loaderRel, vanillaRel}

	for _, relative := range candidates {
		absolute, resolveErr := ResolveInsideRoot(root, relative)
		if resolveErr != nil {
			continue
		}
		info, statErr := os.Stat(absolute)
		if statErr == nil && info.IsDir() {
			return relative, true, nil
		}
	}
	return "", false, nil
}

// LibraryRelPath 生成依赖库在最终布局中的相对路径（`libraries/<maven path>`）。
//
// 参数 mavenPath 是 Maven 坐标对应的相对路径（如 `com/google/guava/1.0/x.jar`），
// 来自版本 JSON 的 `downloads.artifact.path`；缺省时可由坐标推导。
//
// 让下载直接落在 Maven 布局而不是内容寻址目录，是为了去掉「缓存 + 复制」
// 这一层：classpath 指向的位置就是文件存放的位置。
func LibraryRelPath(mavenPath string) (string, *Error) {
	cleaned, err := ValidateRelativePath(mavenPath)
	if err != nil {
		return "", err
	}
	return DirLibraries + "/" + cleaned, nil
}

// AssetObjectRelPath 生成资源对象在最终布局中的相对路径。
//
// 参数 hash 是对象的 SHA-1（十六进制小写）。资源对象天然按内容寻址，
// 多个版本引用同一对象时路径相同，因此不需要额外去重。
func AssetObjectRelPath(hash string) (string, *Error) {
	normalized, err := normalizeHash(HashAlgorithmSHA1, hash)
	if err != nil {
		return "", err
	}
	return DirAssetObjects + "/" + normalized[:2] + "/" + normalized, nil
}

// AssetIndexRelPath 生成资源索引在最终布局中的相对路径。
func AssetIndexRelPath(indexID string) (string, *Error) {
	cleaned, err := validateSimpleName(indexID)
	if err != nil {
		return "", err
	}
	return DirAssetIndexes + "/" + cleaned + ".json", nil
}

// LogConfigRelPath 生成日志配置在最终布局中的相对路径。
func LogConfigRelPath(name string) (string, *Error) {
	cleaned, err := validateSimpleName(name)
	if err != nil {
		return "", err
	}
	return DirAssetLogConfigs + "/" + cleaned, nil
}

// InstallerCacheRelPath 生成官方安装器在缓存目录中的相对路径。
//
// 参数 sha1 是安装器文件的 SHA-1。放在缓存而不是核心：安装器只在安装时用一次，
// 删掉不影响已安装的版本。
func InstallerCacheRelPath(sha1 string) (string, *Error) {
	normalized, err := normalizeHash(HashAlgorithmSHA1, sha1)
	if err != nil {
		return "", err
	}
	return DirCacheInstallers + "/" + normalized + ".jar", nil
}

// RuntimeRelPath 生成 Runtime 安装目录内文件的相对路径（`runtimes/<runtimeId>/<relPath>`）。
//
// relPath 允许包含子目录（如 `bin/java.exe`），但同样要通过根内路径校验。
func RuntimeRelPath(runtimeID, relPath string) (string, *Error) {
	id, err := validateSimpleName(runtimeID)
	if err != nil {
		return "", err
	}
	cleaned, err := ValidateRelativePath(relPath)
	if err != nil {
		return "", err
	}
	return DirRuntimes + "/" + id + "/" + cleaned, nil
}

// StagingRelPath 生成下载临时文件的相对路径（`downloads/staging/<taskId>.part`）。
func StagingRelPath(taskID string) (string, *Error) {
	cleaned, err := validateSimpleName(taskID)
	if err != nil {
		return "", err
	}
	return DirStaging + "/" + cleaned + ".part", nil
}

// QuarantineRelPath 生成隔离文件的相对路径（`downloads/quarantine/<taskId>.bad`）。
//
// 目录按 taskId 前两位分组，避免单目录堆积大量文件（与内容寻址缓存同一考虑）。
func QuarantineRelPath(taskID string) (string, *Error) {
	cleaned, err := validateSimpleName(taskID)
	if err != nil {
		return "", err
	}
	bucket := cleaned
	if len(bucket) > 2 {
		bucket = bucket[:2]
	}
	return DirQuarantine + "/" + bucket + "/" + cleaned + ".bad", nil
}

// ValidateRelativePath 校验并归一化一个相对路径。
//
// 这是本包的**统一入口**：所有相对路径（不论是计划给出的还是内部拼出的）
// 都必须经过它。语义与 P2 的 resolveInside 对齐（见 doc.go 的对照表），差别只有一点：
// 本函数把空相对路径视为非法，因为「目标」必须指向具体文件或目录，
// 而 resolveInside 把空路径解释为「根目录本身」是读取场景的需要。
//
// 返回值为 slash 形式的归一化相对路径（`a//b` → `a/b`）。
func ValidateRelativePath(rel string) (string, *Error) {
	trimmed := strings.TrimSpace(rel)
	if trimmed == "" {
		return "", newInputError(ErrorCodeArtifactPath, messagePathEmpty)
	}

	// NUL 必须先于任何路径操作拒绝，否则底层系统调用可能截断后绕过校验。
	if strings.Contains(trimmed, nulCharacter) {
		return "", newInputError(ErrorCodeArtifactPath, messagePathNUL)
	}

	// 绝对路径注入：Windows 盘符、UNC 前缀与 POSIX 根都要拒绝。
	// filepath.IsAbs 在 Windows 上对 `/x` 返回 false（会被当作「当前盘相对」），
	// 因此还要单独检查前导分隔符。
	if filepath.IsAbs(trimmed) || hasVolumePrefix(trimmed) || startsWithSeparator(trimmed) {
		return "", newInputError(ErrorCodeArtifactPath, messagePathVolumeOrAbsolute(trimmed))
	}

	if hasParentSegment(trimmed) {
		return "", newInputError(ErrorCodeArtifactPath, messagePathParentSegment)
	}

	// 统一成正斜杠后再 Clean：Windows 允许 `\` 与 `/` 混用，
	// 不先转换会导致同一路径在两种分隔符下产生两份缓存。
	// filepath.FromSlash 在 POSIX 上不会处理反斜杠，因此这里必须显式替换。
	portable := strings.ReplaceAll(trimmed, "\\", "/")
	slash := filepath.ToSlash(filepath.Clean(filepath.FromSlash(portable)))
	if slash == "." {
		return "", newInputError(ErrorCodeArtifactPath, messagePathEmpty)
	}

	// Clean 之后仍以 `..` 开头说明归一化后越界（防御未预见的分隔符花样）。
	if slash == duplicateSegment || strings.HasPrefix(slash, duplicateSegment+"/") {
		return "", newInputError(ErrorCodeArtifactPath, messagePathParentSegment)
	}

	return slash, nil
}

// messagePathVolumeOrAbsolute 按形态选择「绝对路径」或「盘符 / UNC」文案。
func messagePathVolumeOrAbsolute(value string) string {
	if hasVolumePrefix(value) || startsWithSeparator(value) {
		return messagePathVolume
	}
	return messagePathAbsolute
}

// validateSimpleName 校验标识符（版本 id、indexId、文件名等）是否可安全用作路径段。
//
// 拒绝：空、NUL、路径分隔符、冒号（Windows 盘符与 NTFS 流）、`..`、前导分隔符。
// 允许点号（例如 `1.21`、`version_manifest_v2.json`）。
func validateSimpleName(value string) (string, *Error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", newInputError(ErrorCodeArtifactPath, messageSimpleNameInvalid)
	}
	if strings.Contains(trimmed, nulCharacter) {
		return "", newInputError(ErrorCodeArtifactPath, messagePathNUL)
	}
	if strings.ContainsAny(trimmed, `/\:`) {
		return "", newInputError(ErrorCodeArtifactPath, messageSimpleNameInvalid)
	}
	// `..` 与 `.` 不能作为路径段。
	if trimmed == duplicateSegment || trimmed == "." {
		return "", newInputError(ErrorCodeArtifactPath, messageSimpleNameInvalid)
	}
	return trimmed, nil
}

// ResolveInsideRoot 把相对路径安全地解析到指定根目录内部，返回绝对路径。
//
// 语义与 P2 的 resolveInside 逐条对齐（见 doc.go 对照表）：
//   - 空相对路径合法，返回根目录本身；
//   - NUL / 绝对路径 / 盘符 / 前导分隔符 / `..` 段一律拒绝；
//   - 拼接后用 EnsureWithinRoot 做 `filepath.Rel` 边界校验（不是字符串前缀）；
//   - 目标已存在时用 EvalSymlinks 解析真实路径并**再次**校验，防止
//     `root/link -> C:\Windows` 这类符号链接逃逸；
//   - 目标不存在时只要父目录链在根内即视为安全（写入也只能落在根内）。
//
// 该函数供 P3-06 的提交阶段使用；本包自身不调用它，因此不会产生文件系统写入。
func ResolveInsideRoot(root, rel string) (string, *Error) {
	trimmedRoot := strings.TrimSpace(root)
	if trimmedRoot == "" {
		return "", newInputError(ErrorCodeArtifactPath, messageRootEmpty)
	}
	if !filepath.IsAbs(trimmedRoot) {
		return "", newInputError(ErrorCodeArtifactPath, messageRootRelative)
	}

	if rel == "" {
		return trimmedRoot, nil
	}

	// 复用统一校验：NUL、绝对路径、盘符、UNC、`..`。
	cleaned, err := ValidateRelativePath(rel)
	if err != nil {
		return "", err
	}

	joined := filepath.Clean(filepath.Join(trimmedRoot, filepath.FromSlash(cleaned)))
	if err := EnsureWithinRoot(trimmedRoot, joined); err != nil {
		return "", err
	}
	// macOS commonly exposes temporary paths through aliases such as
	// /var -> /private/var. Compare resolved paths against the resolved root,
	// otherwise an internal symlink can look like an escape.
	rootBoundary := filepath.Clean(trimmedRoot)
	if resolvedRoot, evalErr := filepath.EvalSymlinks(trimmedRoot); evalErr == nil {
		rootBoundary = filepath.Clean(resolvedRoot)
	}

	// Missing leaves still require validating the nearest existing ancestor.
	// An unresolved link is rejected rather than treated as a missing directory.
	for ancestor := joined; ; ancestor = filepath.Dir(ancestor) {
		resolved, evalErr := filepath.EvalSymlinks(ancestor)
		if evalErr == nil {
			if err := EnsureWithinRoot(rootBoundary, resolved); err != nil {
				return "", err
			}
			suffix, relErr := filepath.Rel(ancestor, joined)
			if relErr != nil {
				return "", newInputError(ErrorCodeArtifactPath, messagePathEscape)
			}
			resolvedCandidate := filepath.Join(resolved, suffix)
			relativeToRoot, relErr := filepath.Rel(rootBoundary, resolvedCandidate)
			if relErr != nil {
				return "", newInputError(ErrorCodeArtifactPath, messagePathEscape)
			}
			// Preserve the caller's root spelling (/var vs /private/var) while
			// still returning the symlink-resolved internal target.
			return filepath.Join(trimmedRoot, relativeToRoot), nil
		}
		if info, statErr := os.Lstat(ancestor); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
			return "", newInputError(ErrorCodeArtifactPath, messagePathEscape)
		}
		if !os.IsNotExist(evalErr) {
			return "", newInputError(ErrorCodeArtifactPath, messagePathEscape)
		}
		if ancestor == filepath.Clean(trimmedRoot) {
			return joined, nil
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return "", newInputError(ErrorCodeArtifactPath, messagePathEscape)
		}
	}

}

// EnsureWithinRoot 校验绝对路径 candidate 是否确实位于 root 之内。
//
// 使用 filepath.Rel 而不是字符串前缀比较：前缀比较会把 `C:\game2` 误判为
// `C:\game` 的子路径（经典边界漏洞，P2-03 已踩过一次，卡面完成标准第 3 条点名）。
// Rel 返回以 `..` 开头或本身为 `..` 时说明 candidate 在 root 之外；
// Rel 出错说明两者不在同一卷（例如 root 在 C:、candidate 在 D:），同样越界。
//
// Windows 文件系统不区分大小写，比较前统一转小写，避免 `C:\Game` 与 `c:\game`
// 被误判为不同路径。
func EnsureWithinRoot(root, candidate string) *Error {
	rootClean := filepath.Clean(root)
	candidateClean := filepath.Clean(candidate)

	if runtime.GOOS == "windows" {
		rootClean = strings.ToLower(rootClean)
		candidateClean = strings.ToLower(candidateClean)
	}

	relativePath, err := filepath.Rel(rootClean, candidateClean)
	if err != nil {
		return newInputError(ErrorCodeArtifactPath, messagePathEscape)
	}

	if relativePath == "." {
		return nil
	}

	if relativePath == duplicateSegment || strings.HasPrefix(relativePath, duplicateSegment+string(filepath.Separator)) {
		return newInputError(ErrorCodeArtifactPath, messagePathEscape)
	}

	return nil
}

// hasVolumePrefix 判断路径是否带盘符前缀（`C:foo` / `C:\foo`）。
//
// 盘符相对路径既不是绝对路径也不在根内，必须单独拒绝：
// 它会改变解析卷，绕过「相对根目录」的边界。
func hasVolumePrefix(value string) bool {
	if len(value) < 2 {
		return false
	}
	if value[1] != ':' {
		return false
	}
	char := value[0]
	return (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z')
}

// startsWithSeparator 判断路径是否以路径分隔符开头。
//
// 覆盖 POSIX 的 `/x` 与 Windows 的 `\x`：两者在拼接语义上都表示「回到根」，
// 而不是「相对于数据根」。UNC（`\\server\share`）也由此一并拒绝。
func startsWithSeparator(value string) bool {
	if value == "" {
		return false
	}
	return value[0] == '/' || value[0] == '\\'
}

// hasParentSegment 判断路径是否包含 `..` 段。
//
// 逐段检查而不是字符串包含判断：后者会把正常文件名 `..bashrc` 或 `foo..bar`
// 误判成穿越。Windows 允许 `\` 与 `/` 混用，因此两种分隔符都要切分。
func hasParentSegment(value string) bool {
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
