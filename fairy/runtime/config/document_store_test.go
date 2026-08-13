package config

import (
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestNewSeekDBDocumentStoreValidatesBoundary(t *testing.T) {
	if _, err := NewSeekDBDocumentStore(nil, time.Second); !errors.Is(err, ErrConfigDocumentStoreRequired) {
		t.Fatalf("NewSeekDBDocumentStore(nil) error = %v", err)
	}
	if _, err := NewSeekDBDocumentStore(new(sql.DB), 0); !errors.Is(err, ErrConfigQueryLimitInvalid) {
		t.Fatalf("NewSeekDBDocumentStore(invalid limit) error = %v", err)
	}
	store, err := NewSeekDBDocumentStore(new(sql.DB), time.Second)
	if err != nil || store == nil {
		t.Fatalf("NewSeekDBDocumentStore(valid) = (%#v, %v)", store, err)
	}
	if _, err := NewSeekDBProfileStore(nil); !errors.Is(err, ErrConfigDocumentStoreRequired) {
		t.Fatalf("NewSeekDBProfileStore(nil) error = %v", err)
	}
}

func TestConfigDocumentValidationMatchesSeekDBSchema(t *testing.T) {
	for _, value := range []string{"runtime", "user_profile", "model/connections:v1", strings.Repeat("a", 64)} {
		if !portableConfigKey(value, 128) {
			t.Errorf("portableConfigKey(%q) = false", value)
		}
	}
	for _, value := range []string{"", "has space", "配置", "line\nbreak", strings.Repeat("a", 129)} {
		if portableConfigKey(value, 128) {
			t.Errorf("portableConfigKey(%q) = true", value)
		}
	}
	for _, raw := range []string{`{}`, ` {"enabled":true} `, `{"nested":{"value":1}}`} {
		if !isJSONObject([]byte(raw)) {
			t.Errorf("isJSONObject(%q) = false", raw)
		}
	}
	for _, raw := range []string{"", "null", `[]`, `"text"`, `{"broken":`} {
		if isJSONObject([]byte(raw)) {
			t.Errorf("isJSONObject(%q) = true", raw)
		}
	}
}
