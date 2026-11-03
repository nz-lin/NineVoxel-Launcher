package launch

import (
	"errors"
	"fmt"
	"strings"

	"example.com/ninevoxel/launcher/internal/settings"
)

const (
	// defaultLaunchMemoryMB 是实例与全局设置都未提供内存时的保守默认值。
	defaultLaunchMemoryMB = 8192

	// minLaunchMemoryMB 与 maxLaunchMemoryMB 与实例配置的存储范围保持一致。
	minLaunchMemoryMB = 512
	maxLaunchMemoryMB = 1024 * 1024
)

// InstanceLaunchConfig is the backend-owned subset needed to launch an
// instance. It deliberately uses no instance/runtime package types so the
// assemble layer can inject resolvers without creating import cycles.
type InstanceLaunchConfig struct {
	GameDirectory       string
	MCVersion           string
	MemoryMB            int
	JVMArgs             string
	CustomJavaPath      string
	CustomJavaRuntimeID string
	Installed           bool
}

// Resolver injection points are populated by internal/app before any launch.
// Paths never cross the Wails service boundary.
var (
	resolveInstanceLaunchConfig = func(string) (InstanceLaunchConfig, error) {
		return InstanceLaunchConfig{}, errors.New("实例启动解析器尚未初始化")
	}
	resolveSelectedJavaRuntime = func() (string, error) {
		return "", errors.New("Java 运行时解析器尚未初始化")
	}
	resolveJavaRuntimeByID = func(string) (string, error) {
		return "", errors.New("Java 运行时解析器尚未初始化")
	}
	launchSettingString = settings.StringSetting
	launchSettingInt    = settings.IntSetting
)

// SetInstanceLaunchResolver injects the local-first instance resolver.
func SetInstanceLaunchResolver(resolver func(string) (InstanceLaunchConfig, error)) {
	if resolver != nil {
		resolveInstanceLaunchConfig = resolver
	}
}

// SetJavaRuntimeResolvers injects runtime-ID and selected-runtime resolution.
func SetJavaRuntimeResolvers(
	byID func(string) (string, error),
	selected func() (string, error),
) {
	if byID != nil {
		resolveJavaRuntimeByID = byID
	}
	if selected != nil {
		resolveSelectedJavaRuntime = selected
	}
}

// resolveStartLaunchRequest turns an instance-scoped request into the complete
// structured request consumed by the existing launch plan builder.
//
// The instance manifest is authoritative when InstanceID is present: callers
// cannot override its game directory, version, Java or memory. Additional JVM
// and game arguments supplied by the request are still appended and validated
// by the existing argument pipeline.
func resolveStartLaunchRequest(req StartLaunchRequest) (StartLaunchRequest, *Error) {
	resolved := req
	instanceID := strings.TrimSpace(resolved.InstanceID)
	if instanceID != "" {
		config, err := resolveInstanceLaunchConfig(instanceID)
		if err != nil {
			return StartLaunchRequest{}, newInputError(ErrorCodeLaunchRequest, err.Error())
		}
		if !config.Installed {
			return StartLaunchRequest{}, newInputError(ErrorCodeLaunchRequest,
				"实例游戏资源尚未安装完成，请先完成安装。")
		}

		resolved.GameDirectory = config.GameDirectory
		resolved.VersionID = config.MCVersion
		resolved.JavaPath = ""
		resolved.MemoryMB = config.MemoryMB
		instanceArgs := splitLegacyArguments(config.JVMArgs)
		resolved.AdditionalJvmArgs = append(instanceArgs, resolved.AdditionalJvmArgs...)

		switch {
		case strings.TrimSpace(config.CustomJavaRuntimeID) != "":
			javaPath, javaErr := resolveJavaRuntimeByID(config.CustomJavaRuntimeID)
			if javaErr != nil {
				return StartLaunchRequest{}, newInputError(ErrorCodeLaunchRequest,
					"实例指定的 Java 运行时不可用："+javaErr.Error())
			}
			resolved.JavaPath = javaPath
		case strings.TrimSpace(config.CustomJavaPath) != "":
			resolved.JavaPath = strings.TrimSpace(config.CustomJavaPath)
		}
	}

	if strings.TrimSpace(resolved.JavaPath) == "" {
		if globalPath := strings.TrimSpace(launchSettingString(settings.KeyLaunchJavaPath, "")); globalPath != "" {
			resolved.JavaPath = globalPath
		} else {
			javaPath, javaErr := resolveSelectedJavaRuntime()
			if javaErr != nil {
				return StartLaunchRequest{}, newInputError(ErrorCodeLaunchRequest,
					"未找到可用的 Java：请在设置中选择 Java，或先在 Java 管理器中安装运行时。")
			}
			resolved.JavaPath = javaPath
		}
	}

	memoryMB := resolved.MemoryMB
	if memoryMB <= 0 {
		memoryMB = launchSettingInt(settings.KeyLaunchMaxMemoryMb, defaultLaunchMemoryMB)
	}
	if memoryMB <= 0 {
		memoryMB = defaultLaunchMemoryMB
	}
	if memoryMB < minLaunchMemoryMB || memoryMB > maxLaunchMemoryMB {
		return StartLaunchRequest{}, newInputError(ErrorCodeLaunchRequest,
			fmt.Sprintf("最大内存必须介于 %d MB 和 %d MB 之间。", minLaunchMemoryMB, maxLaunchMemoryMB))
	}
	resolved.MemoryMB = memoryMB
	resolved.AdditionalJvmArgs = withMaxHeap(resolved.AdditionalJvmArgs, memoryMB)

	return resolved, nil
}

// withMaxHeap removes caller-provided heap-size flags and appends the
// instance-owned maximum heap size. This makes the instance memory setting the
// single source of truth and prevents duplicate -Xmx flags from producing
// order-dependent JVM behaviour.
func withMaxHeap(args []string, memoryMB int) []string {
	result := make([]string, 0, len(args)+1)
	skipNext := false
	for _, arg := range args {
		if skipNext {
			skipNext = false
			continue
		}
		trimmed := strings.TrimSpace(arg)
		lower := strings.ToLower(trimmed)
		if lower == "-xmx" || lower == "-xx:maxheapsize" {
			skipNext = true
			continue
		}
		if strings.HasPrefix(lower, "-xmx") || strings.HasPrefix(lower, "-xx:maxheapsize=") {
			continue
		}
		result = append(result, arg)
	}
	return append(result, fmt.Sprintf("-Xmx%dM", memoryMB))
}
