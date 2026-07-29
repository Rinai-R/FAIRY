//go:build integration

package memory

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"fairy/coredb"

	"github.com/pgvector/pgvector-go"
)

type mappedSemanticEmbedder struct {
	vectors map[string][]float32
	err     error
}

func (*mappedSemanticEmbedder) Ready() bool            { return true }
func (*mappedSemanticEmbedder) Status() SemanticStatus { return SemanticStatusReady }
func (*mappedSemanticEmbedder) Dims() int              { return SemanticEmbeddingDimensions }
func (embedder *mappedSemanticEmbedder) Embed(texts []string) ([][]float32, error) {
	if embedder.err != nil {
		return nil, embedder.err
	}
	vectors := make([][]float32, len(texts))
	for index, text := range texts {
		vector, ok := embedder.vectors[text]
		if !ok {
			return nil, errors.New("missing integration vector for " + text)
		}
		vectors[index] = slices.Clone(vector)
	}
	return vectors, nil
}

func TestPostgresPgvectorLifecycleHybridRecallAndPublicIsolation(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := coredb.Migrate(ctx, pool.Raw()); err != nil {
		t.Fatal(err)
	}
	unitX := testVector(1, 0)
	unitY := testVector(0, 1)
	knowledgeVector := testVector(0.9, 0.1)
	embedder := &mappedSemanticEmbedder{vectors: map[string][]float32{
		"私人向量目标":          unitX,
		"其他角色私人目标":        unitX,
		"公开主题\n公开向量事实":    knowledgeVector,
		"语义查询":            unitX,
		"无关查询":            unitY,
		"候选主题 状态 当前 语义查询": unitX,
	}}
	store, err := NewStoreFromPoolWithEmbedder(pool, embedder)
	if err != nil {
		t.Fatal(err)
	}
	conversationA, turnA := seedCompletedTurn(t, ctx, store, "character-vector-a")
	_, _ = seedCompletedTurn(t, ctx, store, "character-vector-b")

	personalA, err := store.CreatePersonalMemoryContext(ctx, "relationship", MemoryScope{Type: "character", CharacterID: "character-vector-a"}, "私人向量目标", 9200)
	if err != nil {
		t.Fatal(err)
	}
	personalB, err := store.CreatePersonalMemoryContext(ctx, "relationship", MemoryScope{Type: "character", CharacterID: "character-vector-b"}, "其他角色私人目标", 9100)
	if err != nil {
		t.Fatal(err)
	}
	knowledge, err := store.InsertVerifiedKnowledgeContext(ctx, "公开主题", "公开向量事实", conversationA, turnA, 9000, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertPostgresEmbedding(t, ctx, pool, "personal_memories", personalA.ID, personalA.Content, true)
	assertPostgresEmbedding(t, ctx, pool, "knowledge_entries", knowledge.ID, knowledge.Topic+"\n"+knowledge.Statement, true)
	assertPostgresVectorConstraints(t, ctx, pool, personalA.ID)
	assertPostgresHNSWPlan(t, ctx, pool, unitX)

	private, err := store.RetrieveContext(ctx, "character-vector-a", "语义查询")
	if err != nil {
		t.Fatal(err)
	}
	if private.SemanticStatus != string(SemanticStatusUsed) {
		t.Fatalf("semantic status = %q", private.SemanticStatus)
	}
	if !containsRetrievedPersonalID(private.PersonalMemories, personalA.ID) {
		t.Fatalf("private recall omitted current-character memory: %#v", private.PersonalMemories)
	}
	if containsRetrievedPersonalID(private.PersonalMemories, personalB.ID) {
		t.Fatalf("private recall leaked other-character memory: %#v", private.PersonalMemories)
	}
	if len(private.Knowledge) == 0 || private.Knowledge[0].ID != knowledge.ID {
		t.Fatalf("private knowledge recall = %#v", private.Knowledge)
	}

	public, err := store.RetrievePublicKnowledgeContext(ctx, "语义查询")
	if err != nil {
		t.Fatal(err)
	}
	if len(public.PersonalMemories) != 0 {
		t.Fatalf("public recall leaked personal memory: %#v", public.PersonalMemories)
	}
	if len(public.Knowledge) == 0 || public.Knowledge[0].ID != knowledge.ID {
		t.Fatalf("public knowledge recall = %#v", public.Knowledge)
	}
	ingestRecall, err := store.SearchKnowledgeForIngestContext(
		ctx,
		"语义查询",
		MaxKnowledgeSearchCandidates,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(ingestRecall) == 0 || ingestRecall[0].ID != knowledge.ID {
		t.Fatalf("knowledge ingest recall = %#v", ingestRecall)
	}

	if err := store.TombstonePersonalMemoryContext(ctx, personalA.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.TombstoneKnowledgeContext(ctx, knowledge.ID); err != nil {
		t.Fatal(err)
	}
	afterDelete, err := store.RetrieveContext(ctx, "character-vector-a", "语义查询")
	if err != nil {
		t.Fatal(err)
	}
	if containsRetrievedPersonalID(afterDelete.PersonalMemories, personalA.ID) || len(afterDelete.Knowledge) != 0 {
		t.Fatalf("tombstoned records remained visible: %#v", afterDelete)
	}
}

func TestPostgresPgvectorProviderFailureWritesNothing(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := coredb.Migrate(ctx, pool.Raw()); err != nil {
		t.Fatal(err)
	}
	store, err := NewStoreFromPoolWithEmbedder(pool, &mappedSemanticEmbedder{err: errors.New("provider unavailable")})
	if err != nil {
		t.Fatal(err)
	}
	seedCompletedTurn(t, ctx, store, "character-vector-failure")
	if _, err := store.SearchKnowledgeForIngestContext(
		ctx,
		"向量服务失败时必须重试当前任务。",
		MaxKnowledgeSearchCandidates,
	); err == nil {
		t.Fatal("SearchKnowledgeForIngestContext error = nil")
	}
	if _, err := store.CreatePersonalMemoryContext(ctx, "preference", MemoryScope{Type: "global"}, "不能落库", 9000); err == nil {
		t.Fatal("CreatePersonalMemoryContext error = nil")
	}
	var count int
	if err := pool.Raw().QueryRow(ctx, "SELECT count(*) FROM personal_memories WHERE content = '不能落库'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("failed provider wrote %d records", count)
	}
}

func TestPostgresPgvectorRebuildRepairsMissingVectors(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := coredb.Migrate(ctx, pool.Raw()); err != nil {
		t.Fatal(err)
	}
	textStore, err := NewStoreFromPool(pool)
	if err != nil {
		t.Fatal(err)
	}
	conversationID, turnID := seedCompletedTurn(t, ctx, textStore, "character-vector-rebuild")
	personal, err := textStore.CreatePersonalMemoryContext(ctx, "profile", MemoryScope{Type: "global"}, "待重建记忆", 9000)
	if err != nil {
		t.Fatal(err)
	}
	knowledge, err := textStore.InsertVerifiedKnowledgeContext(ctx, "待重建主题", "待重建事实", conversationID, turnID, 9000, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result, err := textStore.RebuildVectors(ctx, 2); err == nil {
		t.Fatalf("text-only rebuild = %#v, nil error", result)
	}
	assertPostgresEmbedding(t, ctx, pool, "personal_memories", personal.ID, personal.Content, false)
	assertPostgresEmbedding(t, ctx, pool, "knowledge_entries", knowledge.ID, knowledge.Topic+"\n"+knowledge.Statement, false)
	embedder := &mappedSemanticEmbedder{vectors: map[string][]float32{
		"待重建记忆":        testVector(1, 0),
		"待重建主题\n待重建事实": testVector(0, 1),
	}}
	vectorStore, err := NewStoreFromPoolWithEmbedder(pool, embedder)
	if err != nil {
		t.Fatal(err)
	}
	first, err := vectorStore.RebuildVectors(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if first.ScannedItems != 2 || first.UpdatedItems != 2 || first.SkippedItems != 0 || first.FailedItems != 0 {
		t.Fatalf("first rebuild = %#v", first)
	}
	assertPostgresEmbedding(t, ctx, pool, "personal_memories", personal.ID, personal.Content, true)
	assertPostgresEmbedding(t, ctx, pool, "knowledge_entries", knowledge.ID, knowledge.Topic+"\n"+knowledge.Statement, true)
	second, err := vectorStore.RebuildVectors(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if second.ScannedItems != 2 || second.UpdatedItems != 0 || second.SkippedItems != 2 || second.FailedItems != 0 {
		t.Fatalf("second rebuild = %#v", second)
	}
}

func seedCompletedTurn(t *testing.T, ctx context.Context, store *Store, characterID string) (string, string) {
	t.Helper()
	bootstrap, err := store.OpenOrCreateCharacterConversationContext(ctx, characterID)
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
	return bootstrap.Conversation.ID, turn.ID
}

func testVector(x, y float32) []float32 {
	vector := make([]float32, SemanticEmbeddingDimensions)
	vector[0] = x
	vector[1] = y
	return vector
}

func assertPostgresVectorConstraints(t *testing.T, ctx context.Context, pool *coredb.Pool, personalMemoryID string) {
	t.Helper()
	if _, err := pool.Raw().Exec(ctx, `
UPDATE personal_memories
SET embedding = NULL
WHERE id = $1
`, personalMemoryID); err == nil {
		t.Fatal("partial embedding metadata must be rejected")
	}
	if _, err := pool.Raw().Exec(ctx, `
UPDATE personal_memories
SET embedding = '[1,2,3]'::public.vector
WHERE id = $1
`, personalMemoryID); err == nil {
		t.Fatal("wrong embedding dimensions must be rejected")
	}
}

func assertPostgresHNSWPlan(t *testing.T, ctx context.Context, pool *coredb.Pool, queryVector []float32) {
	t.Helper()
	connection, err := pool.Raw().Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Release()
	if _, err := connection.Exec(ctx, "SET enable_seqscan = off"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = connection.Exec(context.Background(), "RESET enable_seqscan")
	}()
	rows, err := connection.Query(ctx, `
EXPLAIN
SELECT id
FROM personal_memories
WHERE status = 'active'
  AND review_status = 'ready'
  AND embedding IS NOT NULL
ORDER BY embedding OPERATOR(public.<=>) $1::public.vector
LIMIT 8
`, pgvector.NewVector(queryVector).String())
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		plan.WriteString(line)
		plan.WriteByte('\n')
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.String(), "personal_memories_embedding_hnsw") {
		t.Fatalf("HNSW index was not usable:\n%s", plan.String())
	}
}
