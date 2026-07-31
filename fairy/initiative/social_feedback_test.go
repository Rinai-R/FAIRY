package initiative

import (
	"errors"
	"strings"
	"sync"
	"sync/atomic"
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
		registration: FeedbackRegistration{
			CharacterID: "character-1", ConversationID: "conversation-1", TurnID: "turn-1",
			Candidates: []memory.SocialFeedbackCandidate{{
				ID: "entry-1", Kind: memory.SocialMemoryBehavior, Situation: "群友焦虑时",
				Content: "先接住情绪", RecallCue: "焦虑安慰",
			}},
			ReplyText: "先别急，看看眼前卡在哪一步。",
		},
		observations: observations,
	}
}

func feedbackInputs(host *learningTestHost) []memory.SocialFeedbackBatchInput {
	host.mu.Lock()
	defer host.mu.Unlock()
	return append([]memory.SocialFeedbackBatchInput(nil), host.feedback...)
}

func TestFeedbackCompilerIsStrict(t *testing.T) {
	candidates := []memory.SocialFeedbackCandidate{{ID: "entry-1"}, {ID: "entry-2"}}
	observations := []AmbientObservation{{MessageID: "later-1"}}
	valid := `{"evaluations":[{"entryId":"s0","adoption":"adopted","outcome":"positive","credit":"entry","evidenceMessageIds":["later-1"]},{"entryId":"s1","adoption":"not_adopted","outcome":"unknown","credit":"unknown","evidenceMessageIds":[]}]}`
	got, err := compileSocialFeedback(valid, candidates, observations)
	if err != nil || len(got) != 2 || got[0].EntryID != "entry-1" || got[1].EntryID != "entry-2" {
		t.Fatalf("compile valid feedback = %#v, %v", got, err)
	}
	for _, draft := range []string{
		`{"evaluations":[{"entryId":"s0","adoption":"adopted","outcome":"positive","credit":"entry","evidenceMessageIds":["later-1"]}]}`,
		`{"evaluations":[{"entryId":"s0","adoption":"adopted","outcome":"positive","credit":"entry","evidenceMessageIds":["later-1"]},{"entryId":"s0","adoption":"not_adopted","outcome":"unknown","credit":"unknown","evidenceMessageIds":[]}]}`,
		`{"evaluations":[{"entryId":"entry-1","adoption":"adopted","outcome":"positive","credit":"entry","evidenceMessageIds":["later-1"]},{"entryId":"s1","adoption":"not_adopted","outcome":"unknown","credit":"unknown","evidenceMessageIds":[]}]}`,
		`{"evaluations":[{"entryId":"s0","adoption":"adopted","outcome":"positive","credit":"entry","evidenceMessageIds":["forged"]},{"entryId":"s1","adoption":"not_adopted","outcome":"unknown","credit":"unknown","evidenceMessageIds":[]}]}`,
		`{"evaluations":[{"entryId":"s0","adoption":"not_adopted","outcome":"negative","credit":"entry","evidenceMessageIds":["later-1"]},{"entryId":"s1","adoption":"not_adopted","outcome":"unknown","credit":"unknown","evidenceMessageIds":[]}]}`,
		`{"evaluations":[{"entryId":"s0","adoption":"adopted","outcome":"positive","credit":"entry","evidenceMessageIds":["later-1"],"score":1},{"entryId":"s1","adoption":"not_adopted","outcome":"unknown","credit":"unknown","evidenceMessageIds":[]}]}`,
		valid + ` trailing`,
	} {
		if _, err := compileSocialFeedback(draft, candidates, observations); err == nil {
			t.Fatalf("accepted invalid feedback: %q", draft)
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
	if len(feedback) != 1 || len(feedback[0].Evaluations) != 1 || feedback[0].Evaluations[0].Adoption != memory.SocialFeedbackUncertain || feedback[0].Evaluations[0].Outcome != memory.SocialFeedbackUnknown || feedback[0].ObservedMessageCount != 0 {
		t.Fatalf("zero observation feedback = %#v", feedback)
	}
	host.mu.Lock()
	request := host.request
	host.mu.Unlock()
	if request.Shape.Lane != "" {
		t.Fatalf("zero observation called model lane %q", request.Shape.Lane)
	}

	host = newLearningTestHost()
	host.draft = `{"evaluations":[{"entryId":"s0","adoption":"adopted","outcome":"positive","credit":"entry","evidenceMessageIds":["later-message"]}]}`
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
	if len(request.Input) != 1 || !strings.Contains(request.Input[0].Content, `"entryId":"s0"`) || strings.Contains(request.Input[0].Content, "entry-1") {
		t.Fatalf("feedback request exposed the wrong candidate identity: %#v", request.Input)
	}
	feedback = feedbackInputs(host)
	if len(feedback) != 1 || len(feedback[0].Evaluations) != 1 || feedback[0].Evaluations[0].Outcome != memory.SocialFeedbackPositive || feedback[0].ObservedMessageCount != 2 || feedback[0].EvaluatorRevision != SocialFeedbackEvaluatorRevision {
		t.Fatalf("model feedback = %#v", feedback)
	}
}

func TestFeedbackStoreFailureIsReturnedAfterStrictEvaluation(t *testing.T) {
	host := newLearningTestHost()
	host.draft = `{"evaluations":[{"entryId":"s0","adoption":"adopted","outcome":"partial","credit":"entry","evidenceMessageIds":["later-message"]}]}`
	host.feedbackErr = errors.New("store unavailable")
	engine := &FeedbackEngine{host: host}
	if err := engine.process(t.Context(), feedbackTestSnapshot(1)); !errors.Is(err, host.feedbackErr) {
		t.Fatalf("process error = %v, want store error", err)
	}
	feedback := feedbackInputs(host)
	if len(feedback) != 1 || feedback[0].Evaluations[0].Outcome != memory.SocialFeedbackPartial {
		t.Fatalf("strict feedback did not reach store: %#v", feedback)
	}
}

func TestFeedbackInvalidModelResultDoesNotPersist(t *testing.T) {
	host := newLearningTestHost()
	host.draft = `{"evaluations":[{"entryId":"s0","adoption":"adopted","outcome":"positive","credit":"entry","evidenceMessageIds":["later-message"],"reason":"guess"}]}`
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
	host.draft = `{"evaluations":[{"entryId":"s0","adoption":"adopted","outcome":"negative","credit":"entry","evidenceMessageIds":["later-x"]}]}`
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
			if len(feedback[0].Evaluations) != 1 || feedback[0].Evaluations[0].Outcome != memory.SocialFeedbackNegative || feedback[0].ObservedMessageCount != socialFeedbackObservationLimit {
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

func TestFeedbackEngineBoundsGlobalPendingWithoutCreatingGroup(t *testing.T) {
	engine := newFeedbackEngine(newLearningTestHost(), 1, 2, time.Hour)
	defer engine.Close()
	for index, conversationID := range []string{"conversation-1", "conversation-2"} {
		registration := feedbackTestSnapshot(0).registration
		registration.ConversationID = conversationID
		registration.TurnID = "turn-" + conversationID
		if !engine.Register(registration) {
			t.Fatalf("Register(%d) = false", index)
		}
	}
	overflow := feedbackTestSnapshot(0).registration
	overflow.ConversationID = "conversation-3"
	overflow.TurnID = "turn-3"
	if engine.Register(overflow) {
		t.Fatal("global pending overflow accepted")
	}
	engine.mu.Lock()
	pendingCount := engine.pendingCount
	_, createdOverflowGroup := engine.pending[overflow.ConversationID]
	engine.mu.Unlock()
	if pendingCount != 2 {
		t.Fatalf("pending count = %d, want 2", pendingCount)
	}
	if createdOverflowGroup {
		t.Fatal("overflow registration created an empty group")
	}
	if stats := engine.Stats(); stats.Dropped != 1 {
		t.Fatalf("stats = %#v, want dropped=1", stats)
	}
}

func TestFeedbackEngineRejectsDuplicateTurnWithoutReplacingOwner(t *testing.T) {
	engine := newFeedbackEngine(newLearningTestHost(), 1, 2, time.Hour)
	defer engine.Close()
	original := feedbackTestSnapshot(0).registration
	if !engine.Register(original) {
		t.Fatal("initial Register = false")
	}
	duplicate := original
	duplicate.ReplyText = "replacement"
	duplicate.Candidates = []memory.SocialFeedbackCandidate{{ID: "replacement-entry", Content: "replacement"}}
	if engine.Register(duplicate) {
		t.Fatal("duplicate Register = true")
	}
	engine.mu.Lock()
	pending := engine.pending[original.ConversationID][original.TurnID]
	pendingCount := engine.pendingCount
	engine.mu.Unlock()
	if pendingCount != 1 {
		t.Fatalf("pending count = %d, want 1", pendingCount)
	}
	if pending == nil || pending.registration.ReplyText != original.ReplyText || len(pending.registration.Candidates) != 1 || pending.registration.Candidates[0].ID != original.Candidates[0].ID {
		t.Fatalf("duplicate replaced owner: %#v", pending)
	}
	if stats := engine.Stats(); stats.Dropped != 1 {
		t.Fatalf("stats = %#v, want dropped=1", stats)
	}
}

func TestFeedbackEngineCloseWaitsForStartedTimerCallback(t *testing.T) {
	engine := newFeedbackEngine(newLearningTestHost(), 1, 1, time.Millisecond)
	callbackStarted := make(chan struct{})
	callbackRelease := make(chan struct{})
	var callbackCalls atomic.Int64
	engine.timerCallbackHook = func() {
		if callbackCalls.Add(1) == 1 {
			close(callbackStarted)
		}
		<-callbackRelease
	}
	if !engine.Register(feedbackTestSnapshot(0).registration) {
		t.Fatal("Register = false")
	}
	select {
	case <-callbackStarted:
	case <-time.After(time.Second):
		t.Fatal("timer callback did not start")
	}
	closed := make(chan struct{})
	go func() {
		engine.Close()
		close(closed)
	}()
	select {
	case <-closed:
		t.Fatal("Close returned before timer callback exited")
	default:
	}
	close(callbackRelease)
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close did not return after timer callback exited")
	}
	engine.mu.Lock()
	pendingCount := engine.pendingCount
	pendingGroups := len(engine.pending)
	engine.mu.Unlock()
	if pendingCount != 0 || pendingGroups != 0 {
		t.Fatalf("pending after Close = %d/%d", pendingCount, pendingGroups)
	}
	if engine.Register(feedbackTestSnapshot(0).registration) {
		t.Fatal("post-close Register = true")
	}
}

func TestFeedbackEngineConcurrentRegisterObserveAndClose(t *testing.T) {
	engine := newFeedbackEngine(newLearningTestHost(), 2, 8, time.Hour)
	for index := 0; index < 4; index++ {
		registration := feedbackTestSnapshot(0).registration
		registration.ConversationID = "conversation-" + strings.Repeat("x", index+1)
		registration.TurnID = "turn-" + strings.Repeat("x", index+1)
		if !engine.Register(registration) {
			t.Fatalf("initial Register(%d) = false", index)
		}
	}
	var concurrent sync.WaitGroup
	concurrent.Add(4)
	go func() {
		defer concurrent.Done()
		for index := 0; index < 64; index++ {
			registration := feedbackTestSnapshot(0).registration
			registration.ConversationID = "new-" + strings.Repeat("c", index+1)
			registration.TurnID = "new-" + strings.Repeat("t", index+1)
			engine.Register(registration)
		}
	}()
	go func() {
		defer concurrent.Done()
		for index := 0; index < 64; index++ {
			engine.Observe("conversation-x", AmbientObservation{MessageID: "observation"})
		}
	}()
	go func() {
		defer concurrent.Done()
		for index := 0; index < 64; index++ {
			engine.Observe("conversation-xx", AmbientObservation{MessageID: "second-observation"})
		}
	}()
	go func() {
		defer concurrent.Done()
		engine.Close()
	}()
	concurrent.Wait()
	engine.Close()
	engine.mu.Lock()
	pendingCount := engine.pendingCount
	pendingGroups := len(engine.pending)
	engine.mu.Unlock()
	if pendingCount != 0 || pendingGroups != 0 {
		t.Fatalf("pending after concurrent Close = %d/%d", pendingCount, pendingGroups)
	}
}
