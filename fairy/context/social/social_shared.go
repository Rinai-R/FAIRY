package social

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"
)

func SocialMemoryContentHash(entry SocialMemoryEntryInput) string {
	digest := socialMemoryContentDigest(entry)
	return hex.EncodeToString(digest[:])
}

func socialMemoryContentDigest(entry SocialMemoryEntryInput) [32]byte {
	normalize := func(value string) string { return strings.Join(strings.Fields(value), " ") }
	payload := strings.Join([]string{
		entry.Kind, normalize(entry.Situation), normalize(entry.Content), normalize(entry.RecallCue),
	}, "\x00")
	return sha256.Sum256([]byte(payload))
}

type socialFeedbackEntryState struct {
	status           string
	positiveCount    int64
	partialCount     int64
	negativeCount    int64
	quarantinedUntil *int64
}

func sameSocialFeedbackEvent(event SocialFeedbackEvent, input SocialFeedbackBatchInput, evaluation SocialFeedbackEvaluation) bool {
	return event.CharacterID == input.CharacterID &&
		event.ConversationID == input.ConversationID &&
		event.TurnID == input.TurnID &&
		event.EntryID == evaluation.EntryID &&
		event.Adoption == evaluation.Adoption &&
		event.Outcome == evaluation.Outcome &&
		event.Credit == evaluation.Credit &&
		slices.Equal(event.EvidenceMessageIDs, evaluation.EvidenceMessageIDs) &&
		event.ObservedMessageCount == input.ObservedMessageCount &&
		event.EvaluatorRevision == input.EvaluatorRevision
}
