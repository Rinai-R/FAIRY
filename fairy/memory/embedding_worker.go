package memory

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"fairy/memory/semantic"
	"fairy/vectorindex"

	"github.com/google/uuid"
)

const (
	embeddingJobErrorStaleItem     = "stale_item"
	embeddingJobErrorStaleContent  = "stale_content"
	embeddingJobErrorEmbedFailed   = "embed_failed"
	embeddingJobErrorInvalidVector = "invalid_vector"
)

var errEmbeddingJobStale = errors.New("embedding job item is no longer current")

type VectorIndex interface {
	Ready(context.Context) error
	Upsert(context.Context, vectorindex.Point) error
}

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
	PointID     string
	ContentHash string
}

type embeddingJobPayload struct {
	Content     string
	ScopeType   string
	CharacterID string
}

func (s *Store) ProcessEmbeddingJobsWithVectorIndex(ctx context.Context, embedder semantic.Embedder, index VectorIndex, limit int) (EmbeddingJobResult, error) {
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
	if s == nil || s.pool == nil {
		return result, errors.New("postgres embedding worker requires PostgreSQL store")
	}
	if index == nil {
		return result, errors.New("vector index is required")
	}
	if err := index.Ready(ctx); err != nil {
		return result, fmt.Errorf("vector index is unavailable: %w", err)
	}
	jobs, err := s.claimEmbeddingJobsPostgres(ctx, min(limit, maxEmbeddingJobsPerPass), nowUnixMS())
	if err != nil {
		return result, err
	}
	for _, job := range jobs {
		result.Processed++
		payload, err := s.embeddingJobPayloadPostgres(ctx, job)
		if err != nil {
			if failErr := s.finishEmbeddingJobFailedPostgres(ctx, job, embeddingJobErrorStaleItem, err.Error(), false, nowUnixMS()); failErr != nil {
				return result, failErr
			}
			result.Failed++
			continue
		}
		if semanticContentHash(payload.Content) != job.ContentHash {
			if err := s.finishEmbeddingJobFailedPostgres(ctx, job, embeddingJobErrorStaleContent, "embedding job content hash no longer matches current item", false, nowUnixMS()); err != nil {
				return result, err
			}
			result.Failed++
			continue
		}
		vectors, err := embedder.Embed([]string{payload.Content})
		if err != nil {
			if failErr := s.finishEmbeddingJobFailedPostgres(ctx, job, embeddingJobErrorEmbedFailed, err.Error(), true, nowUnixMS()); failErr != nil {
				return result, failErr
			}
			result.Failed++
			continue
		}
		if len(vectors) != 1 {
			if err := s.finishEmbeddingJobFailedPostgres(ctx, job, embeddingJobErrorInvalidVector, fmt.Sprintf("embedder returned %d vectors for one text", len(vectors)), false, nowUnixMS()); err != nil {
				return result, err
			}
			result.Failed++
			continue
		}
		if err := vectorindex.ValidateVector(vectors[0]); err != nil {
			if failErr := s.finishEmbeddingJobFailedPostgres(ctx, job, embeddingJobErrorInvalidVector, err.Error(), false, nowUnixMS()); failErr != nil {
				return result, failErr
			}
			result.Failed++
			continue
		}
		pointID, err := uuid.Parse(job.PointID)
		if err != nil {
			if failErr := s.finishEmbeddingJobFailedPostgres(ctx, job, embeddingJobErrorInvalidVector, "embedding job point id is invalid", false, nowUnixMS()); failErr != nil {
				return result, failErr
			}
			result.Failed++
			continue
		}
		if err := index.Upsert(ctx, vectorindex.Point{ID: pointID, Vector: vectors[0], Payload: vectorindex.PointPayloadInput{ItemKind: job.ItemKind, ItemID: job.ItemID, ModelID: job.ModelID, ScopeType: payload.ScopeType, CharacterID: payload.CharacterID, ContentHash: job.ContentHash}}); err != nil {
			if failErr := s.finishEmbeddingJobFailedPostgres(ctx, job, embeddingJobErrorEmbedFailed, err.Error(), true, nowUnixMS()); failErr != nil {
				return result, failErr
			}
			result.Failed++
			continue
		}
		if err := s.finishEmbeddingJobSucceededPostgres(ctx, job, nowUnixMS()); err != nil {
			return result, err
		}
		result.Succeeded++
	}
	return result, nil
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
