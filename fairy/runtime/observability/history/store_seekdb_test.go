package history

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"testing"
	"time"
)

func TestNewSeekDBStoreRejectsInvalidInputs(t *testing.T) {
	database := sql.OpenDB(historyUnitConnector{})
	t.Cleanup(func() { _ = database.Close() })
	tests := []struct {
		name       string
		database   *sql.DB
		queryLimit time.Duration
		want       error
	}{
		{name: "missing database", queryLimit: time.Second, want: ErrHistorySeekDBConnectionEmpty},
		{name: "invalid query limit", database: database, want: ErrHistorySeekDBQueryLimitInvalid},
		{name: "valid", database: database, queryLimit: time.Second},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, err := NewSeekDBStore(test.database, test.queryLimit)
			if !errors.Is(err, test.want) {
				t.Fatalf("NewSeekDBStore() error = %v, want %v", err, test.want)
			}
			if test.want != nil {
				return
			}
			t.Cleanup(store.Close)
			if store == nil || !store.usesSeekDB() || store.usesPostgres() {
				t.Fatalf("NewSeekDBStore() = %#v", store)
			}
		})
	}
}

type historyUnitConnector struct{}

func (historyUnitConnector) Connect(context.Context) (driver.Conn, error) {
	return nil, errors.New("unexpected observability history database connection")
}

func (historyUnitConnector) Driver() driver.Driver { return historyUnitDriver{} }

type historyUnitDriver struct{}

func (historyUnitDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("unexpected observability history database open")
}
