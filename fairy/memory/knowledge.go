package memory

import (
	"context"
)

type AssistantSource struct {
	Title           string `json:"title"`
	URL             string `json:"url"`
	Snippet         string `json:"snippet"`
	Rank            uint8  `json:"rank"`
	FetchedAtUnixMS int64  `json:"fetchedAtUnixMs"`
}

type KnowledgeRecord struct {
	ID                    string            `json:"id"`
	Topic                 string            `json:"topic"`
	Statement             string            `json:"statement"`
	Status                string            `json:"status"`
	VerificationBasis     string            `json:"verificationBasis"`
	ConfidenceBasisPoints uint16            `json:"confidenceBasisPoints"`
	SourceConversationID  string            `json:"sourceConversationId"`
	SourceTurnID          string            `json:"sourceTurnId"`
	SupersedesID          *string           `json:"supersedesId"`
	Sources               []AssistantSource `json:"sources"`
	CreatedAtUnixMS       int64             `json:"createdAtUnixMs"`
	UpdatedAtUnixMS       int64             `json:"updatedAtUnixMs"`
}

type KnowledgeCatalog struct {
	Candidates []KnowledgeRecord `json:"candidates"`
	Verified   []KnowledgeRecord `json:"verified"`
}

type WireError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

type ExtractionBatchRecord struct {
	ID                string     `json:"id"`
	ConversationID    string     `json:"conversationId"`
	CharacterID       string     `json:"characterId"`
	Status            string     `json:"status"`
	FirstTurnSequence uint64     `json:"firstTurnSequence"`
	LastTurnSequence  uint64     `json:"lastTurnSequence"`
	Error             *WireError `json:"error"`
	CreatedAtUnixMS   int64      `json:"createdAtUnixMs"`
	UpdatedAtUnixMS   int64      `json:"updatedAtUnixMs"`
}

type ExtractionBatchCatalog struct {
	Running []ExtractionBatchRecord `json:"running"`
	Failed  []ExtractionBatchRecord `json:"failed"`
}

func (s *Store) KnowledgeCatalog() (KnowledgeCatalog, error) {
	return s.KnowledgeCatalogContext(context.Background())
}

func (s *Store) KnowledgeCatalogContext(ctx context.Context) (KnowledgeCatalog, error) {
	return s.knowledgeCatalogPostgres(ctx)
}

func (s *Store) ConfirmKnowledgeCandidate(id string) (KnowledgeRecord, error) {
	return s.ConfirmKnowledgeCandidateContext(context.Background(), id)
}

func (s *Store) ConfirmKnowledgeCandidateContext(ctx context.Context, id string) (KnowledgeRecord, error) {
	return s.confirmKnowledgeCandidatePostgres(ctx, id)
}

func (s *Store) TombstoneKnowledge(id string) error {
	return s.TombstoneKnowledgeContext(context.Background(), id)
}

func (s *Store) TombstoneKnowledgeContext(ctx context.Context, id string) error {
	return s.tombstoneKnowledgePostgres(ctx, id)
}

func (s *Store) ExtractionBatchCatalog(characterID string) (ExtractionBatchCatalog, error) {
	return s.ExtractionBatchCatalogContext(context.Background(), characterID)
}

func (s *Store) ExtractionBatchCatalogContext(ctx context.Context, characterID string) (ExtractionBatchCatalog, error) {
	return s.extractionBatchCatalogPostgres(ctx, characterID)
}

func (s *Store) RetryExtractionBatch(id string) error {
	return s.RetryExtractionBatchContext(context.Background(), id)
}

func (s *Store) RetryExtractionBatchContext(ctx context.Context, id string) error {
	return s.retryExtractionBatchPostgres(ctx, id)
}
