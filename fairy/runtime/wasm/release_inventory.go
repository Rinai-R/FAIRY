package wasm

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"fairy/plugin"
)

const (
	ReleaseInventorySchemaVersion = 1
	ReleaseInventoryFileName      = "inventory.json"
	ReleaseEvidenceFileName       = "installation-evidence.json"

	maxReleasePackageBytes = 32 << 20
	maxReleaseLicenseBytes = 1 << 20
	shadowHealthTimeout    = 5 * time.Second
)

var (
	ErrReleaseInventoryInvalid = errors.New("plugin release inventory is invalid")
	ErrReleaseArtifactInvalid  = errors.New("plugin release artifact is invalid")
	ErrReleaseEvidenceInvalid  = errors.New("plugin installation evidence is invalid")
)

// ReleaseInventory is the complete set of WASM plugins shipped inside one
// Desktop release. An explicit empty list is valid and means the Wazero host is
// available only for user-installed packages; no built-in plugin is advertised.
type ReleaseInventory struct {
	SchemaVersion int                     `json:"schemaVersion"`
	Plugins       []ReleasePluginArtifact `json:"plugins"`
}

// ReleasePluginArtifact records both provenance and every file needed to prove
// that a declared plugin is an executable, license-complete WASM package.
type ReleasePluginArtifact struct {
	ID                   string          `json:"id"`
	Version              string          `json:"version"`
	SourceURL            string          `json:"sourceURL"`
	SourceRevision       string          `json:"sourceRevision"`
	Platform             string          `json:"platform"`
	MinimumOS            string          `json:"minimumOS"`
	PackagePath          string          `json:"packagePath"`
	PackageSHA256        string          `json:"packageSHA256"`
	PackageSize          int64           `json:"packageSize"`
	ModuleSHA256         string          `json:"moduleSHA256"`
	License              string          `json:"license"`
	LicensePath          string          `json:"licensePath"`
	LicenseSHA256        string          `json:"licenseSHA256"`
	LicenseSize          int64           `json:"licenseSize"`
	ABI                  plugin.ABIRange `json:"abi"`
	RequiredPaths        []string        `json:"requiredPaths"`
	ExternalDependencies []string        `json:"externalDependencies"`
}

type releaseInstallationEvidence struct {
	SchemaVersion   int                     `json:"schemaVersion"`
	InventorySHA256 string                  `json:"inventorySHA256"`
	Plugins         []releasePluginEvidence `json:"plugins"`
}

type releasePluginEvidence struct {
	ID              string          `json:"id"`
	Version         string          `json:"version"`
	PackagePath     string          `json:"packagePath"`
	PackageSHA256   string          `json:"packageSHA256"`
	ModuleSHA256    string          `json:"moduleSHA256"`
	LicensePath     string          `json:"licensePath"`
	LicenseSHA256   string          `json:"licenseSHA256"`
	ABI             plugin.ABIRange `json:"abi"`
	ShadowHealth    string          `json:"shadowHealth"`
	RequiredPaths   []string        `json:"requiredPaths"`
	ExternalDepends []string        `json:"externalDependencies"`
}

type verifiedReleasePlugin struct {
	entry      ReleasePluginArtifact
	packageRaw []byte
	licenseRaw []byte
	bundle     plugin.Bundle
}

func ParseReleaseInventory(r io.Reader) (ReleaseInventory, error) {
	if r == nil {
		return ReleaseInventory{}, fmt.Errorf("%w: reader is required", ErrReleaseInventoryInvalid)
	}
	decoder := json.NewDecoder(r)
	decoder.DisallowUnknownFields()
	var inventory ReleaseInventory
	if err := decoder.Decode(&inventory); err != nil {
		return ReleaseInventory{}, fmt.Errorf("%w: %v", ErrReleaseInventoryInvalid, err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return ReleaseInventory{}, fmt.Errorf("%w: inventory must contain one JSON value", ErrReleaseInventoryInvalid)
	}
	if err := validateReleaseInventory(inventory); err != nil {
		return ReleaseInventory{}, err
	}
	return inventory, nil
}

// InstallReleaseInventory verifies every declared package with the real Wazero
// host, then atomically writes the sealed release inventory and deterministic
// installation evidence. The destination must not exist, preventing stale or
// undeclared plugin files from surviving a package rebuild.
func InstallReleaseInventory(ctx context.Context, inventoryPath, sourceRoot, destination string) error {
	if ctx == nil {
		return fmt.Errorf("%w: context is required", ErrReleaseInventoryInvalid)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	inventoryRaw, err := readRegularAbsoluteFile(inventoryPath, maxReleaseLicenseBytes)
	if err != nil {
		return fmt.Errorf("read plugin release inventory: %w", err)
	}
	inventory, err := ParseReleaseInventory(bytes.NewReader(inventoryRaw))
	if err != nil {
		return err
	}
	if err := requireDirectory(sourceRoot); err != nil {
		return fmt.Errorf("plugin release source root: %w", err)
	}
	if destination == "" || destination != filepath.Clean(destination) {
		return fmt.Errorf("%w: destination must be a clean path", ErrReleaseInventoryInvalid)
	}
	if _, err := os.Lstat(destination); err == nil {
		return fmt.Errorf("%w: destination already exists", ErrReleaseInventoryInvalid)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect plugin release destination: %w", err)
	}
	parent := filepath.Dir(destination)
	if err := requireDirectory(parent); err != nil {
		return fmt.Errorf("plugin release destination parent: %w", err)
	}

	verified, err := verifyReleasePlugins(ctx, inventory, sourceRoot, false)
	if err != nil {
		return err
	}
	evidence, err := encodeReleaseEvidence(inventoryRaw, verified)
	if err != nil {
		return err
	}

	staging, err := os.MkdirTemp(parent, ".fairy-plugin-release-")
	if err != nil {
		return fmt.Errorf("create plugin release staging directory: %w", err)
	}
	defer os.RemoveAll(staging)
	if err := writeReleaseFile(staging, ReleaseInventoryFileName, inventoryRaw); err != nil {
		return err
	}
	if err := writeReleaseFile(staging, ReleaseEvidenceFileName, evidence); err != nil {
		return err
	}
	for _, item := range verified {
		if err := writeReleaseFile(staging, installedPackagePath(item.entry), item.packageRaw); err != nil {
			return err
		}
		if err := writeReleaseFile(staging, installedLicensePath(item.entry), item.licenseRaw); err != nil {
			return err
		}
	}
	if err := os.Rename(staging, destination); err != nil {
		return fmt.Errorf("install plugin release inventory: %w", err)
	}
	return nil
}

// VerifyInstalledReleaseInventory replays package, license, ABI, checksum and
// shadow-health verification from the final App layout and rejects any file or
// directory not declared by the sealed inventory.
func VerifyInstalledReleaseInventory(ctx context.Context, releaseRoot string) error {
	if ctx == nil {
		return fmt.Errorf("%w: context is required", ErrReleaseEvidenceInvalid)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := requireDirectory(releaseRoot); err != nil {
		return fmt.Errorf("%w: release root: %v", ErrReleaseEvidenceInvalid, err)
	}
	inventoryRaw, err := readReleaseFile(releaseRoot, ReleaseInventoryFileName, maxReleaseLicenseBytes)
	if err != nil {
		return err
	}
	inventory, err := ParseReleaseInventory(bytes.NewReader(inventoryRaw))
	if err != nil {
		return err
	}
	verified, err := verifyReleasePlugins(ctx, inventory, releaseRoot, true)
	if err != nil {
		return err
	}
	wantEvidence, err := encodeReleaseEvidence(inventoryRaw, verified)
	if err != nil {
		return err
	}
	gotEvidence, err := readReleaseFile(releaseRoot, ReleaseEvidenceFileName, maxReleaseLicenseBytes)
	if err != nil {
		return err
	}
	if !bytes.Equal(gotEvidence, wantEvidence) {
		return fmt.Errorf("%w: evidence does not match verified artifacts", ErrReleaseEvidenceInvalid)
	}
	return verifyReleaseTree(releaseRoot, inventory)
}

func validateReleaseInventory(inventory ReleaseInventory) error {
	if inventory.SchemaVersion != ReleaseInventorySchemaVersion {
		return fmt.Errorf("%w: schemaVersion = %d, want %d", ErrReleaseInventoryInvalid, inventory.SchemaVersion, ReleaseInventorySchemaVersion)
	}
	if inventory.Plugins == nil {
		return fmt.Errorf("%w: plugins must be an explicit JSON array", ErrReleaseInventoryInvalid)
	}
	if len(inventory.Plugins) > 32 {
		return fmt.Errorf("%w: plugins exceed release inventory limit", ErrReleaseInventoryInvalid)
	}
	seenIdentity := make(map[string]struct{}, len(inventory.Plugins))
	seenPaths := make(map[string]struct{}, len(inventory.Plugins)*2)
	previous := ""
	for index, entry := range inventory.Plugins {
		if err := validateReleasePlugin(entry); err != nil {
			return fmt.Errorf("%w: plugins[%d]: %v", ErrReleaseInventoryInvalid, index, err)
		}
		identity := entry.ID + "@" + entry.Version
		if previous != "" && identity <= previous {
			return fmt.Errorf("%w: plugins must be strictly sorted by id and version", ErrReleaseInventoryInvalid)
		}
		previous = identity
		if _, exists := seenIdentity[identity]; exists {
			return fmt.Errorf("%w: duplicate plugin %s", ErrReleaseInventoryInvalid, identity)
		}
		seenIdentity[identity] = struct{}{}
		for _, artifactPath := range []string{entry.PackagePath, entry.LicensePath} {
			if _, exists := seenPaths[artifactPath]; exists {
				return fmt.Errorf("%w: duplicate artifact path %s", ErrReleaseInventoryInvalid, artifactPath)
			}
			seenPaths[artifactPath] = struct{}{}
		}
	}
	return nil
}

func validateReleasePlugin(entry ReleasePluginArtifact) error {
	if entry.ID == "" || entry.Version == "" {
		return errors.New("id and version are required")
	}
	identityManifest := plugin.Manifest{
		SchemaVersion: plugin.ManifestSchema,
		ID:            entry.ID, Version: entry.Version, ABI: entry.ABI,
		Entry: plugin.EntryModule, Exports: plugin.RequiredExports(), Capabilities: []string{},
		ConfigSchemaVersion: 1, DataSchemaVersion: 1,
	}
	if err := plugin.CheckCompatibility(plugin.ABIVersion, identityManifest); err != nil {
		return fmt.Errorf("plugin identity or ABI: %v", err)
	}
	parsedSource, err := url.Parse(entry.SourceURL)
	if err != nil || parsedSource.Scheme != "https" || parsedSource.Host == "" || parsedSource.User != nil || parsedSource.RawQuery != "" || parsedSource.Fragment != "" {
		return errors.New("sourceURL must be a clean HTTPS provenance URL")
	}
	if err := requireCleanASCII(entry.SourceRevision, 128); err != nil {
		return fmt.Errorf("sourceRevision %v", err)
	}
	if entry.Platform != "wasm32" || entry.MinimumOS != "none" {
		return errors.New("platform/minimumOS must be wasm32/none")
	}
	if err := requireReleasePath(entry.PackagePath, ".fairy-plugin"); err != nil {
		return fmt.Errorf("packagePath %v", err)
	}
	if !strings.HasPrefix(entry.PackagePath, "plugins/") {
		return errors.New("packagePath must be below plugins/")
	}
	if err := requireReleasePath(entry.LicensePath, ".LICENSE"); err != nil {
		return fmt.Errorf("licensePath %v", err)
	}
	if !strings.HasPrefix(entry.LicensePath, "licenses/") {
		return errors.New("licensePath must be below licenses/")
	}
	if err := requireHexSHA256(entry.PackageSHA256); err != nil {
		return fmt.Errorf("packageSHA256 %v", err)
	}
	if err := requireHexSHA256(entry.ModuleSHA256); err != nil {
		return fmt.Errorf("moduleSHA256 %v", err)
	}
	if err := requireHexSHA256(entry.LicenseSHA256); err != nil {
		return fmt.Errorf("licenseSHA256 %v", err)
	}
	if entry.PackageSize <= 0 || entry.PackageSize > maxReleasePackageBytes {
		return errors.New("packageSize is outside the release budget")
	}
	if entry.LicenseSize <= 0 || entry.LicenseSize > maxReleaseLicenseBytes {
		return errors.New("licenseSize is outside the release budget")
	}
	if err := requireSPDX(entry.License); err != nil {
		return err
	}
	if entry.ABI.Min < 1 || entry.ABI.Max < entry.ABI.Min || plugin.ABIVersion < entry.ABI.Min || plugin.ABIVersion > entry.ABI.Max {
		return errors.New("abi does not include the current host ABI")
	}
	wantPaths := []string{plugin.PackageManifestName, plugin.PackageModuleName, plugin.PackageChecksumsName}
	if !slices.Equal(entry.RequiredPaths, wantPaths) {
		return fmt.Errorf("requiredPaths must equal %v", wantPaths)
	}
	if entry.ExternalDependencies == nil || len(entry.ExternalDependencies) != 0 {
		return errors.New("externalDependencies must be an explicit empty array")
	}
	return nil
}

func verifyReleasePlugins(ctx context.Context, inventory ReleaseInventory, root string, installed bool) ([]verifiedReleasePlugin, error) {
	verified := make([]verifiedReleasePlugin, 0, len(inventory.Plugins))
	for _, entry := range inventory.Plugins {
		packagePath := entry.PackagePath
		licensePath := entry.LicensePath
		if installed {
			packagePath = installedPackagePath(entry)
			licensePath = installedLicensePath(entry)
		}
		packageRaw, err := readReleaseFile(root, packagePath, maxReleasePackageBytes)
		if err != nil {
			return nil, fmt.Errorf("%w: %s package: %v", ErrReleaseArtifactInvalid, entry.ID, err)
		}
		licenseRaw, err := readReleaseFile(root, licensePath, maxReleaseLicenseBytes)
		if err != nil {
			return nil, fmt.Errorf("%w: %s license: %v", ErrReleaseArtifactInvalid, entry.ID, err)
		}
		if int64(len(packageRaw)) != entry.PackageSize || digestHex(packageRaw) != entry.PackageSHA256 {
			return nil, fmt.Errorf("%w: %s package size or hash mismatch", ErrReleaseArtifactInvalid, entry.ID)
		}
		if int64(len(licenseRaw)) != entry.LicenseSize || digestHex(licenseRaw) != entry.LicenseSHA256 {
			return nil, fmt.Errorf("%w: %s license size or hash mismatch", ErrReleaseArtifactInvalid, entry.ID)
		}
		bundle, err := plugin.OpenBundle(bytes.NewReader(packageRaw), int64(len(packageRaw)))
		if err != nil {
			return nil, fmt.Errorf("%w: %s package: %v", ErrReleaseArtifactInvalid, entry.ID, err)
		}
		if bundle.Manifest.ID != entry.ID || bundle.Manifest.Version != entry.Version || bundle.Manifest.ABI != entry.ABI {
			return nil, fmt.Errorf("%w: %s manifest identity or ABI mismatch", ErrReleaseArtifactInvalid, entry.ID)
		}
		if hex.EncodeToString(bundle.SHA256[:]) != entry.ModuleSHA256 {
			return nil, fmt.Errorf("%w: %s module hash mismatch", ErrReleaseArtifactInvalid, entry.ID)
		}
		verified = append(verified, verifiedReleasePlugin{entry: entry, packageRaw: packageRaw, licenseRaw: licenseRaw, bundle: bundle})
	}
	if err := verifyShadowHealth(ctx, verified); err != nil {
		return nil, err
	}
	return verified, nil
}

func verifyShadowHealth(ctx context.Context, plugins []verifiedReleasePlugin) error {
	if len(plugins) == 0 {
		return nil
	}
	host, err := Open(ctx)
	if err != nil {
		return fmt.Errorf("%w: open Wazero host: %v", ErrReleaseArtifactInvalid, err)
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cancel()
		_ = host.Close(closeCtx)
	}()
	for _, item := range plugins {
		checkCtx, cancel := context.WithTimeout(ctx, shadowHealthTimeout)
		err := host.ShadowHealth(checkCtx, "release-"+item.entry.ID+"-"+item.entry.Version, item.bundle.Module)
		cancel()
		if err != nil {
			return fmt.Errorf("%w: %s shadow health: %v", ErrReleaseArtifactInvalid, item.entry.ID, err)
		}
	}
	return nil
}

func encodeReleaseEvidence(inventoryRaw []byte, plugins []verifiedReleasePlugin) ([]byte, error) {
	evidence := releaseInstallationEvidence{
		SchemaVersion:   ReleaseInventorySchemaVersion,
		InventorySHA256: digestHex(inventoryRaw),
		Plugins:         make([]releasePluginEvidence, 0, len(plugins)),
	}
	for _, item := range plugins {
		evidence.Plugins = append(evidence.Plugins, releasePluginEvidence{
			ID: item.entry.ID, Version: item.entry.Version,
			PackagePath: installedPackagePath(item.entry), PackageSHA256: item.entry.PackageSHA256,
			ModuleSHA256: item.entry.ModuleSHA256,
			LicensePath:  installedLicensePath(item.entry), LicenseSHA256: item.entry.LicenseSHA256,
			ABI: item.entry.ABI, ShadowHealth: "passed",
			RequiredPaths:   slices.Clone(item.entry.RequiredPaths),
			ExternalDepends: slices.Clone(item.entry.ExternalDependencies),
		})
	}
	raw, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode plugin installation evidence: %w", err)
	}
	return append(raw, '\n'), nil
}

func verifyReleaseTree(root string, inventory ReleaseInventory) error {
	expectedFiles := map[string]struct{}{
		ReleaseInventoryFileName: {},
		ReleaseEvidenceFileName:  {},
	}
	expectedDirs := map[string]struct{}{".": {}}
	for _, entry := range inventory.Plugins {
		for _, filename := range []string{installedPackagePath(entry), installedLicensePath(entry)} {
			expectedFiles[filename] = struct{}{}
			for current := path.Dir(filename); current != "."; current = path.Dir(current) {
				expectedDirs[current] = struct{}{}
			}
		}
	}
	walkErr := filepath.WalkDir(root, func(filename string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, filename)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: symlink %s is not allowed", ErrReleaseEvidenceInvalid, relative)
		}
		if entry.IsDir() {
			if _, ok := expectedDirs[relative]; !ok {
				return fmt.Errorf("%w: undeclared directory %s", ErrReleaseEvidenceInvalid, relative)
			}
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%w: %s is not a regular file", ErrReleaseEvidenceInvalid, relative)
		}
		if _, ok := expectedFiles[relative]; !ok {
			return fmt.Errorf("%w: undeclared file %s", ErrReleaseEvidenceInvalid, relative)
		}
		delete(expectedFiles, relative)
		return nil
	})
	if walkErr != nil {
		return walkErr
	}
	if len(expectedFiles) != 0 {
		missing := make([]string, 0, len(expectedFiles))
		for filename := range expectedFiles {
			missing = append(missing, filename)
		}
		slices.Sort(missing)
		return fmt.Errorf("%w: missing files %v", ErrReleaseEvidenceInvalid, missing)
	}
	return nil
}

func installedPackagePath(entry ReleasePluginArtifact) string {
	return path.Join("packages", entry.ID, entry.Version, "plugin.fairy-plugin")
}

func installedLicensePath(entry ReleasePluginArtifact) string {
	return path.Join("licenses", entry.ID, entry.Version, "LICENSE")
}

func readReleaseFile(root, relative string, limit int64) ([]byte, error) {
	filename, err := joinReleasePath(root, relative)
	if err != nil {
		return nil, err
	}
	if err := requireReleaseParents(root, relative); err != nil {
		return nil, err
	}
	return readRegularAbsoluteFile(filename, limit)
}

func requireReleaseParents(root, relative string) error {
	current := root
	parts := strings.Split(path.Dir(relative), "/")
	for _, part := range parts {
		if part == "." {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return errors.New("artifact parent must be a non-symlink directory")
		}
	}
	return nil
}

func readRegularAbsoluteFile(filename string, limit int64) ([]byte, error) {
	if filename == "" || limit <= 0 {
		return nil, errors.New("regular file path and limit are required")
	}
	info, err := os.Lstat(filename)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, errors.New("file must be a regular non-symlink")
	}
	if info.Size() <= 0 || info.Size() > limit {
		return nil, fmt.Errorf("file size %d is outside budget", info.Size())
	}
	raw, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) != info.Size() {
		return nil, errors.New("file changed while being read")
	}
	return raw, nil
}

func writeReleaseFile(root, relative string, raw []byte) error {
	filename, err := joinReleasePath(root, relative)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
		return fmt.Errorf("create plugin release directory: %w", err)
	}
	if err := os.WriteFile(filename, raw, 0o644); err != nil {
		return fmt.Errorf("write plugin release file %s: %w", relative, err)
	}
	return nil
}

func joinReleasePath(root, relative string) (string, error) {
	if err := requireReleasePath(relative, ""); err != nil {
		return "", err
	}
	filename := filepath.Join(root, filepath.FromSlash(relative))
	rel, err := filepath.Rel(root, filename)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("artifact path escapes release root")
	}
	return filename, nil
}

func requireReleasePath(value, suffix string) error {
	if value == "" || value != path.Clean(value) || path.IsAbs(value) || strings.Contains(value, `\`) || value == ".." || strings.HasPrefix(value, "../") {
		return errors.New("must be a clean relative slash path")
	}
	if suffix != "" && !strings.HasSuffix(value, suffix) {
		return fmt.Errorf("must end in %s", suffix)
	}
	return nil
}

func requireDirectory(dirname string) error {
	if dirname == "" {
		return errors.New("directory path is required")
	}
	info, err := os.Lstat(dirname)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("must be a non-symlink directory")
	}
	return nil
}

func requireHexSHA256(value string) error {
	if len(value) != sha256.Size*2 {
		return errors.New("must be 64 lowercase hex characters")
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || hex.EncodeToString(decoded) != value {
		return errors.New("must be 64 lowercase hex characters")
	}
	return nil
}

func requireCleanASCII(value string, limit int) error {
	if value == "" || len(value) > limit || value != strings.TrimSpace(value) {
		return errors.New("must be a clean non-empty ASCII value")
	}
	for _, char := range value {
		if char < 0x21 || char > 0x7e {
			return errors.New("must be a clean non-empty ASCII value")
		}
	}
	return nil
}

func requireSPDX(value string) error {
	if err := requireCleanASCII(value, 64); err != nil {
		return fmt.Errorf("license %v", err)
	}
	for _, char := range value {
		letter := char >= 'A' && char <= 'Z' || char >= 'a' && char <= 'z'
		digit := char >= '0' && char <= '9'
		if !letter && !digit && !strings.ContainsRune(".-+", char) {
			return errors.New("license must be a simple SPDX identifier")
		}
	}
	return nil
}

func digestHex(raw []byte) string {
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}
