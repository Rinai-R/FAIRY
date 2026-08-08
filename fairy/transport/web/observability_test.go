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
}
