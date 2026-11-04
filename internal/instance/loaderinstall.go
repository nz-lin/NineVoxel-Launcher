package instance

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"example.com/ninevoxel/launcher/internal/artifact"
	"example.com/ninevoxel/launcher/internal/download"
	"example.com/ninevoxel/launcher/internal/loader"
	"example.com/ninevoxel/launcher/internal/version"
)

var (
	ensureLoaderProfile    = loader.EnsureProfile
	prepareLoaderInstaller = loader.PrepareInstaller
	resolveInstallerJava   = func(string, string, string) (string, error) {
		return "", errors.New("尚未配置安装器 Java 解析器")
	}

	// 以下注入点便于测试替换：安装编排涉及网络与子进程，
	// 单元测试需要在不联网、不起 JVM 的前提下验证顺序与错误处理。
	planForgeInstall      = loader.PlanForgeInstall
	processorJarPlan      = loader.ProcessorJarPlan
	loaderLibraryPlan     = loader.LoaderLibraryPlan
	writeForgeVersionJSON = loader.WriteVersionJSON
	processorJarPaths     = loader.ProcessorJarPaths
	runForgeProcessors    = loader.RunForgeProcessors
	cleanupForgeInstall   = loader.CleanupTempDir

	// runLoaderInstallPlan 执行安装期的下载计划。
	//
	// 抽成变量是因为它需要走全局下载队列（绑定真实数据根），
	// 而集成测试要在临时目录里跑完整安装流程。
	runLoaderInstallPlan = func(s *InstanceService, ctx context.Context, id string, plan download.TransferPlan, base, span float64) error {
		return s.runPlan(ctx, id, plan, base, span)
	}
)

// SetInstallJavaResolver injects the same Java priority used by launch:
// instance runtime, instance path, global path, then selected runtime.
//
// 第三个参数是 MC 版本：没有显式配置时按该版本声明的 Java 要求自动匹配。
// 安装器对 Java 版本同样敏感——用 Java 25 跑 1.7.10 的 Forge installer 会直接失败。
func SetInstallJavaResolver(resolver func(customPath, customRuntimeID, mcVersion string) (string, error)) {
	if resolver != nil {
		resolveInstallerJava = resolver
	}
}

func (s *InstanceService) installProfileLoader(ctx context.Context, item Instance, kind loader.Kind) error {
	versionID, appErr := ensureLoaderProfile(ctx, kind, item.MCVersion, item.LoaderVersion)
	if appErr != nil {
		return appErr
	}
	if err := s.installWithManifest(ctx, kind, item.MCVersion, item.LoaderVersion, versionID, func() (installArtifacts, error) {
		return s.installPreparedVersion(ctx, item, versionID, 1)
	}); err != nil {
		return err
	}
	if err := s.setResolvedVersionID(item.ID, versionID); err != nil {
		return err
	}
	s.removeObsoleteLoaderVersion(item, versionID)
	return nil
}

// installInstallerLoader 安装 Forge / NeoForge。
//
// 流程（顺序不可调整）：
//  1. 下载原版：processor 需要原版 jar 作为输入；
//  2. 下载官方安装器（已通过 SHA-1 校验）；
//  3. 解析安装器里的 install_profile.json 与 version.json，得到安装计划；
//  4. 下载 processor 依赖与加载器依赖库；
//  5. 写入版本描述；
//  6. 按序执行 processor 并校验产出；
//  7. 写核心清单（唯一安装成功判据）。
//
// 为什么自己编排 processor 而不执行官方安装器的 main class：
// `--installClient` 是上游可能移除的入口，而 processor jar 是安装器自带、
// 由官方维护的产物。编排它们不改变「加载器产物由官方代码生成」这一前提，
// 只是不再依赖那个命令行入口。
func (s *InstanceService) installInstallerLoader(ctx context.Context, item Instance, kind loader.Kind) error {
	prepared, err := version.PrepareInstallation(item.MCVersion)
	if err != nil {
		return fmt.Errorf("解析 Minecraft 基础版本失败：%w", err)
	}
	installer, appErr := prepareLoaderInstaller(ctx, kind, item.MCVersion, item.LoaderVersion)
	if appErr != nil {
		return appErr
	}

	// 原版下载占前 55%：它通常是最大的一块（客户端 jar + 资源对象）。
	artifacts, installErr := s.installPreparedRange(ctx, item, prepared, s.runPlan, 0.55)
	if installErr != nil {
		return installErr
	}
	if err := runLoaderInstallPlan(s, ctx, item.ID, installer.Plan, 0.55, 0.05); err != nil {
		return err
	}
	// 刻意不把安装器缓存记入 artifacts.Downloaded。
	//
	// 清单的语义是「运行这个版本必需的文件」；安装器只在安装期用一次，
	// 记进清单会让重建永久保留它，也会让「修复」去重新下载一个运行期
	// 根本用不到的文件。

	javaPath, err := resolveInstallerJava(item.CustomJavaPath, item.CustomJavaRuntimeID, item.MCVersion)
	if err != nil {
		return fmt.Errorf("无法为 %s 安装器准备 Java：%w", strings.ToUpper(string(kind)), err)
	}

	vanillaJar, jarErr := vanillaClientJarPath(s.root, item.MCVersion)
	if jarErr != nil {
		return jarErr
	}
	plan, planErr := planForgeInstall(ctx, kind, s.root, installer.InstallerPath, vanillaJar)
	if planErr != nil {
		return planErr
	}
	defer cleanupForgeInstall(plan)

	if err := s.progress(item.ID, "", 0.62, 0); err != nil {
		return err
	}
	// processor 依赖：只有几十 KB 到几百 KB，先下载它们才能执行。
	if jarPlan := processorJarPlan(plan); len(jarPlan.Items) > 0 {
		if err := runLoaderInstallPlan(s, ctx, item.ID, jarPlan, 0.62, 0.08); err != nil {
			return err
		}
	}
	// 加载器依赖库：几十 MB，是安装阶段的主要下载量。
	if libPlan := loaderLibraryPlan(plan); len(libPlan.Items) > 0 {
		if err := runLoaderInstallPlan(s, ctx, item.ID, libPlan, 0.7, 0.15); err != nil {
			return err
		}
	}
	for _, library := range plan.Libraries {
		artifacts.Downloaded = append(artifacts.Downloaded, library.TargetRelPath)
	}
	// 仅安装期使用的库不进清单：它们是 processor 的运行时素材，
	// 不在该版本的 classpath 上。记进清单会让重建永久保留用不到的文件。
	artifacts.Downloaded = excludePaths(artifacts.Downloaded, plan.InstallOnlyLibraries)

	if err := writeForgeVersionJSON(s.root, plan); err != nil {
		return err
	}
	jarPaths, pathsErr := processorJarPaths(s.root, plan)
	if pathsErr != nil {
		return pathsErr
	}
	if err := s.progress(item.ID, "", 0.86, 0); err != nil {
		return err
	}
	if err := runForgeProcessors(ctx, javaPath, plan, jarPaths, nil); err != nil {
		return err
	}

	// processor 产出显式记入清单：它们没有下载源，扫描目录无法区分
	// 「本次生成」与「上次残留」。
	artifacts.ProcessorOutputs = append(artifacts.ProcessorOutputs, processorArtifacts(s.root, plan)...)

	// 清单是唯一的「安装成功」判据：写不出来就说明产物不完整。
	if err := s.writeCoreManifest(kind, item.MCVersion, item.LoaderVersion, plan.VersionID, artifacts); err != nil {
		return err
	}
	if err := s.setResolvedVersionID(item.ID, plan.VersionID); err != nil {
		return err
	}
	if err := s.progress(item.ID, "", 0.99, 0); err != nil {
		return err
	}
	s.removeObsoleteLoaderVersion(item, plan.VersionID)
	return nil
}

// excludePaths 从 paths 中移除 excludes 里的条目，保持原顺序。
func excludePaths(paths, excludes []string) []string {
	if len(excludes) == 0 {
		return paths
	}
	drop := make(map[string]struct{}, len(excludes))
	for _, path := range excludes {
		drop[path] = struct{}{}
	}
	result := make([]string, 0, len(paths))
	for _, path := range paths {
		if _, skip := drop[path]; skip {
			continue
		}
		result = append(result, path)
	}
	return result
}

// processorArtifacts 汇总本次安装生成的、没有独立下载源的产物。
//
// 两个来源缺一不可：
//
//	outputs 声明    Forge 提供；有哈希，可校验
//	data 声明       NeoForge 不提供 outputs，产物路径只在 data 里出现
//
// 只记录**实际存在**的路径：data 里的 `[net.minecraft:client:<ver>]` 一类
// 是原版输入而不是产物，安装后并不存在，不该进清单。
func processorArtifacts(root string, plan loader.ForgeInstallPlan) []string {
	result := make([]string, 0, len(plan.Processors)+len(plan.DeclaredArtifacts))
	seen := make(map[string]struct{}, cap(result))

	record := func(absolute string) {
		info, statErr := os.Stat(absolute)
		if statErr != nil || !info.Mode().IsRegular() {
			return
		}
		relative, relErr := filepath.Rel(root, absolute)
		if relErr != nil || strings.HasPrefix(relative, "..") {
			return
		}
		slash := filepath.ToSlash(relative)
		if _, exists := seen[slash]; exists {
			return
		}
		seen[slash] = struct{}{}
		result = append(result, slash)
	}

	for _, step := range plan.Processors {
		for _, output := range step.Outputs {
			record(output.AbsolutePath)
		}
	}
	for _, declared := range plan.DeclaredArtifacts {
		record(declared)
	}
	return result
}

// vanillaClientJarPath 返回原版客户端 jar 的绝对路径。
//
// 这是 processor 的输入。它必须已经存在：加载器安装永远排在原版安装之后，
// 缺失说明原版安装本身不完整，应让用户先修好原版而不是在加载器阶段猜。
func vanillaClientJarPath(root, minecraftVersion string) (string, error) {
	relative, pathErr := artifact.VersionRelPath(minecraftVersion, minecraftVersion+".jar")
	if pathErr != nil {
		return "", errors.New("原版客户端路径无效")
	}
	absolute, resolveErr := artifact.ResolveInsideRoot(root, relative)
	if resolveErr != nil {
		return "", errors.New("原版客户端路径无效")
	}
	info, statErr := os.Stat(absolute)
	if statErr != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		return "", errors.New("原版客户端文件缺失，请先完成原版安装")
	}
	return absolute, nil
}

func (s *InstanceService) setResolvedVersionID(instanceID, versionID string) error {
	if !safeVersionDirectoryName(versionID) {
		return errors.New("加载器版本目录无效")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	item, err := s.getLocked(instanceID)
	if err != nil {
		return err
	}
	item.VersionID = versionID
	return s.saveLocked(item)
}

func versionDirectorySet(gameDirectory string) (map[string]bool, error) {
	entries, err := os.ReadDir(filepath.Join(gameDirectory, "versions"))
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]bool{}, nil
		}
		return nil, errors.New("无法读取实例版本目录")
	}
	result := make(map[string]bool, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			result[entry.Name()] = true
		}
	}
	return result, nil
}

// removeObsoleteLoaderVersion 清理切换加载器后不再需要的旧核心版本。
//
// 必须做引用检查：核心版本由所有实例共用，直接删除会让其他仍在使用该版本的
// 实例无法启动，而故障现象与操作对象完全无关（A 实例换加载器，B 实例起不来）。
func (s *InstanceService) removeObsoleteLoaderVersion(item Instance, currentVersionID string) {
	previous := strings.TrimSpace(item.VersionID)
	if previous == "" || previous == item.MCVersion || previous == currentVersionID {
		return
	}
	s.removeCoreVersionIfUnreferenced(previous, item.ID)
}

func safeVersionDirectoryName(versionID string) bool {
	if versionID == "" || versionID == "." || versionID == ".." || len(versionID) > 128 {
		return false
	}
	normalized, err := artifact.ValidateRelativePath(versionID)
	return err == nil && normalized == versionID && !strings.ContainsAny(versionID, `/\\:`)
}

// UpdateLoader switches an instance to a new loader release and starts a real
// reinstall. The old loader directory is removed only after the new install succeeds.
func (s *InstanceService) UpdateLoader(req ChangeLoaderRequest) (Instance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.openLocked(); err != nil {
		return Instance{}, err
	}
	item, err := s.getLocked(req.InstanceID)
	if err != nil {
		return Instance{}, err
	}
	if item.InstallationState == "installing" {
		return Instance{}, errors.New("当前实例正在安装，请稍后再切换加载器")
	}
	kind, err := loader.ParseKind(req.Loader)
	if err != nil || kind == loader.KindVanilla {
		return Instance{}, errors.New("请选择有效的模组加载器")
	}
	c := item.Config
	c.Loader = string(kind)
	c.LoaderVersion = strings.TrimSpace(req.LoaderVersion)
	c, err = validConfig(c)
	if err != nil {
		return Instance{}, err
	}
	if _, err := s.gameDir(item); err != nil {
		return Instance{}, err
	}
	if item.Loader == c.Loader && item.LoaderVersion == c.LoaderVersion && item.InstallationState == "installed" {
		return item, nil
	}
	item.Config = c
	item.InstallationState = "not_installed"
	item.InstallationError = ""
	item.InstallationProgress = 0
	item.InstallTaskID = ""
	if err := s.saveLocked(item); err != nil {
		return Instance{}, err
	}
	return s.startInstallLocked(item)
}

// UninstallLoader returns an instance to the vanilla core. Loader-only version
// files are removed and jar mods are disabled so the next launch is truly vanilla.
func (s *InstanceService) UninstallLoader(req IDRequest) (Instance, error) {
	s.mu.Lock()
	if err := s.openLocked(); err != nil {
		s.mu.Unlock()
		return Instance{}, err
	}
	item, err := s.getLocked(req.InstanceID)
	if err != nil {
		s.mu.Unlock()
		return Instance{}, err
	}
	if item.InstallationState == "installing" {
		s.mu.Unlock()
		return Instance{}, errors.New("当前实例正在安装，请稍后再卸载加载器")
	}
	if item.Loader == string(loader.KindVanilla) || item.Loader == "" {
		s.mu.Unlock()
		return item, nil
	}
	game, err := s.gameDir(item)
	if err != nil {
		s.mu.Unlock()
		return Instance{}, err
	}
	oldVersionID := strings.TrimSpace(item.VersionID)
	if oldVersionID == "" && item.LoaderVersion != "" {
		if kind, kindErr := loader.ParseKind(item.Loader); kindErr == nil {
			if computed, versionErr := loader.VersionID(kind, item.MCVersion, item.LoaderVersion); versionErr == nil {
				oldVersionID = computed
			}
		}
	}
	item.Loader = string(loader.KindVanilla)
	item.LoaderVersion = ""
	item.InstallTaskID = ""
	item.InstallationError = ""
	item.InstallationProgress = 0
	if coreVersionInstalled(s.root, item.MCVersion) {
		item.VersionID = item.MCVersion
		item.InstallationState = "installed"
		item.InstallationProgress = 1
	} else {
		item.VersionID = ""
		item.InstallationState = "not_installed"
	}
	if err := s.saveLocked(item); err != nil {
		s.mu.Unlock()
		return Instance{}, err
	}
	s.mu.Unlock()

	if oldVersionID != "" && oldVersionID != item.MCVersion && safeVersionDirectoryName(oldVersionID) {
		// 核心版本由所有实例共用：只有确认无人引用时才删除。
		s.removeCoreVersionIfUnreferenced(oldVersionID, item.ID)
	}
	_ = disableJarMods(game)
	return item, nil
}

func disableJarMods(gameDirectory string) error {
	mods, err := managedPath(gameDirectory, "mods")
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(mods)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return errors.New("无法读取模组目录")
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".jar") {
			continue
		}
		source, pathErr := managedPath(mods, entry.Name())
		if pathErr != nil {
			continue
		}
		disabled, pathErr := managedPath(mods, entry.Name()+".disabled")
		if pathErr != nil {
			continue
		}
		if _, statErr := os.Stat(disabled); statErr == nil {
			continue
		}
		_ = os.Rename(source, disabled)
	}
	return nil
}
