package compaction

import (
	"errors"
	"testing"
)

func TestNewStoreRejectsMissingPool(t *testing.T) {
	store, err := NewStoreFromPool(nil)
	if store != nil || !errors.Is(err, ErrDatabasePoolEmpty) {
		t.Fatalf("NewStoreFromPool(nil) = (%v, %v), want nil, %v", store, err, ErrDatabasePoolEmpty)
	}
}
