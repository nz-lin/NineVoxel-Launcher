package loader

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"example.com/ninevoxel/launcher/internal/artifact"
	"example.com/ninevoxel/launcher/internal/download"
)

// 本文件实现 Forge / NeoForge 安装编排的「规划」阶段：读懂官方安装器里的
// `install_profile.json` 与 `version.json`，把「要下载什么、要跑什么」解析成
// 一份可执行的计划。
//
// 为什么自己编排而不执行官方安装器的 main class：
//
//	官方安装器的命令行入口（`--installClient`）是上游可能移除的实现细节——
//	第三方启动器因此写了各种包装器来绕开它。编排 processor 用的是安装器
//	**自带**的 processor jar，仍然完全是官方机制，只是不再依赖那个入口。
//
// 这里只做规划，不执行进程：下载要走 download 队列（进度、续传、校验），
// 进程执行由调用方按阶段推进。loader 包不引入下载队列的依赖。

const (
	// forgeProfileMaxBytes 是 install_profile.json 的允许上限。
	//
	// 正常文件几十 KB；上限取得宽松一些以容纳整合包作者的定制版本，
	// 同时拦住被异常写坏的超大文件。
	forgeProfileMaxBytes = 8 << 20

	// forgeVersionJSONMaxBytes 是安装器内 version.json 的允许上限。
	forgeVersionJSONMaxBytes = 32 << 20

	// forgeMaxProcessors 是允许的 processor 数量上限。
	//
	// 现代 Forge 只有 5 个左右；留出余量但不接受无上限，
	// 避免一份异常 profile 让安装跑上千次 JVM。
	forgeMaxProcessors = 64
)

// 用于解析 processor 依赖的 Maven 仓库候选。
//
// Forge 与 NeoForge 互为 fork，依赖会互相镜像，但顺序不同：优先各自的官方
// 仓库，另一家作为回落，避免因单一仓库缺件而失败。
var (
	forgeProcessorMavenBases = []string{
		"https://maven.minecraftforge.net/",
		"https://maven.neoforged.net/releases/",
	}
	neoforgeProcessorMavenBases = []string{
		"https://maven.neoforged.net/releases/",
		"https://maven.minecraftforge.net/",
	}
)

// minecraftJarFileName 是交给 processor 的原版 jar 在临时目录里的名字。
//
// 名字固定为 `minecraft.jar`：部分 processor 会据此推断目标布局。
const minecraftJarFileName = "minecraft.jar"

// ProcessorJar 是一个 processor 运行时依赖。
//
// 这些 jar 不在 version.json 的 libraries 里，只在 install_profile.json 的
// `jar` 与 `classpath` 中出现，因此必须单独解析与下载。
type ProcessorJar struct {
	// Coordinate 是 Maven 坐标（`group:artifact:version`）。
	Coordinate string
	// MavenBase 是提供该依赖的仓库根地址。
	MavenBase string
	// URL 是下载地址。
	URL string
	// SHA1 是官方 Maven 提供的校验值。
	SHA1 string
	// Size 是字节数。
	Size int64
	// TargetRelPath 是相对数据根的位置。
	//
	// 放在 `cache/` 而不是 `libraries/`：processor 依赖只在安装期使用，
	// 不属于任何已安装版本的组成部分，不应进入安装清单。
	TargetRelPath string
}

// ProcessorOutput 是一个 processor 的产出。
type ProcessorOutput struct {
	// AbsolutePath 是产物的绝对路径。
	AbsolutePath string
	// SHA1 是期望的 SHA-1（小写）。
	SHA1 string
}

// ProcessorStep 是一次 processor 执行。
type ProcessorStep struct {
	// JarCoordinate 是被执行的 processor jar。
	JarCoordinate string
	// ClasspathCoordinates 是额外的 classpath 依赖。
	ClasspathCoordinates []string
	// Args 是替换完占位符的完整参数列表。
	Args []string
	// Outputs 是该 processor 的产出，用于校验与增量跳过。
	Outputs []ProcessorOutput
	// Sides 是声明支持的侧（此处只会包含 client）。
	Sides []string
}

// ForgeInstallPlan 是一次 Forge / NeoForge 安装的完整计划。
type ForgeInstallPlan struct {
	// VersionID 是产出该加载器后的版本标识。
	VersionID string
	// MCVersion 是 profile 声明的 Minecraft 版本。
	MCVersion string
	// VersionJSON 是安装器内嵌的 version.json 原始字节，需写入版本目录。
	VersionJSON []byte
	// Libraries 是加载器自身的依赖库下载项。
	//
	// 不含由 processor 生成的产物（那些在 version.json 里没有下载地址）。
	Libraries []download.PlanItem
	// ProcessorJars 是 processor 运行时依赖。
	ProcessorJars []ProcessorJar
	// Processors 是按序执行的 client 侧 processor。
	Processors []ProcessorStep
	// TempDir 是安装期临时目录；调用方在执行结束后必须整体删除。
	TempDir string
	// MinecraftJarSource 是原版客户端 jar 的绝对路径。
	//
	// 执行阶段会把它**复制**到 TempDir 后再交给 processor：processor 会向
	// 输入 jar 旁写标记文件，直接传共享缓存里的那份会污染其他实例共用的
	// 原版 jar。
	MinecraftJarSource string
}

// forgeInstallProfile 是 install_profile.json 的映射。
type forgeInstallProfile struct {
	Spec       int                          `json:"spec"`
	Profile    string                       `json:"profile"`
	Version    string                       `json:"version"`
	Path       string                       `json:"path"`
	Minecraft  string                       `json:"minecraft"`
	Data       map[string]forgeProfileValue `json:"data"`
	Processors []forgeProcessorDocument     `json:"processors"`
}

// forgeProfileValue 是 data 条目的双侧取值。
type forgeProfileValue struct {
	Client string `json:"client"`
	Server string `json:"server"`
}

// forgeProcessorDocument 是 processors 数组的元素。
type forgeProcessorDocument struct {
	Sides     []string          `json:"sides"`
	Jar       string            `json:"jar"`
	Classpath []string          `json:"classpath"`
	Args      []string          `json:"args"`
	Outputs   map[string]string `json:"outputs"`
}

// forgeVersionDocument 是安装器内 version.json 里与安装相关的字段。
type forgeVersionDocument struct {
	ID           string                 `json:"id"`
	InheritsFrom string                 `json:"inheritsFrom"`
	Libraries    []forgeLibraryDocument `json:"libraries"`
}

// forgeLibraryDocument 是 version.json 的一条库记录。
type forgeLibraryDocument struct {
	Name      string `json:"name"`
	Downloads *struct {
		Artifact *struct {
			Path string `json:"path"`
			URL  string `json:"url"`
			SHA1 string `json:"sha1"`
			Size int64  `json:"size"`
		} `json:"artifact"`
	} `json:"downloads"`
}

// PlanForgeInstall 解析官方安装器并生成安装计划。
//
// 参数 kind 决定优先使用哪个 Maven 仓库解析 processor 依赖；root 为核心根目录
// （= 数据根）；installerPath 为已通过校验的安装器。
//
// 返回的计划已经把全部占位符替换成绝对路径，执行阶段不再做路径推断。
// 调用方必须在结束后删除 plan.TempDir。
//
// 参数 minecraftJarPath 是原版客户端 jar 的绝对路径；processor 会拿到它的
// 隔离副本，见 ForgeInstallPlan.MinecraftJarSource。
func PlanForgeInstall(ctx context.Context, kind Kind, root, installerPath, minecraftJarPath string) (ForgeInstallPlan, *Error) {
	if !filepath.IsAbs(minecraftJarPath) {
		return ForgeInstallPlan{}, installerError("原版客户端文件路径无效，无法安装加载器。", false)
	}
	if info, statErr := os.Stat(minecraftJarPath); statErr != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		return ForgeInstallPlan{}, installerError("原版客户端文件缺失，请先完成原版安装。", true)
	}
	archive, openErr := zip.OpenReader(installerPath)
	if openErr != nil {
		return ForgeInstallPlan{}, installerError("官方安装器文件已损坏，请重新下载后重试。", true)
	}
	defer archive.Close()

	profileBody, readErr := readArchiveEntry(archive.File, "install_profile.json", forgeProfileMaxBytes)
	if readErr != nil {
		return ForgeInstallPlan{}, installerError("官方安装器缺少安装配置，可能不是有效的加载器安装器。", false)
	}
	versionBody, readErr := readArchiveEntry(archive.File, "version.json", forgeVersionJSONMaxBytes)
	if readErr != nil {
		return ForgeInstallPlan{}, installerError("官方安装器缺少版本描述，无法确定加载器组成。", false)
	}

	var profile forgeInstallProfile
	if jsonErr := json.Unmarshal(profileBody, &profile); jsonErr != nil {
		return ForgeInstallPlan{}, installerError("官方安装器的安装配置格式不正确。", false)
	}
	var versionDocument forgeVersionDocument
	if jsonErr := json.Unmarshal(versionBody, &versionDocument); jsonErr != nil {
		return ForgeInstallPlan{}, installerError("官方安装器的版本描述格式不正确。", false)
	}

	// spec 1 才是 processor 机制。更早的版本没有 processors 字段，
	// 通过解压 universal jar 安装，属于另一条路径，不在本实现范围内。
	if profile.Spec != 1 {
		return ForgeInstallPlan{}, installerError("该加载器版本使用旧的安装格式，暂不支持。", false)
	}
	if !safeVersionID(profile.Version) || !safeVersionID(versionDocument.ID) {
		return ForgeInstallPlan{}, installerError("官方安装器的版本标识无效。", false)
	}
	if profile.Version != versionDocument.ID {
		return ForgeInstallPlan{}, installerError("官方安装器的版本标识不一致，文件可能已损坏。", false)
	}
	if strings.TrimSpace(profile.Minecraft) == "" || versionDocument.InheritsFrom != profile.Minecraft {
		return ForgeInstallPlan{}, installerError("官方安装器的继承版本不一致，文件可能已损坏。", false)
	}
	if len(profile.Processors) > forgeMaxProcessors {
		return ForgeInstallPlan{}, installerError("官方安装器的处理器数量异常。", false)
	}

	plan := ForgeInstallPlan{
		VersionID:          versionDocument.ID,
		MCVersion:          profile.Minecraft,
		VersionJSON:        versionBody,
		MinecraftJarSource: minecraftJarPath,
	}

	resolver, resolveErr := newForgeValueResolver(root, installerPath, minecraftJarPath, profile)
	if resolveErr != nil {
		return ForgeInstallPlan{}, resolveErr
	}
	plan.TempDir = resolver.extractDir
	// 失败路径由调用方通过 plan.TempDir 清理；这里不 defer，
	// 因为成功时临时目录还要留给执行阶段使用。

	if err := planLoaderLibraries(&plan); err != nil {
		return ForgeInstallPlan{}, err
	}

	bases := forgeProcessorMavenBases
	if kind == KindNeoForge {
		bases = neoforgeProcessorMavenBases
	}

	// processor 依赖去重：同一坐标可能出现在多个 processor 的 classpath 里。
	jarIndex := make(map[string]int, 8)
	addJar := func(coordinate string) *Error {
		coordinate = strings.TrimSpace(coordinate)
		if coordinate == "" {
			return nil
		}
		if _, exists := jarIndex[coordinate]; exists {
			return nil
		}
		jar, jarErr := resolveProcessorJarDep(ctx, root, kind, profile.Minecraft, coordinate, bases)
		if jarErr != nil {
			return jarErr
		}
		jarIndex[coordinate] = len(plan.ProcessorJars)
		plan.ProcessorJars = append(plan.ProcessorJars, jar)
		return nil
	}

	for _, document := range profile.Processors {
		if !forgeProcessorAppliesToClient(document.Sides) {
			continue
		}
		if addErr := addJar(document.Jar); addErr != nil {
			return ForgeInstallPlan{}, addErr
		}
		for _, coordinate := range document.Classpath {
			if addErr := addJar(coordinate); addErr != nil {
				return ForgeInstallPlan{}, addErr
			}
		}
		step, stepErr := resolver.processorStep(document)
		if stepErr != nil {
			return ForgeInstallPlan{}, stepErr
		}
		plan.Processors = append(plan.Processors, step)
	}

	return plan, nil
}

// planLoaderLibraries 把 version.json 里带下载地址的库转成下载项。
//
// 缺失下载地址的条目是 processor 的产出（例如 Forge 的 client jar），
// 由执行阶段生成，不能当作规划失败——这正是官方安装器同样处理的情形。
func planLoaderLibraries(plan *ForgeInstallPlan) *Error {
	var document forgeVersionDocument
	if jsonErr := json.Unmarshal(plan.VersionJSON, &document); jsonErr != nil {
		return installerError("官方安装器的版本描述格式不正确。", false)
	}
	for _, library := range document.Libraries {
		if library.Downloads == nil || library.Downloads.Artifact == nil {
			continue
		}
		entry := library.Downloads.Artifact
		if strings.TrimSpace(entry.URL) == "" {
			continue
		}
		relative, pathErr := artifact.ValidateRelativePath(entry.Path)
		if pathErr != nil || relative != entry.Path {
			return installerError("加载器依赖库的路径无效，文件可能已损坏。", false)
		}
		if err := validateDownloadTarget(entry.URL); err != nil {
			return installerError("加载器依赖库的下载地址不在允许的来源列表中。", false)
		}
		if entry.Size <= 0 {
			return installerError("加载器依赖库缺少文件大小，文件可能已损坏。", false)
		}
		plan.Libraries = append(plan.Libraries, download.PlanItem{
			ArtifactID:    "loader-library/" + entry.Path,
			TaskType:      download.TaskTypeLibrary,
			SourceURI:     entry.URL,
			TargetRelPath: relative,
			TargetKind:    download.TargetKindShared,
			ExpectedSHA1:  strings.ToLower(entry.SHA1),
			ExpectedSize:  entry.Size,
		})
	}
	return nil
}

// resolveProcessorJarDep 是 resolveProcessorJar 的注入点。
//
// 依赖解析需要访问官方 Maven，单元测试用合成的安装器时无法联网；
// 抽成变量后，规划逻辑（占位符替换、processor 筛选、去重）可以离线验证。
var resolveProcessorJarDep = resolveProcessorJar

// resolveProcessorJar 从候选仓库中挑选一个提供该依赖的源。
//
// 逐个探测 `.sha1`：官方 Maven 对每个构件都发布校验值，能取到就说明该仓库
// 有这个 jar。取不到时换下一个候选，全部失败才报错。
func resolveProcessorJar(ctx context.Context, root string, kind Kind, minecraftVersion, coordinate string, bases []string) (ProcessorJar, *Error) {
	relative, pathErr := mavenArtifactPath(coordinate)
	if pathErr != nil {
		return ProcessorJar{}, installerError("官方安装配置包含无效的处理器坐标，文件可能已损坏。", false)
	}
	targetRel := artifact.DirCache + "/processors/" + relative
	if _, validErr := artifact.ValidateRelativePath(targetRel); validErr != nil {
		return ProcessorJar{}, installerError("处理器缓存路径无效。", false)
	}

	var lastErr error
	for _, base := range bases {
		artifactURL := strings.TrimRight(base, "/") + "/" + relative
		if err := validateDownloadTarget(artifactURL); err != nil {
			lastErr = err
			continue
		}
		body, _, fetchErr := fetchMetadataDocument(
			ctx, root, kind, minecraftVersion, "processor-sha1-"+coordinate, artifactURL+".sha1", profileLibrarySHA1MaxBytes,
		)
		if fetchErr != nil {
			lastErr = fetchErr
			continue
		}
		sha1, parseErr := parseSHA1(body)
		if parseErr != nil {
			lastErr = parseErr
			continue
		}
		size, sizeErr := probeMetadataFileSize(ctx, artifactURL)
		if sizeErr != nil || size <= 0 {
			lastErr = errors.New("size unavailable")
			continue
		}
		return ProcessorJar{
			Coordinate:    coordinate,
			MavenBase:     base,
			URL:           artifactURL,
			SHA1:          sha1,
			Size:          size,
			TargetRelPath: targetRel,
		}, nil
	}
	_ = lastErr
	return ProcessorJar{}, installerError("无法获取加载器安装所需的处理器依赖，请检查网络后重试。", true)
}

// forgeProcessorAppliesToClient 判断 processor 是否需要在客户端执行。
//
// 空 sides 视为双侧（旧格式）；现代 Forge 只把真正需要的步骤标为 client。
func forgeProcessorAppliesToClient(sides []string) bool {
	if len(sides) == 0 {
		return true
	}
	for _, side := range sides {
		if strings.EqualFold(strings.TrimSpace(side), "client") {
			return true
		}
	}
	return false
}

// readArchiveEntry 读取 zip 中指定名称的条目。
func readArchiveEntry(files []*zip.File, name string, maxBytes int64) ([]byte, error) {
	for _, file := range files {
		if file.Name != name {
			continue
		}
		if file.UncompressedSize64 == 0 || int64(file.UncompressedSize64) > maxBytes {
			return nil, errors.New("entry size invalid")
		}
		reader, openErr := file.Open()
		if openErr != nil {
			return nil, openErr
		}
		defer reader.Close()
		body, readErr := io.ReadAll(io.LimitReader(reader, maxBytes))
		if readErr != nil {
			return nil, readErr
		}
		return body, nil
	}
	return nil, os.ErrNotExist
}

// validateDownloadTarget 校验下载地址属于允许的来源。
//
// 复用 download 包的源注册表而不是 loader 自己的元数据白名单：库文件与
// 元数据来自不同的域名集合（`libraries.minecraft.net` 提供库、但不提供任何
// 加载器元数据），各写一份必然会漏。
func validateDownloadTarget(raw string) error {
	registry := download.NewSourceRegistryFromProvider()
	if appErr := registry.ValidateURL(raw); appErr != nil {
		return errors.New(appErr.Code)
	}
	return nil
}

// forgeValueResolver 把 install_profile 里的占位符与 data 条目解析成实际值。
//
// 解析规则来自 Forge 安装器自身的约定（不是我们的发明）：
//
//	`[group:artifact:version(:classifier)]`  → libraries/<maven path>
//	`'literal'`                              → 去掉引号的字面量
//	`/path/in/installer`                     → 从安装器解压到临时目录
//	`{VAR}`                                  → 引用另一个已解析的值
type forgeValueResolver struct {
	root          string
	installerPath string
	librariesDir  string
	version       string
	minecraft     string
	minecraftJar  string
	extractDir    string
	resolved      map[string]string
	resolving     map[string]bool
	profile       forgeInstallProfile
}

// newForgeValueResolver 构造解析器并预先解析全部 data 条目。
//
// 解压目录在核心根的 `temp/` 下：安装结束后由调用方整体删除，
// 中途失败也不会在用户目录里留下散落文件。
func newForgeValueResolver(root, installerPath, minecraftJarPath string, profile forgeInstallProfile) (*forgeValueResolver, *Error) {
	librariesDir, resolveErr := artifact.ResolveInsideRoot(root, artifact.DirLibraries)
	if resolveErr != nil {
		return nil, installerError("核心库目录无效。", false)
	}
	parent := filepath.Join(root, "temp")
	if mkErr := os.MkdirAll(parent, 0o755); mkErr != nil {
		return nil, installerError("无法创建安装临时目录。", false)
	}
	extractDir, mkErr := os.MkdirTemp(parent, "forge-extract-")
	if mkErr != nil {
		return nil, installerError("无法创建安装临时目录。", false)
	}

	resolver := &forgeValueResolver{
		root:          root,
		installerPath: installerPath,
		librariesDir:  librariesDir,
		version:       profile.Version,
		minecraft:     profile.Minecraft,
		minecraftJar:  filepath.Join(extractDir, minecraftJarFileName),
		extractDir:    extractDir,
		resolved:      make(map[string]string, len(profile.Data)),
		resolving:     make(map[string]bool, len(profile.Data)),
		profile:       profile,
	}

	for key := range profile.Data {
		if _, err := resolver.dataValue(key); err != nil {
			return nil, err
		}
	}
	return resolver, nil
}

// dataValue 解析一个 data 条目（client 侧），支持 `{VAR}` 间接引用。
func (r *forgeValueResolver) dataValue(key string) (string, *Error) {
	if value, exists := r.resolved[key]; exists {
		return value, nil
	}
	entry, exists := r.profile.Data[key]
	if !exists {
		return "", installerError("官方安装配置缺少必需的数据项，文件可能已损坏。", false)
	}
	raw := strings.TrimSpace(entry.Client)
	if raw == "" {
		return "", installerError("官方安装配置的数据项为空，文件可能已损坏。", false)
	}

	// 引用另一个 data 条目：用 resolving 标记检测循环。
	if strings.HasPrefix(raw, "{") && strings.HasSuffix(raw, "}") {
		referenced := strings.TrimSpace(raw[1 : len(raw)-1])
		if r.resolving[referenced] {
			return "", installerError("官方安装配置存在循环引用，文件可能已损坏。", false)
		}
		r.resolving[key] = true
		value, err := r.dataValue(referenced)
		delete(r.resolving, key)
		if err != nil {
			return "", err
		}
		r.resolved[key] = value
		return value, nil
	}

	resolved, err := r.resolveLiteral(raw)
	if err != nil {
		return "", err
	}
	r.resolved[key] = resolved
	return resolved, nil
}

// resolveLiteral 按四种形态解析单个值。
func (r *forgeValueResolver) resolveLiteral(raw string) (string, *Error) {
	// 1. Maven 坐标：`[group:artifact:version:classifier]`
	if strings.HasPrefix(raw, "[") && strings.HasSuffix(raw, "]") {
		coordinate := strings.TrimSpace(raw[1 : len(raw)-1])
		relative, pathErr := mavenArtifactPath(coordinate)
		if pathErr != nil {
			return "", installerError("官方安装配置包含无效的依赖坐标，文件可能已损坏。", false)
		}
		absolute, resolveErr := artifact.ResolveInsideRoot(r.root, artifact.DirLibraries+"/"+relative)
		if resolveErr != nil {
			return "", installerError("依赖库路径无效。", false)
		}
		return absolute, nil
	}

	// 2. 字面量：`'text'`
	if len(raw) >= 2 && strings.HasPrefix(raw, "'") && strings.HasSuffix(raw, "'") {
		return raw[1 : len(raw)-1], nil
	}

	// 3. 安装器内文件：`/data/xxx`
	if strings.HasPrefix(raw, "/") {
		return r.extractInstallerEntry(raw)
	}

	// 4. 其余按字面量处理：Forge 的 profile 里确实存在不加引号的路径片段。
	return raw, nil
}

// extractInstallerEntry 把安装器内的文件解压到临时目录，返回绝对路径。
func (r *forgeValueResolver) extractInstallerEntry(name string) (string, *Error) {
	entryName := strings.TrimPrefix(strings.TrimSpace(name), "/")
	if entryName == "" || strings.Contains(entryName, "..") {
		return "", installerError("官方安装配置引用了非法的安装器文件。", false)
	}
	archive, openErr := zip.OpenReader(r.installerPath)
	if openErr != nil {
		return "", installerError("官方安装器文件已损坏，请重新下载后重试。", true)
	}
	defer archive.Close()

	for _, file := range archive.File {
		if file.Name != entryName {
			continue
		}
		target := filepath.Join(r.extractDir, filepath.FromSlash(entryName))
		if mkErr := os.MkdirAll(filepath.Dir(target), 0o755); mkErr != nil {
			return "", installerError("无法创建安装临时目录。", false)
		}
		reader, readErr := file.Open()
		if readErr != nil {
			return "", installerError("无法读取安装器内容。", true)
		}
		output, createErr := os.Create(target)
		if createErr != nil {
			reader.Close()
			return "", installerError("无法写入安装临时文件。", false)
		}
		_, copyErr := io.Copy(output, reader)
		closeErr := output.Close()
		reader.Close()
		if copyErr != nil || closeErr != nil {
			return "", installerError("解压安装器内容失败。", true)
		}
		return target, nil
	}
	return "", installerError("官方安装器缺少必需的数据文件，文件可能已损坏。", false)
}

// processorStep 构造一步 processor 执行：替换参数与产出路径。
func (r *forgeValueResolver) processorStep(document forgeProcessorDocument) (ProcessorStep, *Error) {
	step := ProcessorStep{
		JarCoordinate: strings.TrimSpace(document.Jar),
		Args:          make([]string, 0, len(document.Args)),
		Sides:         document.Sides,
	}
	if step.JarCoordinate == "" {
		return ProcessorStep{}, installerError("官方安装配置的处理器缺少坐标，文件可能已损坏。", false)
	}
	for _, coordinate := range document.Classpath {
		if trimmed := strings.TrimSpace(coordinate); trimmed != "" {
			step.ClasspathCoordinates = append(step.ClasspathCoordinates, trimmed)
		}
	}
	for _, arg := range document.Args {
		value, err := r.substitute(arg)
		if err != nil {
			return ProcessorStep{}, err
		}
		step.Args = append(step.Args, value)
	}
	for pathPattern, sha1Pattern := range document.Outputs {
		outputPath, pathErr := r.substitute(pathPattern)
		if pathErr != nil {
			return ProcessorStep{}, pathErr
		}
		sha1, sha1Err := r.substitute(sha1Pattern)
		if sha1Err != nil {
			return ProcessorStep{}, sha1Err
		}
		if !validProfileSHA1(sha1) {
			return ProcessorStep{}, installerError("官方安装配置的处理器校验值无效，文件可能已损坏。", false)
		}
		step.Outputs = append(step.Outputs, ProcessorOutput{
			AbsolutePath: outputPath,
			SHA1:         strings.ToLower(sha1),
		})
	}
	return step, nil
}

// substitute 替换文本里的全部 `{KEY}` 占位符。
//
// 内置键对应 Forge 安装器的固定约定；其余键来自 data 条目。
// 未知占位符原样保留：让 processor 自己报错比我们猜更准确。
func (r *forgeValueResolver) substitute(text string) (string, *Error) {
	result := text
	// 占位符可能嵌套引用（值本身含占位符），因此重复到不再变化为止。
	// 轮数上限防止畸形配置造成无限替换。
	for round := 0; round < 8; round++ {
		next, replaced := r.substituteOnce(result)
		if !replaced {
			return result, nil
		}
		result = next
	}
	return result, nil
}

// substituteOnce 做一轮替换，返回是否发生过替换。
func (r *forgeValueResolver) substituteOnce(text string) (string, bool) {
	var builder strings.Builder
	builder.Grow(len(text))
	replaced := false
	for index := 0; index < len(text); {
		start := strings.IndexByte(text[index:], '{')
		if start < 0 {
			builder.WriteString(text[index:])
			break
		}
		start += index
		end := strings.IndexByte(text[start:], '}')
		if end < 0 {
			builder.WriteString(text[index:])
			break
		}
		end += start
		builder.WriteString(text[index:start])
		key := text[start+1 : end]
		value, ok := r.builtin(key)
		if !ok {
			value, ok = r.resolved[key]
		}
		if !ok {
			builder.WriteString(text[start : end+1])
		} else {
			builder.WriteString(value)
			replaced = true
		}
		index = end + 1
	}
	return builder.String(), replaced
}

// builtin 返回 Forge 安装器内置的固定占位符。
func (r *forgeValueResolver) builtin(key string) (string, bool) {
	switch key {
	case "ROOT":
		return r.root, true
	case "INSTALLER":
		return r.installerPath, true
	case "LIBRARY_DIR":
		return r.librariesDir, true
	case "MINECRAFT_VERSION":
		return r.minecraft, true
	case "MINECRAFT_JAR":
		return r.minecraftJar, true
	case "SIDE":
		return "client", true
	default:
		return "", false
	}
}
