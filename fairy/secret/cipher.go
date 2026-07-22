package secret

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

	"fairy/interaction"
	"golang.org/x/crypto/hkdf"
)

const (
	EnvMasterKey = "FAIRY_SECRET_MASTER_KEY"
	KeyVersion   = 1
	keyBytes     = 32
)

var (
	ErrMasterKeyRequired  = errors.New("FAIRY_SECRET_MASTER_KEY is required")
	ErrMasterKeyInvalid   = errors.New("FAIRY_SECRET_MASTER_KEY must be an exact base64 encoding of 32 bytes")
	ErrCipherRequired     = errors.New("secret cipher is required")
	ErrDecryptFailed      = errors.New("secret ciphertext authentication failed")
	ErrEndpointKeyInvalid = errors.New("endpoint key must be 1-128 ASCII characters from [A-Za-z0-9._:-]")
)

type Cipher struct {
	aead             cipher.AEAD
	rand             io.Reader
	endpointHMACKey  []byte
	principalHMACKey []byte
}

func CipherFromEnv(getenv func(string) string) (*Cipher, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	raw := getenv(EnvMasterKey)
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
	secretCipher, err := newCipher(key, rand.Reader)
	clear(key)
	return secretCipher, err
}

func newCipher(key []byte, random io.Reader) (*Cipher, error) {
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
	return &Cipher{aead: aead, rand: random, endpointHMACKey: endpointHMACKey, principalHMACKey: principalHMACKey}, nil
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

func (c *Cipher) DigestEndpointKey(endpoint interaction.EndpointKind, rawKey string) (string, error) {
	if c == nil || len(c.endpointHMACKey) == 0 {
		return "", ErrCipherRequired
	}
	if err := interaction.ValidateEndpoint(endpoint); err != nil {
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

func (c *Cipher) DigestPrincipal(principal interaction.PrincipalRef) (string, error) {
	if c == nil || len(c.principalHMACKey) == 0 {
		return "", ErrCipherRequired
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

func (c *Cipher) Seal(namespace, name string, plaintext []byte) (nonce, ciphertext []byte, aad string, err error) {
	if c == nil || c.aead == nil {
		return nil, nil, "", ErrCipherRequired
	}
	aad = secretAAD(namespace, name, KeyVersion)
	nonce = make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(c.rand, nonce); err != nil {
		return nil, nil, "", errors.New("generating secret nonce failed")
	}
	ciphertext = c.aead.Seal(nil, nonce, plaintext, []byte(aad))
	return nonce, ciphertext, aad, nil
}

func (c *Cipher) Open(namespace, name string, keyVersion int, nonce, ciphertext []byte, storedAAD string) ([]byte, error) {
	if c == nil || c.aead == nil {
		return nil, ErrCipherRequired
	}
	if keyVersion != KeyVersion || len(nonce) != c.aead.NonceSize() {
		return nil, ErrDecryptFailed
	}
	wantAAD := secretAAD(namespace, name, keyVersion)
	if storedAAD != wantAAD {
		return nil, ErrDecryptFailed
	}
	plaintext, err := c.aead.Open(nil, nonce, ciphertext, []byte(wantAAD))
	if err != nil {
		return nil, ErrDecryptFailed
	}
	return plaintext, nil
}

func secretAAD(namespace, name string, keyVersion int) string {
	return fmt.Sprintf("fairy-secret:v%d:%s:%s", keyVersion, namespace, name)
}
