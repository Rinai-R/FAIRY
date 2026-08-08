package companion

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"fairy/character"
	"fairy/config"
	"fairy/initiative"
	"fairy/memory"
	"fairy/model"
)

func socialLearningObservations() []initiative.AmbientObservation {
	return []initiative.AmbientObservation{
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
	item, err := initiative.BehaviorItem(memoryPort.retrieved)
	if err != nil || item == nil {
		t.Fatalf("BehaviorItem() = %#v, %v", item, err)
	}
	if !strings.Contains(item.Content, `"kind":"behavior"`) || strings.Contains(item.Content, `"kind":"episode"`) {
		t.Fatalf("behavior context = %s", item.Content)
	}
}

func TestPublicBehaviorTrialHasSameEligibilityForParticipationAndReply(t *testing.T) {
	trial := memory.SocialMemoryEntry{
		ID: "trial", Kind: memory.SocialMemoryBehavior, Status: "suppressed",
		Situation: "被点名时", Content: "先短回再补一句", RecallCue: "被点名",
	}
	retrieved := memory.SocialMemoryContext{Entries: []memory.SocialMemoryEntry{trial}}
	participationItem, err := initiative.BehaviorItem(retrieved)
	if err != nil || participationItem == nil || !strings.Contains(participationItem.Content, `"kind":"behavior"`) || !strings.Contains(participationItem.Content, "先短回再补一句") {
		t.Fatalf("participation trial = %#v, %v", participationItem, err)
	}

	memoryPort := &socialLearningMemory{retrieved: retrieved}
	service := newSocialLearningTestService(memoryPort, &socialLearningModel{})
	intent := &ReplyIntent{MemoryQuery: "被点名"}
	respondContext, err := service.retrieveSocialRespondContext(t.Context(), "character-1", "conversation-1", publicAmbientResolved(), intent, nil)
	if err != nil || respondContext == nil || len(respondContext.Memory.Entries) != 1 || respondContext.Memory.Entries[0].ID != trial.ID {
		t.Fatalf("reply trial = %#v, %v", respondContext, err)
	}
}

type socialLearningMemory struct {
	mu                     sync.Mutex
	storeErr               error
	upsertErr              error
	retrieved              memory.SocialMemoryContext
	retrieveErr            error
	retrieveCharacterID    string
	retrieveConversationID string
	retrieveQuery          string
	feedbackErr            error
	metadataLoads          int
}

func (m *socialLearningMemory) LoadConversationRecord(conversationID string) (memory.ConversationRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.metadataLoads++
	return memory.ConversationRecord{ID: conversationID, CharacterID: "character-1"}, nil
}

func (m *socialLearningMemory) StoreSocialMemoryEntries(_ context.Context, input memory.SocialMemoryBatchInput) ([]memory.SocialMemoryEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.storeErr != nil {
		return nil, m.storeErr
	}
	return []memory.SocialMemoryEntry{{ID: "entry-1"}}, nil
}

func (m *socialLearningMemory) RetrieveSocialMemoryContext(_ context.Context, characterID, conversationID, query string) (memory.SocialMemoryContext, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.retrieveCharacterID = characterID
	m.retrieveConversationID = conversationID
	m.retrieveQuery = query
	return m.retrieved, m.retrieveErr
}

func (m *socialLearningMemory) RecordSocialFeedbackBatch(_ context.Context, input memory.SocialFeedbackBatchInput) (memory.SocialFeedbackBatchResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.feedbackErr != nil {
		return memory.SocialFeedbackBatchResult{}, m.feedbackErr
	}
	return memory.SocialFeedbackBatchResult{}, nil
}

func TestSocialMemoryFeedbackCandidatesPreserveInjectedAbstractContent(t *testing.T) {
	context := memory.SocialMemoryContext{Entries: []memory.SocialMemoryEntry{{
		ID: "entry-1", Kind: memory.SocialMemoryBehavior, Situation: "群友焦虑时",
		Content: "先接住情绪", RecallCue: "焦虑安慰",
	}}}
	candidates := socialMemoryFeedbackCandidates(context)
	if len(candidates) != 1 || candidates[0] != (memory.SocialFeedbackCandidate{
		ID: "entry-1", Kind: memory.SocialMemoryBehavior, Situation: "群友焦虑时",
		Content: "先接住情绪", RecallCue: "焦虑安慰",
	}) {
		t.Fatalf("feedback candidates = %#v", candidates)
	}
}

func TestSocialFeedbackContextIncludesBaselineAndToolCandidatesOnce(t *testing.T) {
	baseline := memory.SocialMemoryContext{Entries: []memory.SocialMemoryEntry{
		{ID: "baseline", Kind: memory.SocialMemoryEpisode, Situation: "项目进度", Content: "先回应当前进展", RecallCue: "进度"},
		{ID: "shared", Kind: memory.SocialMemoryBehavior, Situation: "群友焦虑", Content: "先接住情绪", RecallCue: "焦虑"},
	}}
	toolResult := memory.SocialMemoryContext{Entries: []memory.SocialMemoryEntry{
		{ID: "shared", Kind: memory.SocialMemoryBehavior, Situation: "群友焦虑", Content: "先接住情绪", RecallCue: "焦虑"},
		{ID: "tool-only", Kind: memory.SocialMemoryExpression, Situation: "轻松回应", Content: "使用简短自然的口吻", RecallCue: "轻松"},
	}}

	feedbackContext := mergeSocialMemory(baseline, toolResult)
	candidates := socialMemoryFeedbackCandidates(feedbackContext)
	if len(candidates) != 3 {
		t.Fatalf("feedback candidate count = %d, want 3: %#v", len(candidates), candidates)
	}
	for index, wantID := range []string{"baseline", "shared", "tool-only"} {
		if candidates[index].ID != wantID {
			t.Fatalf("feedback candidate %d ID = %q, want %q", index, candidates[index].ID, wantID)
		}
	}
}

func (m *socialLearningMemory) UpsertSocialPersonNote(_ context.Context, input memory.SocialPersonNoteInput) (memory.SocialPersonNote, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.upsertErr != nil {
		return memory.SocialPersonNote{}, m.upsertErr
	}
	return memory.SocialPersonNote{ID: "note-1", CharacterID: input.CharacterID, ConversationID: input.ConversationID, SenderID: input.SenderID, SenderName: input.SenderName, Note: input.Note}, nil
}

func (m *socialLearningMemory) ListSocialPersonNotes(context.Context, string, string, []string) ([]memory.SocialPersonNote, error) {
	return nil, nil
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

type socialLearningCharacterLookup struct{}

func (socialLearningCharacterLookup) Lookup(characterID string) (character.Record, bool, error) {
	record := character.Record{CharacterID: "character-1", Revision: 1, Name: "Fairy", Description: "群友", TextLanguage: "zh", SpeakingLanguage: "zh"}
	return record, characterID == record.CharacterID, nil
}

type socialLearningConfig struct{ ConfigSource }

func (socialLearningConfig) ModelConnection() (config.ModelConnection, error) {
	return config.ModelConnection{Model: "model-1", Capabilities: config.GatewayCapabilities{PromptCacheKey: true}}, nil
}

func newSocialLearningTestService(memoryPort *socialLearningMemory, modelPort ModelPort) *CompanionService {
	service := NewCompanionService()
	service.memory = memoryPorts{
		ambient: ambientMemoryPorts{
			metadata:        memoryPort,
			socialRetrieval: memoryPort,
			socialContext:   memoryPort,
			socialLearning:  memoryPort,
		},
	}
	service.model = modelPort
	service.characterLookup = socialLearningCharacterLookup{}
	service.cfg = socialLearningConfig{}
	if err := service.BindInteraction("conversation-1", publicAmbientBinding()); err != nil {
		panic(err)
	}
	return service
}

func TestRetrieveSocialRespondContextUsesPublicConversationScope(t *testing.T) {
	memoryPort := &socialLearningMemory{retrieved: memory.SocialMemoryContext{Entries: []memory.SocialMemoryEntry{
		{ID: "entry-1", Kind: memory.SocialMemoryEpisode, Situation: "实习", Content: "群内讨论实习进度", RecallCue: "实习"},
		{ID: "entry-2", Kind: memory.SocialMemoryExpression, Situation: "安慰", Content: "先短句接住", RecallCue: "安慰"},
	}}}
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
	if context.RecentFeedback != "" {
		t.Fatalf("reply-level feedback should not be injected: %q", context.RecentFeedback)
	}
	privateContext, err := service.retrieveSocialRespondContext(t.Context(), "character-1", "conversation-1", desktopResolved(), intent, nil)
	if err != nil || privateContext != nil {
		t.Fatalf("private context = %#v, error = %v", privateContext, err)
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
