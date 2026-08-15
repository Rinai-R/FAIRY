package knowledge

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"strings"
	"testing"

	"fairy/plugin"
	"fairy/plugin/sdk"
	"fairy/plugin/websearch"
)

func TestPluginToolsSearchValidatesSchemaAndStaysOfflineWithoutPlugin(t *testing.T) {
	if _, err := (*PluginTools)(nil).Search(t.Context(), "q", 5); !errors.Is(err, ErrPluginCapabilityUnavailable) {
		t.Fatalf("nil plugin = %v", err)
	}
	unavailable := &PluginTools{Ready: true, InstanceID: "web-1", BaseURL: "http://127.0.0.1:7000"}
	if _, err := unavailable.Search(t.Context(), "q", 5); !errors.Is(err, ErrPluginCapabilityUnavailable) {
		t.Fatalf("missing invoker = %v", err)
	}
	if _, err := (UnavailableDocumentFetcher{}).FetchSource(t.Context(), IngestSource{URL: "https://example.test"}); !errors.Is(err, ErrPluginCapabilityUnavailable) {
		t.Fatalf("unavailable fetch = %v", err)
	}

	tools := &PluginTools{
		Ready:      true,
		InstanceID: "web-1",
		BaseURL:    "http://127.0.0.1:7000",
		SearchInvoke: func(_ context.Context, raw []byte) ([]byte, error) {
			envelope, err := sdk.Decode(raw)
			if err != nil {
				t.Fatal(err)
			}
			return sdk.EncodeResult(envelope.Correlation, json.RawMessage(`{"tool":"web_search","source":"plugin","result":{"query":"q","sources":[{"title":"来源","url":"https://news.example/1","snippet":"摘要","rank":1}]}}`))
		},
	}
	hits, err := tools.Search(t.Context(), "q", 5)
	if err != nil || len(hits) != 1 || hits[0].URL != "https://news.example/1" {
		t.Fatalf("hits = (%#v, %v)", hits, err)
	}

	tools.SearchInvoke = func(_ context.Context, raw []byte) ([]byte, error) {
		envelope, err := sdk.Decode(raw)
		if err != nil {
			t.Fatal(err)
		}
		return sdk.EncodeResult(envelope.Correlation, json.RawMessage(`{"tool":"web_search","source":"plugin","knowledgeEntries":[{"id":"k1"}],"result":{"query":"q","sources":[]}}`))
	}
	if _, err := tools.Search(t.Context(), "q", 5); err == nil || !strings.Contains(err.Error(), "knowledge") {
		t.Fatalf("schema = %v", err)
	}
}

func TestPluginToolsFetchUsesPluginResultAndDoesNotCallNetworkOnDeny(t *testing.T) {
	tools := &PluginTools{
		Ready:      true,
		InstanceID: "web-1",
		BaseURL:    "http://127.0.0.1:7000",
		Resolver:   staticKnowledgeResolver{addresses: []netip.Addr{netip.MustParseAddr("203.0.113.10")}},
		NewFetch: func(string) (EnvelopeInvoker, error) {
			return func(_ context.Context, raw []byte) ([]byte, error) {
				envelope, err := sdk.Decode(raw)
				if err != nil {
					t.Fatal(err)
				}
				return sdk.Encode(plugin.NewError(envelope.Correlation, plugin.CodeCapabilityDenied, "http.request: not granted"))
			}, nil
		},
	}
	if _, err := tools.FetchSource(t.Context(), IngestSource{ID: "src", URL: "https://news.example/item", Title: "新闻"}); !errors.Is(err, ErrPluginCapabilityUnavailable) {
		t.Fatalf("denied fetch = %v", err)
	}

	tools.NewFetch = func(string) (EnvelopeInvoker, error) {
		return func(_ context.Context, raw []byte) ([]byte, error) {
			envelope, err := sdk.Decode(raw)
			if err != nil {
				t.Fatal(err)
			}
			body, err := json.Marshal(map[string]any{
				"tool":   websearch.FetchTool,
				"source": "plugin",
				"result": map[string]any{
					"url":         "https://news.example/item",
					"status":      200,
					"contentType": "text/plain",
					"body":        "公开正文",
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			return sdk.EncodeResult(envelope.Correlation, body)
		}, nil
	}
	document, err := tools.FetchSource(t.Context(), IngestSource{ID: "src", URL: "https://news.example/item", Title: "新闻"})
	if err != nil || document.Content != "公开正文" || document.CanonicalURL != "https://news.example/item" {
		t.Fatalf("document = (%#v, %v)", document, err)
	}
}

func TestWebSearchDiscoverRequiresEnabledHTTPGrant(t *testing.T) {
	if _, ok := websearch.Discover(nil); ok {
		t.Fatal("empty instances were discovered")
	}
	id, ok := websearch.Discover([]plugin.InstanceRecord{{
		ID: "web-1", PluginID: websearch.PluginID, Enabled: true, Lifecycle: "ready",
		CapabilityGrants: []string{"http.request"},
	}})
	if !ok || id != "web-1" {
		t.Fatalf("discover = (%q, %v)", id, ok)
	}
	if _, ok := websearch.Discover([]plugin.InstanceRecord{{
		ID: "web-1", PluginID: websearch.PluginID, Enabled: true, Lifecycle: "ready",
	}}); ok {
		t.Fatal("ungranted instance was discovered")
	}
}
