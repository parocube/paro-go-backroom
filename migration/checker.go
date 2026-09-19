package migration

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pressly/goose/v3"
)

// ErrSchemaMismatch 表示数据库缺少迁移或存在当前程序不支持的已生效版本，必须由显式迁移流程处理。
var ErrSchemaMismatch = errors.New("database schema does not match embedded migrations")

// Checker 在启动和 readiness 中只读核对迁移历史；不执行迁移，也不创建 Goose 版本表。
// expected 构造后不再修改，因此可由多个健康检查请求并发复用。
type Checker struct {
	pool     *pgxpool.Pool
	expected []int64
	timeout  time.Duration
}

// NewChecker 从程序内置的 SQL 文件名提取版本；配置为空、文件无效或版本重复时返回错误。
func NewChecker(pool *pgxpool.Pool, files fs.FS, timeout time.Duration) (*Checker, error) {
	if pool == nil || files == nil || timeout <= 0 {
		return nil, errors.New("schema checker configuration is invalid")
	}
	names, err := fs.Glob(files, "*.sql")
	if err != nil {
		return nil, fmt.Errorf("list embedded migrations: %w", err)
	}
	if len(names) == 0 {
		return nil, errors.New("embedded migrations are empty")
	}
	// 版本 0 是 Goose 创建的初始记录；正版本来自文件，不能把最大版本号当成完整迁移历史。
	expected := []int64{0}
	for _, name := range names {
		version, err := goose.NumericComponent(name)
		if err != nil {
			return nil, fmt.Errorf("parse embedded migration version: %w", err)
		}
		expected = append(expected, version)
	}
	slices.Sort(expected)
	for index := 1; index < len(expected); index++ {
		if expected[index] == expected[index-1] {
			return nil, fmt.Errorf("duplicate embedded migration version: %d", expected[index])
		}
	}
	return &Checker{pool: pool, expected: expected, timeout: timeout}, nil
}

// Ping 在有限时间内读取每个版本的最后一条记录，要求当前生效版本与内置迁移完全一致。
// Goose 的旧历史使用追加的 up/down 行，新版本可能删除回滚行；两种历史都按最终状态判断。
func (c *Checker) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	// 单条 SELECT 同时探测连接和迁移历史，快照内不存在先读最大版本、再读明细的竞态。
	// 不调用 Goose 的版本读取 API，因为它会在版本表缺失时尝试建表。
	rows, err := c.pool.Query(ctx, `
		SELECT DISTINCT ON (version_id) version_id, is_applied
		FROM goose_db_version
		ORDER BY version_id, id DESC
	`)
	if err != nil {
		return schemaReadError(err)
	}
	defer rows.Close()
	applied := make(map[int64]bool, len(c.expected))
	for rows.Next() {
		var version int64
		var isApplied bool
		if err := rows.Scan(&version, &isApplied); err != nil {
			return schemaReadError(err)
		}
		if isApplied {
			applied[version] = true
		}
	}
	if err := rows.Err(); err != nil {
		return schemaReadError(err)
	}
	for _, version := range c.expected {
		if !applied[version] {
			return fmt.Errorf("%w: migration %d is not applied", ErrSchemaMismatch, version)
		}
		delete(applied, version)
	}
	// 尚未定义跨版本兼容范围；额外的已生效迁移一律拒绝，避免旧程序误用新结构。
	if len(applied) != 0 {
		return fmt.Errorf("%w: unsupported applied migration", ErrSchemaMismatch)
	}
	return nil
}

// schemaReadError 屏蔽数据库返回的任意明细，但保留原因链供 errors.Is/As 判断取消、超时和 SQLSTATE。
func schemaReadError(err error) error {
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) && postgresError.Code == "42P01" {
		return fmt.Errorf("%w: migration history table is missing", ErrSchemaMismatch)
	}
	return &historyReadError{cause: err}
}

type historyReadError struct {
	cause error
}

func (e *historyReadError) Error() string { return "read migration history failed" }
func (e *historyReadError) Unwrap() error { return e.cause }
