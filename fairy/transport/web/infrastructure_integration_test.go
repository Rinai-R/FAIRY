//go:build integration

package web_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	fairycore "fairy/app/core"
	api "fairy/transport/web"

	"go.uber.org/zap"
)

func TestProductionInfrastructureStatusAndMetrics(t *testing.T) {
	applySeekDBAPIEnv(t)

	rt, err := fairycore.Open(fairycore.RuntimeOptions{ConfigRoot: t.TempDir(), Logger: zap.NewNop()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.Close() })
	baseURL, token := startProductionAPIServer(t, rt)
	statusResponse := doRequest(t, http.MethodGet, baseURL+"/v1/status", token)
	statusBody, err := io.ReadAll(statusResponse.Body)
	statusResponse.Body.Close()
	if err != nil || statusResponse.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body=%s err=%v", statusResponse.StatusCode, statusBody, err)
	}
	for _, forbidden := range []string{"fairy_test_password", "postgres://", "FAIRY_SECRET_MASTER_KEY"} {
		if strings.Contains(string(statusBody), forbidden) {
			t.Fatalf("status leaked %q: %s", forbidden, statusBody)
		}
	}
	var status map[string]any
	if err := json.Unmarshal(statusBody, &status); err != nil {
		t.Fatal(err)
	}
	assertReadyDependency(t, status, "database")
	assertReadyDependency(t, status, "secretKey")
	database := status["database"].(map[string]any)
	if database["storage"] != "seekdb" {
		t.Fatalf("database storage = %#v", database["storage"])
	}
	schema := database["schema"].(map[string]any)
	if schema["state"] != "current" {
		t.Fatalf("database schema status = %#v", schema)
	}
	if _, ok := status["qdrant"]; ok {
		t.Fatalf("status still exposes qdrant: %#v", status["qdrant"])
	}

	metricsResponse := doRequest(t, http.MethodGet, baseURL+"/v1/metrics", token)
	defer metricsResponse.Body.Close()
	var metrics map[string]any
	if err := json.NewDecoder(metricsResponse.Body).Decode(&metrics); err != nil {
		t.Fatal(err)
	}
	if metricsResponse.StatusCode != http.StatusOK {
		t.Fatalf("metrics status = %d payload=%#v", metricsResponse.StatusCode, metrics)
	}
	databaseMetrics := metrics["database"].(map[string]any)
	if databaseMetrics["available"] != true {
		t.Fatalf("database metrics = %#v", databaseMetrics)
	}
	if databaseMetrics["vectorRows"] != float64(0) {
		t.Fatalf("database vector metrics = %#v", databaseMetrics)
	}
	if _, ok := metrics["qdrant"]; ok {
		t.Fatalf("metrics still exposes qdrant: %#v", metrics["qdrant"])
	}
}

func TestProductionMetricSamplerPersistsWithoutMetricsRequestsAndRestoresAfterRestart(t *testing.T) {
	applySeekDBAPIEnv(t)

	rt, err := fairycore.Open(fairycore.RuntimeOptions{ConfigRoot: t.TempDir(), Logger: zap.NewNop()})
	if err != nil {
		t.Fatal(err)
	}
	closed := false
	t.Cleanup(func() {
		if !closed {
			_ = rt.Close()
		}
	})

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	listener.Close()
	server, err := api.NewServer(rt.APIDependencies(), api.Options{
		Addr: addr, Token: "integration-test-token", Logger: zap.NewNop(),
	})
	if err != nil {
		t.Fatal(err)
	}
	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		_ = server.Run()
	}()
	waitHTTP(t, "http://"+addr+"/v1/status", "integration-test-token")
	waitForPersistedMetricCount(t, rt, 2)

	shutdownCtx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	if err := server.Shutdown(shutdownCtx); err != nil {
		cancel()
		t.Fatal(err)
	}
	cancel()
	select {
	case <-runDone:
	case <-time.After(2 * time.Second):
		t.Fatal("metric sampler server did not stop")
	}
	if err := rt.Close(); err != nil {
		t.Fatal(err)
	}
	closed = true

	restarted, err := fairycore.Open(fairycore.RuntimeOptions{ConfigRoot: t.TempDir(), Logger: zap.NewNop()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
	points, err := restarted.History.RecentMetrics(t.Context(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(points) < 2 {
		t.Fatalf("restored metric samples = %d, want at least 2", len(points))
	}
}

func waitForPersistedMetricCount(t *testing.T, rt *fairycore.Runtime, want int) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		points, err := rt.History.RecentMetrics(t.Context(), want)
		if err != nil {
			t.Fatal(err)
		}
		if len(points) >= want {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("persisted metric samples did not reach %d without /v1/metrics requests", want)
}

func TestProductionPersonalMemoryContentLimitReturnsBadRequest(t *testing.T) {
	applySeekDBAPIEnv(t)

	rt, err := fairycore.Open(fairycore.RuntimeOptions{ConfigRoot: t.TempDir(), Logger: zap.NewNop()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.Close() })
	bootstrap, err := rt.TranscriptStore.OpenOrCreateCharacterConversationContext(t.Context(), "character-api-memory-limit")
	if err != nil {
		t.Fatal(err)
	}
	turn, err := rt.TranscriptStore.BeginTurnContext(t.Context(), bootstrap.Conversation.ID, "source")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rt.TranscriptStore.CompleteTurnContext(t.Context(), bootstrap.Conversation.ID, turn.ID, "reply"); err != nil {
		t.Fatal(err)
	}

	baseURL, token := startProductionAPIServer(t, rt)
	body := fmt.Sprintf(`{"kind":"preference","scope":{"type":"global"},"content":%q,"confidenceBasisPoints":9000}`, strings.Repeat("界", 2401))
	req, err := http.NewRequest(http.MethodPost, baseURL+"/v1/memories/personal", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusBadRequest || !strings.Contains(string(responseBody), "2400 Unicode characters") {
		t.Fatalf("oversized memory response = %d %s", response.StatusCode, responseBody)
	}
	if strings.Contains(string(responseBody), "postgres://") {
		t.Fatalf("oversized memory error leaked database URL: %s", responseBody)
	}
	database, err := rt.Foundation.SQL()
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err := database.QueryRowContext(t.Context(), "SELECT count(*) FROM personal_memories").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("personal memory count = %d, want 0", count)
	}
}

func TestProductionKnowledgeManagementRouteRequiresAuthAndRemovedJobsRouteStaysAbsent(t *testing.T) {
	applySeekDBAPIEnv(t)

	rt, err := fairycore.Open(fairycore.RuntimeOptions{ConfigRoot: t.TempDir(), Logger: zap.NewNop()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.Close() })
	baseURL, token := startProductionAPIServer(t, rt)

	unauthorized, err := http.Get(baseURL + "/v1/knowledge")
	if err != nil {
		t.Fatal(err)
	}
	unauthorized.Body.Close()
	if unauthorized.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d", unauthorized.StatusCode)
	}
	response := doRequest(t, http.MethodGet, baseURL+"/v1/knowledge", token)
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("/v1/knowledge status=%d body=%s err=%v", response.StatusCode, body, err)
	}
	removed := doRequest(t, http.MethodGet, baseURL+"/v1/knowledge/jobs", token)
	removed.Body.Close()
	if removed.StatusCode != http.StatusNotFound {
		t.Fatalf("removed /v1/knowledge/jobs status=%d, want 404", removed.StatusCode)
	}
}

func assertReadyDependency(t *testing.T, payload map[string]any, name string) {
	t.Helper()
	dependency, ok := payload[name].(map[string]any)
	if !ok || dependency["ready"] != true || dependency["mode"] != "production" {
		t.Fatalf("%s status = %#v", name, payload[name])
	}
}

func startProductionAPIServer(t *testing.T, rt *fairycore.Runtime) (string, string) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	listener.Close()
	const token = "integration-test-token"
	server, err := api.NewServer(rt.APIDependencies(), api.Options{Addr: addr, Token: token, Logger: zap.NewNop()})
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = server.Run() }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	})
	waitHTTP(t, "http://"+addr+"/v1/status", token)
	return "http://" + addr, token
}

func doRequest(t *testing.T, method, rawURL, token string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, rawURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func waitHTTP(t *testing.T, rawURL, token string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		req, err := http.NewRequest(http.MethodGet, rawURL, nil)
		if err != nil {
			t.Fatal(err)
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		response, err := http.DefaultClient.Do(req)
		if err == nil {
			response.Body.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("HTTP server did not become ready: %s", rawURL)
}
