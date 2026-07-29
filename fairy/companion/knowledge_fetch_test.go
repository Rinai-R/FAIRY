package companion

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"testing"

	"fairy/memory"
)

type staticKnowledgeResolver struct {
	addresses []netip.Addr
	err       error
}

func (r staticKnowledgeResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return append([]netip.Addr(nil), r.addresses...), r.err
}

type knowledgeRoundTripper func(*http.Request) (*http.Response, error)

func (f knowledgeRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestKnowledgeFetchRejectsNonPublicAddresses(t *testing.T) {
	for _, raw := range []string{"127.0.0.1", "10.0.0.2", "169.254.1.1", "::1", "fc00::1"} {
		t.Run(raw, func(t *testing.T) {
			fetcher := &httpKnowledgeDocumentFetcher{
				resolver: staticKnowledgeResolver{addresses: []netip.Addr{netip.MustParseAddr(raw)}},
				dialer:   &net.Dialer{},
				client:   &http.Client{},
			}
			if err := fetcher.validateURL(t.Context(), mustKnowledgeURL(t, "https://example.test/page")); err == nil {
				t.Fatal("validateURL() error = nil")
			}
		})
	}
}

func TestKnowledgeFetchRejectsRedirectToPrivateAddress(t *testing.T) {
	fetcher := &httpKnowledgeDocumentFetcher{
		resolver: staticKnowledgeResolver{addresses: []netip.Addr{netip.MustParseAddr("127.0.0.1")}},
		dialer:   &net.Dialer{},
	}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://internal.example/metadata", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := fetcher.checkRedirect(request, []*http.Request{{}}); err == nil {
		t.Fatal("checkRedirect() error = nil")
	}
	if err := fetcher.checkRedirect(request, make([]*http.Request, knowledgeFetchMaxRedirects)); err == nil {
		t.Fatal("redirect limit error = nil")
	}
}

func TestKnowledgeFetchCleansHashesAndChunksPublicHTML(t *testing.T) {
	fetcher := &httpKnowledgeDocumentFetcher{
		resolver: staticKnowledgeResolver{addresses: []netip.Addr{netip.MustParseAddr("93.184.216.34")}},
		dialer:   &net.Dialer{},
		client: &http.Client{Transport: knowledgeRoundTripper(func(request *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"text/html; charset=utf-8"}, "ETag": []string{`"v1"`}},
				Body: io.NopCloser(strings.NewReader(
					`<html><head><style>hidden</style></head><body><nav>menu</nav><main><h1>标题</h1><p>` +
						strings.Repeat("稳定正文 ", 300) + `</p></main><script>secret()</script></body></html>`,
				)),
				Request: request,
			}, nil
		})},
	}
	batch := memory.KnowledgeIngestBatch{
		ID: "batch", ConversationID: "conversation", TurnID: "turn",
		Sources: []memory.KnowledgeIngestSource{{
			ID: "source", Title: "来源", URL: "https://example.test/page", Rank: 1,
		}},
	}
	documents, err := fetcher.FetchBatch(t.Context(), batch)
	if err != nil {
		t.Fatal(err)
	}
	if len(documents) != 1 || documents[0].ContentHash == "" || len(documents[0].Chunks) < 2 {
		t.Fatalf("documents = %#v", documents)
	}
	for _, chunk := range documents[0].Chunks {
		if strings.Contains(chunk.Text, "hidden") || strings.Contains(chunk.Text, "menu") || strings.Contains(chunk.Text, "secret") {
			t.Fatalf("chunk retained skipped content: %q", chunk.Text)
		}
		if chunk.ID == "" || chunk.TextHash == "" {
			t.Fatalf("chunk identity = %#v", chunk)
		}
	}
	again, err := fetcher.FetchBatch(t.Context(), batch)
	if err != nil {
		t.Fatal(err)
	}
	if again[0].ContentHash != documents[0].ContentHash || again[0].Chunks[0].ID != documents[0].Chunks[0].ID {
		t.Fatalf("document identity is not deterministic: %#v %#v", documents, again)
	}
}

func TestKnowledgeFetchRejectsUnsupportedOrOversizedContent(t *testing.T) {
	for name, response := range map[string]*http.Response{
		"pdf": {
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/pdf"}},
			Body:       io.NopCloser(strings.NewReader("%PDF")),
		},
		"oversized": {
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/plain"}},
			Body:       io.NopCloser(strings.NewReader(strings.Repeat("x", knowledgeFetchMaxBodyBytes+1))),
		},
	} {
		t.Run(name, func(t *testing.T) {
			fetcher := &httpKnowledgeDocumentFetcher{
				resolver: staticKnowledgeResolver{addresses: []netip.Addr{netip.MustParseAddr("93.184.216.34")}},
				dialer:   &net.Dialer{},
				client: &http.Client{Transport: knowledgeRoundTripper(func(request *http.Request) (*http.Response, error) {
					response.Request = request
					return response, nil
				})},
			}
			_, err := fetcher.FetchBatch(t.Context(), memory.KnowledgeIngestBatch{
				ID: "batch", ConversationID: "conversation", TurnID: "turn",
				Sources: []memory.KnowledgeIngestSource{{ID: "source", URL: "https://example.test/page", Rank: 1}},
			})
			if err == nil {
				t.Fatal("FetchBatch() error = nil")
			}
		})
	}
}

func mustKnowledgeURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}
