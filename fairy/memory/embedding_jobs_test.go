//go:build sqlite_legacy

package memory

import (
	"database/sql"
	"path/filepath"
	"testing"
)

type embeddingJobRecord struct {
	ItemKind    string
	ItemID      string
	ModelID     string
	Dimensions  int
	ContentHash string
	Status      string
}

func embeddingJobsForItem(t *testing.T, store *Store, itemKind string, itemID string) []embeddingJobRecord {
	t.Helper()
	db, err := store.openReadOnly()
	if err != nil {
		t.Fatalf("openReadOnly() error = %v", err)
	}
	defer db.Close()
	rows, err := db.Query("SELECT item_kind, item_id, model_id, dimensions, content_hash, status FROM memory_embedding_jobs WHERE item_kind = ?1 AND item_id = ?2 ORDER BY created_at_ms ASC, id ASC", itemKind, itemID)
	if err != nil {
		t.Fatalf("query embedding jobs error = %v", err)
	}
	defer rows.Close()
	jobs := make([]embeddingJobRecord, 0)
	for rows.Next() {
		var job embeddingJobRecord
		if err := rows.Scan(&job.ItemKind, &job.ItemID, &job.ModelID, &job.Dimensions, &job.ContentHash, &job.Status); err != nil {
			t.Fatalf("scan embedding job error = %v", err)
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate embedding jobs error = %v", err)
	}
	return jobs
}

func embeddingJobCount(t *testing.T, store *Store) int {
	t.Helper()
	db, err := store.openReadOnly()
	if err != nil {
		t.Fatalf("openReadOnly() error = %v", err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM memory_embedding_jobs").Scan(&count); err != nil {
		t.Fatalf("count embedding jobs error = %v", err)
	}
	return count
}

func assertSinglePendingEmbeddingJob(t *testing.T, store *Store, itemKind string, itemID string, content string) {
	t.Helper()
	jobs := embeddingJobsForItem(t, store, itemKind, itemID)
	if len(jobs) != 1 {
		t.Fatalf("embedding jobs for %s/%s = %#v, want one", itemKind, itemID, jobs)
	}
	job := jobs[0]
	if job.Status != "pending" || job.ModelID != SemanticEmbeddingModelID || job.Dimensions != SemanticEmbeddingDimensions {
		t.Fatalf("embedding job metadata = %#v", job)
	}
	if job.ContentHash != semanticContentHash(content) {
		t.Fatalf("content hash = %q, want %q", job.ContentHash, semanticContentHash(content))
	}
}

func TestEmbeddingJobsForPersonalMemoryLifecycle(t *testing.T) {
	store, characterID := seededMemoryStore(t)
	preference, err := store.CreatePersonalMemory("preference", MemoryScope{Type: "global"}, "喜欢安静", 9000)
	if err != nil {
		t.Fatalf("CreatePersonalMemory(preference) error = %v", err)
	}
	assertSinglePendingEmbeddingJob(t, store, embeddingItemKindPersonalMemory, preference.ID, "喜欢安静")

	legacy, err := store.CreatePersonalMemory("relationship", MemoryScope{Type: "unassigned_legacy"}, "旧关系", 6000)
	if err != nil {
		t.Fatalf("CreatePersonalMemory(legacy) error = %v", err)
	}
	if jobs := embeddingJobsForItem(t, store, embeddingItemKindPersonalMemory, legacy.ID); len(jobs) != 0 {
		t.Fatalf("legacy needs_review memory queued embeddings: %#v", jobs)
	}

	assigned, err := store.AssignLegacyRelationship(legacy.ID, characterID)
	if err != nil {
		t.Fatalf("AssignLegacyRelationship() error = %v", err)
	}
	assertSinglePendingEmbeddingJob(t, store, embeddingItemKindPersonalMemory, assigned.ID, "旧关系")

	revised, err := store.RevisePersonalMemory(preference.ID, "喜欢咖啡", 9300)
	if err != nil {
		t.Fatalf("RevisePersonalMemory() error = %v", err)
	}
	assertSinglePendingEmbeddingJob(t, store, embeddingItemKindPersonalMemory, revised.ID, "喜欢咖啡")
}

func TestEmbeddingJobsForExtractionMutations(t *testing.T) {
	store, err := OpenOrCreate(filepath.Join(t.TempDir(), "fairy.sqlite3"))
	if err != nil {
		t.Fatalf("OpenOrCreate() error = %v", err)
	}
	bootstrap, err := store.OpenOrCreateCharacterConversation("character-1")
	if err != nil {
		t.Fatalf("OpenOrCreateCharacterConversation() error = %v", err)
	}
	turn, err := store.BeginTurn(bootstrap.Conversation.ID, "我喜欢安静")
	if err != nil {
		t.Fatalf("BeginTurn() error = %v", err)
	}
	if _, err := store.CompleteTurn(bootstrap.Conversation.ID, turn.ID, "好，我记住了"); err != nil {
		t.Fatalf("CompleteTurn() error = %v", err)
	}
	batch, err := store.ClaimExtractionBatch(bootstrap.Conversation.ID, 1)
	if err != nil || batch == nil {
		t.Fatalf("ClaimExtractionBatch() = %#v, %v", batch, err)
	}
	results, err := store.CommitMemoryMutations(batch.BatchID, batch.CharacterID, nil, []MemoryMutation{{
		Operation:             "create",
		Kind:                  "preference",
		Scope:                 MemoryScope{Type: "global"},
		Content:               "用户喜欢安静",
		ConfidenceBasisPoints: 9000,
	}})
	if err != nil {
		t.Fatalf("CommitMemoryMutations(create) error = %v", err)
	}
	if len(results) != 1 || results[0].MemoryID == "" {
		t.Fatalf("create results = %#v", results)
	}
	assertSinglePendingEmbeddingJob(t, store, embeddingItemKindPersonalMemory, results[0].MemoryID, "用户喜欢安静")

	turn2, err := store.BeginTurn(bootstrap.Conversation.ID, "我更喜欢咖啡")
	if err != nil {
		t.Fatalf("BeginTurn(2) error = %v", err)
	}
	if _, err := store.CompleteTurn(bootstrap.Conversation.ID, turn2.ID, "好"); err != nil {
		t.Fatalf("CompleteTurn(2) error = %v", err)
	}
	batch2, err := store.ClaimExtractionBatch(bootstrap.Conversation.ID, 1)
	if err != nil || batch2 == nil {
		t.Fatalf("ClaimExtractionBatch(2) = %#v, %v", batch2, err)
	}
	results2, err := store.CommitMemoryMutations(batch2.BatchID, batch2.CharacterID, []string{results[0].MemoryID}, []MemoryMutation{{
		Operation:             "supersede",
		MemoryID:              results[0].MemoryID,
		Kind:                  "preference",
		Scope:                 MemoryScope{Type: "global"},
		Content:               "用户喜欢咖啡",
		ConfidenceBasisPoints: 9300,
	}})
	if err != nil {
		t.Fatalf("CommitMemoryMutations(supersede) error = %v", err)
	}
	if len(results2) != 1 || results2[0].MemoryID == "" {
		t.Fatalf("supersede results = %#v", results2)
	}
	assertSinglePendingEmbeddingJob(t, store, embeddingItemKindPersonalMemory, results2[0].MemoryID, "用户喜欢咖啡")
}

func TestEmbeddingJobForConfirmedKnowledge(t *testing.T) {
	store, conversationID, _, turnID := seededKnowledgeStore(t)
	candidateID := insertKnowledgeFixture(t, store, conversationID, turnID, "candidate", "unverified")
	confirmed, err := store.ConfirmKnowledgeCandidate(candidateID)
	if err != nil {
		t.Fatalf("ConfirmKnowledgeCandidate() error = %v", err)
	}
	assertSinglePendingEmbeddingJob(t, store, embeddingItemKindKnowledge, confirmed.ID, confirmed.Topic+"\n"+confirmed.Statement)
}

func TestEmbeddingJobQueueDedupesSameContentHash(t *testing.T) {
	store, characterID := seededMemoryStore(t)
	record, err := store.CreatePersonalMemory("relationship", MemoryScope{Type: "character", CharacterID: characterID}, "信任角色", 8500)
	if err != nil {
		t.Fatalf("CreatePersonalMemory() error = %v", err)
	}
	before := embeddingJobCount(t, store)
	db, err := store.openWrite()
	if err != nil {
		t.Fatalf("openWrite() error = %v", err)
	}
	_, err = db.Exec("INSERT OR IGNORE INTO memory_embedding_jobs(id, item_kind, item_id, model_id, dimensions, content_hash, status, created_at_ms, updated_at_ms) VALUES (?1, ?2, ?3, ?4, ?5, ?6, 'pending', 1, 1)", newID(), embeddingItemKindPersonalMemory, record.ID, SemanticEmbeddingModelID, SemanticEmbeddingDimensions, semanticContentHash(record.Content))
	if closeErr := db.Close(); closeErr != nil {
		t.Fatalf("db.Close() error = %v", closeErr)
	}
	if err != nil && err != sql.ErrNoRows {
		t.Fatalf("manual duplicate insert error = %v", err)
	}
	if after := embeddingJobCount(t, store); after != before {
		t.Fatalf("embedding job count after duplicate insert = %d, want %d", after, before)
	}
}
