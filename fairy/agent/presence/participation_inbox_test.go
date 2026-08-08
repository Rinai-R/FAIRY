package presence

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestInboxStartsFirstMessageImmediately(t *testing.T) {
	inbox := NewInbox(t.Context(), fakeInboxHost{})
	defer inbox.Close()
	started := make(chan ambientBatch, 1)
	inbox.decideHook = func(_ context.Context, batch ambientBatch) (ParticipationResult, error) {
		started <- batch
		return ParticipationResult{Action: ParticipationSilent}, nil
	}
	if err := inbox.Observe("c1", testObservation(1)); err != nil {
		t.Fatal(err)
	}
	batch := receiveBatch(t, started)
	if batch.evaluationReason != ParticipationReasonMessage || len(batch.messages) != 1 || !batch.messages[0].IsNew {
		t.Fatalf("batch = %#v", batch)
	}
}

func TestInboxSerializesAndKeepsBoundedWindows(t *testing.T) {
	inbox := NewInbox(t.Context(), fakeInboxHost{})
	defer inbox.Close()
	started := make(chan ambientBatch, 2)
	release := make(chan ParticipationResult, 2)
	var active atomic.Int32
	var maximum atomic.Int32
	inbox.decideHook = func(_ context.Context, batch ambientBatch) (ParticipationResult, error) {
		current := active.Add(1)
		for current > maximum.Load() && !maximum.CompareAndSwap(maximum.Load(), current) {
		}
		started <- batch
		decision := <-release
		active.Add(-1)
		return decision, nil
	}
	if err := inbox.Observe("c1", testObservation(0)); err != nil {
		t.Fatal(err)
	}
	first := receiveBatch(t, started)
	for index := 1; index <= MaxAmbientCacheObservations+5; index++ {
		if err := inbox.Observe("c1", testObservation(index)); err != nil {
			t.Fatal(err)
		}
	}
	target := first.messages[0].MessageID
	release <- ParticipationResult{Action: ParticipationReply, TargetMessageID: &target}
	latest := receiveBatch(t, started)
	if len(latest.messages) != MaxAmbientObservations || latest.messages[0].MessageID != "m26" || latest.messages[len(latest.messages)-1].MessageID != "m45" {
		t.Fatalf("messages = %#v", latest.messages)
	}
	if len(latest.cacheMessages) != MaxAmbientObservations+5 || latest.cacheMessages[0].MessageID != "m21" {
		t.Fatalf("cache = %#v", latest.cacheMessages)
	}
	release <- ParticipationResult{Action: ParticipationSilent}
	waitUntil(t, func() bool { return active.Load() == 0 })
	if maximum.Load() > 1 {
		t.Fatalf("concurrent decisions = %d", maximum.Load())
	}
}

func TestInboxCancelsStaleDecision(t *testing.T) {
	inbox := NewInbox(t.Context(), fakeInboxHost{})
	defer inbox.Close()
	firstStarted := make(chan struct{})
	firstCanceled := make(chan struct{})
	latest := make(chan ambientBatch, 1)
	var calls atomic.Int32
	inbox.decideHook = func(ctx context.Context, batch ambientBatch) (ParticipationResult, error) {
		if calls.Add(1) == 1 {
			close(firstStarted)
			<-ctx.Done()
			close(firstCanceled)
			return ParticipationResult{}, ctx.Err()
		}
		latest <- batch
		return ParticipationResult{Action: ParticipationSilent}, nil
	}
	if err := inbox.Observe("c1", testObservation(1)); err != nil {
		t.Fatal(err)
	}
	<-firstStarted
	if err := inbox.Observe("c1", testObservation(2)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-firstCanceled:
	case <-time.After(time.Second):
		t.Fatal("stale decision was not canceled")
	}
	batch := receiveBatch(t, latest)
	if batch.generation != 2 || len(batch.messages) != 2 || batch.messages[1].MessageID != "m2" {
		t.Fatalf("latest = %#v", batch)
	}
}

func TestInboxKeepsRecentReplyPerSender(t *testing.T) {
	inbox := NewInbox(t.Context(), fakeInboxHost{})
	defer inbox.Close()
	inbox.decideHook = func(_ context.Context, batch ambientBatch) (ParticipationResult, error) {
		target := batch.messages[len(batch.messages)-1].MessageID
		return ParticipationResult{Action: ParticipationReply, TargetMessageID: &target, Intent: &ReplyIntent{ReplyAct: "接话", Tone: "自然", RelationshipSignal: "群友", ReplyMode: "brief", Focus: "新消息"}}, nil
	}
	requests := make(chan TurnRequest, 3)
	inbox.submitHook = func(request TurnRequest) (TurnOutcome, error) {
		requests <- request
		return TurnOutcome{ResponseText: "上一条已经说过的内容"}, nil
	}
	if err := inbox.Observe("c1", testObservation(1)); err != nil {
		t.Fatal(err)
	}
	if first := receiveTurnRequest(t, requests); first.RecentTargetReply != "" {
		t.Fatalf("first recent reply = %q", first.RecentTargetReply)
	}
	waitUntil(t, func() bool {
		inbox.mu.Lock()
		defer inbox.mu.Unlock()
		return inbox.states["c1"].recentRepliesBySender["u1"] != ""
	})
	if err := inbox.Observe("c1", testObservation(2)); err != nil {
		t.Fatal(err)
	}
	if second := receiveTurnRequest(t, requests); second.RecentTargetReply != "上一条已经说过的内容" {
		t.Fatalf("same sender recent reply = %q", second.RecentTargetReply)
	}
	other := testObservation(3)
	other.SenderID = "u2"
	if err := inbox.Observe("c1", other); err != nil {
		t.Fatal(err)
	}
	if third := receiveTurnRequest(t, requests); third.RecentTargetReply != "" {
		t.Fatalf("different sender recent reply = %q", third.RecentTargetReply)
	}
}

func TestInboxEvictsLeastRecentlyObservedQuiescentConversation(t *testing.T) {
	inbox := NewInbox(t.Context(), fakeInboxHost{})
	defer inbox.Close()
	inbox.stateCapacity = 2
	inbox.decideHook = func(context.Context, ambientBatch) (ParticipationResult, error) {
		return ParticipationResult{Action: ParticipationSilent}, nil
	}
	observeAndWaitForIdle(t, inbox, "conversation-1", testObservation(1))
	observeAndWaitForIdle(t, inbox, "conversation-2", testObservation(2))
	observeAndWaitForIdle(t, inbox, "conversation-1", testObservation(3))
	observeAndWaitForIdle(t, inbox, "conversation-3", testObservation(4))

	inbox.mu.Lock()
	_, firstFound := inbox.states["conversation-1"]
	_, secondFound := inbox.states["conversation-2"]
	_, thirdFound := inbox.states["conversation-3"]
	stateCount := len(inbox.states)
	inbox.mu.Unlock()
	if !firstFound || secondFound || !thirdFound || stateCount != 2 {
		t.Fatalf("states after LRU eviction: first=%t second=%t third=%t count=%d", firstFound, secondFound, thirdFound, stateCount)
	}

	observeAndWaitForIdle(t, inbox, "conversation-2", testObservation(5))
	inbox.mu.Lock()
	recreated := inbox.states["conversation-2"]
	inbox.mu.Unlock()
	if recreated == nil || recreated.generation != 1 {
		t.Fatalf("recreated state = %#v, want fresh generation 1", recreated)
	}
}

func TestInboxRejectsNewConversationWhenAllStateIsActiveBeforeHostSideEffects(t *testing.T) {
	host := &countingInboxHost{}
	inbox := NewInbox(t.Context(), host)
	defer inbox.Close()
	inbox.stateCapacity = 2
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	inbox.decideHook = func(context.Context, ambientBatch) (ParticipationResult, error) {
		started <- struct{}{}
		<-release
		return ParticipationResult{Action: ParticipationSilent}, nil
	}
	if err := inbox.Observe("conversation-1", testObservation(1)); err != nil {
		t.Fatal(err)
	}
	<-started
	if err := inbox.Observe("conversation-2", testObservation(2)); err != nil {
		t.Fatal(err)
	}
	<-started

	if err := inbox.Observe("conversation-3", testObservation(3)); err == nil || !strings.Contains(err.Error(), "capacity") {
		t.Fatalf("capacity error = %v", err)
	}
	if got := host.begins.Load(); got != 2 {
		t.Fatalf("BeginMessageTrace calls = %d, want 2", got)
	}
	if got := host.feedback.Load(); got != 2 {
		t.Fatalf("ObserveSocialFeedback calls = %d, want 2", got)
	}
	if got := host.cancels.Load(); got != 2 {
		t.Fatalf("CancelTurnBeforeDelivery calls = %d, want 2", got)
	}
	inbox.mu.Lock()
	_, rejectedStateExists := inbox.states["conversation-3"]
	stateCount := len(inbox.states)
	inbox.mu.Unlock()
	if rejectedStateExists || stateCount != 2 {
		t.Fatalf("rejected state exists=%t count=%d", rejectedStateExists, stateCount)
	}
	close(release)
}

func TestInboxDoesNotEvictWaitingConversationAtCapacity(t *testing.T) {
	inbox := NewInbox(t.Context(), fakeInboxHost{})
	defer inbox.Close()
	inbox.stateCapacity = 1
	waitSeconds := 300
	inbox.decideHook = func(context.Context, ambientBatch) (ParticipationResult, error) {
		return ParticipationResult{Action: ParticipationWait, WaitSeconds: &waitSeconds}, nil
	}
	if err := inbox.Observe("conversation-waiting", testObservation(1)); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, func() bool {
		inbox.mu.Lock()
		defer inbox.mu.Unlock()
		state := inbox.states["conversation-waiting"]
		return state != nil && state.timer != nil && !state.running
	})
	if err := inbox.Observe("conversation-new", testObservation(2)); err == nil || !strings.Contains(err.Error(), "capacity") {
		t.Fatalf("capacity error = %v", err)
	}
	inbox.mu.Lock()
	waiting := inbox.states["conversation-waiting"]
	_, rejectedStateExists := inbox.states["conversation-new"]
	inbox.mu.Unlock()
	if waiting == nil || waiting.timer == nil || rejectedStateExists {
		t.Fatalf("waiting=%#v rejectedStateExists=%t", waiting, rejectedStateExists)
	}
}

func TestAmbientStateBoundsRecentRepliesBySenderAndRefreshesHotSender(t *testing.T) {
	state := &ambientState{}
	for index := 1; index <= maxAmbientRecentReplies; index++ {
		state.rememberRecentReply(fmt.Sprintf("sender-%02d", index), fmt.Sprintf("reply-%02d", index))
	}
	state.rememberRecentReply("sender-01", "reply-hot")
	state.rememberRecentReply("sender-new", "reply-new")

	if got := len(state.recentRepliesBySender); got != maxAmbientRecentReplies {
		t.Fatalf("recent reply count = %d, want %d", got, maxAmbientRecentReplies)
	}
	if got := state.recentRepliesBySender["sender-01"]; got != "reply-hot" {
		t.Fatalf("hot sender reply = %q", got)
	}
	if _, found := state.recentRepliesBySender["sender-02"]; found {
		t.Fatal("least recently used sender reply was retained")
	}
	if got := state.recentRepliesBySender["sender-new"]; got != "reply-new" {
		t.Fatalf("new sender reply = %q", got)
	}
}

func TestInboxCloseClearsConversationStates(t *testing.T) {
	inbox := NewInbox(t.Context(), fakeInboxHost{})
	inbox.decideHook = func(context.Context, ambientBatch) (ParticipationResult, error) {
		return ParticipationResult{Action: ParticipationSilent}, nil
	}
	observeAndWaitForIdle(t, inbox, "conversation-1", testObservation(1))
	inbox.Close()
	inbox.mu.Lock()
	stateCount := len(inbox.states)
	inbox.mu.Unlock()
	if stateCount != 0 {
		t.Fatalf("states after Close = %d, want 0", stateCount)
	}
}

func TestInboxEnqueuesLearningEveryObservationThresholdOnce(t *testing.T) {
	host := &learningInboxHost{}
	inbox := NewInbox(t.Context(), host)
	defer inbox.Close()
	inbox.decideHook = func(context.Context, ambientBatch) (ParticipationResult, error) {
		return ParticipationResult{Action: ParticipationSilent}, nil
	}
	for index := 1; index <= 40; index++ {
		if err := inbox.Observe("conversation-1", testObservation(index)); err != nil {
			t.Fatal(err)
		}
		waitUntil(t, func() bool {
			inbox.mu.Lock()
			defer inbox.mu.Unlock()
			return !inbox.states["conversation-1"].running
		})
	}
	if host.enqueued.Load() != 2 {
		t.Fatalf("learning enqueues = %d", host.enqueued.Load())
	}
}

type learningInboxHost struct {
	fakeInboxHost
	enqueued atomic.Int32
}

func (h *learningInboxHost) EnqueueSocialLearning(string, []AmbientObservation) {
	h.enqueued.Add(1)
}

type fakeInboxHost struct{}

func (fakeInboxHost) BeginMessageTrace(_, _, traceID string) string      { return traceID }
func (fakeInboxHost) ObserveSocialFeedback(string, AmbientObservation)   {}
func (fakeInboxHost) EnqueueSocialLearning(string, []AmbientObservation) {}
func (fakeInboxHost) CancelTurnBeforeDelivery(string)                    {}
func (fakeInboxHost) DecideParticipation(context.Context, ParticipationRequest) (ParticipationResult, error) {
	return ParticipationResult{Action: ParticipationSilent}, nil
}
func (fakeInboxHost) SubmitTurn(TurnRequest) (TurnOutcome, error)  { return TurnOutcome{}, nil }
func (fakeInboxHost) EndMessageTrace(string, string)               {}
func (fakeInboxHost) EmitParticipation(Event)                      {}
func (fakeInboxHost) RecordParticipation([]string, string, string) {}
func (fakeInboxHost) WarnAmbient(string, string, uint64, error)    {}

type countingInboxHost struct {
	fakeInboxHost
	begins   atomic.Int32
	feedback atomic.Int32
	cancels  atomic.Int32
}

func (h *countingInboxHost) BeginMessageTrace(_, _, traceID string) string {
	h.begins.Add(1)
	return traceID
}

func (h *countingInboxHost) ObserveSocialFeedback(string, AmbientObservation) {
	h.feedback.Add(1)
}

func (h *countingInboxHost) CancelTurnBeforeDelivery(string) {
	h.cancels.Add(1)
}

func observeAndWaitForIdle(t *testing.T, inbox *Inbox, conversationID string, observation AmbientObservation) {
	t.Helper()
	if err := inbox.Observe(conversationID, observation); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, func() bool {
		inbox.mu.Lock()
		defer inbox.mu.Unlock()
		state := inbox.states[conversationID]
		return state != nil && !state.running
	})
}

func testObservation(index int) AmbientObservation {
	return AmbientObservation{MessageID: fmt.Sprintf("m%d", index), SenderID: "u1", SenderName: "n", Text: "t", TimestampUnixMS: int64(index + 1)}
}

func receiveBatch(t *testing.T, batches <-chan ambientBatch) ambientBatch {
	t.Helper()
	select {
	case batch := <-batches:
		return batch
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for ambient batch")
		return ambientBatch{}
	}
}

func receiveTurnRequest(t *testing.T, requests <-chan TurnRequest) TurnRequest {
	t.Helper()
	select {
	case request := <-requests:
		return request
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for turn request")
		return TurnRequest{}
	}
}

func waitUntil(t *testing.T, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition did not become ready")
}
