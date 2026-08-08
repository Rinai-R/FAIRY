package conversation

import (
	"fairy/agent/conversation/lifecycle"
	"fairy/agent/reply"
	"fairy/transport/session"
	"sync"
	"testing"
	"time"
)

type telemetryCall struct {
	kind, traceID, action, stage string
	traceIDs                     []string
}

type fakeMessageTelemetry struct {
	mu       sync.Mutex
	sequence int
	calls    chan telemetryCall
}

func newFakeMessageTelemetry() *fakeMessageTelemetry {
	return &fakeMessageTelemetry{calls: make(chan telemetryCall, 16)}
}

func (f *fakeMessageTelemetry) Begin(source, conversationID string) string {
	f.mu.Lock()
	f.sequence++
	traceID := "trace-" + string(rune('0'+f.sequence))
	f.mu.Unlock()
	f.calls <- telemetryCall{kind: "begin", traceID: traceID}
	return traceID
}

func (f *fakeMessageTelemetry) Participation(traceIDs []string, targetTraceID, action string) {
	f.calls <- telemetryCall{kind: "participation", traceID: targetTraceID, action: action, traceIDs: append([]string(nil), traceIDs...)}
}

func (f *fakeMessageTelemetry) TurnStarted(traceID, conversationID, turnID string) {
	f.calls <- telemetryCall{kind: "turn", traceID: traceID}
}

func (f *fakeMessageTelemetry) TurnStage(conversationID, turnID, stage string) {
	f.calls <- telemetryCall{kind: "stage", stage: stage}
}

func (f *fakeMessageTelemetry) End(traceID, status string) {
	f.calls <- telemetryCall{kind: "end", traceID: traceID, action: status}
}

func TestPublishLifeReportsFinalBeatAndTerminalStages(t *testing.T) {
	service := NewService()
	telemetry := newFakeMessageTelemetry()
	AttachMessageTelemetry(service, telemetry)
	life := lifecycle.New("c1", "t1")
	for _, state := range []lifecycle.State{lifecycle.StateInterpreting, lifecycle.StateGathering, lifecycle.StatePlanning, lifecycle.StateResponding} {
		if _, err := service.publishLife(life, func() (session.Event, error) { return life.Transition(state) }); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := service.publishLife(life, func() (session.Event, error) {
		return life.BeatReady(reply.BeatReadyCompletion{BeatID: "b1", Kind: "final", DisplayText: "hello"})
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.publishLife(life, func() (session.Event, error) { return life.Complete(lifecycle.Completion{}) }); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"lifecycle:interpreting", "lifecycle:gathering", "lifecycle:planning", "lifecycle:responding",
		"first_beat", "completed",
	}
	for index, stage := range want {
		call := receiveTelemetryCall(t, telemetry.calls)
		if call.stage != stage {
			t.Fatalf("stage[%d] = %#v, want %q", index, call, stage)
		}
	}
}

func receiveTelemetryCall(t *testing.T, calls <-chan telemetryCall) telemetryCall {
	t.Helper()
	select {
	case call := <-calls:
		return call
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for telemetry call")
		return telemetryCall{}
	}
}
