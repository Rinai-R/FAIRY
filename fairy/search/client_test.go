package search

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewServiceDefaultsBaseURL(t *testing.T) {
	service := NewService("")
	if service.BaseURL() != defaultBaseURL {
		t.Fatalf("BaseURL() = %q, want %q", service.BaseURL(), defaultBaseURL)
	}
}

func TestSearchAgainstMockOpenSERP(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.URL.Path != "/duck/search" {
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

	service := NewService(server.URL)
	hits, err := service.Search(t.Context(), "test", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Title != "最新情报" {
		t.Fatalf("hits = %#v", hits)
	}
}

func TestSearchFailsWhenEndpointDown(t *testing.T) {
	service := NewService("http://127.0.0.1:1")
	_, err := service.Search(t.Context(), "test", 5)
	if err == nil {
		t.Fatal("expected endpoint error")
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
