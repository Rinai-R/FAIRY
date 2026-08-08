//go:build integration

package conversation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"fairy/agent/reply"
	historyruntime "fairy/context/history/runtime"
	"fairy/runtime/config"
	"fairy/runtime/model"
)

// immediateCompactionRetention represents the strongest scheduling order the
// RetentionPort contract permits: an admitted job may start before
// ScheduleCompaction returns to its caller.
type immediateCompactionRetention struct {
	mu        sync.Mutex
	scheduled int
}

var _ RetentionPort = (*immediateCompactionRetention)(nil)

func (retention *immediateCompactionRetention) ScheduleCompaction(job func()) error {
	retention.mu.Lock()
	retention.scheduled++
	retention.mu.Unlock()
	job()
	return nil
}

func (*immediateCompactionRetention) ActiveJobs() int64                        { return 0 }
func (*immediateCompactionRetention) ObserveCompletedTurn(RetentionCompletion) {}
func (*immediateCompactionRetention) TakeCommittedCoverage(string) bool        { return false }
func (*immediateCompactionRetention) Close()                                   {}

func (retention *immediateCompactionRetention) scheduledJobs() int {
	retention.mu.Lock()
	defer retention.mu.Unlock()
	return retention.scheduled
}

type hardPressureIntegrationConfig struct{ companionIntegrationConfig }

func (hardPressureIntegrationConfig) ModelConnection() (config.ModelConnection, error) {
	connection, err := (companionIntegrationConfig{}).ModelConnection()
	if err != nil {
		return config.ModelConnection{}, err
	}
	connection.Protocol = "responses"
	connection.ContextWindowTokens = 8_192
	connection.Capabilities.PromptCacheKey = true
	connection.Capabilities.CacheRetention = true
	return connection, nil
}

type hardPressureAutoCompactionModel struct {
	mu       sync.Mutex
	requests []model.CompiledPromptRequest
}

func (provider *hardPressureAutoCompactionModel) ExecuteRequestContext(_ context.Context, request model.CompiledPromptRequest) ([]model.StreamEvent, error) {
	provider.mu.Lock()
	provider.requests = append(provider.requests, request)
	provider.mu.Unlock()
	if request.Shape.Lane == model.PromptLaneCompact {
		return []model.StreamEvent{
			{Type: "text_delta", Data: structuredCompactSummary},
			{Type: "usage", Usage: &model.Usage{PromptTokens: 3_200, CompletionTokens: 80}},
		}, nil
	}
	return []model.StreamEvent{
		{Type: "text_delta", Data: `{"chains":[{"visualState":"idle","text":"收到，我们继续。"}]}`},
		{Type: "usage", Usage: &model.Usage{PromptTokens: 7_000, CompletionTokens: 32}},
		{Type: "completed", Data: "resp_auto_compaction"},
	}, nil
}

func (*hardPressureAutoCompactionModel) ExecutePrompt(model.PromptLane, string, uint32, []model.PromptItem, string) ([]model.StreamEvent, error) {
	return nil, fmt.Errorf("unexpected ExecutePrompt")
}

func (provider *hardPressureAutoCompactionModel) lanes() []model.PromptLane {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	lanes := make([]model.PromptLane, 0, len(provider.requests))
	for _, request := range provider.requests {
		lanes = append(lanes, request.Shape.Lane)
	}
	return lanes
}

func TestPostgresCompletedTurnReleasesGateBeforeAutomaticFullCompaction(t *testing.T) {
	store, _, cleanup := openCompanionIntegrationStore(t)
	defer cleanup()
	const characterID = "character-auto-compaction-order"
	bootstrap, err := store.OpenOrCreateCharacterConversation(characterID)
	if err != nil {
		t.Fatalf("OpenOrCreateCharacterConversation: %v", err)
	}
	conversationID := bootstrap.Conversation.ID
	for index := 0; index < 6; index++ {
		turn, beginErr := store.BeginTurn(conversationID, fmt.Sprintf("旧问题 %d：%s", index, strings.Repeat("甲", 1_000)))
		if beginErr != nil {
			t.Fatalf("BeginTurn(%d): %v", index, beginErr)
		}
		if _, completeErr := store.CompleteTurn(conversationID, turn.ID, fmt.Sprintf("旧回答 %d：%s", index, strings.Repeat("乙", 1_000))); completeErr != nil {
			t.Fatalf("CompleteTurn(%d): %v", index, completeErr)
		}
	}
	before, err := store.LoadConversation(conversationID)
	if err != nil {
		t.Fatalf("LoadConversation before turn: %v", err)
	}

	provider := &hardPressureAutoCompactionModel{}
	service := newCompanionIntegrationService(store, characterID, provider)
	AttachConfigSource(service, hardPressureIntegrationConfig{})
	retention := &immediateCompactionRetention{}
	AttachRetention(service, retention)
	mustBindDesktopInteraction(t, service, conversationID)
	outcome, err := service.SubmitCompiledTurn(SubmitCompiledTurnRequest{
		ConversationID:        conversationID,
		Input:                 "继续刚才的话题",
		MaxOutputTokens:       160,
		AvailableVisualStates: []reply.VisualState{{ID: "idle", Description: "idle"}},
	})
	if err != nil {
		t.Fatalf("SubmitCompiledTurn: %v", err)
	}

	lanes := provider.lanes()
	if len(lanes) != 2 || lanes[0] != model.PromptLaneRespond || lanes[1] != model.PromptLaneCompact {
		t.Fatalf("model lanes = %v, want [respond compact]", lanes)
	}
	if jobs := retention.scheduledJobs(); jobs != 1 {
		t.Fatalf("scheduled compaction jobs = %d, want 1", jobs)
	}
	service.backgroundErrorMu.Lock()
	backgroundErr := service.backgroundError
	service.backgroundErrorMu.Unlock()
	if backgroundErr != nil {
		t.Fatalf("automatic compaction background error: %v", backgroundErr)
	}
	metrics := service.AgentLoopMetrics().Compaction
	if metrics.L3Applied != 1 || metrics.Failed != 0 {
		t.Fatalf("compaction metrics = %#v, want one successful L3", metrics)
	}

	after, err := store.LoadConversation(conversationID)
	if err != nil {
		t.Fatalf("LoadConversation after turn: %v", err)
	}
	if len(after.Messages) != len(before.Messages)+2 {
		t.Fatalf("full transcript messages = %d, want %d", len(after.Messages), len(before.Messages)+2)
	}
	if after.PromptWindow.Revision != before.PromptWindow.Revision+1 ||
		after.PromptWindow.ProjectionRevision != before.PromptWindow.ProjectionRevision+1 ||
		after.PromptWindow.Summary == nil || *after.PromptWindow.Summary != structuredCompactSummary ||
		after.PromptWindow.CutoffMessageSequence == 0 {
		t.Fatalf("prompt window after automatic compaction = %#v", after.PromptWindow)
	}
	active, err := store.history.LoadConversationPrompt(conversationID)
	if err != nil {
		t.Fatalf("LoadConversationPrompt: %v", err)
	}
	if len(active.Messages) == 0 || len(active.Messages) >= len(after.Messages) {
		t.Fatalf("active prompt messages = %d, full transcript = %d", len(active.Messages), len(after.Messages))
	}
	for _, message := range active.Messages {
		if message.Sequence <= after.PromptWindow.CutoffMessageSequence {
			t.Fatalf("active prompt retained sequence %d at/before cutoff %d", message.Sequence, after.PromptWindow.CutoffMessageSequence)
		}
	}
	if got := active.Messages[len(active.Messages)-1].TurnID; got != outcome.TurnID {
		t.Fatalf("active prompt tail turn = %q, want %q", got, outcome.TurnID)
	}
	if continuation, found, loadErr := store.runtime.LoadLaneContinuation(conversationID, string(model.PromptLaneRespond)); loadErr != nil {
		t.Fatalf("LoadLaneContinuation: %v", loadErr)
	} else if found {
		t.Fatalf("stale continuation survived full compaction: %#v", continuation)
	}
	ledger, err := store.ListTurnRuntimeEvents(conversationID, outcome.TurnID)
	if err != nil {
		t.Fatalf("ListTurnRuntimeEvents: %v", err)
	}
	assertL3CompactionLedger(t, ledger)
}

func TestPostgresTerminalPersistenceFailureReleasesGateWithoutSchedulingCompaction(t *testing.T) {
	store, _, cleanup := openCompanionIntegrationStore(t)
	defer cleanup()
	const characterID = "character-auto-compaction-terminal-failure"
	bootstrap, err := store.OpenOrCreateCharacterConversation(characterID)
	if err != nil {
		t.Fatalf("OpenOrCreateCharacterConversation: %v", err)
	}
	ports := store.ports()
	ports.turn.turns = terminalFailureTurnStore{base: store, completeErr: errCompletePersistence}
	provider := &hardPressureAutoCompactionModel{}
	service := newCompanionIntegrationServiceWithPorts(ports, characterID, provider)
	AttachConfigSource(service, hardPressureIntegrationConfig{})
	retention := &immediateCompactionRetention{}
	AttachRetention(service, retention)
	mustBindDesktopInteraction(t, service, bootstrap.Conversation.ID)

	if _, submitErr := service.SubmitCompiledTurn(SubmitCompiledTurnRequest{
		ConversationID:        bootstrap.Conversation.ID,
		Input:                 "触发终态持久化失败",
		MaxOutputTokens:       160,
		AvailableVisualStates: []reply.VisualState{{ID: "idle", Description: "idle"}},
	}); !errors.Is(submitErr, errCompletePersistence) {
		t.Fatalf("SubmitCompiledTurn = %v, want %v", submitErr, errCompletePersistence)
	}
	if jobs := retention.scheduledJobs(); jobs != 0 {
		t.Fatalf("failed Turn scheduled %d compaction jobs", jobs)
	}
	if _, reserveErr := service.reserveTurn(bootstrap.Conversation.ID); reserveErr != nil {
		t.Fatalf("failed Turn did not release conversation gate: %v", reserveErr)
	}
	service.endTurn(bootstrap.Conversation.ID, "")
}

func assertL3CompactionLedger(t *testing.T, events []historyruntime.TurnRuntimeEventRecord) {
	t.Helper()
	for _, event := range events {
		if event.EventType != runtimeLedgerEventCompaction {
			continue
		}
		var metadata struct {
			Layer   string `json:"layer"`
			Trigger string `json:"trigger"`
		}
		if err := json.Unmarshal([]byte(event.MetadataJSON), &metadata); err != nil {
			t.Fatalf("decode compaction ledger: %v", err)
		}
		if metadata.Layer == "l3" && metadata.Trigger == "hard_watermark" {
			return
		}
	}
	t.Fatal("runtime ledger is missing hard-watermark L3 compaction")
}
