//go:build integration

package web_test

import (
	"net"
	"os"
	"testing"

	"fairy/runtime/seekdb"
	"fairy/runtime/seekdb/seekdbtest"
)

func applySeekDBAPIEnv(t *testing.T) {
	t.Helper()
	library := os.Getenv(seekdb.EnvLibrary)
	if library == "" {
		t.Skip(seekdb.EnvLibrary + " is not set")
	}
	t.Setenv(seekdb.EnvLibrary, library)
	t.Setenv(seekdb.EnvDataDir, seekdbtest.DataDir(t))
	t.Setenv(seekdb.EnvDatabase, seekdb.DefaultDatabase)
	t.Setenv(seekdb.EnvConnectLimit, "5s")
	t.Setenv(seekdb.EnvStartLimit, "90s")
	t.Setenv(seekdb.EnvQueryLimit, "15s")
	t.Setenv(seekdb.EnvShutdownLimit, "20s")
	t.Setenv(seekdb.EnvMaxOpenConns, "8")
	t.Setenv(seekdb.EnvMaxIdleConns, "4")
	t.Setenv("FAIRY_DATABASE_URL", "postgres://invalid-legacy-sentinel")
	t.Setenv("FAIRY_PGVECTOR_URL", "http://invalid-legacy-sentinel")
	t.Setenv("QDRANT_URL", "http://invalid-legacy-sentinel")
}

func reserveAPISeekDBAddress(t *testing.T) string {
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
