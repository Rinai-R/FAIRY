package plugin

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestNewStoreRejectsMissingDatabaseAndZeroLimit(t *testing.T) {
	if _, err := NewStore(nil, time.Second); !errors.Is(err, ErrStoreRequired) {
		t.Fatalf("NewStore(nil) = %v", err)
	}
	if _, err := NewStore(&sql.DB{}, 0); !errors.Is(err, ErrQueryLimitInvalid) {
		t.Fatalf("NewStore(zero limit) = %v", err)
	}
}

func TestValidateInstanceRejectsSecretfulConfigAndInvalidLifecycle(t *testing.T) {
	record := InstanceRecord{
		ID:             "echo-1",
		PluginID:       "fairy.plugin.example",
		PluginVersion:  "1.0.0",
		Enabled:        true,
		Lifecycle:      "ready",
		ConfigDocument: json.RawMessage(`{"api_key":"sk-live-secret"}`),
	}
	if err := validateInstance(record); !errors.Is(err, ErrConfigContainsSecret) {
		t.Fatalf("secretful config = %v", err)
	}
	if err := validateInstance(record); err != nil && strings.Contains(err.Error(), "sk-live-secret") {
		t.Fatalf("validation echoed secret: %v", err)
	}
	record.ConfigDocument = json.RawMessage(`{}`)
	record.Lifecycle = "disabled"
	if err := validateInstance(record); !errors.Is(err, ErrInstanceInvalid) {
		t.Fatalf("enabled+disabled = %v", err)
	}
}

func TestValidateStateKeyRejectsTraversalAndSQL(t *testing.T) {
	if err := validateStateKey("../etc/passwd"); !errors.Is(err, ErrStateKeyInvalid) {
		t.Fatalf("traversal = %v", err)
	}
	if err := validateStateKey("select * from conversations"); !errors.Is(err, ErrStateKeyInvalid) {
		t.Fatalf("sql key = %v", err)
	}
	if err := validateStateKey("cursor"); err != nil {
		t.Fatal(err)
	}
}

func TestKnownCapabilitiesDoNotIncludeSQL(t *testing.T) {
	for _, name := range KnownCapabilities() {
		if strings.Contains(strings.ToLower(name), "sql") {
			t.Fatalf("capability %q grants SQL", name)
		}
	}
}

func TestValidatePackageRequiresManifestAndDigest(t *testing.T) {
	record := PackageRecord{
		ID:      "fairy.plugin.example",
		Version: "1.0.0",
		Manifest: Manifest{
			SchemaVersion: 1, ID: "fairy.plugin.example", Version: "1.0.0",
			ABI: ABIRange{Min: 1, Max: 1}, Entry: EntryModule, Exports: RequiredExports(),
			ConfigSchemaVersion: 1, DataSchemaVersion: 1,
		},
	}
	if err := validatePackage(record); !errors.Is(err, ErrPackageInvalid) {
		t.Fatalf("missing digest/abi = %v", err)
	}
	record.ABIVersion = 1
	record.VerifiedAtUnixMS = 1
	record.ArtifactSHA256 = sha256.Sum256([]byte("module"))
	if err := validatePackage(record); err != nil {
		t.Fatal(err)
	}
}
