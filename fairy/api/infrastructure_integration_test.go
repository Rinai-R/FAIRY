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
	pgstore "fairy/postgres"
	fairyruntime "fairy/runtime"
	"fairy/vectorindex"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

func TestProductionInfrastructureStatusAndMetrics(t *testing.T) {
	databaseURL, cleanup := isolatedAPISchema(t)
	defer cleanup()
	qdrantURL := apiTestQdrantURL()
	ensureAPIQdrantCollection(t, qdrantURL)
	masterKey := base64.StdEncoding.EncodeToString([]byte("abcdef0123456789abcdef0123456789"))
	setAPIProductionEnv(t, databaseURL, qdrantURL, masterKey)

	rt, err := fairyruntime.Open(fairyruntime.Options{ConfigRoot: t.TempDir(), Logger: zap.NewNop()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.Close() })
	insertPendingEmbeddingMetric(t, rt.Database)

	baseURL := startProductionAPIServer(t, rt)
	statusResponse := doRequest(t, http.MethodGet, baseURL+"/v1/status", "")
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
	assertReadyDependency(t, status, "qdrant")
	assertReadyDependency(t, status, "secretKey")
	database := status["database"].(map[string]any)
	schema := database["schema"].(map[string]any)
	if schema["current"] != true || schema["presentObjects"] != schema["expectedObjects"] {
		t.Fatalf("database schema status = %#v", schema)
	}
	qdrant := status["qdrant"].(map[string]any)
	collection := qdrant["collection"].(map[string]any)
	if collection["dimensions"] != float64(vectorindex.Dimensions) || collection["distance"] != vectorindex.Distance {
		t.Fatalf("qdrant collection status = %#v", collection)
	}

	metricsResponse := doRequest(t, http.MethodGet, baseURL+"/v1/metrics", "")
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
	vectorMetrics := databaseMetrics["vector"].(map[string]any)
	jobs := vectorMetrics["embeddingJobs"].(map[string]any)
	if jobs["pending"] != float64(1) {
		t.Fatalf("embedding job metrics = %#v", jobs)
	}
	qdrantMetrics := metrics["qdrant"].(map[string]any)
	snapshot := qdrantMetrics["snapshot"].(map[string]any)
	if snapshot["pointCount"] != collection["pointsCount"] {
		t.Fatalf("qdrant metrics=%#v collection=%#v", snapshot, collection)
	}
}

func assertReadyDependency(t *testing.T, payload map[string]any, name string) {
	t.Helper()
	dependency, ok := payload[name].(map[string]any)
	if !ok || dependency["ready"] != true || dependency["mode"] != "production" {
		t.Fatalf("%s status = %#v", name, payload[name])
	}
}

func insertPendingEmbeddingMetric(t *testing.T, pool *pgstore.Pool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := pool.Raw().Exec(ctx, `
INSERT INTO memory_embedding_jobs(
  id, item_kind, item_id, model_id, dimensions, point_id, content_hash,
  status, created_at_ms, updated_at_ms
) VALUES (
  'metric-job', 'personal_memory', 'metric-item', 'bge-small-zh-v1.5', 512,
  '11111111-1111-1111-1111-111111111111', repeat('a', 64), 'pending', 1, 1
)`)
	if err != nil {
		t.Fatal(err)
	}
}

func startProductionAPIServer(t *testing.T, rt *fairyruntime.Runtime) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	listener.Close()
	server, err := api.NewServer(rt, api.Options{Addr: addr, Logger: zap.NewNop()})
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = server.Run() }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	})
	waitHTTP(t, "http://"+addr+"/v1/status")
	return "http://" + addr
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
	pool, err := pgstore.Open(ctx, pgstore.ShortTimeoutConfig(databaseURL))
	if err != nil {
		t.Fatal(err)
	}
	if err := pgstore.Migrate(ctx, pool); err != nil {
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

func ensureAPIQdrantCollection(t *testing.T, rawURL string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err := vectorindex.Open(ctx, vectorindex.Config{URL: rawURL, Timeout: 5 * time.Second, CollectionName: vectorindex.CollectionName})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.MigrateCollection(ctx); err != nil {
		t.Fatal(err)
	}
}

func setAPIProductionEnv(t *testing.T, databaseURL, qdrantURL, masterKey string) {
	t.Helper()
	t.Setenv(pgstore.EnvDatabaseURL, databaseURL)
	t.Setenv(pgstore.EnvMaxConns, "4")
	t.Setenv(pgstore.EnvMinConns, "0")
	t.Setenv(pgstore.EnvConnectTimeout, "2s")
	t.Setenv(pgstore.EnvQueryTimeout, "2s")
	t.Setenv(vectorindex.EnvURL, qdrantURL)
	t.Setenv(vectorindex.EnvTimeout, "2s")
	t.Setenv("FAIRY_SECRET_MASTER_KEY", masterKey)
}

func apiTestQdrantURL() string {
	if value := os.Getenv("FAIRY_TEST_QDRANT_GRPC_URL"); value != "" {
		return value
	}
	return "http://127.0.0.1:16334"
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

func waitHTTP(t *testing.T, rawURL string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		response, err := http.Get(rawURL)
		if err == nil {
			response.Body.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("HTTP server did not become ready: %s", rawURL)
}
