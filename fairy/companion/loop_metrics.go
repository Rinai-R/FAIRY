package companion

import (
	"sync"
	"time"
)

type LatencyMetrics struct {
	Observations    uint64 `json:"observations"`
	TotalDurationMS uint64 `json:"totalDurationMs"`
	MaxDurationMS   uint64 `json:"maxDurationMs"`
}

type AgentLoopMetricsSnapshot struct {
	ProviderFirstByte LatencyMetrics `json:"providerFirstByte"`
	ReplyPreview      LatencyMetrics `json:"replyPreview"`
	FirstBeat         LatencyMetrics `json:"firstBeat"`
	Completed         LatencyMetrics `json:"completed"`
}

type agentLoopMetrics struct {
	mu       sync.Mutex
	snapshot AgentLoopMetricsSnapshot
}

func (m *agentLoopMetrics) observe(target *LatencyMetrics, duration time.Duration) {
	if duration < 0 {
		duration = 0
	}
	value := uint64(duration.Milliseconds())
	m.mu.Lock()
	defer m.mu.Unlock()
	target.Observations++
	target.TotalDurationMS += value
	if value > target.MaxDurationMS {
		target.MaxDurationMS = value
	}
}

func (m *agentLoopMetrics) providerFirstByte(duration time.Duration) {
	m.observe(&m.snapshot.ProviderFirstByte, duration)
}

func (m *agentLoopMetrics) replyPreview(duration time.Duration) {
	m.observe(&m.snapshot.ReplyPreview, duration)
}

func (m *agentLoopMetrics) firstBeat(duration time.Duration) {
	m.observe(&m.snapshot.FirstBeat, duration)
}

func (m *agentLoopMetrics) completed(duration time.Duration) {
	m.observe(&m.snapshot.Completed, duration)
}

func (m *agentLoopMetrics) Snapshot() AgentLoopMetricsSnapshot {
	if m == nil {
		return AgentLoopMetricsSnapshot{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.snapshot
}

func (s *CompanionService) AgentLoopMetrics() AgentLoopMetricsSnapshot {
	if s == nil {
		return AgentLoopMetricsSnapshot{}
	}
	return s.loopMetrics.Snapshot()
}
