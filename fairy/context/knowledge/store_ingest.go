package knowledge

import (
	"context"
	"errors"
	"fairy/runtime/embedding"
	"fmt"
	"strings"
)

func (s *Store) insertVerifiedKnowledgePostgres(
	ctx context.Context,
	topic, statement, conversationID, turnID string,
	confidenceBasisPoints uint16,
	sources []AssistantSource,
) (Record, error) {
	topic = strings.TrimSpace(topic)
	statement = strings.TrimSpace(statement)
	if topic == "" || statement == "" {
		return Record{}, errors.New("knowledge topic and statement are required")
	}
	if err := ValidateID("conversation_id", conversationID); err != nil {
		return Record{}, err
	}
	if err := ValidateID("turn_id", turnID); err != nil {
		return Record{}, err
	}
	if confidenceBasisPoints == 0 {
		confidenceBasisPoints = 7500
	}
	if confidenceBasisPoints > 10000 {
		return Record{}, errors.New("knowledge confidence is invalid")
	}
	for _, source := range sources {
		if source.Rank < 1 || source.Rank > 5 {
			return Record{}, errors.New("knowledge source rank is invalid")
		}
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	embedder := s.embedder.Snapshot()
	existingID, found, err := FindVerifiedKnowledgeIDByStatement(queryCtx, s.pool.Raw(), statement)
	if err != nil {
		return Record{}, err
	}
	if found {
		existing, err := KnowledgeByID(queryCtx, s.pool.Raw(), existingID)
		if err != nil {
			return Record{}, err
		}
		if embedder == nil {
			return existing, nil
		}
		content := existing.Topic + "\n" + existing.Statement
		modelID, err := embedding.ModelID(embedder)
		if err != nil {
			return Record{}, err
		}
		current, err := knowledgeEmbeddingCurrent(queryCtx, s.pool.Raw(), existingID, modelID, embedding.ContentHash(content))
		if err != nil {
			return Record{}, err
		}
		if current {
			return existing, nil
		}
		value, err := embedding.ForContent(embedder, content)
		if err != nil {
			return Record{}, err
		}
		tx, err := s.pool.Raw().Begin(queryCtx)
		if err != nil {
			return Record{}, fmt.Errorf("beginning knowledge backfill transaction: %w", err)
		}
		defer tx.Rollback(queryCtx)
		changed, err := tx.Exec(queryCtx, `
UPDATE knowledge_entries
SET embedding_model_id_v2 = $2,
    embedding_content_hash_v2 = $3,
    embedding_v2 = $4::public.vector,
    updated_at_ms = $5
WHERE id = $1
  AND status = 'verified'
  AND topic = $6
  AND statement = $7
  AND (
    embedding_model_id_v2 IS DISTINCT FROM $2
    OR embedding_content_hash_v2 IS DISTINCT FROM $3
    OR embedding_v2 IS NULL
		)`, existingID, value.ModelID, value.ContentHash, value.Vector.String(), nowUnixMS(), existing.Topic, existing.Statement)
		if err != nil {
			return Record{}, fmt.Errorf("backfilling knowledge embedding: %w", err)
		}
		if changed.RowsAffected() != 1 {
			current, checkErr := knowledgeEmbeddingCurrent(queryCtx, tx, existingID, value.ModelID, value.ContentHash)
			if checkErr != nil {
				return Record{}, checkErr
			}
			if !current {
				return Record{}, errors.New("knowledge changed during embedding backfill")
			}
		}
		if err := tx.Commit(queryCtx); err != nil {
			return Record{}, fmt.Errorf("committing knowledge embedding backfill: %w", err)
		}
		return KnowledgeByID(ctx, s.pool.Raw(), existingID)
	}
	prepared, err := embedding.ForContent(embedder, topic+"\n"+statement)
	if err != nil {
		return Record{}, err
	}
	tx, err := s.pool.Raw().Begin(queryCtx)
	if err != nil {
		return Record{}, fmt.Errorf("beginning knowledge insert transaction: %w", err)
	}
	defer tx.Rollback(queryCtx)
	existingID, found, err = FindVerifiedKnowledgeIDByStatement(queryCtx, tx, statement)
	if err != nil {
		return Record{}, err
	}
	if found {
		if err := tx.Commit(queryCtx); err != nil {
			return Record{}, fmt.Errorf("committing duplicate knowledge lookup: %w", err)
		}
		return KnowledgeByID(ctx, s.pool.Raw(), existingID)
	}
	now := nowUnixMS()
	id := newID()
	if err := InsertVerifiedKnowledgeEntry(queryCtx, tx, id, topic, statement, conversationID, turnID, confidenceBasisPoints, now, prepared); err != nil {
		return Record{}, err
	}
	if len(sources) > 0 {
		if err := InsertKnowledgeSource(queryCtx, tx, id, newID(), sources[0]); err != nil {
			return Record{}, fmt.Errorf("inserting direct knowledge source: %w", err)
		}
	}
	if err := tx.Commit(queryCtx); err != nil {
		return Record{}, fmt.Errorf("committing knowledge insert: %w", err)
	}
	return KnowledgeByID(ctx, s.pool.Raw(), id)
}

func knowledgeEmbeddingCurrent(ctx context.Context, db DatabaseQuerier, id, modelID, contentHash string) (bool, error) {
	var current bool
	if err := db.QueryRow(ctx, `
SELECT COALESCE(
  embedding_model_id_v2 = $2
  AND embedding_content_hash_v2 = $3
  AND embedding_v2 IS NOT NULL,
  false
)
FROM knowledge_entries
WHERE id = $1
`, id, modelID, contentHash).Scan(&current); err != nil {
		return false, fmt.Errorf("checking knowledge embedding: %w", err)
	}
	return current, nil
}
