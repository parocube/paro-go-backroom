// Package migration 执行调用方提供的 PostgreSQL Goose 迁移，并只读校验有效版本。
// SQL、迁移许可及数据库生命周期由调用方负责。
package migration

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"sync"

	"github.com/pressly/goose/v3"
)

// goose 使用进程级全局 BaseFS 和 dialect；互斥锁避免并行测试或命令互相覆盖配置。
var gooseConfigMu sync.Mutex

// Up 在传入文件系统中按顺序应用尚未执行的迁移；数据库或迁移失败会原样包装返回。
func Up(ctx context.Context, db *sql.DB, files fs.FS) error {
	return run(func() error {
		return goose.UpContext(ctx, db, ".")
	}, files, "apply migrations")
}

// Down 回滚最近一次迁移；调用方负责决定是否允许该破坏性操作。
func Down(ctx context.Context, db *sql.DB, files fs.FS) error {
	return run(func() error {
		return goose.DownContext(ctx, db, ".")
	}, files, "roll back migration")
}

// Status 输出迁移状态；它与 Up、Down 共用全局 Goose 配置保护。
func Status(ctx context.Context, db *sql.DB, files fs.FS) error {
	return run(func() error {
		return goose.StatusContext(ctx, db, ".")
	}, files, "read migration status")
}

// run 在持锁期间配置本次迁移来源和数据库方言，再执行一个 Goose 操作。
func run(operation func() error, files fs.FS, description string) error {
	gooseConfigMu.Lock()
	defer gooseConfigMu.Unlock()

	goose.SetBaseFS(files)
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("configure migration dialect: %w", err)
	}
	if err := operation(); err != nil {
		return fmt.Errorf("%s: %w", description, err)
	}
	return nil
}
