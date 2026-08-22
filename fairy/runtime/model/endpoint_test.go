package model

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func endpointTestProviderURL(t *testing.T, server *httptest.Server, suffix string) string {
	t.Helper()
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	return parsed.Scheme + "://provider.example:" + parsed.Port() + suffix
}

func endpointTestClientFactory(server *httptest.Server) endpointProviderClientFactory {
	return func(endpoint string, timeout time.Duration) (*http.Client, error) {
		return newEndpointProviderClient(
			endpoint,
			timeout,
			func(context.Context, string) ([]net.IPAddr, error) {
				return []net.IPAddr{{IP: net.ParseIP("203.0.113.7")}}, nil
			},
			func(ctx context.Context, _, _ string) (net.Conn, error) {
				var dialer net.Dialer
				return dialer.DialContext(ctx, server.Listener.Addr().Network(), server.Listener.Addr().String())
			},
		)
	}
}
