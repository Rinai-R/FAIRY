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
	if len(first) != 13 || len(second) != 13 {
		t.Fatalf("builtin migration counts = %d and %d, want 13", len(first), len(second))
	}
	foundation := Revision{Number: foundationSchemaRevision, Checksum: foundationSchemaChecksum()}
	conversation := Revision{Number: conversationSchemaRevision, Checksum: conversationSchemaChecksum()}
	turnEvidence := Revision{Number: turnEvidenceSchemaRevision, Checksum: turnEvidenceSchemaChecksum()}
	transcriptRecall := Revision{Number: transcriptRecallSchemaRevision, Checksum: transcriptRecallSchemaChecksum()}
	conversationRuntime := Revision{Number: conversationRuntimeSchemaRevision, Checksum: conversationRuntimeSchemaChecksum()}
	extractionCoordination := Revision{Number: extractionCoordinationRevision, Checksum: extractionCoordinationChecksum()}
	cognitiveRecords := Revision{Number: cognitiveRecordsSchemaRevision, Checksum: cognitiveRecordsSchemaChecksum()}
	duplicateRevalidation := Revision{Number: duplicateRevalidationRevision, Checksum: duplicateRevalidationSchemaChecksum()}
	socialFeedback := Revision{Number: socialFeedbackEventsRevision, Checksum: socialFeedbackEventsSchemaChecksum()}
	stickerCatalog := Revision{Number: stickerCatalogSchemaRevision, Checksum: stickerCatalogSchemaChecksum()}
	toolExecution := Revision{Number: toolExecutionLedgerRevision, Checksum: toolExecutionLedgerSchemaChecksum()}
	observabilityHistory := Revision{Number: observabilityHistoryRevision, Checksum: observabilityHistorySchemaChecksum()}
	pluginPersistence := Revision{Number: pluginPersistenceRevision, Checksum: pluginPersistenceSchemaChecksum()}
	if current := CurrentSchemaRevision(); current != pluginPersistence {
		t.Fatalf("current schema revision = %#v, want %#v", current, pluginPersistence)
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
	if first[3].Revision != transcriptRecall || first[3].Name != "create-conversation-message-fulltext-index" {
		t.Fatalf("transcript recall migration = %#v, want revision %#v", first[3], transcriptRecall)
	}
	if first[4].Revision != conversationRuntime || first[4].Name != "create-conversation-runtime-schema" {
		t.Fatalf("conversation runtime migration = %#v, want revision %#v", first[4], conversationRuntime)
	}
	if first[5].Revision != extractionCoordination || first[5].Name != "strengthen-extraction-coordination-schema" {
		t.Fatalf("extraction coordination migration = %#v, want revision %#v", first[5], extractionCoordination)
	}
	if first[6].Revision != cognitiveRecords || first[6].Name != "create-cognitive-records-schema" {
		t.Fatalf("cognitive records migration = %#v, want revision %#v", first[6], cognitiveRecords)
	}
	if first[7].Revision != duplicateRevalidation || first[7].Name != "index-personal-memory-duplicates" {
		t.Fatalf("duplicate revalidation migration = %#v, want revision %#v", first[7], duplicateRevalidation)
	}
	if first[8].Revision != socialFeedback || first[8].Name != "create-social-memory-feedback-events" {
		t.Fatalf("social feedback events migration = %#v, want revision %#v", first[8], socialFeedback)
	}
	if first[9].Revision != stickerCatalog || first[9].Name != "create-sticker-and-expression-delivery-schema" {
		t.Fatalf("sticker catalog migration = %#v, want revision %#v", first[9], stickerCatalog)
	}
	if first[10].Revision != toolExecution || first[10].Name != "create-tool-execution-ledger-schema" {
		t.Fatalf("tool execution ledger migration = %#v, want revision %#v", first[10], toolExecution)
	}
	if first[11].Revision != observabilityHistory || first[11].Name != "create-observability-history-schema" {
		t.Fatalf("observability history migration = %#v, want revision %#v", first[11], observabilityHistory)
	}
	if first[12].Revision != pluginPersistence || first[12].Name != "create-plugin-persistence-schema" {
		t.Fatalf("plugin persistence migration = %#v, want revision %#v", first[12], pluginPersistence)
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
	first[3].Name = "mutated-transcript-recall"
	first[4].Revision.Number = 101
	first[5].Name = "mutated-extraction-coordination"
	first[6].Revision.Number = 102
	first[7].Name = "mutated-duplicate-revalidation"
	first[8].Name = "mutated-social-feedback-events"
	first[9].Name = "mutated-sticker-catalog"
	first[10].Name = "mutated-tool-execution-ledger"
	first[11].Name = "mutated-observability-history"
	first[12].Name = "mutated-plugin-persistence"
	if second[0].Name != "create-foundation-schema" || second[0].Revision != foundation ||
		second[1].Name != "create-conversation-schema" || second[1].Revision != conversation ||
		second[2].Name != "create-turn-evidence-schema" || second[2].Revision != turnEvidence ||
		second[3].Name != "create-conversation-message-fulltext-index" || second[3].Revision != transcriptRecall ||
		second[4].Name != "create-conversation-runtime-schema" || second[4].Revision != conversationRuntime ||
		second[5].Name != "strengthen-extraction-coordination-schema" || second[5].Revision != extractionCoordination ||
		second[6].Name != "create-cognitive-records-schema" || second[6].Revision != cognitiveRecords ||
		second[7].Name != "index-personal-memory-duplicates" || second[7].Revision != duplicateRevalidation ||
		second[8].Name != "create-social-memory-feedback-events" || second[8].Revision != socialFeedback ||
		second[9].Name != "create-sticker-and-expression-delivery-schema" || second[9].Revision != stickerCatalog ||
		second[10].Name != "create-tool-execution-ledger-schema" || second[10].Revision != toolExecution ||
		second[11].Name != "create-observability-history-schema" || second[11].Revision != observabilityHistory ||
		second[12].Name != "create-plugin-persistence-schema" || second[12].Revision != pluginPersistence {
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
	if got := hex.EncodeToString(transcriptRecall.Checksum[:]); got != "b40266f72516a16b74e237c8b52366cc0bab99f77749b449e6c4ef3f444a33f6" {
		t.Fatalf("transcript recall checksum = %s, update requires an explicit revision decision", got)
	}
	if got := hex.EncodeToString(conversationRuntime.Checksum[:]); got != "6d93c442f7d95deeb631397da210b818926b9d5cd496d2375d9adc22333c801c" {
		t.Fatalf("conversation runtime checksum = %s, update requires an explicit revision decision", got)
	}
	if got := hex.EncodeToString(extractionCoordination.Checksum[:]); got != "4984285a4efd211d0a0e1dc807237ae4fdb57ae3a7c2eaacd490f56534838935" {
		t.Fatalf("extraction coordination checksum = %s, update requires an explicit revision decision", got)
	}
	if got := hex.EncodeToString(cognitiveRecords.Checksum[:]); got != "93de00e817d242133b8a9227e5a40e512fde004b262f9dbae013fa1efdcae708" {
		t.Fatalf("cognitive records checksum = %s, update requires an explicit revision decision", got)
	}
	if got := hex.EncodeToString(duplicateRevalidation.Checksum[:]); got != "a6b91fc39ba1e5a46ff80fdbe0c85ba76177b6c9466b5f0f61d8cea6f31c82f6" {
		t.Fatalf("duplicate revalidation checksum = %s, update requires an explicit revision decision", got)
	}
	if got := hex.EncodeToString(socialFeedback.Checksum[:]); got != "b7b9325c52f070b5e54c57e2e4f5a14854782052c332334fa794edf305199dc7" {
		t.Fatalf("social feedback events checksum = %s, update requires an explicit revision decision", got)
	}
	if got := hex.EncodeToString(stickerCatalog.Checksum[:]); got != "9bd51976b0ebe8993f774fff2d34bfa2773311956c0e50e0c2c0e9ff87a606db" {
		t.Fatalf("sticker catalog checksum = %s, update requires an explicit revision decision", got)
	}
	if got := hex.EncodeToString(toolExecution.Checksum[:]); got != "1d26c56f049f7e7335f5f44f4e8395554ad69fba7a579aee257a3258d3b1d843" {
		t.Fatalf("tool execution ledger checksum = %s, update requires an explicit revision decision", got)
	}
	if got := hex.EncodeToString(observabilityHistory.Checksum[:]); got != "78bf83cce13800083ab7cdc442064f0f743809a9ae38431670b4858e96c37f00" {
		t.Fatalf("observability history checksum = %s, update requires an explicit revision decision", got)
	}
	if got := hex.EncodeToString(pluginPersistence.Checksum[:]); got != "2034be1c5a466974097144f16c4aa66093018d408b2efc0fc1229b749ae5c88e" {
		t.Fatalf("plugin persistence checksum = %s, update requires an explicit revision decision", got)
	}
}

func TestExtractionCoordinationRevisionStrengthensOnlyConversationTurns(t *testing.T) {
	if len(extractionCoordinationDDL) != 3 {
		t.Fatalf("extraction coordination DDL count = %d, want 3", len(extractionCoordinationDDL))
	}
	forbidden := []string{
		"personal_memories", "memory_context_coverages", "extraction_batches",
		"extraction_batch_turns", "feedback_events",
	}
	for index, statement := range extractionCoordinationDDL {
		lower := strings.ToLower(statement)
		if !strings.Contains(lower, "conversation_turns") {
			t.Errorf("extraction coordination DDL %d does not target conversation_turns", index+1)
		}
		for _, token := range forbidden {
			if strings.Contains(lower, token) {
				t.Errorf("extraction coordination DDL %d contains forbidden table %q", index+1, token)
			}
		}
	}

	turns := extractionCoordinationTurnTable()
	if turns.name != "conversation_turns" || len(turns.columns) != len(conversationSchema[3].columns) ||
		len(turns.foreignKeys) != len(conversationSchema[3].foreignKeys) {
		t.Fatalf("revision-six Turn shape changed columns or foreign keys: %#v", turns)
	}
	if !schemaHasIndex(turns, ascendingBTreeIndex(
		extractionLeaseIndexName, false,
		"conversation_id", "status", "extraction_state", "extraction_lease_expires_at_ms", "sequence",
	)) {
		t.Fatal("revision-six Turn shape lacks conversation-scoped extraction lease index")
	}
	if !schemaHasIndex(turns, ascendingBTreeIndex(
		extractionBatchIndexName, false,
		"extraction_claim_id", "extraction_lease_owner", "extraction_state", "sequence", "conversation_id",
	)) {
		t.Fatal("revision-six Turn shape lacks worker-owned batch index")
	}
	if len(turns.checks) != len(conversationSchema[3].checks)+1 ||
		turns.checks[len(turns.checks)-1].name != extractionStateMachineCheckName {
		t.Fatalf("revision-six Turn CHECKs = %#v", turns.checks)
	}
	if len(conversationSchema[3].indexes) != 7 || len(conversationSchema[3].checks) != 1 {
		t.Fatal("revision six mutated the immutable revision-two Turn contract")
	}
}

func TestCognitiveRecordsSchemaDefinesOnlyDirectRecordsAndCoverage(t *testing.T) {
	wantTables := []string{
		"personal_memories",
		"memory_context_coverages",
		"social_memory_entries",
		"knowledge_entries",
	}
	assertPortableSchemaTables(t, cognitiveRecordsSchema[:], wantTables)
	for _, table := range cognitiveRecordsSchema {
		lower := strings.ToLower(table.ddl)
		for _, forbidden := range []string{
			"social_memory_feedback_events", "feedback_events", "personal_memory_evidence",
			"extraction_batches", "vector(512)", "embedding_v2", "embedding_model_id",
		} {
			if strings.Contains(lower, forbidden) {
				t.Errorf("cognitive table %s contains out-of-scope token %q", table.name, forbidden)
			}
		}
	}

	personal := cognitiveRecordsSchema[0]
	if embedding := schemaColumnNamed(t, personal, "embedding"); embedding.columnType != "vector(1024)" || !embedding.nullable {
		t.Fatalf("personal embedding = %#v", embedding)
	}
	if !schemaHasIndex(personal, ascendingBTreeIndex(
		"personal_memories_scope_status_idx", false,
		"scope_kind", "character_id", "review_status", "status", "updated_at_ms", "id",
	)) {
		t.Fatal("personal memories lack scope-before-status candidate index")
	}
	if slices.ContainsFunc(personal.columns, func(column schemaColumn) bool {
		return column.name == "normalized_content_hash"
	}) || slices.ContainsFunc(personal.indexes, func(index schemaIndex) bool {
		return index.name == personalMemoryDuplicateRevalidationIndex
	}) {
		t.Fatal("immutable revision-seven personal memory shape contains revision-eight duplicate metadata")
	}
	if len(personal.foreignKeys) != 2 || personal.foreignKeys[0].referencedTable != "conversation_turns" ||
		personal.foreignKeys[0].deleteRule != "restrict" || len(personal.foreignKeys[0].columns) != 2 ||
		personal.foreignKeys[1].referencedTable != "personal_memories" || personal.foreignKeys[1].deleteRule != "restrict" {
		t.Fatalf("personal memory lifecycle foreign keys = %#v", personal.foreignKeys)
	}
	personalCheck := schemaCheckNamed(t, personal, "personal_memories_invariants_check")
	for _, token := range []string{
		"(`character_id` = trim(`character_id`))",
		"(`scope_kind` = 'unassigned_legacy') and (`character_id` is null) and (`review_status` = 'needs_review') and (`kind` = 'relationship')",
		"(`review_status` = 'ready') and (`embedding_space_id` is not null)",
	} {
		if !strings.Contains(personalCheck.clause, token) {
			t.Errorf("personal memory CHECK lacks %q: %s", token, personalCheck.clause)
		}
	}

	coverage := cognitiveRecordsSchema[1]
	if len(coverage.indexes) != 2 || len(coverage.foreignKeys) != 2 ||
		coverage.foreignKeys[0].deleteRule != "cascade" || coverage.foreignKeys[1].deleteRule != "restrict" {
		t.Fatalf("memory coverage contract = indexes %#v, foreign keys %#v", coverage.indexes, coverage.foreignKeys)
	}
	if ignored := cognitiveIgnoredPhysicalIndexes(coverage.name); len(ignored) != 0 {
		t.Fatalf("memory coverage special indexes = %#v, want none", ignored)
	}

	social := cognitiveRecordsSchema[2]
	for _, obsolete := range []string{"last_feedback_turn_id", "use_count", "positive_count", "negative_count", "unknown_count"} {
		if slices.ContainsFunc(social.columns, func(column schemaColumn) bool { return column.name == obsolete }) {
			t.Errorf("social memory retained obsolete column %s", obsolete)
		}
	}
	if hash := schemaColumnNamed(t, social, "content_hash"); hash.columnType != "binary(32)" || hash.nullable {
		t.Fatalf("social content hash = %#v", hash)
	}
	if !schemaHasIndex(social, ascendingBTreeIndex(
		"social_memory_entries_scope_status_idx", false,
		"character_id", "conversation_id", "status", "kind", "feedback_quarantined_until_ms", "updated_at_ms", "id",
	)) {
		t.Fatal("social memory lacks public-scope status candidate index")
	}
	if len(social.foreignKeys) != 1 || social.foreignKeys[0].deleteRule != "cascade" ||
		len(social.foreignKeys[0].columns) != 2 {
		t.Fatalf("social memory conversation ownership = %#v", social.foreignKeys)
	}
	socialCheck := schemaCheckNamed(t, social, "social_memory_entries_invariants_check")
	if !strings.Contains(socialCheck.clause,
		"(`kind` = 'person_note') and (`sender_id` is not null) and (`situation` = `sender_id`) and (CHAR_LENGTH(`content`) <= 240)",
	) {
		t.Fatalf("social memory CHECK does not cap person-note content at 240 runes: %s", socialCheck.clause)
	}

	knowledge := cognitiveRecordsSchema[3]
	if topic := schemaColumnNamed(t, knowledge, "topic"); topic.columnType != "varchar(512)" {
		t.Fatalf("knowledge topic = %#v", topic)
	}
	if statement := schemaColumnNamed(t, knowledge, "statement"); statement.columnType != "longtext" {
		t.Fatalf("knowledge statement = %#v", statement)
	}
	for _, table := range []schemaTable{personal, social, knowledge} {
		if space := schemaColumnNamed(t, table, "embedding_space_id"); space.columnType != "varchar(256)" || !space.nullable {
			t.Errorf("table %s embedding space = %#v", table.name, space)
		}
	}
	if !schemaHasIndex(knowledge, ascendingBTreeIndex(
		"knowledge_entries_status_updated_idx", false, "status", "updated_at_ms", "id",
	)) {
		t.Fatal("knowledge entries lack status-before-ranking candidate index")
	}
	if len(knowledge.foreignKeys) != 2 || knowledge.foreignKeys[0].deleteRule != "restrict" ||
		knowledge.foreignKeys[1].deleteRule != "restrict" {
		t.Fatalf("knowledge lifecycle foreign keys = %#v", knowledge.foreignKeys)
	}
}

func TestDuplicateRevalidationSchemaEvolvesOnlyPersonalDuplicateMetadata(t *testing.T) {
	if personalMemoryDuplicateBackfillBatchSize != 128 {
		t.Fatalf("personal duplicate backfill batch size = %d, want 128", personalMemoryDuplicateBackfillBatchSize)
	}
	if len(duplicateRevalidationSchemaContract) != 7 {
		t.Fatalf("duplicate revalidation contract statement count = %d, want 7", len(duplicateRevalidationSchemaContract))
	}
	for _, token := range []string{
		"ADD COLUMN normalized_content_hash BINARY(32) NULL",
		"strings.Join(strings.Fields(content), \" \")",
		"GO VERIFY EVERY personal_memories.normalized_content_hash",
		"IN ID-KEYSET BATCHES OF 128",
		"MODIFY COLUMN normalized_content_hash BINARY(32) NOT NULL",
		"CREATE INDEX IF NOT EXISTS " + personalMemoryDuplicateRevalidationIndex,
		"(kind, scope_kind, character_id, review_status, status, normalized_content_hash, id)",
		"CREATE TABLE IF NOT EXISTS " + personalMemoryWriteGuardTableName,
		"CONSTRAINT personal_memory_write_guard_singleton_check CHECK (id = 1)",
		"INSERT INTO " + personalMemoryWriteGuardTableName + " (id) VALUES (1)",
		"ON DUPLICATE KEY UPDATE id = VALUES(id)",
	} {
		if !strings.Contains(strings.Join(duplicateRevalidationSchemaContract[:], "\n"), token) {
			t.Errorf("duplicate revalidation contract lacks %q", token)
		}
	}

	revisionSeven := cognitiveRecordsSchema[0]
	evolved := duplicateRevalidationPersonalTable()
	if evolved.name != "personal_memories" || len(evolved.columns) != len(revisionSeven.columns)+1 ||
		len(evolved.indexes) != len(revisionSeven.indexes)+1 ||
		len(evolved.checks) != len(revisionSeven.checks) ||
		len(evolved.foreignKeys) != len(revisionSeven.foreignKeys) {
		t.Fatalf("revision-eight personal memory shape = %#v", evolved)
	}
	hash := schemaColumnNamed(t, evolved, "normalized_content_hash")
	if hash.columnType != "binary(32)" || hash.nullable || hash.defaultValue.Valid ||
		hash.extra != "" || hash.generationExpression != "" {
		t.Fatalf("normalized personal content hash column = %#v", hash)
	}
	if !schemaHasIndex(evolved, ascendingBTreeIndex(
		personalMemoryDuplicateRevalidationIndex, false,
		"kind", "scope_kind", "character_id", "review_status", "status", "normalized_content_hash", "id",
	)) {
		t.Fatal("revision-eight personal memory shape lacks bounded duplicate revalidation index")
	}
	if slices.ContainsFunc(revisionSeven.columns, func(column schemaColumn) bool {
		return column.name == "normalized_content_hash"
	}) || slices.ContainsFunc(revisionSeven.indexes, func(index schemaIndex) bool {
		return index.name == personalMemoryDuplicateRevalidationIndex
	}) {
		t.Fatal("building revision-eight personal shape mutated immutable revision seven")
	}
	guard := personalMemoryWriteGuardSchema
	if guard.name != personalMemoryWriteGuardTableName || guard.ddl != createPersonalMemoryWriteGuardDDL ||
		len(guard.columns) != 1 || guard.columns[0] != (schemaColumn{name: "id", columnType: "tinyint unsigned"}) ||
		!schemaHasIndex(guard, ascendingBTreeIndex("PRIMARY", true, "id")) ||
		len(guard.checks) != 1 || guard.checks[0] != (schemaCheck{
		name: "personal_memory_write_guard_singleton_check", clause: "(`id` = 1)",
	}) || len(guard.foreignKeys) != 0 {
		t.Fatalf("personal memory write guard schema = %#v", guard)
	}
}

func TestSocialFeedbackEventsSchemaDefinesAuditLedgerWithoutBody(t *testing.T) {
	table := socialMemoryFeedbackEventsSchema
	assertPortableSchemaTables(t, []schemaTable{table}, []string{socialMemoryFeedbackEventsTableName})
	lower := strings.ToLower(table.ddl)
	for _, forbidden := range []string{
		"situation", "content", "recall_cue", "embedding", "jsonb", "bytea", "$1",
	} {
		if strings.Contains(lower, forbidden) {
			t.Errorf("social feedback events DDL contains forbidden token %q", forbidden)
		}
	}
	if evidence := schemaColumnNamed(t, table, "evidence_message_ids"); evidence.columnType != "json" || evidence.nullable {
		t.Fatalf("social feedback evidence IDs = %#v", evidence)
	}
	if !schemaHasIndex(table, ascendingBTreeIndex(
		"social_memory_feedback_events_turn_entry_key", true, "turn_id", "entry_id",
	)) {
		t.Fatal("social feedback events lack (turn_id, entry_id) idempotency key")
	}
	if len(table.foreignKeys) != 3 ||
		table.foreignKeys[0].referencedTable != "conversations" || table.foreignKeys[0].deleteRule != "cascade" ||
		table.foreignKeys[1].referencedTable != "conversation_turns" || table.foreignKeys[1].deleteRule != "cascade" ||
		table.foreignKeys[2].referencedTable != "social_memory_entries" || table.foreignKeys[2].deleteRule != "cascade" {
		t.Fatalf("social feedback foreign keys = %#v", table.foreignKeys)
	}
	check := schemaCheckNamed(t, table, "social_memory_feedback_events_invariants_check")
	for _, token := range []string{
		"(JSON_TYPE(`evidence_message_ids`) = 'ARRAY')",
		"(JSON_LENGTH(`evidence_message_ids`) <= 6)",
		"((`outcome` = 'unknown') = (JSON_LENGTH(`evidence_message_ids`) = 0))",
		"(`adoption` in ('adopted','not_adopted','uncertain'))",
	} {
		if !strings.Contains(check.clause, token) {
			t.Errorf("social feedback CHECK lacks %q: %s", token, check.clause)
		}
	}
}

func TestStickerCatalogSchemaDefinesMetadataAndDeliveryLedger(t *testing.T) {
	assertPortableSchemaTables(t, stickerCatalogSchema[:], []string{stickersTableName, expressionDeliveriesTableName})
	stickers := stickerCatalogSchema[0]
	lower := strings.ToLower(stickers.ddl)
	for _, forbidden := range []string{"bytea", "jsonb", "longblob", "content ", "$1"} {
		if strings.Contains(lower, forbidden) {
			t.Errorf("stickers DDL contains forbidden token %q", forbidden)
		}
	}
	if hash := schemaColumnNamed(t, stickers, "content_sha256"); hash.columnType != "binary(32)" || hash.nullable {
		t.Fatalf("sticker content hash = %#v", hash)
	}
	if !schemaHasIndex(stickers, ascendingBTreeIndex("stickers_content_sha256_key", true, "content_sha256")) {
		t.Fatal("stickers lack unique content hash")
	}
	check := schemaCheckNamed(t, stickers, "stickers_invariants_check")
	for _, token := range []string{
		"(`mime_type` in ('image/jpeg','image/png','image/gif','image/webp'))",
		"(`status` in ('draft','active','disabled'))",
		"((`status` <> 'active') or (CHAR_LENGTH(`description`) > 0))",
		"(JSON_TYPE(`tags`) = 'ARRAY')",
	} {
		if !strings.Contains(check.clause, token) {
			t.Errorf("stickers CHECK lacks %q: %s", token, check.clause)
		}
	}

	deliveries := stickerCatalogSchema[1]
	if !schemaHasIndex(deliveries, ascendingBTreeIndex("PRIMARY", true, "conversation_id", "turn_id", "beat_id")) {
		t.Fatal("expression deliveries lack (conversation, turn, beat) idempotency key")
	}
	if len(deliveries.foreignKeys) != 1 ||
		deliveries.foreignKeys[0].referencedTable != "conversation_turns" ||
		deliveries.foreignKeys[0].deleteRule != "cascade" {
		t.Fatalf("expression delivery foreign keys = %#v", deliveries.foreignKeys)
	}
	deliveryCheck := schemaCheckNamed(t, deliveries, "expression_deliveries_invariants_check")
	for _, token := range []string{
		"(`status` in ('succeeded','failed'))",
		"((`status` = 'succeeded') = (`error_message` is null))",
		"((`status` = 'succeeded') or (`external_message_id` is null))",
	} {
		if !strings.Contains(deliveryCheck.clause, token) {
			t.Errorf("expression deliveries CHECK lacks %q: %s", token, deliveryCheck.clause)
		}
	}
}

func TestPluginPersistenceSchemaDefinesHostOwnedStateStatsJournalAndSecretRefs(t *testing.T) {
	assertPortableSchemaTables(t, pluginPersistenceSchema[:], []string{
		pluginInstanceStateTableName,
		pluginInstanceStatsTableName,
		pluginUpgradeJournalTableName,
		pluginInstanceConfigRefsTableName,
	})
	for _, table := range pluginPersistenceSchema {
		lower := strings.ToLower(table.ddl)
		for _, forbidden := range []string{"bytea", "jsonb", "$1", "password", "api_key", "guest sql"} {
			if strings.Contains(lower, forbidden) {
				t.Errorf("%s DDL contains forbidden token %q", table.name, forbidden)
			}
		}
	}
	state := pluginPersistenceSchema[0]
	if !schemaHasIndex(state, ascendingBTreeIndex("PRIMARY", true, "instance_id", "state_key")) {
		t.Fatal("plugin instance state lacks instance-scoped primary key")
	}
	if len(state.foreignKeys) != 1 || state.foreignKeys[0].referencedTable != "plugin_instances" ||
		state.foreignKeys[0].deleteRule != "cascade" {
		t.Fatalf("plugin instance state foreign key = %#v", state.foreignKeys)
	}
	refs := pluginPersistenceSchema[3]
	if len(refs.foreignKeys) != 2 || refs.foreignKeys[1].referencedTable != "secret_values" ||
		refs.foreignKeys[1].deleteRule != "restrict" {
		t.Fatalf("plugin config refs must point at secret_values without cascading deletes: %#v", refs.foreignKeys)
	}
}

func TestObservabilityHistorySchemaDefinesTypedJSONProjectionTable(t *testing.T) {
	assertPortableSchemaTables(t, observabilityHistorySchema[:], []string{observabilityRecordsTableName})
	table := observabilityHistorySchema[0]
	lower := strings.ToLower(table.ddl)
	for _, forbidden := range []string{"bytea", "jsonb", "longblob", "result_data", "content ", "$1"} {
		if strings.Contains(lower, forbidden) {
			t.Errorf("observability_records DDL contains forbidden token %q", forbidden)
		}
	}
	if !schemaHasIndex(table, ascendingBTreeIndex("PRIMARY", true, "kind", "record_key")) {
		t.Fatal("observability records lack (kind, record_key) idempotency key")
	}
	if !schemaHasIndex(table, ascendingBTreeIndex("observability_records_kind_recorded_idx", false, "kind", "recorded_at_ms", "record_key")) {
		t.Fatal("observability records lack kind/recorded lookup index")
	}
	check := schemaCheckNamed(t, table, "observability_records_invariants_check")
	for _, token := range []string{
		"(`kind` in ('log','trace','metric'))",
		"(JSON_TYPE(`payload`) = 'OBJECT')",
		"(`recorded_at_ms` > 0)",
	} {
		if !strings.Contains(check.clause, token) {
			t.Errorf("observability records CHECK lacks %q: %s", token, check.clause)
		}
	}
}

func TestToolExecutionLedgerSchemaDefinesIdempotentMetadataOnlyTable(t *testing.T) {
	assertPortableSchemaTables(t, toolExecutionLedgerSchema[:], []string{toolExecutionsTableName})
	table := toolExecutionLedgerSchema[0]
	lower := strings.ToLower(table.ddl)
	for _, forbidden := range []string{"bytea", "jsonb", "longblob", "payload", "result_data", "content ", "$1"} {
		if strings.Contains(lower, forbidden) {
			t.Errorf("tool_executions DDL contains forbidden token %q", forbidden)
		}
	}
	if !schemaHasIndex(table, ascendingBTreeIndex("tool_executions_turn_call_key", true, "conversation_id", "turn_id", "call_id")) {
		t.Fatal("tool executions lack (conversation, turn, call) idempotency key")
	}
	if !schemaHasIndex(table, ascendingBTreeIndex("tool_executions_turn_tool_key", true, "conversation_id", "turn_id", "tool_name")) {
		t.Fatal("tool executions lack (conversation, turn, tool) uniqueness")
	}
	if len(table.foreignKeys) != 1 ||
		table.foreignKeys[0].referencedTable != "conversation_turns" ||
		table.foreignKeys[0].deleteRule != "cascade" {
		t.Fatalf("tool execution foreign keys = %#v", table.foreignKeys)
	}
	check := schemaCheckNamed(t, table, "tool_executions_invariants_check")
	for _, token := range []string{
		"(`tool_name` = 'desktop_observe')",
		"(`status` in ('pending','completed','failed','cancelled'))",
		"((`status` = 'completed') = ((`result_media_type` is not null)",
		"(`result_sha256` regexp '^[0-9a-f]{64}$')",
	} {
		if !strings.Contains(check.clause, token) {
			t.Errorf("tool executions CHECK lacks %q: %s", token, check.clause)
		}
	}
}

func TestNormalizedPersonalMemoryContentHashMatchesGoFieldsContract(t *testing.T) {
	tests := []struct {
		name       string
		content    string
		normalized string
	}{
		{name: "unchanged", content: "alpha beta", normalized: "alpha beta"},
		{name: "ascii whitespace", content: " \talpha\n\rbeta\v\f ", normalized: "alpha beta"},
		{name: "unicode whitespace", content: "alpha\u00a0beta\u3000gamma", normalized: "alpha beta gamma"},
		{name: "zero width space is content", content: "alpha\u200bbeta", normalized: "alpha\u200bbeta"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			want := sha256.Sum256([]byte(test.normalized))
			if got := normalizedPersonalMemoryContentHash(test.content); got != want {
				t.Fatalf("normalized content hash = %x, want %x", got, want)
			}
		})
	}
}

func TestRequiredPersonalMemoryNormalizedHashRejectsSemanticDrift(t *testing.T) {
	content := "alpha\u00a0beta"
	want := normalizedPersonalMemoryContentHash(content)
	if err := comparePersonalMemoryNormalizedHash("memory-1", content, want[:]); err != nil {
		t.Fatalf("matching required normalized content hash rejected: %v", err)
	}
	for _, drifted := range [][]byte{
		nil,
		{},
		make([]byte, sha256.Size),
		sha256.New().Sum(nil),
	} {
		if err := comparePersonalMemoryNormalizedHash("memory-1", content, drifted); err == nil {
			t.Fatalf("drifted required normalized content hash accepted: %x", drifted)
		}
	}
}

func TestPersonalMemoryWriteGuardRequiresExactSingletonRow(t *testing.T) {
	if err := comparePersonalMemoryWriteGuardRows([]uint64{1}); err != nil {
		t.Fatalf("exact personal memory write guard row rejected: %v", err)
	}
	for _, rows := range [][]uint64{nil, {}, {0}, {2}, {1, 1}, {1, 2}} {
		if err := comparePersonalMemoryWriteGuardRows(rows); err == nil {
			t.Fatalf("invalid personal memory write guard rows accepted: %v", rows)
		}
	}
}

func TestCognitiveRecordsSchemaDefinesImmutableIKAndHNSWIndexes(t *testing.T) {
	if len(cognitiveSpecialIndexes) != 6 {
		t.Fatalf("cognitive special index count = %d, want 6", len(cognitiveSpecialIndexes))
	}
	want := map[string]struct {
		table     string
		kind      string
		columns   []string
		ddlTokens []string
	}{
		personalMemoryFullTextIndex: {"personal_memories", "fulltext", []string{"content"}, []string{"FULLTEXT INDEX", "ik_mode='max_word'"}},
		personalMemoryVectorIndex:   {"personal_memories", "vector", []string{"embedding"}, []string{"VECTOR INDEX", "DISTANCE=COSINE", "TYPE=HNSW", "LIB=VSAG"}},
		socialMemoryFullTextIndex:   {"social_memory_entries", "fulltext", []string{"situation", "content", "recall_cue"}, []string{"FULLTEXT INDEX", "ik_mode='max_word'"}},
		socialMemoryVectorIndex:     {"social_memory_entries", "vector", []string{"embedding"}, []string{"VECTOR INDEX", "DISTANCE=COSINE", "TYPE=HNSW", "LIB=VSAG"}},
		knowledgeFullTextIndex:      {"knowledge_entries", "fulltext", []string{"topic", "statement"}, []string{"FULLTEXT INDEX", "ik_mode='max_word'"}},
		knowledgeVectorIndex:        {"knowledge_entries", "vector", []string{"embedding"}, []string{"VECTOR INDEX", "DISTANCE=COSINE", "TYPE=HNSW", "LIB=VSAG"}},
	}
	for _, index := range cognitiveSpecialIndexes {
		expected, exists := want[index.name]
		if !exists {
			t.Errorf("unexpected cognitive special index %q", index.name)
			continue
		}
		delete(want, index.name)
		if index.table != expected.table || index.indexType != expected.kind || !slices.Equal(index.columns, expected.columns) {
			t.Errorf("special index %s = %#v", index.name, index)
		}
		table := slices.IndexFunc(cognitiveRecordsSchema[:], func(table schemaTable) bool { return table.name == index.table })
		if table < 0 {
			t.Errorf("special index %s has no owner table", index.name)
			continue
		}
		for _, token := range expected.ddlTokens {
			if !strings.Contains(cognitiveRecordsSchema[table].ddl, token) {
				t.Errorf("special index %s table DDL lacks %q", index.name, token)
			}
		}
		if index.indexType == "vector" {
			if !strings.Contains(strings.ToUpper(cognitiveRecordsSchema[table].ddl), "ORGANIZATION = HEAP") {
				t.Errorf("vector table %s is not declared as a heap", index.table)
			}
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing cognitive special indexes = %#v", want)
	}
}

func TestCognitiveSpecialIndexComparisonRejectsLogicalAndPhysicalDrift(t *testing.T) {
	fullText, ok := cognitiveSpecialIndexNamed(knowledgeFullTextIndex)
	if !ok {
		t.Fatal("knowledge full-text index contract is missing")
	}
	metadata := make([]transcriptRecallIndexMetadata, len(fullText.columns))
	for ordinal, column := range fullText.columns {
		metadata[ordinal] = transcriptRecallIndexMetadata{
			table: fullText.table, name: fullText.name, nonUnique: 1, sequence: ordinal + 1,
			column: column, collation: sql.NullString{String: "a", Valid: true},
			indexType: fullText.indexType, comment: "available", visible: "yes",
		}
	}
	if err := compareCognitiveSpecialIndexMetadata(fullText, metadata); err != nil {
		t.Fatalf("compareCognitiveSpecialIndexMetadata(equal) error = %v", err)
	}
	for _, mutate := range []func([]transcriptRecallIndexMetadata){
		func(rows []transcriptRecallIndexMetadata) { rows[0].column = "content" },
		func(rows []transcriptRecallIndexMetadata) { rows[0].indexType = "btree" },
		func(rows []transcriptRecallIndexMetadata) { rows[0].visible = "no" },
		func(rows []transcriptRecallIndexMetadata) { rows[0].comment = "building" },
		func(rows []transcriptRecallIndexMetadata) { rows[0].sequence = 2 },
	} {
		drifted := slices.Clone(metadata)
		mutate(drifted)
		if err := compareCognitiveSpecialIndexMetadata(fullText, drifted); err == nil {
			t.Fatalf("drifted logical index was accepted: %#v", drifted)
		}
	}
	if err := compareCognitiveSpecialIndexMetadata(fullText, nil); err == nil {
		t.Fatal("missing logical full-text index was accepted")
	}

	vector, ok := cognitiveSpecialIndexNamed(personalMemoryVectorIndex)
	if !ok {
		t.Fatal("personal vector index contract is missing")
	}
	validVectorCreate := "CREATE TABLE `personal_memories` (`embedding` VECTOR(1024) DEFAULT NULL, " +
		"VECTOR KEY `personal_memories_embedding_vec_idx` (`embedding`) " +
		"WITH (DISTANCE=COSINE, TYPE=HNSW, LIB=VSAG, M=16, EF_CONSTRUCTION=200, EF_SEARCH=64, SYNC_MODE=ASYNC)) " +
		"ORGANIZATION HEAP"
	if err := compareCognitiveSpecialIndexCreateTable(vector, vector.table, validVectorCreate); err != nil {
		t.Fatalf("compareCognitiveSpecialIndexCreateTable(equal vector) error = %v", err)
	}
	for _, drifted := range []string{
		strings.Replace(validVectorCreate, "TYPE=HNSW", "TYPE=IVF_FLAT", 1),
		strings.Replace(validVectorCreate, "DISTANCE=COSINE", "DISTANCE=L2", 1),
		strings.Replace(validVectorCreate, "SYNC_MODE=ASYNC", "SYNC_MODE=SYNC", 1),
		strings.Replace(validVectorCreate, "ORGANIZATION HEAP", "ORGANIZATION INDEX", 1),
	} {
		if err := compareCognitiveSpecialIndexCreateTable(vector, vector.table, drifted); err == nil {
			t.Fatalf("drifted vector SHOW CREATE TABLE was accepted: %s", drifted)
		}
	}
	validFullTextCreate := "CREATE TABLE `knowledge_entries` (`topic` varchar(512), `statement` longtext, " +
		"FULLTEXT KEY `knowledge_entries_text_fts_idx` (`topic`, `statement`) " +
		"WITH PARSER ik PARSER_PROPERTIES=(ik_mode=\"max_word\"))"
	if err := compareCognitiveSpecialIndexCreateTable(fullText, fullText.table, validFullTextCreate); err != nil {
		t.Fatalf("compareCognitiveSpecialIndexCreateTable(equal fulltext) error = %v", err)
	}
	if err := compareCognitiveSpecialIndexCreateTable(
		fullText, fullText.table, strings.Replace(validFullTextCreate, "max_word", "smart", 1),
	); err == nil {
		t.Fatal("drifted IK parser was accepted")
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
	if !schemaHasIndex(messages, ascendingBTreeIndex(
		"conversation_messages_conversation_role_created_idx", false,
		"conversation_id", "role", "created_at_ms", "sequence",
	)) {
		t.Fatal("conversation messages lack the bounded activity projection index")
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

func TestConversationRuntimeSchemaDefinesLedgerContinuationAndWindowContracts(t *testing.T) {
	wantTables := []string{"turn_runtime_events", "lane_continuations", "context_windows"}
	assertPortableSchemaTables(t, conversationRuntimeSchema[:], wantTables)

	events := conversationRuntimeSchema[0]
	if !schemaHasIndex(events, ascendingBTreeIndex(
		"turn_runtime_events_conversation_turn_sequence_key", true,
		"conversation_id", "turn_id", "sequence",
	)) {
		t.Fatal("runtime events lack one ordered sequence per conversation Turn")
	}
	if !schemaHasIndex(events, ascendingBTreeIndex(
		"turn_runtime_events_type_created_idx", false, "event_type", "created_at_ms", "sequence",
	)) {
		t.Fatal("runtime events lack stable event-type history lookup")
	}
	if sequence := schemaColumnNamed(t, events, "sequence"); sequence.columnType != "bigint" {
		t.Fatalf("runtime event sequence type = %q, want signed bigint", sequence.columnType)
	}
	if metadata := schemaColumnNamed(t, events, "metadata_json"); metadata.columnType != "json" || metadata.nullable {
		t.Fatalf("runtime event metadata = %#v", metadata)
	}
	if len(events.foreignKeys) != 1 || events.foreignKeys[0].referencedTable != "conversation_turns" ||
		len(events.foreignKeys[0].columns) != 2 ||
		events.foreignKeys[0].columns[0].name != "conversation_id" ||
		events.foreignKeys[0].columns[0].referencedColumn != "conversation_id" ||
		events.foreignKeys[0].columns[1].name != "turn_id" ||
		events.foreignKeys[0].columns[1].referencedColumn != "id" ||
		events.foreignKeys[0].deleteRule != "cascade" {
		t.Fatalf("runtime event Turn isolation foreign key = %#v", events.foreignKeys)
	}

	continuations := conversationRuntimeSchema[1]
	if !schemaHasIndex(continuations, ascendingBTreeIndex("PRIMARY", true, "conversation_id", "lane")) {
		t.Fatal("lane continuations lack one authoritative record per conversation lane")
	}
	if responseID := schemaColumnNamed(t, continuations, "previous_response_id"); responseID.columnType != "varchar(128)" || responseID.collation != "utf8mb4_bin" {
		t.Fatalf("previous response id = %#v", responseID)
	}
	if revision := schemaColumnNamed(t, continuations, "window_revision"); revision.columnType != "bigint" {
		t.Fatalf("lane continuation window revision = %q, want signed bigint", revision.columnType)
	}

	windows := conversationRuntimeSchema[2]
	if !schemaHasIndex(windows, ascendingBTreeIndex("PRIMARY", true, "conversation_id", "lane")) {
		t.Fatal("context windows lack one authoritative record per conversation lane")
	}
	for _, name := range []string{
		"window_number", "observed_prefill_tokens", "estimated_prefill_tokens",
		"failure_count", "prompt_window_revision",
	} {
		if column := schemaColumnNamed(t, windows, name); column.columnType != "bigint" {
			t.Fatalf("context window %s type = %q, want signed bigint", name, column.columnType)
		}
	}
	if previous := schemaColumnNamed(t, windows, "previous_window_id"); !previous.nullable {
		t.Fatal("context previous window id must remain nullable")
	}
	for _, table := range conversationRuntimeSchema {
		if len(table.foreignKeys) != 1 || table.foreignKeys[0].deleteRule != "cascade" ||
			table.foreignKeys[0].updateRule != "restrict" {
			t.Fatalf("table %s conversation-root lifecycle = %#v", table.name, table.foreignKeys)
		}
	}
}

func TestTranscriptRecallSchemaDefinesImmutableIKFullTextIndex(t *testing.T) {
	const wantDDL = "CREATE FULLTEXT INDEX IF NOT EXISTS conversation_messages_content_fts_idx " +
		"ON conversation_messages(content) WITH PARSER IK PARSER_PROPERTIES=(ik_mode='max_word')"
	if transcriptRecallIndexDDL != wantDDL {
		t.Fatalf("transcript recall DDL = %q, want %q", transcriptRecallIndexDDL, wantDDL)
	}
	if transcriptRecallTableName != "conversation_messages" ||
		transcriptRecallIndexName != "conversation_messages_content_fts_idx" {
		t.Fatalf("transcript recall target = %s.%s", transcriptRecallTableName, transcriptRecallIndexName)
	}
}

func TestTranscriptRecallSchemaComparisonRejectsLogicalAndParserDrift(t *testing.T) {
	wantMetadata := transcriptRecallIndexMetadata{
		table:     transcriptRecallTableName,
		name:      transcriptRecallIndexName,
		nonUnique: 1,
		sequence:  1,
		column:    "content",
		collation: sql.NullString{String: "a", Valid: true},
		indexType: "fulltext",
		comment:   "available",
		visible:   "yes",
	}
	if err := compareTranscriptRecallIndexMetadata([]transcriptRecallIndexMetadata{wantMetadata}); err != nil {
		t.Fatalf("compareTranscriptRecallIndexMetadata(equal) error = %v", err)
	}
	for _, testCase := range []struct {
		name   string
		mutate func(*transcriptRecallIndexMetadata)
	}{
		{name: "missing", mutate: nil},
		{name: "logical column", mutate: func(metadata *transcriptRecallIndexMetadata) { metadata.column = "role" }},
		{name: "prefix", mutate: func(metadata *transcriptRecallIndexMetadata) {
			metadata.subPart = sql.NullInt64{Int64: 8, Valid: true}
		}},
		{name: "not available", mutate: func(metadata *transcriptRecallIndexMetadata) { metadata.comment = "building" }},
		{name: "hidden", mutate: func(metadata *transcriptRecallIndexMetadata) { metadata.visible = "no" }},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if testCase.mutate == nil {
				if err := compareTranscriptRecallIndexMetadata(nil); err == nil {
					t.Fatal("missing transcript recall index was accepted")
				}
				return
			}
			drifted := wantMetadata
			testCase.mutate(&drifted)
			if err := compareTranscriptRecallIndexMetadata([]transcriptRecallIndexMetadata{drifted}); err == nil {
				t.Fatalf("%s transcript recall drift was accepted", testCase.name)
			}
		})
	}
	validCreateTable := `CREATE TABLE ` + "`conversation_messages`" + ` (
  ` + "`content`" + ` longtext NOT NULL,
  FULLTEXT KEY ` + "`conversation_messages_content_fts_idx`" + ` (` + "`content`" + `)
    WITH PARSER ik PARSER_PROPERTIES=(ik_mode="max_word") BLOCK_SIZE 16384
)`
	if err := compareTranscriptRecallCreateTable(transcriptRecallTableName, validCreateTable); err != nil {
		t.Fatalf("compareTranscriptRecallCreateTable(equal) error = %v", err)
	}
	for _, drifted := range []string{
		strings.Replace(validCreateTable, `ik_mode="max_word"`, `ik_mode="smart"`, 1),
		strings.Replace(validCreateTable, "WITH PARSER ik", "WITH PARSER ngram2", 1),
		strings.Replace(validCreateTable, "(`content`)", "(`role`)", 1),
	} {
		if err := compareTranscriptRecallCreateTable(transcriptRecallTableName, drifted); err == nil {
			t.Fatalf("drifted SHOW CREATE TABLE was accepted: %s", drifted)
		}
	}
	if err := compareTranscriptRecallCreateTable("wrong_table", validCreateTable); err == nil {
		t.Fatal("wrong SHOW CREATE TABLE identity was accepted")
	}
}

func TestParseShowIndexOptionalInt64RejectsCorruptMetadata(t *testing.T) {
	if value, err := parseShowIndexOptionalInt64(nil, "sub_part"); err != nil || value.Valid {
		t.Fatalf("NULL SHOW INDEX value = %#v, %v", value, err)
	}
	if value, err := parseShowIndexOptionalInt64(sql.RawBytes("8"), "sub_part"); err != nil ||
		!value.Valid || value.Int64 != 8 {
		t.Fatalf("numeric SHOW INDEX value = %#v, %v", value, err)
	}
	if _, err := parseShowIndexOptionalInt64(sql.RawBytes("not-a-number"), "sub_part"); err == nil {
		t.Fatal("corrupt SHOW INDEX integer was accepted")
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

func schemaCheckNamed(t *testing.T, table schemaTable, name string) schemaCheck {
	t.Helper()
	for _, check := range table.checks {
		if check.name == name {
			return check
		}
	}
	t.Fatalf("table %s lacks CHECK %s", table.name, name)
	return schemaCheck{}
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
		if len(table.columns) == 0 || len(table.indexes) == 0 || len(table.checks) == 0 {
			t.Errorf("table %s verification contract is incomplete", table.name)
		}
		for _, check := range table.checks {
			if !strings.Contains(table.ddl, "CONSTRAINT "+check.name+" CHECK") {
				t.Errorf("table %s is missing named CHECK %s", table.name, check.name)
			}
		}
		for _, column := range table.columns {
			if strings.HasSuffix(column.name, "_id") &&
				column.name != "message_id" && column.name != "evidence_id" &&
				column.name != "previous_response_id" && column.name != "external_message_id" &&
				column.collation != "ascii_bin" {
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
	embedColumns := slices.Clone(wantColumns)
	embedColumns[0].collation = "utf8mb4_bin"
	if err := compareSchemaColumns(wantColumns, embedColumns); err != nil {
		t.Fatalf("compareSchemaColumns(ascii_bin vs utf8mb4_bin) error = %v", err)
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
