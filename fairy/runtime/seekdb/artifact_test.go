package seekdb

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuiltinArtifactCatalogRecordsTargetMatrix(t *testing.T) {
	catalog, err := BuiltinArtifactCatalog()
	if err != nil {
		t.Fatal(err)
	}
	wantTargets := map[string]ArtifactStatus{
		"darwin/arm64":  ArtifactStatusCandidate,
		"darwin/amd64":  ArtifactStatusUnsupported,
		"linux/arm64":   ArtifactStatusUnsupported,
		"linux/amd64":   ArtifactStatusUnsupported,
		"windows/arm64": ArtifactStatusUnsupported,
		"windows/amd64": ArtifactStatusUnsupported,
	}
	if len(catalog.Targets) != len(wantTargets) {
		t.Fatalf("len(Targets) = %d", len(catalog.Targets))
	}
	for _, recorded := range catalog.Targets {
		key := recorded.GOOS + "/" + recorded.GOARCH
		if want, exists := wantTargets[key]; !exists || recorded.Status != want {
			t.Fatalf("unexpected target %s = %q", key, recorded.Status)
		}
		delete(wantTargets, key)
	}
	if len(wantTargets) != 0 {
		t.Fatalf("missing targets = %v", wantTargets)
	}
	target, err := catalog.Target("darwin", "arm64")
	if err != nil {
		t.Fatal(err)
	}
	if target.Status != ArtifactStatusCandidate || target.Artifact == nil {
		t.Fatalf("darwin/arm64 target = %+v", target)
	}
	if catalog.ReleaseTag != "8c2bd7064084d985e3a9c5b8368976ffef8e8394" || target.Artifact.Version != "8c2bd7064084d985e3a9c5b8368976ffef8e8394" || target.Artifact.License != "Apache-2.0" {
		t.Fatalf("darwin/arm64 artifact = %+v", target.Artifact)
	}
	if target.Artifact.LibraryPath != "libseekdb.dylib" || len(target.Artifact.ExternalDependencies) != 0 || len(target.Artifact.RequiredPaths) != 1 {
		t.Fatalf("darwin/arm64 runtime closure = %+v", target.Artifact)
	}
	if target.Artifact.MinimumOSVersion != "15.0" {
		t.Fatalf("minimum OS = %q", target.Artifact.MinimumOSVersion)
	}
	if _, err := catalog.Verified("darwin", "arm64"); !errors.Is(err, ErrArtifactCandidate) {
		t.Fatalf("Verified(darwin/arm64) error = %v", err)
	}
	if _, err := catalog.Verified("darwin", "amd64"); !errors.Is(err, ErrArtifactUnsupported) {
		t.Fatalf("Verified(darwin/amd64) error = %v", err)
	}
}

func TestArtifactCatalogRejectsUnsafeOrAmbiguousMetadata(t *testing.T) {
	validArtifact := fixtureArtifact("payload")
	tests := []struct {
		name    string
		catalog string
		want    string
	}{
		{name: "unknown field", catalog: `{"schemaVersion":1,"product":"seekdb","releaseTag":"v1.0.0","releaseURL":"https://github.com/oceanbase/seekdb/releases/tag/v1.0.0","targets":[],"extra":true}`, want: "unknown field"},
		{name: "duplicate target", catalog: catalogJSON(validArtifact, targetJSON("candidate", validArtifact), targetJSON("candidate", validArtifact)), want: "duplicated"},
		{name: "candidate without artifact", catalog: catalogJSON(validArtifact, `{"goos":"darwin","goarch":"arm64","status":"candidate","reason":"pending"}`), want: "requires artifact"},
		{name: "unsupported with artifact", catalog: catalogJSON(validArtifact, targetJSON("unsupported", validArtifact)), want: "must not contain artifact"},
		{name: "bad digest", catalog: catalogJSON(validArtifact, targetJSON("candidate", strings.Replace(validArtifact, fixtureDigest("payload"), "abc", 1))), want: "64 lowercase"},
		{name: "absolute library", catalog: catalogJSON(validArtifact, targetJSON("candidate", strings.Replace(validArtifact, `"libseekdb.dylib"`, `"/usr/lib/libseekdb.dylib"`, 1))), want: "clean relative"},
		{name: "parent library", catalog: catalogJSON(validArtifact, targetJSON("candidate", strings.Replace(validArtifact, `"libseekdb.dylib"`, `"../libseekdb.dylib"`, 1))), want: "clean relative"},
		{name: "NUL library", catalog: catalogJSON(validArtifact, targetJSON("candidate", strings.Replace(validArtifact, `"libseekdb.dylib"`, `"libseekdb.dylib\u0000"`, 1))), want: "required and must be clean"},
		{name: "library omitted from tree", catalog: catalogJSON(validArtifact, targetJSON("candidate", strings.Replace(validArtifact, `["libseekdb.dylib","LICENSE"]`, `["LICENSE"]`, 1))), want: "include the library"},
		{name: "parent required path", catalog: catalogJSON(validArtifact, targetJSON("candidate", strings.Replace(validArtifact, `"LICENSE"`, `"../LICENSE"`, 1))), want: "clean relative"},
		{name: "missing minimum OS", catalog: catalogJSON(validArtifact, targetJSON("candidate", strings.Replace(validArtifact, `"minimumOSVersion":"15.0"`, `"minimumOSVersion":""`, 1))), want: "required and must be clean"},
		{name: "invalid minimum OS", catalog: catalogJSON(validArtifact, targetJSON("candidate", strings.Replace(validArtifact, `"minimumOSVersion":"15.0"`, `"minimumOSVersion":"macos15"`, 1))), want: "numeric components"},
		{name: "insecure source", catalog: catalogJSON(validArtifact, targetJSON("candidate", strings.Replace(validArtifact, "https://github.com/oceanbase/seekdb/releases/download/v1.0.0/seekdb-1.0.0.tar.gz", "http://example.test/seekdb.tar.gz", 1))), want: "versioned official HTTPS"},
		{name: "latest source", catalog: catalogJSON(validArtifact, targetJSON("candidate", strings.Replace(validArtifact, "/download/v1.0.0/", "/download/latest/", 1))), want: "must not use latest"},
		{name: "wrong source repository", catalog: catalogJSON(validArtifact, targetJSON("candidate", strings.Replace(validArtifact, "oceanbase/seekdb", "example/seekdb", 1))), want: "versioned official HTTPS"},
		{name: "release mismatch", catalog: catalogJSON(validArtifact, targetJSON("candidate", strings.Replace(validArtifact, "/download/v1.0.0/", "/download/v1.1.0/", 1))), want: "release tag does not match"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ParseArtifactCatalog(strings.NewReader(test.catalog))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ParseArtifactCatalog() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestRuntimeArtifactVerify(t *testing.T) {
	artifact := fixtureRuntimeArtifact("verified payload")
	if err := artifact.Verify(strings.NewReader("verified payload")); err != nil {
		t.Fatal(err)
	}
	if err := artifact.Verify(strings.NewReader("changed payload!")); !errors.Is(err, ErrArtifactIntegrity) {
		t.Fatalf("Verify(changed) error = %v", err)
	}
	artifact.Size++
	if err := artifact.Verify(strings.NewReader("verified payload")); !errors.Is(err, ErrArtifactIntegrity) {
		t.Fatalf("Verify(size) error = %v", err)
	}
}

func TestArtifactCatalogVerifiedRequiresExplicitVerifiedStatus(t *testing.T) {
	artifactJSON := fixtureArtifact("payload")
	catalog, err := ParseArtifactCatalog(strings.NewReader(catalogJSON(artifactJSON, targetJSON("verified", artifactJSON))))
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := catalog.Verified("darwin", "arm64")
	if err != nil {
		t.Fatal(err)
	}
	if artifact.SHA256 != fixtureDigest("payload") {
		t.Fatalf("Verified() = %+v", artifact)
	}
}

func TestArtifactCatalogVerifyBundleChecksArchiveAndLicenseDocuments(t *testing.T) {
	artifact := fixtureRuntimeArtifact("payload")
	catalog := ArtifactCatalog{
		SchemaVersion: 1,
		Product:       "seekdb",
		ReleaseTag:    "v1.0.0",
		ReleaseURL:    "https://github.com/oceanbase/seekdb/releases/tag/v1.0.0",
		Targets: []ArtifactTarget{{
			GOOS: "darwin", GOARCH: "arm64", Status: ArtifactStatusVerified,
			Reason: "fixture", Artifact: &artifact,
		}},
	}
	if err := catalog.Validate(); err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	bundle := ArtifactBundle{
		LibraryPath: filepath.Join(directory, "libseekdb.dylib"),
		LicensePath: filepath.Join(directory, "LICENSE"),
		NoticePath:  filepath.Join(directory, "NOTICE"),
	}
	for filename, content := range map[string]string{
		bundle.LibraryPath: "payload",
		bundle.LicensePath: "license",
		bundle.NoticePath:  "notice",
	} {
		if err := os.WriteFile(filename, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := catalog.VerifyBundle("darwin", "arm64", bundle); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bundle.NoticePath, []byte("modified"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := catalog.VerifyBundle("darwin", "arm64", bundle); !errors.Is(err, ErrArtifactIntegrity) || !strings.Contains(err.Error(), "NOTICE") {
		t.Fatalf("VerifyBundle(modified NOTICE) error = %v", err)
	}
}

func fixtureRuntimeArtifact(payload string) RuntimeArtifact {
	return RuntimeArtifact{
		Version:          "1.0.0",
		SourceURL:        "https://github.com/oceanbase/seekdb/releases/download/v1.0.0/seekdb-1.0.0.tar.gz",
		ProvenanceURL:    "https://github.com/oceanbase/seekdb/releases/tag/v1.0.0",
		SHA256:           fixtureDigest(payload),
		Size:             int64(len(payload)),
		License:          "Apache-2.0",
		LicenseURL:       "https://raw.githubusercontent.com/oceanbase/seekdb/v1.0.0/LICENSE",
		LicenseSHA256:    fixtureDigest("license"),
		NoticeURL:        "https://raw.githubusercontent.com/oceanbase/seekdb/v1.0.0/NOTICE",
		NoticeSHA256:     fixtureDigest("notice"),
		ArchiveFormat:    "tar.gz",
		LibraryPath:      "libseekdb.dylib",
		RequiredPaths:    []string{"libseekdb.dylib", "LICENSE"},
		MinimumOSVersion: "15.0",
	}
}

func fixtureArtifact(payload string) string {
	artifact := fixtureRuntimeArtifact(payload)
	return fmt.Sprintf(`{"version":%q,"sourceURL":%q,"provenanceURL":%q,"sha256":%q,"size":%d,"license":%q,"licenseURL":%q,"licenseSHA256":%q,"noticeURL":%q,"noticeSHA256":%q,"archiveFormat":%q,"libraryPath":%q,"requiredPaths":[%q,%q],"externalDependencies":[],"minimumOSVersion":%q}`,
		artifact.Version, artifact.SourceURL, artifact.ProvenanceURL, artifact.SHA256, artifact.Size, artifact.License, artifact.LicenseURL, artifact.LicenseSHA256, artifact.NoticeURL, artifact.NoticeSHA256, artifact.ArchiveFormat, artifact.LibraryPath, artifact.RequiredPaths[0], artifact.RequiredPaths[1], artifact.MinimumOSVersion)
}

func targetJSON(status, artifact string) string {
	return fmt.Sprintf(`{"goos":"darwin","goarch":"arm64","status":%q,"reason":"fixture","artifact":%s}`, status, artifact)
}

func catalogJSON(_ string, targets ...string) string {
	return fmt.Sprintf(`{"schemaVersion":1,"product":"seekdb","releaseTag":"v1.0.0","releaseURL":"https://github.com/oceanbase/seekdb/releases/tag/v1.0.0","targets":[%s]}`, strings.Join(targets, ","))
}

func fixtureDigest(payload string) string {
	digest := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(digest[:])
}
