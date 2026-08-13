//go:build integration

package seekdb

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
)

func TestRealSeekDBFoundationSchemaRecoversPartialAndPersists(t *testing.T) {
	instance, config := openSchemaMigrationRuntime(t)
	closed := false
	defer func() {
		if !closed {
			closeRuntimeForIntegrationTest(t, instance, config.ShutdownLimit)
		}
	}()
	database := instance.SQL()

	// Simulate a process exit after the first implicitly committed DDL but
	// before the revision journal was committed. The builtin migration must
	// accept that exact table and create the remaining five.
	if _, err := database.ExecContext(t.Context(), foundationSchema[0].ddl); err != nil {
		t.Fatalf("precreate partial foundation schema: %v", err)
	}
	if got := integrationTableCount(t, database, foundationSchema[0].name); got != 1 {
		t.Fatalf("precreated table count = %d, want 1", got)
	}

	if err := MigrateSchema(t.Context(), database, BuiltinMigrations()); err != nil {
		t.Fatalf("MigrateSchema(partial foundation) error = %v", err)
	}
	assertCurrentSchema(t, database, CurrentSchemaRevision())
	assertFoundationSchemaVerified(t, database)
	assertConversationSchemaVerified(t, database)
	assertFoundationTableSet(t, database)
	if got := integrationJournalRowForRevision(t, database, foundationSchemaRevision).AttemptCount; got != 1 {
		t.Fatalf("foundation attempt count = %d, want 1", got)
	}
	if got := integrationJournalRowForRevision(t, database, conversationSchemaRevision).AttemptCount; got != 1 {
		t.Fatalf("conversation attempt count = %d, want 1", got)
	}

	if err := MigrateSchema(t.Context(), database, BuiltinMigrations()); err != nil {
		t.Fatalf("idempotent MigrateSchema(foundation) error = %v", err)
	}
	if got := integrationJournalRowForRevision(t, database, foundationSchemaRevision).AttemptCount; got != 1 {
		t.Fatalf("foundation attempt count after repeat = %d, want 1", got)
	}
	if got := integrationJournalRowForRevision(t, database, conversationSchemaRevision).AttemptCount; got != 1 {
		t.Fatalf("conversation attempt count after repeat = %d, want 1", got)
	}
	assertFoundationChecksRejectInvalidData(t, database)

	closeRuntimeForIntegrationTest(t, instance, config.ShutdownLimit)
	closed = true

	restarted, err := Open(t.Context(), config)
	if err != nil {
		t.Fatalf("restart SeekDB foundation runtime: %v", err)
	}
	instance = restarted
	closed = false
	assertCurrentSchema(t, restarted.SQL(), CurrentSchemaRevision())
	assertFoundationSchemaVerified(t, restarted.SQL())
	assertConversationSchemaVerified(t, restarted.SQL())
	if got := integrationJournalRowForRevision(t, restarted.SQL(), foundationSchemaRevision).AttemptCount; got != 1 {
		t.Fatalf("foundation attempt count after restart = %d, want 1", got)
	}
	if got := integrationJournalRowForRevision(t, restarted.SQL(), conversationSchemaRevision).AttemptCount; got != 1 {
		t.Fatalf("conversation attempt count after restart = %d, want 1", got)
	}
}

func TestRealSeekDBConversationSchemaUpgradesRevisionOneWithoutRewritingIt(t *testing.T) {
	instance, config := openSchemaMigrationRuntime(t)
	defer closeRuntimeForIntegrationTest(t, instance, config.ShutdownLimit)
	database := instance.SQL()
	migrations := BuiltinMigrations()

	if err := MigrateSchema(t.Context(), database, migrations[:1]); err != nil {
		t.Fatalf("MigrateSchema(revision one) error = %v", err)
	}
	foundationRevision := migrations[0].Revision
	assertCurrentSchema(t, database, foundationRevision)
	if _, err := database.ExecContext(t.Context(), `
INSERT INTO config_documents(namespace, document_key, schema_version, revision, document, created_at_ms, updated_at_ms)
VALUES (?, ?, ?, ?, ?, ?, ?)`, "runtime", "upgrade-proof", 1, 1, `{"kept":true}`, 1, 1); err != nil {
		t.Fatalf("insert revision-one data: %v", err)
	}

	if err := MigrateSchema(t.Context(), database, migrations); err != nil {
		t.Fatalf("MigrateSchema(revision two) error = %v", err)
	}
	assertCurrentSchema(t, database, CurrentSchemaRevision())
	assertFoundationSchemaVerified(t, database)
	assertConversationSchemaVerified(t, database)
	for _, revision := range []int64{foundationSchemaRevision, conversationSchemaRevision} {
		row := integrationJournalRowForRevision(t, database, revision)
		if row.State != string(MigrationCurrent) || row.AttemptCount != 1 {
			t.Fatalf("revision %d journal = %#v", revision, row)
		}
	}
	var document string
	if err := database.QueryRowContext(t.Context(), `
SELECT document FROM config_documents WHERE namespace = ? AND document_key = ?`,
		"runtime", "upgrade-proof",
	).Scan(&document); err != nil {
		t.Fatalf("read revision-one data after upgrade: %v", err)
	}
	if document != `{"kept": true}` && document != `{"kept":true}` {
		t.Fatalf("revision-one document after upgrade = %q", document)
	}
}

func TestRealSeekDBConversationSchemaRecoversPartialAndRemainsIdempotent(t *testing.T) {
	instance, config := openSchemaMigrationRuntime(t)
	defer closeRuntimeForIntegrationTest(t, instance, config.ShutdownLimit)
	database := instance.SQL()
	migrations := BuiltinMigrations()

	if err := MigrateSchema(t.Context(), database, migrations[:1]); err != nil {
		t.Fatalf("MigrateSchema(revision one) error = %v", err)
	}
	if _, err := database.ExecContext(t.Context(), conversationSchema[0].ddl); err != nil {
		t.Fatalf("precreate partial conversation schema: %v", err)
	}
	if err := MigrateSchema(t.Context(), database, migrations); err != nil {
		t.Fatalf("MigrateSchema(partial revision two) error = %v", err)
	}
	assertCurrentSchema(t, database, CurrentSchemaRevision())
	assertConversationSchemaVerified(t, database)
	if got := integrationJournalRowForRevision(t, database, conversationSchemaRevision).AttemptCount; got != 1 {
		t.Fatalf("conversation attempt count = %d, want 1", got)
	}
	if err := MigrateSchema(t.Context(), database, migrations); err != nil {
		t.Fatalf("MigrateSchema(repeated revision two) error = %v", err)
	}
	if got := integrationJournalRowForRevision(t, database, conversationSchemaRevision).AttemptCount; got != 1 {
		t.Fatalf("conversation attempt count after repeat = %d, want 1", got)
	}
}

func TestRealSeekDBConversationSchemaEnforcesIsolationOrderingAndExternalMessageIdentity(t *testing.T) {
	instance, config := openSchemaMigrationRuntime(t)
	defer closeRuntimeForIntegrationTest(t, instance, config.ShutdownLimit)
	database := instance.SQL()
	if err := MigrateSchema(t.Context(), database, BuiltinMigrations()); err != nil {
		t.Fatalf("MigrateSchema() error = %v", err)
	}

	insertIntegrationConversation(t, database, "conversation-a", "character-a", "endpoint")
	insertIntegrationConversation(t, database, "conversation-b", "character-b", "endpoint")
	insertIntegrationConversation(t, database, "conversation-default-a", "character-a", "character")
	insertIntegrationConversation(t, database, "conversation-default-a-second", "character-a", "character")
	insertIntegrationPromptWindow(t, database, "conversation-a")
	groupDigest := sha256.Sum256([]byte("onebot group 100"))
	privateDigest := sha256.Sum256([]byte("onebot user 200"))
	principalDigest := sha256.Sum256([]byte("principal user 200"))

	insertIntegrationCharacterConversation(t, database, "character-a", "conversation-default-a")
	expectSeekDBConstraintError(t, database, `
INSERT INTO character_conversations(character_id, conversation_id, kind)
VALUES (?, ?, ?)`, "character-b", "conversation-default-a", "character")
	expectSeekDBConstraintError(t, database, `
INSERT INTO character_conversations(character_id, conversation_id, kind)
VALUES (?, ?, ?)`, "character-a", "conversation-default-a-second", "character")
	expectSeekDBConstraintError(t, database, `
INSERT INTO character_conversations(character_id, conversation_id, kind)
VALUES (?, ?, ?)`, "character-b", "conversation-b", "character")

	expectSeekDBConstraintError(t, database, `
INSERT INTO endpoint_conversations(
  character_id, endpoint, endpoint_key_digest, conversation_id, kind, audience, initiation, presentation,
  evaluation, principal_namespace, principal_digest, created_at_ms, updated_at_ms
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"character-b", "desktop", sha256Bytes("mismatched character"), "conversation-a",
		"endpoint", "single", "direct", "embodied", 0, nil, nil, 1, 1,
	)
	expectSeekDBConstraintError(t, database, `
INSERT INTO endpoint_conversations(
  character_id, endpoint, endpoint_key_digest, conversation_id, kind, audience, initiation, presentation,
  evaluation, principal_namespace, principal_digest, created_at_ms, updated_at_ms
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"character-a", "im", sha256Bytes("single without principal"), "conversation-a",
		"endpoint", "single", "direct", "chat", 0, nil, nil, 1, 1,
	)
	expectSeekDBConstraintError(t, database, `
INSERT INTO endpoint_conversations(
  character_id, endpoint, endpoint_key_digest, conversation_id, kind, audience, initiation, presentation,
  evaluation, principal_namespace, principal_digest, created_at_ms, updated_at_ms
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"character-b", "im", sha256Bytes("group with principal"), "conversation-b",
		"endpoint", "multi", "ambient", "chat", 0, "qq.onebot", principalDigest[:], 1, 1,
	)
	expectSeekDBConstraintError(t, database, `
INSERT INTO endpoint_conversations(
  character_id, endpoint, endpoint_key_digest, conversation_id, kind, audience, initiation, presentation,
  evaluation, principal_namespace, principal_digest, created_at_ms, updated_at_ms
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"character-a", "desktop", sha256Bytes("default conversation misclassified as endpoint"), "conversation-default-a",
		"endpoint", "single", "direct", "embodied", 0, nil, nil, 1, 1,
	)
	insertIntegrationEndpoint(t, database, "character-a", "im", groupDigest[:], "conversation-a", "multi", "ambient", "chat", 0, nil, nil)
	insertIntegrationEndpoint(t, database, "character-b", "im", privateDigest[:], "conversation-b", "single", "direct", "chat", 0, "qq.onebot", principalDigest[:])

	insertIntegrationTurn(t, database, "turn-a-1", "conversation-a", "external-message-42", 1)
	insertIntegrationTurn(t, database, "turn-a-2", "conversation-a", "external-message-43", 2)
	insertIntegrationTurn(t, database, "turn-b-1", "conversation-b", "external-message-42", 1)
	unicodeMessageID := strings.Repeat("界", 128)
	insertIntegrationTurn(t, database, "turn-b-2", "conversation-b", unicodeMessageID, 2)
	var storedUnicodeMessageID string
	if err := database.QueryRowContext(t.Context(), `
SELECT message_id FROM conversation_turns
WHERE conversation_id = ? AND message_id = ?`, "conversation-b", unicodeMessageID).Scan(&storedUnicodeMessageID); err != nil {
		t.Fatalf("read exact Unicode external message ID: %v", err)
	}
	if storedUnicodeMessageID != unicodeMessageID {
		t.Fatalf("Unicode external message ID was truncated: got %d runes", len([]rune(storedUnicodeMessageID)))
	}
	expectSeekDBConstraintError(t, database, integrationTurnInsertSQL,
		"turn-b-message-too-long", "conversation-b", strings.Repeat("界", 129), 3,
		"interpreting", "user", nil, nil, nil, "ineligible", nil, nil, nil, 0, 0, nil, nil, 2, 2,
	)
	for index, controlMessageID := range []string{"message\n42", "message\x7f42", "message\u008542"} {
		expectSeekDBConstraintError(t, database, integrationTurnInsertSQL,
			fmt.Sprintf("turn-b-control-message-%d", index), "conversation-b", controlMessageID, int64(10+index),
			"interpreting", "user", nil, nil, nil, "ineligible", nil, nil, nil, 0, 0, nil, nil, 2, 2,
		)
	}
	// Ambient de-duplication is a bounded process-local window. Once that
	// window expires, the same external message may be admitted again and the
	// durable association must retain both turns in sequence order.
	insertIntegrationTurn(t, database, "turn-a-reaccepted-external", "conversation-a", "external-message-42", 3)
	var externalAssociations int
	if err := database.QueryRowContext(t.Context(), `
SELECT COUNT(*) FROM conversation_turns
WHERE conversation_id = ? AND message_id = ?`, "conversation-a", "external-message-42").Scan(&externalAssociations); err != nil {
		t.Fatal(err)
	}
	if externalAssociations != 2 {
		t.Fatalf("reaccepted external message associations = %d, want 2", externalAssociations)
	}
	expectSeekDBConstraintError(t, database, integrationTurnInsertSQL,
		"turn-a-duplicate-sequence", "conversation-a", "external-message-44", 2,
		"interpreting", "user", nil, nil, nil, "ineligible", nil, nil, nil, 0, 0, nil, nil, 2, 2,
	)
	insertIntegrationTurn(t, database, "turn-b-max-sequence", "conversation-b", "external-message-max", math.MaxInt64)
	insertIntegrationMessage(t, database, "message-b-max-sequence", "conversation-b", "turn-b-max-sequence", math.MaxInt64, "user", "maximum sequence")
	var storedTurnSequence, storedMessageSequence int64
	if err := database.QueryRowContext(t.Context(), `
SELECT t.sequence, m.sequence
FROM conversation_turns t
JOIN conversation_messages m
  ON m.turn_id = t.id AND m.conversation_id = t.conversation_id
WHERE t.id = ?`, "turn-b-max-sequence").Scan(&storedTurnSequence, &storedMessageSequence); err != nil {
		t.Fatalf("read maximum signed sequences: %v", err)
	}
	if storedTurnSequence != math.MaxInt64 || storedMessageSequence != math.MaxInt64 {
		t.Fatalf("maximum signed sequences = turn %d, message %d", storedTurnSequence, storedMessageSequence)
	}
	overflowSequence := uint64(math.MaxInt64) + 1
	expectSeekDBConstraintError(t, database, integrationTurnInsertSQL,
		"turn-b-overflow-sequence", "conversation-b", "external-message-overflow", overflowSequence,
		"interpreting", "user", nil, nil, nil, "ineligible", nil, nil, nil, 0, 0, nil, nil, 2, 2,
	)
	expectSeekDBConstraintError(t, database, integrationMessageInsertSQL,
		"message-b-overflow-sequence", "conversation-b", "turn-b-max-sequence", overflowSequence,
		"assistant", "overflow sequence", `[]`, 2,
	)

	// Insert physical rows in reverse order; the conversation sequence remains
	// the sole durable pagination order.
	insertIntegrationMessage(t, database, "message-a-2", "conversation-a", "turn-a-2", 2, "user", "second")
	insertIntegrationMessage(t, database, "message-a-1", "conversation-a", "turn-a-1", 1, "user", "first")
	rows, err := database.QueryContext(t.Context(), `
SELECT sequence, content FROM conversation_messages
WHERE conversation_id = ? ORDER BY sequence ASC`, "conversation-a")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var sequences []int64
	var contents []string
	for rows.Next() {
		var sequence int64
		var content string
		if err := rows.Scan(&sequence, &content); err != nil {
			t.Fatal(err)
		}
		sequences = append(sequences, sequence)
		contents = append(contents, content)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(sequences) != 2 || sequences[0] != 1 || sequences[1] != 2 || contents[0] != "first" || contents[1] != "second" {
		t.Fatalf("stable message order = sequences %v, contents %v", sequences, contents)
	}

	expectSeekDBConstraintError(t, database, integrationMessageInsertSQL,
		"message-crossed", "conversation-b", "turn-a-1", 2, "assistant", "crossed conversation", `[]`, 2,
	)
	expectSeekDBConstraintError(t, database, integrationMessageInsertSQL,
		"message-duplicate-role", "conversation-a", "turn-a-1", 3, "user", "duplicate role", `[]`, 3,
	)
	expectSeekDBConstraintError(t, database, integrationMessageInsertSQL,
		"message-empty", "conversation-b", "turn-b-1", 1, "user", "", `[]`, 1,
	)
	expectSeekDBConstraintError(t, database, `
INSERT INTO prompt_windows(
  conversation_id, revision, summary, cutoff_message_sequence, projection_revision, projection_state, updated_at_ms
) VALUES (?, ?, ?, ?, ?, ?, ?)`, "conversation-b", 1, nil, 0, 1, `[]`, 1)

	if _, err := database.ExecContext(t.Context(), "DELETE FROM conversations WHERE id = ?", "conversation-a"); err != nil {
		t.Fatalf("delete conversation cascade root: %v", err)
	}
	for _, table := range []string{"endpoint_conversations", "conversation_turns", "conversation_messages", "prompt_windows"} {
		var count int
		if err := database.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM "+table+" WHERE conversation_id = ?", "conversation-a").Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("%s rows after conversation cascade = %d", table, count)
		}
	}
	if _, err := database.ExecContext(t.Context(), "DELETE FROM conversations WHERE id = ?", "conversation-default-a"); err != nil {
		t.Fatalf("delete default conversation cascade root: %v", err)
	}
	var defaultMappingCount int
	if err := database.QueryRowContext(t.Context(), `
SELECT COUNT(*) FROM character_conversations
WHERE conversation_id = ?`, "conversation-default-a").Scan(&defaultMappingCount); err != nil {
		t.Fatal(err)
	}
	if defaultMappingCount != 0 {
		t.Fatalf("character conversation mappings after cascade = %d", defaultMappingCount)
	}
	var remaining int
	if err := database.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM conversations WHERE id = ?", "conversation-b").Scan(&remaining); err != nil || remaining != 1 {
		t.Fatalf("isolated conversation remaining = %d, error = %v", remaining, err)
	}
}

func TestRealSeekDBConversationSchemaRejectsShapeDriftAtRevisionTwo(t *testing.T) {
	instance, config := openSchemaMigrationRuntime(t)
	defer closeRuntimeForIntegrationTest(t, instance, config.ShutdownLimit)
	database := instance.SQL()
	migrations := BuiltinMigrations()
	if err := MigrateSchema(t.Context(), database, migrations[:1]); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(t.Context(), conversationSchema[0].ddl); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(t.Context(), `
CREATE TABLE character_conversations (
  character_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  conversation_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  kind VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  PRIMARY KEY (character_id),
  UNIQUE KEY character_conversations_conversation_key (conversation_id),
  CONSTRAINT character_conversations_invariants_check CHECK (
    CHAR_LENGTH(character_id) > 0 AND character_id = TRIM(character_id) AND
    CHAR_LENGTH(conversation_id) > 0 AND conversation_id = TRIM(conversation_id) AND
    kind = 'character'
  )
)`); err != nil {
		t.Fatal(err)
	}
	err := MigrateSchema(t.Context(), database, migrations)
	if err == nil {
		t.Fatal("MigrateSchema accepted a pre-existing revision-two table with an invalid shape")
	}
	row := integrationJournalRowForRevision(t, database, conversationSchemaRevision)
	if row.State != string(MigrationFailed) || row.ErrorCode != "VERIFY_FAILED" || row.AttemptCount != 1 {
		t.Fatalf("revision-two shape-drift journal = %#v", row)
	}
	status, readinessErr := CheckSchema(t.Context(), database, CurrentSchemaRevision())
	if !errors.Is(readinessErr, ErrSchemaNotCurrent) || status.State != SchemaNotCurrent ||
		status.Observed == nil || status.Observed.Revision.Number != conversationSchemaRevision {
		t.Fatalf("revision-two shape-drift readiness = %#v, error = %v", status, readinessErr)
	}
}

func TestRealSeekDBFoundationSchemaRejectsShapeDrift(t *testing.T) {
	instance, config := openSchemaMigrationRuntime(t)
	defer closeRuntimeForIntegrationTest(t, instance, config.ShutdownLimit)
	database := instance.SQL()

	if _, err := database.ExecContext(t.Context(), `
CREATE TABLE config_documents (
  namespace VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  document_key VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  PRIMARY KEY (namespace, document_key)
)`); err != nil {
		t.Fatal(err)
	}
	err := MigrateSchema(t.Context(), database, BuiltinMigrations())
	if err == nil {
		t.Fatal("MigrateSchema accepted a pre-existing table with an invalid shape")
	}
	row := integrationJournalRowForRevision(t, database, foundationSchemaRevision)
	if row.State != string(MigrationFailed) || row.ErrorCode != "VERIFY_FAILED" || row.AttemptCount != 1 {
		t.Fatalf("shape-drift journal = %#v", row)
	}
	status, readinessErr := CheckSchema(t.Context(), database, CurrentSchemaRevision())
	if !errors.Is(readinessErr, ErrSchemaNotCurrent) || status.State != SchemaNotCurrent {
		t.Fatalf("shape-drift readiness = %#v, error = %v", status, readinessErr)
	}
}

func TestRealSeekDBFoundationSchemaRejectsSameNameWeakCheck(t *testing.T) {
	instance, config := openSchemaMigrationRuntime(t)
	defer closeRuntimeForIntegrationTest(t, instance, config.ShutdownLimit)
	database := instance.SQL()

	if _, err := database.ExecContext(t.Context(), `
CREATE TABLE owner_identities (
  namespace VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  subject_digest BINARY(32) NOT NULL,
  created_at_ms BIGINT UNSIGNED NOT NULL,
  PRIMARY KEY (namespace, subject_digest),
  KEY owner_identities_created_idx (created_at_ms),
  CONSTRAINT owner_identities_invariants_check CHECK (created_at_ms >= 0)
)`); err != nil {
		t.Fatal(err)
	}
	assertFoundationMigrationVerifyFailure(t, database, "same-name weak CHECK")
}

func TestRealSeekDBFoundationSchemaRejectsMissingOrWrongForeignKey(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		mutateDDL func(string) string
	}{
		{
			name: "missing",
			mutateDDL: func(ddl string) string {
				return strings.Replace(ddl, `  CONSTRAINT plugin_instances_package_fk FOREIGN KEY (plugin_id, plugin_version)
    REFERENCES plugin_packages (plugin_id, version) ON UPDATE RESTRICT ON DELETE RESTRICT,
`, "", 1)
			},
		},
		{
			name: "wrong update rule",
			mutateDDL: func(ddl string) string {
				return strings.Replace(ddl, "ON UPDATE RESTRICT ON DELETE RESTRICT", "ON UPDATE NO ACTION ON DELETE RESTRICT", 1)
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			instance, config := openSchemaMigrationRuntime(t)
			defer closeRuntimeForIntegrationTest(t, instance, config.ShutdownLimit)
			database := instance.SQL()
			if _, err := database.ExecContext(t.Context(), foundationSchema[4].ddl); err != nil {
				t.Fatalf("precreate plugin_packages: %v", err)
			}
			mutated := testCase.mutateDDL(foundationSchema[5].ddl)
			if mutated == foundationSchema[5].ddl {
				t.Fatal("test setup did not mutate plugin_instances DDL")
			}
			if _, err := database.ExecContext(t.Context(), mutated); err != nil {
				t.Fatalf("precreate plugin_instances: %v", err)
			}
			assertFoundationMigrationVerifyFailure(t, database, testCase.name+" foreign key")
		})
	}
}

func assertFoundationMigrationVerifyFailure(t *testing.T, database *sql.DB, drift string) {
	t.Helper()
	err := MigrateSchema(t.Context(), database, BuiltinMigrations())
	if err == nil {
		t.Fatalf("MigrateSchema accepted %s", drift)
	}
	t.Logf("MigrateSchema rejected %s: %v", drift, err)
	row := integrationJournalRowForRevision(t, database, foundationSchemaRevision)
	if row.State != string(MigrationFailed) || row.ErrorCode != "VERIFY_FAILED" || row.AttemptCount != 1 {
		t.Fatalf("%s journal = %#v", drift, row)
	}
}

func assertFoundationSchemaVerified(t *testing.T, database *sql.DB) {
	t.Helper()
	connection, err := database.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if err := BuiltinMigrations()[0].Verify(t.Context(), connection); err != nil {
		t.Fatalf("verify foundation schema: %v", err)
	}
}

func assertConversationSchemaVerified(t *testing.T, database *sql.DB) {
	t.Helper()
	connection, err := database.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if err := BuiltinMigrations()[1].Verify(t.Context(), connection); err != nil {
		t.Fatalf("verify conversation schema: %v", err)
	}
}

func assertFoundationTableSet(t *testing.T, database *sql.DB) {
	t.Helper()
	for _, table := range foundationSchema {
		if got := integrationTableCount(t, database, table.name); got != 1 {
			t.Errorf("foundation table %s count = %d, want 1", table.name, got)
		}
	}
	for _, table := range conversationSchema {
		if got := integrationTableCount(t, database, table.name); got != 1 {
			t.Errorf("conversation table %s count = %d, want 1", table.name, got)
		}
	}
	var businessTableCount int
	if err := database.QueryRowContext(t.Context(), `
SELECT COUNT(*)
FROM information_schema.tables
WHERE table_schema = DATABASE() AND table_name <> 'schema_revisions'`).Scan(&businessTableCount); err != nil {
		t.Fatal(err)
	}
	want := len(foundationSchema) + len(conversationSchema)
	if businessTableCount != want {
		t.Fatalf("business table count = %d, want %d", businessTableCount, want)
	}
}

func assertFoundationChecksRejectInvalidData(t *testing.T, database *sql.DB) {
	t.Helper()
	nonce := make([]byte, 12)
	digest := sha256.Sum256([]byte("verified plugin package"))
	validManifest := `{"id":"fairy.test","version":"1.0.0"}`

	expectSeekDBConstraintError(t, database, `
INSERT INTO config_documents(namespace, document_key, schema_version, revision, document, created_at_ms, updated_at_ms)
VALUES (?, ?, ?, ?, ?, ?, ?)`, "runtime", "model", 1, 1, `[]`, 1, 1)
	expectSeekDBConstraintError(t, database, `
INSERT INTO secret_values(namespace, name, key_version, nonce, ciphertext, aad, created_at_ms, updated_at_ms)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, "model", "primary", 1, nonce[:11], []byte("ciphertext"), "model/primary", 1, 1)
	expectSeekDBConstraintError(t, database, `
INSERT INTO owner_identities(namespace, subject_digest, created_at_ms)
VALUES (?, ?, ?)`, "qq", digest[:], 0)
	expectSeekDBConstraintError(t, database, `
	INSERT INTO characters(character_id, revision, name, snapshot, appearance_ref, created_at_ms, updated_at_ms)
	VALUES (?, ?, ?, ?, ?, ?, ?)`, "atri", 0, "亚托莉", `{}`, nil, 1, 1)
	expectSeekDBConstraintError(t, database, `
INSERT INTO plugin_packages(plugin_id, version, abi_version, artifact_sha256, publisher_digest, manifest, verified_at_ms)
VALUES (?, ?, ?, ?, ?, ?, ?)`, "fairy.test", "1.0.0", 1, digest[:], nil, `[]`, 1)

	if _, err := database.ExecContext(t.Context(), `
INSERT INTO plugin_packages(plugin_id, version, abi_version, artifact_sha256, publisher_digest, manifest, verified_at_ms)
VALUES (?, ?, ?, ?, ?, ?, ?)`, "fairy.test", "1.0.0", 1, digest[:], nil, validManifest, 1); err != nil {
		t.Fatalf("insert valid plugin package: %v", err)
	}
	expectSeekDBConstraintError(t, database, `
INSERT INTO plugin_instances(instance_id, plugin_id, plugin_version, enabled, lifecycle_state, capability_grants, config_document, created_at_ms, updated_at_ms)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, "qq-main", "fairy.test", "1.0.0", 0, "ready", `[]`, `{}`, 1, 1)
	expectSeekDBConstraintError(t, database, `
INSERT INTO plugin_instances(instance_id, plugin_id, plugin_version, enabled, lifecycle_state, capability_grants, config_document, created_at_ms, updated_at_ms)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, "qq-disabled-enabled", "fairy.test", "1.0.0", 1, "disabled", `[]`, `{}`, 1, 1)
	for _, row := range []struct {
		instanceID string
		enabled    int
		state      string
	}{
		{instanceID: "qq-disabled", enabled: 0, state: "disabled"},
		{instanceID: "qq-ready", enabled: 1, state: "ready"},
	} {
		if _, err := database.ExecContext(t.Context(), `
INSERT INTO plugin_instances(instance_id, plugin_id, plugin_version, enabled, lifecycle_state, capability_grants, config_document, created_at_ms, updated_at_ms)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, row.instanceID, "fairy.test", "1.0.0", row.enabled, row.state, `[]`, `{}`, 1, 1); err != nil {
			t.Fatalf("insert valid plugin instance %s: %v", row.instanceID, err)
		}
	}
}

const integrationTurnInsertSQL = `
INSERT INTO conversation_turns(
  id, conversation_id, message_id, sequence, status, origin,
  error_code, error_message, error_retryable,
  extraction_state, extraction_claim_id, extraction_lease_owner, extraction_lease_expires_at_ms,
  extraction_attempt_count, extraction_next_attempt_at_ms, extraction_error_code, extraction_error_message,
  created_at_ms, updated_at_ms
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

const integrationMessageInsertSQL = `
INSERT INTO conversation_messages(
  id, conversation_id, turn_id, sequence, role, content, expression_parts, created_at_ms
) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`

func insertIntegrationConversation(t *testing.T, database *sql.DB, id, characterID, kind string) {
	t.Helper()
	if _, err := database.ExecContext(t.Context(), `
INSERT INTO conversations(id, character_id, kind, created_at_ms, updated_at_ms)
VALUES (?, ?, ?, ?, ?)`, id, characterID, kind, 1, 1); err != nil {
		t.Fatalf("insert conversation %s: %v", id, err)
	}
}

func insertIntegrationPromptWindow(t *testing.T, database *sql.DB, conversationID string) {
	t.Helper()
	if _, err := database.ExecContext(t.Context(), `
INSERT INTO prompt_windows(
  conversation_id, revision, summary, cutoff_message_sequence, projection_revision, projection_state, updated_at_ms
) VALUES (?, ?, ?, ?, ?, ?, ?)`, conversationID, 1, nil, 0, 1, `{"version":1,"omissions":[]}`, 1); err != nil {
		t.Fatalf("insert prompt window for %s: %v", conversationID, err)
	}
}

func insertIntegrationCharacterConversation(t *testing.T, database *sql.DB, characterID, conversationID string) {
	t.Helper()
	if _, err := database.ExecContext(t.Context(), `
INSERT INTO character_conversations(character_id, conversation_id, kind)
VALUES (?, ?, ?)`, characterID, conversationID, "character"); err != nil {
		t.Fatalf("insert character conversation %s: %v", conversationID, err)
	}
}

func insertIntegrationEndpoint(
	t *testing.T,
	database *sql.DB,
	characterID, endpoint string,
	endpointDigest []byte,
	conversationID, audience, initiation, presentation string,
	evaluation int,
	principalNamespace, principalDigest any,
) {
	t.Helper()
	if _, err := database.ExecContext(t.Context(), `
INSERT INTO endpoint_conversations(
  character_id, endpoint, endpoint_key_digest, conversation_id, kind, audience, initiation, presentation,
  evaluation, principal_namespace, principal_digest, created_at_ms, updated_at_ms
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		characterID, endpoint, endpointDigest, conversationID, "endpoint", audience, initiation, presentation,
		evaluation, principalNamespace, principalDigest, 1, 1,
	); err != nil {
		t.Fatalf("insert endpoint conversation %s: %v", conversationID, err)
	}
}

func insertIntegrationTurn(t *testing.T, database *sql.DB, id, conversationID, externalMessageID string, sequence int64) {
	t.Helper()
	if _, err := database.ExecContext(t.Context(), integrationTurnInsertSQL,
		id, conversationID, externalMessageID, sequence,
		"interpreting", "user", nil, nil, nil, "ineligible", nil, nil, nil, 0, 0, nil, nil, 1, 1,
	); err != nil {
		t.Fatalf("insert conversation turn %s: %v", id, err)
	}
}

func insertIntegrationMessage(
	t *testing.T,
	database *sql.DB,
	id, conversationID, turnID string,
	sequence int64,
	role, content string,
) {
	t.Helper()
	if _, err := database.ExecContext(t.Context(), integrationMessageInsertSQL,
		id, conversationID, turnID, sequence, role, content, `[]`, 1,
	); err != nil {
		t.Fatalf("insert conversation message %s: %v", id, err)
	}
}

func sha256Bytes(value string) []byte {
	digest := sha256.Sum256([]byte(value))
	return digest[:]
}

func expectSeekDBConstraintError(t *testing.T, database *sql.DB, statement string, arguments ...any) {
	t.Helper()
	if _, err := database.ExecContext(t.Context(), statement, arguments...); err == nil {
		t.Fatalf("SeekDB accepted row rejected by named CHECK: %s", normalizeDDL(statement))
	}
}

func verifyFoundationWithContext(ctx context.Context, database *sql.DB) error {
	connection, err := database.Conn(ctx)
	if err != nil {
		return err
	}
	defer connection.Close()
	return verifyFoundationSchema(ctx, connection)
}
