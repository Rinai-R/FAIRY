package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"fairy/memory/semantic"
	"fairy/vectorindex"

	"github.com/google/uuid"
)

const maxVectorMaintenancePageSize = 100

type VectorMaintenanceIndex interface {
	Ready(context.Context) error
	Upsert(context.Context, vectorindex.Point) error
	ListPoints(context.Context) ([]vectorindex.StoredPoint, error)
	DeletePoints(context.Context, []uuid.UUID) error
	Collection() string
}

type VectorRebuildResult struct {
	RunID          string `json:"runId"`
	ScannedItems   int    `json:"scannedItems"`
	UpsertedPoints int    `json:"upsertedPoints"`
}

type VectorReconciliationResult struct {
	RunID         string   `json:"runId"`
	DryRun        bool     `json:"dryRun"`
	MissingPoints []string `json:"missingPoints"`
	StalePoints   []string `json:"stalePoints"`
	OrphanPoints  []string `json:"orphanPoints"`
}

type authoritativeVectorItem struct {
	ItemKind    string
	ItemID      string
	Content     string
	ScopeType   string
	CharacterID string
}

func (s *Store) RebuildVectorIndex(ctx context.Context, embedder semantic.Embedder, index VectorMaintenanceIndex, pageSize int) (VectorRebuildResult, error) {
	if s == nil || s.pool == nil {
		return VectorRebuildResult{}, errors.New("vector rebuild requires PostgreSQL store")
	}
	if embedder == nil || !embedder.Ready() {
		return VectorRebuildResult{}, errors.New("vector rebuild requires ready embedder")
	}
	if embedder.Dims() != SemanticEmbeddingDimensions {
		return VectorRebuildResult{}, fmt.Errorf("embedding dimensions = %d, want %d", embedder.Dims(), SemanticEmbeddingDimensions)
	}
	if index == nil || index.Collection() == "" {
		return VectorRebuildResult{}, errors.New("vector rebuild requires vector index")
	}
	if pageSize < 1 || pageSize > maxVectorMaintenancePageSize {
		return VectorRebuildResult{}, fmt.Errorf("vector rebuild page size must be between 1 and %d", maxVectorMaintenancePageSize)
	}
	if err := index.Ready(ctx); err != nil {
		return VectorRebuildResult{}, fmt.Errorf("vector index is unavailable: %w", err)
	}
	runID := newID()
	now := nowUnixMS()
	queryCtx, cancel := s.pool.QueryContext(ctx)
	_, err := s.pool.Raw().Exec(queryCtx, "INSERT INTO vector_rebuild_runs(id, collection_name, model_id, status, created_at_ms, updated_at_ms) VALUES ($1, $2, $3, 'running', $4, $4)", runID, index.Collection(), SemanticEmbeddingModelID, now)
	cancel()
	if err != nil {
		return VectorRebuildResult{}, fmt.Errorf("creating vector rebuild run: %w", err)
	}
	result := VectorRebuildResult{RunID: runID}
	lastKind, lastID := "", ""
	for {
		items, err := s.authoritativeVectorPage(ctx, lastKind, lastID, pageSize)
		if err != nil {
			s.failVectorRebuildRun(ctx, runID, err)
			return result, err
		}
		if len(items) == 0 {
			break
		}
		contents := make([]string, len(items))
		for i := range items {
			contents[i] = items[i].Content
		}
		vectors, err := embedder.Embed(contents)
		if err != nil {
			s.failVectorRebuildRun(ctx, runID, err)
			return result, fmt.Errorf("embedding vector rebuild page: %w", err)
		}
		if len(vectors) != len(items) {
			err := fmt.Errorf("embedder returned %d vectors for %d rebuild items", len(vectors), len(items))
			s.failVectorRebuildRun(ctx, runID, err)
			return result, err
		}
		for i, item := range items {
			if err := vectorindex.ValidateVector(vectors[i]); err != nil {
				s.failVectorRebuildRun(ctx, runID, err)
				return result, err
			}
			pointID, err := vectorindex.PointID(item.ItemKind, item.ItemID, SemanticEmbeddingModelID)
			if err != nil {
				s.failVectorRebuildRun(ctx, runID, err)
				return result, err
			}
			hash := semanticContentHash(item.Content)
			if err := index.Upsert(ctx, vectorindex.Point{ID: pointID, Vector: vectors[i], Payload: vectorindex.PointPayloadInput{ItemKind: item.ItemKind, ItemID: item.ItemID, ModelID: SemanticEmbeddingModelID, ScopeType: item.ScopeType, CharacterID: item.CharacterID, ContentHash: hash}}); err != nil {
				s.failVectorRebuildRun(ctx, runID, err)
				return result, err
			}
			if err := s.markRebuiltVectorItem(ctx, item, pointID, hash); err != nil {
				s.failVectorRebuildRun(ctx, runID, err)
				return result, err
			}
			result.UpsertedPoints++
		}
		result.ScannedItems += len(items)
		lastKind, lastID = items[len(items)-1].ItemKind, items[len(items)-1].ItemID
		checkpoint, _ := json.Marshal(map[string]string{"lastItemKind": lastKind, "lastItemID": lastID})
		queryCtx, cancel = s.pool.QueryContext(ctx)
		_, err = s.pool.Raw().Exec(queryCtx, "UPDATE vector_rebuild_runs SET scanned_items = $2, upserted_points = $3, checkpoint_json = $4, updated_at_ms = $5 WHERE id = $1 AND status = 'running'", runID, result.ScannedItems, result.UpsertedPoints, checkpoint, nowUnixMS())
		cancel()
		if err != nil {
			return result, fmt.Errorf("updating vector rebuild checkpoint: %w", err)
		}
	}
	queryCtx, cancel = s.pool.QueryContext(ctx)
	_, err = s.pool.Raw().Exec(queryCtx, "UPDATE vector_rebuild_runs SET status = 'succeeded', updated_at_ms = $2 WHERE id = $1 AND status = 'running'", runID, nowUnixMS())
	cancel()
	if err != nil {
		return result, fmt.Errorf("completing vector rebuild run: %w", err)
	}
	return result, nil
}

func (s *Store) authoritativeVectorPage(ctx context.Context, lastKind, lastID string, limit int) ([]authoritativeVectorItem, error) {
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	query := "SELECT item_kind, item_id, content, scope_type, character_id FROM (" +
		" SELECT 'personal_memory'::text AS item_kind, p.id AS item_id, p.content," +
		" p.scope_kind AS scope_type, COALESCE(p.character_id, '') AS character_id" +
		" FROM personal_memories p WHERE p.status = 'active' AND p.review_status = 'ready'" +
		" UNION ALL" +
		" SELECT 'knowledge'::text AS item_kind, k.id AS item_id, k.topic || chr(10) || k.statement AS content," +
		" 'knowledge'::text AS scope_type, ''::text AS character_id" +
		" FROM knowledge_entries k WHERE k.status = 'verified'" +
		") items WHERE (item_kind, item_id) > ($1, $2)" +
		" ORDER BY item_kind ASC, item_id ASC LIMIT $3"
	rows, err := s.pool.Raw().Query(queryCtx, query, lastKind, lastID, limit)
	if err != nil {
		return nil, fmt.Errorf("querying authoritative vector items: %w", err)
	}
	defer rows.Close()
	items := make([]authoritativeVectorItem, 0, limit)
	for rows.Next() {
		var item authoritativeVectorItem
		if err := rows.Scan(&item.ItemKind, &item.ItemID, &item.Content, &item.ScopeType, &item.CharacterID); err != nil {
			return nil, fmt.Errorf("scanning authoritative vector item: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating authoritative vector items: %w", err)
	}
	return items, nil
}

func (s *Store) allAuthoritativeVectorItems(ctx context.Context) (map[uuid.UUID]authoritativeVectorItem, error) {
	items := make(map[uuid.UUID]authoritativeVectorItem)
	lastKind, lastID := "", ""
	for {
		page, err := s.authoritativeVectorPage(ctx, lastKind, lastID, maxVectorMaintenancePageSize)
		if err != nil {
			return nil, err
		}
		if len(page) == 0 {
			return items, nil
		}
		for _, item := range page {
			pointID, err := vectorindex.PointID(item.ItemKind, item.ItemID, SemanticEmbeddingModelID)
			if err != nil {
				return nil, err
			}
			items[pointID] = item
		}
		lastKind, lastID = page[len(page)-1].ItemKind, page[len(page)-1].ItemID
	}
}

func (s *Store) markRebuiltVectorItem(ctx context.Context, item authoritativeVectorItem, pointID uuid.UUID, hash string) error {
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	tx, err := s.pool.Raw().Begin(queryCtx)
	if err != nil {
		return fmt.Errorf("beginning rebuilt vector state transaction: %w", err)
	}
	defer tx.Rollback(queryCtx)
	now := nowUnixMS()
	query := "INSERT INTO memory_embedding_items(" +
		"id, item_kind, item_id, model_id, dimensions, point_id, content_hash, status, embedded_at_ms, created_at_ms, updated_at_ms" +
		") VALUES ($1, $2, $3, $4, $5, $6, $7, 'embedded', $8, $8, $8)" +
		" ON CONFLICT(item_kind, item_id, model_id) DO UPDATE SET" +
		" dimensions = excluded.dimensions, point_id = excluded.point_id, content_hash = excluded.content_hash," +
		" status = 'embedded', error_code = NULL, error_message = NULL," +
		" embedded_at_ms = excluded.embedded_at_ms, updated_at_ms = excluded.updated_at_ms"
	if _, err := tx.Exec(queryCtx, query, newID(), item.ItemKind, item.ItemID, SemanticEmbeddingModelID, SemanticEmbeddingDimensions, pointID, hash, now); err != nil {
		return fmt.Errorf("marking rebuilt vector item: %w", err)
	}
	if _, err := tx.Exec(queryCtx, "UPDATE memory_embedding_jobs SET status = 'succeeded', lease_owner = NULL, lease_expires_at_ms = NULL, error_code = NULL, error_message = NULL, retryable = false, updated_at_ms = $6 WHERE item_kind = $1 AND item_id = $2 AND model_id = $3 AND content_hash = $4 AND point_id = $5 AND status IN ('pending', 'failed')", item.ItemKind, item.ItemID, SemanticEmbeddingModelID, hash, pointID, now); err != nil {
		return fmt.Errorf("completing rebuilt embedding jobs: %w", err)
	}
	if err := tx.Commit(queryCtx); err != nil {
		return fmt.Errorf("committing rebuilt vector state: %w", err)
	}
	return nil
}

func (s *Store) failVectorRebuildRun(ctx context.Context, runID string, cause error) {
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	_, _ = s.pool.Raw().Exec(queryCtx, "UPDATE vector_rebuild_runs SET status = 'failed', error_message = $2, updated_at_ms = $3 WHERE id = $1 AND status = 'running'", runID, cleanEmbeddingErrorMessage(cause.Error()), nowUnixMS())
}

func (s *Store) ReconcileVectorIndex(ctx context.Context, index VectorMaintenanceIndex, apply bool) (VectorReconciliationResult, error) {
	if s == nil || s.pool == nil {
		return VectorReconciliationResult{}, errors.New("vector reconciliation requires PostgreSQL store")
	}
	if index == nil || index.Collection() == "" {
		return VectorReconciliationResult{}, errors.New("vector reconciliation requires vector index")
	}
	if err := index.Ready(ctx); err != nil {
		return VectorReconciliationResult{}, fmt.Errorf("vector index is unavailable: %w", err)
	}
	runID := newID()
	result := VectorReconciliationResult{
		RunID: runID, DryRun: !apply,
		MissingPoints: []string{}, StalePoints: []string{}, OrphanPoints: []string{},
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	now := nowUnixMS()
	_, err := s.pool.Raw().Exec(queryCtx, "INSERT INTO vector_reconciliation_runs(id, collection_name, dry_run, status, created_at_ms, updated_at_ms) VALUES ($1, $2, $3, 'running', $4, $4)", runID, index.Collection(), !apply, now)
	cancel()
	if err != nil {
		return result, fmt.Errorf("creating vector reconciliation run: %w", err)
	}
	expected, err := s.allAuthoritativeVectorItems(ctx)
	if err != nil {
		s.failVectorReconciliationRun(ctx, runID, err)
		return result, err
	}
	stored, err := index.ListPoints(ctx)
	if err != nil {
		s.failVectorReconciliationRun(ctx, runID, err)
		return result, err
	}
	storedByID := make(map[uuid.UUID]vectorindex.StoredPoint, len(stored))
	for _, point := range stored {
		storedByID[point.PointID] = point
		item, ok := expected[point.PointID]
		if !ok {
			result.OrphanPoints = append(result.OrphanPoints, point.PointID.String())
			continue
		}
		if point.ContentHash != semanticContentHash(item.Content) {
			result.StalePoints = append(result.StalePoints, point.PointID.String())
		}
	}
	for pointID := range expected {
		if _, ok := storedByID[pointID]; !ok {
			result.MissingPoints = append(result.MissingPoints, pointID.String())
		}
	}
	sort.Strings(result.MissingPoints)
	sort.Strings(result.StalePoints)
	sort.Strings(result.OrphanPoints)
	if apply && len(result.OrphanPoints) > 0 {
		current, err := s.allAuthoritativeVectorItems(ctx)
		if err != nil {
			s.failVectorReconciliationRun(ctx, runID, err)
			return result, err
		}
		confirmed := make([]uuid.UUID, 0, len(result.OrphanPoints))
		for _, rawID := range result.OrphanPoints {
			pointID, err := uuid.Parse(rawID)
			if err != nil {
				continue
			}
			if _, nowAuthoritative := current[pointID]; !nowAuthoritative {
				confirmed = append(confirmed, pointID)
			}
		}
		if err := index.DeletePoints(ctx, confirmed); err != nil {
			s.failVectorReconciliationRun(ctx, runID, err)
			return result, err
		}
	}
	report, err := json.Marshal(map[string]any{
		"missingPointIDs": result.MissingPoints,
		"stalePointIDs":   result.StalePoints,
		"orphanPointIDs":  result.OrphanPoints,
	})
	if err != nil {
		s.failVectorReconciliationRun(ctx, runID, err)
		return result, err
	}
	queryCtx, cancel = s.pool.QueryContext(ctx)
	defer cancel()
	_, err = s.pool.Raw().Exec(queryCtx, "UPDATE vector_reconciliation_runs SET status = 'succeeded', missing_points = $2, stale_points = $3, orphan_points = $4, report_json = $5, updated_at_ms = $6 WHERE id = $1 AND status = 'running'", runID, len(result.MissingPoints), len(result.StalePoints), len(result.OrphanPoints), report, nowUnixMS())
	if err != nil {
		return result, fmt.Errorf("completing vector reconciliation run: %w", err)
	}
	return result, nil
}

func (s *Store) failVectorReconciliationRun(ctx context.Context, runID string, cause error) {
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	_, _ = s.pool.Raw().Exec(queryCtx, "UPDATE vector_reconciliation_runs SET status = 'failed', error_message = $2, updated_at_ms = $3 WHERE id = $1 AND status = 'running'", runID, cleanEmbeddingErrorMessage(cause.Error()), nowUnixMS())
}
