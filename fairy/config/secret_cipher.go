package config

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"fairy/session"

	"golang.org/x/crypto/hkdf"
)

const (
	SecretEnvMasterKey = "FAIRY_SECRET_MASTER_KEY"
	SecretKeyVersion   = 1
	keyBytes           = 32
)

var (
	ErrMasterKeyRequired    = errors.New("FAIRY_SECRET_MASTER_KEY is required")
	ErrMasterKeyInvalid     = errors.New("FAIRY_SECRET_MASTER_KEY must be an exact base64 encoding of 32 bytes")
	ErrSecretCipherRequired = errors.New("secret cipher is required")
	ErrSecretDecryptFailed  = errors.New("secret ciphertext authentication failed")
	ErrEndpointKeyInvalid   = errors.New("endpoint key must be 1-128 ASCII characters from [A-Za-z0-9._:-]")
)

type SecretCipher struct {
	aead             cipher.AEAD
	rand             io.Reader
	endpointHMACKey  []byte
	principalHMACKey []byte
}

func SecretCipherFromEnv(getenv func(string) string) (*SecretCipher, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	raw := getenv(SecretEnvMasterKey)
	if raw == "" {
		return nil, ErrMasterKeyRequired
	}
	if strings.TrimSpace(raw) != raw {
		return nil, ErrMasterKeyInvalid
	}
	key, err := base64.StdEncoding.Strict().DecodeString(raw)
	if err != nil || len(key) != keyBytes {
		clear(key)
		return nil, ErrMasterKeyInvalid
	}
	secretCipher, err := newSecretCipher(key, rand.Reader)
	clear(key)
	return secretCipher, err
}

func newSecretCipher(key []byte, random io.Reader) (*SecretCipher, error) {
	if len(key) != keyBytes {
		return nil, ErrMasterKeyInvalid
	}
	if random == nil {
		return nil, errors.New("secret cipher random source is required")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, errors.New("initializing secret cipher failed")
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, errors.New("initializing secret cipher mode failed")
	}
	endpointHMACKey, err := deriveHMACKey(key, "FAIRY endpoint binding v1")
	if err != nil {
		return nil, err
	}
	principalHMACKey, err := deriveHMACKey(key, "FAIRY principal identity v1")
	if err != nil {
		clear(endpointHMACKey)
		return nil, err
	}
	return &SecretCipher{aead: aead, rand: random, endpointHMACKey: endpointHMACKey, principalHMACKey: principalHMACKey}, nil
}

func deriveHMACKey(key []byte, info string) ([]byte, error) {
	derived := make([]byte, 32)
	reader := hkdf.New(sha256.New, key, nil, []byte(info))
	if _, err := io.ReadFull(reader, derived); err != nil {
		clear(derived)
		return nil, errors.New("deriving identity binding key failed")
	}
	return derived, nil
}

func (c *SecretCipher) DigestEndpointKey(endpoint session.EndpointKind, rawKey string) (string, error) {
	if c == nil || len(c.endpointHMACKey) == 0 {
		return "", ErrSecretCipherRequired
	}
	if err := session.ValidateEndpoint(endpoint); err != nil {
		return "", err
	}
	if err := ValidateEndpointKey(rawKey); err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, c.endpointHMACKey)
	_, _ = mac.Write([]byte(endpoint))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(rawKey))
	return hex.EncodeToString(mac.Sum(nil)), nil
}

func (c *SecretCipher) DigestPrincipal(principal session.PrincipalRef) (string, error) {
	if c == nil || len(c.principalHMACKey) == 0 {
		return "", ErrSecretCipherRequired
	}
	if err := principal.Validate(); err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, c.principalHMACKey)
	_, _ = mac.Write([]byte(principal.Namespace))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(principal.Subject))
	return hex.EncodeToString(mac.Sum(nil)), nil
}

func ValidateEndpointKey(rawKey string) error {
	if rawKey == "" || strings.TrimSpace(rawKey) != rawKey || len(rawKey) > 128 {
		return ErrEndpointKeyInvalid
	}
	for _, r := range rawKey {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune("._:-", r) {
			continue
		}
		return ErrEndpointKeyInvalid
	}
	return nil
}

func (c *SecretCipher) Seal(namespace, name string, plaintext []byte) (nonce, ciphertext []byte, aad string, err error) {
	if c == nil || c.aead == nil {
		return nil, nil, "", ErrSecretCipherRequired
	}
	aad = secretAAD(namespace, name, SecretKeyVersion)
	nonce = make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(c.rand, nonce); err != nil {
		return nil, nil, "", errors.New("generating secret nonce failed")
	}
	ciphertext = c.aead.Seal(nil, nonce, plaintext, []byte(aad))
	return nonce, ciphertext, aad, nil
}

func (c *SecretCipher) Open(namespace, name string, keyVersion int, nonce, ciphertext []byte, storedAAD string) ([]byte, error) {
	if c == nil || c.aead == nil {
		return nil, ErrSecretCipherRequired
	}
	if keyVersion != SecretKeyVersion || len(nonce) != c.aead.NonceSize() {
		return nil, ErrSecretDecryptFailed
	}
	wantAAD := secretAAD(namespace, name, keyVersion)
	if storedAAD != wantAAD {
		return nil, ErrSecretDecryptFailed
	}
	plaintext, err := c.aead.Open(nil, nonce, ciphertext, []byte(wantAAD))
	if err != nil {
		return nil, ErrSecretDecryptFailed
	}
	return plaintext, nil
}

func secretAAD(namespace, name string, keyVersion int) string {
	return fmt.Sprintf("fairy-secret:v%d:%s:%s", keyVersion, namespace, name)
}
