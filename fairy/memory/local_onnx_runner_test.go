package memory

import (
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sugarme/tokenizer"
	ort "github.com/yalue/onnxruntime_go"
)

type fakeONNXTokenizer struct {
	encodings map[string]*tokenizer.Encoding
	err       error
	texts     []string
}

func (t *fakeONNXTokenizer) EncodeSingle(input string, addSpecialTokensOpt ...bool) (*tokenizer.Encoding, error) {
	t.texts = append(t.texts, input)
	if t.err != nil {
		return nil, t.err
	}
	encoding, ok := t.encodings[input]
	if !ok {
		return nil, errors.New("missing fake encoding")
	}
	return encoding, nil
}

type fakeONNXSession struct {
	vectors [][]float32
	err     error
	batch   localONNXInputBatch
	closed  bool
}

func (s *fakeONNXSession) Run(batch localONNXInputBatch) ([][]float32, error) {
	s.batch = batch
	if s.err != nil {
		return nil, s.err
	}
	return s.vectors, nil
}

func (s *fakeONNXSession) Destroy() error {
	s.closed = true
	return nil
}

func fakeEncoding(ids []int, mask []int, types []int) *tokenizer.Encoding {
	return tokenizer.NewEncoding(ids, types, nil, nil, nil, mask, nil)
}

func nonZeroVector(value float32) []float32 {
	vector := make([]float32, SemanticEmbeddingDimensions)
	vector[0] = value
	return vector
}

func TestLocalONNXEmbeddingRunnerEmbedsAndPadsBatch(t *testing.T) {
	tk := &fakeONNXTokenizer{encodings: map[string]*tokenizer.Encoding{
		"咖啡": fakeEncoding([]int{101, 200, 102}, []int{1, 1, 1}, []int{0, 0, 0}),
		"陪伴": fakeEncoding([]int{101, 300, 400, 102}, []int{1, 1, 1, 1}, []int{0, 0, 0, 0}),
	}}
	session := &fakeONNXSession{vectors: [][]float32{nonZeroVector(2), nonZeroVector(3)}}
	runner, err := newLocalONNXEmbeddingRunnerFromParts(tk, session, 3)
	if err != nil {
		t.Fatalf("newLocalONNXEmbeddingRunnerFromParts() error = %v", err)
	}
	if !runner.Ready() || runner.Dims() != SemanticEmbeddingDimensions {
		t.Fatalf("runner readiness = %v dims = %d", runner.Ready(), runner.Dims())
	}
	vectors, err := runner.Embed([]string{"咖啡", "陪伴"})
	if err != nil {
		t.Fatalf("Embed() error = %v", err)
	}
	if len(vectors) != 2 || len(vectors[0]) != SemanticEmbeddingDimensions || len(vectors[1]) != SemanticEmbeddingDimensions {
		t.Fatalf("vectors = %#v", vectors)
	}
	if session.batch.BatchSize != 2 || session.batch.SequenceLength != 3 {
		t.Fatalf("batch shape = %#v", session.batch)
	}
	wantIDs := []int64{101, 200, 102, 101, 300, 400}
	if !equalInt64s(session.batch.InputIDs, wantIDs) {
		t.Fatalf("input ids = %#v, want %#v", session.batch.InputIDs, wantIDs)
	}
	if !equalInt64s(session.batch.AttentionMask, []int64{1, 1, 1, 1, 1, 1}) {
		t.Fatalf("attention mask = %#v", session.batch.AttentionMask)
	}
	if err := runner.Close(); err != nil || !session.closed {
		t.Fatalf("Close() error = %v closed=%v", err, session.closed)
	}
}

func TestLocalONNXEmbeddingRunnerPadsShorterRows(t *testing.T) {
	tk := &fakeONNXTokenizer{encodings: map[string]*tokenizer.Encoding{
		"长": fakeEncoding([]int{1, 2, 3}, []int{1, 1, 1}, []int{0, 0, 0}),
		"短": fakeEncoding([]int{4}, []int{1}, []int{0}),
	}}
	session := &fakeONNXSession{vectors: [][]float32{nonZeroVector(2), nonZeroVector(3)}}
	runner, err := newLocalONNXEmbeddingRunnerFromParts(tk, session, 8)
	if err != nil {
		t.Fatalf("newLocalONNXEmbeddingRunnerFromParts() error = %v", err)
	}
	if _, err := runner.Embed([]string{"长", "短"}); err != nil {
		t.Fatalf("Embed() error = %v", err)
	}
	if !equalInt64s(session.batch.InputIDs, []int64{1, 2, 3, 4, 0, 0}) {
		t.Fatalf("input ids = %#v", session.batch.InputIDs)
	}
	if !equalInt64s(session.batch.AttentionMask, []int64{1, 1, 1, 1, 0, 0}) {
		t.Fatalf("attention mask = %#v", session.batch.AttentionMask)
	}
}

func TestLocalONNXEmbeddingRunnerRejectsInvalidVectors(t *testing.T) {
	cases := []struct {
		name    string
		vectors [][]float32
	}{
		{name: "wrong dims", vectors: [][]float32{{1, 2}}},
		{name: "zero", vectors: [][]float32{make([]float32, SemanticEmbeddingDimensions)}},
		{name: "nan", vectors: [][]float32{func() []float32 {
			v := nonZeroVector(1)
			v[1] = float32(math.NaN())
			return v
		}()}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			tk := &fakeONNXTokenizer{encodings: map[string]*tokenizer.Encoding{"x": fakeEncoding([]int{1}, []int{1}, []int{0})}}
			runner, err := newLocalONNXEmbeddingRunnerFromParts(tk, &fakeONNXSession{vectors: tt.vectors}, 8)
			if err != nil {
				t.Fatalf("new runner error = %v", err)
			}
			if _, err := runner.Embed([]string{"x"}); err == nil {
				t.Fatal("Embed() error = nil")
			}
		})
	}
}

func TestLocalONNXEmbeddingRunnerMissingAssets(t *testing.T) {
	_, err := NewLocalONNXEmbeddingRunner(t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "semantic embedding model missing") {
		t.Fatalf("NewLocalONNXEmbeddingRunner(missing) error = %v", err)
	}
}

func TestLocalONNXEmbeddingRunnerRealAssets(t *testing.T) {
	assetRoot := os.Getenv("FAIRY_LOCAL_BGE_ASSET_ROOT")
	if assetRoot == "" {
		t.Skip("set FAIRY_LOCAL_BGE_ASSET_ROOT to a directory containing model.onnx, model_quantized.onnx_data, tokenizer.json, and ONNX Runtime")
	}
	root := t.TempDir()
	destDir := filepath.Join(root, "intelligence", "embeddings", SemanticEmbeddingModelID)
	if err := os.MkdirAll(destDir, 0o700); err != nil {
		t.Fatalf("MkdirAll(asset dir) error = %v", err)
	}
	for _, name := range []string{"model.onnx", "model_quantized.onnx_data", "tokenizer.json", localONNXRuntimeLibraryFilename()} {
		if err := copyTestAsset(filepath.Join(assetRoot, name), filepath.Join(destDir, name)); err != nil {
			t.Fatalf("copy %s error = %v", name, err)
		}
	}
	runner, err := NewLocalONNXEmbeddingRunner(root)
	if err != nil {
		t.Fatalf("NewLocalONNXEmbeddingRunner(real assets) error = %v", err)
	}
	defer runner.Close()
	embedder, err := NewLocalBGEEmbedder(runner)
	if err != nil {
		t.Fatalf("NewLocalBGEEmbedder(real runner) error = %v", err)
	}
	vectors, err := embedder.Embed([]string{"咖啡", "陪伴"})
	if err != nil {
		t.Fatalf("Embed(real assets) error = %v", err)
	}
	if len(vectors) != 2 {
		t.Fatalf("vectors len = %d, want 2", len(vectors))
	}
	for index, vector := range vectors {
		if len(vector) != SemanticEmbeddingDimensions {
			t.Fatalf("vector %d dims = %d, want %d", index, len(vector), SemanticEmbeddingDimensions)
		}
		var sumSquares float64
		for _, value := range vector {
			v := float64(value)
			if math.IsNaN(v) || math.IsInf(v, 0) {
				t.Fatalf("vector %d contains non-finite value", index)
			}
			sumSquares += v * v
		}
		if math.Abs(sumSquares-1) > 0.001 {
			t.Fatalf("vector %d squared norm = %.6f, want ~1", index, sumSquares)
		}
	}
}

func TestMeanPoolLocalONNXTokens(t *testing.T) {
	data := make([]float32, 1*3*SemanticEmbeddingDimensions)
	data[0] = 1
	data[SemanticEmbeddingDimensions] = 3
	data[2*SemanticEmbeddingDimensions] = 100
	vectors, err := meanPoolLocalONNXTokens(data, []int64{1, 1, 0}, 1, 3, SemanticEmbeddingDimensions)
	if err != nil {
		t.Fatalf("meanPoolLocalONNXTokens() error = %v", err)
	}
	if len(vectors) != 1 || vectors[0][0] != 2 {
		t.Fatalf("pooled vectors = %#v", vectors)
	}
	if _, err := meanPoolLocalONNXTokens(data, []int64{0, 0, 0}, 1, 3, SemanticEmbeddingDimensions); err == nil {
		t.Fatal("meanPoolLocalONNXTokens(empty mask) error = nil")
	}
}

func TestSelectLocalONNXInputAndOutputNames(t *testing.T) {
	inputs := []ort.InputOutputInfo{{Name: "input_ids"}, {Name: "attention_mask"}, {Name: "token_type_ids"}}
	names, includeTypes, err := selectLocalONNXInputNames(inputs)
	if err != nil {
		t.Fatalf("selectLocalONNXInputNames() error = %v", err)
	}
	if !includeTypes || !equalStrings(names, []string{"input_ids", "attention_mask", "token_type_ids"}) {
		t.Fatalf("input names = %#v includeTypes=%v", names, includeTypes)
	}
	output, err := selectLocalONNXOutput([]ort.InputOutputInfo{{Name: "last_hidden_state", Dimensions: ort.NewShape(-1, -1, SemanticEmbeddingDimensions)}})
	if err != nil {
		t.Fatalf("selectLocalONNXOutput() error = %v", err)
	}
	if output.Name != "last_hidden_state" {
		t.Fatalf("output = %#v", output)
	}
}

func copyTestAsset(sourcePath string, destPath string) error {
	source, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer source.Close()
	dest, err := os.OpenFile(destPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer dest.Close()
	_, err = io.Copy(dest, source)
	return err
}

func equalInt64s(a []int64, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for index := range a {
		if a[index] != b[index] {
			return false
		}
	}
	return true
}

func equalStrings(a []string, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for index := range a {
		if a[index] != b[index] {
			return false
		}
	}
	return true
}
