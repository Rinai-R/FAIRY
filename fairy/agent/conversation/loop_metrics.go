package conversation

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
	ProviderFirstByte LatencyMetrics    `json:"providerFirstByte"`
	ReplyPreview      LatencyMetrics    `json:"replyPreview"`
	FirstBeat         LatencyMetrics    `json:"firstBeat"`
	Completed         LatencyMetrics    `json:"completed"`
	Compaction        CompactionMetrics `json:"compaction"`
}

type CompactionMetrics struct {
	L1Applied uint64 `json:"l1Applied"`
	L2Applied uint64 `json:"l2Applied"`
	L3Applied uint64 `json:"l3Applied"`
	Failed    uint64 `json:"failed"`
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

func (m *agentLoopMetrics) compactionApplied(layer string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	switch layer {
	case "l1":
		m.snapshot.Compaction.L1Applied++
	case "l2":
		m.snapshot.Compaction.L2Applied++
	case "l3":
		m.snapshot.Compaction.L3Applied++
	}
}

func (m *agentLoopMetrics) compactionFailed() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.snapshot.Compaction.Failed++
}

func (m *agentLoopMetrics) Snapshot() AgentLoopMetricsSnapshot {
	if m == nil {
		return AgentLoopMetricsSnapshot{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.snapshot
}

func (s *Service) AgentLoopMetrics() AgentLoopMetricsSnapshot {
	if s == nil {
		return AgentLoopMetricsSnapshot{}
	}
	return s.loopMetrics.Snapshot()
}
