package identity

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestNewSeekDBStoreRequiresBoundedDatabase(t *testing.T) {
	tests := []struct {
		name       string
		database   *sql.DB
		queryLimit time.Duration
		want       error
	}{
		{name: "missing database", queryLimit: time.Second, want: ErrIdentitySeekDBRequired},
		{name: "zero query limit", database: new(sql.DB), want: ErrIdentityQueryLimitInvalid},
		{name: "negative query limit", database: new(sql.DB), queryLimit: -time.Second, want: ErrIdentityQueryLimitInvalid},
		{name: "valid", database: new(sql.DB), queryLimit: time.Second},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, err := NewSeekDBStore(test.database, test.queryLimit)
			if !errors.Is(err, test.want) {
				t.Fatalf("NewSeekDBStore() error = %v, want %v", err, test.want)
			}
			if test.want == nil && (store == nil || store.seekDB != test.database) {
				t.Fatalf("NewSeekDBStore() = %#v", store)
			}
		})
	}
}

func TestSeekDBStoreQueryContextIsBounded(t *testing.T) {
	store := &Store{queryLimit: time.Second}
	ctx, cancel := store.seekDBQueryContext(context.Background())
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > time.Second {
		t.Fatalf("query deadline = %v, ok %v", deadline, ok)
	}
}

func TestPrincipalDigestSeekDBCodec(t *testing.T) {
	want := strings.Repeat("a1", principalDigestBytes)
	digest, err := validateAndDecodeIdentity("qq.onebot", want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := encodePrincipalDigest(digest)
	if err != nil || got != want {
		t.Fatalf("encodePrincipalDigest() = %q, %v, want %q", got, err, want)
	}
	if _, err := encodePrincipalDigest(digest[:len(digest)-1]); !errors.Is(err, ErrOwnerIdentityCorrupt) {
		t.Fatalf("encodePrincipalDigest(short) error = %v", err)
	}
}

func TestOwnerMethodsValidateIdentityBeforeDatabase(t *testing.T) {
	store := &Store{}
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
