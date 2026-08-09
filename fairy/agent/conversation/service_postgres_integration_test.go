//go:build integration

package conversation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"fairy/agent/conversation/delivery"
	"fairy/agent/conversation/lifecycle"
	"fairy/agent/reply"
	"fairy/agent/tool"
	"fairy/context/character"
	historycompaction "fairy/context/history/compaction"
	historyexpr "fairy/context/history/expression"
	historyruntime "fairy/context/history/runtime"
	history "fairy/context/history/transcript"
	"fairy/context/knowledge"
	"fairy/context/memory/extraction"
	"fairy/context/memory/personal"
	"fairy/context/social"
	"fairy/runtime/config"
	coredb "fairy/runtime/database"
	"fairy/runtime/model"
	"fairy/runtime/observability"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	initiative "fairy/agent/presence"
	"fairy/transport/session"
)

type companionInitiativeTestTurnStarter struct {
	service *Service
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

func TestCompactedTranscriptRecallIsConversationAndCutoffScopedIntegration(t *testing.T) {
	stores, pool, cleanup := openCompanionIntegrationStore(t)
	defer cleanup()

	first, err := stores.OpenOrCreateCharacterConversation("character-transcript-first")
	if err != nil {
		t.Fatal(err)
	}
	second, err := stores.OpenOrCreateCharacterConversation("character-transcript-second")
	if err != nil {
		t.Fatal(err)
	}
	oldTurn, err := stores.BeginTurn(first.Conversation.ID, "还记得海边约定吗")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stores.CompleteTurn(first.Conversation.ID, oldTurn.ID, "记得，等夏天一起去。 "); err != nil {
		t.Fatal(err)
	}
	cutoff := oldTurn.UserMessage.Sequence + 1
	newTurn, err := stores.BeginTurn(first.Conversation.ID, "海边约定后来改成了秋天")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stores.CompleteTurn(first.Conversation.ID, newTurn.ID, "这条在 cutoff 之后。 "); err != nil {
		t.Fatal(err)
	}
	foreignTurn, err := stores.BeginTurn(second.Conversation.ID, "海边约定只属于另一个会话")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stores.CompleteTurn(second.Conversation.ID, foreignTurn.ID, "不能泄漏。 "); err != nil {
		t.Fatal(err)
	}

	result, err := stores.history.SearchCompactedTranscript(t.Context(), first.Conversation.ID, cutoff, "海边约定", history.MaxCompactedTranscriptTurns)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Turns) != 1 || result.Turns[0].TurnID != oldTurn.ID || len(result.Turns[0].Messages) != 2 {
		t.Fatalf("recall = %#v", result)
	}
	for _, message := range result.Turns[0].Messages {
		if message.ConversationID != first.Conversation.ID || message.Sequence > cutoff || strings.Contains(message.Content, "cutoff 之后") || strings.Contains(message.Content, "不能泄漏") {
			t.Fatalf("out-of-scope message = %#v", message)
		}
	}
	empty, err := stores.history.SearchCompactedTranscript(t.Context(), first.Conversation.ID, cutoff, "完全不存在的内容", history.MaxCompactedTranscriptTurns)
	if err != nil {
		t.Fatal(err)
	}
	if len(empty.Turns) != 0 || empty.Truncated {
		t.Fatalf("empty recall = %#v", empty)
	}
	var hasIndex bool
	if err := pool.QueryRow(t.Context(), "SELECT to_regclass('conversation_messages_content_trgm') IS NOT NULL").Scan(&hasIndex); err != nil {
		t.Fatal(err)
	}
	if !hasIndex {
		t.Fatal("conversation_messages_content_trgm is missing")
	}
}

func TestCompactedTranscriptRecallReportsTurnLimitIntegration(t *testing.T) {
	stores, _, cleanup := openCompanionIntegrationStore(t)
	defer cleanup()
	bootstrap, err := stores.OpenOrCreateCharacterConversation("character-transcript-limit")
	if err != nil {
		t.Fatal(err)
	}
	var cutoff uint64
	for index := 0; index < 4; index++ {
		turn, err := stores.BeginTurn(bootstrap.Conversation.ID, fmt.Sprintf("共同暗号 海风 %d", index))
		if err != nil {
			t.Fatal(err)
		}
		message, err := stores.CompleteTurn(bootstrap.Conversation.ID, turn.ID, fmt.Sprintf("这一轮回答 %d", index))
		if err != nil {
			t.Fatal(err)
		}
		cutoff = message.Sequence
	}
	result, err := stores.history.SearchCompactedTranscript(t.Context(), bootstrap.Conversation.ID, cutoff, "共同暗号", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Turns) != 2 || !result.Truncated {
		t.Fatalf("limited recall = %#v", result)
	}
	for _, turn := range result.Turns {
		if len(turn.Messages) != 2 {
			t.Fatalf("turn was not returned as a semantic unit: %#v", turn)
		}
	}
}

type companionIntegrationModel struct {
	chains []reply.ReplyChain
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
		return companionIntegrationModel{chains: []reply.ReplyChain{
			{VisualState: "idle", Text: "第一拍"},
			{VisualState: "idle", Text: "违规第二拍"},
		}}.ExecuteRequestContext(context.Background(), request)
	}
	return companionIntegrationModel{chains: []reply.ReplyChain{{VisualState: "idle", Text: "重试后的即时接话。"}}}.ExecuteRequestContext(context.Background(), request)
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
	return companionIntegrationModel{chains: []reply.ReplyChain{{VisualState: "idle", Text: "第二次严格返回。"}}}.ExecuteRequestContext(context.Background(), request)
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
	return companionIntegrationModel{chains: []reply.ReplyChain{{VisualState: "idle", Text: "群聊回复。"}}}.ExecuteRequestContext(context.Background(), request)
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

func (s *countingPromptContextStore) LoadConversationPrompt(conversationID string) (history.ConversationPromptContext, error) {
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

type externalOwnerIdentity struct{}

func (externalOwnerIdentity) IsOwner(string, string) (bool, error) { return false, nil }

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
	return companionIntegrationModel{chains: []reply.ReplyChain{{VisualState: "idle", Text: "收到。"}}}.ExecuteRequestContext(ctx, request)
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

type transcriptToolIntegrationModel struct {
	requests []model.CompiledPromptRequest
}

func (provider *transcriptToolIntegrationModel) ExecuteRequestContext(_ context.Context, request model.CompiledPromptRequest) ([]model.StreamEvent, error) {
	provider.requests = append(provider.requests, request)
	if len(provider.requests) == 1 {
		return []model.StreamEvent{{Type: "function_calls", FunctionCalls: []model.FunctionCall{{
			CallID: "history-call-1", Name: tool.ConversationHistorySearch, Arguments: `{"query":"海边约定"}`,
		}}}}, nil
	}
	return companionIntegrationModel{chains: []reply.ReplyChain{{VisualState: "idle", Text: "我记得，夏天一起去海边。"}}}.ExecuteRequestContext(context.Background(), request)
}

func (*transcriptToolIntegrationModel) ExecutePrompt(model.PromptLane, string, uint32, []model.PromptItem, string) ([]model.StreamEvent, error) {
	return nil, errors.New("unexpected ExecutePrompt")
}

func (provider *emptyMemoryToolIntegrationModel) ExecuteRequestContext(_ context.Context, request model.CompiledPromptRequest) ([]model.StreamEvent, error) {
	provider.requests = append(provider.requests, request)
	if len(provider.requests) <= tool.ModelDrivenBudget(desktopResolved()) {
		callID := fmt.Sprintf("memory-call-%d", len(provider.requests))
		return []model.StreamEvent{{Type: "function_calls", FunctionCalls: []model.FunctionCall{{
			CallID: callID, Name: tool.MemorySearch, Arguments: `{"query":"你好"}`,
		}}}}, nil
	}
	return companionIntegrationModel{chains: []reply.ReplyChain{{VisualState: "idle", Text: "你好呀。"}}}.ExecuteRequestContext(context.Background(), request)
}

func (*emptyMemoryToolIntegrationModel) ExecutePrompt(model.PromptLane, string, uint32, []model.PromptItem, string) ([]model.StreamEvent, error) {
	return nil, errors.New("unexpected ExecutePrompt")
}

func (provider *desktopToolIntegrationModel) ExecuteRequestContext(_ context.Context, request model.CompiledPromptRequest) ([]model.StreamEvent, error) {
	provider.requests = append(provider.requests, request)
	if len(provider.requests) == 1 {
		return []model.StreamEvent{{Type: "function_calls", FunctionCalls: []model.FunctionCall{{CallID: "desktop-call-1", Name: tool.DesktopObserve, Arguments: `{}`}}}}, nil
	}
	return companionIntegrationModel{chains: []reply.ReplyChain{{VisualState: "idle", Text: "我看到了，我们继续。"}}}.ExecuteRequestContext(context.Background(), request)
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
			CallID: "web-1", Name: tool.WebSearch, Arguments: `{"query":"公开新闻"}`,
		}}}}, nil
	}
	return companionIntegrationModel{chains: []reply.ReplyChain{{VisualState: "idle", Text: "公开消息。"}}}.ExecuteRequestContext(context.Background(), request)
}

func (*groupWebIntegrationModel) ExecutePrompt(model.PromptLane, string, uint32, []model.PromptItem, string) ([]model.StreamEvent, error) {
	return []model.StreamEvent{{Type: "text_delta", Data: "摘要"}}, nil
}

type groupWebSearchStub struct{}

func (groupWebSearchStub) Search(context.Context, string, int) ([]knowledge.WebSearchHit, error) {
	return []knowledge.WebSearchHit{{Title: "公开新闻标题", URL: "https://example.com/news", Snippet: "公开摘要"}}, nil
}

func (groupWebSearchStub) Close() error { return nil }

type terminalFailureTurnStore struct {
	base         TurnStore
	completeErr  error
	interruptErr error
}

func mustBindDesktopInteraction(t *testing.T, service *Service, conversationID string) {
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

func (m terminalFailureTurnStore) BeginCorrelatedTurn(conversationID, userMessage, messageID string) (history.PersistedTurn, error) {
	return m.base.BeginCorrelatedTurn(conversationID, userMessage, messageID)
}

func (m terminalFailureTurnStore) BeginInitiationTurn(conversationID string, evidenceIDs []string) (history.PersistedTurn, error) {
	return m.base.BeginInitiationTurn(conversationID, evidenceIDs)
}

func (m terminalFailureTurnStore) CompleteExpressionTurnForPolicy(string, string, string, []historyexpr.Part, bool) (history.MessageRecord, error) {
	return history.MessageRecord{}, m.completeErr
}

func (m terminalFailureTurnStore) InterruptExpressionTurn(string, string, string, []historyexpr.Part) (*history.MessageRecord, error) {
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
	ports := store.ports()
	promptContext := &countingPromptContextStore{base: ports.turn.promptContext}
	ports.turn.promptContext = promptContext
	characters := &countingCharacterLookup{record: companionIntegrationCharacter(characterID)}
	owner := &countingOwnerIdentity{}
	provider := &preparationCountingModel{promptContext: promptContext, characters: characters, owner: owner}
	service := newServiceWithPorts("", ports, provider, nil)
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
	attachSuccessfulTestSurface(t, service, nil)
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
	ports := store.ports()
	promptContext := &countingPromptContextStore{base: ports.turn.promptContext}
	ports.turn.promptContext = promptContext
	characters := &countingCharacterLookup{record: companionIntegrationCharacter(characterID)}
	provider := &preparationCountingModel{promptContext: promptContext, characters: characters}
	service := newServiceWithPorts("", ports, provider, nil)
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
	service := newCompanionIntegrationService(store, "character-paced", companionIntegrationModel{chains: []reply.ReplyChain{
		{VisualState: "idle", Text: "第一拍。"},
		{VisualState: "happy", Text: "第二拍"},
	}})
	metrics := observability.NewMessageMetrics()
	t.Cleanup(metrics.Close)
	persistedTrace := make(chan observability.MessageTraceDetail, 1)
	metrics.SetTerminalSink(func(detail observability.MessageTraceDetail) bool {
		persistedTrace <- detail
		return true
	})
	AttachMessageTelemetry(service, metrics)
	mustBindDesktopInteraction(t, service, bootstrap.Conversation.ID)
	var mu sync.Mutex
	var events []session.Event
	attachSuccessfulTestSurface(t, service, func(event session.Event) {
		mu.Lock()
		events = append(events, event)
		mu.Unlock()
	})

	outcome, err := service.SubmitCompiledTurn(SubmitCompiledTurnRequest{
		ConversationID:        bootstrap.Conversation.ID,
		Input:                 "请分两拍告诉我",
		MaxOutputTokens:       160,
		AvailableVisualStates: []reply.VisualState{{ID: "idle", Description: "idle"}, {ID: "happy", Description: "happy"}},
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
	if terminalEventCount(events, lifecycle.StateCompleted) != 1 || terminalEventCount(events, lifecycle.StateInterrupted) != 0 {
		t.Fatalf("terminal events = %#v", events)
	}
	if len(events) == 0 || events[len(events)-1].State != string(lifecycle.StateCompleted) {
		t.Fatalf("completed was not the final event: %#v", events)
	}
	select {
	case detail := <-persistedTrace:
		receipts := 0
		for _, span := range detail.Spans {
			if span.Operation == "Surface 回执" {
				receipts++
				if span.Status != "completed" || span.Attributes["status"] != "succeeded" {
					t.Fatalf("delivery receipt = %#v", span)
				}
			}
		}
		if detail.Status != "completed" || receipts != 2 {
			t.Fatalf("terminal trace = %#v", detail)
		}
	case <-time.After(time.Second):
		t.Fatal("terminal trace was not persisted")
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

func TestPostgresCompanionDeliveryFailureStopsFollowingBeats(t *testing.T) {
	store, _, cleanup := openCompanionIntegrationStore(t)
	defer cleanup()
	bootstrap, err := store.OpenOrCreateCharacterConversation("character-delivery-failed")
	if err != nil {
		t.Fatal(err)
	}
	service := newCompanionIntegrationService(store, bootstrap.Conversation.CharacterID, companionIntegrationModel{chains: []reply.ReplyChain{
		{VisualState: "idle", Text: "第一拍。"},
		{VisualState: "idle", Text: "不应发布的第二拍"},
	}})
	mustBindDesktopInteraction(t, service, bootstrap.Conversation.ID)
	var events []session.Event
	AttachEventEmitter(service, func(event session.Event) {
		events = append(events, event)
		var payload lifecycle.BeatReadyPayload
		if json.Unmarshal(event.Payload, &payload) != nil || payload.Type != "beat.ready" || payload.Kind != reply.BeatKindFinal {
			return
		}
		if reportErr := service.ReportExpressionDelivery(session.ExpressionDeliveryResult{
			ConversationID: event.ConversationID,
			TurnID:         event.TurnID,
			BeatID:         payload.BeatID,
			Status:         session.ExpressionDeliveryFailed,
			ErrorMessage:   "test surface rejected expression",
		}); reportErr != nil {
			t.Errorf("ReportExpressionDelivery: %v", reportErr)
		}
	})

	_, submitErr := service.SubmitCompiledTurn(SubmitCompiledTurnRequest{
		ConversationID:        bootstrap.Conversation.ID,
		Input:                 "测试失败停止",
		MaxOutputTokens:       160,
		AvailableVisualStates: []reply.VisualState{{ID: "idle", Description: "idle"}},
	})
	if submitErr == nil || !strings.Contains(submitErr.Error(), "surface expression delivery failed") {
		t.Fatalf("SubmitCompiledTurn() error = %v", submitErr)
	}
	if beats := finalBeatEvents(events); len(beats) != 1 || beats[0].ChainIndex != 0 {
		t.Fatalf("final beats = %#v", beats)
	}
	if terminalEventCount(events, lifecycle.StateFailed) != 1 || terminalEventCount(events, lifecycle.StateCompleted) != 0 {
		t.Fatalf("terminal events = %#v", events)
	}
}

func TestPostgresCompanionDeliveryTimeoutFailsWithoutCompleting(t *testing.T) {
	store, _, cleanup := openCompanionIntegrationStore(t)
	defer cleanup()
	bootstrap, err := store.OpenOrCreateCharacterConversation("character-delivery-timeout")
	if err != nil {
		t.Fatal(err)
	}
	service := newCompanionIntegrationService(store, bootstrap.Conversation.CharacterID, companionIntegrationModel{chains: []reply.ReplyChain{
		{VisualState: "idle", Text: "等待 Surface 回执。"},
		{VisualState: "idle", Text: "不应发布的第二拍"},
	}})
	service.expressionDeliveries = delivery.NewRegistry(5 * time.Millisecond)
	mustBindDesktopInteraction(t, service, bootstrap.Conversation.ID)
	var events []session.Event
	AttachEventEmitter(service, func(event session.Event) { events = append(events, event) })

	_, submitErr := service.SubmitCompiledTurn(SubmitCompiledTurnRequest{
		ConversationID:        bootstrap.Conversation.ID,
		Input:                 "测试回执超时",
		MaxOutputTokens:       160,
		AvailableVisualStates: []reply.VisualState{{ID: "idle", Description: "idle"}},
	})
	if submitErr == nil || !strings.Contains(submitErr.Error(), "surface expression delivery timed out") {
		t.Fatalf("SubmitCompiledTurn() error = %v", submitErr)
	}
	if beats := finalBeatEvents(events); len(beats) != 1 || beats[0].ChainIndex != 0 {
		t.Fatalf("final beats = %#v", beats)
	}
	if terminalEventCount(events, lifecycle.StateFailed) != 1 || terminalEventCount(events, lifecycle.StateCompleted) != 0 {
		t.Fatalf("terminal events = %#v", events)
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
			MaxOutputTokens: 160, AvailableVisualStates: []reply.VisualState{{ID: "idle", Description: "idle"}},
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
	if len(firstTools) == 0 || firstTools[len(firstTools)-1].Name != tool.DesktopObserve {
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
			toolCall = item.ToolCallID == "desktop-call-1" && item.ToolName == tool.DesktopObserve
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
		AvailableVisualStates: []reply.VisualState{{ID: "idle", Description: "idle"}},
	})
	if err != nil {
		t.Fatalf("SubmitCompiledTurn: %v", err)
	}
	if outcome.ResponseText != "你好呀。" || len(provider.requests) != tool.ModelDrivenBudget(desktopResolved())+1 {
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

func TestPostgresTranscriptToolUsesCompactedHistoryWithoutChangingStableState(t *testing.T) {
	stores, pool, cleanup := openCompanionIntegrationStore(t)
	defer cleanup()
	bootstrap, err := stores.OpenOrCreateCharacterConversation("character-transcript-tool")
	if err != nil {
		t.Fatal(err)
	}
	oldTurn, err := stores.BeginTurn(bootstrap.Conversation.ID, "还记得海边约定吗")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stores.CompleteTurn(bootstrap.Conversation.ID, oldTurn.ID, "记得，夏天一起去海边。 "); err != nil {
		t.Fatal(err)
	}
	cutoff := oldTurn.UserMessage.Sequence + 1
	if _, err := pool.Exec(t.Context(), `UPDATE prompt_windows SET revision = 2, summary = $2, cutoff_message_sequence = $3 WHERE conversation_id = $1`, bootstrap.Conversation.ID, structuredCompactSummary, cutoff); err != nil {
		t.Fatal(err)
	}
	provider := &transcriptToolIntegrationModel{}
	service := newCompanionIntegrationService(stores, "character-transcript-tool", provider)
	mustBindDesktopInteraction(t, service, bootstrap.Conversation.ID)

	outcome, err := service.SubmitCompiledTurn(SubmitCompiledTurnRequest{
		ConversationID: bootstrap.Conversation.ID, Input: "你还记得那件事吗", MaxOutputTokens: 160,
		AvailableVisualStates: []reply.VisualState{{ID: "idle", Description: "idle"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.ResponseText != "我记得，夏天一起去海边。" || len(provider.requests) != 2 {
		t.Fatalf("outcome = %#v, requests = %d", outcome, len(provider.requests))
	}
	if provider.requests[0].CacheInput == nil || provider.requests[1].CacheInput == nil || *provider.requests[0].CacheInput != *provider.requests[1].CacheInput {
		t.Fatalf("cache inputs changed: %#v %#v", provider.requests[0].CacheInput, provider.requests[1].CacheInput)
	}
	var contextProjection tool.TranscriptContext
	var foundCall, foundResult, foundContext bool
	for _, item := range provider.requests[1].Input {
		switch item.Type {
		case model.PromptItemToolCall:
			foundCall = item.ToolCallID == "history-call-1" && item.ToolName == tool.ConversationHistorySearch
		case model.PromptItemToolResult:
			foundResult = item.ToolCallID == "history-call-1"
		case model.PromptItemContextData:
			if strings.Contains(item.Content, `"contextType":"compacted_transcript_recall"`) {
				if err := json.Unmarshal([]byte(item.Content), &contextProjection); err != nil {
					t.Fatal(err)
				}
				foundContext = true
			}
		}
	}
	if !foundCall || !foundResult || !foundContext || len(contextProjection.Turns) != 1 || len(contextProjection.Turns[0].Messages) != 2 {
		t.Fatalf("post-tool input = %#v", provider.requests[1].Input)
	}
	ledger, err := stores.ListTurnRuntimeEvents(outcome.ConversationID, outcome.TurnID)
	if err != nil {
		t.Fatal(err)
	}
	var inspected *tool.TranscriptContext
	for _, event := range ledger {
		if event.EventType != runtimeLedgerEventTool {
			continue
		}
		var metadata struct {
			Tool   string                       `json:"tool"`
			Detail tool.TranscriptRuntimeDetail `json:"detail"`
		}
		if json.Unmarshal([]byte(event.MetadataJSON), &metadata) == nil && metadata.Tool == tool.ConversationHistorySearch {
			inspected = metadata.Detail.Result
		}
	}
	if inspected == nil || !reflect.DeepEqual(*inspected, contextProjection) {
		t.Fatalf("inspected = %#v, model projection = %#v", inspected, contextProjection)
	}
	after, err := stores.LoadConversation(bootstrap.Conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.PromptWindow.Revision != 2 || after.PromptWindow.CutoffMessageSequence != cutoff {
		t.Fatalf("prompt window changed = %#v", after.PromptWindow)
	}
	var memoryCount int
	if err := pool.QueryRow(t.Context(), "SELECT count(*) FROM personal_memories").Scan(&memoryCount); err != nil {
		t.Fatal(err)
	}
	if memoryCount != 0 {
		t.Fatalf("transcript search wrote %d memories", memoryCount)
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
	AttachDeferredTurnScheduler(service, immediateDeferredTurnScheduler{})
	mustBindDesktopInteraction(t, service, bootstrap.Conversation.ID)
	completed := make(chan session.Event, 1)
	attachSuccessfulTestSurface(t, service, func(event session.Event) {
		if event.State == string(lifecycle.StateCompleted) {
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
		if record.EventType == runtimeLedgerEventTerminal && record.State != nil && *record.State == string(lifecycle.StateCompleted) {
			completedTerminals++
		}
	}
	for _, state := range []lifecycle.State{lifecycle.StateInterpreting, lifecycle.StateGathering, lifecycle.StatePlanning, lifecycle.StateResponding} {
		if transitions[string(state)] != 1 {
			t.Fatalf("transition %q count = %d, all = %#v", state, transitions[string(state)], transitions)
		}
	}
	if completedTerminals != 1 {
		t.Fatalf("completed terminal count = %d", completedTerminals)
	}
}

type immediateDeferredTurnScheduler struct{}

func (immediateDeferredTurnScheduler) ScheduleDeferred(job func()) error {
	job()
	return nil
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
	attachSuccessfulTestSurface(t, service, func(event session.Event) {
		mu.Lock()
		events = append(events, event)
		mu.Unlock()
	})

	outcome, err := service.SubmitCompiledTurn(SubmitCompiledTurnRequest{
		ConversationID:        bootstrap.Conversation.ID,
		Input:                 "测试严格回复重试",
		MaxOutputTokens:       160,
		AvailableVisualStates: []reply.VisualState{{ID: "idle", Description: "idle"}},
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
	if terminalEventCount(events, lifecycle.StateCompleted) != 1 || terminalEventCount(events, lifecycle.StateFailed) != 0 {
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
		AvailableVisualStates: []reply.VisualState{{ID: "idle", Description: "idle"}},
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
	service := newCompanionIntegrationService(store, "character-cancel", companionIntegrationModel{chains: []reply.ReplyChain{
		{VisualState: "idle", Text: "已说出的第一拍。"},
		{VisualState: "idle", Text: "不应发布的第二拍"},
		{VisualState: "idle", Text: "不应发布的第三拍"},
	}})
	mustBindDesktopInteraction(t, service, bootstrap.Conversation.ID)
	var mu sync.Mutex
	var events []session.Event
	cancelled := make(chan error, 1)
	AttachEventEmitter(service, func(event session.Event) {
		mu.Lock()
		events = append(events, event)
		mu.Unlock()
		payload := decodeEventPayload[lifecycle.BeatReadyPayload](t, event.Payload)
		if payload.Type == "beat.ready" && payload.Kind == reply.BeatKindFinal && payload.ChainIndex == 0 {
			if err := service.ReportExpressionDelivery(session.ExpressionDeliveryResult{
				ConversationID: event.ConversationID, TurnID: event.TurnID, BeatID: payload.BeatID,
				Status: session.ExpressionDeliverySucceeded,
			}); err != nil {
				t.Errorf("ReportExpressionDelivery: %v", err)
				return
			}
			go func() {
				time.Sleep(10 * time.Millisecond)
				cancelled <- service.CancelTurn(event.ConversationID, event.TurnID)
			}()
		}
	})

	_, submitErr := service.SubmitCompiledTurn(SubmitCompiledTurnRequest{
		ConversationID:        bootstrap.Conversation.ID,
		Input:                 "说到一半就停",
		MaxOutputTokens:       160,
		AvailableVisualStates: []reply.VisualState{{ID: "idle", Description: "idle"}},
	})
	cancelErr := <-cancelled
	if !errors.Is(submitErr, ErrTurnInterrupted) || cancelErr != nil {
		t.Fatalf("SubmitCompiledTurn = %v, CancelTurn = %v", submitErr, cancelErr)
	}
	beats := finalBeatEvents(events)
	if len(beats) != 1 || beats[0].ChainIndex != 0 {
		t.Fatalf("final beats = %#v", beats)
	}
	if terminalEventCount(events, lifecycle.StateInterrupted) != 1 || terminalEventCount(events, lifecycle.StateCompleted) != 0 {
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
	ports := store.ports()
	ports.turn.turns = terminalFailureTurnStore{base: store, completeErr: errCompletePersistence}
	service := newCompanionIntegrationServiceWithPorts(ports, "character-terminal-failure", companionIntegrationModel{chains: []reply.ReplyChain{
		{VisualState: "idle", Text: "已发布但无法保存"},
	}})
	mustBindDesktopInteraction(t, service, bootstrap.Conversation.ID)
	var events []session.Event
	attachSuccessfulTestSurface(t, service, func(event session.Event) { events = append(events, event) })
	_, submitErr := service.SubmitCompiledTurn(SubmitCompiledTurnRequest{
		ConversationID:        bootstrap.Conversation.ID,
		Input:                 "测试持久化错误",
		MaxOutputTokens:       160,
		AvailableVisualStates: []reply.VisualState{{ID: "idle", Description: "idle"}},
	})
	if !errors.Is(submitErr, errCompletePersistence) {
		t.Fatalf("SubmitCompiledTurn = %v, want complete persistence error", submitErr)
	}
	if terminalEventCount(events, lifecycle.StateFailed) != 1 || terminalEventCount(events, lifecycle.StateCompleted) != 0 {
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
	ports := store.ports()
	ports.turn.turns = terminalFailureTurnStore{base: store, interruptErr: errInterruptPersistence}
	service := newCompanionIntegrationServiceWithPorts(ports, "character-interrupt-failure", companionIntegrationModel{chains: []reply.ReplyChain{
		{VisualState: "idle", Text: "第一拍"},
		{VisualState: "idle", Text: "第二拍"},
	}})
	mustBindDesktopInteraction(t, service, bootstrap.Conversation.ID)
	var events []session.Event
	cancelled := make(chan error, 1)
	AttachEventEmitter(service, func(event session.Event) {
		events = append(events, event)
		payload := decodeEventPayload[lifecycle.BeatReadyPayload](t, event.Payload)
		if payload.Type == "beat.ready" && payload.ChainIndex == 0 {
			if err := service.ReportExpressionDelivery(session.ExpressionDeliveryResult{
				ConversationID: event.ConversationID, TurnID: event.TurnID, BeatID: payload.BeatID,
				Status: session.ExpressionDeliverySucceeded,
			}); err != nil {
				t.Errorf("ReportExpressionDelivery: %v", err)
				return
			}
			go func() {
				time.Sleep(10 * time.Millisecond)
				cancelled <- service.CancelTurn(event.ConversationID, event.TurnID)
			}()
		}
	})
	_, submitErr := service.SubmitCompiledTurn(SubmitCompiledTurnRequest{
		ConversationID:        bootstrap.Conversation.ID,
		Input:                 "测试中断持久化错误",
		MaxOutputTokens:       160,
		AvailableVisualStates: []reply.VisualState{{ID: "idle", Description: "idle"}},
	})
	<-cancelled
	if !errors.Is(submitErr, errInterruptPersistence) || !errors.Is(submitErr, ErrTurnInterrupted) {
		t.Fatalf("SubmitCompiledTurn = %v, want interrupt and persistence errors", submitErr)
	}
	if terminalEventCount(events, lifecycle.StateFailed) != 1 || terminalEventCount(events, lifecycle.StateInterrupted) != 0 {
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
	if _, err := store.CreatePersonalMemory("preference", personal.Scope{Type: "global"}, privateFixture, 9000); err != nil {
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
		AvailableVisualStates: []reply.VisualState{{ID: "idle", Description: "idle"}},
	}); err != nil {
		t.Fatalf("SubmitCompiledTurn: %v", err)
	}
	provider.mu.Lock()
	request := provider.request
	provider.mu.Unlock()
	toolNames := make(map[string]bool, len(request.Tools))
	for _, spec := range request.Tools {
		toolNames[spec.Name] = true
		if spec.Name == tool.MemorySearch {
			t.Fatalf("group request exposes %q: %#v", tool.MemorySearch, request.Tools)
		}
	}
	for _, name := range []string{tool.PublicMemorySearch, tool.SocialContextSearch, tool.SocialExpressionSelect} {
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
	if _, err := store.StoreSocialMemoryEntries(context.Background(), social.SocialMemoryBatchInput{
		CharacterID: characterID, ConversationID: groupConversation.Conversation.ID,
		Entries: []social.SocialMemoryEntryInput{{
			Kind: social.SocialMemoryEpisode, Situation: "群里讨论实习焦虑", Content: "大家先听完项目经历再给建议", RecallCue: "实习焦虑 项目经历",
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
		AvailableVisualStates: []reply.VisualState{{ID: "idle", Description: "idle"}},
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
		AvailableVisualStates: []reply.VisualState{{ID: "idle", Description: "idle"}},
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

func TestPostgresExternalDirectTurnRepliesWithoutPrivateOrGroupMemory(t *testing.T) {
	store, pool, cleanup := openCompanionIntegrationStore(t)
	defer cleanup()
	const characterID = "character-external-direct"
	binding := session.Binding{Endpoint: session.EndpointIM, Facts: session.Facts{
		Audience: session.AudienceSingle, Initiation: session.InitiationDirect, Presentation: session.PresentationChat,
		PrincipalNamespace: "qq.onebot", PrincipalDigest: strings.Repeat("e", 64),
	}}
	bootstrap, err := store.OpenOrCreateEndpointConversation(characterID, binding, strings.Repeat("f", 64))
	if err != nil {
		t.Fatalf("OpenOrCreateEndpointConversation: %v", err)
	}
	ownerConversation, err := store.OpenOrCreateCharacterConversation(characterID)
	if err != nil {
		t.Fatalf("OpenOrCreateCharacterConversation: %v", err)
	}
	seedTurn, err := store.BeginTurn(ownerConversation.Conversation.ID, "主人提供个人偏好")
	if err != nil {
		t.Fatalf("BeginTurn(private fixture): %v", err)
	}
	if _, err := store.CompleteTurn(ownerConversation.Conversation.ID, seedTurn.ID, "已记录"); err != nil {
		t.Fatalf("CompleteTurn(private fixture): %v", err)
	}
	const privateFixture = "外部私聊绝不能读取的个人记忆-31af"
	if _, err := store.CreatePersonalMemory("preference", personal.Scope{Type: "global"}, privateFixture, 9000); err != nil {
		t.Fatalf("CreatePersonalMemory: %v", err)
	}
	provider := &capturingIntegrationModel{}
	service := newCompanionIntegrationService(store, characterID, provider)
	AttachProfileSource(service, rejectingGroupProfile{})
	AttachOwnerIdentityStore(service, externalOwnerIdentity{})
	if err := service.BindInteraction(bootstrap.Conversation.ID, binding); err != nil {
		t.Fatalf("BindInteraction: %v", err)
	}
	before := groupPrivacyJobCounts(t, pool)
	if _, err := service.SubmitTurn(SubmitTurnRequest{
		ConversationID: bootstrap.Conversation.ID,
		Input:          "普通用户私聊",
		MessageSource:  "direct",
	}); err != nil {
		t.Fatalf("external direct SubmitTurn: %v", err)
	}
	provider.mu.Lock()
	request := provider.request
	provider.mu.Unlock()
	toolNames := make(map[string]bool, len(request.Tools))
	for _, spec := range request.Tools {
		toolNames[spec.Name] = true
	}
	if !toolNames[tool.PublicMemorySearch] || toolNames[tool.MemorySearch] || toolNames[tool.SocialContextSearch] || toolNames[tool.SocialExpressionSelect] {
		t.Fatalf("external direct tools = %#v", request.Tools)
	}
	for _, item := range request.Input {
		if strings.Contains(item.Content, privateFixture) {
			t.Fatalf("external direct prompt leaked private fixture: %s", item.Content)
		}
	}
	if after := groupPrivacyJobCounts(t, pool); after != before {
		t.Fatalf("external direct background jobs changed: before=%v after=%v", before, after)
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
		if _, err := store.StoreSocialMemoryEntries(context.Background(), social.SocialMemoryBatchInput{
			CharacterID: characterID, ConversationID: fixture.conversationID,
			Entries: []social.SocialMemoryEntryInput{{
				Kind: social.SocialMemoryEpisode, Situation: "群里聊实习和项目经历", Content: fixture.content, RecallCue: "实习焦虑 项目经历",
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
	if _, err := store.CreatePersonalMemory("preference", personal.Scope{Type: "global"}, privateFixture, 9000); err != nil {
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
		AvailableVisualStates: []reply.VisualState{{ID: "idle", Description: "idle"}},
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
		AvailableVisualStates: []reply.VisualState{{ID: "idle", Description: "idle"}},
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
	var inspection tool.RuntimeDetail
	foundInspection := false
	for _, event := range ledger {
		if event.EventType != runtimeLedgerEventTool {
			continue
		}
		var metadata struct {
			Detail tool.RuntimeDetail `json:"detail"`
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

func newCompanionIntegrationService(store *companionIntegrationStores, characterID string, scripted ModelPort) *Service {
	return newCompanionIntegrationServiceWithPorts(store.ports(), characterID, scripted)
}

func newCompanionIntegrationServiceWithPorts(ports memoryPorts, characterID string, scripted ModelPort) *Service {
	service := newServiceWithPorts("", ports, scripted, nil)
	AttachCharacterLookup(service, companionIntegrationCharacterLookup{record: companionIntegrationCharacter(characterID)})
	AttachProfileSource(service, companionIntegrationProfile{})
	AttachConfigSource(service, companionIntegrationConfig{})
	attachSuccessfulTestSurface(nil, service, nil)
	return service
}

func attachSuccessfulTestSurface(t *testing.T, service *Service, observe func(session.Event)) {
	if t != nil {
		t.Helper()
	}
	AttachEventEmitter(service, func(event session.Event) {
		if observe != nil {
			observe(event)
		}
		var payload lifecycle.BeatReadyPayload
		if json.Unmarshal(event.Payload, &payload) != nil || payload.Type != "beat.ready" || payload.Kind != reply.BeatKindFinal {
			return
		}
		err := service.ReportExpressionDelivery(session.ExpressionDeliveryResult{
			ConversationID: event.ConversationID,
			TurnID:         event.TurnID,
			BeatID:         payload.BeatID,
			Status:         session.ExpressionDeliverySucceeded,
		})
		if err == nil {
			return
		}
		if t != nil {
			t.Errorf("ReportExpressionDelivery: %v", err)
		}
	})
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

func finalBeatEvents(events []session.Event) []lifecycle.BeatReadyPayload {
	result := make([]lifecycle.BeatReadyPayload, 0)
	for _, event := range events {
		var payload lifecycle.BeatReadyPayload
		if json.Unmarshal(event.Payload, &payload) == nil && payload.Type == "beat.ready" && payload.Kind == reply.BeatKindFinal {
			result = append(result, payload)
		}
	}
	return result
}

func terminalEventCount(events []session.Event, state lifecycle.State) int {
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
		var payload lifecycle.FailedPayload
		if json.Unmarshal(events[index].Payload, &payload) == nil && payload.Type == "failed" {
			return &failedEvent{Code: payload.Error.Code}
		}
	}
	return nil
}

func hasRuntimeLedgerType(events []historyruntime.TurnRuntimeEventRecord, eventType string) bool {
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

type companionIntegrationStores struct {
	history    *history.Store
	compaction *historycompaction.Store
	runtime    *historyruntime.Store
	memory     *personal.Store
	extraction *extraction.Store
	knowledge  *knowledge.Store
	social     *social.Store
}

func (s *companionIntegrationStores) ports() memoryPorts {
	return memoryPortsFromStores(s.history, s.compaction, s.runtime, s.memory, s.extraction, s.knowledge, s.social)
}

func (s *companionIntegrationStores) OpenOrCreateCharacterConversation(characterID string) (history.ConversationBootstrap, error) {
	return s.history.OpenOrCreateCharacterConversation(characterID)
}

func (s *companionIntegrationStores) OpenOrCreateEndpointConversation(characterID string, binding session.Binding, digest string) (history.ConversationBootstrap, error) {
	return s.history.OpenOrCreateEndpointConversation(characterID, binding, digest)
}

func (s *companionIntegrationStores) BeginCorrelatedTurn(conversationID, input, messageID string) (history.PersistedTurn, error) {
	return s.history.BeginCorrelatedTurn(conversationID, input, messageID)
}

func (s *companionIntegrationStores) BeginTurn(conversationID, input string) (history.PersistedTurn, error) {
	return s.history.BeginTurn(conversationID, input)
}

func (s *companionIntegrationStores) BeginInitiationTurn(conversationID string, evidenceIDs []string) (history.PersistedTurn, error) {
	return s.history.BeginInitiationTurn(conversationID, evidenceIDs)
}

func (s *companionIntegrationStores) CompleteTurn(conversationID, turnID, output string) (history.MessageRecord, error) {
	return s.history.CompleteTurn(conversationID, turnID, output)
}

func (s *companionIntegrationStores) CompleteExpressionTurnForPolicy(conversationID, turnID, output string, parts []historyexpr.Part, eligible bool) (history.MessageRecord, error) {
	return s.history.CompleteExpressionTurnForPolicy(conversationID, turnID, output, parts, eligible)
}

func (s *companionIntegrationStores) InterruptExpressionTurn(conversationID, turnID, prefix string, parts []historyexpr.Part) (*history.MessageRecord, error) {
	return s.history.InterruptExpressionTurn(conversationID, turnID, prefix, parts)
}

func (s *companionIntegrationStores) FailTurn(conversationID, turnID, code, message string, retryable bool) error {
	return s.history.FailTurn(conversationID, turnID, code, message, retryable)
}

func (s *companionIntegrationStores) LoadConversation(conversationID string) (history.ConversationBootstrap, error) {
	return s.history.LoadConversation(conversationID)
}

func (s *companionIntegrationStores) ListTurnRuntimeEvents(conversationID, turnID string) ([]historyruntime.TurnRuntimeEventRecord, error) {
	return s.runtime.ListTurnRuntimeEvents(conversationID, turnID)
}

func (s *companionIntegrationStores) CreatePersonalMemory(kind string, scope personal.Scope, content string, confidence uint16) (personal.Record, error) {
	return s.memory.CreatePersonalMemory(kind, scope, content, confidence)
}

func (s *companionIntegrationStores) StoreSocialMemoryEntries(ctx context.Context, input social.SocialMemoryBatchInput) ([]social.SocialMemoryEntry, error) {
	return s.social.StoreSocialMemoryEntries(ctx, input)
}

func openCompanionIntegrationStore(t *testing.T) (*companionIntegrationStores, *pgxpool.Pool, func()) {
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
	historyStore, err := history.NewStoreFromPool(pool)
	if err != nil {
		pool.Close()
		t.Fatal(err)
	}
	compactionStore, err := historycompaction.NewStoreFromPool(pool)
	if err != nil {
		pool.Close()
		t.Fatal(err)
	}
	runtimeStore, err := historyruntime.NewStoreFromPool(pool)
	if err != nil {
		pool.Close()
		t.Fatal(err)
	}
	memoryStore, err := personal.NewStoreFromPool(pool, nil)
	if err != nil {
		pool.Close()
		t.Fatal(err)
	}
	extractionStore, err := extraction.NewStoreFromPool(pool, nil)
	if err != nil {
		pool.Close()
		t.Fatal(err)
	}
	knowledgeStore, err := knowledge.NewStoreFromPool(pool, nil)
	if err != nil {
		pool.Close()
		t.Fatal(err)
	}
	socialStore, err := social.NewStoreFromPool(pool)
	if err != nil {
		pool.Close()
		t.Fatal(err)
	}
	store := &companionIntegrationStores{history: historyStore, compaction: compactionStore, runtime: runtimeStore, memory: memoryStore, extraction: extractionStore, knowledge: knowledgeStore, social: socialStore}
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
