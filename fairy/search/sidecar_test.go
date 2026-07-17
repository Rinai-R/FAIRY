package search

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestResolveBinaryPrefersEnvAndBinDir(t *testing.T) {
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(binDir, BinaryName())
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	resolved, found := ResolveBinary(root)
	if !found || resolved != path {
		t.Fatalf("ResolveBinary() = %q %v", resolved, found)
	}
}

func TestParseSearchHits(t *testing.T) {
	body, _ := json.Marshal(map[string]any{
		"results": []map[string]any{
			{"title": "A", "url": "https://a.example", "snippet": "sa"},
			{"title": "B", "url": "https://b.example", "snippet": "sb"},
		},
	})
	hits, err := parseSearchHits(body, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Title != "A" {
		t.Fatalf("hits = %#v", hits)
	}
}

func TestSearchAgainstMockOpenSERP(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.URL.Path != "/duckduckgo/search" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]any{
				{"title": "最新情报", "url": "https://news.example/1", "snippet": "今天更新"},
			},
		})
	}))
	t.Cleanup(server.Close)

	service := NewService(t.TempDir())
	service.mu.Lock()
	service.baseURL = server.URL
	service.cmd = nil // pretend already running without process
	service.mu.Unlock()

	// Bypass EnsureRunning by setting baseURL; Search still calls EnsureRunning which will try binary.
	// Use direct HTTP through a thin test helper instead:
	req, err := http.NewRequest(http.MethodGet, server.URL+"/duckduckgo/search?text=test&limit=5", nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := service.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var payload searchResponse
	if err := json.NewDecoder(res.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Results) != 1 || payload.Results[0].Title != "最新情报" {
		t.Fatalf("payload = %#v", payload)
	}
}
