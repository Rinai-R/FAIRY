package plugin

import (
	"encoding/json"
	"fmt"
	"io"
	"path"
	"strings"
)

type ABIRange struct {
	Min uint32 `json:"min"`
	Max uint32 `json:"max"`
}

type Manifest struct {
	SchemaVersion       uint32   `json:"schemaVersion"`
	ID                  string   `json:"id"`
	Version             string   `json:"version"`
	ABI                 ABIRange `json:"abi"`
	Entry               string   `json:"entry"`
	Exports             []string `json:"exports"`
	Capabilities        []string `json:"capabilities"`
	ConfigSchemaVersion uint32   `json:"configSchemaVersion"`
	DataSchemaVersion   uint32   `json:"dataSchemaVersion"`
}

func ParseManifest(r io.Reader) (Manifest, error) {
	decoder := json.NewDecoder(r)
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, coded(CodeManifestInvalid, fmt.Sprintf("parsing plugin manifest: %v", err))
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return Manifest{}, coded(CodeManifestInvalid, "plugin manifest must contain a single JSON value")
	}
	if err := validateManifest(manifest); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func CheckCompatibility(hostABI uint32, manifest Manifest) error {
	if err := validateManifest(manifest); err != nil {
		return err
	}
	if hostABI < manifest.ABI.Min || hostABI > manifest.ABI.Max {
		return coded(CodeABIIncompatible, fmt.Sprintf("host ABI %d is outside plugin range %d-%d", hostABI, manifest.ABI.Min, manifest.ABI.Max))
	}
	return nil
}

func validateManifest(manifest Manifest) error {
	if manifest.SchemaVersion != ManifestSchema {
		return coded(CodeManifestInvalid, fmt.Sprintf("schemaVersion = %d, want %d", manifest.SchemaVersion, ManifestSchema))
	}
	if err := validatePluginID(manifest.ID); err != nil {
		return err
	}
	if err := validateSemver(manifest.Version); err != nil {
		return err
	}
	if manifest.ABI.Min < 1 || manifest.ABI.Max < manifest.ABI.Min {
		return coded(CodeManifestInvalid, fmt.Sprintf("abi range %d-%d is invalid", manifest.ABI.Min, manifest.ABI.Max))
	}
	if manifest.Entry != EntryModule {
		return coded(CodeManifestInvalid, "entry must be module.wasm")
	}
	if path.Clean(manifest.Entry) != manifest.Entry || strings.Contains(manifest.Entry, `\`) {
		return coded(CodeManifestInvalid, "entry must be a clean relative path")
	}
	if err := validateExports(manifest.Exports); err != nil {
		return err
	}
	if err := validateCapabilities(manifest.Capabilities); err != nil {
		return err
	}
	if manifest.ConfigSchemaVersion < 1 || manifest.DataSchemaVersion < 1 {
		return coded(CodeManifestInvalid, "config and data schema versions must be at least 1")
	}
	return nil
}

func validatePluginID(id string) error {
	if id == "" || len(id) > 128 {
		return coded(CodeManifestInvalid, "plugin id must be 1-128 characters")
	}
	if id[0] == '.' || id[0] == '-' || id[len(id)-1] == '.' || id[len(id)-1] == '-' {
		return coded(CodeManifestInvalid, "plugin id must start and end with an alphanumeric character")
	}
	for _, r := range id {
		letter := r >= 'a' && r <= 'z'
		digit := r >= '0' && r <= '9'
		if !letter && !digit && r != '.' && r != '-' {
			return coded(CodeManifestInvalid, "plugin id must be lowercase alphanumeric with '.' or '-'")
		}
	}
	return nil
}

func validateSemver(version string) error {
	parts := strings.Split(version, ".")
	if len(parts) != 3 {
		return coded(CodeManifestInvalid, "plugin version must be MAJOR.MINOR.PATCH")
	}
	for _, part := range parts {
		if part == "" || (len(part) > 1 && part[0] == '0') {
			return coded(CodeManifestInvalid, "plugin version must be MAJOR.MINOR.PATCH")
		}
		for _, r := range part {
			if r < '0' || r > '9' {
				return coded(CodeManifestInvalid, "plugin version must be MAJOR.MINOR.PATCH")
			}
		}
	}
	return nil
}

func validateExports(exports []string) error {
	required := RequiredExports()
	if len(exports) != len(required) {
		return coded(CodeManifestInvalid, "plugin must export fairy_alloc, fairy_free, fairy_init, fairy_handle, and fairy_shutdown")
	}
	for i, name := range required {
		if exports[i] != name {
			return coded(CodeManifestInvalid, "plugin exports must be fairy_alloc, fairy_free, fairy_init, fairy_handle, and fairy_shutdown in order")
		}
	}
	return nil
}

func validateCapabilities(capabilities []string) error {
	known := map[string]struct{}{}
	for _, name := range KnownCapabilities() {
		known[name] = struct{}{}
	}
	seen := map[string]struct{}{}
	for _, name := range capabilities {
		if _, ok := known[name]; !ok {
			return coded(CodeManifestInvalid, fmt.Sprintf("unknown capability %q", name))
		}
		if _, dup := seen[name]; dup {
			return coded(CodeManifestInvalid, fmt.Sprintf("duplicated capability %q", name))
		}
		seen[name] = struct{}{}
	}
	return nil
}

func coded(code, message string) error {
	return &CodedError{Code: code, Message: message}
}
