package seekdb

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"slices"
	"strings"
	"testing"
)

func TestBuiltinMigrationsExposeImmutableFoundationRevision(t *testing.T) {
	first := BuiltinMigrations()
	second := BuiltinMigrations()
	if len(first) != 1 || len(second) != 1 {
		t.Fatalf("builtin migration counts = %d and %d, want 1", len(first), len(second))
	}
	want := CurrentSchemaRevision()
	if first[0].Revision != want || first[0].Name != "create-foundation-schema" {
		t.Fatalf("builtin migration = %#v, want revision %#v", first[0], want)
	}
	if first[0].Apply == nil || first[0].Verify == nil {
		t.Fatal("builtin migration must provide Apply and Verify")
	}
	first[0].Name = "mutated-by-caller"
	first[0].Revision.Number = 99
	if second[0].Name != "create-foundation-schema" || second[0].Revision != want {
		t.Fatalf("caller mutation changed later BuiltinMigrations result: %#v", second[0])
	}
	if got := hex.EncodeToString(want.Checksum[:]); got != "e674bec12d0b6895da8b351d082a686c9c2f44990fb68749bbad083c4a6805d3" {
		t.Fatalf("foundation checksum = %s, update requires an explicit revision decision", got)
	}
}

func TestFoundationSchemaContainsOnlyDeclaredTablesAndPortableSeekDBDDL(t *testing.T) {
	wantTables := []string{
		"config_documents",
		"secret_values",
		"owner_identities",
		"characters",
		"plugin_packages",
		"plugin_instances",
	}
	if len(foundationSchema) != len(wantTables) {
		t.Fatalf("foundation table count = %d, want %d", len(foundationSchema), len(wantTables))
	}
	for index, table := range foundationSchema {
		if table.name != wantTables[index] {
			t.Fatalf("foundation table %d = %q, want %q", index, table.name, wantTables[index])
		}
		upperDDL := strings.ToUpper(table.ddl)
		if !strings.HasPrefix(upperDDL, "CREATE TABLE IF NOT EXISTS "+strings.ToUpper(table.name)+" ") {
			t.Errorf("table %s DDL is not retry-safe CREATE TABLE IF NOT EXISTS", table.name)
		}
		for _, forbidden := range []string{"POSTGRES", "PGVECTOR", "QDRANT", "SQLITE", "GORM", "$1", "JSONB", "BYTEA"} {
			if strings.Contains(upperDDL, forbidden) {
				t.Errorf("table %s DDL contains forbidden token %q", table.name, forbidden)
			}
		}
		if len(table.columns) == 0 || len(table.indexes) == 0 || len(table.checks) != 1 {
			t.Errorf("table %s verification contract is incomplete", table.name)
		}
		if !strings.Contains(table.ddl, "CONSTRAINT "+table.checks[0].name+" CHECK") {
			t.Errorf("table %s is missing named CHECK %s", table.name, table.checks[0].name)
		}
		for _, column := range table.columns {
			if strings.HasSuffix(column.name, "_id") && column.collation != "ascii_bin" {
				t.Errorf("table %s identifier %s collation = %q, want ascii_bin", table.name, column.name, column.collation)
			}
		}
	}
}

func TestSchemaDDLChecksumNormalizesWhitespaceButNotTokens(t *testing.T) {
	compact := []string{"CREATE TABLE x (id BIGINT NOT NULL)", "CREATE TABLE y (body JSON NOT NULL)"}
	spaced := []string{"\n CREATE   TABLE x\n(id BIGINT   NOT NULL) \n", "CREATE TABLE y (body JSON NOT NULL)"}
	if schemaDDLChecksum(compact) != schemaDDLChecksum(spaced) {
		t.Fatal("schema checksum changed for whitespace-only variation")
	}
	changed := slices.Clone(compact)
	changed[1] = "CREATE TABLE y (body LONGBLOB NOT NULL)"
	if schemaDDLChecksum(compact) == schemaDDLChecksum(changed) {
		t.Fatal("schema checksum did not change with DDL tokens")
	}
	literalWhitespace := []string{`CREATE TABLE x (label VARCHAR(16) CHECK (label = 'a  b'))`}
	literalCollapsed := []string{`CREATE TABLE x (label VARCHAR(16) CHECK (label = 'a b'))`}
	if schemaDDLChecksum(literalWhitespace) == schemaDDLChecksum(literalCollapsed) {
		t.Fatal("schema checksum collapsed significant whitespace inside a string literal")
	}
	escapedLiteral := `CREATE TABLE x (label VARCHAR(16) CHECK (label = 'a''  b'))`
	if normalizeDDL(escapedLiteral) != escapedLiteral {
		t.Fatalf("normalizeDDL changed an escaped string literal: %q", normalizeDDL(escapedLiteral))
	}
	if CurrentSchemaRevision().Checksum == ([sha256.Size]byte{}) {
		t.Fatal("current schema checksum is empty")
	}
}

func TestSchemaContractComparisonRejectsShapeDrift(t *testing.T) {
	wantColumns := []schemaColumn{{name: "id", columnType: "varchar(32)", collation: "ascii_bin"}}
	if err := compareSchemaColumns(wantColumns, slices.Clone(wantColumns)); err != nil {
		t.Fatalf("compareSchemaColumns(equal) error = %v", err)
	}
	wrongColumns := slices.Clone(wantColumns)
	wrongColumns[0].nullable = true
	if err := compareSchemaColumns(wantColumns, wrongColumns); err == nil {
		t.Fatal("compareSchemaColumns accepted nullable drift")
	}
	wrongColumns = slices.Clone(wantColumns)
	wrongColumns[0].defaultValue = sql.NullString{String: "generated-by-drift", Valid: true}
	if err := compareSchemaColumns(wantColumns, wrongColumns); err == nil {
		t.Fatal("compareSchemaColumns accepted default drift")
	}
	wrongColumns = slices.Clone(wantColumns)
	wrongColumns[0].extra = "default_generated"
	if err := compareSchemaColumns(wantColumns, wrongColumns); err == nil {
		t.Fatal("compareSchemaColumns accepted extra drift")
	}
	wrongColumns = slices.Clone(wantColumns)
	wrongColumns[0].generationExpression = "id + 1"
	if err := compareSchemaColumns(wantColumns, wrongColumns); err == nil {
		t.Fatal("compareSchemaColumns accepted generated-column drift")
	}

	wantIndexes := []schemaIndex{
		ascendingBTreeIndex("PRIMARY", true, "id"),
		ascendingBTreeIndex("x_scope_idx", false, "scope", "updated_at_ms"),
	}
	actualIndexes := []schemaIndex{wantIndexes[0], wantIndexes[1]}
	if err := compareSchemaIndexes(wantIndexes, actualIndexes); err != nil {
		t.Fatalf("compareSchemaIndexes(equal) error = %v", err)
	}
	wrongIndexes := []schemaIndex{wantIndexes[0], ascendingBTreeIndex("x_scope_idx", false, "updated_at_ms", "scope")}
	if err := compareSchemaIndexes(wantIndexes, wrongIndexes); err == nil {
		t.Fatal("compareSchemaIndexes accepted column-order drift")
	}
	wrongIndexes = slices.Clone(wantIndexes)
	wrongIndexes[1].indexType = "hash"
	if err := compareSchemaIndexes(wantIndexes, wrongIndexes); err == nil {
		t.Fatal("compareSchemaIndexes accepted index-type drift")
	}
	wrongIndexes = slices.Clone(wantIndexes)
	wrongIndexes[1].columns = slices.Clone(wrongIndexes[1].columns)
	wrongIndexes[1].columns[0].subPart = sql.NullInt64{Int64: 8, Valid: true}
	if err := compareSchemaIndexes(wantIndexes, wrongIndexes); err == nil {
		t.Fatal("compareSchemaIndexes accepted prefix-length drift")
	}
	wrongIndexes = slices.Clone(wantIndexes)
	wrongIndexes[1].columns = slices.Clone(wrongIndexes[1].columns)
	wrongIndexes[1].columns[0].collation = sql.NullString{String: "d", Valid: true}
	if err := compareSchemaIndexes(wantIndexes, wrongIndexes); err == nil {
		t.Fatal("compareSchemaIndexes accepted index-collation drift")
	}

	wantChecks := []schemaCheck{{name: "x_check", clause: "(`id` > 0)"}}
	if err := compareSchemaChecks(wantChecks, slices.Clone(wantChecks)); err != nil {
		t.Fatalf("compareSchemaChecks(equal) error = %v", err)
	}
	if err := compareSchemaChecks(wantChecks, []schemaCheck{{name: "x_check", clause: "(`id` >= 0)"}}); err == nil {
		t.Fatal("compareSchemaChecks accepted a weaker same-name clause")
	}

	wantForeignKeys := []schemaForeignKey{{
		name:            "x_parent_fk",
		referencedTable: "parents",
		updateRule:      "restrict",
		deleteRule:      "restrict",
		columns: []schemaForeignKeyColumn{
			{name: "parent_id", referencedColumn: "id", sameSchema: true},
		},
	}}
	if err := compareSchemaForeignKeys(wantForeignKeys, slices.Clone(wantForeignKeys)); err != nil {
		t.Fatalf("compareSchemaForeignKeys(equal) error = %v", err)
	}
	wrongForeignKeys := slices.Clone(wantForeignKeys)
	wrongForeignKeys[0].deleteRule = "cascade"
	if err := compareSchemaForeignKeys(wantForeignKeys, wrongForeignKeys); err == nil {
		t.Fatal("compareSchemaForeignKeys accepted delete-rule drift")
	}
}
