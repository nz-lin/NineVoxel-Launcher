package launch

import "errors"

// 配置模式校验用到的错误。
//
// 这些错误由 instance 包在保存时透传成用户文案，因此只描述「哪里不对」，
// 不包含内部细节。
var (
	// errInvalidInstanceMemory 表示实例内存超出允许范围。
	errInvalidInstanceMemory = errors.New("实例内存必须介于 512 MB 和 1048576 MB 之间")
	// errInvalidMemoryMode 表示内存模式取值未知。
	errInvalidMemoryMode = errors.New("实例内存模式无效")
	// errInvalidJVMMode 表示 JVM 参数模式取值未知。
	errInvalidJVMMode = errors.New("实例 JVM 参数模式无效")
	// errInvalidJavaMode 表示 Java 模式取值未知。
	errInvalidJavaMode = errors.New("实例 Java 模式无效")
	// errMissingRuntimeID 表示选择 managed 模式但未指定运行时。
	errMissingRuntimeID = errors.New("选择使用启动器管理的 Java 时，必须指定运行时")
	// errMissingJavaPath 表示选择 custom 模式但未指定路径。
	errMissingJavaPath = errors.New("选择自定义 Java 时，必须指定可执行文件路径")
)
