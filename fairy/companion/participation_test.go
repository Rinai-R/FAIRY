package companion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"fairy/character"
	"fairy/config"
	"fairy/initiative"
	"fairy/memory"
	"fairy/model"
	"fairy/session"
)

func validAmbientObservation() initiative.AmbientObservation {
	return initiative.AmbientObservation{MessageID: "m1", SenderID: "u1", SenderName: "群友", Text: "这话题挺有意思", IsNew: true, TimestampUnixMS: 1}
}

func validParticipationRequest() initiative.ParticipationRequest {
	return initiative.ParticipationRequest{
		ConversationID: "c1", EvaluationReason: initiative.ParticipationReasonMessage,
		Messages: []initiative.AmbientObservation{validAmbientObservation()},
	}
}

func validReplyDecision(target string) string {
	return fmt.Sprintf(`{"action":"reply","targetMessageId":%q,"intent":{"replyAct":"接住话题","tone":"自然","relationshipSignal":"平等群友","replyMode":"brief","focus":"当前发言","avoid":[],"referenceInfo":"","memoryQuery":"","expressionQuery":"轻松接话"}}`, target)
}

func TestParticipationInstructionsRequireOneConversationalHook(t *testing.T) {
	for _, required := range []string{"choose exactly one conversational hook", "surrounding messages are background only", `"focus":"<one conversational hook to answer>"`} {
		if !strings.Contains(initiative.ParticipationInstructions, required) {
			t.Fatalf("ParticipationInstructions missing %q", required)
		}
	}
}

func TestCompileParticipationIsStrict(t *testing.T) {
	messages := []initiative.AmbientObservation{validAmbientObservation()}
	tests := []struct {
		draft  string
		action initiative.ParticipationAction
	}{
		{validReplyDecision("m1"), initiative.ParticipationReply},
		{`{"action":"wait","waitSeconds":7}`, initiative.ParticipationWait},
		{`{"action":"silent"}`, initiative.ParticipationSilent},
		{"  {\"action\":\"silent\"}  ", initiative.ParticipationSilent},
		{"```json\n{\"action\":\"wait\",\"waitSeconds\":3}\n```", initiative.ParticipationWait},
	}
	for _, test := range tests {
		result, err := initiative.CompileParticipation(test.draft, messages)
		if err != nil || result.Action != test.action {
			t.Fatalf("draft %q: result=%#v err=%v", test.draft, result, err)
		}
		if test.action == initiative.ParticipationReply && (result.Intent == nil || result.Intent.ReplyMode != "brief" || result.Intent.ExpressionQuery != "轻松接话") {
			t.Fatalf("reply intent = %#v", result.Intent)
		}
	}
	for _, invalid := range []string{
		`{"action":"maybe"}`,
		`{"action":"reply"}`,
		`{"action":"reply","targetMessageId":"missing"}`,
		`{"action":"reply","targetMessageId":null}`,
		`{"action":"reply","targetMessageId":"m1","waitSeconds":1}`,
		`{"action":"reply","targetMessageId":"m1","intent":null}`,
		`{"action":"reply","targetMessageId":"m1","intent":{"replyAct":"接话","tone":"自然","relationshipSignal":"群友","replyMode":"verbose","focus":"话题","avoid":[],"referenceInfo":"","memoryQuery":"","expressionQuery":"接话"}}`,
		`{"action":"reply","targetMessageId":"m1","intent":{"replyAct":"接话","tone":"自然","relationshipSignal":"群友","replyMode":"brief","focus":"话题","avoid":[],"referenceInfo":"","memoryQuery":"","expressionQuery":"接话","reason":"不应输出"}}`,
		`{"action":"wait","waitSeconds":0}`,
		`{"action":"wait","waitSeconds":301}`,
		`{"action":"wait","waitSeconds":1.5}`,
		`{"action":"silent","targetMessageId":null}`,
		`{"action":"silent","reason":"because"}`,
		`{"action":"silent"} trailing`,
		``,
	} {
		if _, err := initiative.CompileParticipation(invalid, messages); err == nil {
			t.Fatalf("invalid participation accepted: %q", invalid)
		}
	}
}

func TestCompileParticipationDerivesExpressionQueryFromFocusWhenProviderOmitsIt(t *testing.T) {
	messages := []initiative.AmbientObservation{validAmbientObservation()}
	draft := `{"action":"reply","targetMessageId":"m1","intent":{"replyAct":"接话","tone":"自然","relationshipSignal":"群友","replyMode":"brief","focus":"对方刚提到的实验结果","avoid":[],"referenceInfo":"","memoryQuery":""}}`
	result, err := initiative.CompileParticipation(draft, messages)
	if err != nil {
		t.Fatal(err)
	}
	if result.Intent == nil || result.Intent.ExpressionQuery != "对方刚提到的实验结果" {
		t.Fatalf("reply intent = %#v", result.Intent)
	}
}

func TestReplyIntentIsNotSerializedAcrossSurfaceContracts(t *testing.T) {
	for name, value := range map[string]any{
		"participation": initiative.ParticipationResult{Action: initiative.ParticipationReply, Intent: &initiative.ReplyIntent{ReplyAct: "接话", RelationshipSignal: "群友", ExpressionQuery: "轻松接话"}},
		"submit":        SubmitTurnRequest{ConversationID: "c1", Input: "hello", ReplyIntent: &ReplyIntent{ReplyAct: "接话", RelationshipSignal: "群友", ExpressionQuery: "轻松接话"}},
	} {
		payload, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("%s marshal: %v", name, err)
		}
		for _, forbidden := range []string{"replyAct", "relationshipSignal", "expressionQuery"} {
			if strings.Contains(string(payload), forbidden) {
				t.Fatalf("%s leaked reply intent: %s", name, payload)
			}
		}
	}
}

func TestValidateParticipationBoundsAndReason(t *testing.T) {
	request := validParticipationRequest()
	if err := initiative.ValidateParticipationRequest(request); err != nil {
		t.Fatal(err)
	}
	waitRequest := request
	waitRequest.EvaluationReason = initiative.ParticipationReasonWaitElapsed
	waitRequest.Messages = append([]initiative.AmbientObservation(nil), request.Messages...)
	waitRequest.Messages[0].IsNew = false
	if err := initiative.ValidateParticipationRequest(waitRequest); err != nil {
		t.Fatal(err)
	}
	invalidMessage := request
	invalidMessage.Messages = append([]initiative.AmbientObservation(nil), request.Messages...)
	invalidMessage.Messages[0].IsNew = false
	if err := initiative.ValidateParticipationRequest(invalidMessage); err == nil {
		t.Fatal("message reason without new observation accepted")
	}
	invalidWait := request
	invalidWait.EvaluationReason = initiative.ParticipationReasonWaitElapsed
	if err := initiative.ValidateParticipationRequest(invalidWait); err == nil {
		t.Fatal("wait_elapsed with new observation accepted")
	}
	request.Messages[0].Text = strings.Repeat("群", initiative.MaxAmbientTextRunes+1)
	if err := initiative.ValidateParticipationRequest(request); err == nil {
		t.Fatal("oversized text accepted")
	}
	request = validParticipationRequest()
	request.Messages = append(request.Messages, request.Messages[0])
	if err := initiative.ValidateParticipationRequest(request); err == nil {
		t.Fatal("duplicate message ID accepted")
	}
	request.Messages = make([]initiative.AmbientObservation, initiative.MaxAmbientObservations+1)
	if err := initiative.ValidateParticipationRequest(request); err == nil {
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
	presence, err := initiative.DeriveRecentPresence(messages, now)
	if err != nil {
		t.Fatal(err)
	}
	if presence.AssistantReplies5Minutes != 2 || presence.AssistantReplies30Minutes != 4 {
		t.Fatalf("presence = %#v", presence)
	}
	if presence.SecondsSinceLastReply == nil || *presence.SecondsSinceLastReply != 60 {
		t.Fatalf("seconds since last reply = %#v", presence.SecondsSinceLastReply)
	}
	empty, err := initiative.DeriveRecentPresence(nil, now)
	if err != nil || empty.SecondsSinceLastReply != nil {
		t.Fatalf("empty presence = %#v, %v", empty, err)
	}
	if _, err := initiative.DeriveRecentPresence([]memory.MessageRecord{{Role: "assistant", CreatedAtUnixMS: now + 1}}, now); err == nil {
		t.Fatal("future assistant timestamp accepted")
	}
}

func TestBuildParticipationInputHasPolicyPresenceAndNoProfile(t *testing.T) {
	seconds := int64(12)
	input, err := initiative.BuildParticipationInput(character.Record{
		CharacterID: "character-1", Revision: 1, Name: "亚托莉", Description: "自然参与群聊", TextLanguage: "zh", SpeakingLanguage: "zh",
	}, publicAmbientResolved(), initiative.ParticipationReasonMessage, []initiative.AmbientObservation{validAmbientObservation()}, initiative.RecentPresence{
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

func TestBuildParticipationInputKeepsAcceptedObservationPrefixStable(t *testing.T) {
	now := int64(1_800_000_000_000)
	record := character.Record{
		CharacterID: "character-1", Revision: 1, Name: "亚托莉", Description: "自然参与群聊", TextLanguage: "zh", SpeakingLanguage: "zh",
	}
	first := initiative.AmbientObservation{
		MessageID: "m1", SenderID: "u1", SenderName: "甲", Text: "你们觉得呢？", DirectedToBot: true, IsNew: true, TimestampUnixMS: now - 1000,
	}
	before, err := initiative.BuildParticipationInputWithSignals(record, publicAmbientResolved(), initiative.ParticipationReasonMessage, []initiative.AmbientObservation{first}, nil, initiative.RecentPresence{}, now, nil)
	if err != nil {
		t.Fatal(err)
	}
	first.IsNew = false
	second := initiative.AmbientObservation{
		MessageID: "m2", SenderID: "u2", SenderName: "乙", Text: "我觉得可以", IsNew: true, TimestampUnixMS: now,
	}
	after, err := initiative.BuildParticipationInputWithSignals(record, publicAmbientResolved(), initiative.ParticipationReasonMessage, []initiative.AmbientObservation{first, second}, nil, initiative.RecentPresence{}, now, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 4 || len(after) != 5 {
		t.Fatalf("input lengths before=%d after=%d", len(before), len(after))
	}
	for index := 0; index < 3; index++ {
		if before[index] != after[index] {
			t.Fatalf("cacheable prefix item %d changed:\nbefore=%s\nafter=%s", index, before[index].Content, after[index].Content)
		}
	}
	if strings.Contains(after[2].Content, `"isNew"`) {
		t.Fatalf("immutable observation contains isNew: %s", after[2].Content)
	}
	if !strings.Contains(after[len(after)-1].Content, `"newMessageIds":["m2"]`) {
		t.Fatalf("dynamic decision context missing new message IDs: %s", after[len(after)-1].Content)
	}
	waitInput, err := initiative.BuildParticipationInputWithSignals(record, publicAmbientResolved(), initiative.ParticipationReasonWaitElapsed, []initiative.AmbientObservation{first}, nil, initiative.RecentPresence{}, now, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(waitInput[len(waitInput)-1].Content, `"newMessageIds":[]`) {
		t.Fatalf("wait decision context must contain an empty new message list: %s", waitInput[len(waitInput)-1].Content)
	}
}

func TestBuildParticipationInputKeepsCachePrefixStableAfterRollingWindowSlides(t *testing.T) {
	now := int64(1_800_000_000_000)
	record := character.Record{
		CharacterID: "character-1", Revision: 1, Name: "亚托莉", Description: "自然参与群聊", TextLanguage: "zh", SpeakingLanguage: "zh",
	}
	cacheBefore := make([]initiative.AmbientObservation, 0, initiative.MaxAmbientObservations)
	for i := 1; i <= initiative.MaxAmbientObservations; i++ {
		cacheBefore = append(cacheBefore, initiative.AmbientObservation{
			MessageID: fmt.Sprintf("m%d", i), SenderID: fmt.Sprintf("u%d", i%6), SenderName: fmt.Sprintf("群友%d", i%6),
			Text: fmt.Sprintf("第%d条群聊观察", i), TimestampUnixMS: now + int64(i),
		})
	}
	windowBefore := append([]initiative.AmbientObservation(nil), cacheBefore...)
	windowBefore[len(windowBefore)-1].IsNew = true
	before, err := initiative.BuildParticipationInputWithSignals(record, publicAmbientResolved(), initiative.ParticipationReasonMessage, windowBefore, cacheBefore, initiative.RecentPresence{}, now+100, nil)
	if err != nil {
		t.Fatal(err)
	}

	cacheAfter := append([]initiative.AmbientObservation(nil), cacheBefore...)
	cacheAfter = append(cacheAfter, initiative.AmbientObservation{
		MessageID: "m21", SenderID: "u3", SenderName: "群友3", Text: "第21条群聊观察", TimestampUnixMS: now + 21,
		IsNew: true,
	})
	windowAfter := append([]initiative.AmbientObservation(nil), cacheAfter[1:]...)
	windowAfter[len(windowAfter)-1].IsNew = true
	after, err := initiative.BuildParticipationInputWithSignals(record, publicAmbientResolved(), initiative.ParticipationReasonMessage, windowAfter, cacheAfter, initiative.RecentPresence{}, now+200, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != initiative.MaxAmbientObservations+3 || len(after) != initiative.MaxAmbientObservations+4 {
		t.Fatalf("input lengths before=%d after=%d", len(before), len(after))
	}
	for index := 0; index < len(before)-1; index++ {
		if before[index] != after[index] {
			t.Fatalf("cache prefix item %d changed after rolling window slid:\nbefore=%s\nafter=%s", index, before[index].Content, after[index].Content)
		}
	}
	decisionTail := after[len(after)-1].Content
	if !strings.Contains(decisionTail, "\"newMessageIds\":[\"m21\"]") {
		t.Fatalf("decision tail missing new message: %s", decisionTail)
	}
	if strings.Contains(decisionTail, "\"replyCandidateMessageIds\":[\"m1\"") || !strings.Contains(decisionTail, "\"replyCandidateMessageIds\":[\"m2\"") {
		t.Fatalf("reply candidates must be active rolling window only: %s", decisionTail)
	}
	if strings.Contains(after[2].Content, "\"isNew\"") {
		t.Fatalf("cache observation contains isNew: %s", after[2].Content)
	}
}

func TestDeriveParticipationSignalsContainsOnlyObjectiveTimingAndPresenceFacts(t *testing.T) {
	now := int64((10 * time.Minute) / time.Millisecond)
	messages := []initiative.AmbientObservation{
		{MessageID: "m1", SenderID: "u1", SenderName: "甲", Text: "你觉得呢？", DirectedToBot: true, IsNew: true, TimestampUnixMS: now - 5*int64(time.Second.Milliseconds())},
		{MessageID: "m2", SenderID: "u2", SenderName: "乙", Text: "你觉得呢？", IsNew: true, TimestampUnixMS: now},
	}
	signals, err := initiative.DeriveParticipationSignals(messages, []memory.MessageRecord{{Role: "assistant", CreatedAtUnixMS: now - 2*int64(time.Minute.Milliseconds())}, {Role: "user", CreatedAtUnixMS: now - int64(time.Minute.Milliseconds())}}, now)
	if err != nil {
		t.Fatalf("DeriveParticipationSignals() error = %v", err)
	}
	if signals.DirectedCount != 1 || signals.PendingCount != 2 || signals.DistinctSenderCount != 2 || signals.MessageSpanSeconds != 5 {
		t.Fatalf("signals = %#v", signals)
	}
	if signals.RecentSelfReplyRatio != 0.5 || signals.EffectiveReplyFrequencyPerMinute <= 0 {
		t.Fatalf("frequency signals = %#v", signals)
	}
	payload, err := json.Marshal(signals)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"questionCount", "requestCount", "shortReactionCount", "repetitionCount"} {
		if strings.Contains(string(payload), forbidden) {
			t.Fatalf("semantic content score leaked into participation facts: %s", payload)
		}
	}
}

func TestParticipationActivityMatchesTranscriptSignalsAndPromptTail(t *testing.T) {
	now := int64(1_800_000_000_000)
	latestMinute := now - time.Minute.Milliseconds()
	latestOld := now - 40*time.Minute.Milliseconds()
	observations := []initiative.AmbientObservation{
		{MessageID: "m1", SenderID: "u1", SenderName: "甲", Text: "你觉得呢？", DirectedToBot: true, IsNew: true, TimestampUnixMS: now - time.Second.Milliseconds()},
		{MessageID: "m2", SenderID: "u2", SenderName: "乙", Text: "我觉得可以", IsNew: true, TimestampUnixMS: now},
	}
	record := character.Record{CharacterID: "character-1", Revision: 1, Name: "亚托莉", Description: "自然参与群聊", TextLanguage: "zh", SpeakingLanguage: "zh"}
	tests := []struct {
		name       string
		transcript []memory.MessageRecord
		activity   memory.ConversationActivity
	}{
		{name: "empty history"},
		{
			name: "inclusive five and thirty minute boundaries",
			transcript: []memory.MessageRecord{
				{Role: "assistant", CreatedAtUnixMS: latestMinute},
				{Role: "assistant", CreatedAtUnixMS: now - 5*time.Minute.Milliseconds()},
				{Role: "assistant", CreatedAtUnixMS: now - 5*time.Minute.Milliseconds() - 1},
				{Role: "assistant", CreatedAtUnixMS: now - 30*time.Minute.Milliseconds()},
				{Role: "assistant", CreatedAtUnixMS: now - 30*time.Minute.Milliseconds() - 1},
				{Role: "user", CreatedAtUnixMS: now - 10*time.Minute.Milliseconds()},
				{Role: "user", CreatedAtUnixMS: now - 30*time.Minute.Milliseconds()},
				{Role: "user", CreatedAtUnixMS: now - 30*time.Minute.Milliseconds() - 1},
			},
			activity: memory.ConversationActivity{
				AssistantMessages5Minutes: 2, AssistantMessages30Minutes: 4,
				UserMessages30Minutes: 2, LastAssistantMessageAtUnixMS: &latestMinute,
			},
		},
		{
			name: "latest assistant predates activity window",
			transcript: []memory.MessageRecord{
				{Role: "assistant", CreatedAtUnixMS: latestOld},
				{Role: "user", CreatedAtUnixMS: now - 10*time.Minute.Milliseconds()},
			},
			activity: memory.ConversationActivity{UserMessages30Minutes: 1, LastAssistantMessageAtUnixMS: &latestOld},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transcriptPresence, err := initiative.DeriveRecentPresence(test.transcript, now)
			if err != nil {
				t.Fatal(err)
			}
			activityPresence, err := initiative.DeriveRecentPresenceFromActivity(test.activity, now)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(transcriptPresence, activityPresence) {
				t.Fatalf("presence mismatch: transcript=%#v activity=%#v", transcriptPresence, activityPresence)
			}
			transcriptSignals, err := initiative.DeriveParticipationSignals(observations, test.transcript, now)
			if err != nil {
				t.Fatal(err)
			}
			activitySignals, err := initiative.DeriveParticipationSignalsFromActivity(observations, test.activity, now)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(transcriptSignals, activitySignals) {
				t.Fatalf("signals mismatch: transcript=%#v activity=%#v", transcriptSignals, activitySignals)
			}
			transcriptInput, err := initiative.BuildParticipationInputWithSignals(record, publicAmbientResolved(), initiative.ParticipationReasonMessage, observations, nil, transcriptPresence, now, test.transcript)
			if err != nil {
				t.Fatal(err)
			}
			activityInput, err := initiative.BuildParticipationInputWithActivity(record, publicAmbientResolved(), initiative.ParticipationReasonMessage, observations, nil, activityPresence, now, test.activity)
			if err != nil {
				t.Fatal(err)
			}
			transcriptTail := transcriptInput[len(transcriptInput)-1]
			activityTail := activityInput[len(activityInput)-1]
			if !reflect.DeepEqual(transcriptTail, activityTail) {
				t.Fatalf("prompt tail mismatch:\ntranscript=%s\nactivity=%s", transcriptTail.Content, activityTail.Content)
			}
		})
	}
}

type participationMemory struct {
	bootstrap     memory.ConversationBootstrap
	binding       session.Binding
	found         bool
	lookupErr     error
	retrieveCalls int
	activityLoads int
	retrieved     memory.SocialMemoryContext
}

func (m *participationMemory) LoadConversationActivity(_ string, nowUnixMS int64) (memory.ConversationActivity, error) {
	m.activityLoads++
	activity := memory.ConversationActivity{Conversation: m.bootstrap.Conversation}
	var latestAssistant int64
	for _, message := range m.bootstrap.Messages {
		age := nowUnixMS - message.CreatedAtUnixMS
		switch message.Role {
		case "assistant":
			if age < 0 {
				return memory.ConversationActivity{}, errors.New("assistant message timestamp is after activity evaluation time")
			}
			if age <= 5*time.Minute.Milliseconds() {
				activity.AssistantMessages5Minutes++
			}
			if age <= 30*time.Minute.Milliseconds() {
				activity.AssistantMessages30Minutes++
			}
			if message.CreatedAtUnixMS > latestAssistant {
				latestAssistant = message.CreatedAtUnixMS
			}
		case "user":
			if age >= 0 && age <= 30*time.Minute.Milliseconds() {
				activity.UserMessages30Minutes++
			}
		}
	}
	if latestAssistant > 0 {
		activity.LastAssistantMessageAtUnixMS = &latestAssistant
	}
	return activity, nil
}

func (m *participationMemory) LookupEndpointForConversation(string) (session.Binding, bool, error) {
	if m.lookupErr != nil {
		return session.Binding{}, false, m.lookupErr
	}
	return m.binding, m.found, nil
}

func (m *participationMemory) RetrieveSocialMemoryContext(context.Context, string, string, string) (memory.SocialMemoryContext, error) {
	m.retrieveCalls++
	return m.retrieved, nil
}

func (m *participationMemory) ListSocialPersonNotes(context.Context, string, string, []string) ([]memory.SocialPersonNote, error) {
	return nil, nil
}

func (m *participationMemory) RecentSocialFeedbackSummary(context.Context, string, string) (memory.RecentSocialFeedbackSummary, error) {
	return memory.RecentSocialFeedbackSummary{}, nil
}

func participationMemoryPorts(store *participationMemory) memoryPorts {
	return memoryPorts{
		ambient: ambientMemoryPorts{
			bindings:        store,
			activity:        store,
			socialRetrieval: store,
			socialContext:   store,
		},
	}
}

type participationCharacterLookup struct{ record character.Record }

func (c participationCharacterLookup) Lookup(characterID string) (character.Record, bool, error) {
	return c.record, c.record.CharacterID == characterID, nil
}

type participationConfig struct{ ConfigSource }

func (participationConfig) ModelConnection() (config.ModelConnection, error) {
	return config.ModelConnection{Model: "model-1", Capabilities: config.GatewayCapabilities{PromptCacheKey: true}}, nil
}

type participationDecisionHost struct {
	service *CompanionService
}

func decideParticipation(service *CompanionService, ctx context.Context, request initiative.ParticipationRequest) (initiative.ParticipationResult, error) {
	return initiative.NewEngine(participationDecisionHost{service: service}).DecideParticipation(ctx, request)
}

func (h participationDecisionHost) LoadConversationActivity(conversationID string, nowUnixMS int64) (memory.ConversationActivity, error) {
	if h.service == nil || h.service.memory.ambient.activity == nil {
		return memory.ConversationActivity{}, ErrTurnRuntimeUnavailable
	}
	return h.service.memory.ambient.activity.LoadConversationActivity(conversationID, nowUnixMS)
}

func (h participationDecisionHost) ResolveInteraction(conversationID string) (session.Resolved, error) {
	if h.service == nil {
		return session.Resolved{}, ErrTurnRuntimeUnavailable
	}
	return h.service.ResolveInteraction(conversationID)
}

func (h participationDecisionHost) ActiveCharacter(characterID string) (character.Record, error) {
	if h.service == nil {
		return character.Record{}, ErrTurnRuntimeUnavailable
	}
	return h.service.activeCharacter(characterID)
}

func (h participationDecisionHost) ListSocialPersonNotes(ctx context.Context, characterID, conversationID string, senderIDs []string) ([]memory.SocialPersonNote, error) {
	return h.service.memory.ambient.socialContext.ListSocialPersonNotes(ctx, characterID, conversationID, senderIDs)
}

func (h participationDecisionHost) RetrieveSocialMemoryContext(ctx context.Context, characterID, conversationID, query string) (memory.SocialMemoryContext, error) {
	return h.service.memory.ambient.socialRetrieval.RetrieveSocialMemoryContext(ctx, characterID, conversationID, query)
}

func (h participationDecisionHost) ModelConnection() (config.ModelConnection, error) {
	return h.service.configSource().ModelConnection()
}

func (h participationDecisionHost) ExecuteRequest(ctx context.Context, request model.CompiledPromptRequest) ([]model.StreamEvent, error) {
	return h.service.modelPort().ExecuteRequestContext(ctx, request)
}

type participationModel struct {
	ModelPort
	draft    string
	drafts   []string
	usage    *model.Usage
	usages   []*model.Usage
	request  model.CompiledPromptRequest
	requests []model.CompiledPromptRequest
	err      error
}

func (m *participationModel) ExecuteRequestContext(ctx context.Context, request model.CompiledPromptRequest) ([]model.StreamEvent, error) {
	m.request = request
	m.requests = append(m.requests, request)
	if m.err != nil {
		return nil, m.err
	}
	call := len(m.requests) - 1
	draft := m.draft
	if call < len(m.drafts) {
		draft = m.drafts[call]
	}
	usage := m.usage
	if call < len(m.usages) {
		usage = m.usages[call]
	}
	events := []model.StreamEvent{{Type: "text_delta", Data: draft}}
	if usage != nil {
		events = append(events, model.StreamEvent{Type: "usage", Usage: usage})
	}
	return events, nil
}

func TestDecideParticipationRetriesOneInvalidDraftAndAccumulatesUsage(t *testing.T) {
	firstCached := uint64(7)
	secondCached := uint64(11)
	modelPort := &participationModel{
		drafts: []string{"", `{"action":"silent"}`},
		usages: []*model.Usage{
			{PromptTokens: 17, CompletionTokens: 1, CachedInputTokens: &firstCached},
			{PromptTokens: 19, CompletionTokens: 3, CachedInputTokens: &secondCached},
		},
	}
	memoryPort := &participationMemory{bootstrap: memory.ConversationBootstrap{Conversation: memory.ConversationRecord{ID: "c1", CharacterID: "character-1"}}}
	service := NewCompanionService()
	service.memory = participationMemoryPorts(memoryPort)
	service.model = modelPort
	service.characterLookup = participationCharacterLookup{record: character.Record{CharacterID: "character-1", Revision: 1, Name: "亚托莉", Description: "群友", TextLanguage: "zh", SpeakingLanguage: "zh"}}
	service.cfg = participationConfig{}
	if err := service.BindInteraction("c1", publicAmbientBinding()); err != nil {
		t.Fatal(err)
	}

	result, err := decideParticipation(service, t.Context(), validParticipationRequest())
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != initiative.ParticipationSilent || len(modelPort.requests) != 2 || len(result.Usage) != 2 {
		t.Fatalf("result=%#v calls=%d", result, len(modelPort.requests))
	}
	if memoryPort.activityLoads != 1 {
		t.Fatalf("conversation activity loads = %d, want 1", memoryPort.activityLoads)
	}
	if got := *result.Usage[0].Usage.CachedInputTokens.Tokens; got != firstCached {
		t.Fatalf("first cached tokens = %d", got)
	}
	if got := *result.Usage[1].Usage.CachedInputTokens.Tokens; got != secondCached {
		t.Fatalf("second cached tokens = %d", got)
	}
}

func TestDecideParticipationFailsAfterTwoInvalidRetries(t *testing.T) {
	modelPort := &participationModel{drafts: []string{"", "not json", "still not json"}}
	service := NewCompanionService()
	service.memory = participationMemoryPorts(&participationMemory{bootstrap: memory.ConversationBootstrap{Conversation: memory.ConversationRecord{ID: "c1", CharacterID: "character-1"}}})
	service.model = modelPort
	service.characterLookup = participationCharacterLookup{record: character.Record{CharacterID: "character-1", Revision: 1, Name: "亚托莉", Description: "群友", TextLanguage: "zh", SpeakingLanguage: "zh"}}
	service.cfg = participationConfig{}
	if err := service.BindInteraction("c1", publicAmbientBinding()); err != nil {
		t.Fatal(err)
	}

	_, err := decideParticipation(service, t.Context(), validParticipationRequest())
	if err == nil || !strings.Contains(err.Error(), "remained invalid after 2 retries") || len(modelPort.requests) != 3 {
		t.Fatalf("error=%v calls=%d", err, len(modelPort.requests))
	}
}

func TestDecideParticipationRequiresAmbientPublicAndPropagatesContext(t *testing.T) {
	cachedTokens := uint64(11)
	modelPort := &participationModel{draft: `{"action":"silent"}`, usage: &model.Usage{PromptTokens: 17, CompletionTokens: 3, CachedInputTokens: &cachedTokens}}
	service := NewCompanionService()
	service.memory = participationMemoryPorts(&participationMemory{bootstrap: memory.ConversationBootstrap{Conversation: memory.ConversationRecord{ID: "c1", CharacterID: "character-1"}}})
	service.model = modelPort
	service.characterLookup = participationCharacterLookup{record: character.Record{CharacterID: "character-1", Revision: 1, Name: "亚托莉", Description: "群友", TextLanguage: "zh", SpeakingLanguage: "zh"}}
	service.cfg = participationConfig{}
	request := validParticipationRequest()
	if _, err := decideParticipation(service, t.Context(), request); err == nil || !strings.Contains(err.Error(), "no interaction binding") {
		t.Fatalf("missing interaction error = %v", err)
	}
	if err := service.BindInteraction("c1", publicAmbientBinding()); err != nil {
		t.Fatal(err)
	}
	result, err := decideParticipation(service, t.Context(), request)
	if err != nil || result.Action != initiative.ParticipationSilent {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if modelPort.request.Shape.Lane != model.PromptLaneParticipate || modelPort.request.Shape.PromptCacheKey != "fairy:c1:participate" || modelPort.request.Shape.MaxOutputTokens != initiative.ParticipationMaxOutputTokens {
		t.Fatalf("request shape = %#v", modelPort.request.Shape)
	}
	if modelPort.request.CacheInput == nil || modelPort.request.CacheInput.Lane != model.PromptLaneParticipate || modelPort.request.CacheInput.CharacterRevision != 1 || modelPort.request.CacheInput.StablePromptHash == "" {
		t.Fatalf("cache input = %#v", modelPort.request.CacheInput)
	}
	if len(result.Usage) != 1 || result.Usage[0].Lane != string(model.PromptLaneParticipate) {
		t.Fatalf("usage = %#v", result.Usage)
	}
	if result.Usage[0].Usage.InputTokens == nil || *result.Usage[0].Usage.InputTokens != 17 {
		t.Fatalf("input usage = %#v", result.Usage[0].Usage.InputTokens)
	}
	if result.Usage[0].Usage.CachedInputTokens.Status != "observed" || result.Usage[0].Usage.CachedInputTokens.Tokens == nil || *result.Usage[0].Usage.CachedInputTokens.Tokens != 11 {
		t.Fatalf("cached usage = %#v", result.Usage[0].Usage.CachedInputTokens)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	modelPort.err = canceled.Err()
	if _, err := decideParticipation(service, canceled, request); !errors.Is(err, context.Canceled) {
		t.Fatalf("context error = %v", err)
	}
}

func TestDecideParticipationSuppressesOldMessageTargetForMessageEvaluation(t *testing.T) {
	oldTarget := "old"
	modelPort := &participationModel{draft: validReplyDecision(oldTarget)}
	service := NewCompanionService()
	service.memory = participationMemoryPorts(&participationMemory{bootstrap: memory.ConversationBootstrap{Conversation: memory.ConversationRecord{ID: "c1", CharacterID: "character-1"}}})
	service.model = modelPort
	service.characterLookup = participationCharacterLookup{record: character.Record{CharacterID: "character-1", Revision: 1, Name: "亚托莉", Description: "群友", TextLanguage: "zh", SpeakingLanguage: "zh"}}
	service.cfg = participationConfig{}
	if err := service.BindInteraction("c1", publicAmbientBinding()); err != nil {
		t.Fatal(err)
	}
	request := initiative.ParticipationRequest{
		ConversationID: "c1", EvaluationReason: initiative.ParticipationReasonMessage,
		Messages: []initiative.AmbientObservation{
			{MessageID: oldTarget, SenderID: "u1", SenderName: "甲", Text: "刚才的问题", TimestampUnixMS: 1, IsNew: false},
			{MessageID: "new", SenderID: "u2", SenderName: "乙", Text: "新的补充", TimestampUnixMS: 2, IsNew: true},
		},
	}
	result, err := decideParticipation(service, t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != initiative.ParticipationSilent {
		t.Fatalf("old target result = %#v", result)
	}
}

func TestDecideParticipationSkipsSocialMemoryForPersonalInteraction(t *testing.T) {
	memoryPort := &participationMemory{
		bootstrap: memory.ConversationBootstrap{Conversation: memory.ConversationRecord{ID: "c1", CharacterID: "character-1"}},
		retrieved: memory.SocialMemoryContext{Entries: []memory.SocialMemoryEntry{{
			ID: "e1", Kind: memory.SocialMemoryBehavior, Situation: "不该出现", Content: "私人路径不得注入", RecallCue: "禁止",
		}}},
	}
	service := NewCompanionService()
	service.memory = participationMemoryPorts(memoryPort)
	service.model = &participationModel{draft: `{"action":"silent"}`}
	service.characterLookup = participationCharacterLookup{record: character.Record{CharacterID: "character-1", Revision: 1, Name: "亚托莉", Description: "桌面", TextLanguage: "zh", SpeakingLanguage: "zh"}}
	service.cfg = participationConfig{}
	if err := service.BindInteraction("c1", session.Binding{
		Endpoint: session.EndpointDesktop,
		Facts: session.Facts{
			Audience: session.AudienceSingle, Initiation: session.InitiationDirect,
			Presentation: session.PresentationEmbodied,
		},
	}); err != nil {
		t.Fatal(err)
	}
	_, err := decideParticipation(service, t.Context(), validParticipationRequest())
	if err == nil || !strings.Contains(err.Error(), "public ambient") {
		t.Fatalf("personal participation error = %v", err)
	}
	if memoryPort.retrieveCalls != 0 {
		t.Fatalf("RetrieveSocialMemoryContext called %d times on personal path", memoryPort.retrieveCalls)
	}
}
