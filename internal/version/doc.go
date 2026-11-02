// Package version 负责 Minecraft 官方版本清单（version_manifest_v2.json）的
// 获取、结构校验、缓存与离线降级，并通过 VersionService 向前端提供只读的
// 「可下载版本列表」。
//
// 职责边界（P3-01 卡面）：
//   - 只处理清单本身，不请求任何 `versions/<id>/<id>.json`，也不写 `versions/` 目录；
//     「选中某个版本后拉取版本 JSON」属于 P3-02。
//   - 清单只从官方源获取，不经过镜像；本轮只用 piston-meta.mojang.com，
//     白名单常量写死在包内，不接受请求参数覆盖。
//   - 缓存为文件（`<数据根>/artifacts/metadata/`），本卡不建数据库表、不需要迁移。
//
// 与 artifact 包的解耦（P3-01 与 P3-03 并行）：
//   - 本包不 import artifact 包，数据根通过 DataRootProvider 注入；
//   - 未注入时任何缓存读写都返回 VERSION_LAYOUT_001，由装配层在 P3-03 完成后接线。
//
// 完整性口径（已知信任缺口）：
//   - 清单文件自身没有自校验哈希，本包只能依赖 TLS、结构校验与响应体大小上限；
//   - 因此所有面向前端的 DTO 都带 `verified: false`，表示「未经内容哈希校验」，
//     不得把它当作安全保证，也不得在前端改写成「已验证」。
//
// 稳定错误码（冻结，不得改义复用）：
//   - VERSION_MANIFEST_001 清单获取失败（网络错误、非 2xx 状态、超时），可重试；
//   - VERSION_MANIFEST_002 清单结构非法（缺字段、latest 悬空、条数/大小超限、
//     重定向之外的 URL 非法），不可重试；
//   - VERSION_URL_001 清单地址非法（非 https、host 不在白名单、重定向跳数超限）；
//   - VERSION_LAYOUT_001 数据根不可用（DataRootProvider 未注入或返回错误）。
package version
