package memory

import (
	"errors"
	"testing"
	"time"
)

func TestNewStoreFromPoolRequiresPool(t *testing.T) {
	store, err := NewStoreFromPool(nil)
	if store != nil || !errors.Is(err, ErrDatabasePoolEmpty) {
		t.Fatalf("NewStoreFromPool(nil) = (%v, %v), want (nil, %v)", store, err, ErrDatabasePoolEmpty)
	}
}

func TestNewMemoryServiceFromStoreRequiresStore(t *testing.T) {
	service, err := NewMemoryServiceFromStore(nil)
	if service != nil || !errors.Is(err, ErrDatabasePoolEmpty) {
		t.Fatalf("NewMemoryServiceFromStore(nil) = (%v, %v), want (nil, %v)", service, err, ErrDatabasePoolEmpty)
	}
}

func TestNewStoreFromPoolLeaseValidationRunsAfterPoolValidation(t *testing.T) {
	store, err := newStoreFromPoolWithLease(nil, "worker-1", time.Second)
	if store != nil || !errors.Is(err, ErrDatabasePoolEmpty) {
		t.Fatalf("newStoreFromPoolWithLease(nil) = (%v, %v)", store, err)
	}
}
