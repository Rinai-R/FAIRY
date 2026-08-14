package personal

import (
	"context"

	"fairy/runtime/embedding"
)

func (s *Store) SemanticEmbeddingStatus(ctx context.Context) (SemanticEmbeddingReadiness, error) {
	if s.usesSeekDB() {
		return s.semanticEmbeddingStatusSeekDB(ctx)
	}
	if !s.usesPostgres() {
		return SemanticEmbeddingReadiness{}, ErrStoreBackendUnavailable
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	vectorRows, err := countPostgresScalar(queryCtx, s, "SELECT count(*) FROM personal_memories WHERE embedding_v2 IS NOT NULL")
	if err != nil {
		return SemanticEmbeddingReadiness{}, err
	}
	return s.semanticEmbeddingReadiness(vectorRows), nil
}

func (s *Store) semanticEmbeddingStatusSeekDB(ctx context.Context) (SemanticEmbeddingReadiness, error) {
	queryCtx, cancel := s.seekDBQueryContext(ctx)
	defer cancel()
	var vectorRows int64
	if err := s.seekDB.QueryRowContext(queryCtx, "SELECT COUNT(*) FROM personal_memories WHERE embedding IS NOT NULL").Scan(&vectorRows); err != nil {
		return SemanticEmbeddingReadiness{}, err
	}
	return s.semanticEmbeddingReadiness(vectorRows), nil
}

func (s *Store) semanticEmbeddingReadiness(vectorRows int64) SemanticEmbeddingReadiness {
	status := embedding.SemanticStatusUnavailable
	reason := "api_embedder_required"
	if s.embedder.Ready() {
		status = embedding.SemanticStatusReady
		reason = ""
	}
	return SemanticEmbeddingReadiness{
		Dimensions: embedding.Dimensions, DatabaseStatus: SemanticDatabaseStatusReady,
		SemanticStatus: string(status), Reason: reason, VectorRows: vectorRows,
	}
}
