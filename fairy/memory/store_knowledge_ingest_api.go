package memory

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"unicode/utf8"
)

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

func (s *Store) EnqueueKnowledgeIngestSnapshots(snapshots []KnowledgeIngestSnapshot) error {
	return s.EnqueueKnowledgeIngestSnapshotsContext(context.Background(), snapshots)
}

func (s *Store) EnqueueKnowledgeIngestSnapshotsContext(ctx context.Context, snapshots []KnowledgeIngestSnapshot) error {
	return s.enqueueKnowledgeIngestSnapshotsPostgres(ctx, snapshots)
}

func (s *Store) EnqueueKnowledgeIngestBatches(batches []KnowledgeIngestBatch) error {
	return s.EnqueueKnowledgeIngestBatchesContext(context.Background(), batches)
}

func (s *Store) EnqueueKnowledgeIngestBatchesContext(ctx context.Context, batches []KnowledgeIngestBatch) error {
	return s.enqueueKnowledgeIngestBatchesPostgres(ctx, batches)
}

func (s *Store) ClaimKnowledgeIngestBatches(limit int) ([]KnowledgeIngestClaim, error) {
	return s.ClaimKnowledgeIngestBatchesContext(context.Background(), limit)
}

func (s *Store) ClaimKnowledgeIngestBatchesContext(ctx context.Context, limit int) ([]KnowledgeIngestClaim, error) {
	return s.claimKnowledgeIngestBatchesPostgres(ctx, limit)
}

func (s *Store) FailKnowledgeIngestBatch(jobID, message string) error {
	return s.FailKnowledgeIngestBatchContext(context.Background(), jobID, message)
}

func (s *Store) FailKnowledgeIngestBatchContext(ctx context.Context, jobID, message string) error {
	if err := ValidateID("knowledge_ingest_job_id", jobID); err != nil {
		return err
	}
	return s.finishKnowledgeIngestJobPostgres(ctx, jobID, "failed", CleanEmbeddingErrorMessage(message))
}

func (s *Store) DropKnowledgeIngestBatch(jobID, message string) error {
	return s.DropKnowledgeIngestBatchContext(context.Background(), jobID, message)
}

func (s *Store) DropKnowledgeIngestBatchContext(ctx context.Context, jobID, message string) error {
	if err := ValidateID("knowledge_ingest_job_id", jobID); err != nil {
		return err
	}
	return s.finishKnowledgeIngestJobPostgres(ctx, jobID, "dropped", CleanEmbeddingErrorMessage(message))
}

func (s *Store) CommitKnowledgeIngestBatch(jobID, batchID string, facts []KnowledgeIngestFact) (int, error) {
	return s.CommitKnowledgeIngestBatchContext(context.Background(), jobID, batchID, facts)
}

func (s *Store) CommitKnowledgeIngestBatchContext(ctx context.Context, jobID, batchID string, facts []KnowledgeIngestFact) (int, error) {
	return s.commitKnowledgeIngestBatchPostgres(ctx, jobID, batchID, facts)
}

func (s *Store) ProcessKnowledgeIngestJobs(limit int) (int, error) {
	return s.ProcessKnowledgeIngestJobsContext(context.Background(), limit)
}

func (s *Store) ProcessKnowledgeIngestJobsContext(ctx context.Context, limit int) (int, error) {
	return s.processKnowledgeIngestJobsPostgres(ctx, limit)
}

func acceptKnowledgeIngest(category, topic, statement, sourceURL string, rank uint8) bool {
	switch strings.TrimSpace(category) {
	case "anime", "game", "book":
	default:
		return false
	}
	topic = strings.TrimSpace(topic)
	statement = strings.TrimSpace(statement)
	if topic == "" || statement == "" || rank < 1 || rank > 5 {
		return false
	}
	if utf8.RuneCountInString(statement) < 8 {
		return false
	}
	parsed, err := url.Parse(strings.TrimSpace(sourceURL))
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Hostname() == "" || parsed.User != nil {
		return false
	}
	return true
}

func validateKnowledgeIngestBatch(batch KnowledgeIngestBatch) ([]byte, error) {
	if err := ValidateID("knowledge_ingest_batch_id", batch.ID); err != nil {
		return nil, err
	}
	if err := ValidateID("conversation_id", batch.ConversationID); err != nil {
		return nil, err
	}
	if err := ValidateID("turn_id", batch.TurnID); err != nil {
		return nil, err
	}
	switch strings.TrimSpace(batch.Category) {
	case "anime", "game", "book":
	default:
		return nil, errors.New("knowledge ingest category is invalid")
	}
	if len(batch.Sources) == 0 || len(batch.Sources) > MaxKnowledgeIngestSources {
		return nil, errors.New("knowledge ingest source count is invalid")
	}
	seenIDs := make(map[string]struct{}, len(batch.Sources))
	seenURLs := make(map[string]struct{}, len(batch.Sources))
	for _, source := range batch.Sources {
		if err := ValidateID("knowledge_ingest_source_id", source.ID); err != nil {
			return nil, err
		}
		if _, duplicate := seenIDs[source.ID]; duplicate {
			return nil, errors.New("knowledge ingest source ID is duplicated")
		}
		seenIDs[source.ID] = struct{}{}
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
		if _, duplicate := seenURLs[source.URL]; duplicate {
			return nil, errors.New("knowledge ingest canonical URL is duplicated")
		}
		seenURLs[source.URL] = struct{}{}
		if source.Rank < 1 || source.Rank > MaxKnowledgeIngestSources || source.FetchedAtUnixMS < 0 {
			return nil, errors.New("knowledge ingest source metadata is invalid")
		}
	}
	encoded, err := json.Marshal(batch.Sources)
	if err != nil {
		return nil, err
	}
	if len(encoded) > MaxKnowledgeIngestSourceJSONBytes {
		return nil, errors.New("knowledge ingest source payload is too large")
	}
	return encoded, nil
}
