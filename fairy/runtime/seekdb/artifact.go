package seekdb

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"strings"
)

const (
	artifactCatalogSchemaVersion = 2
	builtinDarwinArm64RecipePath = "fairy/runtime/seekdb/build/darwin-arm64.sh"
)

var (
	ErrArtifactCandidate   = errors.New("SeekDB runtime artifact is not release-verified")
	ErrArtifactIntegrity   = errors.New("SeekDB runtime artifact integrity check failed")
	ErrArtifactUnsupported = errors.New("SeekDB runtime artifact is unsupported")

	//go:embed artifacts.json
	builtinArtifactCatalog []byte

	//go:embed build/darwin-arm64.sh
	builtinDarwinArm64Recipe []byte
)

type ArtifactStatus string

const (
	ArtifactStatusCandidate   ArtifactStatus = "candidate"
	ArtifactStatusUnsupported ArtifactStatus = "unsupported"
	ArtifactStatusVerified    ArtifactStatus = "verified"
)

type ArtifactCatalog struct {
	SchemaVersion int              `json:"schemaVersion"`
	Product       string           `json:"product"`
	ReleaseTag    string           `json:"releaseTag"`
	ReleaseURL    string           `json:"releaseURL"`
	Targets       []ArtifactTarget `json:"targets"`
}

type ArtifactTarget struct {
	GOOS     string           `json:"goos"`
	GOARCH   string           `json:"goarch"`
	Status   ArtifactStatus   `json:"status"`
	Reason   string           `json:"reason"`
	Artifact *RuntimeArtifact `json:"artifact,omitempty"`
}

type RuntimeArtifact struct {
	Version              string              `json:"version"`
	SourceURL            string              `json:"sourceURL"`
	ProvenanceURL        string              `json:"provenanceURL"`
	SHA256               string              `json:"sha256"`
	Size                 int64               `json:"size"`
	License              string              `json:"license"`
	LicenseURL           string              `json:"licenseURL"`
	LicenseSHA256        string              `json:"licenseSHA256"`
	NoticeURL            string              `json:"noticeURL"`
	NoticeSHA256         string              `json:"noticeSHA256"`
	ArchiveFormat        string              `json:"archiveFormat"`
	LibraryPath          string              `json:"libraryPath"`
	RequiredPaths        []string            `json:"requiredPaths"`
	ExternalDependencies []string            `json:"externalDependencies"`
	MinimumOSVersion     string              `json:"minimumOSVersion"`
	BuildRecipe          BuildRecipeContract `json:"buildRecipe"`
	MachO                *MachOContract      `json:"machO,omitempty"`
}

// BuildRecipeContract records the build inputs that cannot be inferred from
// the resulting dylib. The recipe itself is embedded into this package and its
// digest is checked when the builtin catalog is loaded.
type BuildRecipeContract struct {
	Path                           string `json:"path"`
	SHA256                         string `json:"sha256"`
	CommandLineToolsPackageVersion string `json:"commandLineToolsPackageVersion"`
	SDKVersion                     string `json:"sdkVersion"`
	LLVMVersion                    string `json:"llvmVersion"`
	DeploymentTarget               string `json:"deploymentTarget"`
	CMakeVersion                   string `json:"cmakeVersion"`
	RustVersion                    string `json:"rustVersion"`
	PythonVersion                  string `json:"pythonVersion"`
}

// MachOContract is the exact unsigned native-library shape accepted by the
// release packager before it signs the nested dylib.
type MachOContract struct {
	InstallName           string   `json:"installName"`
	SDKVersion            string   `json:"sdkVersion"`
	DynamicDependencies   []string `json:"dynamicDependencies"`
	ExportedSymbolCount   int      `json:"exportedSymbolCount"`
	ExportedSymbolsSHA256 string   `json:"exportedSymbolsSHA256"`
}

// ArtifactBundle names the release inputs that must be checked before a
// platform package can embed SeekDB. The paths are build inputs, not runtime
// configuration.
type ArtifactBundle struct {
	LibraryPath      string
	LicensePath      string
	NoticePath       string
	AppInfoPlistPath string
}

func BuiltinArtifactCatalog() (ArtifactCatalog, error) {
	catalog, err := ParseArtifactCatalog(strings.NewReader(string(builtinArtifactCatalog)))
	if err != nil {
		return ArtifactCatalog{}, err
	}
	for _, target := range catalog.Targets {
		if target.Artifact == nil || target.Artifact.BuildRecipe.Path != builtinDarwinArm64RecipePath {
			continue
		}
		if err := verifyArtifactContent(
			strings.NewReader(string(builtinDarwinArm64Recipe)),
			"build recipe",
			target.Artifact.BuildRecipe.SHA256,
			int64(len(builtinDarwinArm64Recipe)),
		); err != nil {
			return ArtifactCatalog{}, err
		}
	}
	return catalog, nil
}

func ParseArtifactCatalog(reader io.Reader) (ArtifactCatalog, error) {
	if reader == nil {
		return ArtifactCatalog{}, errors.New("SeekDB artifact catalog reader is required")
	}
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	var catalog ArtifactCatalog
	if err := decoder.Decode(&catalog); err != nil {
		return ArtifactCatalog{}, fmt.Errorf("decode SeekDB artifact catalog: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return ArtifactCatalog{}, errors.New("decode SeekDB artifact catalog: trailing JSON value")
		}
		return ArtifactCatalog{}, fmt.Errorf("decode SeekDB artifact catalog: %w", err)
	}
	if err := catalog.Validate(); err != nil {
		return ArtifactCatalog{}, err
	}
	return catalog, nil
}

func (c ArtifactCatalog) Validate() error {
	if c.SchemaVersion != artifactCatalogSchemaVersion {
		return fmt.Errorf("SeekDB artifact catalog schema version %d is unsupported", c.SchemaVersion)
	}
	if c.Product != "seekdb" {
		return errors.New("SeekDB artifact catalog product must be seekdb")
	}
	if c.ReleaseTag == "" || c.ReleaseTag != strings.TrimSpace(c.ReleaseTag) || strings.Contains(c.ReleaseTag, "/") {
		return errors.New("SeekDB artifact catalog release tag is required and must be clean")
	}
	expectedReleaseURL, sourceURL, licenseURL, noticeURL := catalogReleaseURLs(c.ReleaseTag)
	if err := validateOfficialURL("releaseURL", c.ReleaseURL, expectedReleaseURL); err != nil {
		return err
	}
	if c.ReleaseURL != expectedReleaseURL {
		return errors.New("SeekDB artifact catalog release URL must exactly match its release tag")
	}
	if len(c.Targets) == 0 {
		return errors.New("SeekDB artifact catalog must contain targets")
	}
	seen := make(map[string]struct{}, len(c.Targets))
	for index := range c.Targets {
		target := &c.Targets[index]
		if err := target.validate(); err != nil {
			return fmt.Errorf("validate SeekDB artifact target %d: %w", index, err)
		}
		if target.Artifact != nil {
			artifact := target.Artifact
			if isGitCommit(c.ReleaseTag) {
				if artifact.SourceURL != sourceURL {
					return fmt.Errorf("validate SeekDB artifact target %d: source URL release tag does not match catalog", index)
				}
			} else if !strings.HasPrefix(artifact.SourceURL, sourceURL) {
				return fmt.Errorf("validate SeekDB artifact target %d: source URL release tag does not match catalog", index)
			}
			if artifact.ProvenanceURL != c.ReleaseURL {
				return fmt.Errorf("validate SeekDB artifact target %d: provenance URL does not match catalog release", index)
			}
			if artifact.LicenseURL != licenseURL || artifact.NoticeURL != noticeURL {
				return fmt.Errorf("validate SeekDB artifact target %d: license documents do not match catalog release", index)
			}
		}
		key := target.GOOS + "/" + target.GOARCH
		if _, exists := seen[key]; exists {
			return fmt.Errorf("SeekDB artifact target %s is duplicated", key)
		}
		seen[key] = struct{}{}
	}
	return nil
}

func (c ArtifactCatalog) Target(goos, goarch string) (ArtifactTarget, error) {
	if err := validatePlatform(goos, goarch); err != nil {
		return ArtifactTarget{}, err
	}
	for _, target := range c.Targets {
		if target.GOOS == goos && target.GOARCH == goarch {
			return target, nil
		}
	}
	return ArtifactTarget{}, fmt.Errorf("%w: %s/%s is not recorded", ErrArtifactUnsupported, goos, goarch)
}

func (c ArtifactCatalog) Candidate(goos, goarch string) (RuntimeArtifact, error) {
	target, err := c.Target(goos, goarch)
	if err != nil {
		return RuntimeArtifact{}, err
	}
	if target.Status == ArtifactStatusUnsupported || target.Artifact == nil {
		return RuntimeArtifact{}, fmt.Errorf("%w: %s/%s: %s", ErrArtifactUnsupported, goos, goarch, target.Reason)
	}
	return *target.Artifact, nil
}

func (c ArtifactCatalog) Verified(goos, goarch string) (RuntimeArtifact, error) {
	target, err := c.Target(goos, goarch)
	if err != nil {
		return RuntimeArtifact{}, err
	}
	switch target.Status {
	case ArtifactStatusVerified:
		if target.Artifact == nil {
			return RuntimeArtifact{}, fmt.Errorf("%w: %s/%s has no artifact metadata", ErrArtifactUnsupported, goos, goarch)
		}
		return *target.Artifact, nil
	case ArtifactStatusCandidate:
		return RuntimeArtifact{}, fmt.Errorf("%w: %s/%s: %s", ErrArtifactCandidate, goos, goarch, target.Reason)
	default:
		return RuntimeArtifact{}, fmt.Errorf("%w: %s/%s: %s", ErrArtifactUnsupported, goos, goarch, target.Reason)
	}
}

func (a RuntimeArtifact) Verify(reader io.Reader) error {
	return verifyArtifactContent(reader, "library", a.SHA256, a.Size)
}

func (a RuntimeArtifact) VerifyFile(filename string) error {
	return verifyArtifactFile(filename, "library", a.SHA256, a.Size)
}

func (a RuntimeArtifact) VerifyLicenseFile(filename string) error {
	return verifyArtifactFile(filename, "LICENSE", a.LicenseSHA256, 0)
}

func (a RuntimeArtifact) VerifyNoticeFile(filename string) error {
	return verifyArtifactFile(filename, "NOTICE", a.NoticeSHA256, 0)
}

// VerifyBundle fails closed unless the target is explicitly release-verified
// and every redistributable input matches the immutable catalog metadata.
func (c ArtifactCatalog) VerifyBundle(goos, goarch string, bundle ArtifactBundle) error {
	artifact, err := c.Verified(goos, goarch)
	if err != nil {
		return err
	}
	if err := validateArtifactBundlePaths(bundle); err != nil {
		return err
	}
	if err := artifact.VerifyFile(bundle.LibraryPath); err != nil {
		return err
	}
	if err := artifact.verifyNativeFile(goos, goarch, bundle.LibraryPath); err != nil {
		return err
	}
	if err := artifact.VerifyLicenseFile(bundle.LicensePath); err != nil {
		return err
	}
	if err := artifact.VerifyNoticeFile(bundle.NoticePath); err != nil {
		return err
	}
	return verifyAppMinimumOS(bundle.AppInfoPlistPath, artifact.MinimumOSVersion)
}

// VerifyPackagedBundle replays the verified artifact contract against the
// files inside an assembled App. Before nested signing it is identical to
// VerifyBundle and therefore checks the exact unsigned hash and size. After
// signing, the whole-file digest necessarily changes; in that case this method
// requires a verified catalog entry and rechecks the immutable Mach-O shape,
// bounded signed size, LICENSE, NOTICE, and App minimum OS. The release caller
// remains responsible for codesign verification of the signature itself.
func (c ArtifactCatalog) VerifyPackagedBundle(goos, goarch string, bundle ArtifactBundle) error {
	artifact, err := c.Verified(goos, goarch)
	if err != nil {
		return err
	}
	if err := validateArtifactBundlePaths(bundle); err != nil {
		return err
	}
	if goos != "darwin" || artifact.MachO == nil {
		return c.VerifyBundle(goos, goarch, bundle)
	}
	signed, err := machOHasCodeSignature(bundle.LibraryPath)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrArtifactIntegrity, err)
	}
	if !signed {
		return c.VerifyBundle(goos, goarch, bundle)
	}
	info, err := os.Lstat(bundle.LibraryPath)
	if err != nil {
		return fmt.Errorf("%w: inspect signed library: %v", ErrArtifactIntegrity, err)
	}
	const maximumSignatureGrowth = int64(32 << 20)
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() < artifact.Size || info.Size() > artifact.Size+maximumSignatureGrowth {
		return fmt.Errorf("%w: signed library size %d is outside the permitted range", ErrArtifactIntegrity, info.Size())
	}
	if err := verifyPackagedMachOFile(bundle.LibraryPath, artifact.MinimumOSVersion, *artifact.MachO); err != nil {
		return fmt.Errorf("%w: %v", ErrArtifactIntegrity, err)
	}
	if err := artifact.VerifyLicenseFile(bundle.LicensePath); err != nil {
		return err
	}
	if err := artifact.VerifyNoticeFile(bundle.NoticePath); err != nil {
		return err
	}
	return verifyAppMinimumOS(bundle.AppInfoPlistPath, artifact.MinimumOSVersion)
}

func validateArtifactBundlePaths(bundle ArtifactBundle) error {
	for name, filename := range map[string]string{
		"library":        bundle.LibraryPath,
		"LICENSE":        bundle.LicensePath,
		"NOTICE":         bundle.NoticePath,
		"app Info.plist": bundle.AppInfoPlistPath,
	} {
		if filename == "" || filename != strings.TrimSpace(filename) || strings.ContainsRune(filename, 0) {
			return fmt.Errorf("SeekDB %s build input path is required and must be clean", name)
		}
	}
	return nil
}

func (t ArtifactTarget) validate() error {
	if err := validatePlatform(t.GOOS, t.GOARCH); err != nil {
		return err
	}
	if t.Reason == "" || t.Reason != strings.TrimSpace(t.Reason) || strings.ContainsRune(t.Reason, 0) {
		return errors.New("SeekDB artifact target reason is required and must be clean")
	}
	switch t.Status {
	case ArtifactStatusCandidate, ArtifactStatusVerified:
		if t.Artifact == nil {
			return fmt.Errorf("SeekDB artifact target %s requires artifact metadata", t.Status)
		}
		if err := t.Artifact.validate(); err != nil {
			return err
		}
		if t.GOOS == "darwin" {
			if t.Artifact.LibraryPath != "libseekdb.dylib" || t.Artifact.MachO == nil {
				return errors.New("darwin SeekDB artifact must be a Mach-O dylib")
			}
		} else if t.Artifact.LibraryPath == "libseekdb.dylib" || t.Artifact.MachO != nil {
			return errors.New("non-darwin SeekDB artifact must not use a Mach-O dylib")
		}
		return nil
	case ArtifactStatusUnsupported:
		if t.Artifact != nil {
			return errors.New("unsupported SeekDB artifact target must not contain artifact metadata")
		}
		return nil
	default:
		return fmt.Errorf("SeekDB artifact status %q is invalid", t.Status)
	}
}

func (a RuntimeArtifact) validate() error {
	cleanValues := []struct {
		name  string
		value string
	}{
		{"version", a.Version},
		{"sha256", a.SHA256},
		{"license", a.License},
		{"licenseSHA256", a.LicenseSHA256},
		{"noticeSHA256", a.NoticeSHA256},
		{"archiveFormat", a.ArchiveFormat},
		{"libraryPath", a.LibraryPath},
		{"minimumOSVersion", a.MinimumOSVersion},
	}
	for _, item := range cleanValues {
		if item.value == "" || item.value != strings.TrimSpace(item.value) || strings.ContainsRune(item.value, 0) {
			return fmt.Errorf("SeekDB artifact %s is required and must be clean", item.name)
		}
	}
	if err := validateOfficialSourceURL(a.SourceURL); err != nil {
		return err
	}
	if err := validateOfficialProvenanceURL(a.ProvenanceURL); err != nil {
		return err
	}
	for _, item := range []struct {
		name   string
		raw    string
		prefix string
	}{
		{"licenseURL", a.LicenseURL, "https://raw.githubusercontent.com/oceanbase/seekdb/"},
		{"noticeURL", a.NoticeURL, "https://raw.githubusercontent.com/oceanbase/seekdb/"},
	} {
		if err := validateOfficialURL(item.name, item.raw, item.prefix); err != nil {
			return err
		}
	}
	for _, digest := range []struct {
		name  string
		value string
	}{
		{"sha256", a.SHA256},
		{"licenseSHA256", a.LicenseSHA256},
		{"noticeSHA256", a.NoticeSHA256},
	} {
		if err := validateSHA256(digest.name, digest.value); err != nil {
			return err
		}
	}
	if a.Size <= 0 {
		return errors.New("SeekDB artifact size must be greater than zero")
	}
	switch a.ArchiveFormat {
	case "tar.gz", "zip":
		if !strings.HasSuffix(a.SourceURL, "."+a.ArchiveFormat) || !strings.Contains(path.Base(a.SourceURL), a.Version) {
			return errors.New("SeekDB artifact source filename must match its version and archive format")
		}
	case "dylib", "so":
		if !strings.Contains(a.SourceURL, a.Version) {
			return errors.New("SeekDB artifact source filename must match its version and archive format")
		}
	default:
		return fmt.Errorf("SeekDB artifact archive format %q is unsupported", a.ArchiveFormat)
	}
	if !isCleanArchivePath(a.LibraryPath) {
		return errors.New("SeekDB artifact library path must be a clean relative archive path")
	}
	if a.LibraryPath != "libseekdb.dylib" && a.LibraryPath != "libseekdb.so" {
		return errors.New("SeekDB artifact library path must be libseekdb.dylib or libseekdb.so")
	}
	if len(a.RequiredPaths) == 0 {
		return errors.New("SeekDB artifact required paths must not be empty")
	}
	seenPaths := make(map[string]struct{}, len(a.RequiredPaths))
	libraryRecorded := false
	for _, requiredPath := range a.RequiredPaths {
		if !isCleanArchivePath(requiredPath) {
			return errors.New("SeekDB artifact required paths must be clean relative archive paths")
		}
		if _, exists := seenPaths[requiredPath]; exists {
			return fmt.Errorf("SeekDB artifact required path %q is duplicated", requiredPath)
		}
		seenPaths[requiredPath] = struct{}{}
		libraryRecorded = libraryRecorded || requiredPath == a.LibraryPath
	}
	if !libraryRecorded {
		return errors.New("SeekDB artifact required paths must include the library path")
	}
	seenDependencies := make(map[string]struct{}, len(a.ExternalDependencies))
	for _, dependency := range a.ExternalDependencies {
		if dependency == "" || dependency != strings.TrimSpace(dependency) || strings.ContainsAny(dependency, "/\\") || strings.ContainsRune(dependency, 0) {
			return errors.New("SeekDB artifact external dependencies must be clean names")
		}
		if _, exists := seenDependencies[dependency]; exists {
			return fmt.Errorf("SeekDB artifact external dependency %q is duplicated", dependency)
		}
		seenDependencies[dependency] = struct{}{}
	}
	if err := validateMinimumOSVersion(a.MinimumOSVersion); err != nil {
		return err
	}
	if err := a.BuildRecipe.validate(a.MinimumOSVersion); err != nil {
		return err
	}
	if strings.HasSuffix(a.LibraryPath, ".dylib") {
		if a.MachO == nil {
			return errors.New("SeekDB dylib artifact requires a Mach-O contract")
		}
		if err := a.MachO.validate(a.BuildRecipe.SDKVersion); err != nil {
			return err
		}
	} else if a.MachO != nil {
		return errors.New("non-dylib SeekDB artifact must not contain a Mach-O contract")
	}
	return nil
}

func (c BuildRecipeContract) validate(minimumOSVersion string) error {
	for _, item := range []struct {
		name  string
		value string
	}{
		{"path", c.Path},
		{"sha256", c.SHA256},
		{"commandLineToolsPackageVersion", c.CommandLineToolsPackageVersion},
		{"sdkVersion", c.SDKVersion},
		{"llvmVersion", c.LLVMVersion},
		{"deploymentTarget", c.DeploymentTarget},
		{"cmakeVersion", c.CMakeVersion},
		{"rustVersion", c.RustVersion},
		{"pythonVersion", c.PythonVersion},
	} {
		if item.value == "" || item.value != strings.TrimSpace(item.value) || strings.ContainsRune(item.value, 0) {
			return fmt.Errorf("SeekDB build recipe %s is required and must be clean", item.name)
		}
	}
	if !isCleanArchivePath(c.Path) || !strings.HasSuffix(c.Path, ".sh") {
		return errors.New("SeekDB build recipe path must be a clean relative shell-script path")
	}
	if err := validateSHA256("build recipe sha256", c.SHA256); err != nil {
		return err
	}
	for name, version := range map[string]string{
		"llvmVersion":      c.LLVMVersion,
		"sdkVersion":       c.SDKVersion,
		"deploymentTarget": c.DeploymentTarget,
		"cmakeVersion":     c.CMakeVersion,
		"rustVersion":      c.RustVersion,
		"pythonVersion":    c.PythonVersion,
	} {
		if err := validateNumericVersion(version); err != nil {
			return fmt.Errorf("SeekDB build recipe %s: %w", name, err)
		}
	}
	if err := validateNumericVersionComponents(c.CommandLineToolsPackageVersion, 2, 5); err != nil {
		return fmt.Errorf("SeekDB build recipe commandLineToolsPackageVersion: %w", err)
	}
	if c.DeploymentTarget != minimumOSVersion {
		return errors.New("SeekDB build recipe deployment target must match artifact minimum OS version")
	}
	return nil
}

func (c MachOContract) validate(recipeSDKVersion string) error {
	if c.InstallName != "@rpath/libseekdb.dylib" {
		return errors.New("SeekDB Mach-O install name must be @rpath/libseekdb.dylib")
	}
	if err := validateNumericVersion(c.SDKVersion); err != nil {
		return fmt.Errorf("SeekDB Mach-O SDK version: %w", err)
	}
	if c.SDKVersion != recipeSDKVersion {
		return errors.New("SeekDB Mach-O SDK version must match its build recipe")
	}
	if c.ExportedSymbolCount <= 0 {
		return errors.New("SeekDB Mach-O exported symbol count must be greater than zero")
	}
	if err := validateSHA256("Mach-O exported symbols sha256", c.ExportedSymbolsSHA256); err != nil {
		return err
	}
	if len(c.DynamicDependencies) == 0 {
		return errors.New("SeekDB Mach-O dynamic dependencies must record the system closure")
	}
	previous := ""
	for _, dependency := range c.DynamicDependencies {
		if dependency == "" || dependency != strings.TrimSpace(dependency) || strings.ContainsRune(dependency, 0) {
			return errors.New("SeekDB Mach-O dynamic dependencies must be clean absolute paths")
		}
		if !strings.HasPrefix(dependency, "/usr/lib/") && !strings.HasPrefix(dependency, "/System/Library/") {
			return fmt.Errorf("SeekDB Mach-O dynamic dependency %q is not an operating-system library", dependency)
		}
		if previous != "" && dependency <= previous {
			return errors.New("SeekDB Mach-O dynamic dependencies must be sorted and unique")
		}
		previous = dependency
	}
	return nil
}

func validateSHA256(name, value string) error {
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != sha256.Size || strings.ToLower(value) != value {
		return fmt.Errorf("SeekDB artifact %s must be 64 lowercase hexadecimal characters", name)
	}
	return nil
}

func validateNumericVersion(value string) error {
	return validateNumericVersionComponents(value, 2, 4)
}

func validateNumericVersionComponents(value string, minimum, maximum int) error {
	parts := strings.Split(value, ".")
	if len(parts) < minimum || len(parts) > maximum {
		return fmt.Errorf("version must contain %d to %d numeric components", minimum, maximum)
	}
	for _, part := range parts {
		if part == "" {
			return errors.New("version must contain only numeric components")
		}
		for _, char := range part {
			if char < '0' || char > '9' {
				return errors.New("version must contain only numeric components")
			}
		}
	}
	return nil
}

func verifyArtifactFile(filename, kind, expectedSHA256 string, expectedSize int64) error {
	file, err := os.Open(filename)
	if err != nil {
		return fmt.Errorf("open SeekDB %s build input: %w", kind, err)
	}
	defer file.Close()
	return verifyArtifactContent(file, kind, expectedSHA256, expectedSize)
}

func verifyArtifactContent(reader io.Reader, kind, expectedSHA256 string, expectedSize int64) error {
	if reader == nil {
		return fmt.Errorf("SeekDB %s reader is required", kind)
	}
	hash := sha256.New()
	size, err := io.Copy(hash, reader)
	if err != nil {
		return fmt.Errorf("read SeekDB %s: %w", kind, err)
	}
	if expectedSize > 0 && size != expectedSize {
		return fmt.Errorf("%w: %s size is %d bytes, expected %d", ErrArtifactIntegrity, kind, size, expectedSize)
	}
	if actual := hex.EncodeToString(hash.Sum(nil)); actual != expectedSHA256 {
		return fmt.Errorf("%w: %s SHA-256 mismatch", ErrArtifactIntegrity, kind)
	}
	return nil
}

func verifyAppMinimumOS(filename, expected string) error {
	file, err := os.Open(filename)
	if err != nil {
		return fmt.Errorf("open app Info.plist: %w", err)
	}
	defer file.Close()

	decoder := xml.NewDecoder(file)
	found := ""
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("decode app Info.plist: %w", err)
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != "key" {
			continue
		}
		var key string
		if err := decoder.DecodeElement(&key, &start); err != nil {
			return fmt.Errorf("decode app Info.plist key: %w", err)
		}
		if key != "LSMinimumSystemVersion" {
			continue
		}
		if found != "" {
			return errors.New("app Info.plist contains duplicate LSMinimumSystemVersion keys")
		}
		for {
			token, err := decoder.Token()
			if err != nil {
				return errors.New("app Info.plist LSMinimumSystemVersion has no string value")
			}
			valueStart, ok := token.(xml.StartElement)
			if !ok {
				continue
			}
			if valueStart.Name.Local != "string" {
				return errors.New("app Info.plist LSMinimumSystemVersion must be a string")
			}
			if err := decoder.DecodeElement(&found, &valueStart); err != nil {
				return fmt.Errorf("decode app Info.plist LSMinimumSystemVersion: %w", err)
			}
			break
		}
	}
	if found == "" {
		return errors.New("app Info.plist LSMinimumSystemVersion is required")
	}
	actualVersion, err := parseMachOVersion(found)
	if err != nil {
		return fmt.Errorf("app Info.plist LSMinimumSystemVersion: %w", err)
	}
	expectedVersion, err := parseMachOVersion(expected)
	if err != nil {
		return err
	}
	if actualVersion != expectedVersion {
		return fmt.Errorf("%w: app minimum OS is %s, SeekDB requires %s", ErrArtifactIntegrity, found, expected)
	}
	return nil
}

func isCleanArchivePath(value string) bool {
	return value != "" &&
		value != "." &&
		value != ".." &&
		!path.IsAbs(value) &&
		path.Clean(value) == value &&
		!strings.HasPrefix(value, "../") &&
		!strings.Contains(value, "\\") &&
		!strings.ContainsRune(value, 0)
}

func validateMinimumOSVersion(value string) error {
	if err := validateNumericVersion(value); err != nil {
		return fmt.Errorf("SeekDB artifact minimum OS %w", err)
	}
	return nil
}

func catalogReleaseURLs(tag string) (releaseURL, sourceURL, licenseURL, noticeURL string) {
	licenseURL = "https://raw.githubusercontent.com/oceanbase/seekdb/" + tag + "/LICENSE"
	noticeURL = "https://raw.githubusercontent.com/oceanbase/seekdb/" + tag + "/NOTICE"
	if isGitCommit(tag) {
		return "https://github.com/oceanbase/seekdb/commit/" + tag,
			"https://github.com/oceanbase/seekdb/archive/" + tag + ".tar.gz",
			licenseURL,
			noticeURL
	}
	return "https://github.com/oceanbase/seekdb/releases/tag/" + tag,
		"https://github.com/oceanbase/seekdb/releases/download/" + tag + "/",
		licenseURL,
		noticeURL
}

func isGitCommit(value string) bool {
	if len(value) != 40 {
		return false
	}
	for _, char := range value {
		digit := char >= '0' && char <= '9'
		hex := char >= 'a' && char <= 'f'
		if !digit && !hex {
			return false
		}
	}
	return true
}

func validateOfficialSourceURL(raw string) error {
	if strings.HasPrefix(raw, "https://github.com/oceanbase/seekdb/releases/download/") ||
		strings.HasPrefix(raw, "https://github.com/oceanbase/seekdb/archive/") {
		return validateOfficialURL("sourceURL", raw, strings.TrimSuffix(raw, path.Base(raw)))
	}
	return fmt.Errorf("SeekDB artifact %s must be a versioned official HTTPS URL", "sourceURL")
}

func validateOfficialProvenanceURL(raw string) error {
	if strings.HasPrefix(raw, "https://github.com/oceanbase/seekdb/releases/tag/") ||
		strings.HasPrefix(raw, "https://github.com/oceanbase/seekdb/commit/") {
		return validateOfficialURL("provenanceURL", raw, raw)
	}
	return fmt.Errorf("SeekDB artifact %s must be a versioned official HTTPS URL", "provenanceURL")
}

func validateOfficialURL(name, raw, prefix string) error {
	parsed, err := url.ParseRequestURI(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || !strings.HasPrefix(raw, prefix) {
		return fmt.Errorf("SeekDB artifact %s must be a versioned official HTTPS URL", name)
	}
	if strings.Contains(strings.ToLower(raw), "/latest") {
		return fmt.Errorf("SeekDB artifact %s must not use latest", name)
	}
	return nil
}

func validatePlatform(goos, goarch string) error {
	if goos == "" || goarch == "" || goos != strings.TrimSpace(goos) || goarch != strings.TrimSpace(goarch) || strings.ContainsRune(goos+goarch, 0) {
		return errors.New("SeekDB artifact GOOS and GOARCH are required and must be clean")
	}
	if strings.ContainsAny(goos+goarch, "/\\") {
		return errors.New("SeekDB artifact GOOS and GOARCH must not contain path separators")
	}
	return nil
}
