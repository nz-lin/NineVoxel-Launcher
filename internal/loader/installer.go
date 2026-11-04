package loader

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
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

// installerFailureMessage 把安装器输出归纳成一句可读的失败原因。
//
// 安装器的完整输出可能有几十 KB 且混着下载日志，不能直接展示；但它对失败
// 原因的描述往往很具体（缺少 launcher_profiles.json、代理不可用、Java 版本
// 不匹配），只回一句「执行失败」会让用户无从下手。
//
// 只提取第一条匹配到的已知原因；都匹配不到时退回通用文案。
func installerFailureMessage(output string) string {
	lower := strings.ToLower(output)
	cases := []struct {
		marker  string
		message string
	}{
		{"there is no minecraft launcher profile", "官方安装器未找到 launcher_profiles.json，请重试安装。"},
		{"connection reset", "官方安装器下载依赖时连接被重置，请检查网络或代理设置后重试。"},
		{"connection timed out", "官方安装器下载依赖超时，请检查网络或代理设置后重试。"},
		{"unknownhostexception", "官方安装器无法解析下载地址的域名，请检查网络或 DNS 设置。"},
		{"access denied", "官方安装器没有写入权限，请检查数据目录权限。"},
		{"unsupportedclassversionerror", "当前 Java 版本过低，无法运行该加载器安装器，请改用更高版本的 Java。"},
		{"could not find or load main class", "官方安装器文件可能已损坏，请重新下载后重试。"},
	}
	for _, item := range cases {
		if strings.Contains(lower, item.marker) {
			return item.message
		}
	}
	return "官方安装器执行失败，请确认 Java 版本与磁盘权限后重试。"
}

type limitedBuffer struct {
	buffer bytes.Buffer
	limit  int
}

// String 返回已捕获输出的文本。
func (b *limitedBuffer) String() string { return b.buffer.String() }

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

func safeVersionID(versionID string) bool {
	if versionID == "" || versionID == "." || versionID == ".." || len(versionID) > 128 {
		return false
	}
	normalized, err := artifact.ValidateRelativePath(versionID)
	return err == nil && normalized == versionID && !strings.ContainsAny(versionID, `/\\:`)
}
