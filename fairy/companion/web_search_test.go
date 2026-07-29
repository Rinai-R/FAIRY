package companion

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewServiceDefaultsBaseURL(t *testing.T) {
	service := NewWebSearchService("")
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

	service := NewWebSearchService(server.URL)
	hits, err := service.Search(t.Context(), "test", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Title != "最新情报" {
		t.Fatalf("hits = %#v", hits)
	}
}

func TestSearchFailsWhenEndpointDown(t *testing.T) {
	service := NewWebSearchService("http://127.0.0.1:1")
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

func TestNewWebSearchBatchCleansCanonicalizesAndDeduplicatesSources(t *testing.T) {
	hits := []WebSearchHit{
		{
			Title:   "  A &amp; B <b>作品</b> ",
			URL:     "HTTPS://Example.COM/path?utm_source=test&id=2#section",
			Snippet: " 第一段  <em>摘要</em>\n内容 ",
		},
		{
			Title:   "重复 URL",
			URL:     "https://example.com/path?id=2&utm_medium=social",
			Snippet: "不应保留",
		},
		{
			Title:   "A & B 作品",
			URL:     "https://mirror.example/path",
			Snippet: "第一段 摘要 内容",
		},
		{
			Title:   "独立来源",
			URL:     "https://second.example/item?version=1&fbclid=tracking",
			Snippet: "另一条可靠摘要",
		},
		{Title: "非法来源", URL: "javascript:alert(1)", Snippet: "必须丢弃"},
	}
	batch, err := newWebSearchBatch("conversation-1", "turn-1", "call-1", hits, 123)
	if err != nil {
		t.Fatal(err)
	}
	if batch.ID == "" || batch.ConversationID != "conversation-1" || batch.TurnID != "turn-1" || batch.ToolCallID != "call-1" {
		t.Fatalf("batch identity = %#v", batch)
	}
	if len(batch.Sources) != 2 {
		t.Fatalf("sources = %#v", batch.Sources)
	}
	first := batch.Sources[0]
	if first.Title != "A & B 作品" || first.Snippet != "第一段 摘要 内容" || first.URL != "https://example.com/path?id=2" || first.Rank != 1 || first.FetchedAtUnixMS != 123 || first.ID == "" {
		t.Fatalf("first source = %#v", first)
	}
	second := batch.Sources[1]
	if second.URL != "https://second.example/item?version=1" || second.Rank != 4 || second.ID == first.ID {
		t.Fatalf("second source = %#v", second)
	}
}

func TestNewWebSearchBatchKeepsToolCallBoundaries(t *testing.T) {
	hits := []WebSearchHit{{Title: "作品", URL: "https://example.test/item", Snippet: "足够清晰的摘要"}}
	first, err := newWebSearchBatch("conversation", "turn", "call-a", hits, 1)
	if err != nil {
		t.Fatal(err)
	}
	second, err := newWebSearchBatch("conversation", "turn", "call-b", hits, 1)
	if err != nil {
		t.Fatal(err)
	}
	nextTurn, err := newWebSearchBatch("conversation", "turn-next", "call-a", hits, 1)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID || first.ID == nextTurn.ID || second.ID == nextTurn.ID {
		t.Fatalf("batch IDs are not isolated: %q %q %q", first.ID, second.ID, nextTurn.ID)
	}
	if first.Sources[0].ID != second.Sources[0].ID {
		t.Fatalf("source identity should describe canonical evidence: %q != %q", first.Sources[0].ID, second.Sources[0].ID)
	}
}

func TestNewWebSearchBatchBoundsAndRejectsInvalidInputs(t *testing.T) {
	if _, err := newWebSearchBatch("", "turn", "call", nil, 0); err == nil {
		t.Fatal("expected missing identity error")
	}
	hits := make([]WebSearchHit, 0, 8)
	for index := 0; index < 7; index++ {
		hits = append(hits, WebSearchHit{
			Title:   "来源 " + string(rune('A'+index)),
			URL:     "https://example.test/item/" + string(rune('a'+index)),
			Snippet: "摘要 " + string(rune('A'+index)),
		})
	}
	hits = append(hits, WebSearchHit{Title: strings.Repeat("长", maxSearchTitleRunes+1), URL: "https://too-long.example", Snippet: "摘要"})
	batch, err := newWebSearchBatch("conversation", "turn", "call", hits, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Sources) != defaultSearchLimit {
		t.Fatalf("source count = %d, want %d", len(batch.Sources), defaultSearchLimit)
	}
}
