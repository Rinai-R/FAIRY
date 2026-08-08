package knowledge

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
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

type blockingKnowledgeResponseBody struct {
	reader  *strings.Reader
	started chan struct{}
	release <-chan struct{}
	blocked bool
}

func (body *blockingKnowledgeResponseBody) Read(buffer []byte) (int, error) {
	if !body.blocked {
		body.blocked = true
		close(body.started)
		<-body.release
	}
	return body.reader.Read(buffer)
}

func (*blockingKnowledgeResponseBody) Close() error {
	return nil
}

func waitForKnowledgeFetchClockAfter(timestamp int64) {
	for time.Now().UnixMilli() <= timestamp {
		<-time.After(time.Millisecond)
	}
}

func TestKnowledgeFetchRejectsNonPublicAddresses(t *testing.T) {
	for _, raw := range []string{"127.0.0.1", "10.0.0.2", "169.254.1.1", "::1", "fc00::1"} {
		t.Run(raw, func(t *testing.T) {
			fetcher := &HTTPDocumentFetcher{
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
	fetcher := &HTTPDocumentFetcher{
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

func TestKnowledgeFetchCleansAndKeepsCompletePublicHTML(t *testing.T) {
	bodyPrefix := strings.Repeat("稳定正文 ", 5000)
	bodyTail := "旧固定窗口覆盖范围之后的完整尾部"
	fetcher := &HTTPDocumentFetcher{
		resolver: staticKnowledgeResolver{addresses: []netip.Addr{netip.MustParseAddr("93.184.216.34")}},
		dialer:   &net.Dialer{},
		client: &http.Client{Transport: knowledgeRoundTripper(func(request *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"text/html; charset=utf-8"}, "ETag": []string{`"v1"`}},
				Body: io.NopCloser(strings.NewReader(
					`<html><head><style>hidden</style></head><body><nav>menu</nav><main><h1>标题</h1><p>` +
						bodyPrefix + bodyTail + `</p></main><script>secret()</script></body></html>`,
				)),
				Request: request,
			}, nil
		})},
	}
	source := IngestSource{
		ID: "source", Title: "来源", URL: "https://example.test/page", Rank: 1,
	}
	document, err := fetcher.FetchSource(t.Context(), source)
	if err != nil {
		t.Fatal(err)
	}
	if document.ContentHash == "" || document.EvidenceID == "" || document.Content == "" {
		t.Fatalf("document = %#v", document)
	}
	if strings.Contains(document.Content, "hidden") || strings.Contains(document.Content, "menu") || strings.Contains(document.Content, "secret") {
		t.Fatalf("document retained skipped content")
	}
	if !strings.Contains(document.Content, bodyTail) {
		t.Fatalf("document lost its tail")
	}
	if !strings.Contains(document.Content, "标题\n") {
		t.Fatalf("document did not preserve block structure: %q", document.Content[:min(len(document.Content), 80)])
	}
	again, err := fetcher.FetchSource(t.Context(), source)
	if err != nil {
		t.Fatal(err)
	}
	if again.ContentHash != document.ContentHash || again.EvidenceID != document.EvidenceID || again.Content != document.Content {
		t.Fatalf("document identity is not deterministic: %#v %#v", document, again)
	}
}

func TestKnowledgeFetchFreshnessUsesRequestOrderNotBodyCompletion(t *testing.T) {
	firstBodyStarted := make(chan struct{})
	releaseFirstBody := make(chan struct{})
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() {
			close(releaseFirstBody)
		})
	}
	t.Cleanup(release)
	callCount := 0
	fetcher := &HTTPDocumentFetcher{
		resolver: staticKnowledgeResolver{addresses: []netip.Addr{netip.MustParseAddr("93.184.216.34")}},
		dialer:   &net.Dialer{},
		client: &http.Client{Transport: knowledgeRoundTripper(func(request *http.Request) (*http.Response, error) {
			callCount++
			var body io.ReadCloser = io.NopCloser(strings.NewReader("后发请求返回的新正文。"))
			if callCount == 1 {
				body = &blockingKnowledgeResponseBody{
					reader:  strings.NewReader("先发请求返回的旧正文。"),
					started: firstBodyStarted,
					release: releaseFirstBody,
				}
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"text/plain"}},
				Body:       body,
				Request:    request,
			}, nil
		})},
	}
	source := IngestSource{
		ID: "freshness-source", Title: "来源", URL: "https://example.test/freshness", Rank: 1,
	}
	firstResult := make(chan Document, 1)
	firstError := make(chan error, 1)
	go func() {
		document, err := fetcher.FetchSource(t.Context(), source)
		firstResult <- document
		firstError <- err
	}()
	<-firstBodyStarted
	waitForKnowledgeFetchClockAfter(time.Now().UnixMilli())
	second, err := fetcher.FetchSource(t.Context(), source)
	if err != nil {
		t.Fatal(err)
	}
	waitForKnowledgeFetchClockAfter(second.FetchedAtUnixMS)
	release()
	first := <-firstResult
	if err := <-firstError; err != nil {
		t.Fatal(err)
	}
	if first.FetchedAtUnixMS >= second.FetchedAtUnixMS {
		t.Fatalf(
			"request-order freshness first=%d second=%d; slower first request must remain older",
			first.FetchedAtUnixMS,
			second.FetchedAtUnixMS,
		)
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
		"control": {
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/plain"}},
			Body:       io.NopCloser(strings.NewReader("合法正文中混入\x00禁止控制字符。")),
		},
		"invalid_html_utf8": {
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/html"}},
			Body:       io.NopCloser(strings.NewReader("<html><body>合法正文混入\xff非法字节。</body></html>")),
		},
	} {
		t.Run(name, func(t *testing.T) {
			fetcher := &HTTPDocumentFetcher{
				resolver: staticKnowledgeResolver{addresses: []netip.Addr{netip.MustParseAddr("93.184.216.34")}},
				dialer:   &net.Dialer{},
				client: &http.Client{Transport: knowledgeRoundTripper(func(request *http.Request) (*http.Response, error) {
					response.Request = request
					return response, nil
				})},
			}
			_, err := fetcher.FetchSource(t.Context(), IngestSource{
				ID: "source", URL: "https://example.test/page", Rank: 1,
			})
			if err == nil {
				t.Fatal("FetchSource() error = nil")
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
