package memory

import (
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"fairy/memory/semantic"
)

const (
	embeddingJobErrorStaleItem     = "stale_item"
	embeddingJobErrorStaleContent  = "stale_content"
	embeddingJobErrorEmbedFailed   = "embed_failed"
	embeddingJobErrorInvalidVector = "invalid_vector"
)

var errEmbeddingJobStale = errors.New("embedding job item is no longer current")

// EmbeddingJobResult summarizes one explicit worker pass.
type EmbeddingJobResult struct {
	SemanticStatus string `json:"semanticStatus"`
	Processed      int    `json:"processed"`
	Succeeded      int    `json:"succeeded"`
	Failed         int    `json:"failed"`
	Skipped        int    `json:"skipped"`
}

type embeddingJob struct {
	ID          string
	ItemKind    string
	ItemID      string
	ModelID     string
	Dimensions  int
	ContentHash string
}

// ProcessEmbeddingJobs consumes pending embedding jobs with an injected embedder.
//
// It does not start background goroutines or download model assets. Unavailable
// embedders leave pending jobs untouched so callers can retry after local model
// setup completes.
func (s *Store) ProcessEmbeddingJobs(embedder semantic.Embedder, limit int) (EmbeddingJobResult, error) {
	result := EmbeddingJobResult{SemanticStatus: string(semantic.StatusUnavailable)}
	if limit <= 0 {
		return result, nil
	}
	if embedder == nil || !embedder.Ready() {
		return result, nil
	}
	result.SemanticStatus = string(embedder.Status())
	if result.SemanticStatus == "" || result.SemanticStatus == string(semantic.StatusUnavailable) {
		result.SemanticStatus = string(semantic.StatusReady)
	}
	if dims := embedder.Dims(); dims != SemanticEmbeddingDimensions {
		return result, fmt.Errorf("embedding dimensions = %d, want %d", dims, SemanticEmbeddingDimensions)
	}

	db, err := s.openWrite()
	if err != nil {
		return result, err
	}
	defer db.Close()

	for result.Processed < limit {
		job, ok, err := claimNextEmbeddingJob(db, nowUnixMS())
		if err != nil {
			return result, err
		}
		if !ok {
			break
		}
		result.Processed++

		content, err := embeddingJobContent(db, job)
		if err != nil {
			code := embeddingJobErrorStaleItem
			if errors.Is(err, errEmbeddingJobStale) {
				code = embeddingJobErrorStaleItem
			}
			if failErr := finishEmbeddingJobFailed(db, job, code, err.Error(), false, nowUnixMS()); failErr != nil {
				return result, failErr
			}
			result.Failed++
			continue
		}
		if semanticContentHash(content) != job.ContentHash {
			if err := finishEmbeddingJobFailed(db, job, embeddingJobErrorStaleContent, "embedding job content hash no longer matches current item", false, nowUnixMS()); err != nil {
				return result, err
			}
			result.Failed++
			continue
		}

		vectors, err := embedder.Embed([]string{content})
		if err != nil {
			if failErr := finishEmbeddingJobFailed(db, job, embeddingJobErrorEmbedFailed, err.Error(), true, nowUnixMS()); failErr != nil {
				return result, failErr
			}
			result.Failed++
			continue
		}
		if len(vectors) != 1 {
			if err := finishEmbeddingJobFailed(db, job, embeddingJobErrorInvalidVector, fmt.Sprintf("embedder returned %d vectors for one text", len(vectors)), false, nowUnixMS()); err != nil {
				return result, err
			}
			result.Failed++
			continue
		}
		vectorLiteral, err := sqliteVecLiteral(vectors[0])
		if err != nil {
			if failErr := finishEmbeddingJobFailed(db, job, embeddingJobErrorInvalidVector, err.Error(), false, nowUnixMS()); failErr != nil {
				return result, failErr
			}
			result.Failed++
			continue
		}
		if err := finishEmbeddingJobSucceeded(db, job, vectorLiteral, nowUnixMS()); err != nil {
			return result, err
		}
		result.Succeeded++
	}
	return result, nil
}

func claimNextEmbeddingJob(db *sql.DB, now int64) (embeddingJob, bool, error) {
	tx, err := db.Begin()
	if err != nil {
		return embeddingJob{}, false, fmt.Errorf("beginning embedding job claim: %w", err)
	}
	defer tx.Rollback()

	var job embeddingJob
	err = tx.QueryRow(`SELECT id, item_kind, item_id, model_id, dimensions, content_hash
FROM memory_embedding_jobs
WHERE status = 'pending' AND model_id = ?1 AND dimensions = ?2
ORDER BY updated_at_ms ASC, id ASC
LIMIT 1`, SemanticEmbeddingModelID, SemanticEmbeddingDimensions).Scan(&job.ID, &job.ItemKind, &job.ItemID, &job.ModelID, &job.Dimensions, &job.ContentHash)
	if errors.Is(err, sql.ErrNoRows) {
		return embeddingJob{}, false, nil
	}
	if err != nil {
		return embeddingJob{}, false, fmt.Errorf("querying pending embedding job: %w", err)
	}
	changed, err := tx.Exec("UPDATE memory_embedding_jobs SET status = 'running', updated_at_ms = ?2 WHERE id = ?1 AND status = 'pending'", job.ID, now)
	if err != nil {
		return embeddingJob{}, false, fmt.Errorf("claiming embedding job: %w", err)
	}
	count, err := changed.RowsAffected()
	if err != nil {
		return embeddingJob{}, false, fmt.Errorf("checking embedding job claim: %w", err)
	}
	if count != 1 {
		return embeddingJob{}, false, nil
	}
	if err := tx.Commit(); err != nil {
		return embeddingJob{}, false, fmt.Errorf("committing embedding job claim: %w", err)
	}
	return job, true, nil
}

func embeddingJobContent(db *sql.DB, job embeddingJob) (string, error) {
	var content string
	var err error
	switch job.ItemKind {
	case embeddingItemKindPersonalMemory:
		err = db.QueryRow("SELECT content FROM personal_memories WHERE id = ?1 AND review_status = 'ready' AND status = 'active'", job.ItemID).Scan(&content)
	case embeddingItemKindKnowledge:
		err = db.QueryRow("SELECT topic || char(10) || statement FROM knowledge_entries WHERE id = ?1 AND status = 'verified'", job.ItemID).Scan(&content)
	default:
		return "", fmt.Errorf("%w: unsupported item kind %q", errEmbeddingJobStale, job.ItemKind)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return "", errEmbeddingJobStale
	}
	if err != nil {
		return "", fmt.Errorf("reading embedding job content: %w", err)
	}
	return content, nil
}

func finishEmbeddingJobSucceeded(db *sql.DB, job embeddingJob, vectorLiteral string, now int64) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("beginning embedding job success transaction: %w", err)
	}
	defer tx.Rollback()

	if err := upsertEmbeddingItem(tx, job, "pending", "", "", now); err != nil {
		return err
	}
	var vectorRowID int64
	if err := tx.QueryRow("SELECT vector_rowid FROM memory_embedding_items WHERE item_kind = ?1 AND item_id = ?2 AND model_id = ?3", job.ItemKind, job.ItemID, job.ModelID).Scan(&vectorRowID); err != nil {
		return fmt.Errorf("reading embedding item rowid: %w", err)
	}
	if _, err := tx.Exec("DELETE FROM memory_embedding_vec WHERE rowid = ?1", vectorRowID); err != nil {
		return fmt.Errorf("clearing previous embedding vector: %w", err)
	}
	if _, err := tx.Exec("INSERT INTO memory_embedding_vec(rowid, embedding) VALUES (?1, ?2)", vectorRowID, vectorLiteral); err != nil {
		return fmt.Errorf("writing embedding vector: %w", err)
	}
	if _, err := tx.Exec("UPDATE memory_embedding_items SET status = 'embedded', error_code = NULL, error_message = NULL, updated_at_ms = ?4 WHERE item_kind = ?1 AND item_id = ?2 AND model_id = ?3", job.ItemKind, job.ItemID, job.ModelID, now); err != nil {
		return fmt.Errorf("marking embedding item embedded: %w", err)
	}
	if _, err := tx.Exec("UPDATE memory_embedding_jobs SET status = 'succeeded', error_code = NULL, error_message = NULL, retryable = 0, updated_at_ms = ?2 WHERE id = ?1", job.ID, now); err != nil {
		return fmt.Errorf("marking embedding job succeeded: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing embedding job success: %w", err)
	}
	return nil
}

func finishEmbeddingJobFailed(db *sql.DB, job embeddingJob, code string, message string, retryable bool, now int64) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("beginning embedding job failure transaction: %w", err)
	}
	defer tx.Rollback()

	message = cleanEmbeddingErrorMessage(message)
	if err := upsertEmbeddingItem(tx, job, "failed", code, message, now); err != nil {
		return err
	}
	retryableValue := 0
	if retryable {
		retryableValue = 1
	}
	if _, err := tx.Exec("UPDATE memory_embedding_jobs SET status = 'failed', error_code = ?2, error_message = ?3, retryable = ?4, updated_at_ms = ?5 WHERE id = ?1", job.ID, code, message, retryableValue, now); err != nil {
		return fmt.Errorf("marking embedding job failed: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing embedding job failure: %w", err)
	}
	return nil
}

func upsertEmbeddingItem(tx *sql.Tx, job embeddingJob, status string, code string, message string, now int64) error {
	_, err := tx.Exec(`INSERT INTO memory_embedding_items(item_kind, item_id, model_id, dimensions, content_hash, status, error_code, error_message, created_at_ms, updated_at_ms)
VALUES (?1, ?2, ?3, ?4, ?5, ?6, NULLIF(?7, ''), NULLIF(?8, ''), ?9, ?9)
ON CONFLICT(item_kind, item_id, model_id) DO UPDATE SET
  dimensions = excluded.dimensions,
  content_hash = excluded.content_hash,
  status = excluded.status,
  error_code = excluded.error_code,
  error_message = excluded.error_message,
  updated_at_ms = excluded.updated_at_ms`, job.ItemKind, job.ItemID, job.ModelID, job.Dimensions, job.ContentHash, status, code, message, now)
	if err != nil {
		return fmt.Errorf("upserting embedding item: %w", err)
	}
	return nil
}

func sqliteVecLiteral(vector []float32) (string, error) {
	if len(vector) != SemanticEmbeddingDimensions {
		return "", fmt.Errorf("embedding vector dimensions = %d, want %d", len(vector), SemanticEmbeddingDimensions)
	}
	var builder strings.Builder
	builder.Grow(len(vector) * 8)
	builder.WriteByte('[')
	for index, value := range vector {
		asFloat := float64(value)
		if math.IsNaN(asFloat) || math.IsInf(asFloat, 0) {
			return "", fmt.Errorf("embedding vector contains non-finite value at index %d", index)
		}
		if index > 0 {
			builder.WriteByte(',')
		}
		builder.WriteString(strconv.FormatFloat(asFloat, 'g', -1, 32))
	}
	builder.WriteByte(']')
	return builder.String(), nil
}

func cleanEmbeddingErrorMessage(message string) string {
	message = strings.TrimSpace(message)
	if message == "" {
		return "embedding job failed"
	}
	message = strings.Join(strings.Fields(message), " ")
	const maxErrorMessageLength = 500
	if len(message) > maxErrorMessageLength {
		return message[:maxErrorMessageLength]
	}
	return message
}
