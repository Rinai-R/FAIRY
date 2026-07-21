package secret

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestDigestSurfaceKeyIsStableAndDomainSeparated(t *testing.T) {
	key := base64.StdEncoding.EncodeToString([]byte("abcdef0123456789abcdef0123456789"))
	ciphertext, err := CipherFromEnv(func(string) string { return key })
	if err != nil {
		t.Fatal(err)
	}
	first, err := ciphertext.DigestSurfaceKey("im_group", "onebot-group:123")
	if err != nil {
		t.Fatal(err)
	}
	second, err := ciphertext.DigestSurfaceKey("im_group", "onebot-group:123")
	if err != nil {
		t.Fatal(err)
	}
	if first != second || len(first) != 64 {
		t.Fatalf("digest = %q/%q", first, second)
	}
	otherSurface, err := ciphertext.DigestSurfaceKey("desktop", "onebot-group:123")
	if err != nil {
		t.Fatal(err)
	}
	if first == otherSurface {
		t.Fatal("surface domain must change digest")
	}
	if strings.Contains(first, "123") {
		t.Fatalf("digest exposes raw key: %q", first)
	}
}

func TestValidateSurfaceKey(t *testing.T) {
	for _, raw := range []string{"a", "onebot-group:123", strings.Repeat("a", 128)} {
		if err := ValidateSurfaceKey(raw); err != nil {
			t.Errorf("ValidateSurfaceKey(%q): %v", raw, err)
		}
	}
	for _, raw := range []string{"", " one", "one\n", "../secret", strings.Repeat("a", 129), "群"} {
		if err := ValidateSurfaceKey(raw); err == nil {
			t.Errorf("ValidateSurfaceKey(%q) unexpectedly succeeded", raw)
		}
	}
}
