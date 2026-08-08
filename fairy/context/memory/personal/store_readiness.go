package personal

import (
	"context"

	"fairy/runtime/embedding"
)

func (s *Store) SemanticEmbeddingStatus(ctx context.Context) (SemanticEmbeddingReadiness, error) {
	if s == nil || s.pool == nil {
		return SemanticEmbeddingReadiness{}, ErrDatabasePoolEmpty
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	vectorRows, err := countPostgresScalar(queryCtx, s, "SELECT count(*) FROM personal_memories WHERE embedding_v2 IS NOT NULL")
	if err != nil {
		return SemanticEmbeddingReadiness{}, err
	}
	status := embedding.SemanticStatusUnavailable
	reason := "api_embedder_required"
	if s.embedder.Ready() {
		status = embedding.SemanticStatusReady
		reason = ""
	}
	return SemanticEmbeddingReadiness{
		Dimensions: embedding.Dimensions, DatabaseStatus: SemanticDatabaseStatusReady,
		SemanticStatus: string(status), Reason: reason, VectorRows: vectorRows,
	}, nil
}
