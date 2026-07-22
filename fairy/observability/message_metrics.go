package observability

import (
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

const (
	defaultMessageEventCapacity = 1024
	defaultRecentTraceCapacity  = 50
)

type MessageLatencyMetrics struct {
	Observations    uint64 `json:"observations"`
	TotalDurationMS uint64 `json:"totalDurationMs"`
	MaxDurationMS   uint64 `json:"maxDurationMs"`
}

type MessageLatencySnapshot struct {
	ReceiveToDecision  MessageLatencyMetrics `json:"receiveToDecision"`
	ReceiveToTurn      MessageLatencyMetrics `json:"receiveToTurn"`
	TurnToFirstBeat    MessageLatencyMetrics `json:"turnToFirstBeat"`
	TurnToCompleted    MessageLatencyMetrics `json:"turnToCompleted"`
	ReceiveToFirstBeat MessageLatencyMetrics `json:"receiveToFirstBeat"`
	ReceiveToCompleted MessageLatencyMetrics `json:"receiveToCompleted"`
}

type MessageTrace struct {
	TraceID             string `json:"traceId"`
	Source              string `json:"source"`
	ConversationID      string `json:"conversationId"`
	TurnID              string `json:"turnId,omitempty"`
	Status              string `json:"status"`
	ReceivedAtUnixMS    int64  `json:"receivedAtUnixMs"`
	DecisionAtUnixMS    int64  `json:"decisionAtUnixMs,omitempty"`
	TurnStartedAtUnixMS int64  `json:"turnStartedAtUnixMs,omitempty"`
	FirstBeatAtUnixMS   int64  `json:"firstBeatAtUnixMs,omitempty"`
	CompletedAtUnixMS   int64  `json:"completedAtUnixMs,omitempty"`
	TotalDurationMS     uint64 `json:"totalDurationMs,omitempty"`
}

type MessageMetricsSnapshot struct {
	Received        uint64                 `json:"received"`
	Sent            uint64                 `json:"sent"`
	DirectReceived  uint64                 `json:"directReceived"`
	AmbientReceived uint64                 `json:"ambientReceived"`
	Completed       uint64                 `json:"completed"`
	Failed          uint64                 `json:"failed"`
	Interrupted     uint64                 `json:"interrupted"`
	Silent          uint64                 `json:"silent"`
	Active          uint64                 `json:"active"`
	DroppedEvents   uint64                 `json:"droppedEvents"`
	Latencies       MessageLatencySnapshot `json:"latencies"`
	Recent          []MessageTrace         `json:"recent"`
}

type messageEventKind uint8

const (
	messageBegin messageEventKind = iota + 1
	messageParticipation
	messageTurnStarted
	messageTurnStage
	messageEnd
)

type messageEvent struct {
	kind          messageEventKind
	at            time.Time
	traceID       string
	traceIDs      []string
	targetTraceID string
	source        string
	conversation  string
	turnID        string
	action        string
	stage         string
	status        string
}

type messageTraceState struct {
	trace      MessageTrace
	receivedAt time.Time
	decisionAt time.Time
	turnAt     time.Time
	beatAt     time.Time
	terminal   bool
	sent       bool
}

// MessageMetrics asynchronously aggregates message throughput and trace timing.
// Producers never wait for the owner goroutine; queue pressure is observable as
// DroppedEvents and never changes the business result.
type MessageMetrics struct {
	events         chan messageEvent
	stop           chan struct{}
	done           chan struct{}
	closeOnce      sync.Once
	stopped        atomic.Bool
	dropped        atomic.Uint64
	sequence       atomic.Uint64
	recentCapacity int
	snapshot       atomic.Value
}

func NewMessageMetrics() *MessageMetrics {
	return newMessageMetrics(defaultMessageEventCapacity, defaultRecentTraceCapacity, true)
}

func newMessageMetrics(queueCapacity, recentCapacity int, start bool) *MessageMetrics {
	if queueCapacity < 1 {
		queueCapacity = 1
	}
	if recentCapacity < 1 {
		recentCapacity = 1
	}
	m := &MessageMetrics{
		events: make(chan messageEvent, queueCapacity), stop: make(chan struct{}), done: make(chan struct{}),
		recentCapacity: recentCapacity,
	}
	m.snapshot.Store(MessageMetricsSnapshot{Recent: []MessageTrace{}})
	if start {
		go m.run()
	}
	return m
}

func (m *MessageMetrics) Begin(source, conversationID string) string {
	if m == nil {
		return ""
	}
	traceID := fmt.Sprintf("msg-%d", m.sequence.Add(1))
	m.submit(messageEvent{kind: messageBegin, at: time.Now(), traceID: traceID, source: source, conversation: conversationID})
	return traceID
}

func (m *MessageMetrics) Participation(traceIDs []string, targetTraceID, action string) {
	if m == nil || len(traceIDs) == 0 {
		return
	}
	ids := append([]string(nil), traceIDs...)
	m.submit(messageEvent{kind: messageParticipation, at: time.Now(), traceIDs: ids, targetTraceID: targetTraceID, action: action})
}

func (m *MessageMetrics) TurnStarted(traceID, conversationID, turnID string) {
	if m == nil || traceID == "" {
		return
	}
	m.submit(messageEvent{kind: messageTurnStarted, at: time.Now(), traceID: traceID, conversation: conversationID, turnID: turnID})
}

func (m *MessageMetrics) TurnStage(conversationID, turnID, stage string) {
	if m == nil || turnID == "" {
		return
	}
	m.submit(messageEvent{kind: messageTurnStage, at: time.Now(), conversation: conversationID, turnID: turnID, stage: stage})
}

func (m *MessageMetrics) End(traceID, status string) {
	if m == nil || traceID == "" {
		return
	}
	m.submit(messageEvent{kind: messageEnd, at: time.Now(), traceID: traceID, status: status})
}

func (m *MessageMetrics) Snapshot() MessageMetricsSnapshot {
	if m == nil {
		return MessageMetricsSnapshot{Recent: []MessageTrace{}}
	}
	snapshot := m.snapshot.Load().(MessageMetricsSnapshot)
	snapshot.DroppedEvents = m.dropped.Load()
	snapshot.Recent = append([]MessageTrace(nil), snapshot.Recent...)
	return snapshot
}

func (m *MessageMetrics) Close() {
	if m == nil {
		return
	}
	m.closeOnce.Do(func() {
		m.stopped.Store(true)
		close(m.stop)
		<-m.done
	})
}

func (m *MessageMetrics) submit(event messageEvent) {
	if m.stopped.Load() {
		m.dropped.Add(1)
		return
	}
	select {
	case m.events <- event:
	default:
		m.dropped.Add(1)
	}
}

func (m *MessageMetrics) run() {
	defer close(m.done)
	state := messageMetricsState{
		traces: make(map[string]*messageTraceState), turns: make(map[string]string),
		recentCapacity: m.recentCapacity,
	}
	for {
		select {
		case event := <-m.events:
			state.apply(event)
			m.snapshot.Store(state.snapshot(m.dropped.Load()))
		case <-m.stop:
			for {
				select {
				case event := <-m.events:
					state.apply(event)
				default:
					m.snapshot.Store(state.snapshot(m.dropped.Load()))
					return
				}
			}
		}
	}
}

type messageMetricsState struct {
	snapshotBase   MessageMetricsSnapshot
	traces         map[string]*messageTraceState
	turns          map[string]string
	recentIDs      []string
	recentCapacity int
}

func (s *messageMetricsState) apply(event messageEvent) {
	switch event.kind {
	case messageBegin:
		s.begin(event)
	case messageParticipation:
		s.participation(event)
	case messageTurnStarted:
		s.turnStarted(event)
	case messageTurnStage:
		s.turnStage(event)
	case messageEnd:
		s.end(event.traceID, event.status, event.at)
	}
}

func (s *messageMetricsState) begin(event messageEvent) {
	if event.traceID == "" || s.traces[event.traceID] != nil {
		return
	}
	trace := &messageTraceState{
		trace: MessageTrace{
			TraceID: event.traceID, Source: event.source, ConversationID: event.conversation,
			Status: "received", ReceivedAtUnixMS: event.at.UnixMilli(),
		},
		receivedAt: event.at,
	}
	s.traces[event.traceID] = trace
	s.snapshotBase.Received++
	s.snapshotBase.Active++
	if event.source == "ambient" {
		s.snapshotBase.AmbientReceived++
	} else {
		s.snapshotBase.DirectReceived++
	}
}

func (s *messageMetricsState) participation(event messageEvent) {
	for _, traceID := range event.traceIDs {
		trace := s.traces[traceID]
		if trace == nil || trace.terminal {
			continue
		}
		if trace.decisionAt.IsZero() {
			trace.decisionAt = event.at
			trace.trace.DecisionAtUnixMS = event.at.UnixMilli()
			observeMessageLatency(&s.snapshotBase.Latencies.ReceiveToDecision, event.at.Sub(trace.receivedAt))
		}
		switch event.action {
		case "wait":
			trace.trace.Status = "waiting"
		case "reply":
			if traceID == event.targetTraceID {
				trace.trace.Status = "selected"
			} else {
				s.end(traceID, "silent", event.at)
			}
		case "silent":
			s.end(traceID, "silent", event.at)
		case "failed":
			s.end(traceID, "failed", event.at)
		}
	}
}

func (s *messageMetricsState) turnStarted(event messageEvent) {
	trace := s.traces[event.traceID]
	if trace == nil || trace.terminal || event.turnID == "" {
		return
	}
	trace.turnAt = event.at
	trace.trace.TurnID = event.turnID
	trace.trace.TurnStartedAtUnixMS = event.at.UnixMilli()
	trace.trace.Status = "running"
	s.turns[event.turnID] = event.traceID
	observeMessageLatency(&s.snapshotBase.Latencies.ReceiveToTurn, event.at.Sub(trace.receivedAt))
}

func (s *messageMetricsState) turnStage(event messageEvent) {
	traceID := s.turns[event.turnID]
	trace := s.traces[traceID]
	if trace == nil || trace.terminal {
		return
	}
	switch event.stage {
	case "first_beat":
		if trace.beatAt.IsZero() {
			trace.beatAt = event.at
			trace.trace.FirstBeatAtUnixMS = event.at.UnixMilli()
			observeMessageLatency(&s.snapshotBase.Latencies.TurnToFirstBeat, event.at.Sub(trace.turnAt))
			observeMessageLatency(&s.snapshotBase.Latencies.ReceiveToFirstBeat, event.at.Sub(trace.receivedAt))
		}
		if !trace.sent {
			trace.sent = true
			s.snapshotBase.Sent++
		}
	case "completed", "failed", "interrupted":
		s.end(traceID, event.stage, event.at)
	}
}

func (s *messageMetricsState) end(traceID, status string, at time.Time) {
	trace := s.traces[traceID]
	if trace == nil || trace.terminal {
		return
	}
	trace.terminal = true
	trace.trace.Status = status
	trace.trace.CompletedAtUnixMS = at.UnixMilli()
	trace.trace.TotalDurationMS = durationMS(at.Sub(trace.receivedAt))
	if s.snapshotBase.Active > 0 {
		s.snapshotBase.Active--
	}
	switch status {
	case "completed":
		s.snapshotBase.Completed++
		if !trace.turnAt.IsZero() {
			observeMessageLatency(&s.snapshotBase.Latencies.TurnToCompleted, at.Sub(trace.turnAt))
		}
		observeMessageLatency(&s.snapshotBase.Latencies.ReceiveToCompleted, at.Sub(trace.receivedAt))
	case "failed":
		s.snapshotBase.Failed++
	case "interrupted":
		s.snapshotBase.Interrupted++
	case "silent":
		s.snapshotBase.Silent++
	}
	if trace.trace.TurnID != "" {
		delete(s.turns, trace.trace.TurnID)
	}
	s.recentIDs = append(s.recentIDs, traceID)
	for len(s.recentIDs) > s.recentCapacity {
		oldest := s.recentIDs[0]
		s.recentIDs = s.recentIDs[1:]
		delete(s.traces, oldest)
	}
}

func (s *messageMetricsState) snapshot(dropped uint64) MessageMetricsSnapshot {
	snapshot := s.snapshotBase
	snapshot.DroppedEvents = dropped
	snapshot.Recent = make([]MessageTrace, 0, min(len(s.traces), s.recentCapacity))
	for _, trace := range s.traces {
		snapshot.Recent = append(snapshot.Recent, trace.trace)
	}
	sort.Slice(snapshot.Recent, func(i, j int) bool {
		if snapshot.Recent[i].ReceivedAtUnixMS != snapshot.Recent[j].ReceivedAtUnixMS {
			return snapshot.Recent[i].ReceivedAtUnixMS > snapshot.Recent[j].ReceivedAtUnixMS
		}
		return snapshot.Recent[i].TraceID > snapshot.Recent[j].TraceID
	})
	if len(snapshot.Recent) > s.recentCapacity {
		snapshot.Recent = snapshot.Recent[:s.recentCapacity]
	}
	return snapshot
}

func observeMessageLatency(metric *MessageLatencyMetrics, duration time.Duration) {
	value := durationMS(duration)
	metric.Observations++
	metric.TotalDurationMS += value
	if value > metric.MaxDurationMS {
		metric.MaxDurationMS = value
	}
}

func durationMS(duration time.Duration) uint64 {
	if duration <= 0 {
		return 0
	}
	return uint64(duration / time.Millisecond)
}
