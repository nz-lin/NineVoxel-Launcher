package launch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ===== 启动脚本（stdin 协议）=====

// TestBuildLaunchScriptFormat 验证脚本格式：主类行 → 参数行 → launch 收尾。
//
// 格式是引导器与启动器之间的唯一契约，顺序或键名改动必须同时改 Java 侧。
func TestBuildLaunchScriptFormat(t *testing.T) {
	script, err := buildLaunchScript("net.minecraft.client.main.Main", []ArgumentPart{
		{Value: "--username", Source: argumentSourceArgumentsGame},
		{Value: "Steve", Source: argumentSourceArgumentsGame},
		{Value: "--accessToken", Source: argumentSourceArgumentsGame},
		{Value: "token-value", Source: argumentSourceArgumentsGame},
	})
	if err != nil {
		t.Fatalf("生成脚本失败: %s", err.Message)
	}

	want := "mainClass net.minecraft.client.main.Main\n" +
		"param --username\n" +
		"param Steve\n" +
		"param --accessToken\n" +
		"param token-value\n" +
		"launch\n"
	if script != want {
		t.Fatalf("脚本内容期望:\n%s\n实际:\n%s", want, script)
	}
}

// TestBuildLaunchScriptKeepsSpacesAndUnicode 验证含空格与中文的参数原样传递。
//
// 值不转义：引导器按「第一个空格」切键值，其后全部属于值，因此路径里的空格
// 与中文都不需要编码——这与 `@argfile` 需要按系统代码页转码形成对比。
func TestBuildLaunchScriptKeepsSpacesAndUnicode(t *testing.T) {
	gameDir := `C:\Program Files\九宫格 启动器\游戏目录`

	script, err := buildLaunchScript("Main", []ArgumentPart{{Value: gameDir, Source: argumentSourceLauncher}})
	if err != nil {
		t.Fatalf("生成脚本失败: %s", err.Message)
	}

	if !strings.Contains(script, "param "+gameDir+"\n") {
		t.Fatalf("含空格与中文的参数被改动:\n%s", script)
	}
}

// TestBuildLaunchScriptSkipsBlankParts 验证空白参数项不进入脚本。
func TestBuildLaunchScriptSkipsBlankParts(t *testing.T) {
	script, err := buildLaunchScript("Main", []ArgumentPart{
		{Value: "", Source: argumentSourceLauncher},
		{Value: "   ", Source: argumentSourceLauncher},
		{Value: "--version", Source: argumentSourceArgumentsGame},
	})
	if err != nil {
		t.Fatalf("生成脚本失败: %s", err.Message)
	}

	if strings.Count(script, "\n") != 3 {
		t.Fatalf("脚本期望 3 行（mainClass / 1 个参数 / launch），实际:\n%s", script)
	}
}

// TestBuildLaunchScriptRejectsNewline 验证含换行的参数被拒绝而不是被截断。
//
// 行协议以换行分隔参数：值里出现换行会被引导器当成两个参数，
// 静默放行等于把错误内容交给游戏。
func TestBuildLaunchScriptRejectsNewline(t *testing.T) {
	for _, value := range []string{"a\nb", "a\rb"} {
		_, err := buildLaunchScript("Main", []ArgumentPart{{Value: value, Source: argumentSourceLauncher}})
		if err == nil {
			t.Fatalf("含换行的参数 %q 期望被拒绝", value)
		}
		if err.Code != ErrorCodeLaunchArgs {
			t.Fatalf("错误码期望 %q，实际 %q", ErrorCodeLaunchArgs, err.Code)
		}
	}
}

// TestBuildLaunchScriptRequiresMainClass 验证缺主类被拒绝。
func TestBuildLaunchScriptRequiresMainClass(t *testing.T) {
	_, err := buildLaunchScript("   ", nil)
	if err == nil {
		t.Fatal("缺主类期望返回错误")
	}
	if err.Code != ErrorCodeLaunchArgs {
		t.Fatalf("错误码期望 %q，实际 %q", ErrorCodeLaunchArgs, err.Code)
	}
}

// TestBuildLaunchScriptRejectsMainClassWithSpace 验证主类名不得含空格。
func TestBuildLaunchScriptRejectsMainClassWithSpace(t *testing.T) {
	_, err := buildLaunchScript("net.minecraft Main", nil)
	if err == nil {
		t.Fatal("含空格的主类名期望被拒绝")
	}
}

// ===== 内嵌字节码 =====

// TestPrepareLauncherPartWritesClassFile 验证引导器按包结构落到临时目录。
//
// JVM 按包路径定位类：`com/ninezhou/ninevoxel/EntryPoint.class` 少一层或错一层都会
// `ClassNotFoundException`，而这条路径只会在真正启动时才暴露。
func TestPrepareLauncherPartWritesClassFile(t *testing.T) {
	root, cleanup, err := prepareLauncherPart()
	if err != nil {
		t.Fatalf("准备引导器失败: %s", err.Message)
	}
	if cleanup == nil {
		t.Fatal("必须返回清理函数")
	}

	classFile := filepath.Join(root, "com", "ninezhou", "ninevoxel", "EntryPoint.class")
	info, statErr := os.Stat(classFile)
	if statErr != nil {
		t.Fatalf("引导器类文件应存在: %v", statErr)
	}
	if info.Size() == 0 {
		t.Fatal("引导器类文件不应为空")
	}

	cleanup()
	if _, afterErr := os.Stat(root); !os.IsNotExist(afterErr) {
		t.Fatalf("清理后目录应不存在，实际 stat 结果: %v", afterErr)
	}
}

// TestLauncherPartIsJava8Bytecode 验证内嵌字节码的 class 版本不高于 Java 8。
//
// 这是硬约束：1.12.2 及更早的实例使用 Java 8，Java 9+ 编译的字节码会直接抛
// `UnsupportedClassVersionError`。重新编译引导器时如果误用默认 `--release`，
// 这个用例会立刻失败。
func TestLauncherPartIsJava8Bytecode(t *testing.T) {
	root, cleanup, err := prepareLauncherPart()
	if err != nil {
		t.Fatalf("准备引导器失败: %s", err.Message)
	}
	defer cleanup()

	content, readErr := os.ReadFile(filepath.Join(root, "com", "ninezhou", "ninevoxel", "EntryPoint.class"))
	if readErr != nil {
		t.Fatalf("读取类文件失败: %v", readErr)
	}
	if len(content) < 8 {
		t.Fatalf("类文件过短（%d 字节）", len(content))
	}
	// 魔数 0xCAFEBABE 之后是 minor(2) + major(2)；52 对应 Java 8。
	if content[0] != 0xCA || content[1] != 0xFE || content[2] != 0xBA || content[3] != 0xBE {
		t.Fatalf("不是合法的 class 文件（魔数 % X）", content[:4])
	}
	majorVersion := int(content[6])<<8 | int(content[7])
	if majorVersion > 52 {
		t.Fatalf("class 版本 %d 高于 Java 8（52），Java 8 实例会启动失败", majorVersion)
	}
}

// TestPrepareLauncherPartIsolatesInstances 验证两次准备得到不同目录。
//
// 固定路径会让并发启动的多实例互相覆盖字节码。
func TestPrepareLauncherPartIsolatesInstances(t *testing.T) {
	first, cleanupFirst, firstErr := prepareLauncherPart()
	if firstErr != nil {
		t.Fatalf("准备引导器失败: %s", firstErr.Message)
	}
	defer cleanupFirst()

	second, cleanupSecond, secondErr := prepareLauncherPart()
	if secondErr != nil {
		t.Fatalf("准备引导器失败: %s", secondErr.Message)
	}
	defer cleanupSecond()

	if first == second {
		t.Fatalf("两次准备不应复用同一目录: %q", first)
	}
}
