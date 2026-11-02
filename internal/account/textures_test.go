// 本文件覆盖纹理地址规范化：Mojang 官方 http 地址升级 https，自建站点保持原样。
package account

import "testing"

// TestNormalizeTextureURL 验证只对 Mojang/Minecraft 官方域名升级协议。
func TestNormalizeTextureURL(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "官方纹理 http 升级 https",
			in:   "http://textures.minecraft.net/texture/abc",
			want: "https://textures.minecraft.net/texture/abc",
		},
		{
			name: "官方子域 http 升级 https",
			in:   "http://skins.minecraft.net/MinecraftSkins/Steve.png",
			want: "https://skins.minecraft.net/MinecraftSkins/Steve.png",
		},
		{
			name: "mojang 域 http 升级 https",
			in:   "http://textures.mojang.com/texture/abc",
			want: "https://textures.mojang.com/texture/abc",
		},
		{
			name: "已是 https 保持不变",
			in:   "https://textures.minecraft.net/texture/abc",
			want: "https://textures.minecraft.net/texture/abc",
		},
		{
			name: "自建 http 站点保持不变",
			in:   "http://skin.example.test/textures/abc",
			want: "http://skin.example.test/textures/abc",
		},
		{
			name: "自建 https 站点保持不变",
			in:   "https://littleskin.cn/textures/abc",
			want: "https://littleskin.cn/textures/abc",
		},
		{
			name: "空字符串",
			in:   "   ",
			want: "",
		},
		{
			name: "无法解析的地址原样返回",
			in:   "://bad",
			want: "://bad",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := NormalizeTextureURL(testCase.in); got != testCase.want {
				t.Fatalf("NormalizeTextureURL(%q) = %q，期望 %q", testCase.in, got, testCase.want)
			}
		})
	}
}

// TestNormalizeProfileTextures 验证皮肤与披风两个字段都会规范化。
func TestNormalizeProfileTextures(t *testing.T) {
	profile := Profile{
		ID:      "0123456789abcdef0123456789abcdef",
		Name:    "Steve",
		SkinURL: "http://textures.minecraft.net/texture/skin",
		CapeURL: "http://textures.minecraft.net/texture/cape",
	}
	normalized := normalizeProfileTextures(profile)
	if normalized.SkinURL != "https://textures.minecraft.net/texture/skin" {
		t.Fatalf("皮肤地址未升级：%s", normalized.SkinURL)
	}
	if normalized.CapeURL != "https://textures.minecraft.net/texture/cape" {
		t.Fatalf("披风地址未升级：%s", normalized.CapeURL)
	}
	if normalized.ID != profile.ID || normalized.Name != profile.Name {
		t.Fatal("规范化不应改动身份字段")
	}
}
