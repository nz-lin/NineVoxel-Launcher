package loader

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"example.com/ninevoxel/launcher/internal/artifact"
	"example.com/ninevoxel/launcher/internal/version"
)

const profileMaxBytes = 2 << 20

type profileDocument struct {
	ID           string            `json:"id"`
	InheritsFrom string            `json:"inheritsFrom"`
	MainClass    string            `json:"mainClass"`
	Libraries    []json.RawMessage `json:"libraries"`
}

// EnsureProfile downloads and validates a Fabric or Quilt profile JSON and
// stores it in the launcher's versions directory. An already valid local file
// is reused without a network request.
func EnsureProfile(ctx context.Context, kind Kind, minecraftVersion, loaderVersion string) (string, *Error) {
	if kind != KindFabric && kind != KindQuilt {
		return "", inputError("该加载器不使用 profile 元数据安装。")
	}
	mc, err := validateMinecraftVersion(minecraftVersion)
	if err != nil {
		return "", inputError(err.Error())
	}
	loaderVersion, err = validateLoaderVersion(loaderVersion)
	if err != nil {
		return "", inputError(err.Error())
	}
	versionID, err := VersionID(kind, mc, loaderVersion)
	if err != nil {
		return "", inputError(err.Error())
	}
	root, rootErr := artifact.DataRoot()
	if rootErr != nil {
		return "", cacheError("无法访问启动器数据目录。")
	}
	relative, pathErr := artifact.VersionRelPath(versionID, versionID+".json")
	if pathErr != nil {
		return "", profileError("加载器版本目录无效。", false)
	}
	absolute, resolveErr := artifact.ResolveInsideRoot(root, relative)
	if resolveErr != nil {
		return "", profileError("加载器版本目录无效。", false)
	}
	if ensureErr := version.EnsureVersionMetadata(mc); ensureErr != nil {
		return "", profileError("无法获取 Minecraft 父版本元数据，请先刷新版本清单。", true)
	}
	if profileFileValid(absolute, versionID, mc) {
		return versionID, nil
	}

	base := "https://meta.fabricmc.net/v2/versions/loader/"
	if kind == KindQuilt {
		base = "https://meta.quiltmc.org/v3/versions/loader/"
	}
	endpoint := base + url.PathEscape(mc) + "/" + url.PathEscape(loaderVersion) + "/profile/json"
	body, _, fetchErr := fetchMetadataDocument(ctx, root, kind, mc, "profile-"+loaderVersion, endpoint, profileMaxBytes)
	if fetchErr != nil {
		return "", profileError("无法获取加载器官方安装配置，请检查网络后重试。", true)
	}
	var profile profileDocument
	if unmarshalErr := json.Unmarshal(body, &profile); unmarshalErr != nil {
		return "", profileError("加载器官方安装配置格式不正确。", false)
	}
	if strings.TrimSpace(profile.ID) != versionID {
		return "", profileError("加载器官方安装配置的版本标识不匹配。", false)
	}
	if strings.TrimSpace(profile.InheritsFrom) != mc {
		return "", profileError("加载器官方安装配置指向了不匹配的 Minecraft 版本。", false)
	}
	if strings.TrimSpace(profile.MainClass) == "" || len(profile.Libraries) == 0 {
		return "", profileError("加载器官方安装配置缺少启动信息。", false)
	}
	body, normalizeErr := normalizeProfileLibraries(ctx, root, kind, mc, body)
	if normalizeErr != nil {
		return "", profileError(normalizeErr.Error(), true)
	}
	if writeErr := writeProfileAtomic(absolute, body); writeErr != nil {
		return "", profileError(writeErr.Error(), true)
	}
	if !profileFileValid(absolute, versionID, mc) {
		return "", profileError("加载器安装配置写入后校验失败。", true)
	}
	return versionID, nil
}

func profileFileValid(absolute, expectedID, minecraftVersion string) bool {
	info, err := os.Stat(absolute)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > profileMaxBytes {
		return false
	}
	body, err := os.ReadFile(absolute)
	if err != nil {
		return false
	}
	var profile profileDocument
	if err := json.Unmarshal(body, &profile); err != nil {
		return false
	}
	return profile.ID == expectedID && profile.InheritsFrom == minecraftVersion &&
		strings.TrimSpace(profile.MainClass) != "" && profileLibrariesReady(profile.Libraries)
}

func writeProfileAtomic(absolute string, body []byte) error {
	directory := filepath.Dir(absolute)
	if err := os.MkdirAll(directory, 0755); err != nil {
		return errors.New("创建加载器版本目录失败。")
	}
	temp, err := os.CreateTemp(directory, ".loader-profile-*")
	if err != nil {
		return errors.New("创建加载器安装配置失败。")
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if _, err := temp.Write(body); err != nil {
		temp.Close()
		return errors.New("写入加载器安装配置失败。")
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return errors.New("同步加载器安装配置失败。")
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("关闭加载器安装配置失败：%w", err)
	}
	if err := os.Chmod(tempPath, 0644); err != nil {
		return errors.New("设置加载器安装配置权限失败。")
	}
	if err := os.Rename(tempPath, absolute); err != nil {
		return errors.New("提交加载器安装配置失败。")
	}
	return nil
}
