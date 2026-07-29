package memory

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

func (s *Store) KnowledgeIngestReady() bool {
	return s != nil && s.pool != nil && s.pool.Raw() != nil
}

func (s *Store) InsertVerifiedKnowledge(
	topic string,
	statement string,
	conversationID string,
	turnID string,
	confidenceBasisPoints uint16,
	sources []AssistantSource,
) (KnowledgeRecord, error) {
	return s.InsertVerifiedKnowledgeContext(context.Background(), topic, statement, conversationID, turnID, confidenceBasisPoints, sources)
}

func (s *Store) InsertVerifiedKnowledgeContext(
	ctx context.Context,
	topic string,
	statement string,
	conversationID string,
	turnID string,
	confidenceBasisPoints uint16,
	sources []AssistantSource,
) (KnowledgeRecord, error) {
	return s.insertVerifiedKnowledgePostgres(ctx, topic, statement, conversationID, turnID, confidenceBasisPoints, sources)
}

func (s *Store) EnqueueKnowledgeIngestTasks(tasks []KnowledgeIngestTask) error {
	return s.EnqueueKnowledgeIngestTasksContext(context.Background(), tasks)
}

func (s *Store) EnqueueKnowledgeIngestTasksContext(ctx context.Context, tasks []KnowledgeIngestTask) error {
	return s.enqueueKnowledgeIngestTasksPostgres(ctx, tasks)
}

func (s *Store) ClaimKnowledgeIngestTasks(limit int) ([]KnowledgeIngestClaim, error) {
	return s.ClaimKnowledgeIngestTasksContext(context.Background(), limit)
}

func (s *Store) ClaimKnowledgeIngestTasksContext(ctx context.Context, limit int) ([]KnowledgeIngestClaim, error) {
	return s.claimKnowledgeIngestTasksPostgres(ctx, limit)
}

func (s *Store) KnowledgeIngestLeaseDuration() time.Duration {
	if s == nil {
		return 0
	}
	return s.jobLeaseDuration
}

func (s *Store) RenewKnowledgeIngestLease(jobID string) error {
	return s.RenewKnowledgeIngestLeaseContext(context.Background(), jobID)
}

func (s *Store) RenewKnowledgeIngestLeaseContext(ctx context.Context, jobID string) error {
	if err := ValidateID("knowledge_ingest_job_id", jobID); err != nil {
		return err
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	now := nowUnixMS()
	return RenewKnowledgeIngestJobLease(
		queryCtx,
		s.pool.Raw(),
		jobID,
		s.workerID,
		now+s.jobLeaseDuration.Milliseconds(),
		now,
	)
}

func (s *Store) ReleaseClaimedKnowledgeIngestJob(jobID string) error {
	return s.ReleaseClaimedKnowledgeIngestJobContext(context.Background(), jobID)
}

func (s *Store) ReleaseClaimedKnowledgeIngestJobContext(ctx context.Context, jobID string) error {
	if err := ValidateID("knowledge_ingest_job_id", jobID); err != nil {
		return err
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	return ReleaseKnowledgeIngestJob(
		queryCtx,
		s.pool.Raw(),
		jobID,
		s.workerID,
		nowUnixMS(),
	)
}

func (s *Store) FailClaimedKnowledgeIngestJob(jobID, message string) error {
	return s.FailClaimedKnowledgeIngestJobContext(context.Background(), jobID, message)
}

func (s *Store) FailClaimedKnowledgeIngestJobContext(ctx context.Context, jobID, message string) error {
	if err := ValidateID("knowledge_ingest_job_id", jobID); err != nil {
		return err
	}
	return s.finishKnowledgeIngestJobPostgres(ctx, jobID, "failed", CleanEmbeddingErrorMessage(message))
}

func (s *Store) RetryClaimedKnowledgeIngestJob(jobID, category, message string) error {
	return s.RetryClaimedKnowledgeIngestJobContext(context.Background(), jobID, category, message)
}

func (s *Store) RetryClaimedKnowledgeIngestJobContext(ctx context.Context, jobID, category, message string) error {
	if err := ValidateID("knowledge_ingest_job_id", jobID); err != nil {
		return err
	}
	category = strings.TrimSpace(category)
	if category == "" {
		return errors.New("knowledge ingest retry category is required")
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	now := nowUnixMS()
	changed, err := s.pool.Raw().Exec(queryCtx, `
UPDATE feedback_events
SET status = CASE WHEN attempt_count >= $4 THEN 'failed' ELSE 'pending' END,
    lease_owner = NULL,
    lease_expires_at_ms = NULL,
    claim_group_id = NULL,
    next_attempt_at_ms = CASE
      WHEN attempt_count >= $4 THEN 0
      ELSE $5::bigint + LEAST(30000::bigint, 1000::bigint * (1::bigint << GREATEST(0, attempt_count - 1)))
    END,
    error_category = $2,
    error_message = NULLIF($3, ''),
    updated_at_ms = $5
WHERE id = $1 AND type = 'web_knowledge'
  AND status = 'running' AND lease_owner = $6 AND claim_group_id = id`,
		jobID, category, CleanEmbeddingErrorMessage(message), MaxKnowledgeIngestAttempts, now, s.workerID,
	)
	if err != nil {
		return err
	}
	if changed.RowsAffected() != 1 {
		return errors.New("knowledge ingest job is not owned by this worker")
	}
	return nil
}

func (s *Store) DropClaimedKnowledgeIngestJob(jobID, message string) error {
	return s.DropClaimedKnowledgeIngestJobContext(context.Background(), jobID, message)
}

func (s *Store) DropClaimedKnowledgeIngestJobContext(ctx context.Context, jobID, message string) error {
	if err := ValidateID("knowledge_ingest_job_id", jobID); err != nil {
		return err
	}
	return s.finishKnowledgeIngestJobPostgres(ctx, jobID, "dropped", CleanEmbeddingErrorMessage(message))
}

func (s *Store) KnowledgeDocumentNeedsExtraction(jobID, taskID string, document KnowledgeDocument) (bool, error) {
	return s.KnowledgeDocumentNeedsExtractionContext(context.Background(), jobID, taskID, document)
}

func (s *Store) KnowledgeDocumentNeedsExtractionContext(ctx context.Context, jobID, taskID string, document KnowledgeDocument) (bool, error) {
	return s.knowledgeDocumentNeedsExtractionPostgres(ctx, jobID, taskID, document)
}

func (s *Store) SearchKnowledgeForIngest(query string, limit int) ([]RetrievedKnowledge, error) {
	return s.SearchKnowledgeForIngestContext(context.Background(), query, limit)
}

func (s *Store) SearchKnowledgeForIngestContext(ctx context.Context, query string, limit int) ([]RetrievedKnowledge, error) {
	if limit < 1 || limit > MaxKnowledgeSearchCandidates {
		return nil, errors.New("knowledge ingest search limit is invalid")
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, errors.New("knowledge ingest search query is required")
	}
	retrieved, err := s.retrievePublicKnowledgeForIngestPostgres(ctx, query)
	if err != nil {
		return nil, err
	}
	if len(retrieved.Knowledge) > limit {
		retrieved.Knowledge = retrieved.Knowledge[:limit]
	}
	return append([]RetrievedKnowledge(nil), retrieved.Knowledge...), nil
}

func (s *Store) CommitKnowledgeDocumentActions(jobID, taskID string, document KnowledgeDocument, suppliedKnowledgeIDs []string, actions []KnowledgeDocumentAction) (int, error) {
	return s.CommitKnowledgeDocumentActionsContext(context.Background(), jobID, taskID, document, suppliedKnowledgeIDs, actions)
}

func (s *Store) CommitKnowledgeDocumentActionsContext(ctx context.Context, jobID, taskID string, document KnowledgeDocument, suppliedKnowledgeIDs []string, actions []KnowledgeDocumentAction) (int, error) {
	return s.commitKnowledgeDocumentActionsPostgres(ctx, jobID, taskID, document, suppliedKnowledgeIDs, actions)
}

func validateKnowledgeIngestTask(task KnowledgeIngestTask) ([]byte, error) {
	if err := ValidateID("knowledge_ingest_task_id", task.ID); err != nil {
		return nil, err
	}
	if err := ValidateID("conversation_id", task.ConversationID); err != nil {
		return nil, err
	}
	if err := ValidateID("turn_id", task.TurnID); err != nil {
		return nil, err
	}
	source := task.Source
	if err := ValidateID("knowledge_ingest_source_id", source.ID); err != nil {
		return nil, err
	}
	if strings.TrimSpace(source.Title) != source.Title || strings.TrimSpace(source.Snippet) != source.Snippet || source.Title == "" && source.Snippet == "" {
		return nil, errors.New("knowledge ingest source text is invalid")
	}
	if utf8.RuneCountInString(source.Title) > MaxKnowledgeIngestTitleRunes || utf8.RuneCountInString(source.Snippet) > MaxKnowledgeIngestSnippetRunes {
		return nil, errors.New("knowledge ingest source text is too long")
	}
	parsed, err := url.Parse(source.URL)
	if err != nil || parsed.User != nil || parsed.Hostname() == "" || parsed.Fragment != "" || parsed.String() != source.URL || parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errors.New("knowledge ingest source URL is invalid")
	}
	if source.Rank < 1 || source.Rank > MaxKnowledgeIngestSourceRank || source.FetchedAtUnixMS < 0 {
		return nil, errors.New("knowledge ingest source metadata is invalid")
	}
	encoded, err := json.Marshal(source)
	if err != nil {
		return nil, err
	}
	if len(encoded) > MaxKnowledgeIngestSourceJSONBytes {
		return nil, errors.New("knowledge ingest source payload is too large")
	}
	return encoded, nil
}
