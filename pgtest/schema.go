// Package pgtest 为真实 PostgreSQL 测试提供独立 schema 和有界清理。
package pgtest

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"regexp"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// schemaNamePattern 只接受本工具生成的随机 schema，避免清理逻辑误删测试外的数据。
var schemaNamePattern = regexp.MustCompile(`^test_[0-9a-f]{32}$`)

// NewSchema 为单个测试创建随机 schema，并返回 search_path 已限定到该 schema 的连接。
// 清理时必须先关闭 scopedDB，随后删除 schema，最后关闭管理员连接，避免仍被使用的连接阻止删除。
func NewSchema(t *testing.T) (*sql.DB, string) {
	t.Helper()

	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Fatal("TEST_DATABASE_URL is required")
	}

	adminConfig, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		t.Fatalf("parse TEST_DATABASE_URL: %v", err)
	}
	adminDB := stdlib.OpenDB(*adminConfig)
	if err := adminDB.PingContext(t.Context()); err != nil {
		_ = adminDB.Close()
		t.Fatalf("connect to test PostgreSQL: %v", err)
	}

	schemaName := randomSchemaName(t)
	if _, err := adminDB.ExecContext(t.Context(), "CREATE SCHEMA "+quoteSchemaName(schemaName)); err != nil {
		_ = adminDB.Close()
		t.Fatalf("create test schema: %v", err)
	}

	// search_path 让迁移和 SQL 无需拼接 schema 名，且每个测试只会读写自己的隔离空间。
	scopedConfig := adminConfig.Copy()
	scopedConfig.RuntimeParams["search_path"] = schemaName
	scopedDB := stdlib.OpenDB(*scopedConfig)
	if err := scopedDB.PingContext(t.Context()); err != nil {
		_ = scopedDB.Close()
		_, _ = adminDB.ExecContext(t.Context(), "DROP SCHEMA "+quoteSchemaName(schemaName)+" CASCADE")
		_ = adminDB.Close()
		t.Fatalf("connect to isolated test schema: %v", err)
	}

	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := scopedDB.Close(); err != nil {
			t.Errorf("close isolated database: %v", err)
		}
		if _, err := adminDB.ExecContext(cleanupCtx, "DROP SCHEMA "+quoteSchemaName(schemaName)+" CASCADE"); err != nil {
			t.Errorf("drop test schema: %v", err)
		}
		if err := adminDB.Close(); err != nil {
			t.Errorf("close administrator database: %v", err)
		}
	})

	return scopedDB, schemaName
}

func randomSchemaName(t *testing.T) string {
	t.Helper()
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		t.Fatalf("generate test schema name: %v", err)
	}
	return "test_" + hex.EncodeToString(bytes)
}

// quoteSchemaName 再次校验随机名称，确保动态拼接的 DDL 目标始终受测试工具控制。
func quoteSchemaName(name string) string {
	if !schemaNamePattern.MatchString(name) {
		panic(fmt.Sprintf("invalid generated schema name %q", name))
	}
	return `"` + name + `"`
}
