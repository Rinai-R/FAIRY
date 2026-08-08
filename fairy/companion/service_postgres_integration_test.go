//go:build integration

package companion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"fairy/character"
	"fairy/config"
	"fairy/coredb"
	"fairy/memory"
	"fairy/model"
	"fairy/reply"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"fairy/initiative"
	"fairy/session"
)

type companionInitiativeTestTurnStarter struct {
	service *CompanionService
}

func (a companionInitiativeTestTurnStarter) CancelTurnBeforeDelivery(conversationID string) {
	a.service.CancelTurnBeforeDelivery(conversationID)
}

func (a companionInitiativeTestTurnStarter) SubmitTurn(initiative.TurnRequest) (initiative.TurnOutcome, error) {
	return initiative.TurnOutcome{}, errors.New("unexpected ambient turn")
}

func (a companionInitiativeTestTurnStarter) ScheduleDesktopInitiation(conversationID string, evidenceIDs []string, observation session.DesktopObservation) error {
	return a.service.ScheduleDesktopInitiation(DesktopInitiationRequest{
		ConversationID: conversationID, ObservationEvidenceIDs: evidenceIDs,
	}, observation)
}

var (
	errCompletePersistence  = errors.New("complete persistence unavailable")
	errInterruptPersistence = errors.New("interrupt persistence unavailable")
)

const structuredCompactSummary = `{"currentGoal":"继续当前对话","userConstraints":"遵守用户明确约束","relationship":"保持当前关系边界","keyFacts":[],"completedWork":"已整理较早对话","openQuestions":"等待用户继续","nextSteps":"结合最近原始对话回复","sourceRefs":[]}`

type companionIntegrationModel struct {
	chains []ReplyChain
}

func (m companionIntegrationModel) ExecuteRequestContext(context.Context, model.CompiledPromptRequest) ([]model.StreamEvent, error) {
	payload := "{\"chains\":["
	for index, chain := range m.chains {
		if index > 0 {
			payload += ","
		}
		payload += fmt.Sprintf("{\"visualState\":%q,\"text\":%q}", chain.VisualState, chain.Text)
	}
	payload += "]}"
	return []model.StreamEvent{
		{Type: "text_delta", Data: payload},
		{Type: "usage", Usage: &model.Usage{PromptTokens: 17, CompletionTokens: 9}},
	}, nil
}

func (companionIntegrationModel) ExecutePrompt(model.PromptLane, string, uint32, []model.PromptItem, string) ([]model.StreamEvent, error) {
	return []model.StreamEvent{{Type: "text_delta", Data: "摘要"}, {Type: "usage", Usage: &model.Usage{PromptTokens: 2, CompletionTokens: 1}}}, nil
}

type retryingReplyIntegrationModel struct {
	mu       sync.Mutex
	requests []model.CompiledPromptRequest
}

type retryingPublicShapeIntegrationModel struct {
	mu       sync.Mutex
	requests []model.CompiledPromptRequest
}

func (m *retryingPublicShapeIntegrationModel) ExecuteRequestContext(_ context.Context, request model.CompiledPromptRequest) ([]model.StreamEvent, error) {
	m.mu.Lock()
	m.requests = append(m.requests, request)
	call := len(m.requests)
	m.mu.Unlock()
	if call == 1 {
		return companionIntegrationModel{chains: []ReplyChain{
			{VisualState: "idle", Text: "第一拍"},
			{VisualState: "idle", Text: "违规第二拍"},
		}}.ExecuteRequestContext(context.Background(), request)
	}
	return companionIntegrationModel{chains: []ReplyChain{{VisualState: "idle", Text: "重试后的即时接话。"}}}.ExecuteRequestContext(context.Background(), request)
}

func (m *retryingPublicShapeIntegrationModel) ExecutePrompt(model.PromptLane, string, uint32, []model.PromptItem, string) ([]model.StreamEvent, error) {
	return []model.StreamEvent{{Type: "text_delta", Data: "摘要"}}, nil
}

func (m *retryingPublicShapeIntegrationModel) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.requests)
}

func (m *retryingReplyIntegrationModel) ExecuteRequestContext(_ context.Context, request model.CompiledPromptRequest) ([]model.StreamEvent, error) {
	m.mu.Lock()
	m.requests = append(m.requests, request)
	call := len(m.requests)
	m.mu.Unlock()
	if call == 1 {
		return []model.StreamEvent{
			{Type: "text_delta", Data: "not strict reply json"},
			{Type: "usage", Usage: &model.Usage{PromptTokens: 11, CompletionTokens: 3}},
		}, nil
	}
	return companionIntegrationModel{chains: []ReplyChain{{VisualState: "idle", Text: "第二次严格返回。"}}}.ExecuteRequestContext(context.Background(), request)
}

func (*retryingReplyIntegrationModel) ExecutePrompt(model.PromptLane, string, uint32, []model.PromptItem, string) ([]model.StreamEvent, error) {
	return []model.StreamEvent{{Type: "text_delta", Data: "摘要"}}, nil
}

func (m *retryingReplyIntegrationModel) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.requests)
}

type capturingIntegrationModel struct {
	mu      sync.Mutex
	request model.CompiledPromptRequest
}

func (m *capturingIntegrationModel) ExecuteRequestContext(_ context.Context, request model.CompiledPromptRequest) ([]model.StreamEvent, error) {
	m.mu.Lock()
	m.request = request
	m.mu.Unlock()
	if request.Shape.Lane == model.PromptLaneCompact {
		return []model.StreamEvent{{Type: "text_delta", Data: structuredCompactSummary}, {Type: "usage", Usage: &model.Usage{PromptTokens: 2, CompletionTokens: 1}}}, nil
	}
	return companionIntegrationModel{chains: []ReplyChain{{VisualState: "idle", Text: "群聊回复。"}}}.ExecuteRequestContext(context.Background(), request)
}

func (m *capturingIntegrationModel) ExecutePrompt(model.PromptLane, string, uint32, []model.PromptItem, string) ([]model.StreamEvent, error) {
	return []model.StreamEvent{{Type: "text_delta", Data: structuredCompactSummary}, {Type: "usage", Usage: &model.Usage{PromptTokens: 2, CompletionTokens: 1}}}, nil
}

type companionIntegrationCharacterLookup struct {
	record character.Record
}

func (c companionIntegrationCharacterLookup) Lookup(characterID string) (character.Record, bool, error) {
	return c.record, c.record.CharacterID == characterID, nil
}

type countingPromptContextStore struct {
	base      PromptContextStore
	loadCalls atomic.Int64
}

func (s *countingPromptContextStore) LoadConversationPrompt(conversationID string) (memory.ConversationPromptContext, error) {
	s.loadCalls.Add(1)
	return s.base.LoadConversationPrompt(conversationID)
}

type countingCharacterLookup struct {
	record      character.Record
	lookupCalls atomic.Int64
}

func (c *countingCharacterLookup) Lookup(characterID string) (character.Record, bool, error) {
	c.lookupCalls.Add(1)
	return c.record, c.record.CharacterID == characterID, nil
}

type countingOwnerIdentity struct {
	lookups atomic.Int64
}

func (o *countingOwnerIdentity) IsOwner(string, string) (bool, error) {
	o.lookups.Add(1)
	return true, nil
}

type preparationCallSnapshot struct {
	promptContextLoads int64
	characterLookups   int64
	ownerLookups       int64
}

type preparationCountingModel struct {
	promptContext *countingPromptContextStore
	characters    *countingCharacterLookup
	owner         *countingOwnerIdentity
	mu            sync.Mutex
	first         *preparationCallSnapshot
}

func (m *preparationCountingModel) ExecuteRequestContext(ctx context.Context, request model.CompiledPromptRequest) ([]model.StreamEvent, error) {
	m.mu.Lock()
	if m.first == nil {
		snapshot := preparationCallSnapshot{
			promptContextLoads: m.promptContext.loadCalls.Load(),
			characterLookups:   m.characters.lookupCalls.Load(),
		}
		if m.owner != nil {
			snapshot.ownerLookups = m.owner.lookups.Load()
		}
		m.first = &snapshot
	}
	m.mu.Unlock()
	if request.Shape.Lane == model.PromptLaneCompact {
		return []model.StreamEvent{
			{Type: "text_delta", Data: structuredCompactSummary},
			{Type: "usage", Usage: &model.Usage{PromptTokens: 8, CompletionTokens: 4}},
		}, nil
	}
	return companionIntegrationModel{chains: []ReplyChain{{VisualState: "idle", Text: "收到。"}}}.ExecuteRequestContext(ctx, request)
}

func (*preparationCountingModel) ExecutePrompt(model.PromptLane, string, uint32, []model.PromptItem, string) ([]model.StreamEvent, error) {
	return nil, errors.New("unexpected ExecutePrompt")
}

func (m *preparationCountingModel) firstSnapshot(t *testing.T) preparationCallSnapshot {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.first == nil {
		t.Fatal("model boundary did not capture preparation calls")
	}
	return *m.first
}

type companionIntegrationProfile struct{}

func (companionIntegrationProfile) Current() (*config.ProfileSnapshot, error) { return nil, nil }

type rejectingGroupProfile struct{}

func (rejectingGroupProfile) Current() (*config.ProfileSnapshot, error) {
	return nil, errors.New("group surface must not read private profile")
}

type companionIntegrationConfig struct{}

func (companionIntegrationConfig) ModelConnection() (config.ModelConnection, error) {
	return config.ModelConnection{
		Protocol:            "chat_completions",
		Endpoint:            "http://model.invalid",
		Model:               "deepseek-v4-flash",
		ContextWindowTokens: 1048576,
		AuthMode:            "no_auth",
	}, nil
}

func (companionIntegrationConfig) WebSearchSettings() (config.WebSearchSettings, error) {
	return config.WebSearchSettings{SchemaVersion: 1, Enabled: false}, nil
}

type cacheEnabledCompanionIntegrationConfig struct{ companionIntegrationConfig }

func (cacheEnabledCompanionIntegrationConfig) ModelConnection() (config.ModelConnection, error) {
	connection, err := (companionIntegrationConfig{}).ModelConnection()
	if err != nil {
		return config.ModelConnection{}, err
	}
	connection.Protocol = "responses"
	connection.Capabilities.PromptCacheKey = true
	return connection, nil
}

type groupWebIntegrationConfig struct{ companionIntegrationConfig }

func (groupWebIntegrationConfig) WebSearchSettings() (config.WebSearchSettings, error) {
	return config.WebSearchSettings{SchemaVersion: 1, Enabled: true}, nil
}

type visionCompanionIntegrationConfig struct{ companionIntegrationConfig }

func (visionCompanionIntegrationConfig) ModelConnection() (config.ModelConnection, error) {
	connection, err := (companionIntegrationConfig{}).ModelConnection()
	connection.Capabilities.VisionInput = true
	return connection, err
}

type desktopToolIntegrationCoordinator struct {
	available  bool
	calls      int
	completed  func()
	release    <-chan struct{}
	dispatched chan<- DesktopToolExecution
}

func (coordinator *desktopToolIntegrationCoordinator) Available(string) bool {
	return coordinator.available
}
func (coordinator *desktopToolIntegrationCoordinator) CancelTurn(context.Context, string, string) error {
	return nil
}
func (coordinator *desktopToolIntegrationCoordinator) Begin(_ context.Context, request DesktopToolRequest, completed func()) (DesktopToolExecution, error) {
	coordinator.calls++
	coordinator.completed = completed
	return DesktopToolExecution{
		ID: "execution-1", ConversationID: request.ConversationID, TurnID: request.TurnID,
		CallID: request.CallID, DeadlineUnixMS: request.Deadline.UnixMilli(),
	}, nil
}
func (coordinator *desktopToolIntegrationCoordinator) DispatchExecution(_ context.Context, execution DesktopToolExecution) error {
	if coordinator.dispatched != nil {
		coordinator.dispatched <- execution
	}
	if coordinator.release == nil {
		coordinator.completed()
		return nil
	}
	go func() {
		<-coordinator.release
		coordinator.completed()
	}()
	return nil
}
func (coordinator *desktopToolIntegrationCoordinator) Result(context.Context, string) (DesktopToolEvidence, error) {
	return DesktopToolEvidence{
		ExecutionID: "execution-1", MediaType: "image/png", Width: 1, Height: 1,
		DataURL: "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=",
	}, nil
}

type desktopToolIntegrationModel struct {
	requests []model.CompiledPromptRequest
}

type emptyMemoryToolIntegrationModel struct {
	requests []model.CompiledPromptRequest
}

func (provider *emptyMemoryToolIntegrationModel) ExecuteRequestContext(_ context.Context, request model.CompiledPromptRequest) ([]model.StreamEvent, error) {
	provider.requests = append(provider.requests, request)
	if len(provider.requests) <= privateModelToolBudget {
		callID := fmt.Sprintf("memory-call-%d", len(provider.requests))
		return []model.StreamEvent{{Type: "function_calls", FunctionCalls: []model.FunctionCall{{
			CallID: callID, Name: toolMemorySearch, Arguments: `{"query":"你好"}`,
		}}}}, nil
	}
	return companionIntegrationModel{chains: []ReplyChain{{VisualState: "idle", Text: "你好呀。"}}}.ExecuteRequestContext(context.Background(), request)
}

func (*emptyMemoryToolIntegrationModel) ExecutePrompt(model.PromptLane, string, uint32, []model.PromptItem, string) ([]model.StreamEvent, error) {
	return nil, errors.New("unexpected ExecutePrompt")
}

func (provider *desktopToolIntegrationModel) ExecuteRequestContext(_ context.Context, request model.CompiledPromptRequest) ([]model.StreamEvent, error) {
	provider.requests = append(provider.requests, request)
	if len(provider.requests) == 1 {
		return []model.StreamEvent{{Type: "function_calls", FunctionCalls: []model.FunctionCall{{CallID: "desktop-call-1", Name: toolDesktopObserve, Arguments: `{}`}}}}, nil
	}
	return companionIntegrationModel{chains: []ReplyChain{{VisualState: "idle", Text: "我看到了，我们继续。"}}}.ExecuteRequestContext(context.Background(), request)
}

func (*desktopToolIntegrationModel) ExecutePrompt(model.PromptLane, string, uint32, []model.PromptItem, string) ([]model.StreamEvent, error) {
	return nil, errors.New("unexpected ExecutePrompt")
}

type groupWebIntegrationModel struct {
	mu       sync.Mutex
	requests []model.CompiledPromptRequest
}

func (m *groupWebIntegrationModel) ExecuteRequestContext(_ context.Context, request model.CompiledPromptRequest) ([]model.StreamEvent, error) {
	m.mu.Lock()
	m.requests = append(m.requests, request)
	call := len(m.requests)
	m.mu.Unlock()
	if call == 1 {
		return []model.StreamEvent{{Type: "function_calls", FunctionCalls: []model.FunctionCall{{
			CallID: "web-1", Name: toolWebSearch, Arguments: `{"query":"公开新闻"}`,
		}}}}, nil
	}
	return companionIntegrationModel{chains: []ReplyChain{{VisualState: "idle", Text: "公开消息。"}}}.ExecuteRequestContext(context.Background(), request)
}

func (*groupWebIntegrationModel) ExecutePrompt(model.PromptLane, string, uint32, []model.PromptItem, string) ([]model.StreamEvent, error) {
	return []model.StreamEvent{{Type: "text_delta", Data: "摘要"}}, nil
}

type groupWebSearchStub struct{}

func (groupWebSearchStub) Search(context.Context, string, int) ([]WebSearchHit, error) {
	return []WebSearchHit{{Title: "公开新闻标题", URL: "https://example.com/news", Snippet: "公开摘要"}}, nil
}

func (groupWebSearchStub) Close() error { return nil }

type terminalFailureTurnStore struct {
	base         TurnStore
	completeErr  error
	interruptErr error
}

func mustBindDesktopInteraction(t *testing.T, service *CompanionService, conversationID string) {
	t.Helper()
	err := service.BindInteraction(conversationID, session.Binding{
		Endpoint: session.EndpointDesktop,
		Facts: session.Facts{
			Audience: session.AudienceSingle, Initiation: session.InitiationDirect,
			Presentation: session.PresentationEmbodied,
		},
	})
	if err != nil {
		t.Fatalf("BindInteraction: %v", err)
	}
}

func (m terminalFailureTurnStore) BeginTurn(conversationID, userMessage string) (memory.PersistedTurn, error) {
	return m.base.BeginTurn(conversationID, userMessage)
}

func (m terminalFailureTurnStore) BeginInitiationTurn(conversationID string, evidenceIDs []string) (memory.PersistedTurn, error) {
	return m.base.BeginInitiationTurn(conversationID, evidenceIDs)
}

func (m terminalFailureTurnStore) CompleteExpressionTurnForPolicy(string, string, string, []memory.ExpressionPart, bool) (memory.MessageRecord, error) {
	return memory.MessageRecord{}, m.completeErr
}

func (m terminalFailureTurnStore) InterruptExpressionTurn(string, string, string, []memory.ExpressionPart) (*memory.MessageRecord, error) {
	return nil, m.interruptErr
}

func (m terminalFailureTurnStore) FailTurn(conversationID, turnID, code, message string, retryable bool) error {
	return m.base.FailTurn(conversationID, turnID, code, message, retryable)
}

func TestPostgresDirectTurnPreparationCallBounds(t *testing.T) {
	store, _, cleanup := openCompanionIntegrationStore(t)
	defer cleanup()
	const characterID = "character-preparation-counts"
	bootstrap, err := store.OpenOrCreateCharacterConversation(characterID)
	if err != nil {
		t.Fatalf("OpenOrCreateCharacterConversation: %v", err)
	}
	ports := memoryPortsFromStore(store)
	promptContext := &countingPromptContextStore{base: ports.turn.promptContext}
	ports.turn.promptContext = promptContext
	characters := &countingCharacterLookup{record: companionIntegrationCharacter(characterID)}
	owner := &countingOwnerIdentity{}
	provider := &preparationCountingModel{promptContext: promptContext, characters: characters, owner: owner}
	service := newCompanionServiceWithPorts("", ports, provider, nil)
	t.Cleanup(func() { _ = service.Close() })
	AttachCharacterLookup(service, characters)
	AttachProfileSource(service, companionIntegrationProfile{})
	AttachConfigSource(service, companionIntegrationConfig{})
	AttachOwnerIdentityStore(service, owner)
	binding := session.Binding{Endpoint: session.EndpointIM, Facts: session.Facts{
		Audience: session.AudienceSingle, Initiation: session.InitiationDirect,
		Presentation: session.PresentationChat, PrincipalNamespace: "qq.onebot",
		PrincipalDigest: strings.Repeat("a", 64),
	}}
	if err := service.BindInteraction(bootstrap.Conversation.ID, binding); err != nil {
		t.Fatalf("BindInteraction: %v", err)
	}
	if _, err := service.SubmitTurn(SubmitTurnRequest{
		ConversationID: bootstrap.Conversation.ID,
		Input:          "测试准备阶段读取次数",
	}); err != nil {
		t.Fatalf("SubmitTurn: %v", err)
	}
	want := preparationCallSnapshot{promptContextLoads: 2, characterLookups: 2, ownerLookups: 1}
	if got := provider.firstSnapshot(t); got != want {
		t.Fatalf("direct pre-model preparation calls = %#v, want %#v", got, want)
	}
}

func TestPostgresCompactionPreparationCallBounds(t *testing.T) {
	store, _, cleanup := openCompanionIntegrationStore(t)
	defer cleanup()
	const characterID = "character-compaction-counts"
	bootstrap, err := store.OpenOrCreateCharacterConversation(characterID)
	if err != nil {
		t.Fatalf("OpenOrCreateCharacterConversation: %v", err)
	}
	seed, err := store.BeginTurn(bootstrap.Conversation.ID, "需要压缩的历史消息")
	if err != nil {
		t.Fatalf("BeginTurn: %v", err)
	}
	if _, err := store.CompleteTurn(bootstrap.Conversation.ID, seed.ID, "历史回复"); err != nil {
		t.Fatalf("CompleteTurn: %v", err)
	}
	ports := memoryPortsFromStore(store)
	promptContext := &countingPromptContextStore{base: ports.turn.promptContext}
	ports.turn.promptContext = promptContext
	characters := &countingCharacterLookup{record: companionIntegrationCharacter(characterID)}
	provider := &preparationCountingModel{promptContext: promptContext, characters: characters}
	service := newCompanionServiceWithPorts("", ports, provider, nil)
	t.Cleanup(func() { _ = service.Close() })
	AttachCharacterLookup(service, characters)
	AttachProfileSource(service, companionIntegrationProfile{})
	AttachConfigSource(service, companionIntegrationConfig{})
	mustBindDesktopInteraction(t, service, bootstrap.Conversation.ID)
	if _, err := service.CompactConversation(bootstrap.Conversation.ID); err != nil {
		t.Fatalf("CompactConversation: %v", err)
	}
	want := preparationCallSnapshot{promptContextLoads: 1, characterLookups: 1}
	if got := provider.firstSnapshot(t); got != want {
		t.Fatalf("compaction pre-model preparation calls = %#v, want %#v", got, want)
	}
}

func TestPostgresCompanionMultiBeatCompletesWithPacing(t *testing.T) {
	store, _, cleanup := openCompanionIntegrationStore(t)
	defer cleanup()
	bootstrap, err := store.OpenOrCreateCharacterConversation("character-paced")
	if err != nil {
		t.Fatalf("OpenOrCreateCharacterConversation: %v", err)
	}
	service := newCompanionIntegrationService(store, "character-paced", companionIntegrationModel{chains: []ReplyChain{
		{VisualState: "idle", Text: "第一拍。"},
		{VisualState: "happy", Text: "第二拍"},
	}})
	mustBindDesktopInteraction(t, service, bootstrap.Conversation.ID)
	var mu sync.Mutex
	var events []session.Event
	AttachEventEmitter(service, func(event session.Event) {
		mu.Lock()
		events = append(events, event)
		mu.Unlock()
	})

	outcome, err := service.SubmitCompiledTurn(SubmitCompiledTurnRequest{
		ConversationID:        bootstrap.Conversation.ID,
		Input:                 "请分两拍告诉我",
		MaxOutputTokens:       160,
		AvailableVisualStates: []VisualState{{ID: "idle", Description: "idle"}, {ID: "happy", Description: "happy"}},
	})
	if err != nil {
		t.Fatalf("SubmitCompiledTurn: %v", err)
	}
	if outcome.ResponseText != "第一拍。\n第二拍" {
		t.Fatalf("ResponseText = %q", outcome.ResponseText)
	}
	beats := finalBeatEvents(events)
	if len(beats) != 2 || beats[0].ChainIndex != 0 || beats[1].ChainIndex != 1 {
		t.Fatalf("final beats = %#v", beats)
	}
	if beats[0].PaceWaitMS != 0 || beats[0].PublishedPrefixCount != 1 || beats[1].PaceWaitMS <= 0 || beats[1].PublishedPrefixCount != 2 {
		t.Fatalf("pacing fields = %#v", beats)
	}
	if terminalEventCount(events, turnStateCompleted) != 1 || terminalEventCount(events, turnStateInterrupted) != 0 {
		t.Fatalf("terminal events = %#v", events)
	}
	reloaded, err := store.LoadConversation(bootstrap.Conversation.ID)
	if err != nil {
		t.Fatalf("LoadConversation: %v", err)
	}
	if len(reloaded.Messages) != 2 || reloaded.Messages[1].Content != outcome.ResponseText {
		t.Fatalf("messages = %#v", reloaded.Messages)
	}
	ledger, err := store.ListTurnRuntimeEvents(outcome.ConversationID, outcome.TurnID)
	if err != nil {
		t.Fatalf("ListTurnRuntimeEvents: %v", err)
	}
	if !hasRuntimeLedgerType(ledger, runtimeLedgerEventBeatDelivery) {
		t.Fatalf("ledger missing beat_delivery: %#v", ledger)
	}
}

func TestPostgresDesktopToolResumesSameTurnWithFullMultimodalRequest(t *testing.T) {
	store, pool, cleanup := openCompanionIntegrationStore(t)
	defer cleanup()
	bootstrap, err := store.OpenOrCreateCharacterConversation("character-desktop-tool")
	if err != nil {
		t.Fatal(err)
	}
	provider := &desktopToolIntegrationModel{}
	release := make(chan struct{})
	dispatched := make(chan DesktopToolExecution, 1)
	coordinator := &desktopToolIntegrationCoordinator{available: true, release: release, dispatched: dispatched}
	service := newCompanionIntegrationService(store, "character-desktop-tool", provider)
	AttachConfigSource(service, visionCompanionIntegrationConfig{})
	AttachDesktopToolCoordinator(service, coordinator)
	mustBindDesktopInteraction(t, service, bootstrap.Conversation.ID)
	type submitResult struct {
		outcome TurnOutcome
		err     error
	}
	submitted := make(chan submitResult, 1)
	go func() {
		outcome, submitErr := service.SubmitCompiledTurn(SubmitCompiledTurnRequest{
			ConversationID: bootstrap.Conversation.ID, Input: "看看我屏幕上的内容",
			MaxOutputTokens: 160, AvailableVisualStates: []VisualState{{ID: "idle", Description: "idle"}},
		})
		submitted <- submitResult{outcome: outcome, err: submitErr}
	}()
	execution := <-dispatched
	if execution.TurnID == "" || execution.CallID != "desktop-call-1" {
		t.Fatalf("dispatched execution = %#v", execution)
	}
	select {
	case early := <-submitted:
		t.Fatalf("turn returned before desktop completion: outcome=%#v err=%v", early.outcome, early.err)
	default:
	}
	close(release)
	submit := <-submitted
	outcome, err := submit.outcome, submit.err
	if err != nil {
		t.Fatal(err)
	}
	if coordinator.calls != 1 || len(provider.requests) != 2 {
		t.Fatalf("tool calls=%d model requests=%d", coordinator.calls, len(provider.requests))
	}
	firstTools := provider.requests[0].Tools
	if len(firstTools) == 0 || firstTools[len(firstTools)-1].Name != toolDesktopObserve {
		t.Fatalf("first request tools = %#v", firstTools)
	}
	second := provider.requests[1]
	if second.PreviousResponseID != "" {
		t.Fatalf("post-tool request reused previous response %q", second.PreviousResponseID)
	}
	var toolCall, toolResult, image bool
	for _, item := range second.Input {
		switch item.Type {
		case model.PromptItemToolCall:
			toolCall = item.ToolCallID == "desktop-call-1" && item.ToolName == toolDesktopObserve
		case model.PromptItemToolResult:
			toolResult = item.ToolCallID == "desktop-call-1"
			if item.Parts != nil {
				for _, part := range *item.Parts {
					image = image || part.Type == model.PromptContentImage
				}
			}
		}
	}
	if !toolCall || !toolResult || !image {
		t.Fatalf("post-tool input missing correlated multimodal result: %#v", second.Input)
	}
	reloaded, err := store.LoadConversation(bootstrap.Conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.Messages) != 2 || reloaded.Messages[1].TurnID != outcome.TurnID {
		t.Fatalf("same-turn transcript = %#v", reloaded.Messages)
	}
	ledger, err := store.ListTurnRuntimeEvents(outcome.ConversationID, outcome.TurnID)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range ledger {
		if strings.Contains(event.MetadataJSON, "data:image/") || strings.Contains(event.MetadataJSON, "iVBOR") {
			t.Fatalf("runtime ledger persisted capture content: %s", event.MetadataJSON)
		}
	}
	assertPostgresDoesNotContain(t, pool, "iVBORw0KGgoAAAANSUhEUgAAAAEAAAAB")
}

func TestPostgresEmptyMemoryToolResultsProgressToFinalReply(t *testing.T) {
	store, _, cleanup := openCompanionIntegrationStore(t)
	defer cleanup()
	bootstrap, err := store.OpenOrCreateCharacterConversation("character-empty-memory-tool")
	if err != nil {
		t.Fatal(err)
	}
	provider := &emptyMemoryToolIntegrationModel{}
	service := newCompanionIntegrationService(store, "character-empty-memory-tool", provider)
	mustBindDesktopInteraction(t, service, bootstrap.Conversation.ID)

	outcome, err := service.SubmitCompiledTurn(SubmitCompiledTurnRequest{
		ConversationID:        bootstrap.Conversation.ID,
		Input:                 "你好",
		MaxOutputTokens:       160,
		AvailableVisualStates: []VisualState{{ID: "idle", Description: "idle"}},
	})
	if err != nil {
		t.Fatalf("SubmitCompiledTurn: %v", err)
	}
	if outcome.ResponseText != "你好呀。" || len(provider.requests) != privateModelToolBudget+1 {
		t.Fatalf("outcome=%#v model requests=%d", outcome, len(provider.requests))
	}
	if len(provider.requests[0].Tools) == 0 || len(provider.requests[1].Tools) == 0 || len(provider.requests[2].Tools) != 0 {
		t.Fatalf("tool availability by request = %d/%d/%d", len(provider.requests[0].Tools), len(provider.requests[1].Tools), len(provider.requests[2].Tools))
	}
	for requestIndex, request := range provider.requests[1:] {
		wantPairs := requestIndex + 1
		calls := 0
		results := 0
		for _, item := range request.Input {
			switch item.Type {
			case model.PromptItemToolCall:
				calls++
			case model.PromptItemToolResult:
				results++
				if item.Parts == nil || len(*item.Parts) != 1 || !strings.Contains((*item.Parts)[0].Text, `"empty":true`) {
					t.Fatalf("request %d tool result = %#v", requestIndex+2, item)
				}
			}
		}
		if calls != wantPairs || results != wantPairs {
			t.Fatalf("request %d call/result pairs = %d/%d, want %d", requestIndex+2, calls, results, wantPairs)
		}
	}
}

func TestPostgresDesktopVisionInitiationCreatesZeroMessageTurn(t *testing.T) {
	store, _, cleanup := openCompanionIntegrationStore(t)
	defer cleanup()
	bootstrap, err := store.OpenOrCreateCharacterConversation("character-proactive-desktop-tool")
	if err != nil {
		t.Fatal(err)
	}
	provider := &desktopToolIntegrationModel{}
	coordinator := &desktopToolIntegrationCoordinator{available: true}
	service := newCompanionIntegrationService(store, "character-proactive-desktop-tool", provider)
	AttachConfigSource(service, visionCompanionIntegrationConfig{})
	AttachDesktopToolCoordinator(service, coordinator)
	mustBindDesktopInteraction(t, service, bootstrap.Conversation.ID)
	outcome, err := service.SubmitDesktopVisionInitiation(DesktopVisionInitiationRequest{ConversationID: bootstrap.Conversation.ID})
	if err != nil {
		t.Fatal(err)
	}
	reloaded, err := store.LoadConversation(bootstrap.Conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.Messages) != 1 || reloaded.Messages[0].Role != "assistant" || reloaded.Messages[0].TurnID != outcome.TurnID {
		t.Fatalf("zero-message transcript = %#v", reloaded.Messages)
	}
	if coordinator.calls != 1 || len(provider.requests) != 2 {
		t.Fatalf("proactive tool calls=%d model requests=%d", coordinator.calls, len(provider.requests))
	}
	if !strings.Contains(provider.requests[0].Shape.Instructions, "private zero-message turn") {
		t.Fatalf("proactive instructions = %q", provider.requests[0].Shape.Instructions)
	}
}

func TestPostgresDesktopInitiationUsesContextWithoutFabricatedUserMessage(t *testing.T) {
	store, pool, cleanup := openCompanionIntegrationStore(t)
	defer cleanup()
	bootstrap, err := store.OpenOrCreateCharacterConversation("character-initiation")
	if err != nil {
		t.Fatal(err)
	}
	provider := &capturingIntegrationModel{}
	service := newCompanionIntegrationService(store, "character-initiation", provider)
	mustBindDesktopInteraction(t, service, bootstrap.Conversation.ID)

	now := time.Now()
	observation := session.DesktopObservation{
		ObservationID: "obs-1", TimestampUnixMS: now.UnixMilli(), Trigger: session.DesktopTriggerLifecycle,
		Activity: session.DesktopActivityIdle, Lifecycle: session.DesktopLifecycleReturned, Privacy: session.DesktopPrivacyNormal,
	}
	evidence := initiative.NewEvidenceRegistry()
	AttachDesktopEvidenceValidator(service, evidence)
	if err := evidence.Accept(observation, now); err != nil {
		t.Fatal(err)
	}
	outcome, err := service.SubmitDesktopInitiation(DesktopInitiationRequest{
		ConversationID: bootstrap.Conversation.ID, ObservationEvidenceIDs: []string{"obs-1"},
	}, observation)
	if err != nil {
		t.Fatal(err)
	}
	reloaded, err := store.LoadConversation(bootstrap.Conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.Messages) != 1 || reloaded.Messages[0].Role != "assistant" || reloaded.Messages[0].Content != outcome.ResponseText {
		t.Fatalf("messages = %#v", reloaded.Messages)
	}
	var origin, extractionState string
	if err := pool.QueryRow(context.Background(), "SELECT origin, extraction_state FROM conversation_turns WHERE id = $1", outcome.TurnID).Scan(&origin, &extractionState); err != nil {
		t.Fatal(err)
	}
	if origin != "desktop_initiation" || extractionState != "ineligible" {
		t.Fatalf("origin=%q extraction=%q", origin, extractionState)
	}

	provider.mu.Lock()
	request := provider.request
	provider.mu.Unlock()
	foundInitiation := false
	for _, item := range request.Input {
		if item.Type == model.PromptItemUserMessage {
			t.Fatalf("fabricated user message in model input: %#v", request.Input)
		}
		if item.Type == model.PromptItemContextData && strings.Contains(item.Content, `"contextType":"desktop_initiation"`) {
			foundInitiation = true
			if strings.Contains(item.Content, "obs-1") {
				t.Fatalf("evidence ID leaked to model input: %s", item.Content)
			}
		}
	}
	if !foundInitiation {
		t.Fatalf("desktop initiation context missing: %#v", request.Input)
	}
}

func TestPostgresDesktopObservationDirectFlowSchedulesInitiation(t *testing.T) {
	store, pool, cleanup := openCompanionIntegrationStore(t)
	defer cleanup()
	bootstrap, err := store.OpenOrCreateCharacterConversation("character-observation-graph")
	if err != nil {
		t.Fatal(err)
	}
	service := newCompanionIntegrationService(store, "character-observation-graph", &capturingIntegrationModel{})
	mustBindDesktopInteraction(t, service, bootstrap.Conversation.ID)
	completed := make(chan session.Event, 1)
	AttachEventEmitter(service, func(event session.Event) {
		if event.State == string(turnStateCompleted) {
			select {
			case completed <- event:
			default:
			}
		}
	})

	now := time.Now()
	initiativeService := initiative.NewService(t.Context(), initiative.ServiceOptions{
		Turns: companionInitiativeTestTurnStarter{service: service}, Interactions: service,
	})
	t.Cleanup(initiativeService.Close)
	AttachDesktopEvidenceValidator(service, initiativeService.EvidenceValidator())
	plan, err := initiativeService.ObserveDesktop(bootstrap.Conversation.ID, session.DesktopObservation{
		ObservationID: "obs-graph", TimestampUnixMS: now.UnixMilli(), Trigger: session.DesktopTriggerLifecycle,
		Activity: session.DesktopActivityIdle, Lifecycle: session.DesktopLifecycleReturned, Privacy: session.DesktopPrivacyNormal,
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Action != initiative.DesktopActionInitiate {
		t.Fatalf("plan action = %q", plan.Action)
	}
	foundInitiate := false
	for _, diagnostic := range plan.Diagnostics {
		if diagnostic.Node == "initiate" && diagnostic.Status == "completed" {
			foundInitiate = true
		}
	}
	if !foundInitiate {
		t.Fatalf("initiate diagnostic missing: %#v", plan.Diagnostics)
	}

	var event session.Event
	select {
	case event = <-completed:
	case <-time.After(5 * time.Second):
		t.Fatal("desktop initiation did not complete")
	}
	deadline := time.Now().Add(5 * time.Second)
	for service.ActiveBackgroundJobs() != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if service.ActiveBackgroundJobs() != 0 {
		t.Fatal("desktop initiation background job did not finish")
	}
	var origin string
	if err := pool.QueryRow(context.Background(), "SELECT origin FROM conversation_turns WHERE id = $1", event.TurnID).Scan(&origin); err != nil {
		t.Fatal(err)
	}
	if origin != "desktop_initiation" {
		t.Fatalf("origin = %q", origin)
	}
	reloaded, err := store.LoadConversation(bootstrap.Conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.Messages) != 1 || reloaded.Messages[0].Role != "assistant" {
		t.Fatalf("messages = %#v", reloaded.Messages)
	}
	ledger, err := store.ListTurnRuntimeEvents(bootstrap.Conversation.ID, event.TurnID)
	if err != nil {
		t.Fatal(err)
	}
	transitions := make(map[string]int)
	completedTerminals := 0
	for _, record := range ledger {
		if record.EventType == "node" {
			t.Fatalf("legacy graph node event remained in direct turn ledger: %#v", record)
		}
		if record.EventType == runtimeLedgerEventTransition && record.State != nil {
			transitions[*record.State]++
		}
		if record.EventType == runtimeLedgerEventTerminal && record.State != nil && *record.State == string(turnStateCompleted) {
			completedTerminals++
		}
	}
	for _, state := range []turnState{turnStateInterpreting, turnStateGathering, turnStatePlanning, turnStateResponding} {
		if transitions[string(state)] != 1 {
			t.Fatalf("transition %q count = %d, all = %#v", state, transitions[string(state)], transitions)
		}
	}
	if completedTerminals != 1 {
		t.Fatalf("completed terminal count = %d", completedTerminals)
	}
}

func TestPostgresCompanionRetriesOneInvalidReplyWithoutDuplicatingTurn(t *testing.T) {
	store, _, cleanup := openCompanionIntegrationStore(t)
	defer cleanup()
	bootstrap, err := store.OpenOrCreateCharacterConversation("character-retry")
	if err != nil {
		t.Fatalf("OpenOrCreateCharacterConversation: %v", err)
	}
	provider := &retryingReplyIntegrationModel{}
	service := newCompanionIntegrationService(store, "character-retry", provider)
	mustBindDesktopInteraction(t, service, bootstrap.Conversation.ID)
	var mu sync.Mutex
	var events []session.Event
	AttachEventEmitter(service, func(event session.Event) {
		mu.Lock()
		events = append(events, event)
		mu.Unlock()
	})

	outcome, err := service.SubmitCompiledTurn(SubmitCompiledTurnRequest{
		ConversationID:        bootstrap.Conversation.ID,
		Input:                 "测试严格回复重试",
		MaxOutputTokens:       160,
		AvailableVisualStates: []VisualState{{ID: "idle", Description: "idle"}},
	})
	if err != nil {
		t.Fatalf("SubmitCompiledTurn: %v", err)
	}
	if provider.callCount() != 2 || outcome.ResponseText != "第二次严格返回。" {
		t.Fatalf("calls=%d outcome=%#v", provider.callCount(), outcome)
	}
	reloaded, err := store.LoadConversation(bootstrap.Conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.Messages) != 2 || reloaded.Messages[0].Role != "user" || reloaded.Messages[1].Role != "assistant" {
		t.Fatalf("messages=%#v", reloaded.Messages)
	}
	mu.Lock()
	defer mu.Unlock()
	if terminalEventCount(events, turnStateCompleted) != 1 || terminalEventCount(events, turnStateFailed) != 0 {
		t.Fatalf("terminal events=%#v", events)
	}
}

func TestPostgresCompanionRegeneratesInvalidPublicReplyShape(t *testing.T) {
	store, _, cleanup := openCompanionIntegrationStore(t)
	defer cleanup()
	bootstrap, err := store.OpenOrCreateCharacterConversation("character-public-shape-retry")
	if err != nil {
		t.Fatalf("OpenOrCreateCharacterConversation: %v", err)
	}
	provider := &retryingPublicShapeIntegrationModel{}
	service := newCompanionIntegrationService(store, "character-public-shape-retry", provider)
	if err := service.BindInteraction(bootstrap.Conversation.ID, publicAmbientBinding()); err != nil {
		t.Fatalf("BindInteraction: %v", err)
	}
	intent := &ReplyIntent{ReplyAct: "接话", Tone: "自然", RelationshipSignal: "群友", ReplyMode: "brief", Focus: "当前消息", ExpressionQuery: "自然接话"}
	outcome, err := service.SubmitCompiledTurn(SubmitCompiledTurnRequest{
		ConversationID:        bootstrap.Conversation.ID,
		Input:                 "刚才那件事有点焦虑",
		ReplyIntent:           intent,
		MaxOutputTokens:       160,
		AvailableVisualStates: []VisualState{{ID: "idle", Description: "idle"}},
	})
	if err != nil {
		t.Fatalf("SubmitCompiledTurn: %v", err)
	}
	if provider.callCount() != 2 || outcome.ResponseText != "重试后的即时接话。" {
		t.Fatalf("calls=%d outcome=%#v", provider.callCount(), outcome)
	}
	reloaded, err := store.LoadConversation(bootstrap.Conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.Messages) != 2 || reloaded.Messages[1].Content != "重试后的即时接话。" {
		t.Fatalf("messages=%#v", reloaded.Messages)
	}
}

func TestPostgresCompanionCancelAfterFirstBeatPersistsPrefix(t *testing.T) {
	store, pool, cleanup := openCompanionIntegrationStore(t)
	defer cleanup()
	bootstrap, err := store.OpenOrCreateCharacterConversation("character-cancel")
	if err != nil {
		t.Fatalf("OpenOrCreateCharacterConversation: %v", err)
	}
	service := newCompanionIntegrationService(store, "character-cancel", companionIntegrationModel{chains: []ReplyChain{
		{VisualState: "idle", Text: "已说出的第一拍。"},
		{VisualState: "idle", Text: "不应发布的第二拍"},
		{VisualState: "idle", Text: "不应发布的第三拍"},
	}})
	mustBindDesktopInteraction(t, service, bootstrap.Conversation.ID)
	var mu sync.Mutex
	var events []session.Event
	var cancelErr error
	AttachEventEmitter(service, func(event session.Event) {
		mu.Lock()
		events = append(events, event)
		mu.Unlock()
		payload := decodeEventPayload[beatReadyPayload](t, event.Payload)
		if payload.Type == "beat.ready" && payload.Kind == reply.BeatKindFinal && payload.ChainIndex == 0 {
			cancelErr = service.CancelTurn(event.ConversationID, event.TurnID)
		}
	})

	_, submitErr := service.SubmitCompiledTurn(SubmitCompiledTurnRequest{
		ConversationID:        bootstrap.Conversation.ID,
		Input:                 "说到一半就停",
		MaxOutputTokens:       160,
		AvailableVisualStates: []VisualState{{ID: "idle", Description: "idle"}},
	})
	if !errors.Is(submitErr, ErrTurnInterrupted) || cancelErr != nil {
		t.Fatalf("SubmitCompiledTurn = %v, CancelTurn = %v", submitErr, cancelErr)
	}
	beats := finalBeatEvents(events)
	if len(beats) != 1 || beats[0].ChainIndex != 0 {
		t.Fatalf("final beats = %#v", beats)
	}
	if terminalEventCount(events, turnStateInterrupted) != 1 || terminalEventCount(events, turnStateCompleted) != 0 {
		t.Fatalf("terminal events = %#v", events)
	}
	reloaded, err := store.LoadConversation(bootstrap.Conversation.ID)
	if err != nil {
		t.Fatalf("LoadConversation: %v", err)
	}
	if len(reloaded.Messages) != 2 || reloaded.Messages[1].Content != "已说出的第一拍。" {
		t.Fatalf("messages = %#v", reloaded.Messages)
	}
	var status, extractionState string
	if err := pool.QueryRow(context.Background(), "SELECT status, extraction_state FROM conversation_turns WHERE id = $1", events[0].TurnID).Scan(&status, &extractionState); err != nil {
		t.Fatalf("query turn: %v", err)
	}
	if status != "interrupted" || extractionState != "ineligible" {
		t.Fatalf("turn = (%q, %q)", status, extractionState)
	}
}

func TestPostgresCompanionTerminalPersistenceFailureEmitsFailed(t *testing.T) {
	store, _, cleanup := openCompanionIntegrationStore(t)
	defer cleanup()
	bootstrap, err := store.OpenOrCreateCharacterConversation("character-terminal-failure")
	if err != nil {
		t.Fatalf("OpenOrCreateCharacterConversation: %v", err)
	}
	ports := memoryPortsFromStore(store)
	ports.turn.turns = terminalFailureTurnStore{base: store, completeErr: errCompletePersistence}
	service := newCompanionIntegrationServiceWithPorts(ports, "character-terminal-failure", companionIntegrationModel{chains: []ReplyChain{
		{VisualState: "idle", Text: "已发布但无法保存"},
	}})
	mustBindDesktopInteraction(t, service, bootstrap.Conversation.ID)
	var events []session.Event
	AttachEventEmitter(service, func(event session.Event) { events = append(events, event) })
	_, submitErr := service.SubmitCompiledTurn(SubmitCompiledTurnRequest{
		ConversationID:        bootstrap.Conversation.ID,
		Input:                 "测试持久化错误",
		MaxOutputTokens:       160,
		AvailableVisualStates: []VisualState{{ID: "idle", Description: "idle"}},
	})
	if !errors.Is(submitErr, errCompletePersistence) {
		t.Fatalf("SubmitCompiledTurn = %v, want complete persistence error", submitErr)
	}
	if terminalEventCount(events, turnStateFailed) != 1 || terminalEventCount(events, turnStateCompleted) != 0 {
		t.Fatalf("terminal events = %#v", events)
	}
	failed := lastFailedEvent(events)
	if failed == nil || failed.Code != "TURN_TERMINAL_PERSISTENCE_FAILED" {
		t.Fatalf("failed event = %#v", failed)
	}
}

func TestPostgresCompanionInterruptPersistenceFailureEmitsFailed(t *testing.T) {
	store, _, cleanup := openCompanionIntegrationStore(t)
	defer cleanup()
	bootstrap, err := store.OpenOrCreateCharacterConversation("character-interrupt-failure")
	if err != nil {
		t.Fatalf("OpenOrCreateCharacterConversation: %v", err)
	}
	ports := memoryPortsFromStore(store)
	ports.turn.turns = terminalFailureTurnStore{base: store, interruptErr: errInterruptPersistence}
	service := newCompanionIntegrationServiceWithPorts(ports, "character-interrupt-failure", companionIntegrationModel{chains: []ReplyChain{
		{VisualState: "idle", Text: "第一拍"},
		{VisualState: "idle", Text: "第二拍"},
	}})
	mustBindDesktopInteraction(t, service, bootstrap.Conversation.ID)
	var events []session.Event
	AttachEventEmitter(service, func(event session.Event) {
		events = append(events, event)
		payload := decodeEventPayload[beatReadyPayload](t, event.Payload)
		if payload.Type == "beat.ready" && payload.ChainIndex == 0 {
			_ = service.CancelTurn(event.ConversationID, event.TurnID)
		}
	})
	_, submitErr := service.SubmitCompiledTurn(SubmitCompiledTurnRequest{
		ConversationID:        bootstrap.Conversation.ID,
		Input:                 "测试中断持久化错误",
		MaxOutputTokens:       160,
		AvailableVisualStates: []VisualState{{ID: "idle", Description: "idle"}},
	})
	if !errors.Is(submitErr, errInterruptPersistence) || !errors.Is(submitErr, ErrTurnInterrupted) {
		t.Fatalf("SubmitCompiledTurn = %v, want interrupt and persistence errors", submitErr)
	}
	if terminalEventCount(events, turnStateFailed) != 1 || terminalEventCount(events, turnStateInterrupted) != 0 {
		t.Fatalf("terminal events = %#v", events)
	}
}

func TestPostgresGroupTurnExcludesPrivateMemoryJobsAndKeepsCompaction(t *testing.T) {
	store, pool, cleanup := openCompanionIntegrationStore(t)
	defer cleanup()
	const characterID = "character-group-privacy"
	bootstrap, err := store.OpenOrCreateCharacterConversation(characterID)
	if err != nil {
		t.Fatalf("OpenOrCreateCharacterConversation: %v", err)
	}
	seedTurn, err := store.BeginTurn(bootstrap.Conversation.ID, "桌面私人上下文")
	if err != nil {
		t.Fatalf("BeginTurn(private fixture): %v", err)
	}
	if _, err := store.CompleteTurn(bootstrap.Conversation.ID, seedTurn.ID, "桌面回复"); err != nil {
		t.Fatalf("CompleteTurn(private fixture): %v", err)
	}
	const privateFixture = "仅限桌面的私人记忆-7f62"
	if _, err := store.CreatePersonalMemory("preference", memory.MemoryScope{Type: "global"}, privateFixture, 9000); err != nil {
		t.Fatalf("CreatePersonalMemory: %v", err)
	}
	before := groupPrivacyJobCounts(t, pool)
	provider := &capturingIntegrationModel{}
	service := newCompanionIntegrationService(store, characterID, provider)
	AttachConfigSource(service, cacheEnabledCompanionIntegrationConfig{})
	AttachProfileSource(service, rejectingGroupProfile{})
	if err := service.BindInteraction(bootstrap.Conversation.ID, publicAmbientBinding()); err != nil {
		t.Fatalf("BindInteraction: %v", err)
	}
	if _, err := service.SubmitCompiledTurn(SubmitCompiledTurnRequest{
		ConversationID:        bootstrap.Conversation.ID,
		Input:                 "大家好",
		MaxOutputTokens:       160,
		AvailableVisualStates: []VisualState{{ID: "idle", Description: "idle"}},
	}); err != nil {
		t.Fatalf("SubmitCompiledTurn: %v", err)
	}
	provider.mu.Lock()
	request := provider.request
	provider.mu.Unlock()
	toolNames := make(map[string]bool, len(request.Tools))
	for _, tool := range request.Tools {
		toolNames[tool.Name] = true
		if tool.Name == toolMemorySearch {
			t.Fatalf("group request exposes %q: %#v", toolMemorySearch, request.Tools)
		}
	}
	for _, name := range []string{toolPublicMemorySearch, toolSocialContextSearch, toolSocialExpressionSelect} {
		if !toolNames[name] {
			t.Fatalf("group request tools = %#v, missing %q", request.Tools, name)
		}
	}
	for _, item := range request.Input {
		if strings.Contains(item.Content, privateFixture) {
			t.Fatalf("private fixture leaked into group prompt item: %s", item.Content)
		}
	}
	if after := groupPrivacyJobCounts(t, pool); after != before {
		t.Fatalf("group background job counts changed: before=%v after=%v", before, after)
	}
	result, err := service.CompactConversation(bootstrap.Conversation.ID)
	if err != nil {
		t.Fatalf("CompactConversation(group): %v", err)
	}
	provider.mu.Lock()
	compactRequest := provider.request
	provider.mu.Unlock()
	if compactRequest.Shape.Lane != model.PromptLaneCompact || compactRequest.CacheInput == nil || compactRequest.CacheInput.Lane != model.PromptLaneCompact {
		t.Fatalf("compact cache identity = shape:%q input:%#v", compactRequest.Shape.Lane, compactRequest.CacheInput)
	}
	if want := model.LaneCacheKey(bootstrap.Conversation.ID, model.PromptLaneCompact); compactRequest.Shape.PromptCacheKey != want {
		t.Fatalf("compact compatibility cache key = %q, want %q", compactRequest.Shape.PromptCacheKey, want)
	}
	if result.WindowRevision < 2 || result.RetainedDialogueItems == 0 {
		t.Fatalf("group compaction result = %#v", result)
	}
	reloaded, err := store.LoadConversation(bootstrap.Conversation.ID)
	if err != nil {
		t.Fatalf("LoadConversation after group compaction: %v", err)
	}
	if reloaded.PromptWindow.Summary == nil ||
		*reloaded.PromptWindow.Summary != structuredCompactSummary ||
		reloaded.PromptWindow.ProjectionRevision != 2 ||
		reloaded.PromptWindow.Projection.RecentTailStartSequence != 1 {
		t.Fatalf("group prompt window after compaction = %#v", reloaded.PromptWindow)
	}
}

func TestPostgresPrivateTurnSeesCrossGroupSocialMemoryButPublicTurnDoesNot(t *testing.T) {
	store, _, cleanup := openCompanionIntegrationStore(t)
	defer cleanup()
	const characterID = "character-cross-group-memory"
	privateConversation, err := store.OpenOrCreateCharacterConversation(characterID)
	if err != nil {
		t.Fatalf("OpenOrCreateCharacterConversation: %v", err)
	}
	groupConversation, err := store.OpenOrCreateEndpointConversation(characterID, publicAmbientBinding(), strings.Repeat("b", 64))
	if err != nil {
		t.Fatalf("OpenOrCreateEndpointConversation: %v", err)
	}
	if _, err := store.StoreSocialMemoryEntries(context.Background(), memory.SocialMemoryBatchInput{
		CharacterID: characterID, ConversationID: groupConversation.Conversation.ID,
		Entries: []memory.SocialMemoryEntryInput{{
			Kind: memory.SocialMemoryEpisode, Situation: "群里讨论实习焦虑", Content: "大家先听完项目经历再给建议", RecallCue: "实习焦虑 项目经历",
			SourceStartUnixMS: 10, SourceEndUnixMS: 20,
		}},
	}); err != nil {
		t.Fatalf("StoreSocialMemoryEntries: %v", err)
	}

	privateProvider := &capturingIntegrationModel{}
	privateService := newCompanionIntegrationService(store, characterID, privateProvider)
	mustBindDesktopInteraction(t, privateService, privateConversation.Conversation.ID)
	if _, err := privateService.SubmitCompiledTurn(SubmitCompiledTurnRequest{
		ConversationID: privateConversation.Conversation.ID, Input: "我最近有点实习焦虑", MaxOutputTokens: 160,
		AvailableVisualStates: []VisualState{{ID: "idle", Description: "idle"}},
	}); err != nil {
		t.Fatalf("private SubmitCompiledTurn: %v", err)
	}
	privateProvider.mu.Lock()
	privateRequest := privateProvider.request
	privateProvider.mu.Unlock()
	if !compiledPromptContains(privateRequest, "大家先听完项目经历再给建议") {
		t.Fatal("private prompt did not include cross-group social memory")
	}

	publicProvider := &capturingIntegrationModel{}
	publicService := newCompanionIntegrationService(store, characterID, publicProvider)
	if err := publicService.BindInteraction(groupConversation.Conversation.ID, publicAmbientBinding()); err != nil {
		t.Fatalf("BindInteraction(public): %v", err)
	}
	if _, err := publicService.SubmitCompiledTurn(SubmitCompiledTurnRequest{
		ConversationID: groupConversation.Conversation.ID, Input: "大家好", MaxOutputTokens: 160,
		AvailableVisualStates: []VisualState{{ID: "idle", Description: "idle"}},
	}); err != nil {
		t.Fatalf("public SubmitCompiledTurn: %v", err)
	}
	publicProvider.mu.Lock()
	publicRequest := publicProvider.request
	publicProvider.mu.Unlock()
	if compiledPromptContains(publicRequest, "大家先听完项目经历再给建议") {
		t.Fatal("public prompt leaked cross-group social memory")
	}
}

func TestPostgresPublicTurnNaturalSocialQueryStaysInCurrentConversation(t *testing.T) {
	store, _, cleanup := openCompanionIntegrationStore(t)
	defer cleanup()
	const characterID = "character-public-natural-social"
	current, err := store.OpenOrCreateEndpointConversation(characterID, publicAmbientBinding(), strings.Repeat("c", 64))
	if err != nil {
		t.Fatalf("OpenOrCreateEndpointConversation(current): %v", err)
	}
	other, err := store.OpenOrCreateEndpointConversation(characterID, publicAmbientBinding(), strings.Repeat("d", 64))
	if err != nil {
		t.Fatalf("OpenOrCreateEndpointConversation(other): %v", err)
	}
	const currentFixture = "当前群的项目经历线索-9ac1"
	const otherFixture = "另一个群的项目经历线索-4de7"
	const privateFixture = "私人记忆绝不进入公共提示-78be"
	for _, fixture := range []struct {
		conversationID string
		content        string
	}{
		{conversationID: current.Conversation.ID, content: currentFixture},
		{conversationID: other.Conversation.ID, content: otherFixture},
	} {
		if _, err := store.StoreSocialMemoryEntries(context.Background(), memory.SocialMemoryBatchInput{
			CharacterID: characterID, ConversationID: fixture.conversationID,
			Entries: []memory.SocialMemoryEntryInput{{
				Kind: memory.SocialMemoryEpisode, Situation: "群里聊实习和项目经历", Content: fixture.content, RecallCue: "实习焦虑 项目经历",
				SourceStartUnixMS: 10, SourceEndUnixMS: 20,
			}},
		}); err != nil {
			t.Fatalf("StoreSocialMemoryEntries(%s): %v", fixture.conversationID, err)
		}
	}
	privateConversation, err := store.OpenOrCreateCharacterConversation(characterID)
	if err != nil {
		t.Fatalf("OpenOrCreateCharacterConversation(private fixture): %v", err)
	}
	privateTurn, err := store.BeginTurn(privateConversation.Conversation.ID, "私人上下文")
	if err != nil {
		t.Fatalf("BeginTurn(private fixture): %v", err)
	}
	if _, err := store.CompleteTurn(privateConversation.Conversation.ID, privateTurn.ID, "私人回复"); err != nil {
		t.Fatalf("CompleteTurn(private fixture): %v", err)
	}
	if _, err := store.CreatePersonalMemory("preference", memory.MemoryScope{Type: "global"}, privateFixture, 9000); err != nil {
		t.Fatalf("CreatePersonalMemory: %v", err)
	}

	provider := &capturingIntegrationModel{}
	service := newCompanionIntegrationService(store, characterID, provider)
	if err := service.BindInteraction(current.Conversation.ID, publicAmbientBinding()); err != nil {
		t.Fatalf("BindInteraction: %v", err)
	}
	if _, err := service.SubmitCompiledTurn(SubmitCompiledTurnRequest{
		ConversationID: current.Conversation.ID, Input: "我最近有点实习焦虑", MaxOutputTokens: 160,
		ReplyIntent:           &ReplyIntent{ReplyAct: "接话", Tone: "自然", RelationshipSignal: "群友", ReplyMode: "brief", Focus: "当前消息", MemoryQuery: "我最近有点实习焦虑", ExpressionQuery: "实习焦虑"},
		AvailableVisualStates: []VisualState{{ID: "idle", Description: "idle"}},
	}); err != nil {
		t.Fatalf("SubmitCompiledTurn: %v", err)
	}
	provider.mu.Lock()
	request := provider.request
	provider.mu.Unlock()
	if !compiledPromptContains(request, currentFixture) {
		t.Fatal("public prompt did not include current-conversation social memory")
	}
	for _, forbidden := range []string{otherFixture, privateFixture} {
		if compiledPromptContains(request, forbidden) {
			t.Fatalf("public prompt leaked %q", forbidden)
		}
	}
}

func TestPostgresGroupWebSearchKeepsPromptAndLearningTaskEphemeral(t *testing.T) {
	store, pool, cleanup := openCompanionIntegrationStore(t)
	defer cleanup()
	const characterID = "character-group-web"
	bootstrap, err := store.OpenOrCreateCharacterConversation(characterID)
	if err != nil {
		t.Fatalf("OpenOrCreateCharacterConversation: %v", err)
	}
	provider := &groupWebIntegrationModel{}
	service := newCompanionIntegrationService(store, characterID, provider)
	AttachConfigSource(service, groupWebIntegrationConfig{})
	service.webSearch = groupWebSearchStub{}
	if err := service.BindInteraction(bootstrap.Conversation.ID, publicAmbientBinding()); err != nil {
		t.Fatalf("BindInteraction: %v", err)
	}
	before := groupPrivacyJobCounts(t, pool)
	outcome, err := service.SubmitCompiledTurn(SubmitCompiledTurnRequest{
		ConversationID: bootstrap.Conversation.ID, Input: "有什么新闻", MaxOutputTokens: 160,
		AvailableVisualStates: []VisualState{{ID: "idle", Description: "idle"}},
	})
	if err != nil {
		t.Fatalf("SubmitCompiledTurn: %v", err)
	}
	provider.mu.Lock()
	requests := append([]model.CompiledPromptRequest(nil), provider.requests...)
	provider.mu.Unlock()
	if len(requests) != 2 {
		t.Fatalf("model request count = %d, want 2", len(requests))
	}
	if !compiledPromptContains(requests[1], "公开新闻标题") {
		t.Fatal("web result was not available to the current group turn")
	}
	ledger, err := store.ListTurnRuntimeEvents(outcome.ConversationID, outcome.TurnID)
	if err != nil {
		t.Fatalf("ListTurnRuntimeEvents: %v", err)
	}
	var inspection runtimeToolDetail
	foundInspection := false
	for _, event := range ledger {
		if event.EventType != runtimeLedgerEventTool {
			continue
		}
		var metadata struct {
			Detail runtimeToolDetail `json:"detail"`
		}
		if err := json.Unmarshal([]byte(event.MetadataJSON), &metadata); err != nil {
			t.Fatal(err)
		}
		if metadata.Detail.Version == "v1" {
			inspection = metadata.Detail
			foundInspection = true
			break
		}
	}
	if !foundInspection || inspection.Arguments.Query != "公开新闻" || inspection.Result == nil || inspection.MergedContext == nil {
		t.Fatalf("web tool inspection = %#v", inspection)
	}
	if len(inspection.Result.Knowledge) != 1 || inspection.Result.Knowledge[0].Statement != "公开新闻标题 — 公开摘要" {
		t.Fatalf("web tool result = %#v", inspection.Result)
	}
	if len(inspection.MergedContext.Knowledge) != 1 || inspection.MergedContext.Knowledge[0].Sources[0].URL != "https://example.com/news" {
		t.Fatalf("merged model context = %#v", inspection.MergedContext)
	}
	after := groupPrivacyJobCounts(t, pool)
	if after.extraction != before.extraction {
		t.Fatalf("group web search changed personal extraction count: before=%v after=%v", before, after)
	}
	var feedbackTableAbsent bool
	if err := pool.QueryRow(t.Context(), "SELECT to_regclass('feedback_events') IS NULL").Scan(&feedbackTableAbsent); err != nil {
		t.Fatal(err)
	}
	if !feedbackTableAbsent {
		t.Fatal("web learning created persistent feedback storage")
	}
}

func compiledPromptContains(request model.CompiledPromptRequest, text string) bool {
	for _, item := range request.Input {
		if strings.Contains(item.Content, text) {
			return true
		}
	}
	return false
}

type privacyJobCounts struct {
	extraction int
}

func groupPrivacyJobCounts(t *testing.T, pool *pgxpool.Pool) privacyJobCounts {
	t.Helper()
	var counts privacyJobCounts
	if err := pool.QueryRow(context.Background(), `
SELECT count(*)
FROM conversation_turns
WHERE status = 'completed' AND extraction_state IN ('pending', 'claimed', 'failed')`).Scan(&counts.extraction); err != nil {
		t.Fatalf("counting private extraction turns: %v", err)
	}
	return counts
}

func newCompanionIntegrationService(store *memory.Store, characterID string, scripted ModelPort) *CompanionService {
	return newCompanionIntegrationServiceWithPorts(memoryPortsFromStore(store), characterID, scripted)
}

func newCompanionIntegrationServiceWithPorts(ports memoryPorts, characterID string, scripted ModelPort) *CompanionService {
	service := newCompanionServiceWithPorts("", ports, scripted, nil)
	AttachCharacterLookup(service, companionIntegrationCharacterLookup{record: companionIntegrationCharacter(characterID)})
	AttachProfileSource(service, companionIntegrationProfile{})
	AttachConfigSource(service, companionIntegrationConfig{})
	return service
}

func companionIntegrationCharacter(characterID string) character.Record {
	return character.Record{
		CharacterID:      characterID,
		Revision:         1,
		Name:             "Fairy",
		Description:      "认真听用户说话。",
		TextLanguage:     "zh",
		SpeakingLanguage: "zh",
		Appearance: character.Appearance{Status: "assigned", Visual: &character.Manifest{States: []character.State{{
			ID: "idle", Description: "idle", ImagePath: "states/idle.png",
		}}}},
	}
}

func finalBeatEvents(events []session.Event) []beatReadyPayload {
	result := make([]beatReadyPayload, 0)
	for _, event := range events {
		var payload beatReadyPayload
		if json.Unmarshal(event.Payload, &payload) == nil && payload.Type == "beat.ready" && payload.Kind == reply.BeatKindFinal {
			result = append(result, payload)
		}
	}
	return result
}

func terminalEventCount(events []session.Event, state turnState) int {
	count := 0
	for _, event := range events {
		if event.State != string(state) {
			continue
		}
		var envelope struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(event.Payload, &envelope) != nil {
			continue
		}
		switch envelope.Type {
		case "completed", "failed", "state_changed":
			count++
		}
	}
	return count
}

type failedEvent struct{ Code string }

func lastFailedEvent(events []session.Event) *failedEvent {
	for index := len(events) - 1; index >= 0; index-- {
		var payload failedPayload
		if json.Unmarshal(events[index].Payload, &payload) == nil && payload.Type == "failed" {
			return &failedEvent{Code: payload.Error.Code}
		}
	}
	return nil
}

func hasRuntimeLedgerType(events []memory.TurnRuntimeEventRecord, eventType string) bool {
	for _, event := range events {
		if event.EventType == eventType {
			return true
		}
	}
	return false
}

func assertPostgresDoesNotContain(t *testing.T, pool *pgxpool.Pool, fixture string) {
	t.Helper()
	rows, err := pool.Query(t.Context(), `
SELECT table_name, column_name
FROM information_schema.columns
WHERE table_schema = current_schema()
  AND data_type IN ('text', 'character varying', 'json', 'jsonb')
ORDER BY table_name, ordinal_position`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type column struct{ table, name string }
	columns := make([]column, 0)
	for rows.Next() {
		var current column
		if err := rows.Scan(&current.table, &current.name); err != nil {
			t.Fatal(err)
		}
		columns = append(columns, current)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for _, current := range columns {
		tableID := pgx.Identifier{current.table}.Sanitize()
		columnID := pgx.Identifier{current.name}.Sanitize()
		var count int
		if err := pool.QueryRow(t.Context(), "SELECT count(*) FROM "+tableID+" WHERE "+columnID+"::text LIKE $1", "%"+fixture+"%").Scan(&count); err != nil {
			t.Fatalf("scan %s.%s: %v", current.table, current.name, err)
		}
		if count != 0 {
			t.Fatalf("database persisted raw desktop fixture in %s.%s", current.table, current.name)
		}
	}
}

func openCompanionIntegrationStore(t *testing.T) (*memory.Store, *pgxpool.Pool, func()) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rawURL := os.Getenv("FAIRY_TEST_DATABASE_URL")
	if rawURL == "" {
		rawURL = "postgres://fairy:fairy_test_password@127.0.0.1:15432/fairy_test?sslmode=disable"
	}
	admin, err := pgxpool.New(ctx, rawURL)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("fairy_companion_test_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	admin.Close()
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	pool, err := coredb.Open(ctx, coredb.ShortTimeoutConfig(parsed.String()))
	if err != nil {
		t.Fatal(err)
	}
	if err := coredb.Migrate(ctx, pool.Raw()); err != nil {
		pool.Close()
		t.Fatal(err)
	}
	store, err := memory.NewStoreFromPool(pool)
	if err != nil {
		pool.Close()
		t.Fatal(err)
	}
	cleanup := func() {
		pool.Close()
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		admin, err := pgxpool.New(cleanupCtx, rawURL)
		if err == nil {
			defer admin.Close()
			_, _ = admin.Exec(cleanupCtx, "DROP SCHEMA IF EXISTS "+quoted+" CASCADE")
		}
	}
	return store, pool.Raw(), cleanup
}
