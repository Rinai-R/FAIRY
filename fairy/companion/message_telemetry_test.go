package companion

import (
	"fairy/session"
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
	service := NewCompanionService()
	telemetry := newFakeMessageTelemetry()
	AttachMessageTelemetry(service, telemetry)
	life := newTurnLifecycle("c1", "t1")
	for _, state := range []turnState{turnStateInterpreting, turnStateGathering, turnStatePlanning, turnStateResponding} {
		if _, err := service.publishLife(life, func() (session.Event, error) { return life.Transition(state) }); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := service.publishLife(life, func() (session.Event, error) {
		return life.BeatReady(BeatReadyCompletion{BeatID: "b1", Kind: "final", DisplayText: "hello"})
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.publishLife(life, func() (session.Event, error) { return life.Complete(turnCompletion{}) }); err != nil {
		t.Fatal(err)
	}
	firstBeat := receiveTelemetryCall(t, telemetry.calls)
	completed := receiveTelemetryCall(t, telemetry.calls)
	if firstBeat.stage != "first_beat" || completed.stage != "completed" {
		t.Fatalf("stages = %#v, %#v", firstBeat, completed)
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
