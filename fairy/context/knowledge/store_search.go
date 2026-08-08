package knowledge

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"fairy/runtime/embedding"

	"github.com/pgvector/pgvector-go"
)

func (s *Store) searchForIngestPostgres(ctx context.Context, query string, limit int) ([]Retrieved, error) {
	retrieval, err := s.searchPostgres(ctx, query, limit, true)
	return retrieval.Entries, err
}

func (s *Store) RetrieveContext(ctx context.Context, query string) (Retrieval, error) {
	return s.searchPostgres(ctx, query, MaxSearchCandidates, false)
}

func (s *Store) searchPostgres(ctx context.Context, query string, limit int, failOnEmbeddingError bool) (Retrieval, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return Retrieval{}, errors.New("knowledge search query is required")
	}
	if limit <= 0 || limit > MaxSearchCandidates {
		limit = MaxSearchCandidates
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()

	var vector *pgvector.Vector
	modelID := ""
	semanticStatus := string(embedding.SemanticStatusUnavailable)
	if embedder := s.embedder.Snapshot(); embedder != nil && embedder.Ready() {
		if embedder.Dims() != embedding.Dimensions {
			return Retrieval{}, fmt.Errorf("embedding dimensions = %d, want %d", embedder.Dims(), embedding.Dimensions)
		}
		var err error
		modelID, err = embedding.ModelID(embedder)
		if err != nil {
			return Retrieval{}, err
		}
		vectors, err := embedder.Embed([]string{query})
		if err != nil {
			if failOnEmbeddingError {
				return Retrieval{}, fmt.Errorf("embedding knowledge ingest recall: %w", err)
			}
			vectors = nil
		}
		if vectors != nil && len(vectors) != 1 {
			return Retrieval{}, fmt.Errorf("embedding result count = %d, want 1", len(vectors))
		}
		if len(vectors) == 1 {
			if err := embedding.ValidateVector(vectors[0]); err != nil {
				return Retrieval{}, err
			}
			value := pgvector.NewVector(vectors[0])
			vector = &value
			semanticStatus = string(embedding.SemanticStatusUsed)
		}
	}

	var rows QuerierRows
	var err error
	if vector == nil {
		rows, err = s.pool.Raw().Query(queryCtx, knowledgeTextSearchSQL, query, limit)
	} else {
		rows, err = s.pool.Raw().Query(queryCtx, knowledgeHybridSearchSQL, query, vector.String(), modelID, limit)
	}
	if err != nil {
		return Retrieval{}, fmt.Errorf("searching knowledge: %w", err)
	}
	defer rows.Close()
	results := make([]Retrieved, 0, limit)
	for rows.Next() {
		var record Retrieved
		var confidence int
		if err := rows.Scan(&record.ID, &record.Topic, &record.Statement, &record.VerificationBasis, &confidence, &record.UpdatedAtUnixMS, &record.TextScore); err != nil {
			return Retrieval{}, fmt.Errorf("scanning knowledge search result: %w", err)
		}
		if confidence < 0 || confidence > 10000 {
			return Retrieval{}, errors.New("knowledge search confidence is invalid")
		}
		record.Layer = "knowledge"
		record.ConfidenceBasisPoints = uint16(confidence)
		record.Sources, err = KnowledgeSources(queryCtx, s.pool.Raw(), record.ID)
		if err != nil {
			return Retrieval{}, err
		}
		results = append(results, record)
	}
	if err := rows.Err(); err != nil {
		return Retrieval{}, fmt.Errorf("iterating knowledge search results: %w", err)
	}
	return Retrieval{Entries: results, SemanticStatus: semanticStatus}, nil
}

type QuerierRows interface {
	Next() bool
	Scan(...any) error
	Err() error
	Close()
}

const knowledgeTextSearchSQL = `
SELECT id, topic, statement, verification_basis, confidence_basis_points, updated_at_ms,
       GREATEST(public.similarity(topic, $1), public.similarity(statement, $1),
                public.word_similarity($1, topic), public.word_similarity($1, statement)) AS score
FROM knowledge_entries
WHERE status = 'verified' AND (
  topic ILIKE '%' || $1 || '%' OR statement ILIKE '%' || $1 || '%'
  OR topic OPERATOR(public.%) $1 OR statement OPERATOR(public.%) $1
  OR $1 OPERATOR(public.<%) topic OR $1 OPERATOR(public.<%) statement)
ORDER BY score DESC, confidence_basis_points DESC, updated_at_ms DESC, id ASC
LIMIT $2`

const knowledgeHybridSearchSQL = `
SELECT id, topic, statement, verification_basis, confidence_basis_points, updated_at_ms,
       0.55 * GREATEST(public.similarity(topic, $1), public.similarity(statement, $1),
                       public.word_similarity($1, topic), public.word_similarity($1, statement))
       + 0.35 * CASE
           WHEN embedding_model_id_v2 = $3
            AND embedding_content_hash_v2 = encode(sha256(convert_to(topic || chr(10) || statement, 'UTF8')), 'hex')
            AND embedding_v2 IS NOT NULL
           THEN GREATEST(0.0, LEAST(1.0, 1.0 - (embedding_v2 OPERATOR(public.<=>) $2::public.vector)))
           ELSE 0.0
         END
       + 0.10 * (confidence_basis_points::float8 / 10000.0) AS score
FROM knowledge_entries
WHERE status = 'verified'
  AND (
    topic ILIKE '%' || $1 || '%' OR statement ILIKE '%' || $1 || '%'
    OR topic OPERATOR(public.%) $1 OR statement OPERATOR(public.%) $1
    OR $1 OPERATOR(public.<%) topic OR $1 OPERATOR(public.<%) statement
    OR (embedding_model_id_v2 = $3
        AND embedding_content_hash_v2 = encode(sha256(convert_to(topic || chr(10) || statement, 'UTF8')), 'hex')
        AND embedding_v2 IS NOT NULL)
  )
ORDER BY score DESC, updated_at_ms DESC, id ASC
LIMIT $4`
