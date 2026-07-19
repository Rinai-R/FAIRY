package memory

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
)

func TestPrepareBundledSemanticEmbeddingAssetsCopiesMissingResources(t *testing.T) {
	root := t.TempDir()
	bundle := fstest.MapFS{
		SemanticEmbeddingBundleModelRelativePath:     {Data: []byte("model-bytes"), Mode: 0o600},
		SemanticEmbeddingBundleModelDataRelativePath: {Data: []byte("model-data-bytes"), Mode: 0o600},
		SemanticEmbeddingBundleTokenizerRelativePath: {Data: []byte("tokenizer-bytes"), Mode: 0o600},
		SemanticEmbeddingBundleRuntimeRelativePath(): {Data: []byte("runtime-bytes"), Mode: 0o600},
	}
	result, err := PrepareBundledSemanticEmbeddingAssets(root, bundle)
	if err != nil {
		t.Fatalf("PrepareBundledSemanticEmbeddingAssets() error = %v", err)
	}
	if !result.Ready() || result.Reason != SemanticAssetReasonCopied || !result.ModelCopied || !result.ModelDataCopied || !result.TokenizerCopied || !result.RuntimeCopied {
		t.Fatalf("preparation result = %#v", result)
	}
	assertFileContent(t, result.ModelPath, "model-bytes")
	assertFileContent(t, result.ModelDataPath, "model-data-bytes")
	assertFileContent(t, result.TokenizerPath, "tokenizer-bytes")
	assertFileContent(t, result.RuntimePath, "runtime-bytes")
}

func TestPrepareBundledSemanticEmbeddingAssetsPreservesExistingResources(t *testing.T) {
	root := t.TempDir()
	modelPath, err := SemanticEmbeddingModelPath(root)
	if err != nil {
		t.Fatalf("SemanticEmbeddingModelPath() error = %v", err)
	}
	modelDataPath, err := SemanticEmbeddingModelDataPath(root)
	if err != nil {
		t.Fatalf("SemanticEmbeddingModelDataPath() error = %v", err)
	}
	tokenizerPath, err := SemanticEmbeddingTokenizerPath(root)
	if err != nil {
		t.Fatalf("SemanticEmbeddingTokenizerPath() error = %v", err)
	}
	runtimePath, err := DefaultSemanticEmbeddingRuntimeLibraryPath(root)
	if err != nil {
		t.Fatalf("SemanticEmbeddingRuntimeLibraryPath() error = %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(modelPath), 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(modelPath, []byte("existing-model"), 0o600); err != nil {
		t.Fatalf("WriteFile(model) error = %v", err)
	}
	if err := os.WriteFile(modelDataPath, []byte("existing-model-data"), 0o600); err != nil {
		t.Fatalf("WriteFile(model data) error = %v", err)
	}
	if err := os.WriteFile(tokenizerPath, []byte("existing-tokenizer"), 0o600); err != nil {
		t.Fatalf("WriteFile(tokenizer) error = %v", err)
	}
	if err := os.WriteFile(runtimePath, []byte("existing-runtime"), 0o600); err != nil {
		t.Fatalf("WriteFile(runtime) error = %v", err)
	}
	bundle := fstest.MapFS{
		SemanticEmbeddingBundleModelRelativePath:     {Data: []byte("new-model"), Mode: 0o600},
		SemanticEmbeddingBundleModelDataRelativePath: {Data: []byte("new-model-data"), Mode: 0o600},
		SemanticEmbeddingBundleTokenizerRelativePath: {Data: []byte("new-tokenizer"), Mode: 0o600},
		SemanticEmbeddingBundleRuntimeRelativePath(): {Data: []byte("new-runtime"), Mode: 0o600},
	}
	result, err := PrepareBundledSemanticEmbeddingAssets(root, bundle)
	if err != nil {
		t.Fatalf("PrepareBundledSemanticEmbeddingAssets() error = %v", err)
	}
	if !result.Ready() || result.Copied() || result.Reason != SemanticAssetReasonAlreadyPresent {
		t.Fatalf("preparation result = %#v", result)
	}
	assertFileContent(t, modelPath, "existing-model")
	assertFileContent(t, modelDataPath, "existing-model-data")
	assertFileContent(t, tokenizerPath, "existing-tokenizer")
	assertFileContent(t, runtimePath, "existing-runtime")
}

func TestPrepareBundledSemanticEmbeddingAssetsMissingBundleDoesNotCreateFakeDirectory(t *testing.T) {
	root := t.TempDir()
	result, err := PrepareBundledSemanticEmbeddingAssets(root, fstest.MapFS{})
	if err != nil {
		t.Fatalf("PrepareBundledSemanticEmbeddingAssets() error = %v", err)
	}
	if result.Ready() || result.Reason != SemanticAssetReasonBundleModelMissing || result.Copied() {
		t.Fatalf("preparation result = %#v", result)
	}
	modelPath, err := SemanticEmbeddingModelPath(root)
	if err != nil {
		t.Fatalf("SemanticEmbeddingModelPath() error = %v", err)
	}
	if _, err := os.Stat(filepath.Dir(modelPath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("asset preparation created directory or unexpected stat error: %v", err)
	}
}

func TestPrepareBundledSemanticEmbeddingAssetsMissingTokenizerDoesNotCopyPartialModel(t *testing.T) {
	root := t.TempDir()
	bundle := fstest.MapFS{
		SemanticEmbeddingBundleModelRelativePath:     {Data: []byte("model-bytes"), Mode: 0o600},
		SemanticEmbeddingBundleModelDataRelativePath: {Data: []byte("model-data-bytes"), Mode: 0o600},
	}
	result, err := PrepareBundledSemanticEmbeddingAssets(root, bundle)
	if err != nil {
		t.Fatalf("PrepareBundledSemanticEmbeddingAssets() error = %v", err)
	}
	if result.Ready() || result.Reason != SemanticAssetReasonBundleTokenizerMissing || result.Copied() {
		t.Fatalf("preparation result = %#v", result)
	}
	modelPath, err := SemanticEmbeddingModelPath(root)
	if err != nil {
		t.Fatalf("SemanticEmbeddingModelPath() error = %v", err)
	}
	if _, err := os.Stat(modelPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial model copied or unexpected stat error: %v", err)
	}
}

func TestPrepareBundledSemanticEmbeddingAssetsMissingModelDataDoesNotCopyPartialModel(t *testing.T) {
	root := t.TempDir()
	bundle := fstest.MapFS{
		SemanticEmbeddingBundleModelRelativePath: {Data: []byte("model-bytes"), Mode: 0o600},
	}
	result, err := PrepareBundledSemanticEmbeddingAssets(root, bundle)
	if err != nil {
		t.Fatalf("PrepareBundledSemanticEmbeddingAssets() error = %v", err)
	}
	if result.Ready() || result.Reason != SemanticAssetReasonBundleModelDataMissing || result.Copied() {
		t.Fatalf("preparation result = %#v", result)
	}
	modelPath, err := SemanticEmbeddingModelPath(root)
	if err != nil {
		t.Fatalf("SemanticEmbeddingModelPath() error = %v", err)
	}
	if _, err := os.Stat(modelPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial model copied or unexpected stat error: %v", err)
	}
}

func TestPrepareBundledSemanticEmbeddingAssetsMissingRuntimeDoesNotCopyPartialResources(t *testing.T) {
	root := t.TempDir()
	bundle := fstest.MapFS{
		SemanticEmbeddingBundleModelRelativePath:     {Data: []byte("model-bytes"), Mode: 0o600},
		SemanticEmbeddingBundleModelDataRelativePath: {Data: []byte("model-data-bytes"), Mode: 0o600},
		SemanticEmbeddingBundleTokenizerRelativePath: {Data: []byte("tokenizer-bytes"), Mode: 0o600},
	}
	result, err := PrepareBundledSemanticEmbeddingAssets(root, bundle)
	if err != nil {
		t.Fatalf("PrepareBundledSemanticEmbeddingAssets() error = %v", err)
	}
	if result.Ready() || result.Reason != SemanticAssetReasonBundleRuntimeMissing || result.Copied() {
		t.Fatalf("preparation result = %#v", result)
	}
	modelPath, err := SemanticEmbeddingModelPath(root)
	if err != nil {
		t.Fatalf("SemanticEmbeddingModelPath() error = %v", err)
	}
	if _, err := os.Stat(modelPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial model copied or unexpected stat error: %v", err)
	}
}

func assertFileContent(t *testing.T, path string, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s) error = %v", path, err)
	}
	if string(data) != want {
		t.Fatalf("ReadFile(%s) = %q, want %q", path, data, want)
	}
}
