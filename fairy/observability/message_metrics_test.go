package observability

import (
	"encoding/json"
	"strings"
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
