package launch

import (
	"errors"
	"testing"
)

// 本文件覆盖「启动前 Java 就绪检查」。
//
// 检查必须与真正的启动共用同一份优先级：两处各写一份「怎么挑 Java」，
// 迟早会出现「检查说可以启动、启动却报缺少 Java」这种自相矛盾的提示。

// stubRequiredMajor 固定版本声明的 Java 大版本要求。
func stubRequiredMajor(major int) {
	requiredJavaMajorFor = func(string) int { return major }
}

// TestJavaReadinessReportsMissing 验证没有可用 Java 时给出所需大版本。
func TestJavaReadinessReportsMissing(t *testing.T) {
	restore := replaceLaunchResolvers(t)
	defer restore()

	stubInstance(t, InstanceLaunchConfig{
		GameDirectory: `/instances/inst-test/.minecraft`,
		MCVersion:     "1.20.1",
		JavaMode:      JavaFollowGlobal,
		Installed:     true,
	})
	stubGlobalJava("")
	stubRequiredMajor(21)
	resolveJavaForMajor = func(int) (string, error) { return "", errors.New("没有匹配的运行时") }
	resolveSelectedJavaRuntime = func() (string, error) { return "", errors.New("尚未选择 Java 运行时") }

	readiness, appErr := ResolveInstanceJavaReadiness("inst-test")
	if appErr != nil {
		t.Fatalf("缺 Java 不应是错误: %v", appErr)
	}
	if readiness.Ready {
		t.Fatal("没有任何 Java 时 Ready 应为 false")
	}
	if readiness.RequiredJavaMajor != 21 {
		t.Fatalf("所需大版本 = %d，期望 21", readiness.RequiredJavaMajor)
	}
	if readiness.Source != JavaSourceMissing || readiness.Message == "" {
		t.Fatalf("应给出缺失原因，实际 %+v", readiness)
	}
}

// TestJavaReadinessAutoMatched 验证自动匹配成功后报告来源。
func TestJavaReadinessAutoMatched(t *testing.T) {
	restore := replaceLaunchResolvers(t)
	defer restore()

	stubInstance(t, InstanceLaunchConfig{
		GameDirectory: `/instances/inst-test/.minecraft`,
		MCVersion:     "1.20.1",
		JavaMode:      JavaFollowGlobal,
		Installed:     true,
	})
	stubGlobalJava("")
	stubRequiredMajor(17)
	resolveJavaForMajor = func(int) (string, error) { return `/runtimes/java-17/bin/java`, nil }
	resolveSelectedJavaRuntime = func() (string, error) {
		t.Fatal("自动匹配成功后不应回退到已选运行时")
		return "", nil
	}

	readiness, appErr := ResolveInstanceJavaReadiness("inst-test")
	if appErr != nil {
		t.Fatal(appErr)
	}
	if !readiness.Ready {
		t.Fatalf("自动匹配成功时应 Ready: %+v", readiness)
	}
	if readiness.Source != JavaSourceAutoMajor {
		t.Fatalf("来源 = %q，期望 %q", readiness.Source, JavaSourceAutoMajor)
	}
}

// TestJavaReadinessGlobalPathWins 验证全局路径优先于自动匹配。
func TestJavaReadinessGlobalPathWins(t *testing.T) {
	restore := replaceLaunchResolvers(t)
	defer restore()

	stubInstance(t, InstanceLaunchConfig{
		GameDirectory: `/instances/inst-test/.minecraft`,
		MCVersion:     "1.20.1",
		JavaMode:      JavaFollowGlobal,
		Installed:     true,
	})
	stubGlobalJava(`/global/bin/java`)
	stubRequiredMajor(17)
	resolveJavaForMajor = func(int) (string, error) {
		t.Fatal("全局路径已配置时不应自动匹配")
		return "", nil
	}

	readiness, appErr := ResolveInstanceJavaReadiness("inst-test")
	if appErr != nil {
		t.Fatal(appErr)
	}
	if !readiness.Ready || readiness.Source != JavaSourceGlobalPath {
		t.Fatalf("应使用全局路径: %+v", readiness)
	}
}

// TestJavaReadinessInstanceCustomWins 验证实例指定路径优先于全局。
func TestJavaReadinessInstanceCustomWins(t *testing.T) {
	restore := replaceLaunchResolvers(t)
	defer restore()

	stubInstance(t, InstanceLaunchConfig{
		GameDirectory:  `/instances/inst-test/.minecraft`,
		MCVersion:      "1.20.1",
		JavaMode:       JavaCustom,
		CustomJavaPath: `/instance/java/bin/java`,
		Installed:      true,
	})
	stubGlobalJava(`/global/bin/java`)
	stubRequiredMajor(17)

	readiness, appErr := ResolveInstanceJavaReadiness("inst-test")
	if appErr != nil {
		t.Fatal(appErr)
	}
	if !readiness.Ready || readiness.Source != JavaSourceInstanceCustom {
		t.Fatalf("应使用实例指定的 Java: %+v", readiness)
	}
}

// TestJavaReadinessBrokenManagedRuntimeDoesNotFallBack 验证实例指定的运行时
// 不可用时不静默回退。
//
// 静默回退会让用户以为自己指定的 Java 生效了，实际跑在另一个版本上——
// 这类问题在「游戏崩溃但不报 Java 相关错误」时极难定位。
func TestJavaReadinessBrokenManagedRuntimeDoesNotFallBack(t *testing.T) {
	restore := replaceLaunchResolvers(t)
	defer restore()

	stubInstance(t, InstanceLaunchConfig{
		GameDirectory:       `/instances/inst-test/.minecraft`,
		MCVersion:           "1.20.1",
		JavaMode:            JavaManaged,
		CustomJavaRuntimeID: "java-17-missing",
		Installed:           true,
	})
	stubGlobalJava(`/global/bin/java`)
	resolveJavaRuntimeByID = func(string) (string, error) { return "", errors.New("找不到该 Java 运行时") }

	readiness, appErr := ResolveInstanceJavaReadiness("inst-test")
	if appErr != nil {
		t.Fatal(appErr)
	}
	if readiness.Ready {
		t.Fatal("实例指定的运行时缺失时不应静默回退到全局 Java")
	}
	if readiness.Source != JavaSourceMissing || readiness.Message == "" {
		t.Fatalf("应说明实例 Java 不可用，实际 %+v", readiness)
	}
}

// TestJavaReadinessNotInstalled 验证未安装实例直接给出可操作提示。
func TestJavaReadinessNotInstalled(t *testing.T) {
	restore := replaceLaunchResolvers(t)
	defer restore()

	stubInstance(t, InstanceLaunchConfig{MCVersion: "1.20.1", Installed: false})

	readiness, appErr := ResolveInstanceJavaReadiness("inst-test")
	if appErr != nil {
		t.Fatal(appErr)
	}
	if readiness.Ready {
		t.Fatal("未安装实例不应报告 Java 就绪")
	}
	if readiness.Message == "" {
		t.Fatal("应提示先完成安装")
	}
}
