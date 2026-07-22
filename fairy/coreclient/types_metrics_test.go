package coreclient

import (
	"encoding/json"
	"testing"
)

func TestMetricsDecodesMessageTraceSnapshot(t *testing.T) {
	var metrics Metrics
	if err := json.Unmarshal([]byte(`{"messages":{"received":3,"sent":1,"directReceived":1,"ambientReceived":2,"completed":1,"failed":0,"interrupted":0,"silent":1,"active":1,"droppedEvents":0,"latencies":{"receiveToDecision":{"observations":2,"totalDurationMs":20,"maxDurationMs":12},"receiveToTurn":{"observations":1,"totalDurationMs":15,"maxDurationMs":15},"turnToFirstBeat":{"observations":1,"totalDurationMs":40,"maxDurationMs":40},"turnToCompleted":{"observations":1,"totalDurationMs":50,"maxDurationMs":50},"receiveToFirstBeat":{"observations":1,"totalDurationMs":55,"maxDurationMs":55},"receiveToCompleted":{"observations":1,"totalDurationMs":65,"maxDurationMs":65}},"recent":[{"traceId":"msg-3","source":"ambient","conversationId":"c1","turnId":"t1","status":"completed","receivedAtUnixMs":1,"totalDurationMs":65}]}}`), &metrics); err != nil {
		t.Fatal(err)
	}
	if metrics.Messages.Received != 3 || metrics.Messages.Sent != 1 || len(metrics.Messages.Recent) != 1 {
		t.Fatalf("message metrics = %#v", metrics.Messages)
	}
	if metrics.Messages.Latencies.ReceiveToCompleted.MaxDurationMS != 65 {
		t.Fatalf("latencies = %#v", metrics.Messages.Latencies)
	}
}

func TestRuntimeMetricsDecodesAgentLoopLatency(t *testing.T) {
	var metrics RuntimeMetrics
	if err := json.Unmarshal([]byte(`{"activeBackgroundJobs":0,"eventSubscribers":1,"agentLoop":{"providerFirstByte":{"observations":2,"totalDurationMs":30,"maxDurationMs":20},"replyPreview":{"observations":1,"totalDurationMs":25,"maxDurationMs":25},"firstBeat":{"observations":1,"totalDurationMs":40,"maxDurationMs":40},"completed":{"observations":1,"totalDurationMs":45,"maxDurationMs":45}}}`), &metrics); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if metrics.AgentLoop.ProviderFirstByte.Observations != 2 || metrics.AgentLoop.Completed.MaxDurationMS != 45 {
		t.Fatalf("agent loop metrics = %#v", metrics.AgentLoop)
	}
}
