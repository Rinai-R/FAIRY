package memory

import (
	"context"
	"errors"
)

type MemoryScope struct {
	Type        string `json:"type"`
	CharacterID string `json:"characterId,omitempty"`
}

type PersonalMemoryRecord struct {
	ID                    string      `json:"id"`
	Kind                  string      `json:"kind"`
	Scope                 MemoryScope `json:"scope"`
	ReviewStatus          string      `json:"reviewStatus"`
	Content               string      `json:"content"`
	Status                string      `json:"status"`
	ConfidenceBasisPoints uint16      `json:"confidenceBasisPoints"`
	SourceConversationID  string      `json:"sourceConversationId"`
	SourceTurnID          string      `json:"sourceTurnId"`
	SupersedesID          *string     `json:"supersedesId"`
	CreatedAtUnixMS       int64       `json:"createdAtUnixMs"`
	UpdatedAtUnixMS       int64       `json:"updatedAtUnixMs"`
}

type PersonalMemoryCatalog struct {
	Global      []PersonalMemoryRecord `json:"global"`
	Character   []PersonalMemoryRecord `json:"character"`
	NeedsReview []PersonalMemoryRecord `json:"needsReview"`
}

func (s *Store) PersonalMemoryCatalog(characterID string) (PersonalMemoryCatalog, error) {
	return s.PersonalMemoryCatalogContext(context.Background(), characterID)
}

func (s *Store) PersonalMemoryCatalogContext(ctx context.Context, characterID string) (PersonalMemoryCatalog, error) {
	return s.personalMemoryCatalogPostgres(ctx, characterID)
}

func (s *Store) CreatePersonalMemory(kind string, scope MemoryScope, content string, confidence uint16) (PersonalMemoryRecord, error) {
	return s.CreatePersonalMemoryContext(context.Background(), kind, scope, content, confidence)
}

func (s *Store) CreatePersonalMemoryContext(ctx context.Context, kind string, scope MemoryScope, content string, confidence uint16) (PersonalMemoryRecord, error) {
	return s.createPersonalMemoryPostgres(ctx, kind, scope, content, confidence)
}

func (s *Store) RevisePersonalMemory(id string, content string, confidence uint16) (PersonalMemoryRecord, error) {
	return s.RevisePersonalMemoryContext(context.Background(), id, content, confidence)
}

func (s *Store) RevisePersonalMemoryContext(ctx context.Context, id string, content string, confidence uint16) (PersonalMemoryRecord, error) {
	return s.revisePersonalMemoryPostgres(ctx, id, content, confidence)
}

func (s *Store) TombstonePersonalMemory(id string) error {
	return s.TombstonePersonalMemoryContext(context.Background(), id)
}

func (s *Store) TombstonePersonalMemoryContext(ctx context.Context, id string) error {
	return s.tombstonePersonalMemoryPostgres(ctx, id)
}

func (s *Store) AssignLegacyRelationship(id string, characterID string) (PersonalMemoryRecord, error) {
	return s.AssignLegacyRelationshipContext(context.Background(), id, characterID)
}

func (s *Store) AssignLegacyRelationshipContext(ctx context.Context, id string, characterID string) (PersonalMemoryRecord, error) {
	return s.assignLegacyRelationshipPostgres(ctx, id, characterID)
}

func validateMemoryInput(kind string, scope MemoryScope, content string, confidence uint16) error {
	if kind != "preference" && kind != "profile" && kind != "relationship" && kind != "experience" {
		return errors.New("memory kind is unsupported")
	}
	if kind == "relationship" {
		if scope.Type != "character" && scope.Type != "unassigned_legacy" {
			return errors.New("relationship memory requires character or legacy scope")
		}
	} else if scope.Type != "global" {
		return errors.New("non-relationship memory requires global scope")
	}
	if scope.Type == "character" {
		if err := validateID("character_id", scope.CharacterID); err != nil {
			return err
		}
	}
	if err := validateContent("memory content", content); err != nil {
		return err
	}
	if confidence > 10000 {
		return errors.New("memory confidence is invalid")
	}
	return nil
}

func memoryScopeColumns(scope MemoryScope) (string, *string, string) {
	if scope.Type == "character" {
		return "character", &scope.CharacterID, "ready"
	}
	if scope.Type == "unassigned_legacy" {
		return "unassigned_legacy", nil, "needs_review"
	}
	return "global", nil, "ready"
}
