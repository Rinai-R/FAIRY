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

const MaxSearchLimit = 64

type SearchHit struct {
	PointID     uuid.UUID
	ItemKind    string
	ItemID      string
	ModelID     string
	ScopeType   string
	CharacterID string
	ContentHash string
	Score       float64
}

func (c *Client) Search(ctx context.Context, vector []float32, modelID, characterID string, limit int) (hits []SearchHit, err error) {
	started := time.Now()
	defer func() { c.observe("search", started, err) }()
	if c == nil || c.client == nil {
		return nil, errors.New("qdrant client is not open")
	}
	if err := ValidateVector(vector); err != nil {
		return nil, err
	}
	if !validPointToken(modelID) {
		return nil, errors.New("vector model id is invalid")
	}
	if !validPointToken(characterID) {
		return nil, errors.New("vector character id is invalid")
	}
	if limit < 1 || limit > MaxSearchLimit {
		return nil, fmt.Errorf("vector search limit must be between 1 and %d", MaxSearchLimit)
	}
	queryCtx, cancel := c.queryContext(ctx)
	defer cancel()
	queryLimit := uint64(limit)
	points, err := c.client.Query(queryCtx, &qdrant.QueryPoints{
		CollectionName: c.config.collectionName(),
		Query:          qdrant.NewQueryDense(vector),
		Filter: &qdrant.Filter{
			Must: []*qdrant.Condition{qdrant.NewMatchKeyword("model_id", modelID)},
			Should: []*qdrant.Condition{
				qdrant.NewMatchKeywords("scope_type", "global", "knowledge"),
				qdrant.NewMatchKeyword("character_id", characterID),
			},
		},
		Limit:       &queryLimit,
		WithPayload: qdrant.NewWithPayloadInclude("item_kind", "item_id", "model_id", "scope_type", "character_id", "content_hash"),
		WithVectors: qdrant.NewWithVectors(false),
	})
	if err != nil {
		return nil, sanitizeError("qdrant search points", c.config, err)
	}
	hits = make([]SearchHit, 0, len(points))
	for _, point := range points {
		hit, err := decodeSearchHit(point)
		if err != nil {
			return nil, err
		}
		hits = append(hits, hit)
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].PointID.String() < hits[j].PointID.String()
	})
	return hits, nil
}

func decodeSearchHit(point *qdrant.ScoredPoint) (SearchHit, error) {
	if point == nil || point.GetId() == nil {
		return SearchHit{}, errors.New("qdrant search hit is missing point id")
	}
	pointID, err := uuid.Parse(point.GetId().GetUuid())
	if err != nil {
		return SearchHit{}, errors.New("qdrant search hit point id is invalid")
	}
	payload := point.GetPayload()
	hit := SearchHit{
		PointID:     pointID,
		ItemKind:    payloadString(payload, "item_kind"),
		ItemID:      payloadString(payload, "item_id"),
		ModelID:     payloadString(payload, "model_id"),
		ScopeType:   payloadString(payload, "scope_type"),
		CharacterID: payloadString(payload, "character_id"),
		ContentHash: payloadString(payload, "content_hash"),
		Score:       float64(point.GetScore()),
	}
	if _, err := PointPayload(PointPayloadInput{ItemKind: hit.ItemKind, ItemID: hit.ItemID, ModelID: hit.ModelID, ScopeType: hit.ScopeType, CharacterID: hit.CharacterID, ContentHash: hit.ContentHash}); err != nil {
		return SearchHit{}, fmt.Errorf("qdrant search hit payload is invalid: %w", err)
	}
	expectedID, err := PointID(hit.ItemKind, hit.ItemID, hit.ModelID)
	if err != nil || expectedID != hit.PointID {
		return SearchHit{}, errors.New("qdrant search hit point id does not match payload")
	}
	return hit, nil
}

func payloadString(payload map[string]*qdrant.Value, key string) string {
	value := payload[key]
	if value == nil {
		return ""
	}
	return value.GetStringValue()
}
