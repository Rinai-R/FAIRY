package model

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"fairy/runtime/config"
	"fairy/runtime/embedding"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

// APIEmbeddingOptions configures an OpenAI-compatible /embeddings backend.
type APIEmbeddingOptions struct {
	Provider   string
	Endpoint   string
	AuthMode   string
	BearerKey  string
	Model      string
	Dimensions int
	HTTPClient *http.Client
}

// APIEmbedder implements SemanticEmbedder through the official OpenAI Go
// SDK against an explicitly configured compatible provider.
type APIEmbedder struct {
	provider   string
	baseURL    string
	authMode   string
	bearerKey  string
	model      string
	spaceID    string
	dimensions int
	client     openai.Client
}

var (
	_ embedding.SemanticEmbedder        = (*APIEmbedder)(nil)
	_ embedding.ContextSemanticEmbedder = (*APIEmbedder)(nil)
)

func NewAPIEmbedder(options APIEmbeddingOptions) (*APIEmbedder, error) {
	provider := strings.TrimSpace(options.Provider)
	if provider != config.SemanticEmbeddingProviderSiliconFlow && provider != config.SemanticEmbeddingProviderOpenAICompatible {
		return nil, fmt.Errorf("semantic embedding provider %q is not supported", provider)
	}
	baseURL, err := embeddingBaseURL(options.Endpoint)
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
	bearerKey := strings.TrimSpace(options.BearerKey)
	if authMode == "bearer_key" && bearerKey == "" {
		return nil, errors.New("model bearer credential is required")
	}
	httpClient := options.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	clientOptions := []option.RequestOption{
		option.WithBaseURL(baseURL),
		option.WithAPIKey(bearerKey),
		option.WithHTTPClient(httpClient),
	}
	spaceID, err := embeddingSpaceID(provider, baseURL, model, dimensions)
	if err != nil {
		return nil, err
	}
	return &APIEmbedder{
		provider:   provider,
		baseURL:    baseURL,
		authMode:   authMode,
		bearerKey:  bearerKey,
		model:      model,
		spaceID:    spaceID,
		dimensions: dimensions,
		client:     openai.NewClient(clientOptions...),
	}, nil
}

func (e *APIEmbedder) Ready() bool {
	return e != nil && e.baseURL != "" && e.model != "" && e.dimensions == config.SemanticEmbeddingDimensions && (e.authMode == "no_auth" || e.bearerKey != "")
}

func (e *APIEmbedder) Status() embedding.SemanticStatus {
	if e.Ready() {
		return embedding.SemanticStatusReady
	}
	return embedding.SemanticStatusUnavailable
}

func (e *APIEmbedder) Dims() int {
	if e == nil {
		return 0
	}
	return e.dimensions
}

func (e *APIEmbedder) ModelID() string {
	if e == nil {
		return ""
	}
	return e.spaceID
}

func (e *APIEmbedder) Embed(texts []string) ([][]float32, error) {
	return e.EmbedContext(context.Background(), texts)
}

func (e *APIEmbedder) EmbedContext(ctx context.Context, texts []string) ([][]float32, error) {
	if ctx == nil {
		return nil, errors.New("embedding context is required")
	}
	if !e.Ready() {
		return nil, embedding.ErrSemanticUnavailable
	}
	if len(texts) == 0 {
		return [][]float32{}, nil
	}
	params := openai.EmbeddingNewParams{
		Input:          openai.EmbeddingNewParamsInputUnion{OfArrayOfStrings: texts},
		Model:          e.model,
		EncodingFormat: openai.EmbeddingNewParamsEncodingFormatFloat,
	}
	if e.provider == config.SemanticEmbeddingProviderOpenAICompatible {
		params.Dimensions = openai.Int(int64(e.dimensions))
	}
	response, err := e.client.Embeddings.New(ctx, params)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, sanitizeEmbeddingError(err, e.bearerKey, texts)
	}
	vectors, err := orderedSDKEmbeddingVectors(response.Data, len(texts), e.dimensions)
	if err != nil {
		return nil, sanitizeEmbeddingError(err, e.bearerKey, texts)
	}
	return vectors, nil
}

func orderedSDKEmbeddingVectors(data []openai.Embedding, wantCount int, wantDims int) ([][]float32, error) {
	if len(data) != wantCount {
		return nil, fmt.Errorf("embedding API returned %d vectors, want %d", len(data), wantCount)
	}
	vectors := make([][]float32, wantCount)
	seen := make([]bool, wantCount)
	for _, item := range data {
		if item.Index < 0 || item.Index >= int64(wantCount) {
			return nil, fmt.Errorf("embedding API returned index %d outside input range", item.Index)
		}
		index := int(item.Index)
		if seen[index] {
			return nil, fmt.Errorf("embedding API returned duplicate index %d", item.Index)
		}
		if len(item.Embedding) != wantDims {
			return nil, fmt.Errorf("embedding dimensions = %d, want %d", len(item.Embedding), wantDims)
		}
		vector := make([]float32, wantDims)
		for i, value := range item.Embedding {
			if math.IsNaN(value) || math.IsInf(value, 0) || math.Abs(value) > math.MaxFloat32 {
				return nil, fmt.Errorf("embedding API returned non-finite value at vector %d dimension %d", index, i)
			}
			vector[i] = float32(value)
		}
		seen[index] = true
		vectors[index] = vector
	}
	for i, ok := range seen {
		if !ok {
			return nil, fmt.Errorf("embedding API did not return index %d", i)
		}
	}
	return vectors, nil
}

func embeddingBaseURL(endpoint string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil {
		return "", fmt.Errorf("parsing model endpoint: %w", err)
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil {
		return "", errors.New("model endpoint must be an HTTP(S) URL without userinfo")
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
	parsed.Path = strings.TrimSuffix(parsed.Path, "/") + "/"
	return parsed.String(), nil
}

func embeddingSpaceID(provider, baseURL, model string, dimensions int) (string, error) {
	contract := struct {
		Version    int    `json:"version"`
		Provider   string `json:"provider"`
		BaseURL    string `json:"base_url"`
		Model      string `json:"model"`
		Dimensions int    `json:"dimensions"`
	}{Version: 1, Provider: provider, BaseURL: baseURL, Model: model, Dimensions: dimensions}
	raw, err := json.Marshal(contract)
	if err != nil {
		return "", fmt.Errorf("encoding semantic embedding space: %w", err)
	}
	sum := sha256.Sum256(raw)
	return "embedding-space-v1:" + hex.EncodeToString(sum[:]), nil
}

func sanitizeEmbeddingError(err error, bearerKey string, texts []string) error {
	if err == nil {
		return nil
	}
	message := scrubSecret(err, bearerKey).Error()
	if bearerKey != "" {
		message = strings.ReplaceAll(message, "Bearer "+bearerKey, "Bearer [REDACTED]")
		message = strings.ReplaceAll(message, "Authorization: Bearer [REDACTED]", "auth header [REDACTED]")
	}
	for _, text := range texts {
		if text != "" {
			message = strings.ReplaceAll(message, text, "[REDACTED INPUT]")
		}
	}
	message = strings.ReplaceAll(message, "Authorization", "auth header")
	return errors.New(message)
}
