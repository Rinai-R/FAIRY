package memory

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
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

func (s *Store) CommitKnowledgeDocumentActions(
	task KnowledgeIngestTask,
	document KnowledgeDocument,
	suppliedKnowledgeIDs []string,
	actions []KnowledgeDocumentAction,
) (int, error) {
	return s.CommitKnowledgeDocumentActionsContext(context.Background(), task, document, suppliedKnowledgeIDs, actions)
}

func (s *Store) CommitKnowledgeDocumentActionsContext(
	ctx context.Context,
	task KnowledgeIngestTask,
	document KnowledgeDocument,
	suppliedKnowledgeIDs []string,
	actions []KnowledgeDocumentAction,
) (int, error) {
	return s.commitKnowledgeDocumentActionsPostgres(ctx, task, document, suppliedKnowledgeIDs, actions)
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
