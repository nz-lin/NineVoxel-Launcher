package instance

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"example.com/ninevoxel/launcher/internal/artifact"
)

const (
	manifestFileName      = "instance.json"
	manifestSchemaVersion = 1
	instancesRootRel      = "instances"
	trashRootRel          = "instances/.trash"
)

var errManifestNotFound = errors.New("实例清单不存在")

// instanceManifest 是实例目录中的权威配置。目录本身可被用户复制、移动或备份，
// SQLite 只保存可重建的索引与界面状态。
type instanceManifest struct {
	SchemaVersion int    `json:"schemaVersion"`
	ID            string `json:"id"`
	Config
	JavaVersion          string  `json:"javaVersion,omitempty"`
	InstallationState    string  `json:"installationState,omitempty"`
	InstallationProgress float64 `json:"installationProgress,omitempty"`
	InstallationError    string  `json:"installationError,omitempty"`
	InstallTaskID        string  `json:"installTaskId,omitempty"`
	VersionID            string  `json:"versionId,omitempty"`
	CreatedAtMS          int64   `json:"createdAtMs"`
	TrashedAtMS          int64   `json:"trashedAtMs,omitempty"`
	needsRewrite         bool
}

func manifestFromInstance(item Instance, trashedAtMS int64) instanceManifest {
	return instanceManifest{
		SchemaVersion:        manifestSchemaVersion,
		ID:                   item.ID,
		Config:               item.Config,
		JavaVersion:          item.JavaVersion,
		InstallationState:    item.InstallationState,
		InstallationProgress: item.InstallationProgress,
		InstallationError:    item.InstallationError,
		InstallTaskID:        item.InstallTaskID,
		VersionID:            item.VersionID,
		CreatedAtMS:          item.CreatedAtMS,
		TrashedAtMS:          trashedAtMS,
	}
}

func (m instanceManifest) toInstance(folderPath string) Instance {
	return Instance{
		Config:               m.Config,
		ID:                   m.ID,
		FolderPath:           folderPath,
		JavaVersion:          m.JavaVersion,
		InstallationState:    m.InstallationState,
		InstallationProgress: m.InstallationProgress,
		InstallationError:    m.InstallationError,
		InstallTaskID:        m.InstallTaskID,
		VersionID:            m.VersionID,
		CreatedAtMS:          m.CreatedAtMS,
	}
}

func activeInstanceRel(id string) string {
	return instancesRootRel + "/" + id
}

func activeGameRel(id string) string {
	return activeInstanceRel(id) + "/.minecraft"
}

func trashInstanceRel(trashID string) string {
	return trashRootRel + "/" + trashID
}

func validateInstanceID(id string) bool {
	if len(id) != 37 || !strings.HasPrefix(id, "inst-") {
		return false
	}
	for _, char := range id[5:] {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func validateTrashID(id string) bool {
	if id == "" || id == "." || id == ".." || len(id) > 160 || !strings.HasPrefix(id, "inst-") {
		return false
	}
	for _, char := range id[5:] {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') && char != '-' {
			return false
		}
	}
	return true
}

func readInstanceManifest(directory string) (instanceManifest, error) {
	raw, err := os.ReadFile(filepath.Join(directory, manifestFileName))
	if os.IsNotExist(err) {
		return instanceManifest{}, errManifestNotFound
	}
	if err != nil {
		return instanceManifest{}, errors.New("无法读取实例清单")
	}
	if len(raw) == 0 || len(raw) > 1<<20 {
		return instanceManifest{}, errors.New("实例清单大小异常")
	}
	var manifest instanceManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return instanceManifest{}, errors.New("实例清单格式损坏")
	}
	if manifest.SchemaVersion == 0 {
		manifest.SchemaVersion = manifestSchemaVersion
		manifest.needsRewrite = true
	}
	if manifest.SchemaVersion != manifestSchemaVersion {
		return instanceManifest{}, errors.New("实例清单版本不受支持")
	}
	if !validateInstanceID(manifest.ID) {
		return instanceManifest{}, errors.New("实例清单标识无效")
	}
	originalConfig := manifest.Config
	config, err := validConfig(manifest.Config)
	if err != nil {
		return instanceManifest{}, err
	}
	manifest.Config = config
	if originalConfig != config {
		manifest.needsRewrite = true
	}
	return manifest, nil
}

func writeInstanceManifest(directory string, manifest instanceManifest) error {
	if !validateInstanceID(manifest.ID) {
		return errors.New("实例清单标识无效")
	}
	manifest.SchemaVersion = manifestSchemaVersion
	config, err := validConfig(manifest.Config)
	if err != nil {
		return err
	}
	manifest.Config = config
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return errors.New("实例清单无法编码")
	}
	if err := os.MkdirAll(directory, 0755); err != nil {
		return errors.New("创建实例目录失败")
	}
	temp, err := os.CreateTemp(directory, ".instance-*.json")
	if err != nil {
		return errors.New("创建实例清单失败")
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if _, err := temp.Write(raw); err != nil {
		temp.Close()
		return errors.New("写入实例清单失败")
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return errors.New("同步实例清单失败")
	}
	if err := temp.Close(); err != nil {
		return errors.New("关闭实例清单失败")
	}
	if err := os.Chmod(tempPath, 0644); err != nil {
		return errors.New("设置实例清单权限失败")
	}
	if err := os.Rename(tempPath, filepath.Join(directory, manifestFileName)); err != nil {
		return errors.New("提交实例清单失败")
	}
	return nil
}

// recoverManifest 尝试为丢失清单的实例目录重建一份清单。
//
// 存储布局改为共享核心后，实例目录不再包含 versions / libraries / assets，
// 因此**无法**再从目录本身推断它用的是哪个游戏版本：同一版本可能被多个
// 实例共用，猜一个版本比明确失败更糟（会把实例指向错误的核心版本）。
//
// 调用方（实例扫描与回收站恢复）在收到错误后会跳过该目录或提示用户，
// 不会破坏任何数据；用户重新创建实例即可恢复使用。
func recoverManifest(directory, id string, createdMS, trashedAtMS int64) (instanceManifest, error) {
	if !validateInstanceID(id) {
		return instanceManifest{}, errors.New("实例目录标识无效")
	}
	return instanceManifest{}, errors.New("实例清单丢失，且共享核心中的版本无法确定归属")
}

// coreVersionInstalled 判断某个版本是否已在共享核心中安装完成。
//
// 存储布局改为共享核心后，libraries / assets / versions 都不在实例目录里，
// 因此「实例是否可启动」必须按核心根检查，而不是按实例目录检查。
//
// 判据取版本 JSON 是否存在：它与安装流程的产出对齐，且不依赖清单机制
// （清单是较新的能力，早期安装的版本可能还没有）。
func coreVersionInstalled(root, version string) bool {
	if version == "" {
		return false
	}
	absolute, err := artifact.ResolveInsideRoot(root, "versions/"+version+"/"+version+".json")
	if err != nil {
		return false
	}
	info, statErr := os.Stat(absolute)
	return statErr == nil && info.Mode().IsRegular()
}

func trashDeletedAt(trashID string, directory string) int64 {
	if parsed, err := strconv.ParseInt(trashID[strings.LastIndex(trashID, "-")+1:], 10, 64); err == nil && parsed > 0 {
		if parsed > 1_000_000_000_000_000 {
			return parsed / 1_000_000
		}
		return parsed
	}
	if info, err := os.Stat(directory); err == nil {
		return info.ModTime().UnixMilli()
	}
	return 0
}

func instanceIDFromTrashID(trashID string) string {
	if len(trashID) < 37 {
		return ""
	}
	id := trashID[:37]
	if validateInstanceID(id) {
		return id
	}
	return ""
}
