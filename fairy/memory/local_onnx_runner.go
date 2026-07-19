package memory

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/sugarme/tokenizer"
	"github.com/sugarme/tokenizer/pretrained"
	ort "github.com/yalue/onnxruntime_go"
)

const (
	LocalONNXRuntimeEnvVar     = "FAIRY_ONNXRUNTIME_PATH"
	localONNXMaxSequenceTokens = 512
)

var (
	localONNXRuntimeMu          sync.Mutex
	localONNXRuntimeInitialized bool
	localONNXRuntimePath        string
)

type localONNXTokenizer interface {
	EncodeSingle(input string, addSpecialTokensOpt ...bool) (*tokenizer.Encoding, error)
}

type localONNXSession interface {
	Run(batch localONNXInputBatch) ([][]float32, error)
	Destroy() error
}

type localONNXInputBatch struct {
	InputIDs       []int64
	AttentionMask  []int64
	TokenTypeIDs   []int64
	BatchSize      int
	SequenceLength int
}

// LocalONNXEmbeddingRunner owns the tokenizer + ONNX execution path for the
// default bge-small-zh-v1.5 provider. It does not download assets or start a
// sidecar; callers must provide local model/tokenizer/runtime files.
type LocalONNXEmbeddingRunner struct {
	tokenizer localONNXTokenizer
	session   localONNXSession
	maxTokens int
}

var _ LocalEmbeddingRunner = (*LocalONNXEmbeddingRunner)(nil)

func NewLocalONNXEmbeddingRunner(root string) (*LocalONNXEmbeddingRunner, error) {
	modelPath, err := SemanticEmbeddingModelPath(root)
	if err != nil {
		return nil, err
	}
	modelDataPath, err := SemanticEmbeddingModelDataPath(root)
	if err != nil {
		return nil, err
	}
	tokenizerPath, err := SemanticEmbeddingTokenizerPath(root)
	if err != nil {
		return nil, err
	}
	runtimePath, err := SemanticEmbeddingRuntimeLibraryPath(root)
	if err != nil {
		return nil, err
	}
	if err := requireRegularLocalSemanticAsset(modelPath, "semantic embedding model"); err != nil {
		return nil, err
	}
	if err := requireRegularLocalSemanticAsset(modelDataPath, "semantic embedding model data"); err != nil {
		return nil, err
	}
	if err := requireRegularLocalSemanticAsset(tokenizerPath, "semantic embedding tokenizer"); err != nil {
		return nil, err
	}
	if err := requireRegularLocalSemanticAsset(runtimePath, "ONNX Runtime shared library"); err != nil {
		return nil, err
	}
	tk, err := pretrained.FromFile(tokenizerPath)
	if err != nil {
		return nil, fmt.Errorf("loading local semantic tokenizer: %w", err)
	}
	if err := initializeLocalONNXRuntime(runtimePath); err != nil {
		return nil, err
	}
	session, err := newLocalORTEmbeddingSession(modelPath)
	if err != nil {
		return nil, err
	}
	return newLocalONNXEmbeddingRunnerFromParts(tk, session, localONNXMaxSequenceTokens)
}

func newLocalONNXEmbeddingRunnerFromParts(tk localONNXTokenizer, session localONNXSession, maxTokens int) (*LocalONNXEmbeddingRunner, error) {
	if tk == nil {
		return nil, errors.New("local ONNX tokenizer is required")
	}
	if session == nil {
		return nil, errors.New("local ONNX session is required")
	}
	if maxTokens <= 0 {
		maxTokens = localONNXMaxSequenceTokens
	}
	return &LocalONNXEmbeddingRunner{tokenizer: tk, session: session, maxTokens: maxTokens}, nil
}

func (r *LocalONNXEmbeddingRunner) Ready() bool {
	return r != nil && r.tokenizer != nil && r.session != nil
}

func (r *LocalONNXEmbeddingRunner) Dims() int {
	return SemanticEmbeddingDimensions
}

func (r *LocalONNXEmbeddingRunner) Embed(texts []string) ([][]float32, error) {
	if !r.Ready() {
		return nil, errors.New("local ONNX embedding runner unavailable")
	}
	if len(texts) == 0 {
		return [][]float32{}, nil
	}
	batch, err := r.encodeBatch(texts)
	if err != nil {
		return nil, err
	}
	vectors, err := r.session.Run(batch)
	if err != nil {
		return nil, fmt.Errorf("running local ONNX embedding session: %w", err)
	}
	if len(vectors) != len(texts) {
		return nil, fmt.Errorf("local ONNX session returned %d vectors for %d texts", len(vectors), len(texts))
	}
	for index, vector := range vectors {
		if err := validateLocalONNXVector(vector); err != nil {
			return nil, fmt.Errorf("validating local ONNX vector %d: %w", index, err)
		}
	}
	return vectors, nil
}

func (r *LocalONNXEmbeddingRunner) Close() error {
	if r == nil || r.session == nil {
		return nil
	}
	return r.session.Destroy()
}

func (r *LocalONNXEmbeddingRunner) encodeBatch(texts []string) (localONNXInputBatch, error) {
	encodedIDs := make([][]int64, len(texts))
	encodedMasks := make([][]int64, len(texts))
	encodedTypes := make([][]int64, len(texts))
	maxLen := 0
	for index, text := range texts {
		encoding, err := r.tokenizer.EncodeSingle(text, true)
		if err != nil {
			return localONNXInputBatch{}, fmt.Errorf("tokenizing input %d: %w", index, err)
		}
		ids := intsToInt64s(encoding.GetIds())
		mask := intsToInt64s(encoding.GetAttentionMask())
		types := intsToInt64s(encoding.GetTypeIds())
		if len(ids) == 0 {
			return localONNXInputBatch{}, fmt.Errorf("tokenizing input %d produced no tokens", index)
		}
		if len(ids) > r.maxTokens {
			ids = ids[:r.maxTokens]
		}
		if len(mask) > len(ids) {
			mask = mask[:len(ids)]
		}
		if len(types) > len(ids) {
			types = types[:len(ids)]
		}
		if len(mask) < len(ids) {
			mask = padInt64s(mask, len(ids), 1)
		}
		if len(types) < len(ids) {
			types = padInt64s(types, len(ids), 0)
		}
		encodedIDs[index] = ids
		encodedMasks[index] = mask
		encodedTypes[index] = types
		if len(ids) > maxLen {
			maxLen = len(ids)
		}
	}
	inputIDs := make([]int64, 0, len(texts)*maxLen)
	attentionMask := make([]int64, 0, len(texts)*maxLen)
	tokenTypeIDs := make([]int64, 0, len(texts)*maxLen)
	for index := range texts {
		inputIDs = append(inputIDs, padInt64s(encodedIDs[index], maxLen, 0)...)
		attentionMask = append(attentionMask, padInt64s(encodedMasks[index], maxLen, 0)...)
		tokenTypeIDs = append(tokenTypeIDs, padInt64s(encodedTypes[index], maxLen, 0)...)
	}
	return localONNXInputBatch{
		InputIDs:       inputIDs,
		AttentionMask:  attentionMask,
		TokenTypeIDs:   tokenTypeIDs,
		BatchSize:      len(texts),
		SequenceLength: maxLen,
	}, nil
}

func intsToInt64s(values []int) []int64 {
	result := make([]int64, len(values))
	for index, value := range values {
		result[index] = int64(value)
	}
	return result
}

func padInt64s(values []int64, length int, pad int64) []int64 {
	if len(values) >= length {
		out := make([]int64, length)
		copy(out, values[:length])
		return out
	}
	out := make([]int64, length)
	copy(out, values)
	for index := len(values); index < length; index++ {
		out[index] = pad
	}
	return out
}

func validateLocalONNXVector(vector []float32) error {
	if len(vector) != SemanticEmbeddingDimensions {
		return fmt.Errorf("dimensions = %d, want %d", len(vector), SemanticEmbeddingDimensions)
	}
	var sumSquares float64
	for _, value := range vector {
		v := float64(value)
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return errors.New("contains non-finite value")
		}
		sumSquares += v * v
	}
	if sumSquares == 0 {
		return errors.New("zero vector cannot be used")
	}
	return nil
}

func SemanticEmbeddingRuntimeLibraryPath(root string) (string, error) {
	if override := os.Getenv(LocalONNXRuntimeEnvVar); override != "" {
		return override, nil
	}
	return DefaultSemanticEmbeddingRuntimeLibraryPath(root)
}

func DefaultSemanticEmbeddingRuntimeLibraryPath(root string) (string, error) {
	if root == "" {
		return "", ErrRootRequired
	}
	return filepath.Join(root, "intelligence", "embeddings", SemanticEmbeddingModelID, localONNXRuntimeLibraryFilename()), nil
}

func localONNXRuntimeLibraryFilename() string {
	switch runtime.GOOS {
	case "darwin":
		return "libonnxruntime.dylib"
	case "windows":
		return "onnxruntime.dll"
	default:
		return "libonnxruntime.so"
	}
}

func requireRegularLocalSemanticAsset(path string, label string) error {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%s missing: %s", label, path)
		}
		return fmt.Errorf("checking %s: %w", label, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s path is not a regular file: %s", label, path)
	}
	return nil
}

func initializeLocalONNXRuntime(libraryPath string) error {
	localONNXRuntimeMu.Lock()
	defer localONNXRuntimeMu.Unlock()
	if localONNXRuntimeInitialized {
		if localONNXRuntimePath != libraryPath {
			return fmt.Errorf("ONNX Runtime already initialized with %s", localONNXRuntimePath)
		}
		return nil
	}
	ort.SetSharedLibraryPath(libraryPath)
	if err := ort.InitializeEnvironment(); err != nil {
		return fmt.Errorf("initializing ONNX Runtime: %w", err)
	}
	localONNXRuntimeInitialized = true
	localONNXRuntimePath = libraryPath
	return nil
}

type localORTEmbeddingSession struct {
	session       *ort.DynamicAdvancedSession
	inputNames    []string
	outputName    string
	outputRank    int
	outputHidden  int
	includeTypes  bool
	poolTokenAxis bool
}

func newLocalORTEmbeddingSession(modelPath string) (*localORTEmbeddingSession, error) {
	inputs, outputs, err := ort.GetInputOutputInfo(modelPath)
	if err != nil {
		return nil, fmt.Errorf("reading ONNX model input/output info: %w", err)
	}
	inputNames, includeTypes, err := selectLocalONNXInputNames(inputs)
	if err != nil {
		return nil, err
	}
	output, err := selectLocalONNXOutput(outputs)
	if err != nil {
		return nil, err
	}
	session, err := ort.NewDynamicAdvancedSession(modelPath, inputNames, []string{output.Name}, nil)
	if err != nil {
		return nil, fmt.Errorf("creating ONNX embedding session: %w", err)
	}
	rank := len(output.Dimensions)
	hidden := SemanticEmbeddingDimensions
	if rank > 0 {
		lastDim := output.Dimensions[rank-1]
		if lastDim > 0 {
			hidden = int(lastDim)
		}
	}
	return &localORTEmbeddingSession{
		session:       session,
		inputNames:    inputNames,
		outputName:    output.Name,
		outputRank:    rank,
		outputHidden:  hidden,
		includeTypes:  includeTypes,
		poolTokenAxis: rank >= 3,
	}, nil
}

func (s *localORTEmbeddingSession) Destroy() error {
	if s == nil || s.session == nil {
		return nil
	}
	return s.session.Destroy()
}

func (s *localORTEmbeddingSession) Run(batch localONNXInputBatch) ([][]float32, error) {
	if batch.BatchSize <= 0 || batch.SequenceLength <= 0 {
		return [][]float32{}, nil
	}
	shape := ort.NewShape(int64(batch.BatchSize), int64(batch.SequenceLength))
	inputIDs, err := ort.NewTensor[int64](shape, batch.InputIDs)
	if err != nil {
		return nil, fmt.Errorf("creating input_ids tensor: %w", err)
	}
	defer inputIDs.Destroy()
	attentionMask, err := ort.NewTensor[int64](shape, batch.AttentionMask)
	if err != nil {
		return nil, fmt.Errorf("creating attention_mask tensor: %w", err)
	}
	defer attentionMask.Destroy()
	inputs := []ort.Value{inputIDs, attentionMask}
	if s.includeTypes {
		tokenTypeIDs, err := ort.NewTensor[int64](shape, batch.TokenTypeIDs)
		if err != nil {
			return nil, fmt.Errorf("creating token_type_ids tensor: %w", err)
		}
		defer tokenTypeIDs.Destroy()
		inputs = append(inputs, tokenTypeIDs)
	}
	outputShape := ort.NewShape(int64(batch.BatchSize), int64(s.outputHidden))
	if s.poolTokenAxis {
		outputShape = ort.NewShape(int64(batch.BatchSize), int64(batch.SequenceLength), int64(s.outputHidden))
	}
	output, err := ort.NewEmptyTensor[float32](outputShape)
	if err != nil {
		return nil, fmt.Errorf("creating output tensor: %w", err)
	}
	defer output.Destroy()
	if err := s.session.Run(inputs, []ort.Value{output}); err != nil {
		return nil, err
	}
	data := output.GetData()
	if s.poolTokenAxis {
		return meanPoolLocalONNXTokens(data, batch.AttentionMask, batch.BatchSize, batch.SequenceLength, s.outputHidden)
	}
	return splitLocalONNXVectors(data, batch.BatchSize, s.outputHidden)
}

func selectLocalONNXInputNames(inputs []ort.InputOutputInfo) ([]string, bool, error) {
	available := make(map[string]bool, len(inputs))
	for _, input := range inputs {
		available[input.Name] = true
	}
	if !available["input_ids"] {
		return nil, false, errors.New("ONNX model missing input_ids input")
	}
	if !available["attention_mask"] {
		return nil, false, errors.New("ONNX model missing attention_mask input")
	}
	inputNames := []string{"input_ids", "attention_mask"}
	includeTypes := available["token_type_ids"]
	if includeTypes {
		inputNames = append(inputNames, "token_type_ids")
	}
	return inputNames, includeTypes, nil
}

func selectLocalONNXOutput(outputs []ort.InputOutputInfo) (ort.InputOutputInfo, error) {
	preferred := []string{"sentence_embedding", "embeddings", "embedding", "pooler_output", "last_hidden_state"}
	byName := make(map[string]ort.InputOutputInfo, len(outputs))
	for _, output := range outputs {
		byName[output.Name] = output
	}
	for _, name := range preferred {
		if output, ok := byName[name]; ok {
			return output, nil
		}
	}
	for _, output := range outputs {
		if len(output.Dimensions) >= 2 {
			return output, nil
		}
	}
	return ort.InputOutputInfo{}, errors.New("ONNX model has no supported embedding output")
}

func splitLocalONNXVectors(data []float32, batchSize int, dims int) ([][]float32, error) {
	if dims != SemanticEmbeddingDimensions {
		return nil, fmt.Errorf("output dimensions = %d, want %d", dims, SemanticEmbeddingDimensions)
	}
	if len(data) != batchSize*dims {
		return nil, fmt.Errorf("output length = %d, want %d", len(data), batchSize*dims)
	}
	vectors := make([][]float32, batchSize)
	for batch := 0; batch < batchSize; batch++ {
		start := batch * dims
		vector := make([]float32, dims)
		copy(vector, data[start:start+dims])
		vectors[batch] = vector
	}
	return vectors, nil
}

func meanPoolLocalONNXTokens(data []float32, attentionMask []int64, batchSize int, sequenceLength int, hidden int) ([][]float32, error) {
	if hidden != SemanticEmbeddingDimensions {
		return nil, fmt.Errorf("hidden dimensions = %d, want %d", hidden, SemanticEmbeddingDimensions)
	}
	if len(data) != batchSize*sequenceLength*hidden {
		return nil, fmt.Errorf("token output length = %d, want %d", len(data), batchSize*sequenceLength*hidden)
	}
	if len(attentionMask) != batchSize*sequenceLength {
		return nil, fmt.Errorf("attention mask length = %d, want %d", len(attentionMask), batchSize*sequenceLength)
	}
	vectors := make([][]float32, batchSize)
	for batch := 0; batch < batchSize; batch++ {
		vector := make([]float32, hidden)
		var tokenCount float32
		for token := 0; token < sequenceLength; token++ {
			maskIndex := batch*sequenceLength + token
			if attentionMask[maskIndex] == 0 {
				continue
			}
			tokenCount++
			base := (batch*sequenceLength + token) * hidden
			for dim := 0; dim < hidden; dim++ {
				vector[dim] += data[base+dim]
			}
		}
		if tokenCount == 0 {
			return nil, fmt.Errorf("batch %d has empty attention mask", batch)
		}
		for dim := range vector {
			vector[dim] /= tokenCount
		}
		vectors[batch] = vector
	}
	return vectors, nil
}
