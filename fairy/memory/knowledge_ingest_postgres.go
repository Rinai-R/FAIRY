package memory

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

const maxKnowledgeIngestJobsPerPass = 100

type knowledgeIngestJob struct {
	id             string
	conversationID string
	turnID         string
	query          string
	title          string
	url            string
	snippet        string
	rank           uint8
	fetchedAt      int64
}

func (s *Store) insertVerifiedKnowledgePostgres(ctx context.Context, topic, statement, conversationID, turnID string, confidenceBasisPoints uint16, sources []AssistantSource) (KnowledgeRecord, error) {
	topic = strings.TrimSpace(topic)
	statement = strings.TrimSpace(statement)
	if topic == "" || statement == "" {
		return KnowledgeRecord{}, errors.New("knowledge topic and statement are required")
	}
	if err := validateID("conversation_id", conversationID); err != nil {
		return KnowledgeRecord{}, err
	}
	if err := validateID("turn_id", turnID); err != nil {
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
	tx, err := s.pool.Raw().Begin(queryCtx)
	if err != nil {
		return KnowledgeRecord{}, fmt.Errorf("beginning knowledge insert transaction: %w", err)
	}
	defer tx.Rollback(queryCtx)
	var existingID string
	err = tx.QueryRow(queryCtx, "SELECT id FROM knowledge_entries WHERE status = 'verified' AND statement = $1 ORDER BY updated_at_ms DESC, id ASC LIMIT 1", statement).Scan(&existingID)
	if err == nil {
		if err := tx.Commit(queryCtx); err != nil {
			return KnowledgeRecord{}, fmt.Errorf("committing duplicate knowledge lookup: %w", err)
		}
		return knowledgeByIDPostgres(ctx, s.pool.Raw(), existingID)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return KnowledgeRecord{}, fmt.Errorf("checking duplicate knowledge: %w", err)
	}
	now := nowUnixMS()
	id := newID()
	_, err = tx.Exec(queryCtx, `
INSERT INTO knowledge_entries(
  id, topic, statement, status, verification_basis, confidence_basis_points,
  source_conversation_id, source_turn_id, created_at_ms, updated_at_ms
) VALUES ($1, $2, $3, 'verified', 'retrieval_ingest', $4, $5, $6, $7, $7)`, id, topic, statement, confidenceBasisPoints, conversationID, turnID, now)
	if err != nil {
		return KnowledgeRecord{}, fmt.Errorf("inserting verified knowledge: %w", err)
	}
	for index, source := range sources {
		_, err := tx.Exec(queryCtx, "INSERT INTO knowledge_sources(knowledge_id, source_id, title, url, snippet, rank, fetched_at_ms) VALUES ($1, $2, $3, $4, $5, $6, $7)", id, newID(), source.Title, source.URL, source.Snippet, source.Rank, source.FetchedAtUnixMS)
		if err != nil {
			return KnowledgeRecord{}, fmt.Errorf("inserting knowledge source[%d]: %w", index, err)
		}
	}
	if err := enqueueKnowledgeEmbeddingJobPostgres(queryCtx, tx, id, topic, statement, now); err != nil {
		return KnowledgeRecord{}, err
	}
	if err := tx.Commit(queryCtx); err != nil {
		return KnowledgeRecord{}, fmt.Errorf("committing knowledge insert: %w", err)
	}
	return knowledgeByIDPostgres(ctx, s.pool.Raw(), id)
}

func (s *Store) enqueueKnowledgeIngestSnapshotsPostgres(ctx context.Context, snapshots []KnowledgeIngestSnapshot) error {
	if len(snapshots) == 0 {
		return nil
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	tx, err := s.pool.Raw().Begin(queryCtx)
	if err != nil {
		return fmt.Errorf("beginning knowledge ingest enqueue transaction: %w", err)
	}
	defer tx.Rollback(queryCtx)
	now := nowUnixMS()
	for _, snapshot := range snapshots {
		if strings.TrimSpace(snapshot.Title) == "" && strings.TrimSpace(snapshot.Snippet) == "" {
			continue
		}
		if err := validateID("conversation_id", snapshot.ConversationID); err != nil {
			return err
		}
		if err := validateID("turn_id", snapshot.TurnID); err != nil {
			return err
		}
		if snapshot.Rank < 1 || snapshot.Rank > 5 {
			return errors.New("knowledge ingest rank is invalid")
		}
		_, err := tx.Exec(queryCtx, `
INSERT INTO knowledge_ingest_jobs(
  id, conversation_id, turn_id, query, title, url, snippet, rank, fetched_at_ms,
  status, created_at_ms, updated_at_ms
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 'pending', $10, $10)`, newID(), snapshot.ConversationID, snapshot.TurnID, snapshot.Query, snapshot.Title, snapshot.URL, snapshot.Snippet, snapshot.Rank, snapshot.FetchedAtUnixMS, now)
		if err != nil {
			return fmt.Errorf("queueing knowledge ingest job: %w", err)
		}
	}
	if err := tx.Commit(queryCtx); err != nil {
		return fmt.Errorf("committing knowledge ingest enqueue: %w", err)
	}
	return nil
}

func (s *Store) processKnowledgeIngestJobsPostgres(ctx context.Context, limit int) (int, error) {
	if limit < 1 || limit > maxKnowledgeIngestJobsPerPass {
		return 0, fmt.Errorf("knowledge ingest job limit must be between 1 and %d", maxKnowledgeIngestJobsPerPass)
	}
	jobs, err := s.claimKnowledgeIngestJobsPostgres(ctx, limit, nowUnixMS())
	if err != nil {
		return 0, err
	}
	written := 0
	for _, job := range jobs {
		topic := strings.TrimSpace(job.title)
		if topic == "" {
			topic = strings.TrimSpace(job.query)
		}
		statement := strings.TrimSpace(job.snippet)
		if statement == "" {
			statement = topic
		}
		if !acceptKnowledgeIngest(job.query, topic, statement, job.url, job.rank) {
			if err := s.finishKnowledgeIngestJobPostgres(ctx, job.id, "dropped", ""); err != nil {
				return written, err
			}
			continue
		}
		_, err := s.InsertVerifiedKnowledgeContext(ctx, topic, statement, job.conversationID, job.turnID, 7000, []AssistantSource{{Title: job.title, URL: job.url, Snippet: job.snippet, Rank: job.rank, FetchedAtUnixMS: job.fetchedAt}})
		if err != nil {
			if finishErr := s.finishKnowledgeIngestJobPostgres(ctx, job.id, "failed", cleanEmbeddingErrorMessage(err.Error())); finishErr != nil {
				return written, finishErr
			}
			continue
		}
		if err := s.finishKnowledgeIngestJobPostgres(ctx, job.id, "succeeded", ""); err != nil {
			return written, err
		}
		written++
	}
	return written, nil
}

func (s *Store) claimKnowledgeIngestJobsPostgres(ctx context.Context, limit int, now int64) ([]knowledgeIngestJob, error) {
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	leaseExpires := now + s.jobLeaseDuration.Milliseconds()
	rows, err := s.pool.Raw().Query(queryCtx, `
WITH candidates AS (
  SELECT id FROM knowledge_ingest_jobs
  WHERE status = 'pending' OR (status = 'running' AND lease_expires_at_ms <= $1)
  ORDER BY updated_at_ms ASC, id ASC
  LIMIT $2
  FOR UPDATE SKIP LOCKED
)
UPDATE knowledge_ingest_jobs j
SET status = 'running', lease_owner = $3, lease_expires_at_ms = $4,
    attempt_count = j.attempt_count + 1, updated_at_ms = $1
FROM candidates c
WHERE j.id = c.id
RETURNING j.id, j.conversation_id, j.turn_id, j.query, j.title, j.url, j.snippet, j.rank, j.fetched_at_ms`, now, limit, s.workerID, leaseExpires)
	if err != nil {
		return nil, fmt.Errorf("claiming knowledge ingest jobs: %w", err)
	}
	defer rows.Close()
	jobs := make([]knowledgeIngestJob, 0, limit)
	for rows.Next() {
		var job knowledgeIngestJob
		var rank int
		if err := rows.Scan(&job.id, &job.conversationID, &job.turnID, &job.query, &job.title, &job.url, &job.snippet, &rank, &job.fetchedAt); err != nil {
			return nil, fmt.Errorf("scanning claimed knowledge ingest job: %w", err)
		}
		if rank < 1 || rank > 5 {
			return nil, errors.New("claimed knowledge ingest rank is invalid")
		}
		job.rank = uint8(rank)
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating claimed knowledge ingest jobs: %w", err)
	}
	return jobs, nil
}

func (s *Store) finishKnowledgeIngestJobPostgres(ctx context.Context, id, status, message string) error {
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	changed, err := s.pool.Raw().Exec(queryCtx, `
UPDATE knowledge_ingest_jobs
SET status = $3, lease_owner = NULL, lease_expires_at_ms = NULL,
    error_message = NULLIF($4, ''), updated_at_ms = $5
WHERE id = $1 AND status = 'running' AND lease_owner = $2`, id, s.workerID, status, message, nowUnixMS())
	if err != nil {
		return fmt.Errorf("finishing knowledge ingest job: %w", err)
	}
	if changed.RowsAffected() != 1 {
		return errors.New("knowledge ingest job is not owned by this worker")
	}
	return nil
}
