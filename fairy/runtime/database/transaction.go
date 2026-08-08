package database

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// Transaction is the bounded relational resource shared by domain-owned
// transaction operations. Database owns the resource; domains own their SQL.
type Transaction = pgx.Tx

func (p *Pool) Begin(ctx context.Context) (Transaction, error) {
	if p == nil || p.pool == nil {
		return nil, errors.New("database pool is not open")
	}
	return p.pool.Begin(ctx)
}
