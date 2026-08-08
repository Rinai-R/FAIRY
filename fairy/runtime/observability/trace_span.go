package observability

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

var allowedSpanCategories = map[string]struct{}{
	"message": {}, "participation": {}, "turn": {}, "lifecycle": {}, "context": {},
	"model": {}, "tool": {}, "compile": {}, "delivery": {},
}

var allowedSpanAttributeKeys = map[string]struct{}{
	"source": {}, "action": {}, "stage": {}, "attempt": {}, "model": {}, "cacheMode": {},
	"tool": {}, "status": {}, "itemCount": {}, "resultCount": {}, "errorCode": {},
	"chainCount": {}, "callIndex": {}, "outputKind": {}, "lane": {},
	"inputTokens": {}, "outputTokens": {}, "cachedInputTokens": {}, "cacheWriteTokens": {},
}

func (s *messageMetricsState) startSpan(event messageEvent) {
	s.ensureSpanIndex()
	trace := s.traces[event.traceID]
	if trace == nil || trace.terminal || event.spanID == "" {
		return
	}
	if len(trace.spans) >= maxTraceSpans {
		trace.dropped++
		return
	}
	parentSpanID := event.parentSpanID
	if parentSpanID == "" || trace.spans[parentSpanID] == nil {
		parentSpanID = trace.stageSpan
		if parentSpanID == "" {
			parentSpanID = trace.turnSpanID
		}
		if parentSpanID == "" {
			parentSpanID = trace.rootSpanID
		}
	}
	operation, _ := truncateRunes(strings.TrimSpace(event.operation), maxSpanAttributeValueRunes)
	if operation == "" {
		trace.dropped++
		return
	}
	category := normalizeSpanCategory(event.category)
	span := TraceSpan{
		SpanID: event.spanID, ParentSpanID: parentSpanID, Operation: operation, Category: category,
		Status: "running", StartedAtUnixMS: event.at.UnixMilli(), Attributes: normalizeSpanAttributes(event.attributes),
	}
	if !trace.addSpan(span) {
		trace.dropped++
		return
	}
	s.spanTraces[event.spanID] = event.traceID
}

func (s *messageMetricsState) finishSpan(event messageEvent) {
	traceID := s.spanTraces[event.spanID]
	trace := s.traces[traceID]
	if trace == nil || trace.terminal {
		return
	}
	trace.closeSpan(event.spanID, normalizeSpanStatus(event.status), event.at, event.attributes)
}

func (s *messageMetricsState) indexTraceSpans(traceID string, trace *messageTraceState) {
	s.ensureSpanIndex()
	for spanID := range trace.spans {
		s.spanTraces[spanID] = traceID
	}
}

func (s *messageMetricsState) ensureSpanIndex() {
	if s.spanTraces == nil {
		s.spanTraces = make(map[string]string)
	}
}

func (s *messageMetricsState) removeTrace(traceID string) {
	trace := s.traces[traceID]
	if trace != nil {
		for spanID := range trace.spans {
			delete(s.spanTraces, spanID)
		}
	}
	delete(s.traces, traceID)
}

func (s *messageMetricsState) traceDetails(recent []MessageTrace) map[string]MessageTraceDetail {
	details := make(map[string]MessageTraceDetail, len(recent))
	for _, summary := range recent {
		detail := s.detailForTrace(summary.TraceID)
		if detail.TraceID == "" {
			continue
		}
		details[summary.TraceID] = detail
	}
	return details
}

func (s *messageMetricsState) detailForTrace(traceID string) MessageTraceDetail {
	trace := s.traces[traceID]
	if trace == nil {
		return MessageTraceDetail{}
	}
	spans := make([]TraceSpan, 0, len(trace.spanOrder))
	for _, spanID := range trace.spanOrder {
		span := trace.spans[spanID]
		if span != nil {
			spans = append(spans, cloneTraceSpan(*span))
		}
	}
	summary := trace.trace
	return MessageTraceDetail{
		TraceID: summary.TraceID, MessageID: summary.MessageID, ConversationID: summary.ConversationID, TurnID: summary.TurnID,
		Source: summary.Source, Status: summary.Status, StartedAtUnixMS: summary.ReceivedAtUnixMS,
		EndedAtUnixMS: summary.CompletedAtUnixMS, DurationMS: summary.TotalDurationMS,
		DroppedSpanCount: trace.dropped, Truncated: trace.dropped > 0, Spans: spans,
	}
}

func (t *messageTraceState) addSpan(span TraceSpan) bool {
	if t == nil || span.SpanID == "" || t.spans[span.SpanID] != nil || len(t.spans) >= maxTraceSpans {
		return false
	}
	span.Attributes = normalizeSpanAttributes(span.Attributes)
	t.spans[span.SpanID] = &span
	t.spanOrder = append(t.spanOrder, span.SpanID)
	return true
}

func (t *messageTraceState) closeSpan(spanID, status string, at time.Time, attributes map[string]string) {
	if t == nil {
		return
	}
	span := t.spans[spanID]
	if span == nil || span.EndedAtUnixMS != 0 {
		return
	}
	endedAt := at.UnixMilli()
	if endedAt < span.StartedAtUnixMS {
		endedAt = span.StartedAtUnixMS
	}
	span.Status = normalizeSpanStatus(status)
	span.EndedAtUnixMS = endedAt
	span.DurationMS = uint64(endedAt - span.StartedAtUnixMS)
	mergeSpanAttributes(span.Attributes, attributes)
}

func (t *messageTraceState) closeAllOpenSpans(status string, at time.Time) {
	if t == nil {
		return
	}
	for index := len(t.spanOrder) - 1; index >= 0; index-- {
		t.closeSpan(t.spanOrder[index], status, at, nil)
	}
	t.stageSpan = ""
}

func (t *messageTraceState) transitionStage(traceID, stage string, at time.Time, spanTraces map[string]string) {
	if t == nil || stage == "" {
		return
	}
	if t.stageSpan != "" {
		t.closeSpan(t.stageSpan, "completed", at, nil)
	}
	if len(t.spans) >= maxTraceSpans {
		t.dropped++
		t.stageSpan = ""
		return
	}
	spanID := fmt.Sprintf("%s-stage-%d", traceID, len(t.spanOrder)+1)
	parent := t.turnSpanID
	if parent == "" {
		parent = t.rootSpanID
	}
	if t.addSpan(TraceSpan{
		SpanID: spanID, ParentSpanID: parent, Operation: lifecycleStageLabel(stage), Category: "lifecycle",
		Status: "running", StartedAtUnixMS: at.UnixMilli(), Attributes: map[string]string{"stage": stage},
	}) {
		t.stageSpan = spanID
		spanTraces[spanID] = traceID
	}
}

func normalizeSpanCategory(category string) string {
	category = strings.TrimSpace(strings.ToLower(category))
	if _, ok := allowedSpanCategories[category]; ok {
		return category
	}
	return "turn"
}

func normalizeSpanStatus(status string) string {
	switch strings.TrimSpace(strings.ToLower(status)) {
	case "completed", "failed", "interrupted", "silent", "running":
		return strings.TrimSpace(strings.ToLower(status))
	default:
		return "completed"
	}
}

func normalizeTerminalSpanStatus(status string) string {
	switch status {
	case "failed":
		return "failed"
	case "interrupted":
		return "interrupted"
	case "silent":
		return "silent"
	default:
		return "completed"
	}
}

func normalizeSource(source string) string {
	if source == "ambient" {
		return "ambient"
	}
	return "direct"
}

func lifecycleStageLabel(stage string) string {
	switch stage {
	case "interpreting":
		return "理解输入"
	case "gathering":
		return "收集上下文"
	case "planning":
		return "规划回复"
	case "responding":
		return "交付回复"
	default:
		return stage
	}
}

func normalizeSpanAttributes(attributes map[string]string) map[string]string {
	if len(attributes) == 0 {
		return map[string]string{}
	}
	keys := make([]string, 0, len(attributes))
	for key := range attributes {
		if _, ok := allowedSpanAttributeKeys[key]; ok {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	if len(keys) > maxSpanAttributes {
		keys = keys[:maxSpanAttributes]
	}
	result := make(map[string]string, len(keys))
	for _, key := range keys {
		normalizedKey, _ := truncateRunes(key, maxSpanAttributeKeyRunes)
		normalizedValue, _ := truncateRunes(strings.TrimSpace(attributes[key]), maxSpanAttributeValueRunes)
		if normalizedKey != "" && normalizedValue != "" {
			result[normalizedKey] = normalizedValue
		}
	}
	return result
}

func mergeSpanAttributes(target map[string]string, additions map[string]string) {
	if target == nil {
		return
	}
	for key, value := range normalizeSpanAttributes(additions) {
		if len(target) >= maxSpanAttributes {
			return
		}
		target[key] = value
	}
}

func cloneStringMap(value map[string]string) map[string]string {
	if len(value) == 0 {
		return nil
	}
	clone := make(map[string]string, len(value))
	for key, item := range value {
		clone[key] = item
	}
	return clone
}

func cloneTraceSpan(span TraceSpan) TraceSpan {
	span.Attributes = cloneStringMap(span.Attributes)
	if span.Attributes == nil {
		span.Attributes = map[string]string{}
	}
	return span
}

func cloneTraceDetail(detail MessageTraceDetail) MessageTraceDetail {
	detail.Spans = append([]TraceSpan(nil), detail.Spans...)
	for index := range detail.Spans {
		detail.Spans[index] = cloneTraceSpan(detail.Spans[index])
	}
	return detail
}
