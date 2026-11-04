package loader

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"example.com/ninevoxel/launcher/internal/artifact"
	"example.com/ninevoxel/launcher/internal/download"
)

const installerChecksumMaxBytes = 4 << 10

// Installer describes a verified official installer and its download plan.
type Installer struct {
	Plan          download.TransferPlan
	InstallerPath string
	VersionID     string
}

// PrepareInstaller resolves the official Forge/NeoForge installer, its SHA-1
// checksum and the deterministic cache target. The installer is never executed
// until the download queue has verified the file.
func PrepareInstaller(ctx context.Context, kind Kind, minecraftVersion, loaderVersion string) (Installer, *Error) {
	if kind != KindForge && kind != KindNeoForge {
		return Installer{}, inputError("该加载器不需要官方安装器。")
	}
	mc, err := validateMinecraftVersion(minecraftVersion)
	if err != nil {
		return Installer{}, inputError(err.Error())
	}
	loaderVersion, err = validateLoaderVersion(loaderVersion)
	if err != nil {
		return Installer{}, inputError(err.Error())
	}
	groupPath, coordinate, fileName, expectedID, err := installerCoordinates(kind, mc, loaderVersion)
	if err != nil {
		return Installer{}, inputError(err.Error())
	}
	root, rootErr := artifact.DataRoot()
	if rootErr != nil {
		return Installer{}, cacheError("无法访问启动器数据目录。")
	}
	installerURL := "https://maven.neoforged.net/releases/net/neoforged/" + groupPath + "/" + coordinate + "/" + fileName
	if kind == KindForge {
		installerURL = "https://maven.minecraftforge.net/net/minecraftforge/forge/" + coordinate + "/" + fileName
	}
	checksumBody, _, checksumErr := fetchMetadataDocument(
		ctx, root, kind, mc, "installer-checksum-"+loaderVersion,
		installerURL+".sha1", installerChecksumMaxBytes,
	)
	if checksumErr != nil {
		return Installer{}, installerError("无法获取官方安装器校验和，请检查网络后重试。", true)
	}
	checksum, err := parseSHA1(checksumBody)
	if err != nil {
		return Installer{}, installerError("官方安装器校验和格式不正确。", false)
	}
	installerSize, sizeErr := probeMetadataFileSize(ctx, installerURL)
	if sizeErr != nil {
		return Installer{}, installerError("无法获取官方安装器文件大小，请检查网络后重试。", true)
	}
	targetRel, targetErr := artifact.ObjectRelPath(artifact.HashAlgorithmSHA1, checksum)
	if targetErr != nil {
		return Installer{}, installerError("官方安装器缓存路径无效。", false)
	}
	installerPath, resolveErr := artifact.ResolveInsideRoot(root, targetRel)
	if resolveErr != nil {
		return Installer{}, installerError("官方安装器缓存路径无效。", false)
	}
	planHash := installerPlanHash(kind, mc, loaderVersion, checksum)
	return Installer{
		Plan: download.TransferPlan{
			VersionID: expectedID,
			PlanHash:  planHash,
			Items: []download.PlanItem{{
				ArtifactID:    string(kind) + "/installer/" + loaderVersion,
				TaskType:      download.TaskTypeLoader,
				SourceURI:     installerURL,
				TargetRelPath: targetRel,
				TargetKind:    download.TargetKindShared,
				ExpectedSHA1:  checksum,
				ExpectedSize:  installerSize,
			}},
		},
		InstallerPath: installerPath,
		VersionID:     expectedID,
	}, nil
}

func installerCoordinates(kind Kind, minecraftVersion, loaderVersion string) (groupPath, coordinate, fileName, versionID string, err error) {
	versionID, err = VersionID(kind, minecraftVersion, loaderVersion)
	if err != nil {
		return "", "", "", "", err
	}
	switch kind {
	case KindForge:
		coordinate = minecraftVersion + "-" + loaderVersion
		return "", coordinate, "forge-" + coordinate + "-installer.jar", versionID, nil
	case KindNeoForge:
		if minecraftVersion == "1.20.1" {
			coordinate = minecraftVersion + "-" + loaderVersion
			return "forge", coordinate, "forge-" + coordinate + "-installer.jar", versionID, nil
		}
		return "neoforge", loaderVersion, "neoforge-" + loaderVersion + "-installer.jar", versionID, nil
	default:
		return "", "", "", "", errors.New("该加载器不需要官方安装器")
	}
}

func parseSHA1(body []byte) (string, error) {
	for _, field := range strings.Fields(string(body)) {
		field = strings.ToLower(strings.TrimSpace(field))
		if _, value, found := strings.Cut(field, "="); found {
			field = strings.TrimSpace(value)
		}
		if len(field) != 40 {
			continue
		}
		if _, err := hex.DecodeString(field); err != nil {
			continue
		}
		return field, nil
	}
	return "", errors.New("checksum missing")
}

func installerPlanHash(kind Kind, minecraftVersion, loaderVersion, checksum string) string {
	sum := sha256.Sum256([]byte("loader-installer\x00" + string(kind) + "\x00" + minecraftVersion + "\x00" + loaderVersion + "\x00" + checksum))
	return hex.EncodeToString(sum[:16])
}

// InstallerContext 明确区分安装器的三种路径语义。
//
// 不要用一个参数同时承担「工作目录」和「安装目标」：官方安装器会在自己的工作
// 目录里读写 launcher_profiles.json 等临时文件，而安装产物必须落在核心根。
// 两者混同会让并发安装互相覆盖配置。
type InstallerContext struct {
	// WorkingDirectory 是安装器进程的工作目录，每次安装独立。
	WorkingDirectory string
	// GameDirectory 是 --installClient 的目标目录（= 核心根）。
	GameDirectory string
	// ResourceRoot 是只读资源根。
	//
	// 当前与 GameDirectory 相同；分开表达便于将来把安装重定向到暂存目录后
	// 再原子切换。
	ResourceRoot string
}

// RunInstaller executes the verified official installer in client mode. The
// upstream installer performs its own downloads and processor steps; output is
// captured only for a concise failure indication and never echoed verbatim to
// the UI.
func RunInstaller(ctx context.Context, javaPath string, paths InstallerContext, installerPath string) error {
	if !filepath.IsAbs(javaPath) {
		return installerError("安装器 Java 路径无效。", false)
	}
	if !filepath.IsAbs(paths.GameDirectory) || !filepath.IsAbs(installerPath) {
		return installerError("安装器目录无效。", false)
	}
	if !filepath.IsAbs(paths.WorkingDirectory) {
		return installerError("安装器工作目录无效。", false)
	}
	if info, err := os.Stat(installerPath); err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		return installerError("官方安装器文件不完整，请重试下载。", true)
	}
	if err := ensureLauncherProfiles(paths.WorkingDirectory); err != nil {
		return err
	}
	command := exec.CommandContext(ctx, javaPath, "-jar", installerPath, "--installClient")
	// 安装器自身以工作目录为基准寻找 launcher_profiles.json 等文件，
	// 而 --installClient 不带目录参数时默认写入当前目录。
	// 因此这里必须切到核心根，才能让产物与实例目录分离。
	command.Dir = paths.GameDirectory
	if err := copyLauncherProfiles(paths.WorkingDirectory, paths.GameDirectory); err != nil {
		return err
	}
	output := &limitedBuffer{limit: 128 << 10}
	command.Stdout = output
	command.Stderr = output
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return installerError("加载器安装已中断，请重试。", true)
		}
		return installerError("官方安装器执行失败，请确认 Java 版本与磁盘权限后重试。", true)
	}
	return nil
}

// copyLauncherProfiles 把工作区里的 launcher_profiles.json 复制到安装目标目录。
//
// 官方安装器要求该文件存在。我们在独立工作区里生成模板，再带到核心根：
// 这样多个安装并发时不会互相改写，同时满足安装器的前置条件。
//
// 已存在时不覆盖：它可能包含用户手工添加的内容，覆盖没有收益。
func copyLauncherProfiles(workDir, gameDir string) error {
	source := filepath.Join(workDir, "launcher_profiles.json")
	target := filepath.Join(gameDir, "launcher_profiles.json")
	if _, statErr := os.Stat(target); statErr == nil {
		return nil
	}
	content, readErr := os.ReadFile(source)
	if readErr != nil {
		return installerError("无法读取安装器工作区配置。", false)
	}
	if writeErr := os.WriteFile(target, content, 0o644); writeErr != nil {
		return installerError("无法写入安装器配置。", false)
	}
	return nil
}

// ensureLauncherProfiles creates the minimal launcher profile document that
// Forge/NeoForge installers require. Existing user data is never replaced; only
// a missing file is created, while malformed files fail with an actionable error.
func ensureLauncherProfiles(gameDirectory string) error {
	path := filepath.Join(gameDirectory, "launcher_profiles.json")
	info, err := os.Stat(path)
	if err == nil {
		if !info.Mode().IsRegular() {
			return installerError("实例目录中的 launcher_profiles.json 不是普通文件。", false)
		}
		if info.Size() > 2<<20 {
			return installerError("实例目录中的 launcher_profiles.json 过大。", false)
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return installerError("无法读取实例目录中的 launcher_profiles.json。", false)
		}
		var document map[string]any
		if jsonErr := json.Unmarshal(body, &document); jsonErr != nil {
			return installerError("实例目录中的 launcher_profiles.json 格式损坏，请修复后再安装加载器。", false)
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return installerError("无法检查实例目录中的 launcher_profiles.json。", false)
	}

	body := []byte("{\n  \"profiles\": {},\n  \"settings\": {},\n  \"version\": 3\n}\n")
	temp, createErr := os.CreateTemp(gameDirectory, ".launcher-profiles-*")
	if createErr != nil {
		return installerError("无法创建启动器配置文件。", false)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if _, writeErr := temp.Write(body); writeErr != nil {
		temp.Close()
		return installerError("无法写入启动器配置文件。", false)
	}
	if syncErr := temp.Sync(); syncErr != nil {
		temp.Close()
		return installerError("无法同步启动器配置文件。", false)
	}
	if closeErr := temp.Close(); closeErr != nil {
		return installerError("无法关闭启动器配置文件。", false)
	}
	if renameErr := os.Rename(tempPath, path); renameErr != nil {
		return installerError("无法提交启动器配置文件。", false)
	}
	return nil
}

type limitedBuffer struct {
	buffer bytes.Buffer
	limit  int
}

func (b *limitedBuffer) Write(data []byte) (int, error) {
	original := len(data)
	remaining := b.limit - b.buffer.Len()
	if remaining > 0 {
		if len(data) > remaining {
			data = data[:remaining]
		}
		_, _ = b.buffer.Write(data)
	}
	return original, nil
}

// DetectInstalledVersionID finds the version directory created by a Forge or
// NeoForge installer. The expected identifier is preferred; a scan is used for
// loader releases whose directory naming changed across versions.
func DetectInstalledVersionID(gameDirectory string, kind Kind, minecraftVersion, loaderVersion string, before map[string]bool) (string, error) {
	expected, err := VersionID(kind, minecraftVersion, loaderVersion)
	if err != nil {
		return "", err
	}
	if installedLoaderVersionJSON(gameDirectory, expected, minecraftVersion) {
		return expected, nil
	}
	entries, readErr := os.ReadDir(filepath.Join(gameDirectory, "versions"))
	if readErr != nil {
		return "", errors.New("安装器未生成版本目录")
	}
	matches := make([]string, 0, 2)
	for _, entry := range entries {
		id := entry.Name()
		if !entry.IsDir() || before[id] || !safeVersionID(id) {
			continue
		}
		if !installedLoaderVersionJSON(gameDirectory, id, minecraftVersion) {
			continue
		}
		lower := strings.ToLower(id)
		if strings.Contains(lower, string(kind)) || (kind == KindNeoForge && minecraftVersion == "1.20.1" && strings.Contains(lower, "forge")) {
			matches = append(matches, id)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		return "", errors.New("安装器生成了多个候选版本目录")
	}
	return "", errors.New("安装器未生成可识别的版本目录")
}

func installedLoaderVersionJSON(gameDirectory, versionID, minecraftVersion string) bool {
	if !safeVersionID(versionID) {
		return false
	}
	body, err := os.ReadFile(filepath.Join(gameDirectory, "versions", versionID, versionID+".json"))
	if err != nil || len(body) == 0 || len(body) > 8<<20 {
		return false
	}
	var document struct {
		ID           string `json:"id"`
		InheritsFrom string `json:"inheritsFrom"`
		MainClass    string `json:"mainClass"`
	}
	if err := json.Unmarshal(body, &document); err != nil {
		return false
	}
	return document.ID == versionID && document.InheritsFrom == minecraftVersion && strings.TrimSpace(document.MainClass) != ""
}

func safeVersionID(versionID string) bool {
	if versionID == "" || versionID == "." || versionID == ".." || len(versionID) > 128 {
		return false
	}
	normalized, err := artifact.ValidateRelativePath(versionID)
	return err == nil && normalized == versionID && !strings.ContainsAny(versionID, `/\\:`)
}
