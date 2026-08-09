package conversation

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	initiative "fairy/agent/presence"
	"fairy/agent/tool"
	"fairy/context/character"
	history "fairy/context/history/transcript"
	"fairy/context/social"
	"fairy/runtime/config"
	"fairy/runtime/model"
)

func socialLearningObservations() []initiative.AmbientObservation {
	return []initiative.AmbientObservation{
		{MessageID: "m1", SenderID: "u1", SenderName: "甲", Text: "最近投简历有点焦虑", TimestampUnixMS: 1000},
		{MessageID: "m2", SenderID: "u2", SenderName: "乙", Text: "先把项目经历整理清楚", TimestampUnixMS: 2000},
	}
}

func TestParticipationBehaviorContextKeepsOnlyBehaviorEntries(t *testing.T) {
	memoryPort := &socialLearningMemory{retrieved: social.SocialMemoryContext{Entries: []social.SocialMemoryEntry{
		{ID: "e1", Kind: social.SocialMemoryEpisode, Situation: "话题", Content: "进展", RecallCue: "话题"},
		{ID: "e2", Kind: social.SocialMemoryBehavior, Situation: "被点名时", Content: "先短回再补一句", RecallCue: "被点名"},
		{ID: "e3", Kind: social.SocialMemoryBehavior, Situation: "冷场时", Content: "不硬插话", RecallCue: "冷场"},
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
	trial := social.SocialMemoryEntry{
		ID: "trial", Kind: social.SocialMemoryBehavior, Status: "suppressed",
		Situation: "被点名时", Content: "先短回再补一句", RecallCue: "被点名",
	}
	retrieved := social.SocialMemoryContext{Entries: []social.SocialMemoryEntry{trial}}
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
	retrieved              social.SocialMemoryContext
	retrievedByQuery       map[string]social.SocialMemoryContext
	retrieveErr            error
	retrieveErrByQuery     map[string]error
	retrieveCharacterID    string
	retrieveConversationID string
	retrieveQuery          string
	retrieveQueries        []string
	personNotes            []social.SocialPersonNote
	feedbackErr            error
	metadataLoads          int
}

func (m *socialLearningMemory) LoadConversationRecord(conversationID string) (history.ConversationRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.metadataLoads++
	return history.ConversationRecord{ID: conversationID, CharacterID: "character-1"}, nil
}

func (m *socialLearningMemory) StoreSocialMemoryEntries(_ context.Context, input social.SocialMemoryBatchInput) ([]social.SocialMemoryEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.storeErr != nil {
		return nil, m.storeErr
	}
	return []social.SocialMemoryEntry{{ID: "entry-1"}}, nil
}

func (m *socialLearningMemory) RetrieveSocialMemoryContext(_ context.Context, characterID, conversationID, query string) (social.SocialMemoryContext, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.retrieveCharacterID = characterID
	m.retrieveConversationID = conversationID
	m.retrieveQuery = query
	m.retrieveQueries = append(m.retrieveQueries, query)
	if err := m.retrieveErrByQuery[query]; err != nil {
		return social.SocialMemoryContext{}, err
	}
	if retrieved, ok := m.retrievedByQuery[query]; ok {
		return retrieved, nil
	}
	return m.retrieved, m.retrieveErr
}

func (m *socialLearningMemory) RetrieveCharacterSocialMemoryContext(_ context.Context, characterID, query string) (social.SocialMemoryContext, error) {
	return m.RetrieveSocialMemoryContext(context.Background(), characterID, "", query)
}

func (m *socialLearningMemory) RecordSocialFeedbackBatch(_ context.Context, input social.SocialFeedbackBatchInput) (social.SocialFeedbackBatchResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.feedbackErr != nil {
		return social.SocialFeedbackBatchResult{}, m.feedbackErr
	}
	return social.SocialFeedbackBatchResult{}, nil
}

func TestSocialMemoryFeedbackCandidatesPreserveInjectedAbstractContent(t *testing.T) {
	context := social.SocialMemoryContext{Entries: []social.SocialMemoryEntry{{
		ID: "entry-1", Kind: social.SocialMemoryBehavior, Situation: "群友焦虑时",
		Content: "先接住情绪", RecallCue: "焦虑安慰",
	}}}
	candidates := socialMemoryFeedbackCandidates(context)
	if len(candidates) != 1 || candidates[0] != (social.SocialFeedbackCandidate{
		ID: "entry-1", Kind: social.SocialMemoryBehavior, Situation: "群友焦虑时",
		Content: "先接住情绪", RecallCue: "焦虑安慰",
	}) {
		t.Fatalf("feedback candidates = %#v", candidates)
	}
}

func TestSocialFeedbackContextIncludesBaselineAndToolCandidatesOnce(t *testing.T) {
	baseline := social.SocialMemoryContext{Entries: []social.SocialMemoryEntry{
		{ID: "baseline", Kind: social.SocialMemoryEpisode, Situation: "项目进度", Content: "先回应当前进展", RecallCue: "进度"},
		{ID: "shared", Kind: social.SocialMemoryBehavior, Situation: "群友焦虑", Content: "先接住情绪", RecallCue: "焦虑"},
	}}
	toolResult := social.SocialMemoryContext{Entries: []social.SocialMemoryEntry{
		{ID: "shared", Kind: social.SocialMemoryBehavior, Situation: "群友焦虑", Content: "先接住情绪", RecallCue: "焦虑"},
		{ID: "tool-only", Kind: social.SocialMemoryExpression, Situation: "轻松回应", Content: "使用简短自然的口吻", RecallCue: "轻松"},
	}}

	feedbackContext := tool.MergeSocialMemory(baseline, toolResult)
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

func (m *socialLearningMemory) UpsertSocialPersonNote(_ context.Context, input social.SocialPersonNoteInput) (social.SocialPersonNote, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.upsertErr != nil {
		return social.SocialPersonNote{}, m.upsertErr
	}
	return social.SocialPersonNote{ID: "note-1", CharacterID: input.CharacterID, ConversationID: input.ConversationID, SenderID: input.SenderID, SenderName: input.SenderName, Note: input.Note}, nil
}

func (m *socialLearningMemory) ListSocialPersonNotes(context.Context, string, string, []string) ([]social.SocialPersonNote, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]social.SocialPersonNote(nil), m.personNotes...), nil
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

func newSocialLearningTestService(memoryPort *socialLearningMemory, modelPort ModelPort) *Service {
	service := NewService()
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
	memoryPort := &socialLearningMemory{retrieved: social.SocialMemoryContext{Entries: []social.SocialMemoryEntry{
		{ID: "entry-1", Kind: social.SocialMemoryEpisode, Situation: "实习", Content: "群内讨论实习进度", RecallCue: "实习"},
		{ID: "entry-2", Kind: social.SocialMemoryExpression, Situation: "安慰", Content: "先短句接住", RecallCue: "安慰"},
	}}}
	service := newSocialLearningTestService(memoryPort, &socialLearningModel{})
	intent := &ReplyIntent{MemoryQuery: "之前的实习讨论", ExpressionQuery: "安慰焦虑的群友"}
	context, err := service.retrieveSocialRespondContext(t.Context(), "character-1", "conversation-1", publicAmbientResolved(), intent, nil)
	if err != nil {
		t.Fatal(err)
	}
	if context == nil || len(context.Memory.Entries) != 2 || context.Memory.Entries[0].Kind != social.SocialMemoryEpisode || context.Memory.Entries[1].Kind != social.SocialMemoryExpression {
		t.Fatalf("context = %#v", context)
	}
	if memoryPort.retrieveCharacterID != "character-1" || memoryPort.retrieveConversationID != "conversation-1" {
		t.Fatalf("scope = (%q, %q)", memoryPort.retrieveCharacterID, memoryPort.retrieveConversationID)
	}
	if strings.Join(memoryPort.retrieveQueries, "|") != "之前的实习讨论|安慰焦虑的群友" {
		t.Fatalf("queries = %#v", memoryPort.retrieveQueries)
	}
	if context.RecentFeedback != "" {
		t.Fatalf("reply-level feedback should not be injected: %q", context.RecentFeedback)
	}
	privateContext, err := service.retrieveSocialRespondContext(t.Context(), "character-1", "conversation-1", desktopResolved(), intent, nil)
	if err != nil || privateContext != nil {
		t.Fatalf("private context = %#v, error = %v", privateContext, err)
	}
	if len(memoryPort.retrieveQueries) != 2 {
		t.Fatalf("private path queried social memory: %#v", memoryPort.retrieveQueries)
	}
}

func TestRetrieveSocialRespondContextAllowsExpressionOnlyIntent(t *testing.T) {
	memoryPort := &socialLearningMemory{retrieved: social.SocialMemoryContext{Entries: []social.SocialMemoryEntry{
		{ID: "expression-1", Kind: social.SocialMemoryExpression, Situation: "安慰", Content: "先短句接住", RecallCue: "焦虑"},
	}}}
	service := newSocialLearningTestService(memoryPort, &socialLearningModel{})
	intent := &ReplyIntent{ExpressionQuery: "安慰焦虑的群友"}
	context, err := service.retrieveSocialRespondContext(t.Context(), "character-1", "conversation-1", publicAmbientResolved(), intent, nil)
	if err != nil {
		t.Fatal(err)
	}
	if context == nil || len(context.Memory.Entries) != 1 || context.Memory.Entries[0].Kind != social.SocialMemoryExpression {
		t.Fatalf("context = %#v", context)
	}
	if memoryPort.retrieveQuery != "安慰焦虑的群友" {
		t.Fatalf("retrieve query = %q", memoryPort.retrieveQuery)
	}
}

func TestRetrieveSocialRespondContextReusesIdenticalQueries(t *testing.T) {
	memoryPort := &socialLearningMemory{
		retrieved: social.SocialMemoryContext{Entries: []social.SocialMemoryEntry{
			{ID: "episode-1", Kind: social.SocialMemoryEpisode, Situation: "实习", Content: "最近在投简历", RecallCue: "焦虑"},
			{ID: "behavior-1", Kind: social.SocialMemoryBehavior, Situation: "群友焦虑", Content: "先接住情绪", RecallCue: "焦虑"},
			{ID: "expression-1", Kind: social.SocialMemoryExpression, Situation: "安慰", Content: "用短句自然回应", RecallCue: "焦虑"},
		}},
		personNotes: []social.SocialPersonNote{{ID: "note-1", SenderID: "user-1", Note: "正在找实习"}},
	}
	service := newSocialLearningTestService(memoryPort, &socialLearningModel{})
	intent := &ReplyIntent{MemoryQuery: " 实习焦虑 ", ExpressionQuery: "实习焦虑"}
	context, err := service.retrieveSocialRespondContext(t.Context(), "character-1", "conversation-1", publicAmbientResolved(), intent, []string{"user-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(memoryPort.retrieveQueries) != 1 || memoryPort.retrieveQueries[0] != "实习焦虑" {
		t.Fatalf("queries = %#v", memoryPort.retrieveQueries)
	}
	if len(context.Memory.Entries) != 3 || len(context.PersonNotes) != 1 {
		t.Fatalf("context = %#v", context)
	}
}

func TestRetrieveSocialRespondContextDeduplicatesAndBoundsCandidates(t *testing.T) {
	memoryEntries := make([]social.SocialMemoryEntry, 0, 9)
	for index := 0; index < 9; index++ {
		memoryEntries = append(memoryEntries, social.SocialMemoryEntry{ID: fmt.Sprintf("memory-%02d", index), Kind: social.SocialMemoryEpisode})
	}
	expressionEntries := []social.SocialMemoryEntry{{ID: "memory-04", Kind: social.SocialMemoryExpression}}
	for index := 0; index < 9; index++ {
		expressionEntries = append(expressionEntries, social.SocialMemoryEntry{ID: fmt.Sprintf("expression-%02d", index), Kind: social.SocialMemoryExpression})
	}
	memoryPort := &socialLearningMemory{retrievedByQuery: map[string]social.SocialMemoryContext{
		"memory":     {Entries: memoryEntries},
		"expression": {Entries: expressionEntries},
	}}
	service := newSocialLearningTestService(memoryPort, &socialLearningModel{})
	context, err := service.retrieveSocialRespondContext(t.Context(), "character-1", "conversation-1", publicAmbientResolved(), &ReplyIntent{MemoryQuery: "memory", ExpressionQuery: "expression"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(context.Memory.Entries) != social.MaxSocialFeedbackIDs {
		t.Fatalf("entry count = %d, want %d", len(context.Memory.Entries), social.MaxSocialFeedbackIDs)
	}
	for index := 0; index < 9; index++ {
		if context.Memory.Entries[index].ID != fmt.Sprintf("memory-%02d", index) {
			t.Fatalf("memory order at %d = %q", index, context.Memory.Entries[index].ID)
		}
	}
	for index, want := range []string{"expression-00", "expression-01", "expression-02"} {
		if context.Memory.Entries[9+index].ID != want {
			t.Fatalf("expression order at %d = %q, want %q", index, context.Memory.Entries[9+index].ID, want)
		}
	}
}

func TestRetrieveSocialRespondContextReturnsExpressionStorageFailure(t *testing.T) {
	service := newSocialLearningTestService(&socialLearningMemory{retrieveErrByQuery: map[string]error{"expression": errors.New("database failed")}}, &socialLearningModel{})
	intent := &ReplyIntent{MemoryQuery: "memory", ExpressionQuery: "expression"}
	if _, err := service.retrieveSocialRespondContext(t.Context(), "character-1", "conversation-1", publicAmbientResolved(), intent, nil); err == nil {
		t.Fatal("retrieveSocialRespondContext() expression error = nil")
	}
}

func TestRetrieveSocialRespondContextReturnsStorageFailure(t *testing.T) {
	service := newSocialLearningTestService(&socialLearningMemory{retrieveErr: errors.New("database failed")}, &socialLearningModel{})
	intent := &ReplyIntent{MemoryQuery: "接住焦虑"}
	if _, err := service.retrieveSocialRespondContext(t.Context(), "character-1", "conversation-1", publicAmbientResolved(), intent, nil); err == nil {
		t.Fatal("retrieveSocialRespondContext() error = nil")
	}
}
