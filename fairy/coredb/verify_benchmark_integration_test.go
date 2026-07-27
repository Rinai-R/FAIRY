//go:build integration

package coredb

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type countingQueryTracer struct {
	count atomic.Int64
}

func (tracer *countingQueryTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	tracer.count.Add(1)
	return ctx
}

func (*countingQueryTracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (tracer *countingQueryTracer) Reset() {
	tracer.count.Store(0)
}

func (tracer *countingQueryTracer) Count() int64 {
	return tracer.count.Load()
}

func TestVerifySchemaUsesOneCatalogQueryIntegration(t *testing.T) {
	pool, tracer := openTracedIsolatedPool(t, t.Context())
	if err := Migrate(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	tracer.Reset()
	if _, err := VerifySchema(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	queries := tracer.Count()
	if queries != 1 {
		t.Fatalf("VerifySchema query count = %d, want 1 catalog snapshot", queries)
	}
}

func BenchmarkVerifySchemaIntegration(b *testing.B) {
	pool, tracer := openTracedIsolatedPool(b, b.Context())
	if err := Migrate(b.Context(), pool); err != nil {
		b.Fatal(err)
	}
	if _, err := VerifySchema(b.Context(), pool); err != nil {
		b.Fatal(err)
	}
	tracer.Reset()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, err := VerifySchema(b.Context(), pool); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	b.ReportMetric(float64(tracer.Count())/float64(b.N), "queries/op")
}

func openTracedIsolatedPool(tb testing.TB, ctx context.Context) (*pgxpool.Pool, *countingQueryTracer) {
	tb.Helper()
	databaseURL := os.Getenv("FAIRY_TEST_DATABASE_URL")
	if databaseURL == "" {
		databaseURL = "postgres://fairy:fairy_test_password@127.0.0.1:15432/fairy_test?sslmode=disable"
	}
	adminConfig := ShortTimeoutConfig(databaseURL)
	admin, err := pgxpool.New(ctx, adminConfig.URL)
	if err != nil {
		tb.Fatalf("open admin pool: %v", err)
	}
	tb.Cleanup(admin.Close)

	schemaName := fmt.Sprintf("fairy_benchmark_%d", time.Now().UnixNano())
	quotedSchema := pgx.Identifier{schemaName}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		tb.Fatalf("create schema: %v", err)
	}
	tb.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		cleanupPool, err := pgxpool.New(cleanupCtx, adminConfig.URL)
		if err != nil {
			tb.Logf("open cleanup pool: %v", err)
			return
		}
		defer cleanupPool.Close()
		_, _ = cleanupPool.Exec(cleanupCtx, "DROP SCHEMA IF EXISTS "+quotedSchema+" CASCADE")
	})

	parsedURL, err := url.Parse(databaseURL)
	if err != nil {
		tb.Fatalf("parse database URL: %v", err)
	}
	query := parsedURL.Query()
	query.Set("search_path", schemaName)
	parsedURL.RawQuery = query.Encode()
	config := ShortTimeoutConfig(parsedURL.String())
	poolConfig, err := pgxpool.ParseConfig(config.URL)
	if err != nil {
		tb.Fatalf("parse pool config: %v", err)
	}
	poolConfig.MaxConns = config.MaxConns
	poolConfig.MinConns = config.MinConns
	poolConfig.ConnConfig.ConnectTimeout = config.ConnectTimeout
	tracer := &countingQueryTracer{}
	poolConfig.ConnConfig.Tracer = tracer
	rawPool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		tb.Fatalf("open traced pool: %v", err)
	}
	tb.Cleanup(rawPool.Close)
	pingCtx, cancel := context.WithTimeout(ctx, config.QueryTimeout)
	defer cancel()
	if err := rawPool.Ping(pingCtx); err != nil {
		tb.Fatal(err)
	}
	return rawPool, tracer
}
