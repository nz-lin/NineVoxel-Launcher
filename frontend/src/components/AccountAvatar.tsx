/**
 * 账号头像：从 Minecraft 皮肤纹理中裁剪出「头部」显示。
 *
 * 为什么不能直接把皮肤 PNG 当头像：
 *   - 微软 / Yggdrasil 返回的是 64×64（或 64×32）的完整皮肤纹理，
 *     直接 `<img>` 会把整个人（头 + 身体 + 四肢）显示出来；
 *   - 头像只应显示 8×8 的脸部区域（叠加 8×8 的帽子层，若存在）。
 *
 * 实现方式：用两层 `<img>` + 容器裁剪，不依赖任何第三方头像服务：
 *   - 底层：整张皮肤放大 8 倍，定位到脸部（源坐标 8,8）；
 *   - 上层：帽子层（源坐标 40,8），仅在皮肤高度 ≥ 64（新版格式）时显示；
 *   - `image-rendering: pixelated` 保持像素风，不发糊。
 *
 * 这样 Yggdrasil 自建皮肤站的地址也能直接工作，不需要把 UUID 发给第三方。
 */
import React, { useEffect, useMemo, useState } from 'react'

/** 官方纹理域名：这些域名的 http 地址统一升级到 https。 */
const OFFICIAL_TEXTURE_SUFFIXES = ['.minecraft.net', '.mojang.com']
const OFFICIAL_TEXTURE_HOSTS = ['textures.minecraft.net', 'minecraft.net', 'mojang.com']

/**
 * 规范化皮肤地址。
 *
 * WebView 会拦截 https 页面里的 http 图片（混合内容），表现为「头像加载失败」。
 * 只对 Mojang 官方域名升级协议；自建 Yggdrasil 皮肤站可能是 http 或自签证书，
 * 强制改写会让本可用的纹理失效，因此原样保留。
 *
 * @param raw 原始皮肤地址
 * @returns 规范化后的地址；空输入返回空字符串
 */
function normalizeSkinURL(raw: string | null | undefined): string {
  const trimmed = (raw ?? '').trim()
  if (trimmed === '') {
    return ''
  }
  try {
    const parsed = new URL(trimmed)
    if (parsed.protocol !== 'http:') {
      return trimmed
    }
    const host = parsed.hostname.toLowerCase()
    const official =
      OFFICIAL_TEXTURE_HOSTS.includes(host) ||
      OFFICIAL_TEXTURE_SUFFIXES.some((suffix) => host.endsWith(suffix))
    if (!official) {
      return trimmed
    }
    parsed.protocol = 'https:'
    return parsed.toString()
  } catch {
    return trimmed
  }
}

/** 按名字生成稳定的头像底色（没有皮肤时使用）。 */
function fallbackBackground(name: string): string {
  let hash = 0
  for (const char of name) {
    hash = (hash * 31 + (char.codePointAt(0) ?? 0)) % 360
  }
  return `linear-gradient(135deg, hsl(${hash} 60% 42%), hsl(${(hash + 38) % 360} 65% 30%))`
}

/** 账号头像的属性。 */
export interface AccountAvatarProps {
  /** 玩家名（用于无皮肤时的首字母与可访问标签）。 */
  name: string

  /** 皮肤纹理地址；空表示没有皮肤。 */
  skinUrl?: string | null

  /** 头像边长（px）。 */
  size?: number

  /** 额外的容器样式（圆角、边框、文字大小等）。 */
  className?: string
}

/**
 * 渲染 Minecraft 玩家头像（头部裁剪）。
 *
 * @param props 头像属性
 * @returns 头像元素；无皮肤或加载失败时显示首字母色块
 */
export const AccountAvatar: React.FC<AccountAvatarProps> = ({
  name,
  skinUrl,
  size = 40,
  className = '',
}) => {
  const normalizedSkinURL = useMemo(() => normalizeSkinURL(skinUrl), [skinUrl])
  const [loadFailed, setLoadFailed] = useState(false)
  const [skinHeight, setSkinHeight] = useState(0)

  // 地址变化时重置加载状态（同一组件复用不同账号的场景）。
  useEffect(() => {
    setLoadFailed(false)
    setSkinHeight(0)
  }, [normalizedSkinURL])

  const letter = (name.trim().charAt(0) || '?').toUpperCase()
  const showSkin = normalizedSkinURL !== '' && !loadFailed
  // 源纹理 64px 宽 → 显示 size；源脸 8px → 显示 size，因此整图宽度为 8×size。
  const scaledSkinSize = size * 8
  const pixelated: React.CSSProperties = { imageRendering: 'pixelated' }

  return (
    <div
      className={`relative shrink-0 overflow-hidden ${className}`}
      style={{ width: size, height: size }}
      role="img"
      aria-label={`${name} 的头像`}
    >
      {!showSkin && (
        <span
          className="absolute inset-0 flex items-center justify-center font-bold text-white"
          style={{ background: fallbackBackground(name) }}
          aria-hidden="true"
        >
          {letter}
        </span>
      )}

      {showSkin && (
        <>
          {/* 底层：脸部（源坐标 8,8） */}
          <img
            src={normalizedSkinURL}
            alt=""
            aria-hidden="true"
            draggable={false}
            referrerPolicy="no-referrer"
            className="absolute max-w-none select-none"
            style={{
              ...pixelated,
              width: scaledSkinSize,
              height: scaledSkinSize,
              left: -size,
              top: -size,
            }}
            onLoad={(event) => {
              setSkinHeight(event.currentTarget.naturalHeight)
            }}
            onError={() => {
              setLoadFailed(true)
            }}
          />
          {/* 上层：帽子（源坐标 40,8），仅 64 高以上的新格式皮肤存在 */}
          {skinHeight >= 64 && (
            <img
              src={normalizedSkinURL}
              alt=""
              aria-hidden="true"
              draggable={false}
              referrerPolicy="no-referrer"
              className="absolute max-w-none select-none"
              style={{
                ...pixelated,
                width: scaledSkinSize,
                height: scaledSkinSize,
                left: -5 * size,
                top: -size,
              }}
            />
          )}
        </>
      )}
    </div>
  )
}
