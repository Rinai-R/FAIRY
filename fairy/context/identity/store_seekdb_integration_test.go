//go:build integration

package identity

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fairy/runtime/seekdb"
)

func TestRealSeekDBIdentityStorePreservesDigestAndIdempotentBinding(t *testing.T) {
	instance, database, runtimeConfig := openIdentitySeekDBRuntime(t)
	t.Cleanup(func() {
		closeIdentitySeekDBRuntime(t, instance, runtimeConfig.ShutdownLimit)
	})
	if err := seekdb.MigrateSchema(t.Context(), database, seekdb.BuiltinMigrations()); err != nil {
		t.Fatalf("migrate SeekDB foundation schema: %v", err)
	}

	store, err := NewSeekDBStore(database, runtimeConfig.QueryLimit)
	if err != nil {
		t.Fatal(err)
	}
	firstCreatedAt := int64(1_800_000_000_000)
	store.now = func() time.Time { return time.UnixMilli(firstCreatedAt) }
	qqDigest := strings.Repeat("a1", principalDigestBytes)
	telegramDigest := strings.Repeat("02", principalDigestBytes)

	if err := store.BindOwnerContext(t.Context(), "qq.onebot", qqDigest); err != nil {
		t.Fatalf("BindOwnerContext(first): %v", err)
	}
	store.now = func() time.Time { return time.UnixMilli(firstCreatedAt + 10_000) }
	if err := store.BindOwnerContext(t.Context(), "qq.onebot", qqDigest); err != nil {
		t.Fatalf("BindOwnerContext(idempotent): %v", err)
	}
	if err := store.BindOwnerContext(t.Context(), "telegram.bot", telegramDigest); err != nil {
		t.Fatalf("BindOwnerContext(second namespace): %v", err)
	}

	owners, err := store.ListOwnersContext(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(owners) != 2 {
		t.Fatalf("ListOwnersContext() length = %d, want 2: %#v", len(owners), owners)
	}
	if owners[0].Namespace != "qq.onebot" || owners[0].PrincipalDigest != qqDigest || owners[0].CreatedAtUnixMS != firstCreatedAt {
		t.Fatalf("first owner = %#v", owners[0])
	}
	if owners[1].Namespace != "telegram.bot" || owners[1].PrincipalDigest != telegramDigest {
		t.Fatalf("second owner = %#v", owners[1])
	}
	for _, owner := range owners {
		if len(owner.PrincipalDigest) != principalDigestBytes*2 || strings.ToLower(owner.PrincipalDigest) != owner.PrincipalDigest {
			t.Fatalf("owner digest is not 64 lowercase hex characters: %q", owner.PrincipalDigest)
		}
	}

	storedDigest := readStoredIdentityDigest(t, database, "qq.onebot")
	wantStoredDigest, err := validateAndDecodeIdentity("qq.onebot", qqDigest)
	if err != nil {
		t.Fatal(err)
	}
	if len(storedDigest) != principalDigestBytes || !bytes.Equal(storedDigest, wantStoredDigest) {
		t.Fatalf("stored digest = %x (%d bytes), want %x", storedDigest, len(storedDigest), wantStoredDigest)
	}

	owner, err := store.IsOwnerContext(t.Context(), "qq.onebot", qqDigest)
	if err != nil || !owner {
		t.Fatalf("IsOwnerContext(bound) = %v, %v", owner, err)
	}
	owner, err = store.IsOwnerContext(t.Context(), "telegram.bot", qqDigest)
	if err != nil || owner {
		t.Fatalf("IsOwnerContext(wrong namespace) = %v, %v", owner, err)
	}

	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := store.IsOwnerContext(canceled, "qq.onebot", qqDigest); !errors.Is(err, context.Canceled) {
		t.Fatalf("IsOwnerContext(canceled) error = %v, want context.Canceled", err)
	}

	if err := store.UnbindOwnerContext(t.Context(), "qq.onebot", qqDigest); err != nil {
		t.Fatalf("UnbindOwnerContext(): %v", err)
	}
	if err := store.UnbindOwnerContext(t.Context(), "qq.onebot", qqDigest); !errors.Is(err, ErrOwnerIdentityNotFound) {
		t.Fatalf("UnbindOwnerContext(second) error = %v, want ErrOwnerIdentityNotFound", err)
	}
}

func TestRealSeekDBIdentityMutationHelpersHonorTransactionOutcome(t *testing.T) {
	instance, database, runtimeConfig := openIdentitySeekDBRuntime(t)
	t.Cleanup(func() {
		closeIdentitySeekDBRuntime(t, instance, runtimeConfig.ShutdownLimit)
	})
	if err := seekdb.MigrateSchema(t.Context(), database, seekdb.BuiltinMigrations()); err != nil {
		t.Fatalf("migrate SeekDB foundation schema: %v", err)
	}
	store, err := NewSeekDBStore(database, runtimeConfig.QueryLimit)
	if err != nil {
		t.Fatal(err)
	}
	const namespace = "matrix.local"
	digestHex := strings.Repeat("bc", principalDigestBytes)
	digest, err := validateAndDecodeIdentity(namespace, digestHex)
	if err != nil {
		t.Fatal(err)
	}

	rolledBack, err := database.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := bindOwnerSeekDB(t.Context(), rolledBack, namespace, digest, 1_800_000_000_001); err != nil {
		rolledBack.Rollback()
		t.Fatal(err)
	}
	inside, err := isOwnerSeekDB(t.Context(), rolledBack, namespace, digest)
	if err != nil || !inside {
		rolledBack.Rollback()
		t.Fatalf("transactional IsOwner = %v, %v", inside, err)
	}
	if err := rolledBack.Rollback(); err != nil {
		t.Fatal(err)
	}
	owner, err := store.IsOwnerContext(t.Context(), namespace, digestHex)
	if err != nil || owner {
		t.Fatalf("IsOwnerContext(after rollback) = %v, %v", owner, err)
	}

	committed, err := database.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := bindOwnerSeekDB(t.Context(), committed, namespace, digest, 1_800_000_000_002); err != nil {
		committed.Rollback()
		t.Fatal(err)
	}
	if err := committed.Commit(); err != nil {
		t.Fatal(err)
	}
	owner, err = store.IsOwnerContext(t.Context(), namespace, digestHex)
	if err != nil || !owner {
		t.Fatalf("IsOwnerContext(after commit) = %v, %v", owner, err)
	}
}

func openIdentitySeekDBRuntime(t *testing.T) (*seekdb.Runtime, *sql.DB, seekdb.Config) {
	t.Helper()
	binary := os.Getenv(seekdb.EnvBinaryPath)
	if binary == "" {
		t.Skip(seekdb.EnvBinaryPath + " is not set")
	}
	runtimeConfig := seekdb.Config{
		BinaryPath:    binary,
		LibraryDirs:   filepath.SplitList(os.Getenv(seekdb.EnvLibraryPath)),
		DataDir:       filepath.Join(t.TempDir(), "seekdb-data"),
		Address:       reserveIdentityLoopbackAddress(t),
		Database:      seekdb.DefaultDatabase,
		User:          seekdb.DefaultUser,
		ConnectLimit:  5 * time.Second,
		StartLimit:    90 * time.Second,
		QueryLimit:    15 * time.Second,
		ShutdownLimit: 20 * time.Second,
		MaxOpenConns:  4,
		MaxIdleConns:  2,
	}
	instance, err := seekdb.Open(t.Context(), runtimeConfig)
	if err != nil {
		t.Fatalf("open real SeekDB runtime: %v", err)
	}
	return instance, instance.SQL(), runtimeConfig
}

func reserveIdentityLoopbackAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}

func closeIdentitySeekDBRuntime(t *testing.T, instance *seekdb.Runtime, timeout time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := instance.Close(ctx); err != nil {
		t.Errorf("close real SeekDB runtime: %v", err)
	}
}

func readStoredIdentityDigest(t *testing.T, database *sql.DB, namespace string) []byte {
	t.Helper()
	var digest []byte
	if err := database.QueryRowContext(t.Context(), `
SELECT subject_digest
FROM owner_identities
WHERE namespace = ?`, namespace).Scan(&digest); err != nil {
		t.Fatal(err)
	}
	return digest
}
