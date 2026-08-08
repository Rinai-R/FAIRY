package extraction

import (
	"errors"
	"testing"
	"time"
)

func TestNewStoreFromPoolWithLeaseValidatesPoolFirst(t *testing.T) {
	store, err := NewStoreFromPoolWithLease(nil, nil, "worker-1", time.Second)
	if store != nil || !errors.Is(err, ErrDatabasePoolEmpty) {
		t.Fatalf("NewStoreFromPoolWithLease(nil) = (%v, %v)", store, err)
	}
}
