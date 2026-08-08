package conversation

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAgentLoopMetricsAggregatesWithoutContent(t *testing.T) {
	var metrics agentLoopMetrics
	metrics.providerFirstByte(12 * time.Millisecond)
	metrics.providerFirstByte(20 * time.Millisecond)
	metrics.replyPreview(30 * time.Millisecond)
	snapshot := metrics.Snapshot()
	if snapshot.ProviderFirstByte.Observations != 2 || snapshot.ProviderFirstByte.TotalDurationMS != 32 || snapshot.ProviderFirstByte.MaxDurationMS != 20 {
		t.Fatalf("provider first byte = %#v", snapshot.ProviderFirstByte)
	}
	if snapshot.ReplyPreview.Observations != 1 || snapshot.ReplyPreview.MaxDurationMS != 30 {
		t.Fatalf("reply preview = %#v", snapshot.ReplyPreview)
	}
}

func TestCompactionMetricsCountAppliedLayersAndFailuresConcurrently(t *testing.T) {
	var metrics agentLoopMetrics
	const workers = 25
	var group sync.WaitGroup
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			metrics.compactionApplied("l1")
			metrics.compactionApplied("l2")
			metrics.compactionApplied("l3")
			metrics.compactionApplied("unknown")
			metrics.compactionFailed()
		}()
	}
	group.Wait()
	snapshot := metrics.Snapshot().Compaction
	if snapshot.L1Applied != workers || snapshot.L2Applied != workers ||
		snapshot.L3Applied != workers || snapshot.Failed != workers {
		t.Fatalf("compaction metrics = %#v", snapshot)
	}
}

func TestCompactionMetricsNoopLeavesCountersUnchanged(t *testing.T) {
	var metrics agentLoopMetrics
	if snapshot := metrics.Snapshot().Compaction; snapshot != (CompactionMetrics{}) {
		t.Fatalf("empty compaction metrics = %#v", snapshot)
	}
}

func TestCompactionMetricsSnapshotStaysLowSensitivity(t *testing.T) {
	var metrics agentLoopMetrics
	metrics.compactionApplied("l1")
	metrics.compactionApplied("l2")
	metrics.compactionApplied("l3")
	metrics.compactionFailed()
	payload, err := json.Marshal(metrics.Snapshot().Compaction)
	if err != nil {
		t.Fatal(err)
	}
	encoded := strings.ToLower(string(payload))
	for _, expected := range []string{`"l1Applied":1`, `"l2Applied":1`, `"l3Applied":1`, `"failed":1`} {
		if !strings.Contains(string(payload), expected) {
			t.Fatalf("compaction metrics omitted %s: %s", expected, payload)
		}
	}
	for _, forbidden := range []string{"content", "summary", "tool", "memory", "conversation", "turn", "cachekey", "hash"} {
		if strings.Contains(encoded, forbidden) {
			t.Fatalf("compaction metrics exposed forbidden key %q: %s", forbidden, payload)
		}
	}
}
