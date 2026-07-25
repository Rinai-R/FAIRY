package companion

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"fairy/character"
	"fairy/config"
	"fairy/internal/app/sociallearning"
	"fairy/memory"
	"fairy/model"
)

func socialLearningObservations() []AmbientObservation {
	return []AmbientObservation{
		{MessageID: "m1", SenderID: "u1", SenderName: "甲", Text: "最近投简历有点焦虑", TimestampUnixMS: 1000},
		{MessageID: "m2", SenderID: "u2", SenderName: "乙", Text: "先把项目经历整理清楚", TimestampUnixMS: 2000},
	}
}

func TestParticipationBehaviorContextKeepsOnlyBehaviorEntries(t *testing.T) {
	memoryPort := &socialLearningMemory{retrieved: memory.SocialMemoryContext{Entries: []memory.SocialMemoryEntry{
		{ID: "e1", Kind: memory.SocialMemoryEpisode, Situation: "话题", Content: "进展", RecallCue: "话题"},
		{ID: "e2", Kind: memory.SocialMemoryBehavior, Situation: "被点名时", Content: "先短回再补一句", RecallCue: "被点名"},
		{ID: "e3", Kind: memory.SocialMemoryBehavior, Situation: "冷场时", Content: "不硬插话", RecallCue: "冷场"},
	}}}
	service := newSocialLearningTestService(memoryPort, &socialLearningModel{})
	item, err := service.participationBehaviorContext(t.Context(), "character-1", "conversation-1", socialLearningObservations())
	if err != nil || item == nil {
		t.Fatalf("participationBehaviorContext() = %#v, %v", item, err)
	}
	if !strings.Contains(item.Content, `"kind":"behavior"`) || strings.Contains(item.Content, `"kind":"episode"`) {
		t.Fatalf("behavior context = %s", item.Content)
	}
}

type socialLearningMemory struct {
	MemoryPort
	mu                     sync.Mutex
	stored                 []memory.SocialMemoryBatchInput
	storeErr               error
	upserted               []memory.SocialPersonNoteInput
	upsertErr              error
	retrieved              memory.SocialMemoryContext
	retrieveErr            error
	retrieveCharacterID    string
	retrieveConversationID string
	retrieveQuery          string
	feedback               []memory.SocialReplyFeedbackInput
	feedbackErr            error
	feedbackSummary        memory.RecentSocialFeedbackSummary
	feedbackSummaryErr     error
	feedbackSummaryCalls   int
}

func (m *socialLearningMemory) LoadConversation(conversationID string) (memory.ConversationBootstrap, error) {
	return memory.ConversationBootstrap{Conversation: memory.ConversationRecord{ID: conversationID, CharacterID: "character-1"}}, nil
}

func (m *socialLearningMemory) StoreSocialMemoryEntries(_ context.Context, input memory.SocialMemoryBatchInput) ([]memory.SocialMemoryEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.storeErr != nil {
		return nil, m.storeErr
	}
	m.stored = append(m.stored, input)
	return []memory.SocialMemoryEntry{{ID: "entry-1"}}, nil
}

func (m *socialLearningMemory) storedCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.stored)
}

func (m *socialLearningMemory) RetrieveSocialMemoryContext(_ context.Context, characterID, conversationID, query string) (memory.SocialMemoryContext, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.retrieveCharacterID = characterID
	m.retrieveConversationID = conversationID
	m.retrieveQuery = query
	return m.retrieved, m.retrieveErr
}

func (m *socialLearningMemory) RecordSocialReplyFeedback(_ context.Context, input memory.SocialReplyFeedbackInput) (memory.SocialReplyFeedback, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.feedbackErr != nil {
		return memory.SocialReplyFeedback{}, m.feedbackErr
	}
	m.feedback = append(m.feedback, input)
	return memory.SocialReplyFeedback{ID: "feedback-1", Outcome: input.Outcome}, nil
}

func (m *socialLearningMemory) RecentSocialFeedbackSummary(_ context.Context, _, _ string) (memory.RecentSocialFeedbackSummary, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.feedbackSummaryCalls++
	return m.feedbackSummary, m.feedbackSummaryErr
}

func (m *socialLearningMemory) UpsertSocialPersonNote(_ context.Context, input memory.SocialPersonNoteInput) (memory.SocialPersonNote, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.upsertErr != nil {
		return memory.SocialPersonNote{}, m.upsertErr
	}
	m.upserted = append(m.upserted, input)
	return memory.SocialPersonNote{ID: "note-1", CharacterID: input.CharacterID, ConversationID: input.ConversationID, SenderID: input.SenderID, SenderName: input.SenderName, Note: input.Note}, nil
}

func (m *socialLearningMemory) upsertedNotes() []memory.SocialPersonNoteInput {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]memory.SocialPersonNoteInput(nil), m.upserted...)
}

func (m *socialLearningMemory) ListSocialPersonNotes(context.Context, string, string, []string) ([]memory.SocialPersonNote, error) {
	return nil, nil
}

func (m *socialLearningMemory) feedbackInputs() []memory.SocialReplyFeedbackInput {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]memory.SocialReplyFeedbackInput(nil), m.feedback...)
}

type socialLearningModel struct {
	ModelPort
	mu      sync.Mutex
	draft   string
	err     error
	block   bool
	started chan struct{}
	request model.CompiledPromptRequest
	events  []model.StreamEvent
}

func (m *socialLearningModel) ExecuteRequestContext(ctx context.Context, request model.CompiledPromptRequest) ([]model.StreamEvent, error) {
	m.mu.Lock()
	m.request = request
	block := m.block
	err := m.err
	draft := m.draft
	events := append([]model.StreamEvent(nil), m.events...)
	m.mu.Unlock()
	if block {
		if m.started != nil {
			close(m.started)
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, err
	}
	if events != nil {
		return events, nil
	}
	return []model.StreamEvent{{Type: "text_delta", Data: draft}}, nil
}

type socialLearningCatalog struct{ CharacterCatalog }

func (socialLearningCatalog) List() (character.Catalog, error) {
	record := character.Record{CharacterID: "character-1", Revision: 1, Name: "Fairy", Description: "群友", TextLanguage: "zh", SpeakingLanguage: "zh"}
	return character.Catalog{Characters: []character.Record{record}}, nil
}

type socialLearningConfig struct{ ConfigSource }

func (socialLearningConfig) ModelConnection() (config.ModelConnection, error) {
	return config.ModelConnection{Model: "model-1", Capabilities: config.GatewayCapabilities{PromptCacheKey: true}}, nil
}

func newSocialLearningTestService(memoryPort MemoryPort, modelPort ModelPort) *CompanionService {
	service := NewCompanionService()
	service.memory = memoryPort
	service.model = modelPort
	service.characters = socialLearningCatalog{}
	service.cfg = socialLearningConfig{}
	service.interactions["conversation-1"] = publicAmbientBinding()
	return service
}

func validSocialLearningDraft() string {
	return `{"entries":[{"kind":"episode","situation":"群友谈论求职准备","content":"群内会交换整理项目经历的建议","recallCue":"求职或实习准备","sourceMessageIds":["m1","m2"]}]}`
}

func TestRetrieveSocialRespondContextUsesPublicConversationScope(t *testing.T) {
	memoryPort := &socialLearningMemory{retrieved: memory.SocialMemoryContext{Entries: []memory.SocialMemoryEntry{
		{ID: "entry-1", Kind: memory.SocialMemoryEpisode, Situation: "实习", Content: "群内讨论实习进度", RecallCue: "实习"},
		{ID: "entry-2", Kind: memory.SocialMemoryExpression, Situation: "安慰", Content: "先短句接住", RecallCue: "安慰"},
	}}, feedbackSummary: memory.RecentSocialFeedbackSummary{
		SampleCount: 4, PositiveCount: 3, NegativeCount: 1, ObservedMessageCount: 7, LatestOutcome: memory.SocialFeedbackNegative,
	}}
	service := newSocialLearningTestService(memoryPort, &socialLearningModel{})
	intent := &ReplyIntent{MemoryQuery: "之前的实习讨论", ExpressionQuery: "安慰焦虑的群友"}
	context, err := service.retrieveSocialRespondContext(t.Context(), "character-1", "conversation-1", publicAmbientResolved(), intent, nil)
	if err != nil {
		t.Fatal(err)
	}
	if context == nil || len(context.Memory.Entries) != 1 || context.Memory.Entries[0].Kind != memory.SocialMemoryEpisode {
		t.Fatalf("context = %#v", context)
	}
	if memoryPort.retrieveCharacterID != "character-1" || memoryPort.retrieveConversationID != "conversation-1" {
		t.Fatalf("scope = (%q, %q)", memoryPort.retrieveCharacterID, memoryPort.retrieveConversationID)
	}
	if memoryPort.retrieveQuery != "之前的实习讨论" {
		t.Fatalf("query = %q", memoryPort.retrieveQuery)
	}
	for _, expected := range []string{"sample=4", "positive=3", "negative=1", "latest=negative", "observedMessages=7"} {
		if !strings.Contains(context.RecentFeedback, expected) {
			t.Fatalf("recent feedback %q does not contain %q", context.RecentFeedback, expected)
		}
	}
	privateContext, err := service.retrieveSocialRespondContext(t.Context(), "character-1", "conversation-1", desktopResolved(), intent, nil)
	if err != nil || privateContext != nil {
		t.Fatalf("private context = %#v, error = %v", privateContext, err)
	}
	if memoryPort.feedbackSummaryCalls != 1 {
		t.Fatalf("feedback summary calls = %d, want public call only", memoryPort.feedbackSummaryCalls)
	}
}

func TestRetrieveSocialRespondContextAllowsExpressionOnlyIntent(t *testing.T) {
	memoryPort := &socialLearningMemory{}
	service := newSocialLearningTestService(memoryPort, &socialLearningModel{})
	intent := &ReplyIntent{ExpressionQuery: "安慰焦虑的群友"}
	context, err := service.retrieveSocialRespondContext(t.Context(), "character-1", "conversation-1", publicAmbientResolved(), intent, nil)
	if err != nil {
		t.Fatal(err)
	}
	if context == nil || len(context.Memory.Entries) != 0 {
		t.Fatalf("context = %#v", context)
	}
	if memoryPort.retrieveQuery != "" {
		t.Fatalf("unexpected retrieve query %q", memoryPort.retrieveQuery)
	}
}

func TestRetrieveSocialRespondContextReturnsStorageFailure(t *testing.T) {
	service := newSocialLearningTestService(&socialLearningMemory{retrieveErr: errors.New("database failed")}, &socialLearningModel{})
	intent := &ReplyIntent{MemoryQuery: "接住焦虑"}
	if _, err := service.retrieveSocialRespondContext(t.Context(), "character-1", "conversation-1", publicAmbientResolved(), intent, nil); err == nil {
		t.Fatal("retrieveSocialRespondContext() error = nil")
	}
}

func TestAmbientLearningEnqueuesEveryThresholdOnce(t *testing.T) {
	engine := sociallearning.NewLearningEngine(nil, 2)
	service := NewCompanionService()
	service.socialLearning = engine
	service.interactions["conversation-1"] = publicAmbientBinding()
	defer service.Close()
	for index := 1; index <= 40; index++ {
		observation := AmbientObservation{
			MessageID: "m" + strings.Repeat("x", index), SenderID: "u1", SenderName: "甲", Text: "消息", TimestampUnixMS: int64(index),
		}
		if err := service.ObserveAmbient("conversation-1", observation); err != nil {
			t.Fatal(err)
		}
	}
	if stats := engine.Stats(); stats.Enqueued != 2 || stats.Dropped != 0 {
		t.Fatalf("Stats() = %#v", stats)
	}
}
