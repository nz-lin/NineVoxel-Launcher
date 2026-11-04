package loader

import (
	"archive/zip"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"example.com/ninevoxel/launcher/internal/artifact"
	"example.com/ninevoxel/launcher/internal/download"
	"example.com/ninevoxel/launcher/internal/platform"
)

// 本文件执行 Forge / NeoForge 的 processor 链。
//
// 每个 processor 都是一个独立的 Java 进程：`java -cp <processor>:<deps> <mainClass> <args>`。
// 这是官方安装器本身的执行方式，我们只是把编排权拿到自己手里。
//
// 执行顺序与校验规则：
//   - 按 install_profile.json 的数组顺序执行，不可重排；
//   - 已存在且哈希正确的产出直接跳过（增量安装，重复执行很快）；
//   - 每步结束后按 outputs 的 SHA-1 校验，不符立即失败并保留输出。

const (
	// processorMaxLogBytes 是单个 processor 保留的日志上限。
	//
	// 失败时要把它展示给用户，但不能无限增长：256 KiB 足够容纳一次
	// 重映射的完整进度输出。
	processorMaxLogBytes = 256 << 10
)

// ProcessorProgress 是一次 processor 执行的进度回调。
//
// 参数 index 为从 0 开始的序号，total 为总数，description 为可读说明。
type ProcessorProgress func(index, total int, description string)

// RunForgeProcessors 按序执行计划中的 processor。
//
// 参数 jarPaths 把 Maven 坐标映射到已下载的 jar 绝对路径；调用方负责在调用
// 前把 plan.ProcessorJars 全部下载完成。
//
// 调用方必须保证 plan.TempDir 在调用结束后被删除（成功与失败都一样）。
func RunForgeProcessors(ctx context.Context, javaPath string, plan ForgeInstallPlan, jarPaths map[string]string, progress ProcessorProgress) error {
	if !filepath.IsAbs(javaPath) {
		return installerError("安装器 Java 路径无效。", false)
	}
	if len(plan.Processors) == 0 {
		return installerError("官方安装器未声明任何客户端处理器，无法完成安装。", false)
	}

	// 原版 jar 必须先复制到隔离位置：processor 会在输入 jar 旁写标记文件，
	// 直接使用共享缓存里的那份会污染其他实例共用的原版文件。
	minecraftJar, copyErr := isolateMinecraftJar(plan)
	if copyErr != nil {
		return copyErr
	}

	total := len(plan.Processors)
	for index, step := range plan.Processors {
		if err := ctx.Err(); err != nil {
			return installerError("加载器安装已中断，请重试。", true)
		}
		description := processorDescription(step, index)
		if progress != nil {
			progress(index, total, description)
		}

		// 增量：产出已全部正确时跳过，重复安装不必再跑一遍 JVM。
		if processorOutputsReady(step) {
			continue
		}

		jarPath, exists := jarPaths[step.JarCoordinate]
		if !exists || !filepath.IsAbs(jarPath) {
			return installerError("加载器安装所需的处理器依赖缺失，请重试安装。", true)
		}
		classpath := make([]string, 0, len(step.ClasspathCoordinates)+1)
		classpath = append(classpath, jarPath)
		for _, coordinate := range step.ClasspathCoordinates {
			depPath, depExists := jarPaths[coordinate]
			if !depExists || !filepath.IsAbs(depPath) {
				return installerError("加载器安装所需的处理器依赖缺失，请重试安装。", true)
			}
			classpath = append(classpath, depPath)
		}

		mainClass, manifestErr := jarMainClass(jarPath)
		if manifestErr != nil {
			return installerError("加载器安装所需的处理器文件已损坏，请重试安装。", true)
		}

		if err := runProcessor(ctx, javaPath, mainClass, classpath, step.Args); err != nil {
			return err
		}
		if err := verifyProcessorOutputs(step); err != nil {
			return err
		}
	}

	// 隔离副本用完即删：它只服务于本次安装。
	_ = os.Remove(minecraftJar)
	return nil
}

// isolateMinecraftJar 把原版 jar 复制到临时目录并返回副本路径。
func isolateMinecraftJar(plan ForgeInstallPlan) (string, *Error) {
	target := filepath.Join(plan.TempDir, minecraftJarFileName)
	if info, statErr := os.Stat(target); statErr == nil && info.Mode().IsRegular() && info.Size() > 0 {
		return target, nil
	}
	source, openErr := os.Open(plan.MinecraftJarSource)
	if openErr != nil {
		return "", installerError("原版客户端文件缺失，请先完成原版安装。", true)
	}
	defer source.Close()

	if mkErr := os.MkdirAll(plan.TempDir, 0o755); mkErr != nil {
		return "", installerError("无法创建安装临时目录。", false)
	}
	output, createErr := os.Create(target)
	if createErr != nil {
		return "", installerError("无法写入安装临时文件。", false)
	}
	_, copyErr := io.Copy(output, source)
	syncErr := output.Sync()
	closeErr := output.Close()
	if copyErr != nil || syncErr != nil || closeErr != nil {
		_ = os.Remove(target)
		return "", installerError("复制原版客户端文件失败，请检查磁盘空间。", true)
	}
	return target, nil
}

// processorOutputsReady 判断该 processor 的产出是否已全部就绪。
//
// 只要有一个缺失或哈希不符就必须重跑：processor 的产出之间可能互相依赖，
// 部分重跑没有意义。
func processorOutputsReady(step ProcessorStep) bool {
	if len(step.Outputs) == 0 {
		// 没有声明产出的 processor 无法判断是否完成，保守地每次都执行。
		return false
	}
	for _, output := range step.Outputs {
		if !processorOutputMatches(output) {
			return false
		}
	}
	return true
}

// processorOutputMatches 判断单个产出是否存在且哈希正确。
//
// 不用 download.VerifyFile：它要求调用方声明期望大小，而 install_profile 的
// outputs 只给 SHA-1。这里只比对哈希，大小由文件本身决定。
func processorOutputMatches(output ProcessorOutput) bool {
	result, err := download.HashFile(output.AbsolutePath)
	if err != nil {
		return false
	}
	return strings.EqualFold(result.SHA1, output.SHA1)
}

// verifyProcessorOutputs 校验 processor 的产出。
func verifyProcessorOutputs(step ProcessorStep) error {
	for _, output := range step.Outputs {
		if !processorOutputMatches(output) {
			return installerError(
				"加载器安装产物校验未通过（"+filepath.Base(output.AbsolutePath)+"），请重试安装。", true)
		}
	}
	return nil
}

// processorCommand 组装一次 processor 的完整命令行。
//
// 抽成纯函数是为了让顺序与分隔符可以被单元测试固定住：
// classpath 的分隔符必须跟随平台（Windows 是 `;`），写错时表现为
// 「找不到主类」，从错误信息很难反推。
func processorCommand(javaPath, mainClass string, classpath, args []string) []string {
	result := make([]string, 0, len(args)+8)
	result = append(result, platform.JavaProxyOptions()...)
	result = append(result, "-cp", strings.Join(classpath, string(os.PathListSeparator)), mainClass)
	result = append(result, args...)
	return result
}

// runProcessor 执行一个 processor 进程。
//
// 输出统一收集到受限缓冲区：失败时用于归纳原因，成功时丢弃。
func runProcessor(ctx context.Context, javaPath, mainClass string, classpath, args []string) error {
	base := processorCommand(javaPath, mainClass, classpath, args)

	err, output := runProcessorOnce(ctx, javaPath, base)
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return installerError("加载器安装已中断，请重试。", true)
	}

	// 模块系统限制是最常见的失败原因之一：新版 Forge 的 processor 会在
	// Java 9+ 上访问未导出的包。按需追加一次 --add-exports 再试，
	// 而不是无条件加上——无条件加会让旧版本 Java 直接拒绝启动。
	if mentionsModuleAccess(output) {
		retry := make([]string, 0, len(base)+2)
		retry = append(retry, "--add-exports", "cpw.mods.bootstraplauncher/cpw.mods.bootstraplauncher=ALL-UNNAMED")
		retry = append(retry, base...)
		if retryErr, retryOutput := runProcessorOnce(ctx, javaPath, retry); retryErr == nil {
			return nil
		} else if ctx.Err() != nil {
			return installerError("加载器安装已中断，请重试。", true)
		} else {
			output = retryOutput
		}
	}
	return installerError(processorFailureMessage(output), true)
}

// runProcessorOnce 执行一次 Java 进程，返回错误与合并输出。
func runProcessorOnce(ctx context.Context, javaPath string, args []string) (error, string) {
	command := exec.CommandContext(ctx, javaPath, args...)
	output := &limitedBuffer{limit: processorMaxLogBytes}
	command.Stdout = output
	command.Stderr = output
	command.Stdin = nil
	// 隐藏控制台窗口：processor 是后台任务，闪黑框会让用户以为出错。
	platform.Current.Process.HideWindow(command)
	return command.Run(), output.String()
}

// mentionsModuleAccess 判断输出是否提示模块访问受限。
func mentionsModuleAccess(output string) bool {
	lower := strings.ToLower(output)
	return strings.Contains(lower, "illegalaccesserror") ||
		strings.Contains(lower, "module") && strings.Contains(lower, "not export") ||
		strings.Contains(lower, "add-exports") ||
		strings.Contains(lower, "does not export")
}

// processorFailureMessage 把 processor 输出归纳成一句可读的失败原因。
//
// 与官方安装器的归因保持同一套关键词：两者失败原因高度重合，
// 分开维护会逐渐漂移。
func processorFailureMessage(output string) string {
	if message := installerFailureMessage(output); message != "官方安装器执行失败，请确认 Java 版本与磁盘权限后重试。" {
		return message
	}
	lower := strings.ToLower(output)
	switch {
	case strings.Contains(lower, "outofmemoryerror"):
		return "加载器安装过程中内存不足，请在设置中调高内存后重试。"
	case strings.Contains(lower, "no space left"):
		return "磁盘空间不足，无法完成加载器安装。"
	case strings.Contains(lower, "corrupt"):
		return "加载器安装所需文件已损坏，请重试安装。"
	case strings.Contains(lower, "java.lang.illegalargumentexception"):
		return "加载器安装参数不被接受，可能是该版本安装器格式已变化。"
	default:
		return "加载器安装步骤执行失败，请检查网络与 Java 版本后重试。"
	}
}

// processorDescription 生成可读的步骤说明。
//
// 用坐标末段而不是完整 Maven 坐标：`binarypatcher` 比
// `net.minecraftforge:binarypatcher:1.3.4` 更容易让用户理解。
func processorDescription(step ProcessorStep, index int) string {
	name := step.JarCoordinate
	if parts := strings.Split(step.JarCoordinate, ":"); len(parts) >= 2 {
		name = parts[1]
	}
	return "正在执行安装步骤 " + name + "（" + strconv.Itoa(index+1) + "）"
}

// jarMainClass 从 jar 的 MANIFEST.MF 读取 Main-Class。
//
// Forge 的 processor 依赖不在 install_profile.json 里声明主类，主类只存在于
// 该 jar 的清单中——这与官方安装器读取的位置一致。
func jarMainClass(jarPath string) (string, error) {
	archive, openErr := zip.OpenReader(jarPath)
	if openErr != nil {
		return "", openErr
	}
	defer archive.Close()

	body, readErr := readArchiveEntry(archive.File, "META-INF/MANIFEST.MF", 1<<20)
	if readErr != nil {
		return "", readErr
	}
	mainClass := parseManifestMainClass(body)
	if mainClass == "" {
		return "", errors.New("manifest has no main class")
	}
	return mainClass, nil
}

// parseManifestMainClass 解析 MANIFEST.MF 的 Main-Class。
//
// 清单规则：行以 CRLF 或 LF 分隔；以单个空格开头的行是上一行的续行。
// 这里只需要一个字段，因此实现得很窄，但必须正确处理续行——
// 否则遇到被拆行的主类名会读出半个类名。
func parseManifestMainClass(body []byte) string {
	text := strings.ReplaceAll(string(body), "\r\n", "\n")
	lines := strings.Split(text, "\n")
	current := ""
	for _, line := range lines {
		if strings.HasPrefix(line, " ") {
			current += strings.TrimPrefix(line, " ")
			continue
		}
		if name, value, found := strings.Cut(current, ": "); found {
			if strings.EqualFold(strings.TrimSpace(name), "Main-Class") {
				return strings.TrimSpace(value)
			}
		}
		current = line
	}
	// 处理最后一行没有后续行的情况。
	if name, value, found := strings.Cut(current, ": "); found {
		if strings.EqualFold(strings.TrimSpace(name), "Main-Class") {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// ProcessorJarPlan 把 processor 依赖汇总成一个下载计划。
//
// 单独成一个计划是为了让调用方用既有的下载队列执行：这样进度、续传、
// 哈希校验与隔离区都复用同一套实现，不需要在 loader 包里再写一遍。
func ProcessorJarPlan(plan ForgeInstallPlan) download.TransferPlan {
	items := make([]download.PlanItem, 0, len(plan.ProcessorJars))
	for _, jar := range plan.ProcessorJars {
		items = append(items, download.PlanItem{
			ArtifactID:    "processor/" + jar.Coordinate,
			TaskType:      download.TaskTypeLoader,
			SourceURI:     jar.URL,
			TargetRelPath: jar.TargetRelPath,
			TargetKind:    download.TargetKindShared,
			ExpectedSHA1:  jar.SHA1,
			ExpectedSize:  jar.Size,
		})
	}
	return download.TransferPlan{
		VersionID: plan.VersionID,
		PlanHash:  "forge-processors-" + plan.VersionID,
		Items:     items,
	}
}

// LoaderLibraryPlan 把加载器自身的依赖库汇总成一个下载计划。
func LoaderLibraryPlan(plan ForgeInstallPlan) download.TransferPlan {
	return download.TransferPlan{
		VersionID: plan.VersionID,
		PlanHash:  "forge-libraries-" + plan.VersionID,
		Items:     plan.Libraries,
	}
}

// ProcessorJarPaths 计算 processor 依赖的绝对路径。
//
// 下载完成后按同一规则换算：计划里存的是相对路径，执行时需要绝对路径。
func ProcessorJarPaths(root string, plan ForgeInstallPlan) (map[string]string, *Error) {
	result := make(map[string]string, len(plan.ProcessorJars))
	for _, jar := range plan.ProcessorJars {
		absolute, resolveErr := artifact.ResolveInsideRoot(root, jar.TargetRelPath)
		if resolveErr != nil {
			return nil, installerError("处理器缓存路径无效。", false)
		}
		result[jar.Coordinate] = absolute
	}
	return result, nil
}

// WriteVersionJSON 把安装器内嵌的 version.json 写入版本目录。
//
// 写入经临时文件 + Rename：安装中途失败时不会留下半个 JSON 让后续流程
// 误判为「版本已存在」。
func WriteVersionJSON(root string, plan ForgeInstallPlan) *Error {
	relative, pathErr := artifact.LoaderVersionRelPath(plan.VersionID, plan.VersionID+".json")
	if pathErr != nil {
		return installerError("版本目录路径无效。", false)
	}
	target, resolveErr := artifact.ResolveInsideRoot(root, relative)
	if resolveErr != nil {
		return installerError("版本目录路径无效。", false)
	}
	if mkErr := os.MkdirAll(filepath.Dir(target), 0o755); mkErr != nil {
		return installerError("无法创建版本目录。", false)
	}
	temp, createErr := os.CreateTemp(filepath.Dir(target), ".version-*.json")
	if createErr != nil {
		return installerError("无法写入版本描述。", false)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)

	if _, writeErr := temp.Write(plan.VersionJSON); writeErr != nil {
		temp.Close()
		return installerError("无法写入版本描述。", false)
	}
	if syncErr := temp.Sync(); syncErr != nil {
		temp.Close()
		return installerError("无法同步版本描述。", false)
	}
	if closeErr := temp.Close(); closeErr != nil {
		return installerError("无法关闭版本描述文件。", false)
	}
	if renameErr := os.Rename(tempPath, target); renameErr != nil {
		return installerError("无法提交版本描述。", false)
	}
	return nil
}

// CleanupTempDir 删除安装期临时目录。
func CleanupTempDir(plan ForgeInstallPlan) {
	if plan.TempDir == "" {
		return
	}
	_ = os.RemoveAll(plan.TempDir)
}
