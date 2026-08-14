//go:build integration

package edge

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fairy/runtime/seekdb"
	"fairy/transport/session"
)

func TestOpenComposesSeekDBCoreAndSessionFacade(t *testing.T) {
	applyEdgeSeekDBEnvironment(t)
	rt, err := Open(t.Context(), Options{ConfigRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := rt.Close(t.Context()); err != nil {
			t.Error(err)
		}
	})
	if rt.Core() == nil || rt.Core().Foundation == nil {
		t.Fatal("edge did not compose Core SeekDB foundation")
	}
	if rt.Session() == nil || rt.Facade() == nil {
		t.Fatal("edge did not compose Session service and facade")
	}
	if err := rt.PluginHost(); !errors.Is(err, ErrPluginHostUnavailable) {
		t.Fatalf("PluginHost() = %v, want %v", err, ErrPluginHostUnavailable)
	}
	status, err := rt.Core().Foundation.Status(t.Context())
	if err != nil || status.Storage != "seekdb" || status.Schema.State != seekdb.SchemaCurrent {
		t.Fatalf("foundation status = (%#v, %v)", status, err)
	}
	_, err = rt.Facade().OpenSession(t.Context(), session.OpenSessionRequest{
		Endpoint:    session.EndpointDesktop,
		EndpointKey: "edge-desktop",
		Interaction: session.Context{
			Audience: session.AudienceSingle, Initiation: session.InitiationDirect, Presentation: session.PresentationChat,
		},
	})
	if err != nil && !strings.Contains(err.Error(), "character") {
		t.Fatalf("OpenSession() error = %v", err)
	}
}

func applyEdgeSeekDBEnvironment(t *testing.T) {
	t.Helper()
	binary := os.Getenv(seekdb.EnvBinaryPath)
	if binary == "" {
		t.Skip(seekdb.EnvBinaryPath + " is not set")
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	t.Setenv(seekdb.EnvBinaryPath, binary)
	t.Setenv(seekdb.EnvLibraryPath, os.Getenv(seekdb.EnvLibraryPath))
	t.Setenv(seekdb.EnvDataDir, filepath.Join(t.TempDir(), "seekdb-data"))
	t.Setenv(seekdb.EnvAddress, address)
	t.Setenv(seekdb.EnvDatabase, seekdb.DefaultDatabase)
	t.Setenv(seekdb.EnvUser, seekdb.DefaultUser)
	t.Setenv(seekdb.EnvConnectLimit, "5s")
	t.Setenv(seekdb.EnvStartLimit, "90s")
	t.Setenv(seekdb.EnvQueryLimit, "15s")
	t.Setenv(seekdb.EnvShutdownLimit, "20s")
	t.Setenv("FAIRY_DATABASE_URL", "postgres://invalid-legacy-sentinel")
}
