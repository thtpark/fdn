package database

import (
	"context"
	"path/filepath"
	"testing"
)

func TestMigrationsPersistAndRejectFuture(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "private", "db.sqlite")
	migrations := []string{"CREATE TABLE example(id INTEGER PRIMARY KEY);"}
	db, err := Open(ctx, path, migrations, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO example VALUES(42)"); err != nil {
		t.Fatal(err)
	}
	if err := Check(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(ctx, path, migrations, 1)
	if err != nil {
		t.Fatal(err)
	}
	var value int
	if err := db.QueryRow("SELECT id FROM example").Scan(&value); err != nil || value != 42 {
		t.Fatal(value, err)
	}
	if _, err := db.Exec("PRAGMA user_version=99"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err := Open(ctx, path, migrations, 1); err == nil {
		t.Fatal("future schema accepted")
	}
}
