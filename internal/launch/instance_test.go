package launch

import (
	"errors"
	"testing"
)

func TestWithMaxHeapUsesInstanceMemory(t *testing.T) {
	args := withMaxHeap([]string{
		"-XX:+UseG1GC",
		"-Xmx2G",
		"-Xmx",
		"3G",
		"-XX:MaxHeapSize=4G",
		"-Dfoo=bar",
	}, 6144)

	expected := []string{"-XX:+UseG1GC", "-Dfoo=bar", "-Xmx6144M"}
	if len(args) != len(expected) {
		t.Fatalf("参数数量 = %d，期望 %d：%#v", len(args), len(expected), args)
	}
	for index := range expected {
		if args[index] != expected[index] {
			t.Fatalf("参数[%d] = %q，期望 %q", index, args[index], expected[index])
		}
	}
}

func TestResolveStartLaunchRequestUsesInstanceConfiguration(t *testing.T) {
	restore := replaceLaunchResolvers(t)
	defer restore()

	resolveInstanceLaunchConfig = func(id string) (InstanceLaunchConfig, error) {
		if id != "inst-test" {
			t.Fatalf("实例 id = %q", id)
		}
		return InstanceLaunchConfig{
			GameDirectory:       `C:\instances\inst-test\.minecraft`,
			MCVersion:           "1.21.4",
			MemoryMode:          MemoryCustom,
			MemoryMB:            6144,
			JVMMode:             JVMCustom,
			JVMArgs:             "-XX:+UseG1GC -Xmx2G",
			JavaMode:            JavaManaged,
			CustomJavaRuntimeID: "runtime-test",
			Installed:           true,
		}, nil
	}
	resolveJavaRuntimeByID = func(id string) (string, error) {
		if id != "runtime-test" {
			t.Fatalf("runtime id = %q", id)
		}
		return `C:\runtimes\test\bin\java.exe`, nil
	}

	request, appErr := resolveStartLaunchRequest(StartLaunchRequest{
		InstanceID:        "inst-test",
		JavaPath:          `C:\ignored\java.exe`,
		GameDirectory:     `C:\ignored`,
		VersionID:         "ignored",
		MemoryMB:          1024,
		AdditionalJvmArgs: []string{"-Dfoo=bar"},
	})
	if appErr != nil {
		t.Fatalf("解析失败：%s", appErr.Message)
	}
	if request.GameDirectory != `C:\instances\inst-test\.minecraft` || request.VersionID != "1.21.4" {
		t.Fatalf("实例目录或版本未覆盖：%#v", request)
	}
	if request.JavaPath != `C:\runtimes\test\bin\java.exe` {
		t.Fatalf("Java 路径 = %q", request.JavaPath)
	}
	if request.MemoryMB != 6144 {
		t.Fatalf("内存 = %d", request.MemoryMB)
	}
	// 顺序：本次请求附加 → 实例自定义 → 统一追加的 -Xmx。
	// 实例参数现在排在请求附加之后（旧实现是之前），因为模式化之后
	// 「实例配置」被明确当作比临时附加更"持久"的一层。
	if len(request.AdditionalJvmArgs) != 3 ||
		request.AdditionalJvmArgs[0] != "-Dfoo=bar" ||
		request.AdditionalJvmArgs[1] != "-XX:+UseG1GC" ||
		request.AdditionalJvmArgs[2] != "-Xmx6144M" {
		t.Fatalf("JVM 参数 = %#v", request.AdditionalJvmArgs)
	}
}

func TestResolveStartLaunchRequestFallsBackToGlobalJava(t *testing.T) {
	restore := replaceLaunchResolvers(t)
	defer restore()

	launchSettingString = func(key, fallback string) string {
		if key != "launch.javaPath" {
			t.Fatalf("设置 key = %q", key)
		}
		return `C:\global\bin\java.exe`
	}
	launchSettingInt = func(key string, fallback int) int { return 8192 }

	request, appErr := resolveStartLaunchRequest(StartLaunchRequest{
		GameDirectory: `C:\game`,
		VersionID:     "1.20.1",
	})
	if appErr != nil {
		t.Fatalf("解析失败：%s", appErr.Message)
	}
	if request.JavaPath != `C:\global\bin\java.exe` || request.MemoryMB != 8192 {
		t.Fatalf("全局 Java/内存未生效：%#v", request)
	}
}

func TestResolveStartLaunchRequestRejectsUninstalledInstance(t *testing.T) {
	restore := replaceLaunchResolvers(t)
	defer restore()

	resolveInstanceLaunchConfig = func(string) (InstanceLaunchConfig, error) {
		return InstanceLaunchConfig{Installed: false}, nil
	}

	_, appErr := resolveStartLaunchRequest(StartLaunchRequest{InstanceID: "inst-test"})
	if appErr == nil || appErr.Code != ErrorCodeLaunchRequest {
		t.Fatalf("期望 LAUNCH_REQUEST_001，实际 %#v", appErr)
	}
}

func TestResolveStartLaunchRequestReportsMissingJava(t *testing.T) {
	restore := replaceLaunchResolvers(t)
	defer restore()

	launchSettingString = func(string, string) string { return "" }
	resolveSelectedJavaRuntime = func() (string, error) {
		return "", errors.New("没有 Java")
	}

	_, appErr := resolveStartLaunchRequest(StartLaunchRequest{
		GameDirectory: `C:\game`,
		VersionID:     "1.20.1",
	})
	if appErr == nil || appErr.Code != ErrorCodeLaunchRequest {
		t.Fatalf("期望 LAUNCH_REQUEST_001，实际 %#v", appErr)
	}
}

func replaceLaunchResolvers(t *testing.T) func() {
	t.Helper()
	oldInstance := resolveInstanceLaunchConfig
	oldSelected := resolveSelectedJavaRuntime
	oldRuntime := resolveJavaRuntimeByID
	oldMajor := resolveJavaForMajor
	oldRequired := requiredJavaMajorFor
	oldString := launchSettingString
	oldInt := launchSettingInt
	return func() {
		resolveInstanceLaunchConfig = oldInstance
		resolveSelectedJavaRuntime = oldSelected
		resolveJavaRuntimeByID = oldRuntime
		resolveJavaForMajor = oldMajor
		requiredJavaMajorFor = oldRequired
		launchSettingString = oldString
		launchSettingInt = oldInt
	}
}
