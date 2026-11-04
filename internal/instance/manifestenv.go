package instance

import (
	"time"

	"example.com/ninevoxel/launcher/internal/core"
)

// 本文件提供清单构建所需的环境事实，并集中成可替换变量，便于测试固定。

// installedAt = 安装时间来源（UTC 毫秒）。测试可替换以得到确定性输出。
var installedAt = func() int64 { return time.Now().UTC().UnixMilli() }

// nowMillis 返回安装时间（UTC 毫秒）。
func nowMillis() int64 { return installedAt() }

// platformInfo 返回当前平台标识，写入清单用于跨平台校验。
func platformInfo() core.CorePlatform {
	return core.CurrentPlatform()
}
