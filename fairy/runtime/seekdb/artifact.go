package seekdb

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"strings"
)

const artifactCatalogSchemaVersion = 1

var (
	ErrArtifactCandidate   = errors.New("SeekDB runtime artifact is not release-verified")
	ErrArtifactIntegrity   = errors.New("SeekDB runtime artifact integrity check failed")
	ErrArtifactUnsupported = errors.New("SeekDB runtime artifact is unsupported")

	//go:embed artifacts.json
	builtinArtifactCatalog []byte
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
	Version              string   `json:"version"`
	SourceURL            string   `json:"sourceURL"`
	ProvenanceURL        string   `json:"provenanceURL"`
	SHA256               string   `json:"sha256"`
	Size                 int64    `json:"size"`
	License              string   `json:"license"`
	LicenseURL           string   `json:"licenseURL"`
	LicenseSHA256        string   `json:"licenseSHA256"`
	NoticeURL            string   `json:"noticeURL"`
	NoticeSHA256         string   `json:"noticeSHA256"`
	ArchiveFormat        string   `json:"archiveFormat"`
	LibraryPath          string   `json:"libraryPath"`
	RequiredPaths        []string `json:"requiredPaths"`
	ExternalDependencies []string `json:"externalDependencies"`
	MinimumOSVersion     string   `json:"minimumOSVersion"`
}

// ArtifactBundle names the release inputs that must be checked before a
// platform package can embed SeekDB. The paths are build inputs, not runtime
// configuration.
type ArtifactBundle struct {
	LibraryPath string
	LicensePath string
	NoticePath  string
}

func BuiltinArtifactCatalog() (ArtifactCatalog, error) {
	return ParseArtifactCatalog(strings.NewReader(string(builtinArtifactCatalog)))
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
	for name, filename := range map[string]string{
		"library": bundle.LibraryPath,
		"LICENSE": bundle.LicensePath,
		"NOTICE":  bundle.NoticePath,
	} {
		if filename == "" || filename != strings.TrimSpace(filename) || strings.ContainsRune(filename, 0) {
			return fmt.Errorf("SeekDB %s build input path is required and must be clean", name)
		}
	}
	if err := artifact.VerifyFile(bundle.LibraryPath); err != nil {
		return err
	}
	if err := artifact.VerifyLicenseFile(bundle.LicensePath); err != nil {
		return err
	}
	return artifact.VerifyNoticeFile(bundle.NoticePath)
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
		return t.Artifact.validate()
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
		decoded, err := hex.DecodeString(digest.value)
		if err != nil || len(decoded) != sha256.Size || strings.ToLower(digest.value) != digest.value {
			return fmt.Errorf("SeekDB artifact %s must be 64 lowercase hexadecimal characters", digest.name)
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
	parts := strings.Split(value, ".")
	if len(parts) < 2 || len(parts) > 4 {
		return errors.New("SeekDB artifact minimum OS version must contain two to four numeric components")
	}
	for _, part := range parts {
		if part == "" {
			return errors.New("SeekDB artifact minimum OS version must contain only numeric components")
		}
		for _, char := range part {
			if char < '0' || char > '9' {
				return errors.New("SeekDB artifact minimum OS version must contain only numeric components")
			}
		}
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
