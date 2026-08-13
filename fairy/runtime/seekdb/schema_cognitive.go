package seekdb

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"slices"
	"strings"
)

const (
	personalMemoryFullTextIndex = "personal_memories_content_fts_idx"
	personalMemoryVectorIndex   = "personal_memories_embedding_vec_idx"
	socialMemoryFullTextIndex   = "social_memory_entries_text_fts_idx"
	socialMemoryVectorIndex     = "social_memory_entries_embedding_vec_idx"
	knowledgeFullTextIndex      = "knowledge_entries_text_fts_idx"
	knowledgeVectorIndex        = "knowledge_entries_embedding_vec_idx"
)

// Revision seven creates only the four authoritative cognitive-record tables.
// Feedback events and retrieval/store behavior belong to later revisions.
var cognitiveRecordsSchema = [...]schemaTable{
	{
		name: "personal_memories",
		ddl: `CREATE TABLE IF NOT EXISTS personal_memories (
  id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  kind VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  scope_kind VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  character_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NULL,
  review_status VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  content LONGTEXT CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  status VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  confidence_basis_points INT UNSIGNED NOT NULL,
  source_conversation_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  source_turn_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  evidence_ids JSON NOT NULL,
  supersedes_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NULL,
  embedding_space_id VARCHAR(256) CHARACTER SET ascii COLLATE ascii_bin NULL,
  embedding_content_hash BINARY(32) NULL,
  embedding VECTOR(1024) NULL,
  created_at_ms BIGINT UNSIGNED NOT NULL,
  updated_at_ms BIGINT UNSIGNED NOT NULL,
  PRIMARY KEY (id),
  KEY personal_memories_scope_status_idx (
    scope_kind, character_id, review_status, status, updated_at_ms, id
  ),
  KEY personal_memories_source_turn_idx (source_conversation_id, source_turn_id),
  KEY personal_memories_supersedes_idx (supersedes_id),
  FULLTEXT INDEX personal_memories_content_fts_idx (content)
    WITH PARSER IK PARSER_PROPERTIES=(ik_mode='max_word'),
  VECTOR INDEX personal_memories_embedding_vec_idx (embedding)
    WITH (DISTANCE=COSINE, TYPE=HNSW, LIB=VSAG),
  CONSTRAINT personal_memories_source_turn_fk
    FOREIGN KEY (source_conversation_id, source_turn_id)
    REFERENCES conversation_turns (conversation_id, id)
    ON UPDATE RESTRICT ON DELETE RESTRICT,
  CONSTRAINT personal_memories_supersedes_fk FOREIGN KEY (supersedes_id)
    REFERENCES personal_memories (id) ON UPDATE RESTRICT ON DELETE RESTRICT,
  CONSTRAINT personal_memories_invariants_check CHECK (
    CHAR_LENGTH(id) BETWEEN 1 AND 128 AND id = TRIM(id) AND
    CHAR_LENGTH(kind) BETWEEN 1 AND 32 AND kind = TRIM(kind) AND
    status IN ('active', 'superseded', 'tombstone') AND
    confidence_basis_points BETWEEN 0 AND 10000 AND
    CHAR_LENGTH(content) BETWEEN 1 AND 2400 AND content REGEXP '[^[:space:]]' AND
    JSON_TYPE(evidence_ids) = 'ARRAY' AND JSON_LENGTH(evidence_ids) <= 8 AND
    ((scope_kind = 'global' AND character_id IS NULL AND review_status = 'ready' AND
        kind IN ('profile', 'preference', 'experience')) OR
      (scope_kind = 'character' AND character_id IS NOT NULL AND
        CHAR_LENGTH(character_id) BETWEEN 1 AND 128 AND character_id = TRIM(character_id) AND
        review_status = 'ready' AND kind = 'relationship') OR
      (scope_kind = 'unassigned_legacy' AND character_id IS NULL AND
        review_status = 'needs_review' AND kind = 'relationship')) AND
    (supersedes_id IS NULL OR supersedes_id <> id) AND
    ((embedding_space_id IS NULL AND embedding_content_hash IS NULL AND embedding IS NULL) OR
      (review_status = 'ready' AND embedding_space_id IS NOT NULL AND
        CHAR_LENGTH(embedding_space_id) BETWEEN 1 AND 256 AND
        embedding_space_id = TRIM(embedding_space_id) AND
        embedding_content_hash IS NOT NULL AND embedding IS NOT NULL)) AND
    updated_at_ms >= created_at_ms
  )
) ORGANIZATION = HEAP`,
		columns: []schemaColumn{
			{name: "id", columnType: "varchar(128)", collation: "ascii_bin"},
			{name: "kind", columnType: "varchar(32)", collation: "ascii_bin"},
			{name: "scope_kind", columnType: "varchar(32)", collation: "ascii_bin"},
			{name: "character_id", columnType: "varchar(128)", nullable: true, collation: "ascii_bin"},
			{name: "review_status", columnType: "varchar(16)", collation: "ascii_bin"},
			{name: "content", columnType: "longtext", collation: "utf8mb4_bin"},
			{name: "status", columnType: "varchar(16)", collation: "ascii_bin"},
			{name: "confidence_basis_points", columnType: "int unsigned"},
			{name: "source_conversation_id", columnType: "varchar(128)", collation: "ascii_bin"},
			{name: "source_turn_id", columnType: "varchar(128)", collation: "ascii_bin"},
			{name: "evidence_ids", columnType: "json"},
			{name: "supersedes_id", columnType: "varchar(128)", nullable: true, collation: "ascii_bin"},
			{name: "embedding_space_id", columnType: "varchar(256)", nullable: true, collation: "ascii_bin"},
			{name: "embedding_content_hash", columnType: "binary(32)", nullable: true},
			{name: "embedding", columnType: "vector(1024)", nullable: true},
			{name: "created_at_ms", columnType: "bigint unsigned"},
			{name: "updated_at_ms", columnType: "bigint unsigned"},
		},
		indexes: []schemaIndex{
			ascendingBTreeIndex("PRIMARY", true, "id"),
			ascendingBTreeIndex("personal_memories_scope_status_idx", false,
				"scope_kind", "character_id", "review_status", "status", "updated_at_ms", "id"),
			ascendingBTreeIndex("personal_memories_source_turn_idx", false, "source_conversation_id", "source_turn_id"),
			ascendingBTreeIndex("personal_memories_supersedes_idx", false, "supersedes_id"),
		},
		checks: []schemaCheck{{
			name: "personal_memories_invariants_check",
			clause: "(((CHAR_LENGTH(`id`) >= 1) and (CHAR_LENGTH(`id`) <= 128)) and (`id` = trim(`id`)) and " +
				"((CHAR_LENGTH(`kind`) >= 1) and (CHAR_LENGTH(`kind`) <= 32)) and (`kind` = trim(`kind`)) and " +
				"(`status` in ('active','superseded','tombstone')) and ((`confidence_basis_points` >= 0) and (`confidence_basis_points` <= 10000)) and " +
				"((CHAR_LENGTH(`content`) >= 1) and (CHAR_LENGTH(`content`) <= 2400)) and (`content` regexp '[^[:space:]]') and " +
				"(JSON_TYPE(`evidence_ids`) = 'ARRAY') and (JSON_LENGTH(`evidence_ids`) <= 8) and " +
				"(((`scope_kind` = 'global') and (`character_id` is null) and (`review_status` = 'ready') and (`kind` in ('profile','preference','experience'))) or " +
				"((`scope_kind` = 'character') and (`character_id` is not null) and ((CHAR_LENGTH(`character_id`) >= 1) and (CHAR_LENGTH(`character_id`) <= 128)) and " +
				"(`character_id` = trim(`character_id`)) and (`review_status` = 'ready') and (`kind` = 'relationship')) or " +
				"((`scope_kind` = 'unassigned_legacy') and (`character_id` is null) and (`review_status` = 'needs_review') and (`kind` = 'relationship'))) and " +
				"((`supersedes_id` is null) or (`supersedes_id` <> `id`)) and " +
				"(((`embedding_space_id` is null) and (`embedding_content_hash` is null) and (`embedding` is null)) or " +
				"((`review_status` = 'ready') and (`embedding_space_id` is not null) and ((CHAR_LENGTH(`embedding_space_id`) >= 1) and (CHAR_LENGTH(`embedding_space_id`) <= 256)) and " +
				"(`embedding_space_id` = trim(`embedding_space_id`)) and (`embedding_content_hash` is not null) and (`embedding` is not null))) and " +
				"(`updated_at_ms` >= `created_at_ms`))",
		}},
		foreignKeys: []schemaForeignKey{
			{
				name: "personal_memories_source_turn_fk", referencedTable: "conversation_turns",
				updateRule: "restrict", deleteRule: "restrict",
				columns: []schemaForeignKeyColumn{
					{name: "source_conversation_id", referencedColumn: "conversation_id", sameSchema: true},
					{name: "source_turn_id", referencedColumn: "id", sameSchema: true},
				},
			},
			{
				name: "personal_memories_supersedes_fk", referencedTable: "personal_memories",
				updateRule: "restrict", deleteRule: "restrict",
				columns: []schemaForeignKeyColumn{{name: "supersedes_id", referencedColumn: "id", sameSchema: true}},
			},
		},
	},
	{
		name: "memory_context_coverages",
		ddl: `CREATE TABLE IF NOT EXISTS memory_context_coverages (
  conversation_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  turn_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  memory_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  result_status VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  created_at_ms BIGINT UNSIGNED NOT NULL,
  PRIMARY KEY (conversation_id, turn_id, memory_id),
  KEY memory_context_coverages_memory_idx (memory_id),
  CONSTRAINT memory_context_coverages_turn_fk FOREIGN KEY (conversation_id, turn_id)
    REFERENCES conversation_turns (conversation_id, id) ON UPDATE RESTRICT ON DELETE CASCADE,
  CONSTRAINT memory_context_coverages_memory_fk FOREIGN KEY (memory_id)
    REFERENCES personal_memories (id) ON UPDATE RESTRICT ON DELETE RESTRICT,
  CONSTRAINT memory_context_coverages_invariants_check CHECK (
    result_status IN ('applied', 'no_change')
  )
)`,
		columns: []schemaColumn{
			{name: "conversation_id", columnType: "varchar(128)", collation: "ascii_bin"},
			{name: "turn_id", columnType: "varchar(128)", collation: "ascii_bin"},
			{name: "memory_id", columnType: "varchar(128)", collation: "ascii_bin"},
			{name: "result_status", columnType: "varchar(16)", collation: "ascii_bin"},
			{name: "created_at_ms", columnType: "bigint unsigned"},
		},
		indexes: []schemaIndex{
			ascendingBTreeIndex("PRIMARY", true, "conversation_id", "turn_id", "memory_id"),
			ascendingBTreeIndex("memory_context_coverages_memory_idx", false, "memory_id"),
		},
		checks: []schemaCheck{{
			name:   "memory_context_coverages_invariants_check",
			clause: "(`result_status` in ('applied','no_change'))",
		}},
		foreignKeys: []schemaForeignKey{
			{
				name: "memory_context_coverages_turn_fk", referencedTable: "conversation_turns",
				updateRule: "restrict", deleteRule: "cascade",
				columns: []schemaForeignKeyColumn{
					{name: "conversation_id", referencedColumn: "conversation_id", sameSchema: true},
					{name: "turn_id", referencedColumn: "id", sameSchema: true},
				},
			},
			{
				name: "memory_context_coverages_memory_fk", referencedTable: "personal_memories",
				updateRule: "restrict", deleteRule: "restrict",
				columns: []schemaForeignKeyColumn{{name: "memory_id", referencedColumn: "id", sameSchema: true}},
			},
		},
	},
	{
		name: "social_memory_entries",
		ddl: `CREATE TABLE IF NOT EXISTS social_memory_entries (
  id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  character_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  conversation_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  kind VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  situation VARCHAR(240) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  content VARCHAR(800) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  recall_cue VARCHAR(400) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  content_hash BINARY(32) NOT NULL,
  sender_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NULL,
  sender_name VARCHAR(80) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  status VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  source_start_ms BIGINT UNSIGNED NOT NULL,
  source_end_ms BIGINT UNSIGNED NOT NULL,
  feedback_evaluation_count BIGINT UNSIGNED NOT NULL,
  feedback_adopted_count BIGINT UNSIGNED NOT NULL,
  feedback_positive_count BIGINT UNSIGNED NOT NULL,
  feedback_partial_count BIGINT UNSIGNED NOT NULL,
  feedback_negative_count BIGINT UNSIGNED NOT NULL,
  feedback_score_basis_points INT NOT NULL,
  feedback_quarantined_until_ms BIGINT UNSIGNED NULL,
  embedding_space_id VARCHAR(256) CHARACTER SET ascii COLLATE ascii_bin NULL,
  embedding_content_hash BINARY(32) NULL,
  embedding VECTOR(1024) NULL,
  created_at_ms BIGINT UNSIGNED NOT NULL,
  updated_at_ms BIGINT UNSIGNED NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY social_memory_entries_scope_hash_key (conversation_id, kind, content_hash),
  UNIQUE KEY social_memory_entries_person_note_key (character_id, conversation_id, sender_id),
  UNIQUE KEY social_memory_entries_feedback_scope_key (id, character_id, conversation_id),
  KEY social_memory_entries_scope_status_idx (
    character_id, conversation_id, status, kind, feedback_quarantined_until_ms, updated_at_ms, id
  ),
  FULLTEXT INDEX social_memory_entries_text_fts_idx (situation, content, recall_cue)
    WITH PARSER IK PARSER_PROPERTIES=(ik_mode='max_word'),
  VECTOR INDEX social_memory_entries_embedding_vec_idx (embedding)
    WITH (DISTANCE=COSINE, TYPE=HNSW, LIB=VSAG),
  CONSTRAINT social_memory_entries_conversation_fk FOREIGN KEY (conversation_id, character_id)
    REFERENCES conversations (id, character_id) ON UPDATE RESTRICT ON DELETE CASCADE,
  CONSTRAINT social_memory_entries_invariants_check CHECK (
    kind IN ('episode', 'expression', 'behavior', 'person_note') AND
    status IN ('active', 'suppressed') AND
    CHAR_LENGTH(situation) BETWEEN 1 AND 240 AND situation = TRIM(situation) AND
      situation NOT REGEXP '[[:cntrl:]]' AND
    CHAR_LENGTH(content) BETWEEN 1 AND 800 AND content = TRIM(content) AND
      content NOT REGEXP '[[:cntrl:]]' AND
    CHAR_LENGTH(recall_cue) BETWEEN 1 AND 400 AND recall_cue = TRIM(recall_cue) AND
      recall_cue NOT REGEXP '[[:cntrl:]]' AND
    ((kind = 'person_note' AND sender_id IS NOT NULL AND situation = sender_id AND
        CHAR_LENGTH(content) <= 240 AND
        source_start_ms = source_end_ms AND status = 'active' AND
        feedback_evaluation_count = 0 AND feedback_adopted_count = 0 AND
        feedback_positive_count = 0 AND feedback_partial_count = 0 AND feedback_negative_count = 0 AND
        feedback_score_basis_points = 0 AND feedback_quarantined_until_ms IS NULL) OR
      (kind <> 'person_note' AND sender_id IS NULL AND sender_name = '')) AND
    source_start_ms > 0 AND source_end_ms >= source_start_ms AND
    feedback_adopted_count <= feedback_evaluation_count AND
    feedback_positive_count + feedback_partial_count + feedback_negative_count <= feedback_adopted_count AND
    feedback_score_basis_points BETWEEN -10000 AND 10000 AND
    ((status = 'suppressed') = (feedback_quarantined_until_ms IS NOT NULL)) AND
    ((embedding_space_id IS NULL AND embedding_content_hash IS NULL AND embedding IS NULL) OR
      (kind <> 'person_note' AND embedding_space_id IS NOT NULL AND
        CHAR_LENGTH(embedding_space_id) BETWEEN 1 AND 256 AND embedding_space_id = TRIM(embedding_space_id) AND
        embedding_content_hash IS NOT NULL AND embedding IS NOT NULL)) AND
    updated_at_ms >= created_at_ms
  )
) ORGANIZATION = HEAP`,
		columns: []schemaColumn{
			{name: "id", columnType: "varchar(128)", collation: "ascii_bin"},
			{name: "character_id", columnType: "varchar(128)", collation: "ascii_bin"},
			{name: "conversation_id", columnType: "varchar(128)", collation: "ascii_bin"},
			{name: "kind", columnType: "varchar(16)", collation: "ascii_bin"},
			{name: "situation", columnType: "varchar(240)", collation: "utf8mb4_bin"},
			{name: "content", columnType: "varchar(800)", collation: "utf8mb4_bin"},
			{name: "recall_cue", columnType: "varchar(400)", collation: "utf8mb4_bin"},
			{name: "content_hash", columnType: "binary(32)"},
			{name: "sender_id", columnType: "varchar(128)", nullable: true, collation: "ascii_bin"},
			{name: "sender_name", columnType: "varchar(80)", collation: "utf8mb4_bin"},
			{name: "status", columnType: "varchar(16)", collation: "ascii_bin"},
			{name: "source_start_ms", columnType: "bigint unsigned"},
			{name: "source_end_ms", columnType: "bigint unsigned"},
			{name: "feedback_evaluation_count", columnType: "bigint unsigned"},
			{name: "feedback_adopted_count", columnType: "bigint unsigned"},
			{name: "feedback_positive_count", columnType: "bigint unsigned"},
			{name: "feedback_partial_count", columnType: "bigint unsigned"},
			{name: "feedback_negative_count", columnType: "bigint unsigned"},
			{name: "feedback_score_basis_points", columnType: "int"},
			{name: "feedback_quarantined_until_ms", columnType: "bigint unsigned", nullable: true},
			{name: "embedding_space_id", columnType: "varchar(256)", nullable: true, collation: "ascii_bin"},
			{name: "embedding_content_hash", columnType: "binary(32)", nullable: true},
			{name: "embedding", columnType: "vector(1024)", nullable: true},
			{name: "created_at_ms", columnType: "bigint unsigned"},
			{name: "updated_at_ms", columnType: "bigint unsigned"},
		},
		indexes: []schemaIndex{
			ascendingBTreeIndex("PRIMARY", true, "id"),
			ascendingBTreeIndex("social_memory_entries_scope_hash_key", true, "conversation_id", "kind", "content_hash"),
			ascendingBTreeIndex("social_memory_entries_person_note_key", true, "character_id", "conversation_id", "sender_id"),
			ascendingBTreeIndex("social_memory_entries_feedback_scope_key", true, "id", "character_id", "conversation_id"),
			ascendingBTreeIndex("social_memory_entries_scope_status_idx", false,
				"character_id", "conversation_id", "status", "kind", "feedback_quarantined_until_ms", "updated_at_ms", "id"),
		},
		checks: []schemaCheck{{
			name: "social_memory_entries_invariants_check",
			clause: "((`kind` in ('episode','expression','behavior','person_note')) and (`status` in ('active','suppressed')) and " +
				"((CHAR_LENGTH(`situation`) >= 1) and (CHAR_LENGTH(`situation`) <= 240)) and (`situation` = trim(`situation`)) and (not((`situation` regexp '[[:cntrl:]]'))) and " +
				"((CHAR_LENGTH(`content`) >= 1) and (CHAR_LENGTH(`content`) <= 800)) and (`content` = trim(`content`)) and (not((`content` regexp '[[:cntrl:]]'))) and " +
				"((CHAR_LENGTH(`recall_cue`) >= 1) and (CHAR_LENGTH(`recall_cue`) <= 400)) and (`recall_cue` = trim(`recall_cue`)) and (not((`recall_cue` regexp '[[:cntrl:]]'))) and " +
				"(((`kind` = 'person_note') and (`sender_id` is not null) and (`situation` = `sender_id`) and (CHAR_LENGTH(`content`) <= 240) and " +
				"(`source_start_ms` = `source_end_ms`) and (`status` = 'active') and " +
				"(`feedback_evaluation_count` = 0) and (`feedback_adopted_count` = 0) and (`feedback_positive_count` = 0) and (`feedback_partial_count` = 0) and " +
				"(`feedback_negative_count` = 0) and (`feedback_score_basis_points` = 0) and (`feedback_quarantined_until_ms` is null)) or " +
				"((`kind` <> 'person_note') and (`sender_id` is null) and (`sender_name` = ''))) and " +
				"(`source_start_ms` > 0) and (`source_end_ms` >= `source_start_ms`) and (`feedback_adopted_count` <= `feedback_evaluation_count`) and " +
				"(((`feedback_positive_count` + `feedback_partial_count`) + `feedback_negative_count`) <= `feedback_adopted_count`) and " +
				"((`feedback_score_basis_points` >= -10000) and (`feedback_score_basis_points` <= 10000)) and " +
				"((`status` = 'suppressed') = (`feedback_quarantined_until_ms` is not null)) and " +
				"(((`embedding_space_id` is null) and (`embedding_content_hash` is null) and (`embedding` is null)) or " +
				"((`kind` <> 'person_note') and (`embedding_space_id` is not null) and ((CHAR_LENGTH(`embedding_space_id`) >= 1) and (CHAR_LENGTH(`embedding_space_id`) <= 256)) and " +
				"(`embedding_space_id` = trim(`embedding_space_id`)) and (`embedding_content_hash` is not null) and (`embedding` is not null))) and " +
				"(`updated_at_ms` >= `created_at_ms`))",
		}},
		foreignKeys: []schemaForeignKey{{
			name: "social_memory_entries_conversation_fk", referencedTable: "conversations",
			updateRule: "restrict", deleteRule: "cascade",
			columns: []schemaForeignKeyColumn{
				{name: "conversation_id", referencedColumn: "id", sameSchema: true},
				{name: "character_id", referencedColumn: "character_id", sameSchema: true},
			},
		}},
	},
	{
		name: "knowledge_entries",
		ddl: `CREATE TABLE IF NOT EXISTS knowledge_entries (
  id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  topic VARCHAR(512) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  statement LONGTEXT CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  status VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  verification_basis VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  confidence_basis_points INT UNSIGNED NOT NULL,
  source_conversation_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  source_turn_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  source_url VARCHAR(2048) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NULL,
  source_title VARCHAR(512) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NULL,
  source_content_hash BINARY(32) NULL,
  source_content_type VARCHAR(255) CHARACTER SET ascii COLLATE ascii_bin NULL,
  source_fetched_at_ms BIGINT UNSIGNED NULL,
  source_etag VARCHAR(512) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NULL,
  source_last_modified VARCHAR(512) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NULL,
  reconciler_revision VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NULL,
  evidence_text LONGTEXT CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NULL,
  supersedes_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NULL,
  embedding_space_id VARCHAR(256) CHARACTER SET ascii COLLATE ascii_bin NULL,
  embedding_content_hash BINARY(32) NULL,
  embedding VECTOR(1024) NULL,
  created_at_ms BIGINT UNSIGNED NOT NULL,
  updated_at_ms BIGINT UNSIGNED NOT NULL,
  PRIMARY KEY (id),
  KEY knowledge_entries_status_updated_idx (status, updated_at_ms, id),
  KEY knowledge_entries_source_url_idx (source_url(191)),
  KEY knowledge_entries_source_turn_idx (source_conversation_id, source_turn_id),
  KEY knowledge_entries_supersedes_idx (supersedes_id),
  FULLTEXT INDEX knowledge_entries_text_fts_idx (topic, statement)
    WITH PARSER IK PARSER_PROPERTIES=(ik_mode='max_word'),
  VECTOR INDEX knowledge_entries_embedding_vec_idx (embedding)
    WITH (DISTANCE=COSINE, TYPE=HNSW, LIB=VSAG),
  CONSTRAINT knowledge_entries_source_turn_fk
    FOREIGN KEY (source_conversation_id, source_turn_id)
    REFERENCES conversation_turns (conversation_id, id)
    ON UPDATE RESTRICT ON DELETE RESTRICT,
  CONSTRAINT knowledge_entries_supersedes_fk FOREIGN KEY (supersedes_id)
    REFERENCES knowledge_entries (id) ON UPDATE RESTRICT ON DELETE RESTRICT,
  CONSTRAINT knowledge_entries_identity_check CHECK (
    CHAR_LENGTH(id) BETWEEN 1 AND 128 AND id = TRIM(id) AND
    CHAR_LENGTH(topic) BETWEEN 1 AND 512 AND topic = TRIM(topic) AND
    CHAR_LENGTH(statement) BETWEEN 1 AND 2400 AND statement REGEXP '[^[:space:]]' AND
    status IN ('candidate', 'verified', 'superseded', 'rejected', 'tombstone') AND
    verification_basis IN ('unverified', 'user_confirmed', 'retrieval_ingest') AND
    confidence_basis_points BETWEEN 0 AND 10000 AND
    (supersedes_id IS NULL OR supersedes_id <> id)
  ),
  CONSTRAINT knowledge_entries_source_check CHECK (
    ((status = 'candidate' AND verification_basis = 'unverified' AND source_url IS NULL) OR
      (status = 'verified' AND verification_basis IN ('user_confirmed', 'retrieval_ingest')) OR
      status IN ('superseded', 'rejected', 'tombstone')) AND
    ((source_url IS NULL AND source_title IS NULL AND source_content_hash IS NULL AND
        source_content_type IS NULL AND source_fetched_at_ms IS NULL AND source_etag IS NULL AND
        source_last_modified IS NULL AND reconciler_revision IS NULL AND evidence_text IS NULL) OR
      (source_url IS NOT NULL AND source_url REGEXP '^https?://[^[:space:]]+$' AND
        source_content_hash IS NOT NULL AND source_content_type IS NOT NULL AND
        CHAR_LENGTH(source_content_type) > 0 AND source_fetched_at_ms IS NOT NULL AND
        evidence_text IS NOT NULL AND CHAR_LENGTH(evidence_text) > 0))
  ),
  CONSTRAINT knowledge_entries_embedding_check CHECK (
    ((embedding_space_id IS NULL AND embedding_content_hash IS NULL AND embedding IS NULL) OR
      (embedding_space_id IS NOT NULL AND CHAR_LENGTH(embedding_space_id) BETWEEN 1 AND 256 AND
        embedding_space_id = TRIM(embedding_space_id) AND
        embedding_content_hash IS NOT NULL AND embedding IS NOT NULL)) AND
    updated_at_ms >= created_at_ms
  )
) ORGANIZATION = HEAP`,
		columns: []schemaColumn{
			{name: "id", columnType: "varchar(128)", collation: "ascii_bin"},
			{name: "topic", columnType: "varchar(512)", collation: "utf8mb4_bin"},
			{name: "statement", columnType: "longtext", collation: "utf8mb4_bin"},
			{name: "status", columnType: "varchar(16)", collation: "ascii_bin"},
			{name: "verification_basis", columnType: "varchar(32)", collation: "ascii_bin"},
			{name: "confidence_basis_points", columnType: "int unsigned"},
			{name: "source_conversation_id", columnType: "varchar(128)", collation: "ascii_bin"},
			{name: "source_turn_id", columnType: "varchar(128)", collation: "ascii_bin"},
			{name: "source_url", columnType: "varchar(2048)", nullable: true, collation: "utf8mb4_bin"},
			{name: "source_title", columnType: "varchar(512)", nullable: true, collation: "utf8mb4_bin"},
			{name: "source_content_hash", columnType: "binary(32)", nullable: true},
			{name: "source_content_type", columnType: "varchar(255)", nullable: true, collation: "ascii_bin"},
			{name: "source_fetched_at_ms", columnType: "bigint unsigned", nullable: true},
			{name: "source_etag", columnType: "varchar(512)", nullable: true, collation: "utf8mb4_bin"},
			{name: "source_last_modified", columnType: "varchar(512)", nullable: true, collation: "utf8mb4_bin"},
			{name: "reconciler_revision", columnType: "varchar(128)", nullable: true, collation: "ascii_bin"},
			{name: "evidence_text", columnType: "longtext", nullable: true, collation: "utf8mb4_bin"},
			{name: "supersedes_id", columnType: "varchar(128)", nullable: true, collation: "ascii_bin"},
			{name: "embedding_space_id", columnType: "varchar(256)", nullable: true, collation: "ascii_bin"},
			{name: "embedding_content_hash", columnType: "binary(32)", nullable: true},
			{name: "embedding", columnType: "vector(1024)", nullable: true},
			{name: "created_at_ms", columnType: "bigint unsigned"},
			{name: "updated_at_ms", columnType: "bigint unsigned"},
		},
		indexes: []schemaIndex{
			ascendingBTreeIndex("PRIMARY", true, "id"),
			ascendingBTreeIndex("knowledge_entries_status_updated_idx", false, "status", "updated_at_ms", "id"),
			{
				name: "knowledge_entries_source_url_idx", indexType: "btree",
				columns: []schemaIndexColumn{{
					name: "source_url", subPart: sql.NullInt64{Int64: 191, Valid: true},
					collation: sql.NullString{String: "a", Valid: true},
				}},
			},
			ascendingBTreeIndex("knowledge_entries_source_turn_idx", false, "source_conversation_id", "source_turn_id"),
			ascendingBTreeIndex("knowledge_entries_supersedes_idx", false, "supersedes_id"),
		},
		checks: []schemaCheck{{
			name: "knowledge_entries_identity_check",
			clause: "(((CHAR_LENGTH(`id`) >= 1) and (CHAR_LENGTH(`id`) <= 128)) and (`id` = trim(`id`)) and " +
				"((CHAR_LENGTH(`topic`) >= 1) and (CHAR_LENGTH(`topic`) <= 512)) and (`topic` = trim(`topic`)) and " +
				"((CHAR_LENGTH(`statement`) >= 1) and (CHAR_LENGTH(`statement`) <= 2400)) and (`statement` regexp '[^[:space:]]') and " +
				"(`status` in ('candidate','verified','superseded','rejected','tombstone')) and " +
				"(`verification_basis` in ('unverified','user_confirmed','retrieval_ingest')) and " +
				"((`confidence_basis_points` >= 0) and (`confidence_basis_points` <= 10000)) and " +
				"((`supersedes_id` is null) or (`supersedes_id` <> `id`)))",
		}, {
			name: "knowledge_entries_source_check",
			clause: "" +
				"((((`status` = 'candidate') and (`verification_basis` = 'unverified') and (`source_url` is null)) or " +
				"((`status` = 'verified') and (`verification_basis` in ('user_confirmed','retrieval_ingest'))) or (`status` in ('superseded','rejected','tombstone'))) and " +
				"(((`source_url` is null) and (`source_title` is null) and (`source_content_hash` is null) and (`source_content_type` is null) and " +
				"(`source_fetched_at_ms` is null) and (`source_etag` is null) and (`source_last_modified` is null) and (`reconciler_revision` is null) and (`evidence_text` is null)) or " +
				"((`source_url` is not null) and (`source_url` regexp '^https?://[^[:space:]]+$') and (`source_content_hash` is not null) and " +
				"(`source_content_type` is not null) and (CHAR_LENGTH(`source_content_type`) > 0) and (`source_fetched_at_ms` is not null) and " +
				"(`evidence_text` is not null) and (CHAR_LENGTH(`evidence_text`) > 0))))",
		}, {
			name: "knowledge_entries_embedding_check",
			clause: "" +
				"((((`embedding_space_id` is null) and (`embedding_content_hash` is null) and (`embedding` is null)) or " +
				"((`embedding_space_id` is not null) and ((CHAR_LENGTH(`embedding_space_id`) >= 1) and (CHAR_LENGTH(`embedding_space_id`) <= 256)) and " +
				"(`embedding_space_id` = trim(`embedding_space_id`)) and (`embedding_content_hash` is not null) and (`embedding` is not null))) and " +
				"(`updated_at_ms` >= `created_at_ms`))",
		}},
		foreignKeys: []schemaForeignKey{
			{
				name: "knowledge_entries_source_turn_fk", referencedTable: "conversation_turns",
				updateRule: "restrict", deleteRule: "restrict",
				columns: []schemaForeignKeyColumn{
					{name: "source_conversation_id", referencedColumn: "conversation_id", sameSchema: true},
					{name: "source_turn_id", referencedColumn: "id", sameSchema: true},
				},
			},
			{
				name: "knowledge_entries_supersedes_fk", referencedTable: "knowledge_entries",
				updateRule: "restrict", deleteRule: "restrict",
				columns: []schemaForeignKeyColumn{{name: "supersedes_id", referencedColumn: "id", sameSchema: true}},
			},
		},
	},
}

type cognitiveSpecialIndex struct {
	table       string
	name        string
	columns     []string
	indexType   string
	createToken string
}

var cognitiveSpecialIndexes = [...]cognitiveSpecialIndex{
	{
		table: "personal_memories", name: personalMemoryFullTextIndex,
		columns: []string{"content"}, indexType: "fulltext",
		createToken: "fulltext key `" + personalMemoryFullTextIndex + "` (`content`) " +
			"with parser ik parser_properties=(ik_mode=\"max_word\")",
	},
	{
		table: "personal_memories", name: personalMemoryVectorIndex,
		columns: []string{"embedding"}, indexType: "vector",
		createToken: "vector key `" + personalMemoryVectorIndex + "` (`embedding`) " +
			"with (distance=cosine, type=hnsw, lib=vsag, m=16, ef_construction=200, ef_search=64, sync_mode=async)",
	},
	{
		table: "social_memory_entries", name: socialMemoryFullTextIndex,
		columns: []string{"situation", "content", "recall_cue"}, indexType: "fulltext",
		createToken: "fulltext key `" + socialMemoryFullTextIndex + "` (`situation`, `content`, `recall_cue`) " +
			"with parser ik parser_properties=(ik_mode=\"max_word\")",
	},
	{
		table: "social_memory_entries", name: socialMemoryVectorIndex,
		columns: []string{"embedding"}, indexType: "vector",
		createToken: "vector key `" + socialMemoryVectorIndex + "` (`embedding`) " +
			"with (distance=cosine, type=hnsw, lib=vsag, m=16, ef_construction=200, ef_search=64, sync_mode=async)",
	},
	{
		table: "knowledge_entries", name: knowledgeFullTextIndex,
		columns: []string{"topic", "statement"}, indexType: "fulltext",
		createToken: "fulltext key `" + knowledgeFullTextIndex + "` (`topic`, `statement`) " +
			"with parser ik parser_properties=(ik_mode=\"max_word\")",
	},
	{
		table: "knowledge_entries", name: knowledgeVectorIndex,
		columns: []string{"embedding"}, indexType: "vector",
		createToken: "vector key `" + knowledgeVectorIndex + "` (`embedding`) " +
			"with (distance=cosine, type=hnsw, lib=vsag, m=16, ef_construction=200, ef_search=64, sync_mode=async)",
	},
}

func cognitiveRecordsSchemaChecksum() [sha256.Size]byte {
	statements := make([]string, 0, len(cognitiveRecordsSchema))
	for _, table := range cognitiveRecordsSchema {
		statements = append(statements, table.ddl)
	}
	return schemaDDLChecksum(statements)
}

func applyCognitiveRecordsSchema(ctx context.Context, connection *sql.Conn) error {
	for _, table := range cognitiveRecordsSchema {
		if _, err := connection.ExecContext(ctx, table.ddl); err != nil {
			return fmt.Errorf("create SeekDB cognitive table %s: %w", table.name, err)
		}
	}
	for _, index := range cognitiveSpecialIndexes {
		metadata, err := readLogicalIndexMetadata(ctx, connection, index.table, index.name)
		if err != nil {
			return fmt.Errorf("read SeekDB cognitive index %s: %w", index.name, err)
		}
		if len(metadata) != 0 {
			if err := verifyCognitiveSpecialIndex(ctx, connection, index, metadata); err != nil {
				return fmt.Errorf("existing SeekDB cognitive index %s drifted: %w", index.name, err)
			}
			continue
		}
		return fmt.Errorf("create SeekDB cognitive index %s: table DDL did not create it", index.name)
	}
	return nil
}

func verifyCognitiveRecordsSchema(ctx context.Context, connection *sql.Conn) error {
	if err := verifyExtractionCoordinationSchema(ctx, connection); err != nil {
		return err
	}
	if err := verifyCognitiveRegularTables(ctx, connection); err != nil {
		return err
	}
	for _, index := range cognitiveSpecialIndexes {
		metadata, err := readLogicalIndexMetadata(ctx, connection, index.table, index.name)
		if err != nil {
			return fmt.Errorf("verify SeekDB cognitive index %s metadata: %w", index.name, err)
		}
		if err := verifyCognitiveSpecialIndex(ctx, connection, index, metadata); err != nil {
			return fmt.Errorf("verify SeekDB cognitive index %s: %w", index.name, err)
		}
	}
	return nil
}

func verifyCognitiveRegularTables(ctx context.Context, connection *sql.Conn) error {
	enforcedAvailable, err := schemaCheckEnforcementAvailable(ctx, connection)
	if err != nil {
		return fmt.Errorf("verify SeekDB CHECK enforcement metadata: %w", err)
	}
	for _, table := range cognitiveRecordsSchema {
		ignored := cognitiveIgnoredPhysicalIndexes(table.name)
		if err := verifySchemaTableIgnoringIndexes(ctx, connection, table, enforcedAvailable, ignored); err != nil {
			return err
		}
	}
	return nil
}

func cognitiveIgnoredPhysicalIndexes(table string) []string {
	var names []string
	for _, index := range cognitiveSpecialIndexes {
		if index.table != table {
			continue
		}
		names = append(names, index.name)
		if index.indexType == "vector" {
			names = append(names, index.name+"_index_id_table", index.name+"_index_snapshot_data_table")
		}
	}
	return names
}

func readLogicalIndexMetadata(
	ctx context.Context,
	connection *sql.Conn,
	table string,
	indexName string,
) ([]transcriptRecallIndexMetadata, error) {
	rows, err := connection.QueryContext(ctx, "SHOW INDEX FROM "+table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	positions := make(map[string]int, len(columns))
	for ordinal, column := range columns {
		positions[strings.ToLower(column)] = ordinal
	}
	for _, required := range []string{
		"table", "non_unique", "key_name", "seq_in_index", "column_name",
		"collation", "sub_part", "index_type", "comment", "visible",
	} {
		if _, ok := positions[required]; !ok {
			return nil, fmt.Errorf("SHOW INDEX lacks %s column", required)
		}
	}
	values := make([]sql.RawBytes, len(columns))
	destinations := make([]any, len(columns))
	for ordinal := range values {
		destinations[ordinal] = &values[ordinal]
	}
	var metadata []transcriptRecallIndexMetadata
	for rows.Next() {
		if err := rows.Scan(destinations...); err != nil {
			return nil, err
		}
		if string(values[positions["key_name"]]) != indexName {
			continue
		}
		nonUnique, err := parseShowIndexInteger(values[positions["non_unique"]], "non_unique")
		if err != nil {
			return nil, err
		}
		sequence, err := parseShowIndexInteger(values[positions["seq_in_index"]], "seq_in_index")
		if err != nil {
			return nil, err
		}
		subPart, err := parseShowIndexOptionalInt64(values[positions["sub_part"]], "sub_part")
		if err != nil {
			return nil, err
		}
		metadata = append(metadata, transcriptRecallIndexMetadata{
			table: string(values[positions["table"]]), name: string(values[positions["key_name"]]),
			nonUnique: nonUnique, sequence: sequence,
			column:    string(values[positions["column_name"]]),
			collation: showIndexNullString(values[positions["collation"]]), subPart: subPart,
			indexType: strings.ToLower(string(values[positions["index_type"]])),
			comment:   strings.ToLower(string(values[positions["comment"]])),
			visible:   strings.ToLower(string(values[positions["visible"]])),
		})
	}
	return metadata, rows.Err()
}

func compareCognitiveSpecialIndexMetadata(
	expected cognitiveSpecialIndex,
	actual []transcriptRecallIndexMetadata,
) error {
	if len(actual) != len(expected.columns) {
		return fmt.Errorf("logical index row count = %d, want %d", len(actual), len(expected.columns))
	}
	for ordinal, column := range expected.columns {
		want := transcriptRecallIndexMetadata{
			table: expected.table, name: expected.name, nonUnique: 1, sequence: ordinal + 1,
			column: column, collation: sql.NullString{String: "a", Valid: true},
			indexType: expected.indexType, comment: "available", visible: "yes",
		}
		if actual[ordinal] != want {
			return fmt.Errorf("logical index row %d = %#v, want %#v", ordinal+1, actual[ordinal], want)
		}
	}
	return nil
}

func verifyCognitiveSpecialIndex(
	ctx context.Context,
	connection *sql.Conn,
	expected cognitiveSpecialIndex,
	metadata []transcriptRecallIndexMetadata,
) error {
	if err := compareCognitiveSpecialIndexMetadata(expected, metadata); err != nil {
		return err
	}
	var tableName, createTable string
	if err := connection.QueryRowContext(ctx, "SHOW CREATE TABLE "+expected.table).Scan(&tableName, &createTable); err != nil {
		return err
	}
	return compareCognitiveSpecialIndexCreateTable(expected, tableName, createTable)
}

func compareCognitiveSpecialIndexCreateTable(
	expected cognitiveSpecialIndex,
	tableName string,
	createTable string,
) error {
	if tableName != expected.table {
		return fmt.Errorf("SHOW CREATE TABLE name = %q, want %q", tableName, expected.table)
	}
	normalized := strings.ToLower(normalizeDDL(createTable))
	if !strings.Contains(normalized, expected.createToken) {
		return fmt.Errorf("SHOW CREATE TABLE lacks immutable special index clause %q", expected.createToken)
	}
	if expected.indexType == "vector" && !strings.Contains(normalized, "organization heap") {
		return fmt.Errorf("SHOW CREATE TABLE %s is not ORGANIZATION HEAP", expected.table)
	}
	return nil
}

func cognitiveSpecialIndexNamed(name string) (cognitiveSpecialIndex, bool) {
	index := slices.IndexFunc(cognitiveSpecialIndexes[:], func(candidate cognitiveSpecialIndex) bool {
		return candidate.name == name
	})
	if index < 0 {
		return cognitiveSpecialIndex{}, false
	}
	return cognitiveSpecialIndexes[index], true
}
