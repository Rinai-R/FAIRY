package main

import (
	"context"
	"debug/macho"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fairy/app/edge"
	"fairy/runtime/config"
	"fairy/runtime/wasm"
	api "fairy/transport/web"
)

func TestPackagedSeekDBRuntimeVerificationWritesMarkerOnlyAfterHostReturns(t *testing.T) {
	root := t.TempDir()
	called := false
	err := verifyCurrentPackagedSeekDBRuntimeWith(root, func() error {
		return nil
	}, func(ctx context.Context, gotRoot string) error {
		called = true
		if gotRoot != root {
			t.Fatalf("verification root = %q, want %q", gotRoot, root)
		}
		if err := ctx.Err(); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(root, packagedSeekDBRuntimeMarker)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("marker existed before verifier returned: %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("packaged SeekDB runtime verifier was not called")
	}
	marker, err := os.ReadFile(filepath.Join(root, packagedSeekDBRuntimeMarker))
	if err != nil {
		t.Fatal(err)
	}
	if string(marker) != "completed\n" {
		t.Fatalf("marker = %q", marker)
	}
}

func TestPackagedSeekDBRuntimeVerificationFailsClosedBeforeMarker(t *testing.T) {
	sentinel := errors.New("embedded runtime exited")
	for _, test := range []struct {
		name        string
		layout      func() error
		runtime     packagedSeekDBRuntimeVerifier
		want        error
		wantRuntime bool
	}{
		{
			name:   "layout",
			layout: func() error { return sentinel },
			runtime: func(context.Context, string) error {
				t.Fatal("runtime verifier ran after layout failure")
				return nil
			},
			want: sentinel,
		},
		{
			name:   "runtime",
			layout: func() error { return nil },
			runtime: func(context.Context, string) error {
				return sentinel
			},
			want:        sentinel,
			wantRuntime: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			err := verifyCurrentPackagedSeekDBRuntimeWith(root, test.layout, test.runtime)
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if _, statErr := os.Stat(filepath.Join(root, packagedSeekDBRuntimeMarker)); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("failed verification wrote marker: %v", statErr)
			}
		})
	}
}

func TestPackageVerificationCommandRejectsMalformedAndUnknownModes(t *testing.T) {
	if handled, err := runPackageVerificationCommand(nil); handled || err != nil {
		t.Fatalf("ordinary launch = (%v, %v)", handled, err)
	}
	for _, args := range [][]string{
		{"--verify-package-layout", "extra"},
		{"--verify-seekdb-runtime"},
		{"--verify-seekdb-runtime", "/tmp/one", "extra"},
		{"--verify-endpoint-readiness", "--unknown"},
		{"--verify-endpoint-readiness", "--require-openserp", "extra"},
		{"--verify-unknown"},
	} {
		if handled, err := runPackageVerificationCommand(args); !handled || err == nil {
			t.Fatalf("runPackageVerificationCommand(%q) = (%v, %v), want handled error", args, handled, err)
		}
	}
}

func TestEndpointReadinessRequiresSanitizedProductionCapabilities(t *testing.T) {
	ready := edge.Overview{
		Profile:   string(edge.ProfileEndpointStrict),
		Storage:   api.StorageStatus{Ready: true, Mode: "production", Storage: "seekdb"},
		SecretKey: edge.SecretKeyStatus{Ready: true, Mode: "production"},
		Model: edge.ModelStatus{
			Configured: true, Ready: true, CredentialConfigured: true,
			Endpoint: "https://chat-private.example", Model: "private-chat-model", Reason: "private-chat-reason",
		},
		Semantic: edge.SemanticStatus{
			Provider: config.SemanticEmbeddingProviderOpenAICompatible, Enabled: true, Configured: true,
			CredentialConfigured: true, Dimensions: config.SemanticEmbeddingDimensions,
			Endpoint: "https://embedding-private.example", Model: "private-embedding-model", Reason: "private-embedding-reason",
		},
		WebSearch: edge.WebSearchStatus{Enabled: true, Ready: true, BaseURL: "https://openserp-private.example"},
	}
	if err := validateEndpointReadiness(ready, true); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name     string
		mutate   func(*edge.Overview)
		require  bool
		wantText string
	}{
		{name: "profile", mutate: func(got *edge.Overview) { got.Profile = "full" }, wantText: "endpoint-strict"},
		{name: "storage", mutate: func(got *edge.Overview) { got.Storage.Ready = false }, wantText: "SeekDB"},
		{name: "secret_store", mutate: func(got *edge.Overview) { got.SecretKey.Ready = false }, wantText: "secret storage"},
		{name: "chat_configuration", mutate: func(got *edge.Overview) { got.Model.Configured = false }, wantText: "chat provider"},
		{name: "chat_credential", mutate: func(got *edge.Overview) { got.Model.CredentialConfigured = false }, wantText: "chat provider"},
		{name: "semantic_configuration", mutate: func(got *edge.Overview) { got.Semantic.Configured = false }, wantText: "semantic embedding"},
		{name: "semantic_dimensions", mutate: func(got *edge.Overview) { got.Semantic.Dimensions = 512 }, wantText: "1024-dimensional"},
		{name: "openserp", mutate: func(got *edge.Overview) { got.WebSearch.Ready = false }, require: true, wantText: "OpenSERP"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := ready
			test.mutate(&got)
			err := validateEndpointReadiness(got, test.require)
			if err == nil || !strings.Contains(err.Error(), test.wantText) {
				t.Fatalf("validateEndpointReadiness() error = %v, want %q", err, test.wantText)
			}
			for _, private := range []string{got.Model.Endpoint, got.Model.Model, got.Model.Reason, got.Semantic.Endpoint, got.Semantic.Model, got.Semantic.Reason, got.WebSearch.BaseURL} {
				if private != "" && strings.Contains(err.Error(), private) {
					t.Fatalf("readiness error leaked private status %q: %v", private, err)
				}
			}
		})
	}

	withoutOpenSERP := ready
	withoutOpenSERP.WebSearch = edge.WebSearchStatus{}
	if err := validateEndpointReadiness(withoutOpenSERP, false); err != nil {
		t.Fatalf("optional OpenSERP = %v", err)
	}
}

func TestVerifyPackageLayoutAcceptsOnlyRealBundledRuntimeInputs(t *testing.T) {
	contents := filepath.Join(t.TempDir(), "FAIRY.app", "Contents")
	executable, library := createPackageFixture(t, contents)
	if err := verifyPackageFixtureLayout(contents, executable, library); err != nil {
		t.Fatal(err)
	}

	plugins := filepath.Join(contents, "Resources", "plugins")
	if err := os.MkdirAll(plugins, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := verifyPackageFixtureLayout(contents, executable, library); err == nil {
		t.Fatal("verifyPackageLayout() accepted a manifest-only builtin plugin directory")
	}
	if err := os.Remove(plugins); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(library); err != nil {
		t.Fatal(err)
	}
	externalLibrary := filepath.Join(t.TempDir(), "libseekdb.dylib")
	if err := os.WriteFile(externalLibrary, []byte("external"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(externalLibrary, library); err != nil {
		t.Fatal(err)
	}
	if err := verifyPackageFixtureLayout(contents, executable, library); err == nil {
		t.Fatal("verifyPackageLayout() accepted a symlinked SeekDB")
	}
}

func TestVerifyPackageLayoutRequiresFinalSeekDBArtifactVerification(t *testing.T) {
	contents := filepath.Join(t.TempDir(), "FAIRY.app", "Contents")
	executable, library := createPackageFixture(t, contents)
	if err := verifyPackageLayoutWithArtifactVerifier(contents, executable, library, nil); err == nil || !strings.Contains(err.Error(), "artifact verifier is required") {
		t.Fatalf("missing artifact verifier error = %v", err)
	}
	sentinel := errors.New("artifact drift")
	called := false
	err := verifyPackageLayoutWithArtifactVerifier(contents, executable, library, func(bundle edge.PackagedSeekDBArtifact) error {
		called = true
		if bundle.LibraryPath != library || bundle.LicensePath != filepath.Join(contents, "Resources", "licenses", "SEEKDB-LICENSE") ||
			bundle.NoticePath != filepath.Join(contents, "Resources", "licenses", "SEEKDB-NOTICE") || bundle.AppInfoPlistPath != filepath.Join(contents, "Info.plist") {
			t.Fatalf("packaged artifact bundle = %+v", bundle)
		}
		return sentinel
	})
	if !called || !errors.Is(err, sentinel) {
		t.Fatalf("artifact verifier result = (called=%v, err=%v)", called, err)
	}
}

func TestVerifyPackageLayoutRejectsUnsealedPluginInventory(t *testing.T) {
	contents := filepath.Join(t.TempDir(), "FAIRY.app", "Contents")
	executable, library := createPackageFixture(t, contents)
	pluginRelease := filepath.Join(contents, "Resources", "plugin-release")
	if err := os.WriteFile(filepath.Join(pluginRelease, "manifest.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifyPackageFixtureLayout(contents, executable, library); err == nil {
		t.Fatal("verifyPackageLayout() accepted an undeclared plugin artifact")
	}
}

func TestVerifyPackageLayoutRejectsHelperExecutableAndRuntimeEnvironment(t *testing.T) {
	t.Run("helper", func(t *testing.T) {
		contents := filepath.Join(t.TempDir(), "FAIRY.app", "Contents")
		executable, library := createPackageFixture(t, contents)
		helper := filepath.Join(contents, "MacOS", "python")
		if err := os.WriteFile(helper, []byte("#!/bin/sh\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := verifyPackageFixtureLayout(contents, executable, library); err == nil {
			t.Fatal("verifyPackageLayout() accepted an undeclared helper executable")
		}
	})

	t.Run("environment", func(t *testing.T) {
		contents := filepath.Join(t.TempDir(), "FAIRY.app", "Contents")
		executable, library := createPackageFixture(t, contents)
		info := `<?xml version="1.0"?><plist><dict><key>LSEnvironment</key><dict><key>FAIRY_MODEL_ENDPOINT</key><string>http://127.0.0.1:11434</string></dict></dict></plist>`
		if err := os.WriteFile(filepath.Join(contents, "Info.plist"), []byte(info), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := verifyPackageFixtureLayout(contents, executable, library); err == nil {
			t.Fatal("verifyPackageLayout() accepted an injected runtime environment")
		}
	})
}

func TestVerifyEndpointExecutableBoundaryRejectsOptionalRuntimeImplementations(t *testing.T) {
	allowed := filepath.Join(t.TempDir(), "FAIRY-allowed")
	if err := os.WriteFile(allowed, []byte("endpoint strict executable fixture"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := verifyEndpointExecutableBoundary(allowed); err != nil {
		t.Fatalf("allowed executable = %v", err)
	}
	for _, test := range []struct {
		name   string
		marker string
		want   string
	}{
		{name: "onebot", marker: "fairy/plugin/qqonebot", want: "OneBot package"},
		{name: "llama_native", marker: "llama.cpp", want: "native Llama runtime"},
		{name: "onnx_runtime", marker: "github.com/yalue/onnxruntime_go", want: "ONNX Runtime"},
		{name: "tensorflow", marker: "github.com/tensorflow/tensorflow", want: "TensorFlow runtime"},
		{name: "tflite", marker: "github.com/mattn/go-tflite", want: "TensorFlow Lite runtime"},
		{name: "libtorch", marker: "libtorch", want: "Torch native inference runtime"},
		{name: "ggml", marker: "github.com/ggerganov/ggml", want: "GGML runtime"},
		{name: "sentence_transformers", marker: "sentence-transformers", want: "Python sentence embedding runtime"},
		{name: "postgres", marker: "github.com/jackc/pgx", want: "PostgreSQL pgx runtime"},
		{name: "mysql", marker: "github.com/go-sql-driver/mysql", want: "MySQL TCP driver"},
		{name: "sqlite", marker: "modernc.org/sqlite", want: "SQLite pure-Go runtime"},
		{name: "qdrant", marker: "github.com/qdrant/go-client", want: "external vector database runtime"},
		{name: "docker", marker: "github.com/docker/docker", want: "Docker runtime"},
	} {
		t.Run(test.name, func(t *testing.T) {
			forbidden := filepath.Join(t.TempDir(), "FAIRY-forbidden")
			if err := os.WriteFile(forbidden, []byte("prefix "+test.marker+" suffix"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := verifyEndpointExecutableBoundary(forbidden); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("forbidden executable = %v", err)
			}
		})
	}
}

func TestImportedMachOLibraryPolicyAllowsOnlySystemAndDeclaredSeekDB(t *testing.T) {
	for _, library := range []string{
		"/usr/lib/libSystem.B.dylib",
		"/System/Library/Frameworks/AppKit.framework/Versions/C/AppKit",
		"@rpath/libseekdb.dylib",
	} {
		if err := validateImportedMachOLibrary(library, true); err != nil {
			t.Fatalf("validateImportedMachOLibrary(%q) = %v", library, err)
		}
	}
	for _, library := range []string{
		"/opt/homebrew/lib/libpython.dylib",
		"/usr/local/lib/libpq.dylib",
		"@loader_path/libonebot.dylib",
		"@rpath/local-model.dylib",
		"@rpath/libseekdb.dylib",
	} {
		allowSeekDB := library != "@rpath/libseekdb.dylib"
		if err := validateImportedMachOLibrary(library, allowSeekDB); err == nil {
			t.Fatalf("validateImportedMachOLibrary(%q) accepted an undeclared dependency", library)
		}
	}
}

func TestVerifyPackageLayoutRejectsExternalSeekDB(t *testing.T) {
	contents := filepath.Join(t.TempDir(), "FAIRY.app", "Contents")
	executable := filepath.Join(contents, "MacOS", "FAIRY")
	external := filepath.Join(t.TempDir(), "libseekdb.dylib")
	if err := verifyPackageFixtureLayout(contents, executable, external); err == nil {
		t.Fatal("verifyPackageLayout() accepted an external SeekDB path")
	}
}

func verifyPackageFixtureLayout(contents, executable, library string) error {
	return verifyPackageLayoutWithArtifactVerifier(contents, executable, library, func(edge.PackagedSeekDBArtifact) error {
		return nil
	})
}

func installEmptyPluginRelease(t *testing.T, destination string) {
	t.Helper()
	sourceRoot := t.TempDir()
	inventoryPath := filepath.Join(sourceRoot, "plugin-inventory.json")
	if err := os.WriteFile(inventoryPath, []byte("{\n  \"schemaVersion\": 1,\n  \"plugins\": []\n}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := wasm.InstallReleaseInventory(t.Context(), inventoryPath, sourceRoot, destination); err != nil {
		t.Fatal(err)
	}
}

func createPackageFixture(t *testing.T, contents string) (string, string) {
	t.Helper()
	executable := filepath.Join(contents, "MacOS", "FAIRY")
	library := filepath.Join(contents, "Frameworks", "libseekdb.dylib")
	files := map[string][]byte{
		filepath.Join(contents, "Info.plist"):                                  []byte(`<?xml version="1.0"?><plist><dict><key>CFBundleExecutable</key><string>FAIRY</string><key>LSMinimumSystemVersion</key><string>15.0.0</string></dict></plist>`),
		filepath.Join(contents, "Resources", "plugin-host.defaults.json"):      []byte(`{"defaultCapabilityGrants":[],"note":"deny by default"}`),
		filepath.Join(contents, "Resources", "plugin-abi", "manifest.v1.json"): []byte(`{}`),
		filepath.Join(contents, "Resources", "plugin-abi", "envelope.v1.json"): []byte(`{}`),
		filepath.Join(contents, "Resources", "licenses", "SEEKDB-LICENSE"):     []byte("license\n"),
		filepath.Join(contents, "Resources", "licenses", "SEEKDB-NOTICE"):      []byte("notice\n"),
	}
	for filename, raw := range files {
		if err := os.MkdirAll(filepath.Dir(filename), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filename, raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeTestMachO(t, executable, macho.TypeExec)
	writeTestMachO(t, library, macho.TypeDylib)
	installEmptyPluginRelease(t, filepath.Join(contents, "Resources", "plugin-release"))
	return executable, library
}

func writeTestMachO(t *testing.T, filename string, fileType macho.Type) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filename), 0o700); err != nil {
		t.Fatal(err)
	}
	header := make([]byte, 32)
	binary.LittleEndian.PutUint32(header[0:4], macho.Magic64)
	binary.LittleEndian.PutUint32(header[4:8], uint32(macho.CpuArm64))
	binary.LittleEndian.PutUint32(header[8:12], 0)
	binary.LittleEndian.PutUint32(header[12:16], uint32(fileType))
	if err := os.WriteFile(filename, header, 0o700); err != nil {
		t.Fatal(err)
	}
}
