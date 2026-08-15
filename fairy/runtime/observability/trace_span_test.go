package observability

import (
	"strings"
	"testing"
	"time"
)

func TestMessageTraceDetailBuildsBoundedParentChildSpans(t *testing.T) {
	metrics := NewMessageMetrics()
	t.Cleanup(metrics.Close)
	traceID := metrics.Begin("direct", "conversation")
	metrics.Participation([]string{traceID}, traceID, "reply")
	metrics.TurnStarted(traceID, "conversation", "turn")
	metrics.TurnStage("conversation", "turn", "lifecycle:interpreting")
	modelSpan := metrics.StartSpan(traceID, "", "模型调用", "model", map[string]string{
		"attempt": "1", "model": "demo", "query": "must-not-survive", "status": "running",
	})
	metrics.FinishSpan(modelSpan, "completed", map[string]string{"status": "ok", "prompt": "must-not-survive"})
	metrics.TurnStage("conversation", "turn", "completed")

	detail := waitForTraceDetail(t, metrics, traceID, func(value MessageTraceDetail) bool {
		return value.Status == "completed" && len(value.Spans) >= 5
	})
	if detail.TraceID != traceID || detail.ConversationID != "conversation" || detail.TurnID != "turn" {
		t.Fatalf("detail identity = %#v", detail)
	}
	if detail.EndedAtUnixMS < detail.StartedAtUnixMS || detail.DurationMS != uint64(detail.EndedAtUnixMS-detail.StartedAtUnixMS) {
		t.Fatalf("detail timing = %#v", detail)
	}
	byID := make(map[string]TraceSpan, len(detail.Spans))
	for _, span := range detail.Spans {
		byID[span.SpanID] = span
		if span.Status == "running" || span.EndedAtUnixMS < span.StartedAtUnixMS || span.DurationMS != uint64(span.EndedAtUnixMS-span.StartedAtUnixMS) {
			t.Fatalf("terminal span timing = %#v", span)
		}
	}
	model := byID[modelSpan]
	parent := byID[model.ParentSpanID]
	if model.Category != "model" || parent.Category != "lifecycle" || parent.ParentSpanID != traceID+"-turn" || model.Attributes["attempt"] != "1" || model.Attributes["status"] != "ok" {
		t.Fatalf("model span = %#v", model)
	}
	if _, exists := model.Attributes["query"]; exists {
		t.Fatalf("query leaked into attributes: %#v", model.Attributes)
	}
	if _, exists := model.Attributes["prompt"]; exists {
		t.Fatalf("prompt leaked into attributes: %#v", model.Attributes)
	}

	detail.Spans[0].Attributes["source"] = "mutated"
	again, ok := metrics.Trace(traceID)
	if !ok || again.Spans[0].Attributes["source"] == "mutated" {
		t.Fatal("Trace returned mutable owner state")
	}
}

func TestMessageTraceDetailBoundsSpansAndReportsTruncation(t *testing.T) {
	metrics := NewMessageMetrics()
	t.Cleanup(metrics.Close)
	traceID := metrics.Begin("direct", "conversation")
	for index := 0; index < maxTraceSpans+10; index++ {
		metrics.StartSpan(traceID, "", "调用点", "tool", map[string]string{"callIndex": "1"})
	}
	metrics.End(traceID, "completed")
	detail := waitForTraceDetail(t, metrics, traceID, func(value MessageTraceDetail) bool { return value.Status == "completed" })
	if len(detail.Spans) != maxTraceSpans || !detail.Truncated || detail.DroppedSpanCount == 0 {
		t.Fatalf("bounded detail = spans:%d truncated:%v dropped:%d", len(detail.Spans), detail.Truncated, detail.DroppedSpanCount)
	}
}

func TestMessageTracePersistsBoundedSurfaceDeliveryReceiptBeforeTerminal(t *testing.T) {
	metrics := NewMessageMetrics()
	t.Cleanup(metrics.Close)
	persisted := make(chan MessageTraceDetail, 1)
	metrics.SetTerminalSink(func(detail MessageTraceDetail) bool {
		persisted <- detail
		return true
	})
	traceID := metrics.BeginCorrelated("ambient", "conversation-1", "inbound-45122")
	metrics.Participation([]string{traceID}, traceID, "reply")
	metrics.TurnStarted(traceID, "conversation-1", "turn-1")
	metrics.TurnStage("conversation-1", "turn-1", "lifecycle:responding")
	metrics.SurfaceDelivery("turn-1", "final-0", "succeeded", "45123", "")
	metrics.End(traceID, "completed")

	select {
	case detail := <-persisted:
		var receipt *TraceSpan
		for index := range detail.Spans {
			if detail.Spans[index].Operation == "Surface 回执" {
				receipt = &detail.Spans[index]
				break
			}
		}
		if receipt == nil || receipt.Category != "delivery" || receipt.Status != "completed" || receipt.Attributes["beatId"] != "final-0" || receipt.Attributes["status"] != "succeeded" || receipt.Attributes["externalMessageId"] != "45123" {
			t.Fatalf("surface receipt = %#v, detail = %#v", receipt, detail)
		}
		if receipt.ParentSpanID == "" || receipt.StartedAtUnixMS != receipt.EndedAtUnixMS || receipt.DurationMS != 0 {
			t.Fatalf("surface receipt timing = %#v", receipt)
		}
	case <-time.After(time.Second):
		t.Fatal("terminal trace was not persisted")
	}
}

func TestMessageTraceRejectsInvalidSurfaceDeliveryAttributes(t *testing.T) {
	metrics := NewMessageMetrics()
	t.Cleanup(metrics.Close)
	traceID := metrics.Begin("direct", "conversation-1")
	metrics.Participation([]string{traceID}, traceID, "reply")
	metrics.TurnStarted(traceID, "conversation-1", "turn-1")
	metrics.SurfaceDelivery("turn-1", "final-0", "succeeded", " external-id ", "")
	metrics.SurfaceDelivery("turn-1", "final-1", "failed", "", "provider response body")
	metrics.End(traceID, "failed")

	detail := waitForTraceDetail(t, metrics, traceID, func(value MessageTraceDetail) bool { return value.Status == "failed" })
	for _, span := range detail.Spans {
		if span.Operation == "Surface 回执" {
			t.Fatalf("invalid delivery receipt was accepted: %#v", span)
		}
	}
}

func TestMessageTraceStoresOnlyStableSurfaceDeliveryFailure(t *testing.T) {
	metrics := NewMessageMetrics()
	t.Cleanup(metrics.Close)
	traceID := metrics.Begin("direct", "conversation-1")
	metrics.Participation([]string{traceID}, traceID, "reply")
	metrics.TurnStarted(traceID, "conversation-1", "turn-1")
	metrics.SurfaceDelivery("turn-1", "final-0", "failed", "", "SURFACE_DELIVERY_FAILED")
	metrics.End(traceID, "failed")

	detail := waitForTraceDetail(t, metrics, traceID, func(value MessageTraceDetail) bool { return value.Status == "failed" })
	for _, span := range detail.Spans {
		if span.Operation != "Surface 回执" {
			continue
		}
		if span.Status != "failed" || span.Attributes["errorCode"] != "SURFACE_DELIVERY_FAILED" || span.Attributes["status"] != "failed" {
			t.Fatalf("failed delivery receipt = %#v", span)
		}
		if _, exists := span.Attributes["externalMessageId"]; exists {
			t.Fatalf("failed receipt contains external message ID: %#v", span)
		}
		return
	}
	t.Fatal("failed surface receipt is missing")
}

func TestSpanProducerNeverBlocksOnFullQueue(t *testing.T) {
	metrics := newMessageMetrics(1, 1, false)
	metrics.Begin("direct", "conversation")
	started := time.Now()
	spanID := metrics.StartSpan("msg-1", "", "模型调用", "model", nil)
	metrics.FinishSpan(spanID, "completed", nil)
	if elapsed := time.Since(started); elapsed > 50*time.Millisecond {
		t.Fatalf("span producer blocked for %s", elapsed)
	}
	if metrics.Snapshot().DroppedEvents != 2 {
		t.Fatalf("dropped events = %d, want 2", metrics.Snapshot().DroppedEvents)
	}
}

func TestPluginSpanParentsUnderTurnAndKeepsOnlyDeclaredAttributes(t *testing.T) {
	metrics := NewMessageMetrics()
	t.Cleanup(metrics.Close)
	traceID := metrics.Begin("direct", "conversation")
	metrics.Participation([]string{traceID}, traceID, "reply")
	metrics.TurnStarted(traceID, "conversation", "turn")
	metrics.TurnStage("conversation", "turn", "lifecycle:responding")
	spanID := metrics.StartSpan(traceID, "", "插件调用", "plugin", map[string]string{
		"pluginId": "echo-1", "pluginVersion": "1.0.0", "capability": "handle",
		"attempt": "1", "bytes": "12", "authorization": "Bearer secret", "payload": "must-not-survive",
	})
	metrics.FinishSpan(spanID, "failed", map[string]string{
		"status": "failed", "errorCode": "CAPABILITY_DENIED", "duration": "4", "token": "secret",
	})
	metrics.End(traceID, "failed")
	detail := waitForTraceDetail(t, metrics, traceID, func(value MessageTraceDetail) bool { return value.Status == "failed" })
	var pluginSpan *TraceSpan
	for index := range detail.Spans {
		if detail.Spans[index].Category == "plugin" {
			pluginSpan = &detail.Spans[index]
			break
		}
	}
	if pluginSpan == nil || pluginSpan.ParentSpanID == "" || pluginSpan.Attributes["pluginId"] != "echo-1" || pluginSpan.Attributes["capability"] != "handle" || pluginSpan.Attributes["errorCode"] != "CAPABILITY_DENIED" {
		t.Fatalf("plugin span = %#v", pluginSpan)
	}
	parent := spanByID(detail.Spans, pluginSpan.ParentSpanID)
	if parent.Category != "lifecycle" && parent.Category != "turn" {
		t.Fatalf("plugin parent = %#v", parent)
	}
	for _, leaked := range []string{"authorization", "payload", "token"} {
		if _, exists := pluginSpan.Attributes[leaked]; exists {
			t.Fatalf("plugin span leaked %q: %#v", leaked, pluginSpan.Attributes)
		}
	}
}

func spanByID(spans []TraceSpan, spanID string) TraceSpan {
	for _, span := range spans {
		if span.SpanID == spanID {
			return span
		}
	}
	return TraceSpan{}
}

func TestTraceDetailContainsNoSensitiveAttributeNames(t *testing.T) {
	metrics := NewMessageMetrics()
	t.Cleanup(metrics.Close)
	traceID := metrics.Begin("direct", "conversation")
	spanID := metrics.StartSpan(traceID, "", "工具调用", "tool", map[string]string{
		"tool": "web_search", "token": "secret", "authorization": "Bearer hidden", "query": "private text",
	})
	metrics.FinishSpan(spanID, "failed", map[string]string{"errorCode": "SEARCH_FAILED"})
	metrics.End(traceID, "failed")
	detail := waitForTraceDetail(t, metrics, traceID, func(value MessageTraceDetail) bool { return value.Status == "failed" })
	encoded := strings.ToLower(detail.Spans[len(detail.Spans)-1].Attributes["tool"])
	for _, forbidden := range []string{"secret", "bearer", "private text"} {
		if strings.Contains(encoded, forbidden) {
			t.Fatalf("trace detail leaked %q: %#v", forbidden, detail)
		}
	}
}

func waitForTraceDetail(t *testing.T, metrics *MessageMetrics, traceID string, ready func(MessageTraceDetail) bool) MessageTraceDetail {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if detail, ok := metrics.Trace(traceID); ok && ready(detail) {
			return detail
		}
		time.Sleep(time.Millisecond)
	}
	detail, _ := metrics.Trace(traceID)
	t.Fatalf("trace detail did not reach expected state: %#v", detail)
	return MessageTraceDetail{}
}
