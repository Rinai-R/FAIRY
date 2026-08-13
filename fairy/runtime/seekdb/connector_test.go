package seekdb

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestMySQLConnectorIsLocalBoundedAndConservative(t *testing.T) {
	config := testRuntimeConfig(t)
	config.Password = "connector-private-password"
	driverConfig, err := mysqlDriverConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	if driverConfig.User != config.User || driverConfig.Passwd != config.Password || driverConfig.Addr != config.Address || driverConfig.DBName != config.Database {
		t.Fatalf("mysqlDriverConfig() identity = %+v", driverConfig)
	}
	if driverConfig.Net != "tcp" || driverConfig.Timeout != config.ConnectLimit || driverConfig.ReadTimeout != config.QueryLimit || driverConfig.WriteTimeout != config.QueryLimit {
		t.Fatalf("mysqlDriverConfig() transport = %+v", driverConfig)
	}
	if !driverConfig.ParseTime || !driverConfig.CheckConnLiveness || !driverConfig.AllowNativePasswords {
		t.Fatalf("mysqlDriverConfig() safe defaults = %+v", driverConfig)
	}
	if driverConfig.AllowAllFiles || driverConfig.AllowCleartextPasswords || driverConfig.AllowFallbackToPlaintext || driverConfig.AllowOldPasswords || driverConfig.InterpolateParams || driverConfig.MultiStatements {
		t.Fatalf("mysqlDriverConfig() enabled unsafe option = %+v", driverConfig)
	}
}

func TestOpenSQLDatabaseAppliesPoolLimit(t *testing.T) {
	config := testRuntimeConfig(t)
	config.MaxOpenConns = 7
	config.MaxIdleConns = 3
	database, err := openSQLDatabase(config)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if got := database.Stats().MaxOpenConnections; got != 7 {
		t.Fatalf("MaxOpenConnections = %d", got)
	}
}

func TestRuntimeExposesSQLAndBoundedQueryContext(t *testing.T) {
	config := testRuntimeConfig(t)
	config.QueryLimit = 50 * time.Millisecond
	runtime, err := open(t.Context(), config, testLaunchOptions(t, "block"))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.SQL() == nil {
		t.Fatal("SQL() = nil")
	}
	queryCtx, cancel := runtime.QueryContext(t.Context())
	defer cancel()
	deadline, ok := queryCtx.Deadline()
	if !ok || time.Until(deadline) > config.QueryLimit {
		t.Fatalf("QueryContext() deadline = %v, ok = %t", deadline, ok)
	}
	closeCtx, closeCancel := context.WithTimeout(t.Context(), time.Second)
	defer closeCancel()
	if err := runtime.Close(closeCtx); err != nil {
		t.Fatal(err)
	}
	if runtime.SQL() != nil {
		t.Fatal("SQL() remained available after Close")
	}
}

func TestRuntimeErrorRedactsCredential(t *testing.T) {
	config := testRuntimeConfig(t)
	config.Password = "must-not-appear"
	config.LibraryDirs = []string{"/private/seekdb-library"}
	err := redactRuntimeError(config, errors.New("dial failed with must-not-appear from /private/seekdb-library"))
	if strings.Contains(err.Error(), config.Password) || strings.Contains(err.Error(), config.LibraryDirs[0]) || !strings.Contains(err.Error(), "[seekdb-credential]") || !strings.Contains(err.Error(), "[seekdb-library]") {
		t.Fatalf("redactRuntimeError() = %v", err)
	}
}
