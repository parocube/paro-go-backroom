//go:build integration

package migration_test

import (
	"testing"
	"testing/fstest"

	"github.com/parocube/paro-go-backroom/migration"
	"github.com/parocube/paro-go-backroom/pgtest"
)

func TestMigrationRoundTrip(t *testing.T) {
	// 保护迁移执行器可在独立 schema 中完成 Up、Down、再 Up。
	db, _ := pgtest.NewSchema(t)
	files := fstest.MapFS{
		"00001_test.sql": &fstest.MapFile{Data: []byte(`-- +goose Up
CREATE TABLE migration_probe (id integer PRIMARY KEY);
-- +goose Down
DROP TABLE migration_probe;
`)},
	}

	requireNoError(t, migration.Up(t.Context(), db, files))
	requireNoError(t, migration.Down(t.Context(), db, files))
	requireNoError(t, migration.Up(t.Context(), db, files))
}

func requireNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
