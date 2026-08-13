package embedding

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/pgvector/pgvector-go"
)

const Dimensions = 1024

// EmbeddingValue is either completely disabled (all zero values) or a complete
// PostgreSQL vector projection for one authoritative record.
type EmbeddingValue struct {
	ModelID     string
	ContentHash string
	Vector      *pgvector.Vector
}

func (value EmbeddingValue) Enabled() bool {
	return value.ModelID != "" || value.ContentHash != "" || value.Vector != nil
}

func (value EmbeddingValue) Validate() error {
	if !value.Enabled() {
		return nil
	}
	if value.ModelID == "" || strings.TrimSpace(value.ModelID) != value.ModelID || containsDisallowedControl(value.ModelID) {
		return errors.New("embedding model id is invalid")
	}
	if !validContentHash(value.ContentHash) {
		return errors.New("embedding content hash is invalid")
	}
	if value.Vector == nil {
		return errors.New("embedding vector is required")
	}
	return ValidateVector(value.Vector.Slice())
}

func ForContent(embedder SemanticEmbedder, content string) (EmbeddingValue, error) {
	values, err := ForContents(embedder, []string{content})
	if err != nil {
		return EmbeddingValue{}, err
	}
	return values[0], nil
}

func ForContents(embedder SemanticEmbedder, contents []string) ([]EmbeddingValue, error) {
	return forContents(context.Background(), embedder, contents, false)
}

// ForContentsContext snapshots one provider and prepares a consistent batch.
// It fails closed before invoking a legacy provider so every background owner
// can bound cancellation and shutdown.
func ForContentsContext(
	ctx context.Context,
	embedder SemanticEmbedder,
	contents []string,
) ([]EmbeddingValue, error) {
	return forContents(ctx, embedder, contents, true)
}

func forContents(
	ctx context.Context,
	embedder SemanticEmbedder,
	contents []string,
	requireContext bool,
) ([]EmbeddingValue, error) {
	if ctx == nil {
		return nil, errors.New("embedding context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(contents) == 0 {
		return nil, nil
	}
	embedder = Snapshot(embedder)
	if embedder == nil {
		return make([]EmbeddingValue, len(contents)), nil
	}
	if !embedder.Ready() {
		return nil, ErrSemanticUnavailable
	}
	if dims := embedder.Dims(); dims != Dimensions {
		return nil, fmt.Errorf("embedding dimensions = %d, want %d", dims, Dimensions)
	}
	modelID, err := ModelID(embedder)
	if err != nil {
		return nil, err
	}
	var vectors [][]float32
	if contextual, ok := embedder.(ContextSemanticEmbedder); ok {
		vectors, err = contextual.EmbedContext(ctx, contents)
	} else if requireContext {
		return nil, ErrSemanticCancellationUnsupported
	} else {
		vectors, err = embedder.Embed(contents)
	}
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("embedding content: %w", err)
	}
	if len(vectors) != len(contents) {
		return nil, fmt.Errorf("embedding result count = %d, want %d", len(vectors), len(contents))
	}
	values := make([]EmbeddingValue, len(contents))
	for index, vectorSlice := range vectors {
		if err := ValidateVector(vectorSlice); err != nil {
			return nil, fmt.Errorf("validating embedding[%d]: %w", index, err)
		}
		vector := pgvector.NewVector(vectorSlice)
		values[index] = EmbeddingValue{
			ModelID:     modelID,
			ContentHash: ContentHash(contents[index]),
			Vector:      &vector,
		}
		if err := values[index].Validate(); err != nil {
			return nil, fmt.Errorf("validating embedding[%d]: %w", index, err)
		}
	}
	return values, nil
}

func ModelID(embedder SemanticEmbedder) (string, error) {
	if embedder == nil {
		return "", ErrSemanticUnavailable
	}
	modelID := embedder.ModelID()
	if modelID == "" || strings.TrimSpace(modelID) != modelID || containsDisallowedControl(modelID) {
		return "", errors.New("embedding model id is invalid")
	}
	return modelID, nil
}

func ContentHash(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func ValidateVector(vector []float32) error {
	if len(vector) != Dimensions {
		return fmt.Errorf("vector dimensions = %d, want %d", len(vector), Dimensions)
	}
	for index, value := range vector {
		asFloat := float64(value)
		if math.IsNaN(asFloat) || math.IsInf(asFloat, 0) {
			return fmt.Errorf("vector contains non-finite value at index %d", index)
		}
	}
	return nil
}

func containsDisallowedControl(value string) bool {
	for _, character := range value {
		if character == 0 || character < 32 && character != '\n' && character != '\r' && character != '\t' {
			return true
		}
	}
	return false
}

func validContentHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if character >= '0' && character <= '9' || character >= 'a' && character <= 'f' {
			continue
		}
		return false
	}
	return true
}

func CleanEmbeddingErrorMessage(message string) string {
	message = strings.TrimSpace(message)
	if message == "" {
		return "embedding failed"
	}
	message = strings.Join(strings.Fields(message), " ")
	const maxErrorMessageLength = 500
	if len(message) > maxErrorMessageLength {
		return message[:maxErrorMessageLength]
	}
	return message
}
