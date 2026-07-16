package companion

import (
	"context"
	"errors"
	"fmt"
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
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"{\\\"chains\\\":[{\\\"visualState\\\":\\\"idle\\\",\\\"text\\\":\\\"我在。\\\"}]}\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
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

func TestCancelTurnCancelsActiveContext(t *testing.T) {
	root := t.TempDir()
	memoryStore, _ := seedCompanionRuntime(t, root)
	service := NewCompanionServiceWithRuntime(root, memoryStore, model.NewModelService(root))
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
