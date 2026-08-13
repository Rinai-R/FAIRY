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
	"time"
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
	assertTurnEvidenceSchemaVerified(t, database)
	assertExtractionCoordinationSchemaVerified(t, database)
	assertFoundationTableSet(t, database)
	if got := integrationJournalRowForRevision(t, database, foundationSchemaRevision).AttemptCount; got != 1 {
		t.Fatalf("foundation attempt count = %d, want 1", got)
	}
	if got := integrationJournalRowForRevision(t, database, conversationSchemaRevision).AttemptCount; got != 1 {
		t.Fatalf("conversation attempt count = %d, want 1", got)
	}
	if got := integrationJournalRowForRevision(t, database, turnEvidenceSchemaRevision).AttemptCount; got != 1 {
		t.Fatalf("turn evidence attempt count = %d, want 1", got)
	}
	if got := integrationJournalRowForRevision(t, database, transcriptRecallSchemaRevision).AttemptCount; got != 1 {
		t.Fatalf("transcript recall attempt count = %d, want 1", got)
	}
	if got := integrationJournalRowForRevision(t, database, conversationRuntimeSchemaRevision).AttemptCount; got != 1 {
		t.Fatalf("conversation runtime attempt count = %d, want 1", got)
	}
	if got := integrationJournalRowForRevision(t, database, extractionCoordinationRevision).AttemptCount; got != 1 {
		t.Fatalf("extraction coordination attempt count = %d, want 1", got)
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
	if got := integrationJournalRowForRevision(t, database, turnEvidenceSchemaRevision).AttemptCount; got != 1 {
		t.Fatalf("turn evidence attempt count after repeat = %d, want 1", got)
	}
	if got := integrationJournalRowForRevision(t, database, transcriptRecallSchemaRevision).AttemptCount; got != 1 {
		t.Fatalf("transcript recall attempt count after repeat = %d, want 1", got)
	}
	if got := integrationJournalRowForRevision(t, database, conversationRuntimeSchemaRevision).AttemptCount; got != 1 {
		t.Fatalf("conversation runtime attempt count after repeat = %d, want 1", got)
	}
	if got := integrationJournalRowForRevision(t, database, extractionCoordinationRevision).AttemptCount; got != 1 {
		t.Fatalf("extraction coordination attempt count after repeat = %d, want 1", got)
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
	assertTurnEvidenceSchemaVerified(t, restarted.SQL())
	assertExtractionCoordinationSchemaVerified(t, restarted.SQL())
	if got := integrationJournalRowForRevision(t, restarted.SQL(), foundationSchemaRevision).AttemptCount; got != 1 {
		t.Fatalf("foundation attempt count after restart = %d, want 1", got)
	}
	if got := integrationJournalRowForRevision(t, restarted.SQL(), conversationSchemaRevision).AttemptCount; got != 1 {
		t.Fatalf("conversation attempt count after restart = %d, want 1", got)
	}
	if got := integrationJournalRowForRevision(t, restarted.SQL(), turnEvidenceSchemaRevision).AttemptCount; got != 1 {
		t.Fatalf("turn evidence attempt count after restart = %d, want 1", got)
	}
	if got := integrationJournalRowForRevision(t, restarted.SQL(), transcriptRecallSchemaRevision).AttemptCount; got != 1 {
		t.Fatalf("transcript recall attempt count after restart = %d, want 1", got)
	}
	if got := integrationJournalRowForRevision(t, restarted.SQL(), conversationRuntimeSchemaRevision).AttemptCount; got != 1 {
		t.Fatalf("conversation runtime attempt count after restart = %d, want 1", got)
	}
	if got := integrationJournalRowForRevision(t, restarted.SQL(), extractionCoordinationRevision).AttemptCount; got != 1 {
		t.Fatalf("extraction coordination attempt count after restart = %d, want 1", got)
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
	assertExtractionCoordinationSchemaVerified(t, database)
	for _, revision := range []int64{
		foundationSchemaRevision,
		conversationSchemaRevision,
		turnEvidenceSchemaRevision,
		transcriptRecallSchemaRevision,
		conversationRuntimeSchemaRevision,
		extractionCoordinationRevision,
	} {
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
	assertExtractionCoordinationSchemaVerified(t, database)
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

func TestRealSeekDBTurnEvidenceSchemaUpgradesRevisionTwoAndEnforcesEdges(t *testing.T) {
	instance, config := openSchemaMigrationRuntime(t)
	defer closeRuntimeForIntegrationTest(t, instance, config.ShutdownLimit)
	database := instance.SQL()
	migrations := BuiltinMigrations()

	if err := MigrateSchema(t.Context(), database, migrations[:2]); err != nil {
		t.Fatalf("MigrateSchema(revision two) error = %v", err)
	}
	assertConversationSchemaVerified(t, database)
	if _, err := database.ExecContext(t.Context(), turnEvidenceSchema[0].ddl); err != nil {
		t.Fatalf("precreate partial turn evidence schema: %v", err)
	}
	insertIntegrationConversation(t, database, "evidence-conversation", "evidence-character", "character")
	insertIntegrationPromptWindow(t, database, "evidence-conversation")
	insertIntegrationCharacterConversation(t, database, "evidence-character", "evidence-conversation")
	insertIntegrationTurn(t, database, "evidence-turn", "evidence-conversation", "", math.MaxInt64)
	if err := MigrateSchema(t.Context(), database, migrations); err != nil {
		t.Fatalf("MigrateSchema(revision three) error = %v", err)
	}
	assertCurrentSchema(t, database, CurrentSchemaRevision())
	assertTurnEvidenceSchemaVerified(t, database)
	assertExtractionCoordinationSchemaVerified(t, database)

	if _, err := database.ExecContext(t.Context(), `
INSERT INTO conversation_turn_evidence(turn_id, evidence_id, created_at_ms)
VALUES (?, ?, ?)`, "evidence-turn", "观察-一", 1); err != nil {
		t.Fatalf("insert Unicode turn evidence: %v", err)
	}
	expectSeekDBConstraintError(t, database, `
INSERT INTO conversation_turn_evidence(turn_id, evidence_id, created_at_ms)
VALUES (?, ?, ?)`, "evidence-turn", "bad\nevidence", 1)
	expectSeekDBConstraintError(t, database, `
INSERT INTO conversation_turn_evidence(turn_id, evidence_id, created_at_ms)
VALUES (?, ?, ?)`, "missing-turn", "orphan", 1)
	expectSeekDBConstraintError(t, database, `
INSERT INTO conversation_turn_evidence(turn_id, evidence_id, created_at_ms)
VALUES (?, ?, ?)`, "evidence-turn", "观察-一", 1)

	if _, err := database.ExecContext(t.Context(), "DELETE FROM conversation_turns WHERE id = ?", "evidence-turn"); err != nil {
		t.Fatalf("delete turn for evidence cascade: %v", err)
	}
	var count int
	if err := database.QueryRowContext(t.Context(), `
SELECT COUNT(*) FROM conversation_turn_evidence WHERE turn_id = ?`, "evidence-turn").Scan(&count); err != nil {
		t.Fatalf("count cascaded turn evidence: %v", err)
	}
	if count != 0 {
		t.Fatalf("turn evidence after turn delete = %d, want 0", count)
	}
	if err := MigrateSchema(t.Context(), database, migrations); err != nil {
		t.Fatalf("idempotent revision three: %v", err)
	}
	if got := integrationJournalRowForRevision(t, database, turnEvidenceSchemaRevision).AttemptCount; got != 1 {
		t.Fatalf("turn evidence attempt count after repeat = %d, want 1", got)
	}
}

func TestRealSeekDBTurnEvidenceSchemaRejectsShapeDrift(t *testing.T) {
	instance, config := openSchemaMigrationRuntime(t)
	defer closeRuntimeForIntegrationTest(t, instance, config.ShutdownLimit)
	database := instance.SQL()
	migrations := BuiltinMigrations()
	if err := MigrateSchema(t.Context(), database, migrations[:2]); err != nil {
		t.Fatalf("MigrateSchema(revision two) error = %v", err)
	}
	assertConversationSchemaVerified(t, database)
	if _, err := database.ExecContext(t.Context(), `
CREATE TABLE conversation_turn_evidence (
  turn_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  evidence_id VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  created_at_ms BIGINT UNSIGNED NOT NULL,
  PRIMARY KEY (turn_id, evidence_id)
)`); err != nil {
		t.Fatalf("precreate drifted turn evidence table: %v", err)
	}
	err := MigrateSchema(t.Context(), database, migrations)
	if err == nil || !strings.Contains(err.Error(), "conversation_turn_evidence") {
		t.Fatalf("MigrateSchema(drifted turn evidence) error = %v", err)
	}
	row := integrationJournalRowForRevision(t, database, turnEvidenceSchemaRevision)
	if row.State != string(MigrationFailed) || row.AttemptCount != 1 {
		t.Fatalf("drifted turn evidence journal = %#v", row)
	}
	status, readinessErr := CheckSchema(t.Context(), database, CurrentSchemaRevision())
	if !errors.Is(readinessErr, ErrSchemaNotCurrent) || status.State != SchemaNotCurrent ||
		status.Observed == nil || status.Observed.Revision.Number != turnEvidenceSchemaRevision {
		t.Fatalf("drifted turn evidence readiness = %#v, %v", status, readinessErr)
	}
}

func TestRealSeekDBTranscriptRecallSchemaRecoversPartialAndRemainsIdempotent(t *testing.T) {
	instance, config := openSchemaMigrationRuntime(t)
	defer closeRuntimeForIntegrationTest(t, instance, config.ShutdownLimit)
	database := instance.SQL()
	migrations := BuiltinMigrations()
	if err := MigrateSchema(t.Context(), database, migrations[:3]); err != nil {
		t.Fatalf("MigrateSchema(revision three) error = %v", err)
	}
	insertIntegrationConversation(t, database, "transcript-recall-conversation", "transcript-recall-character", "character")
	insertIntegrationPromptWindow(t, database, "transcript-recall-conversation")
	insertIntegrationTurn(t, database, "transcript-recall-turn", "transcript-recall-conversation", "external-recall", 1)
	insertIntegrationMessage(
		t, database,
		"transcript-recall-message", "transcript-recall-conversation", "transcript-recall-turn",
		1, "user", "苍之彼方的四重奏与海边约定",
	)

	// SeekDB DDL commits independently. Simulate a process exit after the
	// physical index exists but before revision four was journaled current.
	if _, err := database.ExecContext(t.Context(), transcriptRecallIndexDDL); err != nil {
		t.Fatalf("precreate partial transcript recall index: %v", err)
	}
	connection, err := database.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	legacyVerifyErr := migrations[1].Verify(t.Context(), connection)
	if closeErr := connection.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if legacyVerifyErr == nil {
		t.Fatal("revision-two verifier accepted the revision-four index")
	}
	if err := MigrateSchema(t.Context(), database, migrations); err != nil {
		t.Fatalf("MigrateSchema(partial revision four) error = %v", err)
	}
	assertCurrentSchema(t, database, CurrentSchemaRevision())
	assertExtractionCoordinationSchemaVerified(t, database)
	row := integrationJournalRowForRevision(t, database, transcriptRecallSchemaRevision)
	if row.State != string(MigrationCurrent) || row.AttemptCount != 1 {
		t.Fatalf("transcript recall journal = %#v", row)
	}
	var matchCount int
	if err := database.QueryRowContext(t.Context(), `
SELECT COUNT(*) FROM conversation_messages
WHERE conversation_id = ? AND sequence <= ?
  AND MATCH(content) AGAINST(? IN NATURAL LANGUAGE MODE)`,
		"transcript-recall-conversation", 1, "苍之彼方",
	).Scan(&matchCount); err != nil {
		t.Fatalf("query partial transcript recall index: %v", err)
	}
	if matchCount != 1 {
		t.Fatalf("transcript recall match count = %d, want 1", matchCount)
	}
	if err := MigrateSchema(t.Context(), database, migrations); err != nil {
		t.Fatalf("MigrateSchema(repeated revision four) error = %v", err)
	}
	if got := integrationJournalRowForRevision(t, database, transcriptRecallSchemaRevision).AttemptCount; got != 1 {
		t.Fatalf("transcript recall attempt count after repeat = %d, want 1", got)
	}
}

func TestRealSeekDBTranscriptRecallSchemaBackfillsRevisionThreeMessagesAndPersists(t *testing.T) {
	const (
		conversationID = "transcript-backfill-conversation"
		messageCount   = 4000
		batchSize      = 200
		searchQuery    = "苍之彼方"
		queryLimit     = 15 * time.Second
	)
	instance, config := openSchemaMigrationRuntime(t)
	closed := false
	defer func() {
		if !closed {
			closeRuntimeForIntegrationTest(t, instance, config.ShutdownLimit)
		}
	}()
	database := instance.SQL()
	migrations := BuiltinMigrations()
	if config.QueryLimit != queryLimit {
		t.Fatalf("schema integration query limit = %s, want immutable backfill limit %s", config.QueryLimit, queryLimit)
	}
	if err := MigrateSchema(t.Context(), database, migrations[:3]); err != nil {
		t.Fatalf("MigrateSchema(revision three) error = %v", err)
	}
	assertCurrentSchema(t, database, migrations[2].Revision)
	assertConversationSchemaVerified(t, database)
	assertTurnEvidenceSchemaVerified(t, database)
	insertIntegrationConversation(t, database, conversationID, "transcript-backfill-character", "character")
	insertIntegrationPromptWindow(t, database, conversationID)
	insertIntegrationTranscriptBackfillMessages(t, database, conversationID, messageCount, batchSize)
	var storedCount int
	if err := database.QueryRowContext(t.Context(), `
SELECT COUNT(*) FROM conversation_messages WHERE conversation_id = ?`, conversationID).Scan(&storedCount); err != nil {
		t.Fatalf("count revision-three transcript backfill fixture: %v", err)
	}
	if storedCount != messageCount {
		t.Fatalf("revision-three transcript backfill fixture count = %d, want %d", storedCount, messageCount)
	}

	// Production foundation startup gives the whole migration the larger of
	// StartLimit and QueryLimit, while the SQL driver still bounds each DDL and
	// metadata query by QueryLimit. Exercise that exact outer context and keep
	// the observed backfill below the stricter per-query bound as well.
	migrationLimit := max(config.StartLimit, config.QueryLimit)
	migrationContext, cancelMigration := context.WithTimeout(t.Context(), migrationLimit)
	migrationStarted := time.Now()
	migrationErr := MigrateSchema(migrationContext, database, migrations)
	migrationElapsed := time.Since(migrationStarted)
	migrationContextErr := migrationContext.Err()
	cancelMigration()
	if migrationErr != nil {
		t.Fatalf("MigrateSchema(revision four backfill) after %s: %v", migrationElapsed, migrationErr)
	}
	if migrationContextErr != nil {
		t.Fatalf("revision-four backfill exhausted migration context after %s: %v", migrationElapsed, migrationContextErr)
	}
	if migrationElapsed >= queryLimit || migrationElapsed >= migrationLimit {
		t.Fatalf(
			"revision-four backfill elapsed = %s, want less than query limit %s and migration limit %s",
			migrationElapsed, queryLimit, migrationLimit,
		)
	}
	assertCurrentSchema(t, database, CurrentSchemaRevision())
	assertExtractionCoordinationSchemaVerified(t, database)
	row := integrationJournalRowForRevision(t, database, transcriptRecallSchemaRevision)
	if row.State != string(MigrationCurrent) || row.AttemptCount != 1 {
		t.Fatalf("transcript backfill journal = %#v", row)
	}
	assertTranscriptBackfillMatchCount(t, database, conversationID, searchQuery, messageCount)

	closeRuntimeForIntegrationTest(t, instance, config.ShutdownLimit)
	closed = true
	restartStarted := time.Now()
	restarted, err := Open(t.Context(), config)
	restartElapsed := time.Since(restartStarted)
	if err != nil {
		t.Fatalf("restart SeekDB transcript backfill runtime after %s: %v", restartElapsed, err)
	}
	instance = restarted
	closed = false
	if restartElapsed >= config.StartLimit {
		t.Fatalf("transcript backfill restart elapsed = %s, want less than start limit %s", restartElapsed, config.StartLimit)
	}
	assertCurrentSchema(t, restarted.SQL(), CurrentSchemaRevision())
	assertExtractionCoordinationSchemaVerified(t, restarted.SQL())
	assertTranscriptBackfillMatchCount(t, restarted.SQL(), conversationID, searchQuery, messageCount)
	t.Logf(
		"revision-four FULLTEXT backfilled %d messages in %s (query limit %s, migration limit %s); restart ready in %s (start limit %s)",
		messageCount, migrationElapsed, queryLimit, migrationLimit, restartElapsed, config.StartLimit,
	)
}

func TestRealSeekDBTranscriptRecallSchemaRejectsParserAndLogicalColumnDrift(t *testing.T) {
	for _, testCase := range []struct {
		name              string
		preexistingIndex  string
		wantErrorFragment string
	}{
		{
			name: "wrong parser",
			preexistingIndex: `CREATE FULLTEXT INDEX conversation_messages_content_fts_idx
ON conversation_messages(content) WITH PARSER IK PARSER_PROPERTIES=(ik_mode='smart')`,
			wantErrorFragment: "immutable parser clause",
		},
		{
			name: "wrong logical column",
			preexistingIndex: `CREATE FULLTEXT INDEX conversation_messages_content_fts_idx
ON conversation_messages(role) WITH PARSER SPACE`,
			wantErrorFragment: "logical index",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			instance, config := openSchemaMigrationRuntime(t)
			defer closeRuntimeForIntegrationTest(t, instance, config.ShutdownLimit)
			database := instance.SQL()
			migrations := BuiltinMigrations()
			if err := MigrateSchema(t.Context(), database, migrations[:3]); err != nil {
				t.Fatalf("MigrateSchema(revision three) error = %v", err)
			}
			if _, err := database.ExecContext(t.Context(), testCase.preexistingIndex); err != nil {
				t.Fatalf("precreate drifted transcript recall index: %v", err)
			}
			err := MigrateSchema(t.Context(), database, migrations)
			if err == nil || !strings.Contains(err.Error(), testCase.wantErrorFragment) {
				t.Fatalf("MigrateSchema(%s) error = %v", testCase.name, err)
			}
			row := integrationJournalRowForRevision(t, database, transcriptRecallSchemaRevision)
			if row.State != string(MigrationFailed) || row.ErrorCode != "VERIFY_FAILED" || row.AttemptCount != 1 {
				t.Fatalf("%s transcript recall journal = %#v", testCase.name, row)
			}
			status, readinessErr := CheckSchema(t.Context(), database, CurrentSchemaRevision())
			if !errors.Is(readinessErr, ErrSchemaNotCurrent) || status.State != SchemaNotCurrent ||
				status.Observed == nil || status.Observed.Revision.Number != transcriptRecallSchemaRevision {
				t.Fatalf("%s transcript recall readiness = %#v, %v", testCase.name, status, readinessErr)
			}
		})
	}
}

func TestRealSeekDBConversationRuntimeSchemaFreshInstallIsCurrent(t *testing.T) {
	instance, config := openSchemaMigrationRuntime(t)
	defer closeRuntimeForIntegrationTest(t, instance, config.ShutdownLimit)
	database := instance.SQL()
	if err := MigrateSchema(t.Context(), database, BuiltinMigrations()); err != nil {
		t.Fatalf("MigrateSchema(fresh current schema) error = %v", err)
	}
	assertCurrentSchema(t, database, CurrentSchemaRevision())
	assertExtractionCoordinationSchemaVerified(t, database)
	assertFoundationTableSet(t, database)
	for _, revision := range []int64{
		foundationSchemaRevision,
		conversationSchemaRevision,
		turnEvidenceSchemaRevision,
		transcriptRecallSchemaRevision,
		conversationRuntimeSchemaRevision,
		extractionCoordinationRevision,
	} {
		row := integrationJournalRowForRevision(t, database, revision)
		if row.State != string(MigrationCurrent) || row.AttemptCount != 1 {
			t.Fatalf("fresh revision %d journal = %#v", revision, row)
		}
	}
}

func TestRealSeekDBConversationRuntimeSchemaUpgradesRevisionFourRecoversPartialAndPersists(t *testing.T) {
	instance, config := openSchemaMigrationRuntime(t)
	closed := false
	defer func() {
		if !closed {
			closeRuntimeForIntegrationTest(t, instance, config.ShutdownLimit)
		}
	}()
	database := instance.SQL()
	migrations := BuiltinMigrations()
	if err := MigrateSchema(t.Context(), database, migrations[:4]); err != nil {
		t.Fatalf("MigrateSchema(revision four) error = %v", err)
	}
	assertCurrentSchema(t, database, migrations[3].Revision)
	assertTranscriptRecallSchemaVerified(t, database)
	insertIntegrationConversation(t, database, "runtime-upgrade-conversation", "runtime-upgrade-character", "character")
	insertIntegrationTurn(t, database, "runtime-upgrade-turn", "runtime-upgrade-conversation", "", 1)

	// SeekDB DDL commits independently. Simulate a process exit after the
	// runtime-event table exists but before the other two tables and revision
	// journal entry were committed.
	if _, err := database.ExecContext(t.Context(), conversationRuntimeSchema[0].ddl); err != nil {
		t.Fatalf("precreate partial conversation runtime schema: %v", err)
	}
	if err := MigrateSchema(t.Context(), database, migrations[:5]); err != nil {
		t.Fatalf("MigrateSchema(partial revision five) error = %v", err)
	}
	assertCurrentSchema(t, database, migrations[4].Revision)
	assertConversationRuntimeSchemaVerified(t, database)
	assertFoundationTableSet(t, database)
	row := integrationJournalRowForRevision(t, database, conversationRuntimeSchemaRevision)
	if row.State != string(MigrationCurrent) || row.AttemptCount != 1 {
		t.Fatalf("conversation runtime journal = %#v", row)
	}
	var upgradeTurnCount int
	if err := database.QueryRowContext(t.Context(), `
SELECT COUNT(*) FROM conversation_turns WHERE conversation_id = ? AND id = ?`,
		"runtime-upgrade-conversation", "runtime-upgrade-turn",
	).Scan(&upgradeTurnCount); err != nil {
		t.Fatalf("read revision-four Turn after revision-five upgrade: %v", err)
	}
	if upgradeTurnCount != 1 {
		t.Fatalf("revision-four Turn count after revision-five upgrade = %d, want 1", upgradeTurnCount)
	}
	assertConversationRuntimeConstraints(t, database)

	if err := MigrateSchema(t.Context(), database, migrations[:5]); err != nil {
		t.Fatalf("MigrateSchema(repeated revision five) error = %v", err)
	}
	if got := integrationJournalRowForRevision(t, database, conversationRuntimeSchemaRevision).AttemptCount; got != 1 {
		t.Fatalf("conversation runtime attempt count after repeat = %d, want 1", got)
	}

	closeRuntimeForIntegrationTest(t, instance, config.ShutdownLimit)
	closed = true
	restarted, err := Open(t.Context(), config)
	if err != nil {
		t.Fatalf("restart SeekDB conversation runtime: %v", err)
	}
	instance = restarted
	closed = false
	assertCurrentSchema(t, restarted.SQL(), migrations[4].Revision)
	assertConversationRuntimeSchemaVerified(t, restarted.SQL())
	for _, table := range []string{"turn_runtime_events", "lane_continuations", "context_windows"} {
		var count int
		if err := restarted.SQL().QueryRowContext(
			t.Context(), "SELECT COUNT(*) FROM "+table+" WHERE conversation_id = ?", "runtime-valid-conversation",
		).Scan(&count); err != nil {
			t.Fatalf("count persisted %s rows: %v", table, err)
		}
		if count != 1 {
			t.Fatalf("persisted %s rows = %d, want 1", table, count)
		}
	}
}

func TestRealSeekDBConversationRuntimeSchemaRejectsShapeDrift(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		mutateDDL func(string) string
	}{
		{
			name: "nullable column",
			mutateDDL: func(ddl string) string {
				return strings.Replace(ddl,
					"state VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NULL,",
					"state VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,", 1)
			},
		},
		{
			name: "wrong index",
			mutateDDL: func(ddl string) string {
				return strings.Replace(ddl,
					"KEY turn_runtime_events_type_created_idx (event_type, created_at_ms, sequence),",
					"KEY turn_runtime_events_type_created_idx (event_type, sequence, created_at_ms),", 1)
			},
		},
		{
			name: "same-name weak check",
			mutateDDL: func(ddl string) string {
				return strings.Replace(ddl, "    sequence > 0 AND", "    sequence >= 0 AND", 1)
			},
		},
		{
			name: "missing foreign key",
			mutateDDL: func(ddl string) string {
				return strings.Replace(ddl, `  CONSTRAINT turn_runtime_events_turn_fk FOREIGN KEY (conversation_id, turn_id)
    REFERENCES conversation_turns (conversation_id, id) ON UPDATE RESTRICT ON DELETE CASCADE,
`, "", 1)
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			instance, config := openSchemaMigrationRuntime(t)
			defer closeRuntimeForIntegrationTest(t, instance, config.ShutdownLimit)
			database := instance.SQL()
			migrations := BuiltinMigrations()
			if err := MigrateSchema(t.Context(), database, migrations[:4]); err != nil {
				t.Fatalf("MigrateSchema(revision four) error = %v", err)
			}
			mutated := testCase.mutateDDL(conversationRuntimeSchema[0].ddl)
			if mutated == conversationRuntimeSchema[0].ddl {
				t.Fatal("test setup did not mutate runtime event DDL")
			}
			if _, err := database.ExecContext(t.Context(), mutated); err != nil {
				t.Fatalf("precreate drifted runtime event table: %v", err)
			}
			err := MigrateSchema(t.Context(), database, migrations)
			if err == nil || !strings.Contains(err.Error(), "turn_runtime_events") {
				t.Fatalf("MigrateSchema(%s) error = %v", testCase.name, err)
			}
			row := integrationJournalRowForRevision(t, database, conversationRuntimeSchemaRevision)
			if row.State != string(MigrationFailed) || row.ErrorCode != "VERIFY_FAILED" || row.AttemptCount != 1 {
				t.Fatalf("%s conversation runtime journal = %#v", testCase.name, row)
			}
			status, readinessErr := CheckSchema(t.Context(), database, CurrentSchemaRevision())
			if !errors.Is(readinessErr, ErrSchemaNotCurrent) || status.State != SchemaNotCurrent ||
				status.Observed == nil || status.Observed.Revision.Number != conversationRuntimeSchemaRevision {
				t.Fatalf("%s conversation runtime readiness = %#v, %v", testCase.name, status, readinessErr)
			}
		})
	}
}

func TestRealSeekDBExtractionCoordinationSchemaUpgradesRevisionFiveRecoversPartialAndPersists(t *testing.T) {
	instance, config := openSchemaMigrationRuntime(t)
	closed := false
	defer func() {
		if !closed {
			closeRuntimeForIntegrationTest(t, instance, config.ShutdownLimit)
		}
	}()
	database := instance.SQL()
	migrations := BuiltinMigrations()
	if err := MigrateSchema(t.Context(), database, migrations[:5]); err != nil {
		t.Fatalf("MigrateSchema(revision five) error = %v", err)
	}
	assertCurrentSchema(t, database, migrations[4].Revision)
	assertConversationRuntimeSchemaVerified(t, database)

	const conversationID = "extraction-upgrade-conversation"
	insertIntegrationConversation(t, database, conversationID, "extraction-upgrade-character", "character")
	insertIntegrationExtractionTurn(t, database, extractionTurnFixture{
		id: "extraction-upgrade-pending", conversationID: conversationID, sequence: 1,
		status: "completed", origin: "user", state: "pending",
	})
	insertIntegrationExtractionTurn(t, database, extractionTurnFixture{
		id: "extraction-upgrade-claimed", conversationID: conversationID, sequence: 2,
		status: "completed", origin: "user", state: "claimed",
		claimID: "claim-upgrade", leaseOwner: "worker-upgrade", leaseExpiresAtMS: 100,
		attemptCount: 1, updatedAtMS: 200,
	})
	insertIntegrationExtractionTurn(t, database, extractionTurnFixture{
		id: "extraction-upgrade-failed", conversationID: conversationID, sequence: 3,
		status: "completed", origin: "user", state: "failed", attemptCount: 3,
		errorCode: "lease_expired", errorMessage: "extraction lease expired after maximum attempts",
	})

	// Revision-six DDL is implicitly committed one statement at a time. Leave
	// the exact CHECK and first index behind as if the process exited before the
	// second index and journal transition were committed.
	for index, statement := range extractionCoordinationDDL[:2] {
		if _, err := database.ExecContext(t.Context(), statement); err != nil {
			t.Fatalf("precreate partial extraction coordination DDL %d: %v", index+1, err)
		}
	}
	if err := MigrateSchema(t.Context(), database, migrations); err != nil {
		t.Fatalf("MigrateSchema(partial revision six) error = %v", err)
	}
	assertCurrentSchema(t, database, CurrentSchemaRevision())
	assertExtractionCoordinationSchemaVerified(t, database)
	assertFoundationTableSet(t, database)
	for _, forbidden := range []string{
		"personal_memories", "memory_context_coverages", "extraction_batches", "extraction_batch_turns",
	} {
		if got := integrationTableCount(t, database, forbidden); got != 0 {
			t.Fatalf("revision six created forbidden table %s", forbidden)
		}
	}
	row := integrationJournalRowForRevision(t, database, extractionCoordinationRevision)
	if row.State != string(MigrationCurrent) || row.AttemptCount != 1 {
		t.Fatalf("extraction coordination journal = %#v", row)
	}
	var pending, claimed, failed int
	if err := database.QueryRowContext(t.Context(), `
SELECT
  SUM(extraction_state = 'pending'),
  SUM(extraction_state = 'claimed'),
  SUM(extraction_state = 'failed')
FROM conversation_turns
WHERE conversation_id = ?`, conversationID).Scan(&pending, &claimed, &failed); err != nil {
		t.Fatalf("read revision-five extraction rows after upgrade: %v", err)
	}
	if pending != 1 || claimed != 1 || failed != 1 {
		t.Fatalf("preserved extraction states = pending:%d claimed:%d failed:%d", pending, claimed, failed)
	}
	assertExtractionCoordinationConstraints(t, database, conversationID, 10)

	if err := MigrateSchema(t.Context(), database, migrations); err != nil {
		t.Fatalf("MigrateSchema(repeated revision six) error = %v", err)
	}
	if got := integrationJournalRowForRevision(t, database, extractionCoordinationRevision).AttemptCount; got != 1 {
		t.Fatalf("extraction coordination attempt count after repeat = %d, want 1", got)
	}

	closeRuntimeForIntegrationTest(t, instance, config.ShutdownLimit)
	closed = true
	restarted, err := Open(t.Context(), config)
	if err != nil {
		t.Fatalf("restart SeekDB extraction coordination runtime: %v", err)
	}
	instance = restarted
	closed = false
	assertCurrentSchema(t, restarted.SQL(), CurrentSchemaRevision())
	assertExtractionCoordinationSchemaVerified(t, restarted.SQL())
	if got := integrationJournalRowForRevision(t, restarted.SQL(), extractionCoordinationRevision).AttemptCount; got != 1 {
		t.Fatalf("extraction coordination attempt count after restart = %d, want 1", got)
	}
	if err := restarted.SQL().QueryRowContext(t.Context(), `
SELECT COUNT(*) FROM conversation_turns WHERE conversation_id = ?`, conversationID).Scan(&pending); err != nil {
		t.Fatalf("count persisted extraction rows: %v", err)
	}
	if pending != 3 {
		t.Fatalf("persisted extraction row count = %d, want 3", pending)
	}
}

func TestRealSeekDBExtractionCoordinationSchemaRejectsWeakCheckAndWrongIndex(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		preapply func(*testing.T, *sql.DB)
	}{
		{
			name: "same-name weak check",
			preapply: func(t *testing.T, database *sql.DB) {
				weak := strings.Replace(
					extractionCoordinationDDL[0],
					"extraction_attempt_count < 3",
					"extraction_attempt_count <= 3",
					1,
				)
				if weak == extractionCoordinationDDL[0] {
					t.Fatal("test setup did not weaken extraction CHECK")
				}
				if _, err := database.ExecContext(t.Context(), weak); err != nil {
					t.Fatalf("precreate weak extraction CHECK: %v", err)
				}
			},
		},
		{
			name: "wrong lease index",
			preapply: func(t *testing.T, database *sql.DB) {
				if _, err := database.ExecContext(t.Context(), `
CREATE INDEX conversation_turns_extraction_lease_idx
  ON conversation_turns(conversation_id, extraction_state, status, extraction_lease_expires_at_ms, sequence)`); err != nil {
					t.Fatalf("precreate wrong extraction lease index: %v", err)
				}
			},
		},
		{
			name: "wrong batch index",
			preapply: func(t *testing.T, database *sql.DB) {
				if _, err := database.ExecContext(t.Context(), `
CREATE INDEX conversation_turns_extraction_batch_idx
  ON conversation_turns(extraction_claim_id, extraction_state, extraction_lease_owner, sequence, conversation_id)`); err != nil {
					t.Fatalf("precreate wrong extraction batch index: %v", err)
				}
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			instance, config := openSchemaMigrationRuntime(t)
			defer closeRuntimeForIntegrationTest(t, instance, config.ShutdownLimit)
			database := instance.SQL()
			migrations := BuiltinMigrations()
			if err := MigrateSchema(t.Context(), database, migrations[:5]); err != nil {
				t.Fatalf("MigrateSchema(revision five) error = %v", err)
			}
			testCase.preapply(t, database)
			err := MigrateSchema(t.Context(), database, migrations)
			if err == nil || !strings.Contains(err.Error(), "conversation_turns") {
				t.Fatalf("MigrateSchema(%s) error = %v", testCase.name, err)
			}
			row := integrationJournalRowForRevision(t, database, extractionCoordinationRevision)
			if row.State != string(MigrationFailed) || row.ErrorCode != "VERIFY_FAILED" || row.AttemptCount != 1 {
				t.Fatalf("%s extraction coordination journal = %#v", testCase.name, row)
			}
			status, readinessErr := CheckSchema(t.Context(), database, CurrentSchemaRevision())
			if !errors.Is(readinessErr, ErrSchemaNotCurrent) || status.State != SchemaNotCurrent ||
				status.Observed == nil || status.Observed.Revision.Number != extractionCoordinationRevision {
				t.Fatalf("%s extraction readiness = %#v, %v", testCase.name, status, readinessErr)
			}
		})
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

func assertTurnEvidenceSchemaVerified(t *testing.T, database *sql.DB) {
	t.Helper()
	connection, err := database.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if err := BuiltinMigrations()[2].Verify(t.Context(), connection); err != nil {
		t.Fatalf("verify turn evidence schema: %v", err)
	}
}

func assertTranscriptRecallSchemaVerified(t *testing.T, database *sql.DB) {
	t.Helper()
	connection, err := database.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if err := BuiltinMigrations()[3].Verify(t.Context(), connection); err != nil {
		t.Fatalf("verify transcript recall schema: %v", err)
	}
}

func assertConversationRuntimeSchemaVerified(t *testing.T, database *sql.DB) {
	t.Helper()
	connection, err := database.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if err := BuiltinMigrations()[4].Verify(t.Context(), connection); err != nil {
		t.Fatalf("verify conversation runtime schema: %v", err)
	}
}

func assertExtractionCoordinationSchemaVerified(t *testing.T, database *sql.DB) {
	t.Helper()
	connection, err := database.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if err := BuiltinMigrations()[5].Verify(t.Context(), connection); err != nil {
		t.Fatalf("verify extraction coordination schema: %v", err)
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
	for _, table := range turnEvidenceSchema {
		if got := integrationTableCount(t, database, table.name); got != 1 {
			t.Errorf("turn evidence table %s count = %d, want 1", table.name, got)
		}
	}
	for _, table := range conversationRuntimeSchema {
		if got := integrationTableCount(t, database, table.name); got != 1 {
			t.Errorf("conversation runtime table %s count = %d, want 1", table.name, got)
		}
	}
	var businessTableCount int
	if err := database.QueryRowContext(t.Context(), `
SELECT COUNT(*)
FROM information_schema.tables
WHERE table_schema = DATABASE() AND table_name <> 'schema_revisions'`).Scan(&businessTableCount); err != nil {
		t.Fatal(err)
	}
	want := len(foundationSchema) + len(conversationSchema) + len(turnEvidenceSchema) + len(conversationRuntimeSchema)
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

func assertConversationRuntimeConstraints(t *testing.T, database *sql.DB) {
	t.Helper()
	const (
		validConversationID = "runtime-valid-conversation"
		validTurnID         = "runtime-valid-turn"
	)
	hashA := strings.Repeat("a", 64)
	hashB := strings.Repeat("b", 64)
	hashC := strings.Repeat("c", 64)
	insertIntegrationConversation(t, database, validConversationID, "runtime-valid-character", "character")
	insertIntegrationTurn(t, database, validTurnID, validConversationID, "", 1)
	if _, err := database.ExecContext(t.Context(), `
INSERT INTO turn_runtime_events(
  id, conversation_id, turn_id, sequence, event_type, state, code, metadata_json, created_at_ms
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"runtime-valid-event", validConversationID, validTurnID, 1,
		"模型调用", "completed", nil, `{"usage":{"input":1}}`, 1,
	); err != nil {
		t.Fatalf("insert valid runtime event: %v", err)
	}
	if _, err := database.ExecContext(t.Context(), `
INSERT INTO lane_continuations(
  conversation_id, lane, previous_response_id, request_shape_hash,
  input_prefix_hash, response_item_hash, window_revision, updated_at_ms
) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		validConversationID, "respond", "响应-一", hashA, hashB, hashC, 1, 1,
	); err != nil {
		t.Fatalf("insert valid lane continuation: %v", err)
	}
	if _, err := database.ExecContext(t.Context(), `
INSERT INTO context_windows(
  conversation_id, lane, window_number, first_window_id, previous_window_id,
  window_id, observed_prefill_tokens, estimated_prefill_tokens, last_trigger,
  failure_count, prompt_window_revision, updated_at_ms
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		validConversationID, "respond", 0, "window-first", nil,
		"window-current", nil, 1024, "模型完成", 0, 1, 1,
	); err != nil {
		t.Fatalf("insert valid context window: %v", err)
	}

	insertIntegrationConversation(t, database, "runtime-other-conversation", "runtime-other-character", "character")
	insertIntegrationTurn(t, database, "runtime-other-turn", "runtime-other-conversation", "", 1)
	expectSeekDBConstraintError(t, database, `
INSERT INTO turn_runtime_events(
  id, conversation_id, turn_id, sequence, event_type, state, code, metadata_json, created_at_ms
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"runtime-crossed-event", validConversationID, "runtime-other-turn", 2,
		"model", nil, nil, `{}`, 1,
	)
	for _, invalid := range []struct {
		id        string
		sequence  int
		eventType string
		metadata  string
	}{
		{id: "runtime-zero-sequence", sequence: 0, eventType: "model", metadata: `{}`},
		{id: "runtime-control-event", sequence: 2, eventType: "model\ncall", metadata: `{}`},
		{id: "runtime-array-metadata", sequence: 2, eventType: "model", metadata: `[]`},
		{id: "runtime-duplicate-sequence", sequence: 1, eventType: "model", metadata: `{}`},
	} {
		expectSeekDBConstraintError(t, database, `
INSERT INTO turn_runtime_events(
  id, conversation_id, turn_id, sequence, event_type, state, code, metadata_json, created_at_ms
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			invalid.id, validConversationID, validTurnID, invalid.sequence,
			invalid.eventType, nil, nil, invalid.metadata, 1,
		)
	}

	for _, invalid := range []struct {
		lane               string
		previousResponseID string
		requestHash        string
		windowRevision     int
	}{
		{lane: "unknown", previousResponseID: "response", requestHash: hashA, windowRevision: 1},
		{lane: "compact", previousResponseID: "response\n", requestHash: hashA, windowRevision: 1},
		{lane: "compact", previousResponseID: "response", requestHash: strings.Repeat("A", 64), windowRevision: 1},
		{lane: "compact", previousResponseID: "response", requestHash: hashA, windowRevision: 0},
	} {
		expectSeekDBConstraintError(t, database, `
INSERT INTO lane_continuations(
  conversation_id, lane, previous_response_id, request_shape_hash,
  input_prefix_hash, response_item_hash, window_revision, updated_at_ms
) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			validConversationID, invalid.lane, invalid.previousResponseID,
			invalid.requestHash, hashB, hashC, invalid.windowRevision, 1,
		)
	}

	for _, invalid := range []struct {
		lane           string
		windowNumber   int
		previousWindow any
		observed       any
		lastTrigger    string
		promptRevision int
	}{
		{lane: "unknown", windowNumber: 0, observed: nil, lastTrigger: "created", promptRevision: 1},
		{lane: "compact", windowNumber: -1, observed: nil, lastTrigger: "created", promptRevision: 1},
		{lane: "compact", windowNumber: 0, previousWindow: "bad\nwindow", observed: nil, lastTrigger: "created", promptRevision: 1},
		{lane: "compact", windowNumber: 0, observed: -1, lastTrigger: "created", promptRevision: 1},
		{lane: "compact", windowNumber: 0, observed: nil, lastTrigger: "", promptRevision: 1},
		{lane: "compact", windowNumber: 0, observed: nil, lastTrigger: "created", promptRevision: 0},
	} {
		expectSeekDBConstraintError(t, database, `
INSERT INTO context_windows(
  conversation_id, lane, window_number, first_window_id, previous_window_id,
  window_id, observed_prefill_tokens, estimated_prefill_tokens, last_trigger,
  failure_count, prompt_window_revision, updated_at_ms
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			validConversationID, invalid.lane, invalid.windowNumber, "first-window", invalid.previousWindow,
			"current-window", invalid.observed, nil, invalid.lastTrigger, 0, invalid.promptRevision, 1,
		)
	}

	const cascadeConversationID = "runtime-cascade-conversation"
	insertIntegrationConversation(t, database, cascadeConversationID, "runtime-cascade-character", "character")
	insertIntegrationTurn(t, database, "runtime-cascade-turn", cascadeConversationID, "", 1)
	if _, err := database.ExecContext(t.Context(), `
INSERT INTO turn_runtime_events(
  id, conversation_id, turn_id, sequence, event_type, state, code, metadata_json, created_at_ms
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"runtime-cascade-event", cascadeConversationID, "runtime-cascade-turn", 1,
		"model", nil, nil, `{}`, 1,
	); err != nil {
		t.Fatalf("insert cascading runtime event: %v", err)
	}
	if _, err := database.ExecContext(t.Context(), `
INSERT INTO lane_continuations(
  conversation_id, lane, previous_response_id, request_shape_hash,
  input_prefix_hash, response_item_hash, window_revision, updated_at_ms
) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		cascadeConversationID, "respond", "response", hashA, hashB, hashC, 1, 1,
	); err != nil {
		t.Fatalf("insert cascading lane continuation: %v", err)
	}
	if _, err := database.ExecContext(t.Context(), `
INSERT INTO context_windows(
  conversation_id, lane, window_number, first_window_id, previous_window_id,
  window_id, observed_prefill_tokens, estimated_prefill_tokens, last_trigger,
  failure_count, prompt_window_revision, updated_at_ms
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		cascadeConversationID, "respond", 0, "first-window", nil,
		"current-window", nil, nil, "created", 0, 1, 1,
	); err != nil {
		t.Fatalf("insert cascading context window: %v", err)
	}
	if _, err := database.ExecContext(t.Context(), "DELETE FROM conversations WHERE id = ?", cascadeConversationID); err != nil {
		t.Fatalf("delete conversation runtime cascade root: %v", err)
	}
	for _, table := range []string{"turn_runtime_events", "lane_continuations", "context_windows"} {
		var count int
		if err := database.QueryRowContext(
			t.Context(), "SELECT COUNT(*) FROM "+table+" WHERE conversation_id = ?", cascadeConversationID,
		).Scan(&count); err != nil {
			t.Fatalf("count cascaded %s rows: %v", table, err)
		}
		if count != 0 {
			t.Fatalf("%s rows after conversation cascade = %d, want 0", table, count)
		}
	}
}

const integrationTurnInsertPrefix = `
INSERT INTO conversation_turns(
  id, conversation_id, message_id, sequence, status, origin,
  error_code, error_message, error_retryable,
  extraction_state, extraction_claim_id, extraction_lease_owner, extraction_lease_expires_at_ms,
  extraction_attempt_count, extraction_next_attempt_at_ms, extraction_error_code, extraction_error_message,
  created_at_ms, updated_at_ms
) VALUES `

const integrationTurnInsertValues = `(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

const integrationTurnInsertSQL = integrationTurnInsertPrefix + integrationTurnInsertValues

type extractionTurnFixture struct {
	id               string
	conversationID   string
	sequence         int64
	status           string
	origin           string
	state            string
	claimID          string
	leaseOwner       string
	leaseExpiresAtMS int64
	attemptCount     int64
	nextAttemptAtMS  int64
	errorCode        string
	errorMessage     string
	updatedAtMS      int64
}

func extractionTurnArguments(fixture extractionTurnFixture) []any {
	nullable := func(value string) any {
		if value == "" {
			return nil
		}
		return value
	}
	var leaseExpiresAt any
	if fixture.leaseExpiresAtMS > 0 {
		leaseExpiresAt = fixture.leaseExpiresAtMS
	}
	updatedAtMS := fixture.updatedAtMS
	if updatedAtMS == 0 {
		updatedAtMS = 1
	}
	return []any{
		fixture.id, fixture.conversationID, nil, fixture.sequence,
		fixture.status, fixture.origin, nil, nil, nil,
		fixture.state, nullable(fixture.claimID), nullable(fixture.leaseOwner), leaseExpiresAt,
		fixture.attemptCount, fixture.nextAttemptAtMS,
		nullable(fixture.errorCode), nullable(fixture.errorMessage),
		int64(1), updatedAtMS,
	}
}

func insertIntegrationExtractionTurn(t *testing.T, database *sql.DB, fixture extractionTurnFixture) {
	t.Helper()
	if _, err := database.ExecContext(t.Context(), integrationTurnInsertSQL, extractionTurnArguments(fixture)...); err != nil {
		t.Fatalf("insert extraction Turn %s: %v", fixture.id, err)
	}
}

func assertExtractionCoordinationConstraints(
	t *testing.T,
	database *sql.DB,
	conversationID string,
	firstSequence int64,
) {
	t.Helper()
	testCases := []struct {
		name    string
		fixture extractionTurnFixture
	}{
		{
			name: "pending requires completed user Turn",
			fixture: extractionTurnFixture{
				status: "planning", origin: "user", state: "pending",
			},
		},
		{
			name: "ineligible has no attempts",
			fixture: extractionTurnFixture{
				status: "completed", origin: "user", state: "ineligible", attemptCount: 1,
			},
		},
		{
			name: "pending remains below maximum attempts",
			fixture: extractionTurnFixture{
				status: "completed", origin: "user", state: "pending", attemptCount: 3,
			},
		},
		{
			name: "claimed has at least one attempt",
			fixture: extractionTurnFixture{
				status: "completed", origin: "user", state: "claimed",
				claimID: "claim-zero-attempt", leaseOwner: "worker", leaseExpiresAtMS: 100,
			},
		},
		{
			name: "claimed has no pending retry time",
			fixture: extractionTurnFixture{
				status: "completed", origin: "user", state: "claimed",
				claimID: "claim-next-attempt", leaseOwner: "worker", leaseExpiresAtMS: 100,
				attemptCount: 1, nextAttemptAtMS: 10,
			},
		},
		{
			name: "processed has no extraction error",
			fixture: extractionTurnFixture{
				status: "completed", origin: "user", state: "processed", attemptCount: 1,
				errorCode: "unexpected", errorMessage: "unexpected",
			},
		},
		{
			name: "failed has a paired error",
			fixture: extractionTurnFixture{
				status: "completed", origin: "user", state: "failed", attemptCount: 1,
			},
		},
		{
			name: "claim identity is clean",
			fixture: extractionTurnFixture{
				status: "completed", origin: "user", state: "claimed",
				claimID: " ", leaseOwner: "worker", leaseExpiresAtMS: 100, attemptCount: 1,
			},
		},
	}
	for index, testCase := range testCases {
		fixture := testCase.fixture
		fixture.id = fmt.Sprintf("invalid-extraction-%02d", index+1)
		fixture.conversationID = conversationID
		fixture.sequence = firstSequence + int64(index)
		t.Run(testCase.name, func(t *testing.T) {
			expectSeekDBConstraintError(t, database, integrationTurnInsertSQL, extractionTurnArguments(fixture)...)
		})
	}
}

const integrationMessageInsertPrefix = `
INSERT INTO conversation_messages(
  id, conversation_id, turn_id, sequence, role, content, expression_parts, created_at_ms
) VALUES `

const integrationMessageInsertValues = `(?, ?, ?, ?, ?, ?, ?, ?)`

const integrationMessageInsertSQL = integrationMessageInsertPrefix + integrationMessageInsertValues

func insertIntegrationTranscriptBackfillMessages(
	t *testing.T,
	database *sql.DB,
	conversationID string,
	messageCount, batchSize int,
) {
	t.Helper()
	if messageCount <= 0 || batchSize <= 0 {
		t.Fatalf("invalid transcript backfill fixture bounds: messages=%d batch=%d", messageCount, batchSize)
	}
	for batchStart := 0; batchStart < messageCount; batchStart += batchSize {
		batchEnd := min(batchStart+batchSize, messageCount)
		rows := batchEnd - batchStart
		turnArguments := make([]any, 0, rows*19)
		messageArguments := make([]any, 0, rows*8)
		for offset := batchStart; offset < batchEnd; offset++ {
			sequence := int64(offset + 1)
			turnID := fmt.Sprintf("transcript-backfill-turn-%04d", sequence)
			messageID := fmt.Sprintf("transcript-backfill-message-%04d", sequence)
			turnArguments = append(turnArguments,
				turnID, conversationID, nil, sequence,
				"completed", "user", nil, nil, nil,
				"ineligible", nil, nil, nil, 0, 0, nil, nil,
				sequence, sequence,
			)
			messageArguments = append(messageArguments,
				messageID, conversationID, turnID, sequence, "user",
				fmt.Sprintf("第 %d 条既有对话记录：苍之彼方的四重奏与海边约定", sequence),
				`[]`, sequence,
			)
		}
		transaction, err := database.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatalf("begin transcript backfill fixture batch %d: %v", batchStart/batchSize, err)
		}
		turnStatement := integrationTurnInsertPrefix + strings.TrimSuffix(
			strings.Repeat(integrationTurnInsertValues+",", rows), ",",
		)
		if _, err := transaction.ExecContext(t.Context(), turnStatement, turnArguments...); err != nil {
			_ = transaction.Rollback()
			t.Fatalf("insert transcript backfill turns batch %d: %v", batchStart/batchSize, err)
		}
		messageStatement := integrationMessageInsertPrefix + strings.TrimSuffix(
			strings.Repeat(integrationMessageInsertValues+",", rows), ",",
		)
		if _, err := transaction.ExecContext(t.Context(), messageStatement, messageArguments...); err != nil {
			_ = transaction.Rollback()
			t.Fatalf("insert transcript backfill messages batch %d: %v", batchStart/batchSize, err)
		}
		if err := transaction.Commit(); err != nil {
			t.Fatalf("commit transcript backfill fixture batch %d: %v", batchStart/batchSize, err)
		}
	}
}

func assertTranscriptBackfillMatchCount(
	t *testing.T,
	database *sql.DB,
	conversationID, query string,
	want int,
) {
	t.Helper()
	var count int
	if err := database.QueryRowContext(t.Context(), `
SELECT COUNT(*) FROM conversation_messages
WHERE conversation_id = ?
  AND MATCH(content) AGAINST(? IN NATURAL LANGUAGE MODE)`, conversationID, query).Scan(&count); err != nil {
		t.Fatalf("query transcript backfill FULLTEXT index: %v", err)
	}
	if count != want {
		t.Fatalf("transcript backfill FULLTEXT match count = %d, want %d", count, want)
	}
}

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
	var storedMessageID any
	if externalMessageID != "" {
		storedMessageID = externalMessageID
	}
	if _, err := database.ExecContext(t.Context(), integrationTurnInsertSQL,
		id, conversationID, storedMessageID, sequence,
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
