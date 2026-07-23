package memory

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	SocialMemoryEpisode    = "episode"
	SocialMemoryExpression = "expression"
	SocialMemoryBehavior   = "behavior"

	SocialFeedbackPositive = "positive"
	SocialFeedbackNegative = "negative"
	SocialFeedbackUnknown  = "unknown"

	MaxSocialSituationRunes = 240
	MaxSocialContentRunes   = 800
	MaxSocialRecallRunes    = 400
	maxSocialBatchEntries   = 12
	maxSocialFeedbackIDs    = 12

	// SocialNegativeSuppressThreshold marks an entry suppressed after enough visible negative outcomes.
	SocialNegativeSuppressThreshold = 3
)

type SocialMemoryEntryInput struct {
	Kind              string
	Situation         string
	Content           string
	RecallCue         string
	SourceStartUnixMS int64
	SourceEndUnixMS   int64
}

type SocialMemoryBatchInput struct {
	CharacterID    string
	ConversationID string
	Entries        []SocialMemoryEntryInput
}

type SocialMemoryEntry struct {
	ID                string
	CharacterID       string
	ConversationID    string
	Kind              string
	Situation         string
	Content           string
	RecallCue         string
	Status            string
	SourceStartUnixMS int64
	SourceEndUnixMS   int64
	UseCount          int64
	PositiveCount     int64
	NegativeCount     int64
	UnknownCount      int64
	CreatedAtUnixMS   int64
	UpdatedAtUnixMS   int64
}

type SocialMemoryContext struct {
	Entries []SocialMemoryEntry
}

func (c SocialMemoryContext) Empty() bool { return len(c.Entries) == 0 }

type SocialReplyFeedbackInput struct {
	CharacterID          string
	ConversationID       string
	TurnID               string
	EntryIDs             []string
	Outcome              string
	ObservedMessageCount int
}

type SocialReplyFeedback struct {
	ID                   string
	CharacterID          string
	ConversationID       string
	TurnID               string
	EntryIDs             []string
	Outcome              string
	ObservedMessageCount int
	CreatedAtUnixMS      int64
}

func (s *Store) StoreSocialMemoryEntries(ctx context.Context, input SocialMemoryBatchInput) ([]SocialMemoryEntry, error) {
	if s == nil || s.pool == nil {
		return nil, ErrDatabasePoolEmpty
	}
	if err := validateSocialMemoryBatch(input); err != nil {
		return nil, err
	}
	return s.storeSocialMemoryEntriesPostgres(ctx, input)
}

func (s *Store) RetrieveSocialMemoryContext(ctx context.Context, characterID, conversationID, query string) (SocialMemoryContext, error) {
	if s == nil || s.pool == nil {
		return SocialMemoryContext{}, ErrDatabasePoolEmpty
	}
	if err := validateID("character_id", characterID); err != nil {
		return SocialMemoryContext{}, err
	}
	if err := validateID("conversation_id", conversationID); err != nil {
		return SocialMemoryContext{}, err
	}
	normalized, err := normalizePostgresSearchQuery(query)
	if err != nil {
		return SocialMemoryContext{}, err
	}
	if normalized == "" {
		return SocialMemoryContext{Entries: []SocialMemoryEntry{}}, nil
	}
	return s.retrieveSocialMemoryContextPostgres(ctx, characterID, conversationID, normalized)
}

func (s *Store) RecordSocialReplyFeedback(ctx context.Context, input SocialReplyFeedbackInput) (SocialReplyFeedback, error) {
	if s == nil || s.pool == nil {
		return SocialReplyFeedback{}, ErrDatabasePoolEmpty
	}
	if err := validateSocialReplyFeedback(input); err != nil {
		return SocialReplyFeedback{}, err
	}
	return s.recordSocialReplyFeedbackPostgres(ctx, input)
}

func validateSocialMemoryBatch(input SocialMemoryBatchInput) error {
	if err := validateID("character_id", input.CharacterID); err != nil {
		return err
	}
	if err := validateID("conversation_id", input.ConversationID); err != nil {
		return err
	}
	if len(input.Entries) == 0 || len(input.Entries) > maxSocialBatchEntries {
		return fmt.Errorf("social memory batch must contain between 1 and %d entries", maxSocialBatchEntries)
	}
	for index, entry := range input.Entries {
		if !validSocialMemoryKind(entry.Kind) {
			return fmt.Errorf("social memory entry %d kind is invalid", index)
		}
		if err := validateSocialText("situation", entry.Situation, MaxSocialSituationRunes); err != nil {
			return fmt.Errorf("social memory entry %d: %w", index, err)
		}
		if err := validateSocialText("content", entry.Content, MaxSocialContentRunes); err != nil {
			return fmt.Errorf("social memory entry %d: %w", index, err)
		}
		if err := validateSocialText("recall_cue", entry.RecallCue, MaxSocialRecallRunes); err != nil {
			return fmt.Errorf("social memory entry %d: %w", index, err)
		}
		if entry.SourceStartUnixMS <= 0 || entry.SourceEndUnixMS < entry.SourceStartUnixMS {
			return fmt.Errorf("social memory entry %d source time range is invalid", index)
		}
	}
	return nil
}

func validateSocialReplyFeedback(input SocialReplyFeedbackInput) error {
	if err := validateID("character_id", input.CharacterID); err != nil {
		return err
	}
	if err := validateID("conversation_id", input.ConversationID); err != nil {
		return err
	}
	if err := validateID("turn_id", input.TurnID); err != nil {
		return err
	}
	if input.Outcome != SocialFeedbackPositive && input.Outcome != SocialFeedbackNegative && input.Outcome != SocialFeedbackUnknown {
		return errors.New("social feedback outcome is invalid")
	}
	if len(input.EntryIDs) > maxSocialFeedbackIDs {
		return fmt.Errorf("social feedback must reference at most %d entries", maxSocialFeedbackIDs)
	}
	seen := make(map[string]struct{}, len(input.EntryIDs))
	for _, id := range input.EntryIDs {
		if err := validateID("social_memory_entry_id", id); err != nil {
			return err
		}
		if _, exists := seen[id]; exists {
			return errors.New("social feedback contains duplicate entry IDs")
		}
		seen[id] = struct{}{}
	}
	if input.ObservedMessageCount < 0 {
		return errors.New("social feedback observed message count must be non-negative")
	}
	return nil
}

func validSocialMemoryKind(kind string) bool {
	return kind == SocialMemoryEpisode || kind == SocialMemoryExpression || kind == SocialMemoryBehavior
}

func validateSocialText(name, value string, limit int) error {
	if value == "" || strings.TrimSpace(value) != value {
		return fmt.Errorf("social memory %s is required and must not contain control characters", name)
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return fmt.Errorf("social memory %s is required and must not contain control characters", name)
		}
	}
	if utf8.RuneCountInString(value) > limit {
		return fmt.Errorf("social memory %s must not exceed %d runes", name, limit)
	}
	return nil
}
