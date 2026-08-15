package wasm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"fairy/plugin"
	"fairy/plugin/sdk"
	"fairy/plugin/testhost"
	"fairy/plugin/websearch"
)

func TestWebSearchPluginUsesHostHTTPAndReturnsBoundedSources(t *testing.T) {
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
				{"title": "来源一", "url": "https://one.example/item", "snippet": "公开摘要"},
				{"title": "来源二", "url": "https://two.example/item", "snippet": "第二条"},
				{"title": "来源三", "url": "https://three.example/item", "snippet": "第三条"},
			},
		})
	}))
	t.Cleanup(server.Close)

	grant := Grant{HTTPRequest: mustHTTPGrant(t, server.URL, 64<<10)}
	instance := loadProxy(t, grant)
	host := newWebSearchTestHost(t, instance)

	out, err := host.Invoke(t.Context(), webSearchEnvelope(t, server.URL, "新闻", 2))
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := sdk.Decode(out)
	if err != nil || parsed.Kind != "result" || parsed.Correlation.TraceID != "trace-search" {
		t.Fatalf("result = (%#v, %v)", parsed, err)
	}
	var payload struct {
		Tool   string                 `json:"tool"`
		Source string                 `json:"source"`
		Result websearch.SearchResult `json:"result"`
	}
	if err := json.Unmarshal(parsed.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Tool != websearch.ToolName || payload.Source != "plugin" || len(payload.Result.Sources) != 2 {
		t.Fatalf("payload = %#v", payload)
	}
	if payload.Result.Sources[0].Title != "来源一" || payload.Result.Sources[0].URL != "https://one.example/item" || payload.Result.Sources[0].Rank != 1 {
		t.Fatalf("sources = %#v", payload.Result.Sources)
	}
}

func TestWebSearchPluginDeniedHTTPDoesNotReturnEmptyHits(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("plugin reached network without grant: %s %s", r.Method, r.URL)
	}))
	t.Cleanup(server.Close)
	instance := loadProxy(t, Grant{})
	host := newWebSearchTestHost(t, instance)
	out, err := host.Invoke(t.Context(), webSearchEnvelope(t, server.URL, "新闻", 5))
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := sdk.Decode(out)
	if err != nil || parsed.Kind != "error" || parsed.Error == nil || parsed.Error.Code != plugin.CodeCapabilityDenied {
		t.Fatalf("denied = (%#v, %v)", parsed, err)
	}
}

func TestWebSearchPluginUnauthorizedHostIsDenied(t *testing.T) {
	allowed := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("authorized host was called for an unauthorized target")
	}))
	t.Cleanup(allowed.Close)
	grant := Grant{HTTPRequest: mustHTTPGrant(t, allowed.URL, 64<<10)}
	instance := loadProxy(t, grant)
	host := newWebSearchTestHost(t, instance)
	out, err := host.Invoke(t.Context(), webSearchEnvelope(t, "https://evil.example", "新闻", 5))
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := sdk.Decode(out)
	if err != nil || parsed.Kind != "error" || parsed.Error == nil || parsed.Error.Code != plugin.CodeCapabilityDenied {
		t.Fatalf("unauthorized = (%#v, %v)", parsed, err)
	}
}

func TestWebSearchPluginProviderFailureIsIsolated(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, "upstream secret")
	}))
	t.Cleanup(server.Close)
	grant := Grant{HTTPRequest: mustHTTPGrant(t, server.URL, 64<<10)}
	instance := loadProxy(t, grant)
	host := newWebSearchTestHost(t, instance)
	out, err := host.Invoke(t.Context(), webSearchEnvelope(t, server.URL, "新闻", 5))
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := sdk.Decode(out)
	if err != nil || parsed.Kind != "error" || parsed.Error == nil || parsed.Error.Code != plugin.CodeModuleTrap {
		t.Fatalf("provider failure = (%#v, %v)", parsed, err)
	}
	if strings.Contains(parsed.Error.Message, "secret") {
		t.Fatalf("error leaked provider body: %q", parsed.Error.Message)
	}

	echoHost, err := testhost.New(func(_ context.Context, envelope plugin.Envelope) (plugin.Envelope, error) {
		return envelope, nil
	}, testhost.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	echoRaw, err := sdk.Encode(plugin.Envelope{
		ABIVersion:  plugin.ABIVersion,
		Kind:        "handle",
		Correlation: plugin.Correlation{PluginInstanceID: "desktop-1", TraceID: "trace-desktop"},
		Payload:     json.RawMessage(`{"ok":true}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	echoOut, err := echoHost.Invoke(t.Context(), echoRaw)
	if err != nil {
		t.Fatal(err)
	}
	echoEnv, err := sdk.Decode(echoOut)
	if err != nil || echoEnv.Kind != "handle" || echoEnv.Correlation.TraceID != "trace-desktop" {
		t.Fatalf("desktop echo = (%#v, %v)", echoEnv, err)
	}
}

func newWebSearchTestHost(t *testing.T, instance *Instance) *testhost.Host {
	t.Helper()
	var host *testhost.Host
	host, err := testhost.New(websearch.NewHandler(func(ctx context.Context, capability string, payload json.RawMessage) ([]byte, error) {
		return host.Call(ctx, capability, payload)
	}), testhost.Options{
		MaxInputBytes: testhost.DefaultOptions().MaxInputBytes,
		MaxCalls:      testhost.DefaultOptions().MaxCalls,
		Capabilities:  []string{"http.request"},
		HostCall: func(ctx context.Context, capability string, payload json.RawMessage) ([]byte, error) {
			raw, err := sdk.EncodeHostRequest(capability, payload)
			if err != nil {
				return nil, err
			}
			return instance.Handle(ctx, raw)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return host
}

func webSearchEnvelope(t *testing.T, baseURL, query string, limit int) []byte {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"tool": websearch.ToolName,
		"arguments": map[string]any{
			"query": query,
			"limit": limit,
		},
		"config": map[string]any{"baseURL": baseURL},
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := sdk.Encode(plugin.Envelope{
		ABIVersion:  plugin.ABIVersion,
		Kind:        "handle",
		Correlation: plugin.Correlation{PluginInstanceID: "web-search-1", TraceID: "trace-search"},
		Payload:     payload,
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
