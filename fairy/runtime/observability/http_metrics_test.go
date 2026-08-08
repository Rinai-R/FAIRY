package observability

import (
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"
)

func TestHTTPMetricsNormalizesRoutesAndReturnsDeepCopy(t *testing.T) {
	metrics := NewHTTPMetrics()
	for _, status := range []int{200, 404} {
		observation := metrics.Begin("POST", "/v1/sessions/:conversationId/turns", false)
		observation.started = observation.started.Add(-time.Millisecond)
		metrics.Finish(observation, status)
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
				observation := metrics.Begin("GET", "/v1/status", false)
				metrics.Finish(observation, 200)
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

func TestHTTPMetricsBoundsExtensionMethodCardinality(t *testing.T) {
	metrics := NewHTTPMetrics()
	const requests = 10_000
	for index := 0; index < requests; index++ {
		observation := metrics.Begin(fmt.Sprintf("FAIRY-EXTENSION-%d", index), "/v1/status", false)
		metrics.Finish(observation, http.StatusNotFound)
	}

	snapshot := metrics.Snapshot()
	if snapshot.Total != requests || snapshot.Status4xx != requests {
		t.Fatalf("snapshot totals = %#v", snapshot)
	}
	if len(snapshot.Routes) != 1 {
		t.Fatalf("route series = %d, want 1", len(snapshot.Routes))
	}
	route := snapshot.Routes[0]
	if route.Method != "OTHER" || route.Route != "/v1/status" || route.RequestCount != requests || route.ErrorCount != requests {
		t.Fatalf("extension route = %#v", route)
	}
}

func TestHTTPMetricsPreservesStandardMethods(t *testing.T) {
	methods := []string{
		http.MethodConnect,
		http.MethodDelete,
		http.MethodGet,
		http.MethodHead,
		http.MethodOptions,
		http.MethodPatch,
		http.MethodPost,
		http.MethodPut,
		http.MethodTrace,
	}
	metrics := NewHTTPMetrics()
	for _, method := range methods {
		metrics.Finish(metrics.Begin(method, "/v1/status", false), http.StatusOK)
	}

	snapshot := metrics.Snapshot()
	if len(snapshot.Routes) != len(methods) {
		t.Fatalf("route series = %d, want %d: %#v", len(snapshot.Routes), len(methods), snapshot.Routes)
	}
	for index, method := range methods {
		if snapshot.Routes[index].Method != method || snapshot.Routes[index].RequestCount != 1 {
			t.Fatalf("route[%d] = %#v, want method %q", index, snapshot.Routes[index], method)
		}
	}
}

func TestHTTPMetricsDoesNotTreatLongLivedConnectionAsRequestLatency(t *testing.T) {
	metrics := NewHTTPMetrics()
	observation := metrics.Begin("GET", "/v1/session/ws", true)
	observation.started = observation.started.Add(-time.Hour)
	metrics.Finish(observation, http.StatusSwitchingProtocols)

	snapshot := metrics.Snapshot()
	if snapshot.Total != 1 || snapshot.InFlight != 0 || len(snapshot.Routes) != 1 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	route := snapshot.Routes[0]
	if !route.LongLived || route.TotalDurationMS != 0 || route.MaxDurationMS != 0 {
		t.Fatalf("long-lived route = %#v", route)
	}
}

func TestHTTPMetricsCountsActiveLongLivedConnectionAtBegin(t *testing.T) {
	metrics := NewHTTPMetrics()
	_ = metrics.Begin("GET", "/v1/session/ws", true)

	snapshot := metrics.Snapshot()
	if snapshot.Total != 1 || snapshot.InFlight != 1 || len(snapshot.Routes) != 1 {
		t.Fatalf("active snapshot = %#v", snapshot)
	}
	if route := snapshot.Routes[0]; !route.LongLived || route.RequestCount != 1 || route.TotalDurationMS != 0 {
		t.Fatalf("active route = %#v", route)
	}
}

func TestSnapshotProcess(t *testing.T) {
	snapshot := SnapshotProcess(time.Now().Add(-2 * time.Second))
	if snapshot.UptimeSeconds < 2 || snapshot.GoVersion == "" || snapshot.Goroutines == 0 || snapshot.HeapAllocBytes == 0 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}
