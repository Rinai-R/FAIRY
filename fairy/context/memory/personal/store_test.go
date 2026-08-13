package personal

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	"fairy/runtime/embedding"
)

func TestNewStoreFromPoolRequiresPool(t *testing.T) {
	store, err := NewStoreFromPool(nil, nil)
	if store != nil || !errors.Is(err, ErrDatabasePoolEmpty) {
		t.Fatalf("NewStoreFromPool(nil) = (%v, %v), want (nil, %v)", store, err, ErrDatabasePoolEmpty)
	}
}

func TestPrepareEmbeddingsKeepsLegacyProviderForSynchronousWrites(t *testing.T) {
	provider := &legacyStoreSemanticEmbedder{}
	store, err := NewSeekDBStore(new(sql.DB), time.Second, provider)
	if err != nil {
		t.Fatal(err)
	}
	values, err := store.PrepareEmbeddings([]string{"content"})
	if err != nil {
		t.Fatal(err)
	}
	if provider.calls != 1 || len(values) != 1 || !values[0].Enabled() {
		t.Fatalf("legacy synchronous embedding = calls:%d values:%#v", provider.calls, values)
	}
	_, err = store.PrepareEmbeddingsContext(t.Context(), []string{"content"})
	if !errors.Is(err, embedding.ErrSemanticCancellationUnsupported) {
		t.Fatalf("legacy background embedding error = %v", err)
	}
	if provider.calls != 1 {
		t.Fatalf("legacy background provider calls = %d, want unchanged", provider.calls)
	}
}

type legacyStoreSemanticEmbedder struct {
	calls int
}

func (*legacyStoreSemanticEmbedder) Ready() bool { return true }
func (*legacyStoreSemanticEmbedder) Status() embedding.SemanticStatus {
	return embedding.SemanticStatusReady
}
func (*legacyStoreSemanticEmbedder) ModelID() string { return "legacy-sync-space" }
func (*legacyStoreSemanticEmbedder) Dims() int       { return embedding.Dimensions }
func (embedder *legacyStoreSemanticEmbedder) Embed(texts []string) ([][]float32, error) {
	embedder.calls++
	results := make([][]float32, len(texts))
	for index := range results {
		results[index] = make([]float32, embedding.Dimensions)
		results[index][0] = 1
	}
	return results, nil
}
