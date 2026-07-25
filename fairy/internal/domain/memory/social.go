package memory

import (
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

	MaxSocialSituationRunes         = 240
	MaxSocialContentRunes           = 800
	MaxSocialRecallRunes            = 400
	MaxSocialBatchEntries           = 12
	MaxSocialFeedbackIDs            = 12
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

type RecentSocialFeedbackSummary struct {
	SampleCount          int
	PositiveCount        int
	NegativeCount        int
	UnknownCount         int
	ObservedMessageCount int
	LatestOutcome        string
}

func (s RecentSocialFeedbackSummary) Empty() bool { return s.SampleCount == 0 }

func ValidSocialMemoryKind(kind string) bool {
	return kind == SocialMemoryEpisode || kind == SocialMemoryExpression || kind == SocialMemoryBehavior
}

func ValidateSocialText(name, value string, limit int) error {
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

func ValidateSocialMemoryBatch(input SocialMemoryBatchInput) error {
	if err := ValidateID("character_id", input.CharacterID); err != nil {
		return err
	}
	if err := ValidateID("conversation_id", input.ConversationID); err != nil {
		return err
	}
	if len(input.Entries) == 0 || len(input.Entries) > MaxSocialBatchEntries {
		return fmt.Errorf("social memory batch must contain between 1 and %d entries", MaxSocialBatchEntries)
	}
	for index, entry := range input.Entries {
		if !ValidSocialMemoryKind(entry.Kind) {
			return fmt.Errorf("social memory entry %d kind is invalid", index)
		}
		if err := ValidateSocialText("situation", entry.Situation, MaxSocialSituationRunes); err != nil {
			return fmt.Errorf("social memory entry %d: %w", index, err)
		}
		if err := ValidateSocialText("content", entry.Content, MaxSocialContentRunes); err != nil {
			return fmt.Errorf("social memory entry %d: %w", index, err)
		}
		if err := ValidateSocialText("recall_cue", entry.RecallCue, MaxSocialRecallRunes); err != nil {
			return fmt.Errorf("social memory entry %d: %w", index, err)
		}
		if entry.SourceStartUnixMS <= 0 || entry.SourceEndUnixMS < entry.SourceStartUnixMS {
			return fmt.Errorf("social memory entry %d source time range is invalid", index)
		}
	}
	return nil
}

func ValidateSocialReplyFeedback(input SocialReplyFeedbackInput) error {
	if err := ValidateID("character_id", input.CharacterID); err != nil {
		return err
	}
	if err := ValidateID("conversation_id", input.ConversationID); err != nil {
		return err
	}
	if err := ValidateID("turn_id", input.TurnID); err != nil {
		return err
	}
	if input.Outcome != SocialFeedbackPositive && input.Outcome != SocialFeedbackNegative && input.Outcome != SocialFeedbackUnknown {
		return errors.New("social feedback outcome is invalid")
	}
	if len(input.EntryIDs) > MaxSocialFeedbackIDs {
		return fmt.Errorf("social feedback must reference at most %d entries", MaxSocialFeedbackIDs)
	}
	seen := make(map[string]struct{}, len(input.EntryIDs))
	for _, id := range input.EntryIDs {
		if err := ValidateID("social_memory_entry_id", id); err != nil {
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
