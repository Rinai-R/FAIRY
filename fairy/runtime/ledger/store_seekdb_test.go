package ledger

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"testing"
	"time"
)

func TestNewSeekDBStoreRejectsInvalidInputs(t *testing.T) {
	database := sql.OpenDB(ledgerUnitConnector{})
	t.Cleanup(func() { _ = database.Close() })
	tests := []struct {
		name       string
		database   *sql.DB
		queryLimit time.Duration
		want       error
	}{
		{name: "missing database", queryLimit: time.Second, want: ErrSeekDBConnectionEmpty},
		{name: "invalid query limit", database: database, want: ErrSeekDBQueryLimitInvalid},
		{name: "valid", database: database, queryLimit: time.Second},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, err := NewSeekDBStore(test.database, test.queryLimit)
			if !errors.Is(err, test.want) {
				t.Fatalf("NewSeekDBStore() error = %v, want %v", err, test.want)
			}
			if test.want == nil && (store == nil || !store.usesSeekDB() || store.usesPostgres()) {
				t.Fatalf("NewSeekDBStore() = %#v", store)
			}
		})
	}
}

type ledgerUnitConnector struct{}

func (ledgerUnitConnector) Connect(context.Context) (driver.Conn, error) {
	return nil, errors.New("unexpected observability ledger database connection")
}

func (ledgerUnitConnector) Driver() driver.Driver { return ledgerUnitDriver{} }

type ledgerUnitDriver struct{}

func (ledgerUnitDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("unexpected observability ledger database open")
}
