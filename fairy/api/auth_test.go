package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	fairyruntime "fairy/runtime"
	"go.uber.org/zap"
)

func TestNewServerRequiresAPIToken(t *testing.T) {
	rt := &fairyruntime.Runtime{Logger: zap.NewNop()}
	if _, err := NewServer(rt, Options{Addr: "127.0.0.1:0", Token: ""}); err == nil || !strings.Contains(err.Error(), "FAIRY_API_TOKEN") {
		t.Fatalf("NewServer(empty token) error = %v", err)
	}
	if _, err := NewServer(rt, Options{Addr: "127.0.0.1:0", Token: "  secret  "}); err == nil || !strings.Contains(err.Error(), "whitespace") {
		t.Fatalf("NewServer(padded token) error = %v", err)
	}
	server, err := NewServer(rt, Options{Addr: "127.0.0.1:0", Token: "secret", Logger: zap.NewNop()})
	if err != nil {
		t.Fatal(err)
	}
	if server.token != "secret" {
		t.Fatalf("token = %q", server.token)
	}
}

func TestAllowedSessionOrigin(t *testing.T) {
	cases := []struct {
		origin string
		want   bool
	}{
		{origin: "", want: true},
		{origin: "http://127.0.0.1", want: true},
		{origin: "http://127.0.0.1:8787", want: true},
		{origin: "http://localhost:8787", want: true},
		{origin: "https://evil.example", want: false},
		{origin: "http://example.com", want: false},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/v1/session/ws", nil)
		if tc.origin != "" {
			req.Header.Set("Origin", tc.origin)
		}
		if got := allowedSessionOrigin(req); got != tc.want {
			t.Fatalf("origin %q = %v, want %v", tc.origin, got, tc.want)
		}
		if got := sessionUpgrader.CheckOrigin(req); got != tc.want {
			t.Fatalf("CheckOrigin(%q) = %v, want %v", tc.origin, got, tc.want)
		}
	}
}
