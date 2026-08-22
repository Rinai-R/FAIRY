package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"fairy/runtime/seekdb"
	"fairy/runtime/wasm"
)

func TestEndpointStrictBuildAndAppLocatorOmitOptionalRuntimeImplementations(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "FAIRY")
	cache := filepath.Join(t.TempDir(), "go-build")
	cmd := exec.Command("go", "build", "-mod=readonly", "-tags=endpointstrict", "-trimpath", "-ldflags=-w", "-o", binary, ".")
	cmd.Env = append(os.Environ(), "GOCACHE="+cache)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build endpoint-strict Desktop: %v\n%s", err, output)
	}
	raw, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range [][]byte{
		[]byte("fairy/plugin/qqonebot"),
		[]byte("fairy/runtime/config.ReadQQOneBot"),
		[]byte("frontend/qq-onebot"),
		[]byte("ManagementQQ"),
		[]byte("SaveManagementQQ"),
		[]byte("QQBridge"),
	} {
		if bytes.Contains(raw, marker) {
			t.Fatalf("endpoint-strict Desktop binary contains OneBot implementation marker %q", marker)
		}
	}
	if err := verifyEndpointExecutableBoundary(binary); err != nil {
		t.Fatalf("endpoint-strict Desktop executable boundary: %v", err)
	}

	contents := filepath.Join(t.TempDir(), "FAIRY.app", "Contents")
	packagedExecutable, packagedLibrary := createPackageFixture(t, contents)
	if err := os.WriteFile(packagedExecutable, raw, 0o700); err != nil {
		t.Fatal(err)
	}
	copyPackageFixtureFile(t, filepath.Join("build", "seekdb", "libseekdb.dylib"), packagedLibrary, 0o700)
	copyPackageFixtureFile(t, filepath.Join("build", "seekdb", "LICENSE"), filepath.Join(contents, "Resources", "licenses", "SEEKDB-LICENSE"), 0o600)
	copyPackageFixtureFile(t, filepath.Join("build", "seekdb", "NOTICE"), filepath.Join(contents, "Resources", "licenses", "SEEKDB-NOTICE"), 0o600)
	verify := exec.Command(packagedExecutable, "--verify-package-layout")
	verify.Env = []string{
		"HOME=" + t.TempDir(),
		"TMPDIR=" + t.TempDir(),
		"PATH=/usr/bin:/bin:/usr/sbin:/sbin",
	}
	output, verifyErr := verify.CombinedOutput()
	catalog, err := seekdb.BuiltinArtifactCatalog()
	if err != nil {
		t.Fatal(err)
	}
	target, err := catalog.Target("darwin", "arm64")
	if err != nil {
		t.Fatal(err)
	}
	switch target.Status {
	case seekdb.ArtifactStatusVerified:
		if verifyErr != nil {
			t.Fatalf("verify endpoint-strict App locator: %v\n%s", verifyErr, output)
		}
	case seekdb.ArtifactStatusCandidate:
		if verifyErr == nil || !strings.Contains(string(output), seekdb.ErrArtifactCandidate.Error()) {
			t.Fatalf("candidate endpoint-strict App verification = (%v, %s), want release-verified rejection", verifyErr, output)
		}
	default:
		if verifyErr == nil {
			t.Fatalf("unsupported endpoint-strict App verification succeeded: %s", output)
		}
	}
}

func copyPackageFixtureFile(t *testing.T, source, destination string, mode os.FileMode) {
	t.Helper()
	raw, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, raw, mode); err != nil {
		t.Fatal(err)
	}
}

func TestEndpointStrictDependencyGraphOmitsExternalRuntimes(t *testing.T) {
	cmd := exec.Command("go", "list", "-mod=readonly", "-tags=endpointstrict", "-deps", ".")
	cmd.Env = append(os.Environ(), "GOCACHE="+filepath.Join(t.TempDir(), "go-build"))
	raw, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("list endpoint-strict dependency graph: %v\n%s", err, raw)
	}
	forbidden := []string{
		"fairy/plugin/qqonebot",
		"github.com/jackc/pgx",
		"github.com/lib/pq",
		"github.com/go-sql-driver/mysql",
		"github.com/mattn/go-sqlite3",
		"modernc.org/sqlite",
		"github.com/qdrant/go-client",
		"github.com/ollama/ollama",
		"github.com/yalue/onnxruntime_go",
		"github.com/tensorflow/tensorflow",
		"github.com/mattn/go-tflite",
		"github.com/ggerganov/ggml",
		"github.com/docker/docker",
	}
	dependencies := strings.Fields(string(raw))
	for _, dependency := range dependencies {
		for _, prefix := range forbidden {
			if dependency == prefix || strings.HasPrefix(dependency, prefix+"/") {
				t.Fatalf("endpoint-strict dependency graph contains forbidden runtime %q", dependency)
			}
		}
	}
}

func TestEndpointStrictSourceDoesNotReachWailsProcessLaunchingManagers(t *testing.T) {
	forbidden := []string{
		".Browser",
		".Autostart",
		".Updater",
		".OpenFileManager(",
		"Browser.OpenURL",
		"Browser.OpenFile",
		"window.open(",
		"globalThis.open(",
		"wails.Browser",
	}

	scan := func(path string) {
		t.Helper()
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, marker := range forbidden {
			if strings.Contains(string(raw), marker) {
				t.Fatalf("endpoint-strict source %s reaches process-launching Wails capability %q", path, marker)
			}
		}
	}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		scan(entry.Name())
	}

	for _, root := range []string{filepath.Join("frontend", "src"), filepath.Join("frontend", "dist")} {
		err = filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if info.IsDir() || strings.Contains(info.Name(), ".test.") {
				return nil
			}
			switch strings.ToLower(filepath.Ext(path)) {
			case ".js", ".jsx", ".mjs", ".ts", ".tsx":
				scan(path)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestPackagedPluginHostResourcesArePresentAndDeniedByDefault(t *testing.T) {
	root := filepath.Join("build")
	defaultsRaw, err := os.ReadFile(filepath.Join(root, "plugin-host.defaults.json"))
	if err != nil {
		t.Fatal(err)
	}
	var defaults struct {
		DefaultCapabilityGrants []string `json:"defaultCapabilityGrants"`
	}
	if err := json.Unmarshal(defaultsRaw, &defaults); err != nil {
		t.Fatal(err)
	}
	if len(defaults.DefaultCapabilityGrants) != 0 {
		t.Fatalf("default grants = %#v", defaults.DefaultCapabilityGrants)
	}
	for _, relative := range []string{
		filepath.Join("..", "fairy", "plugin", "schema", "manifest.v1.json"),
		filepath.Join("..", "fairy", "plugin", "schema", "envelope.v1.json"),
	} {
		if _, err := os.Stat(filepath.Join(".", relative)); err != nil {
			t.Fatalf("missing packaged plugin resource %s: %v", relative, err)
		}
	}
}

func TestDarwinPackageMetadataDoesNotDeclareMissingOrPlaceholderResources(t *testing.T) {
	for _, filename := range []string{
		filepath.Join("build", "darwin", "Info.plist"),
		filepath.Join("build", "darwin", "Info.dev.plist"),
	} {
		raw, err := os.ReadFile(filename)
		if err != nil {
			t.Fatal(err)
		}
		content := string(raw)
		for _, forbidden := range []string{"CFBundleIconFile", "This is a comment", "My Company", "© now", "<string>true</string>"} {
			if strings.Contains(content, forbidden) {
				t.Fatalf("%s retains missing or placeholder package metadata %q", filename, forbidden)
			}
		}
		for _, required := range []string{"FAIRY 0.1.0", "Copyright © 2026 FAIRY"} {
			if !strings.Contains(content, required) {
				t.Fatalf("%s does not contain release metadata %q", filename, required)
			}
		}
	}
}

func TestEndpointPackageDoesNotAdvertiseManifestOnlyBuiltinPlugins(t *testing.T) {
	inventoryFile, err := os.Open(filepath.Join("build", "plugin-inventory.json"))
	if err != nil {
		t.Fatal(err)
	}
	inventory, err := wasm.ParseReleaseInventory(inventoryFile)
	closeErr := inventoryFile.Close()
	if err != nil {
		t.Fatal(err)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	if len(inventory.Plugins) != 0 {
		t.Fatalf("strict endpoint builtin plugin inventory = %#v, want explicit empty inventory", inventory.Plugins)
	}
	taskfile, err := os.ReadFile("Taskfile.yml")
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		"Contents/Resources/plugins",
		"plugin/websearch/manifest.json",
		"plugin/qqonebot/manifest.json",
		"web-search.manifest.json",
		"qq-onebot.manifest.json",
	} {
		if strings.Contains(string(taskfile), forbidden) {
			t.Fatalf("endpoint package recipe advertises manifest-only plugin resource %q", forbidden)
		}
	}
	if !strings.Contains(string(taskfile), "--app-info ../desktop/build/darwin/Info.plist") {
		t.Fatal("endpoint package recipe does not verify the app minimum OS against SeekDB")
	}
	for _, required := range []string{
		"wasm-release-inventory",
		"--inventory ../desktop/build/plugin-inventory.json",
		"Contents/Resources/plugin-release",
		"{{.BIN_DIR}}/{{.APP_NAME}}.app/Contents/MacOS/{{.APP_NAME}} --verify-package-layout",
		"build/verify-seekdb-runtime-darwin.sh --app {{.BIN_DIR}}/{{.APP_NAME}}.app",
	} {
		if !strings.Contains(string(taskfile), required) {
			t.Fatalf("endpoint package recipe does not seal the formal plugin inventory with %q", required)
		}
	}
	if !strings.Contains(string(taskfile), `MACOS_DEPLOYMENT_TARGET: "15.0"`) || !strings.Contains(string(taskfile), `MACOSX_DEPLOYMENT_TARGET: "{{.MACOS_DEPLOYMENT_TARGET}}"`) || !strings.Contains(string(taskfile), `CGO_LDFLAGS: "-mmacosx-version-min={{.MACOS_DEPLOYMENT_TARGET}}"`) {
		t.Fatal("endpoint build does not pin the binary deployment target to the app minimum OS")
	}
	for _, required := range []string{
		`ENDPOINT_BUILD_TAG: endpointstrict`,
		`GOFLAGS="-tags={{.ENDPOINT_BUILD_TAG}}" wails3 generate bindings`,
		`go test -mod=readonly -tags={{.ENDPOINT_BUILD_TAG}}`,
		`go build -mod=readonly -tags={{.ENDPOINT_BUILD_TAG}} -trimpath`,
	} {
		if !strings.Contains(string(taskfile), required) {
			t.Fatalf("endpoint build does not isolate optional extensions with %q", required)
		}
	}
}

func TestDesktopBuildPinsAndOrdersWailsBindingGeneration(t *testing.T) {
	goMod, err := os.ReadFile("go.mod")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(goMod), "github.com/wailsapp/wails/v3 v3.0.0-alpha2.117") {
		t.Fatal("desktop module does not pin the Wails runtime used by generated bindings")
	}

	taskfile, err := os.ReadFile("Taskfile.yml")
	if err != nil {
		t.Fatal(err)
	}
	content := string(taskfile)
	for _, required := range []string{
		"WAILS_VERSION: v3.0.0-alpha2.117",
		"deps: [bindings]",
		`test "$(wails3 version 2>&1)" = "{{.WAILS_VERSION}}"`,
		"wails3 generate bindings -b -d frontend/bindings",
		"deps: [frontend]",
		"go test -mod=readonly -tags={{.ENDPOINT_BUILD_TAG}} ./...",
		"go build -mod=readonly -tags={{.ENDPOINT_BUILD_TAG}} -trimpath -o {{.BIN_DIR}}/{{.APP_NAME}} .",
	} {
		if !strings.Contains(content, required) {
			t.Fatalf("desktop build does not pin and order generated bindings with %q", required)
		}
	}
	for _, forbidden := range []string{
		"deps: [frontend, bindings]",
	} {
		if strings.Contains(content, forbidden) {
			t.Fatalf("desktop build retains an unordered or PATH-owned binding generator %q", forbidden)
		}
	}
}

func TestDarwinReleaseRequiresNestedSigningNotarizationAndFinalVerification(t *testing.T) {
	script, err := os.ReadFile(filepath.Join("build", "release-darwin.sh"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(script)
	ordered := []string{
		`codesign --force --options runtime --timestamp --sign "$identity" "$library"`,
		`codesign --force --options runtime --timestamp --sign "$identity" "$app"`,
		`xcrun notarytool submit "$submission" --keychain-profile "$notary_profile" --wait --output-format json`,
		`xcrun stapler staple "$app"`,
		`xcrun stapler validate "$app"`,
		`spctl --assess --type execute --verbose=4 "$app"`,
		`ditto -c -k --keepParent "$app" "$output"`,
	}
	position := -1
	for _, required := range ordered {
		next := strings.Index(content, required)
		if next < 0 || next <= position {
			t.Fatalf("release step %q is missing or out of order", required)
		}
		position = next
	}
	staticGate := `"$executable" --verify-package-layout`
	dynamicGate := `"$runtime_verifier" --app "$app"`
	if strings.Count(content, staticGate) != 2 || strings.Count(content, dynamicGate) != 2 {
		t.Fatalf("release must run static and dynamic package gates before signing and after staple")
	}
	firstStatic := strings.Index(content, staticGate)
	firstDynamic := strings.Index(content, dynamicGate)
	signLibrary := strings.Index(content, ordered[0])
	assess := strings.Index(content, `spctl --assess --type execute --verbose=4 "$app"`)
	lastStatic := strings.LastIndex(content, staticGate)
	lastDynamic := strings.LastIndex(content, dynamicGate)
	archive := strings.Index(content, `ditto -c -k --keepParent "$app" "$output"`)
	if firstStatic < 0 || firstDynamic <= firstStatic || signLibrary <= firstDynamic ||
		assess < 0 || lastStatic <= assess || lastDynamic <= lastStatic || archive <= lastDynamic {
		t.Fatal("release package gates are not ordered before signing and after final Gatekeeper assessment")
	}
	for _, forbidden := range []string{"--sign -", "--no-wait", "skip-notar"} {
		if strings.Contains(content, forbidden) {
			t.Fatalf("release script contains forbidden bypass %q", forbidden)
		}
	}
	taskfile, err := os.ReadFile("Taskfile.yml")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"FAIRY_CODESIGN_IDENTITY", "FAIRY_NOTARY_PROFILE", "build/release-darwin.sh"} {
		if !strings.Contains(string(taskfile), required) {
			t.Fatalf("release task does not require %q", required)
		}
	}
	content = string(taskfile)
	packageStart := strings.Index(content, "  package:\n")
	releaseStart := strings.Index(content, "  release:\n")
	if packageStart < 0 || releaseStart <= packageStart {
		t.Fatal("desktop taskfile does not contain ordered package and release tasks")
	}
	packageTask := content[packageStart:releaseStart]
	verifyArtifact := strings.Index(packageTask, "- task: seekdb:verify-artifact")
	buildDesktop := strings.Index(packageTask, "- task: build")
	removeOldApp := strings.Index(packageTask, "rm -rf {{.BIN_DIR}}/{{.APP_NAME}}.app")
	if verifyArtifact < 0 || buildDesktop <= verifyArtifact || removeOldApp <= buildDesktop {
		t.Fatal("desktop package must verify the release artifact before build and App replacement")
	}
	staticPackageGate := strings.Index(packageTask, "--verify-package-layout")
	dynamicPackageGate := strings.Index(packageTask, "build/verify-seekdb-runtime-darwin.sh")
	signNested := strings.Index(packageTask, "codesign --force --sign - {{.BIN_DIR}}/{{.APP_NAME}}.app/Contents/Frameworks/libseekdb.dylib")
	signApp := strings.Index(packageTask, "codesign --force --sign - {{.BIN_DIR}}/{{.APP_NAME}}.app\n")
	verifySignature := strings.Index(packageTask, "codesign --verify --deep --strict --verbose=2 {{.BIN_DIR}}/{{.APP_NAME}}.app")
	if signNested < 0 || signApp <= signNested || verifySignature <= signApp || staticPackageGate <= verifySignature || dynamicPackageGate <= staticPackageGate {
		t.Fatal("desktop package must ad-hoc sign nested code before running static and in-process runtime gates")
	}
	releaseTask := content[releaseStart:]
	credentialGate := strings.Index(releaseTask, "preconditions:")
	packageStep := strings.Index(releaseTask, "- task: package")
	releaseStep := strings.Index(releaseTask, "build/release-darwin.sh")
	if credentialGate < 0 || packageStep <= credentialGate || releaseStep <= packageStep {
		t.Fatal("desktop release must check credentials before package and release execution")
	}
	for _, forbidden := range []string{"deps: [build]", "deps: [package]"} {
		if strings.Contains(packageTask+releaseTask, forbidden) {
			t.Fatalf("desktop release retains a dependency that runs before fail-fast gates: %q", forbidden)
		}
	}
}

func TestPackagedSeekDBRuntimeGateRequiresDurableHostCompletion(t *testing.T) {
	script, err := os.ReadFile(filepath.Join("build", "verify-seekdb-runtime-darwin.sh"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(script)
	for _, required := range []string{
		`env -i`,
		`HOME="$root"`,
		`PATH="/usr/bin:/bin:/usr/sbin:/sbin"`,
		`"$executable" --verify-seekdb-runtime "$root"`,
		`marker="$root/host-completed"`,
		`verification_deadline=$((SECONDS + 190))`,
		`shutdown_deadline=$((SECONDS + 2))`,
		`kill -TERM "$helper_pid"`,
		`kill -KILL "$helper_pid"`,
		`helper exited before writing the host-completion marker`,
		`helper timed out before writing the host-completion marker`,
		`host-completion marker is invalid`,
	} {
		if !strings.Contains(content, required) {
			t.Fatalf("packaged SeekDB runtime gate does not enforce %q", required)
		}
	}
	for _, forbidden := range []string{"FAIRY_SEEKDB_LIBRARY=", "--api-key", "FAIRY_CHAT_TEST_API_KEY"} {
		if strings.Contains(content, forbidden) {
			t.Fatalf("packaged SeekDB runtime gate contains forbidden release input %q", forbidden)
		}
	}
}

func TestPackagedSeekDBRuntimeGateRejectsHelperCleanExitWithoutMarker(t *testing.T) {
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		t.Skip("darwin/arm64 release gate")
	}
	app := filepath.Join(t.TempDir(), "FAIRY.app")
	executable := filepath.Join(app, "Contents", "MacOS", "FAIRY")
	if err := os.MkdirAll(filepath.Dir(executable), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("bash", filepath.Join("build", "verify-seekdb-runtime-darwin.sh"), "--app", app)
	output, err := command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "helper exited before writing the host-completion marker") {
		t.Fatalf("clean helper exit = (%v, %s), want missing marker rejection", err, output)
	}
}

func TestPackagedSeekDBRuntimeGateBoundsDestructorHangAfterDurableMarker(t *testing.T) {
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		t.Skip("darwin/arm64 release gate")
	}
	app := filepath.Join(t.TempDir(), "FAIRY.app")
	executable := filepath.Join(app, "Contents", "MacOS", "FAIRY")
	if err := os.MkdirAll(filepath.Dir(executable), 0o700); err != nil {
		t.Fatal(err)
	}
	helper := "#!/bin/sh\nprintf 'completed\\n' > \"$2/host-completed\"\nwhile :; do sleep 1; done\n"
	if err := os.WriteFile(executable, []byte(helper), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "bash", filepath.Join("build", "verify-seekdb-runtime-darwin.sh"), "--app", app)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("bounded marker helper = (%v, %s)", err, output)
	}
	if ctx.Err() != nil || !strings.Contains(string(output), "FAIRY SeekDB runtime verification: PASS") {
		t.Fatalf("bounded marker helper output = %q, context = %v", output, ctx.Err())
	}
}

func TestPackagedSeekDBRuntimeGateRejectsFailureAfterDurableMarker(t *testing.T) {
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		t.Skip("darwin/arm64 release gate")
	}
	app := filepath.Join(t.TempDir(), "FAIRY.app")
	executable := filepath.Join(app, "Contents", "MacOS", "FAIRY")
	if err := os.MkdirAll(filepath.Dir(executable), 0o700); err != nil {
		t.Fatal(err)
	}
	helper := "#!/bin/sh\nprintf 'completed\\n' > \"$2/host-completed\"\nexit 42\n"
	if err := os.WriteFile(executable, []byte(helper), 0o700); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("bash", filepath.Join("build", "verify-seekdb-runtime-darwin.sh"), "--app", app)
	output, err := command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "helper failed after writing the host-completion marker") {
		t.Fatalf("failed marker helper = (%v, %s), want post-marker failure rejection", err, output)
	}
}

func TestDarwinRuntimeEvidenceSeparatesEndpointAcceptanceFromOptionalPublicDistribution(t *testing.T) {
	script, err := os.ReadFile(filepath.Join("build", "observe-release-darwin.sh"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(script)
	for _, required := range []string{
		`--chat-origin`,
		`--embedding-origin`,
		`--openserp-origin`,
		`--require-public-distribution`,
		`schema_version\t3`,
		`verification_level\tendpoint_attempt`,
		`result\tfail`,
		`endpoint_eligible\tfalse`,
		`public_distribution_required\t%s`,
		`release_eligible\tfalse`,
		`package_verification\tfail`,
		`provider_configuration_checked\tfalse`,
		`host_platform_supported\tfalse`,
		`developer_id_checked\tfalse`,
		`notarization_checked\tfalse`,
		`staple_checked\tfalse`,
		`distribution_policy_checked\tfalse`,
		`gatekeeper_checked\tfalse`,
		`provider_smoke_checked\tfalse`,
		`provider_egress_boundary_checked\tfalse`,
		`egress_attribution\tdeclared_origin_set`,
		`capability_origin_overlap\tfalse`,
		`host_os_version\t%s`,
		`host_arch\t%s`,
		`app_minimum_system_version\t%s`,
		`sw_vers -productVersion`,
		`version_at_least()`,
		`host architecture $host_arch is unsupported; want arm64`,
		`host macOS $host_os_version is older than App minimum $app_minimum_system_version`,
		`metadata_set host_platform_supported true`,
		`codesign --verify --deep --strict`,
		`app_parent="$(cd "$(dirname "$app")" && pwd -P)"`,
		`app="$app_parent/${app##*/}"`,
		`Developer ID Application:`,
		`if [[ "$require_public_distribution" == "true" ]]`,
		`packaged libseekdb.dylib TeamIdentifier differs from the App`,
		`syspolicy_check distribution "$app" --json`,
		`system distribution policy rejected the packaged App`,
		`metadata_set staple_checked true`,
		`metadata_set distribution_policy_checked true`,
		`spctl --assess --type execute`,
		`Gatekeeper rejected the packaged App`,
		`"$executable" --verify-package-layout`,
		`env -i`,
		`"$executable" --verify-endpoint-readiness --require-openserp`,
		`"$executable" --verify-endpoint-readiness`,
		`metadata_set provider_configuration_checked true`,
		`"$(dirname "$0")/verify-seekdb-runtime-darwin.sh" --app "$app"`,
		`ps -axo pid=,ppid=`,
		`lsof -nP -a -p "$pid" -i`,
		`lsof -nP -a -p "$pid" -Fn`,
		`env -i`,
		`PATH="/usr/bin:/bin:/usr/sbin:/sbin"`,
		`TELEMETRY_ENABLED="true"`,
		`seekdb_telemetry_override_attempt\ttrue`,
		`local provider origin is forbidden`,
		`origin resolved to a local provider address`,
		`unexpected_listener`,
		`run\tpid\tppid\texecutable`,
		`capability="$(allowed_capability "$remote")"`,
		`metadata_set capability_origin_overlap true`,
		`Multiple comma-separated labels are intentional`,
		`capability_observed_in_run()`,
		`for (label_index = 1; label_index <= count; label_index++)`,
		`${capability}_egress_run_${run}`,
		`openserp_egress_run_${run}`,
		`for capability in chat embedding`,
		`${capability}_egress_observed`,
		`openserp_egress_required`,
		`openserp_egress_observed`,
		`metadata_set provider_egress_boundary_checked true`,
		`metadata_set package_verification pass`,
		`metadata_set endpoint_eligible true`,
		`metadata_set release_eligible true`,
		`metadata_set verification_level final_endpoint`,
		`metadata_set verification_level final_public_release`,
		`metadata_set result pass`,
	} {
		if !strings.Contains(content, required) {
			t.Fatalf("runtime evidence script does not enforce %q", required)
		}
	}
	for _, forbidden := range []string{
		"--api-key",
		"FAIRY_MODEL_ENDPOINT",
		"FAIRY_OPENSERP_URL",
		"python",
		"node ",
		"docker",
		"curl ",
		"wget ",
		"xcrun",
		"stapler",
		"non_loopback_listener",
		"is_loopback_endpoint",
		"endpoint evidence test origins must resolve to disjoint endpoints",
	} {
		if strings.Contains(strings.ToLower(content), strings.ToLower(forbidden)) {
			t.Fatalf("runtime evidence script contains forbidden dependency or credential surface %q", forbidden)
		}
	}
	layoutGate := strings.Index(content, `"$executable" --verify-package-layout`)
	readinessGate := strings.Index(content, `"$executable" --verify-endpoint-readiness`)
	seekDBRuntimeGate := strings.Index(content, `"$(dirname "$0")/verify-seekdb-runtime-darwin.sh" --app "$app"`)
	if layoutGate < 0 || readinessGate <= layoutGate || seekDBRuntimeGate <= readinessGate {
		t.Fatalf("runtime evidence gates are not ordered layout -> endpoint readiness -> isolated SeekDB runtime")
	}
}

func TestDarwinRuntimeEvidenceRejectsLocalProviderOriginBeforeLaunchingApp(t *testing.T) {
	app := filepath.Join(t.TempDir(), "FAIRY.app")
	executable := filepath.Join(app, "Contents", "MacOS", "FAIRY")
	library := filepath.Join(app, "Contents", "Frameworks", "libseekdb.dylib")
	info := filepath.Join(app, "Contents", "Info.plist")
	for _, filename := range []string{executable, library, info} {
		if err := os.MkdirAll(filepath.Dir(filename), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(library, []byte("fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(info, []byte(`<?xml version="1.0"?><plist><dict><key>CFBundleIdentifier</key><string>dev.rinai.fairy.fixture</string><key>LSMinimumSystemVersion</key><string>15.0</string></dict></plist>`), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, origin := range map[string]string{
		"ipv4_loopback":        "http://127.0.0.1:11434",
		"ipv6_loopback":        "http://[::1]:11434",
		"expanded_ipv6":        "http://[0:0:0:0:0:0:0:1]:11434",
		"mapped_ipv4_loopback": "http://[::ffff:127.0.0.1]:11434",
		"ipv4_multicast":       "http://224.0.0.1:11434",
		"ipv6_multicast":       "http://[ff02::1]:11434",
	} {
		t.Run(name, func(t *testing.T) {
			output := filepath.Join(t.TempDir(), "evidence")
			command := exec.Command("bash", filepath.Join("build", "observe-release-darwin.sh"),
				"--app", app,
				"--chat-origin", origin,
				"--embedding-origin", "https://192.0.2.20",
				"--output", output,
			)
			raw, err := command.CombinedOutput()
			if err == nil || !strings.Contains(string(raw), "chat local provider origin is forbidden") {
				t.Fatalf("local provider observation = (%v, %s), want pre-launch rejection", err, raw)
			}
			if strings.Contains(string(raw), "api-key") || strings.Contains(string(raw), "credential") {
				t.Fatalf("local provider rejection leaked credential surface: %s", raw)
			}
		})
	}

	sharedOutput := filepath.Join(t.TempDir(), "evidence")
	shared := exec.Command("bash", filepath.Join("build", "observe-release-darwin.sh"),
		"--app", app,
		"--chat-origin", "https://192.0.2.20",
		"--embedding-origin", "https://192.0.2.20",
		"--output", sharedOutput,
	)
	raw, err := shared.CombinedOutput()
	if err == nil {
		t.Fatal("unsigned fixture unexpectedly passed package verification")
	}
	if strings.Contains(string(raw), "endpoint evidence test origins must resolve to disjoint endpoints") {
		t.Fatalf("shared provider origin was rejected before package verification: %s", raw)
	}
	metadata, readErr := os.ReadFile(filepath.Join(sharedOutput, "metadata.tsv"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !strings.Contains(string(metadata), "capability_origin_overlap\ttrue\n") {
		t.Fatalf("shared provider origin was not recorded in metadata:\n%s", metadata)
	}
}

func TestDarwinRuntimeEvidenceRejectsUnsupportedHostBeforeLaunchingApp(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("darwin release evidence gate")
	}
	app := filepath.Join(t.TempDir(), "FAIRY.app")
	executable := filepath.Join(app, "Contents", "MacOS", "FAIRY")
	library := filepath.Join(app, "Contents", "Frameworks", "libseekdb.dylib")
	info := filepath.Join(app, "Contents", "Info.plist")
	for _, filename := range []string{executable, library, info} {
		if err := os.MkdirAll(filepath.Dir(filename), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nexit 99\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(library, []byte("fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(info, []byte(`<?xml version="1.0"?><plist><dict><key>CFBundleIdentifier</key><string>dev.rinai.fairy.unsupported-host-fixture</string><key>LSMinimumSystemVersion</key><string>99.0</string></dict></plist>`), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name       string
		script     string
		extra      []string
		wantResult string
	}{
		{name: "unsigned_preflight", script: "observe-package-preflight-darwin.sh", wantResult: "preflight_fail"},
		{name: "endpoint_evidence", script: "observe-release-darwin.sh", extra: []string{"--chat-origin", "https://192.0.2.20", "--embedding-origin", "https://192.0.2.21"}, wantResult: "fail"},
	} {
		t.Run(test.name, func(t *testing.T) {
			output := filepath.Join(t.TempDir(), "evidence")
			arguments := []string{filepath.Join("build", test.script), "--app", app, "--output", output, "--duration", "5", "--runs", "2"}
			arguments = append(arguments, test.extra...)
			command := exec.Command("bash", arguments...)
			raw, err := command.CombinedOutput()
			if err == nil || !strings.Contains(string(raw), "is older than App minimum 99.0") {
				t.Fatalf("unsupported host observation = (%v, %s), want pre-launch platform rejection", err, raw)
			}
			metadata, err := os.ReadFile(filepath.Join(output, "metadata.tsv"))
			if err != nil {
				t.Fatal(err)
			}
			content := string(metadata)
			for _, required := range []string{
				"result\t" + test.wantResult + "\n",
				"host_platform_supported\tfalse\n",
				"package_verification\tfail\n",
				"app_minimum_system_version\t99.0\n",
			} {
				if !strings.Contains(content, required) {
					t.Fatalf("unsupported host metadata missing %q:\n%s", required, content)
				}
			}
			if strings.Contains(content, "run_1_pid\t") {
				t.Fatalf("unsupported host launched App before rejection:\n%s", content)
			}
		})
	}
}

func TestDarwinUnsignedPackagePreflightCannotMasqueradeAsReleaseEvidence(t *testing.T) {
	script, err := os.ReadFile(filepath.Join("build", "observe-package-preflight-darwin.sh"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(script)
	for _, required := range []string{
		`verification_level\tunsigned_preflight`,
		`result\tpreflight_fail`,
		`release_eligible\tfalse`,
		`package_verification\tfail`,
		`host_platform_supported\tfalse`,
		`developer_id_checked\tfalse`,
		`notarization_checked\tfalse`,
		`provider_smoke_checked\tfalse`,
		`host_os_version\t%s`,
		`host_arch\t%s`,
		`app_minimum_system_version\t%s`,
		`sw_vers -productVersion`,
		`version_at_least()`,
		`host architecture $host_arch is unsupported; want arm64`,
		`host macOS $host_os_version is older than App minimum $app_minimum_system_version`,
		`metadata_set host_platform_supported true`,
		`metadata_set result preflight_pass`,
		`metadata_set package_verification pass`,
		`codesign --verify --deep --strict`,
		`app_parent="$(cd "$(dirname "$app")" && pwd -P)"`,
		`app="$app_parent/${app##*/}"`,
		`"$executable" --verify-package-layout`,
		`"$runtime_verifier" --app "$app"`,
		`HOME="$private_home"`,
		`PATH="/usr/bin:/bin:/usr/sbin:/sbin"`,
		`TELEMETRY_ENABLED="true"`,
		`seekdb_telemetry_override_attempt\ttrue`,
		`provider_environment_override_attempt\ttrue`,
		`FAIRY_RUNTIME_PROFILE="full"`,
		`FAIRY_DATABASE_URL="postgres://preflight.invalid/fairy"`,
		`FAIRY_OPENSERP_URL="http://127.0.0.1:9"`,
		`OPENAI_API_KEY="preflight-not-a-secret"`,
		`OPENAI_BASE_URL="http://127.0.0.1:9/v1"`,
		`CODEBUDDY_API_KEY="preflight-not-a-secret"`,
		`CODEBUDDY_BASE_URL="http://127.0.0.1:9"`,
		`HTTP_PROXY="http://127.0.0.1:9"`,
		`HTTPS_PROXY="http://127.0.0.1:9"`,
		`ALL_PROXY="http://127.0.0.1:9"`,
		`unexpected_network_socket`,
		`child_process_observed`,
		`profile_reused_across_runs`,
		`kill -TERM "$pid"`,
		`kill -KILL "$pid"`,
	} {
		if !strings.Contains(content, required) {
			t.Fatalf("unsigned package preflight does not enforce %q", required)
		}
	}
	for _, forbidden := range []string{
		`result\tpass`,
		`metadata_set result pass`,
		`xcrun stapler`,
		`spctl --assess`,
		`--chat-origin`,
		`--embedding-origin`,
		`--openserp-origin`,
		`--api-key`,
		`FAIRY_CHAT_TEST_API_KEY`,
		`FAIRY_EMBEDDING_TEST_API_KEY`,
	} {
		if strings.Contains(content, forbidden) {
			t.Fatalf("unsigned package preflight contains release or credential surface %q", forbidden)
		}
	}

	release, err := os.ReadFile(filepath.Join("build", "observe-release-darwin.sh"))
	if err != nil {
		t.Fatal(err)
	}
	releaseContent := string(release)
	for _, required := range []string{
		`--require-public-distribution`,
		`syspolicy_check distribution "$app" --json`,
		`metadata_set staple_checked true`,
		`spctl --assess --type execute`,
		`--chat-origin`,
		`--embedding-origin`,
		`metadata_set endpoint_eligible true`,
		`metadata_set verification_level final_endpoint`,
		`metadata_set verification_level final_public_release`,
		`metadata_set result pass`,
		`kill -TERM "$pid"`,
		`kill -KILL "$pid"`,
	} {
		if !strings.Contains(releaseContent, required) {
			t.Fatalf("final release evidence gate was weakened and no longer enforces %q", required)
		}
	}
	for _, forbidden := range []string{`xcrun`, `stapler`} {
		if strings.Contains(releaseContent, forbidden) {
			t.Fatalf("final clean-host release evidence gate depends on developer tool %q", forbidden)
		}
	}
}
