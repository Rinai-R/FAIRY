package memory

import (
	"errors"
	"strings"
	"testing"
)

func TestNewStoreRequiresDatabase(t *testing.T) {
	if _, err := NewIdentityStore(nil); !errors.Is(err, ErrIdentityDatabasePoolRequired) {
		t.Fatalf("NewIdentityStore(nil) error = %v", err)
	}
}

func TestOwnerMethodsValidateIdentityBeforeDatabase(t *testing.T) {
	store := &IdentityStore{}
	validDigest := strings.Repeat("a", 64)
	tests := []struct {
		name      string
		namespace string
		digest    string
	}{
		{name: "missing namespace", digest: validDigest},
		{name: "invalid namespace", namespace: "QQ/User", digest: validDigest},
		{name: "short digest", namespace: "qq.user", digest: "short"},
		{name: "uppercase digest", namespace: "qq.user", digest: strings.Repeat("A", 64)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := store.BindOwner(test.namespace, test.digest); err == nil {
				t.Fatal("BindOwner succeeded")
			}
			if _, err := store.IsOwner(test.namespace, test.digest); err == nil {
				t.Fatal("IsOwner succeeded")
			}
			if err := store.UnbindOwner(test.namespace, test.digest); err == nil {
				t.Fatal("UnbindOwner succeeded")
			}
		})
	}
}
