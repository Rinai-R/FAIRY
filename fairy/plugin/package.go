package plugin

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
)

var (
	ErrPackageArchiveInvalid = errors.New("plugin package archive is invalid")
	ErrPackageChecksum       = errors.New("plugin package checksum is invalid")
)

const (
	PackageManifestName  = "manifest.json"
	PackageModuleName    = EntryModule
	PackageChecksumsName = "checksums.json"
)

type Bundle struct {
	Manifest Manifest
	Module   []byte
	SHA256   [sha256.Size]byte
}

type packageChecksums struct {
	Module string `json:"module.wasm"`
}

func OpenBundle(r io.ReaderAt, size int64) (Bundle, error) {
	if r == nil || size <= 0 {
		return Bundle{}, ErrPackageArchiveInvalid
	}
	reader, err := zip.NewReader(r, size)
	if err != nil {
		return Bundle{}, fmt.Errorf("%w: %v", ErrPackageArchiveInvalid, err)
	}
	files := map[string]*zip.File{}
	for _, file := range reader.File {
		name := path.Clean(strings.ReplaceAll(file.Name, `\`, "/"))
		if name == "." || strings.HasPrefix(name, "../") || path.IsAbs(name) {
			return Bundle{}, fmt.Errorf("%w: archive entry escapes package root", ErrPackageArchiveInvalid)
		}
		if strings.HasPrefix(name, "assets/") {
			continue
		}
		if _, exists := files[name]; exists {
			return Bundle{}, fmt.Errorf("%w: duplicated archive entry %s", ErrPackageArchiveInvalid, name)
		}
		files[name] = file
	}
	for _, required := range []string{PackageManifestName, PackageModuleName, PackageChecksumsName} {
		if files[required] == nil {
			return Bundle{}, fmt.Errorf("%w: missing %s", ErrPackageArchiveInvalid, required)
		}
	}
	if len(files) != 3 {
		return Bundle{}, fmt.Errorf("%w: archive contains undeclared root files", ErrPackageArchiveInvalid)
	}

	manifestRaw, err := readZipFile(files[PackageManifestName])
	if err != nil {
		return Bundle{}, err
	}
	manifest, err := ParseManifest(bytes.NewReader(manifestRaw))
	if err != nil {
		return Bundle{}, err
	}
	if err := CheckCompatibility(ABIVersion, manifest); err != nil {
		return Bundle{}, err
	}

	module, err := readZipFile(files[PackageModuleName])
	if err != nil {
		return Bundle{}, err
	}
	digest := sha256.Sum256(module)
	checksumRaw, err := readZipFile(files[PackageChecksumsName])
	if err != nil {
		return Bundle{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(checksumRaw))
	decoder.DisallowUnknownFields()
	var checksums packageChecksums
	if err := decoder.Decode(&checksums); err != nil {
		return Bundle{}, fmt.Errorf("%w: checksums.json is invalid", ErrPackageArchiveInvalid)
	}
	want, err := hex.DecodeString(checksums.Module)
	if err != nil || len(want) != sha256.Size {
		return Bundle{}, ErrPackageChecksum
	}
	if !bytes.Equal(digest[:], want) {
		return Bundle{}, ErrPackageChecksum
	}
	return Bundle{Manifest: manifest, Module: module, SHA256: digest}, nil
}

func readZipFile(file *zip.File) ([]byte, error) {
	if file.UncompressedSize64 > 16<<20 {
		return nil, fmt.Errorf("%w: %s exceeds size budget", ErrPackageArchiveInvalid, file.Name)
	}
	rc, err := file.Open()
	if err != nil {
		return nil, fmt.Errorf("%w: open %s: %v", ErrPackageArchiveInvalid, file.Name, err)
	}
	defer rc.Close()
	raw, err := io.ReadAll(io.LimitReader(rc, int64(file.UncompressedSize64)+1))
	if err != nil {
		return nil, fmt.Errorf("%w: read %s: %v", ErrPackageArchiveInvalid, file.Name, err)
	}
	if uint64(len(raw)) != file.UncompressedSize64 {
		return nil, fmt.Errorf("%w: %s size mismatch", ErrPackageArchiveInvalid, file.Name)
	}
	return raw, nil
}
