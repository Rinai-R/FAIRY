package knowledge

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"fairy/runtime/embedding"
)

func TestNewSeekDBStoreValidatesAndSelectsOneAuthority(t *testing.T) {
	if _, err := NewSeekDBStore(nil, time.Second, nil); !errors.Is(err, ErrSeekDBConnectionEmpty) {
		t.Fatalf("NewSeekDBStore(nil) error = %v, want %v", err, ErrSeekDBConnectionEmpty)
	}

	connector := &knowledgeUnitConnector{}
	database := sql.OpenDB(connector)
	t.Cleanup(func() { _ = database.Close() })
	if _, err := NewSeekDBStore(database, 0, nil); !errors.Is(err, ErrSeekDBQueryLimitInvalid) {
		t.Fatalf("NewSeekDBStore(zero query limit) error = %v, want %v", err, ErrSeekDBQueryLimitInvalid)
	}

	store, err := NewSeekDBStore(database, time.Second, nil)
	if err != nil {
		t.Fatalf("NewSeekDBStore() error = %v", err)
	}
	if store.seekDB != database || !store.usesSeekDB() {
		t.Fatal("NewSeekDBStore did not retain SeekDB as its authority")
	}
	if store.pool != nil || store.usesPostgres() {
		t.Fatal("NewSeekDBStore unexpectedly configured a PostgreSQL fallback")
	}
	if !store.KnowledgeIngestReady() {
		t.Fatal("SeekDB knowledge ingest should be ready")
	}
	if got := connector.connects.Load(); got != 0 {
		t.Fatalf("constructor opened %d database connections, want 0", got)
	}
}

func TestSeekDBRetrieveContextValidatesQueryBeforeOpening(t *testing.T) {
	connector := &knowledgeUnitConnector{}
	database := sql.OpenDB(connector)
	t.Cleanup(func() { _ = database.Close() })
	store, err := NewSeekDBStore(database, time.Second, nil)
	if err != nil {
		t.Fatalf("NewSeekDBStore() error = %v", err)
	}

	_, err = store.RetrieveContext(context.Background(), "   ")
	if err == nil || err.Error() != "knowledge search query is required" {
		t.Fatalf("RetrieveContext(blank) error = %v, want query required", err)
	}
	if got := connector.connects.Load(); got != 0 {
		t.Fatalf("blank RetrieveContext opened %d database connections, want 0", got)
	}

	_, err = store.RetrieveContext(context.Background(), "verified fact")
	if err == nil {
		t.Fatal("RetrieveContext() error = nil, want database connection error")
	}
	if got := connector.connects.Load(); got == 0 {
		t.Fatal("RetrieveContext did not query SeekDB")
	}
}

func TestSeekDBKnowledgeHybridSQLKeepsVerifiedScopeBeforeRanking(t *testing.T) {
	for _, fragment := range []string{
		"status = 'verified'",
		"COSINE_DISTANCE(entry.embedding, ?)",
		"embedding_space_id = ?",
		"updated_at_ms >= ?",
		"ORDER BY COSINE_DISTANCE(entry.embedding, ?), entry.id ASC",
	} {
		if !strings.Contains(knowledgeSeekDBExactRecentSearchSQL, fragment) {
			t.Fatalf("SeekDB knowledge exact vector SQL is missing %q", fragment)
		}
	}
	if strings.Contains(knowledgeSeekDBExactRecentSearchSQL, "APPROXIMATE") {
		t.Fatal("exact recent knowledge vector SQL must not use ANN")
	}
	for _, fragment := range []string{
		"FORCE INDEX (knowledge_entries_status_updated_idx)",
		"status = 'verified'",
		"ORDER BY COSINE_DISTANCE(entry.embedding, ?) APPROXIMATE",
	} {
		if !strings.Contains(knowledgeSeekDBANNSearchSQL, fragment) {
			t.Fatalf("SeekDB knowledge ANN SQL is missing %q", fragment)
		}
	}
	for _, table := range []string{"personal_memories", "social_memory_entries"} {
		if strings.Contains(knowledgeSeekDBANNSearchSQL, table) ||
			strings.Contains(knowledgeSeekDBExactRecentSearchSQL, table) ||
			strings.Contains(knowledgeIngestSearchSeekDBSQL, table) {
			t.Fatalf("public retrieval SQL must not read %s", table)
		}
	}
	if got := strings.Count(knowledgeSeekDBANNSearchSQL, "status = 'verified'"); got != 1 {
		t.Fatalf("verified status predicates in ANN SQL = %d, want 1", got)
	}
}

func TestQuerySeekDBKnowledgeEmbeddingMarksTextOnlyWithoutProvider(t *testing.T) {
	literal, spaceID, status, err := querySeekDBKnowledgeEmbedding(context.Background(), nil, "verified fact")
	if err != nil || literal != "" || spaceID != "" || status != embedding.SemanticStatusUnavailable {
		t.Fatalf("nil embedder = (%q, %q, %s, %v)", literal, spaceID, status, err)
	}

	embedder := &knowledgeUnitLegacyEmbedder{}
	literal, spaceID, status, err = querySeekDBKnowledgeEmbedding(context.Background(), embedder, "verified fact")
	if err != nil || literal == "" || spaceID != "unit-model" || status != embedding.SemanticStatusReady {
		t.Fatalf("ready embedder = (%q, %q, %s, %v)", literal, spaceID, status, err)
	}
	if embedder.calls.Load() != 1 {
		t.Fatalf("ready embedder calls = %d, want 1", embedder.calls.Load())
	}
}

func TestSeekDBInsertValidationPrecedesQueryAndProvider(t *testing.T) {
	validSource := AssistantSource{
		Title:           "source",
		URL:             "https://example.test/source",
		Snippet:         "source evidence",
		Rank:            1,
		FetchedAtUnixMS: 1,
	}
	tests := []struct {
		name           string
		topic          string
		conversationID string
		sources        []AssistantSource
	}{
		{name: "rank must equal one", topic: "topic", conversationID: "conversation", sources: []AssistantSource{{
			Title: validSource.Title, URL: validSource.URL, Snippet: validSource.Snippet,
			Rank: 2, FetchedAtUnixMS: validSource.FetchedAtUnixMS,
		}}},
		{name: "at most one source", topic: "topic", conversationID: "conversation", sources: []AssistantSource{validSource, validSource}},
		{name: "topic control", topic: "topic\x00", conversationID: "conversation"},
		{name: "strict ascii id", topic: "topic", conversationID: "会话"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			connector := &knowledgeUnitConnector{}
			database := sql.OpenDB(connector)
			t.Cleanup(func() { _ = database.Close() })
			embedder := &knowledgeUnitContextEmbedder{legacy: &knowledgeUnitLegacyEmbedder{}}
			store, err := NewSeekDBStore(database, time.Second, embedder)
			if err != nil {
				t.Fatalf("NewSeekDBStore() error = %v", err)
			}

			_, err = store.InsertVerifiedKnowledgeContext(
				context.Background(), test.topic, "statement", test.conversationID,
				"turn", 7500, test.sources,
			)
			if err == nil {
				t.Fatal("InsertVerifiedKnowledgeContext() error = nil, want validation error")
			}
			if got := connector.connects.Load(); got != 0 {
				t.Fatalf("validation opened %d database connections, want 0", got)
			}
			if got := embedder.legacy.calls.Load(); got != 0 {
				t.Fatalf("validation invoked legacy provider %d times, want 0", got)
			}
			if got := embedder.contextCalls.Load(); got != 0 {
				t.Fatalf("validation invoked contextual provider %d times, want 0", got)
			}
		})
	}
}

func TestPrepareKnowledgeEmbeddingsRoutesContextExplicitly(t *testing.T) {
	t.Run("sync accepts legacy provider", func(t *testing.T) {
		embedder := &knowledgeUnitLegacyEmbedder{}
		values, err := prepareKnowledgeEmbeddings(
			context.Background(), embedder, []string{"topic\nstatement"}, false,
		)
		if err != nil {
			t.Fatalf("prepareKnowledgeEmbeddings() error = %v", err)
		}
		if len(values) != 1 || !values[0].Enabled() {
			t.Fatalf("prepareKnowledgeEmbeddings() values = %#v, want one enabled value", values)
		}
		if got := embedder.calls.Load(); got != 1 {
			t.Fatalf("legacy Embed calls = %d, want 1", got)
		}
	})

	t.Run("context rejects legacy provider before invocation", func(t *testing.T) {
		embedder := &knowledgeUnitLegacyEmbedder{}
		_, err := prepareKnowledgeEmbeddings(
			context.Background(), embedder, []string{"topic\nstatement"}, true,
		)
		if !errors.Is(err, embedding.ErrSemanticCancellationUnsupported) {
			t.Fatalf("prepareKnowledgeEmbeddings() error = %v, want %v", err, embedding.ErrSemanticCancellationUnsupported)
		}
		if got := embedder.calls.Load(); got != 0 {
			t.Fatalf("legacy Embed calls = %d, want 0", got)
		}
	})

	t.Run("context invokes contextual provider", func(t *testing.T) {
		embedder := &knowledgeUnitContextEmbedder{legacy: &knowledgeUnitLegacyEmbedder{}}
		values, err := prepareKnowledgeEmbeddings(
			context.Background(), embedder, []string{"topic\nstatement"}, true,
		)
		if err != nil {
			t.Fatalf("prepareKnowledgeEmbeddings() error = %v", err)
		}
		if len(values) != 1 || !values[0].Enabled() {
			t.Fatalf("prepareKnowledgeEmbeddings() values = %#v, want one enabled value", values)
		}
		if got := embedder.contextCalls.Load(); got != 1 {
			t.Fatalf("contextual EmbedContext calls = %d, want 1", got)
		}
		if got := embedder.legacy.calls.Load(); got != 0 {
			t.Fatalf("legacy Embed calls = %d, want 0", got)
		}
	})
}

func TestFinalizeSeekDBVectorItemUsesRawHashAndPreservesFields(t *testing.T) {
	item := vectorItem{ID: "knowledge", Topic: "topic line one\ntopic line two", Statement: "statement"}
	content := item.Topic + "\n" + item.Statement
	hash := sha256.Sum256([]byte(content))
	finalizeSeekDBVectorItem(
		&item, "unit-model", sql.NullString{String: "unit-model", Valid: true}, hash[:], true,
	)
	if !item.Current {
		t.Fatal("raw binary content hash should mark the vector current")
	}
	if item.Content != content {
		t.Fatalf("vector content = %q, want %q", item.Content, content)
	}
	if item.Topic != "topic line one\ntopic line two" || item.Statement != "statement" {
		t.Fatalf("vector item fields changed: topic=%q statement=%q", item.Topic, item.Statement)
	}

	uppercaseHex := []byte(strings.ToUpper(embedding.ContentHash(content)))
	finalizeSeekDBVectorItem(
		&item, "unit-model", sql.NullString{String: "unit-model", Valid: true}, uppercaseHex, true,
	)
	if item.Current {
		t.Fatal("hex text must not be accepted as the physical BINARY(32) hash")
	}
}

func TestSeekDBDocumentActionValidationPrecedesQueryAndProvider(t *testing.T) {
	task, document := knowledgeUnitDocument()
	validAdd := DocumentAction{
		Operation: MutationAdd, Content: "新增事实已经生效并保存完整来源。",
		ConfidenceBasisPoints: 9100, Evidence: "新增事实已经生效",
	}
	tests := []struct {
		name     string
		supplied []string
		actions  []DocumentAction
	}{
		{
			name: "reject UPDATE",
			actions: []DocumentAction{{
				Operation: "UPDATE", MemoryID: "knowledge-one",
				Content: validAdd.Content, ConfidenceBasisPoints: 9100,
				Evidence: validAdd.Evidence,
			}},
			supplied: []string{"knowledge-one"},
		},
		{
			name:     "unknown alias",
			actions:  []DocumentAction{{Operation: MutationReplace, MemoryID: "unknown-alias", Content: validAdd.Content, ConfidenceBasisPoints: 9100, Evidence: validAdd.Evidence}},
			supplied: []string{"knowledge-one"},
		},
		{
			name: "duplicate target",
			actions: []DocumentAction{
				{Operation: MutationDelete, MemoryID: "knowledge-one", Evidence: validAdd.Evidence},
				{Operation: MutationNone, MemoryID: "knowledge-one", Evidence: "无需修改的知识仍然保持原样"},
			},
			supplied: []string{"knowledge-one"},
		},
		{
			name: "duplicate content",
			actions: []DocumentAction{
				validAdd,
				{Operation: MutationAdd, Content: validAdd.Content, ConfidenceBasisPoints: 9200, Evidence: "旧的直接知识由新事实完整替代"},
			},
		},
		{
			name:    "forged evidence",
			actions: []DocumentAction{{Operation: MutationAdd, Content: validAdd.Content, ConfidenceBasisPoints: 9100, Evidence: "这段证据并不在当前文档正文中。"}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			connector := &knowledgeUnitConnector{}
			database := sql.OpenDB(connector)
			t.Cleanup(func() { _ = database.Close() })
			embedder := &knowledgeUnitContextEmbedder{legacy: &knowledgeUnitLegacyEmbedder{}}
			store, err := NewSeekDBStore(database, time.Second, embedder)
			if err != nil {
				t.Fatalf("NewSeekDBStore() error = %v", err)
			}

			_, err = store.CommitKnowledgeDocumentActionsContext(
				context.Background(), task, document, test.supplied, test.actions,
			)
			if err == nil {
				t.Fatal("CommitKnowledgeDocumentActionsContext() error = nil, want validation error")
			}
			if got := connector.connects.Load(); got != 0 {
				t.Fatalf("validation opened %d database connections, want 0", got)
			}
			if got := embedder.legacy.calls.Load(); got != 0 {
				t.Fatalf("validation invoked legacy provider %d times, want 0", got)
			}
			if got := embedder.contextCalls.Load(); got != 0 {
				t.Fatalf("validation invoked contextual provider %d times, want 0", got)
			}
		})
	}
}

func knowledgeUnitDocument() (IngestTask, Document) {
	content := "文档说明新增事实已经生效。旧的直接知识由新事实完整替代。已经失效的直接知识不应继续召回。无需修改的知识仍然保持原样。"
	canonicalURL := "https://example.test/knowledge/unit"
	task := IngestTask{
		ID: "unit-task", ConversationID: "conversation", TurnID: "turn",
		Source: IngestSource{
			ID: "unit-source", Title: "端侧知识文档", URL: canonicalURL,
			Snippet: "端侧知识文档摘要", Rank: 1, FetchedAtUnixMS: 1,
		},
	}
	return task, Document{
		SourceID: task.Source.ID, CanonicalURL: canonicalURL, Title: task.Source.Title,
		Content: content, ContentHash: embedding.ContentHash(content),
		EvidenceID: "unit-evidence", ContentType: "text/plain", FetchedAtUnixMS: 1,
	}
}

func TestSeekDBKnowledgeIngestSearchSQLKeepsVerifiedBranchesDeterministic(t *testing.T) {
	for _, fragment := range []string{
		"LOCATE(LOWER(?)",
		"MATCH(topic, statement) AGAINST(? IN NATURAL LANGUAGE MODE)",
		"UNION ALL",
		"MAX(score)",
		"ORDER BY ranked.score DESC, entry.confidence_basis_points DESC",
		"entry.updated_at_ms DESC, entry.id ASC",
	} {
		if !strings.Contains(knowledgeIngestSearchSeekDBSQL, fragment) {
			t.Fatalf("SeekDB ingest search SQL is missing %q", fragment)
		}
	}
	if got := strings.Count(knowledgeIngestSearchSeekDBSQL, "status = 'verified'"); got < 3 {
		t.Fatalf("verified status predicates = %d, want one per branch plus final isolation", got)
	}
	if strings.Contains(knowledgeIngestSearchSeekDBSQL, "FORCE INDEX") {
		t.Fatal("SeekDB ingest search must not force a B-tree index across FULLTEXT matching")
	}
	if !strings.Contains(knowledgeIngestLiteralSearchSeekDBSQL, "LOCATE(LOWER(?)") ||
		strings.Contains(knowledgeIngestLiteralSearchSeekDBSQL, "UNION ALL") ||
		strings.Contains(knowledgeIngestLiteralSearchSeekDBSQL, "MATCH(") {
		t.Fatal("literal-only ingest search must keep LOCATE and drop FULLTEXT")
	}
	if !knowledgeIngestSearchUsesLiteralOnly("qx%_z9") || knowledgeIngestSearchUsesLiteralOnly("aurora protocol") {
		t.Fatal("LIKE wildcards should select the literal-only ingest search")
	}
}

type knowledgeUnitConnector struct {
	connects atomic.Int32
}

func (connector *knowledgeUnitConnector) Connect(context.Context) (driver.Conn, error) {
	connector.connects.Add(1)
	return nil, errors.New("unexpected knowledge unit database connection")
}

func (*knowledgeUnitConnector) Driver() driver.Driver { return knowledgeUnitDriver{} }

type knowledgeUnitDriver struct{}

func (knowledgeUnitDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("unexpected knowledge unit database open")
}

type knowledgeUnitLegacyEmbedder struct {
	calls atomic.Int32
}

func (*knowledgeUnitLegacyEmbedder) Ready() bool { return true }

func (*knowledgeUnitLegacyEmbedder) Status() embedding.SemanticStatus {
	return embedding.SemanticStatusReady
}

func (*knowledgeUnitLegacyEmbedder) ModelID() string { return "unit-model" }

func (*knowledgeUnitLegacyEmbedder) Dims() int { return embedding.Dimensions }

func (embedder *knowledgeUnitLegacyEmbedder) Embed(texts []string) ([][]float32, error) {
	embedder.calls.Add(1)
	return knowledgeUnitVectors(len(texts)), nil
}

type knowledgeUnitContextEmbedder struct {
	legacy       *knowledgeUnitLegacyEmbedder
	contextCalls atomic.Int32
}

func (embedder *knowledgeUnitContextEmbedder) Ready() bool { return embedder.legacy.Ready() }

func (embedder *knowledgeUnitContextEmbedder) Status() embedding.SemanticStatus {
	return embedder.legacy.Status()
}

func (embedder *knowledgeUnitContextEmbedder) ModelID() string { return embedder.legacy.ModelID() }

func (embedder *knowledgeUnitContextEmbedder) Dims() int { return embedder.legacy.Dims() }

func (embedder *knowledgeUnitContextEmbedder) Embed(texts []string) ([][]float32, error) {
	return embedder.legacy.Embed(texts)
}

func (embedder *knowledgeUnitContextEmbedder) EmbedContext(
	ctx context.Context,
	texts []string,
) ([][]float32, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	embedder.contextCalls.Add(1)
	return knowledgeUnitVectors(len(texts)), nil
}

func knowledgeUnitVectors(count int) [][]float32 {
	vectors := make([][]float32, count)
	for index := range vectors {
		vectors[index] = make([]float32, embedding.Dimensions)
		vectors[index][0] = 1
	}
	return vectors
}
