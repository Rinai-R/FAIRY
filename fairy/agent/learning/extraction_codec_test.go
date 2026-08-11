package learning

import (
	"encoding/json"
	"fairy/context/memory/extraction"
	"fairy/context/memory/personal"
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

	candidates := []discoveryCandidate{{Space: discoveryPersonal, Statement: "用户提供了持久偏好", Query: "用户偏好", EvidenceRefs: []string{"turn-1"}}}
	items, aliases, err := buildExtractInput(batch, candidates)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("items length = %d, want 1", len(items))
	}
	if len(aliases.byAlias) != 0 {
		t.Fatalf("unexpected aliases: %#v", aliases.byAlias)
	}
	var envelope struct {
		FairyContextData struct {
			Type  string                `json:"type"`
			Input extractionPromptInput `json:"input"`
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
		`{"mutations":[{"operation":"ADD","kind":"preference","scope":{"type":"global"},"content":"喜欢爵士乐","confidenceBasisPoints":9000}]}`,
		`{"mutations":[{"operation":"ADD","sourceTurnId":"   ","kind":"preference","scope":{"type":"global"},"content":"喜欢爵士乐","confidenceBasisPoints":9000}]}`,
	} {
		if _, err := parseMemoryMutationOutput(raw, memoryAliasSet{}, map[string]struct{}{"turn-1": {}}); err == nil {
			t.Fatalf("parseMemoryMutationOutput(%s) accepted missing source turn evidence", raw)
		}
	}
}

func TestExtractInputUsesRequestLocalAliasesAndParserResolvesThem(t *testing.T) {
	batch := extraction.BatchInput{Turns: []extraction.Turn{{TurnID: "turn-1"}}, ExistingMemories: []personal.Retrieved{{ID: "database-secret-id", Kind: "preference", Scope: personal.Scope{Type: "global"}, Content: "喜欢爵士乐", ConfidenceBasisPoints: 8000}}}
	items, aliases, err := buildExtractInput(batch, []discoveryCandidate{{Space: discoveryPersonal, Statement: "用户不再喜欢爵士乐", Query: "音乐偏好", EvidenceRefs: []string{"turn-1"}}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(items[0].Content, "database-secret-id") || !strings.Contains(items[0].Content, `"memoryId":"m0"`) {
		t.Fatalf("prompt did not isolate database id: %s", items[0].Content)
	}
	output, err := parseMemoryMutationOutput(`{"mutations":[{"operation":"DELETE","sourceTurnId":"turn-1","memoryId":"m0"}]}`, aliases, map[string]struct{}{"turn-1": {}})
	if err != nil {
		t.Fatal(err)
	}
	if got := output.Mutations[0].MemoryID; got != "database-secret-id" {
		t.Fatalf("resolved memory id = %q", got)
	}
}
