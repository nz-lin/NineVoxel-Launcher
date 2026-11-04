// Backend installation bridge. These types are never exposed by VersionService.
package version

import (
	"context"
	"example.com/ninevoxel/launcher/internal/artifact"
	"example.com/ninevoxel/launcher/internal/download"
	"fmt"
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
	if len(plan.Warnings) > 0 {
		return Installation{}, fmt.Errorf("版本安装计划不完整：%s", plan.Warnings[0].Message)
	}
	result := Installation{Plan: download.TransferPlan{VersionID: versionID, PlanHash: plan.PlanHash}, JavaMajor: merged.JavaMajorVersion}
	if merged.AssetIndex != nil {
		result.AssetIndexID = merged.AssetIndex.ID
	}
	for _, item := range plan.Artifacts {
		kind := download.TaskTypeLibrary
		switch item.Kind {
		case "client":
			kind = download.TaskTypeClientJar
		case "asset":
			kind = download.TaskTypeAssetIndex
		case "library", "native", "logging":
			// 这三类直接落在最终布局，无需额外的安装步骤。
		default:
			return Installation{}, fmt.Errorf("未知的制品类型：%s", item.Kind)
		}

		result.Plan.Items = append(result.Plan.Items, download.PlanItem{ArtifactID: item.LogicalKey, TaskType: kind, SourceURI: item.SourceURI, TargetRelPath: item.TargetRelPath, TargetKind: download.TargetKindShared, ExpectedSize: item.ExpectedSize, ExpectedSHA1: item.SHA1, ExpectedSHA256: item.SHA256})

		// 只有 natives 需要安装步骤：下载的是压缩包，必须解压到版本目录。
		// 其余制品下载完成后就已经位于 classpath 引用的位置，复制一遍纯属浪费。
		if !item.Extract {
			continue
		}
		extractTarget := "versions/" + versionID + "/natives"
		if _, err := artifact.ValidateRelativePath(extractTarget); err != nil {
			return Installation{}, err
		}
		result.Files = append(result.Files, InstallFile{
			Source:  item.TargetRelPath,
			Target:  extractTarget,
			Extract: true,
			Exclude: item.ExtractExclude,
			SHA1:    item.SHA1,
			Size:    item.ExpectedSize,
		})
	}
	if len(result.Plan.Items) == 0 {
		return Installation{}, fmt.Errorf("安装计划为空")
	}
	return result, nil
}
