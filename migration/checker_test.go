package migration

import (
	"context"
	"errors"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestNewCheckerRejectsInvalidMigrationSources 保护版本来源必须可解析且唯一；空包和错误文件名不能静默通过。
func TestNewCheckerRejectsInvalidMigrationSources(t *testing.T) {
	validFiles := fstest.MapFS{"00001_probe.sql": {Data: []byte("-- +goose Up\nSELECT 1;\n")}}
	for _, test := range []struct {
		name    string
		pool    *pgxpool.Pool
		files   fs.FS
		timeout time.Duration
	}{
		{name: "missing pool", files: validFiles, timeout: time.Second},
		{name: "missing filesystem", pool: &pgxpool.Pool{}, timeout: time.Second},
		{name: "missing timeout", pool: &pgxpool.Pool{}, files: validFiles},
		{name: "empty filesystem", pool: &pgxpool.Pool{}, files: fstest.MapFS{}, timeout: time.Second},
		{name: "invalid filename", pool: &pgxpool.Pool{}, files: fstest.MapFS{"invalid.sql": {}}, timeout: time.Second},
		{name: "zero version", pool: &pgxpool.Pool{}, files: fstest.MapFS{"00000_zero.sql": {}}, timeout: time.Second},
		{name: "duplicate version", pool: &pgxpool.Pool{}, files: fstest.MapFS{"00001_first.sql": {}, "1_other.sql": {}}, timeout: time.Second},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewChecker(test.pool, test.files, test.timeout); err == nil {
				t.Fatal("NewChecker() accepted invalid migration configuration")
			}
		})
	}
}

// TestSchemaReadErrorKeepsCausesWithoutExposingDetails 保护日志安全，同时让调用方可靠识别数据库与取消错误。
func TestSchemaReadErrorKeepsCausesWithoutExposingDetails(t *testing.T) {
	postgresError := &pgconn.PgError{Code: "42501", Message: "private database detail"}
	err := schemaReadError(postgresError)
	if strings.Contains(err.Error(), postgresError.Message) || !errors.Is(err, postgresError) {
		t.Fatal("schema error exposed database details or lost its cause")
	}
	if !errors.Is(schemaReadError(context.Canceled), context.Canceled) || !errors.Is(schemaReadError(context.DeadlineExceeded), context.DeadlineExceeded) {
		t.Fatal("schema error lost cancellation or deadline identity")
	}
	missingTable := schemaReadError(&pgconn.PgError{Code: "42P01", Message: "private relation detail"})
	if !errors.Is(missingTable, ErrSchemaMismatch) || strings.Contains(missingTable.Error(), "private") {
		t.Fatal("missing history table was not safely classified")
	}
}
