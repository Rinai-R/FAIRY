package database

import "testing"

func schemaTableNames() []string {
	models := schemaModels()
	names := make([]string, 0, len(models))
	for _, model := range models {
		names = append(names, model.(interface{ TableName() string }).TableName())
	}
	return names
}

func TestSchemaModelTableNamesAreUniqueAndExcludeLegacySQLite(t *testing.T) {
	tables := schemaTableNames()
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
