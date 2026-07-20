//go:build sqlite_legacy

package companion

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"fairy/model"
)

func TestSubmitCompiledTurnRejectsConcurrentTurn(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-started:
		default:
			close(started)
		}
		<-release
		writeChatTextDelta(w, testRespondEnvelope(testReplyChain{VisualState: "idle", Text: "我在。"}))
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
	req := SubmitCompiledTurnRequest{
		ConversationID:        bootstrap.Conversation.ID,
		Input:                 "第一轮",
		MaxOutputTokens:       160,
		AvailableVisualStates: []VisualState{{ID: "idle", Description: "idle 状态说明"}},
	}

	var firstErr error
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, firstErr = service.SubmitCompiledTurn(req)
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("first turn did not reach model")
	}

	_, err = service.SubmitCompiledTurn(SubmitCompiledTurnRequest{
		ConversationID:        bootstrap.Conversation.ID,
		Input:                 "第二轮",
		MaxOutputTokens:       160,
		AvailableVisualStates: []VisualState{{ID: "idle", Description: "idle 状态说明"}},
	})
	if !errors.Is(err, ErrTurnInProgress) {
		t.Fatalf("concurrent turn error = %v, want %v", err, ErrTurnInProgress)
	}
	close(release)
	wg.Wait()
	if firstErr != nil {
		t.Fatalf("first turn error = %v", firstErr)
	}
}

func TestSubmitCompiledTurnStaysPlanningWhileModelRuns(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		writeChatTextDelta(w, testRespondEnvelope(testReplyChain{VisualState: "idle", Text: "我在。"}))
		writeChatStop(w)
	}))
	t.Cleanup(server.Close)
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})

	root := t.TempDir()
	writeModelConnectionWithEndpoint(t, root, "chat_completions", server.URL, "no_auth")
	memoryStore, characterID := seedCompanionRuntime(t, root)
	bootstrap, err := memoryStore.OpenOrCreateCharacterConversation(characterID)
	if err != nil {
		t.Fatalf("OpenOrCreateCharacterConversation() error = %v", err)
	}
	service := NewCompanionServiceWithRuntime(root, memoryStore, model.NewModelServiceWithTransport(root, model.SDKTransport{HTTPClient: server.Client()}, nil), nil)
	var mu sync.Mutex
	var emitted []HarnessEvent
	AttachEventEmitter(service, func(event HarnessEvent) {
		mu.Lock()
		emitted = append(emitted, event)
		mu.Unlock()
	})

	result := make(chan error, 1)
	go func() {
		_, err := service.SubmitCompiledTurn(SubmitCompiledTurnRequest{
			ConversationID:        bootstrap.Conversation.ID,
			Input:                 "你好",
			MaxOutputTokens:       160,
			AvailableVisualStates: visualStates("idle"),
		})
		result <- err
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("turn did not reach model")
	}
	mu.Lock()
	statesWhileRunning := make([]TurnState, 0, len(emitted))
	for _, event := range emitted {
		statesWhileRunning = append(statesWhileRunning, event.State)
	}
	mu.Unlock()
	if len(statesWhileRunning) != 3 || statesWhileRunning[0] != TurnStateInterpreting || statesWhileRunning[1] != TurnStateGathering || statesWhileRunning[2] != TurnStatePlanning {
		t.Fatalf("states while model runs = %#v, want interpreting/gathering/planning", statesWhileRunning)
	}
	close(release)
	if err := <-result; err != nil {
		t.Fatalf("SubmitCompiledTurn() error = %v", err)
	}
}

func TestCancelTurnDuringPlanningDoesNotRespondOrPersistAssistant(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(server.Close)
	t.Cleanup(func() { close(release) })

	root := t.TempDir()
	writeModelConnectionWithEndpoint(t, root, "chat_completions", server.URL, "no_auth")
	memoryStore, characterID := seedCompanionRuntime(t, root)
	bootstrap, err := memoryStore.OpenOrCreateCharacterConversation(characterID)
	if err != nil {
		t.Fatalf("OpenOrCreateCharacterConversation() error = %v", err)
	}
	service := NewCompanionServiceWithRuntime(root, memoryStore, model.NewModelServiceWithTransport(root, model.SDKTransport{HTTPClient: server.Client()}, nil), nil)
	var mu sync.Mutex
	var emitted []HarnessEvent
	AttachEventEmitter(service, func(event HarnessEvent) {
		mu.Lock()
		emitted = append(emitted, event)
		mu.Unlock()
	})

	result := make(chan error, 1)
	go func() {
		_, err := service.SubmitCompiledTurn(SubmitCompiledTurnRequest{
			ConversationID:        bootstrap.Conversation.ID,
			Input:                 "先停一下",
			MaxOutputTokens:       160,
			AvailableVisualStates: visualStates("idle"),
		})
		result <- err
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("turn did not reach model")
	}
	mu.Lock()
	if len(emitted) != 3 {
		mu.Unlock()
		t.Fatalf("events before cancel = %#v, want interpreting/gathering/planning", emitted)
	}
	turnID := emitted[0].TurnID
	mu.Unlock()
	if err := service.CancelTurn(bootstrap.Conversation.ID, turnID); err != nil {
		t.Fatalf("CancelTurn() error = %v", err)
	}
	if err := <-result; !errors.Is(err, ErrTurnInterrupted) {
		t.Fatalf("SubmitCompiledTurn() error = %v, want %v", err, ErrTurnInterrupted)
	}

	mu.Lock()
	states := make([]TurnState, 0, len(emitted))
	for _, event := range emitted {
		states = append(states, event.State)
		if _, ok := event.Payload.(replyChainPayload); ok {
			mu.Unlock()
			t.Fatalf("cancelled turn emitted reply chain: %#v", event)
		}
	}
	mu.Unlock()
	if len(states) != 4 || states[0] != TurnStateInterpreting || states[1] != TurnStateGathering || states[2] != TurnStatePlanning || states[3] != TurnStateInterrupted {
		t.Fatalf("states = %#v, want interpreting/gathering/planning/interrupted", states)
	}
	reloaded, err := memoryStore.LoadConversation(bootstrap.Conversation.ID)
	if err != nil {
		t.Fatalf("LoadConversation() error = %v", err)
	}
	if len(reloaded.Messages) != 1 || reloaded.Messages[0].Role != "user" {
		t.Fatalf("messages = %#v, want only persisted user", reloaded.Messages)
	}
}

func TestCancelTurnCancelsActiveContext(t *testing.T) {
	root := t.TempDir()
	memoryStore, _ := seedCompanionRuntime(t, root)
	service := NewCompanionServiceWithRuntime(root, memoryStore, model.NewModelService(root, nil), nil)
	ctx, err := service.reserveTurn("conversation-1")
	if err != nil {
		t.Fatalf("reserveTurn() error = %v", err)
	}
	service.bindTurn("conversation-1", "turn-1")
	if err := service.CancelTurn("conversation-1", "turn-1"); err != nil {
		t.Fatalf("CancelTurn() error = %v", err)
	}
	select {
	case <-ctx.Done():
		if !errors.Is(ctx.Err(), context.Canceled) {
			t.Fatalf("ctx.Err() = %v", ctx.Err())
		}
	case <-time.After(time.Second):
		t.Fatal("active turn context was not cancelled")
	}
}

func TestMapModelCancelError(t *testing.T) {
	if !errors.Is(mapModelCancelError(context.Canceled), ErrTurnInterrupted) {
		t.Fatal("context.Canceled must map to TURN_INTERRUPTED")
	}
	if mapModelCancelError(errors.New("other")) == nil {
		t.Fatal("other errors must pass through")
	}
}
