package memory

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestValidateFeedbackEventInput(t *testing.T) {
	valid := FeedbackEventInput{
		ID: "event-1", Type: FeedbackPersonalMemory,
		ConversationID: "conversation-1", TurnID: "turn-1", CharacterID: "character-1",
		Payload: json.RawMessage(`{}`), Status: "pending",
	}
	if err := ValidateFeedbackEventInput(valid); err != nil {
		t.Fatalf("ValidateFeedbackEventInput(valid) error = %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*FeedbackEventInput)
	}{
		{name: "unknown type", mutate: func(input *FeedbackEventInput) { input.Type = "unknown" }},
		{name: "terminal initial status", mutate: func(input *FeedbackEventInput) { input.Status = "succeeded" }},
		{name: "array payload", mutate: func(input *FeedbackEventInput) { input.Payload = json.RawMessage(`[]`) }},
		{name: "trailing payload", mutate: func(input *FeedbackEventInput) { input.Payload = json.RawMessage(`{} {}`) }},
		{name: "oversized payload", mutate: func(input *FeedbackEventInput) {
			input.Payload = json.RawMessage(`{"value":"` + strings.Repeat("a", MaxFeedbackEventPayloadBytes) + `"}`)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := valid
			test.mutate(&input)
			if err := ValidateFeedbackEventInput(input); err == nil {
				t.Fatal("ValidateFeedbackEventInput() error = nil")
			}
		})
	}
}

func TestValidFeedbackEventType(t *testing.T) {
	for _, eventType := range []FeedbackEventType{
		FeedbackPersonalMemory,
		FeedbackWebKnowledge,
		FeedbackSocialLearning,
		FeedbackSocialReplyOutcome,
	} {
		if !validFeedbackEventType(eventType) {
			t.Fatalf("validFeedbackEventType(%q) = false", eventType)
		}
	}
}
