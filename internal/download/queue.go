// 本文件实现下载队列：优先级调度、并发闸门、暂停/恢复/取消、重试退避。
//
// 传输与提交都通过可注入的函数接缝完成（TransferFunc / CommitFunc），
// 因此本文件完全可以在离线环境下测试：真实 HTTP、断点续传、staging 写入、
// 哈希校验与原子提交分别属于 P3-05 与 P3-06。
package download

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"example.com/ninevoxel/launcher/internal/artifact"
)

// TransferFunc 是传输接缝，由 P3-05 提供真实实现（HTTP、Range 续传、镜像回退）。
//
// 本卡只依赖这个接口做调度与状态推进；未注入时队列进入「传输未接入」的
// 明确失败路径，而不是伪造成功。
type TransferFunc func(ctx context.Context, task Task) TransferResult

// CommitFunc 是提交接缝，由 P3-06 提供真实实现（哈希校验、原子提交）。
//
// 未注入时任务停在 verifying：状态机允许 verifying 长期存在吗？不允许无限期，
// 因此本卡在未注入时把任务置为 failed 并给出明确错误码——见 runTask 的说明。
type CommitFunc func(ctx context.Context, task Task) *Error

// PlanItem 是安装计划里的一个待下载制品（由 P3-02 的计划转换而来）。
type PlanItem struct {
	// ArtifactID 为制品标识，可为空（计划级条目）。
	ArtifactID string

	// TaskType 为任务类型，取值见 TaskType* 常量。
	TaskType string

	// SourceURI 为下载地址（只在后端流转）。
	SourceURI string

	// TargetRelPath 为最终目标相对路径（相对数据根）。
	TargetRelPath string

	// TargetKind 为 shared 或 version。
	TargetKind string

	// ExpectedSize / ExpectedSHA1 / ExpectedSHA256 为校验元数据。
	ExpectedSize   int64
	ExpectedSHA1   string
	ExpectedSHA256 string
}

// TransferPlan 是一次「按计划入队」的完整描述。
type TransferPlan struct {
	// VersionID 为所属版本。
	VersionID string

	// PlanHash 为计划哈希（D-26），用于幂等与计划漂移检测。
	PlanHash string

	// Items 为待下载条目。
	Items []PlanItem
}

// PlanSource 是计划来源接缝，由 P3-02 的计划解析导出接口注入。
//
// 之所以用函数而不是直接调用 version 包：P3-02 目前没有导出的
// install plan 结构（detail.go 内全是私有类型），硬依赖会让本包无法编译；
// 用接缝既保持解耦，也让本卡能在离线环境下用假计划测试。
type PlanSource func(ctx context.Context, req StartDownloadPlanRequest) (TransferPlan, *Error)

// 队列默认参数（D-31 已冻结）。
const (
	// defaultGlobalConcurrency 为全局并发上限。
	defaultGlobalConcurrency = 4

	// defaultPerHostConcurrency 为同一主机并发上限。
	defaultPerHostConcurrency = 2

	// defaultMaxRetries 为失败后的最大重试次数。
	defaultMaxRetries = 3

	// defaultBackoffBase 为首次重试的等待时间，之后指数增长。
	defaultBackoffBase = time.Second

	// defaultBackoffMax 为单次退避等待的上限。
	defaultBackoffMax = 30 * time.Second

	// progressEmitIntervalMs 为进度事件的最小发射间隔（毫秒）。
	progressEmitIntervalMs = 250

	// progressEmitDelta 为进度事件的最小增量（0~1）。
	progressEmitDelta = 0.01

	// maxListLimit 为任务列表单页上限（冻结清单 §1.3）。
	maxListLimit = 100
)

// hostUnknownBucket 是无法解析主机名时使用的统一并发桶。
//
// 不能因为地址解析失败就跳过每主机限制：那样一批坏地址会瞬间
// 占满全局并发，把正常任务挤掉。
const hostUnknownBucket = "unknown"

// QueueOptions 是队列构造参数，测试通过它注入更小的退避基准与假接缝。
type QueueOptions struct {
	// GlobalConcurrency 为全局并发上限，≤0 时取默认值 4。
	GlobalConcurrency int

	// PerHostConcurrency 为同主机并发上限，≤0 时取默认值 2。
	PerHostConcurrency int

	// MaxRetries 为最大重试次数，<0 时取默认值 3。
	MaxRetries int

	// BackoffBase 为退避基准，≤0 时取默认值 1s；测试传 time.Millisecond。
	BackoffBase time.Duration

	// BackoffMax 为退避上限，≤0 时取默认值 30s。
	BackoffMax time.Duration

	// Transfer 为传输实现，nil 表示尚未接入（P3-05）。
	Transfer TransferFunc

	// Commit 为提交实现，nil 表示尚未接入（P3-06）。
	Commit CommitFunc

	// PlanSource 为计划来源，nil 表示尚未接入（P3-02）。
	PlanSource PlanSource

	// Store 为任务持久化实现，nil 时由工厂按数据根打开（失败降级内存）。
	Store TaskStore

	// Now 为时钟，nil 时用 time.Now；测试可注入以断言时间戳行为。
	Now func() int64

	// Sink 接收任务状态变化通知，nil 表示不发射事件。
	Sink func(task Task, progress float64)

	// Logf 接收诊断日志，nil 表示不记录。
	Logf func(format string, args ...any)
}

// progressState 记录单任务的进度事件节流状态。
type progressState struct {
	lastEmitMs int64
	lastRatio  float64
}

// Queue 是下载任务队列。
//
// 并发模型：一把互斥锁保护全部簿记（任务表、待调度列表、运行中计数），
// 调度循环与工作协程都只在锁内做簿记，真正的传输与落库在锁外进行，
// 避免慢 IO 拖住入队与查询。
type Queue struct {
	mu sync.Mutex

	options QueueOptions
	tasks   map[string]*Task
	// seq 为入队序号，用于同优先级下的稳定排序。
	seq map[string]int64
	// pending 为待调度任务 ID（按入队顺序追加，派出时按优先级挑选）。
	pending []string
	// running 为正在运行的任务集合（含重试等待中的任务）。
	running map[string]struct{}
	// hostRunning 为各主机当前运行数。
	hostRunning map[string]int
	// cancels 保存运行中任务的取消函数。
	cancels map[string]context.CancelFunc
	// progress 保存进度事件节流状态。
	progress map[string]*progressState

	ctx    context.Context
	cancel context.CancelFunc
	kick   chan struct{}

	started bool
	closed  bool
	store   TaskStore
	// storageDegraded 表示存储不可用、已降级为内存模式。
	storageDegraded bool
}

// NewQueue 构造队列（尚未启动调度循环）。
func NewQueue(options QueueOptions) *Queue {
	normalized := normalizeQueueOptions(options)
	ctx, cancel := context.WithCancel(context.Background())
	return &Queue{
		options:     normalized,
		tasks:       make(map[string]*Task, 64),
		seq:         make(map[string]int64, 64),
		running:     make(map[string]struct{}, 8),
		hostRunning: make(map[string]int, 8),
		cancels:     make(map[string]context.CancelFunc, 8),
		progress:    make(map[string]*progressState, 64),
		ctx:         ctx,
		cancel:      cancel,
		kick:        make(chan struct{}, 1),
	}
}

// normalizeQueueOptions 补齐默认值并做边界收敛。
func normalizeQueueOptions(options QueueOptions) QueueOptions {
	if options.GlobalConcurrency <= 0 {
		options.GlobalConcurrency = defaultGlobalConcurrency
	}
	if options.PerHostConcurrency <= 0 {
		options.PerHostConcurrency = defaultPerHostConcurrency
	}
	if options.PerHostConcurrency > options.GlobalConcurrency {
		options.PerHostConcurrency = options.GlobalConcurrency
	}
	if options.MaxRetries <= 0 {
		options.MaxRetries = defaultMaxRetries
	}
	if options.BackoffBase <= 0 {
		options.BackoffBase = defaultBackoffBase
	}
	if options.BackoffMax <= 0 {
		options.BackoffMax = defaultBackoffMax
	}
	if options.Now == nil {
		options.Now = func() int64 { return time.Now().UnixMilli() }
	}
	if options.Logf == nil {
		options.Logf = func(string, ...any) {}
	}
	return options
}

// BackoffBase 返回实际生效的退避基准（供测试与诊断确认注入是否生效）。
func (q *Queue) BackoffBase() time.Duration {
	return q.options.BackoffBase
}

// now 是统一的取时入口。
func (q *Queue) now() int64 {
	return q.options.Now()
}

// Start 启动调度循环并载入历史任务。
//
// 历史任务只载入到内存用于展示：真正的「重启恢复矩阵」属 P3-07，
// 本卡不自动续传，避免在未知的半成品 staging 上继续写字节。
func (q *Queue) Start(ctx context.Context) *Error {
	q.mu.Lock()
	if q.started {
		q.mu.Unlock()
		return nil
	}
	q.started = true
	store := q.options.Store
	q.mu.Unlock()

	if store == nil {
		opened, openErr := taskStoreFactory(ctx)
		if openErr != nil {
			// 降级为内存存储：任务记录不落盘，但队列照常工作。
			store = newMemoryTaskStore()
			q.mu.Lock()
			q.storageDegraded = true
			q.mu.Unlock()
			q.options.Logf("download: 任务存储不可用，已降级为内存模式（%s）", openErr.Code)
		} else {
			store = opened
		}
	}
	q.mu.Lock()
	q.store = store
	q.mu.Unlock()

	if loaded, loadErr := store.LoadAll(ctx); loadErr == nil {
		q.mu.Lock()
		for index := range loaded {
			task := loaded[index]
			if _, exists := q.tasks[task.ID]; exists {
				continue
			}
			copied := task
			q.tasks[task.ID] = &copied
			q.seq[task.ID] = int64(index)
		}
		q.mu.Unlock()
	} else {
		q.options.Logf("download: 历史任务载入失败，按空队列启动")
	}

	go q.loop()
	return nil
}

// Close 停止调度循环并等待退出。
//
// 运行中任务会被取消（ctx 取消），状态由各自的收尾逻辑写入；
// 这里不等待所有工作协程结束，避免关闭流程被慢 IO 卡住。
func (q *Queue) Close() error {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return nil
	}
	q.closed = true
	store := q.store
	q.mu.Unlock()

	q.cancel()
	if store != nil {
		return store.Close()
	}
	return nil
}

// StorageDegraded 表示任务存储是否已降级为内存模式。
func (q *Queue) StorageDegraded() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.storageDegraded
}

// loop 是调度循环：收到信号后尽量把空闲并发填满。
func (q *Queue) loop() {
	for {
		select {
		case <-q.ctx.Done():
			return
		case <-q.kick:
			q.dispatch()
		}
	}
}

// kick 唤醒调度循环（非阻塞：已有信号时直接合并）。
func (q *Queue) kickLoop() {
	select {
	case q.kick <- struct{}{}:
	default:
	}
}

// dispatch 派出所有当前可运行的任务。
func (q *Queue) dispatch() {
	for {
		q.mu.Lock()
		if q.closed {
			q.mu.Unlock()
			return
		}
		task, ok := q.pickLocked()
		if !ok {
			q.mu.Unlock()
			return
		}
		host := hostOf(task.SourceURI)
		q.running[task.ID] = struct{}{}
		q.hostRunning[host]++
		q.removePendingLocked(task.ID)
		taskCopy := *task
		q.mu.Unlock()

		go q.runTask(taskCopy)
	}
}

// pickLocked 挑出优先级最高且满足并发限制的待调度任务。
//
// 排序键：(优先级, 入队序号)。同优先级按入队先后，保证行为可复现。
func (q *Queue) pickLocked() (*Task, bool) {
	if len(q.pending) == 0 {
		return nil, false
	}
	if len(q.running) >= q.options.GlobalConcurrency {
		return nil, false
	}
	candidates := make([]string, len(q.pending))
	copy(candidates, q.pending)
	sort.SliceStable(candidates, func(i, j int) bool {
		left, right := q.tasks[candidates[i]], q.tasks[candidates[j]]
		if left == nil || right == nil {
			return candidates[i] < candidates[j]
		}
		leftPriority, rightPriority := left.Priority(), right.Priority()
		if leftPriority != rightPriority {
			return leftPriority < rightPriority
		}
		return q.seq[candidates[i]] < q.seq[candidates[j]]
	})
	for _, id := range candidates {
		task, exists := q.tasks[id]
		if !exists || task.State != StateQueued {
			q.removePendingLocked(id)
			continue
		}
		host := hostOf(task.SourceURI)
		if q.hostRunning[host] >= q.options.PerHostConcurrency {
			continue
		}
		if len(q.running) >= q.options.GlobalConcurrency {
			return nil, false
		}
		return task, true
	}
	return nil, false
}

// removePendingLocked 从待调度列表移除指定任务。
func (q *Queue) removePendingLocked(id string) {
	for index, candidate := range q.pending {
		if candidate == id {
			q.pending = append(q.pending[:index], q.pending[index+1:]...)
			return
		}
	}
}

// enqueuePendingLocked 把任务加入待调度列表并记录序号。
func (q *Queue) enqueuePendingLocked(id string) {
	if _, exists := q.seq[id]; !exists {
		q.seq[id] = q.now()<<16 + int64(len(q.seq))
	}
	q.pending = append(q.pending, id)
}

// hostOf 从下载地址解析主机名，失败时归入统一桶。
func hostOf(sourceURI string) string {
	if sourceURI == "" {
		return hostUnknownBucket
	}
	parsed, err := url.Parse(sourceURI)
	if err != nil || parsed.Host == "" {
		return hostUnknownBucket
	}
	return strings.ToLower(parsed.Host)
}

// runTask 是单任务的工作协程：状态推进 → 传输（含重试）→ 校验/提交。
func (q *Queue) runTask(task Task) {
	taskCtx, cancel := context.WithCancel(q.ctx)
	q.mu.Lock()
	q.cancels[task.ID] = cancel
	q.mu.Unlock()

	defer func() {
		cancel()
		q.mu.Lock()
		delete(q.cancels, task.ID)
		delete(q.running, task.ID)
		host := hostOf(task.SourceURI)
		if q.hostRunning[host] > 0 {
			q.hostRunning[host]--
		}
		q.mu.Unlock()
		q.kickLoop()
	}()

	// queued → resolving：真实地址解析属 P3-05，这里只体现「准备传输」阶段。
	if _, err := q.applyTransition(task.ID, StateResolving); err != nil {
		return
	}
	// resolving → downloading：进入实际传输。
	if _, err := q.applyTransition(task.ID, StateDownloading); err != nil {
		return
	}

	if !q.transferWithRetry(taskCtx, task.ID) {
		return
	}
	if taskCtx.Err() != nil {
		return
	}

	// downloading → verifying：传输结束，进入校验。
	if _, err := q.applyTransition(task.ID, StateVerifying); err != nil {
		return
	}

	commit := q.options.Commit
	if commit == nil {
		// 提交实现尚未接入（P3-06）：停在 verifying 会让任务永远「进行中」，
		// 因此明确失败并给出错误码，界面据此提示「校验尚未接入」。
		q.failTask(task.ID, newSystemError(ErrorCodeSize, messageTransferNotImplemented, false))
		return
	}
	if _, err := q.applyTransition(task.ID, StateCommitting); err != nil {
		return
	}
	snapshot, _ := q.snapshotOf(task.ID)
	if commitErr := commit(taskCtx, snapshot); commitErr != nil {
		if taskCtx.Err() != nil {
			// 暂停/取消已由调用方写入状态，这里不再覆盖。
			return
		}
		q.failTask(task.ID, commitErr)
		return
	}
	if _, err := q.applyTransition(task.ID, StateCompleted); err != nil {
		return
	}
	q.bumpParent(task.ID)
}

// transferWithRetry 执行传输并在失败时按退避重试；返回是否成功。
func (q *Queue) transferWithRetry(ctx context.Context, id string) bool {
	transfer := q.options.Transfer
	if transfer == nil {
		q.failTask(id, newExternalError(ErrorCodeNetwork, messageTransferNotImplemented, false))
		return false
	}

	retries := 0
	for {
		if ctx.Err() != nil {
			return false
		}
		snapshot, ok := q.snapshotOf(id)
		if !ok {
			return false
		}
		result := transfer(ctx, snapshot)
		if ctx.Err() != nil {
			return false
		}
		if result.Err == nil {
			q.applyTransferResult(id, result)
			return true
		}
		if !result.Retryable || retries >= q.options.MaxRetries {
			code := result.ErrorCode
			if code == "" {
				code = ErrorCodeNetwork
			}
			q.failTask(id, newExternalError(code, messageTransferNotImplemented, result.Retryable))
			return false
		}
		retries++
		q.bumpRetryCount(id)
		if !q.waitBackoff(ctx, retries) {
			return false
		}
	}
}

// backoffDelay 计算第 attempt 次重试前的等待时间：base × 2^(attempt-1)，上限 BackoffMax。
func (q *Queue) backoffDelay(attempt int) time.Duration {
	base := q.options.BackoffBase
	limit := q.options.BackoffMax
	delay := base
	for index := 1; index < attempt; index++ {
		delay *= 2
		if delay >= limit {
			return limit
		}
	}
	if delay > limit {
		return limit
	}
	return delay
}

// waitBackoff 等待退避时间；ctx 取消时立即返回 false。
func (q *Queue) waitBackoff(ctx context.Context, attempt int) bool {
	timer := time.NewTimer(q.backoffDelay(attempt))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// applyTransferResult 累计已下载字节并刷新进度事件。
func (q *Queue) applyTransferResult(id string, result TransferResult) {
	q.mu.Lock()
	task, exists := q.tasks[id]
	if !exists {
		q.mu.Unlock()
		return
	}
	task.DownloadedSize += result.Downloaded
	if result.TotalSize > 0 {
		task.ExpectedSize = result.TotalSize
	}
	if result.ETag != "" {
		task.ETag = result.ETag
	}
	if result.LastModified != "" {
		task.LastModified = result.LastModified
	}
	task.RetryCount = 0
	task.UpdatedAtMs = q.now()
	snapshot := *task
	shouldEmit := q.shouldEmitProgressLocked(snapshot)
	q.mu.Unlock()

	q.persist(snapshot)
	if shouldEmit {
		q.emit(snapshot)
	}
}

// bumpRetryCount 增加重试计数（保持 downloading 状态）。
func (q *Queue) bumpRetryCount(id string) {
	q.mu.Lock()
	task, exists := q.tasks[id]
	if !exists {
		q.mu.Unlock()
		return
	}
	task.RetryCount++
	task.UpdatedAtMs = q.now()
	snapshot := *task
	q.mu.Unlock()

	q.persist(snapshot)
	q.emit(snapshot)
	q.options.Logf("download: 任务 %s 第 %d 次重试", id, snapshot.RetryCount)
}

// failTask 把任务置为 failed 并记录错误码与安全文案。
func (q *Queue) failTask(id string, cause *Error) {
	if cause == nil {
		cause = newSystemError(ErrorCodeNetwork, messageTransferNotImplemented, false)
	}
	q.mu.Lock()
	task, exists := q.tasks[id]
	if !exists {
		q.mu.Unlock()
		return
	}
	if IsTerminal(task.State) {
		// 已进入终态（例如用户刚取消）：不覆盖用户意图，也不报错。
		q.mu.Unlock()
		return
	}
	task.ErrorCode = cause.Code
	task.ErrorMessageSafe = cause.Message
	if err := ValidateTransition(task.State, StateFailed); err != nil {
		// 当前状态不允许直接失败（例如 committing → failed 之外的路径），
		// 保留原状态并记录错误，交给人机界面提示。
		q.mu.Unlock()
		q.options.Logf("download: 任务 %s 无法进入 failed（%s）", id, err.Code)
		return
	}
	task.State = StateFailed
	task.UpdatedAtMs = q.now()
	task.CompletedAtMs = task.UpdatedAtMs
	snapshot := *task
	q.mu.Unlock()

	q.persist(snapshot)
	q.emit(snapshot)
	q.bumpParent(id)
}

// applyTransition 在锁内校验并写入状态迁移，随后落库并发事件。
//
// 顺序严格固定为「先落状态、再发事件」：P2-06 已验证，先发事件会让前端
// 收到通知后立刻查询，却读到旧快照。
func (q *Queue) applyTransition(id string, to State) (Task, *Error) {
	q.mu.Lock()
	task, exists := q.tasks[id]
	if !exists {
		q.mu.Unlock()
		return Task{}, newInputError(ErrorCodeState, messageTaskNotFound)
	}
	if transitionErr := ValidateTransition(task.State, to); transitionErr != nil {
		q.mu.Unlock()
		return Task{}, transitionErr
	}
	task.State = to
	task.UpdatedAtMs = q.now()
	if IsTerminal(to) && task.CompletedAtMs == 0 {
		task.CompletedAtMs = task.UpdatedAtMs
	}
	snapshot := *task
	q.mu.Unlock()

	q.persist(snapshot)
	q.emit(snapshot)
	return snapshot, nil
}

// snapshotOf 返回任务副本。
func (q *Queue) snapshotOf(id string) (Task, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	task, exists := q.tasks[id]
	if !exists {
		return Task{}, false
	}
	return *task, true
}

// shouldEmitProgressLocked 判断进度事件是否应发射（≥250ms 或 ≥1%）。
func (q *Queue) shouldEmitProgressLocked(task Task) bool {
	state, exists := q.progress[task.ID]
	now := task.UpdatedAtMs
	ratio := progressRatio(task)
	if !exists {
		q.progress[task.ID] = &progressState{lastEmitMs: now, lastRatio: ratio}
		return true
	}
	if now-state.lastEmitMs >= progressEmitIntervalMs {
		state.lastEmitMs = now
		state.lastRatio = ratio
		return true
	}
	if ratio-state.lastRatio >= progressEmitDelta || (ratio >= 1 && state.lastRatio < 1) {
		state.lastRatio = ratio
		return true
	}
	return false
}

// progressRatio 计算 0~1 的进度，总大小未知时返回 0。
func progressRatio(task Task) float64 {
	if task.ExpectedSize <= 0 {
		return 0
	}
	ratio := float64(task.DownloadedSize) / float64(task.ExpectedSize)
	if ratio < 0 {
		return 0
	}
	if ratio > 1 {
		return 1
	}
	return ratio
}

// persist 把任务写入存储；失败只记日志（存储是副本，不是真相源）。
func (q *Queue) persist(task Task) {
	q.mu.Lock()
	store := q.store
	q.mu.Unlock()
	if store == nil {
		return
	}
	if err := store.Save(q.ctx, task); err != nil {
		q.options.Logf("download: 任务 %s 落库失败（%s）", task.ID, messageStorageFailed)
	}
}

// emit 发状态变化事件（sink 为 nil 时不发）。
func (q *Queue) emit(task Task) {
	if q.options.Sink == nil {
		return
	}
	q.options.Sink(task, progressRatio(task))
}

// bumpParent 在子任务进入终态后更新父任务的聚合进度。
func (q *Queue) bumpParent(id string) {
	q.mu.Lock()
	child, exists := q.tasks[id]
	if !exists || child.ParentTaskID == "" {
		q.mu.Unlock()
		return
	}
	parent, parentExists := q.tasks[child.ParentTaskID]
	if !parentExists {
		q.mu.Unlock()
		return
	}
	total, completed, failed, cancelled := 0, 0, 0, 0
	for _, candidate := range q.tasks {
		if candidate.ParentTaskID != child.ParentTaskID {
			continue
		}
		total++
		switch candidate.State {
		case StateCompleted:
			completed++
		case StateFailed:
			failed++
		case StateCancelled:
			cancelled++
		}
	}
	parent.DownloadedSize = int64(completed)
	parent.ExpectedSize = int64(total)
	if total > 0 && completed+failed+cancelled >= total && !IsTerminal(parent.State) {
		// 父任务的终态按子任务结果聚合：有失败则 failed，全被取消则 cancelled，
		// 否则 completed。这样界面只需看父任务就能判断整批的结局。
		switch {
		case failed > 0:
			parent.State = StateFailed
			parent.ErrorCode = child.ErrorCode
			parent.ErrorMessageSafe = child.ErrorMessageSafe
		case completed == 0 && cancelled > 0:
			parent.State = StateCancelled
		default:
			parent.State = StateCompleted
		}
		parent.UpdatedAtMs = q.now()
		parent.CompletedAtMs = parent.UpdatedAtMs
	}
	parent.UpdatedAtMs = q.now()
	snapshot := *parent
	q.mu.Unlock()

	q.persist(snapshot)
	q.emit(snapshot)
}

// EnqueuePlan 按安装计划创建父任务与全部子任务，并返回父任务受理结果。
//
// 去重规则（卡面第 3 条）：同一 targetRelPath + expectedSHA256 且已处于
// downloading / verifying / committing 的制品不再重复入队，返回已有计划任务。
func (q *Queue) EnqueuePlan(ctx context.Context, req StartDownloadPlanRequest) (TaskAccepted, *Error) {
	if strings.TrimSpace(req.VersionID) == "" {
		return TaskAccepted{}, newInputError(ErrorCodePlan, messageVersionIDEmpty)
	}
	planSource := q.options.PlanSource
	if planSource == nil {
		return TaskAccepted{}, newInputError(ErrorCodePlan, messagePlanSourceMissing)
	}
	plan, planErr := planSource(ctx, req)
	if planErr != nil {
		return TaskAccepted{}, planErr
	}
	if len(plan.Items) == 0 {
		return TaskAccepted{}, newInputError(ErrorCodePlan, messagePlanEmpty)
	}
	if req.PlanHash != "" && plan.PlanHash != "" && req.PlanHash != plan.PlanHash {
		return TaskAccepted{}, newInputError(ErrorCodePlan, messagePlanHashMismatch)
	}

	now := q.now()
	parentID := newTaskID(now)

	// 去重与路径校验：任何一条计划不合法都整体拒绝，避免半个计划落地。
	q.mu.Lock()
	for id, existing := range q.tasks {
		if !isInFlightState(existing.State) {
			continue
		}
		for _, item := range plan.Items {
			if existing.TargetRelPath == item.TargetRelPath && existing.ExpectedSHA256 == item.ExpectedSHA256 {
				createdAt := existing.CreatedAtMs
				parent := existing.ParentTaskID
				state := string(existing.State)
				q.mu.Unlock()
				return TaskAccepted{TaskID: parentOrSelf(parent, id), ParentTaskID: parent, State: state, Duplicate: true, CreatedAtMs: createdAt}, nil
			}
		}
	}
	q.mu.Unlock()

	for _, item := range plan.Items {
		if _, validateErr := artifact.ValidateRelativePath(item.TargetRelPath); validateErr != nil {
			return TaskAccepted{}, newInputError(ErrorCodePlan, messagePlanHashMismatch)
		}
	}

	parent := &Task{
		ID:             parentID,
		TaskType:       TaskTypePlan,
		State:          StateQueued,
		TargetKind:     normalizeTargetKind(req.TargetKind),
		PlanHash:       plan.PlanHash,
		ExpectedSize:   int64(len(plan.Items)),
		CreatedAtMs:    now,
		UpdatedAtMs:    now,
		VersionID:      req.VersionID,
		SourceURI:      "",
		TargetRelPath:  "",
		DownloadedSize: 0,
	}

	children := make([]Task, 0, len(plan.Items))
	for index, item := range plan.Items {
		child := Task{
			ID:             newTaskID(now + int64(index) + 1),
			ParentTaskID:   parentID,
			ArtifactID:     item.ArtifactID,
			TaskType:       item.TaskType,
			State:          StateQueued,
			SourceURI:      item.SourceURI,
			TargetRelPath:  item.TargetRelPath,
			TargetKind:     normalizeTargetKind(firstNonEmpty(item.TargetKind, req.TargetKind)),
			ExpectedSize:   item.ExpectedSize,
			ExpectedSHA1:   item.ExpectedSHA1,
			ExpectedSHA256: item.ExpectedSHA256,
			PlanHash:       plan.PlanHash,
			CreatedAtMs:    now + int64(index) + 1,
			UpdatedAtMs:    now + int64(index) + 1,
			VersionID:      req.VersionID,
		}
		children = append(children, child)
	}

	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return TaskAccepted{}, newInputError(ErrorCodeState, messageQueueClosed)
	}
	q.tasks[parent.ID] = parent
	q.seq[parent.ID] = now<<16 + int64(len(q.seq))
	for index := range children {
		copied := children[index]
		q.tasks[copied.ID] = &copied
		// 父任务不进入待调度列表：它只聚合子任务进度，不传输任何字节。
		q.enqueuePendingLocked(copied.ID)
	}
	parentSnapshot := *parent
	childSnapshots := make([]Task, len(children))
	copy(childSnapshots, children)
	q.mu.Unlock()

	q.persist(parentSnapshot)
	q.emit(parentSnapshot)
	for index := range childSnapshots {
		q.persist(childSnapshots[index])
		q.emit(childSnapshots[index])
	}
	q.kickLoop()

	return TaskAccepted{
		TaskID:      parentID,
		State:       string(StateQueued),
		Duplicate:   false,
		CreatedAtMs: now,
	}, nil
}

// parentOrSelf 在去重命中时返回父任务 ID（若有），否则返回自身。
func parentOrSelf(parent string, id string) string {
	if parent != "" {
		return parent
	}
	return id
}

// normalizeTargetKind 收敛目标类别取值。
func normalizeTargetKind(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case TargetKindVersion:
		return TargetKindVersion
	default:
		return TargetKindShared
	}
}

// firstNonEmpty 返回第一个非空字符串。
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// isInFlightState 判断状态是否属于「去重生效」的运行中状态。
func isInFlightState(state State) bool {
	switch state {
	case StateDownloading, StateVerifying, StateCommitting:
		return true
	default:
		return false
	}
}

// Pause 暂停任务。
func (q *Queue) Pause(id string) (DownloadTaskView, *Error) {
	if strings.TrimSpace(id) == "" {
		return DownloadTaskView{}, newInputError(ErrorCodeState, messageTaskIDEmpty)
	}
	q.mu.Lock()
	task, exists := q.tasks[id]
	if !exists {
		q.mu.Unlock()
		return DownloadTaskView{}, newInputError(ErrorCodeState, messageTaskNotFound)
	}
	current := task.State
	q.mu.Unlock()

	if !CanPause(current) {
		return DownloadTaskView{}, newInputError(ErrorCodeState, messageStateNotAllowed)
	}
	if _, transitionErr := q.applyTransition(id, StatePaused); transitionErr != nil {
		return DownloadTaskView{}, transitionErr
	}
	q.stopWorker(id)
	return q.viewOf(id)
}

// Resume 恢复被暂停的任务：paused → queued 重新排队。
func (q *Queue) Resume(id string) (DownloadTaskView, *Error) {
	if strings.TrimSpace(id) == "" {
		return DownloadTaskView{}, newInputError(ErrorCodeState, messageTaskIDEmpty)
	}
	q.mu.Lock()
	task, exists := q.tasks[id]
	if !exists {
		q.mu.Unlock()
		return DownloadTaskView{}, newInputError(ErrorCodeState, messageTaskNotFound)
	}
	current := task.State
	q.mu.Unlock()

	if !CanResume(current) {
		return DownloadTaskView{}, newInputError(ErrorCodeState, messageStateNotAllowed)
	}
	if _, transitionErr := q.applyTransition(id, StateQueued); transitionErr != nil {
		return DownloadTaskView{}, transitionErr
	}
	q.mu.Lock()
	q.enqueuePendingLocked(id)
	q.mu.Unlock()
	q.kickLoop()
	return q.viewOf(id)
}

// Cancel 取消任务。
//
// 与暂停的区别：取消是终态（cancelled），已下载字节不再续用；
// 提交阶段不可取消，返回 DOWNLOAD_CANCEL_001。
func (q *Queue) Cancel(id string) (DownloadTaskView, *Error) {
	if strings.TrimSpace(id) == "" {
		return DownloadTaskView{}, newInputError(ErrorCodeState, messageTaskIDEmpty)
	}
	q.mu.Lock()
	task, exists := q.tasks[id]
	if !exists {
		q.mu.Unlock()
		return DownloadTaskView{}, newInputError(ErrorCodeState, messageTaskNotFound)
	}
	current := task.State
	q.mu.Unlock()

	if current == StateCommitting {
		return DownloadTaskView{}, newInputError(ErrorCodeCancel, messageCancelDuringCommit)
	}
	if IsTerminal(current) || !CanCancel(current) {
		return DownloadTaskView{}, newInputError(ErrorCodeState, messageStateNotAllowed)
	}
	if _, transitionErr := q.applyTransition(id, StateCancelled); transitionErr != nil {
		return DownloadTaskView{}, transitionErr
	}
	q.stopWorker(id)
	q.bumpParent(id)
	return q.viewOf(id)
}

// stopWorker 取消指定任务的运行上下文（若在运行）。
func (q *Queue) stopWorker(id string) {
	q.mu.Lock()
	cancel := q.cancels[id]
	q.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// Get 返回单个任务视图。
func (q *Queue) Get(id string) (DownloadTaskView, *Error) {
	if strings.TrimSpace(id) == "" {
		return DownloadTaskView{}, newInputError(ErrorCodeState, messageTaskIDEmpty)
	}
	return q.viewOf(id)
}

// viewOf 构造任务视图。
func (q *Queue) viewOf(id string) (DownloadTaskView, *Error) {
	snapshot, exists := q.snapshotOf(id)
	if !exists {
		return DownloadTaskView{}, newInputError(ErrorCodeState, messageTaskNotFound)
	}
	view := taskToView(snapshot)
	if snapshot.TaskType == TaskTypePlan {
		// 父任务的「已下载 / 总数」在 bumpParent 内被复用为「已完成 / 制品总数」，
		// 这里换算成界面需要的 artifactCount / completedCount。
		view.ArtifactCount = int(snapshot.ExpectedSize)
		view.CompletedCount = int(snapshot.DownloadedSize)
		view.TotalBytes = nil
		view.Progress = nil
		if view.ArtifactCount > 0 {
			progress := float64(view.CompletedCount) / float64(view.ArtifactCount)
			view.Progress = &progress
		}
	} else {
		view.ArtifactCount = 1
		if snapshot.State == StateCompleted {
			view.CompletedCount = 1
		}
	}
	return view, nil
}

// List 返回任务列表（内存过滤 + 排序 + 分页）。
func (q *Queue) List(filter DownloadTaskFilter) DownloadTaskList {
	q.mu.Lock()
	candidates := make([]Task, 0, len(q.tasks))
	stateSet := make(map[string]bool, len(filter.States))
	for _, state := range filter.States {
		stateSet[state] = true
	}
	for _, task := range q.tasks {
		if len(stateSet) > 0 && !stateSet[string(task.State)] {
			continue
		}
		if filter.VersionID != "" && task.VersionID != filter.VersionID {
			continue
		}
		candidates = append(candidates, *task)
	}
	q.mu.Unlock()

	sortTasksForDisplay(candidates)
	total := len(candidates)
	offset := filter.Offset
	if offset < 0 {
		offset = 0
	}
	limit := filter.Limit
	if limit <= 0 {
		limit = maxListLimit
	}
	if limit > maxListLimit {
		limit = maxListLimit
	}
	if offset >= total {
		return DownloadTaskList{Tasks: []DownloadTaskView{}, Total: total}
	}
	end := offset + limit
	if end > total {
		end = total
	}
	views := make([]DownloadTaskView, 0, end-offset)
	for index := offset; index < end; index++ {
		view := taskToView(candidates[index])
		if candidates[index].TaskType == TaskTypePlan {
			view.ArtifactCount = int(candidates[index].ExpectedSize)
			view.CompletedCount = int(candidates[index].DownloadedSize)
			view.TotalBytes = nil
			view.Progress = nil
			if view.ArtifactCount > 0 {
				progress := float64(view.CompletedCount) / float64(view.ArtifactCount)
				view.Progress = &progress
			}
		} else {
			view.ArtifactCount = 1
			if candidates[index].State == StateCompleted {
				view.CompletedCount = 1
			}
		}
		views = append(views, view)
	}
	return DownloadTaskList{Tasks: views, Total: total}
}

// newTaskID 生成任务标识（时间戳 + 随机后缀，避免同一毫秒碰撞）。
func newTaskID(nowMs int64) string {
	return fmt.Sprintf("%d-%s", nowMs, randomHex(4))
}

// randomHex 生成 n 字节的随机十六进制串（2n 个字符）。
//
// 用 crypto/rand 而不是时间戳自增：任务 ID 会出现在日志与事件载荷里，
// 可预测的 ID 会让「猜一个 ID 操作别人的任务」变容易（P3 之后再接实例隔离）。
func randomHex(n int) string {
	buffer := make([]byte, n)
	if _, err := rand.Read(buffer); err != nil {
		// 随机源不可用时退回时间戳，保证 ID 仍然唯一且不阻塞调度。
		return fmt.Sprintf("%016x", time.Now().UnixNano())
	}
	return hex.EncodeToString(buffer)
}
