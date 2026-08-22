package knowledge

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestOpenSERPDocumentFetcherNeverConnectsToResultURL(t *testing.T) {
	var directRequests atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		directRequests.Add(1)
	}))
	defer target.Close()

	var extractRequests atomic.Int32
	openSERP := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/extract" || request.URL.Query().Get("url") != target.URL {
			t.Fatalf("OpenSERP request = %s", request.URL.String())
		}
		extractRequests.Add(1)
		writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(writer, "  OpenSERP 已清洗的完整正文。  ")
	}))
	defer openSERP.Close()

	service := NewWebSearchService(openSERP.URL)
	defer service.Close()
	document, err := service.FetchSource(t.Context(), IngestSource{
		ID: "source-1", Title: "来源标题", URL: target.URL, Rank: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if extractRequests.Load() != 1 || directRequests.Load() != 0 {
		t.Fatalf("requests = (OpenSERP %d, direct %d)", extractRequests.Load(), directRequests.Load())
	}
	if document.SourceID != "source-1" || document.CanonicalURL != target.URL || document.Title != "来源标题" ||
		document.Content != "OpenSERP 已清洗的完整正文。" || document.ContentType != "text/plain" ||
		document.ContentHash == "" || document.EvidenceID == "" || document.FetchedAtUnixMS <= 0 {
		t.Fatalf("document = %#v", document)
	}
}

func TestOpenSERPDocumentFetcherMapsBoundedFailures(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		contentType string
		want        error
	}{
		{name: "not found", status: http.StatusNotFound, contentType: "application/json", want: ErrFetchRejected},
		{name: "rate limited", status: http.StatusTooManyRequests, contentType: "application/json", want: ErrFetchTransient},
		{name: "unavailable", status: http.StatusServiceUnavailable, contentType: "application/json", want: ErrFetchTransient},
		{name: "wrong media", status: http.StatusOK, contentType: "application/json", want: ErrFetchRejected},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.URL.Path != "/extract" {
					t.Fatalf("path = %q", request.URL.Path)
				}
				writer.Header().Set("Content-Type", tc.contentType)
				writer.WriteHeader(tc.status)
				_, _ = io.WriteString(writer, "fixture")
			}))
			defer server.Close()
			service := NewWebSearchService(server.URL)
			defer service.Close()
			_, err := service.FetchSource(t.Context(), IngestSource{ID: "source-1", Title: "标题", URL: "https://source.example/item", Rank: 1})
			if !errors.Is(err, tc.want) {
				t.Fatalf("FetchSource() error = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestOpenSERPDocumentFetcherRejectsInvalidSourceBeforeNetwork(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests.Add(1)
	}))
	defer server.Close()
	service := NewWebSearchService(server.URL)
	defer service.Close()
	_, err := service.FetchSource(t.Context(), IngestSource{ID: "source-1", Title: "标题", URL: "file:///tmp/private", Rank: 1})
	if !errors.Is(err, ErrFetchRejected) {
		t.Fatalf("FetchSource() error = %v", err)
	}
	if requests.Load() != 0 {
		t.Fatalf("invalid source made %d requests", requests.Load())
	}
}
