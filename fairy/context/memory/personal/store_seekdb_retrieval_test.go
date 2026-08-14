package personal

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"fairy/runtime/embedding"
)

func TestSeekDBRetrieveContextSelectsSeekDBWithoutPostgresFallback(t *testing.T) {
	connector := &personalUnitConnector{}
	database := sql.OpenDB(connector)
	t.Cleanup(func() { _ = database.Close() })
	store, err := NewSeekDBStore(database, time.Second, nil)
	if err != nil {
		t.Fatalf("NewSeekDBStore() error = %v", err)
	}
	if store.usesPostgres() || store.pool != nil {
		t.Fatal("SeekDB retrieve store exposed a PostgreSQL fallback")
	}

	_, err = store.RetrieveContext(context.Background(), "character-1", "   ")
	if err != nil {
		t.Fatalf("RetrieveContext(blank usable query) error = %v", err)
	}
	if got := connector.connects.Load(); got != 0 {
		t.Fatalf("unusable query opened %d database connections, want 0", got)
	}

	_, err = store.RetrieveContext(context.Background(), "character-1", "global profile")
	if err == nil {
		t.Fatal("RetrieveContext() error = nil, want database connection error")
	}
	if got := connector.connects.Load(); got == 0 {
		t.Fatal("RetrieveContext did not query SeekDB")
	}
}

func TestSeekDBPersonalHybridSQLKeepsScopeAndStatusBeforeRanking(t *testing.T) {
	for _, fragment := range []string{
		"status = 'active' AND review_status = 'ready'",
		"scope_kind = 'global'",
		"scope_kind = 'character' AND character_id = ?",
		"LOCATE(LOWER(?), LOWER(content))",
		"MATCH(content) AGAINST(? IN NATURAL LANGUAGE MODE)",
	} {
		if !strings.Contains(personalMemorySeekDBSearchSQL, fragment) {
			t.Fatalf("SeekDB personal text SQL is missing %q", fragment)
		}
	}
	if strings.Contains(personalMemorySeekDBSearchSQL, "unassigned_legacy") ||
		strings.Contains(personalMemorySeekDBVectorSearchSQL, "unassigned_legacy") {
		t.Fatal("personal retrieval must not include unassigned_legacy in candidate SQL")
	}
	if !strings.Contains(personalMemorySeekDBLiteralSearchSQL, "LOCATE(LOWER(?)") ||
		strings.Contains(personalMemorySeekDBLiteralSearchSQL, "UNION ALL") ||
		strings.Contains(personalMemorySeekDBLiteralSearchSQL, "MATCH(") {
		t.Fatal("literal-only personal search must keep LOCATE and drop FULLTEXT")
	}
	if !personalMemorySeekDBSearchUsesLiteralOnly("qx%_z9abc") ||
		personalMemorySeekDBSearchUsesLiteralOnly("global profile") {
		t.Fatal("LIKE wildcards should select the literal-only personal search")
	}
	for _, fragment := range []string{
		"COSINE_DISTANCE(embedding, ?)",
		"embedding_space_id = ?",
		"status = 'active' AND review_status = 'ready'",
		"updated_at_ms >= ?",
		"ORDER BY COSINE_DISTANCE(embedding, ?), id ASC",
	} {
		if !strings.Contains(personalMemorySeekDBExactRecentSearchSQL, fragment) {
			t.Fatalf("SeekDB personal exact vector SQL is missing %q", fragment)
		}
	}
	if strings.Contains(personalMemorySeekDBExactRecentSearchSQL, "APPROXIMATE") ||
		strings.Contains(personalMemorySeekDBSearchSQL, "APPROXIMATE") {
		t.Fatal("exact personal retrieval must not use ANN")
	}
	for _, fragment := range []string{
		"FORCE INDEX (personal_memories_scope_status_idx)",
		"ORDER BY COSINE_DISTANCE(embedding, ?) APPROXIMATE",
		"review_status = 'ready' AND status = 'active'",
		"scope_kind = 'global' AND character_id IS NULL",
	} {
		if !strings.Contains(personalMemorySeekDBANNGlobalSearchSQL, fragment) {
			t.Fatalf("SeekDB personal global ANN SQL is missing %q", fragment)
		}
	}
	for _, fragment := range []string{
		"FORCE INDEX (personal_memories_scope_status_idx)",
		"ORDER BY COSINE_DISTANCE(embedding, ?) APPROXIMATE",
		"scope_kind = 'character' AND character_id = ?",
		"review_status = 'ready' AND status = 'active'",
	} {
		if !strings.Contains(personalMemorySeekDBANNCharacterSearchSQL, fragment) {
			t.Fatalf("SeekDB personal character ANN SQL is missing %q", fragment)
		}
	}
	if strings.Contains(personalMemorySeekDBANNGlobalSearchSQL, " OR ") ||
		strings.Contains(personalMemorySeekDBANNCharacterSearchSQL, " OR ") {
		t.Fatal("personal ANN SQL must use index-prefix equality, not OR scope")
	}
	if strings.Contains(personalMemorySeekDBSearchSQL, "knowledge_entries") ||
		strings.Contains(personalMemorySeekDBANNGlobalSearchSQL, "knowledge_entries") ||
		strings.Contains(personalMemorySeekDBANNCharacterSearchSQL, "knowledge_entries") ||
		strings.Contains(personalMemorySeekDBExactRecentSearchSQL, "knowledge_entries") {
		t.Fatal("personal retrieval SQL must not read knowledge_entries")
	}
}

func TestQuerySeekDBPersonalEmbeddingMarksTextOnlyWithoutProvider(t *testing.T) {
	literal, spaceID, status, err := querySeekDBPersonalEmbedding(context.Background(), nil, "usable query")
	if err != nil || literal != "" || spaceID != "" || status != embedding.SemanticStatusUnavailable {
		t.Fatalf("nil embedder = (%q, %q, %s, %v)", literal, spaceID, status, err)
	}

	embedder := &legacyStoreSemanticEmbedder{}
	literal, spaceID, status, err = querySeekDBPersonalEmbedding(context.Background(), embedder, "usable query")
	if err != nil || literal == "" || spaceID != "legacy-sync-space" || status != embedding.SemanticStatusReady {
		t.Fatalf("ready embedder = (%q, %q, %s, %v)", literal, spaceID, status, err)
	}
	if embedder.calls != 1 {
		t.Fatalf("ready embedder calls = %d, want 1", embedder.calls)
	}

	failing := &personalUnitFailingEmbedder{err: errors.New("provider failed")}
	literal, spaceID, status, err = querySeekDBPersonalEmbedding(context.Background(), failing, "usable query")
	if err != nil || literal != "" || spaceID != "" || status != embedding.SemanticStatusUnavailable {
		t.Fatalf("provider failure = (%q, %q, %s, %v)", literal, spaceID, status, err)
	}
}

type personalUnitFailingEmbedder struct{ err error }

func (*personalUnitFailingEmbedder) Ready() bool { return true }
func (*personalUnitFailingEmbedder) Status() embedding.SemanticStatus {
	return embedding.SemanticStatusReady
}
func (*personalUnitFailingEmbedder) ModelID() string { return "unit-failing-space" }
func (*personalUnitFailingEmbedder) Dims() int       { return embedding.Dimensions }
func (embedder *personalUnitFailingEmbedder) Embed([]string) ([][]float32, error) {
	return nil, embedder.err
}

type personalUnitConnector struct {
	connects atomic.Int32
}

func (connector *personalUnitConnector) Connect(context.Context) (driver.Conn, error) {
	connector.connects.Add(1)
	return nil, errors.New("unexpected personal unit database connection")
}

func (*personalUnitConnector) Driver() driver.Driver { return personalUnitDriver{} }

type personalUnitDriver struct{}

func (personalUnitDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("unexpected personal unit database open")
}
