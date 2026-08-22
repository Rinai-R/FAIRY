package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"fairy/app/edge"
)

const packagedSeekDBRuntimeMarker = "host-completed"

type packagedSeekDBRuntimeVerifier func(context.Context, string) error

func runPackageVerificationCommand(args []string) (bool, error) {
	if len(args) == 0 || !strings.HasPrefix(args[0], "--verify-") {
		return false, nil
	}
	switch args[0] {
	case "--verify-package-layout":
		if len(args) != 1 {
			return true, errors.New("--verify-package-layout does not accept arguments")
		}
		return true, verifyCurrentPackageLayout()
	case "--verify-seekdb-runtime":
		if len(args) != 2 {
			return true, errors.New("--verify-seekdb-runtime requires one private empty directory")
		}
		return true, verifyCurrentPackagedSeekDBRuntime(args[1], edge.VerifyPackagedSeekDBRuntime)
	case "--verify-endpoint-readiness":
		if len(args) > 2 || (len(args) == 2 && args[1] != "--require-openserp") {
			return true, errors.New("--verify-endpoint-readiness accepts only optional --require-openserp")
		}
		return true, verifyCurrentEndpointReadiness(len(args) == 2)
	default:
		return true, fmt.Errorf("unsupported package verification command %q", args[0])
	}
}

func verifyCurrentEndpointReadiness(requireOpenSERP bool) (retErr error) {
	// This command is evidence about the assembled App, so reject an invalid
	// package before opening the user's real endpoint-strict profile.
	if err := verifyCurrentPackageLayout(); err != nil {
		return err
	}
	profileDir, err := desktopProfileDir()
	if err != nil {
		return fmt.Errorf("resolve endpoint profile: %w", err)
	}
	guard, err := acquireInstanceLock(profileDir, nil)
	if err != nil {
		return fmt.Errorf("acquire endpoint profile: %w", err)
	}
	defer func() {
		retErr = errors.Join(retErr, guard.Close())
	}()

	openCtx, cancelOpen := context.WithTimeout(context.Background(), 45*time.Second)
	runtime, err := defaultOpenEdge(openCtx, profileDir)
	cancelOpen()
	if err != nil {
		return fmt.Errorf("open endpoint-strict runtime: %w", err)
	}
	defer func() {
		closeCtx, cancelClose := context.WithTimeout(context.Background(), defaultRuntimeShutdownLimit)
		retErr = errors.Join(retErr, runtime.Close(closeCtx))
		cancelClose()
	}()
	host := runtime.Management()
	if host == nil {
		return errors.New("endpoint management status is unavailable")
	}
	queryCtx, cancelQuery := context.WithTimeout(context.Background(), managementQueryLimit)
	overview, err := host.Overview(queryCtx)
	cancelQuery()
	if err != nil {
		return fmt.Errorf("read endpoint readiness: %w", err)
	}
	return validateEndpointReadiness(overview, requireOpenSERP)
}

func validateEndpointReadiness(overview edge.Overview, requireOpenSERP bool) error {
	if overview.Profile != string(edge.ProfileEndpointStrict) {
		return errors.New("endpoint readiness requires the endpoint-strict runtime profile")
	}
	if !overview.Storage.Ready || overview.Storage.Storage != "seekdb" {
		return errors.New("endpoint readiness requires ready embedded SeekDB storage")
	}
	if !overview.SecretKey.Ready {
		return errors.New("endpoint readiness requires encrypted local secret storage")
	}
	if !overview.Model.Configured || !overview.Model.Ready || !overview.Model.CredentialConfigured {
		return errors.New("endpoint readiness requires a saved third-party chat provider and credential")
	}
	if !overview.Semantic.Enabled || !overview.Semantic.Configured || !overview.Semantic.CredentialConfigured ||
		overview.Semantic.Dimensions != edge.SemanticEmbeddingDimensions {
		return fmt.Errorf("endpoint readiness requires a saved third-party %d-dimensional semantic embedding provider and credential", edge.SemanticEmbeddingDimensions)
	}
	if requireOpenSERP && (!overview.WebSearch.Enabled || !overview.WebSearch.Ready) {
		return errors.New("endpoint readiness requires saved and reachable OpenSERP configuration for this scenario")
	}
	return nil
}

func verifyCurrentPackageLayout() error {
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve packaged executable: %w", err)
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return fmt.Errorf("resolve packaged executable links: %w", err)
	}
	macOSDir := filepath.Dir(executable)
	contentsDir := filepath.Dir(macOSDir)
	if filepath.Base(macOSDir) != "MacOS" || filepath.Base(contentsDir) != "Contents" {
		return errors.New("package verifier must run from FAIRY.app/Contents/MacOS")
	}
	library, err := edge.LocatePackagedSeekDBLibrary()
	if err != nil {
		return fmt.Errorf("locate packaged SeekDB: %w", err)
	}
	return verifyPackageLayout(contentsDir, executable, library)
}

func verifyCurrentPackagedSeekDBRuntime(root string, verify packagedSeekDBRuntimeVerifier) error {
	return verifyCurrentPackagedSeekDBRuntimeWith(root, verifyCurrentPackageLayout, verify)
}

func verifyCurrentPackagedSeekDBRuntimeWith(root string, verifyLayout func() error, verify packagedSeekDBRuntimeVerifier) error {
	if verifyLayout == nil {
		return errors.New("package layout verifier is required")
	}
	if verify == nil {
		return errors.New("packaged SeekDB runtime verifier is required")
	}
	// Repeat the immutable layout/catalog gate in this mode so direct callers
	// cannot exercise an unverified or externally located dylib.
	if err := verifyLayout(); err != nil {
		return err
	}
	verifyCtx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	err := verify(verifyCtx, root)
	cancel()
	if err != nil {
		return err
	}
	marker := filepath.Join(root, packagedSeekDBRuntimeMarker)
	file, err := os.OpenFile(marker, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create packaged SeekDB host-completion marker: %w", err)
	}
	if _, err := file.WriteString("completed\n"); err != nil {
		_ = file.Close()
		return fmt.Errorf("write packaged SeekDB host-completion marker: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync packaged SeekDB host-completion marker: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close packaged SeekDB host-completion marker: %w", err)
	}
	return nil
}

func verifyPackageLayout(contentsDir, executable, library string) error {
	return verifyPackageLayoutWithArtifactVerifier(contentsDir, executable, library, verifyPackagedSeekDBArtifact)
}

type packageArtifactVerifier func(edge.PackagedSeekDBArtifact) error

func verifyPackageLayoutWithArtifactVerifier(contentsDir, executable, library string, verifyArtifact packageArtifactVerifier) error {
	contentsDir = filepath.Clean(contentsDir)
	expectedExecutable := filepath.Join(contentsDir, "MacOS", "FAIRY")
	expectedLibrary := filepath.Join(contentsDir, "Frameworks", "libseekdb.dylib")
	if filepath.Clean(executable) != expectedExecutable {
		return fmt.Errorf("packaged executable is %q, expected %q", executable, expectedExecutable)
	}
	if filepath.Clean(library) != expectedLibrary {
		return fmt.Errorf("packaged SeekDB is %q, expected %q", library, expectedLibrary)
	}
	requiredFiles := []string{
		expectedExecutable,
		expectedLibrary,
		filepath.Join(contentsDir, "Info.plist"),
		filepath.Join(contentsDir, "Resources", "plugin-host.defaults.json"),
		filepath.Join(contentsDir, "Resources", "plugin-abi", "manifest.v1.json"),
		filepath.Join(contentsDir, "Resources", "plugin-abi", "envelope.v1.json"),
		filepath.Join(contentsDir, "Resources", "licenses", "SEEKDB-LICENSE"),
		filepath.Join(contentsDir, "Resources", "licenses", "SEEKDB-NOTICE"),
	}
	for _, filename := range requiredFiles {
		info, err := os.Lstat(filename)
		if err != nil {
			return fmt.Errorf("required package file %q: %w", filename, err)
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("required package file %q must be a regular non-symlink", filename)
		}
	}
	forbidden := filepath.Join(contentsDir, "Resources", "plugins")
	if _, err := os.Lstat(forbidden); err == nil {
		return errors.New("strict endpoint package must not contain a builtin plugins directory")
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect forbidden package path: %w", err)
	}
	verifyCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := edge.VerifyInstalledPluginReleaseInventory(verifyCtx, filepath.Join(contentsDir, "Resources", "plugin-release")); err != nil {
		return fmt.Errorf("verify packaged WASM inventory: %w", err)
	}
	if verifyArtifact == nil {
		return errors.New("packaged SeekDB artifact verifier is required")
	}
	if err := verifyArtifact(edge.PackagedSeekDBArtifact{
		LibraryPath:      expectedLibrary,
		LicensePath:      filepath.Join(contentsDir, "Resources", "licenses", "SEEKDB-LICENSE"),
		NoticePath:       filepath.Join(contentsDir, "Resources", "licenses", "SEEKDB-NOTICE"),
		AppInfoPlistPath: filepath.Join(contentsDir, "Info.plist"),
	}); err != nil {
		return fmt.Errorf("verify packaged SeekDB artifact: %w", err)
	}
	if err := verifyPackageRuntimeClosure(contentsDir, executable, library); err != nil {
		return fmt.Errorf("verify packaged runtime closure: %w", err)
	}
	return nil
}

func verifyPackagedSeekDBArtifact(bundle edge.PackagedSeekDBArtifact) error {
	return edge.VerifyPackagedSeekDBArtifact(bundle)
}
