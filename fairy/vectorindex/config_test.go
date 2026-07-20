package vectorindex

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestConfigFromEnvParsesExactValuesAndRedactsURL(t *testing.T) {
	config, err := ConfigFromEnv(func(name string) string {
		switch name {
		case EnvURL:
			return "https://user:pass@example.test:6334"
		case EnvAPIKey:
			return "qdrant-secret"
		case EnvTimeout:
			return "2s"
		default:
			return ""
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if config.Timeout != 2*time.Second || config.CollectionName != CollectionName {
		t.Fatalf("config = %#v", config)
	}
	if redacted := config.RedactedURL(); strings.Contains(redacted, "pass") || strings.Contains(redacted, "qdrant-secret") {
		t.Fatalf("RedactedURL leaked secret: %q", redacted)
	}
	descriptor, err := config.Descriptor()
	if err != nil || descriptor.Scheme != "https" || descriptor.Host != "example.test:6334" || descriptor.Collection != CollectionName {
		t.Fatalf("Descriptor() = (%#v, %v)", descriptor, err)
	}
}

func TestConfigFromEnvRejectsMissingWhitespaceAndInvalidTimeout(t *testing.T) {
	if _, err := ConfigFromEnv(func(string) string { return "" }); !errors.Is(err, ErrURLRequired) {
		t.Fatalf("missing url error = %v", err)
	}
	if _, err := ConfigFromEnv(func(name string) string {
		if name == EnvURL {
			return " http://127.0.0.1:6334"
		}
		return ""
	}); !errors.Is(err, ErrWhitespace) {
		t.Fatalf("whitespace url error = %v", err)
	}
	if _, err := ConfigFromEnv(func(name string) string {
		switch name {
		case EnvURL:
			return "http://127.0.0.1:6334"
		case EnvAPIKey:
			return " api-key"
		}
		return ""
	}); !errors.Is(err, ErrWhitespace) {
		t.Fatalf("whitespace api key error = %v", err)
	}
	if _, err := ConfigFromEnv(func(name string) string {
		switch name {
		case EnvURL:
			return "http://127.0.0.1:6334"
		case EnvTimeout:
			return "0s"
		}
		return ""
	}); err == nil || !strings.Contains(err.Error(), "greater than zero") {
		t.Fatalf("invalid timeout error = %v", err)
	}
}

func TestSanitizeErrorRedactsURLAndAPIKey(t *testing.T) {
	config := Config{URL: "https://user:pass@example.test:6334", APIKey: "qdrant-secret"}
	err := sanitizeError("qdrant test", config, errors.New("dial https://user:pass@example.test:6334 with qdrant-secret failed"))
	message := err.Error()
	for _, forbidden := range []string{"user:pass", "qdrant-secret"} {
		if strings.Contains(message, forbidden) {
			t.Fatalf("sanitizeError leaked %q in %q", forbidden, message)
		}
	}
	if !strings.Contains(message, "[REDACTED]") {
		t.Fatalf("sanitizeError message = %q, want redaction marker", message)
	}
}
