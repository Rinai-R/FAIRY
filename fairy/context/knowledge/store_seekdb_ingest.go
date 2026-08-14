package knowledge

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"fairy/runtime/embedding"
)

func (s *Store) insertVerifiedKnowledgeSeekDB(
	ctx context.Context,
	topic, statement, conversationID, turnID string,
	confidenceBasisPoints uint16,
	sources []AssistantSource,
	requireContext bool,
) (Record, error) {
	topic = strings.TrimSpace(topic)
	statement = strings.TrimSpace(statement)
	if err := validateSeekDBDirectKnowledgeContent(topic, statement); err != nil {
		return Record{}, err
	}
	if err := validateSeekDBKnowledgeID("conversation_id", conversationID); err != nil {
		return Record{}, err
	}
	if err := validateSeekDBKnowledgeID("turn_id", turnID); err != nil {
		return Record{}, err
	}
	if confidenceBasisPoints == 0 {
		confidenceBasisPoints = 7500
	}
	if confidenceBasisPoints > 10000 {
		return Record{}, errors.New("knowledge confidence is invalid")
	}
	if err := validateDirectKnowledgeSources(sources); err != nil {
		return Record{}, err
	}
	snapshotCtx, snapshotCancel := s.seekDBQueryContext(ctx)
	existing, found, err := findVerifiedSeekDBKnowledgeByStatement(snapshotCtx, s.seekDB, statement)
	snapshotCancel()
	if err != nil {
		return Record{}, err
	}
	if found {
		return s.refreshVerifiedSeekDBEmbedding(ctx, existing, requireContext)
	}
	content := topic + "\n" + statement
	values, err := prepareKnowledgeEmbeddings(
		ctx, s.semanticEmbedderSnapshot(), []string{content}, requireContext,
	)
	if err != nil {
		return Record{}, err
	}
	spaceID, contentHash, vector, err := seekDBKnowledgeEmbeddingTuple(content, values[0])
	if err != nil {
		return Record{}, err
	}
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	queryCtx, cancel := s.seekDBQueryContext(context.WithoutCancel(ctx))
	defer cancel()
	tx, err := s.seekDB.BeginTx(queryCtx, nil)
	if err != nil {
		return Record{}, fmt.Errorf("beginning SeekDB knowledge insert transaction: %w", err)
	}
	defer tx.Rollback()
	if err := lockSeekDBKnowledgeSource(queryCtx, tx, conversationID, turnID); err != nil {
		return Record{}, err
	}
	existing, found, err = findVerifiedSeekDBKnowledgeByStatement(queryCtx, tx, statement)
	if err != nil {
		return Record{}, err
	}
	if found {
		locked, err := seekDBKnowledgeByID(queryCtx, tx, existing.ID, true)
		if err != nil {
			return Record{}, fmt.Errorf("locking duplicate SeekDB knowledge: %w", err)
		}
		if locked.Status != "verified" || locked.Statement != statement {
			return Record{}, errors.New("duplicate knowledge changed during insert")
		}
		if err := tx.Commit(); err != nil {
			return Record{}, fmt.Errorf("committing SeekDB duplicate knowledge lookup: %w", err)
		}
		return locked, nil
	}
	now := s.currentUnixMS()
	id := newID()
	var (
		sourceURL         any
		sourceTitle       any
		sourceContentHash any
		sourceContentType any
		sourceFetchedAt   any
		evidenceText      any
	)
	if len(sources) == 1 {
		source := sources[0]
		sourceURL = source.URL
		sourceTitle = nullIfEmpty(source.Title)
		sourceContentHash, err = decodeKnowledgeHash(
			"knowledge source content hash", embedding.ContentHash(source.Snippet),
		)
		if err != nil {
			return Record{}, err
		}
		sourceContentType = "text/plain"
		sourceFetchedAt = source.FetchedAtUnixMS
		evidenceText = source.Snippet
	}
	_, err = tx.ExecContext(queryCtx, `
INSERT INTO knowledge_entries(
  id, topic, statement, status, verification_basis, confidence_basis_points,
  source_conversation_id, source_turn_id,
  source_url, source_title, source_content_hash, source_content_type,
  source_fetched_at_ms, evidence_text,
  embedding_space_id, embedding_content_hash, embedding,
  created_at_ms, updated_at_ms
) VALUES (
  ?, ?, ?, 'verified', 'retrieval_ingest', ?, ?, ?,
  ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
)`,
		id, topic, statement, int(confidenceBasisPoints), conversationID, turnID,
		sourceURL, sourceTitle, sourceContentHash, sourceContentType, sourceFetchedAt,
		evidenceText, spaceID, contentHash, vector, now, now,
	)
	if err != nil {
		return Record{}, fmt.Errorf("inserting verified SeekDB knowledge: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Record{}, fmt.Errorf("committing SeekDB knowledge insert transaction: %w", err)
	}
	return seekDBKnowledgeByID(queryCtx, s.seekDB, id, false)
}

func nullIfEmpty(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func findVerifiedSeekDBKnowledgeByStatement(
	ctx context.Context,
	database seekDBKnowledgeRowQuerier,
	statement string,
) (Record, bool, error) {
	row := database.QueryRowContext(ctx, "SELECT "+seekDBKnowledgeRecordColumns+`
FROM knowledge_entries
WHERE status = 'verified' AND statement = ?
ORDER BY updated_at_ms DESC, id ASC
LIMIT 1`, statement)
	record, err := scanSeekDBKnowledge(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, fmt.Errorf("checking duplicate SeekDB knowledge: %w", err)
	}
	return record, true, nil
}

func (s *Store) refreshVerifiedSeekDBEmbedding(
	ctx context.Context,
	snapshot Record,
	requireContext bool,
) (Record, error) {
	embedder := s.semanticEmbedderSnapshot()
	content := snapshot.Topic + "\n" + snapshot.Statement
	var prepared embedding.EmbeddingValue
	if embedder != nil {
		modelID, err := embedding.ModelID(embedder)
		if err != nil {
			return Record{}, err
		}
		contentHash := embedding.ContentHash(content)
		currentCtx, currentCancel := s.seekDBQueryContext(ctx)
		current, err := knowledgeEmbeddingCurrentSeekDB(
			currentCtx, s.seekDB, snapshot.ID, modelID, contentHash,
		)
		currentCancel()
		if err != nil {
			return Record{}, err
		}
		if !current {
			values, err := prepareKnowledgeEmbeddings(ctx, embedder, []string{content}, requireContext)
			if err != nil {
				return Record{}, err
			}
			prepared = values[0]
		}
	}
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	queryCtx, cancel := s.seekDBQueryContext(context.WithoutCancel(ctx))
	defer cancel()
	tx, err := s.seekDB.BeginTx(queryCtx, nil)
	if err != nil {
		return Record{}, fmt.Errorf("beginning SeekDB knowledge backfill transaction: %w", err)
	}
	defer tx.Rollback()
	if err := lockSeekDBKnowledgeSource(
		queryCtx, tx, snapshot.SourceConversationID, snapshot.SourceTurnID,
	); err != nil {
		return Record{}, err
	}
	currentRecord, err := seekDBKnowledgeByID(queryCtx, tx, snapshot.ID, true)
	if err != nil {
		return Record{}, err
	}
	if currentRecord.Status != "verified" || currentRecord.Topic != snapshot.Topic ||
		currentRecord.Statement != snapshot.Statement ||
		currentRecord.SourceConversationID != snapshot.SourceConversationID ||
		currentRecord.SourceTurnID != snapshot.SourceTurnID {
		return Record{}, errors.New("knowledge changed during embedding backfill")
	}
	if !prepared.Enabled() {
		if err := tx.Commit(); err != nil {
			return Record{}, fmt.Errorf("committing SeekDB knowledge duplicate transaction: %w", err)
		}
		return seekDBKnowledgeByID(queryCtx, s.seekDB, snapshot.ID, false)
	}
	spaceID, hash, vector, err := seekDBKnowledgeEmbeddingTuple(content, prepared)
	if err != nil {
		return Record{}, err
	}
	current, err := knowledgeEmbeddingCurrentSeekDB(
		queryCtx, tx, snapshot.ID, prepared.ModelID, prepared.ContentHash,
	)
	if err != nil {
		return Record{}, err
	}
	if current {
		if err := tx.Commit(); err != nil {
			return Record{}, fmt.Errorf("committing concurrent SeekDB knowledge backfill: %w", err)
		}
		return seekDBKnowledgeByID(queryCtx, s.seekDB, snapshot.ID, false)
	}
	result, err := tx.ExecContext(queryCtx, `
UPDATE knowledge_entries
SET embedding_space_id = ?, embedding_content_hash = ?, embedding = ?,
    updated_at_ms = GREATEST(updated_at_ms, ?)
WHERE id = ? AND status = 'verified' AND topic = ? AND statement = ?`,
		spaceID, hash, vector, s.currentUnixMS(), snapshot.ID, snapshot.Topic, snapshot.Statement,
	)
	if err != nil {
		return Record{}, fmt.Errorf("backfilling SeekDB knowledge embedding: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return Record{}, fmt.Errorf("reading SeekDB knowledge backfill count: %w", err)
	}
	if rows != 1 {
		return Record{}, errors.New("knowledge changed during embedding backfill")
	}
	if err := tx.Commit(); err != nil {
		return Record{}, fmt.Errorf("committing SeekDB knowledge backfill transaction: %w", err)
	}
	return seekDBKnowledgeByID(queryCtx, s.seekDB, snapshot.ID, false)
}

func knowledgeEmbeddingCurrentSeekDB(
	ctx context.Context,
	database seekDBKnowledgeRowQuerier,
	id, modelID, contentHash string,
) (bool, error) {
	hash, err := decodeKnowledgeHash("embedding content hash", contentHash)
	if err != nil {
		return false, err
	}
	var current bool
	if err := database.QueryRowContext(ctx, `
SELECT COALESCE(
  embedding_space_id = ? AND embedding_content_hash = ? AND embedding IS NOT NULL,
  false
)
FROM knowledge_entries
WHERE id = ?`, modelID, hash, id).Scan(&current); err != nil {
		return false, fmt.Errorf("checking SeekDB knowledge embedding: %w", err)
	}
	return current, nil
}
