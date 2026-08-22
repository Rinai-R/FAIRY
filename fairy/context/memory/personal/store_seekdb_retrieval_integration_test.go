//go:build integration

package personal

import (
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"fairy/runtime/embedding"
	"fairy/runtime/seekdb"
)

func TestRealSeekDBPersonalRetrievalCoversANNScopeAndSwitch(t *testing.T) {
	instance, database, runtimeConfig := openPersonalSeekDB(t)
	t.Cleanup(func() { closePersonalSeekDB(t, instance, runtimeConfig.ShutdownLimit) })
	if err := seekdb.MigrateSchema(t.Context(), database, seekdb.BuiltinMigrations()); err != nil {
		t.Fatalf("migrate SeekDB personal retrieval schema: %v", err)
	}
	seedPersonalIntegrationAuthority(t, database)

	textStore := newPersonalSeekDBStore(t, database, runtimeConfig.QueryLimit, nil)
	vectorStore := newPersonalSeekDBStore(t, database, runtimeConfig.QueryLimit, personalIntegrationEmbedder{})

	textHit, err := textStore.CreatePersonalMemoryContext(
		t.Context(), "profile", Scope{Type: "global"},
		"hybrid-rank-text-token 用户在公开场合先听完再回应", 5000,
	)
	if err != nil {
		t.Fatalf("create text-only personal memory: %v", err)
	}
	vectorHit, err := vectorStore.CreatePersonalMemoryContext(
		t.Context(), "preference", Scope{Type: "global"},
		"这条个人记忆没有共享查询词，只靠向量召回。", 9000,
	)
	if err != nil {
		t.Fatalf("create vector personal memory: %v", err)
	}

	assertPersonalImmediateRecall(t, vectorStore, vectorHit.ID)
	assertPersonalANNConverges(t, database, vectorHit.ID)
	assertPersonalWrongDimensionsFailClosed(t, database, runtimeConfig.QueryLimit)
	assertPersonalProviderSwitchIsolatesSpaces(t, vectorStore, vectorHit.ID)
	assertPersonalTextOnlyDoesNotInventVectors(t, textStore, textHit.ID, vectorHit.ID)
	assertPersonalHybridRanking(t, vectorStore, textHit.ID)
	other, err := vectorStore.CreatePersonalMemoryContext(
		t.Context(), "relationship", Scope{Type: "character", CharacterID: personalCharacterB},
		"只属于角色 B 的相似向量关系", 9900,
	)
	if err != nil {
		t.Fatalf("create other-character vector memory: %v", err)
	}
	assertPersonalCharacterIsolation(t, vectorStore, other.ID)
}

func assertPersonalImmediateRecall(t *testing.T, store *Store, id string) {
	t.Helper()
	retrieved, err := store.RetrieveContext(t.Context(), personalCharacterA, "zzzzimmediaterecall")
	if err != nil {
		t.Fatalf("immediate personal retrieve: %v", err)
	}
	if retrieved.SemanticStatus != string(embedding.SemanticStatusUsed) {
		t.Fatalf("immediate semantic status = %q, want used", retrieved.SemanticStatus)
	}
	if !personalRetrievedContain(retrieved.PersonalMemories, id) {
		t.Fatalf("immediate retrieve missed newly inserted memory %q: %#v", id, retrieved)
	}
}

func assertPersonalANNConverges(t *testing.T, database *sql.DB, expectedID string) {
	t.Helper()
	vector := personalIntegrationVectorLiteral()
	// The embedded libseekdb v1.3 C ABI executes EXPLAIN but exposes it as a
	// zero-column result. The schema integration suite proves the observable
	// scope-first contract with more excluded rows than the ANN limit. Here the
	// endpoint path proves that the same filtered ANN query converges.
	deadline := time.Now().Add(5 * time.Second)
	for {
		var id string
		err := database.QueryRowContext(t.Context(), `
SELECT id
FROM personal_memories FORCE INDEX (personal_memories_scope_status_idx)
WHERE scope_kind = 'global' AND character_id IS NULL
  AND review_status = 'ready' AND status = 'active'
  AND embedding IS NOT NULL
  AND embedding_space_id = ?
ORDER BY COSINE_DISTANCE(embedding, ?) APPROXIMATE
LIMIT 1`, personalIntegrationEmbeddingSpace, vector).Scan(&id)
		if err == nil {
			if id != expectedID {
				t.Fatalf("ANN personal id = %q, want %q", id, expectedID)
			}
			return
		}
		if !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("ANN personal query: %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("personal ANN did not converge within 5s")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func assertPersonalWrongDimensionsFailClosed(t *testing.T, database *sql.DB, queryLimit time.Duration) {
	t.Helper()
	store := newPersonalSeekDBStore(t, database, queryLimit, personalWrongDimEmbedder{})
	_, err := store.RetrieveContext(t.Context(), personalCharacterA, "zzzzimmediaterecall")
	if err == nil || !strings.Contains(err.Error(), "embedding dimensions") {
		t.Fatalf("wrong-dimension retrieve error = %v", err)
	}
}

func assertPersonalProviderSwitchIsolatesSpaces(t *testing.T, store *Store, spaceAID string) {
	t.Helper()
	store.ReplaceSemanticEmbedder(personalOtherSpaceEmbedder{})
	retrieved, err := store.RetrieveContext(t.Context(), personalCharacterA, "zzzzimmediaterecall")
	if err != nil {
		t.Fatalf("provider-switch retrieve: %v", err)
	}
	if personalRetrievedContain(retrieved.PersonalMemories, spaceAID) {
		t.Fatalf("space B retrieve leaked space A vector %q: %#v", spaceAID, retrieved)
	}
	store.ReplaceSemanticEmbedder(personalIntegrationEmbedder{})
}

func assertPersonalTextOnlyDoesNotInventVectors(t *testing.T, store *Store, textID, vectorID string) {
	t.Helper()
	text, err := store.RetrieveContext(t.Context(), personalCharacterA, "hybrid-rank-text-token")
	if err != nil {
		t.Fatalf("text-only retrieve: %v", err)
	}
	if text.SemanticStatus != string(embedding.SemanticStatusUnavailable) {
		t.Fatalf("text-only status = %q, want unavailable", text.SemanticStatus)
	}
	if !personalRetrievedContain(text.PersonalMemories, textID) {
		t.Fatalf("text-only retrieve missed text record: %#v", text)
	}
	vectorOnly, err := store.RetrieveContext(t.Context(), personalCharacterA, "zzzzimmediaterecall")
	if err != nil {
		t.Fatalf("text-only vector query: %v", err)
	}
	if personalRetrievedContain(vectorOnly.PersonalMemories, vectorID) {
		t.Fatalf("text-only mode invented a vector hit: %#v", vectorOnly)
	}
}

func assertPersonalHybridRanking(t *testing.T, store *Store, textID string) {
	t.Helper()
	retrieved, err := store.RetrieveContext(t.Context(), personalCharacterA, "hybrid-rank-text-token")
	if err != nil {
		t.Fatalf("hybrid ranking retrieve: %v", err)
	}
	if len(retrieved.PersonalMemories) == 0 || retrieved.PersonalMemories[0].ID != textID {
		t.Fatalf("hybrid ranking first id = %#v, want text record %q first", retrieved.PersonalMemories, textID)
	}
}

func assertPersonalCharacterIsolation(t *testing.T, store *Store, otherID string) {
	t.Helper()
	retrieved, err := store.RetrieveContext(t.Context(), personalCharacterA, "zzzzimmediaterecall")
	if err != nil {
		t.Fatalf("character isolation retrieve: %v", err)
	}
	if personalRetrievedContain(retrieved.PersonalMemories, otherID) {
		t.Fatalf("personal retrieve leaked other character: %#v", retrieved)
	}
}

func BenchmarkSeekDBPersonalRetrieveContext(b *testing.B) {
	instance, database, runtimeConfig := openPersonalSeekDB(b)
	b.Cleanup(func() { closePersonalSeekDB(b, instance, runtimeConfig.ShutdownLimit) })
	if err := seekdb.MigrateSchema(b.Context(), database, seekdb.BuiltinMigrations()); err != nil {
		b.Fatalf("migrate SeekDB personal bench schema: %v", err)
	}
	seedPersonalIntegrationAuthority(b, database)
	store := newPersonalSeekDBStore(b, database, runtimeConfig.QueryLimit, personalIntegrationEmbedder{})
	if _, err := store.CreatePersonalMemoryContext(
		b.Context(), "profile", Scope{Type: "global"}, "端侧个人记忆召回基准使用固定向量空间。", 9000,
	); err != nil {
		b.Fatalf("create bench personal memory: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		retrieved, err := store.RetrieveContext(b.Context(), personalCharacterA, "zzzzimmediaterecall")
		if err != nil {
			b.Fatal(err)
		}
		if retrieved.SemanticStatus != string(embedding.SemanticStatusUsed) {
			b.Fatalf("bench semantic status = %q", retrieved.SemanticStatus)
		}
	}
}

type personalWrongDimEmbedder struct{}

func (personalWrongDimEmbedder) Ready() bool { return true }
func (personalWrongDimEmbedder) Status() embedding.SemanticStatus {
	return embedding.SemanticStatusReady
}
func (personalWrongDimEmbedder) ModelID() string { return personalIntegrationEmbeddingSpace }
func (personalWrongDimEmbedder) Dims() int       { return 3 }
func (personalWrongDimEmbedder) Embed([]string) ([][]float32, error) {
	return [][]float32{{1, 0, 0}}, nil
}

type personalOtherSpaceEmbedder struct{}

func (personalOtherSpaceEmbedder) Ready() bool { return true }
func (personalOtherSpaceEmbedder) Status() embedding.SemanticStatus {
	return embedding.SemanticStatusReady
}
func (personalOtherSpaceEmbedder) ModelID() string { return "fairy-personal-other-space" }
func (personalOtherSpaceEmbedder) Dims() int       { return embedding.Dimensions }
func (personalOtherSpaceEmbedder) Embed(texts []string) ([][]float32, error) {
	results := make([][]float32, len(texts))
	for index := range results {
		results[index] = make([]float32, embedding.Dimensions)
		results[index][0] = 1
	}
	return results, nil
}
