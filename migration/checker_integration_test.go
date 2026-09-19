//go:build integration

package migration_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/parocube/paro-go-backroom/migration"
	"github.com/parocube/paro-go-backroom/pgtest"
)

// TestCheckerRequiresExactEffectiveMigrations 使用真实迁移和只读连接，保护缺失、超前、回滚和历史记录规则。
func TestCheckerRequiresExactEffectiveMigrations(t *testing.T) {
	files := fstest.MapFS{
		"00001_first.sql": {Data: []byte("-- +goose Up\nCREATE TABLE first_probe (id integer);\n-- +goose Down\nDROP TABLE first_probe;\n")},
		"00003_third.sql": {Data: []byte("-- +goose Up\nCREATE TABLE third_probe (id integer);\n-- +goose Down\nDROP TABLE third_probe;\n")},
	}
	tests := []struct {
		name         string
		prepare      func(*testing.T, *sql.DB)
		wantMismatch bool
	}{
		{name: "missing version table", wantMismatch: true},
		{name: "outdated", prepare: func(t *testing.T, db *sql.DB) {
			requireNoError(t, migration.Up(t.Context(), db, fstest.MapFS{"00001_first.sql": files["00001_first.sql"]}))
		}, wantMismatch: true},
		{name: "matching nonconsecutive versions", prepare: func(t *testing.T, db *sql.DB) {
			requireNoError(t, migration.Up(t.Context(), db, files))
		}},
		{name: "missing older version with current maximum", prepare: func(t *testing.T, db *sql.DB) {
			requireNoError(t, migration.Up(t.Context(), db, files))
			_, err := db.ExecContext(t.Context(), `DELETE FROM goose_db_version WHERE version_id = 1`)
			requireNoError(t, err)
		}, wantMismatch: true},
		{name: "unsupported applied future version", prepare: func(t *testing.T, db *sql.DB) {
			requireNoError(t, migration.Up(t.Context(), db, files))
			requireNoError(t, migration.Up(t.Context(), db, fstest.MapFS{
				"00004_future.sql": {Data: []byte("-- +goose Up\nCREATE TABLE future_probe (id integer);\n-- +goose Down\nDROP TABLE future_probe;\n")},
			}))
		}, wantMismatch: true},
		{name: "rolled back latest version", prepare: func(t *testing.T, db *sql.DB) {
			requireNoError(t, migration.Up(t.Context(), db, files))
			requireNoError(t, migration.Down(t.Context(), db, files))
		}, wantMismatch: true},
		{name: "up down up", prepare: func(t *testing.T, db *sql.DB) {
			requireNoError(t, migration.Up(t.Context(), db, files))
			requireNoError(t, migration.Down(t.Context(), db, files))
			requireNoError(t, migration.Up(t.Context(), db, files))
		}},
		{name: "historical rollback is effective", prepare: func(t *testing.T, db *sql.DB) {
			requireNoError(t, migration.Up(t.Context(), db, files))
			_, err := db.ExecContext(t.Context(), `INSERT INTO goose_db_version (version_id, is_applied) VALUES (1, false)`)
			requireNoError(t, err)
		}, wantMismatch: true},
		{name: "historical reapply is effective", prepare: func(t *testing.T, db *sql.DB) {
			requireNoError(t, migration.Up(t.Context(), db, files))
			_, err := db.ExecContext(t.Context(), `INSERT INTO goose_db_version (version_id, is_applied) VALUES (1, false), (1, true), (4, true), (4, false)`)
			requireNoError(t, err)
		}},
		{name: "missing baseline", prepare: func(t *testing.T, db *sql.DB) {
			requireNoError(t, migration.Up(t.Context(), db, files))
			_, err := db.ExecContext(t.Context(), `DELETE FROM goose_db_version WHERE version_id = 0`)
			requireNoError(t, err)
		}, wantMismatch: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db, schemaName := pgtest.NewSchema(t)
			if test.prepare != nil {
				test.prepare(t, db)
			}
			before := migrationHistory(t, db)
			pool := readOnlySchemaPool(t, schemaName)
			checker, err := migration.NewChecker(pool, files, time.Second)
			requireNoError(t, err)
			for range 2 {
				err := checker.Ping(t.Context())
				if test.wantMismatch && !errors.Is(err, migration.ErrSchemaMismatch) {
					t.Fatalf("Ping() = %v, want schema mismatch", err)
				}
				if !test.wantMismatch && err != nil {
					t.Fatalf("Ping() = %v, want compatible schema", err)
				}
			}
			if after := migrationHistory(t, db); after != before {
				t.Fatal("schema checker changed migration history")
			}
		})
	}
}

// TestCheckerHonorsCancellationAndTimeout 用真实表锁阻塞查询，验证就绪检查的时间上限和上游取消不会丢失。
func TestCheckerHonorsCancellationAndTimeout(t *testing.T) {
	db, schemaName := pgtest.NewSchema(t)
	files := fstest.MapFS{
		"00001_probe.sql": {Data: []byte("-- +goose Up\nCREATE TABLE probe (id integer);\n-- +goose Down\nDROP TABLE probe;\n")},
	}
	requireNoError(t, migration.Up(t.Context(), db, files))
	checker, err := migration.NewChecker(readOnlySchemaPool(t, schemaName), files, 100*time.Millisecond)
	requireNoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := checker.Ping(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Ping() = %v", err)
	}

	tx, err := db.BeginTx(t.Context(), nil)
	requireNoError(t, err)
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(t.Context(), `LOCK TABLE goose_db_version IN ACCESS EXCLUSIVE MODE`)
	requireNoError(t, err)
	started := time.Now()
	if err := checker.Ping(t.Context()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("blocked Ping() = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("blocked Ping() took %s, want bounded timeout", elapsed)
	}
	requireNoError(t, tx.Rollback())
	requireNoError(t, checker.Ping(t.Context()))
}

// readOnlySchemaPool 为每次检查强制只读事务；意外调用建表或迁移 API 会直接失败。
func readOnlySchemaPool(t *testing.T, schemaName string) *pgxpool.Pool {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(os.Getenv("TEST_DATABASE_URL"))
	requireNoError(t, err)
	cfg.ConnConfig.RuntimeParams["search_path"] = schemaName
	cfg.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	requireNoError(t, err)
	t.Cleanup(pool.Close)
	return pool
}

// migrationHistory 记录版本表存在性与完整历史，确保失败和成功检查都不会修补数据库。
func migrationHistory(t *testing.T, db *sql.DB) string {
	t.Helper()
	var exists bool
	requireNoError(t, db.QueryRowContext(t.Context(), `SELECT to_regclass('goose_db_version') IS NOT NULL`).Scan(&exists))
	if !exists {
		return "missing"
	}
	var history string
	requireNoError(t, db.QueryRowContext(t.Context(), `SELECT COALESCE(json_agg(row_to_json(history) ORDER BY id)::text, '[]') FROM goose_db_version AS history`).Scan(&history))
	return history
}
