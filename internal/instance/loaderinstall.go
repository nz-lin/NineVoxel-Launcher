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
	before, err := versionDirectorySet(game)
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
	if err := executeLoaderInstaller(ctx, javaPath, game, installer.InstallerPath); err != nil {
		return err
	}
	versionID, err := detectLoaderVersionID(game, kind, item.MCVersion, item.LoaderVersion, before)
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

func (s *InstanceService) removeObsoleteLoaderVersion(item Instance, currentVersionID string) {
	previous := strings.TrimSpace(item.VersionID)
	if previous == "" || previous == item.MCVersion || previous == currentVersionID || !safeVersionDirectoryName(previous) {
		return
	}
	game, err := s.gameDir(item)
	if err != nil {
		return
	}
	target, err := managedPath(game, "versions/"+previous)
	if err != nil {
		return
	}
	_ = os.RemoveAll(target)
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
	if installedVersionExists(game, item.MCVersion) {
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
		if target, pathErr := managedPath(game, "versions/"+oldVersionID); pathErr == nil {
			_ = os.RemoveAll(target)
		}
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
