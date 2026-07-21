package companion

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"fairy/character"
	"fairy/config"
	"fairy/memory"
	"fairy/model"
)

func validGroupObservation() GroupObservation {
	return GroupObservation{MessageID: "m1", SenderID: "u1", SenderName: "群友", Text: "这话题挺有意思", IsNew: true, TimestampUnixMS: 1}
}

func validGroupParticipationRequest() GroupParticipationRequest {
	return GroupParticipationRequest{
		ConversationID: "c1", EvaluationReason: GroupParticipationReasonMessage,
		Messages: []GroupObservation{validGroupObservation()},
	}
}

func TestCompileGroupParticipationIsStrict(t *testing.T) {
	messages := []GroupObservation{validGroupObservation()}
	tests := []struct {
		draft  string
		action GroupParticipationAction
	}{
		{`{"action":"reply","targetMessageId":"m1"}`, GroupParticipationReply},
		{`{"action":"wait","waitSeconds":7}`, GroupParticipationWait},
		{`{"action":"silent"}`, GroupParticipationSilent},
	}
	for _, test := range tests {
		result, err := CompileGroupParticipation(test.draft, messages)
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
		if _, err := CompileGroupParticipation(invalid, messages); err == nil {
			t.Fatalf("invalid participation accepted: %q", invalid)
		}
	}
}

func TestValidateGroupParticipationBoundsAndReason(t *testing.T) {
	request := validGroupParticipationRequest()
	if err := ValidateGroupParticipationRequest(request); err != nil {
		t.Fatal(err)
	}
	waitRequest := request
	waitRequest.EvaluationReason = GroupParticipationReasonWaitElapsed
	waitRequest.Messages = append([]GroupObservation(nil), request.Messages...)
	waitRequest.Messages[0].IsNew = false
	if err := ValidateGroupParticipationRequest(waitRequest); err != nil {
		t.Fatal(err)
	}
	invalidMessage := request
	invalidMessage.Messages = append([]GroupObservation(nil), request.Messages...)
	invalidMessage.Messages[0].IsNew = false
	if err := ValidateGroupParticipationRequest(invalidMessage); err == nil {
		t.Fatal("message reason without new observation accepted")
	}
	invalidWait := request
	invalidWait.EvaluationReason = GroupParticipationReasonWaitElapsed
	if err := ValidateGroupParticipationRequest(invalidWait); err == nil {
		t.Fatal("wait_elapsed with new observation accepted")
	}
	request.Messages[0].Text = strings.Repeat("群", maxGroupTextRunes+1)
	if err := ValidateGroupParticipationRequest(request); err == nil {
		t.Fatal("oversized text accepted")
	}
	request = validGroupParticipationRequest()
	request.Messages = append(request.Messages, request.Messages[0])
	if err := ValidateGroupParticipationRequest(request); err == nil {
		t.Fatal("duplicate message ID accepted")
	}
	request.Messages = make([]GroupObservation, maxGroupObservations+1)
	if err := ValidateGroupParticipationRequest(request); err == nil {
		t.Fatal("oversized window accepted")
	}
}

func TestDeriveGroupRecentPresenceUsesInclusiveWindows(t *testing.T) {
	now := int64(1_800_000_000_000)
	messages := []memory.MessageRecord{
		{Role: "assistant", CreatedAtUnixMS: now - time.Minute.Milliseconds()},
		{Role: "assistant", CreatedAtUnixMS: now - 5*time.Minute.Milliseconds()},
		{Role: "assistant", CreatedAtUnixMS: now - 5*time.Minute.Milliseconds() - 1},
		{Role: "assistant", CreatedAtUnixMS: now - 30*time.Minute.Milliseconds()},
		{Role: "assistant", CreatedAtUnixMS: now - 30*time.Minute.Milliseconds() - 1},
		{Role: "user", CreatedAtUnixMS: now},
	}
	presence, err := DeriveGroupRecentPresence(messages, now)
	if err != nil {
		t.Fatal(err)
	}
	if presence.AssistantReplies5Minutes != 2 || presence.AssistantReplies30Minutes != 4 {
		t.Fatalf("presence = %#v", presence)
	}
	if presence.SecondsSinceLastReply == nil || *presence.SecondsSinceLastReply != 60 {
		t.Fatalf("seconds since last reply = %#v", presence.SecondsSinceLastReply)
	}
	empty, err := DeriveGroupRecentPresence(nil, now)
	if err != nil || empty.SecondsSinceLastReply != nil {
		t.Fatalf("empty presence = %#v, %v", empty, err)
	}
	if _, err := DeriveGroupRecentPresence([]memory.MessageRecord{{Role: "assistant", CreatedAtUnixMS: now + 1}}, now); err == nil {
		t.Fatal("future assistant timestamp accepted")
	}
}

func TestBuildGroupParticipationInputHasPolicyPresenceAndNoProfile(t *testing.T) {
	seconds := int64(12)
	input, err := BuildGroupParticipationInput(character.Record{
		CharacterID: "character-1", Revision: 1, Name: "亚托莉", Description: "自然参与群聊", TextLanguage: "zh", SpeakingLanguage: "zh",
	}, GroupParticipationReasonMessage, []GroupObservation{validGroupObservation()}, GroupRecentPresence{
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
	for _, want := range []string{"群友", "group_observations", `"audience":"public"`, `"evaluationReason":"message"`, `"assistantReplies5Minutes":2`} {
		if !strings.Contains(joined, want) {
			t.Fatalf("participation input missing %q: %s", want, joined)
		}
	}
}

type participationMemory struct {
	MemoryPort
	bootstrap memory.ConversationBootstrap
}

func (m participationMemory) LoadConversation(string) (memory.ConversationBootstrap, error) {
	return m.bootstrap, nil
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

func TestDecideGroupParticipationRequiresAmbientPublicAndPropagatesContext(t *testing.T) {
	modelPort := &participationModel{draft: `{"action":"silent"}`}
	service := NewCompanionService()
	service.memory = participationMemory{bootstrap: memory.ConversationBootstrap{Conversation: memory.ConversationRecord{ID: "c1", CharacterID: "character-1"}}}
	service.model = modelPort
	service.characters = participationCatalog{record: character.Record{CharacterID: "character-1", Revision: 1, Name: "亚托莉", Description: "群友", TextLanguage: "zh", SpeakingLanguage: "zh"}}
	service.cfg = participationConfig{}
	request := validGroupParticipationRequest()
	if _, err := service.DecideGroupParticipation(t.Context(), request); err == nil || !strings.Contains(err.Error(), "public ambient") {
		t.Fatalf("non-ambient error = %v", err)
	}
	if err := service.BindSurface("c1", SurfaceIMGroup); err != nil {
		t.Fatal(err)
	}
	result, err := service.DecideGroupParticipation(t.Context(), request)
	if err != nil || result.Action != GroupParticipationSilent {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if modelPort.request.Shape.Lane != model.PromptLaneParticipate || modelPort.request.Shape.PromptCacheKey != "fairy:c1:participate" || modelPort.request.Shape.MaxOutputTokens != 256 {
		t.Fatalf("request shape = %#v", modelPort.request.Shape)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	modelPort.err = canceled.Err()
	if _, err := service.DecideGroupParticipation(canceled, request); !errors.Is(err, context.Canceled) {
		t.Fatalf("context error = %v", err)
	}
}
