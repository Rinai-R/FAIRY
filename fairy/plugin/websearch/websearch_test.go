package websearch_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"fairy/plugin"
	"fairy/plugin/sdk"
	"fairy/plugin/testhost"
	"fairy/plugin/websearch"
)

func TestManifestDeclaresHTTPRequestOnly(t *testing.T) {
	manifest := websearch.Manifest()
	if err := plugin.CheckCompatibility(plugin.ABIVersion, manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.ID != websearch.PluginID || len(manifest.Capabilities) != 1 || manifest.Capabilities[0] != "http.request" {
		t.Fatalf("manifest = %#v", manifest)
	}
	raw, err := os.ReadFile("manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	file, err := plugin.ParseManifest(bytes.NewReader(raw))
	if err != nil || file.ID != manifest.ID {
		t.Fatalf("manifest.json = (%#v, %v)", file, err)
	}
}

func TestParseHitsBoundsSourcesAndSkipsEmptyURLs(t *testing.T) {
	body, err := json.Marshal(map[string]any{
		"results": []map[string]any{
			{"title": "A", "url": "https://a.example", "snippet": "sa"},
			{"title": "missing", "url": "  ", "snippet": "drop"},
			{"title": "B", "url": "https://b.example", "snippet": "sb"},
			{"title": "C", "url": "https://c.example", "snippet": "sc"},
			{"title": "D", "url": "https://d.example", "snippet": "sd"},
			{"title": "E", "url": "https://e.example", "snippet": "se"},
			{"title": "F", "url": "https://f.example", "snippet": "sf"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	sources, err := websearch.ParseHits(body, 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != websearch.MaxHits {
		t.Fatalf("sources = %#v", sources)
	}
	if sources[0].URL != "https://a.example" || sources[0].Rank != 1 || sources[1].URL != "https://b.example" || sources[1].Rank != 2 {
		t.Fatalf("sources = %#v", sources)
	}
}

func TestWebSearchPluginReturnsSourcedHitsThroughHostHTTP(t *testing.T) {
	server := mockOpenSERP(t, http.StatusOK, []map[string]any{
		{"title": "最新情报", "url": "https://news.example/1", "snippet": "今天更新"},
		{"title": "第二来源", "url": "https://news.example/2", "snippet": "补充"},
	})
	host := newSearchHost(t, httpHostCall(http.DefaultClient))
	out, err := host.Invoke(t.Context(), searchEnvelope(t, server.URL, "test", 1))
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
	if payload.Tool != websearch.ToolName || payload.Source != "plugin" || payload.Result.Query != "test" {
		t.Fatalf("payload = %#v", payload)
	}
	if len(payload.Result.Sources) != 1 || payload.Result.Sources[0].Title != "最新情报" || payload.Result.Sources[0].URL != "https://news.example/1" || payload.Result.Sources[0].Rank != 1 {
		t.Fatalf("sources = %#v", payload.Result.Sources)
	}
}

func TestWebSearchPluginFailsClosedWithoutGrantOrProvider(t *testing.T) {
	denied, err := testhost.New(websearch.NewHandler(nil), testhost.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	out, err := denied.Invoke(t.Context(), searchEnvelope(t, "http://127.0.0.1:1", "test", 5))
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := sdk.Decode(out)
	if err != nil || parsed.Kind != "error" || parsed.Error == nil || parsed.Error.Code != plugin.CodeCapabilityDenied {
		t.Fatalf("denied = (%#v, %v)", parsed, err)
	}

	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, "boom")
	}))
	t.Cleanup(down.Close)
	host := newSearchHost(t, httpHostCall(http.DefaultClient))
	out, err = host.Invoke(t.Context(), searchEnvelope(t, down.URL, "test", 5))
	if err != nil {
		t.Fatal(err)
	}
	parsed, err = sdk.Decode(out)
	if err != nil || parsed.Kind != "error" || parsed.Error == nil || parsed.Error.Code != plugin.CodeModuleTrap {
		t.Fatalf("provider = (%#v, %v)", parsed, err)
	}
	if strings.Contains(parsed.Error.Message, "boom") {
		t.Fatalf("error leaked provider body: %q", parsed.Error.Message)
	}

	empty, err := testhost.New(websearch.NewHandler(httpHostCall(http.DefaultClient)), testhost.Options{
		MaxInputBytes: testhost.DefaultOptions().MaxInputBytes,
		MaxCalls:      testhost.DefaultOptions().MaxCalls,
		Capabilities:  []string{"http.request"},
		HostCall:      httpHostCall(http.DefaultClient),
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err = empty.Invoke(t.Context(), searchEnvelope(t, "http://127.0.0.1:1", "  ", 5))
	if err != nil {
		t.Fatal(err)
	}
	parsed, err = sdk.Decode(out)
	if err != nil || parsed.Kind != "error" || parsed.Error == nil || parsed.Error.Code != plugin.CodeManifestInvalid {
		t.Fatalf("empty query = (%#v, %v)", parsed, err)
	}
}

func TestWebSearchPluginFetchReturnsHostHTTPBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/item" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "公开正文")
	}))
	t.Cleanup(server.Close)
	host := newSearchHost(t, httpHostCall(http.DefaultClient))
	payload, err := json.Marshal(map[string]any{
		"tool":      websearch.FetchTool,
		"arguments": map[string]any{"url": server.URL + "/item"},
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := sdk.Encode(plugin.Envelope{
		ABIVersion:  plugin.ABIVersion,
		Kind:        "handle",
		Correlation: plugin.Correlation{PluginInstanceID: "web-search-1"},
		Payload:     payload,
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err := host.Invoke(t.Context(), raw)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := sdk.Decode(out)
	if err != nil || parsed.Kind != "result" {
		t.Fatalf("fetch = (%#v, %v)", parsed, err)
	}
	result, err := websearch.DecodeFetchResult(parsed.Payload)
	if err != nil || result.Status != 200 || result.Body != "公开正文" {
		t.Fatalf("fetch result = (%#v, %v)", result, err)
	}
}

func TestWebSearchPluginDoesNotImportHostNetworkOrCore(t *testing.T) {
	src, err := os.ReadFile("websearch.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{`"net/http"`, "fairy/context", "fairy/app", "fairy/agent", "fairy/runtime/wasm"} {
		if bytes.Contains(src, []byte(forbidden)) {
			t.Fatalf("websearch.go imports %s", forbidden)
		}
	}
}

func TestSearchEndpointUsesOpenSERPDuckPath(t *testing.T) {
	got, err := websearch.SearchEndpoint("http://127.0.0.1:7000/", "hello world", 3)
	if err != nil {
		t.Fatal(err)
	}
	if got != "http://127.0.0.1:7000/duck/search?text=hello+world&limit=3" {
		t.Fatalf("SearchEndpoint() = %q", got)
	}
	if _, err := websearch.SearchEndpoint("http://127.0.0.1:7000", "", 3); err == nil {
		t.Fatal("expected empty query error")
	}
}

func mockOpenSERP(t *testing.T, status int, hits []map[string]any) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.WriteHeader(status)
			return
		}
		if r.URL.Path != "/duck/search" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if status >= 200 && status < 300 {
			_ = json.NewEncoder(w).Encode(map[string]any{"results": hits})
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func newSearchHost(t *testing.T, call testhost.HostCall) *testhost.Host {
	t.Helper()
	var host *testhost.Host
	host, err := testhost.New(websearch.NewHandler(func(ctx context.Context, capability string, payload json.RawMessage) ([]byte, error) {
		return host.Call(ctx, capability, payload)
	}), testhost.Options{
		MaxInputBytes: testhost.DefaultOptions().MaxInputBytes,
		MaxCalls:      testhost.DefaultOptions().MaxCalls,
		Capabilities:  []string{"http.request"},
		HostCall:      call,
	})
	if err != nil {
		t.Fatal(err)
	}
	return host
}

func searchEnvelope(t *testing.T, baseURL, query string, limit int) []byte {
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

func httpHostCall(client *http.Client) testhost.HostCall {
	return func(ctx context.Context, capability string, payload json.RawMessage) ([]byte, error) {
		if capability != "http.request" {
			return nil, &plugin.CodedError{Code: plugin.CodeCapabilityDenied, Message: capability + ": not granted"}
		}
		var req struct {
			Method string `json:"method"`
			URL    string `json:"url"`
		}
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, &plugin.CodedError{Code: plugin.CodeCapabilityDenied, Message: "http.request: payload is invalid"}
		}
		httpReq, err := http.NewRequestWithContext(ctx, req.Method, req.URL, nil)
		if err != nil {
			return nil, &plugin.CodedError{Code: plugin.CodeModuleTrap, Message: "http.request: " + err.Error()}
		}
		resp, err := client.Do(httpReq)
		if err != nil {
			return nil, &plugin.CodedError{Code: plugin.CodeModuleTrap, Message: "http.request: " + err.Error()}
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if err != nil {
			return nil, &plugin.CodedError{Code: plugin.CodeModuleTrap, Message: "http.request: " + err.Error()}
		}
		inner, err := json.Marshal(map[string]any{
			"status":      resp.StatusCode,
			"contentType": resp.Header.Get("Content-Type"),
			"body":        string(body),
		})
		if err != nil {
			return nil, err
		}
		return json.Marshal(struct {
			OK   bool            `json:"ok"`
			Body json.RawMessage `json:"body"`
		}{OK: true, Body: inner})
	}
}
