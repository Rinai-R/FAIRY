package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
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
)

type Cipher struct {
	aead cipher.AEAD
	rand io.Reader
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
	return &Cipher{aead: aead, rand: random}, nil
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
