package plugin

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"
)

func TestOpenBundleAcceptsChecksummedABIPackage(t *testing.T) {
	module := []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}
	raw := mustPackageZip(t, validManifest(), module, sha256.Sum256(module))
	bundle, err := OpenBundle(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Manifest.ID != "fairy.plugin.example" || !bytes.Equal(bundle.Module, module) {
		t.Fatalf("bundle = %#v", bundle.Manifest)
	}
}

func TestOpenBundleRejectsTraversalMismatchedChecksumAndIncompatibleABI(t *testing.T) {
	module := []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}
	digest := sha256.Sum256(module)
	t.Run("checksum", func(t *testing.T) {
		wrong := digest
		wrong[0] ^= 0xff
		raw := mustPackageZip(t, validManifest(), module, wrong)
		if _, err := OpenBundle(bytes.NewReader(raw), int64(len(raw))); !errors.Is(err, ErrPackageChecksum) {
			t.Fatalf("OpenBundle() = %v, want checksum error", err)
		}
	})
	t.Run("abi", func(t *testing.T) {
		manifest := validManifest()
		manifest.ABI = ABIRange{Min: 2, Max: 2}
		raw := mustPackageZip(t, manifest, module, digest)
		if _, err := OpenBundle(bytes.NewReader(raw), int64(len(raw))); !errors.Is(err, ErrABIIncompatible) {
			t.Fatalf("OpenBundle() = %v, want ABI error", err)
		}
	})
	t.Run("traversal", func(t *testing.T) {
		var buffer bytes.Buffer
		writer := zip.NewWriter(&buffer)
		if _, err := writer.Create("../manifest.json"); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		raw := buffer.Bytes()
		if _, err := OpenBundle(bytes.NewReader(raw), int64(len(raw))); !errors.Is(err, ErrPackageArchiveInvalid) {
			t.Fatalf("OpenBundle(traversal) = %v", err)
		}
	})
}

func mustPackageZip(t *testing.T, manifest Manifest, module []byte, digest [sha256.Size]byte) []byte {
	t.Helper()
	manifestRaw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	checksums, err := json.Marshal(packageChecksums{Module: hex.EncodeToString(digest[:])})
	if err != nil {
		t.Fatal(err)
	}
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for name, body := range map[string][]byte{
		PackageManifestName:  manifestRaw,
		PackageModuleName:    module,
		PackageChecksumsName: checksums,
	} {
		file, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}
