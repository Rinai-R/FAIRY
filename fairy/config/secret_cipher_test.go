package config

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

func TestCipherFromEnvRequiresExactBase64Key(t *testing.T) {
	valid := base64.StdEncoding.EncodeToString(make([]byte, keyBytes))
	tests := []struct {
		name string
		raw  string
		want error
	}{
		{name: "missing", want: ErrMasterKeyRequired},
		{name: "whitespace", raw: " " + valid, want: ErrMasterKeyInvalid},
		{name: "short", raw: base64.StdEncoding.EncodeToString(make([]byte, keyBytes-1)), want: ErrMasterKeyInvalid},
		{name: "non canonical", raw: strings.TrimSuffix(valid, "="), want: ErrMasterKeyInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := SecretCipherFromEnv(func(string) string { return tt.raw })
			if !errors.Is(err, tt.want) {
				t.Fatalf("SecretCipherFromEnv() error = %v, want %v", err, tt.want)
			}
		})
	}
	if _, err := SecretCipherFromEnv(func(name string) string {
		if name != SecretEnvMasterKey {
			t.Fatalf("environment name = %q", name)
		}
		return valid
	}); err != nil {
		t.Fatalf("SecretCipherFromEnv(valid) error = %v", err)
	}
}

func TestCipherRoundTripUsesAADAndRejectsWrongKey(t *testing.T) {
	first, err := newSecretCipher(bytesOf(1, keyBytes), strings.NewReader(strings.Repeat("n", 24)))
	if err != nil {
		t.Fatal(err)
	}
	nonce, ciphertext, aad, err := first.Seal("model", "connection-1", []byte("sk-exact-secret"))
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := first.Open("model", "connection-1", SecretKeyVersion, nonce, ciphertext, aad)
	if err != nil || string(plaintext) != "sk-exact-secret" {
		t.Fatalf("Open() = (%q, %v)", plaintext, err)
	}
	if _, err := first.Open("semantic", "connection-1", SecretKeyVersion, nonce, ciphertext, aad); !errors.Is(err, ErrSecretDecryptFailed) {
		t.Fatalf("Open(wrong AAD) error = %v", err)
	}
	wrong, err := newSecretCipher(bytesOf(2, keyBytes), strings.NewReader(strings.Repeat("x", 12)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wrong.Open("model", "connection-1", SecretKeyVersion, nonce, ciphertext, aad); !errors.Is(err, ErrSecretDecryptFailed) {
		t.Fatalf("Open(wrong key) error = %v", err)
	}
}

func bytesOf(value byte, count int) []byte {
	out := make([]byte, count)
	for index := range out {
		out[index] = value
	}
	return out
}
