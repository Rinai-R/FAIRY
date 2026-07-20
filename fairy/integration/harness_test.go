//go:build integration

package integration

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func TestIntegrationServicesReachable(t *testing.T) {
	databaseURL := getenvDefault("FAIRY_TEST_DATABASE_URL", "postgres://fairy:fairy_test_password@127.0.0.1:15432/fairy_test?sslmode=disable")
	qdrantURL := getenvDefault("FAIRY_TEST_QDRANT_URL", "http://127.0.0.1:16333")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	waitTCP(ctx, t, databaseURL)
	waitHTTP(ctx, t, strings.TrimRight(qdrantURL, "/")+"/healthz")
}

func getenvDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func waitTCP(ctx context.Context, t *testing.T, rawURL string) {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse database url: %v", err)
	}
	address := parsed.Host
	if !strings.Contains(address, ":") {
		address += ":5432"
	}
	var lastErr error
	for ctx.Err() == nil {
		conn, err := net.DialTimeout("tcp", address, 500*time.Millisecond)
		if err == nil {
			conn.Close()
			return
		}
		lastErr = err
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("postgres tcp %s unreachable: %v", address, lastErr)
}

func waitHTTP(ctx context.Context, t *testing.T, rawURL string) {
	t.Helper()
	client := http.Client{Timeout: 500 * time.Millisecond}
	var lastStatus int
	var lastErr error
	for ctx.Err() == nil {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			t.Fatalf("build health request: %v", err)
		}
		response, err := client.Do(req)
		if err == nil {
			lastStatus = response.StatusCode
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
		} else {
			lastErr = err
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("http health %s unreachable: status=%d err=%v", rawURL, lastStatus, lastErr)
}
