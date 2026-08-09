//go:build integration

package conversation

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"fairy/agent/conversation/lifecycle"
	"fairy/agent/learning"
	"fairy/agent/reply"
	history "fairy/context/history/transcript"
	"fairy/runtime/model"
	"fairy/transport/session"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	longSessionTurnCount    = 32
	longSessionRestartAfter = longSessionTurnCount / 2
	longSessionInputRunes   = 240
	longSessionReplyRunes   = 500
	longSessionIdleTimeout  = 10 * time.Second
)

type longSessionModel struct {
	mu              sync.Mutex
	respondRequests int
	compactRequests int
}

func (provider *longSessionModel) ExecuteRequestContext(_ context.Context, request model.CompiledPromptRequest) ([]model.StreamEvent, error) {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	switch request.Shape.Lane {
	case model.PromptLaneRespond:
		provider.respondRequests++
		payload, err := json.Marshal(map[string]any{"chains": []map[string]string{{
			"visualState": "idle",
			"text":        "收到，我们继续。" + strings.Repeat("乙", longSessionReplyRunes),
		}}})
		if err != nil {
			return nil, fmt.Errorf("encode long-session reply: %w", err)
		}
		return []model.StreamEvent{
			{Type: "text_delta", Data: string(payload)},
			{Type: "usage", Usage: &model.Usage{PromptTokens: 7_000, CompletionTokens: 24}},
			{Type: "completed", Data: fmt.Sprintf("long-session-response-%d", provider.respondRequests)},
		}, nil
	case model.PromptLaneCompact:
		provider.compactRequests++
		return []model.StreamEvent{
			{Type: "text_delta", Data: structuredCompactSummary},
			{Type: "usage", Usage: &model.Usage{PromptTokens: 3_200, CompletionTokens: 80}},
		}, nil
	default:
		return nil, fmt.Errorf("unexpected long-session model lane %q", request.Shape.Lane)
	}
}

func (*longSessionModel) ExecutePrompt(model.PromptLane, string, uint32, []model.PromptItem, string) ([]model.StreamEvent, error) {
	return nil, fmt.Errorf("unexpected ExecutePrompt")
}

func (provider *longSessionModel) counts() (respond, compact int) {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	return provider.respondRequests, provider.compactRequests
}

type longSessionRetention struct {
	service   *learning.Service
	completed chan struct{}
}

func newLongSessionRetention() *longSessionRetention {
	return &longSessionRetention{
		service:   learning.New(learning.Options{}),
		completed: make(chan struct{}, 1),
	}
}

func (retention *longSessionRetention) ScheduleCompaction(job func()) error {
	return retention.service.ScheduleCompaction(func() {
		defer func() {
			select {
			case retention.completed <- struct{}{}:
			default:
			}
		}()
		job()
	})
}

func (retention *longSessionRetention) ActiveJobs() int64 {
	return retention.service.ActiveJobs()
}

func (retention *longSessionRetention) ObserveCompletedTurn(completed RetentionCompletion) {
	opportunities := make([]learning.KnowledgeOpportunity, 0, len(completed.KnowledgeTasks))
	for _, task := range completed.KnowledgeTasks {
		opportunities = append(opportunities, learning.KnowledgeOpportunity{Task: task})
	}
	retention.service.ObserveCompletedTurn(learning.CompletedTurn{
		ConversationID:         completed.ConversationID,
		ExtractPersonalMemory:  completed.ExtractPersonalMemory,
		KnowledgeOpportunities: opportunities,
	})
}

func (retention *longSessionRetention) TakeCommittedCoverage(conversationID string) bool {
	return retention.service.TakeCommittedCoverage(conversationID)
}

func (retention *longSessionRetention) Close() {
	retention.service.Close()
}

type longSessionEventRecorder struct {
	mu     sync.Mutex
	byTurn map[string][]session.Event
}

func newLongSessionEventRecorder() *longSessionEventRecorder {
	return &longSessionEventRecorder{byTurn: make(map[string][]session.Event)}
}

func (recorder *longSessionEventRecorder) record(event session.Event) {
	recorder.mu.Lock()
	recorder.byTurn[event.TurnID] = append(recorder.byTurn[event.TurnID], event)
	recorder.mu.Unlock()
}

func (recorder *longSessionEventRecorder) events(turnID string) []session.Event {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return append([]session.Event(nil), recorder.byTurn[turnID]...)
}

func TestPostgresLongSessionSurvivesRepeatedCompactionAndServiceRestart(t *testing.T) {
	started := time.Now()
	store, pool, cleanup := openCompanionIntegrationStore(t)
	defer cleanup()
	const characterID = "character-long-session-stability"
	bootstrap, err := store.OpenOrCreateCharacterConversation(characterID)
	if err != nil {
		t.Fatalf("OpenOrCreateCharacterConversation: %v", err)
	}
	conversationID := bootstrap.Conversation.ID
	provider := &longSessionModel{}
	recorder := newLongSessionEventRecorder()
	service := newLongSessionService(t, store, characterID, conversationID, provider, recorder)
	t.Cleanup(func() { _ = service.Close() })

	outcomes := make([]TurnOutcome, 0, longSessionTurnCount)
	var restartRevision, restartCutoff uint64
	for index := 0; index < longSessionTurnCount; index++ {
		outcome, submitErr := service.SubmitCompiledTurn(SubmitCompiledTurnRequest{
			ConversationID:        conversationID,
			Input:                 fmt.Sprintf("轮次-%02d-%s", index+1, strings.Repeat("甲", longSessionInputRunes)),
			MaxOutputTokens:       160,
			AvailableVisualStates: []reply.VisualState{{ID: "idle", Description: "idle"}},
		})
		if submitErr != nil {
			t.Fatalf("SubmitCompiledTurn(%d): %v", index+1, submitErr)
		}
		if strings.TrimSpace(outcome.ResponseText) == "" {
			t.Fatalf("Turn %d returned an empty response", index+1)
		}
		outcomes = append(outcomes, outcome)
		waitLongSessionIdle(t, service)
		assertLongSessionBackgroundHealthy(t, service)
		assertLongSessionTurnEvidence(t, store, recorder, outcome, index+1)

		if index+1 != longSessionRestartAfter {
			continue
		}
		beforeRestart, loadErr := store.LoadConversation(conversationID)
		if loadErr != nil {
			t.Fatalf("LoadConversation before restart: %v", loadErr)
		}
		if len(beforeRestart.Messages) != longSessionRestartAfter*2 || beforeRestart.PromptWindow.Summary == nil || beforeRestart.PromptWindow.CutoffMessageSequence == 0 {
			t.Fatalf(
				"pre-restart state messages=%d revision=%d cutoff=%d has_summary=%t",
				len(beforeRestart.Messages), beforeRestart.PromptWindow.Revision,
				beforeRestart.PromptWindow.CutoffMessageSequence, beforeRestart.PromptWindow.Summary != nil,
			)
		}
		restartRevision = beforeRestart.PromptWindow.Revision
		restartCutoff = beforeRestart.PromptWindow.CutoffMessageSequence
		if closeErr := service.Close(); closeErr != nil {
			t.Fatalf("close pre-restart service: %v", closeErr)
		}
		reopened, reopenErr := store.OpenOrCreateCharacterConversation(characterID)
		if reopenErr != nil {
			t.Fatalf("reopen conversation: %v", reopenErr)
		}
		if reopened.Conversation.ID != conversationID || len(reopened.Messages) != longSessionRestartAfter*2 || reopened.PromptWindow.Revision != restartRevision || reopened.PromptWindow.CutoffMessageSequence != restartCutoff {
			t.Fatalf(
				"reopened state same_conversation=%t messages=%d revision=%d cutoff=%d",
				reopened.Conversation.ID == conversationID, len(reopened.Messages),
				reopened.PromptWindow.Revision, reopened.PromptWindow.CutoffMessageSequence,
			)
		}
		service = newLongSessionService(t, store, characterID, conversationID, provider, recorder)
	}

	waitLongSessionIdle(t, service)
	assertLongSessionBackgroundHealthy(t, service)
	full, err := store.LoadConversation(conversationID)
	if err != nil {
		t.Fatalf("LoadConversation after long session: %v", err)
	}
	assertLongSessionTranscript(t, full.Messages, outcomes)
	assertLongSessionTurnRows(t, pool, conversationID)

	active, err := store.history.LoadConversationPrompt(conversationID)
	if err != nil {
		t.Fatalf("LoadConversationPrompt: %v", err)
	}
	if full.PromptWindow.Revision <= restartRevision || full.PromptWindow.CutoffMessageSequence <= restartCutoff || full.PromptWindow.Summary == nil {
		t.Fatalf(
			"prompt window did not advance across restart: before_revision=%d before_cutoff=%d after_revision=%d after_cutoff=%d has_summary=%t",
			restartRevision, restartCutoff, full.PromptWindow.Revision,
			full.PromptWindow.CutoffMessageSequence, full.PromptWindow.Summary != nil,
		)
	}
	assertLongSessionActivePrompt(t, active.Messages, full.PromptWindow.CutoffMessageSequence, len(full.Messages))
	compactions := countLongSessionCompactions(t, store, outcomes)
	if compactions < 4 {
		t.Fatalf("successful L3 compactions = %d, want at least 4", compactions)
	}
	if continuation, found, loadErr := store.runtime.LoadLaneContinuation(conversationID, string(model.PromptLaneRespond)); loadErr != nil {
		t.Fatalf("LoadLaneContinuation: %v", loadErr)
	} else if found {
		t.Fatalf("stale respond continuation survived long session: revision=%d", continuation.WindowRevision)
	}
	if _, reserveErr := service.reserveTurn(conversationID); reserveErr != nil {
		t.Fatalf("conversation gate did not converge: %v", reserveErr)
	}
	service.endTurn(conversationID, "")
	respondRequests, compactRequests := provider.counts()
	if respondRequests != longSessionTurnCount || compactRequests < compactions {
		t.Fatalf("model requests respond=%d compact=%d ledger_compactions=%d", respondRequests, compactRequests, compactions)
	}
	t.Logf(
		"long-session turns=%d messages=%d compactions=%d revision=%d cutoff=%d active_messages=%d elapsed_ms=%d",
		longSessionTurnCount, len(full.Messages), compactions, full.PromptWindow.Revision,
		full.PromptWindow.CutoffMessageSequence, len(active.Messages), time.Since(started).Milliseconds(),
	)
}

func newLongSessionService(
	t *testing.T,
	store *companionIntegrationStores,
	characterID, conversationID string,
	provider ModelPort,
	recorder *longSessionEventRecorder,
) *Service {
	t.Helper()
	service := newCompanionIntegrationService(store, characterID, provider)
	AttachConfigSource(service, hardPressureIntegrationConfig{})
	AttachRetention(service, newLongSessionRetention())
	mustBindDesktopInteraction(t, service, conversationID)
	attachSuccessfulTestSurface(t, service, recorder.record)
	return service
}

func waitLongSessionIdle(t *testing.T, service *Service) {
	t.Helper()
	retention, ok := service.retention.(*longSessionRetention)
	if !ok {
		t.Fatal("long-session retention adapter is unavailable")
	}
	deadline := time.NewTimer(longSessionIdleTimeout)
	defer deadline.Stop()
	for service.ActiveBackgroundJobs() != 0 {
		select {
		case <-deadline.C:
			t.Fatalf("background jobs did not converge: active=%d", service.ActiveBackgroundJobs())
		case <-retention.completed:
		}
	}
}

func assertLongSessionBackgroundHealthy(t *testing.T, service *Service) {
	t.Helper()
	if jobs := service.ActiveBackgroundJobs(); jobs != 0 {
		t.Fatalf("active background jobs = %d", jobs)
	}
	service.backgroundErrorMu.Lock()
	err := service.backgroundError
	service.backgroundErrorMu.Unlock()
	if err != nil {
		t.Fatalf("long-session background error: %T", err)
	}
}

func assertLongSessionTurnEvidence(
	t *testing.T,
	store *companionIntegrationStores,
	recorder *longSessionEventRecorder,
	outcome TurnOutcome,
	turnNumber int,
) {
	t.Helper()
	events := recorder.events(outcome.TurnID)
	completed := terminalEventCount(events, lifecycle.StateCompleted)
	failed := terminalEventCount(events, lifecycle.StateFailed)
	interrupted := terminalEventCount(events, lifecycle.StateInterrupted)
	if completed != 1 || failed != 0 || interrupted != 0 {
		t.Fatalf(
			"Turn %d terminal events are invalid: completed=%d failed=%d interrupted=%d total=%d",
			turnNumber, completed, failed, interrupted, len(events),
		)
	}
	ledger, err := store.ListTurnRuntimeEvents(outcome.ConversationID, outcome.TurnID)
	if err != nil {
		t.Fatalf("ListTurnRuntimeEvents(%d): %v", turnNumber, err)
	}
	hasModel := hasRuntimeLedgerType(ledger, runtimeLedgerEventModel)
	hasTerminal := hasRuntimeLedgerType(ledger, runtimeLedgerEventTerminal)
	if !hasModel || !hasTerminal {
		t.Fatalf("Turn %d runtime ledger is incomplete: model=%t terminal=%t events=%d", turnNumber, hasModel, hasTerminal, len(ledger))
	}
}

func assertLongSessionTranscript(t *testing.T, messages []history.MessageRecord, outcomes []TurnOutcome) {
	t.Helper()
	if len(messages) != longSessionTurnCount*2 {
		t.Fatalf("full transcript messages = %d, want %d", len(messages), longSessionTurnCount*2)
	}
	if len(outcomes) != longSessionTurnCount {
		t.Fatalf("outcomes = %d, want %d", len(outcomes), longSessionTurnCount)
	}
	seenTurns := make(map[string]struct{}, longSessionTurnCount)
	for index := 0; index < len(messages); index += 2 {
		userMessage, assistantMessage := messages[index], messages[index+1]
		turnIndex := index / 2
		if userMessage.Sequence != uint64(index+1) || assistantMessage.Sequence != uint64(index+2) ||
			userMessage.TurnID == "" || userMessage.TurnID != assistantMessage.TurnID ||
			userMessage.Role != "user" || assistantMessage.Role != "assistant" ||
			userMessage.TurnID != outcomes[turnIndex].TurnID {
			t.Fatalf(
				"transcript pair %d is invalid: sequences=(%d,%d) same_turn=%t roles=(%s,%s) matches_outcome=%t",
				turnIndex+1, userMessage.Sequence, assistantMessage.Sequence,
				userMessage.TurnID == assistantMessage.TurnID, userMessage.Role, assistantMessage.Role,
				userMessage.TurnID == outcomes[turnIndex].TurnID,
			)
		}
		if _, exists := seenTurns[userMessage.TurnID]; exists {
			t.Fatalf("duplicate transcript Turn %q", userMessage.TurnID)
		}
		seenTurns[userMessage.TurnID] = struct{}{}
	}
}

func assertLongSessionTurnRows(t *testing.T, pool *pgxpool.Pool, conversationID string) {
	t.Helper()
	rows, err := pool.Query(t.Context(), `
SELECT status, COUNT(*)
FROM conversation_turns
WHERE conversation_id = $1
GROUP BY status
ORDER BY status`, conversationID)
	if err != nil {
		t.Fatalf("query conversation_turns: %v", err)
	}
	defer rows.Close()
	counts := make(map[string]int)
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			t.Fatalf("scan conversation_turns: %v", err)
		}
		counts[status] = count
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate conversation_turns: %v", err)
	}
	if len(counts) != 1 || counts[string(lifecycle.StateCompleted)] != longSessionTurnCount {
		t.Fatalf("conversation Turn states = %#v", counts)
	}
}

func assertLongSessionActivePrompt(t *testing.T, messages []history.MessageRecord, cutoff uint64, fullCount int) {
	t.Helper()
	if len(messages) == 0 || len(messages) >= fullCount || len(messages)%2 != 0 {
		t.Fatalf("active prompt messages = %d, full transcript = %d", len(messages), fullCount)
	}
	for index := 0; index < len(messages); index += 2 {
		userMessage, assistantMessage := messages[index], messages[index+1]
		if userMessage.Sequence <= cutoff || assistantMessage.Sequence <= cutoff ||
			userMessage.TurnID != assistantMessage.TurnID || userMessage.Role != "user" || assistantMessage.Role != "assistant" {
			t.Fatalf(
				"active prompt pair %d is invalid: cutoff=%d sequences=(%d,%d) same_turn=%t roles=(%s,%s)",
				index/2+1, cutoff, userMessage.Sequence, assistantMessage.Sequence,
				userMessage.TurnID == assistantMessage.TurnID, userMessage.Role, assistantMessage.Role,
			)
		}
	}
}

func countLongSessionCompactions(t *testing.T, store *companionIntegrationStores, outcomes []TurnOutcome) int {
	t.Helper()
	total := 0
	for _, outcome := range outcomes {
		events, err := store.ListTurnRuntimeEvents(outcome.ConversationID, outcome.TurnID)
		if err != nil {
			t.Fatalf("ListTurnRuntimeEvents(%s): %v", outcome.TurnID, err)
		}
		for _, event := range events {
			if event.EventType != runtimeLedgerEventCompaction || event.State == nil || *event.State != string(lifecycle.StateCompleted) {
				continue
			}
			var metadata struct {
				Layer   string `json:"layer"`
				Trigger string `json:"trigger"`
			}
			if err := json.Unmarshal([]byte(event.MetadataJSON), &metadata); err != nil {
				t.Fatalf("decode compaction ledger for Turn %s: %v", outcome.TurnID, err)
			}
			if metadata.Layer == "l3" && metadata.Trigger == "hard_watermark" {
				total++
			}
		}
	}
	return total
}

var _ RetentionPort = (*longSessionRetention)(nil)
