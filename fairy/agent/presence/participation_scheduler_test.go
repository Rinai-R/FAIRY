package presence

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	history "fairy/context/history/transcript"
)

type manualParticipationTimer struct {
	stopped  atomic.Bool
	callback func()
}

func (timer *manualParticipationTimer) Stop() bool {
	return !timer.stopped.Swap(true)
}

func (timer *manualParticipationTimer) Fire() {
	if timer != nil && !timer.stopped.Load() {
		timer.callback()
	}
}

type scheduledParticipationTimer struct {
	delay time.Duration
	timer *manualParticipationTimer
}

type activityInboxHost struct {
	fakeInboxHost
	activity history.ConversationActivity
	err      error
}

func (host activityInboxHost) LoadConversationActivity(string, int64) (history.ConversationActivity, error) {
	return host.activity, host.err
}

type participationScheduleSpan struct {
	traceID    string
	operation  string
	category   string
	status     string
	attributes map[string]string
}

type scheduleTraceInboxHost struct {
	activityInboxHost
	started  chan participationScheduleSpan
	finished chan participationScheduleSpan
}

type blockingActivityInboxHost struct {
	fakeInboxHost
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (host *blockingActivityInboxHost) LoadConversationActivity(string, int64) (history.ConversationActivity, error) {
	host.once.Do(func() {
		close(host.started)
		<-host.release
	})
	return history.ConversationActivity{}, nil
}

func (host *scheduleTraceInboxHost) StartParticipationSpan(traceID, operation, category string, attributes map[string]string) string {
	host.started <- participationScheduleSpan{traceID: traceID, operation: operation, category: category, attributes: attributes}
	return "schedule-span-1"
}

func (host *scheduleTraceInboxHost) FinishParticipationSpan(_ string, status string, attributes map[string]string) {
	host.finished <- participationScheduleSpan{status: status, attributes: attributes}
}

func TestParticipationPressureThreshold(t *testing.T) {
	tests := []struct {
		name     string
		activity history.ConversationActivity
		want     int
	}{
		{name: "quiet conversation", want: 2},
		{name: "one recent reply", activity: history.ConversationActivity{AssistantMessages5Minutes: 1}, want: 3},
		{name: "moderate share", activity: history.ConversationActivity{AssistantMessages30Minutes: 3, UserMessages30Minutes: 7}, want: 3},
		{name: "high recent presence", activity: history.ConversationActivity{AssistantMessages5Minutes: 3, AssistantMessages30Minutes: 7, UserMessages30Minutes: 3}, want: 7},
		{name: "threshold is capped", activity: history.ConversationActivity{AssistantMessages5Minutes: 99, AssistantMessages30Minutes: 99}, want: 7},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := participationPressureThreshold(test.activity); got != test.want {
				t.Fatalf("threshold = %d, want %d", got, test.want)
			}
		})
	}
}

func TestDeriveParticipationSchedule(t *testing.T) {
	now := time.Unix(100, 0)
	activity := history.ConversationActivity{AssistantMessages5Minutes: 1}
	if got := deriveParticipationSchedule(2, now.Add(-2*time.Second), now, activity); got.Ready || got.PressureThreshold != 3 {
		t.Fatalf("below pressure schedule = %#v", got)
	}
	if got := deriveParticipationSchedule(3, now.Add(-2*time.Second), now, activity); !got.Ready {
		t.Fatalf("pressure-ready schedule = %#v", got)
	}
	if got := deriveParticipationSchedule(1, now.Add(-participationMaximumWait), now, activity); !got.Ready {
		t.Fatalf("maximum-wait schedule = %#v", got)
	}
}

func TestParticipationScheduleDelay(t *testing.T) {
	now := time.Unix(100, 0)
	pendingSince := now.Add(-2 * time.Second)
	lastReceived := now.Add(-200 * time.Millisecond)
	if got := participationScheduleDelay(now, pendingSince, lastReceived, time.Time{}); got != 800*time.Millisecond {
		t.Fatalf("quiet delay = %s", got)
	}
	lastReceived = now
	pendingSince = now.Add(-participationMaximumWait + 300*time.Millisecond)
	if got := participationScheduleDelay(now, pendingSince, lastReceived, time.Time{}); got != 300*time.Millisecond {
		t.Fatalf("maximum wait delay = %s", got)
	}
	backoff := now.Add(5 * time.Second)
	if got := participationScheduleDelay(now, pendingSince, lastReceived, backoff); got != 5*time.Second {
		t.Fatalf("backoff delay = %s", got)
	}
}

func TestInboxBuffersBurstBeforeOneParticipationDecision(t *testing.T) {
	timers := make(chan scheduledParticipationTimer, 4)
	inbox := NewInbox(t.Context(), activityInboxHost{})
	defer inbox.Close()
	inbox.after = func(delay time.Duration, callback func()) stoppableTimer {
		timer := &manualParticipationTimer{callback: callback}
		timers <- scheduledParticipationTimer{delay: delay, timer: timer}
		return timer
	}
	batches := make(chan ambientBatch, 1)
	inbox.decideHook = func(_ context.Context, batch ambientBatch) (ParticipationResult, error) {
		batches <- batch
		return ParticipationResult{Action: ParticipationSilent}, nil
	}

	if err := inbox.Observe("c1", testObservation(1)); err != nil {
		t.Fatal(err)
	}
	first := <-timers
	if err := inbox.Observe("c1", testObservation(2)); err != nil {
		t.Fatal(err)
	}
	second := <-timers
	if !first.timer.stopped.Load() {
		t.Fatal("superseded quiet timer remains active")
	}
	select {
	case batch := <-batches:
		t.Fatalf("decision started before quiet timer: %#v", batch)
	default:
	}

	second.timer.Fire()
	batch := receiveBatch(t, batches)
	if batch.generation != 2 || len(batch.messages) != 2 || !batch.messages[0].IsNew || !batch.messages[1].IsNew {
		t.Fatalf("burst batch = %#v", batch)
	}
}

func TestInboxLowTrafficWaitsUntilMaximumDeadline(t *testing.T) {
	timers := make(chan scheduledParticipationTimer, 4)
	current := time.Unix(100, 0)
	inbox := NewInbox(t.Context(), activityInboxHost{})
	defer inbox.Close()
	inbox.now = func() time.Time { return current }
	inbox.after = func(delay time.Duration, callback func()) stoppableTimer {
		timer := &manualParticipationTimer{callback: callback}
		timers <- scheduledParticipationTimer{delay: delay, timer: timer}
		return timer
	}
	batches := make(chan ambientBatch, 1)
	inbox.decideHook = func(_ context.Context, batch ambientBatch) (ParticipationResult, error) {
		batches <- batch
		return ParticipationResult{Action: ParticipationSilent}, nil
	}

	if err := inbox.Observe("c1", testObservation(1)); err != nil {
		t.Fatal(err)
	}
	quiet := <-timers
	if quiet.delay != participationQuietPeriod {
		t.Fatalf("quiet delay = %s", quiet.delay)
	}
	current = current.Add(participationQuietPeriod)
	quiet.timer.Fire()
	maximum := <-timers
	if maximum.delay != participationMaximumWait-participationQuietPeriod {
		t.Fatalf("maximum delay = %s", maximum.delay)
	}
	select {
	case batch := <-batches:
		t.Fatalf("low traffic decided before maximum wait: %#v", batch)
	default:
	}
	current = current.Add(maximum.delay)
	maximum.timer.Fire()
	if batch := receiveBatch(t, batches); batch.generation != 1 {
		t.Fatalf("maximum wait batch = %#v", batch)
	}
}

func TestInboxPlannerWaitAccumulatesWithoutEarlyWake(t *testing.T) {
	timers := make(chan scheduledParticipationTimer, 4)
	inbox := NewInbox(t.Context(), activityInboxHost{})
	defer inbox.Close()
	inbox.messageDelayHook = func(time.Time, time.Time, time.Time, time.Time) time.Duration { return 0 }
	inbox.scheduleHook = func(pending int, since, now time.Time, activity history.ConversationActivity) participationSchedule {
		result := deriveParticipationSchedule(pending, since, now, activity)
		result.Ready = true
		return result
	}
	inbox.after = func(delay time.Duration, callback func()) stoppableTimer {
		timer := &manualParticipationTimer{callback: callback}
		timers <- scheduledParticipationTimer{delay: delay, timer: timer}
		return timer
	}
	batches := make(chan ambientBatch, 2)
	var calls atomic.Int32
	inbox.decideHook = func(_ context.Context, batch ambientBatch) (ParticipationResult, error) {
		batches <- batch
		if calls.Add(1) == 1 {
			seconds := 30
			return ParticipationResult{Action: ParticipationWait, WaitSeconds: &seconds}, nil
		}
		return ParticipationResult{Action: ParticipationSilent}, nil
	}

	if err := inbox.Observe("c1", testObservation(1)); err != nil {
		t.Fatal(err)
	}
	(<-timers).timer.Fire()
	firstBatch := receiveBatch(t, batches)
	if firstBatch.evaluationReason != ParticipationReasonMessage {
		t.Fatalf("first reason = %q", firstBatch.evaluationReason)
	}
	waitTimer := <-timers
	if waitTimer.delay != 30*time.Second {
		t.Fatalf("planner wait delay = %s", waitTimer.delay)
	}
	if err := inbox.Observe("c1", testObservation(2)); err != nil {
		t.Fatal(err)
	}
	select {
	case extra := <-timers:
		t.Fatalf("new message replaced planner wait: %#v", extra)
	default:
	}
	waitTimer.timer.Fire()
	secondBatch := receiveBatch(t, batches)
	if secondBatch.evaluationReason != ParticipationReasonWaitElapsed || secondBatch.generation != 2 || len(secondBatch.messages) != 2 {
		t.Fatalf("wait elapsed batch = %#v", secondBatch)
	}
}

func TestInboxActivityFailureFailsClosedBeforeDecision(t *testing.T) {
	timers := make(chan scheduledParticipationTimer, 1)
	inbox := NewInbox(t.Context(), activityInboxHost{err: errors.New("activity unavailable")})
	defer inbox.Close()
	inbox.messageDelayHook = func(time.Time, time.Time, time.Time, time.Time) time.Duration { return 0 }
	inbox.after = func(delay time.Duration, callback func()) stoppableTimer {
		timer := &manualParticipationTimer{callback: callback}
		timers <- scheduledParticipationTimer{delay: delay, timer: timer}
		return timer
	}
	var decisions atomic.Int32
	inbox.decideHook = func(context.Context, ambientBatch) (ParticipationResult, error) {
		decisions.Add(1)
		return ParticipationResult{Action: ParticipationReply}, nil
	}
	if err := inbox.Observe("c1", testObservation(1)); err != nil {
		t.Fatal(err)
	}
	(<-timers).timer.Fire()
	if decisions.Load() != 0 {
		t.Fatalf("participation decisions = %d, want 0", decisions.Load())
	}
	inbox.mu.Lock()
	state := inbox.states["c1"]
	inbox.mu.Unlock()
	if state == nil || state.acceptedGeneration != 1 || state.running {
		t.Fatalf("fail-closed state = %#v", state)
	}
}

func TestInboxScheduleTraceUsesBoundedActivityAttributes(t *testing.T) {
	timers := make(chan scheduledParticipationTimer, 1)
	host := &scheduleTraceInboxHost{
		activityInboxHost: activityInboxHost{activity: history.ConversationActivity{
			AssistantMessages5Minutes:  1,
			AssistantMessages30Minutes: 2,
			UserMessages30Minutes:      6,
		}},
		started:  make(chan participationScheduleSpan, 1),
		finished: make(chan participationScheduleSpan, 1),
	}
	inbox := NewInbox(t.Context(), host)
	defer inbox.Close()
	inbox.messageDelayHook = func(time.Time, time.Time, time.Time, time.Time) time.Duration { return 0 }
	inbox.scheduleHook = func(pending int, since, now time.Time, activity history.ConversationActivity) participationSchedule {
		result := deriveParticipationSchedule(pending, since, now, activity)
		result.Ready = true
		return result
	}
	inbox.after = func(delay time.Duration, callback func()) stoppableTimer {
		timer := &manualParticipationTimer{callback: callback}
		timers <- scheduledParticipationTimer{delay: delay, timer: timer}
		return timer
	}
	inbox.decideHook = func(context.Context, ambientBatch) (ParticipationResult, error) {
		return ParticipationResult{Action: ParticipationSilent}, nil
	}

	observation := testObservation(1)
	observation.TraceID = "trace-1"
	observation.Text = "不得出现在调度属性中的正文"
	if err := inbox.Observe("c1", observation); err != nil {
		t.Fatal(err)
	}
	(<-timers).timer.Fire()

	started := <-host.started
	if started.traceID != "trace-1" || started.operation != "参与调度" || started.category != "schedule" {
		t.Fatalf("started schedule span = %#v", started)
	}
	finished := <-host.finished
	if finished.status != "completed" {
		t.Fatalf("schedule span status = %q", finished.status)
	}
	want := map[string]string{
		"pendingCount":        "1",
		"pressureThreshold":   "3",
		"assistantReplies5m":  "1",
		"assistantReplies30m": "2",
		"userMessages30m":     "6",
	}
	for key, value := range want {
		if finished.attributes[key] != value {
			t.Fatalf("schedule attribute %s = %q, want %q; all=%#v", key, finished.attributes[key], value, finished.attributes)
		}
	}
	for key, value := range finished.attributes {
		if key == "text" || value == observation.Text {
			t.Fatalf("schedule span leaked message text: %s=%q", key, value)
		}
	}
}

func TestInboxObservationDuringActivityReadRefreshesQuietWindow(t *testing.T) {
	timers := make(chan scheduledParticipationTimer, 3)
	host := &blockingActivityInboxHost{started: make(chan struct{}), release: make(chan struct{})}
	inbox := NewInbox(t.Context(), host)
	defer inbox.Close()
	inbox.messageDelayHook = func(time.Time, time.Time, time.Time, time.Time) time.Duration { return participationQuietPeriod }
	inbox.after = func(delay time.Duration, callback func()) stoppableTimer {
		timer := &manualParticipationTimer{callback: callback}
		timers <- scheduledParticipationTimer{delay: delay, timer: timer}
		return timer
	}
	batches := make(chan ambientBatch, 1)
	inbox.decideHook = func(_ context.Context, batch ambientBatch) (ParticipationResult, error) {
		batches <- batch
		return ParticipationResult{Action: ParticipationSilent}, nil
	}

	if err := inbox.Observe("c1", testObservation(1)); err != nil {
		t.Fatal(err)
	}
	first := <-timers
	go first.timer.Fire()
	<-host.started
	if err := inbox.Observe("c1", testObservation(2)); err != nil {
		t.Fatal(err)
	}
	close(host.release)
	refreshed := <-timers
	if refreshed.delay != participationQuietPeriod {
		t.Fatalf("refreshed quiet delay = %s", refreshed.delay)
	}
	select {
	case batch := <-batches:
		t.Fatalf("decision bypassed refreshed quiet window: %#v", batch)
	default:
	}
	refreshed.timer.Fire()
	if batch := receiveBatch(t, batches); batch.generation != 2 || len(batch.messages) != 2 {
		t.Fatalf("refreshed batch = %#v", batch)
	}
}
