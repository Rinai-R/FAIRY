package companion

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"fairy/character"
	"fairy/config"
	"fairy/interaction"
	"fairy/memory"
	"fairy/model"
)

func socialLearningObservations() []AmbientObservation {
	return []AmbientObservation{
		{MessageID: "m1", SenderID: "u1", SenderName: "甲", Text: "最近投简历有点焦虑", TimestampUnixMS: 1000},
		{MessageID: "m2", SenderID: "u2", SenderName: "乙", Text: "先把项目经历整理清楚", TimestampUnixMS: 2000},
	}
}

func TestCompileSocialLearningRejectsSituationOverMemoryLimit(t *testing.T) {
	messages := socialLearningObservations()
	situation := strings.Repeat("情", memory.MaxSocialSituationRunes+1)
	draft := `{"entries":[{"kind":"episode","situation":"` + situation + `","content":"摘要","recallCue":"线索","sourceMessageIds":["m1"]}]}`
	if _, err := compileSocialLearning(draft, messages); err == nil {
		t.Fatal("compileSocialLearning() accepted oversized situation")
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


func TestCompileSocialLearningRejectsInvalidJSONContracts(t *testing.T) {
	messages := socialLearningObservations()
	tests := []struct {
		name  string
		draft string
	}{
		{name: "unknown top-level field", draft: `{"entries":[],"reason":"no"}`},
		{name: "unknown entry field", draft: `{"entries":[{"kind":"episode","situation":"求职讨论","content":"成员在交流准备方法","recallCue":"求职准备","sourceMessageIds":["m1"],"score":1}]}`},
		{name: "null entries", draft: `{"entries":null}`},
		{name: "missing source", draft: `{"entries":[{"kind":"episode","situation":"求职讨论","content":"成员在交流准备方法","recallCue":"求职准备","sourceMessageIds":[]}]}`},
		{name: "unknown source", draft: `{"entries":[{"kind":"episode","situation":"求职讨论","content":"成员在交流准备方法","recallCue":"求职准备","sourceMessageIds":["missing"]}]}`},
		{name: "duplicate source", draft: `{"entries":[{"kind":"episode","situation":"求职讨论","content":"成员在交流准备方法","recallCue":"求职准备","sourceMessageIds":["m1","m1"]}]}`},
		{name: "long copied quote", draft: `{"entries":[{"kind":"expression","situation":"复述测试","content":"abcdefghijklmnopqrstuvwxyz0123456789","recallCue":"复述","sourceMessageIds":["m1"]}]}`},
	}
	messages[0].Text = "abcdefghijklmnopqrstuvwxyz0123456789"
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := compileSocialLearning(test.draft, messages); err == nil {
				t.Fatal("compileSocialLearning() error = nil")
			}
		})
	}
}

func TestCompileSocialLearningSourceCountBounds(t *testing.T) {
	messages := make([]AmbientObservation, 9)
	for index := range messages {
		messages[index] = AmbientObservation{
			MessageID: "m" + strconv.Itoa(index+1), SenderID: "u1", SenderName: "甲",
			Text: "第" + strconv.Itoa(index+1) + "条讨论", TimestampUnixMS: int64(index + 1),
		}
	}
	draft := func(ids []string) string {
		payload := socialLearnPayloadForTest{Entries: []socialLearnEntryDraft{{
			Kind: memory.SocialMemoryEpisode, Situation: "群友延续同一个公开话题",
			Content: "成员会逐步补充各自的看法", RecallCue: "继续讨论此前的话题", SourceMessageIDs: ids,
		}}}
		encoded, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		return string(encoded)
	}
	for _, count := range []int{1, maxSocialLearningSourceIDs} {
		ids := make([]string, count)
		for index := range ids {
			ids[index] = messages[index].MessageID
		}
		if _, err := compileSocialLearning(draft(ids), messages); err != nil {
			t.Fatalf("compile source count %d: %v", count, err)
		}
	}
	ids := make([]string, maxSocialLearningSourceIDs+1)
	for index := range ids {
		ids[index] = messages[index].MessageID
	}
	if _, err := compileSocialLearning(draft(ids), messages); err == nil {
		t.Fatal("compile source count 9 error = nil")
	}
}

type socialLearnPayloadForTest struct {
	Entries []socialLearnEntryDraft `json:"entries"`
}

func TestCompileSocialLearningAcceptsEmptyEntries(t *testing.T) {
	compiled, err := compileSocialLearning(`{"entries":[]}`, socialLearningObservations())
	if err != nil {
		t.Fatal(err)
	}
	if len(compiled.Entries) != 0 || len(compiled.Notes) != 0 {
		t.Fatalf("compiled = %#v", compiled)
	}
}

func TestCompileSocialLearningAcceptsPersonNotes(t *testing.T) {
	draft := `{"entries":[],"personNotes":[{"senderId":"u1","note":"常在群里聊求职焦虑","sourceMessageIds":["m1"]}]}`
	compiled, err := compileSocialLearning(draft, socialLearningObservations())
	if err != nil {
		t.Fatal(err)
	}
	if len(compiled.Entries) != 0 || len(compiled.Notes) != 1 {
		t.Fatalf("compiled = %#v", compiled)
	}
	if compiled.Notes[0].SenderID != "u1" || compiled.Notes[0].SenderName != "甲" || compiled.Notes[0].Note != "常在群里聊求职焦虑" {
		t.Fatalf("note = %#v", compiled.Notes[0])
	}
}

func TestCompileSocialLearningRejectsInvalidPersonNotes(t *testing.T) {
	messages := socialLearningObservations()
	tests := []struct {
		name  string
		draft string
	}{
		{name: "null personNotes", draft: `{"entries":[],"personNotes":null}`},
		{name: "unknown personNote field", draft: `{"entries":[],"personNotes":[{"senderId":"u1","note":"常发言","sourceMessageIds":["m1"],"diagnosis":"焦虑"}]}`},
		{name: "unknown sender", draft: `{"entries":[],"personNotes":[{"senderId":"missing","note":"常发言","sourceMessageIds":["m1"]}]}`},
		{name: "source not from sender", draft: `{"entries":[],"personNotes":[{"senderId":"u1","note":"常发言","sourceMessageIds":["m2"]}]}`},
		{name: "duplicate sender", draft: `{"entries":[],"personNotes":[{"senderId":"u1","note":"常发言","sourceMessageIds":["m1"]},{"senderId":"u1","note":"爱开玩笑","sourceMessageIds":["m1"]}]}`},
		{name: "oversized note", draft: `{"entries":[],"personNotes":[{"senderId":"u1","note":"` + strings.Repeat("话", memory.MaxSocialPersonNoteRunes+1) + `","sourceMessageIds":["m1"]}]}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := compileSocialLearning(test.draft, messages); err == nil {
				t.Fatal("compileSocialLearning() error = nil")
			}
		})
	}
}

func TestBuildSocialLearningInputContainsOnlyExternalObservations(t *testing.T) {
	items, err := buildSocialLearningInput(
		character.Record{CharacterID: "character-1", Revision: 1, Name: "Fairy", Description: "群友", TextLanguage: "zh", SpeakingLanguage: "zh"},
		publicAmbientResolved(),
		socialLearningObservations(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 4 {
		t.Fatalf("len(items) = %d", len(items))
	}
	for _, item := range items[2:] {
		var payload map[string]any
		if err := json.Unmarshal([]byte(item.Content), &payload); err != nil {
			t.Fatal(err)
		}
		if payload["contextType"] != "external_group_observation" {
			t.Fatalf("contextType = %#v", payload["contextType"])
		}
		for _, forbidden := range []string{"assistant", "reply", "traceId", "isNew"} {
			if _, exists := payload[forbidden]; exists {
				t.Fatalf("unexpected field %q in %#v", forbidden, payload)
			}
		}
	}
}

func TestSocialLearningEnqueueIsNonBlockingWhenFull(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	engine := &SocialLearningEngine{ctx: ctx, cancel: cancel, queue: make(chan socialLearningSnapshot, 1)}
	snapshot := socialLearningSnapshot{ConversationID: "conversation-1", Messages: socialLearningObservations()}
	if !engine.Enqueue(snapshot) {
		t.Fatal("first Enqueue() = false")
	}
	started := time.Now()
	if engine.Enqueue(snapshot) {
		t.Fatal("second Enqueue() = true")
	}
	if time.Since(started) > 100*time.Millisecond {
		t.Fatal("full queue blocked Enqueue")
	}
	if stats := engine.Stats(); stats.Enqueued != 1 || stats.Dropped != 1 {
		t.Fatalf("Stats() = %#v", stats)
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

func TestSocialLearningProcessUsesDedicatedLaneAndCacheKey(t *testing.T) {
	memoryPort := &socialLearningMemory{}
	modelPort := &socialLearningModel{draft: validSocialLearningDraft()}
	service := newSocialLearningTestService(memoryPort, modelPort)
	engine := &SocialLearningEngine{host: service}
	if err := engine.process(t.Context(), socialLearningSnapshot{ConversationID: "conversation-1", Messages: socialLearningObservations()}); err != nil {
		t.Fatal(err)
	}
	if modelPort.request.Shape.Lane != model.PromptLaneSocialLearn {
		t.Fatalf("lane = %q", modelPort.request.Shape.Lane)
	}
	if modelPort.request.Shape.PromptCacheKey != model.LaneCacheKey("conversation-1", model.PromptLaneSocialLearn) {
		t.Fatalf("promptCacheKey = %q", modelPort.request.Shape.PromptCacheKey)
	}
	if memoryPort.storedCount() != 1 {
		t.Fatalf("storedCount() = %d", memoryPort.storedCount())
	}
}

func TestSocialLearningModelFailureDoesNotWrite(t *testing.T) {
	memoryPort := &socialLearningMemory{}
	service := newSocialLearningTestService(memoryPort, &socialLearningModel{err: errors.New("provider failed")})
	engine := &SocialLearningEngine{host: service}
	if err := engine.process(t.Context(), socialLearningSnapshot{ConversationID: "conversation-1", Messages: socialLearningObservations()}); err == nil {
		t.Fatal("process() error = nil")
	}
	if memoryPort.storedCount() != 0 {
		t.Fatalf("storedCount() = %d", memoryPort.storedCount())
	}
}

func TestSocialLearningInvalidEntryMakesWholeBatchZeroWrite(t *testing.T) {
	memoryPort := &socialLearningMemory{}
	draft := `{"entries":[` +
		`{"kind":"episode","situation":"群友谈论求职准备","content":"群内会交换整理项目经历的建议","recallCue":"求职或实习准备","sourceMessageIds":["m1","m2"]},` +
		`{"kind":"expression","situation":"求职讨论","content":"成员会先接住焦虑再交流经验","recallCue":"安慰求职焦虑","sourceMessageIds":["missing"]}` +
		`]}`
	service := newSocialLearningTestService(memoryPort, &socialLearningModel{draft: draft})
	engine := &SocialLearningEngine{host: service}
	if err := engine.process(t.Context(), socialLearningSnapshot{ConversationID: "conversation-1", Messages: socialLearningObservations()}); err == nil {
		t.Fatal("process() error = nil")
	}
	if memoryPort.storedCount() != 0 {
		t.Fatalf("storedCount() = %d", memoryPort.storedCount())
	}
}

func TestSocialLearningEmptyEntriesDoesNotInventMemory(t *testing.T) {
	memoryPort := &socialLearningMemory{}
	service := newSocialLearningTestService(memoryPort, &socialLearningModel{draft: `{"entries":[]}`})
	engine := &SocialLearningEngine{host: service}
	if err := engine.process(t.Context(), socialLearningSnapshot{ConversationID: "conversation-1", Messages: socialLearningObservations()}); err != nil {
		t.Fatal(err)
	}
	if memoryPort.storedCount() != 0 {
		t.Fatalf("storedCount() = %d", memoryPort.storedCount())
	}
	if len(memoryPort.upsertedNotes()) != 0 {
		t.Fatalf("upsertedNotes() = %#v", memoryPort.upsertedNotes())
	}
}

func TestSocialLearningProcessUpsertsPersonNotesWithoutEntries(t *testing.T) {
	memoryPort := &socialLearningMemory{}
	draft := `{"entries":[],"personNotes":[{"senderId":"u1","note":"常在群里聊求职焦虑","sourceMessageIds":["m1"]}]}`
	service := newSocialLearningTestService(memoryPort, &socialLearningModel{draft: draft})
	engine := &SocialLearningEngine{host: service}
	if err := engine.process(t.Context(), socialLearningSnapshot{ConversationID: "conversation-1", Messages: socialLearningObservations()}); err != nil {
		t.Fatal(err)
	}
	if memoryPort.storedCount() != 0 {
		t.Fatalf("storedCount() = %d", memoryPort.storedCount())
	}
	notes := memoryPort.upsertedNotes()
	if len(notes) != 1 {
		t.Fatalf("upsertedNotes() = %#v", notes)
	}
	if notes[0].CharacterID != "character-1" || notes[0].ConversationID != "conversation-1" || notes[0].SenderID != "u1" || notes[0].Note != "常在群里聊求职焦虑" {
		t.Fatalf("note = %#v", notes[0])
	}
}

func TestSocialLearningInvalidPersonNoteMakesWholeBatchZeroWrite(t *testing.T) {
	memoryPort := &socialLearningMemory{}
	draft := `{"entries":[{"kind":"episode","situation":"群友谈论求职准备","content":"群内会交换整理项目经历的建议","recallCue":"求职或实习准备","sourceMessageIds":["m1","m2"]}],` +
		`"personNotes":[{"senderId":"missing","note":"常发言","sourceMessageIds":["m1"]}]}`
	service := newSocialLearningTestService(memoryPort, &socialLearningModel{draft: draft})
	engine := &SocialLearningEngine{host: service}
	if err := engine.process(t.Context(), socialLearningSnapshot{ConversationID: "conversation-1", Messages: socialLearningObservations()}); err == nil {
		t.Fatal("process() error = nil")
	}
	if memoryPort.storedCount() != 0 || len(memoryPort.upsertedNotes()) != 0 {
		t.Fatalf("stored=%d notes=%#v", memoryPort.storedCount(), memoryPort.upsertedNotes())
	}
}

func TestSocialLearningPrivatePathDoesNotWritePersonNotes(t *testing.T) {
	memoryPort := &socialLearningMemory{}
	draft := `{"entries":[],"personNotes":[{"senderId":"u1","note":"常发言","sourceMessageIds":["m1"]}]}`
	service := newSocialLearningTestService(memoryPort, &socialLearningModel{draft: draft})
	service.interactions["conversation-1"] = interaction.Binding{
		Endpoint: interaction.EndpointDesktop,
		Facts: interaction.Facts{
			Audience: interaction.AudienceSingle, Initiation: interaction.InitiationDirect, Presentation: interaction.PresentationEmbodied,
		},
	}
	engine := &SocialLearningEngine{host: service}
	if err := engine.process(t.Context(), socialLearningSnapshot{ConversationID: "conversation-1", Messages: socialLearningObservations()}); err == nil {
		t.Fatal("process() error = nil")
	}
	if memoryPort.storedCount() != 0 || len(memoryPort.upsertedNotes()) != 0 {
		t.Fatalf("stored=%d notes=%#v", memoryPort.storedCount(), memoryPort.upsertedNotes())
	}
}

func TestSocialLearningEmptyDraftReportsSafeProviderMetadata(t *testing.T) {
	memoryPort := &socialLearningMemory{}
	modelPort := &socialLearningModel{events: []model.StreamEvent{
		{Type: "usage", Usage: &model.Usage{PromptTokens: 900, CompletionTokens: 2048}},
		{Type: "completed", FinishReason: "length"},
	}}
	service := newSocialLearningTestService(memoryPort, modelPort)
	engine := &SocialLearningEngine{host: service}
	err := engine.process(t.Context(), socialLearningSnapshot{ConversationID: "conversation-1", Messages: socialLearningObservations()})
	if err == nil {
		t.Fatal("process() error = nil")
	}
	message := err.Error()
	for _, required := range []string{`finishReason="length"`, "completionTokens=2048"} {
		if !strings.Contains(message, required) {
			t.Fatalf("error %q does not contain %q", message, required)
		}
	}
	for _, forbidden := range []string{"EOF", "credential-marker", "reasoning content"} {
		if strings.Contains(message, forbidden) {
			t.Fatalf("error %q contains forbidden %q", message, forbidden)
		}
	}
	if memoryPort.storedCount() != 0 {
		t.Fatalf("storedCount() = %d", memoryPort.storedCount())
	}
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

func TestSocialLearningWorkerCountsStorageFailure(t *testing.T) {
	memoryPort := &socialLearningMemory{storeErr: errors.New("database failed")}
	service := newSocialLearningTestService(memoryPort, &socialLearningModel{draft: validSocialLearningDraft()})
	engine := newSocialLearningEngine(service, 1)
	defer engine.Close()
	engine.Enqueue(socialLearningSnapshot{ConversationID: "conversation-1", Messages: socialLearningObservations()})
	waitForSocialLearningStat(t, engine, func(stats SocialLearningStats) bool { return stats.Failed == 1 })
	if stats := engine.Stats(); stats.Succeeded != 0 {
		t.Fatalf("Stats() = %#v", stats)
	}
}

func TestSocialLearningCloseCancelsBlockedModel(t *testing.T) {
	started := make(chan struct{})
	service := newSocialLearningTestService(&socialLearningMemory{}, &socialLearningModel{block: true, started: started})
	engine := newSocialLearningEngine(service, 1)
	engine.Enqueue(socialLearningSnapshot{ConversationID: "conversation-1", Messages: socialLearningObservations()})
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("worker did not start model request")
	}
	done := make(chan struct{})
	go func() {
		engine.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Close() did not cancel blocked model request")
	}
}

func TestAmbientLearningEnqueuesEveryThresholdOnce(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	engine := &SocialLearningEngine{ctx: ctx, cancel: cancel, queue: make(chan socialLearningSnapshot, 2)}
	service := NewCompanionService()
	service.socialLearning = engine
	service.interactions["conversation-1"] = publicAmbientBinding()
	service.ambient.decideHook = func(context.Context, ambientBatch) (ParticipationResult, error) {
		return ParticipationResult{Action: ParticipationSilent}, nil
	}
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

func waitForSocialLearningStat(t *testing.T, engine *SocialLearningEngine, condition func(SocialLearningStats) bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		// The worker is intentionally asynchronous; polling observes its atomic counters.
		time.Sleep(time.Millisecond)
		if condition(engine.Stats()) {
			return
		}
	}
	t.Fatal("timed out waiting for social learning worker")
}
