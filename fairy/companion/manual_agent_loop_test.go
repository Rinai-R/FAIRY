package companion

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"fairy/character"
	"fairy/memory"
	"fairy/model"
)

type recordingModelTransport struct {
	inner                            model.Transport
	mu                               sync.Mutex
	events                           []model.StreamEvent
	requestHasInternalDecisionPrompt bool
}

func (t *recordingModelTransport) Execute(ctx context.Context, draft model.RequestDraft, bearerKey string, onEvent func(model.StreamEvent)) error {
	t.mu.Lock()
	t.requestHasInternalDecisionPrompt = strings.Contains(draft.BodyJSON, "五项角色决策") && strings.Contains(draft.BodyJSON, "replyIntent")
	t.mu.Unlock()
	return t.inner.Execute(ctx, draft, bearerKey, func(event model.StreamEvent) {
		t.mu.Lock()
		t.events = append(t.events, event)
		t.mu.Unlock()
		onEvent(event)
	})
}

func (t *recordingModelTransport) takeEvents() ([]model.StreamEvent, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	events := append([]model.StreamEvent(nil), t.events...)
	t.events = nil
	return events, t.requestHasInternalDecisionPrompt
}

func replySchemaDiagnostics(events []model.StreamEvent, requestHasInternalDecisionPrompt bool) string {
	var payload struct {
		Chains []any `json:"chains"`
	}
	draft := collectText(events)
	if err := json.Unmarshal([]byte(draft), &payload); err != nil {
		return fmt.Sprintf("requestHasInternalDecisionPrompt=%t jsonError=%q outputRunes=%d", requestHasInternalDecisionPrompt, err.Error(), utf8.RuneCountInString(draft))
	}
	return fmt.Sprintf("requestHasInternalDecisionPrompt=%t chains=%d", requestHasInternalDecisionPrompt, len(payload.Chains))
}

func TestManualCharacterDecisionAgentLoop(t *testing.T) {
	root := os.Getenv("FAIRY_MANUAL_CONFIG_ROOT")
	if root == "" {
		t.Skip("FAIRY_MANUAL_CONFIG_ROOT is not set")
	}
	catalog, err := character.NewStore(root).List()
	if err != nil {
		t.Fatalf("ListCharacters() error = %v", err)
	}
	if catalog.Active == nil {
		t.Fatal("active character is required")
	}
	memoryStore, err := memory.OpenOrCreate(filepath.Join(root, memory.RelativePath))
	if err != nil {
		t.Fatalf("OpenOrCreate(memory) error = %v", err)
	}
	bootstrap, err := memoryStore.OpenOrCreateCharacterConversation(catalog.Active.CharacterID)
	if err != nil {
		t.Fatalf("OpenOrCreateCharacterConversation() error = %v", err)
	}
	transport := &recordingModelTransport{inner: model.SDKTransport{}}
	service := NewCompanionServiceWithRuntime(root, memoryStore, model.NewModelServiceWithTransport(root, transport, nil), nil)
	var mu sync.Mutex
	var events []HarnessEvent
	AttachEventEmitter(service, func(event HarnessEvent) {
		mu.Lock()
		events = append(events, event)
		mu.Unlock()
	})

	inputs := []string{
		"你好，今天见到你了。",
		"别给建议，只陪我聊会儿。今天有点累。",
		"你会怎么陪我度过这个安静的晚上？不要列计划，就像平时聊天那样回我。",
	}
	for index, input := range inputs {
		mu.Lock()
		start := len(events)
		mu.Unlock()
		outcome, err := service.SubmitTurn(SubmitTurnRequest{
			ConversationID: bootstrap.Conversation.ID,
			Input:          input,
			SpeechEnabled:  false,
		})
		if err != nil {
			transportEvents, requestHasInternalDecisionPrompt := transport.takeEvents()
			t.Fatalf("SubmitTurn(%d) error = %v diagnostics=%s", index+1, err, replySchemaDiagnostics(transportEvents, requestHasInternalDecisionPrompt))
		}
		_, _ = transport.takeEvents()
		if strings.TrimSpace(outcome.ResponseText) == "" || len(outcome.Chains) == 0 {
			t.Fatalf("SubmitTurn(%d) outcome = %#v", index+1, outcome)
		}
		mu.Lock()
		turnEvents := append([]HarnessEvent(nil), events[start:]...)
		mu.Unlock()
		if len(turnEvents) < 6 || turnEvents[0].State != TurnStateInterpreting || turnEvents[1].State != TurnStateGathering || turnEvents[2].State != TurnStatePlanning || turnEvents[3].State != TurnStateResponding || turnEvents[len(turnEvents)-1].State != TurnStateCompleted {
			t.Fatalf("SubmitTurn(%d) states = %#v", index+1, turnEvents)
		}
		wire, err := json.Marshal(struct {
			Events  []HarnessEvent `json:"events"`
			Outcome TurnOutcome    `json:"outcome"`
		}{Events: turnEvents, Outcome: outcome})
		if err != nil {
			t.Fatalf("json.Marshal(turn %d) error = %v", index+1, err)
		}
		for _, forbidden := range []string{`"decision"`, `"stance"`, `"replyIntent"`, `"tone"`, `"relationshipSignal"`, `"replyMode"`} {
			if strings.Contains(string(wire), forbidden) {
				t.Fatalf("SubmitTurn(%d) wire leaked %s", index+1, forbidden)
			}
		}
		t.Logf("turn %d visual=%s usage=%d response=%q", index+1, outcome.VisualState, len(turnEvents), outcome.ResponseText)
	}

	reloaded, err := memoryStore.LoadConversation(bootstrap.Conversation.ID)
	if err != nil {
		t.Fatalf("LoadConversation() error = %v", err)
	}
	for _, message := range reloaded.Messages {
		if message.Role != "assistant" {
			continue
		}
		for _, forbidden := range []string{`"decision"`, `"stance"`, `"replyIntent"`, `"relationshipSignal"`, `"replyMode"`} {
			if strings.Contains(message.Content, forbidden) {
				t.Fatalf("persisted assistant message leaked %s: %q", forbidden, message.Content)
			}
		}
	}
}
