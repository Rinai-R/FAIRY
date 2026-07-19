package memory

import (
	"errors"
	"fmt"
	"math"

	"fairy/memory/semantic"
)

// LocalEmbeddingRunner owns the tokenizer + ONNX execution boundary for the
// local bge-small-zh-v1.5 provider. Production code can swap in a real runner
// without changing memory search or embedding job interfaces.
type LocalEmbeddingRunner interface {
	Ready() bool
	Dims() int
	Embed(texts []string) ([][]float32, error)
}

// LocalBGEEmbedder adapts an in-process local runner to semantic.Embedder.
// It never downloads model assets or starts a sidecar.
type LocalBGEEmbedder struct {
	runner LocalEmbeddingRunner
}

var _ semantic.Embedder = (*LocalBGEEmbedder)(nil)

func NewLocalBGEEmbedder(runner LocalEmbeddingRunner) (*LocalBGEEmbedder, error) {
	if runner == nil {
		return nil, errors.New("local embedding runner is required")
	}
	if dims := runner.Dims(); dims != SemanticEmbeddingDimensions {
		return nil, fmt.Errorf("local embedding dimensions = %d, want %d", dims, SemanticEmbeddingDimensions)
	}
	return &LocalBGEEmbedder{runner: runner}, nil
}

func (e *LocalBGEEmbedder) Ready() bool {
	return e != nil && e.runner != nil && e.runner.Ready() && e.runner.Dims() == SemanticEmbeddingDimensions
}

func (e *LocalBGEEmbedder) Status() semantic.Status {
	if e.Ready() {
		return semantic.StatusReady
	}
	return semantic.StatusUnavailable
}

func (e *LocalBGEEmbedder) Dims() int {
	if e == nil || e.runner == nil {
		return 0
	}
	return e.runner.Dims()
}

func (e *LocalBGEEmbedder) Embed(texts []string) ([][]float32, error) {
	if !e.Ready() {
		return nil, semantic.ErrUnavailable
	}
	if len(texts) == 0 {
		return [][]float32{}, nil
	}
	vectors, err := e.runner.Embed(texts)
	if err != nil {
		return nil, fmt.Errorf("running local embedding provider: %w", err)
	}
	if len(vectors) != len(texts) {
		return nil, fmt.Errorf("local embedding provider returned %d vectors for %d texts", len(vectors), len(texts))
	}
	normalized := make([][]float32, len(vectors))
	for index, vector := range vectors {
		item, err := normalizedEmbeddingVector(vector)
		if err != nil {
			return nil, fmt.Errorf("validating local embedding vector %d: %w", index, err)
		}
		normalized[index] = item
	}
	return normalized, nil
}

func normalizedEmbeddingVector(vector []float32) ([]float32, error) {
	if len(vector) != SemanticEmbeddingDimensions {
		return nil, fmt.Errorf("dimensions = %d, want %d", len(vector), SemanticEmbeddingDimensions)
	}
	var sumSquares float64
	for _, value := range vector {
		v := float64(value)
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return nil, errors.New("contains non-finite value")
		}
		sumSquares += v * v
	}
	if sumSquares == 0 {
		return nil, errors.New("zero vector cannot be normalized")
	}
	norm := math.Sqrt(sumSquares)
	normalized := make([]float32, len(vector))
	for index, value := range vector {
		normalized[index] = float32(float64(value) / norm)
	}
	return normalized, nil
}
