// 本文件导出「按需获取版本元数据」的入口，供其它包在读取版本 JSON 前复用。
//
// 为什么需要它：版本 JSON 不保证在本地存在（P3 之前没有任何步骤负责把它放到
// `versions/<id>/` 下）。`internal/version` 内部已经在详情、安装计划、兼容报告
// 三处做了按需获取，但 `internal/runtime` 解析 Java 需求时也要读同一份文件；
// 如果它自己实现一遍下载与校验，就会产生第二套规则（容易漏掉 sha1 校验，
// 也难以保证与详情页看到的是同一份数据）。
package version

// EnsureVersionMetadata 确保指定版本的版本 JSON 已就位。
//
// 参数 versionID 为 Minecraft 版本 id（例如 `1.21.4`）。
// 行为：本地已存在且可读则直接返回（不发起任何网络请求）；否则从已缓存的
// 版本清单中取 url 与 sha1 按需下载，sha1 不匹配绝不写盘，写入采用原子替换，
// 并发针对同一版本会合并为一次请求。
// 返回 nil 表示元数据可用；失败时返回与 GetVersionDetail 相同的错误码
// （`VERSION_LAYOUT_001` 数据根不可用、`VERSION_MANIFEST_001` 清单缓存不可用、
// `VERSION_JSON_001` 下载或校验失败）。
func EnsureVersionMetadata(versionID string) *AppError {
	root, rootErr := resolveDataRoot()
	if rootErr != nil {
		return rootErr
	}
	return ensureVersionMetadata(root, versionID)
}
