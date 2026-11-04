package instance

import (
	"errors"
	"path/filepath"
	"strings"

	"example.com/ninevoxel/launcher/internal/artifact"
)

// launchDataRoot is replaceable in tests; production always uses artifact.DataRoot.
var launchDataRoot = artifact.DataRoot

// LaunchConfig is the filesystem-level configuration needed to launch one
// instance. It is intentionally not exposed through the Wails service: the
// game directory is an absolute path and must stay in the backend.
type LaunchConfig struct {
	GameDirectory       string
	MCVersion           string
	MemoryMB            int
	JVMArgs             string
	CustomJavaPath      string
	CustomJavaRuntimeID string
	Installed           bool
}

// ResolveLaunchConfig reads instance.json as the source of truth and returns
// the absolute game directory plus launch settings.
//
// The SQLite index is deliberately not consulted here. If the user moved or
// deleted the instance folder, the launch must fail explicitly instead of
// launching a path remembered only by an old index row.
func ResolveLaunchConfig(instanceID string) (LaunchConfig, error) {
	id := strings.TrimSpace(instanceID)
	if !validateInstanceID(id) {
		return LaunchConfig{}, errors.New("实例标识无效")
	}

	root, rootErr := launchDataRoot()
	if rootErr != nil {
		return LaunchConfig{}, errors.New("无法访问实例数据目录")
	}
	instanceDirectory, pathErr := artifact.ResolveInsideRoot(root, activeInstanceRel(id))
	if pathErr != nil {
		return LaunchConfig{}, errors.New("实例目录无效")
	}
	manifest, manifestErr := readInstanceManifest(instanceDirectory)
	if errors.Is(manifestErr, errManifestNotFound) {
		return LaunchConfig{}, errInstanceNotFound
	}
	if manifestErr != nil {
		return LaunchConfig{}, manifestErr
	}
	if manifest.ID != id {
		return LaunchConfig{}, errors.New("实例清单标识不匹配")
	}
	if manifest.TrashedAtMS > 0 {
		return LaunchConfig{}, errors.New("实例位于回收站，无法启动")
	}

	gameDirectory, err := artifact.ResolveInsideRoot(root, activeGameRel(id))
	if err != nil {
		return LaunchConfig{}, errors.New("实例游戏目录无效")
	}
	if info, statErr := filepath.Abs(gameDirectory); statErr == nil {
		gameDirectory = info
	}

	launchVersionID := strings.TrimSpace(manifest.VersionID)
	if launchVersionID == "" {
		launchVersionID = manifest.MCVersion
	}
	if _, pathErr := artifact.ValidateRelativePath(launchVersionID); pathErr != nil || strings.ContainsAny(launchVersionID, `/\\:`) {
		return LaunchConfig{}, errors.New("实例启动版本标识无效")
	}
	installed := manifest.InstallationState == "installed" &&
		coreVersionInstalled(root, launchVersionID)
	return LaunchConfig{
		GameDirectory:       gameDirectory,
		MCVersion:           launchVersionID,
		MemoryMB:            manifest.MemoryMB,
		JVMArgs:             manifest.JVMArgs,
		CustomJavaPath:      manifest.CustomJavaPath,
		CustomJavaRuntimeID: manifest.CustomJavaRuntimeID,
		Installed:           installed,
	}, nil
}
