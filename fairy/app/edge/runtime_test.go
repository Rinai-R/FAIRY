package edge

import (
	"errors"
	"strings"
	"testing"

	"fairy/runtime/seekdb"
)

func TestOpenFailsClosedWithoutSeekDB(t *testing.T) {
	t.Setenv("FAIRY_DATABASE_URL", "postgres://invalid-legacy-sentinel")
	t.Setenv(seekdb.EnvLibrary, "")
	t.Setenv(seekdb.EnvDataDir, t.TempDir())

	rt, err := Open(t.Context(), Options{ConfigRoot: t.TempDir()})
	if rt != nil || err == nil {
		t.Fatalf("Open() = (%#v, %v), want SeekDB configuration failure", rt, err)
	}
	if !strings.Contains(err.Error(), "SeekDB") && !strings.Contains(err.Error(), seekdb.EnvLibrary) && !strings.Contains(err.Error(), seekdb.EnvDataDir) {
		t.Fatalf("Open() error = %v, want SeekDB library or data-dir failure", err)
	}
	for _, forbidden := range []string{"FAIRY_DATABASE_URL", "postgres://", "pgvector", "qdrant"} {
		if strings.Contains(strings.ToLower(err.Error()), strings.ToLower(forbidden)) {
			t.Fatalf("Open() error mentioned legacy dependency %q: %v", forbidden, err)
		}
	}
}

func TestOpenRequiresLifetimeContext(t *testing.T) {
	rt, err := Open(nil, Options{ConfigRoot: t.TempDir()})
	if rt != nil || !errors.Is(err, ErrLifetimeContextRequired) {
		t.Fatalf("Open(nil) = (%v, %v)", rt, err)
	}
}

func TestClosedRuntimeDoesNotExposeSession(t *testing.T) {
	var rt *Runtime
	if rt.Session() != nil || rt.Facade() != nil || rt.Core() != nil || rt.NewSession() != nil {
		t.Fatal("nil runtime exposed session composition")
	}
	if err := rt.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.PluginHost(); !errors.Is(err, ErrPluginHostUnavailable) {
		t.Fatalf("PluginHost() = %v, want %v", err, ErrPluginHostUnavailable)
	}
}
