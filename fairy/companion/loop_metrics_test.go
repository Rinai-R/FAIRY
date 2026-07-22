package companion

import (
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
