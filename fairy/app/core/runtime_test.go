package core

import (
	"strings"
	"testing"

	"fairy/runtime/seekdb"
)

func TestOpenFailsClosedWithoutSeekDBAndIgnoresPostgresURL(t *testing.T) {
	t.Setenv("FAIRY_DATABASE_URL", "postgres://invalid-legacy-sentinel")
	t.Setenv("FAIRY_PGVECTOR_URL", "http://invalid-legacy-sentinel")
	t.Setenv("QDRANT_URL", "http://invalid-legacy-sentinel")
	t.Setenv(seekdb.EnvBinaryPath, "")
	t.Setenv(seekdb.EnvDataDir, t.TempDir())

	rt, err := Open(RuntimeOptions{ConfigRoot: t.TempDir()})
	if rt != nil || err == nil {
		t.Fatalf("Open() = (%#v, %v), want SeekDB configuration failure", rt, err)
	}
	message := err.Error()
	if !strings.Contains(message, "SeekDB") && !strings.Contains(message, seekdb.EnvBinaryPath) && !strings.Contains(message, "binary") {
		t.Fatalf("Open() error = %v, want SeekDB binary failure", err)
	}
	for _, forbidden := range []string{"FAIRY_DATABASE_URL", "postgres://", "pgvector", "qdrant"} {
		if strings.Contains(strings.ToLower(message), strings.ToLower(forbidden)) {
			t.Fatalf("Open() error mentioned legacy dependency %q: %v", forbidden, err)
		}
	}
}
