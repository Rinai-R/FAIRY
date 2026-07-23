package memory

import (
	"strings"
	"testing"
)

func validSocialMemoryBatch() SocialMemoryBatchInput {
	return SocialMemoryBatchInput{
		CharacterID: "character-1", ConversationID: "conversation-1",
		Entries: []SocialMemoryEntryInput{{
			Kind: SocialMemoryExpression, Situation: "群友用反讽方式夸张吐槽时",
			Content: "用一小句顺着反讽接话，不解释梗", RecallCue: "轻松群聊中的反讽和抽象梗",
			SourceStartUnixMS: 10, SourceEndUnixMS: 20,
		}},
	}
}

func TestValidateSocialMemoryBatchRejectsRawOrInvalidCandidates(t *testing.T) {
	valid := validSocialMemoryBatch()
	if err := validateSocialMemoryBatch(valid); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*SocialMemoryBatchInput)
	}{
		{"unknown kind", func(input *SocialMemoryBatchInput) { input.Entries[0].Kind = "profile" }},
		{"empty situation", func(input *SocialMemoryBatchInput) { input.Entries[0].Situation = "" }},
		{"control content", func(input *SocialMemoryBatchInput) { input.Entries[0].Content = "bad\ncontent" }},
		{"oversized recall", func(input *SocialMemoryBatchInput) {
			input.Entries[0].RecallCue = strings.Repeat("群", MaxSocialRecallRunes+1)
		}},
		{"invalid range", func(input *SocialMemoryBatchInput) { input.Entries[0].SourceEndUnixMS = 9 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := validSocialMemoryBatch()
			test.mutate(&input)
			if err := validateSocialMemoryBatch(input); err == nil {
				t.Fatal("invalid social memory batch accepted")
			}
		})
	}
}

func TestSocialMemoryContentHashIsStableAndKindSeparated(t *testing.T) {
	entry := validSocialMemoryBatch().Entries[0]
	first := socialMemoryContentHash(entry)
	entry.Content = "用一小句顺着反讽接话，不解释梗"
	if second := socialMemoryContentHash(entry); second != first {
		t.Fatalf("stable hash changed: %s != %s", second, first)
	}
	entry.Kind = SocialMemoryBehavior
	if socialMemoryContentHash(entry) == first {
		t.Fatal("different social memory kinds shared a hash")
	}
}

func TestValidateSocialReplyFeedbackAllowsEmptyEntries(t *testing.T) {
	input := SocialReplyFeedbackInput{
		CharacterID: "character-1", ConversationID: "conversation-1", TurnID: "turn-1",
		Outcome: SocialFeedbackUnknown,
	}
	if err := validateSocialReplyFeedback(input); err != nil {
		t.Fatal(err)
	}
}

