package observability

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	defaultMessageEventCapacity = 1024
	defaultRecentTraceCapacity  = 50
	maxTraceSpans               = 128
	maxSpanAttributes           = 16
	maxSpanAttributeKeyRunes    = 64
	maxSpanAttributeValueRunes  = 128
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
	MessageID           string `json:"messageId,omitempty"`
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

type TraceSpan struct {
	SpanID          string            `json:"spanId"`
	ParentSpanID    string            `json:"parentSpanId,omitempty"`
	Operation       string            `json:"operation"`
	Category        string            `json:"category"`
	Status          string            `json:"status"`
	StartedAtUnixMS int64             `json:"startedAtUnixMs"`
	EndedAtUnixMS   int64             `json:"endedAtUnixMs,omitempty"`
	DurationMS      uint64            `json:"durationMs"`
	Attributes      map[string]string `json:"attributes"`
}

type MessageTraceDetail struct {
	TraceID          string      `json:"traceId"`
	MessageID        string      `json:"messageId,omitempty"`
	ConversationID   string      `json:"conversationId"`
	TurnID           string      `json:"turnId,omitempty"`
	Source           string      `json:"source"`
	Status           string      `json:"status"`
	StartedAtUnixMS  int64       `json:"startedAtUnixMs"`
	EndedAtUnixMS    int64       `json:"endedAtUnixMs,omitempty"`
	DurationMS       uint64      `json:"durationMs"`
	DroppedSpanCount uint64      `json:"droppedSpanCount"`
	Truncated        bool        `json:"truncated"`
	Spans            []TraceSpan `json:"spans"`
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
	messageSpanStart
	messageSpanFinish
)

type messageEvent struct {
	kind          messageEventKind
	at            time.Time
	traceID       string
	traceIDs      []string
	targetTraceID string
	source        string
	conversation  string
	messageID     string
	turnID        string
	action        string
	stage         string
	status        string
	spanID        string
	parentSpanID  string
	operation     string
	category      string
	attributes    map[string]string
}

type messageTraceState struct {
	trace      MessageTrace
	receivedAt time.Time
	decisionAt time.Time
	turnAt     time.Time
	beatAt     time.Time
	terminal   bool
	sent       bool
	rootSpanID string
	turnSpanID string
	stageSpan  string
	spans      map[string]*TraceSpan
	spanOrder  []string
	dropped    uint64
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
	retentionDrops atomic.Uint64
	sequence       atomic.Uint64
	ownerID        uint64
	spanSequence   atomic.Uint64
	recentCapacity int
	activeCapacity int
	snapshot       atomic.Value
	details        atomic.Value
	sinkMu         sync.RWMutex
	terminalSink   func(MessageTraceDetail) bool
}

var messageMetricsOwnerSequence atomic.Uint64

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
		// Keep active retention independent from the recent terminal history.
		// A lost terminal event cannot consume more than one queue's worth of
		// process-local trace state.
		activeCapacity: queueCapacity,
		ownerID:        messageMetricsOwnerSequence.Add(1),
	}
	m.snapshot.Store(MessageMetricsSnapshot{Recent: []MessageTrace{}})
	m.details.Store(map[string]MessageTraceDetail{})
	if start {
		go m.run()
	}
	return m
}

func (m *MessageMetrics) Begin(source, conversationID string) string {
	return m.BeginCorrelated(source, conversationID, "")
}

func (m *MessageMetrics) BeginCorrelated(source, conversationID, messageID string) string {
	if m == nil {
		return ""
	}
	traceID := fmt.Sprintf("msg-%d-%d-%d", time.Now().UnixNano(), m.ownerID, m.sequence.Add(1))
	correlationID := messageID
	if !ValidCorrelationID(correlationID) {
		correlationID = ""
	}
	m.submit(messageEvent{kind: messageBegin, at: time.Now(), traceID: traceID, source: source, conversation: conversationID, messageID: correlationID})
	return traceID
}

// ValidCorrelationID reports whether value can be preserved exactly as an
// external message-to-trace correlation identifier.
func ValidCorrelationID(value string) bool {
	if value == "" || !utf8.ValidString(value) || strings.TrimSpace(value) != value || utf8.RuneCountInString(value) > 128 {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func (m *MessageMetrics) SetTerminalSink(sink func(MessageTraceDetail) bool) {
	if m == nil {
		return
	}
	m.sinkMu.Lock()
	m.terminalSink = sink
	m.sinkMu.Unlock()
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

func (m *MessageMetrics) StartSpan(traceID, parentSpanID, operation, category string, attributes map[string]string) string {
	if m == nil || traceID == "" || operation == "" {
		return ""
	}
	spanID := fmt.Sprintf("span-%d", m.spanSequence.Add(1))
	m.submit(messageEvent{
		kind: messageSpanStart, at: time.Now(), traceID: traceID, spanID: spanID,
		parentSpanID: parentSpanID, operation: operation, category: category,
		attributes: cloneStringMap(attributes),
	})
	return spanID
}

func (m *MessageMetrics) FinishSpan(spanID, status string, attributes map[string]string) {
	if m == nil || spanID == "" {
		return
	}
	m.submit(messageEvent{
		kind: messageSpanFinish, at: time.Now(), spanID: spanID, status: status,
		attributes: cloneStringMap(attributes),
	})
}

func (m *MessageMetrics) Trace(traceID string) (MessageTraceDetail, bool) {
	if m == nil || traceID == "" {
		return MessageTraceDetail{}, false
	}
	details := m.details.Load().(map[string]MessageTraceDetail)
	detail, ok := details[traceID]
	if !ok {
		return MessageTraceDetail{}, false
	}
	return cloneTraceDetail(detail), true
}

func (m *MessageMetrics) TracesByMessageID(messageID string, limit int) []MessageTraceDetail {
	if m == nil || messageID == "" || limit < 1 {
		return []MessageTraceDetail{}
	}
	details := m.details.Load().(map[string]MessageTraceDetail)
	result := make([]MessageTraceDetail, 0, min(limit, len(details)))
	for _, detail := range details {
		if detail.MessageID == messageID {
			result = append(result, cloneTraceDetail(detail))
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].StartedAtUnixMS != result[j].StartedAtUnixMS {
			return result[i].StartedAtUnixMS > result[j].StartedAtUnixMS
		}
		return result[i].TraceID > result[j].TraceID
	})
	if len(result) > limit {
		result = result[:limit]
	}
	return result
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
	snapshot.DroppedEvents = m.totalDropped()
	snapshot.Recent = append([]MessageTrace{}, snapshot.Recent...)
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

func (m *MessageMetrics) totalDropped() uint64 {
	if m == nil {
		return 0
	}
	return m.dropped.Load() + m.retentionDrops.Load()
}

func (m *MessageMetrics) run() {
	defer close(m.done)
	state := messageMetricsState{
		traces: make(map[string]*messageTraceState), turns: make(map[string]string),
		spanTraces:     make(map[string]string),
		recentCapacity: m.recentCapacity, activeCapacity: m.activeCapacity,
		retentionDrops: &m.retentionDrops,
	}
	for {
		select {
		case event := <-m.events:
			state.apply(event)
			m.persistTerminal(state.drainTerminalDetails())
			m.publishSnapshots(&state)
		case <-m.stop:
			for {
				select {
				case event := <-m.events:
					state.apply(event)
					m.persistTerminal(state.drainTerminalDetails())
				default:
					m.publishSnapshots(&state)
					return
				}
			}
		}
	}
}

func (m *MessageMetrics) persistTerminal(details []MessageTraceDetail) {
	m.sinkMu.RLock()
	sink := m.terminalSink
	m.sinkMu.RUnlock()
	if sink == nil {
		return
	}
	for _, detail := range details {
		sink(detail)
	}
}

func (m *MessageMetrics) publishSnapshots(state *messageMetricsState) {
	snapshot := state.snapshot(m.totalDropped())
	m.snapshot.Store(snapshot)
	m.details.Store(state.traceDetails(snapshot.Recent))
}

type messageMetricsState struct {
	snapshotBase    MessageMetricsSnapshot
	traces          map[string]*messageTraceState
	turns           map[string]string
	spanTraces      map[string]string
	recentIDs       []string
	recentCapacity  int
	activeCapacity  int
	retentionDrops  *atomic.Uint64
	terminalDetails []MessageTraceDetail
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
	case messageSpanStart:
		s.startSpan(event)
	case messageSpanFinish:
		s.finishSpan(event)
	}
}

func (s *messageMetricsState) begin(event messageEvent) {
	if event.traceID == "" || s.traces[event.traceID] != nil {
		return
	}
	if s.snapshotBase.Active >= uint64(s.activeCapacity) {
		if !s.evictOldestActive() {
			return
		}
	}
	trace := &messageTraceState{
		trace: MessageTrace{
			TraceID: event.traceID, MessageID: event.messageID, Source: event.source, ConversationID: event.conversation,
			Status: "received", ReceivedAtUnixMS: event.at.UnixMilli(),
		},
		receivedAt: event.at,
		spans:      make(map[string]*TraceSpan),
	}
	trace.rootSpanID = event.traceID + "-root"
	trace.addSpan(TraceSpan{
		SpanID: trace.rootSpanID, Operation: "消息处理", Category: "message", Status: "running",
		StartedAtUnixMS: event.at.UnixMilli(), Attributes: map[string]string{"source": normalizeSource(event.source)},
	})
	participationID := event.traceID + "-participation"
	trace.addSpan(TraceSpan{
		SpanID: participationID, ParentSpanID: trace.rootSpanID, Operation: "参与判断", Category: "participation", Status: "running",
		StartedAtUnixMS: event.at.UnixMilli(), Attributes: map[string]string{},
	})
	s.traces[event.traceID] = trace
	s.indexTraceSpans(event.traceID, trace)
	s.snapshotBase.Received++
	s.snapshotBase.Active++
	if event.source == "ambient" {
		s.snapshotBase.AmbientReceived++
	} else {
		s.snapshotBase.DirectReceived++
	}
}

func (s *messageMetricsState) evictOldestActive() bool {
	oldestID := ""
	var oldestAt time.Time
	for traceID, trace := range s.traces {
		if trace == nil || trace.terminal {
			continue
		}
		if oldestID == "" || trace.receivedAt.Before(oldestAt) || (trace.receivedAt.Equal(oldestAt) && traceID < oldestID) {
			oldestID = traceID
			oldestAt = trace.receivedAt
		}
	}
	if oldestID == "" {
		return false
	}
	trace := s.traces[oldestID]
	if trace.trace.TurnID != "" {
		delete(s.turns, trace.trace.TurnID)
	}
	s.removeTrace(oldestID)
	if s.snapshotBase.Active > 0 {
		s.snapshotBase.Active--
	}
	if s.retentionDrops != nil {
		s.retentionDrops.Add(1)
	}
	return true
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
			spanStatus := "completed"
			attributes := map[string]string{"action": event.action}
			if event.action == "silent_error" {
				spanStatus = "failed"
				attributes = map[string]string{"action": "silent"}
			}
			trace.closeSpan(traceID+"-participation", spanStatus, event.at, attributes)
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
		case "silent_error":
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
	trace.closeSpan(event.traceID+"-participation", "completed", event.at, map[string]string{"action": "turn_started"})
	trace.turnSpanID = event.traceID + "-turn"
	trace.addSpan(TraceSpan{
		SpanID: trace.turnSpanID, ParentSpanID: trace.rootSpanID, Operation: "Turn 执行", Category: "turn", Status: "running",
		StartedAtUnixMS: event.at.UnixMilli(), Attributes: map[string]string{},
	})
	s.spanTraces[trace.turnSpanID] = event.traceID
	observeMessageLatency(&s.snapshotBase.Latencies.ReceiveToTurn, event.at.Sub(trace.receivedAt))
}

func (s *messageMetricsState) turnStage(event messageEvent) {
	traceID := s.turns[event.turnID]
	trace := s.traces[traceID]
	if trace == nil || trace.terminal {
		return
	}
	if strings.HasPrefix(event.stage, "lifecycle:") {
		stage := strings.TrimPrefix(event.stage, "lifecycle:")
		trace.transitionStage(traceID, stage, event.at, s.spanTraces)
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
	trace.trace.TotalDurationMS = unixMillisecondsBetween(trace.trace.ReceivedAtUnixMS, trace.trace.CompletedAtUnixMS)
	spanStatus := normalizeTerminalSpanStatus(status)
	trace.closeAllOpenSpans(spanStatus, at)
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
	s.terminalDetails = append(s.terminalDetails, s.detailForTrace(traceID))
	s.recentIDs = append(s.recentIDs, traceID)
	for len(s.recentIDs) > s.recentCapacity {
		oldest := s.recentIDs[0]
		s.recentIDs = s.recentIDs[1:]
		s.removeTrace(oldest)
	}
}

func (s *messageMetricsState) drainTerminalDetails() []MessageTraceDetail {
	details := s.terminalDetails
	s.terminalDetails = nil
	return details
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

func unixMillisecondsBetween(start, end int64) uint64 {
	if end <= start {
		return 0
	}
	return uint64(end - start)
}
