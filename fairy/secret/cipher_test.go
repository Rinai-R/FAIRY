package secret

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
			_, err := CipherFromEnv(func(string) string { return tt.raw })
			if !errors.Is(err, tt.want) {
				t.Fatalf("CipherFromEnv() error = %v, want %v", err, tt.want)
			}
		})
	}
	if _, err := CipherFromEnv(func(name string) string {
		if name != EnvMasterKey {
			t.Fatalf("environment name = %q", name)
		}
		return valid
	}); err != nil {
		t.Fatalf("CipherFromEnv(valid) error = %v", err)
	}
}

func TestCipherRoundTripUsesAADAndRejectsWrongKey(t *testing.T) {
	first, err := newCipher(bytesOf(1, keyBytes), strings.NewReader(strings.Repeat("n", 24)))
	if err != nil {
		t.Fatal(err)
	}
	nonce, ciphertext, aad, err := first.Seal("model", "connection-1", []byte("sk-exact-secret"))
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := first.Open("model", "connection-1", KeyVersion, nonce, ciphertext, aad)
	if err != nil || string(plaintext) != "sk-exact-secret" {
		t.Fatalf("Open() = (%q, %v)", plaintext, err)
	}
	if _, err := first.Open("speech", "connection-1", KeyVersion, nonce, ciphertext, aad); !errors.Is(err, ErrDecryptFailed) {
		t.Fatalf("Open(wrong AAD) error = %v", err)
	}
	wrong, err := newCipher(bytesOf(2, keyBytes), strings.NewReader(strings.Repeat("x", 12)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wrong.Open("model", "connection-1", KeyVersion, nonce, ciphertext, aad); !errors.Is(err, ErrDecryptFailed) {
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
