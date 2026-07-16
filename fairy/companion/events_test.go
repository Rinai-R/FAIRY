package companion

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"fairy/model"
)

func TestSubmitCompiledTurnEmitsHarnessLifecycle(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"{\\\"chains\\\":[{\\\"visualState\\\":\\\"idle\\\",\\\"text\\\":\\\"我在。\\\"}]}\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":11,\"completion_tokens\":4}}\n\n")
	}))
	t.Cleanup(server.Close)

	root := t.TempDir()
	writeModelConnectionWithEndpoint(t, root, "chat_completions", server.URL, "no_auth")
	memoryStore, characterID := seedCompanionRuntime(t, root)
	bootstrap, err := memoryStore.OpenOrCreateCharacterConversation(characterID)
	if err != nil {
		t.Fatalf("OpenOrCreateCharacterConversation() error = %v", err)
	}
	service := NewCompanionServiceWithRuntime(root, memoryStore, model.NewModelServiceWithTransport(root, model.HTTPTransport{Client: server.Client()}))
	var mu sync.Mutex
	var emitted []HarnessEvent
	AttachEventEmitter(service, func(event HarnessEvent) {
		mu.Lock()
		emitted = append(emitted, event)
		mu.Unlock()
	})

	outcome, err := service.SubmitCompiledTurn(SubmitCompiledTurnRequest{
		ConversationID:        bootstrap.Conversation.ID,
		Input:                 "你好",
		MaxOutputTokens:       160,
		AvailableVisualStates: []VisualState{{ID: "idle", Description: "idle 状态说明"}},
	})
	if err != nil {
		t.Fatalf("SubmitCompiledTurn() error = %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(emitted) < 5 {
		t.Fatalf("emitted = %#v", emitted)
	}
	states := make([]TurnState, 0, len(emitted))
	for _, event := range emitted {
		if event.TurnID != outcome.TurnID || event.ConversationID != outcome.ConversationID {
			t.Fatalf("event ids mismatch: %#v vs %#v", event, outcome)
		}
		states = append(states, event.State)
	}
	if states[0] != TurnStateInterpreting || states[1] != TurnStatePlanning || states[2] != TurnStateResponding {
		t.Fatalf("prefix states = %#v", states[:3])
	}
	if states[len(states)-1] != TurnStateCompleted {
		t.Fatalf("terminal state = %#v", states)
	}
	foundReply := false
	for _, event := range emitted {
		if payload, ok := event.Payload.(replyChainPayload); ok && payload.Type == "reply_chain" {
			foundReply = true
			if payload.Text != "我在。" {
				t.Fatalf("reply payload = %#v", payload)
			}
		}
	}
	if !foundReply {
		t.Fatal("missing reply_chain event")
	}
}