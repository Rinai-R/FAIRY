//go:build integration

package companion

import (
	"context"
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
	"fairy/search"
	"fairy/visual"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
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

type capturingIntegrationModel struct {
	mu      sync.Mutex
	request model.CompiledPromptRequest
}

func (m *capturingIntegrationModel) ExecuteRequestContext(_ context.Context, request model.CompiledPromptRequest) ([]model.StreamEvent, error) {
	m.mu.Lock()
	m.request = request
	m.mu.Unlock()
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

type groupWebIntegrationConfig struct{ companionIntegrationConfig }

func (groupWebIntegrationConfig) WebSearchSettings() (config.WebSearchSettings, error) {
	return config.WebSearchSettings{SchemaVersion: 1, Enabled: true}, nil
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
	var mu sync.Mutex
	var events []HarnessEvent
	AttachEventEmitter(service, func(event HarnessEvent) {
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
	var mu sync.Mutex
	var events []HarnessEvent
	var cancelErr error
	AttachEventEmitter(service, func(event HarnessEvent) {
		mu.Lock()
		events = append(events, event)
		mu.Unlock()
		if payload, ok := event.Payload.(beatReadyPayload); ok && payload.Kind == beatKindFinal && payload.ChainIndex == 0 {
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
	var events []HarnessEvent
	AttachEventEmitter(service, func(event HarnessEvent) { events = append(events, event) })
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
	var events []HarnessEvent
	AttachEventEmitter(service, func(event HarnessEvent) {
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
	AttachProfileSource(service, rejectingGroupProfile{})
	if err := service.BindSurface(bootstrap.Conversation.ID, SurfaceIMGroup); err != nil {
		t.Fatalf("BindSurface: %v", err)
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
	for _, tool := range request.Tools {
		if tool.Name == toolMemorySearch {
			t.Fatalf("group request exposes %q: %#v", toolMemorySearch, request.Tools)
		}
	}
	if len(request.Tools) != 1 || request.Tools[0].Name != toolPublicMemorySearch {
		t.Fatalf("group request tools = %#v, want public memory only", request.Tools)
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
	if err := service.BindSurface(bootstrap.Conversation.ID, SurfaceIMGroup); err != nil {
		t.Fatalf("BindSurface: %v", err)
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

func finalBeatEvents(events []HarnessEvent) []beatReadyPayload {
	result := make([]beatReadyPayload, 0)
	for _, event := range events {
		if payload, ok := event.Payload.(beatReadyPayload); ok && payload.Kind == beatKindFinal {
			result = append(result, payload)
		}
	}
	return result
}

func terminalEventCount(events []HarnessEvent, state TurnState) int {
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

func lastFailedEvent(events []HarnessEvent) *failedEvent {
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
