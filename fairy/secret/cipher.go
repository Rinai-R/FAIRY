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
	"golang.org/x/crypto/hkdf"
	"io"
	"os"
	"strings"
)

const (
	EnvMasterKey = "FAIRY_SECRET_MASTER_KEY"
	KeyVersion   = 1
	keyBytes     = 32
)

var (
	ErrMasterKeyRequired = errors.New("FAIRY_SECRET_MASTER_KEY is required")
	ErrMasterKeyInvalid  = errors.New("FAIRY_SECRET_MASTER_KEY must be an exact base64 encoding of 32 bytes")
	ErrCipherRequired    = errors.New("secret cipher is required")
	ErrDecryptFailed     = errors.New("secret ciphertext authentication failed")
	ErrSurfaceKeyInvalid = errors.New("surface key must be 1-128 ASCII characters from [A-Za-z0-9._:-]")
)

type Cipher struct {
	aead           cipher.AEAD
	rand           io.Reader
	surfaceHMACKey []byte
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
	hmacKey := make([]byte, 32)
	reader := hkdf.New(sha256.New, key, nil, []byte("FAIRY surface binding v1"))
	if _, err := io.ReadFull(reader, hmacKey); err != nil {
		clear(hmacKey)
		return nil, errors.New("deriving surface binding key failed")
	}
	return &Cipher{aead: aead, rand: random, surfaceHMACKey: hmacKey}, nil
}

// DigestSurfaceKey returns a stable, domain-separated HMAC digest for a
// validated external Surface key. The raw key is never returned or persisted.
func (c *Cipher) DigestSurfaceKey(surface, rawKey string) (string, error) {
	if c == nil || len(c.surfaceHMACKey) == 0 {
		return "", ErrCipherRequired
	}
	if strings.TrimSpace(surface) == "" || strings.ContainsAny(surface, "\x00\n\r\t") {
		return "", ErrSurfaceKeyInvalid
	}
	if err := ValidateSurfaceKey(rawKey); err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, c.surfaceHMACKey)
	_, _ = mac.Write([]byte(surface))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(rawKey))
	return hex.EncodeToString(mac.Sum(nil)), nil
}

// ValidateSurfaceKey accepts only stable opaque ASCII identifiers supplied by
// a Surface adapter. Restricting the alphabet keeps the transport contract
// deterministic and prevents control/path characters at the boundary.
func ValidateSurfaceKey(rawKey string) error {
	if rawKey == "" || strings.TrimSpace(rawKey) != rawKey || len(rawKey) > 128 {
		return ErrSurfaceKeyInvalid
	}
	for _, r := range rawKey {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune("._:-", r) {
			continue
		}
		return ErrSurfaceKeyInvalid
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
