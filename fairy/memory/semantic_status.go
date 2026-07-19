package memory

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"fairy/memory/semantic"
)

const (
	SemanticDatabaseStatusMissing = "missing"
	SemanticDatabaseStatusReady   = "ready"
)

// SemanticEmbeddingReadiness reports embedding queue / sqlite-vec health.
// Local ONNX/BGE assets were removed; semantic retrieval requires an API embedder
// attached by the composition root. This status never claims local model readiness.
type SemanticEmbeddingReadiness struct {
	Dimensions     int    `json:"dimensions"`
	DatabaseStatus string `json:"databaseStatus"`
	SemanticStatus string `json:"semanticStatus"`
	Reason         string `json:"reason"`
	PendingJobs    int64  `json:"pendingJobs"`
	RunningJobs    int64  `json:"runningJobs"`
	FailedJobs     int64  `json:"failedJobs"`
	EmbeddedItems  int64  `json:"embeddedItems"`
	VectorRows     int64  `json:"vectorRows"`
}

// LocalSemanticEmbeddingStatus keeps the historical name for MemoryService bindings.
// It now only reports database/queue health; semanticStatus stays unavailable until
// an API embedder is attached at runtime.
func LocalSemanticEmbeddingStatus(root string) (SemanticEmbeddingReadiness, error) {
	if root == "" {
		return SemanticEmbeddingReadiness{}, ErrRootRequired
	}
	status := SemanticEmbeddingReadiness{
		Dimensions:     SemanticEmbeddingDimensions,
		DatabaseStatus: SemanticDatabaseStatusMissing,
		SemanticStatus: string(semantic.StatusUnavailable),
		Reason:         "api_embedder_required",
	}
	if err := fillSemanticEmbeddingDatabaseStatus(root, &status); err != nil {
		return SemanticEmbeddingReadiness{}, err
	}
	return status, nil
}

func fillSemanticEmbeddingDatabaseStatus(root string, status *SemanticEmbeddingReadiness) error {
	path := filepath.Join(root, RelativePath)
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("stat memory database: %w", err)
	}
	db, err := sql.Open(driverName, path)
	if err != nil {
		return fmt.Errorf("opening memory database: %w", err)
	}
	defer db.Close()
	if err := configureSQLiteConnection(db, true); err != nil {
		return err
	}
	status.DatabaseStatus = SemanticDatabaseStatusReady
	pending, err := countScalar(db, "SELECT COUNT(*) FROM memory_embedding_jobs WHERE status = 'pending'")
	if err != nil {
		return err
	}
	running, err := countScalar(db, "SELECT COUNT(*) FROM memory_embedding_jobs WHERE status = 'running'")
	if err != nil {
		return err
	}
	failed, err := countScalar(db, "SELECT COUNT(*) FROM memory_embedding_jobs WHERE status = 'failed'")
	if err != nil {
		return err
	}
	embedded, err := countScalar(db, "SELECT COUNT(*) FROM memory_embedding_items WHERE status = 'embedded'")
	if err != nil {
		return err
	}
	vectors, err := countScalar(db, "SELECT COUNT(*) FROM memory_embedding_vec")
	if err != nil {
		return err
	}
	status.PendingJobs = pending
	status.RunningJobs = running
	status.FailedJobs = failed
	status.EmbeddedItems = embedded
	status.VectorRows = vectors
	return nil
}
