package launch

// 本文件定义「跟随全局 / 自定义」的显式模式。
//
// 背景：旧实现用零值表达「继承全局」——`MemoryMB == 0` 表示跟随全局，
// `JVMArgs == ""` 表示未设置。这样无法区分「我没设置」与「我故意清空」，
// 而后者是真实需求（例如全局带了一堆 JVM 参数，某个实例想完全不用）。
//
// 模式由 instance 包存储、由本包消费，因此类型定义在这里：
// instance 依赖 launch 的方向已经存在（实例安装需要启动参数）。

import "strings"

// MemoryMode 表示实例内存的取值来源。
type MemoryMode string

const (
	// MemoryFollowGlobal 表示沿用全局设置的最大内存（默认）。
	MemoryFollowGlobal MemoryMode = "global"
	// MemoryCustom 表示使用实例自己的 MemoryMB。
	MemoryCustom MemoryMode = "custom"
)

// JVMMode 表示实例附加 JVM 参数的来源。
type JVMMode string

const (
	// JVMFollowGlobal 表示不使用实例自己的 JVM 参数（默认）。
	//
	// 当前设置库还没有「全局自定义 JVM 参数」，因此该模式等价于
	// 「不加额外参数」。将来引入全局参数时，这个模式会自动改为跟随全局。
	JVMFollowGlobal JVMMode = "global"
	// JVMCustom 表示使用实例自己的 JVMArgs；值为空串表示显式不加额外参数。
	JVMCustom JVMMode = "custom"
)

// JavaMode 表示实例 Java 可执行文件的来源。
type JavaMode string

const (
	// JavaFollowGlobal 表示按全局设置与已选运行时解析（默认）。
	JavaFollowGlobal JavaMode = "global"
	// JavaManaged 表示使用启动器管理的运行时（依赖 JavaRuntimeID）。
	JavaManaged JavaMode = "managed"
	// JavaCustom 表示使用用户指定的可执行文件（依赖 JavaPath）。
	JavaCustom JavaMode = "custom"
)

// 实例内存的允许范围。
//
// 下限 512 MB：低于此值现代 Minecraft 无法启动；上限 1 TiB：
// 拦住明显的误输入，而不是限制合理的大内存机器。
const (
	// MinInstanceMemoryMB 是实例内存下限（MB）。
	MinInstanceMemoryMB = 512
	// MaxInstanceMemoryMB 是实例内存上限（MB）。
	MaxInstanceMemoryMB = 1024 * 1024
)

// NormalizeModes 把空的模式字段填充为默认值。
//
// 空值来自两种场景：老清单没有这些字段，或前端没有提交。
// 统一按「跟随全局」处理——这与旧实现的隐式行为一致，不会改变已有实例的表现。
func NormalizeModes(config *InstanceLaunchConfig) {
	if config.MemoryMode == "" {
		config.MemoryMode = MemoryFollowGlobal
	}
	if config.JVMMode == "" {
		config.JVMMode = JVMFollowGlobal
	}
	if config.JavaMode == "" {
		config.JavaMode = JavaFollowGlobal
	}
}

// ValidateModes 校验模式与取值的一致性，并把非当前模式的字段归一化。
//
// 空模式按默认值处理（与 NormalizeModes 一致）：调用方可能只关心某一个
// 维度的校验，不要求每次构造完整配置。
//
// 归一化的目的不是「帮用户改错」，而是避免存储中出现误导性的组合：
// 例如 `MemoryMode=global` 却留着 `MemoryMB=8192`，下次读出来很难判断
// 用户的真实意图。
func ValidateModes(config *InstanceLaunchConfig) error {
	NormalizeModes(config)

	switch config.MemoryMode {
	case MemoryFollowGlobal:
		config.MemoryMB = 0
	case MemoryCustom:
		if config.MemoryMB < MinInstanceMemoryMB || config.MemoryMB > MaxInstanceMemoryMB {
			return errInvalidInstanceMemory
		}
	default:
		return errInvalidMemoryMode
	}

	switch config.JVMMode {
	case JVMFollowGlobal:
		config.JVMArgs = ""
	case JVMCustom:
		// 允许空串：表示「显式不加额外参数」。
	default:
		return errInvalidJVMMode
	}

	switch config.JavaMode {
	case JavaFollowGlobal:
		config.CustomJavaPath = ""
		config.CustomJavaRuntimeID = ""
	case JavaManaged:
		if strings.TrimSpace(config.CustomJavaRuntimeID) == "" {
			return errMissingRuntimeID
		}
		config.CustomJavaPath = ""
	case JavaCustom:
		path := strings.TrimSpace(config.CustomJavaPath)
		if path == "" {
			return errMissingJavaPath
		}
		config.CustomJavaPath = path
		config.CustomJavaRuntimeID = ""
	default:
		return errInvalidJavaMode
	}

	return nil
}
