// 本文件提供纹理地址（皮肤/披风）的规范化。
//
// 背景：Mojang 的 `/minecraft/profile` 返回的皮肤地址历史上是
// `http://textures.minecraft.net/texture/<hash>`。桌面 WebView 在 https/custom
// scheme 页面上加载 http 图片会被混合内容策略拦截，表现为「账号没有头像」。
// 官方纹理域名早已支持 https，因此统一升级到 https。
//
// 边界：只处理 Mojang/Minecraft 官方域名。自建 Yggdrasil 皮肤站的地址可能只有
// http，或使用自签证书，强制改写会让本来可用的纹理失效，因此原样保留。
package account

import (
	"net/url"
	"strings"
)

// isMojangTextureHost 判断 host 是否属于 Mojang/Minecraft 官方纹理域名。
func isMojangTextureHost(host string) bool {
	normalized := strings.ToLower(strings.TrimSpace(host))
	if normalized == "" {
		return false
	}
	switch normalized {
	case "minecraft.net", "mojang.com", "textures.minecraft.net":
		return true
	}
	return strings.HasSuffix(normalized, ".minecraft.net") || strings.HasSuffix(normalized, ".mojang.com")
}

// NormalizeTextureURL 把官方纹理地址升级到 https；其他地址原样返回。
//
// 空字符串、无法解析的地址、已经是 https 的地址都不做改动。
func NormalizeTextureURL(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	parsed, parseErr := url.Parse(trimmed)
	if parseErr != nil {
		return trimmed
	}
	if parsed.Scheme != "http" {
		return trimmed
	}
	if !isMojangTextureHost(parsed.Hostname()) {
		return trimmed
	}
	parsed.Scheme = "https"
	return parsed.String()
}

// normalizeProfileTextures 返回纹理地址规范化后的档案副本。
//
// 不改动 ID/Name/SkinModel；只处理 SkinURL 与 CapeURL。
func normalizeProfileTextures(profile Profile) Profile {
	profile.SkinURL = NormalizeTextureURL(profile.SkinURL)
	profile.CapeURL = NormalizeTextureURL(profile.CapeURL)
	return profile
}
