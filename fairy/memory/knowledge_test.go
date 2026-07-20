//go:build sqlite_legacy

package memory

import (
	"path/filepath"
	"testing"
)

func seededKnowledgeStore(t *testing.T) (*Store, string, string, string) {
	t.Helper()
	store, err := OpenOrCreate(filepath.Join(t.TempDir(), "fairy.sqlite3"))
	if err != nil {
		t.Fatalf("OpenOrCreate() error = %v", err)
	}
	bootstrap, err := store.OpenOrCreateCharacterConversation("6a129284-6358-47b0-ad64-2a5907d36c91")
	if err != nil {
		t.Fatalf("OpenOrCreateCharacterConversation() error = %v", err)
	}
	turn, err := store.BeginTurn(bootstrap.Conversation.ID, "你好")
	if err != nil {
		t.Fatalf("BeginTurn() error = %v", err)
	}
	if _, err := store.CompleteTurn(bootstrap.Conversation.ID, turn.ID, "我在。"); err != nil {
		t.Fatalf("CompleteTurn() error = %v", err)
	}
	return store, bootstrap.Conversation.ID, bootstrap.Conversation.CharacterID, turn.ID
}

func insertKnowledgeFixture(t *testing.T, store *Store, conversationID string, turnID string, status string, basis string) string {
	t.Helper()
	db, err := store.openWrite()
	if err != nil {
		t.Fatalf("openWrite() error = %v", err)
	}
	defer db.Close()
	id := newID()
	_, err = db.Exec("INSERT INTO knowledge_entries(id, topic, statement, status, verification_basis, confidence_basis_points, source_conversation_id, source_turn_id, created_at_ms, updated_at_ms) VALUES (?1, '主题陈述系统', '主题陈述系统内容', ?2, ?3, 8000, ?4, ?5, 1, 1)", id, status, basis, conversationID, turnID)
	if err != nil {
		t.Fatalf("insert knowledge fixture error = %v", err)
	}
	return id
}

func insertExtractionBatchFixture(t *testing.T, store *Store, conversationID string, characterID string, turnID string) string {
	t.Helper()
	db, err := store.openWrite()
	if err != nil {
		t.Fatalf("openWrite() error = %v", err)
	}
	defer db.Close()
	id := newID()
	_, err = db.Exec("INSERT INTO extraction_batches(id, conversation_id, character_id, status, first_turn_sequence, last_turn_sequence, error_code, error_message, error_retryable, created_at_ms, updated_at_ms) VALUES (?1, ?2, ?3, 'failed', 1, 1, 'MODEL_FAILED', '模型失败', 1, 1, 1)", id, conversationID, characterID)
	if err != nil {
		t.Fatalf("insert extraction batch error = %v", err)
	}
	_, err = db.Exec("INSERT INTO extraction_batch_turns(batch_id, turn_id, turn_sequence) VALUES (?1, ?2, 1)", id, turnID)
	if err != nil {
		t.Fatalf("insert extraction batch turn error = %v", err)
	}
	_, err = db.Exec("UPDATE conversation_turns SET extraction_state = 'claimed' WHERE id = ?1", turnID)
	if err != nil {
		t.Fatalf("mark turn claimed error = %v", err)
	}
	return id
}

func TestKnowledgeCatalogConfirmAndTombstone(t *testing.T) {
	store, conversationID, _, turnID := seededKnowledgeStore(t)
	candidateID := insertKnowledgeFixture(t, store, conversationID, turnID, "candidate", "unverified")
	verifiedID := insertKnowledgeFixture(t, store, conversationID, turnID, "verified", "user_confirmed")
	catalog, err := store.KnowledgeCatalog()
	if err != nil {
		t.Fatalf("KnowledgeCatalog() error = %v", err)
	}
	if len(catalog.Candidates) != 1 || catalog.Candidates[0].ID != candidateID || len(catalog.Verified) != 1 || catalog.Verified[0].ID != verifiedID {
		t.Fatalf("catalog = %#v", catalog)
	}
	confirmed, err := store.ConfirmKnowledgeCandidate(candidateID)
	if err != nil {
		t.Fatalf("ConfirmKnowledgeCandidate() error = %v", err)
	}
	if confirmed.Status != "verified" || confirmed.VerificationBasis != "user_confirmed" {
		t.Fatalf("confirmed = %#v", confirmed)
	}
	if err := store.TombstoneKnowledge(verifiedID); err != nil {
		t.Fatalf("TombstoneKnowledge() error = %v", err)
	}
}

func TestExtractionBatchCatalogAndRetry(t *testing.T) {
	store, conversationID, characterID, turnID := seededKnowledgeStore(t)
	batchID := insertExtractionBatchFixture(t, store, conversationID, characterID, turnID)
	catalog, err := store.ExtractionBatchCatalog(characterID)
	if err != nil {
		t.Fatalf("ExtractionBatchCatalog() error = %v", err)
	}
	if len(catalog.Failed) != 1 || catalog.Failed[0].ID != batchID || catalog.Failed[0].Error == nil || !catalog.Failed[0].Error.Retryable {
		t.Fatalf("catalog = %#v", catalog)
	}
	if err := store.RetryExtractionBatch(batchID); err != nil {
		t.Fatalf("RetryExtractionBatch() error = %v", err)
	}
	catalog, err = store.ExtractionBatchCatalog(characterID)
	if err != nil {
		t.Fatalf("ExtractionBatchCatalog() after retry error = %v", err)
	}
	if len(catalog.Failed) != 0 {
		t.Fatalf("catalog after retry = %#v", catalog)
	}
}
