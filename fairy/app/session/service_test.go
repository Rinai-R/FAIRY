package appsession

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"fairy/context/character"
	historyexpr "fairy/context/history/expression"
	history "fairy/context/history/transcript"
	"fairy/transport/session"
)

type fakeSecret struct{}

func (fakeSecret) DigestEndpointKey(session.EndpointKind, string) (string, error) {
	return "endpoint-digest", nil
}
func (fakeSecret) DigestPrincipal(session.PrincipalRef) (string, error) {
	return "principal-digest", nil
}

type fakeCharacters struct {
	catalog character.Catalog
	err     error
}

func (f fakeCharacters) ListCharacters() (character.Catalog, error) {
	return f.catalog, f.err
}

type fakeTranscript struct {
	mu         sync.Mutex
	bootstrap  history.ConversationBootstrap
	openErr    error
	page       history.MessagePage
	listErr    error
	openedWith string
	listedWith string
}

func (f *fakeTranscript) OpenOrCreateEndpointConversationContext(_ context.Context, characterID string, _ session.Binding, _ string) (history.ConversationBootstrap, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.openedWith = characterID
	if f.openErr != nil {
		return history.ConversationBootstrap{}, f.openErr
	}
	return f.bootstrap, nil
}

func (f *fakeTranscript) ListConversationMessagesBeforeContext(_ context.Context, conversationID string, _ uint64, _ int) (history.MessagePage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listedWith = conversationID
	if f.listErr != nil {
		return history.MessagePage{}, f.listErr
	}
	return f.page, nil
}

type fakeTurns struct {
	mu           sync.Mutex
	bound        map[string]session.Binding
	capabilities map[string]session.OutputCapabilities
	submitted    []TurnSubmission
	canceled     [][2]string
	delivered    []session.ExpressionDeliveryResult
	outcome      any
	submitErr    error
	cancelErr    error
	bindErr      error
	deliverErr   error
}

func newFakeTurns() *fakeTurns {
	return &fakeTurns{
		bound:        make(map[string]session.Binding),
		capabilities: make(map[string]session.OutputCapabilities),
		outcome:      session.Outcome{ConversationID: "conversation-1", TurnID: "turn-1", ResponseText: "ok"},
	}
}

func (f *fakeTurns) OutputCapabilities(conversationID string) session.OutputCapabilities {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.capabilities[conversationID]
}
func (f *fakeTurns) ReportExpressionDelivery(result session.ExpressionDeliveryResult) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.deliverErr != nil {
		return f.deliverErr
	}
	f.delivered = append(f.delivered, result)
	return nil
}
func (f *fakeTurns) BindOutputCapabilities(ownerID, conversationID string, capabilities session.OutputCapabilities) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	_ = ownerID
	f.capabilities[conversationID] = capabilities
	return nil
}
func (f *fakeTurns) UnbindOutputCapabilities(ownerID, conversationID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_ = ownerID
	delete(f.capabilities, conversationID)
}
func (f *fakeTurns) SubmitTurn(request TurnSubmission) (any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.submitted = append(f.submitted, request)
	if f.submitErr != nil {
		return nil, f.submitErr
	}
	return f.outcome, nil
}
func (f *fakeTurns) CancelTurn(conversationID, turnID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.canceled = append(f.canceled, [2]string{conversationID, turnID})
	return f.cancelErr
}
func (f *fakeTurns) BindInteraction(conversationID string, binding session.Binding) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.bindErr != nil {
		return f.bindErr
	}
	f.bound[conversationID] = binding
	return nil
}

type fakeInitiative struct {
	ambient     []session.AmbientObservation
	desktop     []session.DesktopObservation
	desktopOut  DesktopObservationResult
	participate session.ParticipationResponse
}

func (f *fakeInitiative) ObserveAmbient(_ string, observation session.AmbientObservation) error {
	f.ambient = append(f.ambient, observation)
	return nil
}
func (f *fakeInitiative) ObserveDesktop(_ string, observation session.DesktopObservation) (DesktopObservationResult, error) {
	f.desktop = append(f.desktop, observation)
	return f.desktopOut, nil
}
func (f *fakeInitiative) DecideParticipation(context.Context, string, session.ParticipationRequest) (session.ParticipationResponse, error) {
	return f.participate, nil
}

func testService(t *testing.T, turns *fakeTurns, transcript *fakeTranscript, initiative InitiativeRuntime) *Service {
	t.Helper()
	if turns == nil {
		turns = newFakeTurns()
	}
	if transcript == nil {
		transcript = &fakeTranscript{bootstrap: history.ConversationBootstrap{
			Conversation: history.ConversationRecord{ID: "conversation-1", CharacterID: "character-1"},
			Messages:     []history.MessageRecord{{ID: "m1", ConversationID: "conversation-1", TurnID: "turn-0", Sequence: 1, Role: "user", Content: "hi"}},
		}}
	}
	active := character.Record{CharacterID: "character-1", Name: "Fairy"}
	return New(Dependencies{
		Secret:     fakeSecret{},
		Characters: fakeCharacters{catalog: character.Catalog{Characters: []character.Record{active}, Active: &active}},
		Transcript: transcript,
		Turns:      turns,
		Initiative: initiative,
		SubscribeTurnEvents: func(conversationID string) (EventSubscription, error) {
			events := make(chan session.Event)
			failures := make(chan error)
			close(events)
			close(failures)
			return EventSubscription{Events: events, Failures: failures, Cancel: func() {}}, nil
		},
		SubscribeParticipation: func(conversationID string) (ParticipationSubscription, error) {
			events := make(chan session.ParticipationEvent)
			failures := make(chan error)
			close(events)
			close(failures)
			return ParticipationSubscription{Events: events, Failures: failures, Cancel: func() {}}, nil
		},
	})
}

func TestServiceOpenFailsClosedWithoutDependencies(t *testing.T) {
	_, err := New(Dependencies{}).Open(t.Context(), session.OpenRequest{
		Endpoint: session.EndpointDesktop, EndpointKey: "desk-1",
		Interaction: session.Context{Audience: session.AudienceSingle, Initiation: session.InitiationDirect, Presentation: session.PresentationEmbodied},
	})
	if !errors.Is(err, ErrSessionUnavailable) {
		t.Fatalf("Open() error = %v, want %v", err, ErrSessionUnavailable)
	}
}

func TestServiceOpenAndListMessagesShareConversationContract(t *testing.T) {
	transcript := &fakeTranscript{
		bootstrap: history.ConversationBootstrap{
			Conversation: history.ConversationRecord{ID: "conversation-1", CharacterID: "character-1"},
			Messages:     []history.MessageRecord{{ID: "m1", ConversationID: "conversation-1", TurnID: "t1", Sequence: 1, Role: "user", Content: "hello"}},
		},
		page: history.MessagePage{Messages: []history.MessageRecord{{
			ID: "m1", ConversationID: "conversation-1", TurnID: "t1", Sequence: 1, Role: "user", Content: "hello",
			Parts: []historyexpr.Part{{Kind: historyexpr.Utterance, Text: "hello", VisualState: "idle"}},
		}}},
	}
	turns := newFakeTurns()
	service := testService(t, turns, transcript, nil)
	opened, err := service.Open(t.Context(), session.OpenRequest{
		Endpoint: session.EndpointDesktop, EndpointKey: "desk-1",
		Interaction: session.Context{Audience: session.AudienceSingle, Initiation: session.InitiationDirect, Presentation: session.PresentationEmbodied},
	})
	if err != nil {
		t.Fatal(err)
	}
	if opened.ConversationID != "conversation-1" || opened.CharacterID != "character-1" || opened.MessageCount != 1 || opened.Endpoint != session.EndpointDesktop {
		t.Fatalf("opened = %#v", opened)
	}
	if _, ok := turns.bound["conversation-1"]; !ok {
		t.Fatal("Open did not bind interaction")
	}
	page, err := service.ListMessages(t.Context(), opened.ConversationID, 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 1 || page.Messages[0].Content != "hello" || page.Messages[0].Parts[0].Kind != session.ExpressionUtterance {
		t.Fatalf("page = %#v", page)
	}
}

func TestServiceSubmitCancelAndDeliveryUseTurnContract(t *testing.T) {
	turns := newFakeTurns()
	service := testService(t, turns, nil, nil)
	response, err := service.SubmitTurn(TurnSubmission{ConversationID: "conversation-1", Input: "ping"})
	if err != nil {
		t.Fatal(err)
	}
	if response.Outcome.TurnID != "turn-1" || response.Outcome.ConversationID != "conversation-1" {
		t.Fatalf("response = %#v", response)
	}
	if _, err := service.SubmitTurn(TurnSubmission{ConversationID: "conversation-1"}); err == nil || !strings.Contains(err.Error(), "input is required") {
		t.Fatalf("empty input error = %v", err)
	}
	if err := service.CancelTurn("conversation-1", "turn-1"); err != nil {
		t.Fatal(err)
	}
	result := session.ExpressionDeliveryResult{
		ConversationID: "conversation-1", TurnID: "turn-1", BeatID: "final-0", Status: session.ExpressionDeliverySucceeded,
	}
	if err := service.ReportExpressionDelivery(result); err != nil {
		t.Fatal(err)
	}
	if len(turns.submitted) != 1 || turns.canceled[0] != [2]string{"conversation-1", "turn-1"} || len(turns.delivered) != 1 {
		t.Fatalf("turns state submitted=%v canceled=%v delivered=%v", turns.submitted, turns.canceled, turns.delivered)
	}
}

func TestFacadeAndServiceShareTurnAndReceiptContracts(t *testing.T) {
	turns := newFakeTurns()
	service := testService(t, turns, nil, &fakeInitiative{
		desktopOut:  DesktopObservationResult{Action: "silent"},
		participate: session.ParticipationResponse{Action: "silent"},
	})
	facade := NewFacade(service)
	t.Cleanup(func() { _ = facade.Close() })
	opened, err := facade.OpenSession(t.Context(), session.OpenSessionRequest{
		Endpoint: session.EndpointDesktop, EndpointKey: "desk-1",
		Interaction:        session.Context{Audience: session.AudienceSingle, Initiation: session.InitiationDirect, Presentation: session.PresentationChat},
		OutputCapabilities: session.OutputCapabilities{Sticker: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	direct, err := service.Open(t.Context(), session.OpenRequest{
		Endpoint: session.EndpointDesktop, EndpointKey: "desk-1",
		Interaction: session.Context{Audience: session.AudienceSingle, Initiation: session.InitiationDirect, Presentation: session.PresentationChat},
	})
	if err != nil {
		t.Fatal(err)
	}
	if opened != direct {
		t.Fatalf("facade open %#v != service open %#v", opened, direct)
	}
	if !turns.OutputCapabilities(opened.ConversationID).Sticker {
		t.Fatal("facade did not bind sticker capability")
	}
	events, err := facade.Watch(t.Context(), opened.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	if events == nil {
		t.Fatal("watch events are required")
	}
	submit, err := facade.SubmitTurn(t.Context(), opened.ConversationID, session.SubmitTurnRequest{Input: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	serviceSubmit, err := service.SubmitTurn(TurnSubmission{ConversationID: opened.ConversationID, Input: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if submit != serviceSubmit {
		t.Fatalf("facade submit %#v != service submit %#v", submit, serviceSubmit)
	}
	receipt := session.ExpressionDeliveryResult{
		ConversationID: opened.ConversationID, TurnID: submit.Outcome.TurnID, BeatID: "final-0", Status: session.ExpressionDeliverySucceeded,
	}
	if err := facade.ReportExpressionDelivery(t.Context(), receipt); err != nil {
		t.Fatal(err)
	}
	if err := facade.ReportExpressionDelivery(t.Context(), session.ExpressionDeliveryResult{
		ConversationID: "other", TurnID: "turn-1", BeatID: "final-0", Status: session.ExpressionDeliverySucceeded,
	}); !errors.Is(err, ErrDeliveryUnavailable) {
		t.Fatalf("unwatched delivery error = %v", err)
	}
	if err := facade.CancelTurn(t.Context(), opened.ConversationID, submit.Outcome.TurnID); err != nil {
		t.Fatal(err)
	}
}

func TestFacadeRejectsDeliveryAfterClose(t *testing.T) {
	service := testService(t, nil, nil, nil)
	facade := NewFacade(service)
	opened, err := facade.OpenSession(t.Context(), session.OpenSessionRequest{
		Endpoint: session.EndpointDesktop, EndpointKey: "desk-1",
		Interaction: session.Context{Audience: session.AudienceSingle, Initiation: session.InitiationDirect, Presentation: session.PresentationChat},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := facade.Watch(t.Context(), opened.ConversationID); err != nil {
		t.Fatal(err)
	}
	_ = facade.Close()
	err = facade.ReportExpressionDelivery(t.Context(), session.ExpressionDeliveryResult{
		ConversationID: opened.ConversationID, TurnID: "turn-1", BeatID: "final-0", Status: session.ExpressionDeliverySucceeded,
	})
	if !errors.Is(err, ErrConnectionClosed) {
		t.Fatalf("delivery after close = %v", err)
	}
}
