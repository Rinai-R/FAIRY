//go:build integration

package memory

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"fairy/memory/semantic"
	pgstore "fairy/postgres"
	"fairy/vectorindex"
)

type failingSemanticIndex struct{}

func (failingSemanticIndex) Ready(context.Context) error { return nil }
func (failingSemanticIndex) Search(context.Context, []float32, string, string, int) ([]vectorindex.SearchHit, error) {
	return nil, errors.New("qdrant timeout")
}

func TestPostgresSemanticRetrievalEnforcesRelationshipScopeAndTruth(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := pgstore.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	store, err := NewStoreFromPool(pool)
	if err != nil {
		t.Fatal(err)
	}
	index := openIsolatedWorkerVectorIndex(t, ctx)
	bootstrap, err := store.OpenOrCreateCharacterConversationContext(ctx, "character-a")
	if err != nil {
		t.Fatal(err)
	}
	turn, err := store.BeginTurnContext(ctx, bootstrap.Conversation.ID, "source")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteTurnContext(ctx, bootstrap.Conversation.ID, turn.ID, "reply"); err != nil {
		t.Fatal(err)
	}
	memoryA, err := store.CreatePersonalMemoryContext(ctx, "relationship", MemoryScope{Type: "character", CharacterID: "character-a"}, "角色A喜欢安静陪伴", 9000)
	if err != nil {
		t.Fatal(err)
	}
	bootstrapB, err := store.OpenOrCreateCharacterConversationContext(ctx, "character-b")
	if err != nil {
		t.Fatal(err)
	}
	turn, err = store.BeginTurnContext(ctx, bootstrapB.Conversation.ID, "second source")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteTurnContext(ctx, bootstrapB.Conversation.ID, turn.ID, "second reply"); err != nil {
		t.Fatal(err)
	}
	memoryB, err := store.CreatePersonalMemoryContext(ctx, "relationship", MemoryScope{Type: "character", CharacterID: "character-b"}, "角色B喜欢安静陪伴", 9000)
	if err != nil {
		t.Fatal(err)
	}
	vector := make([]float32, SemanticEmbeddingDimensions)
	vector[0] = 1
	embedder := postgresWorkerEmbedder{vector: vector}
	worker, err := store.ProcessEmbeddingJobsWithVectorIndex(ctx, embedder, index, 10)
	if err != nil || worker.Succeeded != 2 {
		t.Fatalf("worker = (%#v, %v)", worker, err)
	}
	first, err := store.RetrieveWithSemanticVectorIndex(ctx, "character-a", "陪伴", embedder, index)
	if err != nil {
		t.Fatal(err)
	}
	if first.SemanticStatus != string(semantic.StatusUsed) || len(first.PersonalMemories) != 1 || first.PersonalMemories[0].ID != memoryA.ID {
		t.Fatalf("character A retrieval = %#v; character B id = %s", first, memoryB.ID)
	}
	repeated, err := store.RetrieveWithSemanticVectorIndex(ctx, "character-a", "角色安静陪伴", embedder, index)
	if err != nil {
		t.Fatal(err)
	}
	repeatedAgain, err := store.RetrieveWithSemanticVectorIndex(ctx, "character-a", "角色安静陪伴", embedder, index)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(repeated, repeatedAgain) {
		t.Fatalf("repeated retrieval changed order: %#v then %#v", repeated, repeatedAgain)
	}
	if err := store.TombstonePersonalMemoryContext(ctx, memoryA.ID); err != nil {
		t.Fatal(err)
	}
	stale, err := store.RetrieveWithSemanticVectorIndex(ctx, "character-a", "陪伴", embedder, index)
	if err != nil {
		t.Fatal(err)
	}
	if !stale.Empty() || stale.SemanticStatus != string(semantic.StatusReady) {
		t.Fatalf("stale point retrieval = %#v", stale)
	}
}

func TestPostgresSemanticRetrievalFallsBackToTrigramWhenQdrantFails(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := pgstore.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	store, err := NewStoreFromPool(pool)
	if err != nil {
		t.Fatal(err)
	}
	record := seedPostgresEmbeddingMemory(t, ctx, store, "retrieval-fallback", "安静环境搜索测试")
	vector := make([]float32, SemanticEmbeddingDimensions)
	vector[0] = 1
	result, err := store.RetrieveWithSemanticVectorIndex(ctx, "character-retrieval-fallback", "安静环境搜索测试", postgresWorkerEmbedder{vector: vector}, failingSemanticIndex{})
	if err != nil {
		t.Fatal(err)
	}
	if result.SemanticStatus != string(semantic.StatusUnavailable) || len(result.PersonalMemories) != 1 || result.PersonalMemories[0].ID != record.ID {
		t.Fatalf("fallback result = %#v", result)
	}
}
