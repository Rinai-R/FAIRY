//go:build sqlite_legacy

package api_test

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"fairy/api"
	"fairy/memory"
	"fairy/observability"
	fairyruntime "fairy/runtime"
	"go.uber.org/zap"
)

func TestLogsAndMetricsAPI(t *testing.T) {
	rt, baseURL := startObservabilityServer(t, "secret")
	rt.Logs.Append(observability.EntryInput{
		Level: "info", Logger: "companion.turn", Message: "ignore",
	})
	rt.Logs.Append(observability.EntryInput{
		Level: "warn", Logger: "companion.turn", Message: "Authorization: Bearer hidden",
	})

	res := doRequest(t, http.MethodGet, baseURL+"/v1/logs", "")
	if res.StatusCode != http.StatusUnauthorized {
		res.Body.Close()
		t.Fatalf("unauthorized logs status = %d", res.StatusCode)
	}
	res.Body.Close()

	res = doRequest(t, http.MethodGet, baseURL+"/v1/logs?level=verbose", "secret")
	if res.StatusCode != http.StatusBadRequest {
		res.Body.Close()
		t.Fatalf("invalid filter status = %d", res.StatusCode)
	}
	res.Body.Close()

	res = doRequest(t, http.MethodGet, baseURL+"/v1/logs?level=warn&logger=companion", "secret")
	defer res.Body.Close()
	var logs struct {
		Entries []observability.LogEntry `json:"entries"`
	}
	if err := json.NewDecoder(res.Body).Decode(&logs); err != nil {
		t.Fatal(err)
	}
	if len(logs.Entries) != 1 || strings.Contains(logs.Entries[0].Message, "hidden") {
		t.Fatalf("logs = %#v", logs.Entries)
	}

	for _, conversationID := range []string{"one", "two"} {
		res = doRequest(t, http.MethodPost, baseURL+"/v1/sessions/"+conversationID+"/turns", "secret")
		io.Copy(io.Discard, res.Body)
		res.Body.Close()
	}
	res = doRequest(t, http.MethodGet, baseURL+"/v1/metrics", "secret")
	defer res.Body.Close()
	var metrics struct {
		GeneratedAtUnixMS int64 `json:"generatedAtUnixMs"`
		HTTP              struct {
			Routes []observability.HTTPRouteMetrics `json:"routes"`
		} `json:"http"`
		Logs  observability.LogStats `json:"logs"`
		Usage struct {
			TurnCount uint64 `json:"turnCount"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(res.Body).Decode(&metrics); err != nil {
		t.Fatal(err)
	}
	if metrics.GeneratedAtUnixMS == 0 || metrics.Logs.RetainedEntries < 2 {
		t.Fatalf("metrics = %#v", metrics)
	}
	var turnRoute *observability.HTTPRouteMetrics
	for i := range metrics.HTTP.Routes {
		if metrics.HTTP.Routes[i].Route == "/v1/sessions/:conversationId/turns" {
			turnRoute = &metrics.HTTP.Routes[i]
		}
	}
	if turnRoute == nil || turnRoute.RequestCount != 2 {
		t.Fatalf("turn route = %#v routes=%#v", turnRoute, metrics.HTTP.Routes)
	}
}

func TestLogStreamSendsReadyBacklogAndLive(t *testing.T) {
	rt, baseURL := startObservabilityServer(t, "")
	rt.Logs.Append(observability.EntryInput{Level: "warn", Logger: "companion", Message: "backlog"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/v1/logs/stream?level=warn", nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	scanner := bufio.NewScanner(res.Body)
	events := make(chan string, 8)
	go func() {
		for scanner.Scan() {
			if line := scanner.Text(); strings.HasPrefix(line, "event: ") {
				events <- strings.TrimPrefix(line, "event: ")
			}
		}
		close(events)
	}()
	if event := waitStreamEvent(t, events); event != "ready" {
		t.Fatalf("first event = %q", event)
	}
	if event := waitStreamEvent(t, events); event != "log" {
		t.Fatalf("backlog event = %q", event)
	}
	rt.Logs.Append(observability.EntryInput{Level: "error", Logger: "companion", Message: "live"})
	if event := waitStreamEvent(t, events); event != "log" {
		t.Fatalf("live event = %q", event)
	}
	cancel()
}

func TestMetricsReturnsFailureWhenUsageDatabaseCannotBeRead(t *testing.T) {
	rt, baseURL := startObservabilityServer(t, "")
	databasePath, err := memory.DatabasePath(rt.ConfigRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(databasePath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(databasePath, 0o700); err != nil {
		t.Fatal(err)
	}

	res := doRequest(t, http.MethodGet, baseURL+"/v1/metrics", "")
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusInternalServerError {
		t.Fatalf("metrics status = %d body=%s", res.StatusCode, body)
	}
	if !strings.Contains(string(body), `"error"`) || strings.Contains(string(body), `"turnCount"`) {
		t.Fatalf("metrics failure body = %s", body)
	}
}

func TestNewServerRejectsTokenWhitespace(t *testing.T) {
	rt, err := fairyruntime.Open(testRuntimeOptions(t, t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	if _, err := api.NewServer(rt, api.Options{Token: " secret "}); err == nil {
		t.Fatal("NewServer accepted token whitespace")
	}
}

func startObservabilityServer(t *testing.T, token string) (*fairyruntime.Runtime, string) {
	t.Helper()
	rt, err := fairyruntime.Open(testRuntimeOptions(t, t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.Close() })
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	srv, err := api.NewServer(rt, api.Options{Addr: addr, Token: token, Logger: zap.NewNop()})
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Run() }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})
	waitHTTP(t, "http://"+addr+"/v1/status")
	return rt, "http://" + addr
}

func doRequest(t *testing.T, method, rawURL, token string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(method, rawURL, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func waitStreamEvent(t *testing.T, events <-chan string) string {
	t.Helper()
	select {
	case event, ok := <-events:
		if !ok {
			t.Fatal("stream closed")
		}
		return event
	case <-time.After(2 * time.Second):
		t.Fatal("stream event timeout")
		return ""
	}
}
