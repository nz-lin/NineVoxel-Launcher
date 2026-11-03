// 本文件实现基于 SQLite 的设置存储。
//
// 分工：连接建立与 DSN pragma 全部复用 internal/database（ADR-002 的唯一实现），
// 本文件只负责三件事：
//  1. 解析数据库文件路径（复用 artifact.DataRoot，确保 app.db 与制品缓存使用同一数据根）；
//  2. 打开数据库并执行迁移（复用 database.Open 与 database.Migrate）；
//  3. 以 key-value 形式读写 app_settings 表。
//
// 设计前提：设置是**非秘密的用户偏好**，读写失败必须可降级、可上报，绝不阻塞启动。
package settings

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"example.com/ninevoxel/launcher/internal/artifact"
	"example.com/ninevoxel/launcher/internal/database"
	"example.com/ninevoxel/launcher/internal/platform"
)

// 数据目录与数据库文件名的常量。
const (
	// appDataDirName 是用户配置目录下使用的应用数据目录名。
	appDataDirName = "NineVoxel-Launcher"

	// dataSubDirName 是数据目录下的子目录名。
	dataSubDirName = "data"

	// databaseFileName 是数据库文件名，由接口冻结清单 §8.9 冻结。
	databaseFileName = "app.db"

	// tableNameAppSettings 是设置表的名称，与迁移 SQL 保持一致。
	tableNameAppSettings = "app_settings"
)

// Store 是设置存储的抽象，便于测试替换与未来切换实现。
//
// 当前只有 SQLite 一种实现（主路径已由用户确认）；接口存在是为了让服务层
// 不直接绑定 sql.DB，测试时可注入内存实现。
type Store interface {
	// Load 读取全部已存储的设置项原始值（键为设置 key，值为反序列化后的 any）。
	Load(ctx context.Context) (map[string]any, error)

	// Save 在一个事务内写入一批设置项。
	//
	// 调用方必须已通过 ValidateChange 校验；本方法只负责原子落盘。
	// 参数 changes 为 key → 值的映射；updatedAtMs 为写入时间（UTC Unix 毫秒）。
	Save(ctx context.Context, changes map[string]any, updatedAtMs int64) error

	// Close 释放底层资源。
	Close() error
}

// SQLiteStore 是基于 SQLite 的 Store 实现。
//
// 句柄在应用生命周期内复用（卡面 C.6 要求），不得每个方法新建连接。
type SQLiteStore struct {
	// db 为按 ADR-002 打开的连接池。
	db *sql.DB

	// path 为数据库文件路径。
	//
	// 该值**只用于进程内日志与备份**，不得通过服务方法返回给前端（含绝对路径）。
	path string
}

// DatabasePathStatus 描述数据库文件路径的解析结果。
type DatabasePathStatus struct {
	// Path 为解析出的数据库文件路径；为空表示两处候选目录都不可用。
	Path string

	// Kind 为路径来源类别：userConfig / exeDir / unavailable。
	Kind string
}

// 路径来源类别常量：与 internal/logging 的 directoryKind 风格保持一致。
const (
	// PathKindUserConfig 表示数据库落在用户配置目录。
	PathKindUserConfig = "userConfig"

	// PathKindExeDir 表示数据库回退到可执行文件同级目录。
	PathKindExeDir = "exeDir"

	// PathKindUnavailable 表示两处目录都不可用。
	PathKindUnavailable = "unavailable"
)

// ResolveDatabasePath 解析数据库文件路径。
//
// 数据根以 artifact.DataRoot 为唯一权威：exe 同级 `data/` 已存在时优先使用
// 便携目录；否则使用用户配置目录，用户目录不可写时再回退 exe 同级目录。
// 这样 app.db 不会与实例、版本和下载缓存分裂到两个数据目录。
//
// 可写性判定复用 platform.ProbeWritable，与 P1-01 的目录探测保持同一套语义
// （真实创建临时文件，而不是只看目录是否存在）。
func ResolveDatabasePath() DatabasePathStatus {
	root, rootErr := artifact.DataRoot()
	if rootErr != nil || root == "" {
		return DatabasePathStatus{Kind: PathKindUnavailable}
	}

	// artifact.DataRoot 只做不创建目录的探测；这里创建数据根并再次确认可写，
	// 因为 SQLite 无法在尚不存在的目录中创建 app.db。
	if !platform.ProbeWritable(root) {
		return DatabasePathStatus{Kind: PathKindUnavailable}
	}

	return DatabasePathStatus{
		Path: filepath.Join(root, databaseFileName),
		Kind: databasePathKind(root),
	}
}

// databasePathKind 根据数据根位置返回数据库目录来源类别。
//
// artifact.DataRoot 只会返回用户配置目录或 exe 同级便携目录；无法匹配
// 用户配置目录时按 exe 目录归类，供前端展示而不暴露绝对路径。
func databasePathKind(root string) string {
	if configDir, err := os.UserConfigDir(); err == nil && configDir != "" {
		userRoot := filepath.Join(configDir, appDataDirName, dataSubDirName)
		if filepath.Clean(root) == filepath.Clean(userRoot) {
			return PathKindUserConfig
		}
	}
	return PathKindExeDir
}

// OpenSQLiteStore 打开（必要时创建）SQLite 设置存储并执行迁移。
//
// 参数 ctx 为调用方上下文。
// 行为：
//   - 解析路径失败 → 返回包装 ErrorCodePathInvalid 的错误；
//   - 打开数据库失败 → 返回包装 ErrorCodeDatabaseOpen 的错误；
//   - 迁移失败（含 checksum 不一致）→ 原样返回 database.Migrate 的错误（已带 DB_MIGRATION_001）。
//
// 返回值中的 *SQLiteStore 在失败时为 nil，调用方应据此进入降级模式。
func OpenSQLiteStore(ctx context.Context) (*SQLiteStore, error) {
	status := ResolveDatabasePath()
	if status.Path == "" {
		return nil, fmt.Errorf("%s: 数据目录不可用，无法定位数据库文件", ErrorCodePathInvalid)
	}

	db, err := database.Open(status.Path)
	if err != nil {
		return nil, fmt.Errorf("%s: 打开设置数据库失败: %w", ErrorCodeDatabaseOpen, err)
	}

	// 迁移必须在任何读写之前完成：表不存在时任何查询都会失败。
	if _, err := database.Migrate(ctx, db); err != nil {
		// 迁移错误已由 database 包带上 DB_MIGRATION_001，直接关闭连接并向上传递。
		_ = db.Close()
		return nil, err
	}

	return &SQLiteStore{db: db, path: status.Path}, nil
}

// Path 返回数据库文件路径。
//
// 该方法**仅供进程内日志使用**，不得通过服务方法暴露给前端。
func (s *SQLiteStore) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

// Load 读取全部已存储的设置项。
//
// 返回的是原始反序列化结果（`any`）；是否合法由 NormalizeSnapshot 统一收敛。
// 读取失败返回带上下文的错误，由服务层决定是降级为默认值还是上报错误。
func (s *SQLiteStore) Load(ctx context.Context) (map[string]any, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("%s: 设置数据库未打开", ErrorCodeDatabaseOpen)
	}

	rows, err := s.db.QueryContext(ctx, `SELECT key, value_json FROM `+tableNameAppSettings)
	if err != nil {
		return nil, fmt.Errorf("%s: 读取设置失败: %w", ErrorCodeDatabaseOpen, err)
	}
	defer rows.Close()

	result := make(map[string]any)
	for rows.Next() {
		var key string
		var raw string
		if err := rows.Scan(&key, &raw); err != nil {
			return nil, fmt.Errorf("%s: 解析设置行失败: %w", ErrorCodeDatabaseOpen, err)
		}

		var value any
		if err := json.Unmarshal([]byte(raw), &value); err != nil {
			// 单行 JSON 损坏时不整体失败：跳过该行，该 key 会回退默认值。
			continue
		}
		result[key] = value
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%s: 遍历设置失败: %w", ErrorCodeDatabaseOpen, err)
	}

	return result, nil
}

// Save 在一个事务内原子写入一批设置项。
//
// 参数 changes 为 key → 值；updatedAtMs 为写入时间。
// 使用 Upsert（ON CONFLICT DO UPDATE）保证同一 key 只有一行。
//
// 事务边界：全部写入成功才提交，任一失败整体回滚——
// 「改了一半」会让用户点保存后看到部分生效，是最难解释的失败形态。
func (s *SQLiteStore) Save(ctx context.Context, changes map[string]any, updatedAtMs int64) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("%s: 设置数据库未打开", ErrorCodeDatabaseOpen)
	}
	if len(changes) == 0 {
		return nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("%s: 开始写入事务失败: %w", ErrorCodeDatabaseOpen, err)
	}
	defer func() { _ = tx.Rollback() }()

	// 按 key 排序后写入：保证同一批数据在多次执行时产生相同的 SQL 序列，便于排查。
	keys := make([]string, 0, len(changes))
	for key := range changes {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	const upsert = `INSERT INTO ` + tableNameAppSettings + ` (key, value_json, updated_at_ms)
VALUES (?, ?, ?)
ON CONFLICT(key) DO UPDATE SET value_json = excluded.value_json, updated_at_ms = excluded.updated_at_ms`

	for _, key := range keys {
		encoded, err := json.Marshal(changes[key])
		if err != nil {
			return fmt.Errorf("%s: 序列化设置项 %s 失败: %w", ErrorCodeConfigInvalid, key, err)
		}
		if _, err := tx.ExecContext(ctx, upsert, key, string(encoded), updatedAtMs); err != nil {
			return fmt.Errorf("%s: 写入设置项 %s 失败: %w", ErrorCodeDatabaseOpen, key, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("%s: 提交设置事务失败: %w", ErrorCodeDatabaseOpen, err)
	}
	return nil
}

// Close 关闭数据库连接。
//
// 关闭失败不作为错误上报：进程即将退出，句柄会被系统回收。
func (s *SQLiteStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// MemoryStore 是 Store 的内存实现，仅用于测试与降级场景。
//
// 它保证「数据库不可用时设置仍可在本次会话内读写」，
// 但**不提供持久化**，服务层会据此把 Enabled 标为 false 并提示用户。
type MemoryStore struct {
	// values 为当前内存中的设置值。
	values map[string]any
}

// NewMemoryStore 创建一个空的内存存储。
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{values: make(map[string]any)}
}

// Load 返回内存中的全部设置项。
func (m *MemoryStore) Load(_ context.Context) (map[string]any, error) {
	if m == nil {
		return nil, errors.New("settings: 内存存储未初始化")
	}

	// 返回副本：调用方修改返回值不应影响存储内部状态。
	result := make(map[string]any, len(m.values))
	for key, value := range m.values {
		result[key] = value
	}
	return result, nil
}

// Save 把设置项写入内存（不做原子性保证，内存场景无需事务）。
func (m *MemoryStore) Save(_ context.Context, changes map[string]any, _ int64) error {
	if m == nil {
		return errors.New("settings: 内存存储未初始化")
	}
	for key, value := range changes {
		m.values[key] = value
	}
	return nil
}

// Close 对内存存储为空操作。
func (m *MemoryStore) Close() error {
	return nil
}

// nowMillis 返回当前 UTC Unix 毫秒，作为设置写入时间的统一来源。
func nowMillis() int64 {
	return time.Now().UTC().UnixMilli()
}
