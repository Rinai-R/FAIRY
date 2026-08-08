package personal

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// DatabaseQuerier is the minimal PostgreSQL read surface shared by memory
// projections. It is deliberately domain-neutral and does not imply ownership
// of conversation history.
type DatabaseQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func stringPtrFromPGText(value pgtype.Text) *string {
	if !value.Valid {
		return nil
	}
	result := value.String
	return &result
}
