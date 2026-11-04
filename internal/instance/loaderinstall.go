package instance

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"example.com/ninevoxel/launcher/internal/artifact"
	"example.com/ninevoxel/launcher/internal/loader"
	"example.com/ninevoxel/launcher/internal/version"
)

var (
	ensureLoaderProfile    = loader.EnsureProfile
	prepareLoaderInstaller = loader.PrepareInstaller
	executeLoaderInstaller = loader.RunInstaller
	detectLoaderVersionID  = loader.DetectInstalledVersionID
	resolveInstallerJava   = func(string, string) (string, error) {
		return "", errors.New("尚未配置安装器 Java 解析器")
	}
)

// SetInstallJavaResolver injects the same Java priority used by launch:
// instance runtime, instance path, global path, then selected runtime.
func SetInstallJavaResolver(resolver func(customPath, customRuntimeID string) (string, error)) {
	if resolver != nil {
		resolveInstallerJava = resolver
	}
}

func (s *InstanceService) installProfileLoader(ctx context.Context, item Instance, kind loader.Kind) error {
	versionID, appErr := ensureLoaderProfile(ctx, kind, item.MCVersion, item.LoaderVersion)
	if appErr != nil {
		return appErr
	}
	if err := s.installPreparedVersion(ctx, item, versionID, 1); err != nil {
		return err
	}
	if err := s.setResolvedVersionID(item.ID, versionID); err != nil {
		return err
	}
	s.removeObsoleteLoaderVersion(item, versionID)
	return nil
}

func (s *InstanceService) installInstallerLoader(ctx context.Context, item Instance, kind loader.Kind) error {
	prepared, err := version.PrepareInstallation(item.MCVersion)
	if err != nil {
		return fmt.Errorf("解析 Minecraft 基础版本失败：%w", err)
	}
	installer, appErr := prepareLoaderInstaller(ctx, kind, item.MCVersion, item.LoaderVersion)
	if appErr != nil {
		return appErr
	}
	if err := s.installPreparedRange(ctx, item, prepared, s.runPlan, 0.6); err != nil {
		return err
	}
	game, err := s.gameDir(item)
	if err != nil {
		return err
	}
	_ = game
	// 安装产物写入共享核心，实例目录只保留可变状态。
	before, err := versionDirectorySet(s.root)
	if err != nil {
		return err
	}
	if err := s.runPlan(ctx, item.ID, installer.Plan, 0.6, 0.1); err != nil {
		return err
	}
	javaPath, err := resolveInstallerJava(item.CustomJavaPath, item.CustomJavaRuntimeID)
	if err != nil {
		return fmt.Errorf("无法为 %s 安装器准备 Java：%w", strings.ToUpper(string(kind)), err)
	}
	if err := s.progress(item.ID, "", 0.72, 0); err != nil {
		return err
	}
	workDir, workErr := installerWorkDir(s.root)
	if workErr != nil {
		return workErr
	}
	defer os.RemoveAll(workDir)
	if err := executeLoaderInstaller(ctx, javaPath, loader.InstallerContext{
		WorkingDirectory: workDir,
		GameDirectory:    s.root,
		ResourceRoot:     s.root,
	}, installer.InstallerPath); err != nil {
		return err
	}
	versionID, err := detectLoaderVersionID(s.root, kind, item.MCVersion, item.LoaderVersion, before)
	if err != nil {
		return fmt.Errorf("%s 安装器未生成可用的版本目录：%w", strings.ToUpper(string(kind)), err)
	}
	if err := s.setResolvedVersionID(item.ID, versionID); err != nil {
		return err
	}
	if err := s.progress(item.ID, "", 0.99, 0); err != nil {
		return err
	}
	s.removeObsoleteLoaderVersion(item, versionID)
	return nil
}

// installerWorkDir 创建本次安装专用的临时工作区。
//
// 为什么不让安装器直接用核心根当工作目录：它会在工作目录里生成并改写
// launcher_profiles.json。多个安装并发时，共享同一份配置会互相覆盖。
func installerWorkDir(root string) (string, error) {
	base := filepath.Join(root, "temp", "installers")
	if mkErr := os.MkdirAll(base, 0o755); mkErr != nil {
		return "", errors.New("无法创建安装器工作目录")
	}
	dir, tempErr := os.MkdirTemp(base, "install-")
	if tempErr != nil {
		return "", errors.New("无法创建安装器工作目录")
	}
	return dir, nil
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
