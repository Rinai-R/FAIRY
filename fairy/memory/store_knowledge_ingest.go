package memory

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

func (s *Store) insertVerifiedKnowledgePostgres(ctx context.Context, topic, statement, conversationID, turnID string, confidenceBasisPoints uint16, sources []AssistantSource) (KnowledgeRecord, error) {
	topic = strings.TrimSpace(topic)
	statement = strings.TrimSpace(statement)
	if topic == "" || statement == "" {
		return KnowledgeRecord{}, errors.New("knowledge topic and statement are required")
	}
	if err := ValidateID("conversation_id", conversationID); err != nil {
		return KnowledgeRecord{}, err
	}
	if err := ValidateID("turn_id", turnID); err != nil {
		return KnowledgeRecord{}, err
	}
	if confidenceBasisPoints == 0 {
		confidenceBasisPoints = 7500
	}
	if confidenceBasisPoints > 10000 {
		return KnowledgeRecord{}, errors.New("knowledge confidence is invalid")
	}
	for _, source := range sources {
		if source.Rank < 1 || source.Rank > 5 {
			return KnowledgeRecord{}, errors.New("knowledge source rank is invalid")
		}
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	existingID, found, err := FindVerifiedKnowledgeIDByStatement(queryCtx, s.pool.Raw(), statement)
	if err != nil {
		return KnowledgeRecord{}, err
	}
	if found {
		existing, err := knowledgeByIDPostgres(queryCtx, s.pool.Raw(), existingID)
		if err != nil {
			return KnowledgeRecord{}, err
		}
		if s.semanticEmbedder == nil {
			return existing, nil
		}
		content := existing.Topic + "\n" + existing.Statement
		modelID := s.semanticEmbedder.ModelID()
		if modelID == "" || strings.TrimSpace(modelID) != modelID || ContainsDisallowedControl(modelID) {
			return KnowledgeRecord{}, errors.New("embedding model id is invalid")
		}
		current, err := knowledgeEmbeddingCurrent(queryCtx, s.pool.Raw(), existingID, modelID, semanticContentHash(content))
		if err != nil {
			return KnowledgeRecord{}, err
		}
		if current {
			return existing, nil
		}
		embedding, err := s.embeddingForContent(content)
		if err != nil {
			return KnowledgeRecord{}, err
		}
		tx, err := s.pool.Raw().Begin(queryCtx)
		if err != nil {
			return KnowledgeRecord{}, fmt.Errorf("beginning knowledge backfill transaction: %w", err)
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
  )`, existingID, embedding.ModelID, embedding.ContentHash, embedding.Vector.String(), nowUnixMS(), existing.Topic, existing.Statement)
		if err != nil {
			return KnowledgeRecord{}, fmt.Errorf("backfilling knowledge embedding: %w", err)
		}
		if changed.RowsAffected() != 1 {
			current, checkErr := knowledgeEmbeddingCurrent(queryCtx, tx, existingID, embedding.ModelID, embedding.ContentHash)
			if checkErr != nil {
				return KnowledgeRecord{}, checkErr
			}
			if !current {
				return KnowledgeRecord{}, errors.New("knowledge changed during embedding backfill")
			}
		}
		if err := tx.Commit(queryCtx); err != nil {
			return KnowledgeRecord{}, fmt.Errorf("committing knowledge embedding backfill: %w", err)
		}
		return knowledgeByIDPostgres(ctx, s.pool.Raw(), existingID)
	}
	embedding, err := s.embeddingForContent(topic + "\n" + statement)
	if err != nil {
		return KnowledgeRecord{}, err
	}
	tx, err := s.pool.Raw().Begin(queryCtx)
	if err != nil {
		return KnowledgeRecord{}, fmt.Errorf("beginning knowledge insert transaction: %w", err)
	}
	defer tx.Rollback(queryCtx)
	existingID, found, err = FindVerifiedKnowledgeIDByStatement(queryCtx, tx, statement)
	if err != nil {
		return KnowledgeRecord{}, err
	}
	if found {
		if err := tx.Commit(queryCtx); err != nil {
			return KnowledgeRecord{}, fmt.Errorf("committing duplicate knowledge lookup: %w", err)
		}
		return knowledgeByIDPostgres(ctx, s.pool.Raw(), existingID)
	}
	now := nowUnixMS()
	id := newID()
	if err := InsertVerifiedKnowledgeEntry(queryCtx, tx, id, topic, statement, conversationID, turnID, confidenceBasisPoints, now, embedding); err != nil {
		return KnowledgeRecord{}, err
	}
	if len(sources) > 0 {
		if err := InsertKnowledgeSource(queryCtx, tx, id, newID(), sources[0]); err != nil {
			return KnowledgeRecord{}, fmt.Errorf("inserting direct knowledge source: %w", err)
		}
	}
	if err := tx.Commit(queryCtx); err != nil {
		return KnowledgeRecord{}, fmt.Errorf("committing knowledge insert: %w", err)
	}
	return knowledgeByIDPostgres(ctx, s.pool.Raw(), id)
}

func knowledgeEmbeddingCurrent(ctx context.Context, db ConversationDB, id, modelID, contentHash string) (bool, error) {
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
