// Backend installation bridge. These types are never exposed by VersionService.
package version

import (
	"context"
	"example.com/ninevoxel/launcher/internal/artifact"
	"example.com/ninevoxel/launcher/internal/download"
	"fmt"
	"path"
	"strings"
)

// InstallFile maps a verified cache object to the standard Minecraft layout.
type InstallFile struct {
	Source  string
	Target  string
	Extract bool
	Exclude []string
	SHA1    string
	Size    int64
}

type Installation struct {
	Plan         download.TransferPlan
	Files        []InstallFile
	AssetIndexID string
	JavaMajor    int
}

func DownloadPlan(_ context.Context, req download.StartDownloadPlanRequest) (download.TransferPlan, *download.Error) {
	prepared, err := PrepareInstallation(req.VersionID)
	if err != nil {
		return download.TransferPlan{}, &download.Error{Code: "ARTIFACT_PLAN_001", Message: err.Error()}
	}
	return prepared.Plan, nil
}

// PrepareInstallation resolves official vanilla metadata and all platform artifacts.
func PrepareInstallation(versionID string) (Installation, error) {
	if err := EnsureVersionMetadata(versionID); err != nil {
		return Installation{}, err
	}
	root, appErr := resolveDataRoot()
	if appErr != nil {
		return Installation{}, appErr
	}
	plan, appErr := buildInstallPlan(root, versionID)
	if appErr != nil {
		return Installation{}, appErr
	}
	merged, _, _, appErr := loadInstallVersion(root, versionID)
	if appErr != nil {
		return Installation{}, appErr
	}
	if len(merged.InheritChain) != 1 {
		return Installation{}, fmt.Errorf("暂不支持继承版本或模组加载器安装")
	}
	if len(plan.Warnings) > 0 {
		return Installation{}, fmt.Errorf("版本安装计划不完整：%s", plan.Warnings[0].Message)
	}
	result := Installation{Plan: download.TransferPlan{VersionID: versionID, PlanHash: plan.PlanHash}, JavaMajor: merged.JavaMajorVersion}
	if merged.AssetIndex != nil {
		result.AssetIndexID = merged.AssetIndex.ID
	}
	for _, item := range plan.Artifacts {
		kind := download.TaskTypeLibrary
		target := ""
		switch item.Kind {
		case "client":
			kind = download.TaskTypeClientJar
			target = "versions/" + versionID + "/" + merged.ClientJarID + ".jar"
		case "asset":
			kind = download.TaskTypeAssetIndex
			target = "assets/indexes/" + result.AssetIndexID + ".json"
		case "library":
			for _, lib := range merged.Libraries {
				if lib.Name == item.LogicalKey && lib.Downloads != nil && lib.Downloads.Artifact != nil {
					target = "libraries/" + libraryInstallPath(lib.Name, lib.Downloads.Artifact.Path)
					break
				}
			}
		case "native":
			target = "versions/" + versionID + "/natives"
		case "logging":
			target = "assets/log_configs/" + strings.TrimPrefix(item.LogicalKey, "logConfig/")
		}
		if target == "" {
			return Installation{}, fmt.Errorf("无法确定制品安装路径：%s", item.LogicalKey)
		}
		if _, err := artifact.ValidateRelativePath(target); err != nil {
			return Installation{}, err
		}
		result.Plan.Items = append(result.Plan.Items, download.PlanItem{ArtifactID: item.LogicalKey, TaskType: kind, SourceURI: item.SourceURI, TargetRelPath: item.TargetRelPath, TargetKind: download.TargetKindShared, ExpectedSize: item.ExpectedSize, ExpectedSHA1: item.SHA1, ExpectedSHA256: item.SHA256})
		result.Files = append(result.Files, InstallFile{Source: item.TargetRelPath, Target: target, Extract: item.Extract, Exclude: item.ExtractExclude, SHA1: item.SHA1, Size: item.ExpectedSize})
	}
	if len(result.Plan.Items) == 0 {
		return Installation{}, fmt.Errorf("安装计划为空")
	}
	result.Files = append(result.Files, InstallFile{Source: "versions/" + versionID + "/" + versionID + ".json", Target: "versions/" + versionID + "/" + versionID + ".json"})
	return result, nil
}

func libraryInstallPath(coordinate, declared string) string {
	if declared != "" {
		return declared
	}
	parts := strings.Split(coordinate, ":")
	if len(parts) < 3 {
		return "../invalid"
	}
	file := parts[1] + "-" + parts[2]
	if len(parts) > 3 {
		file += "-" + parts[3]
	}
	return path.Join(strings.ReplaceAll(parts[0], ".", "/"), parts[1], parts[2], file+".jar")
}
