package loader

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"example.com/ninevoxel/launcher/internal/download"
)

// 本文件覆盖 processor 执行的组装逻辑：命令行顺序、classpath 分隔符、
// 产出校验与增量判定。不启动真实 JVM——那部分由集成验证保证。

// TestProcessorCommandOrder 验证命令行顺序与 classpath 分隔符。
//
// 顺序固定为「代理参数 → -cp → classpath → 主类 → 业务参数」：
// 代理参数落在 `-cp` 之前，否则 JVM 会把它当成主类之后的程序参数。
func TestProcessorCommandOrder(t *testing.T) {
	classpath := []string{"/cache/a.jar", "/libraries/b.jar"}
	args := processorCommand("/jdk/bin/java", "example.Main", classpath, []string{"--output", "/x.jar"})

	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-cp") {
		t.Fatalf("缺少 -cp：%v", args)
	}
	classpathIndex := indexOf(args, "-cp")
	mainIndex := indexOf(args, "example.Main")
	if mainIndex != classpathIndex+2 {
		t.Fatalf("主类应紧跟在 classpath 之后：%v", args)
	}
	// 分隔符必须跟随平台，写死会让另一半平台完全无法启动。
	expectedClasspath := strings.Join(classpath, string(os.PathListSeparator))
	if args[classpathIndex+1] != expectedClasspath {
		t.Fatalf("classpath = %q，期望 %q", args[classpathIndex+1], expectedClasspath)
	}
	// 业务参数必须排在主类之后。
	if indexOf(args, "--output") < mainIndex {
		t.Fatalf("业务参数应位于主类之后：%v", args)
	}
	if !filepath.IsAbs(args[0]) && args[0] != "java" {
		// 代理参数在无代理配置时为空，因此首项不一定是 java；
		// 这里只确认没有把主类排到最前。
		if args[0] == "example.Main" {
			t.Fatalf("主类不应位于首位：%v", args)
		}
	}
}

// TestProcessorOutputsReady 验证增量判定：缺失或哈希不符都要求重跑。
func TestProcessorOutputsReady(t *testing.T) {
	target := filepath.Join(t.TempDir(), "out.jar")
	if err := os.WriteFile(target, []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	actual, err := download.HashFile(target)
	if err != nil {
		t.Fatalf("计算哈希失败：%v", err)
	}

	ready := ProcessorStep{Outputs: []ProcessorOutput{{AbsolutePath: target, SHA1: strings.ToUpper(actual.SHA1)}}}
	if !processorOutputsReady(ready) {
		t.Fatal("哈希一致（大小写不同）时应视为就绪")
	}

	mismatch := ProcessorStep{Outputs: []ProcessorOutput{{AbsolutePath: target, SHA1: strings.Repeat("0", 40)}}}
	if processorOutputsReady(mismatch) {
		t.Fatal("哈希不符时不应视为就绪")
	}
	if err := verifyProcessorOutputs(mismatch); err == nil {
		t.Fatal("哈希不符时应校验失败")
	}

	missing := ProcessorStep{Outputs: []ProcessorOutput{{
		AbsolutePath: filepath.Join(t.TempDir(), "absent.jar"),
		SHA1:         strings.Repeat("0", 40),
	}}}
	if processorOutputsReady(missing) {
		t.Fatal("产出缺失时不应视为就绪")
	}

	// 没有声明产出时无法判断，必须每次都执行。
	if processorOutputsReady(ProcessorStep{}) {
		t.Fatal("无产出声明时不应跳过")
	}
}

// TestMentionsModuleAccess 验证模块访问问题的识别。
func TestMentionsModuleAccess(t *testing.T) {
	cases := []struct {
		text string
		want bool
	}{
		{"java.lang.IllegalAccessError: class X cannot access Y", true},
		{"module cpw.mods.bootstraplauncher does not export cpw.mods", true},
		{"Exception in thread \"main\" java.lang.NoClassDefFoundError", false},
		{"", false},
	}
	for _, item := range cases {
		if got := mentionsModuleAccess(item.text); got != item.want {
			t.Fatalf("mentionsModuleAccess(%q) = %v", item.text, got)
		}
	}
}

// TestProcessorFailureMessage 验证失败归因不会退化成一句空话。
func TestProcessorFailureMessage(t *testing.T) {
	cases := []struct{ text, want string }{
		{"java.lang.OutOfMemoryError: Java heap space", "内存不足"},
		{"No space left on device", "磁盘空间不足"},
	}
	for _, item := range cases {
		if got := processorFailureMessage(item.text); !strings.Contains(got, item.want) {
			t.Fatalf("归因 %q = %q，期望包含 %q", item.text, got, item.want)
		}
	}
	// 完全无法识别时仍要给出可操作的建议，而不是空串。
	if got := processorFailureMessage("something unexpected"); got == "" {
		t.Fatal("未知失败也应给出提示")
	}
}

// indexOf 返回元素首次出现的下标；不存在时返回 -1。
func indexOf(values []string, target string) int {
	for index, value := range values {
		if value == target {
			return index
		}
	}
	return -1
}
