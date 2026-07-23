package companion

import (
	"errors"
	"testing"
	"time"

	"fairy/memory"
	"fairy/model"
)

func socialFeedbackTestSnapshot(observationCount int) socialFeedbackSnapshot {
	observations := make([]AmbientObservation, 0, observationCount)
	for index := 0; index < observationCount; index++ {
		observations = append(observations, AmbientObservation{
			MessageID: "later-message", SenderID: "member-1", SenderName: "群友", Text: "接着聊这个话题", TimestampUnixMS: int64(index + 1),
		})
	}
	return socialFeedbackSnapshot{
		registration: socialFeedbackRegistration{
			CharacterID: "character-1", ConversationID: "conversation-1", TurnID: "turn-1",
			EntryIDs: []string{"entry-1"}, ReplyText: "先别急，看看眼前卡在哪一步。",
		},
		observations: observations,
	}
}

func TestCompileSocialFeedbackIsStrict(t *testing.T) {
	for _, draft := range []string{
		`{"outcome":"good"}`,
		`{"outcome":"positive","reason":"continued"}`,
		`{"outcome":null}`,
		`{"outcome":"negative"} trailing`,
	} {
		if _, err := compileSocialFeedback(draft); err == nil {
			t.Fatalf("compileSocialFeedback(%q) error = nil", draft)
		}
	}
	for _, outcome := range []string{memory.SocialFeedbackPositive, memory.SocialFeedbackNegative, memory.SocialFeedbackUnknown} {
		got, err := compileSocialFeedback(`{"outcome":"` + outcome + `"}`)
		if err != nil || got != outcome {
			t.Fatalf("compileSocialFeedback(%q) = (%q, %v)", outcome, got, err)
		}
	}
}

func TestSocialFeedbackWithoutObservationsPersistsUnknownWithoutModel(t *testing.T) {
	memoryPort := &socialLearningMemory{}
	modelPort := &socialLearningModel{err: errors.New("model must not be called")}
	service := newSocialLearningTestService(memoryPort, modelPort)
	engine := &SocialFeedbackEngine{host: service}
	if err := engine.process(t.Context(), socialFeedbackTestSnapshot(0)); err != nil {
		t.Fatal(err)
	}
	feedback := memoryPort.feedbackInputs()
	if len(feedback) != 1 || feedback[0].Outcome != memory.SocialFeedbackUnknown || feedback[0].ObservedMessageCount != 0 {
		t.Fatalf("feedback = %#v", feedback)
	}
	if modelPort.request.Shape.Lane != "" {
		t.Fatalf("model lane = %q", modelPort.request.Shape.Lane)
	}
}

func TestSocialFeedbackUsesSemanticLaneAndPersistsAggregateOnly(t *testing.T) {
	memoryPort := &socialLearningMemory{}
	modelPort := &socialLearningModel{draft: `{"outcome":"positive"}`}
	service := newSocialLearningTestService(memoryPort, modelPort)
	engine := &SocialFeedbackEngine{host: service}
	if err := engine.process(t.Context(), socialFeedbackTestSnapshot(2)); err != nil {
		t.Fatal(err)
	}
	if modelPort.request.Shape.Lane != model.PromptLaneSocialFeedback {
		t.Fatalf("lane = %q", modelPort.request.Shape.Lane)
	}
	if modelPort.request.Shape.PromptCacheKey != model.LaneCacheKey("conversation-1", model.PromptLaneSocialFeedback) {
		t.Fatalf("cache key = %q", modelPort.request.Shape.PromptCacheKey)
	}
	feedback := memoryPort.feedbackInputs()
	if len(feedback) != 1 || feedback[0].Outcome != memory.SocialFeedbackPositive || feedback[0].ObservedMessageCount != 2 {
		t.Fatalf("feedback = %#v", feedback)
	}
}

func TestSocialFeedbackInvalidModelResultDoesNotPersist(t *testing.T) {
	memoryPort := &socialLearningMemory{}
	service := newSocialLearningTestService(memoryPort, &socialLearningModel{draft: `{"outcome":"positive","reason":"guess"}`})
	engine := &SocialFeedbackEngine{host: service}
	if err := engine.process(t.Context(), socialFeedbackTestSnapshot(1)); err == nil {
		t.Fatal("process() error = nil")
	}
	if feedback := memoryPort.feedbackInputs(); len(feedback) != 0 {
		t.Fatalf("feedback = %#v", feedback)
	}
}

func TestSocialFeedbackObservationWindowFinalizesAsynchronously(t *testing.T) {
	memoryPort := &socialLearningMemory{}
	service := newSocialLearningTestService(memoryPort, &socialLearningModel{draft: `{"outcome":"negative"}`})
	engine := newSocialFeedbackEngine(service, 1)
	defer engine.Close()
	if !engine.Register(socialFeedbackTestSnapshot(0).registration) {
		t.Fatal("Register() = false")
	}
	for index := 0; index < socialFeedbackObservationLimit; index++ {
		engine.Observe("conversation-1", AmbientObservation{MessageID: "later", SenderID: "member", SenderName: "群友", Text: "这条回复需要修正", TimestampUnixMS: int64(index + 1)})
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		feedback := memoryPort.feedbackInputs()
		if len(feedback) == 1 {
			if feedback[0].Outcome != memory.SocialFeedbackNegative || feedback[0].ObservedMessageCount != socialFeedbackObservationLimit {
				t.Fatalf("feedback = %#v", feedback)
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timed out waiting for social feedback")
}

func TestSocialFeedbackTimeoutFinalizesUnknown(t *testing.T) {
	memoryPort := &socialLearningMemory{}
	service := newSocialLearningTestService(memoryPort, &socialLearningModel{err: errors.New("model must not be called")})
	engine := newSocialFeedbackEngine(service, 1)
	defer engine.Close()
	registration := socialFeedbackTestSnapshot(0).registration
	if !engine.Register(registration) {
		t.Fatal("Register() = false")
	}
	engine.finalize(registration.ConversationID, registration.TurnID)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		feedback := memoryPort.feedbackInputs()
		if len(feedback) == 1 {
			if feedback[0].Outcome != memory.SocialFeedbackUnknown || feedback[0].ObservedMessageCount != 0 {
				t.Fatalf("feedback = %#v", feedback)
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timed out waiting for unknown feedback")
}

func TestSocialFeedbackCloseCancelsBlockedModel(t *testing.T) {
	started := make(chan struct{})
	service := newSocialLearningTestService(&socialLearningMemory{}, &socialLearningModel{block: true, started: started})
	engine := newSocialFeedbackEngine(service, 1)
	engine.enqueue(socialFeedbackTestSnapshot(1))
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("worker did not start feedback model request")
	}
	done := make(chan struct{})
	go func() {
		engine.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Close() did not cancel blocked feedback request")
	}
}

func TestSocialFeedbackRegistrationIsBounded(t *testing.T) {
	engine := newSocialFeedbackEngine(nil, 1)
	defer engine.Close()
	for index := 0; index < socialFeedbackMaxPendingPerGroup; index++ {
		registration := socialFeedbackTestSnapshot(0).registration
		registration.TurnID = registration.TurnID + string(rune('a'+index))
		if !engine.Register(registration) {
			t.Fatalf("Register(%d) = false", index)
		}
	}
	extra := socialFeedbackTestSnapshot(0).registration
	extra.TurnID = "turn-overflow"
	if engine.Register(extra) {
		t.Fatal("overflow Register() = true")
	}
	if engine.Stats().Dropped != 1 {
		t.Fatalf("dropped = %d", engine.Stats().Dropped)
	}
}

func TestSocialFeedbackRegisterAllowsEmptyEntryIDs(t *testing.T) {
	engine := newSocialFeedbackEngine(nil, 1)
	defer engine.Close()
	registration := socialFeedbackRegistration{
		CharacterID: "character-1", ConversationID: "conversation-1", TurnID: "turn-empty-entries",
		ReplyText: "先别急，看看眼前卡在哪一步。",
	}
	if !engine.Register(registration) {
		t.Fatal("Register() with empty EntryIDs = false")
	}
	if engine.Stats().Registered != 1 {
		t.Fatalf("Stats() = %#v", engine.Stats())
	}
}

func TestSocialFeedbackWithoutObservationsPersistsUnknownForEmptyEntries(t *testing.T) {
	memoryPort := &socialLearningMemory{}
	service := newSocialLearningTestService(memoryPort, &socialLearningModel{err: errors.New("model must not be called")})
	engine := &SocialFeedbackEngine{host: service}
	snapshot := socialFeedbackSnapshot{
		registration: socialFeedbackRegistration{
			CharacterID: "character-1", ConversationID: "conversation-1", TurnID: "turn-1",
			ReplyText: "先别急，看看眼前卡在哪一步。",
		},
	}
	if err := engine.process(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
	feedback := memoryPort.feedbackInputs()
	if len(feedback) != 1 || feedback[0].Outcome != memory.SocialFeedbackUnknown || len(feedback[0].EntryIDs) != 0 {
		t.Fatalf("feedback = %#v", feedback)
	}
}
