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
	SemanticEmbeddingModelRelativePath = "intelligence/embeddings/bge-small-zh-v1.5/model.onnx"

	SemanticModelStatusMissing = "missing"
	SemanticModelStatusPresent = "present"
	SemanticModelStatusInvalid = "invalid"

	SemanticRuntimeStatusUnavailable = "unavailable"

	SemanticDatabaseStatusMissing = "missing"
	SemanticDatabaseStatusReady   = "ready"

	SemanticEmbeddingReasonModelMissing       = "model_missing"
	SemanticEmbeddingReasonModelPathInvalid   = "model_path_invalid"
	SemanticEmbeddingReasonRuntimeUnavailable = "onnx_runtime_unavailable"
)

// SemanticEmbeddingReadiness reports local semantic embedding readiness without
// creating directories, downloading assets, or loading ONNX runtime.
type SemanticEmbeddingReadiness struct {
	ModelID        string `json:"modelId"`
	Dimensions     int    `json:"dimensions"`
	ModelPath      string `json:"modelPath"`
	ModelStatus    string `json:"modelStatus"`
	RuntimeStatus  string `json:"runtimeStatus"`
	DatabaseStatus string `json:"databaseStatus"`
	SemanticStatus string `json:"semanticStatus"`
	Reason         string `json:"reason"`
	PendingJobs    int64  `json:"pendingJobs"`
	RunningJobs    int64  `json:"runningJobs"`
	FailedJobs     int64  `json:"failedJobs"`
	EmbeddedItems  int64  `json:"embeddedItems"`
	VectorRows     int64  `json:"vectorRows"`
}

func SemanticEmbeddingModelPath(root string) (string, error) {
	if root == "" {
		return "", ErrRootRequired
	}
	return filepath.Join(root, SemanticEmbeddingModelRelativePath), nil
}

func LocalSemanticEmbeddingStatus(root string) (SemanticEmbeddingReadiness, error) {
	modelPath, err := SemanticEmbeddingModelPath(root)
	if err != nil {
		return SemanticEmbeddingReadiness{}, err
	}
	status := SemanticEmbeddingReadiness{
		ModelID:        SemanticEmbeddingModelID,
		Dimensions:     SemanticEmbeddingDimensions,
		ModelPath:      modelPath,
		RuntimeStatus:  SemanticRuntimeStatusUnavailable,
		DatabaseStatus: SemanticDatabaseStatusMissing,
		SemanticStatus: string(semantic.StatusUnavailable),
	}
	if err := fillSemanticEmbeddingDatabaseStatus(root, &status); err != nil {
		return SemanticEmbeddingReadiness{}, err
	}
	info, err := os.Stat(modelPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			status.ModelStatus = SemanticModelStatusMissing
			status.Reason = SemanticEmbeddingReasonModelMissing
			return status, nil
		}
		return SemanticEmbeddingReadiness{}, fmt.Errorf("checking semantic embedding model: %w", err)
	}
	if !info.Mode().IsRegular() {
		status.ModelStatus = SemanticModelStatusInvalid
		status.Reason = SemanticEmbeddingReasonModelPathInvalid
		return status, nil
	}
	status.ModelStatus = SemanticModelStatusPresent
	status.Reason = SemanticEmbeddingReasonRuntimeUnavailable
	return status, nil
}

func fillSemanticEmbeddingDatabaseStatus(root string, status *SemanticEmbeddingReadiness) error {
	dbPath, err := DatabasePath(root)
	if err != nil {
		return err
	}
	db, err := NewStore(dbPath).openReadOnly()
	if err != nil {
		if errors.Is(err, ErrDatabaseMissing) {
			status.DatabaseStatus = SemanticDatabaseStatusMissing
			return nil
		}
		return err
	}
	defer db.Close()
	status.DatabaseStatus = SemanticDatabaseStatusReady
	counts, err := readSemanticEmbeddingCounts(db)
	if err != nil {
		return err
	}
	status.PendingJobs = counts.PendingJobs
	status.RunningJobs = counts.RunningJobs
	status.FailedJobs = counts.FailedJobs
	status.EmbeddedItems = counts.EmbeddedItems
	status.VectorRows = counts.VectorRows
	return nil
}

type semanticEmbeddingCounts struct {
	PendingJobs   int64
	RunningJobs   int64
	FailedJobs    int64
	EmbeddedItems int64
	VectorRows    int64
}

func readSemanticEmbeddingCounts(db *sql.DB) (semanticEmbeddingCounts, error) {
	var counts semanticEmbeddingCounts
	queries := []struct {
		label string
		query string
		dest  *int64
	}{
		{label: "pending embedding jobs", query: "SELECT COUNT(*) FROM memory_embedding_jobs WHERE status = 'pending'", dest: &counts.PendingJobs},
		{label: "running embedding jobs", query: "SELECT COUNT(*) FROM memory_embedding_jobs WHERE status = 'running'", dest: &counts.RunningJobs},
		{label: "failed embedding jobs", query: "SELECT COUNT(*) FROM memory_embedding_jobs WHERE status = 'failed'", dest: &counts.FailedJobs},
		{label: "embedded items", query: "SELECT COUNT(*) FROM memory_embedding_items WHERE status = 'embedded'", dest: &counts.EmbeddedItems},
		{label: "embedding vectors", query: "SELECT COUNT(*) FROM memory_embedding_vec", dest: &counts.VectorRows},
	}
	for _, item := range queries {
		if err := db.QueryRow(item.query).Scan(item.dest); err != nil {
			return semanticEmbeddingCounts{}, fmt.Errorf("counting %s: %w", item.label, err)
		}
	}
	return counts, nil
}
