//go:build integration

package importer

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fairy/memory"
	pgstore "fairy/postgres"
	"fairy/secret"
	"fairy/vectorindex"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestVectorAndSecretImportPreservesKNNAndEncryptsPlaintext(t *testing.T) {
	ctx := context.Background()
	path := createIntelligenceFixture(t)
	populateRelationalFixture(t, path, false)
	prepareVectorFixture(t, path)
	secretPath := createSecretFixture(t)
	_, pool, cleanup := importerTestDatabase(t)
	defer cleanup()
	relational, err := ImportRelational(ctx, path, pool)
	if err != nil {
		t.Fatal(err)
	}
	qdrantURL := importerTestQdrantURL()
	resetImporterCollection(t, qdrantURL)
	index, err := vectorindex.Open(ctx, vectorindex.Config{URL: qdrantURL, Timeout: 5 * time.Second, CollectionName: vectorindex.CollectionName})
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	masterKey := base64.StdEncoding.EncodeToString([]byte("00112233445566778899aabbccddeeff"))
	cipher, err := secret.CipherFromEnv(func(name string) string {
		if name == "FAIRY_SECRET_MASTER_KEY" {
			return masterKey
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := ImportVectorsAndSecrets(ctx, relational.RunID, path, secretPath, pool, index, cipher)
	if err != nil {
		t.Fatal(err)
	}
	if result.ImportedPoints != 2 || result.PendingItems != 1 || result.ImportedSecrets != 1 {
		t.Fatalf("vector/secret result = %#v", result)
	}
	hits, err := index.Search(ctx, testVector(1, 0), "bge-small-zh-v1.5", "character-1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) < 2 || hits[0].ItemID != "memory-2" || hits[1].ItemID != "knowledge-2" {
		t.Fatalf("KNN hits = %#v", hits)
	}
	queryCtx, cancel := pool.QueryContext(ctx)
	defer cancel()
	var pendingItem, pendingJob string
	if err := pool.Raw().QueryRow(queryCtx, "SELECT status FROM memory_embedding_items WHERE item_id='memory-1'").Scan(&pendingItem); err != nil {
		t.Fatal(err)
	}
	if err := pool.Raw().QueryRow(queryCtx, "SELECT status FROM memory_embedding_jobs WHERE item_id='memory-1' AND content_hash=$1", contentHash("喜欢安静")).Scan(&pendingJob); err != nil {
		t.Fatal(err)
	}
	if pendingItem != "pending" || pendingJob != "pending" {
		t.Fatalf("missing vector states item=%s job=%s", pendingItem, pendingJob)
	}
	store, err := secret.NewPostgresStore(pool, cipher)
	if err != nil {
		t.Fatal(err)
	}
	loaded, ok, err := store.Load("connection-1")
	if err != nil || !ok || loaded.Expose() != "legacy-fixture-credential" {
		t.Fatalf("secret round trip ok=%v err=%v", ok, err)
	}
	var plaintextRows int64
	if err := pool.Raw().QueryRow(queryCtx, "SELECT count(*) FROM secret_values WHERE encode(ciphertext,'escape') LIKE '%legacy-fixture-credential%' OR aad LIKE '%legacy-fixture-credential%'").Scan(&plaintextRows); err != nil {
		t.Fatal(err)
	}
	reportJSON, _ := json.Marshal(result)
	if plaintextRows != 0 || strings.Contains(string(reportJSON), "legacy-fixture-credential") {
		t.Fatalf("plaintext disclosure rows=%d report=%s", plaintextRows, reportJSON)
	}
}

func TestRunResumesEveryPhaseAndVerifiedRerunIsNoOp(t *testing.T) {
	interrupt := errors.New("injected interruption")
	tests := []struct {
		name    string
		hook    func(*RunHooks)
		wantRun bool
	}{
		{name: "after preflight", hook: func(h *RunHooks) { h.AfterPreflight = func() error { return interrupt } }},
		{name: "after relational", wantRun: true, hook: func(h *RunHooks) { h.AfterRelational = func() error { return interrupt } }},
		{name: "after vector secret", wantRun: true, hook: func(h *RunHooks) { h.AfterVectorSecret = func() error { return interrupt } }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := createIntelligenceFixture(t)
			databaseURL, pool, cleanup := importerTestDatabase(t)
			defer cleanup()
			qdrantURL := importerTestQdrantURL()
			resetImporterCollection(t, qdrantURL)
			options := RunOptions{IntelligencePath: path, Getenv: importerEnvironment(databaseURL, qdrantURL, "")}
			tt.hook(&options.Hooks)
			partial, err := Run(context.Background(), options)
			if !errors.Is(err, interrupt) {
				t.Fatalf("interrupted Run() error = %v", err)
			}
			ctx, cancel := pool.QueryContext(context.Background())
			var runs int64
			if err := pool.Raw().QueryRow(ctx, "SELECT count(*) FROM sqlite_import_runs").Scan(&runs); err != nil {
				cancel()
				t.Fatal(err)
			}
			cancel()
			if (runs == 1) != tt.wantRun || (partial.RunID != "") != tt.wantRun {
				t.Fatalf("partial=%#v runs=%d", partial, runs)
			}
			if !tt.wantRun {
				assertTargetBusinessRows(t, pool, 0)
				return
			}
			options.Hooks = RunHooks{}
			verified, err := Run(context.Background(), options)
			if err != nil || verified.Status != "verified" || verified.NoOp {
				t.Fatalf("resumed Run() = (%#v, %v)", verified, err)
			}
			ctx, cancel = pool.QueryContext(context.Background())
			var updatedAt int64
			if err := pool.Raw().QueryRow(ctx, "SELECT updated_at_ms FROM sqlite_import_runs WHERE id=$1 AND status='verified'", verified.RunID).Scan(&updatedAt); err != nil {
				cancel()
				t.Fatal(err)
			}
			cancel()
			noOp, err := Run(context.Background(), options)
			if err != nil || !noOp.NoOp || noOp.RunID != verified.RunID || noOp.Status != "verified" {
				t.Fatalf("verified rerun = (%#v, %v)", noOp, err)
			}
			ctx, cancel = pool.QueryContext(context.Background())
			var afterUpdatedAt int64
			if err := pool.Raw().QueryRow(ctx, "SELECT updated_at_ms FROM sqlite_import_runs WHERE id=$1", verified.RunID).Scan(&afterUpdatedAt); err != nil {
				cancel()
				t.Fatal(err)
			}
			cancel()
			if afterUpdatedAt != updatedAt {
				t.Fatalf("verified rerun mutated audit timestamp: before=%d after=%d", updatedAt, afterUpdatedAt)
			}
			other := createIntelligenceFixture(t)
			db, err := sql.Open("sqlite", other)
			if err != nil {
				t.Fatal(err)
			}
			_, err = db.Exec("INSERT INTO conversations(id,character_id,created_at_ms,updated_at_ms) VALUES('other','other',1,1)")
			db.Close()
			if err != nil {
				t.Fatal(err)
			}
			_, err = Run(context.Background(), RunOptions{IntelligencePath: other, Getenv: options.Getenv})
			if err == nil || !strings.Contains(err.Error(), "no unique compatible import run") {
				t.Fatalf("different source error = %v", err)
			}
		})
	}
}

func TestRelationalImportPreservesTableAndUsageParity(t *testing.T) {
	path := createIntelligenceFixture(t)
	populateRelationalFixture(t, path, false)
	before, err := fingerprint(path)
	if err != nil {
		t.Fatal(err)
	}
	databaseURL, pool, cleanup := importerTestDatabase(t)
	defer cleanup()
	_ = databaseURL

	result, err := ImportRelational(context.Background(), path, pool)
	if err != nil {
		t.Fatal(err)
	}
	if result.RunID == "" || len(result.Tables) != len(relationalTables) {
		t.Fatalf("relational result = %#v", result)
	}
	for name, parity := range result.Tables {
		if parity.SourceHash == "" || parity.SourceHash != parity.TargetHash {
			t.Fatalf("table %s parity = %#v", name, parity)
		}
	}
	targetStore, err := memory.NewStoreFromPool(pool)
	if err != nil {
		t.Fatal(err)
	}
	targetUsage, err := targetStore.AggregateTokenUsage(100)
	if err != nil {
		t.Fatal(err)
	}
	expectedUsage := memory.UsageReport{
		Overall: []memory.UsageLaneAggregate{{Lane: "respond", InputTokens: 10, OutputTokens: 4, CachedInputTokens: 2, CachedObservedInputTokens: 10, CallCount: 1}},
		Turns: []memory.UsageTurn{
			{ConversationID: "conversation-1", TurnID: "turn-2", CharacterID: "character-1", CreatedAtUnixMS: 60, Status: "failed", Lanes: []memory.UsageLaneAggregate{}},
			{ConversationID: "conversation-1", TurnID: "turn-1", CharacterID: "character-1", CreatedAtUnixMS: 40, Status: "completed", Lanes: []memory.UsageLaneAggregate{{Lane: "respond", InputTokens: 10, OutputTokens: 4, CachedInputTokens: 2, CachedObservedInputTokens: 10, CallCount: 1}}},
		},
		TurnCount: 2,
	}
	expectedJSON, _ := json.Marshal(expectedUsage)
	targetJSON, _ := json.Marshal(targetUsage)
	if string(expectedJSON) != string(targetJSON) {
		t.Fatalf("usage parity mismatch:\nexpected=%s\ntarget=%s", expectedJSON, targetJSON)
	}
	if err := assertFingerprintUnchanged(path, before); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := pool.QueryContext(context.Background())
	defer cancel()
	var runStatus, phase string
	if err := pool.Raw().QueryRow(ctx, "SELECT status, phase FROM sqlite_import_runs WHERE id = $1", result.RunID).Scan(&runStatus, &phase); err != nil {
		t.Fatal(err)
	}
	if runStatus != "running" || phase != "relational_complete" {
		t.Fatalf("run status=%s phase=%s", runStatus, phase)
	}
}

func TestRelationalImportConstraintFailureRollsBackEverything(t *testing.T) {
	path := createIntelligenceFixture(t)
	populateRelationalFixture(t, path, true)
	_, pool, cleanup := importerTestDatabase(t)
	defer cleanup()
	_, err := ImportRelational(context.Background(), path, pool)
	if err == nil || !strings.Contains(err.Error(), "insert target table conversations") {
		t.Fatalf("constraint failure error = %v", err)
	}
	ctx, cancel := pool.QueryContext(context.Background())
	defer cancel()
	for _, table := range []string{"conversations", "sqlite_import_runs", "sqlite_import_checkpoints"} {
		var count int64
		if err := pool.Raw().QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("rollback left %s rows=%d", table, count)
		}
	}
}

func TestPreflightWithRealTargetsIsReadOnlyAndRejectsConflicts(t *testing.T) {
	ctx := context.Background()
	intelligencePath := createIntelligenceFixture(t)
	secretPath := createSecretFixture(t)
	intelligenceBefore, err := fingerprint(intelligencePath)
	if err != nil {
		t.Fatal(err)
	}
	secretBefore, err := fingerprint(secretPath)
	if err != nil {
		t.Fatal(err)
	}
	databaseURL, pool, cleanupDatabase := importerTestDatabase(t)
	defer cleanupDatabase()
	qdrantURL := importerTestQdrantURL()
	resetImporterCollection(t, qdrantURL)
	masterKey := base64.StdEncoding.EncodeToString([]byte("fedcba9876543210fedcba9876543210"))
	getenv := importerEnvironment(databaseURL, qdrantURL, masterKey)

	report, err := Preflight(ctx, PreflightOptions{
		IntelligencePath: intelligencePath, SecretPath: secretPath, Getenv: getenv,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !report.Ready || report.SchemaVersion != 7 || report.SecretRows != 1 || !report.DatabaseSchema.Current || report.QdrantCollection.PointsCount != 0 {
		t.Fatalf("preflight report = %#v", report)
	}
	assertTargetBusinessRows(t, pool, 0)
	if err := assertFingerprintUnchanged(intelligencePath, intelligenceBefore); err != nil {
		t.Fatal(err)
	}
	if err := assertFingerprintUnchanged(secretPath, secretBefore); err != nil {
		t.Fatal(err)
	}

	_, err = Preflight(ctx, PreflightOptions{
		IntelligencePath: intelligencePath, SecretPath: secretPath,
		Getenv: importerEnvironment(databaseURL, qdrantURL, ""),
	})
	if err == nil || !strings.Contains(err.Error(), "FAIRY_SECRET_MASTER_KEY is required") {
		t.Fatalf("missing master key error = %v", err)
	}
	assertTargetBusinessRows(t, pool, 0)

	queryCtx, cancel := pool.QueryContext(ctx)
	_, err = pool.Raw().Exec(queryCtx, "INSERT INTO conversations(id, character_id, created_at_ms, updated_at_ms) VALUES ('conflict', 'character', 1, 1)")
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	_, err = Preflight(ctx, PreflightOptions{
		IntelligencePath: intelligencePath, SecretPath: secretPath, Getenv: getenv,
	})
	if err == nil || !strings.Contains(err.Error(), "no unique compatible import run") {
		t.Fatalf("target conflict error = %v", err)
	}
	assertTargetBusinessRows(t, pool, 1)
	if err := assertFingerprintUnchanged(intelligencePath, intelligenceBefore); err != nil {
		t.Fatal(err)
	}
}

func TestPreflightCorruptSourceDoesNotTouchRealTarget(t *testing.T) {
	databaseURL, pool, cleanupDatabase := importerTestDatabase(t)
	defer cleanupDatabase()
	qdrantURL := importerTestQdrantURL()
	resetImporterCollection(t, qdrantURL)
	path := filepath.Join(t.TempDir(), "corrupt.sqlite3")
	if err := os.WriteFile(path, []byte("not a sqlite database"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := fingerprint(path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Preflight(context.Background(), PreflightOptions{
		IntelligencePath: path,
		Getenv:           importerEnvironment(databaseURL, qdrantURL, base64.StdEncoding.EncodeToString(make([]byte, 32))),
	})
	if err == nil {
		t.Fatal("corrupt source preflight succeeded")
	}
	assertTargetBusinessRows(t, pool, 0)
	if err := assertFingerprintUnchanged(path, before); err != nil {
		t.Fatal(err)
	}
}

func createSecretFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "secrets.sqlite3")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE model_secrets(connection_id TEXT PRIMARY KEY, secret TEXT NOT NULL, updated_at_ms INTEGER NOT NULL);
INSERT INTO model_secrets(connection_id, secret, updated_at_ms) VALUES('connection-1', 'legacy-fixture-credential', 1)`); err != nil {
		t.Fatal(err)
	}
	return path
}

func populateRelationalFixture(t *testing.T, path string, invalidConversation bool) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	updatedAt := int64(100)
	if invalidConversation {
		updatedAt = 1
	}
	statements := []struct {
		query string
		args  []any
	}{
		{"INSERT INTO conversations(id,character_id,created_at_ms,updated_at_ms) VALUES(?,?,?,?)", []any{"conversation-1", "character-1", int64(10), updatedAt}},
		{"INSERT INTO conversation_turns(id,conversation_id,sequence,status,extraction_state,created_at_ms,updated_at_ms) VALUES('turn-1','conversation-1',1,'completed','processed',20,40)", nil},
		{"INSERT INTO conversation_turns(id,conversation_id,sequence,status,error_code,error_message,error_retryable,extraction_state,created_at_ms,updated_at_ms) VALUES('turn-2','conversation-1',2,'failed','provider','redacted failure',1,'ineligible',50,60)", nil},
		{"INSERT INTO conversation_messages(id,conversation_id,turn_id,sequence,role,content,created_at_ms) VALUES('message-1','conversation-1','turn-1',1,'user','你好',21),('message-2','conversation-1','turn-1',2,'assistant','你好呀',39),('message-3','conversation-1','turn-2',3,'user','再见',51)", nil},
		{"INSERT INTO prompt_windows(conversation_id,revision,summary,cutoff_message_sequence,updated_at_ms) VALUES('conversation-1',2,'摘要',2,45)", nil},
		{"INSERT INTO turn_runtime_events(id,conversation_id,turn_id,sequence,event_type,state,code,metadata_json,created_at_ms) VALUES('event-1','conversation-1','turn-1',1,'model','responding',NULL,?,30)", []any{`{"usage":[{"lane":"respond","usage":{"inputTokens":10,"outputTokens":4,"cachedInputTokens":{"status":"observed","tokens":2},"cacheWriteTokens":{"status":"unobserved"}}}]}`}},
		{"INSERT INTO turn_runtime_events(id,conversation_id,turn_id,sequence,event_type,state,code,metadata_json,created_at_ms) VALUES('event-2','conversation-1','turn-1',2,'terminal','completed',NULL,'{}',40),('event-3','conversation-1','turn-2',1,'terminal','failed','provider','{}',60)", nil},
		{"INSERT INTO lane_continuations(conversation_id,lane,previous_response_id,request_shape_hash,input_prefix_hash,response_item_hash,window_revision,updated_at_ms) VALUES('conversation-1','respond','response-1','shape','prefix','items',2,45)", nil},
		{"INSERT INTO context_windows(conversation_id,lane,window_number,first_window_id,previous_window_id,window_id,observed_prefill_tokens,estimated_prefill_tokens,last_trigger,failure_count,prompt_window_revision,updated_at_ms) VALUES('conversation-1','respond',1,'window-1',NULL,'window-1',12,NULL,'created',0,2,45)", nil},
		{"INSERT INTO personal_memories(id,kind,scope_kind,character_id,review_status,content,status,confidence_basis_points,source_conversation_id,source_turn_id,supersedes_id,created_at_ms,updated_at_ms) VALUES('memory-1','preference','global',NULL,'ready','喜欢安静','superseded',8000,'conversation-1','turn-1',NULL,30,35),('memory-2','preference','global',NULL,'ready','喜欢咖啡','active',9000,'conversation-1','turn-1','memory-1',36,40)", nil},
		{"INSERT INTO knowledge_entries(id,topic,statement,status,verification_basis,confidence_basis_points,source_conversation_id,source_turn_id,supersedes_id,created_at_ms,updated_at_ms) VALUES('knowledge-1','旧主题','旧事实','superseded','retrieval_ingest',7000,'conversation-1','turn-1',NULL,30,35),('knowledge-2','主题','事实','verified','user_confirmed',9000,'conversation-1','turn-1','knowledge-1',36,40)", nil},
		{"INSERT INTO knowledge_sources(knowledge_id,source_id,title,url,snippet,rank,fetched_at_ms) VALUES('knowledge-2','source-1','标题','https://example.test','片段',1,38)", nil},
		{"INSERT INTO extraction_batches(id,conversation_id,character_id,status,first_turn_sequence,last_turn_sequence,error_code,error_message,error_retryable,created_at_ms,updated_at_ms) VALUES('batch-1','conversation-1','character-1','failed',1,1,'parse','redacted',0,41,42)", nil},
		{"INSERT INTO extraction_batch_turns(batch_id,turn_id,turn_sequence) VALUES('batch-1','turn-1',1)", nil},
		{"INSERT INTO knowledge_ingest_jobs(id,conversation_id,turn_id,query,title,url,snippet,rank,fetched_at_ms,status,error_message,created_at_ms,updated_at_ms) VALUES('ingest-1','conversation-1','turn-1','查询','标题','https://example.test','片段',1,38,'dropped','structural',41,42)", nil},
		{"INSERT INTO memory_embedding_items(vector_rowid,item_kind,item_id,model_id,dimensions,content_hash,status,error_code,error_message,created_at_ms,updated_at_ms) VALUES(1,'personal_memory','memory-2','bge-small-zh-v1.5',512,?,'pending',NULL,NULL,41,42)", []any{strings.Repeat("a", 64)}},
		{"INSERT INTO memory_embedding_jobs(id,item_kind,item_id,model_id,dimensions,content_hash,status,error_code,error_message,retryable,created_at_ms,updated_at_ms) VALUES('embedding-job-1','personal_memory','memory-2','bge-small-zh-v1.5',512,?,'failed','provider','redacted',1,41,42)", []any{strings.Repeat("a", 64)}},
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement.query, statement.args...); err != nil {
			t.Fatalf("populate fixture: %v\nquery=%s", err, statement.query)
		}
	}
}

func prepareVectorFixture(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	statements := []struct {
		query string
		args  []any
	}{
		{"UPDATE memory_embedding_items SET content_hash=?,status='embedded' WHERE vector_rowid=1", []any{contentHash("喜欢咖啡")}},
		{"UPDATE memory_embedding_jobs SET content_hash=? WHERE id='embedding-job-1'", []any{contentHash("喜欢咖啡")}},
		{"INSERT INTO memory_embedding_items(vector_rowid,item_kind,item_id,model_id,dimensions,content_hash,status,created_at_ms,updated_at_ms) VALUES(2,'knowledge','knowledge-2','bge-small-zh-v1.5',512,?,'embedded',43,44)", []any{contentHash("主题\n事实")}},
		{"INSERT INTO memory_embedding_items(vector_rowid,item_kind,item_id,model_id,dimensions,content_hash,status,created_at_ms,updated_at_ms) VALUES(3,'personal_memory','memory-1','bge-small-zh-v1.5',512,?,'pending',43,44)", []any{contentHash("喜欢安静")}},
		{"INSERT INTO memory_embedding_vec(rowid,embedding) VALUES(1,?),(2,?)", []any{vectorLiteral(testVector(1, 0)), vectorLiteral(testVector(0, 1))}},
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement.query, statement.args...); err != nil {
			t.Fatalf("prepare vector fixture: %v", err)
		}
	}
}

func testVector(first, second float32) []float32 {
	vector := make([]float32, vectorindex.Dimensions)
	vector[0] = first
	vector[1] = second
	return vector
}

func vectorLiteral(vector []float32) string {
	parts := make([]string, len(vector))
	for index, value := range vector {
		parts[index] = fmt.Sprintf("%g", value)
	}
	return "[" + strings.Join(parts, ",") + "]"
}

func importerTestDatabase(t *testing.T) (string, *pgstore.Pool, func()) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rawURL := os.Getenv("FAIRY_TEST_DATABASE_URL")
	if rawURL == "" {
		rawURL = "postgres://fairy:fairy_test_password@127.0.0.1:15432/fairy_test?sslmode=disable"
	}
	admin, err := pgxpool.New(ctx, rawURL)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("fairy_importer_test_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	admin.Close()
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	values := parsed.Query()
	values.Set("search_path", schema+",public")
	parsed.RawQuery = values.Encode()
	databaseURL := parsed.String()
	pool, err := pgstore.Open(ctx, pgstore.ShortTimeoutConfig(databaseURL))
	if err != nil {
		t.Fatal(err)
	}
	if err := pgstore.Migrate(ctx, pool); err != nil {
		pool.Close()
		t.Fatal(err)
	}
	cleanup := func() {
		pool.Close()
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		cleanupPool, err := pgxpool.New(cleanupCtx, rawURL)
		if err != nil {
			t.Logf("open cleanup pool: %v", err)
			return
		}
		defer cleanupPool.Close()
		_, _ = cleanupPool.Exec(cleanupCtx, "DROP SCHEMA IF EXISTS "+quoted+" CASCADE")
	}
	return databaseURL, pool, cleanup
}

func resetImporterCollection(t *testing.T, rawURL string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err := vectorindex.Open(ctx, vectorindex.Config{URL: rawURL, Timeout: 5 * time.Second, CollectionName: vectorindex.CollectionName})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	_ = client.DeleteCollection(ctx)
	if err := client.MigrateCollection(ctx); err != nil {
		t.Fatal(err)
	}
}

func importerEnvironment(databaseURL, qdrantURL, masterKey string) func(string) string {
	values := map[string]string{
		pgstore.EnvDatabaseURL: databaseURL, pgstore.EnvMaxConns: "4", pgstore.EnvMinConns: "0",
		pgstore.EnvConnectTimeout: "2s", pgstore.EnvQueryTimeout: "2s",
		vectorindex.EnvURL: qdrantURL, vectorindex.EnvTimeout: "2s",
		"FAIRY_SECRET_MASTER_KEY": masterKey,
	}
	return func(name string) string { return values[name] }
}

func importerTestQdrantURL() string {
	if value := os.Getenv("FAIRY_TEST_QDRANT_GRPC_URL"); value != "" {
		return value
	}
	return "http://127.0.0.1:16334"
}

func assertTargetBusinessRows(t *testing.T, pool *pgstore.Pool, want int64) {
	t.Helper()
	ctx, cancel := pool.QueryContext(context.Background())
	defer cancel()
	var count int64
	if err := pool.Raw().QueryRow(ctx, "SELECT count(*) FROM conversations").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != want {
		t.Fatalf("target conversation rows = %d, want %d", count, want)
	}
}
