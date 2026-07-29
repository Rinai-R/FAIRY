package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
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
		current, err := knowledgeEmbeddingCurrent(queryCtx, s.pool.Raw(), existingID, semanticContentHash(content))
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
SET embedding_model_id = $2,
    embedding_content_hash = $3,
    embedding = $4::public.vector,
    updated_at_ms = $5
WHERE id = $1
  AND status = 'verified'
  AND topic = $6
  AND statement = $7
  AND (
    embedding_model_id IS DISTINCT FROM $2
    OR embedding_content_hash IS DISTINCT FROM $3
    OR embedding IS NULL
  )`, existingID, embedding.ModelID, embedding.ContentHash, embedding.Vector.String(), nowUnixMS(), existing.Topic, existing.Statement)
		if err != nil {
			return KnowledgeRecord{}, fmt.Errorf("backfilling knowledge embedding: %w", err)
		}
		if changed.RowsAffected() != 1 {
			current, checkErr := knowledgeEmbeddingCurrent(queryCtx, tx, existingID, embedding.ContentHash)
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
	for index, source := range sources {
		if err := InsertKnowledgeSource(queryCtx, tx, id, newID(), source); err != nil {
			return KnowledgeRecord{}, fmt.Errorf("inserting knowledge source[%d]: %w", index, err)
		}
	}
	if err := tx.Commit(queryCtx); err != nil {
		return KnowledgeRecord{}, fmt.Errorf("committing knowledge insert: %w", err)
	}
	return knowledgeByIDPostgres(ctx, s.pool.Raw(), id)
}

func knowledgeEmbeddingCurrent(ctx context.Context, db ConversationDB, id, contentHash string) (bool, error) {
	var current bool
	if err := db.QueryRow(ctx, `
SELECT COALESCE(
  embedding_model_id = $2
  AND embedding_content_hash = $3
  AND embedding IS NOT NULL,
  false
)
FROM knowledge_entries
WHERE id = $1
`, id, SemanticEmbeddingModelID, contentHash).Scan(&current); err != nil {
		return false, fmt.Errorf("checking knowledge embedding: %w", err)
	}
	return current, nil
}

func (s *Store) enqueueKnowledgeIngestTasksPostgres(ctx context.Context, tasks []KnowledgeIngestTask) error {
	if len(tasks) == 0 {
		return nil
	}
	encoded := make([][]byte, len(tasks))
	for index, task := range tasks {
		var err error
		encoded[index], err = validateKnowledgeIngestTask(task)
		if err != nil {
			return fmt.Errorf("validating knowledge ingest task[%d]: %w", index, err)
		}
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	tx, err := s.pool.Raw().Begin(queryCtx)
	if err != nil {
		return fmt.Errorf("beginning knowledge ingest task enqueue transaction: %w", err)
	}
	defer tx.Rollback(queryCtx)
	now := nowUnixMS()
	for index, task := range tasks {
		if err := EnqueueKnowledgeIngestTask(
			queryCtx, tx, newID(), task.ConversationID, task.TurnID,
			task.ID, encoded[index], now,
		); err != nil {
			return err
		}
	}
	if err := tx.Commit(queryCtx); err != nil {
		return fmt.Errorf("committing knowledge ingest task enqueue: %w", err)
	}
	return nil
}

func (s *Store) claimKnowledgeIngestTasksPostgres(ctx context.Context, limit int) ([]KnowledgeIngestClaim, error) {
	if limit < 1 || limit > MaxKnowledgeIngestJobsPerPass {
		return nil, fmt.Errorf("knowledge ingest job limit must be between 1 and %d", MaxKnowledgeIngestJobsPerPass)
	}
	jobs, err := s.claimKnowledgeIngestJobsPostgres(ctx, limit, nowUnixMS())
	if err != nil {
		return nil, err
	}
	claims := make([]KnowledgeIngestClaim, 0, len(jobs))
	for _, job := range jobs {
		task, err := knowledgeIngestTaskFromJob(job)
		if err != nil {
			if finishErr := s.finishKnowledgeIngestJobPostgres(ctx, job.ID, "failed", CleanEmbeddingErrorMessage(err.Error())); finishErr != nil {
				return nil, errors.Join(err, finishErr)
			}
			continue
		}
		claims = append(claims, KnowledgeIngestClaim{JobID: job.ID, Task: task})
	}
	return claims, nil
}

func loadOwnedKnowledgeIngestJob(ctx context.Context, db ConversationDB, jobID, workerID string, forUpdate bool) (KnowledgeIngestJob, error) {
	query := `
SELECT id, conversation_id, turn_id, task_id, source_json
FROM knowledge_ingest_jobs
WHERE id = $1 AND status = 'running' AND lease_owner = $2`
	if forUpdate {
		query += " FOR UPDATE"
	}
	var job KnowledgeIngestJob
	if err := db.QueryRow(ctx, query, jobID, workerID).Scan(
		&job.ID, &job.ConversationID, &job.TurnID,
		&job.TaskID, &job.SourceJSON,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return KnowledgeIngestJob{}, errors.New("knowledge ingest job is not owned by this worker")
		}
		return KnowledgeIngestJob{}, fmt.Errorf("loading claimed knowledge ingest job: %w", err)
	}
	return job, nil
}

func knowledgeIngestTaskFromJob(job KnowledgeIngestJob) (KnowledgeIngestTask, error) {
	var source KnowledgeIngestSource
	if job.TaskID == "" {
		return KnowledgeIngestTask{}, errors.New("knowledge ingest task ID is required")
	}
	decoder := json.NewDecoder(strings.NewReader(strings.TrimSpace(string(job.SourceJSON))))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&source); err != nil {
		return KnowledgeIngestTask{}, errors.New("knowledge ingest source JSON is invalid")
	}
	task := KnowledgeIngestTask{
		ID: job.TaskID, ConversationID: job.ConversationID, TurnID: job.TurnID,
		Source: source,
	}
	if _, err := validateKnowledgeIngestTask(task); err != nil {
		return KnowledgeIngestTask{}, err
	}
	return task, nil
}

func (s *Store) claimKnowledgeIngestJobsPostgres(ctx context.Context, limit int, now int64) ([]KnowledgeIngestJob, error) {
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	leaseExpires := now + s.jobLeaseDuration.Milliseconds()
	return ClaimKnowledgeIngestJobs(queryCtx, s.pool.Raw(), limit, now, s.workerID, leaseExpires)
}

func (s *Store) finishKnowledgeIngestJobPostgres(ctx context.Context, id, status, message string) error {
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	return FinishKnowledgeIngestJob(queryCtx, s.pool.Raw(), id, s.workerID, status, message, nowUnixMS())
}
