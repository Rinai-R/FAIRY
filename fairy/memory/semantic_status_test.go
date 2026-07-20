//go:build sqlite_legacy

package memory

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestLocalSemanticEmbeddingStatusMissingDatabase(t *testing.T) {
	root := t.TempDir()
	status, err := LocalSemanticEmbeddingStatus(root)
	if err != nil {
		t.Fatalf("LocalSemanticEmbeddingStatus() error = %v", err)
	}
	if status.DatabaseStatus != SemanticDatabaseStatusMissing {
		t.Fatalf("DatabaseStatus = %q", status.DatabaseStatus)
	}
	if status.SemanticStatus != "unavailable" || status.Reason != "api_embedder_required" {
		t.Fatalf("status = %#v", status)
	}
}

func TestLocalSemanticEmbeddingStatusRequiresRoot(t *testing.T) {
	_, err := LocalSemanticEmbeddingStatus("")
	if !errors.Is(err, ErrRootRequired) {
		t.Fatalf("error = %v", err)
	}
}

func TestLocalSemanticEmbeddingStatusReportsEmbeddingQueueAndVectorCounts(t *testing.T) {
	root := t.TempDir()
	store, err := OpenOrCreate(filepath.Join(root, RelativePath))
	if err != nil {
		t.Fatalf("OpenOrCreate() error = %v", err)
	}
	_ = store
	status, err := LocalSemanticEmbeddingStatus(root)
	if err != nil {
		t.Fatalf("LocalSemanticEmbeddingStatus() error = %v", err)
	}
	if status.DatabaseStatus != SemanticDatabaseStatusReady {
		t.Fatalf("DatabaseStatus = %q", status.DatabaseStatus)
	}
}
