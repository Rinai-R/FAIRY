package plugin

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func Pack(w io.Writer, manifest Manifest, module []byte) error {
	if w == nil {
		return fmt.Errorf("%w: pack writer is required", ErrPackageArchiveInvalid)
	}
	if err := CheckCompatibility(ABIVersion, manifest); err != nil {
		return err
	}
	if len(module) == 0 {
		return fmt.Errorf("%w: module.wasm is required", ErrPackageArchiveInvalid)
	}
	manifestRaw, err := EncodeManifest(manifest)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(module)
	checksums, err := json.Marshal(packageChecksums{Module: hex.EncodeToString(digest[:])})
	if err != nil {
		return fmt.Errorf("encoding plugin checksums: %w", err)
	}
	archive := zip.NewWriter(w)
	if err := writeZipFile(archive, PackageManifestName, manifestRaw); err != nil {
		_ = archive.Close()
		return err
	}
	if err := writeZipFile(archive, PackageModuleName, module); err != nil {
		_ = archive.Close()
		return err
	}
	if err := writeZipFile(archive, PackageChecksumsName, checksums); err != nil {
		_ = archive.Close()
		return err
	}
	if err := archive.Close(); err != nil {
		return fmt.Errorf("%w: %v", ErrPackageArchiveInvalid, err)
	}
	return nil
}

func writeZipFile(archive *zip.Writer, name string, body []byte) error {
	entry, err := archive.Create(name)
	if err != nil {
		return fmt.Errorf("%w: create %s: %v", ErrPackageArchiveInvalid, name, err)
	}
	if _, err := entry.Write(body); err != nil {
		return fmt.Errorf("%w: write %s: %v", ErrPackageArchiveInvalid, name, err)
	}
	return nil
}

func OpenBundleFile(path string) (Bundle, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Bundle{}, fmt.Errorf("read plugin package %s: %w", path, err)
	}
	return OpenBundle(bytes.NewReader(raw), int64(len(raw)))
}

func OpenBundleDir(dir string) (Bundle, error) {
	manifestRaw, err := os.ReadFile(filepath.Join(dir, PackageManifestName))
	if err != nil {
		return Bundle{}, fmt.Errorf("read plugin manifest: %w", err)
	}
	manifest, err := ParseManifest(bytes.NewReader(manifestRaw))
	if err != nil {
		return Bundle{}, err
	}
	module, err := os.ReadFile(filepath.Join(dir, PackageModuleName))
	if err != nil {
		return Bundle{}, fmt.Errorf("read plugin module: %w", err)
	}
	var packed bytes.Buffer
	if err := Pack(&packed, manifest, module); err != nil {
		return Bundle{}, err
	}
	return OpenBundle(bytes.NewReader(packed.Bytes()), int64(packed.Len()))
}

func ValidatePath(path string) (Bundle, error) {
	info, err := os.Stat(path)
	if err != nil {
		return Bundle{}, fmt.Errorf("stat plugin path: %w", err)
	}
	if info.IsDir() {
		return OpenBundleDir(path)
	}
	return OpenBundleFile(path)
}

func PackDir(dir, output string) (Bundle, error) {
	bundle, err := OpenBundleDir(dir)
	if err != nil {
		return Bundle{}, err
	}
	file, err := os.Create(output)
	if err != nil {
		return Bundle{}, fmt.Errorf("create plugin package: %w", err)
	}
	defer file.Close()
	if err := Pack(file, bundle.Manifest, bundle.Module); err != nil {
		return Bundle{}, err
	}
	return bundle, nil
}
