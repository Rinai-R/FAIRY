package runtime

import (
	"strings"
	"testing"
)

func TestRuntimeStateValidationRejectsInvalidLaneHashAndMetadata(t *testing.T) {
	validHash := strings.Repeat("a", 64)
	if err := validatePromptLane(PromptLaneRespond); err != nil {
		t.Fatalf("validatePromptLane(respond) error = %v", err)
	}
	if err := validatePromptLane("unknown"); err == nil {
		t.Fatal("validatePromptLane(unknown) error = nil")
	}
	if err := validateHash("request_shape_hash", validHash); err != nil {
		t.Fatalf("validateHash(valid) error = %v", err)
	}
	if err := validateHash("request_shape_hash", strings.Repeat("A", 64)); err == nil {
		t.Fatal("validateHash(uppercase) error = nil")
	}
	if _, err := normalizeRuntimeMetadataJSON(`{"usage":[],"api_key":"secret"}`); err == nil {
		t.Fatal("normalizeRuntimeMetadataJSON(secret key) error = nil")
	}
	if _, err := normalizeRuntimeMetadataJSON(`{"usage":[],"message":"Bearer redacted"}`); err == nil {
		t.Fatal("normalizeRuntimeMetadataJSON(secret text) error = nil")
	}
}

func TestRuntimeMetadataAllowsBoundedToolInspectionBeyondLegacyLimit(t *testing.T) {
	payload := `{"detail":{"version":"v1","mergedContext":{"knowledge":[{"statement":"` + strings.Repeat("知", 20_000) + `"}]}}}`
	if len(payload) <= 32*1024 {
		t.Fatalf("fixture is not larger than the legacy metadata limit: %d", len(payload))
	}
	if _, err := normalizeRuntimeMetadataJSON(payload); err != nil {
		t.Fatalf("normalizeRuntimeMetadataJSON(tool detail) error = %v", err)
	}
}
