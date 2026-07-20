package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Pool struct {
	config Config
	pool   *pgxpool.Pool
}

type PoolStats struct {
	TotalConns    int32 `json:"totalConns"`
	AcquiredConns int32 `json:"acquiredConns"`
	IdleConns     int32 `json:"idleConns"`
}

func Open(ctx context.Context, config Config) (*Pool, error) {
	poolConfig, err := pgxpool.ParseConfig(config.URL)
	if err != nil {
		return nil, fmt.Errorf("parse database config %s: %w", config.RedactedURL(), err)
	}
	poolConfig.MaxConns = config.MaxConns
	poolConfig.MinConns = config.MinConns
	poolConfig.ConnConfig.ConnectTimeout = config.ConnectTimeout
	openCtx, cancel := context.WithTimeout(ctx, config.ConnectTimeout)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(openCtx, poolConfig)
	if err != nil {
		return nil, fmt.Errorf("open database pool %s: %w", config.RedactedURL(), err)
	}
	wrapped := &Pool{config: config, pool: pool}
	if err := wrapped.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return wrapped, nil
}

func (p *Pool) Raw() *pgxpool.Pool {
	if p == nil {
		return nil
	}
	return p.pool
}

func (p *Pool) Close() {
	if p != nil && p.pool != nil {
		p.pool.Close()
	}
}

func (p *Pool) Ping(ctx context.Context) error {
	if p == nil || p.pool == nil {
		return fmt.Errorf("database pool is not open")
	}
	queryCtx, cancel := p.QueryContext(ctx)
	defer cancel()
	if err := p.pool.Ping(queryCtx); err != nil {
		return fmt.Errorf("ping database %s: %w", p.config.RedactedURL(), err)
	}
	return nil
}

func (p *Pool) QueryContext(parent context.Context) (context.Context, context.CancelFunc) {
	if p == nil || p.config.QueryTimeout <= 0 {
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, p.config.QueryTimeout)
}

func (p *Pool) Stats() PoolStats {
	if p == nil || p.pool == nil {
		return PoolStats{}
	}
	stats := p.pool.Stat()
	return PoolStats{TotalConns: stats.TotalConns(), AcquiredConns: stats.AcquiredConns(), IdleConns: stats.IdleConns()}
}

func (p *Pool) Config() Config {
	if p == nil {
		return Config{}
	}
	return p.config
}

func ShortTimeoutConfig(databaseURL string) Config {
	return Config{URL: databaseURL, MaxConns: 4, MinConns: 0, ConnectTimeout: time.Second, QueryTimeout: time.Second}
}
