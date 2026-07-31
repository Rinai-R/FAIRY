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
	SocialFeedbackPartial  = "partial"
	SocialFeedbackNegative = "negative"
	SocialFeedbackUnknown  = "unknown"

	SocialFeedbackAdopted    = "adopted"
	SocialFeedbackNotAdopted = "not_adopted"
	SocialFeedbackUncertain  = "uncertain"

	SocialFeedbackCreditEntry     = "entry"
	SocialFeedbackCreditExecution = "execution"
	SocialFeedbackCreditContext   = "context"
	SocialFeedbackCreditUnknown   = "unknown"

	MaxSocialSituationRunes         = 240
	MaxSocialContentRunes           = 800
	MaxSocialRecallRunes            = 400
	MaxSocialBatchEntries           = 12
	MaxSocialFeedbackIDs            = 12
	MaxSocialFeedbackEvidenceIDs    = 6
	MaxSocialFeedbackObservedCount  = 6
	SocialNegativeSuppressThreshold = 3
	SocialFeedbackQuarantineScore   = -4000
	SocialFeedbackQuarantineMS      = int64(7 * 24 * 60 * 60 * 1000)
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
	ID                             string
	CharacterID                    string
	ConversationID                 string
	Kind                           string
	Situation                      string
	Content                        string
	RecallCue                      string
	Status                         string
	SourceStartUnixMS              int64
	SourceEndUnixMS                int64
	UseCount                       int64
	PositiveCount                  int64
	NegativeCount                  int64
	UnknownCount                   int64
	FeedbackEvaluationCount        int64
	FeedbackAdoptedCount           int64
	FeedbackPositiveCount          int64
	FeedbackPartialCount           int64
	FeedbackNegativeCount          int64
	FeedbackScoreBasisPoints       int
	FeedbackQuarantinedUntilUnixMS *int64
	CreatedAtUnixMS                int64
	UpdatedAtUnixMS                int64
}

type SocialMemoryContext struct {
	Entries []SocialMemoryEntry
}

func (c SocialMemoryContext) Empty() bool { return len(c.Entries) == 0 }

type SocialFeedbackCandidate struct {
	ID        string
	Kind      string
	Situation string
	Content   string
	RecallCue string
}

type SocialFeedbackEvaluation struct {
	EntryID            string
	Adoption           string
	Outcome            string
	Credit             string
	EvidenceMessageIDs []string
}

type SocialFeedbackBatchInput struct {
	CharacterID          string
	ConversationID       string
	TurnID               string
	Evaluations          []SocialFeedbackEvaluation
	ObservedMessageCount int
	EvaluatorRevision    string
}

type SocialFeedbackEvent struct {
	ID                   string
	CharacterID          string
	ConversationID       string
	TurnID               string
	EntryID              string
	Adoption             string
	Outcome              string
	Credit               string
	EvidenceMessageIDs   []string
	ObservedMessageCount int
	EvaluatorRevision    string
	CreatedAtUnixMS      int64
}

type SocialFeedbackBatchResult struct {
	Events   []SocialFeedbackEvent
	NoChange bool
}

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

func ValidateSocialFeedbackBatch(input SocialFeedbackBatchInput) error {
	if err := ValidateID("character_id", input.CharacterID); err != nil {
		return err
	}
	if err := ValidateID("conversation_id", input.ConversationID); err != nil {
		return err
	}
	if err := ValidateID("turn_id", input.TurnID); err != nil {
		return err
	}
	if err := ValidateID("evaluator_revision", input.EvaluatorRevision); err != nil {
		return err
	}
	if len(input.Evaluations) == 0 || len(input.Evaluations) > MaxSocialFeedbackIDs {
		return fmt.Errorf("social feedback batch must contain between 1 and %d evaluations", MaxSocialFeedbackIDs)
	}
	if input.ObservedMessageCount < 0 || input.ObservedMessageCount > MaxSocialFeedbackObservedCount {
		return fmt.Errorf("social feedback observed message count must be between 0 and %d", MaxSocialFeedbackObservedCount)
	}
	seenEntries := make(map[string]struct{}, len(input.Evaluations))
	for index, evaluation := range input.Evaluations {
		if err := ValidateID("social_memory_entry_id", evaluation.EntryID); err != nil {
			return fmt.Errorf("social feedback evaluation %d: %w", index, err)
		}
		if _, exists := seenEntries[evaluation.EntryID]; exists {
			return errors.New("social feedback contains duplicate entry IDs")
		}
		seenEntries[evaluation.EntryID] = struct{}{}
		if err := validateSocialFeedbackEvaluation(evaluation, input.ObservedMessageCount); err != nil {
			return fmt.Errorf("social feedback evaluation %d: %w", index, err)
		}
	}
	return nil
}

func validateSocialFeedbackEvaluation(evaluation SocialFeedbackEvaluation, observedMessageCount int) error {
	if evaluation.Adoption != SocialFeedbackAdopted && evaluation.Adoption != SocialFeedbackNotAdopted && evaluation.Adoption != SocialFeedbackUncertain {
		return errors.New("adoption is invalid")
	}
	if evaluation.Outcome != SocialFeedbackPositive && evaluation.Outcome != SocialFeedbackPartial && evaluation.Outcome != SocialFeedbackNegative && evaluation.Outcome != SocialFeedbackUnknown {
		return errors.New("outcome is invalid")
	}
	if evaluation.Credit != SocialFeedbackCreditEntry && evaluation.Credit != SocialFeedbackCreditExecution && evaluation.Credit != SocialFeedbackCreditContext && evaluation.Credit != SocialFeedbackCreditUnknown {
		return errors.New("credit is invalid")
	}
	if evaluation.Adoption != SocialFeedbackAdopted && (evaluation.Outcome != SocialFeedbackUnknown || evaluation.Credit != SocialFeedbackCreditUnknown) {
		return errors.New("not-adopted or uncertain feedback must have unknown outcome and credit")
	}
	if evaluation.Outcome == SocialFeedbackUnknown && evaluation.Credit != SocialFeedbackCreditUnknown {
		return errors.New("unknown outcome must have unknown credit")
	}
	if len(evaluation.EvidenceMessageIDs) > MaxSocialFeedbackEvidenceIDs || len(evaluation.EvidenceMessageIDs) > observedMessageCount {
		return errors.New("evidence message count exceeds the observed feedback window")
	}
	if (evaluation.Outcome == SocialFeedbackUnknown) != (len(evaluation.EvidenceMessageIDs) == 0) {
		return errors.New("known outcomes require evidence and unknown outcomes forbid evidence")
	}
	seenEvidence := make(map[string]struct{}, len(evaluation.EvidenceMessageIDs))
	for _, id := range evaluation.EvidenceMessageIDs {
		if err := ValidateID("social_feedback_evidence_message_id", id); err != nil {
			return err
		}
		if _, exists := seenEvidence[id]; exists {
			return errors.New("social feedback contains duplicate evidence message IDs")
		}
		seenEvidence[id] = struct{}{}
	}
	return nil
}

func SocialFeedbackEffectUnits(evaluation SocialFeedbackEvaluation) (helpful, harmful int64) {
	if evaluation.Adoption != SocialFeedbackAdopted || evaluation.Credit != SocialFeedbackCreditEntry {
		return 0, 0
	}
	switch evaluation.Outcome {
	case SocialFeedbackPositive:
		return 2, 0
	case SocialFeedbackPartial:
		return 1, 0
	case SocialFeedbackNegative:
		return 0, 2
	default:
		return 0, 0
	}
}

func SocialFeedbackScoreBasisPoints(positiveCount, partialCount, negativeCount int64) int {
	helpful := 2*positiveCount + partialCount
	harmful := 2 * negativeCount
	numerator := (helpful - harmful) * 10000
	denominator := helpful + harmful + 4
	if numerator >= 0 {
		return int((numerator + denominator/2) / denominator)
	}
	return int((numerator - denominator/2) / denominator)
}
