package instance

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"example.com/ninevoxel/launcher/internal/artifact"
	"example.com/ninevoxel/launcher/internal/launch"
)

// launchDataRoot is replaceable in tests; production always uses artifact.DataRoot.
var launchDataRoot = artifact.DataRoot

// LaunchConfig is the filesystem-level configuration needed to launch one
// instance. It is intentionally not exposed through the Wails service: the
// game directory is an absolute path and must stay in the backend.
type LaunchConfig struct {
	GameDirectory string
	MCVersion     string
	MemoryMB      int
	JVMArgs       string
	// PatchDirectory 是实例补丁目录的绝对路径；空表示没有实例级覆盖。
	PatchDirectory string
	// JavaMode 决定 Java 来源；只在 managed / custom 时才采用下面两个字段。
	//
	// 必须一起返回：之前只返回路径不返回模式，启动侧无法区分「用户显式指定」
	// 与「未指定」，表现为实例专属 Java 保存后仍然跟随全局。
	JavaMode            launch.JavaMode
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
	// 模式字段可能为空（旧数据或前端的部分提交）：归一化为「跟随全局」，
	// 避免空字符串在 switch 里落空后行为不确定。
	javaMode := manifest.JavaMode
	if javaMode != launch.JavaManaged && javaMode != launch.JavaCustom {
		javaMode = launch.JavaFollowGlobal
	}
	// 补丁目录只在存在时返回：不存在的目录交给 launch 侧按「无覆盖」处理，
	// 而不是让每次读取都先 Stat 一个必然不存在的路径。
	patchDirectory := ""
	if patchAbsolute, pathErr := artifact.ResolveInsideRoot(root, patchesRel(id)); pathErr == nil {
		if info, statErr := os.Stat(patchAbsolute); statErr == nil && info.IsDir() {
			if absolute, absErr := filepath.Abs(patchAbsolute); absErr == nil {
				patchDirectory = absolute
			}
		}
	}
	return LaunchConfig{
		GameDirectory:       gameDirectory,
		MCVersion:           launchVersionID,
		MemoryMB:            manifest.MemoryMB,
		JVMArgs:             manifest.JVMArgs,
		PatchDirectory:      patchDirectory,
		JavaMode:            javaMode,
		CustomJavaPath:      manifest.CustomJavaPath,
		CustomJavaRuntimeID: manifest.CustomJavaRuntimeID,
		Installed:           installed,
	}, nil
}
