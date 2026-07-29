package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/pgvector/pgvector-go"
)

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
	if value.ModelID != SemanticEmbeddingModelID {
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

func embeddingForContent(embedder SemanticEmbedder, content string) (EmbeddingValue, error) {
	values, err := embeddingsForContents(embedder, []string{content})
	if err != nil {
		return EmbeddingValue{}, err
	}
	return values[0], nil
}

func embeddingsForContents(embedder SemanticEmbedder, contents []string) ([]EmbeddingValue, error) {
	if len(contents) == 0 {
		return nil, nil
	}
	if embedder == nil {
		return make([]EmbeddingValue, len(contents)), nil
	}
	if !embedder.Ready() {
		return nil, ErrSemanticUnavailable
	}
	if dims := embedder.Dims(); dims != SemanticEmbeddingDimensions {
		return nil, fmt.Errorf("embedding dimensions = %d, want %d", dims, SemanticEmbeddingDimensions)
	}
	vectors, err := embedder.Embed(contents)
	if err != nil {
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
			ModelID:     SemanticEmbeddingModelID,
			ContentHash: semanticContentHash(contents[index]),
			Vector:      &vector,
		}
		if err := values[index].Validate(); err != nil {
			return nil, fmt.Errorf("validating embedding[%d]: %w", index, err)
		}
	}
	return values, nil
}

func (s *Store) embeddingForContent(content string) (EmbeddingValue, error) {
	if s == nil {
		return EmbeddingValue{}, ErrDatabasePoolEmpty
	}
	return embeddingForContent(s.semanticEmbedder, content)
}

func semanticContentHash(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func ValidateVector(vector []float32) error {
	if len(vector) != SemanticEmbeddingDimensions {
		return fmt.Errorf("vector dimensions = %d, want %d", len(vector), SemanticEmbeddingDimensions)
	}
	for index, value := range vector {
		asFloat := float64(value)
		if math.IsNaN(asFloat) || math.IsInf(asFloat, 0) {
			return fmt.Errorf("vector contains non-finite value at index %d", index)
		}
	}
	return nil
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
