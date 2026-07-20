//go:build integration

package postgres

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestPoolPingAndCancellation(t *testing.T) {
	databaseURL := os.Getenv("FAIRY_TEST_DATABASE_URL")
	if databaseURL == "" {
		databaseURL = "postgres://fairy:fairy_test_password@127.0.0.1:15432/fairy_test?sslmode=disable"
	}
	pool, err := Open(context.Background(), ShortTimeoutConfig(databaseURL))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := pool.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := pool.Stats().AcquiredConns
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	queryCtx, release := pool.QueryContext(ctx)
	defer release()
	if _, err := pool.Raw().Exec(queryCtx, "select pg_sleep(1)"); err == nil {
		t.Fatal("expected canceled query error")
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got := pool.Stats().AcquiredConns; got == before {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("acquired conns = %d, want %d", pool.Stats().AcquiredConns, before)
}
