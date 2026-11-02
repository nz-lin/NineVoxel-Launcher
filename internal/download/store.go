// 本文件实现下载任务表（download_tasks）的持久化。
//
// 设计取舍：
//   - 调度状态的**真相源是内存队列**，数据库是持久化副本。这样重启恢复、
//     进度更新都不必每次读盘，也避免 SQLite 写锁成为下载吞吐的瓶颈；
//   - 因此本文件只提供「保存单个任务」与「载入全部任务」两个动作，
//     列表过滤在内存里做（见 queue.go 的 ListDownloadTasks）；
//   - 存储不可用时**降级为纯内存**而不是让服务起不来：下载任务的丢失
//     只影响「重启后看不到历史任务」，不应该拖垮整个启动器（与 P1-04 一致）。
package download

import (
	"context"
	"database/sql"
	"errors"
	"sync"

	"example.com/ninevoxel/launcher/internal/database"
	"example.com/ninevoxel/launcher/internal/settings"
)

// TaskStore 是任务持久化接口。
//
// 抽出接口是为了让队列在「数据库不可用」时能换上一份内存实现，
// 也让队列的单元测试不必真的落盘。
type TaskStore interface {
	// Save 写入或更新一条任务记录（按 id 幂等）。
	Save(ctx context.Context, task Task) error

	// LoadAll 读取全部任务记录。
	LoadAll(ctx context.Context) ([]Task, error)

	// Close 释放底层资源（内存实现为空操作）。
	Close() error
}

// 任务表涉及的列，集中定义以便读写两侧保持一致。
const taskColumns = `id, parent_task_id, artifact_id, version_id, task_type, state, source_uri,
	staging_rel_path, target_rel_path, target_kind, expected_size, downloaded_size,
	range_start, expected_sha1, expected_sha256, etag, last_modified, plan_hash,
	retry_count, error_code, error_message_safe, verified_at_ms, created_at_ms,
	updated_at_ms, completed_at_ms`

// saveTaskSQL 是幂等写入语句。
//
// 用 ON CONFLICT(id) DO UPDATE 而不是「先查后写」：状态推进频率高（每秒多次进度），
// 先查后写会在并发下产生竞态，也让每次更新多一次往返。
const saveTaskSQL = `INSERT INTO download_tasks (` + taskColumns + `)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(id) DO UPDATE SET
		state = excluded.state,
		staging_rel_path = excluded.staging_rel_path,
		target_rel_path = excluded.target_rel_path,
		target_kind = excluded.target_kind,
		expected_size = excluded.expected_size,
		downloaded_size = excluded.downloaded_size,
		range_start = excluded.range_start,
		expected_sha1 = excluded.expected_sha1,
		expected_sha256 = excluded.expected_sha256,
		etag = excluded.etag,
		last_modified = excluded.last_modified,
		plan_hash = excluded.plan_hash,
		retry_count = excluded.retry_count,
		error_code = excluded.error_code,
		error_message_safe = excluded.error_message_safe,
		verified_at_ms = excluded.verified_at_ms,
		updated_at_ms = excluded.updated_at_ms,
		completed_at_ms = excluded.completed_at_ms`

// loadTasksSQL 读取全部任务（按创建时间升序，保证列表顺序稳定）。
const loadTasksSQL = `SELECT ` + taskColumns + ` FROM download_tasks ORDER BY created_at_ms ASC, id ASC`

// SQLiteTaskStore 是基于 SQLite 的任务存储实现。
type SQLiteTaskStore struct {
	db *sql.DB
}

// NewSQLiteTaskStore 用已有连接构造存储。
//
// 调用方负责连接的打开与迁移（见 OpenTaskStore），
// 这样测试可以把连接指向临时目录或内存 DSN。
func NewSQLiteTaskStore(db *sql.DB) *SQLiteTaskStore {
	return &SQLiteTaskStore{db: db}
}

// Save 写入或更新一条任务记录。
func (s *SQLiteTaskStore) Save(ctx context.Context, task Task) error {
	if s == nil || s.db == nil {
		return errors.New("download: 任务存储未初始化")
	}
	_, err := s.db.ExecContext(ctx, saveTaskSQL,
		task.ID,
		task.ParentTaskID,
		task.ArtifactID,
		task.VersionID,
		task.TaskType,
		string(task.State),
		task.SourceURI,
		task.StagingRelPath,
		task.TargetRelPath,
		task.TargetKind,
		task.ExpectedSize,
		task.DownloadedSize,
		task.RangeStart,
		task.ExpectedSHA1,
		task.ExpectedSHA256,
		task.ETag,
		task.LastModified,
		task.PlanHash,
		task.RetryCount,
		task.ErrorCode,
		task.ErrorMessageSafe,
		task.VerifiedAtMs,
		task.CreatedAtMs,
		task.UpdatedAtMs,
		task.CompletedAtMs,
	)
	return err
}

// LoadAll 读取全部任务记录。
//
// 单条记录的状态值损坏时不报错整体失败：把该条按 failed 载入并保留错误码，
// 比让整个列表接口失败更有用（用户至少能看到「有一条任务坏了」）。
func (s *SQLiteTaskStore) LoadAll(ctx context.Context) ([]Task, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("download: 任务存储未初始化")
	}
	rows, err := s.db.QueryContext(ctx, loadTasksSQL)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = rows.Close()
	}()

	tasks := make([]Task, 0, 32)
	for rows.Next() {
		var (
			task     Task
			stateRaw string
		)
		if scanErr := rows.Scan(
			&task.ID,
			&task.ParentTaskID,
			&task.ArtifactID,
			&task.VersionID,
			&task.TaskType,
			&stateRaw,
			&task.SourceURI,
			&task.StagingRelPath,
			&task.TargetRelPath,
			&task.TargetKind,
			&task.ExpectedSize,
			&task.DownloadedSize,
			&task.RangeStart,
			&task.ExpectedSHA1,
			&task.ExpectedSHA256,
			&task.ETag,
			&task.LastModified,
			&task.PlanHash,
			&task.RetryCount,
			&task.ErrorCode,
			&task.ErrorMessageSafe,
			&task.VerifiedAtMs,
			&task.CreatedAtMs,
			&task.UpdatedAtMs,
			&task.CompletedAtMs,
		); scanErr != nil {
			return nil, scanErr
		}
		task.State = State(stateRaw)
		if !IsValidState(task.State) {
			task.State = StateFailed
			if task.ErrorCode == "" {
				task.ErrorCode = ErrorCodeState
			}
		}
		tasks = append(tasks, task)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, rowsErr
	}
	return tasks, nil
}

// Close 关闭底层连接。
func (s *SQLiteTaskStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// memoryTaskStore 是存储不可用时的降级实现。
//
// 它只保证「本次运行内一致」，进程退出即丢；界面据此提示
// messageDatabaseUnavailable，让用户知道历史任务不会被记住。
type memoryTaskStore struct {
	mu    sync.RWMutex
	tasks map[string]Task
	order []string
}

// newMemoryTaskStore 创建一个空的内存存储。
func newMemoryTaskStore() *memoryTaskStore {
	return &memoryTaskStore{tasks: make(map[string]Task, 32)}
}

// Save 写入或更新一条任务记录。
func (s *memoryTaskStore) Save(_ context.Context, task Task) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.tasks[task.ID]; !exists {
		s.order = append(s.order, task.ID)
	}
	s.tasks[task.ID] = task
	return nil
}

// LoadAll 按写入顺序返回全部任务。
func (s *memoryTaskStore) LoadAll(_ context.Context) ([]Task, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]Task, 0, len(s.order))
	for _, id := range s.order {
		if task, ok := s.tasks[id]; ok {
			result = append(result, task)
		}
	}
	return result, nil
}

// Close 为空操作（无外部资源）。
func (s *memoryTaskStore) Close() error {
	return nil
}

// taskStoreFactory 打开任务存储；测试可替换为内存连接。
//
// 抽成包级变量而不是参数：队列由服务层构造，若把存储当参数暴露给
// 每个调用点，会污染对外方法签名，而 Wails 绑定要求签名稳定。
var taskStoreFactory = openTaskStoreFromDataRoot

// openTaskStoreFromDataRoot 按与设置模块一致的数据目录打开任务库并执行迁移。
//
// 迁移失败时返回 *Error（DB_MIGRATION_001），由调用方降级为内存存储。
func openTaskStoreFromDataRoot(ctx context.Context) (TaskStore, *Error) {
	status := databasePathResolver()
	if status == "" {
		return nil, newSystemError(ErrorCodeDatabaseOpen, messageDatabaseUnavailable, false)
	}
	db, openErr := database.Open(status)
	if openErr != nil {
		return nil, newSystemError(ErrorCodeDatabaseOpen, messageDatabaseUnavailable, false)
	}
	if _, migrateErr := database.Migrate(ctx, db); migrateErr != nil {
		_ = db.Close()
		return nil, newSystemError(ErrorCodeDatabaseMigration, messageDatabaseUnavailable, false)
	}
	return NewSQLiteTaskStore(db), nil
}

// databasePathResolver 返回应用数据库文件路径；测试中替换为临时路径。
var databasePathResolver = resolveDefaultDatabasePath

// resolveDefaultDatabasePath 复用 P1 已有的数据库路径策略。
//
// 与 P2 的启动配置（internal/launch/profile.go）保持同一套规则：
// 用户配置目录优先、便携版 exe 同级 data/ 兜底，两处不能各写一套，
// 否则同一个应用会分裂出两个 app.db。settings 包不依赖本包，
// 因此这里 import settings 不构成包级循环。
func resolveDefaultDatabasePath() string {
	return settings.ResolveDatabasePath().Path
}
