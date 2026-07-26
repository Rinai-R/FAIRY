package vectorindex

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/qdrant/go-client/qdrant"
)

var payloadIndexes = map[string]qdrant.FieldType{
	"item_kind":    qdrant.FieldType_FieldTypeKeyword,
	"item_id":      qdrant.FieldType_FieldTypeKeyword,
	"model_id":     qdrant.FieldType_FieldTypeKeyword,
	"scope_type":   qdrant.FieldType_FieldTypeKeyword,
	"character_id": qdrant.FieldType_FieldTypeKeyword,
	"content_hash": qdrant.FieldType_FieldTypeKeyword,
}

type CollectionStatus struct {
	CollectionName string `json:"collectionName"`
	Dimensions     uint64 `json:"dimensions"`
	Distance       string `json:"distance"`
	PointsCount    uint64 `json:"pointsCount"`
}

func (c *Client) MigrateCollection(ctx context.Context) (err error) {
	started := time.Now()
	defer func() { c.observe("migrate_collection", started, err) }()
	if c == nil || c.client == nil {
		return fmt.Errorf("qdrant client is not open")
	}
	queryCtx, cancel := c.queryContext(ctx)
	defer cancel()
	exists, err := c.client.CollectionExists(queryCtx, c.config.collectionName())
	if err != nil {
		return sanitizeError("qdrant collection exists", c.config, err)
	}
	if !exists {
		if err := c.client.CreateCollection(queryCtx, &qdrant.CreateCollection{
			CollectionName: c.config.collectionName(),
			VectorsConfig:  qdrant.NewVectorsConfig(&qdrant.VectorParams{Size: Dimensions, Distance: qdrant.Distance_Cosine}),
		}); err != nil {
			return sanitizeError("qdrant create collection", c.config, err)
		}
	}
	if err := c.ensurePayloadIndexes(queryCtx); err != nil {
		return err
	}
	_, err = c.VerifyCollection(ctx)
	return err
}

func (c *Client) VerifyCollection(ctx context.Context) (status CollectionStatus, err error) {
	started := time.Now()
	defer func() { c.observe("verify_collection", started, err) }()
	if c == nil || c.client == nil {
		return CollectionStatus{}, fmt.Errorf("qdrant client is not open")
	}
	queryCtx, cancel := c.queryContext(ctx)
	defer cancel()
	info, err := c.client.GetCollectionInfo(queryCtx, c.config.collectionName())
	if err != nil {
		if isNotFound(err) {
			return CollectionStatus{}, errors.New("qdrant collection is missing")
		}
		return CollectionStatus{}, sanitizeError("qdrant get collection", c.config, err)
	}
	params := info.GetConfig().GetParams().GetVectorsConfig().GetParams()
	if params == nil {
		return CollectionStatus{}, errors.New("qdrant collection must use unnamed dense vectors")
	}
	if params.GetSize() != Dimensions || params.GetDistance() != qdrant.Distance_Cosine {
		return CollectionStatus{}, fmt.Errorf("qdrant collection contract mismatch: expected size=%d distance=%s actual size=%d distance=%s", Dimensions, Distance, params.GetSize(), params.GetDistance().String())
	}
	for name, fieldType := range payloadIndexes {
		schema := info.GetPayloadSchema()[name]
		if schema == nil || schema.GetDataType() != payloadSchemaType(fieldType) {
			return CollectionStatus{}, fmt.Errorf("qdrant payload index %q mismatch", name)
		}
	}
	return CollectionStatus{CollectionName: c.config.collectionName(), Dimensions: params.GetSize(), Distance: Distance, PointsCount: info.GetPointsCount()}, nil
}

func (c *Client) ensurePayloadIndexes(ctx context.Context) error {
	wait := true
	for name, fieldType := range payloadIndexes {
		if _, err := c.client.CreateFieldIndex(ctx, &qdrant.CreateFieldIndexCollection{
			CollectionName: c.config.collectionName(),
			Wait:           &wait,
			FieldName:      name,
			FieldType:      &fieldType,
		}); err != nil && !strings.Contains(err.Error(), "already exists") {
			return sanitizeError("qdrant create payload index", c.config, err)
		}
	}
	return nil
}

type PointPayloadInput struct {
	ItemKind    string
	ItemID      string
	ModelID     string
	ScopeType   string
	CharacterID string
	ContentHash string
}

type Point struct {
	ID      uuid.UUID
	Vector  []float32
	Payload PointPayloadInput
}

func (c *Client) Ready(ctx context.Context) error {
	_, err := c.VerifyCollection(ctx)
	return err
}

func (c *Client) DeleteCollection(ctx context.Context) (err error) {
	started := time.Now()
	defer func() { c.observe("delete_collection", started, err) }()
	if c == nil || c.client == nil {
		return fmt.Errorf("qdrant client is not open")
	}
	queryCtx, cancel := c.queryContext(ctx)
	defer cancel()
	if err := c.client.DeleteCollection(queryCtx, c.config.collectionName()); err != nil {
		return sanitizeError("qdrant delete collection", c.config, err)
	}
	return nil
}

func (c *Client) HasPoint(ctx context.Context, id uuid.UUID) (found bool, err error) {
	started := time.Now()
	defer func() { c.observe("get_point", started, err) }()
	if c == nil || c.client == nil {
		return false, fmt.Errorf("qdrant client is not open")
	}
	queryCtx, cancel := c.queryContext(ctx)
	defer cancel()
	points, err := c.client.Get(queryCtx, &qdrant.GetPoints{
		CollectionName: c.config.collectionName(),
		Ids:            []*qdrant.PointId{QdrantPointID(id)},
		WithPayload:    qdrant.NewWithPayload(false),
		WithVectors:    qdrant.NewWithVectors(false),
	})
	if err != nil {
		return false, sanitizeError("qdrant get point", c.config, err)
	}
	return len(points) == 1, nil
}

func (c *Client) Upsert(ctx context.Context, point Point) (err error) {
	started := time.Now()
	defer func() { c.observe("upsert", started, err) }()
	if c == nil || c.client == nil {
		return fmt.Errorf("qdrant client is not open")
	}
	payload, err := PointPayload(point.Payload)
	if err != nil {
		return err
	}
	if err := ValidateVector(point.Vector); err != nil {
		return err
	}
	queryCtx, cancel := c.queryContext(ctx)
	defer cancel()
	wait := true
	_, err = c.client.Upsert(queryCtx, &qdrant.UpsertPoints{
		CollectionName: c.config.collectionName(),
		Wait:           &wait,
		Points: []*qdrant.PointStruct{{
			Id:      QdrantPointID(point.ID),
			Vectors: qdrant.NewVectorsDense(point.Vector),
			Payload: qdrant.NewValueMap(payload),
		}},
	})
	if err != nil {
		return sanitizeError("qdrant upsert point", c.config, err)
	}
	return nil
}

func PointPayload(input PointPayloadInput) (map[string]any, error) {
	if _, err := PointID(input.ItemKind, input.ItemID, input.ModelID); err != nil {
		return nil, err
	}
	if !validPointToken(input.ScopeType) {
		return nil, errors.New("vector scope type is invalid")
	}
	if input.CharacterID != "" && !validPointToken(input.CharacterID) {
		return nil, errors.New("vector character id is invalid")
	}
	if !validContentHash(input.ContentHash) {
		return nil, errors.New("vector content hash is invalid")
	}
	payload := map[string]any{
		"item_kind":    input.ItemKind,
		"item_id":      input.ItemID,
		"model_id":     input.ModelID,
		"scope_type":   input.ScopeType,
		"content_hash": input.ContentHash,
	}
	if input.CharacterID != "" {
		payload["character_id"] = input.CharacterID
	}
	return payload, nil
}

func QdrantPointID(id uuid.UUID) *qdrant.PointId {
	return qdrant.NewID(id.String())
}

func ValidateVector(vector []float32) error {
	if len(vector) != Dimensions {
		return fmt.Errorf("vector dimensions = %d, want %d", len(vector), Dimensions)
	}
	for index, value := range vector {
		asFloat := float64(value)
		if math.IsNaN(asFloat) || math.IsInf(asFloat, 0) {
			return fmt.Errorf("vector contains non-finite value at index %d", index)
		}
	}
	return nil
}

func validContentHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if character >= '0' && character <= '9' || character >= 'a' && character <= 'f' {
			continue
		}
		return false
	}
	return true
}

func payloadSchemaType(fieldType qdrant.FieldType) qdrant.PayloadSchemaType {
	switch fieldType {
	case qdrant.FieldType_FieldTypeKeyword:
		return qdrant.PayloadSchemaType_Keyword
	default:
		return qdrant.PayloadSchemaType_UnknownType
	}
}
