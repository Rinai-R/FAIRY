package memory

import (
	"errors"
	"math"
	"testing"

	"fairy/memory/semantic"
)

type fakeLocalEmbeddingRunner struct {
	ready   bool
	dims    int
	vectors [][]float32
	err     error
	texts   []string
}

func (r *fakeLocalEmbeddingRunner) Ready() bool { return r.ready }
func (r *fakeLocalEmbeddingRunner) Dims() int   { return r.dims }

func (r *fakeLocalEmbeddingRunner) Embed(texts []string) ([][]float32, error) {
	r.texts = append(r.texts, texts...)
	if r.err != nil {
		return nil, r.err
	}
	if r.vectors != nil {
		return r.vectors, nil
	}
	vectors := make([][]float32, len(texts))
	for index := range texts {
		vector := make([]float32, SemanticEmbeddingDimensions)
		vector[index%SemanticEmbeddingDimensions] = 2
		vectors[index] = vector
	}
	return vectors, nil
}

func TestNewLocalBGEEmbedderRequiresRunnerAnd512Dimensions(t *testing.T) {
	if _, err := NewLocalBGEEmbedder(nil); err == nil {
		t.Fatal("NewLocalBGEEmbedder(nil) error = nil")
	}
	if _, err := NewLocalBGEEmbedder(&fakeLocalEmbeddingRunner{ready: true, dims: 384}); err == nil {
		t.Fatal("NewLocalBGEEmbedder(384 dims) error = nil")
	}
	if _, err := NewLocalBGEEmbedder(&fakeLocalEmbeddingRunner{ready: true, dims: SemanticEmbeddingDimensions}); err != nil {
		t.Fatalf("NewLocalBGEEmbedder(512 dims) error = %v", err)
	}
}

func TestLocalBGEEmbedderReadyAndNormalizedVectors(t *testing.T) {
	runner := &fakeLocalEmbeddingRunner{ready: true, dims: SemanticEmbeddingDimensions}
	embedder, err := NewLocalBGEEmbedder(runner)
	if err != nil {
		t.Fatalf("NewLocalBGEEmbedder() error = %v", err)
	}
	if !embedder.Ready() || embedder.Status() != semantic.StatusReady || embedder.Dims() != SemanticEmbeddingDimensions {
		t.Fatalf("embedder readiness = ready %v status %q dims %d", embedder.Ready(), embedder.Status(), embedder.Dims())
	}
	vectors, err := embedder.Embed([]string{"咖啡", "陪伴"})
	if err != nil {
		t.Fatalf("Embed() error = %v", err)
	}
	if len(vectors) != 2 || len(vectors[0]) != SemanticEmbeddingDimensions || len(vectors[1]) != SemanticEmbeddingDimensions {
		t.Fatalf("vectors dimensions = %#v", vectors)
	}
	for vectorIndex, vector := range vectors {
		var sumSquares float64
		for _, value := range vector {
			sumSquares += float64(value * value)
		}
		if math.Abs(sumSquares-1) > 0.00001 {
			t.Fatalf("vector %d norm squares = %f, want 1", vectorIndex, sumSquares)
		}
	}
	if len(runner.texts) != 2 || runner.texts[0] != "咖啡" || runner.texts[1] != "陪伴" {
		t.Fatalf("runner texts = %#v", runner.texts)
	}
}

func TestLocalBGEEmbedderUnavailableDoesNotCallRunner(t *testing.T) {
	runner := &fakeLocalEmbeddingRunner{ready: false, dims: SemanticEmbeddingDimensions}
	embedder, err := NewLocalBGEEmbedder(runner)
	if err != nil {
		t.Fatalf("NewLocalBGEEmbedder() error = %v", err)
	}
	vectors, err := embedder.Embed([]string{"咖啡"})
	if !errors.Is(err, semantic.ErrUnavailable) {
		t.Fatalf("Embed(unavailable) error = %v, want %v", err, semantic.ErrUnavailable)
	}
	if vectors != nil || len(runner.texts) != 0 {
		t.Fatalf("unavailable embedder returned vectors %#v or called runner %#v", vectors, runner.texts)
	}
}

func TestLocalBGEEmbedderRejectsInvalidRunnerVectors(t *testing.T) {
	badVectors := [][]float32{make([]float32, SemanticEmbeddingDimensions)}
	runner := &fakeLocalEmbeddingRunner{ready: true, dims: SemanticEmbeddingDimensions, vectors: badVectors}
	embedder, err := NewLocalBGEEmbedder(runner)
	if err != nil {
		t.Fatalf("NewLocalBGEEmbedder() error = %v", err)
	}
	if _, err := embedder.Embed([]string{"咖啡"}); err == nil {
		t.Fatal("Embed(zero vector) error = nil")
	}
}
