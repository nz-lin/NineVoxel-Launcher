package launch

import (
	"strings"
	"testing"
)

// ===== 模式归一化 =====

// TestNormalizeModesDefaultsToGlobal 验证空模式填为「跟随全局」。
//
// 老清单没有这些字段，默认值必须与旧实现的隐式行为一致，
// 否则升级后所有既有实例的启动参数都会变化。
func TestNormalizeModesDefaultsToGlobal(t *testing.T) {
	config := InstanceLaunchConfig{}
	NormalizeModes(&config)
	if config.MemoryMode != MemoryFollowGlobal {
		t.Fatalf("内存模式 = %q", config.MemoryMode)
	}
	if config.JVMMode != JVMFollowGlobal {
		t.Fatalf("JVM 模式 = %q", config.JVMMode)
	}
	if config.JavaMode != JavaFollowGlobal {
		t.Fatalf("Java 模式 = %q", config.JavaMode)
	}
}

// ===== 内存模式 =====

// TestValidateModesMemoryFollowGlobalClearsValue 验证跟随全局时清空实例值。
//
// 保留一个「不生效的内存值」会让存储出现误导性组合：
// 用户看到 8192，实际跑的是全局值。
func TestValidateModesMemoryFollowGlobalClearsValue(t *testing.T) {
	config := InstanceLaunchConfig{MemoryMode: MemoryFollowGlobal, MemoryMB: 8192}
	if err := ValidateModes(&config); err != nil {
		t.Fatal(err)
	}
	if config.MemoryMB != 0 {
		t.Fatalf("跟随全局时内存应归零，实际 %d", config.MemoryMB)
	}
}

// TestValidateModesMemoryCustomRange 验证自定义内存的范围校验。
func TestValidateModesMemoryCustomRange(t *testing.T) {
	cases := []struct {
		name    string
		memory  int
		wantErr bool
	}{
		{"低于下限", 128, true},
		{"等于下限", MinInstanceMemoryMB, false},
		{"常规值", 4096, false},
		{"等于上限", MaxInstanceMemoryMB, false},
		{"超过上限", MaxInstanceMemoryMB + 1, true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			config := InstanceLaunchConfig{MemoryMode: MemoryCustom, MemoryMB: testCase.memory}
			err := ValidateModes(&config)
			if testCase.wantErr && err == nil {
				t.Fatalf("内存 %d 期望被拒绝", testCase.memory)
			}
			if !testCase.wantErr && err != nil {
				t.Fatalf("内存 %d 期望通过，实际 %v", testCase.memory, err)
			}
		})
	}
}

// ===== JVM 模式 =====

// TestValidateModesJVMFollowGlobalClearsArgs 验证跟随全局时清空实例参数。
func TestValidateModesJVMFollowGlobalClearsArgs(t *testing.T) {
	config := InstanceLaunchConfig{JVMMode: JVMFollowGlobal, JVMArgs: "-XX:+UseG1GC"}
	if err := ValidateModes(&config); err != nil {
		t.Fatal(err)
	}
	if config.JVMArgs != "" {
		t.Fatalf("跟随全局时实例参数应清空，实际 %q", config.JVMArgs)
	}
}

// TestValidateModesJVMCustomAllowsEmpty 验证自定义模式允许空串。
//
// 空串在这里是**有效意图**：「这个实例不要任何额外 JVM 参数」。
// 与「跟随全局」的区别正在于此，也是引入模式的原因。
func TestValidateModesJVMCustomAllowsEmpty(t *testing.T) {
	config := InstanceLaunchConfig{JVMMode: JVMCustom, JVMArgs: ""}
	if err := ValidateModes(&config); err != nil {
		t.Fatalf("自定义模式允许空串，实际 %v", err)
	}
	if config.JVMArgs != "" {
		t.Fatalf("空串应保持为空，实际 %q", config.JVMArgs)
	}
}

// ===== Java 模式 =====

// TestValidateModesJavaManagedRequiresRuntime 验证 managed 模式必须有运行时。
func TestValidateModesJavaManagedRequiresRuntime(t *testing.T) {
	config := InstanceLaunchConfig{JavaMode: JavaManaged}
	if err := ValidateModes(&config); err == nil {
		t.Fatal("managed 模式缺少运行时 ID 时应被拒绝")
	}
}

// TestValidateModesJavaCustomRequiresPath 验证 custom 模式必须有路径。
func TestValidateModesJavaCustomRequiresPath(t *testing.T) {
	config := InstanceLaunchConfig{JavaMode: JavaCustom}
	if err := ValidateModes(&config); err == nil {
		t.Fatal("custom 模式缺少路径时应被拒绝")
	}
}

// TestValidateModesJavaMutualExclusion 验证两种模式互相清空对方字段。
//
// 同时留着路径与运行时 ID 会让「实际用哪个」取决于解析顺序，
// 这类隐式优先级是排查困难的来源。
func TestValidateModesJavaMutualExclusion(t *testing.T) {
	managed := InstanceLaunchConfig{
		JavaMode:            JavaManaged,
		CustomJavaRuntimeID: "runtime-test",
		CustomJavaPath:      `C:\ignored\java.exe`,
	}
	if err := ValidateModes(&managed); err != nil {
		t.Fatal(err)
	}
	if managed.CustomJavaPath != "" {
		t.Fatalf("managed 模式应清空路径，实际 %q", managed.CustomJavaPath)
	}

	custom := InstanceLaunchConfig{
		JavaMode:            JavaCustom,
		CustomJavaPath:      `C:\jdk\bin\java.exe`,
		CustomJavaRuntimeID: "ignored",
	}
	if err := ValidateModes(&custom); err != nil {
		t.Fatal(err)
	}
	if custom.CustomJavaRuntimeID != "" {
		t.Fatalf("custom 模式应清空运行时 ID，实际 %q", custom.CustomJavaRuntimeID)
	}
}

// TestValidateModesRejectsUnknownValues 验证未知模式取值被拒绝。
func TestValidateModesRejectsUnknownValues(t *testing.T) {
	cases := []InstanceLaunchConfig{
		{MemoryMode: "unknown"},
		{JVMMode: "unknown"},
		{JavaMode: "unknown"},
	}
	for _, config := range cases {
		if err := ValidateModes(&config); err == nil {
			t.Fatalf("未知模式应被拒绝：%+v", config)
		}
	}
}

// TestValidateModesErrorsAreReadable 验证错误文案面向用户。
//
// 这些错误最终会出现在界面上，不能含内部标识符或堆栈。
func TestValidateModesErrorsAreReadable(t *testing.T) {
	config := InstanceLaunchConfig{JavaMode: JavaCustom}
	err := ValidateModes(&config)
	if err == nil {
		t.Fatal("期望返回错误")
	}
	message := err.Error()
	if strings.Contains(message, "err") || strings.Contains(message, "nil") {
		t.Fatalf("文案含内部标识符：%q", message)
	}
}
