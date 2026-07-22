package companion

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"fairy/character"
	"fairy/config"
	"fairy/interaction"
	"fairy/memory"
	"fairy/model"
)

func validAmbientObservation() AmbientObservation {
	return AmbientObservation{MessageID: "m1", SenderID: "u1", SenderName: "群友", Text: "这话题挺有意思", IsNew: true, TimestampUnixMS: 1}
}

func validParticipationRequest() ParticipationRequest {
	return ParticipationRequest{
		ConversationID: "c1", EvaluationReason: ParticipationReasonMessage,
		Messages: []AmbientObservation{validAmbientObservation()},
	}
}

func TestCompileParticipationIsStrict(t *testing.T) {
	messages := []AmbientObservation{validAmbientObservation()}
	tests := []struct {
		draft  string
		action ParticipationAction
	}{
		{`{"action":"reply","targetMessageId":"m1"}`, ParticipationReply},
		{`{"action":"wait","waitSeconds":7}`, ParticipationWait},
		{`{"action":"silent"}`, ParticipationSilent},
		{"  {\"action\":\"silent\"}  ", ParticipationSilent},
		{"```json\n{\"action\":\"wait\",\"waitSeconds\":3}\n```", ParticipationWait},
	}
	for _, test := range tests {
		result, err := CompileParticipation(test.draft, messages)
		if err != nil || result.Action != test.action {
			t.Fatalf("draft %q: result=%#v err=%v", test.draft, result, err)
		}
	}
	for _, invalid := range []string{
		`{"action":"maybe"}`,
		`{"action":"reply"}`,
		`{"action":"reply","targetMessageId":"missing"}`,
		`{"action":"reply","targetMessageId":null}`,
		`{"action":"reply","targetMessageId":"m1","waitSeconds":1}`,
		`{"action":"wait","waitSeconds":0}`,
		`{"action":"wait","waitSeconds":301}`,
		`{"action":"wait","waitSeconds":1.5}`,
		`{"action":"silent","targetMessageId":null}`,
		`{"action":"silent","reason":"because"}`,
		`{"action":"silent"} trailing`,
		``,
	} {
		if _, err := CompileParticipation(invalid, messages); err == nil {
			t.Fatalf("invalid participation accepted: %q", invalid)
		}
	}
}

func TestValidateParticipationBoundsAndReason(t *testing.T) {
	request := validParticipationRequest()
	if err := ValidateParticipationRequest(request); err != nil {
		t.Fatal(err)
	}
	waitRequest := request
	waitRequest.EvaluationReason = ParticipationReasonWaitElapsed
	waitRequest.Messages = append([]AmbientObservation(nil), request.Messages...)
	waitRequest.Messages[0].IsNew = false
	if err := ValidateParticipationRequest(waitRequest); err != nil {
		t.Fatal(err)
	}
	invalidMessage := request
	invalidMessage.Messages = append([]AmbientObservation(nil), request.Messages...)
	invalidMessage.Messages[0].IsNew = false
	if err := ValidateParticipationRequest(invalidMessage); err == nil {
		t.Fatal("message reason without new observation accepted")
	}
	invalidWait := request
	invalidWait.EvaluationReason = ParticipationReasonWaitElapsed
	if err := ValidateParticipationRequest(invalidWait); err == nil {
		t.Fatal("wait_elapsed with new observation accepted")
	}
	request.Messages[0].Text = strings.Repeat("群", maxAmbientTextRunes+1)
	if err := ValidateParticipationRequest(request); err == nil {
		t.Fatal("oversized text accepted")
	}
	request = validParticipationRequest()
	request.Messages = append(request.Messages, request.Messages[0])
	if err := ValidateParticipationRequest(request); err == nil {
		t.Fatal("duplicate message ID accepted")
	}
	request.Messages = make([]AmbientObservation, maxAmbientObservations+1)
	if err := ValidateParticipationRequest(request); err == nil {
		t.Fatal("oversized window accepted")
	}
}

func TestDeriveRecentPresenceUsesInclusiveWindows(t *testing.T) {
	now := int64(1_800_000_000_000)
	messages := []memory.MessageRecord{
		{Role: "assistant", CreatedAtUnixMS: now - time.Minute.Milliseconds()},
		{Role: "assistant", CreatedAtUnixMS: now - 5*time.Minute.Milliseconds()},
		{Role: "assistant", CreatedAtUnixMS: now - 5*time.Minute.Milliseconds() - 1},
		{Role: "assistant", CreatedAtUnixMS: now - 30*time.Minute.Milliseconds()},
		{Role: "assistant", CreatedAtUnixMS: now - 30*time.Minute.Milliseconds() - 1},
		{Role: "user", CreatedAtUnixMS: now},
	}
	presence, err := DeriveRecentPresence(messages, now)
	if err != nil {
		t.Fatal(err)
	}
	if presence.AssistantReplies5Minutes != 2 || presence.AssistantReplies30Minutes != 4 {
		t.Fatalf("presence = %#v", presence)
	}
	if presence.SecondsSinceLastReply == nil || *presence.SecondsSinceLastReply != 60 {
		t.Fatalf("seconds since last reply = %#v", presence.SecondsSinceLastReply)
	}
	empty, err := DeriveRecentPresence(nil, now)
	if err != nil || empty.SecondsSinceLastReply != nil {
		t.Fatalf("empty presence = %#v, %v", empty, err)
	}
	if _, err := DeriveRecentPresence([]memory.MessageRecord{{Role: "assistant", CreatedAtUnixMS: now + 1}}, now); err == nil {
		t.Fatal("future assistant timestamp accepted")
	}
}

func TestBuildParticipationInputHasPolicyPresenceAndNoProfile(t *testing.T) {
	seconds := int64(12)
	input, err := BuildParticipationInput(character.Record{
		CharacterID: "character-1", Revision: 1, Name: "亚托莉", Description: "自然参与群聊", TextLanguage: "zh", SpeakingLanguage: "zh",
	}, publicAmbientResolved(), ParticipationReasonMessage, []AmbientObservation{validAmbientObservation()}, RecentPresence{
		AssistantReplies5Minutes: 2, AssistantReplies30Minutes: 4, SecondsSinceLastReply: &seconds,
	})
	if err != nil {
		t.Fatal(err)
	}
	joined := ""
	for _, item := range input {
		joined += item.Content
	}
	if strings.Contains(joined, "preferredName") || strings.Contains(joined, `"contextType":"user_profile"`) {
		t.Fatalf("participation input contains private profile: %s", joined)
	}
	for _, want := range []string{"群友", "ambient_observations", `"memoryPolicy":"public"`, `"presenceProjection":"public_peer"`, "public social setting", `"evaluationReason":"message"`, `"assistantReplies5Minutes":2`} {
		if !strings.Contains(joined, want) {
			t.Fatalf("participation input missing %q: %s", want, joined)
		}
	}
}

func TestDeriveParticipationSignalsPreservesDirectedQuestionAndFrequencyFacts(t *testing.T) {
	now := int64((10 * time.Minute) / time.Millisecond)
	messages := []AmbientObservation{
		{MessageID: "m1", SenderID: "u1", SenderName: "甲", Text: "你觉得呢？", DirectedToBot: true, IsNew: true, TimestampUnixMS: now - 5*int64(time.Second.Milliseconds())},
		{MessageID: "m2", SenderID: "u2", SenderName: "乙", Text: "你觉得呢？", IsNew: true, TimestampUnixMS: now},
	}
	signals, err := DeriveParticipationSignals(messages, []memory.MessageRecord{{Role: "assistant", CreatedAtUnixMS: now - 2*int64(time.Minute.Milliseconds())}, {Role: "user", CreatedAtUnixMS: now - int64(time.Minute.Milliseconds())}}, now)
	if err != nil {
		t.Fatalf("DeriveParticipationSignals() error = %v", err)
	}
	if signals.DirectedCount != 1 || signals.QuestionCount != 2 || signals.PendingCount != 2 || signals.RepetitionCount != 1 {
		t.Fatalf("signals = %#v", signals)
	}
	if signals.RecentSelfReplyRatio != 0.5 || signals.EffectiveReplyFrequencyPerMinute <= 0 {
		t.Fatalf("frequency signals = %#v", signals)
	}
}

type participationMemory struct {
	MemoryPort
	bootstrap memory.ConversationBootstrap
	binding   interaction.Binding
	found     bool
	lookupErr error
}

func (m participationMemory) LoadConversation(string) (memory.ConversationBootstrap, error) {
	return m.bootstrap, nil
}

func (m participationMemory) LookupEndpointForConversation(string) (interaction.Binding, bool, error) {
	if m.lookupErr != nil {
		return interaction.Binding{}, false, m.lookupErr
	}
	return m.binding, m.found, nil
}

type participationCatalog struct{ record character.Record }

func (c participationCatalog) List() (character.Catalog, error) {
	return character.Catalog{Characters: []character.Record{c.record}}, nil
}

type participationConfig struct{ ConfigSource }

func (participationConfig) ModelConnection() (config.ModelConnection, error) {
	return config.ModelConnection{Model: "model-1", Capabilities: config.GatewayCapabilities{PromptCacheKey: true}}, nil
}

type participationModel struct {
	ModelPort
	draft   string
	request model.CompiledPromptRequest
	err     error
}

func (m *participationModel) ExecuteRequestContext(ctx context.Context, request model.CompiledPromptRequest) ([]model.StreamEvent, error) {
	m.request = request
	if m.err != nil {
		return nil, m.err
	}
	return []model.StreamEvent{{Type: "text_delta", Data: m.draft}}, nil
}

func TestDecideParticipationRequiresAmbientPublicAndPropagatesContext(t *testing.T) {
	modelPort := &participationModel{draft: `{"action":"silent"}`}
	service := NewCompanionService()
	service.memory = participationMemory{bootstrap: memory.ConversationBootstrap{Conversation: memory.ConversationRecord{ID: "c1", CharacterID: "character-1"}}}
	service.model = modelPort
	service.characters = participationCatalog{record: character.Record{CharacterID: "character-1", Revision: 1, Name: "亚托莉", Description: "群友", TextLanguage: "zh", SpeakingLanguage: "zh"}}
	service.cfg = participationConfig{}
	request := validParticipationRequest()
	if _, err := service.DecideParticipation(t.Context(), request); err == nil || !strings.Contains(err.Error(), "no interaction binding") {
		t.Fatalf("missing interaction error = %v", err)
	}
	if err := service.BindInteraction("c1", publicAmbientBinding()); err != nil {
		t.Fatal(err)
	}
	result, err := service.DecideParticipation(t.Context(), request)
	if err != nil || result.Action != ParticipationSilent {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if modelPort.request.Shape.Lane != model.PromptLaneParticipate || modelPort.request.Shape.PromptCacheKey != "fairy:c1:participate" || modelPort.request.Shape.MaxOutputTokens != 256 {
		t.Fatalf("request shape = %#v", modelPort.request.Shape)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	modelPort.err = canceled.Err()
	if _, err := service.DecideParticipation(canceled, request); !errors.Is(err, context.Canceled) {
		t.Fatalf("context error = %v", err)
	}
}
