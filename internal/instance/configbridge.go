package instance

import (
	"example.com/ninevoxel/launcher/internal/launch"
)

// 本文件把实例配置桥接到启动包的「模式」模型。
//
// 模式定义放在 launch 包：它是消费方，需要在组装启动参数时判定「用不用
// 实例的值」。instance 只负责存储与校验，因此这里做一层转换，避免两边
// 各维护一份枚举。

// applyModes 用启动包的校验规则归一化实例配置。
//
// 参数 config 为待校验的配置。校验失败时返回错误，由调用方的 validConfig
// 透传成用户文案。
func applyModes(config *Config) error {
	view := launch.InstanceLaunchConfig{
		MemoryMode:          config.MemoryMode,
		MemoryMB:            config.MemoryMB,
		JVMMode:             config.JVMMode,
		JVMArgs:             config.JVMArgs,
		JavaMode:            config.JavaMode,
		CustomJavaPath:      config.CustomJavaPath,
		CustomJavaRuntimeID: config.CustomJavaRuntimeID,
	}
	launch.NormalizeModes(&view)
	if err := launch.ValidateModes(&view); err != nil {
		return err
	}

	config.MemoryMode = view.MemoryMode
	config.MemoryMB = view.MemoryMB
	config.JVMMode = view.JVMMode
	config.JVMArgs = view.JVMArgs
	config.JavaMode = view.JavaMode
	config.CustomJavaPath = view.CustomJavaPath
	config.CustomJavaRuntimeID = view.CustomJavaRuntimeID
	return nil
}

// toLaunchConfig 把实例配置转换为启动包的视图。
func (c Config) toLaunchConfig() launch.InstanceLaunchConfig {
	return launch.InstanceLaunchConfig{
		MCVersion:           c.MCVersion,
		MemoryMode:          c.MemoryMode,
		MemoryMB:            c.MemoryMB,
		JVMMode:             c.JVMMode,
		JVMArgs:             c.JVMArgs,
		JavaMode:            c.JavaMode,
		CustomJavaPath:      c.CustomJavaPath,
		CustomJavaRuntimeID: c.CustomJavaRuntimeID,
	}
}
