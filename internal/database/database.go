// Package database 提供 NineVoxel Launcher 的 SQLite 连接入口。
//
// 本包只负责按 ADR-002 约定的 DSN 打开数据库并暴露最小的引擎信息查询函数，
// 不包含建表、迁移执行与业务查询逻辑，这些内容属于后续阶段的任务。
package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	// 以空白导入方式注册 modernc.org/sqlite 驱动。
	// 该驱动为纯 Go 实现，CGO_ENABLED=0 时也能构建与运行。
	_ "modernc.org/sqlite"
)

// driverName 为 modernc.org/sqlite 注册的驱动名。
const driverName = "sqlite"

// connectionParams 是 ADR-002 要求写入 DSN 的连接级参数。
//
// 这些参数必须写在 DSN 中：一次性 PRAGMA 只对当前连接生效，
// 连接池新建的连接不会继承，因此不能改为打开数据库后再执行。
//   - journal_mode(WAL)：启用预写日志，读写可并发。
//   - foreign_keys(1)：开启外键约束，该开关默认是关闭的。
//   - busy_timeout(5000)：遇到锁冲突时最多等待 5000 毫秒。
//   - synchronous(NORMAL)：WAL 模式下的推荐同步级别，兼顾安全与性能。
//   - _txlock=immediate：事务开始即获取写锁，避免读事务升级写锁时立刻返回 SQLITE_BUSY。
const connectionParams = "_pragma=journal_mode(WAL)" +
	"&_pragma=foreign_keys(1)" +
	"&_pragma=busy_timeout(5000)" +
	"&_pragma=synchronous(NORMAL)" +
	"&_txlock=immediate"

// Open 按 ADR-002 的 DSN 约定打开 SQLite 数据库。
//
// 参数 path 支持两种形式：
//   - 文件路径（相对或绝对），例如 data/launcher.db，函数会自动加上 file: 前缀；
//   - 以 file: 开头的完整 DSN，例如 file::memory:?cache=shared，函数只在后面追加连接参数。
//
// 返回的 *sql.DB 连接池已带上 WAL、外键、busy_timeout 与立即事务参数，
// 调用方在不再使用时负责调用 db.Close() 释放连接。
// 打开或首次建连失败时返回带上下文信息的错误，并且不会泄漏已创建的连接池。
func Open(path string) (*sql.DB, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("database: 数据库路径不能为空")
	}

	db, err := sql.Open(driverName, buildDSN(path))
	if err != nil {
		return nil, fmt.Errorf("database: 打开数据库失败: %w", err)
	}

	// 立即建立一次连接，尽早暴露 DSN 或驱动层面的错误，
	// 避免错误延迟到第一次真正查询时才出现。
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("database: 连接数据库失败: %w", err)
	}

	return db, nil
}

// Version 返回当前数据库连接所使用的 SQLite 引擎版本。
//
// 版本号来自 SELECT sqlite_version()，例如 3.54.1。
// 该函数用于 ADR-002 门槛 3 的 CI 冒烟断言：只要驱动被升级或替换，
// 构建后执行本函数即可观察到引擎版本变化。
// 查询失败时返回空字符串与带上下文的错误。
func Version(ctx context.Context, db *sql.DB) (string, error) {
	var version string
	if err := db.QueryRowContext(ctx, "SELECT sqlite_version()").Scan(&version); err != nil {
		return "", fmt.Errorf("database: 查询 SQLite 版本失败: %w", err)
	}
	if version == "" {
		return "", errors.New("database: SQLite 版本号为空")
	}
	return version, nil
}

// buildDSN 拼接 ADR-002 约定的完整 DSN。
//
// 传入 file: 开头的 DSN 时原样复用，否则把文件路径转换为斜杠形式并补上 file: 前缀；
// 若 DSN 已经带有查询参数则用 & 追加连接参数，否则以 ? 开始。
func buildDSN(path string) string {
	dsn := "file:" + filepath.ToSlash(path)
	if strings.HasPrefix(path, "file:") {
		dsn = path
	}

	separator := "?"
	if strings.Contains(dsn, "?") {
		separator = "&"
	}

	return dsn + separator + connectionParams
}
