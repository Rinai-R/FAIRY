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
	SemanticEmbeddingModelRelativePath     = "intelligence/embeddings/bge-small-zh-v1.5/model.onnx"
	SemanticEmbeddingModelDataRelativePath = "intelligence/embeddings/bge-small-zh-v1.5/model_quantized.onnx_data"
	SemanticEmbeddingTokenizerRelativePath = "intelligence/embeddings/bge-small-zh-v1.5/tokenizer.json"

	SemanticModelStatusMissing     = "missing"
	SemanticModelStatusPresent     = "present"
	SemanticModelStatusInvalid     = "invalid"
	SemanticModelDataStatusMissing = "missing"
	SemanticModelDataStatusPresent = "present"
	SemanticModelDataStatusInvalid = "invalid"
	SemanticTokenizerStatusMissing = "missing"
	SemanticTokenizerStatusPresent = "present"
	SemanticTokenizerStatusInvalid = "invalid"

	SemanticRuntimeStatusUnavailable = "unavailable"
	SemanticRuntimeStatusMissing     = "missing"
	SemanticRuntimeStatusInvalid     = "invalid"
	SemanticRuntimeStatusReady       = "ready"

	SemanticDatabaseStatusMissing = "missing"
	SemanticDatabaseStatusReady   = "ready"

	SemanticEmbeddingReasonModelMissing       = "model_missing"
	SemanticEmbeddingReasonModelPathInvalid   = "model_path_invalid"
	SemanticEmbeddingReasonModelDataMissing   = "model_data_missing"
	SemanticEmbeddingReasonModelDataInvalid   = "model_data_path_invalid"
	SemanticEmbeddingReasonTokenizerMissing   = "tokenizer_missing"
	SemanticEmbeddingReasonTokenizerInvalid   = "tokenizer_path_invalid"
	SemanticEmbeddingReasonRuntimeMissing     = "onnx_runtime_missing"
	SemanticEmbeddingReasonRuntimeInvalid     = "onnx_runtime_path_invalid"
	SemanticEmbeddingReasonRuntimeUnavailable = "onnx_runtime_unavailable"
)

// SemanticEmbeddingReadiness reports local semantic embedding resource
// readiness without creating directories, downloading assets, or loading ONNX
// runtime.
type SemanticEmbeddingReadiness struct {
	ModelID         string `json:"modelId"`
	Dimensions      int    `json:"dimensions"`
	ModelPath       string `json:"modelPath"`
	ModelDataPath   string `json:"modelDataPath"`
	TokenizerPath   string `json:"tokenizerPath"`
	ModelStatus     string `json:"modelStatus"`
	ModelDataStatus string `json:"modelDataStatus"`
	TokenizerStatus string `json:"tokenizerStatus"`
	RuntimeStatus   string `json:"runtimeStatus"`
	DatabaseStatus  string `json:"databaseStatus"`
	SemanticStatus  string `json:"semanticStatus"`
	Reason          string `json:"reason"`
	PendingJobs     int64  `json:"pendingJobs"`
	RunningJobs     int64  `json:"runningJobs"`
	FailedJobs      int64  `json:"failedJobs"`
	EmbeddedItems   int64  `json:"embeddedItems"`
	VectorRows      int64  `json:"vectorRows"`
}

func SemanticEmbeddingModelPath(root string) (string, error) {
	if root == "" {
		return "", ErrRootRequired
	}
	return filepath.Join(root, SemanticEmbeddingModelRelativePath), nil
}

func SemanticEmbeddingModelDataPath(root string) (string, error) {
	if root == "" {
		return "", ErrRootRequired
	}
	return filepath.Join(root, SemanticEmbeddingModelDataRelativePath), nil
}

func SemanticEmbeddingTokenizerPath(root string) (string, error) {
	if root == "" {
		return "", ErrRootRequired
	}
	return filepath.Join(root, SemanticEmbeddingTokenizerRelativePath), nil
}

func LocalSemanticEmbeddingStatus(root string) (SemanticEmbeddingReadiness, error) {
	modelPath, err := SemanticEmbeddingModelPath(root)
	if err != nil {
		return SemanticEmbeddingReadiness{}, err
	}
	modelDataPath, err := SemanticEmbeddingModelDataPath(root)
	if err != nil {
		return SemanticEmbeddingReadiness{}, err
	}
	tokenizerPath, err := SemanticEmbeddingTokenizerPath(root)
	if err != nil {
		return SemanticEmbeddingReadiness{}, err
	}
	status := SemanticEmbeddingReadiness{
		ModelID:        SemanticEmbeddingModelID,
		Dimensions:     SemanticEmbeddingDimensions,
		ModelPath:      modelPath,
		ModelDataPath:  modelDataPath,
		TokenizerPath:  tokenizerPath,
		RuntimeStatus:  SemanticRuntimeStatusUnavailable,
		DatabaseStatus: SemanticDatabaseStatusMissing,
		SemanticStatus: string(semantic.StatusUnavailable),
	}
	if err := fillSemanticEmbeddingDatabaseStatus(root, &status); err != nil {
		return SemanticEmbeddingReadiness{}, err
	}
	modelStatus, reason, err := semanticAssetStatus(modelPath, SemanticModelStatusPresent, SemanticModelStatusMissing, SemanticModelStatusInvalid, SemanticEmbeddingReasonModelMissing, SemanticEmbeddingReasonModelPathInvalid, "semantic embedding model")
	if err != nil {
		return SemanticEmbeddingReadiness{}, err
	}
	status.ModelStatus = modelStatus
	if reason != "" {
		status.Reason = reason
		return status, nil
	}
	modelDataStatus, reason, err := semanticAssetStatus(modelDataPath, SemanticModelDataStatusPresent, SemanticModelDataStatusMissing, SemanticModelDataStatusInvalid, SemanticEmbeddingReasonModelDataMissing, SemanticEmbeddingReasonModelDataInvalid, "semantic embedding model data")
	if err != nil {
		return SemanticEmbeddingReadiness{}, err
	}
	status.ModelDataStatus = modelDataStatus
	if reason != "" {
		status.Reason = reason
		return status, nil
	}
	tokenizerStatus, reason, err := semanticAssetStatus(tokenizerPath, SemanticTokenizerStatusPresent, SemanticTokenizerStatusMissing, SemanticTokenizerStatusInvalid, SemanticEmbeddingReasonTokenizerMissing, SemanticEmbeddingReasonTokenizerInvalid, "semantic embedding tokenizer")
	if err != nil {
		return SemanticEmbeddingReadiness{}, err
	}
	status.TokenizerStatus = tokenizerStatus
	if reason != "" {
		status.Reason = reason
		return status, nil
	}
	runtimePath, err := SemanticEmbeddingRuntimeLibraryPath(root)
	if err != nil {
		return SemanticEmbeddingReadiness{}, err
	}
	runtimeStatus, reason, err := semanticAssetStatus(runtimePath, SemanticRuntimeStatusReady, SemanticRuntimeStatusMissing, SemanticRuntimeStatusInvalid, SemanticEmbeddingReasonRuntimeMissing, SemanticEmbeddingReasonRuntimeInvalid, "ONNX Runtime shared library")
	if err != nil {
		return SemanticEmbeddingReadiness{}, err
	}
	status.RuntimeStatus = runtimeStatus
	if reason != "" {
		status.Reason = reason
		return status, nil
	}
	status.SemanticStatus = string(semantic.StatusReady)
	return status, nil
}

func semanticAssetStatus(path string, present string, missing string, invalid string, missingReason string, invalidReason string, label string) (string, string, error) {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return missing, missingReason, nil
		}
		return "", "", fmt.Errorf("checking %s: %w", label, err)
	}
	if !info.Mode().IsRegular() {
		return invalid, invalidReason, nil
	}
	return present, "", nil
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
