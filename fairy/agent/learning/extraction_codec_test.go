package learning

import (
	"encoding/json"
	"fairy/context/memory/extraction"
	"strings"
	"testing"
)

func TestBuildExtractInputPreservesCompleteTurnEvidence(t *testing.T) {
	userMessage := strings.Repeat("完整用户证据", 500) + "\n第二行\t末尾"
	assistantMessage := strings.Repeat("完整助手证据", 300) + "\r\n结束"
	batch := extraction.BatchInput{
		BatchID:        "batch-1",
		ConversationID: "conversation-1",
		CharacterID:    "character-1",
		Turns: []extraction.Turn{{
			TurnID:           "turn-1",
			UserMessage:      userMessage,
			AssistantMessage: assistantMessage,
		}},
	}

	items, err := buildExtractInput(batch)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("items length = %d, want 1", len(items))
	}
	var envelope struct {
		FairyContextData struct {
			Type  string                `json:"type"`
			Input extraction.BatchInput `json:"input"`
		} `json:"fairy_context_data"`
	}
	if err := json.Unmarshal([]byte(items[0].Content), &envelope); err != nil {
		t.Fatal(err)
	}
	got := envelope.FairyContextData.Input.Turns
	if len(got) != 1 || got[0].UserMessage != userMessage || got[0].AssistantMessage != assistantMessage {
		t.Fatalf("serialized turns do not preserve complete evidence: %#v", got)
	}
}

func TestParseMemoryMutationOutputRejectsMissingSourceTurnID(t *testing.T) {
	for _, raw := range []string{
		`{"mutations":[{"operation":"create","kind":"preference","scope":{"type":"global"},"content":"喜欢爵士乐","confidenceBasisPoints":9000}]}`,
		`{"mutations":[{"operation":"create","sourceTurnId":"   ","kind":"preference","scope":{"type":"global"},"content":"喜欢爵士乐","confidenceBasisPoints":9000}]}`,
	} {
		if _, err := parseMemoryMutationOutput(raw); err == nil {
			t.Fatalf("parseMemoryMutationOutput(%s) accepted missing source turn evidence", raw)
		}
	}
}
