package seekdb

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"slices"
	"strings"
	"testing"
)

func TestBuiltinMigrationsExposeImmutableOrderedRevisionChain(t *testing.T) {
	first := BuiltinMigrations()
	second := BuiltinMigrations()
	if len(first) != 3 || len(second) != 3 {
		t.Fatalf("builtin migration counts = %d and %d, want 3", len(first), len(second))
	}
	foundation := Revision{Number: foundationSchemaRevision, Checksum: foundationSchemaChecksum()}
	conversation := Revision{Number: conversationSchemaRevision, Checksum: conversationSchemaChecksum()}
	turnEvidence := Revision{Number: turnEvidenceSchemaRevision, Checksum: turnEvidenceSchemaChecksum()}
	if current := CurrentSchemaRevision(); current != turnEvidence {
		t.Fatalf("current schema revision = %#v, want %#v", current, turnEvidence)
	}
	if first[0].Revision != foundation || first[0].Name != "create-foundation-schema" {
		t.Fatalf("foundation migration = %#v, want revision %#v", first[0], foundation)
	}
	if first[1].Revision != conversation || first[1].Name != "create-conversation-schema" {
		t.Fatalf("conversation migration = %#v, want revision %#v", first[1], conversation)
	}
	if first[2].Revision != turnEvidence || first[2].Name != "create-turn-evidence-schema" {
		t.Fatalf("turn evidence migration = %#v, want revision %#v", first[2], turnEvidence)
	}
	for index, migration := range first {
		if migration.Apply == nil || migration.Verify == nil {
			t.Fatalf("builtin migration %d must provide Apply and Verify", index+1)
		}
	}
	first[0].Name = "mutated-by-caller"
	first[0].Revision.Number = 99
	first[1].Name = "also-mutated"
	first[2].Revision.Number = 100
	if second[0].Name != "create-foundation-schema" || second[0].Revision != foundation ||
		second[1].Name != "create-conversation-schema" || second[1].Revision != conversation ||
		second[2].Name != "create-turn-evidence-schema" || second[2].Revision != turnEvidence {
		t.Fatalf("caller mutation changed later BuiltinMigrations result: %#v", second[0])
	}
	if got := hex.EncodeToString(foundation.Checksum[:]); got != "e674bec12d0b6895da8b351d082a686c9c2f44990fb68749bbad083c4a6805d3" {
		t.Fatalf("foundation checksum = %s, update requires an explicit revision decision", got)
	}
	if got := hex.EncodeToString(conversation.Checksum[:]); got != "0ee10d718cb767d6427f2f9fbbfaa69d1c8d0a5b0912b128522d024685c975dc" {
		t.Fatalf("conversation checksum = %s, update requires an explicit revision decision", got)
	}
	if got := hex.EncodeToString(turnEvidence.Checksum[:]); got != "7ef1505fd35fc7d5ee059076f349d4c5e910752d52297dfd1e36122b2803531a" {
		t.Fatalf("turn evidence checksum = %s, update requires an explicit revision decision", got)
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
	assertPortableSchemaTables(t, foundationSchema[:], wantTables)
}

func TestConversationSchemaDefinesIsolationOrderingAndCorrelationContracts(t *testing.T) {
	wantTables := []string{
		"conversations",
		"character_conversations",
		"endpoint_conversations",
		"conversation_turns",
		"conversation_messages",
		"prompt_windows",
	}
	assertPortableSchemaTables(t, conversationSchema[:], wantTables)
	conversations := conversationSchema[0]
	if !schemaHasIndex(conversations, ascendingBTreeIndex(
		"conversations_identity_character_kind_key", true, "id", "character_id", "kind",
	)) {
		t.Fatal("conversations lack immutable kind-aware mapping identity")
	}

	character := conversationSchema[1]
	if !schemaHasIndex(character, ascendingBTreeIndex(
		"character_conversations_conversation_key", true, "conversation_id",
	)) {
		t.Fatal("character conversations lack one-to-one authoritative mapping")
	}
	if len(character.foreignKeys) != 1 || len(character.foreignKeys[0].columns) != 3 ||
		character.foreignKeys[0].columns[0].name != "conversation_id" ||
		character.foreignKeys[0].columns[1].name != "character_id" ||
		character.foreignKeys[0].columns[2].name != "kind" {
		t.Fatalf("character conversation isolation foreign key = %#v", character.foreignKeys)
	}
	if kind := schemaColumnNamed(t, character, "kind"); kind.columnType != "varchar(16)" {
		t.Fatalf("character conversation kind type = %q", kind.columnType)
	}

	turns := conversationSchema[3]
	if !schemaHasIndex(turns, ascendingBTreeIndex(
		"conversation_turns_external_message_idx", false, "conversation_id", "message_id", "sequence",
	)) {
		t.Fatal("conversation turns lack ordered conversation-scoped external message lookup")
	}
	if !schemaHasIndex(turns, ascendingBTreeIndex(
		"conversation_turns_conversation_sequence_key", true, "conversation_id", "sequence",
	)) {
		t.Fatal("conversation turns lack stable conversation sequence")
	}
	if sequence := schemaColumnNamed(t, turns, "sequence"); sequence.columnType != "bigint" {
		t.Fatalf("conversation turn sequence type = %q, want signed bigint", sequence.columnType)
	}

	messages := conversationSchema[4]
	if !schemaHasIndex(messages, ascendingBTreeIndex(
		"conversation_messages_conversation_sequence_key", true, "conversation_id", "sequence",
	)) || !schemaHasIndex(messages, ascendingBTreeIndex(
		"conversation_messages_turn_role_key", true, "turn_id", "role",
	)) {
		t.Fatal("conversation messages lack stable sequence or one-role-per-turn identity")
	}
	if sequence := schemaColumnNamed(t, messages, "sequence"); sequence.columnType != "bigint" {
		t.Fatalf("conversation message sequence type = %q, want signed bigint", sequence.columnType)
	}
	if len(messages.foreignKeys) != 1 || len(messages.foreignKeys[0].columns) != 2 ||
		messages.foreignKeys[0].columns[0].name != "conversation_id" ||
		messages.foreignKeys[0].columns[1].name != "turn_id" {
		t.Fatalf("message/turn isolation foreign key = %#v", messages.foreignKeys)
	}

	endpoint := conversationSchema[2]
	if len(endpoint.foreignKeys) != 1 || len(endpoint.foreignKeys[0].columns) != 3 ||
		endpoint.foreignKeys[0].columns[0].name != "conversation_id" ||
		endpoint.foreignKeys[0].columns[1].name != "character_id" ||
		endpoint.foreignKeys[0].columns[2].name != "kind" {
		t.Fatalf("endpoint/character isolation foreign key = %#v", endpoint.foreignKeys)
	}
	if kind := schemaColumnNamed(t, endpoint, "kind"); kind.columnType != "varchar(16)" {
		t.Fatalf("endpoint conversation kind type = %q", kind.columnType)
	}
}

func TestTurnEvidenceSchemaDefinesInitiationEvidenceContract(t *testing.T) {
	wantTables := []string{"conversation_turn_evidence"}
	assertPortableSchemaTables(t, turnEvidenceSchema[:], wantTables)
	table := turnEvidenceSchema[0]
	if !schemaHasIndex(table, ascendingBTreeIndex(
		"conversation_turn_evidence_evidence_idx", false, "evidence_id", "turn_id",
	)) {
		t.Fatal("turn evidence lacks evidence-to-turn lookup")
	}
	if len(table.foreignKeys) != 1 || table.foreignKeys[0].referencedTable != "conversation_turns" ||
		len(table.foreignKeys[0].columns) != 1 || table.foreignKeys[0].columns[0].name != "turn_id" ||
		table.foreignKeys[0].columns[0].referencedColumn != "id" || table.foreignKeys[0].deleteRule != "cascade" {
		t.Fatalf("turn evidence foreign key = %#v", table.foreignKeys)
	}
	if evidence := schemaColumnNamed(t, table, "evidence_id"); evidence.columnType != "varchar(128)" || evidence.collation != "utf8mb4_bin" {
		t.Fatalf("turn evidence id = %#v", evidence)
	}
}

func schemaColumnNamed(t *testing.T, table schemaTable, name string) schemaColumn {
	t.Helper()
	for _, column := range table.columns {
		if column.name == name {
			return column
		}
	}
	t.Fatalf("table %s lacks column %s", table.name, name)
	return schemaColumn{}
}

func schemaHasIndex(table schemaTable, expected schemaIndex) bool {
	return slices.ContainsFunc(table.indexes, func(actual schemaIndex) bool {
		return actual.name == expected.name &&
			actual.unique == expected.unique &&
			actual.indexType == expected.indexType &&
			slices.Equal(actual.columns, expected.columns)
	})
}

func assertPortableSchemaTables(t *testing.T, tables []schemaTable, wantTables []string) {
	t.Helper()
	if len(tables) != len(wantTables) {
		t.Fatalf("schema table count = %d, want %d", len(tables), len(wantTables))
	}
	for index, table := range tables {
		if table.name != wantTables[index] {
			t.Fatalf("schema table %d = %q, want %q", index, table.name, wantTables[index])
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
			if strings.HasSuffix(column.name, "_id") && column.name != "message_id" && column.name != "evidence_id" && column.collation != "ascii_bin" {
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
