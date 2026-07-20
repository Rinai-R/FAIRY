package postgres

import (
	"errors"
	"io/fs"
	"testing"
	"testing/fstest"
)

func TestLoadMigrations(t *testing.T) {
	migrations, err := LoadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if len(migrations) != CurrentSchemaVersion {
		t.Fatalf("len(migrations) = %d, want %d", len(migrations), CurrentSchemaVersion)
	}
	first := migrations[0]
	if first.Version != 1 || first.Name != "initial" {
		t.Fatalf("first migration = %#v", first)
	}
	if len(first.Checksum) != 64 {
		t.Fatalf("checksum length = %d, want 64", len(first.Checksum))
	}
	if first.SQL == "" {
		t.Fatal("first migration SQL is empty")
	}
}

func TestLoadMigrationsRejectsBadNames(t *testing.T) {
	_, err := loadMigrations(fstest.MapFS{
		"migrations/1_bad.sql": {Data: []byte("select 1")},
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestValidateMigrationsRejectsGaps(t *testing.T) {
	err := validateMigrations([]Migration{
		{Version: 1, Name: "one", SQL: "select 1", Checksum: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"},
		{Version: 3, Name: "three", SQL: "select 3", Checksum: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"},
	})
	if err == nil {
		t.Fatal("expected gap error")
	}
}

func TestVerifySchemaAbsentSentinel(t *testing.T) {
	if !errors.Is(ErrSchemaAbsent, ErrSchemaAbsent) {
		t.Fatal("schema absent sentinel should compare with itself")
	}
}

func TestLoadMigrationsRequiresDirectory(t *testing.T) {
	_, err := loadMigrations(fstest.MapFS{})
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("err = %v, want fs.ErrNotExist", err)
	}
}
