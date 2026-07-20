//go:build integration

package memory

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"fairy/memory/semantic"
	pgstore "fairy/postgres"
	"fairy/vectorindex"

	"github.com/google/uuid"
)

type postgresWorkerEmbedder struct {
	vector []float32
}

func (e postgresWorkerEmbedder) Ready() bool             { return true }
func (e postgresWorkerEmbedder) Status() semantic.Status { return semantic.StatusReady }
func (e postgresWorkerEmbedder) Dims() int               { return SemanticEmbeddingDimensions }
func (e postgresWorkerEmbedder) Embed(texts []string) ([][]float32, error) {
	vectors := make([][]float32, len(texts))
	for index := range texts {
		vectors[index] = append([]float32(nil), e.vector...)
	}
	return vectors, nil
}

type unavailableWorkerIndex struct {
	upsertCalled bool
}

func (i *unavailableWorkerIndex) Ready(context.Context) error {
	return errors.New("qdrant unavailable")
}

func (i *unavailableWorkerIndex) Upsert(context.Context, vectorindex.Point) error {
	i.upsertCalled = true
	return nil
}

type recordingWorkerIndex struct {
	upsertCalled bool
}

func (i *recordingWorkerIndex) Ready(context.Context) error { return nil }
func (i *recordingWorkerIndex) Upsert(context.Context, vectorindex.Point) error {
	i.upsertCalled = true
	return nil
}

func TestPostgresEmbeddingWorkerUpsertsQdrantAndCompletesOutbox(t *testing.T) {
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
	record := seedPostgresEmbeddingMemory(t, ctx, store, "worker-success", "喜欢清晨的安静")
	vector := make([]float32, SemanticEmbeddingDimensions)
	vector[3] = 1
	result, err := store.ProcessEmbeddingJobsWithVectorIndex(ctx, postgresWorkerEmbedder{vector: vector}, index, 10)
	if err != nil {
		t.Fatal(err)
	}
	if result.Processed != 1 || result.Succeeded != 1 || result.Failed != 0 || result.SemanticStatus != string(semantic.StatusReady) {
		t.Fatalf("result = %#v", result)
	}
	pointID, err := vectorindex.PointID(vectorindex.ItemKindPersonalMemory, record.ID, SemanticEmbeddingModelID)
	if err != nil {
		t.Fatal(err)
	}
	found, err := index.HasPoint(ctx, pointID)
	if err != nil || !found {
		t.Fatalf("HasPoint() = (%v, %v)", found, err)
	}
	status, err := index.VerifyCollection(ctx)
	if err != nil || status.PointsCount != 1 {
		t.Fatalf("VerifyCollection() = (%#v, %v)", status, err)
	}
	assertPostgresEmbeddingCompleted(t, ctx, pool, record.ID, 1)
}

func TestPostgresEmbeddingWorkerReclaimsAfterQdrantUpsertCrash(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := pgstore.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	store, err := newStoreFromPoolWithLease(pool, "worker-crash", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	index := openIsolatedWorkerVectorIndex(t, ctx)
	record := seedPostgresEmbeddingMemory(t, ctx, store, "worker-crash", "需要幂等写入的记忆")
	jobs, err := store.claimEmbeddingJobsPostgres(ctx, 1, nowUnixMS())
	if err != nil || len(jobs) != 1 {
		t.Fatalf("claim = %#v, %v", jobs, err)
	}
	payload, err := store.embeddingJobPayloadPostgres(ctx, jobs[0])
	if err != nil {
		t.Fatal(err)
	}
	pointID, err := uuid.Parse(jobs[0].PointID)
	if err != nil {
		t.Fatal(err)
	}
	vector := make([]float32, SemanticEmbeddingDimensions)
	vector[4] = 1
	if err := index.Upsert(ctx, vectorindex.Point{ID: pointID, Vector: vector, Payload: vectorindex.PointPayloadInput{
		ItemKind: jobs[0].ItemKind, ItemID: jobs[0].ItemID, ModelID: jobs[0].ModelID,
		ScopeType: payload.ScopeType, CharacterID: payload.CharacterID, ContentHash: jobs[0].ContentHash,
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Raw().Exec(ctx, "UPDATE memory_embedding_jobs SET lease_expires_at_ms = $2 WHERE id = $1", jobs[0].ID, nowUnixMS()-1); err != nil {
		t.Fatal(err)
	}
	result, err := store.ProcessEmbeddingJobsWithVectorIndex(ctx, postgresWorkerEmbedder{vector: vector}, index, 1)
	if err != nil {
		t.Fatal(err)
	}
	if result.Processed != 1 || result.Succeeded != 1 {
		t.Fatalf("result = %#v", result)
	}
	status, err := index.VerifyCollection(ctx)
	if err != nil || status.PointsCount != 1 {
		t.Fatalf("idempotent point count = (%#v, %v)", status, err)
	}
	assertPostgresEmbeddingCompleted(t, ctx, pool, record.ID, 2)
}

func TestPostgresEmbeddingWorkerUnavailableIndexDoesNotClaim(t *testing.T) {
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
	record := seedPostgresEmbeddingMemory(t, ctx, store, "worker-unavailable", "等待向量库恢复")
	index := &unavailableWorkerIndex{}
	result, err := store.ProcessEmbeddingJobsWithVectorIndex(ctx, postgresWorkerEmbedder{vector: make([]float32, SemanticEmbeddingDimensions)}, index, 1)
	if err == nil || !strings.Contains(err.Error(), "vector index is unavailable") {
		t.Fatalf("error = %v", err)
	}
	if result.Processed != 0 || index.upsertCalled {
		t.Fatalf("result=%#v upsertCalled=%v", result, index.upsertCalled)
	}
	var status string
	var ownerMissing bool
	var attempts int
	if err := pool.Raw().QueryRow(ctx, "SELECT status, lease_owner IS NULL, attempt_count FROM memory_embedding_jobs WHERE item_id = $1", record.ID).Scan(&status, &ownerMissing, &attempts); err != nil {
		t.Fatal(err)
	}
	if status != "pending" || !ownerMissing || attempts != 0 {
		t.Fatalf("status=%q ownerMissing=%v attempts=%d", status, ownerMissing, attempts)
	}
}

func TestPostgresEmbeddingWorkerRejectsNonFiniteVectorBeforeUpsert(t *testing.T) {
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
	record := seedPostgresEmbeddingMemory(t, ctx, store, "worker-invalid", "无效向量测试")
	vector := make([]float32, SemanticEmbeddingDimensions)
	vector[9] = float32(math.NaN())
	index := &recordingWorkerIndex{}
	result, err := store.ProcessEmbeddingJobsWithVectorIndex(ctx, postgresWorkerEmbedder{vector: vector}, index, 1)
	if err != nil {
		t.Fatal(err)
	}
	if result.Processed != 1 || result.Failed != 1 || index.upsertCalled {
		t.Fatalf("result=%#v upsertCalled=%v", result, index.upsertCalled)
	}
	var status, code string
	var retryable bool
	if err := pool.Raw().QueryRow(ctx, "SELECT status, error_code, retryable FROM memory_embedding_jobs WHERE item_id = $1", record.ID).Scan(&status, &code, &retryable); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || code != embeddingJobErrorInvalidVector || retryable {
		t.Fatalf("status=%q code=%q retryable=%v", status, code, retryable)
	}
}

func openIsolatedWorkerVectorIndex(t *testing.T, ctx context.Context) *vectorindex.Client {
	t.Helper()
	qdrantURL := os.Getenv("FAIRY_TEST_QDRANT_GRPC_URL")
	if qdrantURL == "" {
		qdrantURL = "http://127.0.0.1:16334"
	}
	client, err := vectorindex.Open(ctx, vectorindex.Config{
		URL: qdrantURL, Timeout: 5 * time.Second,
		CollectionName: fmt.Sprintf("fairy_worker_test_%d", time.Now().UnixNano()),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.MigrateCollection(ctx); err != nil {
		client.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = client.DeleteCollection(cleanupCtx)
		_ = client.Close()
	})
	return client
}

func seedPostgresEmbeddingMemory(t *testing.T, ctx context.Context, store *Store, suffix, content string) PersonalMemoryRecord {
	t.Helper()
	bootstrap, err := store.OpenOrCreateCharacterConversationContext(ctx, "character-"+suffix)
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
	record, err := store.CreatePersonalMemoryContext(ctx, "preference", MemoryScope{Type: "global"}, content, 9000)
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func assertPostgresEmbeddingCompleted(t *testing.T, ctx context.Context, pool *pgstore.Pool, itemID string, wantAttempts int) {
	t.Helper()
	var itemStatus, jobStatus string
	var embeddedAt *int64
	var attempts int
	if err := pool.Raw().QueryRow(ctx, "SELECT status, embedded_at_ms FROM memory_embedding_items WHERE item_id = $1", itemID).Scan(&itemStatus, &embeddedAt); err != nil {
		t.Fatal(err)
	}
	if err := pool.Raw().QueryRow(ctx, "SELECT status, attempt_count FROM memory_embedding_jobs WHERE item_id = $1", itemID).Scan(&jobStatus, &attempts); err != nil {
		t.Fatal(err)
	}
	if itemStatus != "embedded" || jobStatus != "succeeded" || embeddedAt == nil || attempts != wantAttempts {
		t.Fatalf("item=%q job=%q embeddedAt=%v attempts=%d wantAttempts=%d", itemStatus, jobStatus, embeddedAt, attempts, wantAttempts)
	}
}
