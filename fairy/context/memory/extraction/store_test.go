package extraction

import (
	"errors"
	"testing"
	"time"
)

func TestNewSeekDBStoreValidatesDatabaseFirst(t *testing.T) {
	store, err := NewSeekDBStore(nil, time.Second, "worker-1", time.Second)
	if store != nil || !errors.Is(err, ErrSeekDBConnectionEmpty) {
		t.Fatalf("NewSeekDBStore(nil) = (%v, %v)", store, err)
	}
}
