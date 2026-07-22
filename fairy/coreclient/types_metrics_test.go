package coreclient

import (
	"encoding/json"
	"testing"
)

func TestRuntimeMetricsDecodesAgentLoopLatency(t *testing.T) {
	var metrics RuntimeMetrics
	if err := json.Unmarshal([]byte(`{"activeBackgroundJobs":0,"eventSubscribers":1,"agentLoop":{"providerFirstByte":{"observations":2,"totalDurationMs":30,"maxDurationMs":20},"replyPreview":{"observations":1,"totalDurationMs":25,"maxDurationMs":25},"firstBeat":{"observations":1,"totalDurationMs":40,"maxDurationMs":40},"completed":{"observations":1,"totalDurationMs":45,"maxDurationMs":45}}}`), &metrics); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if metrics.AgentLoop.ProviderFirstByte.Observations != 2 || metrics.AgentLoop.Completed.MaxDurationMS != 45 {
		t.Fatalf("agent loop metrics = %#v", metrics.AgentLoop)
	}
}
