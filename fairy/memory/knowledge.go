package memory

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

func ListKnowledge(ctx context.Context, db Querier, status string) ([]KnowledgeRecord, error) {
	rows, err := db.Query(ctx, "SELECT id, topic, statement, status, verification_basis, confidence_basis_points, source_conversation_id, source_turn_id, supersedes_id, created_at_ms, updated_at_ms FROM knowledge_entries WHERE status = $1 ORDER BY updated_at_ms DESC, id ASC LIMIT 20", status)
	if err != nil {
		return nil, fmt.Errorf("querying knowledge catalog: %w", err)
	}
	defer rows.Close()
	records := make([]KnowledgeRecord, 0)
	for rows.Next() {
		record, err := ScanKnowledge(ctx, db, rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating knowledge catalog: %w", err)
	}
	return records, nil
}

func KnowledgeByID(ctx context.Context, db ConversationDB, id string) (KnowledgeRecord, error) {
	row := db.QueryRow(ctx, "SELECT id, topic, statement, status, verification_basis, confidence_basis_points, source_conversation_id, source_turn_id, supersedes_id, created_at_ms, updated_at_ms FROM knowledge_entries WHERE id = $1", id)
	return ScanKnowledge(ctx, db, row)
}

func ScanKnowledge(ctx context.Context, db Querier, row scanner) (KnowledgeRecord, error) {
	var record KnowledgeRecord
	var confidence int
	var supersedes pgtype.Text
	if err := row.Scan(&record.ID, &record.Topic, &record.Statement, &record.Status, &record.VerificationBasis, &confidence, &record.SourceConversationID, &record.SourceTurnID, &supersedes, &record.CreatedAtUnixMS, &record.UpdatedAtUnixMS); err != nil {
		return KnowledgeRecord{}, fmt.Errorf("scanning knowledge: %w", err)
	}
	if confidence < 0 || confidence > 10000 {
		return KnowledgeRecord{}, errors.New("knowledge confidence is invalid")
	}
	record.ConfidenceBasisPoints = uint16(confidence)
	if supersedes.Valid {
		record.SupersedesID = &supersedes.String
	}
	sources, err := KnowledgeSources(ctx, db, record.ID)
	if err != nil {
		return KnowledgeRecord{}, err
	}
	record.Sources = sources
	return record, nil
}

func KnowledgeSources(ctx context.Context, db Querier, id string) ([]AssistantSource, error) {
	rows, err := db.Query(ctx, "SELECT title, url, snippet, rank, fetched_at_ms FROM knowledge_sources WHERE knowledge_id = $1 ORDER BY rank ASC", id)
	if err != nil {
		return nil, fmt.Errorf("querying knowledge sources: %w", err)
	}
	defer rows.Close()
	sources := make([]AssistantSource, 0)
	for rows.Next() {
		var source AssistantSource
		var rank int
		if err := rows.Scan(&source.Title, &source.URL, &source.Snippet, &rank, &source.FetchedAtUnixMS); err != nil {
			return nil, fmt.Errorf("scanning knowledge source: %w", err)
		}
		if rank < 1 || rank > 5 {
			return nil, errors.New("knowledge source rank is invalid")
		}
		source.Rank = uint8(rank)
		sources = append(sources, source)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating knowledge sources: %w", err)
	}
	return sources, nil
}

func ConfirmKnowledgeCandidate(ctx context.Context, tx pgx.Tx, id string, now int64, embedding EmbeddingValue) (topic, statement string, err error) {
	if err := embedding.Validate(); err != nil {
		return "", "", err
	}
	var modelID, contentHash, vector any
	if embedding.Enabled() {
		modelID = embedding.ModelID
		contentHash = embedding.ContentHash
		vector = embedding.Vector.String()
	}
	changed, err := tx.Exec(ctx, `
UPDATE knowledge_entries
SET status = 'verified',
    verification_basis = 'user_confirmed',
    embedding_model_id = $3,
    embedding_content_hash = $4,
    embedding = $5::public.vector,
    updated_at_ms = $2
WHERE id = $1
  AND status = 'candidate'
  AND verification_basis = 'unverified'
  AND NOT EXISTS (
    SELECT 1 FROM knowledge_sources s WHERE s.knowledge_id = knowledge_entries.id
  )`, id, now, modelID, contentHash, vector)
	if err != nil {
		return "", "", fmt.Errorf("confirming knowledge candidate: %w", err)
	}
	if changed.RowsAffected() != 1 {
		return "", "", errors.New("knowledge entry is not a confirmable candidate")
	}
	if err := tx.QueryRow(ctx, "SELECT topic, statement FROM knowledge_entries WHERE id = $1", id).Scan(&topic, &statement); err != nil {
		return "", "", fmt.Errorf("reading confirmed knowledge content: %w", err)
	}
	return topic, statement, nil
}

func TombstoneKnowledge(ctx context.Context, exec interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}, id string, now int64) error {
	changed, err := exec.Exec(ctx, "UPDATE knowledge_entries SET status = 'tombstone', updated_at_ms = $2 WHERE id = $1 AND status IN ('candidate', 'verified')", id, now)
	if err != nil {
		return fmt.Errorf("tombstoning knowledge: %w", err)
	}
	if changed.RowsAffected() != 1 {
		return errors.New("knowledge entry is not tombstoneable")
	}
	return nil
}

func FindVerifiedKnowledgeIDByStatement(ctx context.Context, db ConversationDB, statement string) (string, bool, error) {
	var existingID string
	err := db.QueryRow(ctx, "SELECT id FROM knowledge_entries WHERE status = 'verified' AND statement = $1 ORDER BY updated_at_ms DESC, id ASC LIMIT 1", statement).Scan(&existingID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("checking duplicate knowledge: %w", err)
	}
	return existingID, true, nil
}

func InsertVerifiedKnowledgeEntry(
	ctx context.Context,
	tx pgx.Tx,
	id, topic, statement, conversationID, turnID string,
	confidenceBasisPoints uint16,
	now int64,
	embedding EmbeddingValue,
) error {
	if err := embedding.Validate(); err != nil {
		return err
	}
	var modelID, contentHash, vector any
	if embedding.Enabled() {
		modelID = embedding.ModelID
		contentHash = embedding.ContentHash
		vector = embedding.Vector.String()
	}
	_, err := tx.Exec(ctx, `
INSERT INTO knowledge_entries(
  id, topic, statement, status, verification_basis, confidence_basis_points,
  source_conversation_id, source_turn_id,
  embedding_model_id, embedding_content_hash, embedding,
  created_at_ms, updated_at_ms
) VALUES (
  $1, $2, $3, 'verified', 'retrieval_ingest', $4, $5, $6,
  $7, $8, $9::public.vector,
  $10, $10
)`, id, topic, statement, confidenceBasisPoints, conversationID, turnID, modelID, contentHash, vector, now)
	if err != nil {
		return fmt.Errorf("inserting verified knowledge: %w", err)
	}
	return nil
}

func InsertKnowledgeSource(ctx context.Context, tx pgx.Tx, knowledgeID, sourceID string, source AssistantSource) error {
	_, err := tx.Exec(ctx, "INSERT INTO knowledge_sources(knowledge_id, source_id, title, url, snippet, rank, fetched_at_ms) VALUES ($1, $2, $3, $4, $5, $6, $7)", knowledgeID, sourceID, source.Title, source.URL, source.Snippet, source.Rank, source.FetchedAtUnixMS)
	if err != nil {
		return fmt.Errorf("inserting knowledge source: %w", err)
	}
	return nil
}
