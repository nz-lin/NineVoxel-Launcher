package instance

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
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

func recoverManifest(directory, id string, createdMS, trashedAtMS int64) (instanceManifest, error) {
	if !validateInstanceID(id) {
		return instanceManifest{}, errors.New("实例目录标识无效")
	}
	gameDirectory := filepath.Join(directory, ".minecraft")
	version := detectInstalledVersion(gameDirectory)
	if version == "" {
		return instanceManifest{}, errors.New("实例目录中找不到可识别的游戏版本")
	}
	state := "not_installed"
	if installedVersionExists(gameDirectory, version) {
		state = "installed"
	}
	config, err := validConfig(Config{
		Name:      "恢复的实例 " + id[len(id)-8:],
		MCVersion: version,
		Loader:    "vanilla",
		MemoryMB:  4096,
	})
	if err != nil {
		return instanceManifest{}, err
	}
	if createdMS <= 0 {
		if info, statErr := os.Stat(directory); statErr == nil {
			createdMS = info.ModTime().UnixMilli()
		}
	}
	return instanceManifest{
		SchemaVersion:     manifestSchemaVersion,
		ID:                id,
		Config:            config,
		InstallationState: state,
		CreatedAtMS:       createdMS,
		TrashedAtMS:       trashedAtMS,
	}, nil
}

func detectInstalledVersion(gameDirectory string) string {
	versionsRoot := filepath.Join(gameDirectory, "versions")
	entries, err := os.ReadDir(versionsRoot)
	if err != nil {
		return ""
	}
	type candidate struct {
		name    string
		updated time.Time
	}
	candidates := make([]candidate, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || !isVersionFolderName(entry.Name()) {
			continue
		}
		if _, err := os.Stat(filepath.Join(versionsRoot, entry.Name(), entry.Name()+".json")); err != nil {
			continue
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			continue
		}
		candidates = append(candidates, candidate{name: entry.Name(), updated: info.ModTime()})
	}
	if len(candidates) == 0 {
		return ""
	}
	sort.SliceStable(candidates, func(left, right int) bool {
		if candidates[left].updated.Equal(candidates[right].updated) {
			return candidates[left].name > candidates[right].name
		}
		return candidates[left].updated.After(candidates[right].updated)
	})
	return candidates[0].name
}

func installedVersionExists(gameDirectory, version string) bool {
	if version == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(gameDirectory, "versions", version, version+".json"))
	return err == nil
}

func isVersionFolderName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, `/\\`)
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
