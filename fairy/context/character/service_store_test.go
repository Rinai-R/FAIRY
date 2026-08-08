package character

import "testing"

func TestCharacterServiceReusesCatalogStore(t *testing.T) {
	service := NewCharacterService(t.TempDir())
	first := service.CatalogStore()
	second := service.CatalogStore()
	if first == nil || first != second {
		t.Fatalf("CatalogStore() instances differ: %p vs %p", first, second)
	}
	if _, err := service.ListCharacters(); err != nil {
		t.Fatalf("ListCharacters() error = %v", err)
	}
	if service.CatalogStore() != first {
		t.Fatal("CatalogStore() changed after ListCharacters")
	}
}
