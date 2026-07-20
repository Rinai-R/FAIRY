//go:build integration

package companion

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"fairy/character"
	"fairy/config"
	"fairy/memory"
	"fairy/model"
	pgstore "fairy/postgres"
	"fairy/profile"

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

type companionIntegrationCatalog struct {
	record character.Record
}

func (c companionIntegrationCatalog) List() (character.Catalog, error) {
	return character.Catalog{Characters: []character.Record{c.record}}, nil
}

type companionIntegrationProfile struct{}

func (companionIntegrationProfile) Current() (*profile.Snapshot, error) { return nil, nil }

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

func newCompanionIntegrationService(memoryPort MemoryPort, characterID string, scripted companionIntegrationModel) *CompanionService {
	record := character.Record{
		CharacterID:      characterID,
		Revision:         1,
		Name:             "Fairy",
		Description:      "认真听用户说话。",
		TextLanguage:     "zh",
		SpeakingLanguage: "zh",
		Appearance:       character.Appearance{Status: "assigned"},
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
