package observability

import (
	"sync"
	"testing"
	"time"
)

func TestHTTPMetricsNormalizesRoutesAndReturnsDeepCopy(t *testing.T) {
	metrics := NewHTTPMetrics()
	for _, status := range []int{200, 404} {
		started := metrics.Begin()
		metrics.Finish("POST", "/v1/sessions/:conversationId/turns", status, started.Add(-time.Millisecond))
	}
	snapshot := metrics.Snapshot()
	if snapshot.Total != 2 || snapshot.InFlight != 0 || snapshot.Status2xx != 1 || snapshot.Status4xx != 1 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	if len(snapshot.Routes) != 1 || snapshot.Routes[0].RequestCount != 2 || snapshot.Routes[0].ErrorCount != 1 {
		t.Fatalf("routes = %#v", snapshot.Routes)
	}
	snapshot.Routes[0].RequestCount = 99
	if got := metrics.Snapshot().Routes[0].RequestCount; got != 2 {
		t.Fatalf("internal route count mutated: %d", got)
	}
}

func TestHTTPMetricsConcurrentRecordAndSnapshot(t *testing.T) {
	metrics := NewHTTPMetrics()
	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				started := metrics.Begin()
				metrics.Finish("GET", "/v1/status", 200, started)
				_ = metrics.Snapshot()
			}
		}()
	}
	wg.Wait()
	snapshot := metrics.Snapshot()
	if snapshot.Total != 800 || snapshot.InFlight != 0 || snapshot.Routes[0].RequestCount != 800 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}

func TestSnapshotProcess(t *testing.T) {
	snapshot := SnapshotProcess(time.Now().Add(-2 * time.Second))
	if snapshot.UptimeSeconds < 2 || snapshot.GoVersion == "" || snapshot.Goroutines == 0 || snapshot.HeapAllocBytes == 0 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}
