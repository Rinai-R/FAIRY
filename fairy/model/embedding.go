package model

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"fairy/config"
	"fairy/memory/semantic"
)

const embeddingEncodingFormatFloat = "float"

// APIEmbeddingOptions configures an OpenAI-compatible /embeddings backend.
type APIEmbeddingOptions struct {
	Endpoint   string
	AuthMode   string
	BearerKey  string
	Model      string
	Dimensions int
	HTTPClient *http.Client
}

// APIEmbedder implements semantic.Embedder through an OpenAI-compatible
// embeddings endpoint. It is optional and only constructed when settings are
// explicitly enabled by the app composition root.
type APIEmbedder struct {
	url        string
	authMode   string
	bearerKey  string
	model      string
	dimensions int
	client     *http.Client
}

var _ semantic.Embedder = (*APIEmbedder)(nil)

func NewAPIEmbedder(options APIEmbeddingOptions) (*APIEmbedder, error) {
	endpointURL, err := embeddingsURL(options.Endpoint)
	if err != nil {
		return nil, err
	}
	model := strings.TrimSpace(options.Model)
	if model == "" {
		return nil, errors.New("semantic embedding model is required")
	}
	dimensions := options.Dimensions
	if dimensions == 0 {
		dimensions = config.SemanticEmbeddingDimensions
	}
	if dimensions != config.SemanticEmbeddingDimensions {
		return nil, fmt.Errorf("semantic embedding dimensions = %d, want %d", dimensions, config.SemanticEmbeddingDimensions)
	}
	authMode := strings.TrimSpace(options.AuthMode)
	if authMode != "bearer_key" && authMode != "no_auth" {
		return nil, fmt.Errorf("model auth mode %q is not supported", authMode)
	}
	if authMode == "bearer_key" && strings.TrimSpace(options.BearerKey) == "" {
		return nil, errors.New("model bearer credential is required")
	}
	client := options.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &APIEmbedder{
		url:        endpointURL,
		authMode:   authMode,
		bearerKey:  strings.TrimSpace(options.BearerKey),
		model:      model,
		dimensions: dimensions,
		client:     client,
	}, nil
}

func (s *ModelService) SemanticAPIEmbedder(settings config.SemanticEmbeddingSettings) (*APIEmbedder, error) {
	if !settings.Enabled {
		return nil, errors.New("semantic embedding settings are disabled")
	}
	connection, err := config.ReadModelConnection(s.root)
	if err != nil {
		return nil, fmt.Errorf("reading model connection: %w", err)
	}
	bearerKey, err := s.bearerCredential(connection)
	if err != nil {
		return nil, err
	}
	return NewAPIEmbedder(APIEmbeddingOptions{
		Endpoint:   connection.Endpoint,
		AuthMode:   connection.AuthMode,
		BearerKey:  bearerKey,
		Model:      settings.Model,
		Dimensions: settings.Dimensions,
	})
}

func (e *APIEmbedder) Ready() bool {
	return e != nil && e.url != "" && e.model != "" && e.dimensions == config.SemanticEmbeddingDimensions
}

func (e *APIEmbedder) Status() semantic.Status {
	if e.Ready() {
		return semantic.StatusReady
	}
	return semantic.StatusUnavailable
}

func (e *APIEmbedder) Dims() int {
	if e == nil {
		return 0
	}
	return e.dimensions
}

func (e *APIEmbedder) Embed(texts []string) ([][]float32, error) {
	return e.EmbedContext(context.Background(), texts)
}

func (e *APIEmbedder) EmbedContext(ctx context.Context, texts []string) ([][]float32, error) {
	if ctx == nil {
		return nil, errors.New("embedding context is required")
	}
	if !e.Ready() {
		return nil, semantic.ErrUnavailable
	}
	if len(texts) == 0 {
		return [][]float32{}, nil
	}
	body, err := json.Marshal(embeddingRequest{
		Model:          e.model,
		Input:          texts,
		Dimensions:     e.dimensions,
		EncodingFormat: embeddingEncodingFormatFloat,
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.url, bytes.NewReader(body))
	if err != nil {
		return nil, sanitizeEmbeddingError(err, e.bearerKey)
	}
	req.Header.Set("Content-Type", "application/json")
	if e.authMode == "bearer_key" {
		req.Header.Set("Authorization", "Bearer "+e.bearerKey)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return nil, sanitizeEmbeddingError(fmt.Errorf("calling embedding API: %w", err), e.bearerKey)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, sanitizeEmbeddingError(fmt.Errorf("reading embedding API response: %w", err), e.bearerKey)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, sanitizeEmbeddingError(fmt.Errorf("embedding API status %d: %s", resp.StatusCode, embeddingAPIErrorMessage(raw)), e.bearerKey)
	}
	var decoded embeddingResponse
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, sanitizeEmbeddingError(fmt.Errorf("parsing embedding API response: %w", err), e.bearerKey)
	}
	vectors, err := decoded.orderedVectors(len(texts), e.dimensions)
	if err != nil {
		return nil, sanitizeEmbeddingError(err, e.bearerKey)
	}
	return vectors, nil
}

type embeddingRequest struct {
	Model          string   `json:"model"`
	Input          []string `json:"input"`
	Dimensions     int      `json:"dimensions"`
	EncodingFormat string   `json:"encoding_format"`
}

type embeddingResponse struct {
	Data []embeddingResponseData `json:"data"`
}

type embeddingResponseData struct {
	Index     int       `json:"index"`
	Embedding []float32 `json:"embedding"`
}

func (r embeddingResponse) orderedVectors(wantCount int, wantDims int) ([][]float32, error) {
	if len(r.Data) != wantCount {
		return nil, fmt.Errorf("embedding API returned %d vectors, want %d", len(r.Data), wantCount)
	}
	vectors := make([][]float32, wantCount)
	seen := make([]bool, wantCount)
	for _, item := range r.Data {
		if item.Index < 0 || item.Index >= wantCount {
			return nil, fmt.Errorf("embedding API returned index %d outside input range", item.Index)
		}
		if seen[item.Index] {
			return nil, fmt.Errorf("embedding API returned duplicate index %d", item.Index)
		}
		if len(item.Embedding) != wantDims {
			return nil, fmt.Errorf("embedding dimensions = %d, want %d", len(item.Embedding), wantDims)
		}
		seen[item.Index] = true
		vectors[item.Index] = item.Embedding
	}
	for i, ok := range seen {
		if !ok {
			return nil, fmt.Errorf("embedding API did not return index %d", i)
		}
	}
	return vectors, nil
}

func embeddingsURL(endpoint string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil {
		return "", fmt.Errorf("parsing model endpoint: %w", err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return "", errors.New("model endpoint must include scheme and host")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("model endpoint must not include query or fragment")
	}
	segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	last := ""
	if len(segments) > 0 {
		last = segments[len(segments)-1]
	}
	if last == "responses" || last == "embeddings" || (len(segments) >= 2 && segments[len(segments)-2] == "chat" && last == "completions") {
		return "", errors.New("model endpoint must be a base URL, not a protocol resource URL")
	}
	parsed.Path = "/" + path.Join(strings.Trim(parsed.Path, "/"), "embeddings")
	return parsed.String(), nil
}

func embeddingAPIErrorMessage(raw []byte) string {
	var decoded struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &decoded); err == nil && strings.TrimSpace(decoded.Error.Message) != "" {
		return strings.TrimSpace(decoded.Error.Message)
	}
	message := strings.TrimSpace(string(raw))
	if message == "" {
		return "empty response"
	}
	return message
}

func sanitizeEmbeddingError(err error, bearerKey string) error {
	if err == nil {
		return nil
	}
	message := scrubSecret(err, bearerKey).Error()
	if bearerKey != "" {
		message = strings.ReplaceAll(message, "Bearer "+bearerKey, "Bearer [REDACTED]")
		message = strings.ReplaceAll(message, "Authorization: Bearer [REDACTED]", "auth header [REDACTED]")
	}
	message = strings.ReplaceAll(message, "Authorization", "auth header")
	return errors.New(message)
}
