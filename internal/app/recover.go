// 本文件处理应用启动时的一次性恢复：上次进程异常退出留下的核心重建事务。
//
// 为什么必须在启动阶段、任何安装或启动之前处理：重建会逐个替换 libraries /
// assets / versions / core-index 四个并列目录，无法作为一个整体原子完成。
// 中途退出会留下新旧混合的核心；若带着它继续工作，后续操作会基于一份
// 自相矛盾的数据。
package app

import (
	"strconv"
	"strings"
	"sync"

	"example.com/ninevoxel/launcher/internal/artifact"
	"example.com/ninevoxel/launcher/internal/core"
	"example.com/ninevoxel/launcher/internal/logging"
)

var (
	// appLoggerOnce 保证包级日志器只初始化一次。
	appLoggerOnce sync.Once
	// appLogger 是 app 包共用的日志器。
	appLogger *logging.Logger
)

// logger 返回 app 包使用的日志器。
func logger() *logging.Logger {
	appLoggerOnce.Do(func() {
		appLogger = logging.Default.WithComponent("app")
	})
	return appLogger
}

// recoverCoreRebuild 回滚上次未完成的核心重建事务。
//
// 失败不阻断启动：事务日志与备份都留在磁盘上，下一次启动会继续尝试恢复；
// 让用户看到窗口与错误，比一个打不开的进程更有助于定位问题。
func recoverCoreRebuild() {
	root, rootErr := artifact.DataRoot()
	if rootErr != nil {
		// 数据根不可用时后续安装与启动本来也无法进行，这里只记录跳过，
		// 不把重建恢复的错误混进真正的故障原因里。
		logger().Warn("无法定位数据根目录，跳过核心重建恢复", logging.Fields{
			Event:     "core.rebuild.recover.skipped",
			ErrorCode: "CORE_REBUILD_001",
		})
		return
	}

	report, recoverErr := core.RecoverRebuildDetailed(root)
	if recoverErr != nil {
		logger().Warn("核心重建事务恢复失败", logging.Fields{
			Event:     "core.rebuild.recover.failed",
			ErrorCode: "CORE_REBUILD_002",
			Attributes: map[string]string{
				"reason": recoverErr.Error(),
			},
		})
		return
	}
	if report.Recovered == 0 {
		return
	}
	logger().Info("已完成核心重建事务恢复", logging.Fields{
		Event: "core.rebuild.recover",
		Attributes: map[string]string{
			"recovered":  strconv.Itoa(report.Recovered),
			"operations": strings.Join(report.OperationIDs, ","),
		},
	})
}
