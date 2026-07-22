package companion

import (
	"context"
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

func TestAmbientInboxReportsAcceptedObservationAndSilentDecision(t *testing.T) {
	service := NewCompanionService()
	t.Cleanup(func() { _ = service.Close() })
	telemetry := newFakeMessageTelemetry()
	AttachMessageTelemetry(service, telemetry)
	service.ambient.decideHook = func(context.Context, ambientBatch) (ParticipationResult, error) {
		return ParticipationResult{Action: ParticipationSilent}, nil
	}

	if err := service.ambient.Observe("c1", AmbientObservation{
		MessageID: "m1", SenderID: "u1", SenderName: "user", Text: "hello", TimestampUnixMS: time.Now().UnixMilli(),
	}); err != nil {
		t.Fatal(err)
	}
	begin := receiveTelemetryCall(t, telemetry.calls)
	decision := receiveTelemetryCall(t, telemetry.calls)
	if begin.kind != "begin" || decision.kind != "participation" || decision.action != "silent" {
		t.Fatalf("telemetry calls = %#v, %#v", begin, decision)
	}
	if len(decision.traceIDs) != 1 || decision.traceIDs[0] != begin.traceID {
		t.Fatalf("decision trace IDs = %v, begin = %q", decision.traceIDs, begin.traceID)
	}
}

func TestPublishLifeReportsFinalBeatAndTerminalStages(t *testing.T) {
	service := NewCompanionService()
	telemetry := newFakeMessageTelemetry()
	AttachMessageTelemetry(service, telemetry)
	life := NewTurnLifecycle("c1", "t1")
	for _, state := range []TurnState{TurnStateInterpreting, TurnStateGathering, TurnStatePlanning, TurnStateResponding} {
		if _, err := service.publishLife(life, func() (TurnEvent, error) { return life.Transition(state) }); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := service.publishLife(life, func() (TurnEvent, error) {
		return life.BeatReady(BeatReadyCompletion{BeatID: "b1", Kind: "final", DisplayText: "hello"})
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.publishLife(life, func() (TurnEvent, error) { return life.Complete(TurnCompletion{}) }); err != nil {
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
