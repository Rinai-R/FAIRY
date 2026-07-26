//go:build integration

package vectorindex

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/qdrant/go-client/qdrant"
)

func TestQdrantCollectionMigrateVerifyAndRejectIncompatible(t *testing.T) {
	ctx := context.Background()
	client := openTestQdrantClient(t, ctx, fmt.Sprintf("fairy_memory_test_%d", time.Now().UnixNano()))
	defer client.Close()
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = client.client.DeleteCollection(cleanupCtx, client.config.collectionName())
	})
	if _, err := client.VerifyCollection(ctx); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("VerifyCollection(missing) error = %v", err)
	}
	if err := client.MigrateCollection(ctx); err != nil {
		t.Fatal(err)
	}
	status, err := client.VerifyCollection(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.CollectionName != client.config.collectionName() || status.Dimensions != Dimensions || status.Distance != Distance {
		t.Fatalf("status = %#v", status)
	}
}

func TestQdrantVerifyRejectsWrongDimension(t *testing.T) {
	ctx := context.Background()
	client := openTestQdrantClient(t, ctx, fmt.Sprintf("fairy_memory_wrong_%d", time.Now().UnixNano()))
	defer client.Close()
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = client.client.DeleteCollection(cleanupCtx, client.config.collectionName())
	})
	if err := client.client.CreateCollection(ctx, &qdrant.CreateCollection{
		CollectionName: client.config.collectionName(),
		VectorsConfig:  qdrant.NewVectorsConfig(&qdrant.VectorParams{Size: 4, Distance: qdrant.Distance_Cosine}),
	}); err != nil {
		t.Fatal(err)
	}
	_, err := client.VerifyCollection(ctx)
	if err == nil || !strings.Contains(err.Error(), "contract mismatch") {
		t.Fatalf("VerifyCollection(wrong dimension) error = %v", err)
	}
}

func TestQdrantSearchFiltersRelationshipScope(t *testing.T) {
	ctx := context.Background()
	client := openTestQdrantClient(t, ctx, fmt.Sprintf("fairy_memory_scope_%d", time.Now().UnixNano()))
	defer client.Close()
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = client.DeleteCollection(cleanupCtx)
	})
	if err := client.MigrateCollection(ctx); err != nil {
		t.Fatal(err)
	}
	vector := make([]float32, Dimensions)
	vector[0] = 1
	inputs := []PointPayloadInput{
		{ItemKind: ItemKindPersonalMemory, ItemID: "global-1", ModelID: "model-1", ScopeType: "global", ContentHash: strings.Repeat("a", 64)},
		{ItemKind: ItemKindPersonalMemory, ItemID: "character-a", ModelID: "model-1", ScopeType: "character", CharacterID: "character-a", ContentHash: strings.Repeat("b", 64)},
		{ItemKind: ItemKindPersonalMemory, ItemID: "character-b", ModelID: "model-1", ScopeType: "character", CharacterID: "character-b", ContentHash: strings.Repeat("c", 64)},
	}
	for _, input := range inputs {
		id, err := PointID(input.ItemKind, input.ItemID, input.ModelID)
		if err != nil {
			t.Fatal(err)
		}
		if err := client.Upsert(ctx, Point{ID: id, Vector: vector, Payload: input}); err != nil {
			t.Fatal(err)
		}
	}
	hits, err := client.Search(ctx, vector, "model-1", "character-a", 10)
	if err != nil {
		t.Fatal(err)
	}
	seen := make(map[string]bool)
	for _, hit := range hits {
		seen[hit.ItemID] = true
	}
	if !seen["global-1"] || !seen["character-a"] || seen["character-b"] {
		t.Fatalf("scope-filtered hits = %#v", hits)
	}
	stored, err := client.ListPoints(ctx)
	if err != nil || len(stored) != 3 {
		t.Fatalf("ListPoints() = (%#v, %v)", stored, err)
	}
	deleteID, err := PointID(ItemKindPersonalMemory, "character-b", "model-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := client.DeletePoints(ctx, []uuid.UUID{deleteID}); err != nil {
		t.Fatal(err)
	}
	stored, err = client.ListPoints(ctx)
	if err != nil || len(stored) != 2 {
		t.Fatalf("ListPoints() after delete = (%#v, %v)", stored, err)
	}
	for _, point := range stored {
		if point.ItemID == "character-b" {
			t.Fatalf("deleted point remained: %#v", stored)
		}
	}
}

func TestQdrantMetricsConcurrentUpsertAndSnapshot(t *testing.T) {
	ctx := context.Background()
	client := openTestQdrantClient(t, ctx, fmt.Sprintf("fairy_memory_metrics_%d", time.Now().UnixNano()))
	defer client.Close()
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = client.DeleteCollection(cleanupCtx)
	})
	if err := client.MigrateCollection(ctx); err != nil {
		t.Fatal(err)
	}
	vector := make([]float32, Dimensions)
	vector[0] = 1
	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		worker := worker
		wg.Add(1)
		go func() {
			defer wg.Done()
			itemID := fmt.Sprintf("metrics-%d", worker)
			pointID, err := PointID(ItemKindPersonalMemory, itemID, "model-1")
			if err != nil {
				t.Error(err)
				return
			}
			if err := client.Upsert(ctx, Point{ID: pointID, Vector: vector, Payload: PointPayloadInput{
				ItemKind: ItemKindPersonalMemory, ItemID: itemID, ModelID: "model-1",
				ScopeType: "global", ContentHash: strings.Repeat("a", 64),
			}}); err != nil {
				t.Error(err)
			}
			_, _ = client.Metrics(ctx)
		}()
	}
	wg.Wait()
	metrics, err := client.Metrics(ctx)
	if err != nil {
		t.Fatal(err)
	}
	status, err := client.VerifyCollection(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if metrics.PointCount != 8 || metrics.PointCount != status.PointsCount || metrics.Requests == 0 || metrics.Errors != 0 {
		t.Fatalf("metrics=%#v status=%#v", metrics, status)
	}
	allowed := map[string]bool{"health": true, "migrate_collection": true, "upsert": true, "verify_collection": true}
	for _, operation := range metrics.Operations {
		if !allowed[operation.Operation] {
			t.Fatalf("high-cardinality/unknown operation metric: %#v", operation)
		}
		if strings.Contains(operation.Operation, "metrics-") {
			t.Fatalf("operation metric contains item id: %#v", operation)
		}
	}
}

func TestQdrantMetricsFailInsteadOfFabricatingMissingCollectionCount(t *testing.T) {
	ctx := context.Background()
	client := openTestQdrantClient(t, ctx, fmt.Sprintf("fairy_memory_missing_metrics_%d", time.Now().UnixNano()))
	defer client.Close()
	snapshot, err := client.Metrics(ctx)
	if err == nil {
		t.Fatalf("Metrics(missing collection) = %#v, nil error", snapshot)
	}
}

func TestQdrantUnreachableEndpointHonorsDeadlineAndRedactsSecrets(t *testing.T) {
	ctx := context.Background()
	started := time.Now()
	_, err := Open(ctx, Config{
		URL: "http://user:password@127.0.0.1:1", APIKey: "qdrant-api-secret",
		Timeout: 250 * time.Millisecond, CollectionName: "unreachable",
	})
	if err == nil {
		t.Fatal("Open(unreachable) error = nil")
	}
	if time.Since(started) > 2*time.Second {
		t.Fatalf("unreachable Open exceeded bounded deadline: %v", time.Since(started))
	}
	for _, forbidden := range []string{"password", "qdrant-api-secret"} {
		if strings.Contains(err.Error(), forbidden) {
			t.Fatalf("unreachable error leaked %q: %v", forbidden, err)
		}
	}
}

func openTestQdrantClient(t *testing.T, ctx context.Context, collection string) *Client {
	t.Helper()
	url := os.Getenv("FAIRY_TEST_QDRANT_GRPC_URL")
	if url == "" {
		url = "http://127.0.0.1:16334"
	}
	client, err := Open(ctx, Config{URL: url, Timeout: 5 * time.Second, CollectionName: collection})
	if err != nil {
		t.Fatal(err)
	}
	return client
}
