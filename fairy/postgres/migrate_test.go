package postgres

import (
	"errors"
	"testing"
)

func TestSchemaModelsMatchTableNames(t *testing.T) {
	models := schemaModels()
	tables := schemaTableNames()
	if len(models) != len(tables) {
		t.Fatalf("models = %d, tables = %d", len(models), len(tables))
	}
	seen := make(map[string]bool, len(tables))
	for _, table := range tables {
		if table == "" || seen[table] {
			t.Fatalf("invalid schema table name %q", table)
		}
		seen[table] = true
	}
	if seen["sqlite_import_runs"] || seen["sqlite_import_checkpoints"] {
		t.Fatal("SQLite importer tables must not be part of the current schema")
	}
}

func TestQuoteIdentifier(t *testing.T) {
	if got := quoteIdentifier(`a"b`); got != `"a""b"` {
		t.Fatalf("quoteIdentifier() = %q", got)
	}
}

func TestSchemaErrorSentinels(t *testing.T) {
	if !errors.Is(ErrSchemaAbsent, ErrSchemaAbsent) || !errors.Is(ErrSchemaNotCurrent, ErrSchemaNotCurrent) {
		t.Fatal("schema sentinels should compare with themselves")
	}
}
