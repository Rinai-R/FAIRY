package web

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"fairy/runtime/observability"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/route/param"
)

func TestExperienceQueueStatsKeepZeroValueSchema(t *testing.T) {
	payload, err := json.Marshal(ExperienceStats{})
	if err != nil {
		t.Fatal(err)
	}
	encoded := string(payload)
	for _, field := range []string{
		`"enqueued":0`, `"registered":0`, `"dropped":0`, `"succeeded":0`, `"failed":0`,
		`"modelCalls":0`, `"inputTokens":0`, `"cachedObservedInputTokens":0`,
		`"cachedInputTokens":0`, `"cacheWriteTokens":0`, `"outputTokens":0`,
	} {
		if !strings.Contains(encoded, field) {
			t.Fatalf("experience metrics omitted %s: %s", field, encoded)
		}
	}
	var decoded struct {
		Learning map[string]any `json:"learning"`
		Feedback map[string]any `json:"feedback"`
	}
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	_, learningHasRegistered := decoded.Learning["registered"]
	_, feedbackHasEnqueued := decoded.Feedback["enqueued"]
	if learningHasRegistered || feedbackHasEnqueued {
		t.Fatalf("experience metrics mixed queue semantics: %s", encoded)
	}
}

func TestMetricHistoryPointProjectsExperienceCounters(t *testing.T) {
	response := metricsResponse{GeneratedAtUnixMS: 42}
	response.Runtime.Experience = ExperienceStats{
		Learning: LearningQueueStats{Enqueued: 7, Succeeded: 5, Failed: 1, Dropped: 1},
		Feedback: FeedbackQueueStats{
			Registered: 9, Succeeded: 6, Failed: 2, Dropped: 1,
			ModelCalls: 5, InputTokens: 1200, CachedObservedInputTokens: 1000,
			CachedInputTokens: 700, CacheWriteTokens: 100, OutputTokens: 80,
		},
	}
	response.Runtime.AgentLoop.Compaction = CompactionMetrics{L1Applied: 2, L2Applied: 3, L3Applied: 1, Failed: 4}
	point := metricHistoryPoint(response, time.UnixMilli(1), 0)
	if point.LearningEnqueued != 7 || point.LearningSucceeded != 5 || point.LearningFailed != 1 || point.LearningDropped != 1 {
		t.Fatalf("learning history = %#v", point)
	}
	if point.FeedbackRegistered != 9 || point.FeedbackSucceeded != 6 || point.FeedbackFailed != 2 || point.FeedbackDropped != 1 {
		t.Fatalf("feedback history = %#v", point)
	}
	if point.FeedbackModelCalls != 5 || point.FeedbackInputTokens != 1200 || point.FeedbackCachedObservedInputTokens != 1000 || point.FeedbackCachedInputTokens != 700 || point.FeedbackCacheWriteTokens != 100 || point.FeedbackOutputTokens != 80 {
		t.Fatalf("feedback usage history = %#v", point)
	}
	if point.CompactionL1Applied != 2 || point.CompactionL2Applied != 3 || point.CompactionL3Applied != 1 || point.CompactionFailed != 4 {
		t.Fatalf("compaction history = %#v", point)
	}
}

func TestValidateExperienceStatsRejectsNegativeCounters(t *testing.T) {
	stats := ExperienceStats{Learning: LearningQueueStats{Enqueued: -1}}
	if err := validateExperienceStats(stats); err == nil || !strings.Contains(err.Error(), "learning.enqueued") {
		t.Fatalf("validation error = %v", err)
	}
}

func TestValidateExperienceStatsRejectsNegativeFeedbackUsage(t *testing.T) {
	stats := ExperienceStats{Feedback: FeedbackQueueStats{CachedObservedInputTokens: -1}}
	if err := validateExperienceStats(stats); err == nil || !strings.Contains(err.Error(), "feedback.cachedObservedInputTokens") {
		t.Fatalf("validation error = %v", err)
	}
}

func TestConversationHTTPRouteExcludesManagementAndMarksWebSocketLongLived(t *testing.T) {
	tests := []struct {
		route     string
		tracked   bool
		longLived bool
	}{
		{route: "/v1/session/ws", tracked: true, longLived: true},
		{route: "/v1/session/browser-ticket", tracked: true},
		{route: "/v1/sessions/:conversationId/messages", tracked: true},
		{route: "/v1/sessions/:conversationId/turns/:turnId/runtime", tracked: true},
		{route: "/v1/logs/stream"},
		{route: "/v1/metrics"},
		{route: "/v1/status"},
		{route: "/console/*filepath"},
	}
	for _, test := range tests {
		t.Run(test.route, func(t *testing.T) {
			tracked, longLived := conversationHTTPRoute(test.route)
			if tracked != test.tracked || longLived != test.longLived {
				t.Fatalf("classification = (%t, %t), want (%t, %t)", tracked, longLived, test.tracked, test.longLived)
			}
		})
	}
}

func TestFilterMetricHistoryScopeDropsLegacyAllRouteSamples(t *testing.T) {
	history := []observability.MetricHistoryPoint{
		{TimestampUnixMS: 1, HTTPScope: ""},
		{TimestampUnixMS: 2, HTTPScope: httpMetricScope},
		{TimestampUnixMS: 3, HTTPScope: "other"},
	}
	filtered := filterMetricHistoryScope(history, httpMetricScope)
	if len(filtered) != 1 || filtered[0].TimestampUnixMS != 2 {
		t.Fatalf("filtered history = %#v", filtered)
	}
}

func TestHandleLogStreamRejectsSubscriberCapacityBeforeSSE(t *testing.T) {
	store := observability.NewLogStore(2)
	unsubscribes := make([]func(), 0, observability.DefaultSubscriberCapacity)
	for index := 0; index < observability.DefaultSubscriberCapacity; index++ {
		_, _, unsubscribe, err := store.Subscribe(observability.LogFilter{})
		if err != nil {
			t.Fatal(err)
		}
		unsubscribes = append(unsubscribes, unsubscribe)
	}
	t.Cleanup(func() {
		for _, unsubscribe := range unsubscribes {
			unsubscribe()
		}
	})

	server := &Server{rt: &Dependencies{Logs: store}}
	var request app.RequestContext
	server.handleLogStream(context.Background(), &request)
	if got := request.Response.StatusCode(); got != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", got, http.StatusServiceUnavailable)
	}
	if body := string(request.Response.Body()); !strings.Contains(body, observability.ErrLogSubscriberCapacity.Error()) {
		t.Fatalf("body = %s", body)
	}
	if got := store.Stats().ActiveSubscribers; got != observability.DefaultSubscriberCapacity {
		t.Fatalf("active subscribers = %d, want %d", got, observability.DefaultSubscriberCapacity)
	}
}

func TestHandleTraceDetailReturnsSafeRecentTraceAndNotFound(t *testing.T) {
	metrics := observability.NewMessageMetrics()
	t.Cleanup(metrics.Close)
	traceID := metrics.Begin("direct", "conversation")
	spanID := metrics.StartSpan(traceID, "", "工具调用", "tool", map[string]string{
		"tool": "web_search", "query": "hidden query", "token": "hidden token",
	})
	metrics.FinishSpan(spanID, "completed", map[string]string{"status": "ok"})
	metrics.End(traceID, "completed")
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if detail, ok := metrics.Trace(traceID); ok && detail.Status == "completed" {
			break
		}
		time.Sleep(time.Millisecond)
	}

	server := &Server{rt: &Dependencies{Messages: metrics}}
	var request app.RequestContext
	request.Params = param.Params{{Key: "traceId", Value: traceID}}
	server.handleTraceDetail(context.Background(), &request)
	if got := request.Response.StatusCode(); got != http.StatusOK {
		t.Fatalf("status = %d, body = %s", got, request.Response.Body())
	}
	var detail observability.MessageTraceDetail
	if err := json.Unmarshal(request.Response.Body(), &detail); err != nil {
		t.Fatal(err)
	}
	encoded := strings.ToLower(string(request.Response.Body()))
	for _, forbidden := range []string{"hidden query", "hidden token", "bearer"} {
		if strings.Contains(encoded, forbidden) {
			t.Fatalf("trace response leaked %q: %s", forbidden, encoded)
		}
	}
	if detail.TraceID != traceID || len(detail.Spans) < 3 {
		t.Fatalf("trace detail = %#v", detail)
	}

	request.Reset()
	request.Params = param.Params{{Key: "traceId", Value: "msg-missing"}}
	server.handleTraceDetail(context.Background(), &request)
	if got := request.Response.StatusCode(); got != http.StatusNotFound {
		t.Fatalf("missing status = %d, want %d", got, http.StatusNotFound)
	}

	request.Reset()
	request.Params = param.Params{{Key: "traceId", Value: "msg\tinvalid"}}
	server.handleTraceDetail(context.Background(), &request)
	if got := request.Response.StatusCode(); got != http.StatusBadRequest {
		t.Fatalf("invalid trace id status = %d, want %d", got, http.StatusBadRequest)
	}
}

func TestHandleTraceSearchFindsExactActiveMessageID(t *testing.T) {
	metrics := observability.NewMessageMetrics()
	t.Cleanup(metrics.Close)
	traceID := metrics.BeginCorrelated("ambient", "conversation", "qq-message-17")
	metrics.Participation([]string{traceID}, "", "silent")
	waitForTraceStatus(t, metrics, traceID, "silent")

	server := &Server{rt: &Dependencies{Messages: metrics}}
	var request app.RequestContext
	request.Request.SetRequestURI("/v1/traces?messageId=qq-message-17")
	server.handleTraceSearch(context.Background(), &request)
	if got := request.Response.StatusCode(); got != http.StatusOK {
		t.Fatalf("status = %d, body = %s", got, request.Response.Body())
	}
	var response struct {
		MessageID string                       `json:"messageId"`
		Traces    []observability.MessageTrace `json:"traces"`
	}
	if err := json.Unmarshal(request.Response.Body(), &response); err != nil {
		t.Fatal(err)
	}
	if response.MessageID != "qq-message-17" || len(response.Traces) != 1 || response.Traces[0].TraceID != traceID {
		t.Fatalf("search response = %#v", response)
	}

	request.Reset()
	request.Request.SetRequestURI("/v1/traces?messageId=%0A")
	server.handleTraceSearch(context.Background(), &request)
	if got := request.Response.StatusCode(); got != http.StatusBadRequest {
		t.Fatalf("invalid status = %d, want %d", got, http.StatusBadRequest)
	}
}

func waitForTraceStatus(t *testing.T, metrics *observability.MessageMetrics, traceID, status string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if detail, ok := metrics.Trace(traceID); ok && detail.Status == status {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("trace %s did not reach %s", traceID, status)
}
