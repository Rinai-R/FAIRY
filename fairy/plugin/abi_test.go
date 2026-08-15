package plugin

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

func validManifest() Manifest {
	return Manifest{
		SchemaVersion:       1,
		ID:                  "fairy.plugin.example",
		Version:             "1.0.0",
		ABI:                 ABIRange{Min: 1, Max: 1},
		Entry:               EntryModule,
		Exports:             RequiredExports(),
		Capabilities:        []string{"http.request"},
		ConfigSchemaVersion: 1,
		DataSchemaVersion:   1,
	}
}

func TestParseManifestAcceptsABIContract(t *testing.T) {
	raw, err := json.Marshal(validManifest())
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := ParseManifest(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckCompatibility(ABIVersion, manifest); err != nil {
		t.Fatal(err)
	}
}

func TestCheckCompatibilityRejectsHostOutsideRange(t *testing.T) {
	manifest := validManifest()
	manifest.ABI = ABIRange{Min: 2, Max: 2}
	err := CheckCompatibility(ABIVersion, manifest)
	if !errors.Is(err, ErrABIIncompatible) {
		t.Fatalf("CheckCompatibility() = %v, want %v", err, ErrABIIncompatible)
	}
	var coded *CodedError
	if !errors.As(err, &coded) || coded.Code != CodeABIIncompatible {
		t.Fatalf("error = %#v", err)
	}
	if strings.Contains(err.Error(), "apiKey") || strings.Contains(err.Error(), "Bearer ") {
		t.Fatalf("compatibility error leaked secret material: %v", err)
	}
}

func TestParseManifestRejectsUnknownFieldsAndCapabilities(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{name: "unknown field", raw: `{"schemaVersion":1,"id":"fairy.plugin.example","version":"1.0.0","abi":{"min":1,"max":1},"entry":"module.wasm","exports":["fairy_alloc","fairy_free","fairy_init","fairy_handle","fairy_shutdown"],"capabilities":[],"configSchemaVersion":1,"dataSchemaVersion":1,"apiKey":"sk-live"}`},
		{name: "unknown capability", raw: `{"schemaVersion":1,"id":"fairy.plugin.example","version":"1.0.0","abi":{"min":1,"max":1},"entry":"module.wasm","exports":["fairy_alloc","fairy_free","fairy_init","fairy_handle","fairy_shutdown"],"capabilities":["fs.read"],"configSchemaVersion":1,"dataSchemaVersion":1}`},
		{name: "missing export", raw: `{"schemaVersion":1,"id":"fairy.plugin.example","version":"1.0.0","abi":{"min":1,"max":1},"entry":"module.wasm","exports":["fairy_init"],"capabilities":[],"configSchemaVersion":1,"dataSchemaVersion":1}`},
		{name: "uppercase id", raw: `{"schemaVersion":1,"id":"Fairy.Plugin","version":"1.0.0","abi":{"min":1,"max":1},"entry":"module.wasm","exports":["fairy_alloc","fairy_free","fairy_init","fairy_handle","fairy_shutdown"],"capabilities":[],"configSchemaVersion":1,"dataSchemaVersion":1}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ParseManifest(strings.NewReader(test.raw))
			if !errors.Is(err, ErrManifestInvalid) {
				t.Fatalf("ParseManifest() = %v, want %v", err, ErrManifestInvalid)
			}
			if strings.Contains(err.Error(), "sk-live") {
				t.Fatalf("manifest error echoed credential: %v", err)
			}
		})
	}
}

func TestParseEnvelopeRoundTrip(t *testing.T) {
	envelope := Envelope{
		ABIVersion:  ABIVersion,
		Kind:        "handle",
		Correlation: Correlation{PluginInstanceID: "plugin-1", TraceID: "trace-1"},
		Payload:     json.RawMessage(`{"event":"message"}`),
	}
	raw, err := EncodeEnvelope(envelope)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseEnvelope(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Kind != "handle" || parsed.Correlation.PluginInstanceID != "plugin-1" || parsed.Correlation.TraceID != "trace-1" {
		t.Fatalf("parsed = %#v", parsed)
	}
}

func TestParseEnvelopeRejectsUnknownErrorCode(t *testing.T) {
	_, err := ParseEnvelope(strings.NewReader(`{"abiVersion":1,"kind":"error","correlation":{"pluginInstanceId":"plugin-1"},"error":{"code":"PANIC","message":"boom"}}`))
	if !errors.Is(err, ErrEnvelopeInvalid) {
		t.Fatalf("ParseEnvelope() = %v, want %v", err, ErrEnvelopeInvalid)
	}
}

func TestPublishedSchemasDescribeABIContract(t *testing.T) {
	manifestSchema, err := os.ReadFile("schema/manifest.v1.json")
	if err != nil {
		t.Fatal(err)
	}
	envelopeSchema, err := os.ReadFile("schema/envelope.v1.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{"schemaVersion", "id", "version", "abi", "entry", "exports", "capabilities", "configSchemaVersion", "dataSchemaVersion", "fairy_alloc", "fairy_free", "fairy_init", "fairy_handle", "fairy_shutdown", "http.request"} {
		if !bytes.Contains(manifestSchema, []byte(token)) {
			t.Fatalf("manifest schema missing %s", token)
		}
	}
	for _, token := range []string{"abiVersion", "kind", "correlation", "pluginInstanceId", "ABI_INCOMPATIBLE", "CAPABILITY_DENIED"} {
		if !bytes.Contains(envelopeSchema, []byte(token)) {
			t.Fatalf("envelope schema missing %s", token)
		}
	}
	if !json.Valid(manifestSchema) || !json.Valid(envelopeSchema) {
		t.Fatal("published schema is not valid JSON")
	}
}
