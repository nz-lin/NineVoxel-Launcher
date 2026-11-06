// 本文件提供进程级唯一的数据库连接池。
//
// 背景（连接治理）：此前 settings / account / launch / runtime / instance / download
// 六个服务各自解析路径、各自 Open、各自 Migrate 同一个 app.db。功能上正确，
// 但同一进程会持有多个连接池，路径解析还出现了第二套实现（instance 直接拼
// DataRoot），一旦数据库位置调整就会漏改。
//
// 现在统一为：
//   - 路径只由 artifact.DatabasePath 决定（<数据根>/db/app.db）；
//   - 连接与迁移只在 Default 中执行一次，所有服务共用同一个 *sql.DB；
//   - 持有共享池的服务不得关闭它（用 IsDefault 判定），进程退出时由系统回收。
package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"example.com/ninevoxel/launcher/internal/artifact"
)

// defaultMu 保护 defaultDB / defaultErr 的初始化与读取。
//
// 不用 sync.Once：失败也必须被记住，否则每个服务都会重试一次失败的迁移，
// 启动日志会出现多条重复错误。
var defaultMu sync.Mutex

// defaultDB 为进程级共享连接池；nil 表示尚未打开或打开失败。
var defaultDB *sql.DB

// defaultErr 为首次打开/迁移的失败原因；非 nil 时后续调用直接复用该错误。
var defaultErr error

// legacyLayoutOnce / legacyLayoutErr 保证旧布局搬迁只尝试一次。
var (
	legacyLayoutOnce sync.Once
	legacyLayoutErr  error
)

// DefaultPath 返回默认数据库文件路径，并按需创建数据库目录与搬迁旧布局。
//
// 位置规则只有一处：artifact.DatabasePath()（`<数据根>/db/app.db`）。
//
// 搬迁规则（一次性、尽力而为）：若旧版本的 `<数据根>/app.db` 还存在且新位置
// 尚无同名文件，则把 app.db 及其 `-wal`/`-shm`、`app.db.backup-*` 一并移入 `db/`。
// 这样升级后用户的设置、账号、实例索引不会凭空消失。
//
// 返回错误只表示目录无法创建；路径解析失败会带 artifact 的上下文。
func DefaultPath() (string, error) {
	path, pathErr := artifact.DatabasePath()
	if pathErr != nil {
		return "", fmt.Errorf("database: 解析数据库路径失败: %w", pathErr)
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("database: 创建数据库目录失败: %w", err)
	}

	legacyLayoutOnce.Do(func() {
		legacyLayoutErr = migrateLegacyLayout(filepath.Dir(dir), dir)
	})
	if legacyLayoutErr != nil {
		return "", legacyLayoutErr
	}
	return path, nil
}

// Default 返回进程级共享连接池，首次调用时打开并执行迁移。
//
// 返回的 *sql.DB 由本包持有：调用方**不得**调用 Close（用 IsDefault 判定，
// 或直接交给同样遵守该约定的服务包装）。打开或迁移失败会缓存错误，
// 后续调用立即返回同一个错误，避免启动阶段反复重试与重复日志。
func Default(ctx context.Context) (*sql.DB, error) {
	defaultMu.Lock()
	defer defaultMu.Unlock()

	if defaultDB != nil {
		return defaultDB, nil
	}
	if defaultErr != nil {
		return nil, defaultErr
	}

	path, pathErr := DefaultPath()
	if pathErr != nil {
		defaultErr = pathErr
		return nil, defaultErr
	}

	db, openErr := Open(path)
	if openErr != nil {
		defaultErr = openErr
		return nil, defaultErr
	}

	// 迁移只发生在这里：各服务不再各自 Migrate 同一个文件。
	if _, migrateErr := Migrate(ctx, db); migrateErr != nil {
		_ = db.Close()
		defaultErr = migrateErr
		return nil, defaultErr
	}

	defaultDB = db
	return defaultDB, nil
}

// IsDefault 判断给定连接是否就是进程级共享池。
//
// 服务在关闭自身资源时用它决定是否跳过 Close：共享池随进程结束回收，
// 提前关闭会让其他仍在使用它的服务随机失败。
func IsDefault(db *sql.DB) bool {
	if db == nil {
		return false
	}
	defaultMu.Lock()
	defer defaultMu.Unlock()
	return db == defaultDB
}

// CloseDefault 关闭并重置共享池。
//
// 仅供测试或进程退出前的显式清理使用；正常运行时不需要调用。
func CloseDefault() error {
	defaultMu.Lock()
	defer defaultMu.Unlock()

	db := defaultDB
	defaultDB = nil
	defaultErr = nil
	legacyLayoutOnce = sync.Once{}
	legacyLayoutErr = nil
	if db == nil {
		return nil
	}
	return db.Close()
}

// migrateLegacyLayout 把旧版散落在数据根顶层的数据库文件移入 db/。
//
// 参数 root 为数据根，targetDir 为新的数据库目录。
// 目标已存在时不做任何移动：说明新布局已经生效，旧文件可能是用户手动放回的，
// 不能覆盖新库。
func migrateLegacyLayout(root, targetDir string) error {
	if filepath.Clean(root) == "" || filepath.Clean(targetDir) == "" {
		return errors.New("database: 数据根与数据库目录不能为空")
	}
	if filepath.Clean(filepath.Dir(targetDir)) != filepath.Clean(root) {
		// 防御：只处理「数据根/db」这一种布局。
		return nil
	}

	legacyDB := filepath.Join(root, artifact.DatabaseFileName)
	newDB := filepath.Join(targetDir, artifact.DatabaseFileName)
	if _, statErr := os.Stat(legacyDB); statErr != nil {
		return nil
	}
	if _, statErr := os.Stat(newDB); statErr == nil {
		return nil
	}

	names := []string{
		artifact.DatabaseFileName,
		artifact.DatabaseFileName + "-wal",
		artifact.DatabaseFileName + "-shm",
	}
	moved := make([]string, 0, len(names))
	for _, name := range names {
		from := filepath.Join(root, name)
		if _, statErr := os.Stat(from); statErr != nil {
			continue
		}
		if err := os.Rename(from, filepath.Join(targetDir, name)); err != nil {
			// 已移动的文件必须回滚：主库已搬走而 WAL 仍在旧位置，
			// 会让两边都处于不完整状态，比「不迁移」更糟。
			for index := len(moved) - 1; index >= 0; index-- {
				_ = os.Rename(filepath.Join(targetDir, moved[index]), filepath.Join(root, moved[index]))
			}
			return fmt.Errorf("database: 搬迁旧数据库文件 %s 失败: %w", name, err)
		}
		moved = append(moved, name)
	}

	// 迁移前备份同样属于数据库资产，一并搬走；失败不阻断启动。
	entries, readErr := os.ReadDir(root)
	if readErr != nil {
		return nil
	}
	prefix := artifact.DatabaseFileName + ".backup-"
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), prefix) {
			continue
		}
		_ = os.Rename(
			filepath.Join(root, entry.Name()),
			filepath.Join(targetDir, entry.Name()),
		)
	}
	return nil
}
