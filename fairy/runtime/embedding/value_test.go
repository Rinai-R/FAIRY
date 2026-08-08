package embedding

import (
	"errors"
	"math"
	"strings"
	"testing"
)

const testSemanticEmbeddingModelID = "BAAI/bge-m3"

type fixedSemanticEmbedder struct {
	ready   bool
	modelID string
	dims    int
	vectors [][]float32
	err     error
	inputs  [][]string
}

func (embedder *fixedSemanticEmbedder) ModelID() string {
	if embedder.modelID != "" {
		return embedder.modelID
	}
	return testSemanticEmbeddingModelID
}

func (embedder *fixedSemanticEmbedder) Ready() bool {
	return embedder.ready
}

func (embedder *fixedSemanticEmbedder) Status() SemanticStatus {
	if embedder.ready {
		return SemanticStatusReady
	}
	return SemanticStatusUnavailable
}

func (embedder *fixedSemanticEmbedder) Dims() int {
	return embedder.dims
}

func (embedder *fixedSemanticEmbedder) Embed(texts []string) ([][]float32, error) {
	embedder.inputs = append(embedder.inputs, append([]string(nil), texts...))
	return embedder.vectors, embedder.err
}

func TestEmbeddingForContentDisabled(t *testing.T) {
	value, err := ForContent(nil, "正文")
	if err != nil {
		t.Fatalf("embeddingForContent(nil) error = %v", err)
	}
	if value.Enabled() {
		t.Fatalf("embeddingForContent(nil) = %+v, want disabled", value)
	}
	if err := value.Validate(); err != nil {
		t.Fatalf("disabled embedding.EmbeddingValue.Validate() error = %v", err)
	}
}

func TestEmbeddingForContentBuildsPostgresValue(t *testing.T) {
	vector := make([]float32, Dimensions)
	vector[7] = 0.75
	embedder := &fixedSemanticEmbedder{
		ready:   true,
		dims:    Dimensions,
		vectors: [][]float32{vector},
	}
	value, err := ForContent(embedder, "topic\nstatement")
	if err != nil {
		t.Fatalf("embeddingForContent() error = %v", err)
	}
	if !value.Enabled() || value.ModelID != testSemanticEmbeddingModelID {
		t.Fatalf("embeddingForContent() = %+v", value)
	}
	if value.ContentHash != ContentHash("topic\nstatement") {
		t.Fatalf("ContentHash = %q", value.ContentHash)
	}
	if got := value.Vector.Slice()[7]; got != 0.75 {
		t.Fatalf("Vector[7] = %v, want 0.75", got)
	}
	if len(embedder.inputs) != 1 || len(embedder.inputs[0]) != 1 || embedder.inputs[0][0] != "topic\nstatement" {
		t.Fatalf("Embed inputs = %#v", embedder.inputs)
	}
}

func TestEmbeddingsForContentsUsesOneProviderBatch(t *testing.T) {
	first := make([]float32, Dimensions)
	second := make([]float32, Dimensions)
	first[1] = 0.5
	second[2] = 0.75
	embedder := &fixedSemanticEmbedder{
		ready: true, dims: Dimensions,
		vectors: [][]float32{first, second},
	}
	values, err := ForContents(embedder, []string{"first", "second"})
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 2 || len(embedder.inputs) != 1 || len(embedder.inputs[0]) != 2 {
		t.Fatalf("values=%d inputs=%#v", len(values), embedder.inputs)
	}
	if values[0].ContentHash != ContentHash("first") || values[1].ContentHash != ContentHash("second") {
		t.Fatalf("values=%#v", values)
	}
}

func TestEmbeddingForContentRejectsInvalidProviderResults(t *testing.T) {
	tests := []struct {
		name     string
		embedder *fixedSemanticEmbedder
		want     string
	}{
		{
			name:     "not ready",
			embedder: &fixedSemanticEmbedder{},
			want:     ErrSemanticUnavailable.Error(),
		},
		{
			name:     "wrong declared dimensions",
			embedder: &fixedSemanticEmbedder{ready: true, dims: Dimensions - 1},
			want:     "dimensions",
		},
		{
			name:     "provider error",
			embedder: &fixedSemanticEmbedder{ready: true, dims: Dimensions, err: errors.New("provider failed")},
			want:     "provider failed",
		},
		{
			name:     "zero results",
			embedder: &fixedSemanticEmbedder{ready: true, dims: Dimensions},
			want:     "result count",
		},
		{
			name: "multiple results",
			embedder: &fixedSemanticEmbedder{
				ready: true,
				dims:  Dimensions,
				vectors: [][]float32{
					make([]float32, Dimensions),
					make([]float32, Dimensions),
				},
			},
			want: "result count",
		},
		{
			name: "non finite",
			embedder: &fixedSemanticEmbedder{
				ready: true,
				dims:  Dimensions,
				vectors: func() [][]float32 {
					vector := make([]float32, Dimensions)
					vector[3] = float32(math.Inf(1))
					return [][]float32{vector}
				}(),
			},
			want: "non-finite",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ForContent(test.embedder, "content")
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("embeddingForContent() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestEmbeddingValueRejectsPartialMetadata(t *testing.T) {
	value := EmbeddingValue{ModelID: testSemanticEmbeddingModelID}
	if err := value.Validate(); err == nil {
		t.Fatal("embedding.EmbeddingValue.Validate() error = nil")
	}
}
