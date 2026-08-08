package social

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
	if err := ValidateSocialMemoryBatch(valid); err != nil {
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
			if err := ValidateSocialMemoryBatch(input); err == nil {
				t.Fatal("invalid social memory batch accepted")
			}
		})
	}
}

func validSocialFeedbackBatch() SocialFeedbackBatchInput {
	return SocialFeedbackBatchInput{
		CharacterID: "character-1", ConversationID: "conversation-1", TurnID: "turn-1",
		ObservedMessageCount: 2, EvaluatorRevision: "social-feedback-v1",
		Evaluations: []SocialFeedbackEvaluation{{
			EntryID: "entry-1", Adoption: SocialFeedbackAdopted, Outcome: SocialFeedbackPositive,
			Credit: SocialFeedbackCreditEntry, EvidenceMessageIDs: []string{"message-1"},
		}},
	}
}

func TestValidateSocialFeedbackBatchRejectsInvalidAttributionAndEvidence(t *testing.T) {
	if err := ValidateSocialFeedbackBatch(validSocialFeedbackBatch()); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*SocialFeedbackBatchInput)
	}{
		{"empty evaluations", func(input *SocialFeedbackBatchInput) { input.Evaluations = nil }},
		{"duplicate entry", func(input *SocialFeedbackBatchInput) {
			input.Evaluations = append(input.Evaluations, input.Evaluations[0])
		}},
		{"invalid adoption", func(input *SocialFeedbackBatchInput) { input.Evaluations[0].Adoption = "maybe" }},
		{"not adopted negative", func(input *SocialFeedbackBatchInput) { input.Evaluations[0].Adoption = SocialFeedbackNotAdopted }},
		{"unknown with entry credit", func(input *SocialFeedbackBatchInput) {
			input.Evaluations[0].Outcome = SocialFeedbackUnknown
			input.Evaluations[0].EvidenceMessageIDs = nil
		}},
		{"known without evidence", func(input *SocialFeedbackBatchInput) { input.Evaluations[0].EvidenceMessageIDs = nil }},
		{"unknown with evidence", func(input *SocialFeedbackBatchInput) {
			input.Evaluations[0].Outcome = SocialFeedbackUnknown
			input.Evaluations[0].Credit = SocialFeedbackCreditUnknown
		}},
		{"duplicate evidence", func(input *SocialFeedbackBatchInput) {
			input.Evaluations[0].EvidenceMessageIDs = []string{"message-1", "message-1"}
		}},
		{"evidence exceeds observations", func(input *SocialFeedbackBatchInput) { input.ObservedMessageCount = 0 }},
		{"observations exceed window", func(input *SocialFeedbackBatchInput) { input.ObservedMessageCount = MaxSocialFeedbackObservedCount + 1 }},
		{"empty revision", func(input *SocialFeedbackBatchInput) { input.EvaluatorRevision = "" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := validSocialFeedbackBatch()
			test.mutate(&input)
			if err := ValidateSocialFeedbackBatch(input); err == nil {
				t.Fatal("invalid social feedback batch accepted")
			}
		})
	}
}

func TestSocialFeedbackEffectUnitsOnlyCreditsAdoptedEntryOutcomes(t *testing.T) {
	tests := []struct {
		name        string
		evaluation  SocialFeedbackEvaluation
		wantHelpful int64
		wantHarmful int64
	}{
		{"positive", SocialFeedbackEvaluation{Adoption: SocialFeedbackAdopted, Outcome: SocialFeedbackPositive, Credit: SocialFeedbackCreditEntry}, 2, 0},
		{"partial", SocialFeedbackEvaluation{Adoption: SocialFeedbackAdopted, Outcome: SocialFeedbackPartial, Credit: SocialFeedbackCreditEntry}, 1, 0},
		{"negative", SocialFeedbackEvaluation{Adoption: SocialFeedbackAdopted, Outcome: SocialFeedbackNegative, Credit: SocialFeedbackCreditEntry}, 0, 2},
		{"execution", SocialFeedbackEvaluation{Adoption: SocialFeedbackAdopted, Outcome: SocialFeedbackNegative, Credit: SocialFeedbackCreditExecution}, 0, 0},
		{"context", SocialFeedbackEvaluation{Adoption: SocialFeedbackAdopted, Outcome: SocialFeedbackNegative, Credit: SocialFeedbackCreditContext}, 0, 0},
		{"not adopted", SocialFeedbackEvaluation{Adoption: SocialFeedbackNotAdopted, Outcome: SocialFeedbackUnknown, Credit: SocialFeedbackCreditUnknown}, 0, 0},
		{"uncertain", SocialFeedbackEvaluation{Adoption: SocialFeedbackUncertain, Outcome: SocialFeedbackUnknown, Credit: SocialFeedbackCreditUnknown}, 0, 0},
		{"unknown", SocialFeedbackEvaluation{Adoption: SocialFeedbackAdopted, Outcome: SocialFeedbackUnknown, Credit: SocialFeedbackCreditUnknown}, 0, 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			helpful, harmful := SocialFeedbackEffectUnits(test.evaluation)
			if helpful != test.wantHelpful || harmful != test.wantHarmful {
				t.Fatalf("effect units = (%d, %d), want (%d, %d)", helpful, harmful, test.wantHelpful, test.wantHarmful)
			}
		})
	}
}

func TestSocialFeedbackScoreBasisPointsUsesFixedPrior(t *testing.T) {
	tests := []struct {
		positive, partial, negative int64
		want                        int
	}{
		{0, 0, 0, 0},
		{1, 0, 0, 3333},
		{0, 1, 0, 2000},
		{0, 0, 1, -3333},
		{1, 0, 1, 0},
		{3, 0, 0, 6000},
		{0, 0, 3, -6000},
	}
	for _, test := range tests {
		if got := SocialFeedbackScoreBasisPoints(test.positive, test.partial, test.negative); got != test.want {
			t.Errorf("score(%d,%d,%d) = %d, want %d", test.positive, test.partial, test.negative, got, test.want)
		}
	}
}
