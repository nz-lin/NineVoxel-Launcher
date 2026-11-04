// 本文件实现核心重建的进度上报。
//
// 为什么需要它：RebuildCoreForUser 是一次同步绑定调用，重建真实数据要数分钟。
// 没有中间事件，前端只能显示一个不动的「重建中」，用户既不知道还要多久，
// 也无法区分「在干活」与「卡死了」。
package instance

import (
	"sync"

	"example.com/ninevoxel/launcher/internal/events"
	"github.com/wailsapp/wails/v3/pkg/application"
)

var (
	// rebuildEmitterOnce 保证包级发射器只初始化一次。
	rebuildEmitterOnce sync.Once
	// rebuildEmitter 是重建进度事件的发射器。
	rebuildEmitter *events.Emitter
)

// BindCoreRebuildEmitterToApp 把重建进度发射器绑定到应用实例。
//
// 必须在 application.New 之后调用；未绑定时发射只返回错误，不影响重建本身——
// 进度上报失败不该让用户的重建白做一遍。
func BindCoreRebuildEmitterToApp(wailsApp *application.App) {
	if wailsApp == nil {
		return
	}
	coreRebuildEmitter().BindApp(wailsApp)
}

// coreRebuildEmitter 返回包级发射器。
func coreRebuildEmitter() *events.Emitter {
	rebuildEmitterOnce.Do(func() {
		rebuildEmitter = events.NewEmitter()
	})
	return rebuildEmitter
}

// emitRebuildProgress 上报一次重建进度。
//
// 发射失败被刻意忽略：调用方处在重建的关键路径上，进度事件丢失只影响观感，
// 让它去中断重建是本末倒置。
func emitRebuildProgress(phase string, progress float64) {
	_, _ = events.Emit(coreRebuildEmitter(), events.EventCoreRebuildProgress, events.CoreRebuildProgressPayload{
		Phase:    phase,
		Progress: progress,
	})
}
