package core

import (
	"strings"
	"testing"
)

// ===== 安装身份 =====

// TestNewIdentityNormalizesVanilla 验证原版身份不带加载器版本。
//
// 若允许 `vanilla:26.3:` 与 `vanilla:26.3` 两种写法并存，同一版本会产生
// 两个索引条目，重复安装判定随之失效。
func TestNewIdentityNormalizesVanilla(t *testing.T) {
	identity, err := NewIdentity(KindVanilla, "26.3", "ignored")
	if err != nil {
		t.Fatalf("构造身份失败: %v", err)
	}
	if identity.LoaderVersion != "" {
		t.Fatalf("原版不应携带加载器版本，实际 %q", identity.LoaderVersion)
	}
	if identity.String() != "vanilla:26.3" {
		t.Fatalf("身份键期望 vanilla:26.3，实际 %q", identity.String())
	}
}

// TestNewIdentityRejectsUnsafeValues 验证非法输入被拒绝。
func TestNewIdentityRejectsUnsafeValues(t *testing.T) {
	cases := []struct {
		name   string
		kind   Kind
		mc     string
		loader string
	}{
		{"未知加载器", Kind("forge2"), "26.3", "66.1.0"},
		{"空 MC 版本", KindForge, "", "66.1.0"},
		{"MC 版本含斜杠", KindForge, "../26.3", "66.1.0"},
		{"MC 版本含冒号", KindForge, "26:3", "66.1.0"},
		{"加载器版本含斜杠", KindForge, "26.3", "66/1"},
		{"加载器版本含冒号", KindForge, "26.3", "66:1"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := NewIdentity(testCase.kind, testCase.mc, testCase.loader); err == nil {
				t.Fatalf("期望拒绝 %+v", testCase)
			}
		})
	}
}

// TestIdentityStringRoundTrip 验证身份键可用冒号切回三段。
//
// 索引与锁都以该键为索引，切分语义必须在各段内容下都成立。
func TestIdentityStringRoundTrip(t *testing.T) {
	identity, err := NewIdentity(KindNeoForge, "1.21.1", "21.1.80")
	if err != nil {
		t.Fatalf("构造身份失败: %v", err)
	}
	key := identity.String()
	if key != "neoforge:1.21.1:21.1.80" {
		t.Fatalf("身份键期望 neoforge:1.21.1:21.1.80，实际 %q", key)
	}
	parts := strings.Split(key, ":")
	if len(parts) != 3 {
		t.Fatalf("身份键应可切成三段，实际 %d 段", len(parts))
	}
}

// ===== 配方指纹 =====

// TestRecipeFingerprintDistinguishesInstaller 验证安装器变化会产生不同指纹。
//
// 同一加载器版本因安装器更新而产物不同时，必须能识别出「配方变了」，
// 否则会错误复用旧产物。
func TestRecipeFingerprintDistinguishesInstaller(t *testing.T) {
	identity, err := NewIdentity(KindForge, "26.3", "66.1.0")
	if err != nil {
		t.Fatalf("构造身份失败: %v", err)
	}
	platform := CorePlatform{OS: "windows", Arch: "x64"}

	first := Recipe{Identity: identity, InstallerSHA256: strings.Repeat("a", 64), Platform: platform, Version: recipeVersion}
	second := Recipe{Identity: identity, InstallerSHA256: strings.Repeat("b", 64), Platform: platform, Version: recipeVersion}

	if first.Fingerprint() == second.Fingerprint() {
		t.Fatal("安装器不同时期望产生不同指纹")
	}
}

// TestRecipeFingerprintDistinguishesPlatform 验证平台进入指纹。
//
// natives 与平台相关，跨平台复用同一版本目录会得到不可用的产物。
func TestRecipeFingerprintDistinguishesPlatform(t *testing.T) {
	identity, err := NewIdentity(KindFabric, "1.21.4", "0.16.14")
	if err != nil {
		t.Fatalf("构造身份失败: %v", err)
	}

	windows := Recipe{Identity: identity, Platform: CorePlatform{OS: "windows", Arch: "x64"}, Version: recipeVersion}
	darwin := Recipe{Identity: identity, Platform: CorePlatform{OS: "darwin", Arch: "arm64"}, Version: recipeVersion}

	if windows.Fingerprint() == darwin.Fingerprint() {
		t.Fatal("平台不同时期望产生不同指纹")
	}
}

// TestRecipeFingerprintStable 验证相同配方得到相同指纹。
func TestRecipeFingerprintStable(t *testing.T) {
	identity, err := NewIdentity(KindQuilt, "26.3", "0.30.2")
	if err != nil {
		t.Fatalf("构造身份失败: %v", err)
	}
	recipe := Recipe{Identity: identity, ProfileSHA256: strings.Repeat("c", 64), Platform: CorePlatform{OS: "windows", Arch: "x64"}, Version: recipeVersion}

	if recipe.Fingerprint() != recipe.Fingerprint() {
		t.Fatal("同一配方应得到相同指纹")
	}
	if len(recipe.Fingerprint()) != 64 {
		t.Fatalf("指纹应为 64 位十六进制串，实际长度 %d", len(recipe.Fingerprint()))
	}
}

// TestCurrentPlatformNormalizesArch 验证架构名归一化。
func TestCurrentPlatformNormalizesArch(t *testing.T) {
	cases := map[string]string{
		"amd64": "x64",
		"arm64": "arm64",
		"386":   "x86",
		"riscv": "riscv",
	}
	for input, want := range cases {
		if got := normalizeArch(input); got != want {
			t.Fatalf("%q 归一化期望 %q，实际 %q", input, want, got)
		}
	}
}

// TestCurrentPlatformFilled 验证当前平台字段非空。
func TestCurrentPlatformFilled(t *testing.T) {
	current := CurrentPlatform()
	if current.OS == "" || current.Arch == "" {
		t.Fatalf("当前平台信息不完整: %+v", current)
	}
}
