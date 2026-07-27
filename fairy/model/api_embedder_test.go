package model

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"fairy/config"
)

func TestAPIEmbedderPostsOpenAICompatibleEmbeddingsAndOrdersVectors(t *testing.T) {
	var capturedPath string
	var capturedAuth string
	var capturedBody embeddingRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		capturedAuth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&capturedBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		writeEmbeddingResponse(t, w, []embeddingResponseData{
			{Index: 1, Embedding: testEmbeddingVector(2)},
			{Index: 0, Embedding: testEmbeddingVector(1)},
		})
	}))
	defer server.Close()

	embedder, err := NewAPIEmbedder(APIEmbeddingOptions{
		Endpoint:   server.URL + "/v1/",
		AuthMode:   "bearer_key",
		BearerKey:  "sk-service-secret",
		Model:      "text-embedding-3-small",
		Dimensions: config.SemanticEmbeddingDimensions,
		HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatalf("NewAPIEmbedder() error = %v", err)
	}
	vectors, err := embedder.Embed([]string{"first", "second"})
	if err != nil {
		t.Fatalf("Embed() error = %v", err)
	}
	if capturedPath != "/v1/embeddings" {
		t.Fatalf("path = %q, want /v1/embeddings", capturedPath)
	}
	if capturedAuth != "Bearer sk-service-secret" {
		t.Fatalf("Authorization = %q", capturedAuth)
	}
	if capturedBody.Model != "text-embedding-3-small" || capturedBody.Dimensions != config.SemanticEmbeddingDimensions || capturedBody.EncodingFormat != "float" {
		t.Fatalf("request body = %#v", capturedBody)
	}
	if len(capturedBody.Input) != 2 || capturedBody.Input[0] != "first" || capturedBody.Input[1] != "second" {
		t.Fatalf("request input = %#v", capturedBody.Input)
	}
	if len(vectors) != 2 || vectors[0][0] != 1 || vectors[1][0] != 2 {
		t.Fatalf("vectors order = %#v", []float32{vectors[0][0], vectors[1][0]})
	}
}

func TestAPIEmbedderNoAuthDoesNotSendAuthorization(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "" {
			t.Fatalf("Authorization = %q, want empty", got)
		}
		writeEmbeddingResponse(t, w, []embeddingResponseData{{Index: 0, Embedding: testEmbeddingVector(1)}})
	}))
	defer server.Close()

	embedder, err := NewAPIEmbedder(APIEmbeddingOptions{
		Endpoint:   server.URL,
		AuthMode:   "no_auth",
		Model:      "text-embedding-3-small",
		Dimensions: config.SemanticEmbeddingDimensions,
		HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatalf("NewAPIEmbedder() error = %v", err)
	}
	if _, err := embedder.Embed([]string{"hello"}); err != nil {
		t.Fatalf("Embed() error = %v", err)
	}
}

func TestAPIEmbedderInvalidDimensionsFail(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeEmbeddingResponse(t, w, []embeddingResponseData{{Index: 0, Embedding: []float32{1, 2, 3}}})
	}))
	defer server.Close()

	embedder, err := NewAPIEmbedder(APIEmbeddingOptions{
		Endpoint:   server.URL,
		AuthMode:   "no_auth",
		Model:      "text-embedding-3-small",
		Dimensions: config.SemanticEmbeddingDimensions,
		HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatalf("NewAPIEmbedder() error = %v", err)
	}
	_, err = embedder.Embed([]string{"hello"})
	if err == nil || !strings.Contains(err.Error(), "dimensions = 3, want 512") {
		t.Fatalf("Embed() error = %v, want dimensions failure", err)
	}
}

func TestAPIEmbedderErrorsDoNotLeakSecrets(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"bad Authorization: Bearer sk-leaky-secret"}}`))
	}))
	defer server.Close()

	embedder, err := NewAPIEmbedder(APIEmbeddingOptions{
		Endpoint:   server.URL,
		AuthMode:   "bearer_key",
		BearerKey:  "sk-leaky-secret",
		Model:      "text-embedding-3-small",
		Dimensions: config.SemanticEmbeddingDimensions,
		HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatalf("NewAPIEmbedder() error = %v", err)
	}
	_, err = embedder.Embed([]string{"hello"})
	if err == nil {
		t.Fatal("Embed() error = nil, want failure")
	}
	if strings.Contains(err.Error(), "sk-leaky-secret") || strings.Contains(err.Error(), "Authorization") {
		t.Fatalf("error leaked secret/header: %v", err)
	}
}

func TestAPIEmbedderRejectsResourceEndpoint(t *testing.T) {
	_, err := NewAPIEmbedder(APIEmbeddingOptions{
		Endpoint:   "https://example.test/v1/chat/completions",
		AuthMode:   "no_auth",
		Model:      "text-embedding-3-small",
		Dimensions: config.SemanticEmbeddingDimensions,
	})
	if err == nil || !strings.Contains(err.Error(), "base URL") {
		t.Fatalf("NewAPIEmbedder() error = %v, want base URL error", err)
	}
}

func writeEmbeddingResponse(t *testing.T, w http.ResponseWriter, data []embeddingResponseData) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(embeddingResponse{Data: data}); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}

func testEmbeddingVector(seed float32) []float32 {
	vector := make([]float32, config.SemanticEmbeddingDimensions)
	vector[0] = seed
	return vector
}
