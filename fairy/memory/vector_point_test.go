package memory

import (
	"testing"

	"github.com/google/uuid"
)

func TestPointIDIsDeterministicUUIDv5(t *testing.T) {
	first, err := VectorPointID(ItemKindPersonalMemory, "memory-1", "bge-small-zh-v1.5")
	if err != nil {
		t.Fatal(err)
	}
	second, err := VectorPointID(ItemKindPersonalMemory, "memory-1", "bge-small-zh-v1.5")
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("VectorPointID() = %s then %s", first, second)
	}
	if first.Version() != 5 || first.Variant() != uuid.RFC4122 {
		t.Fatalf("VectorPointID() = %s version=%d variant=%d", first, first.Version(), first.Variant())
	}
	otherKind, err := VectorPointID(ItemKindKnowledge, "memory-1", "bge-small-zh-v1.5")
	if err != nil {
		t.Fatal(err)
	}
	otherModel, err := VectorPointID(ItemKindPersonalMemory, "memory-1", "other-model")
	if err != nil {
		t.Fatal(err)
	}
	if otherKind == first || otherModel == first {
		t.Fatal("point id does not separate item kind and model id")
	}
}

func TestPointIDRejectsInvalidContractFields(t *testing.T) {
	tests := []struct {
		kind, item, model string
	}{
		{"other", "memory-1", "model"},
		{ItemKindPersonalMemory, "", "model"},
		{ItemKindPersonalMemory, " memory-1", "model"},
		{ItemKindPersonalMemory, "memory\x001", "model"},
		{ItemKindPersonalMemory, "memory-1", ""},
		{ItemKindPersonalMemory, "memory-1", "model "},
		{ItemKindPersonalMemory, "memory-1", "model\x00v1"},
	}
	for _, test := range tests {
		if id, err := VectorPointID(test.kind, test.item, test.model); err == nil || id != uuid.Nil {
			t.Fatalf("VectorPointID(%q, %q, %q) = (%s, %v), want nil UUID error", test.kind, test.item, test.model, id, err)
		}
	}
}
