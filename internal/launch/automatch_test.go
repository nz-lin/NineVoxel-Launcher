package launch

import (
	"errors"
	"testing"

	"example.com/ninevoxel/launcher/internal/settings"
)

// 本文件覆盖「全局 Java 缺省时的自动匹配」与「实例 Java 模式透传」。
//
// 这两个行为都有同一个目标：让用户不必手动配置 Java 也能启动正确的版本，
// 同时保证用户显式选择不会被自动逻辑悄悄覆盖。

// stubInstance 把实例解析器替换为固定配置。
func stubInstance(t *testing.T, config InstanceLaunchConfig) {
	t.Helper()
	resolveInstanceLaunchConfig = func(string) (InstanceLaunchConfig, error) {
		return config, nil
	}
}

// stubGlobalJava 把全局 Java 设置替换为给定值（空表示未配置）。
func stubGlobalJava(path string) {
	launchSettingString = func(key, fallback string) string {
		if key == settings.KeyLaunchJavaPath {
			return path
		}
		return fallback
	}
	launchSettingInt = func(string, int) int { return 4096 }
}

// TestAutoMatchesJavaWhenNoGlobalConfigured 验证未配置全局 Java 时按大版本自动匹配。
//
// 这是本次修复的核心：用户从未手动选过 Java，之前会直接报「未找到可用的 Java」。
func TestAutoMatchesJavaWhenNoGlobalConfigured(t *testing.T) {
	restore := replaceLaunchResolvers(t)
	defer restore()

	stubInstance(t, InstanceLaunchConfig{
		GameDirectory: `/instances/inst-test/.minecraft`,
		MCVersion:     "1.20.1",
		JavaMode:      JavaFollowGlobal,
		Installed:     true,
	})
	stubGlobalJava("")

	var askedMajor int
	requiredJavaMajorFor = func(string) int { return 17 }
	resolveJavaForMajor = func(major int) (string, error) {
		askedMajor = major
		return `/runtimes/java-17/bin/java`, nil
	}
	resolveSelectedJavaRuntime = func() (string, error) {
		t.Fatal("自动匹配成功后不应回退到已选运行时")
		return "", nil
	}

	request, appErr := resolveStartLaunchRequest(StartLaunchRequest{InstanceID: "inst-test"})
	if appErr != nil {
		t.Fatalf("解析失败：%s", appErr.Message)
	}
	if request.JavaPath != `/runtimes/java-17/bin/java` {
		t.Fatalf("Java 路径 = %q", request.JavaPath)
	}
	if askedMajor != 17 {
		t.Fatalf("应按版本要求的 17 匹配，实际 %d", askedMajor)
	}
}

// TestUnknownJavaRequirementSkipsAutoMatch 验证版本未声明 Java 要求时不猜。
//
// 不知道要求时应退回「已选运行时」，而不是随便挑一个可能不兼容的版本。
func TestUnknownJavaRequirementSkipsAutoMatch(t *testing.T) {
	restore := replaceLaunchResolvers(t)
	defer restore()

	stubInstance(t, InstanceLaunchConfig{
		GameDirectory: `/instances/inst-test/.minecraft`,
		MCVersion:     "1.20.1",
		JavaMode:      JavaFollowGlobal,
		Installed:     true,
	})
	stubGlobalJava("")
	requiredJavaMajorFor = func(string) int { return 0 }
	resolveJavaForMajor = func(int) (string, error) {
		t.Fatal("要求未知时不应调用自动匹配")
		return "", nil
	}
	resolveSelectedJavaRuntime = func() (string, error) {
		return `/selected/java`, nil
	}

	request, appErr := resolveStartLaunchRequest(StartLaunchRequest{InstanceID: "inst-test"})
	if appErr != nil {
		t.Fatalf("解析失败：%s", appErr.Message)
	}
	if request.JavaPath != `/selected/java` {
		t.Fatalf("应回退到已选运行时，实际 %q", request.JavaPath)
	}
}

// TestExplicitGlobalJavaWins 验证显式配置的全局 Java 优先于自动匹配。
//
// 用户明确指定了路径就是最强意图，自动匹配不该覆盖它。
func TestExplicitGlobalJavaWins(t *testing.T) {
	restore := replaceLaunchResolvers(t)
	defer restore()

	stubInstance(t, InstanceLaunchConfig{
		GameDirectory: `/instances/inst-test/.minecraft`,
		MCVersion:     "1.20.1",
		JavaMode:      JavaFollowGlobal,
		Installed:     true,
	})
	stubGlobalJava(`/custom/java`)

	resolveJavaForMajor = func(int) (string, error) {
		t.Fatal("已有显式全局 Java 时不应自动匹配")
		return "", nil
	}

	request, appErr := resolveStartLaunchRequest(StartLaunchRequest{InstanceID: "inst-test"})
	if appErr != nil {
		t.Fatalf("解析失败：%s", appErr.Message)
	}
	if request.JavaPath != `/custom/java` {
		t.Fatalf("Java 路径 = %q", request.JavaPath)
	}
}

// TestInstanceManagedJavaWinsOverGlobal 验证实例专属 Java 优先于全局与自动匹配。
func TestInstanceManagedJavaWinsOverGlobal(t *testing.T) {
	restore := replaceLaunchResolvers(t)
	defer restore()

	stubInstance(t, InstanceLaunchConfig{
		GameDirectory:       `/instances/inst-test/.minecraft`,
		MCVersion:           "1.20.1",
		JavaMode:            JavaManaged,
		CustomJavaRuntimeID: "runtime-17",
		Installed:           true,
	})
	stubGlobalJava(`/custom/java`)

	resolveJavaRuntimeByID = func(id string) (string, error) {
		return `/runtimes/` + id + `/bin/java`, nil
	}
	resolveJavaForMajor = func(int) (string, error) {
		t.Fatal("实例已指定 Java 时不应自动匹配")
		return "", nil
	}

	request, appErr := resolveStartLaunchRequest(StartLaunchRequest{InstanceID: "inst-test"})
	if appErr != nil {
		t.Fatalf("解析失败：%s", appErr.Message)
	}
	if request.JavaPath != `/runtimes/runtime-17/bin/java` {
		t.Fatalf("实例 Java 未生效，实际 %q", request.JavaPath)
	}
}

// TestInstanceCustomJavaWinsOverGlobal 验证实例指定路径优先。
func TestInstanceCustomJavaWinsOverGlobal(t *testing.T) {
	restore := replaceLaunchResolvers(t)
	defer restore()

	stubInstance(t, InstanceLaunchConfig{
		GameDirectory:  `/instances/inst-test/.minecraft`,
		MCVersion:      "1.20.1",
		JavaMode:       JavaCustom,
		CustomJavaPath: `/instance/java`,
		Installed:      true,
	})
	stubGlobalJava(`/custom/java`)

	request, appErr := resolveStartLaunchRequest(StartLaunchRequest{InstanceID: "inst-test"})
	if appErr != nil {
		t.Fatalf("解析失败：%s", appErr.Message)
	}
	if request.JavaPath != `/instance/java` {
		t.Fatalf("实例自定义 Java 未生效，实际 %q", request.JavaPath)
	}
}

// TestAutoMatchFailureFallsBackToSelected 验证自动匹配失败时回退到已选运行时。
//
// 自动匹配是增强能力：没有匹配项不该让原本能启动的配置变成无法启动。
func TestAutoMatchFailureFallsBackToSelected(t *testing.T) {
	restore := replaceLaunchResolvers(t)
	defer restore()

	stubInstance(t, InstanceLaunchConfig{
		GameDirectory: `/instances/inst-test/.minecraft`,
		MCVersion:     "1.20.1",
		JavaMode:      JavaFollowGlobal,
		Installed:     true,
	})
	stubGlobalJava("")

	resolveJavaForMajor = func(int) (string, error) {
		return "", errors.New("no match")
	}
	resolveSelectedJavaRuntime = func() (string, error) {
		return `/selected/java`, nil
	}

	request, appErr := resolveStartLaunchRequest(StartLaunchRequest{InstanceID: "inst-test"})
	if appErr != nil {
		t.Fatalf("解析失败：%s", appErr.Message)
	}
	if request.JavaPath != `/selected/java` {
		t.Fatalf("应回退到已选运行时，实际 %q", request.JavaPath)
	}
}

// TestNoJavaAtAllGivesActionableError 验证完全没有 Java 时给出可操作提示。
func TestNoJavaAtAllGivesActionableError(t *testing.T) {
	restore := replaceLaunchResolvers(t)
	defer restore()

	stubInstance(t, InstanceLaunchConfig{
		GameDirectory: `/instances/inst-test/.minecraft`,
		MCVersion:     "1.20.1",
		JavaMode:      JavaFollowGlobal,
		Installed:     true,
	})
	stubGlobalJava("")

	resolveJavaForMajor = func(int) (string, error) {
		return "", errors.New("no match")
	}
	resolveSelectedJavaRuntime = func() (string, error) {
		return "", errors.New("not selected")
	}

	_, appErr := resolveStartLaunchRequest(StartLaunchRequest{InstanceID: "inst-test"})
	if appErr == nil {
		t.Fatal("没有可用 Java 时应返回错误")
	}
	if appErr.Code != ErrorCodeLaunchRequest {
		t.Fatalf("错误码 = %q", appErr.Code)
	}
}
