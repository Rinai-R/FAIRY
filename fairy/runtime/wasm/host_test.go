package wasm

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

var emptyModule = []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}

// wasiPreview1FDWriteModule imports wasi_snapshot_preview1.fd_write and nothing else.
var wasiPreview1FDWriteModule = []byte{
	0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00,
	0x01, 0x09, 0x01, 0x60, 0x04, 0x7f, 0x7f, 0x7f, 0x7f, 0x01, 0x7f,
	0x02, 0x23, 0x01, 0x16, 0x77, 0x61, 0x73, 0x69, 0x5f, 0x73, 0x6e, 0x61, 0x70, 0x73, 0x68, 0x6f, 0x74, 0x5f, 0x70, 0x72, 0x65, 0x76, 0x69, 0x65, 0x77, 0x31,
	0x08, 0x66, 0x64, 0x5f, 0x77, 0x72, 0x69, 0x74, 0x65, 0x00, 0x00,
}

func TestOpenHostInstantiatesModulesWithoutWASI(t *testing.T) {
	host, err := Open(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := host.Close(t.Context()); err != nil {
			t.Error(err)
		}
	})
	module, err := host.Instantiate(t.Context(), "empty", emptyModule)
	if err != nil {
		t.Fatal(err)
	}
	if err := module.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestHostDeniesWASIImportsByDefault(t *testing.T) {
	host, err := Open(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := host.Close(t.Context()); err != nil {
			t.Error(err)
		}
	})
	module, err := host.Instantiate(t.Context(), "wasi-guest", wasiPreview1FDWriteModule)
	if err == nil || module != nil {
		t.Fatalf("Instantiate() = (%v, %v), want WASI import rejection", module, err)
	}
	text := strings.ToLower(err.Error())
	if !strings.Contains(text, "wasi_snapshot_preview1") && !strings.Contains(text, "fd_write") {
		t.Fatalf("Instantiate() error = %v, want WASI import diagnostic", err)
	}
	for _, forbidden := range []string{"FAIRY_API_TOKEN", "Bearer ", "password="} {
		if strings.Contains(err.Error(), forbidden) {
			t.Fatalf("Instantiate() error leaked %q: %v", forbidden, err)
		}
	}
}

func TestHostCloseIsIdempotentAndRejectsLaterInstantiate(t *testing.T) {
	host, err := Open(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := host.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := host.Close(t.Context()); err != nil {
		t.Fatalf("second Close() = %v", err)
	}
	if _, err := host.Instantiate(t.Context(), "empty", emptyModule); !errors.Is(err, ErrHostClosed) {
		t.Fatalf("Instantiate() after Close = %v, want %v", err, ErrHostClosed)
	}
}

func TestHostCloseIsBounded(t *testing.T) {
	host, err := Open(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	started := time.Now()
	if err := host.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("Close() took %s", elapsed)
	}
}

func TestOpenRequiresLiveContext(t *testing.T) {
	if _, err := Open(nil); err == nil {
		t.Fatal("Open(nil) error = nil")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := Open(ctx); err == nil {
		t.Fatal("Open(canceled) error = nil")
	}
}

func TestInstantiateRejectsMissingNameOrBinary(t *testing.T) {
	host, err := Open(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := host.Close(t.Context()); err != nil {
			t.Error(err)
		}
	})
	if _, err := host.Instantiate(t.Context(), "", emptyModule); !errors.Is(err, ErrModuleNameRequired) {
		t.Fatalf("missing name error = %v", err)
	}
	if _, err := host.Instantiate(t.Context(), "empty", nil); !errors.Is(err, ErrModuleBinaryRequired) {
		t.Fatalf("missing binary error = %v", err)
	}
}

func TestProductionHostDoesNotImportWASI(t *testing.T) {
	source, err := os.ReadFile("host.go")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(source, []byte("wasi_snapshot_preview1")) || bytes.Contains(source, []byte("WithFS(")) || bytes.Contains(source, []byte("WithEnv(")) {
		t.Fatal("host.go grants WASI, filesystem, or environment access")
	}
	if !bytes.Contains(source, []byte("WithStartFunctions()")) {
		t.Fatal("host.go must clear the default WASI _start function")
	}
}

func TestArtifactCatalogPinsOfficialWazeroV1(t *testing.T) {
	catalog, err := BuiltinArtifactCatalog()
	if err != nil {
		t.Fatal(err)
	}
	root := moduleRoot(t)
	goMod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(goMod, []byte(catalog.Module+" "+catalog.Version)) {
		t.Fatalf("go.mod does not pin %s %s", catalog.Module, catalog.Version)
	}
	goSum, err := os.ReadFile(filepath.Join(root, "go.sum"))
	if err != nil {
		t.Fatal(err)
	}
	want := catalog.Module + " " + catalog.Version + " " + catalog.GoSumZipHash
	if !bytes.Contains(goSum, []byte(want)) {
		t.Fatalf("go.sum does not record %s", want)
	}
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}
