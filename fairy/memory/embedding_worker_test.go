package memory

import (
	"errors"
	"slices"
	"testing"

	"fairy/memory/semantic"
)

type fakeEmbeddingWorkerEmbedder struct {
	ready  bool
	status semantic.Status
	dims   int
	err    error
	texts  []string
}

func (f *fakeEmbeddingWorkerEmbedder) Ready() bool { return f.ready }

func (f *fakeEmbeddingWorkerEmbedder) Status() semantic.Status {
	if f.status != "" {
		return f.status
	}
	if f.ready {
		return semantic.StatusReady
	}
	return semantic.StatusUnavailable
}

func (f *fakeEmbeddingWorkerEmbedder) Dims() int { return f.dims }

func (f *fakeEmbeddingWorkerEmbedder) Embed(texts []string) ([][]float32, error) {
	f.texts = append(f.texts, texts...)
	if f.err != nil {
		return nil, f.err
	}
	vectors := make([][]float32, len(texts))
	for index := range texts {
		vector := make([]float32, SemanticEmbeddingDimensions)
		vector[index%SemanticEmbeddingDimensions] = 1
		vectors[index] = vector
	}
	return vectors, nil
}

func TestProcessEmbeddingJobsWritesSQLiteVecRows(t *testing.T) {
	store, characterID := seededMemoryStore(t)
	memoryRecord, err := store.CreatePersonalMemory("relationship", MemoryScope{Type: "character", CharacterID: characterID}, "信任角色", 9000)
	if err != nil {
		t.Fatalf("CreatePersonalMemory() error = %v", err)
	}
	conversationID, turnID := latestSourceTurnFixture(t, store)
	candidateID := insertKnowledgeFixture(t, store, conversationID, turnID, "candidate", "unverified")
	knowledgeRecord, err := store.ConfirmKnowledgeCandidate(candidateID)
	if err != nil {
		t.Fatalf("ConfirmKnowledgeCandidate() error = %v", err)
	}

	embedder := &fakeEmbeddingWorkerEmbedder{ready: true, status: semantic.StatusReady, dims: SemanticEmbeddingDimensions}
	result, err := store.ProcessEmbeddingJobs(embedder, 10)
	if err != nil {
		t.Fatalf("ProcessEmbeddingJobs() error = %v", err)
	}
	if result.Processed != 2 || result.Succeeded != 2 || result.Failed != 0 || result.SemanticStatus != string(semantic.StatusReady) {
		t.Fatalf("ProcessEmbeddingJobs() result = %#v", result)
	}
	wantTexts := []string{memoryRecord.Content, knowledgeRecord.Topic + "\n" + knowledgeRecord.Statement}
	for _, want := range wantTexts {
		if !slices.Contains(embedder.texts, want) {
			t.Fatalf("embedder texts = %#v, missing %q", embedder.texts, want)
		}
	}
	assertEmbeddedItemAndVector(t, store, embeddingItemKindPersonalMemory, memoryRecord.ID)
	assertEmbeddedItemAndVector(t, store, embeddingItemKindKnowledge, knowledgeRecord.ID)
}

func TestProcessEmbeddingJobsUnavailableLeavesPending(t *testing.T) {
	store, _ := seededMemoryStore(t)
	record, err := store.CreatePersonalMemory("preference", MemoryScope{Type: "global"}, "喜欢安静", 9000)
	if err != nil {
		t.Fatalf("CreatePersonalMemory() error = %v", err)
	}
	result, err := store.ProcessEmbeddingJobs(semantic.UnavailableEmbedder{}, 10)
	if err != nil {
		t.Fatalf("ProcessEmbeddingJobs(unavailable) error = %v", err)
	}
	if result.Processed != 0 || result.Succeeded != 0 || result.Failed != 0 || result.SemanticStatus != string(semantic.StatusUnavailable) {
		t.Fatalf("ProcessEmbeddingJobs(unavailable) result = %#v", result)
	}
	jobs := embeddingJobsForItem(t, store, embeddingItemKindPersonalMemory, record.ID)
	if len(jobs) != 1 || jobs[0].Status != "pending" {
		t.Fatalf("jobs after unavailable worker = %#v", jobs)
	}
}

func TestProcessEmbeddingJobsRecordsEmbedderFailure(t *testing.T) {
	store, _ := seededMemoryStore(t)
	record, err := store.CreatePersonalMemory("preference", MemoryScope{Type: "global"}, "喜欢安静", 9000)
	if err != nil {
		t.Fatalf("CreatePersonalMemory() error = %v", err)
	}
	embedder := &fakeEmbeddingWorkerEmbedder{ready: true, status: semantic.StatusReady, dims: SemanticEmbeddingDimensions, err: errors.New("encoder failed")}
	result, err := store.ProcessEmbeddingJobs(embedder, 10)
	if err != nil {
		t.Fatalf("ProcessEmbeddingJobs(failing) error = %v", err)
	}
	if result.Processed != 1 || result.Succeeded != 0 || result.Failed != 1 {
		t.Fatalf("ProcessEmbeddingJobs(failing) result = %#v", result)
	}

	db, err := store.openReadOnly()
	if err != nil {
		t.Fatalf("openReadOnly() error = %v", err)
	}
	defer db.Close()
	var jobStatus, errorCode string
	var retryable int
	if err := db.QueryRow("SELECT status, error_code, retryable FROM memory_embedding_jobs WHERE item_kind = ?1 AND item_id = ?2", embeddingItemKindPersonalMemory, record.ID).Scan(&jobStatus, &errorCode, &retryable); err != nil {
		t.Fatalf("query failed embedding job error = %v", err)
	}
	if jobStatus != "failed" || errorCode != embeddingJobErrorEmbedFailed || retryable != 1 {
		t.Fatalf("failed job metadata = status %q code %q retryable %d", jobStatus, errorCode, retryable)
	}
	var itemStatus string
	if err := db.QueryRow("SELECT status FROM memory_embedding_items WHERE item_kind = ?1 AND item_id = ?2", embeddingItemKindPersonalMemory, record.ID).Scan(&itemStatus); err != nil {
		t.Fatalf("query failed embedding item error = %v", err)
	}
	if itemStatus == "embedded" {
		t.Fatalf("failed item status = %q, want not embedded", itemStatus)
	}
}

func assertEmbeddedItemAndVector(t *testing.T, store *Store, itemKind string, itemID string) {
	t.Helper()
	db, err := store.openReadOnly()
	if err != nil {
		t.Fatalf("openReadOnly() error = %v", err)
	}
	defer db.Close()

	var vectorRowID int64
	var itemStatus, jobStatus string
	if err := db.QueryRow(`SELECT i.vector_rowid, i.status, j.status
FROM memory_embedding_items i
JOIN memory_embedding_jobs j ON j.item_kind = i.item_kind AND j.item_id = i.item_id AND j.model_id = i.model_id
WHERE i.item_kind = ?1 AND i.item_id = ?2 AND i.model_id = ?3`, itemKind, itemID, SemanticEmbeddingModelID).Scan(&vectorRowID, &itemStatus, &jobStatus); err != nil {
		t.Fatalf("query embedded item error = %v", err)
	}
	if vectorRowID == 0 || itemStatus != "embedded" || jobStatus != "succeeded" {
		t.Fatalf("embedding statuses = rowid %d item %q job %q", vectorRowID, itemStatus, jobStatus)
	}
	var vecCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM memory_embedding_vec WHERE rowid = ?1", vectorRowID).Scan(&vecCount); err != nil {
		t.Fatalf("query vec row error = %v", err)
	}
	if vecCount != 1 {
		t.Fatalf("vec row count = %d, want 1", vecCount)
	}
}

func latestSourceTurnFixture(t *testing.T, store *Store) (string, string) {
	t.Helper()
	db, err := store.openReadOnly()
	if err != nil {
		t.Fatalf("openReadOnly() error = %v", err)
	}
	defer db.Close()
	var conversationID, turnID string
	if err := db.QueryRow(`SELECT c.id, t.id
FROM conversations c
JOIN conversation_turns t ON t.conversation_id = c.id
ORDER BY c.updated_at_ms DESC, t.sequence DESC
LIMIT 1`).Scan(&conversationID, &turnID); err != nil {
		t.Fatalf("query latest source turn error = %v", err)
	}
	return conversationID, turnID
}
