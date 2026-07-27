package initiative

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"fairy/character"
	"fairy/config"
	"fairy/memory"
	"fairy/model"
	"fairy/session"
)

func learningObservations() []AmbientObservation {
	return []AmbientObservation{
		{MessageID: "m1", SenderID: "u1", SenderName: "甲", Text: "最近投简历有点焦虑", TimestampUnixMS: 1000},
		{MessageID: "m2", SenderID: "u2", SenderName: "乙", Text: "先把项目经历整理清楚", TimestampUnixMS: 2000},
	}
}

func publicAmbientResolved() session.Resolved {
	return session.Resolved{
		Endpoint:  session.EndpointIM,
		Facts:     session.Facts{Audience: session.AudienceMulti, Initiation: session.InitiationAmbient, Presentation: session.PresentationChat},
		Principal: session.PrincipalNone, Memory: session.MemoryPublic,
	}
}

type learningTestHost struct {
	mu sync.Mutex

	resolved session.Resolved
	draft    string
	events   []model.StreamEvent
	modelErr error
	block    bool
	started  chan struct{}
	request  model.CompiledPromptRequest

	stored        []memory.SocialMemoryBatchInput
	upserted      []memory.SocialPersonNoteInput
	feedback      []memory.SocialReplyFeedbackInput
	warnings      []error
	metadataLoads int
}

func newLearningTestHost() *learningTestHost {
	return &learningTestHost{resolved: publicAmbientResolved()}
}

func (h *learningTestHost) ResolveInteraction(string) (session.Resolved, error) {
	return h.resolved, nil
}

func (h *learningTestHost) LoadConversationRecord(conversationID string) (memory.ConversationRecord, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.metadataLoads++
	return memory.ConversationRecord{ID: conversationID, CharacterID: "character-1"}, nil
}

func (*learningTestHost) ActiveCharacter(string) (character.Record, error) {
	return character.Record{CharacterID: "character-1", Revision: 1, Name: "Fairy", Description: "群友", TextLanguage: "zh", SpeakingLanguage: "zh"}, nil
}

func (*learningTestHost) ModelConnection() (config.ModelConnection, error) {
	return config.ModelConnection{Model: "model-1", Capabilities: config.GatewayCapabilities{PromptCacheKey: true}}, nil
}

func (h *learningTestHost) ExecuteRequest(ctx context.Context, request model.CompiledPromptRequest) ([]model.StreamEvent, error) {
	h.mu.Lock()
	block, started, err := h.block, h.started, h.modelErr
	events := append([]model.StreamEvent(nil), h.events...)
	draft := h.draft
	h.request = request
	h.mu.Unlock()
	if block {
		if started != nil {
			close(started)
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

func (h *learningTestHost) StoreSocialMemoryEntries(_ context.Context, input memory.SocialMemoryBatchInput) ([]memory.SocialMemoryEntry, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.stored = append(h.stored, input)
	return []memory.SocialMemoryEntry{{ID: "entry-1"}}, nil
}

func (h *learningTestHost) UpsertSocialPersonNote(_ context.Context, input memory.SocialPersonNoteInput) (memory.SocialPersonNote, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.upserted = append(h.upserted, input)
	return memory.SocialPersonNote{ID: "note-1"}, nil
}

func (h *learningTestHost) WarnLearning(_ string, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.warnings = append(h.warnings, err)
}

func TestLearningCompilerPreservesStrictEvidenceContract(t *testing.T) {
	messages := learningObservations()
	valid := `{"entries":[],"personNotes":[{"senderId":"u1","note":"常在群里聊求职焦虑","sourceMessageIds":["m1"]}]}`
	compiled, err := compileSocialLearning(valid, messages)
	if err != nil || len(compiled.Entries) != 0 || len(compiled.Notes) != 1 {
		t.Fatalf("compile valid draft = %#v, %v", compiled, err)
	}
	for _, invalid := range []string{
		`{"entries":null}`,
		`{"entries":[{"kind":"episode","situation":"公开讨论","content":"摘要","recallCue":"线索","sourceMessageIds":["missing"]}]}`,
		`{"entries":[],"personNotes":[{"senderId":"u1","note":"常发言","sourceMessageIds":["m2"]}]}`,
		`{"entries":[],"reason":"unknown"}`,
	} {
		if _, err := compileSocialLearning(invalid, messages); err == nil {
			t.Fatalf("accepted invalid draft: %s", invalid)
		}
	}
}

func TestLearningInputOnlyContainsExternalObservations(t *testing.T) {
	items, err := buildSocialLearningInput(character.Record{CharacterID: "character-1", Revision: 1, Name: "Fairy", Description: "群友", TextLanguage: "zh", SpeakingLanguage: "zh"}, publicAmbientResolved(), learningObservations())
	if err != nil || len(items) != 4 {
		t.Fatalf("input = %#v, %v", items, err)
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
			if _, ok := payload[forbidden]; ok {
				t.Fatalf("unexpected field %q in %#v", forbidden, payload)
			}
		}
	}
}

func TestLearningEngineUsesDedicatedLaneAndCacheIdentity(t *testing.T) {
	host := newLearningTestHost()
	host.draft = `{"entries":[{"kind":"episode","situation":"群友谈论求职准备","content":"群内会交换整理项目经历的建议","recallCue":"求职或实习准备","sourceMessageIds":["m1","m2"]}]}`
	engine := &LearningEngine{host: host}
	if err := engine.process(t.Context(), LearningSnapshot{ConversationID: "conversation-1", Messages: learningObservations()}); err != nil {
		t.Fatal(err)
	}
	host.mu.Lock()
	request := host.request
	stored := append([]memory.SocialMemoryBatchInput(nil), host.stored...)
	metadataLoads := host.metadataLoads
	host.mu.Unlock()
	if request.Shape.Lane != model.PromptLaneSocialLearn || request.Shape.PromptCacheKey != model.LaneCacheKey("conversation-1", model.PromptLaneSocialLearn) {
		t.Fatalf("request shape = %#v", request.Shape)
	}
	if request.CacheInput == nil || request.CacheInput.CharacterRevision != 1 || request.CacheInput.StablePromptHash == "" {
		t.Fatalf("cache input = %#v", request.CacheInput)
	}
	if len(stored) != 1 {
		t.Fatalf("stored = %#v", stored)
	}
	if metadataLoads != 1 {
		t.Fatalf("conversation metadata loads = %d, want 1", metadataLoads)
	}
}

func TestLearningInvalidBatchDoesNotWrite(t *testing.T) {
	host := newLearningTestHost()
	host.draft = `{"entries":[{"kind":"episode","situation":"公开讨论","content":"摘要","recallCue":"线索","sourceMessageIds":["missing"]}]}`
	engine := &LearningEngine{host: host}
	if err := engine.process(t.Context(), LearningSnapshot{ConversationID: "conversation-1", Messages: learningObservations()}); err == nil {
		t.Fatal("process accepted invalid evidence")
	}
	host.mu.Lock()
	defer host.mu.Unlock()
	if len(host.stored) != 0 || len(host.upserted) != 0 {
		t.Fatalf("invalid batch wrote entries=%#v notes=%#v", host.stored, host.upserted)
	}
}

func TestLearningQueueIsNonBlockingAndCloseCancelsModel(t *testing.T) {
	engine := NewLearningEngine(nil, 1)
	snapshot := LearningSnapshot{ConversationID: "conversation-1", Messages: learningObservations()}
	if !engine.Enqueue(snapshot) || engine.Enqueue(snapshot) {
		t.Fatalf("queue stats = %#v", engine.Stats())
	}
	engine.Close()

	started := make(chan struct{})
	host := newLearningTestHost()
	host.block = true
	host.started = started
	engine = NewLearningEngine(host, 1)
	if !engine.Enqueue(snapshot) {
		t.Fatal("enqueue blocked model = false")
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("worker did not start model request")
	}
	done := make(chan struct{})
	go func() { engine.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Close did not cancel model request")
	}
	if engine.Enqueue(snapshot) {
		t.Fatal("post-close enqueue accepted")
	}
}

var _ LearningHost = (*learningTestHost)(nil)

func (h *learningTestHost) RecordSocialReplyFeedback(_ context.Context, input memory.SocialReplyFeedbackInput) (memory.SocialReplyFeedback, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.feedback = append(h.feedback, input)
	return memory.SocialReplyFeedback{ID: "feedback-1", Outcome: input.Outcome}, nil
}

func (h *learningTestHost) WarnFeedback(string, string, error) {}
