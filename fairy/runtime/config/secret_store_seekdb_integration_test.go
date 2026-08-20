//go:build integration

package config

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"fairy/runtime/seekdb"
)

func TestRealSeekDBSecretStoreEncryptsRoundTripsRotatesNonceAndEnforcesSchema(t *testing.T) {
	instance, database, runtimeConfig := openSecretStoreSeekDBRuntime(t)
	t.Cleanup(func() {
		closeSecretStoreSeekDBRuntime(t, instance, runtimeConfig.ShutdownLimit)
	})

	if err := seekdb.MigrateSchema(t.Context(), database, seekdb.BuiltinMigrations()); err != nil {
		t.Fatalf("migrate SeekDB foundation schema: %v", err)
	}
	assertSecretValuesColumnContract(t, database)

	key := integrationRepeatedBytes(0x31, keyBytes)
	cipher, err := newSecretCipher(key, bytes.NewReader(integrationAscendingBytes(36)))
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewSeekDBSecretStore(database, cipher, runtimeConfig.QueryLimit)
	if err != nil {
		t.Fatal(err)
	}

	const (
		connectionID = "seekdb-integration-primary"
		plaintext    = "sk-seekdb-integration-exact-secret"
	)
	value, err := NewSecretValue(plaintext)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveContext(t.Context(), connectionID, value); err != nil {
		t.Fatalf("SaveContext(first): %v", err)
	}
	firstNonce, firstCiphertext := readSeekDBEncryptedSecretRow(t, database, "model", connectionID)
	if bytes.Contains(firstCiphertext, []byte(plaintext)) {
		t.Fatal("stored ciphertext contains plaintext")
	}

	loaded, ok, err := store.LoadContext(t.Context(), connectionID)
	if err != nil || !ok || loaded.Expose() != plaintext {
		t.Fatalf("LoadContext() = (%v, %v, %v)", loaded, ok, err)
	}

	if err := store.SaveContext(t.Context(), connectionID, value); err != nil {
		t.Fatalf("SaveContext(second): %v", err)
	}
	secondNonce, secondCiphertext := readSeekDBEncryptedSecretRow(t, database, "model", connectionID)
	if bytes.Equal(firstNonce, secondNonce) {
		t.Fatal("repeated SaveContext reused the nonce")
	}
	if bytes.Equal(firstCiphertext, secondCiphertext) {
		t.Fatal("repeated SaveContext reused the ciphertext")
	}
	if bytes.Contains(secondCiphertext, []byte(plaintext)) {
		t.Fatal("replacement ciphertext contains plaintext")
	}

	firstCreatedAt, firstUpdatedAt := readSeekDBSecretTimestamps(t, database, "model", connectionID)
	store.now = func() time.Time { return time.UnixMilli(firstCreatedAt - 1_000) }
	if err := store.SaveContext(t.Context(), connectionID, value); err != nil {
		t.Fatalf("SaveContext(clock rollback): %v", err)
	}
	createdAt, updatedAt := readSeekDBSecretTimestamps(t, database, "model", connectionID)
	if createdAt != firstCreatedAt || updatedAt != firstUpdatedAt {
		t.Fatalf("timestamps after clock rollback = (%d, %d), want (%d, %d)", createdAt, updatedAt, firstCreatedAt, firstUpdatedAt)
	}
	store.now = time.Now

	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := store.SaveContext(canceled, "canceled-operation", value); !errors.Is(err, context.Canceled) {
		t.Fatalf("SaveContext(canceled) error = %v, want context.Canceled", err)
	}
	if _, found, err := store.LoadContext(canceled, connectionID); !errors.Is(err, context.Canceled) || found {
		t.Fatalf("LoadContext(canceled) = (found %v, error %v), want context.Canceled", found, err)
	}
	if err := store.DeleteContext(canceled, connectionID); !errors.Is(err, context.Canceled) {
		t.Fatalf("DeleteContext(canceled) error = %v, want context.Canceled", err)
	}

	wrongKey := integrationRepeatedBytes(0xa7, keyBytes)
	wrongCipher, err := newSecretCipher(wrongKey, bytes.NewReader(integrationRepeatedBytes(0x52, 12)))
	if err != nil {
		t.Fatal(err)
	}
	wrongStore, err := NewSeekDBSecretStore(database, wrongCipher, runtimeConfig.QueryLimit)
	if err != nil {
		t.Fatal(err)
	}
	wrongValue, wrongOK, err := wrongStore.LoadContext(t.Context(), connectionID)
	if !errors.Is(err, ErrSecretDecryptFailed) {
		t.Fatalf("LoadContext(wrong key) error = %v, want ErrSecretDecryptFailed", err)
	}
	if wrongOK || wrongValue.Expose() != "" {
		t.Fatalf("LoadContext(wrong key) returned secret data: ok=%v value=%v", wrongOK, wrongValue)
	}
	assertSecretErrorRedacted(t, err, plaintext, key, wrongKey, secondNonce, secondCiphertext)

	if err := store.DeleteContext(t.Context(), connectionID); err != nil {
		t.Fatalf("DeleteContext(): %v", err)
	}
	deletedValue, found, err := store.LoadContext(t.Context(), connectionID)
	if err != nil || found || deletedValue.Expose() != "" {
		t.Fatalf("LoadContext(after delete) = (%v, %v, %v)", deletedValue, found, err)
	}
}

func openSecretStoreSeekDBRuntime(t *testing.T) (*seekdb.Runtime, *sql.DB, seekdb.Config) {
	t.Helper()
	library := os.Getenv(seekdb.EnvLibrary)
	if library == "" {
		t.Skip(seekdb.EnvLibrary + " is not set")
	}
	config := seekdb.Config{
		LibraryPath:    library,
		DataDir:       filepath.Join(t.TempDir(), "seekdb-data"),
		Database:      seekdb.DefaultDatabase,
		ConnectLimit:  5 * time.Second,
		StartLimit:    90 * time.Second,
		QueryLimit:    15 * time.Second,
		ShutdownLimit: 20 * time.Second,
		MaxOpenConns:  4,
		MaxIdleConns:  2,
	}
	instance, err := seekdb.Open(t.Context(), config)
	if err != nil {
		t.Fatalf("open real SeekDB runtime: %v", err)
	}
	return instance, instance.SQL(), config
}

func reserveSecretStoreLoopbackAddress(t *testing.T) string {
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

func closeSecretStoreSeekDBRuntime(t *testing.T, instance *seekdb.Runtime, timeout time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := instance.Close(ctx); err != nil {
		t.Errorf("close real SeekDB runtime: %v", err)
	}
}

func readSeekDBEncryptedSecretRow(t *testing.T, database *sql.DB, namespace, name string) ([]byte, []byte) {
	t.Helper()
	var keyVersion int
	var nonce, ciphertext []byte
	var aad string
	err := database.QueryRowContext(t.Context(), `
SELECT key_version, nonce, ciphertext, aad
FROM secret_values
WHERE namespace = ? AND name = ?`, namespace, name).Scan(&keyVersion, &nonce, &ciphertext, &aad)
	if err != nil {
		t.Fatal(err)
	}
	if keyVersion != SecretKeyVersion || len(nonce) != 12 || aad != secretAAD(namespace, name, SecretKeyVersion) {
		t.Fatalf("encrypted row metadata = version %d nonce %d aad %q", keyVersion, len(nonce), aad)
	}
	return nonce, ciphertext
}

func readSeekDBSecretTimestamps(t *testing.T, database *sql.DB, namespace, name string) (int64, int64) {
	t.Helper()
	var createdAt, updatedAt int64
	if err := database.QueryRowContext(t.Context(), `
SELECT created_at_ms, updated_at_ms
FROM secret_values
WHERE namespace = ? AND name = ?`, namespace, name).Scan(&createdAt, &updatedAt); err != nil {
		t.Fatal(err)
	}
	return createdAt, updatedAt
}

func assertSecretValuesColumnContract(t *testing.T, database *sql.DB) {
	t.Helper()
	type column struct {
		name       string
		columnType string
		nullable   string
		collation  string
	}
	want := []column{
		{name: "namespace", columnType: "varchar(64)", nullable: "NO", collation: "ascii_bin"},
		{name: "name", columnType: "varchar(128)", nullable: "NO", collation: "ascii_bin"},
		{name: "key_version", columnType: "bigint unsigned", nullable: "NO"},
		{name: "nonce", columnType: "varbinary(12)", nullable: "NO"},
		{name: "ciphertext", columnType: "longblob", nullable: "NO"},
		{name: "aad", columnType: "varchar(512)", nullable: "NO", collation: "utf8mb4_bin"},
		{name: "created_at_ms", columnType: "bigint unsigned", nullable: "NO"},
		{name: "updated_at_ms", columnType: "bigint unsigned", nullable: "NO"},
	}
	rows, err := database.QueryContext(t.Context(), `
SELECT column_name, column_type, is_nullable, COALESCE(collation_name, '')
FROM information_schema.columns
WHERE table_schema = DATABASE() AND table_name = 'secret_values'
ORDER BY ordinal_position`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := make([]column, 0, len(want))
	for rows.Next() {
		var item column
		if err := rows.Scan(&item.name, &item.columnType, &item.nullable, &item.collation); err != nil {
			t.Fatal(err)
		}
		item.columnType = normalizeSecretColumnType(item.columnType)
		got = append(got, item)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("secret_values column contract = %#v, want %#v", got, want)
	}
}

func normalizeSecretColumnType(columnType string) string {
	// SeekDB 1.3 exposes MySQL's legacy integer display width through
	// information_schema even though it has no effect on storage semantics.
	if strings.HasPrefix(columnType, "bigint(") && strings.HasSuffix(columnType, ") unsigned") {
		return "bigint unsigned"
	}
	return columnType
}

func assertSecretErrorRedacted(t *testing.T, err error, plaintext string, materials ...[]byte) {
	t.Helper()
	message := err.Error()
	forbidden := []string{plaintext}
	for _, material := range materials {
		forbidden = append(forbidden,
			hex.EncodeToString(material),
			base64.StdEncoding.EncodeToString(material),
		)
	}
	for _, value := range forbidden {
		if value != "" && strings.Contains(message, value) {
			t.Fatalf("error leaked secret material: %q", message)
		}
	}
}

func integrationRepeatedBytes(value byte, count int) []byte {
	return bytes.Repeat([]byte{value}, count)
}

func integrationAscendingBytes(count int) []byte {
	result := make([]byte, count)
	for index := range result {
		result[index] = byte(index + 1)
	}
	return result
}
