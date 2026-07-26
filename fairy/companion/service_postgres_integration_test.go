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
	"testing"
	"time"

	"fairy/character"
	"fairy/config"
	"fairy/memory"
	"fairy/model"
	pgstore "fairy/postgres"
	"fairy/profile"
	"fairy/reply"
	"fairy/search"
	"fairy/visual"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	contracts "fairy/contracts/interaction"
	obs "fairy/contracts/observation"
	appobs "fairy/observation"
)

var (
	errCompletePersistence  = errors.New("complete persistence unavailable")
	errInterruptPersistence = errors.New("interrupt persistence unavailable")
)

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
		return []model.StreamEvent{{Type: "text_delta", Data: "群聊摘要"}, {Type: "usage", Usage: &model.Usage{PromptTokens: 2, CompletionTokens: 1}}}, nil
	}
	return companionIntegrationModel{chains: []ReplyChain{{VisualState: "idle", Text: "群聊回复。"}}}.ExecuteRequestContext(context.Background(), request)
}

func (m *capturingIntegrationModel) ExecutePrompt(model.PromptLane, string, uint32, []model.PromptItem, string) ([]model.StreamEvent, error) {
	return []model.StreamEvent{{Type: "text_delta", Data: "群聊摘要"}, {Type: "usage", Usage: &model.Usage{PromptTokens: 2, CompletionTokens: 1}}}, nil
}

type companionIntegrationCatalog struct {
	record character.Record
}

func (c companionIntegrationCatalog) List() (character.Catalog, error) {
	return character.Catalog{Characters: []character.Record{c.record}}, nil
}

type companionIntegrationProfile struct{}

func (companionIntegrationProfile) Current() (*profile.Snapshot, error) { return nil, nil }

type rejectingGroupProfile struct{}

func (rejectingGroupProfile) Current() (*profile.Snapshot, error) {
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
	available bool
	calls     int
}

func (coordinator *desktopToolIntegrationCoordinator) Available(string) bool {
	return coordinator.available
}
func (coordinator *desktopToolIntegrationCoordinator) CancelTurn(context.Context, string, string) error {
	return nil
}
func (coordinator *desktopToolIntegrationCoordinator) Observe(_ context.Context, request DesktopToolRequest) (DesktopToolEvidence, error) {
	coordinator.calls++
	return DesktopToolEvidence{
		ExecutionID: "execution-1", MediaType: "image/png", Width: 1, Height: 1,
		DataURL: "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=",
	}, nil
}

type desktopToolIntegrationModel struct {
	requests []model.CompiledPromptRequest
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

func (groupWebSearchStub) Search(context.Context, string, int) ([]search.Hit, error) {
	return []search.Hit{{Title: "公开新闻标题", URL: "https://example.com/news", Snippet: "公开摘要"}}, nil
}

func (groupWebSearchStub) Close() error { return nil }

type terminalFailureMemory struct {
	MemoryPort
	completeErr  error
	interruptErr error
}

func mustBindDesktopInteraction(t *testing.T, service *CompanionService, conversationID string) {
	t.Helper()
	err := service.BindInteraction(conversationID, contracts.Binding{
		Endpoint: contracts.EndpointDesktop,
		Facts: contracts.Facts{
			Audience: contracts.AudienceSingle, Initiation: contracts.InitiationDirect,
			Presentation: contracts.PresentationEmbodied,
		},
	})
	if err != nil {
		t.Fatalf("BindInteraction: %v", err)
	}
}

func (m terminalFailureMemory) CompleteTurn(string, string, string) (memory.MessageRecord, error) {
	return memory.MessageRecord{}, m.completeErr
}

func (m terminalFailureMemory) InterruptTurn(string, string, string) (*memory.MessageRecord, error) {
	return nil, m.interruptErr
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
	var events []TurnEvent
	AttachEventEmitter(service, func(event TurnEvent) {
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
	if terminalEventCount(events, TurnStateCompleted) != 1 || terminalEventCount(events, TurnStateInterrupted) != 0 {
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
	coordinator := &desktopToolIntegrationCoordinator{available: true}
	service := newCompanionIntegrationService(store, "character-desktop-tool", provider)
	AttachConfigSource(service, visionCompanionIntegrationConfig{})
	AttachDesktopToolCoordinator(service, coordinator)
	mustBindDesktopInteraction(t, service, bootstrap.Conversation.ID)
	outcome, err := service.SubmitCompiledTurn(SubmitCompiledTurnRequest{
		ConversationID: bootstrap.Conversation.ID, Input: "看看我屏幕上的内容",
		MaxOutputTokens: 160, AvailableVisualStates: []VisualState{{ID: "idle", Description: "idle"}},
	})
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
	observation := DesktopObservation{
		ObservationID: "obs-1", TimestampUnixMS: now.UnixMilli(), Trigger: obs.DesktopTriggerLifecycle,
		Activity: obs.DesktopActivityIdle, Lifecycle: obs.DesktopLifecycleReturned, Privacy: obs.DesktopPrivacyNormal,
	}
	if err := service.desktopEvidence.Accept(observation, now); err != nil {
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

func TestPostgresDesktopObservationGraphSchedulesInitiation(t *testing.T) {
	store, pool, cleanup := openCompanionIntegrationStore(t)
	defer cleanup()
	bootstrap, err := store.OpenOrCreateCharacterConversation("character-observation-graph")
	if err != nil {
		t.Fatal(err)
	}
	service := newCompanionIntegrationService(store, "character-observation-graph", &capturingIntegrationModel{})
	mustBindDesktopInteraction(t, service, bootstrap.Conversation.ID)
	completed := make(chan TurnEvent, 1)
	AttachEventEmitter(service, func(event TurnEvent) {
		if event.State == TurnStateCompleted {
			select {
			case completed <- event:
			default:
			}
		}
	})

	now := time.Now()
	plan, err := service.ObserveDesktop(bootstrap.Conversation.ID, DesktopObservation{
		ObservationID: "obs-graph", TimestampUnixMS: now.UnixMilli(), Trigger: obs.DesktopTriggerLifecycle,
		Activity: obs.DesktopActivityIdle, Lifecycle: obs.DesktopLifecycleReturned, Privacy: obs.DesktopPrivacyNormal,
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Action != appobs.DesktopActionInitiate {
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

	var event TurnEvent
	select {
	case event = <-completed:
	case <-time.After(5 * time.Second):
		t.Fatal("desktop initiation did not complete")
	}
	deadline := time.Now().Add(5 * time.Second)
	for service.backgroundJobs.Load() != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if service.backgroundJobs.Load() != 0 {
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
	nodeEvents := 0
	nodeNames := make(map[string]int)
	for _, record := range ledger {
		if record.EventType != runtimeLedgerEventNode {
			continue
		}
		nodeEvents++
		var metadata map[string]any
		if err := json.Unmarshal([]byte(record.MetadataJSON), &metadata); err != nil {
			t.Fatal(err)
		}
		if len(metadata) != 3 || metadata["node"] == nil || metadata["kind"] == nil || metadata["status"] == nil {
			t.Fatalf("node metadata = %#v", metadata)
		}
		nodeNames[metadata["node"].(string)]++
	}
	if nodeEvents != 10 {
		t.Fatalf("node event count = %d, want 10", nodeEvents)
	}
	for _, node := range []string{"interpreting", "gathering", "planning", "responding", "persist"} {
		if nodeNames[node] != 2 {
			t.Fatalf("node %q events = %d, all = %#v", node, nodeNames[node], nodeNames)
		}
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
	var events []TurnEvent
	AttachEventEmitter(service, func(event TurnEvent) {
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
	if terminalEventCount(events, TurnStateCompleted) != 1 || terminalEventCount(events, TurnStateFailed) != 0 {
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
	var events []TurnEvent
	var cancelErr error
	AttachEventEmitter(service, func(event TurnEvent) {
		mu.Lock()
		events = append(events, event)
		mu.Unlock()
		if payload, ok := event.Payload.(beatReadyPayload); ok && payload.Kind == reply.BeatKindFinal && payload.ChainIndex == 0 {
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
	if terminalEventCount(events, TurnStateInterrupted) != 1 || terminalEventCount(events, TurnStateCompleted) != 0 {
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
	service := newCompanionIntegrationService(terminalFailureMemory{MemoryPort: store, completeErr: errCompletePersistence}, "character-terminal-failure", companionIntegrationModel{chains: []ReplyChain{
		{VisualState: "idle", Text: "已发布但无法保存"},
	}})
	mustBindDesktopInteraction(t, service, bootstrap.Conversation.ID)
	var events []TurnEvent
	AttachEventEmitter(service, func(event TurnEvent) { events = append(events, event) })
	_, submitErr := service.SubmitCompiledTurn(SubmitCompiledTurnRequest{
		ConversationID:        bootstrap.Conversation.ID,
		Input:                 "测试持久化错误",
		MaxOutputTokens:       160,
		AvailableVisualStates: []VisualState{{ID: "idle", Description: "idle"}},
	})
	if !errors.Is(submitErr, errCompletePersistence) {
		t.Fatalf("SubmitCompiledTurn = %v, want complete persistence error", submitErr)
	}
	if terminalEventCount(events, TurnStateFailed) != 1 || terminalEventCount(events, TurnStateCompleted) != 0 {
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
	service := newCompanionIntegrationService(terminalFailureMemory{MemoryPort: store, interruptErr: errInterruptPersistence}, "character-interrupt-failure", companionIntegrationModel{chains: []ReplyChain{
		{VisualState: "idle", Text: "第一拍"},
		{VisualState: "idle", Text: "第二拍"},
	}})
	mustBindDesktopInteraction(t, service, bootstrap.Conversation.ID)
	var events []TurnEvent
	AttachEventEmitter(service, func(event TurnEvent) {
		events = append(events, event)
		if payload, ok := event.Payload.(beatReadyPayload); ok && payload.ChainIndex == 0 {
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
	if terminalEventCount(events, TurnStateFailed) != 1 || terminalEventCount(events, TurnStateInterrupted) != 0 {
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
	if reloaded.PromptWindow.Summary == nil || *reloaded.PromptWindow.Summary != "群聊摘要" || reloaded.PromptWindow.CutoffMessageSequence == 0 {
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

func TestPostgresGroupWebSearchIsEphemeral(t *testing.T) {
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
	if _, err := service.SubmitCompiledTurn(SubmitCompiledTurnRequest{
		ConversationID: bootstrap.Conversation.ID, Input: "有什么新闻", MaxOutputTokens: 160,
		AvailableVisualStates: []VisualState{{ID: "idle", Description: "idle"}},
	}); err != nil {
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
	if after := groupPrivacyJobCounts(t, pool); after != before {
		t.Fatalf("group web search persisted jobs: before=%v after=%v", before, after)
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
	embedding  int
	ingest     int
}

func groupPrivacyJobCounts(t *testing.T, pool *pgxpool.Pool) privacyJobCounts {
	t.Helper()
	var counts privacyJobCounts
	for query, destination := range map[string]*int{
		"SELECT count(*) FROM extraction_batches":    &counts.extraction,
		"SELECT count(*) FROM memory_embedding_jobs": &counts.embedding,
		"SELECT count(*) FROM knowledge_ingest_jobs": &counts.ingest,
	} {
		if err := pool.QueryRow(context.Background(), query).Scan(destination); err != nil {
			t.Fatalf("counting privacy jobs: %v", err)
		}
	}
	return counts
}

func newCompanionIntegrationService(memoryPort MemoryPort, characterID string, scripted ModelPort) *CompanionService {
	record := character.Record{
		CharacterID:      characterID,
		Revision:         1,
		Name:             "Fairy",
		Description:      "认真听用户说话。",
		TextLanguage:     "zh",
		SpeakingLanguage: "zh",
		Appearance: character.Appearance{Status: "assigned", Visual: &visual.Manifest{States: []visual.State{{
			ID: "idle", Description: "idle", ImagePath: "states/idle.png",
		}}}},
	}
	service := NewCompanionServiceWithRuntime("", memoryPort, scripted, nil)
	AttachCharacterCatalog(service, companionIntegrationCatalog{record: record})
	AttachProfileSource(service, companionIntegrationProfile{})
	AttachConfigSource(service, companionIntegrationConfig{})
	return service
}

func finalBeatEvents(events []TurnEvent) []beatReadyPayload {
	result := make([]beatReadyPayload, 0)
	for _, event := range events {
		if payload, ok := event.Payload.(beatReadyPayload); ok && payload.Kind == reply.BeatKindFinal {
			result = append(result, payload)
		}
	}
	return result
}

func terminalEventCount(events []TurnEvent, state TurnState) int {
	count := 0
	for _, event := range events {
		if event.State != state {
			continue
		}
		switch event.Payload.(type) {
		case completedPayload, failedPayload, stateChangedPayload:
			count++
		}
	}
	return count
}

type failedEvent struct{ Code string }

func lastFailedEvent(events []TurnEvent) *failedEvent {
	for index := len(events) - 1; index >= 0; index-- {
		if payload, ok := events[index].Payload.(failedPayload); ok {
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
	pool, err := pgstore.Open(ctx, pgstore.ShortTimeoutConfig(parsed.String()))
	if err != nil {
		t.Fatal(err)
	}
	if err := pgstore.Migrate(ctx, pool); err != nil {
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
