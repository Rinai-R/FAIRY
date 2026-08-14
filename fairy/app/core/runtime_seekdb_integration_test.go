//go:build integration

package core

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fairy/runtime/seekdb"
)

func TestProductionRuntimeCompositionUsesSeekDBWithoutPostgres(t *testing.T) {
	environment := newCoreSeekDBEnvironment(t)
	root := t.TempDir()
	applyCoreSeekDBEnvironment(t, environment)

	rt, err := Open(RuntimeOptions{ConfigRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	if rt.Foundation == nil || rt.MemoryStore == nil || rt.Secret == nil || rt.History == nil || rt.ObservabilityStore == nil {
		t.Fatalf("runtime SeekDB composition incomplete: foundation:%v memory:%v secret:%v history:%v ledger:%v",
			rt.Foundation != nil, rt.MemoryStore != nil, rt.Secret != nil, rt.History != nil, rt.ObservabilityStore != nil)
	}
	if !rt.Secret.Encrypted() {
		t.Fatal("runtime secret store is not encrypted")
	}
	status, err := rt.Foundation.Status(t.Context())
	if err != nil || status.Storage != "seekdb" || status.Schema.State != seekdb.SchemaCurrent {
		t.Fatalf("foundation status = (%#v, %v)", status, err)
	}
	if _, err := rt.MemoryStore.SummaryContext(context.Background()); err != nil {
		t.Fatalf("memory store does not share SeekDB authority: %v", err)
	}
	if _, err := rt.KnowledgeStore.StatsContext(t.Context()); err != nil {
		t.Fatalf("knowledge store does not share SeekDB authority: %v", err)
	}
	for _, path := range []string{"intelligence/fairy.sqlite3", "model/secrets.sqlite3"} {
		if _, err := os.Stat(filepath.Join(root, path)); !os.IsNotExist(err) {
			t.Fatalf("runtime created legacy SQLite path %s: %v", path, err)
		}
	}
	if err := rt.Close(); err != nil {
		t.Fatal(err)
	}
	if err := rt.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	if _, err := rt.Foundation.SQL(); err == nil {
		t.Fatal("closed foundation still exposed SQL")
	}
}

func TestProductionRuntimeRejectsMissingSeekDBDependencies(t *testing.T) {
	environment := newCoreSeekDBEnvironment(t)
	tests := []struct {
		name   string
		mutate func(*coreSeekDBEnvironment)
		want   string
	}{
		{
			name: "binary",
			mutate: func(environment *coreSeekDBEnvironment) {
				environment.values[seekdb.EnvBinaryPath] = ""
			},
			want: "binary",
		},
		{
			name: "data dir",
			mutate: func(environment *coreSeekDBEnvironment) {
				environment.values[seekdb.EnvDataDir] = ""
			},
			want: "data",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cloned := environment.clone()
			test.mutate(cloned)
			applyCoreSeekDBEnvironment(t, cloned)
			rt, err := Open(RuntimeOptions{ConfigRoot: t.TempDir()})
			if rt != nil || err == nil || !strings.Contains(strings.ToLower(err.Error()), test.want) {
				t.Fatalf("Open() = (%v, %v), want error containing %q", rt, err, test.want)
			}
		})
	}
}

type coreSeekDBEnvironment struct {
	values map[string]string
}

func (e *coreSeekDBEnvironment) clone() *coreSeekDBEnvironment {
	values := make(map[string]string, len(e.values))
	for key, value := range e.values {
		values[key] = value
	}
	return &coreSeekDBEnvironment{values: values}
}

func newCoreSeekDBEnvironment(t *testing.T) *coreSeekDBEnvironment {
	t.Helper()
	binary := os.Getenv(seekdb.EnvBinaryPath)
	if binary == "" {
		t.Skip(seekdb.EnvBinaryPath + " is not set")
	}
	return &coreSeekDBEnvironment{values: map[string]string{
		seekdb.EnvBinaryPath:    binary,
		seekdb.EnvLibraryPath:   os.Getenv(seekdb.EnvLibraryPath),
		seekdb.EnvDataDir:       filepath.Join(t.TempDir(), "seekdb-data"),
		seekdb.EnvAddress:       reserveCoreSeekDBAddress(t),
		seekdb.EnvDatabase:      seekdb.DefaultDatabase,
		seekdb.EnvUser:          seekdb.DefaultUser,
		seekdb.EnvConnectLimit:  "5s",
		seekdb.EnvStartLimit:    "90s",
		seekdb.EnvQueryLimit:    "15s",
		seekdb.EnvShutdownLimit: "20s",
		seekdb.EnvMaxOpenConns:  "8",
		seekdb.EnvMaxIdleConns:  "4",
		"FAIRY_DATABASE_URL":    "postgres://invalid-legacy-sentinel",
		"FAIRY_PGVECTOR_URL":    "http://invalid-legacy-sentinel",
		"QDRANT_URL":            "http://invalid-legacy-sentinel",
	}}
}

func applyCoreSeekDBEnvironment(t *testing.T, environment *coreSeekDBEnvironment) {
	t.Helper()
	for key, value := range environment.values {
		t.Setenv(key, value)
	}
}

func reserveCoreSeekDBAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}
