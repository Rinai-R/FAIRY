package admin

import (
	"errors"
	"testing"

	"fairy/context/memory/personal"
)

func TestNewServiceFromStoreRequiresStore(t *testing.T) {
	service, err := NewServiceFromStore(nil)
	if service != nil || !errors.Is(err, personal.ErrDatabasePoolEmpty) {
		t.Fatalf("NewServiceFromStore(nil) = (%v, %v)", service, err)
	}
}
