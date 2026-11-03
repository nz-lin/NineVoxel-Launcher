// 本文件是下载模块的 Wails v3 服务壳：把队列能力暴露成可绑定方法。
//
// 与 internal/launch、internal/version 的做法保持一致：
// 服务结构体本身不持有状态，状态放在包级单例里，这样 Wails 多次扫描绑定
// 也不会创建出多个队列（多队列会让并发上限翻倍，直接违反 D-31）。
package download

import (
	"context"
	"example.com/ninevoxel/launcher/internal/artifact"
	"sync"

	"example.com/ninevoxel/launcher/internal/events"
	"example.com/ninevoxel/launcher/internal/logging"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// DownloadService 是下载队列的 Wails 服务。
type DownloadService struct{}

// NewWailsService 创建下载服务实例（供 application.Options 装配）。
func NewWailsService() application.Service {
	return application.NewService(&DownloadService{})
}

// 包级单例：队列、错误与事件发射器。
var (
	downloadQueueOnce sync.Once
	downloadQueue     *Queue
	downloadQueueErr  *Error

	downloadEmitterOnce sync.Once
	downloadEmitter     *events.Emitter

	downloadLoggerOnce sync.Once
	downloadLogger     *logging.Logger
)

// downloadLog 返回带组件标签的日志器。
//
// 组件名固定为 download：日志检索面板按组件过滤，名字漂移会让过滤失效。
func downloadLog() *logging.Logger {
	downloadLoggerOnce.Do(func() {
		downloadLogger = logging.Default.WithComponent("download")
	})
	return downloadLogger
}

// taskEmitter 返回事件发射器，并惰性绑定当前 Wails 应用。
//
// 惰性绑定而不是要求在 bootstrap 里注册：本卡只允许在 bootstrap 追加
// 一行服务注册，若再要求一行事件绑定，就会越过「只许追加一行」的约束。
func taskEmitter() *events.Emitter {
	downloadEmitterOnce.Do(func() {
		downloadEmitter = events.NewEmitter()
		if app := application.Get(); app != nil {
			downloadEmitter.BindApp(app)
		}
	})
	return downloadEmitter
}

// emitTaskChanged 发射任务状态变化事件。
//
// 调用时机由队列保证：状态与任务表都写完才发事件，
// 因此前端收到事件后立刻查询快照，读到的一定是新状态。
func emitTaskChanged(task Task, progress float64) {
	if task.ParentTaskID != "" {
		return
	}
	emitter := taskEmitter()
	if emitter == nil {
		return
	}
	payload := events.DownloadTaskChangedPayload{
		TaskID:    task.ID,
		State:     string(task.State),
		Progress:  progress,
		ErrorCode: task.ErrorCode,
	}
	if _, emitErr := events.Emit(emitter, events.EventDownloadTaskChanged, payload); emitErr != nil {
		// 应用尚未就绪时发射失败属正常路径（例如命令行模式），不视为错误。
		downloadLog().Warn("下载任务事件发射失败", logging.Fields{
			Event:         events.EventDownloadTaskChanged,
			ErrorCode:     "EVENT_EMIT",
			Attributes:    map[string]string{"taskId": task.ID},
			CorrelationID: "",
		})
	}
}

// sharedDownloadQueue 返回队列单例，必要时启动它。
func sharedDownloadQueue() (*Queue, *Error) {
	downloadQueueOnce.Do(func() {
		root, rootErr := artifact.DataRoot()
		if rootErr != nil {
			downloadQueueErr = newSystemError(ErrorCodeCommit, messageCommitFailed, false)
			return
		}
		transport := NewHTTPTransport(HTTPTransportOptions{Sink: NewStagingSink(root)})
		committer := NewCommitter(CommitOptions{DataRoot: root})
		queue := NewQueue(QueueOptions{
			MaxRetries: defaultMaxRetries,
			PlanSource: configuredPlanSource,
			Transfer: func(ctx context.Context, task Task) TransferResult {
				target, pathErr := artifact.ResolveInsideRoot(root, task.TargetRelPath)
				if pathErr == nil {
					if _, verifyErr := VerifyFile(target, VerifyExpectation{ExpectedSize: task.ExpectedSize, ExpectedSHA1: task.ExpectedSHA1, ExpectedSHA256: task.ExpectedSHA256}); verifyErr == nil {
						return TransferResult{Downloaded: max(0, task.ExpectedSize-task.DownloadedSize), TotalSize: task.ExpectedSize}
					}
				}
				return transport.Transfer(ctx, task)
			},
			Commit: committer.Commit,
			Sink:   emitTaskChanged,
			Logf: func(format string, args ...any) {
				downloadLog().Info("下载队列事件", logging.Fields{
					Event:      "download.queue",
					Attributes: map[string]string{"detail": format},
				})
			},
		})
		if startErr := queue.Start(context.Background()); startErr != nil {
			downloadQueueErr = startErr
			return
		}
		downloadQueue = queue
	})
	if downloadQueueErr != nil {
		return nil, downloadQueueErr
	}
	return downloadQueue, nil
}

// queueOrError 取队列，失败时返回统一错误。
func queueOrError() (*Queue, error) {
	queue, queueErr := sharedDownloadQueue()
	if queueErr != nil {
		return nil, queueErr
	}
	if queue == nil {
		return nil, newSystemError(ErrorCodeDatabaseOpen, messageDatabaseUnavailable, false)
	}
	return queue, nil
}

// StartDownloadPlan 按安装计划创建下载任务。
//
// 返回的 TaskAccepted 中 duplicate=true 表示命中去重规则（同一目标路径与
// 哈希的任务已在运行），此时 taskId 指向已存在的父任务。
func (s *DownloadService) StartDownloadPlan(req StartDownloadPlanRequest) (TaskAccepted, error) {
	queue, queueErr := queueOrError()
	if queueErr != nil {
		return TaskAccepted{}, queueErr
	}
	accepted, planErr := queue.EnqueuePlan(context.Background(), req)
	if planErr != nil {
		downloadLog().Warn("下载任务创建失败", logging.Fields{
			Event:     "download.plan.rejected",
			ErrorCode: planErr.Code,
			Attributes: map[string]string{
				"versionId": req.VersionID,
			},
		})
		return TaskAccepted{}, planErr
	}
	downloadLog().Info("下载任务已创建", logging.Fields{
		Event: "download.plan.accepted",
		Attributes: map[string]string{
			"versionId": req.VersionID,
			"taskId":    accepted.TaskID,
			"duplicate": boolText(accepted.Duplicate),
		},
	})
	return accepted, nil
}

// PauseDownloadTask 暂停任务（保留已下载字节）。
func (s *DownloadService) PauseDownloadTask(req TaskIDRequest) (DownloadTaskView, error) {
	queue, queueErr := queueOrError()
	if queueErr != nil {
		return DownloadTaskView{}, queueErr
	}
	view, pauseErr := queue.Pause(req.TaskID)
	if pauseErr != nil {
		logTaskActionFailure("download.pause.rejected", req.TaskID, pauseErr)
		return DownloadTaskView{}, pauseErr
	}
	return view, nil
}

// ResumeDownloadTask 恢复被暂停的任务。
func (s *DownloadService) ResumeDownloadTask(req TaskIDRequest) (DownloadTaskView, error) {
	queue, queueErr := queueOrError()
	if queueErr != nil {
		return DownloadTaskView{}, queueErr
	}
	view, resumeErr := queue.Resume(req.TaskID)
	if resumeErr != nil {
		logTaskActionFailure("download.resume.rejected", req.TaskID, resumeErr)
		return DownloadTaskView{}, resumeErr
	}
	return view, nil
}

// CancelDownloadTask 取消任务（终态，不可恢复）。
func (s *DownloadService) CancelDownloadTask(req TaskIDRequest) (DownloadTaskView, error) {
	queue, queueErr := queueOrError()
	if queueErr != nil {
		return DownloadTaskView{}, queueErr
	}
	view, cancelErr := queue.Cancel(req.TaskID)
	if cancelErr != nil {
		logTaskActionFailure("download.cancel.rejected", req.TaskID, cancelErr)
		return DownloadTaskView{}, cancelErr
	}
	return view, nil
}

// ClearCompletedDownloadTasks 删除全部已完成任务及其持久化记录。
func (s *DownloadService) ClearCompletedDownloadTasks() (ClearDownloadResult, error) {
	queue, queueErr := queueOrError()
	if queueErr != nil {
		return ClearDownloadResult{}, queueErr
	}
	return ClearDownloadResult{Removed: queue.ClearCompleted(context.Background())}, nil
}

// CancelDownloadOperation 取消某个操作组下所有未结束的计划任务。
//
// 供实例删除等内部流程使用；operationId 由创建任务时写入。
func CancelDownloadOperation(operationID string) error {
	queue, err := queueOrError()
	if err != nil {
		return err
	}
	queue.CancelOperation(operationID)
	return nil
}

// GetDownloadTask 查询单个任务。
func (s *DownloadService) GetDownloadTask(req TaskIDRequest) (DownloadTaskView, error) {
	queue, queueErr := queueOrError()
	if queueErr != nil {
		return DownloadTaskView{}, queueErr
	}
	view, getErr := queue.Get(req.TaskID)
	if getErr != nil {
		return DownloadTaskView{}, getErr
	}
	return view, nil
}

// ListDownloadTasks 查询任务列表（limit 上限 100）。
func (s *DownloadService) ListDownloadTasks(filter DownloadTaskFilter) (DownloadTaskList, error) {
	queue, queueErr := queueOrError()
	if queueErr != nil {
		return DownloadTaskList{}, queueErr
	}
	return queue.List(filter), nil
}

// logTaskActionFailure 记录一次被拒绝的任务操作。
//
// 只记任务 ID 与错误码：任务 ID 不可反推路径，错误码可聚合统计；
// 路径与下载地址一律不进日志（D-17 的脱敏口径同样适用于下载模块）。
func logTaskActionFailure(event string, taskID string, cause *Error) {
	downloadLog().Warn("下载任务操作被拒绝", logging.Fields{
		Event:     event,
		ErrorCode: cause.Code,
		Attributes: map[string]string{
			"taskId": taskID,
		},
	})
}

// boolText 把布尔值转成日志用文本。
func boolText(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

// configuredPlanSource is injected at application assembly, before queue startup.
var configuredPlanSource PlanSource

func SetPlanSource(source PlanSource) { configuredPlanSource = source }

// StartTransferPlan submits a plan prepared entirely by backend code.
func StartTransferPlan(ctx context.Context, plan TransferPlan) (TaskAccepted, error) {
	q, err := queueOrError()
	if err != nil {
		return TaskAccepted{}, err
	}
	accepted, appErr := q.EnqueueTransferPlan(ctx, StartDownloadPlanRequest{VersionID: plan.VersionID, PlanHash: plan.PlanHash, OperationID: plan.OperationID}, plan)
	if appErr != nil {
		return TaskAccepted{}, appErr
	}
	return accepted, nil
}

func GetTransferTask(id string) (DownloadTaskView, error) {
	q, err := queueOrError()
	if err != nil {
		return DownloadTaskView{}, err
	}
	view, appErr := q.Get(id)
	if appErr != nil {
		return DownloadTaskView{}, appErr
	}
	return view, nil
}
