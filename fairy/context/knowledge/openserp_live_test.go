//go:build live

package knowledge

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestLiveOpenSERPSearchAndExtractionUseDeclaredOrigin(t *testing.T) {
	origin := strings.TrimSpace(os.Getenv("FAIRY_OPENSERP_TEST_ORIGIN"))
	if origin == "" {
		t.Skip("no explicit live OpenSERP origin")
	}

	service := NewWebSearchService(origin)
	t.Cleanup(func() {
		if err := service.Close(); err != nil {
			t.Errorf("close OpenSERP service: %v", err)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := service.EnsureReady(ctx); err != nil {
		t.Fatalf("declared OpenSERP origin is not ready: %v", err)
	}

	hits, err := service.Search(ctx, "The Go Programming Language golang.google.cn", defaultSearchLimit)
	if err != nil {
		t.Fatalf("search declared OpenSERP origin: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("declared OpenSERP origin returned no search results")
	}

	var source IngestSource
	for _, hit := range hits {
		if strings.HasPrefix(hit.URL, "https://golang.google.cn/") {
			source = IngestSource{ID: "live-openserp-source", URL: hit.URL, Title: hit.Title}
			break
		}
	}
	if source.ID == "" {
		t.Fatal("declared OpenSERP origin did not return the expected extractable public source")
	}
	document, err := service.FetchSource(ctx, source)
	if err != nil {
		t.Fatalf("extract public source through declared OpenSERP origin: %v", err)
	}
	if document.CanonicalURL != source.URL || strings.TrimSpace(document.Content) == "" ||
		document.ContentHash == "" || document.EvidenceID == "" {
		t.Fatal("declared OpenSERP origin returned an incomplete extracted document")
	}
	t.Logf("OpenSERP origin=%s hits=%d extracted_bytes=%d", service.BaseURL(), len(hits), len(document.Content))
}

func TestLiveOpenSERPBlockedOriginDoesNotFallback(t *testing.T) {
	if strings.TrimSpace(os.Getenv("FAIRY_OPENSERP_TEST_ORIGIN")) == "" {
		t.Skip("no explicit live OpenSERP origin")
	}

	service := NewWebSearchService("http://127.0.0.1:1")
	t.Cleanup(func() {
		if err := service.Close(); err != nil {
			t.Errorf("close blocked OpenSERP service: %v", err)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := service.Search(ctx, "must not use another OpenSERP origin", 1); !errors.Is(err, ErrSearchEndpointNotConfigured) {
		t.Fatalf("blocked declared OpenSERP origin error = %v, want %v", err, ErrSearchEndpointNotConfigured)
	}
}
