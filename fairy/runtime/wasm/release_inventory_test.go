package wasm

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"fairy/plugin"
)

func TestEmptyReleaseInventoryIsExplicitAndSealed(t *testing.T) {
	sourceRoot := t.TempDir()
	inventoryPath := filepath.Join(sourceRoot, "plugin-inventory.json")
	if err := os.WriteFile(inventoryPath, []byte("{\n  \"schemaVersion\": 1,\n  \"plugins\": []\n}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "plugin-release")
	if err := InstallReleaseInventory(t.Context(), inventoryPath, sourceRoot, destination); err != nil {
		t.Fatal(err)
	}
	if err := VerifyInstalledReleaseInventory(t.Context(), destination); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(destination)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Name() != ReleaseEvidenceFileName || entries[1].Name() != ReleaseInventoryFileName {
		t.Fatalf("empty release entries = %#v", entries)
	}
	if err := os.WriteFile(filepath.Join(destination, "manifest.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := VerifyInstalledReleaseInventory(t.Context(), destination); !errors.Is(err, ErrReleaseEvidenceInvalid) {
		t.Fatalf("VerifyInstalledReleaseInventory(undeclared) = %v", err)
	}
}

func TestReleaseInventoryVerifiesPackageLicenseABIAndShadowHealth(t *testing.T) {
	fixture := newReleaseFixture(t)
	if err := InstallReleaseInventory(t.Context(), fixture.inventoryPath, fixture.sourceRoot, fixture.destination); err != nil {
		t.Fatal(err)
	}
	if err := VerifyInstalledReleaseInventory(t.Context(), fixture.destination); err != nil {
		t.Fatal(err)
	}
	for _, relative := range []string{
		ReleaseInventoryFileName,
		ReleaseEvidenceFileName,
		installedPackagePath(fixture.entry),
		installedLicensePath(fixture.entry),
	} {
		info, err := os.Lstat(filepath.Join(fixture.destination, filepath.FromSlash(relative)))
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			t.Fatalf("installed %s = (%v, %v)", relative, info, err)
		}
	}

	evidencePath := filepath.Join(fixture.destination, ReleaseEvidenceFileName)
	evidence, err := os.ReadFile(evidencePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(evidence, []byte(`"shadowHealth": "passed"`)) {
		t.Fatalf("evidence does not record executable installation: %s", evidence)
	}
	if err := os.WriteFile(evidencePath, append(evidence, ' '), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := VerifyInstalledReleaseInventory(t.Context(), fixture.destination); !errors.Is(err, ErrReleaseEvidenceInvalid) {
		t.Fatalf("VerifyInstalledReleaseInventory(tampered evidence) = %v", err)
	}
}

func TestReleaseInventoryRejectsManifestOnlyPackageAndMissingLicense(t *testing.T) {
	t.Run("manifest only", func(t *testing.T) {
		fixture := newReleaseFixture(t)
		manifestRaw, err := plugin.EncodeManifest(testReleaseManifest())
		if err != nil {
			t.Fatal(err)
		}
		var archive bytes.Buffer
		writer := zip.NewWriter(&archive)
		file, err := writer.Create(plugin.PackageManifestName)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write(manifestRaw); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(fixture.sourceRoot, filepath.FromSlash(fixture.entry.PackagePath)), archive.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
		fixture.entry.PackageSize = int64(archive.Len())
		fixture.entry.PackageSHA256 = digestHex(archive.Bytes())
		writeReleaseInventory(t, fixture.inventoryPath, fixture.entry)
		if err := InstallReleaseInventory(t.Context(), fixture.inventoryPath, fixture.sourceRoot, fixture.destination); !errors.Is(err, ErrReleaseArtifactInvalid) {
			t.Fatalf("InstallReleaseInventory(manifest only) = %v", err)
		}
	})

	t.Run("missing license", func(t *testing.T) {
		fixture := newReleaseFixture(t)
		if err := os.Remove(filepath.Join(fixture.sourceRoot, filepath.FromSlash(fixture.entry.LicensePath))); err != nil {
			t.Fatal(err)
		}
		if err := InstallReleaseInventory(t.Context(), fixture.inventoryPath, fixture.sourceRoot, fixture.destination); !errors.Is(err, ErrReleaseArtifactInvalid) {
			t.Fatalf("InstallReleaseInventory(missing license) = %v", err)
		}
	})
}

func TestReleaseInventoryRejectsDriftAndUnsafeMetadata(t *testing.T) {
	fixture := newReleaseFixture(t)
	tests := []struct {
		name   string
		mutate func(*ReleasePluginArtifact)
	}{
		{"package hash", func(entry *ReleasePluginArtifact) { entry.PackageSHA256 = stringsOf('0', 64) }},
		{"module hash", func(entry *ReleasePluginArtifact) { entry.ModuleSHA256 = stringsOf('0', 64) }},
		{"license hash", func(entry *ReleasePluginArtifact) { entry.LicenseSHA256 = stringsOf('0', 64) }},
		{"abi", func(entry *ReleasePluginArtifact) { entry.ABI = plugin.ABIRange{Min: 2, Max: 2} }},
		{"external dependency", func(entry *ReleasePluginArtifact) { entry.ExternalDependencies = []string{"libcurl.dylib"} }},
		{"minimum os", func(entry *ReleasePluginArtifact) { entry.MinimumOS = "12.0" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			entry := fixture.entry
			entry.RequiredPaths = append([]string(nil), fixture.entry.RequiredPaths...)
			entry.ExternalDependencies = append([]string(nil), fixture.entry.ExternalDependencies...)
			test.mutate(&entry)
			writeReleaseInventory(t, fixture.inventoryPath, entry)
			if err := InstallReleaseInventory(t.Context(), fixture.inventoryPath, fixture.sourceRoot, filepath.Join(t.TempDir(), "release")); err == nil {
				t.Fatal("InstallReleaseInventory() accepted drifted metadata")
			}
		})
	}
}

type releaseFixture struct {
	sourceRoot    string
	inventoryPath string
	destination   string
	entry         ReleasePluginArtifact
}

func newReleaseFixture(t *testing.T) releaseFixture {
	t.Helper()
	sourceRoot := t.TempDir()
	packageRelative := "plugins/example.fairy-plugin"
	licenseRelative := "licenses/example.LICENSE"
	if err := os.MkdirAll(filepath.Join(sourceRoot, "plugins"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(sourceRoot, "licenses"), 0o700); err != nil {
		t.Fatal(err)
	}
	module := echoGuestWASM()
	var packed bytes.Buffer
	if err := plugin.Pack(&packed, testReleaseManifest(), module); err != nil {
		t.Fatal(err)
	}
	packageRaw := packed.Bytes()
	licenseRaw := []byte("Apache License\nVersion 2.0\n")
	if err := os.WriteFile(filepath.Join(sourceRoot, filepath.FromSlash(packageRelative)), packageRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceRoot, filepath.FromSlash(licenseRelative)), licenseRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	moduleDigest := sha256.Sum256(module)
	entry := ReleasePluginArtifact{
		ID: "fairy.plugin.release-example", Version: "1.2.3",
		SourceURL: "https://example.invalid/fairy-plugin", SourceRevision: "v1.2.3",
		Platform: "wasm32", MinimumOS: "none",
		PackagePath: packageRelative, PackageSHA256: digestHex(packageRaw), PackageSize: int64(len(packageRaw)),
		ModuleSHA256: hex.EncodeToString(moduleDigest[:]),
		License:      "Apache-2.0", LicensePath: licenseRelative, LicenseSHA256: digestHex(licenseRaw), LicenseSize: int64(len(licenseRaw)),
		ABI:                  testReleaseManifest().ABI,
		RequiredPaths:        []string{plugin.PackageManifestName, plugin.PackageModuleName, plugin.PackageChecksumsName},
		ExternalDependencies: []string{},
	}
	inventoryPath := filepath.Join(sourceRoot, "plugin-inventory.json")
	writeReleaseInventory(t, inventoryPath, entry)
	return releaseFixture{
		sourceRoot: sourceRoot, inventoryPath: inventoryPath,
		destination: filepath.Join(t.TempDir(), "plugin-release"), entry: entry,
	}
}

func testReleaseManifest() plugin.Manifest {
	return plugin.Manifest{
		SchemaVersion: plugin.ManifestSchema,
		ID:            "fairy.plugin.release-example", Version: "1.2.3",
		ABI:   plugin.ABIRange{Min: plugin.ABIVersion, Max: plugin.ABIVersion},
		Entry: plugin.EntryModule, Exports: plugin.RequiredExports(), Capabilities: []string{},
		ConfigSchemaVersion: 1, DataSchemaVersion: 1,
	}
}

func writeReleaseInventory(t *testing.T, filename string, entries ...ReleasePluginArtifact) {
	t.Helper()
	raw, err := json.MarshalIndent(ReleaseInventory{SchemaVersion: ReleaseInventorySchemaVersion, Plugins: entries}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, append(raw, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

func stringsOf(char byte, count int) string {
	return string(bytes.Repeat([]byte{char}, count))
}
