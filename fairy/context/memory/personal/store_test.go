package personal

import (
	"errors"
	"testing"
)

func TestNewStoreFromPoolRequiresPool(t *testing.T) {
	store, err := NewStoreFromPool(nil, nil)
	if store != nil || !errors.Is(err, ErrDatabasePoolEmpty) {
		t.Fatalf("NewStoreFromPool(nil) = (%v, %v), want (nil, %v)", store, err, ErrDatabasePoolEmpty)
	}
}
