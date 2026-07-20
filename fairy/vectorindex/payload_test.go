package vectorindex

import (
	"math"
	"strings"
	"testing"
)

func TestPointPayloadUsesMinimalMetadataOnly(t *testing.T) {
	payload, err := PointPayload(PointPayloadInput{
		ItemKind:    ItemKindPersonalMemory,
		ItemID:      "memory-1",
		ModelID:     "bge-small-zh-v1.5",
		ScopeType:   "character",
		CharacterID: "character-1",
		ContentHash: strings.Repeat("a", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"content", "text", "prompt", "reasoning", "secret", "credential", "api_key"} {
		if _, ok := payload[forbidden]; ok {
			t.Fatalf("payload contains forbidden field %q: %#v", forbidden, payload)
		}
	}
	if payload["item_kind"] != ItemKindPersonalMemory || payload["content_hash"] != strings.Repeat("a", 64) {
		t.Fatalf("payload = %#v", payload)
	}
}

func TestPointPayloadRejectsInvalidHashAndScope(t *testing.T) {
	base := PointPayloadInput{ItemKind: ItemKindKnowledge, ItemID: "knowledge-1", ModelID: "model", ScopeType: "global", ContentHash: strings.Repeat("a", 64)}
	badHash := base
	badHash.ContentHash = strings.Repeat("A", 64)
	if _, err := PointPayload(badHash); err == nil {
		t.Fatal("PointPayload(bad hash) error = nil")
	}
	badScope := base
	badScope.ScopeType = " global"
	if _, err := PointPayload(badScope); err == nil {
		t.Fatal("PointPayload(bad scope) error = nil")
	}
}

func TestValidateVectorRejectsDimensionsAndNonFiniteValues(t *testing.T) {
	if err := ValidateVector(make([]float32, Dimensions)); err != nil {
		t.Fatalf("ValidateVector(valid) error = %v", err)
	}
	if err := ValidateVector(make([]float32, Dimensions-1)); err == nil {
		t.Fatal("ValidateVector(wrong dimensions) error = nil")
	}
	nonFinite := make([]float32, Dimensions)
	nonFinite[7] = float32(math.NaN())
	if err := ValidateVector(nonFinite); err == nil || !strings.Contains(err.Error(), "index 7") {
		t.Fatalf("ValidateVector(NaN) error = %v", err)
	}
}
