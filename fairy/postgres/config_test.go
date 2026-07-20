package postgres

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestConfigFromEnvRequiresExactDatabaseURL(t *testing.T) {
	_, err := ConfigFromEnv(func(string) string { return "" })
	if !errors.Is(err, ErrDatabaseURLRequired) {
		t.Fatalf("err = %v", err)
	}
	_, err = ConfigFromEnv(func(key string) string {
		if key == EnvDatabaseURL {
			return " postgres://user:pass@localhost/db "
		}
		return ""
	})
	if !errors.Is(err, ErrWhitespace) {
		t.Fatalf("err = %v", err)
	}
}

func TestConfigFromEnvParsesDefaultsAndOverrides(t *testing.T) {
	config, err := ConfigFromEnv(func(key string) string {
		switch key {
		case EnvDatabaseURL:
			return "postgres://user:pass@localhost:5432/fairy?sslmode=disable"
		case EnvMaxConns:
			return "8"
		case EnvMinConns:
			return "1"
		case EnvConnectTimeout:
			return "2s"
		case EnvQueryTimeout:
			return "3s"
		default:
			return ""
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if config.MaxConns != 8 || config.MinConns != 1 || config.ConnectTimeout != 2*time.Second || config.QueryTimeout != 3*time.Second {
		t.Fatalf("config = %#v", config)
	}
	descriptor, err := config.Descriptor()
	if err != nil {
		t.Fatal(err)
	}
	if descriptor.Host != "localhost:5432" || descriptor.Database != "fairy" {
		t.Fatalf("descriptor = %#v", descriptor)
	}
	redacted := config.RedactedURL()
	if strings.Contains(redacted, "user") || strings.Contains(redacted, "pass") || !strings.Contains(redacted, "redacted") {
		t.Fatalf("redacted URL = %q", redacted)
	}
}

func TestConfigFromEnvRejectsInvalidPoolAndDurations(t *testing.T) {
	tests := []struct {
		name string
		key  string
		val  string
	}{
		{name: "max too high", key: EnvMaxConns, val: "201"},
		{name: "min greater than max", key: EnvMinConns, val: "21"},
		{name: "bad duration", key: EnvQueryTimeout, val: "soon"},
		{name: "zero duration", key: EnvConnectTimeout, val: "0s"},
		{name: "whitespace", key: EnvMaxConns, val: " 4"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ConfigFromEnv(func(key string) string {
				if key == EnvDatabaseURL {
					return "postgres://user:pass@localhost/fairy"
				}
				if key == tt.key {
					return tt.val
				}
				return ""
			})
			if err == nil {
				t.Fatal("expected error")
			}
		})
	}
}
