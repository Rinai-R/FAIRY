package memory

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSemanticEmbeddingModelPath(t *testing.T) {
	_, err := SemanticEmbeddingModelPath("")
	if !errors.Is(err, ErrRootRequired) {
		t.Fatalf("SemanticEmbeddingModelPath(empty) error = %v, want %v", err, ErrRootRequired)
	}
	got, err := SemanticEmbeddingModelPath("/tmp/fairy")
	if err != nil {
		t.Fatalf("SemanticEmbeddingModelPath() error = %v", err)
	}
	want := filepath.Join("/tmp/fairy", "intelligence", "embeddings", "bge-small-zh-v1.5", "model.onnx")
	if got != want {
		t.Fatalf("SemanticEmbeddingModelPath() = %q, want %q", got, want)
	}
}

func TestLocalSemanticEmbeddingStatusMissingModel(t *testing.T) {
	root := t.TempDir()
	status, err := LocalSemanticEmbeddingStatus(root)
	if err != nil {
		t.Fatalf("LocalSemanticEmbeddingStatus() error = %v", err)
	}
	if status.ModelID != SemanticEmbeddingModelID || status.Dimensions != SemanticEmbeddingDimensions {
		t.Fatalf("model metadata = %#v", status)
	}
	if status.ModelStatus != SemanticModelStatusMissing || status.RuntimeStatus != SemanticRuntimeStatusUnavailable || status.SemanticStatus != "unavailable" || status.Reason != SemanticEmbeddingReasonModelMissing {
		t.Fatalf("missing model status = %#v", status)
	}
	if status.DatabaseStatus != SemanticDatabaseStatusMissing || status.PendingJobs != 0 || status.RunningJobs != 0 || status.FailedJobs != 0 || status.EmbeddedItems != 0 || status.VectorRows != 0 {
		t.Fatalf("missing database status = %#v", status)
	}
	if _, err := os.Stat(filepath.Dir(status.ModelPath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("status check created model directory or unexpected stat error: %v", err)
	}
}

func TestLocalSemanticEmbeddingStatusDirectoryIsInvalid(t *testing.T) {
	root := t.TempDir()
	modelPath, err := SemanticEmbeddingModelPath(root)
	if err != nil {
		t.Fatalf("SemanticEmbeddingModelPath() error = %v", err)
	}
	if err := os.MkdirAll(modelPath, 0o700); err != nil {
		t.Fatalf("MkdirAll(modelPath) error = %v", err)
	}
	status, err := LocalSemanticEmbeddingStatus(root)
	if err != nil {
		t.Fatalf("LocalSemanticEmbeddingStatus() error = %v", err)
	}
	if status.ModelStatus != SemanticModelStatusInvalid || status.SemanticStatus != "unavailable" || status.Reason != SemanticEmbeddingReasonModelPathInvalid {
		t.Fatalf("directory model status = %#v", status)
	}
}

func TestLocalSemanticEmbeddingStatusPresentModelStillUnavailableWithoutRuntime(t *testing.T) {
	root := t.TempDir()
	modelPath, err := SemanticEmbeddingModelPath(root)
	if err != nil {
		t.Fatalf("SemanticEmbeddingModelPath() error = %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(modelPath), 0o700); err != nil {
		t.Fatalf("MkdirAll(model dir) error = %v", err)
	}
	if err := os.WriteFile(modelPath, []byte("placeholder"), 0o600); err != nil {
		t.Fatalf("WriteFile(model) error = %v", err)
	}
	status, err := NewMemoryService(root).SemanticEmbeddingStatus()
	if err != nil {
		t.Fatalf("SemanticEmbeddingStatus() error = %v", err)
	}
	if status.ModelStatus != SemanticModelStatusPresent || status.RuntimeStatus != SemanticRuntimeStatusUnavailable || status.SemanticStatus != "unavailable" || status.Reason != SemanticEmbeddingReasonRuntimeUnavailable {
		t.Fatalf("present model status = %#v", status)
	}
}

func TestLocalSemanticEmbeddingStatusReportsEmbeddingQueueAndVectorCounts(t *testing.T) {
	root := t.TempDir()
	dbPath, err := DatabasePath(root)
	if err != nil {
		t.Fatalf("DatabasePath() error = %v", err)
	}
	store, err := OpenOrCreate(dbPath)
	if err != nil {
		t.Fatalf("OpenOrCreate() error = %v", err)
	}
	db, err := store.openWrite()
	if err != nil {
		t.Fatalf("openWrite() error = %v", err)
	}
	defer db.Close()

	now := nowUnixMS()
	_, err = db.Exec("INSERT INTO memory_embedding_jobs(id, item_kind, item_id, model_id, dimensions, content_hash, status, created_at_ms, updated_at_ms) VALUES (?1, 'personal_memory', 'pending-memory', ?2, ?3, 'hash-pending', 'pending', ?4, ?4), (?5, 'personal_memory', 'running-memory', ?2, ?3, 'hash-running', 'running', ?4, ?4), (?6, 'knowledge', 'failed-knowledge', ?2, ?3, 'hash-failed', 'failed', ?4, ?4)", newID(), SemanticEmbeddingModelID, SemanticEmbeddingDimensions, now, newID(), newID())
	if err != nil {
		t.Fatalf("insert embedding jobs error = %v", err)
	}
	vector := make([]float32, SemanticEmbeddingDimensions)
	vector[0] = 1
	literal, err := sqliteVecLiteral(vector)
	if err != nil {
		t.Fatalf("sqliteVecLiteral() error = %v", err)
	}
	_, err = db.Exec("INSERT INTO memory_embedding_items(vector_rowid, item_kind, item_id, model_id, dimensions, content_hash, status, created_at_ms, updated_at_ms) VALUES (7001, 'personal_memory', 'embedded-memory', ?1, ?2, 'hash-embedded', 'embedded', ?3, ?3)", SemanticEmbeddingModelID, SemanticEmbeddingDimensions, now)
	if err != nil {
		t.Fatalf("insert embedding item error = %v", err)
	}
	if _, err := db.Exec("INSERT INTO memory_embedding_vec(rowid, embedding) VALUES (7001, ?1)", literal); err != nil {
		t.Fatalf("insert embedding vector error = %v", err)
	}

	status, err := NewMemoryService(root).SemanticEmbeddingStatus()
	if err != nil {
		t.Fatalf("SemanticEmbeddingStatus() error = %v", err)
	}
	if status.DatabaseStatus != SemanticDatabaseStatusReady {
		t.Fatalf("database status = %q, want %q", status.DatabaseStatus, SemanticDatabaseStatusReady)
	}
	if status.PendingJobs != 1 || status.RunningJobs != 1 || status.FailedJobs != 1 || status.EmbeddedItems != 1 || status.VectorRows != 1 {
		t.Fatalf("semantic queue/vector counts = %#v", status)
	}
	if status.ModelStatus != SemanticModelStatusMissing || status.SemanticStatus != "unavailable" {
		t.Fatalf("model/semantic status = %#v", status)
	}
}
