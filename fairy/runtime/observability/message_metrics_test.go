package observability

import (
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestMessageMetricsAggregatesDirectTrace(t *testing.T) {
	metrics := NewMessageMetrics()
	t.Cleanup(metrics.Close)
	traceID := metrics.Begin("direct", "c1")
	metrics.TurnStarted(traceID, "c1", "t1")
	metrics.TurnStage("c1", "t1", "first_beat")
	metrics.TurnStage("c1", "t1", "first_beat")
	metrics.TurnStage("c1", "t1", "completed")

	snapshot := waitForMessageSnapshot(t, metrics, func(value MessageMetricsSnapshot) bool { return value.Completed == 1 })
	if snapshot.Received != 1 || snapshot.DirectReceived != 1 || snapshot.Sent != 1 || snapshot.Active != 0 {
		t.Fatalf("message counts = %#v", snapshot)
	}
	if snapshot.Latencies.ReceiveToTurn.Observations != 1 || snapshot.Latencies.TurnToFirstBeat.Observations != 1 || snapshot.Latencies.ReceiveToCompleted.Observations != 1 {
		t.Fatalf("latencies = %#v", snapshot.Latencies)
	}
}

func TestMessageMetricsPublishesTerminalTraceWithoutBlocking(t *testing.T) {
	metrics := NewMessageMetrics()
	t.Cleanup(metrics.Close)
	persisted := make(chan MessageTraceDetail, 1)
	metrics.SetTerminalSink(func(detail MessageTraceDetail) bool {
		persisted <- detail
		return true
	})
	traceID := metrics.Begin("direct", "conversation")
	metrics.End(traceID, "failed")
	select {
	case detail := <-persisted:
		if detail.TraceID != traceID || detail.Status != "failed" || len(detail.Spans) == 0 {
			t.Fatalf("terminal detail = %#v", detail)
		}
	case <-time.After(time.Second):
		t.Fatal("terminal trace was not published")
	}
}

func TestMessageMetricsKeepsFirstTerminalAndMessageCorrelation(t *testing.T) {
	metrics := NewMessageMetrics()
	t.Cleanup(metrics.Close)
	persisted := make(chan MessageTraceDetail, 2)
	metrics.SetTerminalSink(func(detail MessageTraceDetail) bool {
		persisted <- detail
		return true
	})
	traceID := metrics.BeginCorrelated("ambient", "conversation", "external-message-42")
	metrics.Participation([]string{traceID}, "", "silent_error")
	metrics.Participation([]string{traceID}, "", "failed")

	detail := <-persisted
	if detail.Status != "silent" || detail.MessageID != "external-message-42" {
		t.Fatalf("terminal trace = %#v", detail)
	}
	participation := detail.Spans[1]
	if participation.Status != "failed" || participation.Attributes["action"] != "silent" {
		t.Fatalf("fail-closed participation span = %#v", participation)
	}
	select {
	case duplicate := <-persisted:
		t.Fatalf("conflicting late terminal was persisted: %#v", duplicate)
	case <-time.After(20 * time.Millisecond):
	}
	snapshot := waitForMessageSnapshot(t, metrics, func(value MessageMetricsSnapshot) bool { return value.Silent == 1 })
	if snapshot.Failed != 0 || snapshot.Active != 0 || snapshot.Recent[0].Status != "silent" || snapshot.Recent[0].MessageID != "external-message-42" {
		t.Fatalf("first-terminal snapshot = %#v", snapshot)
	}
	found := metrics.TracesByMessageID("external-message-42", 10)
	if len(found) != 1 || found[0].TraceID != traceID || found[0].Status != "silent" {
		t.Fatalf("message correlation = %#v", found)
	}
}

func TestMessageTraceIDsRemainUniqueAcrossMetricOwners(t *testing.T) {
	first := NewMessageMetrics()
	second := NewMessageMetrics()
	t.Cleanup(first.Close)
	t.Cleanup(second.Close)
	if left, right := first.Begin("direct", "c1"), second.Begin("direct", "c2"); left == right {
		t.Fatalf("trace ids collide across owners: %q", left)
	}
}

func TestMessageMetricsParticipationClosesNonTargetTraces(t *testing.T) {
	metrics := NewMessageMetrics()
	t.Cleanup(metrics.Close)
	first := metrics.Begin("ambient", "c1")
	second := metrics.Begin("ambient", "c1")
	metrics.Participation([]string{first, second}, second, "reply")
	metrics.TurnStarted(second, "c1", "t1")
	metrics.TurnStage("c1", "t1", "completed")

	snapshot := waitForMessageSnapshot(t, metrics, func(value MessageMetricsSnapshot) bool { return value.Completed == 1 })
	if snapshot.Received != 2 || snapshot.AmbientReceived != 2 || snapshot.Silent != 1 || snapshot.Sent != 0 || snapshot.Active != 0 {
		t.Fatalf("message counts = %#v", snapshot)
	}
	if snapshot.Latencies.ReceiveToDecision.Observations != 2 || snapshot.Latencies.ReceiveToTurn.Observations != 1 {
		t.Fatalf("latencies = %#v", snapshot.Latencies)
	}
}

func TestMessageMetricsQueuePressureNeverBlocks(t *testing.T) {
	metrics := newMessageMetrics(1, 1, false)
	started := time.Now()
	metrics.Begin("direct", "c1")
	metrics.Begin("direct", "c2")
	if elapsed := time.Since(started); elapsed > 50*time.Millisecond {
		t.Fatalf("non-blocking submissions took %s", elapsed)
	}
	if metrics.Snapshot().DroppedEvents != 1 {
		t.Fatalf("dropped events = %d, want 1", metrics.Snapshot().DroppedEvents)
	}
}

func TestMessageMetricsColdSnapshotSerializesRecentAsArray(t *testing.T) {
	metrics := NewMessageMetrics()
	t.Cleanup(metrics.Close)
	payload, err := json.Marshal(metrics.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(payload), `"recent":[]`) {
		t.Fatalf("cold snapshot must encode recent as an array: %s", payload)
	}
}

func TestMessageMetricsCloseIsIdempotentAndRejectsNewEvents(t *testing.T) {
	metrics := NewMessageMetrics()
	metrics.Close()
	metrics.Close()
	metrics.Begin("direct", "c1")
	if metrics.Snapshot().DroppedEvents != 1 {
		t.Fatalf("dropped events = %d, want 1", metrics.Snapshot().DroppedEvents)
	}
}

func TestMessageMetricsSnapshotIsBoundedAndContainsNoContentFields(t *testing.T) {
	metrics := newMessageMetrics(16, 1, true)
	t.Cleanup(metrics.Close)
	for index := 0; index < 2; index++ {
		traceID := metrics.Begin("direct", "conversation")
		metrics.End(traceID, "failed")
	}
	snapshot := waitForMessageSnapshot(t, metrics, func(value MessageMetricsSnapshot) bool { return value.Failed == 2 })
	if len(snapshot.Recent) != 1 {
		t.Fatalf("recent traces = %d, want 1", len(snapshot.Recent))
	}
	payload, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"messageText", "prompt", "token", "principal"} {
		if strings.Contains(strings.ToLower(string(payload)), strings.ToLower(forbidden)) {
			t.Fatalf("snapshot contains forbidden field %q: %s", forbidden, payload)
		}
	}
}

func TestMessageMetricsBoundsActiveTracesWhenTerminalTelemetryIsLost(t *testing.T) {
	var retentionDrops atomic.Uint64
	state := messageMetricsState{
		traces: make(map[string]*messageTraceState), turns: make(map[string]string),
		recentCapacity: 1, activeCapacity: 2, retentionDrops: &retentionDrops,
	}
	now := time.Now()
	state.begin(messageEvent{traceID: "trace-1", source: "direct", conversation: "c1", at: now})
	state.turnStarted(messageEvent{traceID: "trace-1", conversation: "c1", turnID: "turn-1", at: now.Add(time.Millisecond)})
	state.begin(messageEvent{traceID: "trace-2", source: "direct", conversation: "c2", at: now.Add(2 * time.Millisecond)})

	// Simulate terminal events being dropped by the non-blocking producer queue:
	// both accepted traces remain active when the next Begin reaches the owner.
	state.begin(messageEvent{traceID: "trace-3", source: "direct", conversation: "c3", at: now.Add(3 * time.Millisecond)})

	if len(state.traces) != 2 || state.snapshotBase.Active != 2 || state.snapshotBase.Received != 3 {
		t.Fatalf("bounded active trace state = traces:%d active:%d received:%d", len(state.traces), state.snapshotBase.Active, state.snapshotBase.Received)
	}
	if state.traces["trace-1"] != nil || state.traces["trace-3"] == nil {
		t.Fatalf("retained traces = %#v, want newest trace and no oldest trace", state.traces)
	}
	if _, exists := state.turns["turn-1"]; exists {
		t.Fatal("evicted active trace retained its turn mapping")
	}
	if retentionDrops.Load() != 1 {
		t.Fatalf("retention drops = %d, want 1", retentionDrops.Load())
	}

	state.turnStage(messageEvent{turnID: "turn-1", stage: "first_beat", at: now.Add(4 * time.Millisecond)})
	state.end("trace-1", "failed", now.Add(5*time.Millisecond))
	if state.snapshotBase.Sent != 0 || state.snapshotBase.Failed != 0 || state.snapshotBase.Active != 2 {
		t.Fatalf("late evicted-trace events changed metrics: %#v", state.snapshotBase)
	}

	metrics := newMessageMetrics(1, 1, false)
	metrics.dropped.Store(4)
	metrics.retentionDrops.Store(retentionDrops.Load())
	if dropped := metrics.Snapshot().DroppedEvents; dropped != 5 {
		t.Fatalf("combined dropped events = %d, want 5", dropped)
	}
}

func TestMessageMetricsWorkerRecoversAfterActiveRetentionEviction(t *testing.T) {
	metrics := newMessageMetrics(1, 1, true)
	t.Cleanup(metrics.Close)

	oldest := metrics.Begin("direct", "conversation-old")
	first := waitForMessageSnapshot(t, metrics, func(value MessageMetricsSnapshot) bool {
		return value.Received == 1
	})
	if first.Active != 1 || len(first.Recent) != 1 || first.Recent[0].TraceID != oldest {
		t.Fatalf("first worker snapshot = %#v", first)
	}

	latest := metrics.Begin("direct", "conversation-latest")
	recovered := waitForMessageSnapshot(t, metrics, func(value MessageMetricsSnapshot) bool {
		return value.Received == 2 && value.DroppedEvents == 1
	})
	if recovered.Active != 1 || len(recovered.Recent) != 1 || recovered.Recent[0].TraceID != latest {
		t.Fatalf("recovered worker snapshot = %#v", recovered)
	}

	metrics.End(oldest, "failed")
	metrics.End(latest, "completed")
	completed := waitForMessageSnapshot(t, metrics, func(value MessageMetricsSnapshot) bool {
		return value.Completed == 1
	})
	if completed.Active != 0 || completed.Failed != 0 || completed.DroppedEvents != 1 {
		t.Fatalf("late terminal worker snapshot = %#v", completed)
	}
}

func TestMessageMetricsSeparatelyBoundsActiveAndRecentTraces(t *testing.T) {
	state := messageMetricsState{
		traces: make(map[string]*messageTraceState), turns: make(map[string]string),
		recentCapacity: 1, activeCapacity: 2,
	}
	now := time.Now()
	state.begin(messageEvent{traceID: "trace-1", at: now})
	state.begin(messageEvent{traceID: "trace-2", at: now.Add(time.Millisecond)})
	state.end("trace-2", "completed", now.Add(2*time.Millisecond))
	state.begin(messageEvent{traceID: "trace-3", at: now.Add(3 * time.Millisecond)})
	if len(state.traces) != 3 || state.snapshotBase.Active != 2 || len(state.recentIDs) != 1 {
		t.Fatalf("full active/recent state = traces:%d active:%d recent:%d", len(state.traces), state.snapshotBase.Active, len(state.recentIDs))
	}

	state.end("trace-1", "failed", now.Add(4*time.Millisecond))
	if len(state.traces) != 2 || state.snapshotBase.Active != 1 || len(state.recentIDs) != 1 {
		t.Fatalf("rotated active/recent state = traces:%d active:%d recent:%d", len(state.traces), state.snapshotBase.Active, len(state.recentIDs))
	}
	if state.traces["trace-2"] != nil || state.traces["trace-1"] == nil || state.traces["trace-3"] == nil {
		t.Fatalf("retained active/recent traces = %#v", state.traces)
	}
}

func waitForMessageSnapshot(t *testing.T, metrics *MessageMetrics, ready func(MessageMetricsSnapshot) bool) MessageMetricsSnapshot {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		snapshot := metrics.Snapshot()
		if ready(snapshot) {
			return snapshot
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("message metrics did not reach expected state: %#v", metrics.Snapshot())
	return MessageMetricsSnapshot{}
}
