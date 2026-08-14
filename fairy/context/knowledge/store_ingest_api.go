package knowledge

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"unicode/utf8"
)

func (s *Store) KnowledgeIngestReady() bool {
	return s.usesSeekDB() || s.usesPostgres()
}

func (s *Store) InsertVerifiedKnowledge(
	topic string,
	statement string,
	conversationID string,
	turnID string,
	confidenceBasisPoints uint16,
	sources []AssistantSource,
) (Record, error) {
	return s.insertVerifiedKnowledge(
		context.Background(), topic, statement, conversationID, turnID,
		confidenceBasisPoints, sources, false,
	)
}

func (s *Store) InsertVerifiedKnowledgeContext(
	ctx context.Context,
	topic string,
	statement string,
	conversationID string,
	turnID string,
	confidenceBasisPoints uint16,
	sources []AssistantSource,
) (Record, error) {
	return s.insertVerifiedKnowledge(
		ctx, topic, statement, conversationID, turnID,
		confidenceBasisPoints, sources, true,
	)
}

func (s *Store) insertVerifiedKnowledge(
	ctx context.Context,
	topic string,
	statement string,
	conversationID string,
	turnID string,
	confidenceBasisPoints uint16,
	sources []AssistantSource,
	requireContext bool,
) (Record, error) {
	if err := validateDirectKnowledgeSources(sources); err != nil {
		return Record{}, err
	}
	if s.usesSeekDB() {
		return s.insertVerifiedKnowledgeSeekDB(
			ctx, topic, statement, conversationID, turnID,
			confidenceBasisPoints, sources, requireContext,
		)
	}
	if !s.usesPostgres() {
		return Record{}, ErrStoreBackendUnavailable
	}
	return s.insertVerifiedKnowledgePostgres(
		ctx, topic, statement, conversationID, turnID,
		confidenceBasisPoints, sources,
	)
}

func (s *Store) SearchKnowledgeForIngest(query string, limit int) ([]Retrieved, error) {
	return s.searchKnowledgeForIngest(context.Background(), query, limit, false)
}

func (s *Store) SearchKnowledgeForIngestContext(ctx context.Context, query string, limit int) ([]Retrieved, error) {
	return s.searchKnowledgeForIngest(ctx, query, limit, true)
}

func (s *Store) searchKnowledgeForIngest(
	ctx context.Context,
	query string,
	limit int,
	requireContext bool,
) ([]Retrieved, error) {
	_ = requireContext
	if limit < 1 || limit > MaxSearchCandidates {
		return nil, errors.New("knowledge ingest search limit is invalid")
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, errors.New("knowledge ingest search query is required")
	}
	if s.usesSeekDB() {
		return s.searchForIngestSeekDB(ctx, query, limit)
	}
	if !s.usesPostgres() {
		return nil, ErrStoreBackendUnavailable
	}
	return s.searchForIngestPostgres(ctx, query, limit)
}

func (s *Store) CommitKnowledgeDocumentActions(
	task IngestTask,
	document Document,
	suppliedKnowledgeIDs []string,
	actions []DocumentAction,
) (int, error) {
	return s.commitKnowledgeDocumentActions(
		context.Background(), task, document, suppliedKnowledgeIDs, actions, false,
	)
}

func (s *Store) CommitKnowledgeDocumentActionsContext(
	ctx context.Context,
	task IngestTask,
	document Document,
	suppliedKnowledgeIDs []string,
	actions []DocumentAction,
) (int, error) {
	return s.commitKnowledgeDocumentActions(ctx, task, document, suppliedKnowledgeIDs, actions, true)
}

func (s *Store) commitKnowledgeDocumentActions(
	ctx context.Context,
	task IngestTask,
	document Document,
	suppliedKnowledgeIDs []string,
	actions []DocumentAction,
	requireContext bool,
) (int, error) {
	if s.usesSeekDB() {
		return s.commitKnowledgeDocumentActionsSeekDB(
			ctx, task, document, suppliedKnowledgeIDs, actions, requireContext,
		)
	}
	if !s.usesPostgres() {
		return 0, ErrStoreBackendUnavailable
	}
	return s.commitKnowledgeDocumentActionsPostgres(
		ctx, task, document, suppliedKnowledgeIDs, actions,
	)
}

func validateKnowledgeIngestTask(task IngestTask) ([]byte, error) {
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
	if utf8.RuneCountInString(source.Title) > MaxIngestTitleRunes || utf8.RuneCountInString(source.Snippet) > MaxIngestSnippetRunes {
		return nil, errors.New("knowledge ingest source text is too long")
	}
	parsed, err := url.Parse(source.URL)
	if err != nil || parsed.User != nil || parsed.Hostname() == "" || parsed.Fragment != "" || parsed.String() != source.URL || parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errors.New("knowledge ingest source URL is invalid")
	}
	if source.Rank < 1 || source.Rank > MaxIngestSourceRank || source.FetchedAtUnixMS < 0 {
		return nil, errors.New("knowledge ingest source metadata is invalid")
	}
	encoded, err := json.Marshal(source)
	if err != nil {
		return nil, err
	}
	if len(encoded) > MaxIngestSourceJSONBytes {
		return nil, errors.New("knowledge ingest source payload is too large")
	}
	return encoded, nil
}
