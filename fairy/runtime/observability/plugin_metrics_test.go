package observability

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
)

func TestPluginMetricsCountsBudgetDeniedTrapQueueAndRestart(t *testing.T) {
	metrics := NewPluginMetrics()
	metrics.QueueEnter("echo-1")
	if got := metrics.Snapshot(); got.QueueWaiters != 1 || got.Instances[0].QueueDepth != 1 {
		t.Fatalf("queue enter = %#v", got)
	}
	metrics.QueueLeave("echo-1")
	metrics.Start("echo-1")
	metrics.Finish("echo-1", "", "trace-1", true, false)
	metrics.Finish("echo-1", "BUDGET_EXCEEDED", "trace-1", false, false)
	metrics.Host("echo-1", "CAPABILITY_DENIED", "trace-1")
	metrics.Finish("echo-1", "MODULE_TRAP", "trace-1", false, true)
	snapshot := metrics.Snapshot()
	if snapshot.Calls != 3 || snapshot.HostCalls != 1 || snapshot.InFlight != 0 || snapshot.QueueWaiters != 0 {
		t.Fatalf("totals = %#v", snapshot)
	}
	if snapshot.BudgetExceeded != 1 || snapshot.CapabilityDenied != 1 || snapshot.Traps != 1 || snapshot.Restarts != 1 {
		t.Fatalf("classifiers = %#v", snapshot)
	}
	instance := snapshot.Instances[0]
	if instance.InstanceID != "echo-1" || instance.LastErrorCode != "MODULE_TRAP" || instance.LastTraceID != "trace-1" {
		t.Fatalf("instance = %#v", instance)
	}
	if instance.BudgetExceeded != 1 || instance.CapabilityDenied != 1 || instance.Traps != 1 || instance.Restarts != 1 {
		t.Fatalf("instance classifiers = %#v", instance)
	}
	snapshot.Instances[0].Calls = 99
	if metrics.Snapshot().Instances[0].Calls == 99 {
		t.Fatal("Snapshot returned mutable owner state")
	}
}

func TestPluginMetricsSnapshotJSONUsesEmptyInstanceArray(t *testing.T) {
	raw, err := json.Marshal(NewPluginMetrics().Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"instances":null`) || !strings.Contains(string(raw), `"instances":[]`) {
		t.Fatalf("snapshot json = %s", raw)
	}
}

func TestPluginMetricsConcurrentRecordAndSnapshot(t *testing.T) {
	metrics := NewPluginMetrics()
	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				metrics.QueueEnter("echo-1")
				metrics.QueueLeave("echo-1")
				metrics.Start("echo-1")
				metrics.Finish("echo-1", "", "", true, false)
				metrics.Host("echo-1", "CAPABILITY_DENIED", "")
				_ = metrics.Snapshot()
			}
		}()
	}
	wg.Wait()
	snapshot := metrics.Snapshot()
	if snapshot.Calls != 800 || snapshot.HostCalls != 800 || snapshot.InFlight != 0 || snapshot.QueueWaiters != 0 || snapshot.CapabilityDenied != 800 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}

func TestNilPluginMetricsIsNoop(t *testing.T) {
	var metrics *PluginMetrics
	metrics.QueueEnter("echo-1")
	metrics.QueueLeave("echo-1")
	metrics.Start("echo-1")
	metrics.Finish("echo-1", "MODULE_TRAP", "trace", true, true)
	metrics.Host("echo-1", "CAPABILITY_DENIED", "trace")
	if got := metrics.Snapshot(); len(got.Instances) != 0 || got.Calls != 0 {
		t.Fatalf("nil snapshot = %#v", got)
	}
}
