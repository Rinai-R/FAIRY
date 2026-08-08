package presence

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"fairy/context/character"
	history "fairy/context/history/transcript"
	"fairy/context/social"
	"fairy/runtime/config"
	"fairy/runtime/model"
	"fairy/transport/session"
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

	resolved          session.Resolved
	characterRevision uint64
	draft             string
	events            []model.StreamEvent
	modelErr          error
	block             bool
	started           chan struct{}
	request           model.CompiledPromptRequest

	stored        []social.SocialMemoryBatchInput
	upserted      []social.SocialPersonNoteInput
	feedback      []social.SocialFeedbackBatchInput
	feedbackErr   error
	warnings      []error
	metadataLoads int
}

func newLearningTestHost() *learningTestHost {
	return &learningTestHost{resolved: publicAmbientResolved(), characterRevision: 1}
}

func (h *learningTestHost) ResolveInteraction(string) (session.Resolved, error) {
	return h.resolved, nil
}

func (h *learningTestHost) LoadConversationRecord(conversationID string) (history.ConversationRecord, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.metadataLoads++
	return history.ConversationRecord{ID: conversationID, CharacterID: "character-1"}, nil
}

func (h *learningTestHost) ActiveCharacter(string) (character.Record, error) {
	revision := h.characterRevision
	if revision == 0 {
		revision = 1
	}
	return character.Record{CharacterID: "character-1", Revision: revision, Name: "Fairy", Description: "群友", TextLanguage: "zh", SpeakingLanguage: "zh"}, nil
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
			select {
			case started <- struct{}{}:
			default:
			}
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

func (h *learningTestHost) StoreSocialMemoryEntries(_ context.Context, input social.SocialMemoryBatchInput) ([]social.SocialMemoryEntry, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.stored = append(h.stored, input)
	return []social.SocialMemoryEntry{{ID: "entry-1"}}, nil
}

func (h *learningTestHost) UpsertSocialPersonNote(_ context.Context, input social.SocialPersonNoteInput) (social.SocialPersonNote, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.upserted = append(h.upserted, input)
	return social.SocialPersonNote{ID: "note-1"}, nil
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
	prefix, err := buildSocialStablePrefix(character.Record{CharacterID: "character-1", Revision: 1, Name: "Fairy", Description: "群友", TextLanguage: "zh", SpeakingLanguage: "zh"}, publicAmbientResolved())
	if err != nil {
		t.Fatal(err)
	}
	items, err := buildSocialLearningInput(prefix, learningObservations())
	if err != nil || len(items) != 4 {
		t.Fatalf("input = %#v, %v", items, err)
	}
	if items[0].Type != model.PromptItemContextData || !strings.Contains(items[0].Content, `"contextType":"character"`) {
		t.Fatalf("stable character prefix = %#v", items[0])
	}
	if items[1].Type != model.PromptItemContextData || !strings.Contains(items[1].Content, `"contextType":"interaction"`) {
		t.Fatalf("stable interaction prefix = %#v", items[1])
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
	stored := append([]social.SocialMemoryBatchInput(nil), host.stored...)
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
	snapshot := LearningSnapshot{ConversationID: "conversation-1", Messages: learningObservations()}
	started := make(chan struct{}, 1)
	host := newLearningTestHost()
	host.block = true
	host.started = started
	engine := NewLearningEngine(host, 1)
	if !engine.Enqueue(snapshot) {
		t.Fatal("enqueue blocked model = false")
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("worker did not start model request")
	}
	if !engine.Enqueue(snapshot) || engine.Enqueue(snapshot) {
		t.Fatalf("queue stats = %#v", engine.Stats())
	}
	if stats := engine.Stats(); stats.Enqueued != 2 || stats.Dropped != 1 {
		t.Fatalf("queue stats = %#v, want enqueued=2 dropped=1", stats)
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

func (h *learningTestHost) RecordSocialFeedbackBatch(_ context.Context, input social.SocialFeedbackBatchInput) (social.SocialFeedbackBatchResult, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.feedback = append(h.feedback, input)
	return social.SocialFeedbackBatchResult{}, h.feedbackErr
}

func (h *learningTestHost) WarnFeedback(string, string, error) {}
