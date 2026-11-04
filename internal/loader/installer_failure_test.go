package loader

import (
	"strings"
	"testing"
)

// TestInstallerFailureMessageExtractsKnownCauses 验证失败原因能被识别。
//
// 安装器的报错往往非常具体（缺 profile、代理不可用、Java 版本过低），
// 只回一句「执行失败」会让用户无从下手。
func TestInstallerFailureMessageExtractsKnownCauses(t *testing.T) {
	cases := []struct {
		name   string
		output string
		want   string
	}{
		{
			name:   "缺少 launcher_profiles",
			output: "There is no minecraft launcher profile in \"/home/u/.minecraft\", you need to run the launcher first!",
			want:   "launcher_profiles.json",
		},
		{
			name:   "连接被重置",
			output: "java.net.SocketException: Connection reset\n\tat java.base/sun.nio.ch.NioSocketImpl",
			want:   "连接被重置",
		},
		{
			name:   "域名解析失败",
			output: "java.net.UnknownHostException: maven.neoforged.net",
			want:   "无法解析",
		},
		{
			name:   "Java 版本过低",
			output: "java.lang.UnsupportedClassVersionError: installer has been compiled by a more recent version",
			want:   "Java 版本过低",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := installerFailureMessage(testCase.output)
			if !strings.Contains(got, testCase.want) {
				t.Fatalf("原因期望包含 %q，实际 %q", testCase.want, got)
			}
		})
	}
}

// TestInstallerFailureMessageFallsBack 验证无法识别时退回通用文案。
func TestInstallerFailureMessageFallsBack(t *testing.T) {
	got := installerFailureMessage("something completely unexpected happened")
	if !strings.Contains(got, "官方安装器执行失败") {
		t.Fatalf("期望通用文案，实际 %q", got)
	}
}

// TestInstallerFailureMessageHandlesEmpty 验证空输出不 panic。
func TestInstallerFailureMessageHandlesEmpty(t *testing.T) {
	if got := installerFailureMessage(""); got == "" {
		t.Fatal("空输出也应返回可读文案")
	}
}
