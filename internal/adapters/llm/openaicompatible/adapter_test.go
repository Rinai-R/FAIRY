package openaicompatible

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Rinai-R/FAIRY/internal/llm"
)

func TestCompleteJSONCallsChatCompletions(t *testing.T) {
	t.Parallel()

	var gotPath string
	var gotAuth string
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("authorization")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("Decode request error = %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{
					"content": `{"ok":true}`,
				},
			}},
		})
	}))
	defer server.Close()

	content, err := NewAdapter(Options{
		Profile: llm.Profile{
			Endpoint: server.URL + "/v1",
			APIKey:   "secret-key",
			Model:    "deepseek-chat",
		},
	}).CompleteJSON(context.Background(), llm.Request{
		Profile: llm.Profile{
			ExtraBody: `{"reasoning_effort":"low"}`,
		},
		Messages: []llm.Message{
			{Role: "system", Content: "system"},
			{Role: "user", Content: "user"},
		},
		Temperature: 0.2,
	})
	if err != nil {
		t.Fatalf("CompleteJSON() error = %v", err)
	}
	if gotPath != "/v1/chat/completions" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotAuth != "Bearer secret-key" {
		t.Fatalf("authorization = %q", gotAuth)
	}
	if gotBody["model"] != "deepseek-chat" {
		t.Fatalf("model = %#v", gotBody["model"])
	}
	if gotBody["reasoning_effort"] != "low" {
		t.Fatalf("extra body missing: %#v", gotBody)
	}
	responseFormat, ok := gotBody["response_format"].(map[string]any)
	if !ok || responseFormat["type"] != "json_object" {
		t.Fatalf("response_format = %#v, want json_object", gotBody["response_format"])
	}
	if content != `{"ok":true}` {
		t.Fatalf("content = %q", content)
	}
}

func TestCompleteJSONDoesNotOverrideConfiguredResponseFormat(t *testing.T) {
	t.Parallel()

	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("Decode request error = %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{
					"content": `{"ok":true}`,
				},
			}},
		})
	}))
	defer server.Close()

	_, err := NewAdapter(Options{
		Profile: llm.Profile{
			Endpoint: server.URL,
			APIKey:   "secret-key",
			Model:    "deepseek-chat",
		},
	}).CompleteJSON(context.Background(), llm.Request{
		Profile: llm.Profile{
			ExtraBody: `{"response_format":{"type":"json_schema","json_schema":{"name":"act","schema":{"type":"object"}}}}`,
		},
		Messages: []llm.Message{{Role: "user", Content: "user"}},
	})
	if err != nil {
		t.Fatalf("CompleteJSON() error = %v", err)
	}
	responseFormat, ok := gotBody["response_format"].(map[string]any)
	if !ok || responseFormat["type"] != "json_schema" {
		t.Fatalf("response_format = %#v, want configured json_schema", gotBody["response_format"])
	}
}

func TestCompleteJSONAcceptsContentPartArray(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{
					"content": []map[string]any{{
						"type": "text",
						"text": `{"ok":true}`,
					}},
				},
			}},
		})
	}))
	defer server.Close()

	content, err := NewAdapter(Options{
		Profile: llm.Profile{
			Endpoint: server.URL,
			APIKey:   "secret-key",
			Model:    "deepseek-chat",
		},
	}).CompleteJSON(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: "user", Content: "user"}},
	})
	if err != nil {
		t.Fatalf("CompleteJSON() error = %v", err)
	}
	if content != `{"ok":true}` {
		t.Fatalf("content = %q", content)
	}
}

func TestCompleteJSONRejectsReasoningContentWithoutMessageContent(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{
					"content":           "",
					"reasoning_content": `{"ok":true}`,
				},
			}},
		})
	}))
	defer server.Close()

	_, err := NewAdapter(Options{
		Profile: llm.Profile{
			Endpoint: server.URL,
			APIKey:   "secret-key",
			Model:    "deepseek-chat",
		},
	}).CompleteJSON(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: "user", Content: "user"}},
	})
	if err == nil {
		t.Fatal("CompleteJSON() error = nil")
	}
	if !strings.Contains(err.Error(), "reasoning_content") {
		t.Fatalf("error = %q, want reasoning_content", err)
	}
}

func TestCompleteJSONReportsRefusal(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{
					"content": "",
					"refusal": "cannot comply",
				},
			}},
		})
	}))
	defer server.Close()

	_, err := NewAdapter(Options{
		Profile: llm.Profile{
			Endpoint: server.URL,
			APIKey:   "secret-key",
			Model:    "deepseek-chat",
		},
	}).CompleteJSON(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: "user", Content: "user"}},
	})
	if err == nil {
		t.Fatal("CompleteJSON() error = nil")
	}
	if !strings.Contains(err.Error(), "拒绝内容") {
		t.Fatalf("error = %q, want refusal", err)
	}
}

func TestValidateFailsExplicitlyWhenMissingFields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		profile llm.Profile
		want    string
	}{
		{name: "missing endpoint", profile: llm.Profile{APIKey: "secret", Model: "gpt"}, want: "endpoint 不能为空"},
		{name: "missing api key", profile: llm.Profile{Endpoint: "http://127.0.0.1:1", Model: "gpt"}, want: "api key 不能为空"},
		{name: "missing model", profile: llm.Profile{Endpoint: "http://127.0.0.1:1", APIKey: "secret"}, want: "model 不能为空"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := NewAdapter(Options{}).Validate(tt.profile)
			if err == nil {
				t.Fatal("Validate() error = nil")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %q, want %q", err, tt.want)
			}
		})
	}
}

func TestRemoteErrorDoesNotLeakAPIKey(t *testing.T) {
	t.Parallel()

	const secret = "very-secret-token"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream failed", http.StatusInternalServerError)
	}))
	defer server.Close()

	_, err := NewAdapter(Options{
		Profile: llm.Profile{
			Endpoint: server.URL,
			APIKey:   secret,
			Model:    "deepseek-chat",
		},
	}).CompleteJSON(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: "user", Content: "hello"}},
	})
	if err == nil {
		t.Fatal("CompleteJSON() error = nil")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error leaks api key: %q", err)
	}
}
