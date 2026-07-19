package memory

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

const (
	SemanticEmbeddingBundleModelRelativePath     = "assets/embeddings/bge-small-zh-v1.5/model.onnx"
	SemanticEmbeddingBundleModelDataRelativePath = "assets/embeddings/bge-small-zh-v1.5/model_quantized.onnx_data"
	SemanticEmbeddingBundleTokenizerRelativePath = "assets/embeddings/bge-small-zh-v1.5/tokenizer.json"

	SemanticAssetPreparationReady       = "ready"
	SemanticAssetPreparationUnavailable = "unavailable"

	SemanticAssetReasonAlreadyPresent         = "already_present"
	SemanticAssetReasonCopied                 = "copied"
	SemanticAssetReasonBundleModelMissing     = "bundle_model_missing"
	SemanticAssetReasonBundleModelDataMissing = "bundle_model_data_missing"
	SemanticAssetReasonBundleTokenizerMissing = "bundle_tokenizer_missing"
	SemanticAssetReasonBundleRuntimeMissing   = "bundle_runtime_missing"
	SemanticAssetReasonBundleModelInvalid     = "bundle_model_invalid"
	SemanticAssetReasonBundleModelDataInvalid = "bundle_model_data_invalid"
	SemanticAssetReasonBundleTokenizerInvalid = "bundle_tokenizer_invalid"
	SemanticAssetReasonBundleRuntimeInvalid   = "bundle_runtime_invalid"
)

type SemanticEmbeddingAssetPreparation struct {
	Status          string
	Reason          string
	ModelPath       string
	ModelDataPath   string
	TokenizerPath   string
	RuntimePath     string
	ModelCopied     bool
	ModelDataCopied bool
	TokenizerCopied bool
	RuntimeCopied   bool
}

func (p SemanticEmbeddingAssetPreparation) Ready() bool {
	return p.Status == SemanticAssetPreparationReady
}

func (p SemanticEmbeddingAssetPreparation) Copied() bool {
	return p.ModelCopied || p.ModelDataCopied || p.TokenizerCopied || p.RuntimeCopied
}

type semanticBundledAsset struct {
	label         string
	sourcePath    string
	destPath      string
	missingReason string
	invalidReason string
}

// PrepareBundledSemanticEmbeddingAssets copies bundled bge-small-zh-v1.5
// assets into the config root when they are present in the app bundle.
//
// It never downloads resources, never fabricates placeholder model files, and
// never overwrites existing user-provided local assets.
func PrepareBundledSemanticEmbeddingAssets(root string, bundle fs.FS) (SemanticEmbeddingAssetPreparation, error) {
	modelPath, err := SemanticEmbeddingModelPath(root)
	if err != nil {
		return SemanticEmbeddingAssetPreparation{}, err
	}
	modelDataPath, err := SemanticEmbeddingModelDataPath(root)
	if err != nil {
		return SemanticEmbeddingAssetPreparation{}, err
	}
	tokenizerPath, err := SemanticEmbeddingTokenizerPath(root)
	if err != nil {
		return SemanticEmbeddingAssetPreparation{}, err
	}
	runtimePath, err := DefaultSemanticEmbeddingRuntimeLibraryPath(root)
	if err != nil {
		return SemanticEmbeddingAssetPreparation{}, err
	}
	result := SemanticEmbeddingAssetPreparation{
		Status:        SemanticAssetPreparationUnavailable,
		ModelPath:     modelPath,
		ModelDataPath: modelDataPath,
		TokenizerPath: tokenizerPath,
		RuntimePath:   runtimePath,
	}
	assets := []semanticBundledAsset{
		{
			label:         "semantic embedding model",
			sourcePath:    SemanticEmbeddingBundleModelRelativePath,
			destPath:      modelPath,
			missingReason: SemanticAssetReasonBundleModelMissing,
			invalidReason: SemanticAssetReasonBundleModelInvalid,
		},
		{
			label:         "semantic embedding model data",
			sourcePath:    SemanticEmbeddingBundleModelDataRelativePath,
			destPath:      modelDataPath,
			missingReason: SemanticAssetReasonBundleModelDataMissing,
			invalidReason: SemanticAssetReasonBundleModelDataInvalid,
		},
		{
			label:         "semantic embedding tokenizer",
			sourcePath:    SemanticEmbeddingBundleTokenizerRelativePath,
			destPath:      tokenizerPath,
			missingReason: SemanticAssetReasonBundleTokenizerMissing,
			invalidReason: SemanticAssetReasonBundleTokenizerInvalid,
		},
		{
			label:         "ONNX Runtime shared library",
			sourcePath:    SemanticEmbeddingBundleRuntimeRelativePath(),
			destPath:      runtimePath,
			missingReason: SemanticAssetReasonBundleRuntimeMissing,
			invalidReason: SemanticAssetReasonBundleRuntimeInvalid,
		},
	}

	missing := make([]semanticBundledAsset, 0, len(assets))
	for _, asset := range assets {
		present, err := localSemanticAssetPresent(asset.destPath, asset.label)
		if err != nil {
			return result, err
		}
		if !present {
			missing = append(missing, asset)
		}
	}
	if len(missing) == 0 {
		result.Status = SemanticAssetPreparationReady
		result.Reason = SemanticAssetReasonAlreadyPresent
		return result, nil
	}
	if bundle == nil {
		result.Reason = missing[0].missingReason
		return result, nil
	}
	for _, asset := range missing {
		ok, reason, err := bundledSemanticAssetAvailable(bundle, asset)
		if err != nil {
			return result, err
		}
		if !ok {
			result.Reason = reason
			return result, nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(modelPath), 0o700); err != nil {
		return result, fmt.Errorf("creating semantic embedding asset directory: %w", err)
	}
	for _, asset := range missing {
		if err := copyBundledSemanticAsset(bundle, asset.sourcePath, asset.destPath, asset.label); err != nil {
			return result, err
		}
		switch asset.destPath {
		case modelPath:
			result.ModelCopied = true
		case modelDataPath:
			result.ModelDataCopied = true
		case tokenizerPath:
			result.TokenizerCopied = true
		case runtimePath:
			result.RuntimeCopied = true
		}
	}
	result.Status = SemanticAssetPreparationReady
	result.Reason = SemanticAssetReasonCopied
	return result, nil
}

func SemanticEmbeddingBundleRuntimeRelativePath() string {
	return filepath.Join("assets", "embeddings", SemanticEmbeddingModelID, localONNXRuntimeLibraryFilename())
}

func localSemanticAssetPresent(path string, label string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("checking local %s: %w", label, err)
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("local %s path is not a regular file: %s", label, path)
	}
	return true, nil
}

func bundledSemanticAssetAvailable(bundle fs.FS, asset semanticBundledAsset) (bool, string, error) {
	info, err := fs.Stat(bundle, asset.sourcePath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, asset.missingReason, nil
		}
		return false, "", fmt.Errorf("checking bundled %s: %w", asset.label, err)
	}
	if !info.Mode().IsRegular() {
		return false, asset.invalidReason, nil
	}
	return true, "", nil
}

func copyBundledSemanticAsset(bundle fs.FS, sourcePath string, destPath string, label string) error {
	source, err := bundle.Open(sourcePath)
	if err != nil {
		return fmt.Errorf("opening bundled %s: %w", label, err)
	}
	defer source.Close()

	temp, err := os.CreateTemp(filepath.Dir(destPath), "."+filepath.Base(destPath)+".tmp-*")
	if err != nil {
		return fmt.Errorf("creating temp %s: %w", label, err)
	}
	tempName := temp.Name()
	removeTemp := true
	defer func() {
		if removeTemp {
			_ = os.Remove(tempName)
		}
	}()
	if _, err := io.Copy(temp, source); err != nil {
		_ = temp.Close()
		return fmt.Errorf("copying bundled %s: %w", label, err)
	}
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return fmt.Errorf("setting temp %s permissions: %w", label, err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("closing temp %s: %w", label, err)
	}
	if err := os.Rename(tempName, destPath); err != nil {
		return fmt.Errorf("installing bundled %s: %w", label, err)
	}
	removeTemp = false
	return nil
}
