package wasm

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

const artifactCatalogSchemaVersion = 1

var (
	ErrArtifactCatalogInvalid = errors.New("wazero artifact catalog is invalid")

	//go:embed artifacts.json
	builtinArtifactCatalog []byte
)

type ArtifactCatalog struct {
	SchemaVersion int    `json:"schemaVersion"`
	Product       string `json:"product"`
	Module        string `json:"module"`
	Version       string `json:"version"`
	SourceURL     string `json:"sourceURL"`
	ReleaseURL    string `json:"releaseURL"`
	License       string `json:"license"`
	LicenseURL    string `json:"licenseURL"`
	LicenseSHA256 string `json:"licenseSHA256"`
	GoSumZipHash  string `json:"goSumZipHash"`
}

func BuiltinArtifactCatalog() (ArtifactCatalog, error) {
	return ParseArtifactCatalog(strings.NewReader(string(builtinArtifactCatalog)))
}

func ParseArtifactCatalog(r io.Reader) (ArtifactCatalog, error) {
	decoder := json.NewDecoder(r)
	decoder.DisallowUnknownFields()
	var catalog ArtifactCatalog
	if err := decoder.Decode(&catalog); err != nil {
		return ArtifactCatalog{}, fmt.Errorf("%w: %v", ErrArtifactCatalogInvalid, err)
	}
	if catalog.SchemaVersion != artifactCatalogSchemaVersion {
		return ArtifactCatalog{}, fmt.Errorf("%w: schemaVersion = %d, want %d", ErrArtifactCatalogInvalid, catalog.SchemaVersion, artifactCatalogSchemaVersion)
	}
	if catalog.Product != "wazero" {
		return ArtifactCatalog{}, fmt.Errorf("%w: product = %q, want wazero", ErrArtifactCatalogInvalid, catalog.Product)
	}
	if catalog.Module != "github.com/tetratelabs/wazero" {
		return ArtifactCatalog{}, fmt.Errorf("%w: module = %q", ErrArtifactCatalogInvalid, catalog.Module)
	}
	if !strings.HasPrefix(catalog.Version, "v1.") {
		return ArtifactCatalog{}, fmt.Errorf("%w: version %q is not wazero v1.x", ErrArtifactCatalogInvalid, catalog.Version)
	}
	if catalog.SourceURL != "https://github.com/tetratelabs/wazero" {
		return ArtifactCatalog{}, fmt.Errorf("%w: sourceURL must be the official tetratelabs/wazero repository", ErrArtifactCatalogInvalid)
	}
	if catalog.ReleaseURL != "https://github.com/tetratelabs/wazero/releases/tag/"+catalog.Version {
		return ArtifactCatalog{}, fmt.Errorf("%w: releaseURL must match the pinned version tag", ErrArtifactCatalogInvalid)
	}
	if catalog.License != "Apache-2.0" {
		return ArtifactCatalog{}, fmt.Errorf("%w: license = %q, want Apache-2.0", ErrArtifactCatalogInvalid, catalog.License)
	}
	if catalog.LicenseURL != "https://raw.githubusercontent.com/tetratelabs/wazero/"+catalog.Version+"/LICENSE" {
		return ArtifactCatalog{}, fmt.Errorf("%w: licenseURL must match the pinned version", ErrArtifactCatalogInvalid)
	}
	if err := requireSHA256(catalog.LicenseSHA256); err != nil {
		return ArtifactCatalog{}, fmt.Errorf("%w: licenseSHA256 %v", ErrArtifactCatalogInvalid, err)
	}
	if err := requireGoSumHash(catalog.GoSumZipHash); err != nil {
		return ArtifactCatalog{}, fmt.Errorf("%w: goSumZipHash %v", ErrArtifactCatalogInvalid, err)
	}
	return catalog, nil
}

func requireSHA256(value string) error {
	if len(value) != 64 {
		return errors.New("must be 64 lowercase hex characters")
	}
	for _, r := range value {
		digit := r >= '0' && r <= '9'
		hex := r >= 'a' && r <= 'f'
		if !digit && !hex {
			return errors.New("must be 64 lowercase hex characters")
		}
	}
	return nil
}

func requireGoSumHash(value string) error {
	if !strings.HasPrefix(value, "h1:") || len(value) < 8 {
		return errors.New("must be a Go module zip h1 hash")
	}
	return nil
}
