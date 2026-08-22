package openserp

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestAuthorityNormalizesAndRejectsNonOriginURLs(t *testing.T) {
	authority, err := NewAuthority("HTTPS://Example.COM:8443/")
	if err != nil {
		t.Fatal(err)
	}
	defer authority.Close()
	if authority.Origin() != "https://example.com:8443" {
		t.Fatalf("Origin() = %q", authority.Origin())
	}
	for _, invalid := range []string{
		"", "file:///tmp/openserp", "https://user:secret@example.com", "https://example.com/path",
		"https://example.com?query=1", "https://example.com#fragment", "https://例子.example",
	} {
		if _, err := NewAuthority(invalid); !errors.Is(err, ErrOriginInvalid) {
			t.Fatalf("NewAuthority(%q) error = %v, want %v", invalid, err, ErrOriginInvalid)
		}
	}
}

func TestAuthorityUsesFixedOpenSERPRoutesWithoutProxyOrRedirect(t *testing.T) {
	var redirected atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		redirected.Store(true)
	}))
	defer target.Close()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/health":
			if request.Method != http.MethodGet || request.URL.RawQuery != "" {
				t.Fatalf("health request = %s %s", request.Method, request.URL.String())
			}
			writer.WriteHeader(http.StatusNoContent)
		case "/mega/search":
			if request.Method != http.MethodGet || request.URL.Query().Get("text") != "猫 % 记忆" || request.URL.Query().Get("limit") != "3" {
				t.Fatalf("search request = %s %s", request.Method, request.URL.String())
			}
			writer.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(writer, `{"results":[]}`)
		case "/extract":
			query := request.URL.Query()
			if query.Get("url") != "https://source.example/article?x=1" || query.Get("mode") != "auto" ||
				query.Get("clean") != "true" || query.Get("use_llms_txt") != "false" || query.Get("format") != "text" {
				t.Fatalf("extract request = %s", request.URL.String())
			}
			writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = io.WriteString(writer, "bounded extracted text")
		case "/redirect":
			http.Redirect(writer, request, target.URL, http.StatusFound)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	authority, err := NewAuthority(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer authority.Close()
	if authority.transport.Proxy != nil {
		t.Fatal("authority transport inherited an environment proxy")
	}
	if response, err := authority.Health(t.Context()); err != nil || response.StatusCode != http.StatusNoContent {
		t.Fatalf("Health() = (%#v, %v)", response, err)
	}
	response, err := authority.Search(t.Context(), "猫 % 记忆", 3)
	if err != nil || response.StatusCode != http.StatusOK || string(response.Body) != `{"results":[]}` {
		t.Fatalf("Search() = (%#v, %v)", response, err)
	}
	response, err = authority.Extract(t.Context(), "https://source.example/article?x=1")
	if err != nil || response.StatusCode != http.StatusOK || string(response.Body) != "bounded extracted text" {
		t.Fatalf("Extract() = (%#v, %v)", response, err)
	}

	redirectURL := authority.origin
	redirectURL.Path = "/redirect"
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, redirectURL.String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authority.client.Do(request); !errors.Is(err, ErrRedirectDenied) {
		t.Fatalf("redirect error = %v, want %v", err, ErrRedirectDenied)
	}
	if redirected.Load() {
		t.Fatal("authority followed redirect outside OpenSERP origin")
	}
}

func TestAuthorityRejectsInvalidSearchBeforeNetwork(t *testing.T) {
	authority, err := newAuthority("http://openserp.test:7000", func(context.Context, string) ([]net.IPAddr, error) {
		t.Fatal("resolver called for invalid request")
		return nil, nil
	}, func(context.Context, string, string) (net.Conn, error) {
		t.Fatal("dialer called for invalid request")
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer authority.Close()
	for _, tc := range []struct {
		query string
		limit int
	}{
		{query: "", limit: 1},
		{query: strings.Repeat("界", MaxSearchQueryRunes+1), limit: 1},
		{query: "valid", limit: 0},
		{query: "valid", limit: MaxSearchHits + 1},
	} {
		if _, err := authority.Search(t.Context(), tc.query, tc.limit); !errors.Is(err, ErrCapabilityDenied) {
			t.Fatalf("Search(%q, %d) error = %v", tc.query, tc.limit, err)
		}
	}
	for _, target := range []string{"", "file:///tmp/source", "https://user:secret@example.com", "https://example.com/#fragment"} {
		if _, err := authority.Extract(t.Context(), target); !errors.Is(err, ErrCapabilityDenied) {
			t.Fatalf("Extract(%q) error = %v", target, err)
		}
	}
}

func TestAuthorityRejectsRedirectAndOversizedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/health" {
			http.Redirect(writer, request, "https://example.com/health", http.StatusFound)
			return
		}
		_, _ = io.WriteString(writer, strings.Repeat("x", MaxSearchResponseBytes+1))
	}))
	defer server.Close()
	authority, err := NewAuthority(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer authority.Close()
	if _, err := authority.Health(t.Context()); !errors.Is(err, ErrRedirectDenied) {
		t.Fatalf("Health() error = %v, want redirect denied", err)
	}
	if _, err := authority.Search(t.Context(), "bounded", 1); !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("Search() error = %v, want response too large", err)
	}
}

func TestPinnedDialerRejectsAddressChangeAndDifferentTarget(t *testing.T) {
	lookupCalls := 0
	lookup := func(context.Context, string) ([]net.IPAddr, error) {
		lookupCalls++
		if lookupCalls == 1 {
			return []net.IPAddr{{IP: net.ParseIP("192.0.2.1")}}, nil
		}
		return []net.IPAddr{{IP: net.ParseIP("192.0.2.2")}}, nil
	}
	dial := func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("fixture dial stopped")
	}
	authority, err := newAuthority("https://openserp.example:8443", lookup, dial)
	if err != nil {
		t.Fatal(err)
	}
	defer authority.Close()
	if _, err := authority.dialer.DialContext(t.Context(), "tcp", "openserp.example:8443"); err == nil || errors.Is(err, ErrAddressPolicy) {
		t.Fatalf("first dial error = %v, want fixture dial error after pin", err)
	}
	if _, err := authority.dialer.DialContext(t.Context(), "tcp", "openserp.example:8443"); !errors.Is(err, ErrAddressPolicy) {
		t.Fatalf("second dial error = %v, want address policy", err)
	}
	if _, err := authority.dialer.DialContext(t.Context(), "tcp", "other.example:8443"); !errors.Is(err, ErrAddressPolicy) {
		t.Fatalf("different target error = %v, want address policy", err)
	}
}

func TestAuthoritySearchAndExtractDialOnlyOpenSERPOrigin(t *testing.T) {
	dials := make(chan string, 4)
	requests := make(chan string, 4)
	authority, err := newAuthority(
		"http://openserp.example.test:7000",
		func(context.Context, string) ([]net.IPAddr, error) {
			return []net.IPAddr{{IP: net.ParseIP("192.0.2.40")}}, nil
		},
		func(_ context.Context, _, address string) (net.Conn, error) {
			dials <- address
			clientSide, serverSide := net.Pipe()
			go serveOpenSERPConnection(serverSide, requests)
			return clientSide, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer authority.Close()

	if _, err := authority.Search(t.Context(), "bounded query", 2); err != nil {
		t.Fatal(err)
	}
	resultURL := "https://result.example/private/article"
	if _, err := authority.Extract(t.Context(), resultURL); err != nil {
		t.Fatal(err)
	}

	if got := <-dials; got != "192.0.2.40:7000" {
		t.Fatalf("dial target = %q, want OpenSERP origin address", got)
	}
	select {
	case extra := <-dials:
		t.Fatalf("result URL or another authority caused extra dial %q", extra)
	default:
	}
	first, second := <-requests, <-requests
	if first != "/mega/search?limit=2&text=bounded+query" || second != "/extract?clean=true&format=text&mode=auto&url=https%3A%2F%2Fresult.example%2Fprivate%2Farticle&use_llms_txt=false" {
		t.Fatalf("OpenSERP request targets = %q, %q", first, second)
	}
}

func serveOpenSERPConnection(connection net.Conn, requests chan<- string) {
	defer connection.Close()
	reader := bufio.NewReader(connection)
	for {
		request, err := http.ReadRequest(reader)
		if err != nil {
			return
		}
		requests <- request.URL.RequestURI()
		body := "{}"
		contentType := "application/json"
		if request.URL.Path == "/extract" {
			body = "ok"
			contentType = "text/plain"
		}
		_, _ = io.WriteString(connection, "HTTP/1.1 200 OK\r\nContent-Type: "+contentType+"\r\nContent-Length: 2\r\n\r\n"+body)
	}
}
