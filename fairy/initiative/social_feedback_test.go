package initiative

import (
	"strings"
	"testing"
	"time"

	"fairy/memory"
	"fairy/model"
)

func feedbackTestSnapshot(observationCount int) feedbackSnapshot {
	observations := make([]AmbientObservation, 0, observationCount)
	for index := 0; index < observationCount; index++ {
		observations = append(observations, AmbientObservation{MessageID: "later-message", SenderID: "member-1", SenderName: "群友", Text: "接着聊这个话题", TimestampUnixMS: int64(index + 1)})
	}
	return feedbackSnapshot{
		registration: FeedbackRegistration{CharacterID: "character-1", ConversationID: "conversation-1", TurnID: "turn-1", EntryIDs: []string{"entry-1"}, ReplyText: "先别急，看看眼前卡在哪一步。"},
		observations: observations,
	}
}

func feedbackInputs(host *learningTestHost) []memory.SocialReplyFeedbackInput {
	host.mu.Lock()
	defer host.mu.Unlock()
	return append([]memory.SocialReplyFeedbackInput(nil), host.feedback...)
}

func TestFeedbackCompilerIsStrict(t *testing.T) {
	for _, draft := range []string{`{"outcome":"good"}`, `{"outcome":"positive","reason":"continued"}`, `{"outcome":null}`, `{"outcome":"negative"} trailing`} {
		if _, err := compileSocialFeedback(draft); err == nil {
			t.Fatalf("accepted invalid feedback: %q", draft)
		}
	}
	for _, outcome := range []string{memory.SocialFeedbackPositive, memory.SocialFeedbackNegative, memory.SocialFeedbackUnknown} {
		if got, err := compileSocialFeedback(`{"outcome":"` + outcome + `"}`); err != nil || got != outcome {
			t.Fatalf("compile %q = %q, %v", outcome, got, err)
		}
	}
}

func TestFeedbackZeroObservationAndModelPathsPreserveContract(t *testing.T) {
	host := newLearningTestHost()
	engine := &FeedbackEngine{host: host}
	if err := engine.process(t.Context(), feedbackTestSnapshot(0)); err != nil {
		t.Fatal(err)
	}
	feedback := feedbackInputs(host)
	if len(feedback) != 1 || feedback[0].Outcome != memory.SocialFeedbackUnknown || feedback[0].ObservedMessageCount != 0 {
		t.Fatalf("zero observation feedback = %#v", feedback)
	}
	host.mu.Lock()
	request := host.request
	host.mu.Unlock()
	if request.Shape.Lane != "" {
		t.Fatalf("zero observation called model lane %q", request.Shape.Lane)
	}

	host = newLearningTestHost()
	host.draft = `{"outcome":"positive"}`
	engine = &FeedbackEngine{host: host}
	if err := engine.process(t.Context(), feedbackTestSnapshot(2)); err != nil {
		t.Fatal(err)
	}
	host.mu.Lock()
	request = host.request
	host.mu.Unlock()
	if request.Shape.Lane != model.PromptLaneSocialFeedback || request.Shape.PromptCacheKey != model.LaneCacheKey("conversation-1", model.PromptLaneSocialFeedback) || request.CacheInput == nil || request.CacheInput.StablePromptHash == "" {
		t.Fatalf("feedback request = %#v", request)
	}
	feedback = feedbackInputs(host)
	if len(feedback) != 1 || feedback[0].Outcome != memory.SocialFeedbackPositive || feedback[0].ObservedMessageCount != 2 {
		t.Fatalf("model feedback = %#v", feedback)
	}
}

func TestFeedbackInvalidModelResultDoesNotPersist(t *testing.T) {
	host := newLearningTestHost()
	host.draft = `{"outcome":"positive","reason":"guess"}`
	engine := &FeedbackEngine{host: host}
	if err := engine.process(t.Context(), feedbackTestSnapshot(1)); err == nil {
		t.Fatal("invalid feedback result was accepted")
	}
	if feedback := feedbackInputs(host); len(feedback) != 0 {
		t.Fatalf("invalid result persisted %#v", feedback)
	}
}

func TestFeedbackWindowBoundCloseAndEmptyEntryIDs(t *testing.T) {
	host := newLearningTestHost()
	host.draft = `{"outcome":"negative"}`
	engine := NewFeedbackEngine(host, 1)
	defer engine.Close()
	registration := feedbackTestSnapshot(0).registration
	if !engine.Register(registration) {
		t.Fatal("Register = false")
	}
	for index := 0; index < socialFeedbackObservationLimit; index++ {
		engine.Observe(registration.ConversationID, AmbientObservation{MessageID: "later-" + strings.Repeat("x", index+1), SenderID: "member", SenderName: "群友", Text: "这条回复需要修正", TimestampUnixMS: int64(index + 1)})
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		feedback := feedbackInputs(host)
		if len(feedback) == 1 {
			if feedback[0].Outcome != memory.SocialFeedbackNegative || feedback[0].ObservedMessageCount != socialFeedbackObservationLimit {
				t.Fatalf("feedback = %#v", feedback)
			}
			break
		}
		time.Sleep(time.Millisecond)
	}
	if len(feedbackInputs(host)) != 1 {
		t.Fatal("feedback window did not finalize")
	}
	for index := 0; index < socialFeedbackMaxPendingPerGroup; index++ {
		item := registration
		item.TurnID = "pending-" + strings.Repeat("x", index+1)
		if !engine.Register(item) {
			t.Fatalf("Register(%d) = false", index)
		}
	}
	if engine.Register(FeedbackRegistration{CharacterID: "character-1", ConversationID: "conversation-1", TurnID: "overflow", ReplyText: "reply"}) {
		t.Fatal("pending overflow accepted")
	}
	engine.Close()
	if engine.Register(FeedbackRegistration{CharacterID: "character-1", ConversationID: "conversation-1", TurnID: "after-close", ReplyText: "reply"}) {
		t.Fatal("post-close registration accepted")
	}
}
