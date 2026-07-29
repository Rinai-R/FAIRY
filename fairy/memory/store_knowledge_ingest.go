package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

const (
	maxKnowledgeIngestFacts          = 8
	maxKnowledgeIngestTopicRunes     = 300
	maxKnowledgeIngestStatementRunes = 1200
)

type preparedKnowledgeIngestFact struct {
	fact      KnowledgeIngestFact
	embedding EmbeddingValue
}

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
		if err := ValidateID("conversation_id", snapshot.ConversationID); err != nil {
			return err
		}
		if err := ValidateID("turn_id", snapshot.TurnID); err != nil {
			return err
		}
		if snapshot.Rank < 1 || snapshot.Rank > 5 {
			return errors.New("knowledge ingest rank is invalid")
		}
		if err := EnqueueKnowledgeIngestJob(queryCtx, tx, newID(), snapshot.ConversationID, snapshot.TurnID, snapshot.Query, snapshot.Title, snapshot.URL, snapshot.Snippet, snapshot.Rank, snapshot.FetchedAtUnixMS, now); err != nil {
			return err
		}
	}
	if err := tx.Commit(queryCtx); err != nil {
		return fmt.Errorf("committing knowledge ingest enqueue: %w", err)
	}
	return nil
}

func (s *Store) enqueueKnowledgeIngestBatchesPostgres(ctx context.Context, batches []KnowledgeIngestBatch) error {
	if len(batches) == 0 {
		return nil
	}
	encoded := make([][]byte, len(batches))
	for index, batch := range batches {
		var err error
		encoded[index], err = validateKnowledgeIngestBatch(batch)
		if err != nil {
			return fmt.Errorf("validating knowledge ingest batch[%d]: %w", index, err)
		}
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	tx, err := s.pool.Raw().Begin(queryCtx)
	if err != nil {
		return fmt.Errorf("beginning knowledge ingest batch enqueue transaction: %w", err)
	}
	defer tx.Rollback(queryCtx)
	now := nowUnixMS()
	for index, batch := range batches {
		if err := EnqueueKnowledgeIngestBatch(
			queryCtx, tx, newID(), batch.ConversationID, batch.TurnID,
			batch.ID, batch.Category, encoded[index], now,
		); err != nil {
			return err
		}
	}
	if err := tx.Commit(queryCtx); err != nil {
		return fmt.Errorf("committing knowledge ingest batch enqueue: %w", err)
	}
	return nil
}

func (s *Store) claimKnowledgeIngestBatchesPostgres(ctx context.Context, limit int) ([]KnowledgeIngestClaim, error) {
	if limit < 1 || limit > MaxKnowledgeIngestJobsPerPass {
		return nil, fmt.Errorf("knowledge ingest job limit must be between 1 and %d", MaxKnowledgeIngestJobsPerPass)
	}
	jobs, err := s.claimKnowledgeIngestJobsPostgres(ctx, limit, nowUnixMS())
	if err != nil {
		return nil, err
	}
	claims := make([]KnowledgeIngestClaim, 0, len(jobs))
	for _, job := range jobs {
		batch, err := knowledgeIngestBatchFromJob(job)
		if err != nil {
			if finishErr := s.finishKnowledgeIngestJobPostgres(ctx, job.ID, "failed", CleanEmbeddingErrorMessage(err.Error())); finishErr != nil {
				return nil, errors.Join(err, finishErr)
			}
			continue
		}
		claims = append(claims, KnowledgeIngestClaim{JobID: job.ID, Batch: batch})
	}
	return claims, nil
}

func (s *Store) commitKnowledgeIngestBatchPostgres(ctx context.Context, jobID, batchID string, facts []KnowledgeIngestFact) (int, error) {
	if err := ValidateID("knowledge_ingest_job_id", jobID); err != nil {
		return 0, err
	}
	if err := ValidateID("knowledge_ingest_batch_id", batchID); err != nil {
		return 0, err
	}
	if len(facts) == 0 || len(facts) > maxKnowledgeIngestFacts {
		return 0, errors.New("knowledge ingest fact count is invalid")
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()

	job, err := loadOwnedKnowledgeIngestJob(queryCtx, s.pool.Raw(), jobID, s.workerID, false)
	if err != nil {
		return 0, err
	}
	batch, err := knowledgeIngestBatchFromJob(job)
	if err != nil {
		return 0, err
	}
	if batch.ID != batchID {
		return 0, errors.New("knowledge ingest batch does not match claimed job")
	}
	sourceByID := make(map[string]KnowledgeIngestSource, len(batch.Sources))
	for _, source := range batch.Sources {
		sourceByID[source.ID] = source
	}
	prepared := make([]preparedKnowledgeIngestFact, len(facts))
	contents := make([]string, len(facts))
	seenStatements := make(map[string]struct{}, len(facts))
	for index, fact := range facts {
		fact.Topic = strings.TrimSpace(fact.Topic)
		fact.Statement = strings.TrimSpace(fact.Statement)
		if fact.Topic == "" || utf8.RuneCountInString(fact.Topic) > maxKnowledgeIngestTopicRunes {
			return 0, fmt.Errorf("knowledge ingest fact[%d] topic is invalid", index)
		}
		statementRunes := utf8.RuneCountInString(fact.Statement)
		if statementRunes < 8 || statementRunes > maxKnowledgeIngestStatementRunes {
			return 0, fmt.Errorf("knowledge ingest fact[%d] statement is invalid", index)
		}
		if fact.ConfidenceBasisPoints == 0 || fact.ConfidenceBasisPoints > 10000 {
			return 0, fmt.Errorf("knowledge ingest fact[%d] confidence is invalid", index)
		}
		if _, duplicate := seenStatements[fact.Statement]; duplicate {
			return 0, errors.New("knowledge ingest statements are duplicated")
		}
		seenStatements[fact.Statement] = struct{}{}
		if len(fact.SourceHitIDs) == 0 || len(fact.SourceHitIDs) > MaxKnowledgeIngestSources {
			return 0, fmt.Errorf("knowledge ingest fact[%d] source count is invalid", index)
		}
		seenSourceIDs := make(map[string]struct{}, len(fact.SourceHitIDs))
		for _, sourceID := range fact.SourceHitIDs {
			if _, exists := sourceByID[sourceID]; !exists {
				return 0, fmt.Errorf("knowledge ingest fact[%d] references an unknown source", index)
			}
			if _, duplicate := seenSourceIDs[sourceID]; duplicate {
				return 0, fmt.Errorf("knowledge ingest fact[%d] source is duplicated", index)
			}
			seenSourceIDs[sourceID] = struct{}{}
		}
		fact.SourceHitIDs = append([]string(nil), fact.SourceHitIDs...)
		prepared[index].fact = fact
		contents[index] = fact.Topic + "\n" + fact.Statement
	}
	embeddings, err := embeddingsForContents(s.semanticEmbedder, contents)
	if err != nil {
		return 0, err
	}
	for index := range prepared {
		prepared[index].embedding = embeddings[index]
	}
	sort.Slice(prepared, func(left, right int) bool {
		return prepared[left].fact.Statement < prepared[right].fact.Statement
	})

	tx, err := s.pool.Raw().Begin(queryCtx)
	if err != nil {
		return 0, fmt.Errorf("beginning knowledge ingest commit transaction: %w", err)
	}
	defer tx.Rollback(queryCtx)
	lockedJob, err := loadOwnedKnowledgeIngestJob(queryCtx, tx, jobID, s.workerID, true)
	if err != nil {
		return 0, err
	}
	lockedBatch, err := knowledgeIngestBatchFromJob(lockedJob)
	if err != nil {
		return 0, err
	}
	if lockedBatch.ID != batchID ||
		lockedBatch.ConversationID != batch.ConversationID ||
		lockedBatch.TurnID != batch.TurnID ||
		lockedBatch.Category != batch.Category {
		return 0, errors.New("knowledge ingest batch changed before commit")
	}
	lockedSourceByID := make(map[string]KnowledgeIngestSource, len(lockedBatch.Sources))
	for _, source := range lockedBatch.Sources {
		lockedSourceByID[source.ID] = source
	}
	if len(lockedSourceByID) != len(sourceByID) {
		return 0, errors.New("knowledge ingest batch sources changed before commit")
	}
	for sourceID, source := range sourceByID {
		if lockedSource, exists := lockedSourceByID[sourceID]; !exists || lockedSource != source {
			return 0, errors.New("knowledge ingest batch sources changed before commit")
		}
	}
	sourceByID = lockedSourceByID
	for index, item := range prepared {
		for _, sourceID := range item.fact.SourceHitIDs {
			if _, exists := sourceByID[sourceID]; !exists {
				return 0, fmt.Errorf("knowledge ingest fact[%d] source changed before commit", index)
			}
		}
	}
	now := nowUnixMS()
	for index, item := range prepared {
		if _, err := tx.Exec(queryCtx, "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", item.fact.Statement); err != nil {
			return 0, fmt.Errorf("locking knowledge statement[%d]: %w", index, err)
		}
		knowledgeID, found, err := FindVerifiedKnowledgeIDByStatement(queryCtx, tx, item.fact.Statement)
		if err != nil {
			return 0, err
		}
		if !found {
			knowledgeID = newID()
			if err := InsertVerifiedKnowledgeEntry(
				queryCtx, tx, knowledgeID, item.fact.Topic, item.fact.Statement,
				lockedBatch.ConversationID, lockedBatch.TurnID,
				item.fact.ConfidenceBasisPoints, now, item.embedding,
			); err != nil {
				return 0, fmt.Errorf("inserting knowledge fact[%d]: %w", index, err)
			}
		}
		for sourceIndex, sourceID := range item.fact.SourceHitIDs {
			source := sourceByID[sourceID]
			if err := InsertCanonicalKnowledgeSource(queryCtx, tx, knowledgeID, newID(), AssistantSource{
				Title: source.Title, URL: source.URL, Snippet: source.Snippet,
				Rank: source.Rank, FetchedAtUnixMS: source.FetchedAtUnixMS,
			}); err != nil {
				return 0, fmt.Errorf("merging knowledge fact[%d] source[%d]: %w", index, sourceIndex, err)
			}
		}
	}
	if err := FinishKnowledgeIngestJob(queryCtx, tx, jobID, s.workerID, "succeeded", "", now); err != nil {
		return 0, err
	}
	if err := tx.Commit(queryCtx); err != nil {
		return 0, fmt.Errorf("committing knowledge ingest batch: %w", err)
	}
	return len(prepared), nil
}

func loadOwnedKnowledgeIngestJob(ctx context.Context, db ConversationDB, jobID, workerID string, forUpdate bool) (KnowledgeIngestJob, error) {
	query := `
SELECT id, conversation_id, turn_id, query, title, url, snippet,
       rank, fetched_at_ms, batch_id, sources_json
FROM knowledge_ingest_jobs
WHERE id = $1 AND status = 'running' AND lease_owner = $2`
	if forUpdate {
		query += " FOR UPDATE"
	}
	var job KnowledgeIngestJob
	var rank int
	if err := db.QueryRow(ctx, query, jobID, workerID).Scan(
		&job.ID, &job.ConversationID, &job.TurnID, &job.Query,
		&job.Title, &job.URL, &job.Snippet, &rank, &job.FetchedAt,
		&job.BatchID, &job.SourcesJSON,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return KnowledgeIngestJob{}, errors.New("knowledge ingest job is not owned by this worker")
		}
		return KnowledgeIngestJob{}, fmt.Errorf("loading claimed knowledge ingest job: %w", err)
	}
	if rank < 0 || rank > 255 || job.BatchID == "" && (rank < 1 || rank > 5) {
		return KnowledgeIngestJob{}, errors.New("claimed knowledge ingest rank is invalid")
	}
	job.Rank = uint8(rank)
	return job, nil
}

func knowledgeIngestBatchFromJob(job KnowledgeIngestJob) (KnowledgeIngestBatch, error) {
	batchID := job.BatchID
	sources := make([]KnowledgeIngestSource, 0, MaxKnowledgeIngestSources)
	if batchID == "" {
		batchID = "legacy-" + job.ID
		sources = append(sources, KnowledgeIngestSource{
			ID: "legacy-source-" + job.ID, Title: job.Title, URL: job.URL, Snippet: job.Snippet,
			Rank: job.Rank, FetchedAtUnixMS: job.FetchedAt,
		})
	} else {
		decoder := json.NewDecoder(strings.NewReader(string(job.SourcesJSON)))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&sources); err != nil {
			return KnowledgeIngestBatch{}, errors.New("knowledge ingest sources JSON is invalid")
		}
	}
	batch := KnowledgeIngestBatch{
		ID: batchID, ConversationID: job.ConversationID, TurnID: job.TurnID,
		Category: job.Query, Sources: sources,
	}
	if job.BatchID != "" {
		if _, err := validateKnowledgeIngestBatch(batch); err != nil {
			return KnowledgeIngestBatch{}, err
		}
	}
	return batch, nil
}

func (s *Store) processKnowledgeIngestJobsPostgres(ctx context.Context, limit int) (int, error) {
	if limit < 1 || limit > MaxKnowledgeIngestJobsPerPass {
		return 0, fmt.Errorf("knowledge ingest job limit must be between 1 and %d", MaxKnowledgeIngestJobsPerPass)
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	now := nowUnixMS()
	jobs, err := ClaimLegacyKnowledgeIngestJobs(
		queryCtx, s.pool.Raw(), limit, now, s.workerID,
		now+s.jobLeaseDuration.Milliseconds(),
	)
	if err != nil {
		return 0, err
	}
	written := 0
	for _, job := range jobs {
		topic := strings.TrimSpace(job.Title)
		if topic == "" {
			topic = strings.TrimSpace(job.Query)
		}
		statement := strings.TrimSpace(job.Snippet)
		if statement == "" {
			statement = topic
		}
		if !acceptKnowledgeIngest(job.Query, topic, statement, job.URL, job.Rank) {
			if err := s.finishKnowledgeIngestJobPostgres(ctx, job.ID, "dropped", ""); err != nil {
				return written, err
			}
			continue
		}
		_, err := s.InsertVerifiedKnowledgeContext(ctx, topic, statement, job.ConversationID, job.TurnID, 7000, []AssistantSource{{Title: job.Title, URL: job.URL, Snippet: job.Snippet, Rank: job.Rank, FetchedAtUnixMS: job.FetchedAt}})
		if err != nil {
			if finishErr := s.finishKnowledgeIngestJobPostgres(ctx, job.ID, "failed", CleanEmbeddingErrorMessage(err.Error())); finishErr != nil {
				return written, finishErr
			}
			continue
		}
		if err := s.finishKnowledgeIngestJobPostgres(ctx, job.ID, "succeeded", ""); err != nil {
			return written, err
		}
		written++
	}
	return written, nil
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
