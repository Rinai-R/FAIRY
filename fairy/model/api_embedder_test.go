package model

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"strings"
	"testing"

	"fairy/config"

	"github.com/openai/openai-go/v3"
)

const testSiliconFlowEmbeddingModel = "BAAI/bge-m3"

type embeddingRoundTripFunc func(*http.Request) (*http.Response, error)

func (f embeddingRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

type capturedEmbeddingRequest struct {
	Input          []string `json:"input"`
	Model          string   `json:"model"`
	EncodingFormat string   `json:"encoding_format"`
	Dimensions     *int     `json:"dimensions"`
}

type embeddingResponseItem struct {
	Embedding []float64 `json:"embedding"`
	Index     int64     `json:"index"`
	Object    string    `json:"object"`
}

func TestAPIEmbedderPostsSiliconFlowSDKRequestAndOrdersVectors(t *testing.T) {
	var capturedPath string
	var capturedAuth string
	var capturedBody capturedEmbeddingRequest
	client := embeddingTestClient(t, func(request *http.Request) (*http.Response, error) {
		capturedPath = request.URL.Path
		capturedAuth = request.Header.Get("Authorization")
		if err := json.NewDecoder(request.Body).Decode(&capturedBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		return embeddingJSONResponse(t, http.StatusOK, []embeddingResponseItem{
			{Index: 1, Embedding: testEmbeddingVector(2), Object: "embedding"},
			{Index: 0, Embedding: testEmbeddingVector(1), Object: "embedding"},
		}), nil
	})

	embedder := newEmbeddingTestEmbedder(t, "bearer_key", "sk-service-secret", client)
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
	if capturedBody.Model != testSiliconFlowEmbeddingModel || capturedBody.EncodingFormat != "float" {
		t.Fatalf("request body = %#v", capturedBody)
	}
	if capturedBody.Dimensions != nil {
		t.Fatalf("dimensions = %d, want field omitted for native BGE-M3 output", *capturedBody.Dimensions)
	}
	if len(capturedBody.Input) != 2 || capturedBody.Input[0] != "first" || capturedBody.Input[1] != "second" {
		t.Fatalf("request input = %#v", capturedBody.Input)
	}
	if len(vectors) != 2 || vectors[0][0] != 1 || vectors[1][0] != 2 {
		t.Fatalf("vectors order = %#v", []float32{vectors[0][0], vectors[1][0]})
	}
}

func TestAPIEmbedderNoAuthDoesNotSendAuthorization(t *testing.T) {
	client := embeddingTestClient(t, func(request *http.Request) (*http.Response, error) {
		if got := request.Header.Get("Authorization"); got != "" {
			t.Fatalf("Authorization = %q, want empty", got)
		}
		return embeddingJSONResponse(t, http.StatusOK, []embeddingResponseItem{
			{Index: 0, Embedding: testEmbeddingVector(1), Object: "embedding"},
		}), nil
	})

	embedder := newEmbeddingTestEmbedder(t, "no_auth", "", client)
	if _, err := embedder.Embed([]string{"hello"}); err != nil {
		t.Fatalf("Embed() error = %v", err)
	}
}

func TestAPIEmbedderRejectsInvalidBatchResponses(t *testing.T) {
	tests := []struct {
		name    string
		data    []embeddingResponseItem
		wantErr string
	}{
		{
			name:    "wrong count",
			data:    []embeddingResponseItem{},
			wantErr: "returned 0 vectors, want 1",
		},
		{
			name: "index outside input",
			data: []embeddingResponseItem{
				{Index: 1, Embedding: testEmbeddingVector(1), Object: "embedding"},
			},
			wantErr: "index 1 outside input range",
		},
		{
			name: "duplicate index",
			data: []embeddingResponseItem{
				{Index: 0, Embedding: testEmbeddingVector(1), Object: "embedding"},
				{Index: 0, Embedding: testEmbeddingVector(2), Object: "embedding"},
			},
			wantErr: "duplicate index 0",
		},
		{
			name: "wrong dimensions",
			data: []embeddingResponseItem{
				{Index: 0, Embedding: []float64{1, 2, 3}, Object: "embedding"},
			},
			wantErr: "dimensions = 3, want 1024",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := embeddingTestClient(t, func(*http.Request) (*http.Response, error) {
				return embeddingJSONResponse(t, http.StatusOK, test.data), nil
			})
			embedder := newEmbeddingTestEmbedder(t, "no_auth", "", client)
			inputs := []string{"hello"}
			if test.name == "duplicate index" {
				inputs = append(inputs, "world")
			}
			_, err := embedder.Embed(inputs)
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("Embed() error = %v, want substring %q", err, test.wantErr)
			}
		})
	}
}

func TestOrderedSDKEmbeddingVectorsRejectsNonFiniteValues(t *testing.T) {
	for _, test := range []struct {
		name  string
		value float64
	}{
		{name: "NaN", value: math.NaN()},
		{name: "positive infinity", value: math.Inf(1)},
		{name: "negative infinity", value: math.Inf(-1)},
		{name: "float32 overflow", value: math.MaxFloat64},
	} {
		t.Run(test.name, func(t *testing.T) {
			vector := testEmbeddingVector(1)
			vector[17] = test.value
			_, err := orderedSDKEmbeddingVectors([]openai.Embedding{
				{Index: 0, Embedding: vector},
			}, 1, config.SemanticEmbeddingDimensions)
			if err == nil || !strings.Contains(err.Error(), "non-finite value") {
				t.Fatalf("orderedSDKEmbeddingVectors() error = %v", err)
			}
		})
	}
}

func TestAPIEmbedderRejectsNon2xxAndMalformedResponses(t *testing.T) {
	for _, test := range []struct {
		name       string
		statusCode int
		body       string
		wantErr    string
	}{
		{
			name:       "provider error",
			statusCode: http.StatusBadGateway,
			body:       `{"error":{"message":"provider unavailable","type":"upstream_error"}}`,
			wantErr:    "502",
		},
		{
			name:       "malformed response",
			statusCode: http.StatusOK,
			body:       `{"data":[`,
			wantErr:    "unexpected EOF",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := embeddingTestClient(t, func(*http.Request) (*http.Response, error) {
				return embeddingRawResponse(test.statusCode, test.body), nil
			})
			embedder := newEmbeddingTestEmbedder(t, "no_auth", "", client)
			_, err := embedder.Embed([]string{"hello"})
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(test.wantErr)) {
				t.Fatalf("Embed() error = %v, want substring %q", err, test.wantErr)
			}
		})
	}
}

func TestAPIEmbedderErrorsDoNotLeakSecretsOrInput(t *testing.T) {
	const secret = "sk-unique-leaky-secret"
	const input = "unique private memory body"
	client := embeddingTestClient(t, func(*http.Request) (*http.Response, error) {
		body := `{"error":{"message":"bad Authorization: Bearer ` + secret + ` for ` + input + `"}}`
		return embeddingRawResponse(http.StatusUnauthorized, body), nil
	})

	embedder := newEmbeddingTestEmbedder(t, "bearer_key", secret, client)
	_, err := embedder.Embed([]string{input})
	if err == nil {
		t.Fatal("Embed() error = nil, want failure")
	}
	message := err.Error()
	for _, sensitive := range []string{secret, input, "Authorization"} {
		if strings.Contains(message, sensitive) {
			t.Fatalf("error leaked %q: %v", sensitive, err)
		}
	}
	if !strings.Contains(message, "401") {
		t.Fatalf("error = %v, want diagnostic HTTP status", err)
	}
}

func TestAPIEmbedderReturnsContextCancellation(t *testing.T) {
	client := embeddingTestClient(t, func(request *http.Request) (*http.Response, error) {
		<-request.Context().Done()
		return nil, request.Context().Err()
	})
	embedder := newEmbeddingTestEmbedder(t, "no_auth", "", client)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := embedder.EmbedContext(ctx, []string{"hello"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("EmbedContext() error = %v, want context.Canceled", err)
	}
}

func TestAPIEmbedderRejectsResourceEndpoint(t *testing.T) {
	_, err := NewAPIEmbedder(APIEmbeddingOptions{
		Endpoint:   "https://example.test/v1/chat/completions",
		AuthMode:   "no_auth",
		Model:      testSiliconFlowEmbeddingModel,
		Dimensions: config.SemanticEmbeddingDimensions,
	})
	if err == nil || !strings.Contains(err.Error(), "base URL") {
		t.Fatalf("NewAPIEmbedder() error = %v, want base URL error", err)
	}
}

func newEmbeddingTestEmbedder(t *testing.T, authMode string, bearerKey string, client *http.Client) *APIEmbedder {
	t.Helper()
	embedder, err := NewAPIEmbedder(APIEmbeddingOptions{
		Endpoint:   "https://api.siliconflow.test/v1",
		AuthMode:   authMode,
		BearerKey:  bearerKey,
		Model:      testSiliconFlowEmbeddingModel,
		Dimensions: config.SemanticEmbeddingDimensions,
		HTTPClient: client,
	})
	if err != nil {
		t.Fatalf("NewAPIEmbedder() error = %v", err)
	}
	return embedder
}

func embeddingTestClient(t *testing.T, roundTrip embeddingRoundTripFunc) *http.Client {
	t.Helper()
	return &http.Client{Transport: roundTrip}
}

func embeddingJSONResponse(t *testing.T, statusCode int, data []embeddingResponseItem) *http.Response {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"data":   data,
		"model":  testSiliconFlowEmbeddingModel,
		"object": "list",
		"usage": map[string]int{
			"prompt_tokens": 1,
			"total_tokens":  1,
		},
	})
	if err != nil {
		t.Fatalf("encode response: %v", err)
	}
	return embeddingRawResponse(statusCode, string(body))
}

func embeddingRawResponse(statusCode int, body string) *http.Response {
	return &http.Response{
		StatusCode: statusCode,
		Status:     http.StatusText(statusCode),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewBufferString(body)),
	}
}

func testEmbeddingVector(seed float64) []float64 {
	vector := make([]float64, config.SemanticEmbeddingDimensions)
	vector[0] = seed
	return vector
}
