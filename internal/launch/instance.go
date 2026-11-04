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
	GameDirectory string
	MCVersion     string

	// PatchDirectory 是实例补丁目录（绝对路径）；为空表示没有实例级覆盖。
	//
	// 不出后端：它是宿主路径，只用于读取版本声明覆盖。
	PatchDirectory string

	// MemoryMode 决定内存取值来源；MemoryMB 仅在 custom 模式下生效。
	MemoryMode MemoryMode
	MemoryMB   int

	// JVMMode 决定是否采用实例自己的 JVM 参数。
	JVMMode JVMMode
	JVMArgs string

	// JavaMode 决定 Java 来源；两个字段仅在对应模式下生效。
	JavaMode            JavaMode
	CustomJavaPath      string
	CustomJavaRuntimeID string

	Installed bool
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
	// resolveJavaForMajor 按版本要求的 Java 大版本挑选运行时。
	//
	// 未注入时为空，调用方必须判空：自动匹配是增强能力，缺失时退回
	// 「已选运行时」，不能让启动直接失败。
	resolveJavaForMajor func(int) (string, error)
	// requiredJavaMajorFor 返回版本声明的 Java 大版本要求（0 表示未知）。
	//
	// 抽成变量是为了让「自动匹配」本身可测：真实实现要读核心根的版本 JSON，
	// 单元测试不该为此准备一整份版本元数据。
	requiredJavaMajorFor = requiredJavaMajorVersion
	launchSettingString  = settings.StringSetting
	launchSettingInt     = settings.IntSetting
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

// SetJavaMajorResolver 注入「按版本要求的 Java 大版本挑选运行时」的解析器。
//
// 由 internal/app 在装配阶段绑定到 runtime 包：launch 不直接依赖 runtime，
// 避免把运行时探测与下载带进启动参数组装的依赖链。
func SetJavaMajorResolver(resolver func(int) (string, error)) {
	if resolver != nil {
		resolveJavaForMajor = resolver
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
		resolved.patchDirectory = config.PatchDirectory

		// 内存：模式决定是否采用实例值。跟随全局时置 0，交给下面的全局回退。
		if config.MemoryMode == MemoryCustom {
			resolved.MemoryMB = config.MemoryMB
		} else {
			resolved.MemoryMB = 0
		}

		// JVM 参数：仅 custom 模式采用实例值。特别注意 follow-global 时
		// 必须完全不拼接——旧实现无条件 append，导致「跟随全局」也会带上
		// 实例参数，与模式语义矛盾。
		if config.JVMMode == JVMCustom {
			instanceArgs := splitLegacyArguments(config.JVMArgs)
			resolved.AdditionalJvmArgs = append(resolved.AdditionalJvmArgs, instanceArgs...)
		}

		// Java：只有显式选择 managed / custom 时才覆盖全局。
		switch config.JavaMode {
		case JavaManaged:
			javaPath, javaErr := resolveJavaRuntimeByID(config.CustomJavaRuntimeID)
			if javaErr != nil {
				return StartLaunchRequest{}, newInputError(ErrorCodeLaunchRequest,
					"实例指定的 Java 运行时不可用："+javaErr.Error())
			}
			resolved.JavaPath = javaPath
		case JavaCustom:
			resolved.JavaPath = strings.TrimSpace(config.CustomJavaPath)
		}
	}

	if strings.TrimSpace(resolved.JavaPath) == "" {
		if globalPath := strings.TrimSpace(launchSettingString(settings.KeyLaunchJavaPath, "")); globalPath != "" {
			resolved.JavaPath = globalPath
		} else {
			// 没有显式全局 Java 时，先按版本声明的 Java 大版本自动匹配。
			//
			// 为什么优先于「已选运行时」：用户通常不会手动配置 Java，
			// 而「已选运行时」往往只是上次装某个版本时选中的那个——
			// 用 Java 25 跑要求 Java 8 的 1.7.10 会直接崩，而版本 JSON 里的
			// javaVersion.majorVersion 是权威要求。
			autoMatched := false
			if resolveJavaForMajor != nil {
				if major := requiredJavaMajorFor(resolved.VersionID); major > 0 {
					if path, majorErr := resolveJavaForMajor(major); majorErr == nil {
						resolved.JavaPath = path
						autoMatched = true
					}
				}
			}
			if !autoMatched {
				javaPath, javaErr := resolveSelectedJavaRuntime()
				if javaErr != nil {
					return StartLaunchRequest{}, newInputError(ErrorCodeLaunchRequest,
						"未找到可用的 Java：请在设置中选择 Java，或先在 Java 管理器中安装运行时。")
				}
				resolved.JavaPath = javaPath
			}
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

// RequiredJavaMajorForVersion 返回某个版本声明的 Java 大版本要求；0 表示未知。
//
// 供启动器在装配阶段复用同一套判定（安装器选择 Java 时也要用）：
// 两处各写一份「怎么读版本要求」迟早会漂移。
func RequiredJavaMajorForVersion(versionID string) int {
	return requiredJavaMajorVersion(versionID)
}

// requiredJavaMajorVersion 返回某个版本声明的 Java 大版本要求。
//
// 读的是核心根里的版本 JSON（含继承链合并）：加载器版本的 JSON 通常不重复声明
// javaVersion，要求来自它继承的原版版本，因此必须走合并链而不是只读顶层文件。
//
// 任何读取失败或未声明都返回 0：这是「不知道要求」，调用方应退回原有逻辑，
// 而不是把启动卡死在这里。
func requiredJavaMajorVersion(versionID string) int {
	id := strings.TrimSpace(versionID)
	if id == "" {
		return 0
	}
	resourceRoot, rootErr := resolveResourceRoot()
	if rootErr != nil {
		return 0
	}
	traits, traitsErr := AssessVersionTraits(resourceRoot, id)
	if traitsErr != nil {
		return 0
	}
	if traits.JavaMajorVersion <= 0 {
		return 0
	}
	return traits.JavaMajorVersion
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
