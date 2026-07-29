//go:build integration

package api_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"fairy/api"
	fairycore "fairy/core"
	"fairy/coredb"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

func TestProductionInfrastructureStatusAndMetrics(t *testing.T) {
	databaseURL, cleanup := isolatedAPISchema(t)
	defer cleanup()
	masterKey := base64.StdEncoding.EncodeToString([]byte("abcdef0123456789abcdef0123456789"))
	setAPIProductionEnv(t, databaseURL, masterKey)

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
	for _, forbidden := range []string{"fairy_test_password", masterKey, "FAIRY_SECRET_MASTER_KEY"} {
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
	schema := database["schema"].(map[string]any)
	if schema["current"] != true || schema["presentObjects"] != schema["expectedObjects"] {
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

func TestProductionPersonalMemoryContentLimitReturnsBadRequest(t *testing.T) {
	databaseURL, cleanup := isolatedAPISchema(t)
	defer cleanup()
	masterKey := base64.StdEncoding.EncodeToString([]byte("abcdef0123456789abcdef0123456789"))
	setAPIProductionEnv(t, databaseURL, masterKey)

	rt, err := fairycore.Open(fairycore.RuntimeOptions{ConfigRoot: t.TempDir(), Logger: zap.NewNop()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.Close() })
	bootstrap, err := rt.MemoryStore.OpenOrCreateCharacterConversationContext(t.Context(), "character-api-memory-limit")
	if err != nil {
		t.Fatal(err)
	}
	turn, err := rt.MemoryStore.BeginTurnContext(t.Context(), bootstrap.Conversation.ID, "source")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rt.MemoryStore.CompleteTurnContext(t.Context(), bootstrap.Conversation.ID, turn.ID, "reply"); err != nil {
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
	if strings.Contains(string(responseBody), masterKey) || strings.Contains(string(responseBody), "fairy_test_password") {
		t.Fatalf("oversized memory error leaked secret: %s", responseBody)
	}
	var count int
	if err := rt.Database.Raw().QueryRow(t.Context(), "SELECT count(*) FROM personal_memories").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("personal memory count = %d, want 0", count)
	}
}

func TestProductionKnowledgeManagementRoutesRequireAuthAndReturnCatalogs(t *testing.T) {
	databaseURL, cleanup := isolatedAPISchema(t)
	defer cleanup()
	masterKey := base64.StdEncoding.EncodeToString([]byte("abcdef0123456789abcdef0123456789"))
	setAPIProductionEnv(t, databaseURL, masterKey)

	rt, err := fairycore.Open(fairycore.RuntimeOptions{ConfigRoot: t.TempDir(), Logger: zap.NewNop()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.Close() })
	baseURL, token := startProductionAPIServer(t, rt)

	unauthorized, err := http.Get(baseURL + "/v1/knowledge/jobs")
	if err != nil {
		t.Fatal(err)
	}
	unauthorized.Body.Close()
	if unauthorized.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d", unauthorized.StatusCode)
	}
	for _, path := range []string{"/v1/knowledge", "/v1/knowledge/jobs"} {
		response := doRequest(t, http.MethodGet, baseURL+path, token)
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil || response.StatusCode != http.StatusOK {
			t.Fatalf("%s status=%d body=%s err=%v", path, response.StatusCode, body, err)
		}
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

func isolatedAPISchema(t *testing.T) (string, func()) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rawURL := os.Getenv("FAIRY_TEST_DATABASE_URL")
	if rawURL == "" {
		rawURL = "postgres://fairy:fairy_test_password@127.0.0.1:15432/fairy_test?sslmode=disable"
	}
	admin, err := pgxpool.New(ctx, rawURL)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("fairy_api_test_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	admin.Close()
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	values := parsed.Query()
	values.Set("search_path", schema)
	parsed.RawQuery = values.Encode()
	databaseURL := parsed.String()
	pool, err := coredb.Open(ctx, coredb.ShortTimeoutConfig(databaseURL))
	if err != nil {
		t.Fatal(err)
	}
	if err := coredb.Migrate(ctx, pool.Raw()); err != nil {
		pool.Close()
		t.Fatal(err)
	}
	pool.Close()
	return databaseURL, func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		cleanupPool, err := pgxpool.New(cleanupCtx, rawURL)
		if err != nil {
			t.Logf("open cleanup pool: %v", err)
			return
		}
		defer cleanupPool.Close()
		_, _ = cleanupPool.Exec(cleanupCtx, "DROP SCHEMA IF EXISTS "+quoted+" CASCADE")
	}
}

func setAPIProductionEnv(t *testing.T, databaseURL, masterKey string) {
	t.Helper()
	t.Setenv(coredb.EnvDatabaseURL, databaseURL)
	t.Setenv(coredb.EnvMaxConns, "4")
	t.Setenv(coredb.EnvMinConns, "0")
	t.Setenv(coredb.EnvConnectTimeout, "2s")
	t.Setenv(coredb.EnvQueryTimeout, "2s")
	t.Setenv("FAIRY_SECRET_MASTER_KEY", masterKey)
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
