//go:build integration

package memory

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"fairy/memory/semantic"
	pgstore "fairy/postgres"
	"fairy/vectorindex"

	"github.com/google/uuid"
)

func TestVectorRebuildAndReconciliationAgainstRealServices(t *testing.T) {
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
	first := seedPostgresEmbeddingMemory(t, ctx, store, "rebuild-first", "第一条重建记忆")
	second := seedPostgresEmbeddingMemory(t, ctx, store, "rebuild-second", "第二条重建记忆")
	third := seedPostgresEmbeddingMemory(t, ctx, store, "rebuild-third", "第三条重建记忆")

	if _, err := store.RebuildVectorIndex(ctx, semantic.UnavailableEmbedder{}, index, 2); err == nil {
		t.Fatal("RebuildVectorIndex(unavailable embedder) error = nil")
	}
	status, err := index.VerifyCollection(ctx)
	if err != nil || status.PointsCount != 0 {
		t.Fatalf("point count after unavailable rebuild = (%#v, %v)", status, err)
	}
	var runs int
	if err := pool.Raw().QueryRow(ctx, "SELECT count(*) FROM vector_rebuild_runs").Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if runs != 0 {
		t.Fatalf("rebuild runs after preflight failure = %d", runs)
	}

	vector := make([]float32, SemanticEmbeddingDimensions)
	vector[0] = 1
	rebuild, err := store.RebuildVectorIndex(ctx, postgresWorkerEmbedder{vector: vector}, index, 2)
	if err != nil {
		t.Fatal(err)
	}
	if rebuild.ScannedItems != 3 || rebuild.UpsertedPoints != 3 || rebuild.RunID == "" {
		t.Fatalf("rebuild = %#v", rebuild)
	}
	status, err = index.VerifyCollection(ctx)
	if err != nil || status.PointsCount != 3 {
		t.Fatalf("point count after rebuild = (%#v, %v)", status, err)
	}
	var runStatus string
	var scanned, upserted int
	if err := pool.Raw().QueryRow(ctx, "SELECT status, scanned_items, upserted_points FROM vector_rebuild_runs WHERE id = $1", rebuild.RunID).Scan(&runStatus, &scanned, &upserted); err != nil {
		t.Fatal(err)
	}
	if runStatus != "succeeded" || scanned != 3 || upserted != 3 {
		t.Fatalf("run=%q scanned=%d upserted=%d", runStatus, scanned, upserted)
	}
	var succeededJobs int
	if err := pool.Raw().QueryRow(ctx, "SELECT count(*) FROM memory_embedding_jobs WHERE status = 'succeeded'").Scan(&succeededJobs); err != nil {
		t.Fatal(err)
	}
	if succeededJobs != 3 {
		t.Fatalf("succeeded rebuilt jobs = %d", succeededJobs)
	}

	firstID, _ := vectorindex.PointID(vectorindex.ItemKindPersonalMemory, first.ID, SemanticEmbeddingModelID)
	secondID, _ := vectorindex.PointID(vectorindex.ItemKindPersonalMemory, second.ID, SemanticEmbeddingModelID)
	thirdID, _ := vectorindex.PointID(vectorindex.ItemKindPersonalMemory, third.ID, SemanticEmbeddingModelID)
	if err := index.Upsert(ctx, vectorindex.Point{ID: firstID, Vector: vector, Payload: vectorindex.PointPayloadInput{
		ItemKind: vectorindex.ItemKindPersonalMemory, ItemID: first.ID, ModelID: SemanticEmbeddingModelID,
		ScopeType: "global", ContentHash: strings.Repeat("f", 64),
	}}); err != nil {
		t.Fatal(err)
	}
	if err := index.DeletePoints(ctx, []uuid.UUID{secondID}); err != nil {
		t.Fatal(err)
	}
	orphanID, err := vectorindex.PointID(vectorindex.ItemKindPersonalMemory, "orphan-memory", SemanticEmbeddingModelID)
	if err != nil {
		t.Fatal(err)
	}
	if err := index.Upsert(ctx, vectorindex.Point{ID: orphanID, Vector: vector, Payload: vectorindex.PointPayloadInput{
		ItemKind: vectorindex.ItemKindPersonalMemory, ItemID: "orphan-memory", ModelID: SemanticEmbeddingModelID,
		ScopeType: "global", ContentHash: strings.Repeat("e", 64),
	}}); err != nil {
		t.Fatal(err)
	}
	before, err := index.ListPoints(ctx)
	if err != nil {
		t.Fatal(err)
	}
	dryRun, err := store.ReconcileVectorIndex(ctx, index, false)
	if err != nil {
		t.Fatal(err)
	}
	if !dryRun.DryRun || len(dryRun.MissingPoints) != 1 || len(dryRun.StalePoints) != 1 || len(dryRun.OrphanPoints) != 1 {
		t.Fatalf("dry run = %#v", dryRun)
	}
	afterDryRun, err := index.ListPoints(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, afterDryRun) {
		t.Fatalf("dry run mutated points: before=%#v after=%#v", before, afterDryRun)
	}
	apply, err := store.ReconcileVectorIndex(ctx, index, true)
	if err != nil {
		t.Fatal(err)
	}
	if apply.DryRun || !reflect.DeepEqual(dryRun.MissingPoints, apply.MissingPoints) || !reflect.DeepEqual(dryRun.StalePoints, apply.StalePoints) || !reflect.DeepEqual(dryRun.OrphanPoints, apply.OrphanPoints) {
		t.Fatalf("apply = %#v, dry run = %#v", apply, dryRun)
	}
	foundOrphan, err := index.HasPoint(ctx, orphanID)
	if err != nil || foundOrphan {
		t.Fatalf("orphan after apply = (%v, %v)", foundOrphan, err)
	}
	foundValid, err := index.HasPoint(ctx, thirdID)
	if err != nil || !foundValid {
		t.Fatalf("valid point after apply = (%v, %v)", foundValid, err)
	}
}
