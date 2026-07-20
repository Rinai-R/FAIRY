//go:build integration

package secret

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	pgstore "fairy/postgres"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresStoreEncryptsRoundTripsRotatesNonceAndRejectsWrongKey(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedSecretPool(t, ctx)
	defer pool.Close()
	if err := pgstore.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	cipher, err := newCipher(bytesOf(1, keyBytes), bytes.NewReader(ascendingBytes(48)))
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewPostgresStore(pool, cipher)
	if err != nil {
		t.Fatal(err)
	}
	value, err := NewValue("sk-live-exact-secret")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveContext(ctx, "connection-1", value); err != nil {
		t.Fatal(err)
	}
	firstNonce, firstCiphertext := readEncryptedRow(t, ctx, pool, "model", "connection-1")
	if bytes.Contains(firstCiphertext, []byte(value.Expose())) {
		t.Fatal("ciphertext contains plaintext")
	}
	loaded, ok, err := store.LoadContext(ctx, "connection-1")
	if err != nil || !ok || loaded.Expose() != value.Expose() {
		t.Fatalf("LoadContext() = (%v, %v, %v)", loaded, ok, err)
	}
	if err := store.SaveContext(ctx, "connection-1", value); err != nil {
		t.Fatal(err)
	}
	secondNonce, secondCiphertext := readEncryptedRow(t, ctx, pool, "model", "connection-1")
	if bytes.Equal(firstNonce, secondNonce) || bytes.Equal(firstCiphertext, secondCiphertext) {
		t.Fatal("repeated save reused nonce or ciphertext")
	}
	wrongCipher, err := newCipher(bytesOf(2, keyBytes), bytes.NewReader(bytesOf(4, 12)))
	if err != nil {
		t.Fatal(err)
	}
	wrongStore, err := NewPostgresStore(pool, wrongCipher)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = wrongStore.LoadContext(ctx, "connection-1")
	if err == nil {
		t.Fatal("LoadContext(wrong key) error = nil")
	}
	message := err.Error()
	for _, forbidden := range []string{value.Expose(), fmt.Sprintf("%x", secondCiphertext), fmt.Sprintf("%x", bytesOf(2, keyBytes))} {
		if strings.Contains(message, forbidden) {
			t.Fatalf("error leaked secret material: %q", message)
		}
	}
	if err := store.DeleteContext(ctx, "connection-1"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.LoadContext(ctx, "connection-1"); err != nil || ok {
		t.Fatalf("LoadContext after delete = (ok=%v, err=%v)", ok, err)
	}
}

func readEncryptedRow(t *testing.T, ctx context.Context, pool *pgstore.Pool, namespace, name string) ([]byte, []byte) {
	t.Helper()
	var keyVersion int
	var nonce, ciphertext []byte
	var aad string
	if err := pool.Raw().QueryRow(ctx, "SELECT key_version, nonce, ciphertext, aad FROM secret_values WHERE namespace = $1 AND name = $2", namespace, name).Scan(&keyVersion, &nonce, &ciphertext, &aad); err != nil {
		t.Fatal(err)
	}
	if keyVersion != KeyVersion || len(nonce) != 12 || aad != secretAAD(namespace, name, KeyVersion) {
		t.Fatalf("encrypted row metadata = version %d nonce %d aad %q", keyVersion, len(nonce), aad)
	}
	return nonce, ciphertext
}

func openIsolatedSecretPool(t *testing.T, ctx context.Context) *pgstore.Pool {
	t.Helper()
	databaseURL := os.Getenv("FAIRY_TEST_DATABASE_URL")
	if databaseURL == "" {
		databaseURL = "postgres://fairy:fairy_test_password@127.0.0.1:15432/fairy_test?sslmode=disable"
	}
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := fmt.Sprintf("fairy_secret_test_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		cleanup, err := pgxpool.New(cleanupCtx, databaseURL)
		if err != nil {
			return
		}
		defer cleanup.Close()
		_, _ = cleanup.Exec(cleanupCtx, "DROP SCHEMA IF EXISTS "+quoted+" CASCADE")
	})
	parsed, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	values := parsed.Query()
	values.Set("search_path", schema)
	parsed.RawQuery = values.Encode()
	pool, err := pgstore.Open(ctx, pgstore.ShortTimeoutConfig(parsed.String()))
	if err != nil {
		t.Fatal(err)
	}
	return pool
}

func ascendingBytes(count int) []byte {
	out := make([]byte, count)
	for index := range out {
		out[index] = byte(index + 1)
	}
	return out
}
