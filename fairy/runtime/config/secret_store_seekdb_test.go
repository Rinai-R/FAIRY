package config

import (
	"bytes"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestNewSeekDBSecretStoreValidatesDependencies(t *testing.T) {
	cipher, err := newSecretCipher(bytesOf(1, keyBytes), bytes.NewReader(bytesOf(2, 24)))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name       string
		database   *sql.DB
		cipher     *SecretCipher
		queryLimit time.Duration
		want       error
	}{
		{name: "missing database", cipher: cipher, queryLimit: time.Second, want: ErrSecretSeekDBRequired},
		{name: "missing cipher", database: new(sql.DB), queryLimit: time.Second, want: ErrSecretCipherRequired},
		{name: "invalid query limit", database: new(sql.DB), cipher: cipher, want: ErrSecretQueryLimitInvalid},
		{name: "valid", database: new(sql.DB), cipher: cipher, queryLimit: time.Second},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, err := NewSeekDBSecretStore(test.database, test.cipher, test.queryLimit)
			if !errors.Is(err, test.want) {
				t.Fatalf("NewSeekDBSecretStore() error = %v, want %v", err, test.want)
			}
			if test.want == nil && (store == nil || !store.Encrypted()) {
				t.Fatalf("NewSeekDBSecretStore() = %#v", store)
			}
		})
	}
}

func TestSecretStoreConnectionIDMatchesSeekDBKeyContract(t *testing.T) {
	valid := []string{
		"6a129284-6358-47b0-ad64-2a5907d36c91",
		"semantic_embedding.primary:v1",
		strings.Repeat("a", 128),
	}
	for _, connectionID := range valid {
		if err := validateConnectionID(connectionID); err != nil {
			t.Errorf("validateConnectionID(%q) error = %v", connectionID, err)
		}
	}

	invalid := []struct {
		name         string
		connectionID string
		want         error
	}{
		{name: "empty", want: ErrConnectionIDRequired},
		{name: "too long", connectionID: strings.Repeat("a", 129), want: ErrInvalidConnectionID},
		{name: "unicode", connectionID: "模型连接", want: ErrInvalidConnectionID},
		{name: "space", connectionID: "model primary", want: ErrInvalidConnectionID},
		{name: "control", connectionID: "model\nprimary", want: ErrInvalidConnectionID},
		{name: "slash", connectionID: "model/primary", want: ErrInvalidConnectionID},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			if err := validateConnectionID(test.connectionID); !errors.Is(err, test.want) {
				t.Fatalf("validateConnectionID(%q) error = %v, want %v", test.connectionID, err, test.want)
			}
		})
	}
}
