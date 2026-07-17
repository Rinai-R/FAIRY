package companion

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"fairy/memory"
	"fairy/model"
)

func TestParseMemoryMutationOutputAcceptsEmptyMutations(t *testing.T) {
	output, err := ParseMemoryMutationOutput(`{"mutations":[]}`)
	if err != nil {
		t.Fatalf("ParseMemoryMutationOutput() error = %v", err)
	}
	if len(output.Mutations) != 0 {
		t.Fatalf("mutations = %#v", output.Mutations)
	}
}

func TestParseMemoryMutationOutputRejectsUnknownFields(t *testing.T) {
	_, err := ParseMemoryMutationOutput(`{"mutations":[],"extra":true}`)
	if err == nil {
		t.Fatal("ParseMemoryMutationOutput() error = nil, want unknown field error")
	}
}

func TestBuildExtractInputWrapsBatchAsContextData(t *testing.T) {
	items, err := BuildExtractInput(memory.ExtractionBatchInput{
		BatchID:        "batch-1",
		ConversationID: "conversation-1",
		CharacterID:    "character-1",
		Turns: []memory.ExtractionTurn{{
			TurnID:           "turn-1",
			UserMessage:      "你好",
			AssistantMessage: "我在",
		}},
	})
	if err != nil {
		t.Fatalf("BuildExtractInput() error = %v", err)
	}
	if len(items) != 1 || items[0].Type != model.PromptItemContextData {
		t.Fatalf("items = %#v", items)
	}
	if !strings.Contains(items[0].Content, `"type":"extraction_batch"`) || !strings.Contains(items[0].Content, "batch-1") {
		t.Fatalf("content = %s", items[0].Content)
	}
}

func TestCompanionServiceBackgroundExtractionCommitsCreateMutation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("request body is not JSON: %v", err)
		}
		if strings.Contains(body.Messages[0].Content, "existing personal memories") {
			for _, message := range body.Messages {
				if strings.Contains(message.Content, `"decision"`) || strings.Contains(message.Content, "replyIntent") {
					t.Fatalf("internal decision labels leaked into extraction input: %#v", body.Messages)
				}
			}
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"{\\\"mutations\\\":[{\\\"operation\\\":\\\"create\\\",\\\"kind\\\":\\\"preference\\\",\\\"scope\\\":{\\\"type\\\":\\\"global\\\"},\\\"content\\\":\\\"用户喜欢 Rust\\\",\\\"confidenceBasisPoints\\\":9000}]}\"}}]}\n\n")
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
			return
		}
		writeChatTextDelta(w, testRespondEnvelope(testReplyChain{VisualState: "idle", Text: "好，我记住了。"}))
		writeChatStop(w)
	}))
	t.Cleanup(server.Close)

	root := t.TempDir()
	writeModelConnectionWithEndpoint(t, root, "chat_completions", server.URL, "no_auth")
	memoryStore, characterID := seedCompanionRuntime(t, root)
	bootstrap, err := memoryStore.OpenOrCreateCharacterConversation(characterID)
	if err != nil {
		t.Fatalf("OpenOrCreateCharacterConversation() error = %v", err)
	}
	service := NewCompanionServiceWithRuntime(root, memoryStore, model.NewModelServiceWithTransport(root, model.SDKTransport{HTTPClient: server.Client()}, nil), nil)
	for index := 0; index < int(extractionThreshold); index++ {
		outcome, err := service.SubmitCompiledTurn(SubmitCompiledTurnRequest{
			ConversationID:        bootstrap.Conversation.ID,
			Input:                 fmt.Sprintf("我喜欢 Rust 第%d次", index+1),
			SpeechEnabled:         false,
			MaxOutputTokens:       160,
			AvailableVisualStates: []VisualState{{ID: "idle", Description: "idle 状态说明"}},
		})
		if err != nil {
			t.Fatalf("SubmitCompiledTurn(%d) error = %v", index, err)
		}
		if outcome.ResponseText != "好，我记住了。" {
			t.Fatalf("outcome(%d) = %#v", index, outcome)
		}
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		catalog, err := memoryStore.PersonalMemoryCatalog(characterID)
		if err != nil {
			t.Fatalf("PersonalMemoryCatalog() error = %v", err)
		}
		if len(catalog.Global) >= 1 && catalog.Global[0].Content == "用户喜欢 Rust" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("timed out waiting for threshold extraction to commit personal memory")
}

func TestScheduleBackgroundExtractionWaitsBelowThreshold(t *testing.T) {
	root := t.TempDir()
	writeModelConnectionWithEndpoint(t, root, "chat_completions", "http://127.0.0.1:1", "no_auth")
	memoryStore, characterID := seedCompanionRuntime(t, root)
	bootstrap, err := memoryStore.OpenOrCreateCharacterConversation(characterID)
	if err != nil {
		t.Fatalf("OpenOrCreateCharacterConversation() error = %v", err)
	}
	turn, err := memoryStore.BeginTurn(bootstrap.Conversation.ID, "一次待抽取")
	if err != nil {
		t.Fatalf("BeginTurn() error = %v", err)
	}
	if _, err := memoryStore.CompleteTurn(bootstrap.Conversation.ID, turn.ID, "好。"); err != nil {
		t.Fatalf("CompleteTurn() error = %v", err)
	}
	service := NewCompanionServiceWithRuntime(root, memoryStore, model.NewModelServiceWithTransport(root, model.SDKTransport{}, nil), nil)
	service.scheduleBackgroundExtraction(bootstrap.Conversation.ID)
	time.Sleep(50 * time.Millisecond)
	pending, err := memoryStore.PendingExtractionTurnCount(bootstrap.Conversation.ID)
	if err != nil {
		t.Fatalf("PendingExtractionTurnCount() error = %v", err)
	}
	if pending != 1 {
		t.Fatalf("pending = %d, want 1 while idle timer has not fired", pending)
	}
	service.extractionMu.Lock()
	_, scheduled := service.extractionIdle[bootstrap.Conversation.ID]
	service.extractionMu.Unlock()
	if !scheduled {
		t.Fatal("expected idle extraction timer to be scheduled below threshold")
	}
}
