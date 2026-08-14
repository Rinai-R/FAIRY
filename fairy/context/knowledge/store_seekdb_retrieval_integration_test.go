//go:build integration

package knowledge

import (
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"fairy/runtime/embedding"
	"fairy/runtime/seekdb"
)

func TestRealSeekDBKnowledgeRetrievalCoversANNIsolationAndSwitch(t *testing.T) {
	instance, database, runtimeConfig := openKnowledgeSeekDB(t)
	t.Cleanup(func() { closeKnowledgeSeekDB(t, instance, runtimeConfig.ShutdownLimit) })
	if err := seekdb.MigrateSchema(t.Context(), database, seekdb.BuiltinMigrations()); err != nil {
		t.Fatalf("migrate SeekDB knowledge retrieval schema: %v", err)
	}
	seedKnowledgeIntegrationAuthority(t, database)

	textStore := newKnowledgeSeekDBStore(t, database, runtimeConfig.QueryLimit, nil)
	embedderA := &knowledgeIntegrationEmbedder{spaceID: knowledgeIntegrationSpaceA}
	vectorStore := newKnowledgeSeekDBStore(t, database, runtimeConfig.QueryLimit, embedderA)

	textHit, err := textStore.InsertVerifiedKnowledgeContext(
		t.Context(),
		"hybrid-rank-text-token",
		"这条公开知识只有文本命中，没有向量。",
		knowledgeIntegrationConversation, knowledgeIntegrationTurn, 5000, nil,
	)
	if err != nil {
		t.Fatalf("insert text-only knowledge: %v", err)
	}
	vectorHit, err := vectorStore.InsertVerifiedKnowledgeContext(
		t.Context(),
		"向量补偿知识",
		"这条公开知识没有共享查询词，只靠向量召回。",
		knowledgeIntegrationConversation, knowledgeIntegrationTurn, 9000, nil,
	)
	if err != nil {
		t.Fatalf("insert vector knowledge: %v", err)
	}

	assertKnowledgeImmediateRecall(t, vectorStore, vectorHit.ID)
	assertKnowledgeANNPlanAndConverges(t, database, vectorHit.ID)
	assertKnowledgeWrongDimensionsFailClosed(t, database, runtimeConfig.QueryLimit)
	assertKnowledgeProviderSwitchIsolatesSpaces(t, vectorStore, vectorHit.ID)
	assertKnowledgeTextOnlyDoesNotInventVectors(t, textStore, textHit.ID, vectorHit.ID)
	assertKnowledgeHybridRanking(t, vectorStore, textHit.ID, vectorHit.ID)
	seedKnowledgePrivacyLeakRecords(t, database)
	assertKnowledgePublicRetrievalExcludesPrivate(t, vectorStore)
}

func assertKnowledgeImmediateRecall(t *testing.T, store *Store, id string) {
	t.Helper()
	retrieved, err := store.RetrieveContext(t.Context(), "zzzzimmediaterecall")
	if err != nil {
		t.Fatalf("immediate knowledge retrieve: %v", err)
	}
	if retrieved.SemanticStatus != string(embedding.SemanticStatusUsed) {
		t.Fatalf("immediate semantic status = %q, want used", retrieved.SemanticStatus)
	}
	if !knowledgeRetrievedContainID(retrieved.Entries, id) {
		t.Fatalf("immediate retrieve missed newly inserted knowledge %q: %#v", id, retrieved)
	}
}

func assertKnowledgeANNPlanAndConverges(t *testing.T, database *sql.DB, expectedID string) {
	t.Helper()
	vector := knowledgeIntegrationVectorLiteral()
	args := []any{vector, knowledgeIntegrationSpaceA, vector, 5}
	plan := explainSeekDBQuery(t, database, "EXPLAIN "+knowledgeSeekDBANNSearchSQL, args...)
	for _, fragment := range []string{
		"VECTOR INDEX ADAPTIVE SCAN (PRE-FILTER)",
		"knowledge_entries_status_updated_idx",
		"range_key",
	} {
		if !strings.Contains(plan, fragment) {
			t.Fatalf("knowledge ANN plan lacks %q:\n%s", fragment, plan)
		}
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		var id string
		err := database.QueryRowContext(t.Context(), `
SELECT entry.id
FROM knowledge_entries entry FORCE INDEX (knowledge_entries_status_updated_idx)
WHERE entry.status = 'verified'
  AND entry.embedding_space_id = ?
  AND entry.embedding IS NOT NULL
ORDER BY COSINE_DISTANCE(entry.embedding, ?) APPROXIMATE
LIMIT 1`, knowledgeIntegrationSpaceA, vector).Scan(&id)
		if err == nil {
			if id != expectedID {
				t.Fatalf("ANN knowledge id = %q, want %q", id, expectedID)
			}
			return
		}
		if !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("ANN knowledge query: %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("knowledge ANN did not converge within 5s")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func assertKnowledgeWrongDimensionsFailClosed(t *testing.T, database *sql.DB, queryLimit time.Duration) {
	t.Helper()
	store := newKnowledgeSeekDBStore(t, database, queryLimit, &knowledgeIntegrationEmbedder{
		spaceID: knowledgeIntegrationSpaceA, dims: 3,
	})
	_, err := store.RetrieveContext(t.Context(), "zzzzimmediaterecall")
	if err == nil || !strings.Contains(err.Error(), "embedding dimensions") {
		t.Fatalf("wrong-dimension retrieve error = %v", err)
	}
}

func assertKnowledgeProviderSwitchIsolatesSpaces(t *testing.T, store *Store, spaceAID string) {
	t.Helper()
	store.ReplaceSemanticEmbedder(&knowledgeIntegrationEmbedder{spaceID: knowledgeIntegrationSpaceB})
	retrieved, err := store.RetrieveContext(t.Context(), "zzzzimmediaterecall")
	if err != nil {
		t.Fatalf("provider-switch retrieve: %v", err)
	}
	if knowledgeRetrievedContainID(retrieved.Entries, spaceAID) {
		t.Fatalf("space B retrieve leaked space A vector %q: %#v", spaceAID, retrieved)
	}
	store.ReplaceSemanticEmbedder(&knowledgeIntegrationEmbedder{spaceID: knowledgeIntegrationSpaceA})
}

func assertKnowledgeTextOnlyDoesNotInventVectors(t *testing.T, store *Store, textID, vectorID string) {
	t.Helper()
	text, err := store.RetrieveContext(t.Context(), "hybrid-rank-text-token")
	if err != nil {
		t.Fatalf("text-only retrieve: %v", err)
	}
	if text.SemanticStatus != string(embedding.SemanticStatusUnavailable) {
		t.Fatalf("text-only status = %q, want unavailable", text.SemanticStatus)
	}
	if !knowledgeRetrievedContainID(text.Entries, textID) {
		t.Fatalf("text-only retrieve missed text record: %#v", text)
	}
	vectorOnly, err := store.RetrieveContext(t.Context(), "zzzzimmediaterecall")
	if err != nil {
		t.Fatalf("text-only vector query: %v", err)
	}
	if knowledgeRetrievedContainID(vectorOnly.Entries, vectorID) {
		t.Fatalf("text-only mode invented a vector hit: %#v", vectorOnly)
	}
}

func assertKnowledgeHybridRanking(t *testing.T, store *Store, textID, vectorID string) {
	t.Helper()
	retrieved, err := store.RetrieveContext(t.Context(), "hybrid-rank-text-token")
	if err != nil {
		t.Fatalf("hybrid ranking retrieve: %v", err)
	}
	if !knowledgeRetrievedContainID(retrieved.Entries, textID) {
		t.Fatalf("hybrid ranking missed text record: %#v", retrieved)
	}
	if len(retrieved.Entries) == 0 || retrieved.Entries[0].ID != textID {
		t.Fatalf("hybrid ranking first id = %#v, want text record %q first", retrieved.Entries, textID)
	}
	_ = vectorID
}

func assertKnowledgePublicRetrievalExcludesPrivate(t *testing.T, store *Store) {
	t.Helper()
	retrieved, err := store.RetrieveContext(t.Context(), "玄蓝星航公开知识")
	if err != nil {
		t.Fatalf("public isolation retrieve: %v", err)
	}
	for _, entry := range retrieved.Entries {
		if entry.ID == "privacy-leak-personal" || entry.ID == "privacy-leak-social" {
			t.Fatalf("public retrieval leaked private record: %#v", entry)
		}
	}
}

func explainSeekDBQuery(t *testing.T, database *sql.DB, query string, arguments ...any) string {
	t.Helper()
	rows, err := database.QueryContext(t.Context(), query, arguments...)
	if err != nil {
		t.Fatalf("explain SeekDB query: %v", err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	values := make([]sql.RawBytes, len(columns))
	destinations := make([]any, len(columns))
	for index := range values {
		destinations[index] = &values[index]
	}
	var lines []string
	for rows.Next() {
		if err := rows.Scan(destinations...); err != nil {
			t.Fatal(err)
		}
		parts := make([]string, 0, len(columns))
		for _, value := range values {
			parts = append(parts, string(value))
		}
		lines = append(lines, strings.Join(parts, " | "))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return strings.Join(lines, "\n")
}

func BenchmarkSeekDBKnowledgeRetrieveContext(b *testing.B) {
	instance, database, runtimeConfig := openKnowledgeSeekDB(b)
	b.Cleanup(func() { closeKnowledgeSeekDB(b, instance, runtimeConfig.ShutdownLimit) })
	if err := seekdb.MigrateSchema(b.Context(), database, seekdb.BuiltinMigrations()); err != nil {
		b.Fatalf("migrate SeekDB knowledge bench schema: %v", err)
	}
	seedKnowledgeIntegrationAuthority(b, database)
	store := newKnowledgeSeekDBStore(b, database, runtimeConfig.QueryLimit, &knowledgeIntegrationEmbedder{
		spaceID: knowledgeIntegrationSpaceA,
	})
	if _, err := store.InsertVerifiedKnowledgeContext(
		b.Context(), "bench knowledge", "端侧知识召回基准使用固定向量空间。",
		knowledgeIntegrationConversation, knowledgeIntegrationTurn, 9000, nil,
	); err != nil {
		b.Fatalf("insert bench knowledge: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		retrieved, err := store.RetrieveContext(b.Context(), "zzzzimmediaterecall")
		if err != nil {
			b.Fatal(err)
		}
		if retrieved.SemanticStatus != string(embedding.SemanticStatusUsed) {
			b.Fatalf("bench semantic status = %q", retrieved.SemanticStatus)
		}
	}
}
