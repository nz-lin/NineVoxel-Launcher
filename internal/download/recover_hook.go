package download

// 本文件只做一件事：把 P3-06 已经写好的 Recover 编排函数挂到 Wails 服务启动钩子上。
//
// 为什么必须挂在 ServiceStartup 而不是别处：
//   - Wails v3 在 App.Run() 的启动序列里，先按注册顺序调用各服务的
//     ServiceStartup（pkg/application/application.go 的 startupService），
//     然后才启动事件循环、创建并显示窗口；
//   - 也就是说 ServiceStartup 是「窗口可见之前、用户还没机会点任何按钮」的
//     唯一可靠时点，正好满足「恢复必须先于用户可见的下载操作」。
//   - 该钩子是 Wails 的可选接口（pkg/application/services.go 的 ServiceStartup），
//     不是本包自造的、没人调用的函数；Wails 的绑定生成器还会把它排除在
//     绑定方法之外（generator 的排除表里含 "ServiceStartup"），不会污染前端 API。
//
// 为什么 ServiceStartup 永远返回 nil：
//   - Wails 的语义是「ServiceStartup 返回非 nil 会中止整个启动流程」，
//     而本卡要求恢复失败绝不阻断应用启动。因此这里吞掉全部错误，
//     只把可读结论写进日志。

import (
	"context"
	"strconv"
	"time"

	"example.com/ninevoxel/launcher/internal/artifact"
	"example.com/ninevoxel/launcher/internal/logging"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// startupRecoveryTimeoutDefault 是启动期恢复的默认时限。
//
// 30 秒是「足够扫完几千条任务与暂存文件、又不会让用户明显感到启动变慢」的折中。
// 超时只放弃本次恢复：任务状态与暂存文件都还是原样，下次启动会再试一次。
const startupRecoveryTimeoutDefault = 30 * time.Second

// 以下变量集中了启动恢复的外部依赖，便于单元测试注入。
//
// 抽成包级变量而不是给 ServiceStartup 加参数：Wails 绑定要求方法签名稳定，
// 而 ServiceStartup 的签名由框架固定，无法附加依赖。
var (
	// startupRecoveryTimeout 是本次生效的恢复时限（测试可缩短）。
	startupRecoveryTimeout = startupRecoveryTimeoutDefault

	// startupRecoveryDataRoot 解析数据根（生产环境即 artifact.DataRoot）。
	// 这里保留 artifact.Error 而不是转成本包的 Error：数据根错误由 P3-03 定义，
	// 转码会丢掉原有原因（未配置/不可写/路径非法），而启动日志正需要它。
	startupRecoveryDataRoot = artifact.DataRoot

	// startupRecoveryStore 打开任务存储（生产环境即 P3-04 的 taskStoreFactory）。
	startupRecoveryStore = taskStoreFactory

	// startupRecoveryRun 是实际执行的恢复编排（测试可替换以验证超时分支）。
	startupRecoveryRun = Recover
)

// ServiceStartup 是 Wails 服务启动钩子：在窗口可见之前完成下载恢复。
//
// 返回 nil 是刻意的：Wails 会把非 nil 返回值当作启动失败并中止整个应用，
// 而恢复属于「尽力而为」的维护动作，失败只能记日志、不能拖住启动。
//
// 关于恢复期间的状态事件：此钩子运行在窗口创建之前，前端还没有订阅
// download.task.changed.v1，因此 Recover 经 emitTaskChanged 发出的事件
// 只会走到「发射失败仅记 Warn」的既有分支。这属于**启动期事件无人接收是有意为之**，
// 不为此修改事件发射器语义：恢复的结果已经落库，UI 首帧拉取列表即可看到真实状态。
func (s *DownloadService) ServiceStartup(_ context.Context, _ application.ServiceOptions) error {
	// 与进程生命周期解耦：即使 Wails 传入的 ctx 被提前取消，
	// 也只在 startupRecoveryTimeout 内等待，不会无限挂住启动流程。
	runStartupRecovery()
	return nil
}

// runStartupRecovery 执行一次带超时的启动期恢复。
//
// 该函数保证在 startupRecoveryTimeout 内返回：恢复跑在独立 goroutine 里，
// 主流程只等结果或超时，绝不阻塞窗口创建。
func runStartupRecovery() {
	ctx, cancel := context.WithTimeout(context.Background(), startupRecoveryTimeout)
	defer cancel()

	dataRoot, rootErr := startupRecoveryDataRoot()
	if rootErr != nil {
		// 数据根不可用时没有任何可恢复的文件，直接跳过。
		downloadLog().Warn("启动期下载恢复已跳过：数据根不可用", logging.Fields{
			Event:     "download.recovery.skipped",
			ErrorCode: rootErr.Code,
		})
		return
	}
	store, storeErr := startupRecoveryStore(ctx)
	if storeErr != nil {
		// 存储不可用（例如迁移失败降级）时同样跳过：没有持久化记录就无从判断。
		downloadLog().Warn("启动期下载恢复已跳过：任务存储不可用", logging.Fields{
			Event:     "download.recovery.skipped",
			ErrorCode: storeErr.Code,
		})
		return
	}

	type recoveryOutcome struct {
		result RecoveryResult
		err    *Error
	}
	done := make(chan recoveryOutcome, 1)
	go func() {
		// 由恢复 goroutine 自己负责关闭存储：主流程超时返回后不能再碰它，
		// 否则会出现「关闭后仍在用」的竞态。
		defer func() {
			_ = store.Close()
		}()
		result, recoverErr := startupRecoveryRun(ctx, RecoveryOptions{
			DataRoot: dataRoot,
			Store:    store,
			Logger:   downloadLog(),
		})
		done <- recoveryOutcome{result: result, err: recoverErr}
	}()

	select {
	case <-ctx.Done():
		// 超时：放弃本次恢复，状态与文件保持原样，下次启动再试。
		downloadLog().Warn("启动期下载恢复超时，已放弃并继续启动", logging.Fields{
			Event:      "download.recovery.timeout",
			Attributes: map[string]string{"timeoutMs": strconv.FormatInt(startupRecoveryTimeout.Milliseconds(), 10)},
		})
		return
	case outcome := <-done:
		if outcome.err != nil {
			downloadLog().Warn("启动期下载恢复失败，已继续启动", logging.Fields{
				Event:     "download.recovery.failed",
				ErrorCode: outcome.err.Code,
				Attributes: map[string]string{
					"recovered": strconv.FormatInt(int64(outcome.result.Recovered), 10),
				},
			})
			return
		}
		downloadLog().Info("启动期下载恢复完成", logging.Fields{
			Event: "download.recovery.completed",
			Attributes: map[string]string{
				"recovered": strconv.FormatInt(int64(outcome.result.Recovered), 10),
				"orphans":   strconv.FormatInt(int64(len(outcome.result.Orphans)), 10),
			},
		})
	}
}
