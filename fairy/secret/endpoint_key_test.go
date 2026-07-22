package secret

import (
	"encoding/base64"
	"strings"
	"testing"

	"fairy/interaction"
)

func TestDigestEndpointKeyIsStableAndDomainSeparated(t *testing.T) {
	key := base64.StdEncoding.EncodeToString([]byte("abcdef0123456789abcdef0123456789"))
	ciphertext, err := CipherFromEnv(func(string) string { return key })
	if err != nil {
		t.Fatal(err)
	}
	first, err := ciphertext.DigestEndpointKey(interaction.EndpointIM, "onebot-group:123")
	if err != nil {
		t.Fatal(err)
	}
	second, err := ciphertext.DigestEndpointKey(interaction.EndpointIM, "onebot-group:123")
	if err != nil {
		t.Fatal(err)
	}
	otherEndpoint, err := ciphertext.DigestEndpointKey(interaction.EndpointDesktop, "onebot-group:123")
	if err != nil {
		t.Fatal(err)
	}
	if first != second || len(first) != 64 || first == otherEndpoint || strings.Contains(first, "123") {
		t.Fatalf("endpoint digests = %q/%q/%q", first, second, otherEndpoint)
	}
}

func TestValidateEndpointKey(t *testing.T) {
	for _, raw := range []string{"a", "onebot-group:123", strings.Repeat("a", 128)} {
		if err := ValidateEndpointKey(raw); err != nil {
			t.Errorf("ValidateEndpointKey(%q): %v", raw, err)
		}
	}
	for _, raw := range []string{"", " one", "one\n", "../secret", strings.Repeat("a", 129), "群"} {
		if err := ValidateEndpointKey(raw); err == nil {
			t.Errorf("ValidateEndpointKey(%q) unexpectedly succeeded", raw)
		}
	}
}
