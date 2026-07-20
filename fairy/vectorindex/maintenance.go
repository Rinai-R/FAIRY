package vectorindex

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/qdrant/go-client/qdrant"
)

type StoredPoint struct {
	PointID     uuid.UUID
	ItemKind    string
	ItemID      string
	ModelID     string
	ScopeType   string
	CharacterID string
	ContentHash string
}

func (c *Client) ListPoints(ctx context.Context) (points []StoredPoint, err error) {
	started := time.Now()
	defer func() { c.observe("list_points", started, err) }()
	if c == nil || c.client == nil {
		return nil, errors.New("qdrant client is not open")
	}
	queryCtx, cancel := c.queryContext(ctx)
	defer cancel()
	const pageSize uint32 = 64
	var offset *qdrant.PointId
	points = make([]StoredPoint, 0)
	for {
		page, next, err := c.client.ScrollAndOffset(queryCtx, &qdrant.ScrollPoints{
			CollectionName: c.config.collectionName(),
			Offset:         offset,
			Limit:          qdrant.PtrOf(pageSize),
			WithPayload:    qdrant.NewWithPayloadInclude("item_kind", "item_id", "model_id", "scope_type", "character_id", "content_hash"),
			WithVectors:    qdrant.NewWithVectors(false),
		})
		if err != nil {
			return nil, sanitizeError("qdrant scroll points", c.config, err)
		}
		for _, point := range page {
			stored, err := decodeStoredPoint(point)
			if err != nil {
				return nil, err
			}
			points = append(points, stored)
		}
		if next == nil {
			break
		}
		offset = next
	}
	sort.Slice(points, func(i, j int) bool {
		return points[i].PointID.String() < points[j].PointID.String()
	})
	return points, nil
}

func (c *Client) DeletePoints(ctx context.Context, ids []uuid.UUID) (err error) {
	started := time.Now()
	defer func() { c.observe("delete_points", started, err) }()
	if c == nil || c.client == nil {
		return errors.New("qdrant client is not open")
	}
	if len(ids) == 0 {
		return nil
	}
	pointIDs := make([]*qdrant.PointId, 0, len(ids))
	for _, id := range ids {
		if id == uuid.Nil {
			return errors.New("qdrant delete point id is required")
		}
		pointIDs = append(pointIDs, QdrantPointID(id))
	}
	queryCtx, cancel := c.queryContext(ctx)
	defer cancel()
	wait := true
	if _, err := c.client.Delete(queryCtx, &qdrant.DeletePoints{
		CollectionName: c.config.collectionName(),
		Wait:           &wait,
		Points:         qdrant.NewPointsSelector(pointIDs...),
	}); err != nil {
		return sanitizeError("qdrant delete points", c.config, err)
	}
	return nil
}

func decodeStoredPoint(point *qdrant.RetrievedPoint) (StoredPoint, error) {
	if point == nil || point.GetId() == nil {
		return StoredPoint{}, errors.New("qdrant stored point is missing point id")
	}
	pointID, err := uuid.Parse(point.GetId().GetUuid())
	if err != nil {
		return StoredPoint{}, errors.New("qdrant stored point id is invalid")
	}
	payload := point.GetPayload()
	stored := StoredPoint{
		PointID:     pointID,
		ItemKind:    payloadString(payload, "item_kind"),
		ItemID:      payloadString(payload, "item_id"),
		ModelID:     payloadString(payload, "model_id"),
		ScopeType:   payloadString(payload, "scope_type"),
		CharacterID: payloadString(payload, "character_id"),
		ContentHash: payloadString(payload, "content_hash"),
	}
	if _, err := PointPayload(PointPayloadInput{ItemKind: stored.ItemKind, ItemID: stored.ItemID, ModelID: stored.ModelID, ScopeType: stored.ScopeType, CharacterID: stored.CharacterID, ContentHash: stored.ContentHash}); err != nil {
		return StoredPoint{}, fmt.Errorf("qdrant stored point payload is invalid: %w", err)
	}
	expected, err := PointID(stored.ItemKind, stored.ItemID, stored.ModelID)
	if err != nil || expected != stored.PointID {
		return StoredPoint{}, errors.New("qdrant stored point id does not match payload")
	}
	return stored, nil
}
